// Package store owns persistence: schema migrations plus a small repository
// over events, drops and per-source cursor state. It is backed by SQLite
// through modernc.org/sqlite, a pure-Go driver, so Loot builds without cgo.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nickhirras/loot/internal/core"
)

// Store is the repository. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies all
// pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite tolerates exactly one writer. Serializing at the pool keeps the
	// ingest path free of SQLITE_BUSY retries at the cost of write throughput
	// we do not need.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for tests and ad-hoc queries.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        name       TEXT PRIMARY KEY,
        applied_at INTEGER NOT NULL
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, m := range migrations {
		var seen int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, m.Name).Scan(&seen)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", m.Name, err)
		}
		if seen > 0 {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, m.Name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.Name, err)
		}
	}
	return nil
}

// InsertEvent persists ev. If an event with the same dedupe_key is already
// stored, nothing is written and exists is true — the caller must then skip
// drop creation, which is what keeps webhook retries from double-dropping.
func (s *Store) InsertEvent(ctx context.Context, ev core.Event) (exists bool, err error) {
	if ev.ID == "" {
		return false, errors.New("event id is required")
	}
	if ev.DedupeKey == "" {
		return false, errors.New("event dedupe key is required")
	}

	payload := []byte(ev.Payload)
	if len(payload) == 0 {
		payload = nil
	}

	day := ev.Day
	if day == "" {
		day = core.DayOf(ev.OccurredAt)
	}

	res, err := s.db.ExecContext(ctx, `
        INSERT INTO events (id, source, kind, app, occurred_at, observed_at, day, country,
                            amount, currency, amount_base, quantity, dedupe_key,
                            is_ledger, silent, chest, payload)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(dedupe_key) DO NOTHING`,
		ev.ID, ev.Source, ev.Kind, ev.App,
		ev.OccurredAt.UTC().UnixMilli(), ev.ObservedAt.UTC().UnixMilli(), day,
		strings.ToUpper(ev.Country), ev.Amount, ev.Currency, ev.AmountBase, ev.Quantity,
		ev.DedupeKey, ev.IsLedger, ev.Silent, ev.Chest, payload,
	)
	if err != nil {
		return false, fmt.Errorf("insert event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert event rows: %w", err)
	}
	return n == 0, nil
}

// InsertDrop persists a classified drop. A drop with a ChestDate and no
// RevealedAt is invisible to the feed until its chest is opened.
func (s *Store) InsertDrop(ctx context.Context, d core.Drop) error {
	var revealed any
	if d.RevealedAt != nil {
		revealed = d.RevealedAt.UTC().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO drops (id, event_id, rarity, title, subtitle, xp, created_at, chest_date, revealed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.EventID, string(d.Rarity), d.Title, d.Subtitle, d.XP,
		d.CreatedAt.UTC().UnixMilli(), d.ChestDate, revealed)
	if err != nil {
		return fmt.Errorf("insert drop: %w", err)
	}
	return nil
}

// CountryEventCount returns how many stored events carry the given country.
// The rules engine calls this after insert, so a result of 1 means "this event
// is the first ever from that country".
func (s *Store) CountryEventCount(ctx context.Context, country string) (int, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE country = ?`, country).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("country event count: %w", err)
	}
	return n, nil
}

// IsRecordQuantity reports whether ev has the highest quantity ever seen for
// its (source, app, kind) triple. Used for "best day ever" style rules. The
// event itself is excluded by id, so ties with earlier days are not records.
func (s *Store) IsRecordQuantity(ctx context.Context, ev core.Event) (bool, error) {
	if ev.Quantity <= 0 {
		return false, nil
	}
	var best sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
        SELECT MAX(quantity) FROM events
        WHERE source = ? AND app = ? AND kind = ? AND id <> ?`,
		ev.Source, ev.App, ev.Kind, ev.ID).Scan(&best)
	if err != nil {
		return false, fmt.Errorf("record quantity: %w", err)
	}
	if !best.Valid {
		// No prior history at all: the first day is not a "record", it is just
		// the first day. Avoids an epic drop on every source's very first poll.
		return false, nil
	}
	return int64(ev.Quantity) > best.Int64, nil
}

