# Quests & Mysteries

*Quest 5 of Loot: goals worth chasing, and questions worth asking.*

Loot already tells you what happened. Quests are about what could happen next, and mysteries are about the days you would otherwise scroll past. Both live on the **Quests** tab, next to Feed, Vault and Hearth.

Two rules shape all of it, and neither is negotiable:

- **A quest never fails.** When a window ends unmet the quest quietly becomes history, rendered as a neutral grey `ended · 62%`. There is no streak to break, nothing turns red, nothing says "you missed it", and no event is emitted — expiry is the only state change in Loot that makes no sound at all. The loud moment is reserved for the one where you *finished* something.
- **A mystery is an invitation, not a chore.** Nothing is on fire and nothing needs acknowledging. Ignoring one costs nothing, dismissing one costs nothing, and the only reward is for being curious.

Both pay out through the ordinary drop pipeline: completing a quest and solving a mystery ingest a real event, classified by the real rules engine, so the reward is the same variable, noisy, satisfying thing every other drop is — with its rarity, its colour and its sound.

## Quests

A quest is a **metric**, a **target** and a **window of days**. Nothing else. Progress is a SQL aggregate over the events and drops Loot has already stored, recomputed on every check, so a quest can never drift out of step with the vault beside it.

### Metrics

| metric | what it counts |
|---|---|
| `revenue` | ledger revenue in the display currency — the vault's rule exactly: ledger rows only, `sales_day` summaries excluded, refunds netted out |
| `units` | paid units on ledger rows (free downloads and refunds excluded, as in the vault) |
| `installs` | `install` events — see the overview rule below |
| `subscribers` | the newest `subscription_snapshot` inside the window, summed over apps: a level, not a flow |
| `drops` | visible drops (a drop still sealed in an unopened chest has not happened yet) |
| `settlements` | first-ever countries founded in the window |
| `stars` | GitHub stars |
| `xp` | the XP of visible drops |

**The install rule.** Sources disagree about how to report installs. Google Play publishes an *overview* row for the whole app *and* a row per country for the same day; Flathub publishes one row per app per day with no country at all. Summing everything would count Play twice; summing only country rows would lose Flathub. So, per `(source, app, day)`: **if any row carries no country, those rows are the day; otherwise the country rows are added up.** One rule, no source-specific special cases, and no double counting.

Every metric can optionally be narrowed to one `app`, one `source`, or both.

### Generated quests

A `quests.Generator` runs at startup and again each day just after midnight (local time), and writes the current week's and month's quests from your own history:

| quest | window | target |
|---|---|---|
| Beat last week's revenue | Mon–Sun | last week × 1.05 |
| Units: beat last week | Mon–Sun | last week × 1.05 |
| Installs: beat last week | Mon–Sun | last week × 1.05 |
| Earn N XP this week | Mon–Sun | last week × 1.10 |
| Beat last month's revenue | 1st–last | last month × 1.05 |
| Settle 2 new countries this month | 1st–last | 2 |
| Beat last week's stars | Mon–Sun | last week × 1.05 |

Targets are rounded to numbers a person would have picked (whole units below 100, tens below 1,000, fifties below 10,000, hundreds above), and titled with the target in them: *"Beat last week's revenue · $1,240"*.

Three properties keep the board out of your way:

- **Idempotent.** Every generated quest is keyed on `(metric, app, source, window)`, so the generator may run at every startup and every midnight without ever duplicating one. Deleting one would only bring it back tomorrow, which is why generated quests are not deletable — they expire on their own.
- **Only where there is data.** A database that has never seen a ledger row gets no revenue quest; one that has never seen a star gets no star quest; a metric whose previous window was zero is skipped entirely. An impossible goal is worse than no goal.
- **Capped.** At most six generated quests run at once. A board of twelve is a chore list.

Generation is for the *current* window, on whatever day the server happens to start, rather than only on Monday morning — a server booted on a Wednesday should still have a board, and the dedupe key makes the extra runs free.

### Custom quests

Anything the generator will not write, you can set yourself: any metric, any target, any window, optionally scoped to an app or a source. Custom quests carry a small `custom` tag, and they are the only kind you can delete.

### Completion, and the end of a window

Progress is checked every minute, and immediately after any ingest — a drop that pushes a quest over its target should pay out while it is still on screen, not up to a minute later.

