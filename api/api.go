// Package api exposes a lightweight REST API for observability and future GUI use.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sfathall/aide/pty"
)

// Server serves the REST API.
type Server struct {
	manager *pty.Manager
	srv     *http.Server
}

// New creates an API Server bound to addr (e.g. ":8080").
func New(addr string, manager *pty.Manager) *Server {
	s := &Server{manager: manager}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.srv = &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
}

// Start begins serving. Blocks until ctx is cancelled or the server errors.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	keys := s.manager.ActiveSessions()
	writeJSON(w, http.StatusOK, map[string]any{
		"active_sessions": keys,
		"count":           len(keys),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
