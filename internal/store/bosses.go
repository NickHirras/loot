package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Boss persistence, plus the crash series the boss detector reads.
//
// Like a mystery, a boss is a *reading* of events that are already stored:
// crash events arrive silent from playvitals, Sentry or the generic crash
// webhook, and everything about the fight — when it spawned, how much HP it
// has left, whether it is dead — is recomputed from them. The row exists so
// the fight has an identity and a memory (its name, its spawn day, the drop it
// paid), never as a second copy of the numbers.

// ErrBossNotFound is returned for an unknown boss id.
var ErrBossNotFound = errors.New("boss not found")

// The crash event kinds, re-exported from core so a query in this file reads
// as SQL against a named kind rather than against a string literal.
const (
	CrashKind         = core.KindCrash
	CrashDayKind      = core.KindCrashDay
	CrashResolvedKind = core.KindCrashResolved
)

// BossSeriesKey identifies one fight: a source, an app, and whichever of
// version and issue that source knows about.
type BossSeriesKey struct {
	Source  string `json:"source"`
	App     string `json:"app"`
	Version string `json:"version"`
	Issue   string `json:"issue"`
}

// Key renders the series key as the boss key stored on the row.
func (k BossSeriesKey) Key() string { return core.BossKey(k.Source, k.App, k.Version, k.Issue) }

// BossDay is one day of one fight.
type BossDay struct {
	Day string
	// Crashes is the day's crash (or error event) count.
	Crashes float64
	// UsersAffected is how many distinct people the day touched, when the
	// source reports it; 0 when it does not.
	UsersAffected int
	// Title, URL and Kind are the newest values the day's rows carried.
	Title string
	URL   string
	Kind  string
}

// crashJSON pulls a payload field out as text. Play reports a version *code*
// as a number and Sentry an issue id as a string, so everything is cast rather
// than trusted to arrive as one type.
func crashJSON(path string) string {
	return `COALESCE(CAST(json_extract(e.payload, '` + path + `') AS TEXT), '')`
}

// CrashSeries returns every fight's daily counts over the inclusive window
// [from, to], each series ordered oldest first. Days with no rows are simply
// absent; the detector densifies, because a day with no crashes is the best
// news a fight can have and must not be skipped.
func (s *Store) CrashSeries(ctx context.Context, from, to string) (map[BossSeriesKey][]BossDay, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app,
               `+crashJSON("$.version")+` AS version,
               `+crashJSON("$.issue_id")+` AS issue,
               e.day,
               COALESCE(SUM(e.quantity), 0),
               COALESCE(MAX(CAST(json_extract(e.payload, '$.users_affected') AS INTEGER)), 0),
               COALESCE(MAX(CAST(json_extract(e.payload, '$.issue_title') AS TEXT)), ''),
               COALESCE(MAX(CAST(json_extract(e.payload, '$.url') AS TEXT)), ''),
               COALESCE(MAX(CAST(json_extract(e.payload, '$.kind') AS TEXT)), '')
        FROM events e
        WHERE e.kind = ? AND e.day BETWEEN ? AND ?
        GROUP BY e.source, e.app, version, issue, e.day
        ORDER BY e.day ASC`, CrashKind, from, to)
	if err != nil {
		return nil, fmt.Errorf("crash series: %w", err)
	}
	defer rows.Close()

	out := map[BossSeriesKey][]BossDay{}
	for rows.Next() {
		var (
			key BossSeriesKey
			day BossDay
		)
		if err := rows.Scan(&key.Source, &key.App, &key.Version, &key.Issue, &day.Day,
			&day.Crashes, &day.UsersAffected, &day.Title, &day.URL, &day.Kind); err != nil {
			return nil, fmt.Errorf("scan crash series: %w", err)
		}
		out[key] = append(out[key], day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crash series: %w", err)
	}
	return out, nil
}

// CrashTotals returns crashes per (source, app) per day: the baseline a spawn
// is measured against. It is deliberately app-wide rather than per fight — a
// brand new issue has no history of its own, and "three times the usual for
// this app" is the question worth asking.
func (s *Store) CrashTotals(ctx context.Context, from, to string) (map[SeriesKey]map[string]float64, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app, e.day, COALESCE(SUM(e.quantity), 0)
        FROM events e
        WHERE e.kind = ? AND e.day BETWEEN ? AND ?
        GROUP BY e.source, e.app, e.day`, CrashKind, from, to)
	if err != nil {
		return nil, fmt.Errorf("crash totals: %w", err)
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
			return nil, fmt.Errorf("scan crash totals: %w", err)
		}
		if out[key] == nil {
			out[key] = map[string]float64{}
		}
		out[key][day] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crash totals: %w", err)
	}
	return out, nil
}

