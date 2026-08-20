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

// querier is the subset of *sql.DB that every repository method needs, so the
// same method bodies can run either directly on the pool or inside one
// transaction (see WithTx).
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the repository. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	// q is where reads and writes go: the pool for a normal store, a
	// *sql.Tx for the store handed to WithTx.
	q querier
	// scope narrows reads to one product; the zero value is "every app".
	// See scope.go — it is a lens on reads and never affects a write.
	scope scope
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

	s := &Store{db: db, q: db}
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

	// A source that has not been through Normalize (a test, a hand-built
	// event) still gets a product, because an event with an app but no product
	// would be invisible in every scope — including its own.
	product := ev.Product
	if product == "" {
		product = ev.App
	}

	res, err := s.q.ExecContext(ctx, `
        INSERT INTO events (id, source, kind, app, product, occurred_at, observed_at, day, country,
                            amount, currency, amount_base, quantity, dedupe_key,
                            is_ledger, silent, chest, payload)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(dedupe_key) DO NOTHING`,
		ev.ID, ev.Source, ev.Kind, ev.App, product,
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
	_, err := s.q.ExecContext(ctx, `
        INSERT INTO drops (id, event_id, rarity, title, subtitle, xp, created_at, chest_date, revealed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.EventID, string(d.Rarity), d.Title, d.Subtitle, d.XP,
		d.CreatedAt.UTC().UnixMilli(), d.ChestDate, revealed)
	if err != nil {
		return fmt.Errorf("insert drop: %w", err)
	}
	return nil
}

// WithTx runs fn against a Store whose reads and writes all go through one
// transaction, committing when fn returns nil and rolling back otherwise. It
// exists for bulk work — seeding demo mode writes tens of thousands of rows,
// and one commit instead of one per row is the difference between a second and
// half a minute.
//
// Two rules follow from Loot opening SQLite with a single connection:
//
//   - the *Store passed to fn is only valid until fn returns;
//   - nothing else may touch the outer Store while fn runs, including
//     indirectly (a rules engine holding it as its Lookup, say) — the call
//     would block waiting for the connection the transaction is holding.
//     Build collaborators against the store fn is given instead.
func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(&Store{db: s.db, q: tx, scope: s.scope}); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// CountryFirstEvent reports whether exactly one stored event carries the given
// country — which, called after the event is inserted, means "this one was the
// first ever from there". It stops counting at two, so founding a settlement
// stays cheap no matter how many customers a country has since sent.
func (s *Store) CountryFirstEvent(ctx context.Context, country string) (bool, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		return false, nil
	}
	var n int
	// The `country <> ''` clause is not redundant: events_country_idx is a
	// partial index over exactly those rows, and SQLite will only use it when
	// the query proves the row qualifies — which it cannot do from a bound
	// parameter alone. Without the clause this is a table scan per event.
	err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (
             SELECT 1 FROM events WHERE country = ? AND country <> '' LIMIT 2)`, country).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("country first event: %w", err)
	}
	return n == 1, nil
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
	// See CountryFirstEvent on why the second clause has to be spelled out.
	err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE country = ? AND country <> ''`, country).Scan(&n)
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
	// Asked as "is there anything at least this big?" rather than "what is the
	// biggest?", the events_record_idx range scan stops at the first row it
	// finds instead of walking every event the source has ever produced —
	// which matters when the answer is wanted for every drop of a long
	// backfill. The two questions are equivalent: a strict record is exactly
	// an event no other event ties or beats.
	var (
		prior  int
		beaten int
	)
	err := s.q.QueryRowContext(ctx, `
        SELECT (SELECT COUNT(*) FROM (SELECT 1 FROM events
                       WHERE source = ? AND app = ? AND kind = ? AND id <> ? LIMIT ?)),
               EXISTS (SELECT 1 FROM events
                       WHERE source = ? AND app = ? AND kind = ? AND quantity >= ? AND id <> ?)`,
		ev.Source, ev.App, ev.Kind, ev.ID, MinRecordHistory,
		ev.Source, ev.App, ev.Kind, ev.Quantity, ev.ID).Scan(&prior, &beaten)
	if err != nil {
		return false, fmt.Errorf("record quantity: %w", err)
	}
	if prior < MinRecordHistory {
		// Too little history for "best ever" to mean anything: the first few
		// days of a new series beat each other by construction (seen for real:
		// a 30-day Snapcraft backfill minted epics on days two and three).
		return false, nil
	}
	return beaten == 0, nil
}

// MinRecordHistory is how many prior events a (source, app, kind) series needs
// before a new high counts as a record.
const MinRecordHistory = 7

// EventCount returns the number of stored events for a source ("" = all).
func (s *Store) EventCount(ctx context.Context, source string) (int, error) {
	var (
		n   int
		err error
	)
	if source == "" {
		err = s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	} else {
		err = s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE source = ?`, source).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("event count: %w", err)
	}
	return n, nil
}

