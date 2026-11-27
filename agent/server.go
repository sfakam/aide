// Package agent implements the aide-agents HTTP API: a unix-socket-only
// surface that lets external, custom-built processes trigger resumable
// Claude inference and optionally relay messages through aide's channels.
// See the aide-agents repo's docs/design.md for the full protocol.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/config"
	"github.com/sfathall/aide/store"
)

const (
	defaultInferTimeout = 5 * time.Minute
	maxInferTimeout     = 2 * time.Hour // hard cap regardless of what a caller requests
)

// Server serves the aide-agents HTTP API over a unix domain socket.
type Server struct {
	mu  sync.RWMutex
	cfg *config.Config // protected by mu; swapped wholesale on Reload

	db      *store.Store
	invoker *Invoker
	limiter *rateLimiter
	chs     map[string]channels.Channel
	hb      *HeartbeatTracker

	socketPath string
	srv        *http.Server
}

// New creates a Server bound to socketPath (created on Start).
// chs is the map of running channel adapters, used by the optional
// /messages endpoint. hb records heartbeats so the task supervisor (see the
// main package's scheduler) can detect a stuck "agent_mode: supervised"
// process; pass a freshly created tracker even if no supervised tasks exist.
func New(socketPath string, cfg *config.Config, db *store.Store, claudePath string, chs map[string]channels.Channel, hb *HeartbeatTracker) *Server {
	s := &Server{
		cfg:        cfg,
		db:         db,
		invoker:    &Invoker{ClaudePath: claudePath},
		limiter:    newRateLimiter(),
		chs:        chs,
		hb:         hb,
		socketPath: socketPath,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/infer", s.handleInfer)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/messages", s.handleMessages)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/heartbeat", s.handleHeartbeat)
	s.srv = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: maxInferTimeout + 30*time.Second,
	}
	return s
}

// Reload swaps in a new config without restarting. Agents have no persistent
// process tied to them (see docs/design.md §5), so the whole agent registry
// — tokens, allowlists, rate limits — can be hot-reloaded safely, unlike
// workers/channels.
func (s *Server) Reload(cfg *config.Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	slog.Info("agent config hot-reloaded", "agents", len(cfg.Agents))
}

// Start begins serving on the unix socket. Blocks until ctx is cancelled or
// the server errors.
func (s *Server) Start(ctx context.Context) error {
	if err := os.RemoveAll(s.socketPath); err != nil {
		return fmt.Errorf("remove stale socket %q: %w", s.socketPath, err)
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return fmt.Errorf("chmod %q: %w", s.socketPath, err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.srv.Shutdown(shutCtx)
		os.RemoveAll(s.socketPath)
		return err
	case err := <-errCh:
		return err
	}
}

func (s *Server) agentByID(id string) *config.Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.AgentByID(id)
}

// authenticate resolves the agent for this request and checks its bearer
// token. On failure it writes the response and returns nil.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) *config.Agent {
	agentID := r.PathValue("agent_id")
	a := s.agentByID(agentID)
	if a == nil {
		writeError(w, http.StatusNotFound, "unknown agent_id")
		return nil
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token != a.Token {
		writeError(w, http.StatusUnauthorized, "invalid or missing token")
		return nil
	}
	return a
}

// ─── /infer ─────────────────────────────────────────────────────────────────

type inferRequest struct {
	Prompt            string `json:"prompt"`
	SessionID         string `json:"session_id"`
	SessionTTLMinutes *int   `json:"session_ttl_minutes"`
	Model             string `json:"model"`
	Effort            string `json:"effort"`

	// AppendSystemPrompt and MaxBudgetUSD only narrow what the call can do
	// (more instructions, a spend cap), never widen it, so — unlike
	// model/effort — they aren't checked against an agent allowlist.
	AppendSystemPrompt string   `json:"append_system_prompt"`
	MaxBudgetUSD       float64  `json:"max_budget_usd"`
	AllowedTools       []string `json:"allowed_tools"`
	DisallowedTools    []string `json:"disallowed_tools"`

	// WorkDir, if set, is the directory claude runs in — e.g. a git
	// checkout the agent prepared itself. Aide places no restriction on it:
	// aide and its registered agents already run as the same OS user, so
	// this only changes where the equally-privileged claude process runs
	// from, not what it can reach.
	WorkDir string `json:"work_dir"`

	// TimeoutSecs overrides defaultInferTimeout for this call, capped at
	// maxInferTimeout regardless of what's requested. Long-running
	// unattended reviews need more than the default 5 minutes.
	TimeoutSecs int `json:"timeout_secs"`
}

