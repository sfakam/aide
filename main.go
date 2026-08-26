package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sfathall/aide/api"
	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/channels/telegram"
	"github.com/sfathall/aide/channels/webex"
	"github.com/sfathall/aide/config"
	"github.com/sfathall/aide/pty"
	"github.com/sfathall/aide/router"
	"github.com/sfathall/aide/store"
	"github.com/sfathall/aide/tasks"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath     := flag.String("db", "aide.db", "path to SQLite database")
	apiAddr    := flag.String("api", ":8080", "API listen address (empty to disable)")
	logLevel   := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	// Configure structured logger
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	cfg.ExpandPaths()

	db, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build channel adapters
	chs := make(map[string]channels.Channel, len(cfg.Channels))
	for _, cc := range cfg.Channels {
		ch := buildChannel(cc)
		if ch == nil {
			slog.Error("unknown channel type", "id", cc.ID, "type", cc.Type)
			os.Exit(1)
		}
		cursor, _ := db.LoadCursor(cc.ID)
		ch.LoadCursor(cursor)
		chs[cc.ID] = ch
	}

	// Build session manager (one per worker; sessions are pooled inside)
	managers := make(map[string]*pty.Manager, len(cfg.Workers))
	for i := range cfg.Workers {
		w := &cfg.Workers[i]
		mgr := pty.NewManager(cfg.ClaudePath, config.WorkerSessionDir(w, cfg.WorkDir), w.SessionTimeoutMinutes)
		mgr.StartReaper(ctx)
		managers[w.ID] = mgr
	}

	// Route all workers through a single fan-out manager shim
	fanout := &fanoutManager{managers: managers}

	// Build router
	rt := router.New(cfg, fanout, chs)

	// Load and schedule tasks
	taskCfg, err := tasks.Load(cfg.TasksPath)
	if err != nil {
		slog.Error("failed to load tasks", "err", err)
		os.Exit(1)
	}
	sched := newScheduler(fanout, chs)
	for _, t := range taskCfg.EnabledTasks() {
		if err := sched.add(t); err != nil {
			slog.Error("failed to schedule task", "task", t.ID, "schedule", t.Schedule, "err", err)
			os.Exit(1)
		}
		slog.Info("task scheduled", "task", t.ID, "name", t.Name, "schedule", t.Schedule)
	}
	sched.start()
	defer sched.stop()

	// Inbound message bus
	inbound := make(chan channels.InboundMessage, 64)

	// Start channel adapters
	for _, ch := range chs {
		go func(c channels.Channel) {
			if err := c.Start(ctx, inbound); err != nil && ctx.Err() == nil {
				slog.Error("channel exited with error", "channel", c.ID(), "err", err)
			}
		}(ch)
	}

	// Periodically persist cursors
	go saveCursors(ctx, chs, db)

	// Start API if configured
	if *apiAddr != "" {
		// Use first worker's manager for session listing; extend later for multi-worker
		var firstMgr *pty.Manager
		for _, m := range managers {
			firstMgr = m
			break
		}
		if firstMgr != nil {
			apiSrv := api.New(*apiAddr, firstMgr)
			go func() {
				if err := apiSrv.Start(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("API server stopped", "err", err)
				}
			}()
			slog.Info("API listening", "addr", *apiAddr)
		}
	}

	slog.Info("aide started", "version", version)
	for {
		select {
		case msg := <-inbound:
			go rt.Dispatch(ctx, msg)
			// Persist cursor immediately so a restart doesn't replay this message.
			if err := db.SaveCursor(msg.ChannelID, chs[msg.ChannelID].SaveCursor()); err != nil {
				slog.Warn("failed to save cursor after dispatch", "channel", msg.ChannelID, "err", err)
			}
		case <-ctx.Done():
			slog.Info("shutting down")
			saveCursorsOnce(chs, db)
			return
		}
	}
}

func buildChannel(cc config.Channel) channels.Channel {
	interval := time.Duration(cc.PollIntervalSecs) * time.Second
	switch cc.Type {
	case "telegram":
		return telegram.New(cc.ID, cc.BotToken, interval)
	case "webex":
		return webex.New(cc.ID, cc.BotToken, cc.RoomID, cc.Direct, interval)
	default:
		return nil
	}
}

func saveCursors(ctx context.Context, chs map[string]channels.Channel, db *store.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			saveCursorsOnce(chs, db)
		case <-ctx.Done():
			return
		}
	}
}

func saveCursorsOnce(chs map[string]channels.Channel, db *store.Store) {
	for id, ch := range chs {
		if err := db.SaveCursor(id, ch.SaveCursor()); err != nil {
			slog.Warn("failed to save cursor", "channel", id, "err", err)
		}
	}
}

// fanoutManager wraps multiple per-worker managers under the Worker interface
// expected by router.Router. It routes by the worker ID prefix of the session key.
type fanoutManager struct {
	managers map[string]*pty.Manager
}

func (f *fanoutManager) Send(ctx context.Context, key, sender, text string, statusFn func(string)) (string, error) {
	// Session key format: workerID, workerID:channelID, or workerID:channelID:senderID
	workerID := key
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			workerID = key[:i]
			break
		}
	}
	mgr, ok := f.managers[workerID]
	if !ok {
		return "", nil
	}
	return mgr.Send(ctx, key, sender, text, statusFn)
}
