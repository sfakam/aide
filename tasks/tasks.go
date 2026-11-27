// Package tasks loads and validates scheduled task definitions from a YAML file.
package tasks

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Script is a named shell command.
type Script struct {
	Name string `yaml:"name"`
	Cmd  string `yaml:"cmd"`
}

// Task is a scheduled job that calls Claude (type: ai), runs a one-shot shell
// command (type: script), or manages an aide-agent process (type: agent) —
// either on a cron schedule or as a long-lived supervised process.
type Task struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"` // "ai" (default) | "script" | "agent"
	Enabled     bool     `yaml:"enabled"`
	WorkerID    string   `yaml:"worker_id"`             // required for type: ai
	Prompt      string   `yaml:"prompt"`                // required for type: ai
	Schedule    string   `yaml:"schedule"`              // cron expression or @hourly/@daily/@weekly; unused for agent_mode: supervised
	TimeoutSecs int      `yaml:"timeout_secs"`          // default 120; unused for agent_mode: supervised (runs until it exits)
	SessionMode string   `yaml:"session_mode"`          // "task-isolated" (default) | "worker-shared" | "shared"
	Cmd         *Script  `yaml:"cmd,omitempty"`         // required for type: script and type: agent; the main command
	PreScript   *Script  `yaml:"pre_script,omitempty"`  // runs before main action (ai/script only)
	PostScript  *Script  `yaml:"post_script,omitempty"` // runs after main action (ai/script only)
	Outputs     []Output `yaml:"outputs"`               // optional for type: agent; required otherwise

	// The following apply only to type: agent.

	AgentMode             string `yaml:"agent_mode"`               // "scheduled" | "supervised" — required for type: agent
	AgentID               string `yaml:"agent_id"`                 // optional; the aide-agent id the process authenticates as for /heartbeat
	RestartBackoffSecs    int    `yaml:"restart_backoff_secs"`     // supervised only; initial restart delay, default 5
	MaxRestartBackoffSecs int    `yaml:"max_restart_backoff_secs"` // supervised only; cap for exponential backoff, default 60
	HeartbeatTimeoutSecs  int    `yaml:"heartbeat_timeout_secs"`   // supervised only; 0 = no heartbeat required
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
		if t.Type == "" {
			t.Type = "ai"
		}
		if t.TimeoutSecs <= 0 {
			t.TimeoutSecs = 120
		}
		if t.SessionMode == "" {
			t.SessionMode = "task-isolated"
		}
		if t.Type == "agent" && t.AgentMode == "supervised" {
			if t.RestartBackoffSecs <= 0 {
				t.RestartBackoffSecs = 5
			}
			if t.MaxRestartBackoffSecs <= 0 {
				t.MaxRestartBackoffSecs = 60
			}
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

		switch t.Type {
		case "ai":
			if t.WorkerID == "" {
				return fmt.Errorf("task %q: worker_id is required for type: ai", t.ID)
			}
			if t.Prompt == "" {
				return fmt.Errorf("task %q: prompt is required for type: ai", t.ID)
			}
			switch t.SessionMode {
			case "task-isolated", "worker-shared", "shared":
			default:
				return fmt.Errorf("task %q: invalid session_mode %q", t.ID, t.SessionMode)
			}
		case "script":
			if t.Cmd == nil {
				return fmt.Errorf("task %q: cmd is required for type: script", t.ID)
			}
			if t.Cmd.Name == "" {
				return fmt.Errorf("task %q: cmd.name is required", t.ID)
			}
			if t.Cmd.Cmd == "" {
				return fmt.Errorf("task %q: cmd.cmd is required", t.ID)
			}
		case "agent":
			if t.Cmd == nil {
				return fmt.Errorf("task %q: cmd is required for type: agent", t.ID)
			}
			if t.Cmd.Name == "" {
				return fmt.Errorf("task %q: cmd.name is required", t.ID)
			}
			if t.Cmd.Cmd == "" {
				return fmt.Errorf("task %q: cmd.cmd is required", t.ID)
			}
			switch t.AgentMode {
			case "scheduled":
				if t.Schedule == "" {
					return fmt.Errorf("task %q: schedule is required for agent_mode: scheduled", t.ID)
				}
			case "supervised":
				if t.Schedule != "" {
					return fmt.Errorf("task %q: schedule is not used for agent_mode: supervised (it runs continuously)", t.ID)
				}
				if t.HeartbeatTimeoutSecs > 0 && t.AgentID == "" {
					return fmt.Errorf("task %q: agent_id is required when heartbeat_timeout_secs is set", t.ID)
				}
			default:
				return fmt.Errorf("task %q: invalid agent_mode %q (want scheduled|supervised)", t.ID, t.AgentMode)
			}
		default:
			return fmt.Errorf("task %q: invalid type %q (want ai|script|agent)", t.ID, t.Type)
		}
		if t.Type != "agent" && t.Schedule == "" {
			return fmt.Errorf("task %q: schedule is required", t.ID)
		}
		if t.Type != "agent" && len(t.Outputs) == 0 {
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
		if t.PreScript != nil {
			if t.PreScript.Name == "" {
				return fmt.Errorf("task %q: pre_script.name is required", t.ID)
			}
			if t.PreScript.Cmd == "" {
				return fmt.Errorf("task %q: pre_script.cmd is required", t.ID)
			}
		}
		if t.PostScript != nil {
			if t.PostScript.Name == "" {
				return fmt.Errorf("task %q: post_script.name is required", t.ID)
			}
			if t.PostScript.Cmd == "" {
				return fmt.Errorf("task %q: post_script.cmd is required", t.ID)
			}
		}
	}
	return nil
}
