package store

import (
	"testing"
	"time"
)

func openMemory(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLoadCursor_missing(t *testing.T) {
	s := openMemory(t)
	cur, err := s.LoadCursor("no-such-channel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cur != "" {
		t.Errorf("expected empty cursor, got %q", cur)
	}
}

func TestSaveAndLoadCursor(t *testing.T) {
	s := openMemory(t)
	if err := s.SaveCursor("ch1", "abc123"); err != nil {
		t.Fatalf("save: %v", err)
	}
	cur, err := s.LoadCursor("ch1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cur != "abc123" {
		t.Errorf("got %q, want %q", cur, "abc123")
	}
}

func TestSaveCursor_upsert(t *testing.T) {
	s := openMemory(t)
	s.SaveCursor("ch1", "first")
	s.SaveCursor("ch1", "second")
	cur, _ := s.LoadCursor("ch1")
	if cur != "second" {
		t.Errorf("upsert failed: got %q", cur)
	}
}

func TestSaveCursor_independent_channels(t *testing.T) {
	s := openMemory(t)
	s.SaveCursor("tg", "100")
	s.SaveCursor("wx", "2024-01-01T00:00:00Z")

	tgCur, _ := s.LoadCursor("tg")
	wxCur, _ := s.LoadCursor("wx")

	if tgCur != "100" {
		t.Errorf("tg cursor: got %q", tgCur)
	}
	if wxCur != "2024-01-01T00:00:00Z" {
		t.Errorf("wx cursor: got %q", wxCur)
	}
}

// ─── Agent sessions ─────────────────────────────────────────────────────────

func TestGetAgentSession_missing(t *testing.T) {
	s := openMemory(t)
	as, err := s.GetAgentSession("agent1", "session1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if as != nil {
		t.Errorf("expected nil, got %+v", as)
	}
}

func TestSaveAndGetAgentSession(t *testing.T) {
	s := openMemory(t)
	now := time.Now().UTC().Truncate(time.Second)
	want := AgentSession{
		AgentID:         "agent1",
		SessionID:       "main",
		ClaudeSessionID: "claude-abc",
		TurnCount:       3,
		CreatedAt:       now,
		LastActiveAt:    now,
	}
	if err := s.SaveAgentSession(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetAgentSession("agent1", "main")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a session, got nil")
	}
	if got.ClaudeSessionID != want.ClaudeSessionID || got.TurnCount != want.TurnCount {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.ExpiresAt != nil {
		t.Errorf("expected forever (nil expiry), got %v", got.ExpiresAt)
	}
}

func TestSaveAgentSession_upsert(t *testing.T) {
	s := openMemory(t)
	now := time.Now().UTC()
	base := AgentSession{AgentID: "a", SessionID: "s", ClaudeSessionID: "first", CreatedAt: now, LastActiveAt: now}
	s.SaveAgentSession(base)
	base.ClaudeSessionID = "second"
	base.TurnCount = 5
	s.SaveAgentSession(base)

	got, err := s.GetAgentSession("a", "s")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClaudeSessionID != "second" || got.TurnCount != 5 {
		t.Errorf("upsert failed: got %+v", got)
	}
}

func TestGetAgentSession_expired(t *testing.T) {
	s := openMemory(t)
	past := time.Now().UTC().Add(-time.Hour)
	expired := past.Add(time.Minute) // already in the past
	as := AgentSession{
		AgentID: "a", SessionID: "s", ClaudeSessionID: "x",
		CreatedAt: past, LastActiveAt: past, ExpiresAt: &expired,
	}
	if err := s.SaveAgentSession(as); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetAgentSession("a", "s")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected expired session to read as nil, got %+v", got)
	}
}

func TestDeleteAgentSession(t *testing.T) {
	s := openMemory(t)
	now := time.Now().UTC()
	s.SaveAgentSession(AgentSession{AgentID: "a", SessionID: "s", ClaudeSessionID: "x", CreatedAt: now, LastActiveAt: now})
	if err := s.DeleteAgentSession("a", "s"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.GetAgentSession("a", "s")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestAgentSession_scopedByAgentID(t *testing.T) {
	s := openMemory(t)
	now := time.Now().UTC()
	s.SaveAgentSession(AgentSession{AgentID: "agent1", SessionID: "main", ClaudeSessionID: "x", CreatedAt: now, LastActiveAt: now})
	got, err := s.GetAgentSession("agent2", "main")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected session scoped to agent1 to be invisible to agent2, got %+v", got)
	}
}
