// Package router evaluates wiring rules and fans inbound messages out to workers.
package router

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/config"
)

// Worker is a sender interface so tests can inject a stub.
type Worker interface {
	Send(ctx context.Context, key, sender, text string) (string, error)
}

// Router evaluates wiring configuration and routes inbound messages to workers.
type Router struct {
	cfg      *config.Config
	manager  Worker
	channels map[string]channels.Channel // channelID → Channel (for sending replies)
	patterns map[string]*regexp.Regexp   // wiring index → compiled regex
}

// New creates a Router.
// chs is the map of running channel adapters (used to send replies).
func New(cfg *config.Config, manager Worker, chs map[string]channels.Channel) *Router {
	r := &Router{
		cfg:      cfg,
		manager:  manager,
		channels: chs,
		patterns: make(map[string]*regexp.Regexp),
	}
	// Pre-compile patterns
	for i, w := range cfg.Wirings {
		if w.EngageMode == "pattern" && w.EngagePattern != "" {
			key := fmt.Sprintf("%d", i)
			re, err := regexp.Compile(w.EngagePattern)
			if err != nil {
				slog.Warn("invalid engage_pattern", "wiring", i, "err", err)
				continue
			}
			r.patterns[key] = re
		}
	}
	return r
}

// Dispatch evaluates all wirings for msg's channel and routes to matching workers in parallel.
func (r *Router) Dispatch(ctx context.Context, msg channels.InboundMessage) {
	wirings := r.cfg.WiringsForChannel(msg.ChannelID)
	if len(wirings) == 0 {
		slog.Debug("no wirings for channel", "channel", msg.ChannelID)
		return
	}

	slog.Debug("dispatch",
		"channel", msg.ChannelID,
		"room", msg.RoomID,
		"sender", msg.SenderName,
		"mention", msg.IsMention,
		"text", truncate(msg.Text, 80),
		"wirings", len(wirings),
	)

	var wg sync.WaitGroup
	for i, w := range wirings {
		if !r.engaged(i, w, msg) {
			slog.Debug("wiring skipped",
				"worker", w.WorkerID,
				"engage_mode", w.EngageMode,
			)
			continue
		}
		sessionKey := resolveSessionKey(w, msg)
		slog.Debug("wiring engaged",
			"worker", w.WorkerID,
			"session_key", sessionKey,
			"engage_mode", w.EngageMode,
			"session_mode", w.SessionMode,
		)
		wg.Add(1)
		go func(wiring config.Wiring, key string) {
			defer wg.Done()

			ch, ok := r.channels[msg.ChannelID]
			if !ok {
				slog.Error("channel not found for reply", "channel", msg.ChannelID)
				return
			}

			// If the message is a slash command, acknowledge immediately so the
			// user knows it was received while the skill runs (can take minutes).
			if cmd := slashCmdName(msg.Text); cmd != "" {
				ack := "⏳ Running " + cmd + "..."
				if err := ch.Send(ctx, msg.RoomID, ack); err != nil {
					slog.Warn("ack send failed", "err", err)
				}
			}

			// If the channel supports typing indicators, pulse every 8s while
			// the worker is processing (Webex shows "typing" for ~10s per pulse).
			var stopTyping context.CancelFunc
			if tc, ok := ch.(channels.TypingChannel); ok {
				typingCtx, cancel := context.WithCancel(ctx)
				stopTyping = cancel
				go keepTyping(typingCtx, tc, msg.RoomID)
			}

			slog.Debug("sending to worker", "worker", wiring.WorkerID, "key", key)
			resp, err := r.manager.Send(ctx, key, msg.SenderName, msg.Text)

			if stopTyping != nil {
				stopTyping()
			}

			if err != nil {
				slog.Error("worker send failed", "worker", wiring.WorkerID, "key", key, "err", err)
				return
			}
			slog.Debug("worker replied", "worker", wiring.WorkerID, "key", key, "len", len(resp))
			if err := ch.Send(ctx, msg.RoomID, resp); err != nil {
				slog.Error("channel send failed", "channel", msg.ChannelID, "err", err)
			} else {
				slog.Info("reply sent", "channel", msg.ChannelID, "worker", wiring.WorkerID, "sender", msg.SenderName)
			}
		}(w, sessionKey)
	}
	wg.Wait()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// slashCmdName returns the slash command token from msg (e.g. "/sdlc:full-pr-review"),
// stripping any leading bot-mention word. Returns "" if no slash command is found.
func slashCmdName(msg string) string {
	for _, tok := range strings.Fields(msg) {
		if strings.HasPrefix(tok, "/") && !strings.Contains(tok, "://") {
			return tok
		}
	}
	return ""
}

// keepTyping sends a typing-indicator pulse every 8 seconds until ctx is cancelled.
func keepTyping(ctx context.Context, tc channels.TypingChannel, roomID string) {
	// Send an immediate pulse so the indicator appears without waiting for the first tick.
	if err := tc.SendTyping(ctx, roomID); err != nil {
		slog.Debug("typing indicator failed", "err", err)
	}
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := tc.SendTyping(ctx, roomID); err != nil {
				slog.Debug("typing indicator failed", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// ─── Internal ─────────────────────────────────────────────────────────────────

// engaged returns true if the wiring should handle this message.
func (r *Router) engaged(wiringIndex int, w config.Wiring, msg channels.InboundMessage) bool {
	if len(w.AllowedUsers) > 0 {
		allowed := false
		for _, u := range w.AllowedUsers {
			if strings.EqualFold(u, msg.SenderName) {
				allowed = true
				break
			}
		}
		if !allowed {
			slog.Debug("sender not in allowed_users", "sender", msg.SenderName)
			return false
		}
	}
	switch w.EngageMode {
	case "always":
		return true
	case "mention":
		return msg.IsMention
	case "pattern":
		key := fmt.Sprintf("%d", wiringIndex)
		re, ok := r.patterns[key]
		if !ok {
			return false
		}
		return re.MatchString(msg.Text)
	default:
		return false
	}
}

// resolveSessionKey builds the session key based on session_mode.
func resolveSessionKey(w config.Wiring, msg channels.InboundMessage) string {
	switch w.SessionMode {
	case "worker-shared":
		return w.WorkerID
	case "shared":
		return fmt.Sprintf("%s:%s", w.WorkerID, msg.ChannelID)
	case "per-user":
		return fmt.Sprintf("%s:%s:%s", w.WorkerID, msg.ChannelID, msg.SenderID)
	default:
		return fmt.Sprintf("%s:%s:%s", w.WorkerID, msg.ChannelID, msg.SenderID)
	}
}