type inferResponse struct {
	Response        string   `json:"response"`
	SessionID       string   `json:"session_id,omitempty"`
	ClaudeSessionID string   `json:"claude_session_id"`
	Compacted       bool     `json:"compacted"`
	ExpiresAt       *string  `json:"expires_at,omitempty"`
	IsError         bool     `json:"is_error,omitempty"`
	ErrorSubtype    string   `json:"error_subtype,omitempty"`
	Errors          []string `json:"errors,omitempty"`
	Usage           usage    `json:"usage"`
}

type usage struct {
	InputTokens   int                   `json:"input_tokens"`
	OutputTokens  int                   `json:"output_tokens"`
	CostUSD       float64               `json:"cost_usd,omitempty"`
	DurationMs    int64                 `json:"duration_ms,omitempty"`
	APIDurationMs int64                 `json:"api_duration_ms,omitempty"`
	NumTurns      int                   `json:"num_turns,omitempty"`
	Subagents     int                   `json:"subagents,omitempty"`
	Models        map[string]modelUsage `json:"models,omitempty"`
}

type modelUsage struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

func (s *Server) handleInfer(w http.ResponseWriter, r *http.Request) {
	a := s.authenticate(w, r)
	if a == nil {
		return
	}
	if !s.limiter.Allow(a.ID, a.RateLimit.MaxRequests, a.RateLimit.WindowMinutes) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req inferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	if !a.AllowsModel(model) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("model %q not in agent's allowlist", model))
		return
	}
	effort := req.Effort
	if effort == "" {
		effort = a.DefaultEffort
	}
	if !a.AllowsEffort(effort) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("effort %q not in agent's allowlist", effort))
		return
	}

	now := time.Now().UTC()
	resumeID := ""
	turnCount := 0
	createdAt := now
	compacted := false

	if req.SessionID != "" {
		existing, err := s.db.GetAgentSession(a.ID, req.SessionID)
		if err != nil {
			slog.Error("agent session lookup failed", "agent", a.ID, "session", req.SessionID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if existing != nil {
			if needsRotation(existing, now, a) {
				compacted = true // resuming further would extend a stale/oversized context; start fresh
			} else {
				resumeID = existing.ClaudeSessionID
				turnCount = existing.TurnCount
				createdAt = existing.CreatedAt
			}
		}
	}

	timeout := defaultInferTimeout
	if req.TimeoutSecs > 0 {
		timeout = time.Duration(req.TimeoutSecs) * time.Second
		if timeout > maxInferTimeout {
			timeout = maxInferTimeout
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	result, err := s.invoker.Infer(ctx, InferOptions{
		Prompt:             req.Prompt,
		ResumeSessionID:    resumeID,
		Model:              model,
		Effort:             effort,
		AppendSystemPrompt: req.AppendSystemPrompt,
		MaxBudgetUSD:       req.MaxBudgetUSD,
		AllowedTools:       req.AllowedTools,
		DisallowedTools:    req.DisallowedTools,
		WorkDir:            req.WorkDir,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeError(w, http.StatusRequestTimeout, "claude did not respond in time")
			return
		}
		slog.Error("claude invocation failed", "agent", a.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "inference failed")
		return
	}

	models := make(map[string]modelUsage, len(result.Usage.Models))
	for name, m := range result.Usage.Models {
		models[name] = modelUsage{
			InputTokens:              m.InputTokens,
			OutputTokens:             m.OutputTokens,
			CacheReadInputTokens:     m.CacheReadInputTokens,
			CacheCreationInputTokens: m.CacheCreationInputTokens,
			CostUSD:                  m.CostUSD,
		}
	}
	resp := inferResponse{
		Response:        result.Response,
		ClaudeSessionID: result.ClaudeSessionID,
		Compacted:       compacted,
		IsError:         result.IsError,
		ErrorSubtype:    result.ErrorSubtype,
		Errors:          result.Errors,
		Usage: usage{
			InputTokens:   result.InputTokens,
			OutputTokens:  result.OutputTokens,
			CostUSD:       result.Usage.CostUSD,
			DurationMs:    result.Usage.DurationMs,
			APIDurationMs: result.Usage.APIDurationMs,
			NumTurns:      result.Usage.NumTurns,
			Subagents:     result.Usage.Subagents,
			Models:        models,
		},
	}

	if req.SessionID != "" {
		resp.SessionID = req.SessionID
		expiresAt := resolveExpiry(req.SessionTTLMinutes, a.DefaultSessionTTLMinutes, now)
		if expiresAt != nil {
			expStr := expiresAt.Format(time.RFC3339)
			resp.ExpiresAt = &expStr
		}
		as := store.AgentSession{
			AgentID:         a.ID,
			SessionID:       req.SessionID,
			ClaudeSessionID: result.ClaudeSessionID,
			TurnCount:       turnCount + 1,
			CreatedAt:       createdAt,
			LastActiveAt:    now,
			ExpiresAt:       expiresAt,
		}
		if compacted {
			as.CreatedAt = now
		}
		if err := s.db.SaveAgentSession(as); err != nil {
			slog.Error("failed to save agent session", "agent", a.ID, "session", req.SessionID, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// needsRotation reports whether a resumed session should be rotated
// (started fresh) instead of extended further, per the agent's compaction
// thresholds.
func needsRotation(s *store.AgentSession, now time.Time, a *config.Agent) bool {
	if s.TurnCount >= a.MaxTurnsPerSession {
		return true
	}
	if now.Sub(s.CreatedAt) >= time.Duration(a.MaxSessionDurationMinutes)*time.Minute {
		return true
	}
	if now.Sub(s.LastActiveAt) >= time.Duration(a.IdleTimeoutMinutes)*time.Minute {
		return true
	}
	return false
}

// resolveExpiry computes the stored expiry time: callTTL (if set) takes
// precedence over the agent's configured default; 0 at either level, or
// both unset, means the session never expires.
func resolveExpiry(callTTL, defaultTTL *int, now time.Time) *time.Time {
	ttl := defaultTTL
	if callTTL != nil {
		ttl = callTTL
	}
	if ttl == nil || *ttl == 0 {
		return nil
	}
	t := now.Add(time.Duration(*ttl) * time.Minute)
	return &t
}

// ─── /messages (optional) ───────────────────────────────────────────────────

type messagesRequest struct {
	ChannelID string `json:"channel_id"`
	RoomID    string `json:"room_id"`
	Text      string `json:"text"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	a := s.authenticate(w, r)
	if a == nil {
		return
	}

	var req messagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ChannelID == "" || req.RoomID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "channel_id, room_id, and text are required")
		return
	}
	if !a.AllowsChannel(req.ChannelID, req.RoomID) {
		writeError(w, http.StatusForbidden, "channel/room not in agent's allowlist")
		return
	}
	ch, ok := s.chs[req.ChannelID]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown channel_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := ch.Send(ctx, req.RoomID, req.Text); err != nil {
		slog.Error("agent message send failed", "agent", a.ID, "channel", req.ChannelID, "err", err)
		writeError(w, http.StatusBadGateway, "failed to send message")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ─── /heartbeat (optional) ──────────────────────────────────────────────────

// handleHeartbeat lets a supervised aide-agent process report itself healthy.
// It's meaningful only for tasks with agent_mode: supervised and a
// heartbeat_timeout_secs configured (see tasks/tasks.go) — the supervisor
// reads HeartbeatTracker to decide whether to kill and restart a process
// that's alive but stuck. Calling it with no such task configured is
// harmless; the heartbeat is just never read.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	a := s.authenticate(w, r)
	if a == nil {
		return
	}
	s.hb.Touch(a.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── helpers ────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
