package store

// migrations are applied in order at startup. Each entry runs exactly once and
// is recorded in schema_migrations; never edit an applied migration, append a
// new one instead.
var migrations = []struct {
	Name string
	SQL  string
}{
	{
		Name: "0001_init",
		SQL: `
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
`,
	},
	{
		// Quest 2: silent ledger events, business days, base-currency amounts,
		// the daily chest and an FX rate cache.
		Name: "0002_vault",
		SQL: `
ALTER TABLE events ADD COLUMN silent      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN day         TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN amount_base REAL    NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN chest       INTEGER NOT NULL DEFAULT 0;

-- Existing rows predate the business-day column: derive it from occurred_at
-- (stored as epoch milliseconds) in UTC, which is what the pipeline would
-- have done for a realtime event.
UPDATE events SET day = strftime('%Y-%m-%d', occurred_at / 1000, 'unixepoch') WHERE day = '';

-- amount_base for existing rows is filled in at startup by
-- BackfillAmountBase, which knows the configured display currency; rows in a
-- foreign currency stay 0 until "loot fx recompute" converts them.

CREATE INDEX events_day_idx            ON events(day);
CREATE INDEX events_source_app_day_idx ON events(source, app, day);
-- events_country_idx already covers (country) for non-empty countries.

ALTER TABLE drops ADD COLUMN chest_date  TEXT NOT NULL DEFAULT '';
ALTER TABLE drops ADD COLUMN revealed_at INTEGER;

-- The feed and the chest badge both ask "what is still unopened?", which is a
-- tiny slice of a growing table.
CREATE INDEX drops_unrevealed_idx ON drops(chest_date) WHERE revealed_at IS NULL;
CREATE INDEX drops_revealed_idx   ON drops(revealed_at);

CREATE TABLE fx_rates (
    base  TEXT NOT NULL,
    quote TEXT NOT NULL,
    rate  REAL NOT NULL,
    as_of TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (base, quote)
);
`,
	},
}
