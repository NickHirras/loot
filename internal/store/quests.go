package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Quest persistence and, more interestingly, quest *progress*: every metric a
// quest can count is one aggregate over the events and drops already stored,
// so a quest never needs its own bookkeeping and can never drift out of step
// with the vault. Progress is recomputed, cached on the row, and that cache is
// only ever a rendering convenience.

// ErrQuestNotFound is returned by GetQuest and DeleteQuest for an unknown id.
var ErrQuestNotFound = errors.New("quest not found")

// installValue is the day-level install count for one (source, app, day),
// resolving the double-counting problem that sources with both an overview row
// and per-country rows would otherwise cause.
//
// Google Play reports installs twice: once as an overview row for the whole
// app and once per country. Flathub reports one row per app per day with no
// country at all. Summing everything would count Play twice; summing only
// country rows would lose Flathub. So: if a (source, app, day) has any row
// without a country, those rows *are* the day; otherwise the country rows are
// added up.
const installValue = `
CASE WHEN COUNT(CASE WHEN e.country = '' THEN 1 END) > 0
     THEN COALESCE(SUM(CASE WHEN e.country =  '' THEN e.quantity ELSE 0 END), 0)
     ELSE COALESCE(SUM(CASE WHEN e.country <> '' THEN e.quantity ELSE 0 END), 0)
END`

// questFilters renders the optional app/source narrowing of a quest onto an
// events alias, returning the SQL fragment and its arguments.
func questFilters(app, source string) (string, []any) {
	var (
		sb   strings.Builder
		args []any
	)
	if app != "" {
		sb.WriteString(" AND e.app = ?")
		args = append(args, app)
	}
	if source != "" {
		sb.WriteString(" AND e.source = ?")
		args = append(args, source)
	}
	return sb.String(), args
}

