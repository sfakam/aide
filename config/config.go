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
	Agents     []Agent   `yaml:"agents"`

	// AgentsSocketPath is the unix socket the agent HTTP API listens on.
	AgentsSocketPath string `yaml:"agents_socket_path"`
}

// Channel is a messaging platform connection.
type Channel struct {
	ID               string  `yaml:"id"`
	Type             string  `yaml:"type"` // "telegram" | "webex"
	BotToken         string  `yaml:"bot_token"`
	PollIntervalSecs float64 `yaml:"poll_interval_secs"`
	RoomID           string  `yaml:"room_id"`     // webex: restrict to one room (auto-discovered if direct_with is set)
	Direct           bool    `yaml:"direct"`      // webex: 1:1 direct room — no @mention required
	DirectWith       string  `yaml:"direct_with"` // webex: person email to open/find a 1:1 with; sets direct=true implicitly
}

// Worker is a named Claude instance.
type Worker struct {
	ID                    string `yaml:"id"`
	WorkDir               string `yaml:"work_dir"` // optional; defaults to <base>/<id>
	SessionTimeoutMinutes int    `yaml:"session_timeout_minutes"`
}

// Agent is a registered aide-agent: an external, custom-built process that calls
// aide's unix-socket HTTP API for Claude inference and (optionally) channel relay.
// See aide-agents/docs/design.md for the full protocol.
type Agent struct {
	ID    string `yaml:"id"`
	Token string `yaml:"token"` // bearer token the agent must present on every request

	ModelsAllowed []string `yaml:"models_allowed"` // empty = any model permitted
	DefaultModel  string   `yaml:"default_model"`
	EffortAllowed []string `yaml:"effort_allowed"` // empty = any effort level permitted
	DefaultEffort string   `yaml:"default_effort"`

	RateLimit AgentRateLimit `yaml:"rate_limit"`

	// MaxTurnsPerSession, MaxSessionDurationMinutes, and IdleTimeoutMinutes drive
	// compaction: when a resumed session exceeds any of these, aide rotates it
	// (starts a fresh claude_session_id) before replaying the new prompt.
	MaxTurnsPerSession        int `yaml:"max_turns_per_session"`
	MaxSessionDurationMinutes int `yaml:"max_session_duration_minutes"`
	IdleTimeoutMinutes        int `yaml:"idle_timeout_minutes"`

	// DefaultSessionTTLMinutes is used when a /infer call omits session_ttl_minutes.
	// nil = no agent-level default, so an omitted call-level TTL means "forever".
	DefaultSessionTTLMinutes *int `yaml:"default_session_ttl_minutes"`

	// ChannelsAllowed is only consulted for /messages calls. An agent that never
	// calls /messages can omit it entirely.
	ChannelsAllowed []AgentChannelRule `yaml:"channels_allowed"`
}

// AgentRateLimit caps how often an agent may call /infer or /messages.
type AgentRateLimit struct {
	MaxRequests   int `yaml:"max_requests"`
	WindowMinutes int `yaml:"window_minutes"`
}

// AgentChannelRule permits an agent to post to a channel, optionally restricted
// to specific rooms. An empty RoomIDs means any room on that channel is allowed.
type AgentChannelRule struct {
	ChannelID string   `yaml:"channel_id"`
	RoomIDs   []string `yaml:"room_ids"`
}

// Allows reports whether roomID is permitted for this rule.
func (r AgentChannelRule) Allows(roomID string) bool {
	if len(r.RoomIDs) == 0 {
		return true
	}
	for _, id := range r.RoomIDs {
		if id == roomID {
			return true
		}
	}
	return false
}

// AllowsModel reports whether model is permitted for this agent (empty allowlist = any).
func (a Agent) AllowsModel(model string) bool {
	if len(a.ModelsAllowed) == 0 {
		return true
	}
	return contains(a.ModelsAllowed, model)
}

// AllowsEffort reports whether effort is permitted for this agent (empty allowlist = any).
func (a Agent) AllowsEffort(effort string) bool {
	if len(a.EffortAllowed) == 0 {
		return true
	}
	return contains(a.EffortAllowed, effort)
}

// AllowsChannel reports whether (channelID, roomID) is permitted for /messages.
func (a Agent) AllowsChannel(channelID, roomID string) bool {
	for _, r := range a.ChannelsAllowed {
		if r.ChannelID == channelID && r.Allows(roomID) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Wiring links a channel to a worker with routing and session rules.
type Wiring struct {
	ChannelID       string   `yaml:"channel_id"`
	WorkerID        string   `yaml:"worker_id"`
	EngageMode      string   `yaml:"engage_mode"`       // "always" | "mention" | "pattern"
	EngagePattern   string   `yaml:"engage_pattern"`    // regex; required when mode=pattern
	SessionMode     string   `yaml:"session_mode"`      // "per-user" | "shared" | "worker-shared"
	AllowedUsers    []string `yaml:"allowed_users"`     // email allowlist; empty = allow all
	StatusBatchSecs int      `yaml:"status_batch_secs"` // group tool-call updates over N seconds (0 = send immediately)
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
	c.AgentsSocketPath = expandHome(c.AgentsSocketPath)
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

// AgentByID returns the Agent with the given id, or nil.
func (c *Config) AgentByID(id string) *Agent {
	for i := range c.Agents {
		if c.Agents[i].ID == id {
			return &c.Agents[i]
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
	if c.AgentsSocketPath == "" {
		c.AgentsSocketPath = "~/.aide/agents.sock"
	}
	for i := range c.Agents {
		a := &c.Agents[i]
		if a.RateLimit.MaxRequests == 0 {
			a.RateLimit.MaxRequests = 20
		}
		if a.RateLimit.WindowMinutes == 0 {
			a.RateLimit.WindowMinutes = 60
		}
		if a.MaxTurnsPerSession == 0 {
			a.MaxTurnsPerSession = 50
		}
		if a.MaxSessionDurationMinutes == 0 {
			a.MaxSessionDurationMinutes = 480
		}
		if a.IdleTimeoutMinutes == 0 {
			a.IdleTimeoutMinutes = 1440
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

	agentIDs := make(map[string]bool, len(c.Agents))
	for _, a := range c.Agents {
		if a.ID == "" {
			return fmt.Errorf("agent entry missing id")
		}
		if agentIDs[a.ID] {
			return fmt.Errorf("agent %q: duplicate id", a.ID)
		}
		agentIDs[a.ID] = true
		if a.Token == "" {
			return fmt.Errorf("agent %q: missing token", a.ID)
		}
		if a.DefaultModel != "" && !a.AllowsModel(a.DefaultModel) {
			return fmt.Errorf("agent %q: default_model %q not in models_allowed", a.ID, a.DefaultModel)
		}
		if a.DefaultEffort != "" && !a.AllowsEffort(a.DefaultEffort) {
			return fmt.Errorf("agent %q: default_effort %q not in effort_allowed", a.ID, a.DefaultEffort)
		}
		for _, r := range a.ChannelsAllowed {
			if !channelIDs[r.ChannelID] {
				return fmt.Errorf("agent %q: channels_allowed references unknown channel %q", a.ID, r.ChannelID)
			}
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
