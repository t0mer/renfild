package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Utterance is one row of history: what was said, by whom, and how long every
// stage took. This table is the debugging tool, so it records generously.
type Utterance struct {
	ID              int64     `json:"id"`
	TS              time.Time `json:"ts"`
	SatelliteID     string    `json:"satellite_id"`
	Speaker         string    `json:"speaker"`
	Confidence      float64   `json:"confidence"`
	RunnerUp        string    `json:"runner_up,omitempty"`
	RunnerUpScore   float64   `json:"runner_up_score,omitempty"`
	Transcript      string    `json:"transcript"`
	Intent          string    `json:"intent"`
	Reply           string    `json:"reply"`
	Allowed         bool      `json:"allowed"`
	Error           string    `json:"error,omitempty"`
	AudioPath       string    `json:"audio_path,omitempty"`
	LatencyMsTotal  int       `json:"latency_ms_total"`
	LatencyMsSTT    int       `json:"latency_ms_stt"`
	LatencyMsSpk    int       `json:"latency_ms_spk"`
	LatencyMsIntent int       `json:"latency_ms_intent"`
	LatencyMsTTS    int       `json:"latency_ms_tts"`
}

// HistoryFilter narrows a history query.
type HistoryFilter struct {
	Speaker string
	Intent  string
	Search  string
	Limit   int
	Offset  int
}

// InsertUtterance records one utterance and returns its id.
func (s *Store) InsertUtterance(ctx context.Context, u Utterance) (int64, error) {
	if u.TS.IsZero() {
		u.TS = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO utterances (ts, satellite_id, speaker, confidence, runner_up, runner_up_score,
		                        transcript, intent, reply, allowed, error, audio_path,
		                        latency_ms_total, latency_ms_stt, latency_ms_spk,
		                        latency_ms_intent, latency_ms_tts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.TS.UTC().Format(time.RFC3339Nano), u.SatelliteID, u.Speaker, u.Confidence,
		u.RunnerUp, u.RunnerUpScore, u.Transcript, u.Intent, u.Reply, boolToInt(u.Allowed),
		u.Error, u.AudioPath, u.LatencyMsTotal, u.LatencyMsSTT, u.LatencyMsSpk,
		u.LatencyMsIntent, u.LatencyMsTTS)
	if err != nil {
		return 0, fmt.Errorf("recording utterance: %w", err)
	}
	return result.LastInsertId()
}

// ListUtterances returns history newest-first, with paging and filtering.
func (s *Store) ListUtterances(ctx context.Context, filter HistoryFilter) ([]Utterance, int, error) {
	var (
		where []string
		args  []any
	)
	if filter.Speaker != "" {
		where = append(where, "speaker = ?")
		args = append(args, filter.Speaker)
	}
	if filter.Intent != "" {
		where = append(where, "intent = ?")
		args = append(args, filter.Intent)
	}
	if filter.Search != "" {
		where = append(where, "(transcript LIKE ? OR reply LIKE ?)")
		needle := "%" + filter.Search + "%"
		args = append(args, needle, needle)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM utterances"+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting utterances: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, COALESCE(satellite_id,''), COALESCE(speaker,''), COALESCE(confidence,0),
		       COALESCE(runner_up,''), COALESCE(runner_up_score,0), COALESCE(transcript,''),
		       COALESCE(intent,''), COALESCE(reply,''), allowed, COALESCE(error,''),
		       COALESCE(audio_path,''), COALESCE(latency_ms_total,0), COALESCE(latency_ms_stt,0),
		       COALESCE(latency_ms_spk,0), COALESCE(latency_ms_intent,0), COALESCE(latency_ms_tts,0)
		FROM utterances`+clause+`
		ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing utterances: %w", err)
	}
	defer rows.Close()

	var out []Utterance
	for rows.Next() {
		var (
			item    Utterance
			ts      string
			allowed int
		)
		if err := rows.Scan(&item.ID, &ts, &item.SatelliteID, &item.Speaker, &item.Confidence,
			&item.RunnerUp, &item.RunnerUpScore, &item.Transcript, &item.Intent, &item.Reply,
			&allowed, &item.Error, &item.AudioPath, &item.LatencyMsTotal, &item.LatencyMsSTT,
			&item.LatencyMsSpk, &item.LatencyMsIntent, &item.LatencyMsTTS); err != nil {
			return nil, 0, fmt.Errorf("scanning utterance: %w", err)
		}
		item.TS = parseTime(ts)
		item.Allowed = allowed != 0
		out = append(out, item)
	}
	return out, total, rows.Err()
}

// Stats is the dashboard summary.
type Stats struct {
	Total          int            `json:"total"`
	Last24h        int            `json:"last_24h"`
	BySpeaker      map[string]int `json:"by_speaker"`
	ByIntent       map[string]int `json:"by_intent"`
	AvgLatencyMs   int            `json:"avg_latency_ms"`
	SpeakerCount   int            `json:"speaker_count"`
	IntentCount    int            `json:"intent_count"`
	UnknownRatePct int            `json:"unknown_rate_pct"`
}

// Stats computes the dashboard numbers in a handful of cheap queries.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{BySpeaker: map[string]int{}, ByIntent: map[string]int{}}
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)

	scalars := []struct {
		query  string
		args   []any
		target *int
	}{
		{"SELECT COUNT(*) FROM utterances", nil, &stats.Total},
		{"SELECT COUNT(*) FROM utterances WHERE ts >= ?", []any{cutoff}, &stats.Last24h},
		{"SELECT COUNT(*) FROM speakers", nil, &stats.SpeakerCount},
		{"SELECT COUNT(*) FROM intents", nil, &stats.IntentCount},
		{"SELECT COALESCE(CAST(AVG(latency_ms_total) AS INTEGER), 0) FROM utterances", nil, &stats.AvgLatencyMs},
	}
	for _, scalar := range scalars {
		if err := s.db.QueryRowContext(ctx, scalar.query, scalar.args...).Scan(scalar.target); err != nil {
			return stats, fmt.Errorf("computing stats: %w", err)
		}
	}

	if stats.Total > 0 {
		var unknown int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM utterances WHERE speaker = 'unknown' OR speaker = ''`).Scan(&unknown); err != nil {
			return stats, fmt.Errorf("computing unknown rate: %w", err)
		}
		stats.UnknownRatePct = unknown * 100 / stats.Total
	}

	if err := countInto(ctx, s.db, `SELECT COALESCE(speaker,'unknown'), COUNT(*) FROM utterances
	                                GROUP BY speaker ORDER BY 2 DESC LIMIT 10`, stats.BySpeaker); err != nil {
		return stats, err
	}
	if err := countInto(ctx, s.db, `SELECT COALESCE(intent,''), COUNT(*) FROM utterances
	                                GROUP BY intent ORDER BY 2 DESC LIMIT 10`, stats.ByIntent); err != nil {
		return stats, err
	}
	return stats, nil
}

func countInto(ctx context.Context, db *sql.DB, query string, target map[string]int) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("grouping stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key   string
			count int
		)
		if err := rows.Scan(&key, &count); err != nil {
			return fmt.Errorf("scanning stats: %w", err)
		}
		if key == "" {
			key = "none"
		}
		target[key] = count
	}
	return rows.Err()
}

// PruneAudio deletes stored audio references older than the retention window.
// The files themselves are removed by the caller, which owns the filesystem.
func (s *Store) PruneAudio(ctx context.Context, olderThan time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT audio_path FROM utterances WHERE audio_path != '' AND ts < ?`,
		olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("finding expired audio: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE utterances SET audio_path = '' WHERE audio_path != '' AND ts < ?`,
		olderThan.UTC().Format(time.RFC3339Nano)); err != nil {
		return paths, fmt.Errorf("clearing expired audio paths: %w", err)
	}
	return paths, nil
}

