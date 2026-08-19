package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nickhirras/loot/internal/store"
)

// v1Schema is migration 0001_init exactly as it shipped in Quest 1. It is
// duplicated here on purpose: the point of the test is to prove that a
// database created by the *old* code survives the new migration, so it must
// not be built from the current migration list.
const v1Schema = `
CREATE TABLE events (
    id          TEXT PRIMARY KEY,
    source      TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    app         TEXT    NOT NULL DEFAULT '',
    occurred_at INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    country     TEXT    NOT NULL DEFAULT '',
    amount      REAL    NOT NULL DEFAULT 0,
    currency    TEXT    NOT NULL DEFAULT '',
    quantity    INTEGER NOT NULL DEFAULT 0,
    dedupe_key  TEXT    NOT NULL,
    is_ledger   INTEGER NOT NULL DEFAULT 0,
    payload     BLOB
);

CREATE UNIQUE INDEX events_dedupe_key_idx ON events(dedupe_key);
CREATE INDEX events_source_idx           ON events(source);
CREATE INDEX events_country_idx          ON events(country) WHERE country <> '';
CREATE INDEX events_occurred_idx         ON events(occurred_at);
CREATE INDEX events_record_idx           ON events(source, app, kind, quantity);

CREATE TABLE drops (
    id         TEXT PRIMARY KEY,
    event_id   TEXT    NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rarity     TEXT    NOT NULL,
    title      TEXT    NOT NULL,
    subtitle   TEXT    NOT NULL DEFAULT '',
    xp         INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE INDEX drops_created_idx  ON drops(created_at DESC);
CREATE INDEX drops_rarity_idx   ON drops(rarity);
CREATE INDEX drops_event_idx    ON drops(event_id);

CREATE TABLE source_state (
    source       TEXT PRIMARY KEY,
    state        BLOB,
    last_poll_at INTEGER,
    last_error   TEXT NOT NULL DEFAULT ''
);
`

// TestMigrateFromV1 builds a Quest 1 database by hand, fills it with the rows
// a Quest 1 install would have, and then opens it with the current code.
func TestMigrateFromV1(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loot.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, v1Schema); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES ('0001_init', ?)`,
		time.Now().UnixMilli()); err != nil {
		t.Fatalf("record v1 migration: %v", err)
	}

	// One event on a known day, in USD, plus its drop.
	occurred := time.Date(2026, 3, 4, 21, 30, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
        INSERT INTO events (id, source, kind, app, occurred_at, observed_at, country,
                            amount, currency, quantity, dedupe_key, is_ledger, payload)
        VALUES ('EV1', 'revenuecat', 'purchase', 'com.example.app', ?, ?, 'US',
                9.99, 'USD', 1, 'rc:evt-1', 0, NULL)`,
		occurred.UnixMilli(), occurred.UnixMilli()); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	// A second event in a currency Loot cannot convert without rates.
	if _, err := db.ExecContext(ctx, `
        INSERT INTO events (id, source, kind, app, occurred_at, observed_at, country,
                            amount, currency, quantity, dedupe_key, is_ledger, payload)
        VALUES ('EV2', 'revenuecat', 'purchase', 'com.example.app', ?, ?, 'DE',
                8.00, 'EUR', 1, 'rc:evt-2', 0, NULL)`,
		occurred.UnixMilli(), occurred.UnixMilli()); err != nil {
		t.Fatalf("insert legacy event 2: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
        INSERT INTO drops (id, event_id, rarity, title, subtitle, xp, created_at)
        VALUES ('DR1', 'EV1', 'uncommon', 'New subscriber', '', 25, ?)`,
		occurred.UnixMilli()); err != nil {
		t.Fatalf("insert legacy drop: %v", err)
	}
	db.Close()

	// Now open it with the current code: migration 0002 must apply cleanly.
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("open v1 database with current code: %v", err)
	}
	defer st.Close()

	var day string
	if err := st.DB().QueryRowContext(ctx, `SELECT day FROM events WHERE id = 'EV1'`).Scan(&day); err != nil {
		t.Fatalf("read backfilled day: %v", err)
	}
	if day != "2026-03-04" {
		t.Fatalf("day = %q, want 2026-03-04 backfilled from occurred_at", day)
	}

	var silent, chest int
	var amountBase float64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT silent, chest, amount_base FROM events WHERE id = 'EV1'`).Scan(&silent, &chest, &amountBase); err != nil {
		t.Fatalf("read new columns: %v", err)
	}
	if silent != 0 || chest != 0 {
		t.Fatalf("legacy event came back silent=%d chest=%d, want 0/0", silent, chest)
	}
	if amountBase != 0 {
		t.Fatalf("amount_base = %v before backfill, want 0", amountBase)
	}

	// The startup backfill converts the rows that need no rates.
	n, err := st.BackfillAmountBase(ctx, "USD")
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("backfilled %d rows, want 1 (only the USD one)", n)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT amount_base FROM events WHERE id = 'EV1'`).Scan(&amountBase); err != nil {
		t.Fatalf("read amount_base: %v", err)
	}
	if amountBase != 9.99 {
		t.Fatalf("amount_base = %v, want 9.99", amountBase)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT amount_base FROM events WHERE id = 'EV2'`).Scan(&amountBase); err != nil {
		t.Fatalf("read foreign amount_base: %v", err)
	}
	if amountBase != 0 {
		t.Fatalf("EUR amount_base = %v, want 0 until rates are available", amountBase)
	}

	// The legacy drop is still visible and still counted.
	drops, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 1 || drops[0].ID != "DR1" {
		t.Fatalf("drops = %+v, want the legacy drop", drops)
	}
	if drops[0].ChestDate != "" || drops[0].RevealedAt != nil {
		t.Fatalf("legacy drop was filed into a chest: %+v", drops[0])
	}

	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalDrops != 1 || stats.TotalXP != 25 || stats.UnrevealedCount != 0 {
		t.Fatalf("stats = %+v", stats)
	}

	// Reopening is a no-op: migrations run exactly once.
	st.Close()
	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
}

