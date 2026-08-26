package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/t0mer/renfild/internal/speaker"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ListSpeakers returns every enrolled speaker with its sample count.
func (s *Store) ListSpeakers(ctx context.Context) ([]speaker.Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sp.id, sp.name, sp.role, sp.centroid, sp.threshold, sp.created_at, sp.updated_at,
		       (SELECT COUNT(*) FROM enrollments e WHERE e.speaker_id = sp.id)
		FROM speakers sp
		ORDER BY sp.name`)
	if err != nil {
		return nil, fmt.Errorf("listing speakers: %w", err)
	}
	defer rows.Close()

	var out []speaker.Record
	for rows.Next() {
		record, err := scanSpeaker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// GetSpeaker returns one speaker by id.
func (s *Store) GetSpeaker(ctx context.Context, id int64) (speaker.Record, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT sp.id, sp.name, sp.role, sp.centroid, sp.threshold, sp.created_at, sp.updated_at,
		       (SELECT COUNT(*) FROM enrollments e WHERE e.speaker_id = sp.id)
		FROM speakers sp WHERE sp.id = ?`, id)
	record, err := scanSpeaker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return speaker.Record{}, ErrNotFound
	}
	return record, err
}

// GetSpeakerByName returns one speaker by name.
func (s *Store) GetSpeakerByName(ctx context.Context, name string) (speaker.Record, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT sp.id, sp.name, sp.role, sp.centroid, sp.threshold, sp.created_at, sp.updated_at,
		       (SELECT COUNT(*) FROM enrollments e WHERE e.speaker_id = sp.id)
		FROM speakers sp WHERE sp.name = ?`, name)
	record, err := scanSpeaker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return speaker.Record{}, ErrNotFound
	}
	return record, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSpeaker(row scanner) (speaker.Record, error) {
	var (
		record    speaker.Record
		blob      []byte
		threshold sql.NullFloat64
		role      string
		created   string
		updated   string
	)
	if err := row.Scan(&record.ID, &record.Name, &role, &blob, &threshold,
		&created, &updated, &record.Samples); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record, err
		}
		return record, fmt.Errorf("scanning speaker: %w", err)
	}
	centroid, err := speaker.Decode(blob)
	if err != nil {
		return record, fmt.Errorf("speaker %q: %w", record.Name, err)
	}
	record.Centroid = centroid
	record.Role = speaker.ParseRole(role)
	if threshold.Valid {
		value := threshold.Float64
		record.Threshold = &value
	}
	record.CreatedAt = parseTime(created)
	record.UpdatedAt = parseTime(updated)
	return record, nil
}

// CreateSpeaker inserts a speaker with an initially empty voice print.
func (s *Store) CreateSpeaker(ctx context.Context, name string, role speaker.Role, threshold *float64) (int64, error) {
	stamp := now()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO speakers (name, role, centroid, threshold, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		name, string(role), []byte{}, nullFloat(threshold), stamp, stamp)
	if err != nil {
		return 0, fmt.Errorf("creating speaker %q: %w", name, err)
	}
	return result.LastInsertId()
}

// UpdateSpeaker changes the editable fields of a speaker.
func (s *Store) UpdateSpeaker(ctx context.Context, id int64, name string, role speaker.Role, threshold *float64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE speakers SET name = ?, role = ?, threshold = ?, updated_at = ? WHERE id = ?`,
		name, string(role), nullFloat(threshold), now(), id)
	if err != nil {
		return fmt.Errorf("updating speaker %d: %w", id, err)
	}
	return affected(result)
}

// DeleteSpeaker removes a speaker and, by cascade, its enrollments.
func (s *Store) DeleteSpeaker(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM speakers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting speaker %d: %w", id, err)
	}
	return affected(result)
}

// AddEnrollment stores one sample and recomputes the speaker's centroid, so the
// voice print is never out of step with the samples behind it.
func (s *Store) AddEnrollment(ctx context.Context, speakerID int64, embedding speaker.Vector, durationS float64, label string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting enrollment transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO enrollments (speaker_id, embedding, duration_s, label, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		speakerID, speaker.Encode(embedding), durationS, label, now())
	if err != nil {
		return 0, fmt.Errorf("inserting enrollment: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := recomputeCentroid(ctx, tx, speakerID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing enrollment: %w", err)
	}
	return id, nil
}

// DeleteEnrollment drops one sample and recomputes the centroid without it.
func (s *Store) DeleteEnrollment(ctx context.Context, speakerID, enrollmentID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx,
		`DELETE FROM enrollments WHERE id = ? AND speaker_id = ?`, enrollmentID, speakerID)
	if err != nil {
		return fmt.Errorf("deleting enrollment %d: %w", enrollmentID, err)
	}
	if err := affected(result); err != nil {
		return err
	}
	if err := recomputeCentroid(ctx, tx, speakerID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListEnrollments returns a speaker's samples, newest first.
func (s *Store) ListEnrollments(ctx context.Context, speakerID int64) ([]speaker.Enrollment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, speaker_id, embedding, duration_s, COALESCE(label, ''), created_at
		FROM enrollments WHERE speaker_id = ? ORDER BY id DESC`, speakerID)
	if err != nil {
		return nil, fmt.Errorf("listing enrollments: %w", err)
	}
	defer rows.Close()

	var out []speaker.Enrollment
	for rows.Next() {
		var (
			item     speaker.Enrollment
			blob     []byte
			duration sql.NullFloat64
		)
		if err := rows.Scan(&item.ID, &item.SpeakerID, &blob, &duration, &item.Label, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning enrollment: %w", err)
		}
		vector, err := speaker.Decode(blob)
		if err != nil {
			return nil, err
		}
		item.Embedding = vector
		item.DurationS = duration.Float64
		out = append(out, item)
	}
	return out, rows.Err()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// recomputeCentroid averages a speaker's samples into their stored voice print.
func recomputeCentroid(ctx context.Context, tx execer, speakerID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT embedding FROM enrollments WHERE speaker_id = ?`, speakerID)
	if err != nil {
		return fmt.Errorf("reading enrollments for centroid: %w", err)
	}
	defer rows.Close()

	var vectors []speaker.Vector
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return fmt.Errorf("scanning embedding: %w", err)
		}
		vector, err := speaker.Decode(blob)
		if err != nil {
			return err
		}
		vectors = append(vectors, vector)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	blob := []byte{}
	if len(vectors) > 0 {
		centroid, err := speaker.Centroid(vectors)
		if err != nil {
			return fmt.Errorf("computing centroid: %w", err)
		}
		blob = speaker.Encode(centroid)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE speakers SET centroid = ?, updated_at = ? WHERE id = ?`,
		blob, now(), speakerID); err != nil {
		return fmt.Errorf("storing centroid: %w", err)
	}
	return nil
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func affected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func parseTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}