When progress reaches the target the quest is marked completed (with an UPDATE guarded on "still active", so two racing checks cannot both win), and a `quest_complete` event is ingested, keyed `loot:quest_complete:<id>` so not even a restart mid-flight can mint two drops. The payload carries `quest_id`, `title`, `metric`, `target`, `value`, a pre-formatted `detail` line, and `scope` — `week` or `month`, which is what the rules file matches on:

| scope | rarity | XP |
|---|---|---|
| a week's quest | rare | 200 |
| a month's quest | epic | 500 |

The completion drop lands in the feed with its sound, the Hearth counts its XP, and the card on the Quests tab glows for a few seconds as it arrives.

The day after a window closes, any quest still active becomes `expired`: status changes, progress is kept, and **nothing else happens**. It appears under **Recent** as `ended · 62%`, in grey, next to the ones that finished.

## Mysteries

A mystery is one flagged day: something your numbers did that the days around them do not explain.

### How a day gets flagged

Every series is measured against its own recent past with a **robust** baseline — the median of the trailing 28 days and the median absolute deviation around it. Robust matters here because the thing being looked for (one enormous day) is exactly the thing that would poison a mean and a standard deviation into never noticing it again.

The score is the modified z-score, `0.6745 × (x − median) / MAD`, and a day must clear **|z| ≥ 3.5** *and* move by an amount worth mentioning — ten installs, five units, ten of your display currency. Both halves are needed: a series that idles at 1 and jumps to 4 is statistically enormous and practically nothing.

| kind | what it means |
|---|---|
| `spike` | a day that broke upwards away from its baseline — *"Google Play installs tripled on Fri Aug 14 — why?"* |
| `dip` | the same, downwards — *"App Store revenue dropped 70% on Mon Aug 10"* |
| `refund_spike` | at least three refunds, and at least three times the usual |
| `record` | higher than any of the previous 28 days by at least a quarter, on a series with real history behind it |
| `new_country_cluster` | three or more countries founded on the same day |
| `silence` | a source that reported on at least five of seven days and then went quiet for two completed days or more — *"App Store has gone quiet since Sat Aug 15"* |

The series examined are installs, revenue, units, refunds and cancellations, per `(source, app)`.

Three details keep the casebook short enough to actually read:

- **One question per day.** At most one mystery is raised per `(source, app, day)`; a day whose refunds exploded is reported as a refund story rather than as three overlapping ones.
- **A run is one story.** A week where refunds stayed high asks its question once — a day is only flagged when the day before it was *not* flagged the same way, decided from the data rather than from where the detection window starts, so tomorrow's run reaches the same conclusion about the same week.
- **Completed days only.** Today is still accumulating, and a half-finished day looks exactly like a collapse.

The detector runs at startup and hourly, over the last 14 completed days, and is idempotent: every mystery is keyed on `(kind, source, app, metric, day)`, so re-reading the same fortnight every hour costs a few queries and creates nothing.

`silence` is the one mystery with a usual answer, and it is the reason the feature earns its place even if you never solve another: a source that reported every day and then stopped is almost always an expired key, a rotated credential or a lapsed bucket permission.

### Solving one

Each open mystery carries a 28-day sparkline with the flagged day marked, the observed and expected figures, and one line on what tripped the detector. Under it is a single text field.

**Solve** records what you think happened and ingests a `mystery_solved` event (`loot:mystery_solved:<id>`) — an **uncommon** drop worth **75 XP**. **Dismiss** closes it quietly: no drop, no XP, no judgement.

Solved mysteries go to the **Notebook**, with your note. That list is the actual point of the feature: after a few months it is a record, in your own words, of what really moves your numbers.

## API

| endpoint | purpose |
|---|---|
| `GET /api/quests` | `{active: [...], recent: [...]}` — active quests with fresh progress, and the last 20 completed or ended |
| `POST /api/quests` | set a custom quest |
| `DELETE /api/quests/{id}` | remove a custom quest (generated ones answer 400) |
| `GET /api/mysteries` | `{open: [...], resolved: [...]}` — the last 20 resolved |
| `POST /api/mysteries/{id}/solve` | `{"note": "…"}` — records the note, pays a drop |
| `POST /api/mysteries/{id}/dismiss` | closes it quietly |

`GET /api/stats` also carries `open_mysteries` and `active_quests`, which is what puts the small count badge on the Quests tab.

Creating a quest:

