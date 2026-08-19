# Codex & Season Recap

*Quest 6 of Loot: a permanent record of everything that has ever dropped, and a summary of the year in loot worth sharing.*

The Feed is now, the Vault is this quarter, the Hearth is where. The **Codex** is *ever*. It lives on its own tab, next to Feed, Vault, Hearth and Quests, and it holds three things: a wall of achievements, a column of records, and a poster-shaped recap of one month (or one whole year).

One rule shapes all of it, and it is the same rule quests are built on:

> **Nothing here can ever be lost.**

An achievement, once earned, is earned. There is no decay, no expiry, and no "you dropped below the threshold" — a *Steady* trophy for revenue on thirty consecutive days celebrates the run that happened and never notices that it ended. A record is only ever beaten, never broken. A recap states a decline as a plain fact (`$412 · $610 the month before`) in exactly the same grey as an increase, because Loot is a trophy cabinet, not a report card.

And, as everywhere else in Loot, the reward is a real drop: unlocking an achievement ingests a real event through the real pipeline, classified by the real rules engine, so a trophy arrives with a colour and a sound like everything else.

## Achievements

An achievement is a **key**, a **tier**, a **target** and a way to read progress towards it. The catalog lives in one Go file — [`internal/codex/catalog.go`](../internal/codex/catalog.go) — and adding one is a new line in that table and nothing else: the evaluator, the API and the UI all read the list.

### Tiers

A tier is how grand a trophy is, and it decides the drop its unlock pays:

| tier | drop | XP |
|---|---|---|
| **bronze** | uncommon | 50 |
| **silver** | rare | 150 |
| **gold** | epic | 400 |
| **legendary** | legendary | 1,000 |

The mapping is stated twice on purpose — once in [`internal/core/codex.go`](../internal/core/codex.go), which is what the UI promises, and once in [`internal/rules/default.yaml`](../internal/rules/default.yaml), which is what actually drops — and a test asserts the two agree. Point `rules_path` at your own file and you can re-price them; the tier is in the payload, so a rule needs no arithmetic.

### The catalog

Forty-nine trophies ship with Loot, grouped as the wall lists them.

| key | tier | what it takes |
|---|---|---|
| `first_blood` | bronze | your first ever drop |
| `first_sale` | bronze | the first settled sale in a store's own financial report |
| `first_subscriber` | bronze | the first subscription or RevenueCat purchase |
| `legendary_hunter` | gold | your first legendary drop |
| `cursed_but_unbowed` | silver | a cursed drop followed by a rare or better inside 24 hours |
| `settler_1` … `settler_4` | bronze, bronze, silver, gold | customers in 5 / 10 / 25 / 50 countries |
| `cartographer` | legendary | a settlement on every inhabited continent |
| `hoarder_1` … `hoarder_3` | bronze, silver, gold | open 10 / 50 / 200 daily chests |
| `polyglot_1`, `polyglot_2` | bronze, silver | take money in 5 / 10 currencies |
| `steady_7`, `steady_30` | bronze, silver | ledger revenue on 7 / 30 consecutive days |
| `revenue_100` … `revenue_100k` | bronze → legendary | $100 / $1k / $10k / $100k lifetime revenue |
| `units_100` … `units_100k` | bronze → legendary | 100 / 1k / 10k / 100k lifetime paid units |
| `installs_1k` … `installs_100k` | bronze, silver, gold | 1k / 10k / 100k lifetime installs |
| `subscribers_10` … `subscribers_1000` | bronze, silver, gold | 10 / 100 / 1,000 active subscribers |
| `quests_1`, `quests_10`, `quests_50` | bronze, silver, gold | complete 1 / 10 / 50 quests |
| `mysteries_1`, `mysteries_10` | bronze, silver | explain 1 / 10 mysteries |
| `stars_10`, `stars_100`, `stars_1000` | bronze, silver, gold | 10 / 100 / 1,000 GitHub stars |
| `merchant_2`, `merchant_3`, `merchant_5` | bronze, silver, gold | ledger revenue from 2 / 3 / 5 different stores |
| `record_1`, `record_5`, `record_25` | bronze, silver, gold | 1 / 5 / 25 "best day ever" days |
| `era_town` … `era_empire` | bronze, silver, gold, legendary | reach the Town / City / Kingdom / Empire era |

