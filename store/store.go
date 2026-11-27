// Package store manages the aide SQLite database: channel_state (deduplication
// cursors) and agent_sessions (aide-agent Claude session continuity).
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs migrations.
// Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite write serialisation
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// LoadCursor returns the saved cursor for channelID, or "" if none.
func (s *Store) LoadCursor(channelID string) (string, error) {
	var cursor string
	err := s.db.QueryRow(
		`SELECT cursor FROM channel_state WHERE channel_id = ?`, channelID,
	).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load cursor %q: %w", channelID, err)
	}
	return cursor, nil
}

// SaveCursor upserts the cursor for channelID.
func (s *Store) SaveCursor(channelID, cursor string) error {
	_, err := s.db.Exec(
		`INSERT INTO channel_state (channel_id, cursor) VALUES (?, ?)
		 ON CONFLICT(channel_id) DO UPDATE SET cursor = excluded.cursor`,
		channelID, cursor,
	)
	if err != nil {
		return fmt.Errorf("save cursor %q: %w", channelID, err)
	}
	return nil
}

// AgentSession is one aide-agent's resumable Claude context, keyed by
// (AgentID, SessionID). SessionID is the caller-chosen key from the /infer
// request body; ClaudeSessionID is what gets passed to `claude --resume`.
type AgentSession struct {
	AgentID         string
	SessionID       string
	ClaudeSessionID string
	TurnCount       int
	CreatedAt       time.Time
	LastActiveAt    time.Time
	ExpiresAt       *time.Time // nil = forever
}

const timeLayout = time.RFC3339Nano

// GetAgentSession returns the session, or nil if it doesn't exist or has
// passed its TTL (an expired row is deleted and treated as not found, so the
// caller starts a fresh session rather than erroring).
func (s *Store) GetAgentSession(agentID, sessionID string) (*AgentSession, error) {
	var as AgentSession
	var createdAt, lastActiveAt string
	var expiresAt sql.NullString
	err := s.db.QueryRow(
		`SELECT agent_id, session_id, claude_session_id, turn_count, created_at, last_active_at, expires_at
		 FROM agent_sessions WHERE agent_id = ? AND session_id = ?`,
		agentID, sessionID,
	).Scan(&as.AgentID, &as.SessionID, &as.ClaudeSessionID, &as.TurnCount, &createdAt, &lastActiveAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent session %q/%q: %w", agentID, sessionID, err)
	}
	if as.CreatedAt, err = time.Parse(timeLayout, createdAt); err != nil {
		return nil, fmt.Errorf("get agent session %q/%q: parse created_at: %w", agentID, sessionID, err)
	}
	if as.LastActiveAt, err = time.Parse(timeLayout, lastActiveAt); err != nil {
		return nil, fmt.Errorf("get agent session %q/%q: parse last_active_at: %w", agentID, sessionID, err)
	}
	if expiresAt.Valid {
		t, err := time.Parse(timeLayout, expiresAt.String)
		if err != nil {
			return nil, fmt.Errorf("get agent session %q/%q: parse expires_at: %w", agentID, sessionID, err)
		}
		as.ExpiresAt = &t
	}
	if as.ExpiresAt != nil && as.ExpiresAt.Before(time.Now()) {
		if err := s.DeleteAgentSession(agentID, sessionID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &as, nil
}

// SaveAgentSession upserts a session row. Used both for ordinary continuity
// updates and for rotation (compaction), where the caller passes a fresh
// ClaudeSessionID and TurnCount reset to 0 while keeping the same SessionID.
func (s *Store) SaveAgentSession(as AgentSession) error {
	var expiresAt sql.NullString
	if as.ExpiresAt != nil {
		expiresAt = sql.NullString{String: as.ExpiresAt.Format(timeLayout), Valid: true}
	}
	_, err := s.db.Exec(
		`INSERT INTO agent_sessions (agent_id, session_id, claude_session_id, turn_count, created_at, last_active_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id, session_id) DO UPDATE SET
		   claude_session_id = excluded.claude_session_id,
		   turn_count        = excluded.turn_count,
		   created_at        = excluded.created_at,
		   last_active_at    = excluded.last_active_at,
		   expires_at        = excluded.expires_at`,
		as.AgentID, as.SessionID, as.ClaudeSessionID, as.TurnCount,
		as.CreatedAt.Format(timeLayout), as.LastActiveAt.Format(timeLayout), expiresAt,
	)
	if err != nil {
		return fmt.Errorf("save agent session %q/%q: %w", as.AgentID, as.SessionID, err)
	}
	return nil
}

// DeleteAgentSession removes a session row.
func (s *Store) DeleteAgentSession(agentID, sessionID string) error {
	_, err := s.db.Exec(
		`DELETE FROM agent_sessions WHERE agent_id = ? AND session_id = ?`,
		agentID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("delete agent session %q/%q: %w", agentID, sessionID, err)
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS channel_state (
			channel_id TEXT NOT NULL PRIMARY KEY,
			cursor     TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_sessions (
			agent_id          TEXT NOT NULL,
			session_id        TEXT NOT NULL,
			claude_session_id TEXT NOT NULL,
			turn_count        INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT NOT NULL,
			last_active_at    TEXT NOT NULL,
			expires_at        TEXT,
			PRIMARY KEY (agent_id, session_id)
		)
	`)
	return err
}