// CrashReportedDays returns, per (source, app), the days on which the source
// said anything at all about crashes — including a `crash_day` heartbeat
// carrying a count of zero.
//
// This is the difference between "no crashes today" and "no data today", and
// the whole boss mechanic turns on it. A polling source that can attest to a
// quiet day (Play vitals) emits the heartbeat, so a fight whose crashes
// genuinely stopped is *slain*. A push-only source (Sentry, the generic
// webhook) cannot attest to silence, so its abandoned fights quietly fade
// instead of claiming a kill nobody earned.
func (s *Store) CrashReportedDays(ctx context.Context, from, to string) (map[SeriesKey][]string, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT DISTINCT e.source, e.app, e.day
        FROM events e
        WHERE e.kind IN (?, ?) AND e.day BETWEEN ? AND ?
        ORDER BY e.day ASC`, CrashKind, CrashDayKind, from, to)
	if err != nil {
		return nil, fmt.Errorf("crash reported days: %w", err)
	}
	defer rows.Close()

	out := map[SeriesKey][]string{}
	for rows.Next() {
		var (
			key SeriesKey
			day string
		)
		if err := rows.Scan(&key.Source, &key.App, &day); err != nil {
			return nil, fmt.Errorf("scan crash reported day: %w", err)
		}
		out[key] = append(out[key], day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crash reported days: %w", err)
	}
	return out, nil
}

// CrashResolutions returns the boss keys somebody marked resolved on or after
// `from`, mapped to the day they were resolved. A Sentry issue closed in
// Sentry is a boss slain in Loot: the fix shipped, and waiting two more days
// for the graph to agree would be pedantry.
func (s *Store) CrashResolutions(ctx context.Context, from string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app,
               `+crashJSON("$.version")+` AS version,
               `+crashJSON("$.issue_id")+` AS issue,
               MAX(e.day)
        FROM events e
        WHERE e.kind = ? AND e.day >= ?
        GROUP BY e.source, e.app, version, issue`, CrashResolvedKind, from)
	if err != nil {
		return nil, fmt.Errorf("crash resolutions: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var key BossSeriesKey
		var day string
		if err := rows.Scan(&key.Source, &key.App, &key.Version, &key.Issue, &day); err != nil {
			return nil, fmt.Errorf("scan crash resolution: %w", err)
		}
		out[key.Key()] = day
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crash resolutions: %w", err)
	}
	return out, nil
}

// ForcedBossKeys returns the keys whose crash rows asked for a boss outright —
// `"boss": true` from the generic crash webhook, which is how a Crashlytics
// velocity alert relays "this one is bad" without Loot having to work it out.
func (s *Store) ForcedBossKeys(ctx context.Context, from string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app,
               `+crashJSON("$.version")+` AS version,
               `+crashJSON("$.issue_id")+` AS issue,
               MIN(e.day)
        FROM events e
        WHERE e.kind = ? AND e.day >= ?
              AND json_extract(e.payload, '$.boss') IN (1, 'true')
        GROUP BY e.source, e.app, version, issue`, CrashKind, from)
	if err != nil {
		return nil, fmt.Errorf("forced boss keys: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var key BossSeriesKey
		var day string
		if err := rows.Scan(&key.Source, &key.App, &key.Version, &key.Issue, &day); err != nil {
			return nil, fmt.Errorf("scan forced boss: %w", err)
		}
		out[key.Key()] = day
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate forced bosses: %w", err)
	}
	return out, nil
}

// ------------------------------------------------------------------- storage

const bossColumns = `id, key, source, app, name, title, version, issue_id,
       hp_max, hp, users_affected, spawned_at, spawned_day, last_hit_at,
       status, slain_at, peak_day, detail, xp_awarded`

// InsertBoss stores a newly spawned boss, ignoring one whose key already has a
// row. created is false in that case, which is what lets the detector re-read
// the same fortnight every hour without spawning the same monster twice.
func (s *Store) InsertBoss(ctx context.Context, b core.Boss) (created bool, err error) {
	if b.ID == "" {
		return false, errors.New("boss id is required")
	}
	if b.Key == "" {
		return false, errors.New("boss key is required")
	}
	if b.Status == "" {
		b.Status = core.BossAlive
	}
	if b.SpawnedAt.IsZero() {
		b.SpawnedAt = time.Now().UTC()
	}
	detail := []byte(b.Detail)
	if len(detail) == 0 {
		detail = nil
	}

	res, err := s.q.ExecContext(ctx, `
        INSERT INTO bosses (`+bossColumns+`)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(key) DO NOTHING`,
		b.ID, b.Key, b.Source, b.App, b.Name, b.Title, b.Version, b.IssueID,
		b.HPMax, b.HP, b.UsersAffected, b.SpawnedAt.UTC().UnixMilli(), b.SpawnedDay,
		nil, b.Status, nil, b.PeakDay, detail, b.XPAwarded)
	if err != nil {
		return false, fmt.Errorf("insert boss: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert boss rows: %w", err)
	}
	return n > 0, nil
}

// UpdateBossFight writes one evaluation's reading of a live fight: the current
// HP, the high-water mark, the peak day and the refreshed detail blob. It only
// touches a boss that is still alive, so a slay that landed a millisecond
// earlier cannot be undone by a pass that started before it.
func (s *Store) UpdateBossFight(ctx context.Context, b core.Boss, at time.Time) error {
	detail := []byte(b.Detail)
	if len(detail) == 0 {
		detail = nil
	}
	_, err := s.q.ExecContext(ctx, `
        UPDATE bosses
           SET hp = ?, hp_max = ?, users_affected = ?, peak_day = ?, detail = ?, last_hit_at = ?
         WHERE id = ? AND status = ?`,
		round2(b.HP), round2(b.HPMax), b.UsersAffected, b.PeakDay, detail,
		at.UTC().UnixMilli(), b.ID, core.BossAlive)
	if err != nil {
		return fmt.Errorf("update boss fight: %w", err)
	}
	return nil
}

// CloseBoss ends a fight, but only if it is still alive — the UPDATE's own
// WHERE clause is what guarantees a boss can mint exactly one slain drop even
// if a manual click races the hourly sweep.
func (s *Store) CloseBoss(ctx context.Context, id, status string, hp float64, detail []byte, at time.Time) (bool, error) {
	if len(detail) == 0 {
		detail = nil
	}
	var slainAt any
	if status == core.BossSlain {
		slainAt = at.UTC().UnixMilli()
	}
	res, err := s.q.ExecContext(ctx, `
        UPDATE bosses SET status = ?, hp = ?, detail = COALESCE(?, detail), slain_at = ?
         WHERE id = ? AND status = ?`,
		status, round2(hp), detail, slainAt, id, core.BossAlive)
	if err != nil {
		return false, fmt.Errorf("close boss: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("close boss rows: %w", err)
	}
	return n > 0, nil
}

// SetBossXP records what the slain drop paid.
func (s *Store) SetBossXP(ctx context.Context, id string, xp int) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE bosses SET xp_awarded = ? WHERE id = ?`, xp, id); err != nil {
		return fmt.Errorf("set boss xp: %w", err)
	}
	return nil
}

