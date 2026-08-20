package router

import (
	"context"
	"regexp"
	"sync"
	"testing"

	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/config"
)

// ─── Stubs ────────────────────────────────────────────────────────────────────

type stubWorker struct {
	mu    sync.Mutex
	calls []workerCall
}

type workerCall struct {
	key, sender, text string
}

func (s *stubWorker) Send(_ context.Context, key, sender, text string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, workerCall{key, sender, text})
	s.mu.Unlock()
	return "reply", nil
}

type stubChannel struct {
	id   string
	mu   sync.Mutex
	sent []sentMsg
}

type sentMsg struct{ roomID, text string }

func (c *stubChannel) ID() string { return c.id }
func (c *stubChannel) Start(_ context.Context, _ chan<- channels.InboundMessage) error {
	return nil
}
func (c *stubChannel) Send(_ context.Context, roomID, text string) error {
	c.mu.Lock()
	c.sent = append(c.sent, sentMsg{roomID, text})
	c.mu.Unlock()
	return nil
}
func (c *stubChannel) LoadCursor(_ string) {}
func (c *stubChannel) SaveCursor() string  { return "" }

// ─── Helpers ──────────────────────────────────────────────────────────────────

func makeRouter(wirings []config.Wiring) (*Router, *stubWorker, *stubChannel) {
	cfg := &config.Config{
		Channels: []config.Channel{{ID: "ch1", Type: "telegram", BotToken: "tok"}},
		Workers:  []config.Worker{{ID: "w1", WorkDir: "/tmp/w1"}},
		Wirings:  wirings,
	}
	w := &stubWorker{}
	ch := &stubChannel{id: "ch1"}
	r := &Router{
		cfg:      cfg,
		manager:  w,
		channels: map[string]channels.Channel{"ch1": ch},
		patterns: make(map[string]*regexp.Regexp),
	}
	return r, w, ch
}

// ─── engage_mode tests ────────────────────────────────────────────────────────

func TestDispatch_always(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:   "ch1",
		WorkerID:    "w1",
		EngageMode:  "always",
		SessionMode: "per-user",
	}})
	msg := channels.InboundMessage{ChannelID: "ch1", RoomID: "r1", SenderID: "u1", SenderName: "Alice", Text: "hello"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 1 {
		t.Fatalf("expected 1 worker call, got %d", len(w.calls))
	}
}

func TestDispatch_mention_not_mentioned(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:   "ch1",
		WorkerID:    "w1",
		EngageMode:  "mention",
		SessionMode: "per-user",
	}})
	msg := channels.InboundMessage{ChannelID: "ch1", SenderID: "u1", IsMention: false, Text: "hello"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 0 {
		t.Error("expected no worker call when not mentioned")
	}
}

func TestDispatch_mention_mentioned(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:   "ch1",
		WorkerID:    "w1",
		EngageMode:  "mention",
		SessionMode: "per-user",
	}})
	msg := channels.InboundMessage{ChannelID: "ch1", RoomID: "r1", SenderID: "u1", SenderName: "Bob", IsMention: true, Text: "hello bot"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 1 {
		t.Fatalf("expected 1 worker call, got %d", len(w.calls))
	}
}

func TestDispatch_pattern_match(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:     "ch1",
		WorkerID:      "w1",
		EngageMode:    "pattern",
		EngagePattern: "^!bot",
		SessionMode:   "per-user",
	}})
	r.patterns["0"] = regexp.MustCompile("^!bot")

	msg := channels.InboundMessage{ChannelID: "ch1", RoomID: "r1", SenderID: "u1", SenderName: "Carol", Text: "!bot help"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 1 {
		t.Fatalf("expected 1 worker call, got %d", len(w.calls))
	}
}

func TestDispatch_pattern_no_match(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:     "ch1",
		WorkerID:      "w1",
		EngageMode:    "pattern",
		EngagePattern: "^!bot",
		SessionMode:   "per-user",
	}})
	r.patterns["0"] = regexp.MustCompile("^!bot")

	msg := channels.InboundMessage{ChannelID: "ch1", SenderID: "u1", Text: "just chatting"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 0 {
		t.Error("expected no call when pattern doesn't match")
	}
}

func TestDispatch_no_matching_channel(t *testing.T) {
	r, w, _ := makeRouter([]config.Wiring{{
		ChannelID:   "ch1",
		WorkerID:    "w1",
		EngageMode:  "always",
		SessionMode: "per-user",
	}})
	msg := channels.InboundMessage{ChannelID: "ch2", SenderID: "u1", Text: "hello"}
	r.Dispatch(context.Background(), msg)
	if len(w.calls) != 0 {
		t.Error("expected no call for unrelated channel")
	}
}

// ─── session key resolution ───────────────────────────────────────────────────

func TestResolveSessionKey_perUser(t *testing.T) {
	w := config.Wiring{WorkerID: "w1", ChannelID: "ch1", SessionMode: "per-user"}
	msg := channels.InboundMessage{ChannelID: "ch1", SenderID: "u42"}
	key := resolveSessionKey(w, msg)
	if key != "w1:ch1:u42" {
		t.Errorf("got %q", key)
	}
}

func TestResolveSessionKey_shared(t *testing.T) {
	w := config.Wiring{WorkerID: "w1", ChannelID: "ch1", SessionMode: "shared"}
	msg := channels.InboundMessage{ChannelID: "ch1", SenderID: "u42"}
	key := resolveSessionKey(w, msg)
	if key != "w1:ch1" {
		t.Errorf("got %q", key)
	}
}

func TestResolveSessionKey_workerShared(t *testing.T) {
	w := config.Wiring{WorkerID: "w1", ChannelID: "ch1", SessionMode: "worker-shared"}
	msg := channels.InboundMessage{ChannelID: "ch1", SenderID: "u42"}
	key := resolveSessionKey(w, msg)
	if key != "w1" {
		t.Errorf("got %q", key)
	}
}