// DropView is a drop joined with the event fields the feed needs to render.
type DropView struct {
	core.Drop
	Source string `json:"source"`
	Kind   string `json:"kind"`
	App    string `json:"app"`
	// Product is the canonical app this drop belongs to, or "" for a
	// realm-wide one. The UI filters live drops on it.
	Product    string          `json:"product"`
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
		Product:    v.Product,
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
       e.source, e.kind, e.app, e.product, e.country, e.amount, e.amount_base, e.currency,
       e.quantity, e.day, e.occurred_at
FROM drops d
JOIN events e ON e.id = d.event_id`

// unrevealed matches drops that are waiting inside an unopened chest. An
// immediate drop has no chest_date at all, so it is always visible.
const unrevealed = `(d.chest_date <> '' AND d.revealed_at IS NULL)`

// Drop page sizes. DefaultDropLimit is what a request that asks for nothing
// gets; MaxDropLimit is the ceiling. They are exported so the HTTP handler
// clamps to exactly the same numbers the query does — a handler that computed
// its "next page" cursor from a limit the store had already overruled paged
// the feed wrongly.
const (
	DefaultDropLimit = 100
	MaxDropLimit     = 500
)

// DropQuery parameterizes ListDrops.
type DropQuery struct {
	// Limit caps the page; 0 means DefaultDropLimit, and anything larger than
	// MaxDropLimit is clamped to it.
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
	limit := ClampDropLimit(q.Limit)

	where := []string{}
	args := []any{}
	if q.Before != "" {
		where = append(where, `d.id < ?`)
		args = append(args, q.Before)
	}
	if !q.IncludeUnrevealed {
		where = append(where, `NOT `+unrevealed)
	}
	// Loose: another product's drops are hidden, Loot's own realm-wide ones
	// are not. See scope.go.
	if frag, fragArgs := s.scopeLoose("e"); frag != "" {
		where = append(where, strings.TrimPrefix(frag, " AND "))
		args = append(args, fragArgs...)
	}

	query := dropSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY d.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.q.QueryContext(ctx, query, args...)
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

// ClampDropLimit applies the page-size rules. Callers that need to know how
// big a page really was — to decide whether there is another one — must use
// it rather than their own arithmetic.
func ClampDropLimit(limit int) int {
	if limit <= 0 {
		return DefaultDropLimit
	}
	if limit > MaxDropLimit {
		return MaxDropLimit
	}
	return limit
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
			&v.Source, &v.Kind, &v.App, &v.Product, &v.Country, &v.Amount, &v.AmountBase, &v.Currency,
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
	rows, err := s.q.QueryContext(ctx, `
        SELECT chest_date, rarity, COUNT(*), COALESCE(SUM(xp), 0), MAX(created_at)
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
			filled       int64
		)
		if err := rows.Scan(&date, &rarity, &n, &xp, &filled); err != nil {
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
		if at := time.UnixMilli(filled).UTC(); at.After(out[i].FilledAt) {
			out[i].FilledAt = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chest summaries: %w", err)
	}
	return out, nil
}

// ListChest returns every unopened chest with its drops, oldest day first.
// Drops within a chest come back in reveal order (see core.RevealRank).
func (s *Store) ListChest(ctx context.Context) ([]Chest, error) {
	rows, err := s.q.QueryContext(ctx, dropSelect+`
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
	err := s.q.QueryRowContext(ctx,
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

	// Claim the chest and read back exactly what this call claimed, in one
	// statement. Selecting first and updating afterwards left a window in
	// which two callers — the UI button and the auto-open sweep, or two
	// browsers — both saw the same unopened drops, both returned them, and
	// both cascaded them onto the feed. Whoever loses the race here gets no
	// rows at all, which is what "opening an already-open chest returns no
	// drops and no error" is supposed to mean.
	claimed, err := s.q.QueryContext(ctx, `
        UPDATE drops SET revealed_at = ?
        WHERE chest_date = ? AND chest_date <> '' AND revealed_at IS NULL
        RETURNING id`, now.UTC().UnixMilli(), chestDate)
	if err != nil {
		return nil, fmt.Errorf("reveal chest: %w", err)
	}
	var ids []string
	for claimed.Next() {
		var id string
		if err := claimed.Scan(&id); err != nil {
			claimed.Close()
			return nil, fmt.Errorf("scan revealed drop id: %w", err)
		}
		ids = append(ids, id)
	}
	err = claimed.Err()
	claimed.Close()
	if err != nil {
		return nil, fmt.Errorf("mark chest revealed: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Now read them back in cascade order. They are addressed by id because
	// they no longer match "unrevealed" — that is the point.
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.q.QueryContext(ctx, dropSelect+`
        WHERE d.id IN (`+placeholders(len(ids))+`)
        ORDER BY `+revealOrder+`, d.created_at ASC, d.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("read revealed chest: %w", err)
	}
	drops, err := scanDrops(rows, len(ids))
	rows.Close()
	if err != nil {
		return nil, err
	}
	return drops, nil
}

