package store

import "testing"

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
