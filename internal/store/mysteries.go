package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Mystery persistence, plus the daily series the detector reads. Everything
// here is derived from events that are already stored — a mystery is a
// *reading* of history, never a new fact about it.

// ErrMysteryNotFound is returned for an unknown mystery id.
var ErrMysteryNotFound = errors.New("mystery not found")

// SeriesKey identifies one daily series: a source and an app.
type SeriesKey struct {
	Source string `json:"source"`
	App    string `json:"app"`
}

// SeriesMetric names a daily series the detector can examine. These are not
// quest metrics: they are per (source, app) shapes, chosen because a break in
// any of them is worth a question.
type SeriesMetric string

const (
	SeriesInstalls      SeriesMetric = "installs"
	SeriesUnits         SeriesMetric = "units"
	SeriesRevenue       SeriesMetric = "revenue_base"
	SeriesRefunds       SeriesMetric = "refunds"
	SeriesCancellations SeriesMetric = "cancellations"
)

// DailySeries returns metric values per (source, app) per day over the
// inclusive window [from, to]. Days with no rows are simply absent; the
// detector zero-fills them, because "nothing happened" is data.
func (s *Store) DailySeries(ctx context.Context, metric SeriesMetric, from, to string) (map[SeriesKey]map[string]float64, error) {
	var query string
	switch metric {
	case SeriesInstalls:
		// Overview rows win over per-country rows; see installValue.
		query = `SELECT e.source, e.app, e.day, ` + installValue + `
                 FROM events e WHERE e.kind = 'install' AND e.day BETWEEN ? AND ?
                 GROUP BY e.source, e.app, e.day`
	case SeriesUnits:
		query = `SELECT e.source, e.app, e.day,
                        COALESCE(SUM(CASE WHEN ` + isPaidUnit + ` THEN e.quantity ELSE 0 END), 0)
                 FROM events e WHERE ` + ledgerRows + ` AND e.day BETWEEN ? AND ?
                 GROUP BY e.source, e.app, e.day`
	case SeriesRevenue:
		query = `SELECT e.source, e.app, e.day, COALESCE(SUM(e.amount_base), 0)
                 FROM events e WHERE ` + ledgerRows + ` AND e.day BETWEEN ? AND ?
                 GROUP BY e.source, e.app, e.day`
	case SeriesRefunds:
		query = `SELECT e.source, e.app, e.day,
                        COALESCE(SUM(CASE WHEN ` + isRefund + ` THEN ABS(e.quantity) ELSE 0 END), 0)
                 FROM events e WHERE ` + ledgerRows + ` AND e.day BETWEEN ? AND ?
                 GROUP BY e.source, e.app, e.day`
	case SeriesCancellations:
		query = `SELECT e.source, e.app, e.day, COUNT(*)
                 FROM events e
                 WHERE e.kind IN ('cancellation', 'expiration') AND e.day BETWEEN ? AND ?
                 GROUP BY e.source, e.app, e.day`
	default:
		return nil, fmt.Errorf("unknown series metric %q", metric)
	}

	rows, err := s.q.QueryContext(ctx, query, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily series %s: %w", metric, err)
	}
	defer rows.Close()

	out := map[SeriesKey]map[string]float64{}
	for rows.Next() {
		var (
			key   SeriesKey
			day   string
			value float64
		)
		if err := rows.Scan(&key.Source, &key.App, &day, &value); err != nil {
			return nil, fmt.Errorf("scan daily series %s: %w", metric, err)
		}
		if out[key] == nil {
			out[key] = map[string]float64{}
		}
		out[key][day] = round2(value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily series %s: %w", metric, err)
	}
	return out, nil
}

// SettlementsByDay counts first-ever countries founded per day, which is what
// a "new country cluster" is made of.
func (s *Store) SettlementsByDay(ctx context.Context, from, to string) (map[string]float64, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT day, COUNT(*) FROM events
        WHERE kind = 'settlement' AND day BETWEEN ? AND ?
        GROUP BY day`, from, to)
	if err != nil {
		return nil, fmt.Errorf("settlements by day: %w", err)
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var (
			day string
			n   float64
		)
		if err := rows.Scan(&day, &n); err != nil {
			return nil, fmt.Errorf("scan settlements by day: %w", err)
		}
		out[day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settlements by day: %w", err)
	}
	return out, nil
}

// EventsBySourceDay counts stored events per source per day. It is how silence
// is noticed: a source that reported every day and then stopped.
//
// Loot's own synthetic source is excluded — it has no credentials to break.
func (s *Store) EventsBySourceDay(ctx context.Context, from, to string) (map[string]map[string]int, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT source, day, COUNT(*) FROM events
        WHERE day BETWEEN ? AND ? AND source <> 'loot'
        GROUP BY source, day`, from, to)
	if err != nil {
		return nil, fmt.Errorf("events by source day: %w", err)
	}
	defer rows.Close()

	out := map[string]map[string]int{}
	for rows.Next() {
		var (
			source, day string
			n           int
		)
		if err := rows.Scan(&source, &day, &n); err != nil {
			return nil, fmt.Errorf("scan events by source day: %w", err)
		}
		if out[source] == nil {
			out[source] = map[string]int{}
		}
		out[source][day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events by source day: %w", err)
	}
	return out, nil
}

const mysteryColumns = `id, kind, source, app, metric, day, observed, expected, z,
       title, detail, status, note, created_at, resolved_at, dedupe_key`

// InsertMystery stores a mystery, ignoring one whose (kind, source, app,
// metric, day) has already been flagged. created is false in that case, so the
// detector can re-read the same fortnight as often as it likes.
func (s *Store) InsertMystery(ctx context.Context, m core.Mystery) (created bool, err error) {
	if m.ID == "" {
		return false, errors.New("mystery id is required")
	}
	if m.DedupeKey == "" {
		return false, errors.New("mystery dedupe key is required")
	}
	if m.Status == "" {
		m.Status = core.MysteryOpen
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	detail := []byte(m.Detail)
	if len(detail) == 0 {
		detail = nil
	}

	res, err := s.q.ExecContext(ctx, `
        INSERT INTO mysteries (`+mysteryColumns+`)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(dedupe_key) DO NOTHING`,
		m.ID, m.Kind, m.Source, m.App, m.Metric, m.Day, m.Observed, m.Expected, m.Z,
		m.Title, detail, m.Status, m.Note, m.CreatedAt.UTC().UnixMilli(), nil, m.DedupeKey)
	if err != nil {
		return false, fmt.Errorf("insert mystery: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert mystery rows: %w", err)
	}
	return n > 0, nil
}

// MysteryQuery parameterizes ListMysteries.
type MysteryQuery struct {
	Statuses []string
	Limit    int
}

// ListMysteries returns mysteries matching q, newest flagged day first.
func (s *Store) ListMysteries(ctx context.Context, q MysteryQuery) ([]core.Mystery, error) {
	query := `SELECT ` + mysteryColumns + ` FROM mysteries`
	var args []any
	if len(q.Statuses) > 0 {
		query += ` WHERE status IN (` + placeholders(len(q.Statuses)) + `)`
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	query += ` ORDER BY COALESCE(resolved_at, created_at) DESC, day DESC, id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mysteries: %w", err)
	}
	defer rows.Close()

	out := []core.Mystery{}
	for rows.Next() {
		m, err := scanMystery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mysteries: %w", err)
	}
	return out, nil
}

// GetMystery returns one mystery by id, or ErrMysteryNotFound.
func (s *Store) GetMystery(ctx context.Context, id string) (core.Mystery, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+mysteryColumns+` FROM mysteries WHERE id = ?`, id)
	if err != nil {
		return core.Mystery{}, fmt.Errorf("get mystery: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return core.Mystery{}, fmt.Errorf("get mystery: %w", err)
		}
		return core.Mystery{}, ErrMysteryNotFound
	}
	return scanMystery(rows)
}

// CountOpenMysteries is the number behind the Quests tab's badge.
func (s *Store) CountOpenMysteries(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mysteries WHERE status = ?`, core.MysteryOpen).Scan(&n); err != nil {
		return 0, fmt.Errorf("count open mysteries: %w", err)
	}
	return n, nil
}

// ResolveMystery closes a mystery as solved or dismissed, keeping the note.
// Resolving an already-resolved mystery reports false and changes nothing, so
// a double click cannot pay two drops.
func (s *Store) ResolveMystery(ctx context.Context, id, status, note string, at time.Time) (bool, error) {
	res, err := s.q.ExecContext(ctx, `
        UPDATE mysteries SET status = ?, note = ?, resolved_at = ?
        WHERE id = ? AND status = ?`,
		status, note, at.UTC().UnixMilli(), id, core.MysteryOpen)
	if err != nil {
		return false, fmt.Errorf("resolve mystery: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("resolve mystery rows: %w", err)
	}
	return n > 0, nil
}

func scanMystery(rows *sql.Rows) (core.Mystery, error) {
	var (
		m          core.Mystery
		detail     []byte
		createdAt  int64
		resolvedAt sql.NullInt64
	)
	if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.App, &m.Metric, &m.Day,
		&m.Observed, &m.Expected, &m.Z, &m.Title, &detail, &m.Status, &m.Note,
		&createdAt, &resolvedAt, &m.DedupeKey); err != nil {
		return m, fmt.Errorf("scan mystery: %w", err)
	}
	if len(detail) > 0 {
		m.Detail = append([]byte(nil), detail...)
	}
	m.CreatedAt = time.UnixMilli(createdAt).UTC()
	if resolvedAt.Valid {
		t := time.UnixMilli(resolvedAt.Int64).UTC()
		m.ResolvedAt = &t
	}
	return m, nil
}