// EventCount returns the number of stored events for a source ("" = all).
func (s *Store) EventCount(ctx context.Context, source string) (int, error) {
	var (
		n   int
		err error
	)
	if source == "" {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE source = ?`, source).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("event count: %w", err)
	}
	return n, nil
}

// DropView is a drop joined with the event fields the feed needs to render.
type DropView struct {
	core.Drop
	Source     string          `json:"source"`
	Kind       string          `json:"kind"`
	App        string          `json:"app"`
	Country    string          `json:"country"`
	Amount     float64         `json:"amount"`
	AmountBase float64         `json:"amount_base"`
	Currency   string          `json:"currency"`
	Quantity   int             `json:"quantity"`
	Day        string          `json:"day"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Event reconstructs the originating event from the joined columns, which is
// what the bus needs when replaying a revealed chest drop. Payload is not
// selected by the feed queries, so it is absent here.
func (v DropView) Event() core.Event {
	return core.Event{
		ID:         v.EventID,
		Source:     v.Source,
		Kind:       v.Kind,
		App:        v.App,
		OccurredAt: v.OccurredAt,
		Day:        v.Day,
		Country:    v.Country,
		Amount:     v.Amount,
		AmountBase: v.AmountBase,
		Currency:   v.Currency,
		Quantity:   v.Quantity,
	}
}

const dropSelect = `
SELECT d.id, d.event_id, d.rarity, d.title, d.subtitle, d.xp, d.created_at,
       d.chest_date, d.revealed_at,
       e.source, e.kind, e.app, e.country, e.amount, e.amount_base, e.currency,
       e.quantity, e.day, e.occurred_at
FROM drops d
JOIN events e ON e.id = d.event_id`

// unrevealed matches drops that are waiting inside an unopened chest. An
// immediate drop has no chest_date at all, so it is always visible.
const unrevealed = `(d.chest_date <> '' AND d.revealed_at IS NULL)`

// DropQuery parameterizes ListDrops.
type DropQuery struct {
	// Limit caps the page; 0 means 100, and anything over 500 is clamped.
	Limit int
	// Before returns only drops older than this drop id (ULIDs sort by time).
	Before string
	// IncludeUnrevealed lets drops still sitting in an unopened chest through.
	// The feed leaves it false: an unopened chest must not spoil itself.
	IncludeUnrevealed bool
}

// ListDrops returns the most recent drops, newest first. Drops held in an
// unopened chest are excluded unless q.IncludeUnrevealed is set.
func (s *Store) ListDrops(ctx context.Context, q DropQuery) ([]DropView, error) {
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	where := []string{}
	args := []any{}
	if q.Before != "" {
		where = append(where, `d.id < ?`)
		args = append(args, q.Before)
	}
	if !q.IncludeUnrevealed {
		where = append(where, `NOT `+unrevealed)
	}

	query := dropSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY d.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list drops: %w", err)
	}
	defer rows.Close()

	out, err := scanDrops(rows, limit)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanDrops(rows *sql.Rows, capacity int) ([]DropView, error) {
	out := make([]DropView, 0, capacity)
	for rows.Next() {
		var (
			v          DropView
			rarity     string
			createdAt  int64
			revealedAt sql.NullInt64
			occurredAt int64
		)
		if err := rows.Scan(&v.ID, &v.EventID, &rarity, &v.Title, &v.Subtitle, &v.XP, &createdAt,
			&v.ChestDate, &revealedAt,
			&v.Source, &v.Kind, &v.App, &v.Country, &v.Amount, &v.AmountBase, &v.Currency,
			&v.Quantity, &v.Day, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan drop: %w", err)
		}
		v.Rarity = core.Rarity(rarity)
		v.CreatedAt = time.UnixMilli(createdAt).UTC()
		if revealedAt.Valid {
			t := time.UnixMilli(revealedAt.Int64).UTC()
			v.RevealedAt = &t
		}
		v.OccurredAt = time.UnixMilli(occurredAt).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate drops: %w", err)
	}
	return out, nil
}

// Chest is one day's worth of held drops.
type Chest struct {
	Date     string         `json:"date"`
	Count    int            `json:"count"`
	XP       int            `json:"xp"`
	ByRarity map[string]int `json:"by_rarity"`
	// Drops is populated by ListChest and left empty by ChestSummaries.
	Drops []DropView `json:"drops,omitempty"`
}

// ChestSummaries returns one row per unopened chest, oldest day first. This is
// the cheap query behind the UI badge and the `chest` bus message.
func (s *Store) ChestSummaries(ctx context.Context) ([]core.ChestSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT chest_date, rarity, COUNT(*), COALESCE(SUM(xp), 0)
        FROM drops d
        WHERE `+unrevealed+`
        GROUP BY chest_date, rarity
        ORDER BY chest_date ASC`)
	if err != nil {
		return nil, fmt.Errorf("chest summaries: %w", err)
	}
	defer rows.Close()

	var (
		out   []core.ChestSummary
		index = map[string]int{}
	)
	for rows.Next() {
		var (
			date, rarity string
			n, xp        int
		)
		if err := rows.Scan(&date, &rarity, &n, &xp); err != nil {
			return nil, fmt.Errorf("scan chest summary: %w", err)
		}
		i, ok := index[date]
		if !ok {
			out = append(out, core.ChestSummary{Date: date, ByRarity: map[string]int{}})
			i = len(out) - 1
			index[date] = i
		}
		out[i].Count += n
		out[i].XP += xp
		out[i].ByRarity[rarity] += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chest summaries: %w", err)
	}
	return out, nil
}

// ListChest returns every unopened chest with its drops, oldest day first.
// Drops within a chest come back in reveal order (see core.RevealRank).
func (s *Store) ListChest(ctx context.Context) ([]Chest, error) {
	rows, err := s.db.QueryContext(ctx, dropSelect+`
        WHERE `+unrevealed+`
        ORDER BY d.chest_date ASC, `+revealOrder+`, d.created_at ASC, d.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list chest: %w", err)
	}
	defer rows.Close()

	drops, err := scanDrops(rows, 32)
	if err != nil {
		return nil, err
	}

	var (
		out   []Chest
		index = map[string]int{}
	)
	for _, d := range drops {
		i, ok := index[d.ChestDate]
		if !ok {
			out = append(out, Chest{Date: d.ChestDate, ByRarity: map[string]int{}})
			i = len(out) - 1
			index[d.ChestDate] = i
		}
		c := &out[i]
		c.Count++
		c.XP += d.XP
		c.ByRarity[string(d.Rarity)]++
		c.Drops = append(c.Drops, d)
	}
	return out, nil
}

// revealOrder sorts a chest cascade so it builds towards the best news. It
// mirrors core.RevealRank, which cannot be expressed in SQL directly.
const revealOrder = `CASE d.rarity
    WHEN 'cursed'    THEN 0
    WHEN 'common'    THEN 1
    WHEN 'uncommon'  THEN 2
    WHEN 'rare'      THEN 3
    WHEN 'epic'      THEN 4
    WHEN 'legendary' THEN 5
    ELSE 1 END`

// OldestChestDate returns the day of the oldest unopened chest, or "".
func (s *Store) OldestChestDate(ctx context.Context) (string, error) {
	var date sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(chest_date) FROM drops d WHERE `+unrevealed).Scan(&date)
	if err != nil {
		return "", fmt.Errorf("oldest chest date: %w", err)
	}
	return date.String, nil
}

// RevealChest opens the chest for chestDate (or the oldest unopened chest when
// chestDate is empty), stamps revealed_at on its drops and returns them in
// cascade order: least exciting first, so the reveal climaxes. Opening an
// already-open or unknown chest returns no drops and no error.
func (s *Store) RevealChest(ctx context.Context, chestDate string, now time.Time) ([]DropView, error) {
	if chestDate == "" {
		oldest, err := s.OldestChestDate(ctx)
		if err != nil {
			return nil, err
		}
		if oldest == "" {
			return nil, nil
		}
		chestDate = oldest
	}

	// Select before update: once revealed_at is stamped the rows no longer
	// match "unrevealed", and we still need to hand them back in order.
	rows, err := s.db.QueryContext(ctx, dropSelect+`
        WHERE `+unrevealed+` AND d.chest_date = ?
        ORDER BY `+revealOrder+`, d.created_at ASC, d.id ASC`, chestDate)
	if err != nil {
		return nil, fmt.Errorf("reveal chest: %w", err)
	}
	drops, err := scanDrops(rows, 32)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(drops) == 0 {
		return nil, nil
	}

	stamp := now.UTC()
	if _, err := s.db.ExecContext(ctx, `
        UPDATE drops SET revealed_at = ?
        WHERE chest_date = ? AND revealed_at IS NULL`, stamp.UnixMilli(), chestDate); err != nil {
		return nil, fmt.Errorf("mark chest revealed: %w", err)
	}
	for i := range drops {
		t := stamp
		drops[i].RevealedAt = &t
	}
	return drops, nil
}

// Stats is the aggregate shown in the dashboard header. Every drop count
// excludes drops still held in an unopened chest — an unopened chest must not
// leak its contents through the XP counter.
type Stats struct {
	TotalDrops     int            `json:"total_drops"`
	TotalEvents    int            `json:"total_events"`
	TotalXP        int            `json:"total_xp"`
	ByRarity       map[string]int `json:"by_rarity"`
	BySource       map[string]int `json:"by_source"`
	Countries      []string       `json:"countries"`
	CountriesCount int            `json:"countries_count"`
	// UnrevealedCount is how many drops are waiting in chests, and ChestDates
	// which days those chests are for (oldest first).
	UnrevealedCount int      `json:"unrevealed_count"`
	ChestDates      []string `json:"chest_dates"`
}

// Stats computes dashboard aggregates in a handful of grouped queries.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{
		ByRarity:   map[string]int{},
		BySource:   map[string]int{},
		Countries:  []string{},
		ChestDates: []string{},
	}
	for _, r := range core.Rarities {
		st.ByRarity[string(r)] = 0
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(xp), 0) FROM drops d WHERE NOT `+unrevealed).
		Scan(&st.TotalDrops, &st.TotalXP); err != nil {
		return st, fmt.Errorf("stats totals: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&st.TotalEvents); err != nil {
		return st, fmt.Errorf("stats events: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM drops d WHERE `+unrevealed).Scan(&st.UnrevealedCount); err != nil {
		return st, fmt.Errorf("stats unrevealed: %w", err)
	}

	rarityRows, err := s.db.QueryContext(ctx,
		`SELECT rarity, COUNT(*) FROM drops d WHERE NOT `+unrevealed+` GROUP BY rarity`)
	if err != nil {
		return st, fmt.Errorf("stats rarity: %w", err)
	}
	defer rarityRows.Close()
	for rarityRows.Next() {
		var r string
		var n int
		if err := rarityRows.Scan(&r, &n); err != nil {
			return st, fmt.Errorf("scan rarity: %w", err)
		}
		st.ByRarity[r] = n
	}
	if err := rarityRows.Err(); err != nil {
		return st, fmt.Errorf("iterate rarity: %w", err)
	}

	sourceRows, err := s.db.QueryContext(ctx, `
        SELECT e.source, COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` GROUP BY e.source`)
	if err != nil {
		return st, fmt.Errorf("stats source: %w", err)
	}
	defer sourceRows.Close()
	for sourceRows.Next() {
		var src string
		var n int
		if err := sourceRows.Scan(&src, &n); err != nil {
			return st, fmt.Errorf("scan source: %w", err)
		}
		st.BySource[src] = n
	}
	if err := sourceRows.Err(); err != nil {
		return st, fmt.Errorf("iterate source: %w", err)
	}

	countryRows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT country FROM events WHERE country <> '' ORDER BY country`)
	if err != nil {
		return st, fmt.Errorf("stats countries: %w", err)
	}
	defer countryRows.Close()
	for countryRows.Next() {
		var c string
		if err := countryRows.Scan(&c); err != nil {
			return st, fmt.Errorf("scan country: %w", err)
		}
		st.Countries = append(st.Countries, c)
	}
	if err := countryRows.Err(); err != nil {
		return st, fmt.Errorf("iterate countries: %w", err)
	}
	st.CountriesCount = len(st.Countries)

	chests, err := s.ChestSummaries(ctx)
	if err != nil {
		return st, err
	}
	for _, c := range chests {
		st.ChestDates = append(st.ChestDates, c.Date)
	}

	return st, nil
}

// SourceState is the persisted cursor and health of one source.
type SourceState struct {
	Source     string     `json:"source"`
	State      []byte     `json:"-"`
	LastPollAt *time.Time `json:"last_poll_at"`
	LastError  string     `json:"last_error"`
}

// GetSourceState returns the stored cursor blob for a source. A source that has
// never run returns a nil blob and no error.
func (s *Store) GetSourceState(ctx context.Context, source string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT state FROM source_state WHERE source = ?`, source).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get source state: %w", err)
	}
	return blob, nil
}

// SetSourceState stores the cursor blob for a source.
func (s *Store) SetSourceState(ctx context.Context, source string, state []byte) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO source_state (source, state) VALUES (?, ?)
        ON CONFLICT(source) DO UPDATE SET state = excluded.state`, source, state)
	if err != nil {
		return fmt.Errorf("set source state: %w", err)
	}
	return nil
}

// RecordPoll stamps the last poll time and error text for a source. Passing a
// nil pollErr clears any previous error.
func (s *Store) RecordPoll(ctx context.Context, source string, at time.Time, pollErr error) error {
	msg := ""
	if pollErr != nil {
		msg = pollErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO source_state (source, last_poll_at, last_error) VALUES (?, ?, ?)
        ON CONFLICT(source) DO UPDATE SET last_poll_at = excluded.last_poll_at, last_error = excluded.last_error`,
		source, at.UTC().UnixMilli(), msg)
	if err != nil {
		return fmt.Errorf("record poll: %w", err)
	}
	return nil
}

// SourceStates returns health rows for every source that has state.
func (s *Store) SourceStates(ctx context.Context) (map[string]SourceState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, last_poll_at, last_error FROM source_state`)
	if err != nil {
		return nil, fmt.Errorf("source states: %w", err)
	}
	defer rows.Close()

	out := map[string]SourceState{}
	for rows.Next() {
		var (
			st   SourceState
			at   sql.NullInt64
			serr sql.NullString
		)
		if err := rows.Scan(&st.Source, &at, &serr); err != nil {
			return nil, fmt.Errorf("scan source state: %w", err)
		}
		if at.Valid {
			t := time.UnixMilli(at.Int64).UTC()
			st.LastPollAt = &t
		}
		st.LastError = serr.String
		out[st.Source] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source states: %w", err)
	}
	return out, nil
}
