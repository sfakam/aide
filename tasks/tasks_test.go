package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "tasks.yaml")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLoad_valid(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: standup
    name: Morning Standup
    enabled: true
    worker_id: assistant
    prompt: "Give a brief standup update."
    schedule: "0 9 * * 1-5"
    outputs:
      - channel_id: my-telegram
        room_id: "123"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(cfg.Tasks))
	}
	task := cfg.Tasks[0]
	if task.ID != "standup" {
		t.Errorf("unexpected id %q", task.ID)
	}
	if task.TimeoutSecs != 120 {
		t.Errorf("expected default timeout 120, got %d", task.TimeoutSecs)
	}
	if task.SessionMode != "task-isolated" {
		t.Errorf("expected default session_mode task-isolated, got %q", task.SessionMode)
	}
}

func TestLoad_missing_file(t *testing.T) {
	cfg, err := Load("/nonexistent/tasks.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(cfg.Tasks) != 0 {
		t.Error("expected empty config for missing file")
	}
}

func TestLoad_bad_yaml(t *testing.T) {
	path := writeYAML(t, "not: valid: yaml: ][")
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for bad YAML")
	}
}

func TestLoad_missing_id(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - worker_id: assistant
    prompt: "hello"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing id")
	}
}

func TestLoad_duplicate_id(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    worker_id: assistant
    prompt: "a"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
  - id: foo
    worker_id: assistant
    prompt: "b"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for duplicate id")
	}
}

func TestLoad_missing_worker(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    prompt: "hello"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing worker_id")
	}
}

func TestLoad_missing_prompt(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    worker_id: assistant
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestLoad_missing_schedule(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    worker_id: assistant
    prompt: "hello"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing schedule")
	}
}

func TestLoad_no_outputs(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    worker_id: assistant
    prompt: "hello"
    schedule: "@daily"
    outputs: []
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for empty outputs")
	}
}

func TestLoad_invalid_session_mode(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: foo
    worker_id: assistant
    prompt: "hello"
    schedule: "@daily"
    session_mode: invalid
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid session_mode")
	}
}

func TestEnabledTasks(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: active
    enabled: true
    worker_id: w1
    prompt: "go"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
  - id: inactive
    enabled: false
    worker_id: w1
    prompt: "stop"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled := cfg.EnabledTasks()
	if len(enabled) != 1 || enabled[0].ID != "active" {
		t.Errorf("expected 1 enabled task 'active', got %v", enabled)
	}
}

func TestTaskByID(t *testing.T) {
	path := writeYAML(t, `
tasks:
  - id: t1
    worker_id: w1
    prompt: "a"
    schedule: "@daily"
    outputs:
      - channel_id: ch1
        room_id: r1
`)
	cfg, _ := Load(path)
	if cfg.TaskByID("t1") == nil {
		t.Error("expected to find task t1")
	}
	if cfg.TaskByID("missing") != nil {
		t.Error("expected nil for unknown id")
	}
}

func TestSessionKey(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"task-isolated", "w1:task:my-task"},
		{"worker-shared", "w1"},
		{"shared", "w1:tasks"},
		{"", "w1:task:my-task"}, // empty → default
	}
	for _, c := range cases {
		task := Task{ID: "my-task", WorkerID: "w1", SessionMode: c.mode}
		// Apply defaults so empty mode becomes task-isolated
		cfg := &Config{Tasks: []Task{task}}
		applyDefaults(cfg)
		got := cfg.Tasks[0].SessionKey()
		if got != c.want {
			t.Errorf("mode=%q: got %q, want %q", c.mode, got, c.want)
		}
	}
}