// QuestProgress measures a quest's metric over its own window. It is the whole
// of "how am I doing?", and it is deliberately a fresh read every time: the
// value cached on the row is for rendering, never for arithmetic.
func (s *Store) QuestProgress(ctx context.Context, q core.Quest) (float64, error) {
	where, args := questFilters(q.App, q.Source)
	window := []any{q.WindowStart, q.WindowEnd}

	var (
		query string
		bind  []any
	)
	switch q.Metric {
	case core.MetricRevenue:
		query = `SELECT COALESCE(SUM(e.amount_base), 0) FROM events e
                 WHERE ` + ledgerRows + ` AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	case core.MetricUnits:
		query = `SELECT COALESCE(SUM(CASE WHEN ` + isPaidUnit + ` THEN e.quantity ELSE 0 END), 0)
                 FROM events e
                 WHERE ` + ledgerRows + ` AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	case core.MetricInstalls:
		query = `SELECT COALESCE(SUM(v), 0) FROM (
                     SELECT ` + installValue + ` AS v FROM events e
                     WHERE e.kind = 'install' AND e.day BETWEEN ? AND ?` + where + `
                     GROUP BY e.source, e.app, e.day)`
		bind = append(window, args...)

	case core.MetricSubscribers:
		// A snapshot is a level, not a flow: the newest one inside the window
		// per (source, app) is the answer, and the apps add up.
		query = `SELECT COALESCE(SUM(e.quantity), 0) FROM events e
                 JOIN (
                     SELECT source, app, MAX(day) AS day FROM events e
                     WHERE e.kind = 'subscription_snapshot' AND e.day BETWEEN ? AND ?` + where + `
                     GROUP BY source, app
                 ) latest ON latest.source = e.source AND latest.app = e.app AND latest.day = e.day
                 WHERE e.kind = 'subscription_snapshot'`
		bind = append(window, args...)

	case core.MetricDrops:
		query = `SELECT COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id
                 WHERE NOT ` + unrevealed + ` AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	case core.MetricSettlements:
		query = `SELECT COUNT(*) FROM events e
                 WHERE e.kind = 'settlement' AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	case core.MetricStars:
		// A star event usually carries no quantity; one row is one star.
		query = `SELECT COALESCE(SUM(CASE WHEN e.quantity > 0 THEN e.quantity ELSE 1 END), 0)
                 FROM events e
                 WHERE e.kind = 'star' AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	case core.MetricXP:
		query = `SELECT COALESCE(SUM(d.xp), 0) FROM drops d JOIN events e ON e.id = d.event_id
                 WHERE NOT ` + unrevealed + ` AND e.day BETWEEN ? AND ?` + where
		bind = append(window, args...)

	default:
		return 0, fmt.Errorf("unknown quest metric %q", q.Metric)
	}

	var value sql.NullFloat64
	if err := s.q.QueryRowContext(ctx, query, bind...).Scan(&value); err != nil {
		return 0, fmt.Errorf("quest progress %s: %w", q.Metric, err)
	}
	return round2(value.Float64), nil
}

// MetricsWithData reports which metrics this database has any history for, so
// the generator never invents a revenue quest for somebody who has only ever
// had installs. The answers are cheap existence checks, not counts.
func (s *Store) MetricsWithData(ctx context.Context) (map[core.Metric]bool, error) {
	checks := map[core.Metric]string{
		core.MetricRevenue:     `SELECT EXISTS (SELECT 1 FROM events e WHERE ` + ledgerRows + ` AND e.amount_base <> 0)`,
		core.MetricUnits:       `SELECT EXISTS (SELECT 1 FROM events e WHERE ` + ledgerRows + ` AND ` + isPaidUnit + ` AND e.quantity > 0)`,
		core.MetricInstalls:    `SELECT EXISTS (SELECT 1 FROM events WHERE kind = 'install')`,
		core.MetricSubscribers: `SELECT EXISTS (SELECT 1 FROM events WHERE kind = 'subscription_snapshot')`,
		core.MetricDrops:       `SELECT EXISTS (SELECT 1 FROM drops)`,
		core.MetricSettlements: `SELECT EXISTS (SELECT 1 FROM events WHERE kind = 'settlement')`,
		core.MetricStars:       `SELECT EXISTS (SELECT 1 FROM events WHERE kind = 'star')`,
		core.MetricXP:          `SELECT EXISTS (SELECT 1 FROM drops WHERE xp > 0)`,
	}

	out := make(map[core.Metric]bool, len(checks))
	for _, metric := range core.Metrics {
		var found int
		if err := s.q.QueryRowContext(ctx, checks[metric]).Scan(&found); err != nil {
			return nil, fmt.Errorf("metrics with data %s: %w", metric, err)
		}
		out[metric] = found == 1
	}
	return out, nil
}

const questColumns = `id, kind, metric, target, app, source, window_start, window_end,
       title, status, value, updated_at, xp, created_at, completed_at, dedupe_key`

// InsertQuest stores a quest. A quest whose dedupe key is already present is
// left alone and created is false — which is what makes running the generator
// at every startup free.
func (s *Store) InsertQuest(ctx context.Context, q core.Quest) (created bool, err error) {
	if q.ID == "" {
		return false, errors.New("quest id is required")
	}
	if q.DedupeKey == "" {
		return false, errors.New("quest dedupe key is required")
	}
	if q.Status == "" {
		q.Status = core.QuestActive
	}
	if q.CreatedAt.IsZero() {
		q.CreatedAt = time.Now().UTC()
	}

	res, err := s.q.ExecContext(ctx, `
        INSERT INTO quests (`+questColumns+`)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(dedupe_key) DO NOTHING`,
		q.ID, q.Kind, string(q.Metric), q.Target, q.App, q.Source,
		q.WindowStart, q.WindowEnd, q.Title, q.Status, q.Value, nil, q.XP,
		q.CreatedAt.UTC().UnixMilli(), nil, q.DedupeKey)
	if err != nil {
		return false, fmt.Errorf("insert quest: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert quest rows: %w", err)
	}
	return n > 0, nil
}

// QuestQuery parameterizes ListQuests.
type QuestQuery struct {
	// Statuses limits the result; empty means every status.
	Statuses []string
	// Limit caps the result, 0 meaning "no cap".
	Limit int
	// Newest orders by creation time descending (the "recent" list); the
	// default orders active quests by the window that closes soonest.
	Newest bool
}

// ListQuests returns quests matching q.
func (s *Store) ListQuests(ctx context.Context, q QuestQuery) ([]core.Quest, error) {
	query := `SELECT ` + questColumns + ` FROM quests`
	var args []any
	if len(q.Statuses) > 0 {
		query += ` WHERE status IN (` + placeholders(len(q.Statuses)) + `)`
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	if q.Newest {
		query += ` ORDER BY COALESCE(completed_at, created_at) DESC, id DESC`
	} else {
		query += ` ORDER BY window_end ASC, created_at ASC, id ASC`
	}
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list quests: %w", err)
	}
	defer rows.Close()

	out := []core.Quest{}
	for rows.Next() {
		q, err := scanQuest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quests: %w", err)
	}
	return out, nil
}

// GetQuest returns one quest by id, or ErrQuestNotFound.
func (s *Store) GetQuest(ctx context.Context, id string) (core.Quest, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+questColumns+` FROM quests WHERE id = ?`, id)
	if err != nil {
		return core.Quest{}, fmt.Errorf("get quest: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return core.Quest{}, fmt.Errorf("get quest: %w", err)
		}
		return core.Quest{}, ErrQuestNotFound
	}
	return scanQuest(rows)
}

// CountActiveQuests counts active quests of a kind ("" for every kind). The
// generator uses it to keep the board from becoming a to-do list.
func (s *Store) CountActiveQuests(ctx context.Context, kind string) (int, error) {
	var (
		n   int
		err error
	)
	if kind == "" {
		err = s.q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM quests WHERE status = ?`, core.QuestActive).Scan(&n)
	} else {
		err = s.q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM quests WHERE status = ? AND kind = ?`, core.QuestActive, kind).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("count active quests: %w", err)
	}
	return n, nil
}

// SaveQuestProgress caches a freshly measured value on the row.
func (s *Store) SaveQuestProgress(ctx context.Context, id string, value float64, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE quests SET value = ?, updated_at = ? WHERE id = ?`,
		round2(value), at.UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("save quest progress: %w", err)
	}
	return nil
}

