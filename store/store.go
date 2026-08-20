// Package store manages the aide SQLite database.
// Currently a single table: channel_state for deduplication cursors.
package store

import (
	"database/sql"
	"fmt"

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
	return err
}
