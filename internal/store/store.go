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

	res, err := s.db.ExecContext(ctx, `
        INSERT INTO events (id, source, kind, app, occurred_at, observed_at, country,
                            amount, currency, quantity, dedupe_key, is_ledger, payload)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(dedupe_key) DO NOTHING`,
		ev.ID, ev.Source, ev.Kind, ev.App,
		ev.OccurredAt.UTC().UnixMilli(), ev.ObservedAt.UTC().UnixMilli(),
		strings.ToUpper(ev.Country), ev.Amount, ev.Currency, ev.Quantity,
		ev.DedupeKey, ev.IsLedger, payload,
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

// InsertDrop persists a classified drop.
func (s *Store) InsertDrop(ctx context.Context, d core.Drop) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO drops (id, event_id, rarity, title, subtitle, xp, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.EventID, string(d.Rarity), d.Title, d.Subtitle, d.XP, d.CreatedAt.UTC().UnixMilli())
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
	Currency   string          `json:"currency"`
	Quantity   int             `json:"quantity"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

const dropSelect = `
SELECT d.id, d.event_id, d.rarity, d.title, d.subtitle, d.xp, d.created_at,
       e.source, e.kind, e.app, e.country, e.amount, e.currency, e.quantity, e.occurred_at
FROM drops d
JOIN events e ON e.id = d.event_id`

// ListDrops returns the most recent drops, newest first. When before is a
// non-empty drop id, only drops older than it are returned (ULIDs sort by
// time, so id ordering is time ordering).
func (s *Store) ListDrops(ctx context.Context, limit int, before string) ([]DropView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := dropSelect
	args := []any{}
	if before != "" {
		query += ` WHERE d.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY d.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list drops: %w", err)
	}
	defer rows.Close()

	out := make([]DropView, 0, limit)
	for rows.Next() {
		var (
			v          DropView
			rarity     string
			createdAt  int64
			occurredAt int64
		)
		if err := rows.Scan(&v.ID, &v.EventID, &rarity, &v.Title, &v.Subtitle, &v.XP, &createdAt,
			&v.Source, &v.Kind, &v.App, &v.Country, &v.Amount, &v.Currency, &v.Quantity, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan drop: %w", err)
		}
		v.Rarity = core.Rarity(rarity)
		v.CreatedAt = time.UnixMilli(createdAt).UTC()
		v.OccurredAt = time.UnixMilli(occurredAt).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate drops: %w", err)
	}
	return out, nil
}

// Stats is the aggregate shown in the dashboard header.
type Stats struct {
	TotalDrops     int            `json:"total_drops"`
	TotalEvents    int            `json:"total_events"`
	TotalXP        int            `json:"total_xp"`
	ByRarity       map[string]int `json:"by_rarity"`
	BySource       map[string]int `json:"by_source"`
	Countries      []string       `json:"countries"`
	CountriesCount int            `json:"countries_count"`
}

// Stats computes dashboard aggregates in a handful of grouped queries.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{
		ByRarity:  map[string]int{},
		BySource:  map[string]int{},
		Countries: []string{},
	}
	for _, r := range core.Rarities {
		st.ByRarity[string(r)] = 0
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(xp), 0) FROM drops`).Scan(&st.TotalDrops, &st.TotalXP); err != nil {
		return st, fmt.Errorf("stats totals: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&st.TotalEvents); err != nil {
		return st, fmt.Errorf("stats events: %w", err)
	}

	rarityRows, err := s.db.QueryContext(ctx, `SELECT rarity, COUNT(*) FROM drops GROUP BY rarity`)
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
        SELECT e.source, COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id GROUP BY e.source`)
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
