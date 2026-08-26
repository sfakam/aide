package pty

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// Manager maintains a pool of Sessions keyed by session key.
// It creates sessions on demand and reaps idle ones.
type Manager struct {
	claudePath     string
	baseWorkDir    string
	timeoutMinutes int

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates a Manager.
// baseWorkDir is the parent directory; each session lives in <baseWorkDir>/<key>.
// timeoutMinutes is the idle-session reap timeout (0 → 60 min default).
func NewManager(claudePath, baseWorkDir string, timeoutMinutes int) *Manager {
	if timeoutMinutes <= 0 {
		timeoutMinutes = 60
	}
	return &Manager{
		claudePath:     claudePath,
		baseWorkDir:    baseWorkDir,
		timeoutMinutes: timeoutMinutes,
		sessions:       make(map[string]*Session),
	}
}

// Send routes a message to the session identified by key, spawning it if necessary.
// statusFn, if non-nil, is called with a progress label on each new tool invocation.
func (m *Manager) Send(ctx context.Context, key, sender, text string, statusFn func(string)) (string, error) {
	sess, err := m.getOrCreate(ctx, key)
	if err != nil {
		return "", fmt.Errorf("session %q: %w", key, err)
	}
	return sess.Send(ctx, sender, text, statusFn)
}

// ActiveSessions returns a snapshot of currently alive session keys.
func (m *Manager) ActiveSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.sessions))
	for k, s := range m.sessions {
		if s.IsAlive() {
			keys = append(keys, k)
		}
	}
	return keys
}

// StartReaper runs a background goroutine that kills idle sessions.
// It stops when ctx is cancelled.
func (m *Manager) StartReaper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.reap()
			case <-ctx.Done():
				m.killAll()
				return
			}
		}
	}()
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (m *Manager) getOrCreate(ctx context.Context, key string) (*Session, error) {
	m.mu.Lock()
	sess, ok := m.sessions[key]
	if ok && sess.IsAlive() {
		m.mu.Unlock()
		return sess, nil
	}
	// Create new session
	workDir := filepath.Join(m.baseWorkDir, sanitiseKey(key))
	sess = newSession(key, m.claudePath, workDir)
	m.sessions[key] = sess
	m.mu.Unlock()

	if err := sess.start(ctx); err != nil {
		m.mu.Lock()
		delete(m.sessions, key)
		m.mu.Unlock()
		return nil, err
	}
	return sess, nil
}

func (m *Manager) reap() {
	cutoff := time.Now().Add(-time.Duration(m.timeoutMinutes) * time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, sess := range m.sessions {
		if !sess.IsAlive() || sess.LastUsed().Before(cutoff) {
			slog.Info("reaping idle session", "key", key)
			sess.Kill()
			delete(m.sessions, key)
		}
	}
}

func (m *Manager) killAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, sess := range m.sessions {
		slog.Info("shutting down session", "key", key)
		sess.Kill()
	}
	m.sessions = make(map[string]*Session)
}

// sanitiseKey replaces characters unsafe for directory names.
func sanitiseKey(key string) string {
	out := make([]byte, len(key))
	for i := range key {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' {
			out[i] = c
		} else {
			out[i] = '_'
		}
	}
	return string(out)
}