// RevealedChest is one chest opened by a bulk reveal: the day it belongs to
// and the drops it held, in cascade order.
type RevealedChest struct {
	Date  string     `json:"date"`
	Drops []DropView `json:"drops"`
}

// RevealAllChests opens every chest currently waiting, oldest day first, and
// returns them in that order with each chest's drops in cascade order.
//
// The claim stays per chest rather than becoming one giant UPDATE over every
// unrevealed row. Each chest is still opened atomically by RevealChest, so a
// bulk open racing a single open of one of the same days cannot hand the same
// drop to both callers; the loser simply sees that chest come back empty and
// leaves it out of the result. One statement across all days would have been
// atomic too, but it could not report *which* chest each drop came from
// without a second read, and the caller — the "Open all" button, `loot chest
// open --all` — wants the haul grouped by day.
func (s *Store) RevealAllChests(ctx context.Context, now time.Time) ([]RevealedChest, error) {
	chests, err := s.ChestSummaries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RevealedChest, 0, len(chests))
	for _, c := range chests {
		drops, err := s.RevealChest(ctx, c.Date, now)
		if err != nil {
			return nil, err
		}
		// No drops means somebody else claimed this chest between the summary
		// read and the claim. That chest is not this call's to report.
		if len(drops) == 0 {
			continue
		}
		out = append(out, RevealedChest{Date: c.Date, Drops: drops})
	}
	return out, nil
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

	// Every count here is *loose*: another product's news is hidden, Loot's own
	// realm-wide news is not. A scoped header therefore reads as "this app,
	// plus the things that are about all of it" — which is what the trophies
	// and the global quests genuinely are.
	//
	// The drop counts need the events join once there is a scope to apply;
	// without one the cheaper drops-only query is kept.
	scoped, scopeArgs := s.scopeLoose("e")
	dropsFrom := `FROM drops d`
	if scoped != "" {
		dropsFrom = `FROM drops d JOIN events e ON e.id = d.event_id`
	}

	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) `+dropsFrom+` WHERE NOT `+unrevealed+scoped, scopeArgs...).
		Scan(&st.TotalDrops); err != nil {
		return st, fmt.Errorf("stats totals: %w", err)
	}
	// XP is the one figure here that is never scoped, for the same reason the
	// Hearth's era is not: it is the account's standing, earned across
	// everything you ship. A level that halved when you looked at one of your
	// apps would be telling you something untrue about yourself — and the two
	// numbers have to agree, because the header and the era bar are on screen
	// together.
	if err := s.q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(xp), 0) FROM drops d WHERE NOT `+unrevealed).
		Scan(&st.TotalXP); err != nil {
		return st, fmt.Errorf("stats xp: %w", err)
	}
	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events e WHERE 1 = 1`+scoped, scopeArgs...).Scan(&st.TotalEvents); err != nil {
		return st, fmt.Errorf("stats events: %w", err)
	}
	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) `+dropsFrom+` WHERE `+unrevealed+scoped, scopeArgs...).Scan(&st.UnrevealedCount); err != nil {
		return st, fmt.Errorf("stats unrevealed: %w", err)
	}

	rarityRows, err := s.q.QueryContext(ctx,
		`SELECT d.rarity, COUNT(*) `+dropsFrom+` WHERE NOT `+unrevealed+scoped+` GROUP BY d.rarity`, scopeArgs...)
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

	sourceRows, err := s.q.QueryContext(ctx, `
        SELECT e.source, COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+scoped+` GROUP BY e.source`, scopeArgs...)
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

	countryRows, err := s.q.QueryContext(ctx,
		`SELECT DISTINCT e.country FROM events e WHERE e.country <> ''`+scoped+` ORDER BY e.country`, scopeArgs...)
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
	err := s.q.QueryRowContext(ctx, `SELECT state FROM source_state WHERE source = ?`, source).Scan(&blob)
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
	_, err := s.q.ExecContext(ctx, `
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
	_, err := s.q.ExecContext(ctx, `
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
	rows, err := s.q.QueryContext(ctx, `SELECT source, last_poll_at, last_error FROM source_state`)
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
