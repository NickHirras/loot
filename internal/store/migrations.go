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
}
