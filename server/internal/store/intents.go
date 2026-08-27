package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/speaker"
)

// Rules implements intent.RuleSource: the enabled rule table, cheapest first.
// This is the router's path, so credentials come back decrypted — they are
// about to be sent.
func (s *Store) Rules(ctx context.Context) ([]intent.Rule, error) {
	rules, err := s.listIntents(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if err := intent.OpenHandlerConfig(&rules[i], s.secrets); err != nil {
			return nil, fmt.Errorf("intent %q: %w", rules[i].Name, err)
		}
	}
	return rules, nil
}

// ListIntents returns every rule, enabled or not, for the web UI — with the
// credentials masked, so they are never served over HTTP.
func (s *Store) ListIntents(ctx context.Context) ([]intent.Rule, error) {
	rules, err := s.listIntents(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		intent.MaskHandlerConfig(&rules[i])
	}
	return rules, nil
}

func (s *Store) listIntents(ctx context.Context, onlyEnabled bool) ([]intent.Rule, error) {
	query := `SELECT id, name, enabled, match_type, patterns, min_role, handler,
	                 handler_config, priority, created_at, updated_at
	          FROM intents`
	if onlyEnabled {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY priority, id`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing intents: %w", err)
	}
	defer rows.Close()

	var out []intent.Rule
	for rows.Next() {
		rule, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// GetIntent returns one rule by id.
func (s *Store) GetIntent(ctx context.Context, id int64) (intent.Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, match_type, patterns, min_role, handler,
		       handler_config, priority, created_at, updated_at
		FROM intents WHERE id = ?`, id)
	rule, err := scanIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return intent.Rule{}, ErrNotFound
	}
	if err != nil {
		return rule, err
	}
	// Same as ListIntents: this is what the web UI reads.
	intent.MaskHandlerConfig(&rule)
	return rule, nil
}

// storedIntent returns a rule exactly as the database holds it, credentials
// still sealed. Only the update path needs this, to keep a header the user did
// not retype.
func (s *Store) storedIntent(ctx context.Context, id int64) (intent.Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, match_type, patterns, min_role, handler,
		       handler_config, priority, created_at, updated_at
		FROM intents WHERE id = ?`, id)
	rule, err := scanIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return intent.Rule{}, ErrNotFound
	}
	return rule, err
}

func scanIntent(row scanner) (intent.Rule, error) {
	var (
		rule     intent.Rule
		enabled  int
		patterns string
		minRole  string
		config   string
		created  string
		updated  string
	)
	if err := row.Scan(&rule.ID, &rule.Name, &enabled, &rule.MatchType, &patterns,
		&minRole, &rule.Handler, &config, &rule.Priority, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rule, err
		}
		return rule, fmt.Errorf("scanning intent: %w", err)
	}
	if err := json.Unmarshal([]byte(patterns), &rule.Patterns); err != nil {
		return rule, fmt.Errorf("intent %q: decoding patterns: %w", rule.Name, err)
	}
	rule.Enabled = enabled != 0
	rule.MinRole = speaker.ParseRole(minRole)
	rule.HandlerConfig = json.RawMessage(config)
	rule.CreatedAt = parseTime(created)
	rule.UpdatedAt = parseTime(updated)
	// Compile eagerly so a broken regex surfaces here rather than mid-utterance.
	if err := rule.Compile(); err != nil {
		return rule, fmt.Errorf("intent %q: %w", rule.Name, err)
	}
	return rule, nil
}

// CreateIntent inserts a validated rule and returns its id.
func (s *Store) CreateIntent(ctx context.Context, rule intent.Rule) (int64, error) {
	if err := rule.Validate(); err != nil {
		return 0, err
	}
	if err := intent.SealHandlerConfig(&rule, s.secrets, nil); err != nil {
		return 0, fmt.Errorf("intent %q: %w", rule.Name, err)
	}
	patterns, err := json.Marshal(rule.Patterns)
	if err != nil {
		return 0, fmt.Errorf("encoding patterns: %w", err)
	}
	stamp := now()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO intents (name, enabled, match_type, patterns, min_role, handler,
		                     handler_config, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.Name, boolToInt(rule.Enabled), rule.MatchType, string(patterns), string(rule.MinRole),
		rule.Handler, configOrEmpty(rule.HandlerConfig), rule.Priority, stamp, stamp)
	if err != nil {
		return 0, fmt.Errorf("creating intent %q: %w", rule.Name, err)
	}
	return result.LastInsertId()
}

// UpdateIntent replaces a rule in place.
func (s *Store) UpdateIntent(ctx context.Context, id int64, rule intent.Rule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	stored, err := s.storedIntent(ctx, id)
	if err != nil {
		return err
	}
	if err := intent.SealHandlerConfig(&rule, s.secrets, stored.HandlerConfig); err != nil {
		return fmt.Errorf("intent %q: %w", rule.Name, err)
	}
	patterns, err := json.Marshal(rule.Patterns)
	if err != nil {
		return fmt.Errorf("encoding patterns: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE intents SET name = ?, enabled = ?, match_type = ?, patterns = ?, min_role = ?,
		                   handler = ?, handler_config = ?, priority = ?, updated_at = ?
		WHERE id = ?`,
		rule.Name, boolToInt(rule.Enabled), rule.MatchType, string(patterns), string(rule.MinRole),
		rule.Handler, configOrEmpty(rule.HandlerConfig), rule.Priority, now(), id)
	if err != nil {
		return fmt.Errorf("updating intent %d: %w", id, err)
	}
	return affected(result)
}

// DeleteIntent removes a rule.
func (s *Store) DeleteIntent(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM intents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting intent %d: %w", id, err)
	}
	return affected(result)
}

// ReorderIntents applies new priorities in one transaction, which is what the
// UI's drag-to-reorder sends.
func (s *Store) ReorderIntents(ctx context.Context, priorities map[int64]int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting reorder: %w", err)
	}
	defer tx.Rollback()

	for id, priority := range priorities {
		if _, err := tx.ExecContext(ctx,
			`UPDATE intents SET priority = ?, updated_at = ? WHERE id = ?`,
			priority, now(), id); err != nil {
			return fmt.Errorf("reordering intent %d: %w", id, err)
		}
	}
	return tx.Commit()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func configOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}
