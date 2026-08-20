// Package tasks loads and validates scheduled task definitions from a YAML file.
package tasks

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Task is a scheduled prompt that runs against a worker and posts results to channels.
type Task struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Enabled     bool     `yaml:"enabled"`
	WorkerID    string   `yaml:"worker_id"`
	Prompt      string   `yaml:"prompt"`
	Schedule    string   `yaml:"schedule"`    // cron expression or @hourly/@daily/@weekly
	TimeoutSecs int      `yaml:"timeout_secs"` // default 120
	SessionMode string   `yaml:"session_mode"` // "task-isolated" (default) | "worker-shared" | "shared"
	Outputs     []Output `yaml:"outputs"`
}

// Output defines a channel+room pair where task results are delivered.
type Output struct {
	ChannelID string `yaml:"channel_id"`
	RoomID    string `yaml:"room_id"`
}

// Config is the top-level structure of a tasks YAML file.
type Config struct {
	Tasks []Task `yaml:"tasks"`
}

// Load reads a YAML tasks file and validates it.
// Returns an empty Config (no error) if path does not exist, so tasks are optional.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// EnabledTasks returns only tasks where Enabled is true.
func (c *Config) EnabledTasks() []Task {
	out := make([]Task, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}

// TaskByID returns the task with the given id, or nil.
func (c *Config) TaskByID(id string) *Task {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return &c.Tasks[i]
		}
	}
	return nil
}

// SessionKey returns the PTY session key to use for this task.
// "task-isolated" (default) gives each task its own dedicated session so it
// never contaminates user conversations.
func (t *Task) SessionKey() string {
	switch t.SessionMode {
	case "worker-shared":
		return t.WorkerID
	case "shared":
		return fmt.Sprintf("%s:tasks", t.WorkerID)
	default: // "task-isolated"
		return fmt.Sprintf("%s:task:%s", t.WorkerID, t.ID)
	}
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func applyDefaults(cfg *Config) {
	for i := range cfg.Tasks {
		t := &cfg.Tasks[i]
		if t.TimeoutSecs <= 0 {
			t.TimeoutSecs = 120
		}
		if t.SessionMode == "" {
			t.SessionMode = "task-isolated"
		}
	}
}

func validate(cfg *Config) error {
	seen := make(map[string]bool)
	for i, t := range cfg.Tasks {
		if t.ID == "" {
			return fmt.Errorf("task[%d]: id is required", i)
		}
		if seen[t.ID] {
			return fmt.Errorf("task %q: duplicate id", t.ID)
		}
		seen[t.ID] = true

		if t.WorkerID == "" {
			return fmt.Errorf("task %q: worker_id is required", t.ID)
		}
		if t.Prompt == "" {
			return fmt.Errorf("task %q: prompt is required", t.ID)
		}
		if t.Schedule == "" {
			return fmt.Errorf("task %q: schedule is required", t.ID)
		}
		if len(t.Outputs) == 0 {
			return fmt.Errorf("task %q: at least one output is required", t.ID)
		}
		for j, o := range t.Outputs {
			if o.ChannelID == "" {
				return fmt.Errorf("task %q output[%d]: channel_id is required", t.ID, j)
			}
			if o.RoomID == "" {
				return fmt.Errorf("task %q output[%d]: room_id is required", t.ID, j)
			}
		}
		switch t.SessionMode {
		case "task-isolated", "worker-shared", "shared":
		default:
			return fmt.Errorf("task %q: invalid session_mode %q", t.ID, t.SessionMode)
		}
	}
	return nil
}
