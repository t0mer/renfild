package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// GetSetting reads a JSON-encoded setting into target. It reports false when
// the key has never been written.
func (s *Store) GetSetting(ctx context.Context, key string, target any) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading setting %q: %w", key, err)
	}
	if err := json.Unmarshal([]byte(value), target); err != nil {
		return false, fmt.Errorf("decoding setting %q: %w", key, err)
	}
	return true, nil
}

// PutSetting stores a JSON-encodable value.
func (s *Store) PutSetting(ctx context.Context, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding setting %q: %w", key, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(encoded), now()); err != nil {
		return fmt.Errorf("storing setting %q: %w", key, err)
	}
	return nil
}