Money thresholds are in your `display_currency`, so the "$100" trophy is a hundred of whatever you count in.

Three notes on what is deliberately *not* in the table:

- **No time-of-day achievements.** "Night owl" is a judgement about when you work dressed up as a reward, and Loot has no opinion about your sleep.
- **No streaks.** `steady_7` and `steady_30` celebrate a run of earning days *when it is reached* and then never mention it again. There is nothing to keep alive and nothing to break.
- **Antarctica is not a continent you can settle.** An achievement nobody can finish is a locked card that never moves, which is exactly the quiet nagging the Codex is supposed to avoid.

Two rules for editing the catalog: **never reuse a key** (it is the identity of a trophy somebody may already own), and **never raise a target** (that would un-earn a trophy — ship a new key at the higher threshold instead).

### Evaluation

`codex.Service` measures the whole catalog against **one pass over the history**: a dozen grouped SQL queries reshaped into cumulative day series, one per family. Every threshold, every record and every lifetime total is then answered in Go from that one snapshot rather than by one query per achievement — which is what makes running the catalog after every ingest affordable.

It runs:

- **after any ingest**, debounced by two seconds, so a chest cascade of forty drops costs one pass rather than forty. The drop that pushed you past your thousandth unit should hand you the trophy while it is still on screen;
- **every ten minutes**, as a backstop for the things the calendar changes rather than an event.

Progress on everything still locked is refreshed on every pass and stored on the row, which is what puts `Settler III · 18 / 25 countries` under a dim card.

It is idempotent by construction: unlocking is an `UPDATE … WHERE unlocked_at IS NULL`, and the unlock event's dedupe key is `loot:achievement:<key>`. Two racing passes, a restart mid-flight or a catalog re-run all lose the race and mint nothing.

An unlock that pays XP can carry the account into a new era — which is itself an achievement — so a pass that unlocked anything schedules one more. The pass after that finds nothing and stops.

### Backfill

The first pass over a database that *already has history* is special. Somebody who imports a year of App Store reports has genuinely earned twenty-five achievements, and firing twenty-five drops — several of them legendary — into the live feed at once would be a wall of noise rather than a celebration. So on that first pass:

- each unlock is **dated with the day it was actually earned**, derived from the running total: the day the tenth country was settled, the day the seven-day run completed, the day lifetime revenue first crossed $1,000. The trophy wall then reads as a history rather than as "everything, today";
- each unlock's drop is **filed into today's chest** instead of the feed, and marked `backfilled` in its `meta` and its payload. Opening that chest is a proper cascade, quietest first, exactly like any other chest.

Every later pass unlocks live, one trophy at a time, with its own sound.

### The unlock event

```jsonc
{
  "source": "loot",
  "kind": "achievement",
  "dedupe_key": "loot:achievement:settler_3",
  "chest": true,                       // backfilled unlocks only
  "payload": {
    "key": "settler_3",
    "title": "Settler III",
    "tier": "silver",
    "description": "Customers in 25 countries.",
    "backfilled": true
  }
}
```

The default rules match on `payload.tier` and mint `Achievement: {{.Payload.title}}` with `{{.Payload.description}}` underneath.

## Records

Records are **computed on read** and never stored. That is the same decision the vault makes, for the same three reasons: a derived number can never drift out of step with the money printed next to it; a restated App Store report *improves* a record instead of leaving a stale one behind; and there is no row to accidentally overwrite with a smaller number, so a record can only ever go up.

| record | what it is |
|---|---|
| `best_revenue_day` | the day with the most ledger revenue, in the display currency |
| `best_revenue_day_by_source` | the same, per store — App Store's best day and Play's best day are different days |
| `best_units_day` | the most paid units in a day |
| `best_install_day` | the most installs in a day (the overview/per-country rule, as everywhere) |
| `most_drops_day` / `most_xp_day` | the loudest day the feed has had |
| `most_countries_day` | the most countries founded on one day |
| `biggest_drop` | the single highest-XP drop ever minted, with its title and day |
| `longest_revenue_run` | the longest unbroken run of earning days, and the day it ended — historic and celebratory |
| `first_event_day` | where the whole history starts, plus how many days that is |

Beside them sits the lifetime column: revenue, units, refunds, installs, drops, XP and era, chests opened, countries and continents, currencies, record days, quests completed, mysteries explained, and GitHub stars.

## Season recap