func TestRecomputeAmountBase(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	usd := sampleEvent("rc:usd")
	if _, err := st.InsertEvent(ctx, usd); err != nil {
		t.Fatalf("insert: %v", err)
	}
	eur := sampleEvent("rc:eur")
	eur.Currency = "EUR"
	eur.Amount = 8
	if _, err := st.InsertEvent(ctx, eur); err != nil {
		t.Fatalf("insert: %v", err)
	}
	xyz := sampleEvent("rc:xyz")
	xyz.Currency = "XYZ"
	xyz.Amount = 5
	if _, err := st.InsertEvent(ctx, xyz); err != nil {
		t.Fatalf("insert: %v", err)
	}

	updated, skipped, err := st.RecomputeAmountBase(ctx, func(amount float64, currency string) (float64, bool) {
		switch currency {
		case "USD":
			return amount, true
		case "EUR":
			return amount / 0.8, true
		default:
			return 0, false
		}
	})
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if updated != 2 || skipped != 1 {
		t.Fatalf("updated %d, skipped %d; want 2 and 1", updated, skipped)
	}

	var base float64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT amount_base FROM events WHERE dedupe_key = 'rc:eur'`).Scan(&base); err != nil {
		t.Fatalf("read: %v", err)
	}
	if base != 10 {
		t.Fatalf("amount_base = %v, want 10", base)
	}
}

func TestFXRatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	rates, asOf, err := st.GetFXRates(ctx, "USD")
	if err != nil {
		t.Fatalf("get on a cold cache: %v", err)
	}
	if len(rates) != 0 || asOf != "" {
		t.Fatalf("cold cache returned %v / %q", rates, asOf)
	}

	if err := st.PutFXRates(ctx, "USD", map[string]float64{"EUR": 0.86, "JPY": 159.7}, "2026-08-18"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// A second write replaces the table rather than merging into it.
	if err := st.PutFXRates(ctx, "USD", map[string]float64{"EUR": 0.87}, "2026-08-19"); err != nil {
		t.Fatalf("put again: %v", err)
	}

	rates, asOf, err = st.GetFXRates(ctx, "USD")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(rates) != 1 || rates["EUR"] != 0.87 || asOf != "2026-08-19" {
		t.Fatalf("rates = %v as of %q, want only the newest table", rates, asOf)
	}
}