// CompleteQuest marks a quest completed, but only if it is still active — the
// UPDATE's own WHERE clause is what guarantees a quest can mint exactly one
// completion drop even if two checks race.
func (s *Store) CompleteQuest(ctx context.Context, id string, value float64, at time.Time) (bool, error) {
	res, err := s.q.ExecContext(ctx, `
        UPDATE quests SET status = ?, completed_at = ?, value = ?, updated_at = ?
        WHERE id = ? AND status = ?`,
		core.QuestCompleted, at.UTC().UnixMilli(), round2(value), at.UTC().UnixMilli(),
		id, core.QuestActive)
	if err != nil {
		return false, fmt.Errorf("complete quest: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("complete quest rows: %w", err)
	}
	return n > 0, nil
}

// SetQuestXP records what the completion drop paid.
func (s *Store) SetQuestXP(ctx context.Context, id string, xp int) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE quests SET xp = ? WHERE id = ?`, xp, id); err != nil {
		return fmt.Errorf("set quest xp: %w", err)
	}
	return nil
}

// ExpireQuests retires every active quest whose window ended before today.
// Expiry is silent on purpose: no event, no drop, no sound. An unmet quest
// simply becomes a line in history reading "ended · 62%".
func (s *Store) ExpireQuests(ctx context.Context, today string, at time.Time) ([]core.Quest, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT `+questColumns+` FROM quests WHERE status = ? AND window_end < ?`,
		core.QuestActive, today)
	if err != nil {
		return nil, fmt.Errorf("expire quests: %w", err)
	}
	defer rows.Close()

	var due []core.Quest
	for rows.Next() {
		q, err := scanQuest(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expiring quests: %w", err)
	}
	if len(due) == 0 {
		return nil, nil
	}

	if _, err := s.q.ExecContext(ctx,
		`UPDATE quests SET status = ?, updated_at = ? WHERE status = ? AND window_end < ?`,
		core.QuestExpired, at.UTC().UnixMilli(), core.QuestActive, today); err != nil {
		return nil, fmt.Errorf("expire quests update: %w", err)
	}
	for i := range due {
		due[i].Status = core.QuestExpired
	}
	return due, nil
}

// DeleteQuest removes a quest by id. It returns ErrQuestNotFound for an
// unknown id; the caller decides whether the quest was deletable.
func (s *Store) DeleteQuest(ctx context.Context, id string) error {
	res, err := s.q.ExecContext(ctx, `DELETE FROM quests WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete quest: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete quest rows: %w", err)
	}
	if n == 0 {
		return ErrQuestNotFound
	}
	return nil
}

// scanQuest reads one row of questColumns.
func scanQuest(rows *sql.Rows) (core.Quest, error) {
	var (
		q           core.Quest
		metric      string
		updatedAt   sql.NullInt64
		createdAt   int64
		completedAt sql.NullInt64
	)
	if err := rows.Scan(&q.ID, &q.Kind, &metric, &q.Target, &q.App, &q.Source,
		&q.WindowStart, &q.WindowEnd, &q.Title, &q.Status, &q.Value, &updatedAt, &q.XP,
		&createdAt, &completedAt, &q.DedupeKey); err != nil {
		return q, fmt.Errorf("scan quest: %w", err)
	}
	q.Metric = core.Metric(metric)
	q.CreatedAt = time.UnixMilli(createdAt).UTC()
	if updatedAt.Valid {
		t := time.UnixMilli(updatedAt.Int64).UTC()
		q.UpdatedAt = &t
	}
	if completedAt.Valid {
		t := time.UnixMilli(completedAt.Int64).UTC()
		q.CompletedAt = &t
	}
	q.Pct = core.QuestPct(q.Value, q.Target)
	return q, nil
}

// placeholders renders "?, ?, ?" for an IN clause of n values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
