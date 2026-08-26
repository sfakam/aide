package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/tasks"
)

type taskSender interface {
	Send(ctx context.Context, key, sender, text string) (string, error)
}

type scheduler struct {
	c       *cron.Cron
	manager taskSender
	chs     map[string]channels.Channel
	log     *slog.Logger
}

func newScheduler(manager taskSender, chs map[string]channels.Channel) *scheduler {
	return &scheduler{
		c:       cron.New(),
		manager: manager,
		chs:     chs,
		log:     slog.With("component", "scheduler"),
	}
}

func (s *scheduler) add(t tasks.Task) error {
	_, err := s.c.AddFunc(t.Schedule, func() { s.run(t) })
	return err
}

func (s *scheduler) start() { s.c.Start() }
func (s *scheduler) stop()  { s.c.Stop() }

func (s *scheduler) run(t tasks.Task) {
	timeout := time.Duration(t.TimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s.log.Info("task starting", "task", t.ID, "name", t.Name, "timeout", timeout)
	s.sendToOutputs(ctx, t, "⏳ Running task: **"+t.Name+"**…")
	start := time.Now()
	resp, err := s.manager.Send(ctx, t.SessionKey(), "scheduler", t.Prompt)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			s.log.Error("task timed out", "task", t.ID, "timeout", timeout, "elapsed", elapsed)
		} else {
			s.log.Error("task failed", "task", t.ID, "elapsed", elapsed, "err", err)
		}
		return
	}
	s.log.Info("task complete", "task", t.ID, "elapsed", elapsed, "resp_len", len(resp))

	s.sendToOutputs(ctx, t, resp)
}

func (s *scheduler) sendToOutputs(ctx context.Context, t tasks.Task, text string) {
	for _, out := range t.Outputs {
		ch, ok := s.chs[out.ChannelID]
		if !ok {
			s.log.Error("task output channel not found", "task", t.ID, "channel", out.ChannelID)
			continue
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 15*time.Second)
		err := ch.Send(sendCtx, out.RoomID, text)
		sendCancel()
		if err != nil {
			s.log.Error("task output send failed", "task", t.ID, "channel", out.ChannelID, "err", err)
		} else {
			s.log.Info("task output sent", "task", t.ID, "channel", out.ChannelID)
		}
	}
}
