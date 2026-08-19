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
	{
		// Quest 5: goals with windows, and the flagged days that ask why.
		Name: "0003_quests",
		SQL: `
CREATE TABLE quests (
    id           TEXT PRIMARY KEY,
    kind         TEXT    NOT NULL DEFAULT 'auto',
    metric       TEXT    NOT NULL,
    target       REAL    NOT NULL DEFAULT 0,
    app          TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT '',
    window_start TEXT    NOT NULL,
    window_end   TEXT    NOT NULL,
    title        TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'active',
    -- Progress cache: what the metric last read, and when it was measured.
    value        REAL    NOT NULL DEFAULT 0,
    updated_at   INTEGER,
    xp           INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    completed_at INTEGER,
    -- One auto quest per (metric, app, source, window), so the generator can
    -- run at every startup and every midnight without ever duplicating one.
    dedupe_key   TEXT    NOT NULL
);

CREATE UNIQUE INDEX quests_dedupe_key_idx ON quests(dedupe_key);
CREATE INDEX quests_status_idx           ON quests(status, window_end);

CREATE TABLE mysteries (
    id          TEXT PRIMARY KEY,
    kind        TEXT    NOT NULL,
    source      TEXT    NOT NULL DEFAULT '',
    app         TEXT    NOT NULL DEFAULT '',
    metric      TEXT    NOT NULL DEFAULT '',
    day         TEXT    NOT NULL,
    observed    REAL    NOT NULL DEFAULT 0,
    expected    REAL    NOT NULL DEFAULT 0,
    z           REAL    NOT NULL DEFAULT 0,
    title       TEXT    NOT NULL DEFAULT '',
    detail      BLOB,
    status      TEXT    NOT NULL DEFAULT 'open',
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    resolved_at INTEGER,
    -- (kind, source, app, metric, day): the detector is idempotent, so it can
    -- re-examine the same fortnight every hour for free.
    dedupe_key  TEXT    NOT NULL
);

CREATE UNIQUE INDEX mysteries_dedupe_key_idx ON mysteries(dedupe_key);
CREATE INDEX mysteries_status_idx           ON mysteries(status, day);
`,
	},
	{
		// Quest 6: the Codex. Achievements are the only thing Loot stores that
		// can never be taken away, so the table has no "locked" transition at
		// all — unlocked_at goes from NULL to a timestamp exactly once, and
		// the guarded UPDATE that sets it is what makes the unlock drop
		// unrepeatable.
		//
		// Records and the season recap are deliberately *not* here: both are
		// computed on read from events and drops, so they can never drift out
		// of step with the vault, and a restated report improves a record
		// rather than leaving a stale one behind.
		Name: "0004_codex",
		SQL: `
CREATE TABLE achievements (
    id              TEXT PRIMARY KEY,
    key             TEXT    NOT NULL,
    tier            TEXT    NOT NULL,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    -- NULL while locked; the time the achievement was *earned*, which on a
    -- backfill is a day in the past rather than the moment Loot noticed.
    unlocked_at     INTEGER,
    progress_value  REAL    NOT NULL DEFAULT 0,
    progress_target REAL    NOT NULL DEFAULT 0,
    meta            BLOB,
    created_at      INTEGER NOT NULL
);

CREATE UNIQUE INDEX achievements_key_idx      ON achievements(key);
CREATE INDEX        achievements_unlocked_idx ON achievements(unlocked_at);
`,
	},
	{
		// Quest 3: bosses. A crash cluster that broke away from its own
		// baseline, given a name and a health bar.
		//
		// `key` is unique across the whole table rather than per status, and
		// that is deliberate: a fight is one row for its whole life, from the
		// cursed spawn through the epic kill, so history reads as "you fought
		// this and won" rather than as a pile of near-identical monsters. A
		// version that starts crashing again months later is a *new* fight
		// only if it is a new version — which it usually is, because the key
		// carries the version.
		//
		// hp and hp_max are floats for the same reason the vault's amounts
		// are: a source may report a rate or a user count rather than a whole
		// number of crashes, and rounding at the boundary loses the small
		// fights entirely.
		Name: "0005_bosses",
		SQL: `
CREATE TABLE bosses (
    id             TEXT PRIMARY KEY,
    key            TEXT    NOT NULL,
    source         TEXT    NOT NULL DEFAULT '',
    app            TEXT    NOT NULL DEFAULT '',
    name           TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    version        TEXT    NOT NULL DEFAULT '',
    issue_id       TEXT    NOT NULL DEFAULT '',
    hp_max         REAL    NOT NULL DEFAULT 0,
    hp             REAL    NOT NULL DEFAULT 0,
    users_affected INTEGER NOT NULL DEFAULT 0,
    spawned_at     INTEGER NOT NULL,
    spawned_day    TEXT    NOT NULL DEFAULT '',
    last_hit_at    INTEGER,
    status         TEXT    NOT NULL DEFAULT 'alive',
    slain_at       INTEGER,
    peak_day       TEXT    NOT NULL DEFAULT '',
    detail         BLOB,
    xp_awarded     INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX bosses_key_idx    ON bosses(key);
CREATE INDEX        bosses_status_idx ON bosses(status, spawned_day);
`,
	},
	{
		// The app scope: which product an event belongs to.
		//
		// `app` stays exactly what the source said — it is evidence, and
		// rewriting it would lose the only record of what App Store Connect
		// actually called the row. `product` is the *reading* of it: App
		// resolved through the configured `apps:` mapping, recomputed at
		// startup by RemapProducts whenever that mapping changes. An empty
		// product means "the whole realm" (Loot's own achievements), never
		// "unknown app" — an unmapped app resolves to its own raw name.
		//
		// Both indexes lead with product because every scoped query starts
		// there; (product, day) serves the vault and the codex, and
		// (product, source, day) the per-source breakdowns.
		Name: "0006_products",
		SQL: `
ALTER TABLE events ADD COLUMN product TEXT NOT NULL DEFAULT '';

-- Existing rows start as their own raw app name, which is what an unmapped
-- app resolves to anyway; RemapProducts corrects the mapped ones at startup
-- and logs how many it changed.
UPDATE events SET product = app WHERE product = '' AND app <> '';

CREATE INDEX events_product_day_idx        ON events(product, day);
CREATE INDEX events_product_source_day_idx ON events(product, source, day);
`,
	},
}