// GetUtterance returns a single history row by id.
func (s *Store) GetUtterance(ctx context.Context, id int64) (Utterance, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, ts, COALESCE(satellite_id,''), COALESCE(speaker,''), COALESCE(confidence,0),
		       COALESCE(runner_up,''), COALESCE(runner_up_score,0), COALESCE(transcript,''),
		       COALESCE(intent,''), COALESCE(reply,''), allowed, COALESCE(error,''),
		       COALESCE(audio_path,''), COALESCE(latency_ms_total,0), COALESCE(latency_ms_stt,0),
		       COALESCE(latency_ms_spk,0), COALESCE(latency_ms_intent,0), COALESCE(latency_ms_tts,0)
		FROM utterances WHERE id = ?`, id)

	var (
		item    Utterance
		ts      string
		allowed int
	)
	err := row.Scan(&item.ID, &ts, &item.SatelliteID, &item.Speaker, &item.Confidence,
		&item.RunnerUp, &item.RunnerUpScore, &item.Transcript, &item.Intent, &item.Reply,
		&allowed, &item.Error, &item.AudioPath, &item.LatencyMsTotal, &item.LatencyMsSTT,
		&item.LatencyMsSpk, &item.LatencyMsIntent, &item.LatencyMsTTS)
	if errors.Is(err, sql.ErrNoRows) {
		return Utterance{}, ErrNotFound
	}
	if err != nil {
		return Utterance{}, fmt.Errorf("reading utterance %d: %w", id, err)
	}
	item.TS = parseTime(ts)
	item.Allowed = allowed != 0
	return item, nil
}