// BossQuery parameterizes ListBosses.
type BossQuery struct {
	// Statuses limits the result; empty means every status.
	Statuses []string
	// Limit caps the result, 0 meaning "no cap".
	Limit int
	// Newest orders by when the fight ended (or began); the default orders
	// live fights by the biggest one first, which is the one worth fixing.
	Newest bool
}

// ListBosses returns bosses matching q.
func (s *Store) ListBosses(ctx context.Context, q BossQuery) ([]core.Boss, error) {
	query := `SELECT ` + bossColumns + ` FROM bosses`
	var args []any
	if len(q.Statuses) > 0 {
		query += ` WHERE status IN (` + placeholders(len(q.Statuses)) + `)`
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	if q.Newest {
		query += ` ORDER BY COALESCE(slain_at, spawned_at) DESC, id DESC`
	} else {
		query += ` ORDER BY hp DESC, spawned_at ASC, id ASC`
	}
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bosses: %w", err)
	}
	defer rows.Close()

	out := []core.Boss{}
	for rows.Next() {
		b, err := scanBoss(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bosses: %w", err)
	}
	return out, nil
}

// GetBoss returns one boss by id, or ErrBossNotFound.
func (s *Store) GetBoss(ctx context.Context, id string) (core.Boss, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+bossColumns+` FROM bosses WHERE id = ?`, id)
	if err != nil {
		return core.Boss{}, fmt.Errorf("get boss: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return core.Boss{}, fmt.Errorf("get boss: %w", err)
		}
		return core.Boss{}, ErrBossNotFound
	}
	return scanBoss(rows)
}

// CountAliveBosses is the number behind the Quests tab's red badge.
func (s *Store) CountAliveBosses(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bosses WHERE status = ?`, core.BossAlive).Scan(&n); err != nil {
		return 0, fmt.Errorf("count alive bosses: %w", err)
	}
	return n, nil
}

func scanBoss(rows *sql.Rows) (core.Boss, error) {
	var (
		b         core.Boss
		detail    []byte
		spawnedAt int64
		lastHitAt sql.NullInt64
		slainAt   sql.NullInt64
	)
	if err := rows.Scan(&b.ID, &b.Key, &b.Source, &b.App, &b.Name, &b.Title, &b.Version,
		&b.IssueID, &b.HPMax, &b.HP, &b.UsersAffected, &spawnedAt, &b.SpawnedDay,
		&lastHitAt, &b.Status, &slainAt, &b.PeakDay, &detail, &b.XPAwarded); err != nil {
		return b, fmt.Errorf("scan boss: %w", err)
	}
	if len(detail) > 0 {
		b.Detail = append([]byte(nil), detail...)
	}
	b.SpawnedAt = time.UnixMilli(spawnedAt).UTC()
	if lastHitAt.Valid {
		t := time.UnixMilli(lastHitAt.Int64).UTC()
		b.LastHitAt = &t
	}
	if slainAt.Valid {
		t := time.UnixMilli(slainAt.Int64).UTC()
		b.SlainAt = &t
	}
	return b, nil
}

// SortBossDays orders one fight's days oldest first. The query already does,
// but a caller assembling a series from several sources should not have to
// know that.
func SortBossDays(days []BossDay) {
	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })
}