`GET /api/recap` writes up one **month** or one **season** (a calendar year) as the thing you would screenshot: a big revenue number with a neutral delta, a sparkline with the best day flagged, a row of new flags, a row of trophies, and an ordered list of already-written highlights.

The default is the **last complete month**. A recap of a month still in progress is a half-told story, and the default should be the one worth sharing; ask for the current month explicitly and it comes back with `partial: true` so the card can say "so far".

**Deltas are stated, not scored.** `revenue_delta` carries what the previous period was, the change, the fractional change and a direction of `up` / `down` / `flat` — and the card paints both directions in the same grey. When the previous period was empty, `has_basis` is `false` and no percentage is offered, because "+100% against nothing" is theatre rather than information.

**New countries** are countries whose *first ever* event fell inside the window. A returning customer is not a new settlement.

Highlights are written server-side, best news first, capped at seven:

```
Best day on Jul 14: $663
Unlocked Cartographer and 1 more achievement
Settled 🇿🇦 ZA on Jul 1 (and 6 more countries)
15 epic drops
Reached the Kingdom era
31 chests opened
🇺🇸 US was your biggest market
```

The **Copy summary** button copies the whole thing as plain text. Deliberately not an image: an image export means a canvas renderer and a font pipeline, and pasting a month's numbers into a chat window is what people actually do.

## API

### `GET /api/codex`

Cached for five seconds server-side, as the Hearth is — and invalidated the moment an achievement unlocks, so the refetch its websocket nudge provokes cannot be answered with a wall from before the trophy.

```jsonc
{
  "display_currency": "USD",
  "unlocked": 33,
  "total": 49,
  "achievements": [
    {
      "id": "01M0C81ZNAY1BN9K47EH9VN2RH",
      "key": "settler_3",
      "tier": "silver",
      "title": "Settler III",
      "description": "Customers in 25 countries.",
      "unlocked_at": "2026-06-12T12:00:00Z",   // null while locked
      "progress_value": 25,
      "progress_target": 25,
      "unit": "countries",
      "money": false,
      "meta": { "backfilled": true, "earned_day": "2026-06-12" },
      "pct": 1
    }
  ],
  "records": {
    "best_revenue_day":  { "day": "2026-08-14", "value": 2136.43 },
    "best_revenue_day_by_source": [ { "source": "appstore", "day": "2026-08-14", "value": 1467.71 } ],
    "best_units_day":    { "day": "2026-08-14", "value": 408 },
    "best_install_day":  { "day": "2026-08-14", "value": 8084 },
    "most_drops_day":    { "day": "2026-08-14", "value": 57 },
    "most_xp_day":       { "day": "2026-08-14", "value": 3480 },
    "most_countries_day":{ "day": "2026-04-21", "value": 9 },
    "longest_revenue_run": { "days": 120, "ended_on": "2026-08-18" },
    "biggest_drop": { "id": "01M0…", "title": "Best day ever on App Store",
                      "subtitle": "408 sales · $2,136", "rarity": "legendary",
                      "xp": 1000, "day": "2026-08-14", "source": "appstore" },
    "first_event_day": "2026-04-21",
    "days_since_first_event": 121
  },
  "totals": {
    "revenue_base": 50382.69, "units": 10968, "refunds": 334, "installs": 216937,
    "drops": 2204, "xp": 129470, "era": { "name": "Kingdom", "…": "…" },
    "chests_opened": 120, "countries": 41, "continents": 6, "currencies": 29,
    "quests_completed": 0, "mysteries_solved": 0, "stars": 0, "record_days": 52
  }
}
```

`unlocked_at` is when the achievement was **earned**, which on a backfill is a day in the past. A trophy Loot no longer has a catalog entry for still renders — at the end of the wall — rather than vanishing.

### `GET /api/recap?month=YYYY-MM` · `?season=YYYY`

Neither parameter means the last complete month. A malformed one is a `400` with the reason in `error`.

