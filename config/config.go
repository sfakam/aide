// Package config loads and validates aide.yaml configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure.
type Config struct {
	ClaudePath string    `yaml:"claude_path"`
	WorkDir    string    `yaml:"work_dir"`
	DBPath     string    `yaml:"db_path"`
	TasksPath  string    `yaml:"tasks_path"`
	Channels   []Channel `yaml:"channels"`
	Workers    []Worker  `yaml:"workers"`
	Wirings    []Wiring  `yaml:"wirings"`
}

// Channel is a messaging platform connection.
type Channel struct {
	ID               string  `yaml:"id"`
	Type             string  `yaml:"type"`              // "telegram" | "webex"
	BotToken         string  `yaml:"bot_token"`
	PollIntervalSecs float64 `yaml:"poll_interval_secs"`
	RoomID           string  `yaml:"room_id"` // webex: restrict to one room
}

// Worker is a named Claude instance.
type Worker struct {
	ID                    string `yaml:"id"`
	WorkDir               string `yaml:"work_dir"` // optional; defaults to <base>/<id>
	SessionTimeoutMinutes int    `yaml:"session_timeout_minutes"`
}

// Wiring links a channel to a worker with routing and session rules.
type Wiring struct {
	ChannelID     string `yaml:"channel_id"`
	WorkerID      string `yaml:"worker_id"`
	EngageMode    string `yaml:"engage_mode"`    // "always" | "mention" | "pattern"
	EngagePattern string `yaml:"engage_pattern"` // regex; required when mode=pattern
	SessionMode   string `yaml:"session_mode"`   // "per-user" | "shared" | "worker-shared"
}

// Load reads a YAML config file, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ExpandPaths expands ~ in WorkDir, DBPath, and TasksPath.
func (c *Config) ExpandPaths() {
	c.WorkDir = expandHome(c.WorkDir)
	c.DBPath = expandHome(c.DBPath)
	c.TasksPath = expandHome(c.TasksPath)
	for i := range c.Workers {
		if c.Workers[i].WorkDir != "" {
			c.Workers[i].WorkDir = expandHome(c.Workers[i].WorkDir)
		}
	}
}

// WorkerByID returns the Worker with the given id, or nil.
func (c *Config) WorkerByID(id string) *Worker {
	for i := range c.Workers {
		if c.Workers[i].ID == id {
			return &c.Workers[i]
		}
	}
	return nil
}

// ChannelByID returns the Channel with the given id, or nil.
func (c *Config) ChannelByID(id string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].ID == id {
			return &c.Channels[i]
		}
	}
	return nil
}

// WiringsForChannel returns all wirings whose ChannelID matches.
func (c *Config) WiringsForChannel(channelID string) []Wiring {
	var out []Wiring
	for _, w := range c.Wirings {
		if w.ChannelID == channelID {
			out = append(out, w)
		}
	}
	return out
}

// WorkerSessionDir returns the effective work directory for a worker session.
// If the worker has its own WorkDir, that is returned directly.
// Otherwise it is baseWorkDir/<worker.id>.
func WorkerSessionDir(w *Worker, baseWorkDir string) string {
	if w.WorkDir != "" {
		return w.WorkDir
	}
	return filepath.Join(baseWorkDir, w.ID)
}

// ─── defaults ────────────────────────────────────────────────────────────────

func (c *Config) applyDefaults() {
	if c.ClaudePath == "" {
		c.ClaudePath = "claude"
	}
	if c.WorkDir == "" {
		c.WorkDir = "~/aide-workspace"
	}
	if c.DBPath == "" {
		c.DBPath = "aide.db"
	}
	if c.TasksPath == "" {
		c.TasksPath = "tasks.yaml"
	}
	for i := range c.Channels {
		if c.Channels[i].PollIntervalSecs == 0 {
			c.Channels[i].PollIntervalSecs = 5
		}
	}
	for i := range c.Workers {
		if c.Workers[i].SessionTimeoutMinutes == 0 {
			c.Workers[i].SessionTimeoutMinutes = 60
		}
	}
	for i := range c.Wirings {
		if c.Wirings[i].EngageMode == "" {
			c.Wirings[i].EngageMode = "always"
		}
		if c.Wirings[i].SessionMode == "" {
			c.Wirings[i].SessionMode = "per-user"
		}
	}
}

// ─── validation ──────────────────────────────────────────────────────────────

func (c *Config) validate() error {
	channelIDs := make(map[string]bool, len(c.Channels))
	for _, ch := range c.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channel entry missing id")
		}
		if ch.Type != "telegram" && ch.Type != "webex" {
			return fmt.Errorf("channel %q: unsupported type %q (want telegram|webex)", ch.ID, ch.Type)
		}
		if ch.BotToken == "" {
			return fmt.Errorf("channel %q: missing bot_token", ch.ID)
		}
		channelIDs[ch.ID] = true
	}

	workerIDs := make(map[string]bool, len(c.Workers))
	for _, w := range c.Workers {
		if w.ID == "" {
			return fmt.Errorf("worker entry missing id")
		}
		workerIDs[w.ID] = true
	}

	for _, w := range c.Wirings {
		if !channelIDs[w.ChannelID] {
			return fmt.Errorf("wiring references unknown channel %q", w.ChannelID)
		}
		if !workerIDs[w.WorkerID] {
			return fmt.Errorf("wiring references unknown worker %q", w.WorkerID)
		}
		switch w.EngageMode {
		case "always", "mention", "pattern":
		default:
			return fmt.Errorf("wiring %q→%q: unknown engage_mode %q", w.ChannelID, w.WorkerID, w.EngageMode)
		}
		if w.EngageMode == "pattern" && w.EngagePattern == "" {
			return fmt.Errorf("wiring %q→%q: engage_mode=pattern requires engage_pattern", w.ChannelID, w.WorkerID)
		}
		switch w.SessionMode {
		case "per-user", "shared", "worker-shared":
		default:
			return fmt.Errorf("wiring %q→%q: unknown session_mode %q", w.ChannelID, w.WorkerID, w.SessionMode)
		}
	}
	return nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