```bash
curl -X POST http://localhost:8080/api/quests \
  -H 'Content-Type: application/json' \
  -d '{"metric":"installs","target":5000,"window":"week","app":"net.tidewatch.Tidewatch"}'
```

`window` is `"week"`, `"month"`, or an object with explicit inclusive days:

```jsonc
{"metric": "revenue", "target": 2500, "window": {"start": "2026-09-01", "end": "2026-09-14"}}
```

A quest comes back like this:

```jsonc
{
  "id": "01M0C2YXD3BRNFG233MMKT1FCT",
  "kind": "auto",                     // or "custom"
  "metric": "revenue",
  "target": 5900,
  "app": "", "source": "",            // empty means "everything"
  "window_start": "2026-08-17",
  "window_end": "2026-08-23",         // inclusive
  "title": "Beat last week's revenue · $5,900",
  "status": "active",                 // active | completed | expired
  "value": 1324.22,
  "pct": 0.2244,                      // 0..1, clamped
  "days_left": 6,                     // today counts
  "xp": 0,                            // what the completion drop paid
  "created_at": "2026-08-19T04:02:38.622Z",
  "completed_at": null
}
```

And a mystery:

```jsonc
{
  "id": "01M0C31Q0YT0NPS0P3Z7WPXTQ2",
  "kind": "spike",                    // spike | dip | refund_spike | record | new_country_cluster | silence
  "source": "googleplay",
  "app": "Orbit Weather",
  "metric": "installs",
  "day": "2026-08-14",
  "observed": 1782,
  "expected": 515.5,                  // the 28-day median
  "z": 10.81,
  "title": "Google Play installs tripled on Fri Aug 14 — why?",
  "detail": {
    "series": [{"day": "2026-07-18", "value": 502}, "…28 days ending on the flagged one…"],
    "baseline": 515.5,                // median
    "deviation": 79,                  // median absolute deviation
    "ratio": 3.457,                   // observed / expected
    "unit": "count",                  // or "money"
    "why": "1,782 against a 28-day median of 516 (z 10.8)"
  },
  "status": "open",                   // open | solved | dismissed
  "note": "",
  "created_at": "2026-08-19T04:02:38.700Z",
  "resolved_at": null
}
```

## Websocket

Two new message types ride the existing `/ws` stream:

```jsonc
{"type": "quests"}      // the board changed
{"type": "mysteries"}   // the casebook changed
```

Both are deliberately empty: a nudge to refetch, exactly like `chest`. It keeps a whole board out of every frame, and a client that missed one is corrected by the next.

## Rules

The two rules live in the `Loot` section of [`internal/rules/default.yaml`](../internal/rules/default.yaml), alongside the settlement rule — Loot's own events, as opposed to a store's:

```yaml
  - name: quest-complete-month
    match: {source: loot, kind: quest_complete, payload_match: {scope: month}}
    then: {rarity: epic, title: "Quest complete: {{.Payload.title}}", subtitle: "{{.Payload.detail}}", xp: 500}

  - name: quest-complete
    match: {source: loot, kind: quest_complete}
    then: {rarity: rare, title: "Quest complete: {{.Payload.title}}", subtitle: "{{.Payload.detail}}", xp: 200}

  - name: mystery-solved
    match: {source: loot, kind: mystery_solved}
    then: {rarity: uncommon, title: "Mystery solved", subtitle: "{{.Payload.title}}", xp: 75}
```

Point `rules_path` at your own file to change the rarities, the XP or the wording. Nothing else about quests or mysteries depends on them.

## Demo mode

`loot serve --demo` runs the generator and the detector immediately after seeding, so a demo opens on a board with quests already in progress and a handful of open questions about the history it just invented — the launch-day install spike, the week the refunds went wrong, and the best day the business ever had.

## Storage

Two tables, both added in migration `0003_quests`:

- `quests` — kind, metric, target, optional app/source, inclusive window, title, status, cached progress, XP, and a unique `dedupe_key` that makes generation idempotent.
- `mysteries` — kind, source, app, metric, day, observed, expected, z, title, a JSON `detail` blob (the sparkline and its context), status, your note, and a unique `dedupe_key` of `(kind, source, app, metric, day)`.

Neither table is ever read by the vault, the Hearth or the feed: quests and mysteries are a *reading* of the events already stored, never a new fact about them. Deleting both tables and letting them regenerate would lose nothing but your notes.