```jsonc
{
  "recap": {
    "period": { "kind": "month", "key": "2026-07", "label": "July 2026",
                "from": "2026-07-01", "to": "2026-07-31", "days": 31, "partial": false },
    "display_currency": "USD",
    "empty": false,

    "revenue_base": 15901.48,
    "revenue_delta": { "previous": 11483.6, "change": 4417.88, "pct": 0.3847,
                       "direction": "up", "has_basis": true },
    "units": 3626,
    "units_delta": { "…": "…" },
    "refunds": 91,
    "installs": 67807,

    "new_countries": [ { "country": "ZA", "day": "2026-07-01" } ],
    "best_day":   { "day": "2026-07-10", "value": 663.15 },
    "top_app":    { "key": "Lumen Notes", "revenue_base": 11130.48, "units": 1540 },
    "top_country":{ "key": "US", "revenue_base": 4878.75, "units": 1096 },
    "top_source": { "key": "appstore", "revenue_base": 9219.3, "units": 1983 },

    "drops": 694,
    "drops_by_rarity": { "common": 367, "uncommon": 82, "rare": 190, "epic": 15, "cursed": 40 },
    "top_rarity": "epic",
    "xp": 34880,

    "level_start": 28, "level_end": 34,
    "era_start": "City", "era_end": "Kingdom",

    "chests_opened": 31, "quests_completed": 0, "mysteries_solved": 0,
    "achievements_unlocked": [ { "key": "cartographer", "…": "…" } ],

    "highlights": [ "Best day on Jul 10: $663", "…" ],
    "series": [ { "day": "2026-07-01", "value": 494.99 } ]
  },
  "periods": [ { "kind": "month", "key": "2026-08", "…": "…" }, { "kind": "season", "key": "2026", "…": "…" } ]
}
```

`series` is zero-filled, one point per day, so the sparkline never has holes. `top_rarity` ignores `cursed` — a poster does not take its colour from a cancellation. `periods` is the picker's own options, so the page needs one request rather than two.

### Websocket

```jsonc
{"type": "codex"}    // an achievement unlocked — refetch
```

A bare nudge, exactly like `quests` and `mysteries`: the websocket never carries a second copy of the wall, and a client that missed one message is corrected by the next.

## Storage

One table, added by migration `0004_codex`:

```sql
CREATE TABLE achievements (
    id              TEXT PRIMARY KEY,
    key             TEXT    NOT NULL,   -- UNIQUE; the permanent identity
    tier            TEXT    NOT NULL,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    unlocked_at     INTEGER,            -- NULL while locked; when it was *earned*
    progress_value  REAL    NOT NULL DEFAULT 0,
    progress_target REAL    NOT NULL DEFAULT 0,
    meta            BLOB,               -- {"backfilled": true, "earned_day": "…"}
    created_at      INTEGER NOT NULL
);
```

The upsert that runs on every pass refreshes a locked row's title, description and progress — so editing the catalog's wording updates the wall — and leaves an unlocked row's `unlocked_at`, `meta` and progress completely alone. A trophy is permanent, and its progress line is frozen at the moment it was won.

Records and the recap have no tables at all.

## The Codex tab

Three sections, in this order.

**Trophy wall.** A grid of cards with `all` / `unlocked` / `to win` filter chips and a `33 / 49` counter. An unlocked card is vivid in its tier's metal, with the date it was earned and a small `backfilled` chip where that applies; a locked one is dim with a progress bar. Hovering — or tapping, because a phone has no hover — reveals the description. Locked trophies are dimmed, never hidden: the wall is a map of what there is to win, and hiding the unwon half would make it a list of one.

Tier colours are metals rather than rarities, deliberately. A trophy's tier and the rarity of the drop it pays are two different facts (a gold trophy pays an epic drop), and painting the tier in the rarity's colour made the wall read as "you have four epics" instead of "you have four gold trophies".

**Records.** Two tidy columns: the best-evers on the left with their dates, the lifetime totals on the right.

**Season recap.** A month picker (the last twelve, plus **This year**) above a poster-shaped card whose gradient comes from the rarest thing that dropped that month. Big revenue number, neutral delta line, sparkline with the best day flagged, the figures row, the highlights, the flags of every country settled, the trophies won, and a footer with the era and the top app/source/country. **Copy summary** puts the lot on the clipboard as plain text.

Unlock toasts are not a separate mechanism: an achievement *is* a drop, so it arrives through the ordinary drop pathway with its rarity's colour and its rarity's sound, and lands in the feed like everything else.

## Demo mode

`loot serve --demo` evaluates the Codex once after seeding, which is a backfill: the demo opens on a wall of about thirty trophies dated across its four months of invented history, with their drops waiting in today's chest. Open it and they cascade out quietest first, which is a rather good way to meet the feature.
