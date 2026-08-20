package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

// ─── Load ────────────────────────────────────────────────────────────────────

func TestLoad_valid(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: tg
    type: telegram
    bot_token: tok
workers:
  - id: w1
wirings:
  - channel_id: tg
    worker_id: w1
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClaudePath != "claude" {
		t.Errorf("default claude_path: got %q, want %q", cfg.ClaudePath, "claude")
	}
	if cfg.Wirings[0].EngageMode != "always" {
		t.Errorf("default engage_mode: got %q", cfg.Wirings[0].EngageMode)
	}
	if cfg.Wirings[0].SessionMode != "per-user" {
		t.Errorf("default session_mode: got %q", cfg.Wirings[0].SessionMode)
	}
	if cfg.Workers[0].SessionTimeoutMinutes != 60 {
		t.Errorf("default timeout: got %d", cfg.Workers[0].SessionTimeoutMinutes)
	}
	if cfg.TasksPath != "tasks.yaml" {
		t.Errorf("default tasks_path: got %q", cfg.TasksPath)
	}
}

func TestLoad_missing_file(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoad_bad_yaml(t *testing.T) {
	path := writeTemp(t, "not: valid: yaml: ][")
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for bad YAML")
	}
}

// ─── Validation ──────────────────────────────────────────────────────────────

func TestValidate_unknown_channel_type(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: slack
    bot_token: tok
workers:
  - id: w
wirings:
  - channel_id: c
    worker_id: w
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for unsupported channel type")
	}
}

func TestValidate_missing_bot_token(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: telegram
workers:
  - id: w
wirings:
  - channel_id: c
    worker_id: w
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing bot_token")
	}
}

func TestValidate_wiring_unknown_channel(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: telegram
    bot_token: tok
workers:
  - id: w
wirings:
  - channel_id: MISSING
    worker_id: w
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for wiring referencing unknown channel")
	}
}

func TestValidate_wiring_unknown_worker(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: telegram
    bot_token: tok
workers:
  - id: w
wirings:
  - channel_id: c
    worker_id: MISSING
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for wiring referencing unknown worker")
	}
}

func TestValidate_pattern_mode_without_pattern(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: telegram
    bot_token: tok
workers:
  - id: w
wirings:
  - channel_id: c
    worker_id: w
    engage_mode: pattern
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for pattern mode without engage_pattern")
	}
}

func TestValidate_unknown_session_mode(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: c
    type: telegram
    bot_token: tok
workers:
  - id: w
wirings:
  - channel_id: c
    worker_id: w
    session_mode: invalid
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for unknown session_mode")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func TestWorkerByID(t *testing.T) {
	cfg := &Config{Workers: []Worker{{ID: "a"}, {ID: "b"}}}
	if w := cfg.WorkerByID("b"); w == nil || w.ID != "b" {
		t.Error("WorkerByID failed")
	}
	if w := cfg.WorkerByID("z"); w != nil {
		t.Error("expected nil for unknown id")
	}
}

func TestWiringsForChannel(t *testing.T) {
	cfg := &Config{Wirings: []Wiring{
		{ChannelID: "ch1", WorkerID: "w1"},
		{ChannelID: "ch2", WorkerID: "w2"},
		{ChannelID: "ch1", WorkerID: "w3"},
	}}
	ws := cfg.WiringsForChannel("ch1")
	if len(ws) != 2 {
		t.Errorf("expected 2 wirings, got %d", len(ws))
	}
}

func TestWorkerSessionDir(t *testing.T) {
	w := &Worker{ID: "myworker"}
	got := WorkerSessionDir(w, "/base")
	want := filepath.Join("/base", "myworker")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	w2 := &Worker{ID: "myworker", WorkDir: "/custom/path"}
	if got2 := WorkerSessionDir(w2, "/base"); got2 != "/custom/path" {
		t.Errorf("custom WorkDir not respected: got %q", got2)
	}
}
