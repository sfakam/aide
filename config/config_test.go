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

// ─── Agents ──────────────────────────────────────────────────────────────────

func TestLoad_agent_defaults(t *testing.T) {
	path := writeTemp(t, `
agents:
  - id: a1
    token: secret
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a := cfg.Agents[0]
	if a.RateLimit.MaxRequests != 20 || a.RateLimit.WindowMinutes != 60 {
		t.Errorf("unexpected rate_limit defaults: %+v", a.RateLimit)
	}
	if a.MaxTurnsPerSession != 50 {
		t.Errorf("unexpected max_turns_per_session default: %d", a.MaxTurnsPerSession)
	}
	if a.MaxSessionDurationMinutes != 480 {
		t.Errorf("unexpected max_session_duration_minutes default: %d", a.MaxSessionDurationMinutes)
	}
	if a.IdleTimeoutMinutes != 1440 {
		t.Errorf("unexpected idle_timeout_minutes default: %d", a.IdleTimeoutMinutes)
	}
	if cfg.AgentsSocketPath == "" {
		t.Error("expected a default agents_socket_path")
	}
}

func TestValidate_agent_missing_id(t *testing.T) {
	path := writeTemp(t, `
agents:
  - token: secret
`)
	if _, err := Load(path); err == nil {
		t.Error("expected error for agent missing id")
	}
}

func TestValidate_agent_missing_token(t *testing.T) {
	path := writeTemp(t, `
agents:
  - id: a1
`)
	if _, err := Load(path); err == nil {
		t.Error("expected error for agent missing token")
	}
}

func TestValidate_agent_duplicate_id(t *testing.T) {
	path := writeTemp(t, `
agents:
  - id: a1
    token: tok1
  - id: a1
    token: tok2
`)
	if _, err := Load(path); err == nil {
		t.Error("expected error for duplicate agent id")
	}
}

func TestValidate_agent_default_model_not_allowed(t *testing.T) {
	path := writeTemp(t, `
agents:
  - id: a1
    token: secret
    models_allowed: ["claude-haiku-4-5"]
    default_model: "claude-sonnet-5"
`)
	if _, err := Load(path); err == nil {
		t.Error("expected error for default_model not in models_allowed")
	}
}

func TestValidate_agent_channel_rule_unknown_channel(t *testing.T) {
	path := writeTemp(t, `
agents:
  - id: a1
    token: secret
    channels_allowed:
      - channel_id: MISSING
`)
	if _, err := Load(path); err == nil {
		t.Error("expected error for channels_allowed referencing unknown channel")
	}
}

func TestValidate_agent_channel_rule_known_channel(t *testing.T) {
	path := writeTemp(t, `
channels:
  - id: my-webex
    type: webex
    bot_token: tok
agents:
  - id: a1
    token: secret
    channels_allowed:
      - channel_id: my-webex
        room_ids: ["room1"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a := cfg.AgentByID("a1")
	if a == nil {
		t.Fatal("AgentByID returned nil")
	}
	if !a.AllowsChannel("my-webex", "room1") {
		t.Error("expected room1 to be allowed")
	}
	if a.AllowsChannel("my-webex", "room2") {
		t.Error("expected room2 to be disallowed")
	}
	if a.AllowsChannel("other-channel", "room1") {
		t.Error("expected other-channel to be disallowed")
	}
}

func TestAgent_AllowsModel_emptyAllowlist(t *testing.T) {
	a := Agent{}
	if !a.AllowsModel("anything") {
		t.Error("empty allowlist should permit any model")
	}
}

func TestAgent_AllowsEffort_restricted(t *testing.T) {
	a := Agent{EffortAllowed: []string{"low", "medium"}}
	if !a.AllowsEffort("low") {
		t.Error("expected low to be allowed")
	}
	if a.AllowsEffort("high") {
		t.Error("expected high to be disallowed")
	}
}
