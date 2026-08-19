# Apps: the global scope filter

Loot is for somebody who ships more than one thing. Every panel — the feed, the
vault, the globe, the quest board, the trophy wall — answers a question about
*everything you ship*, and the moment you have three apps that is one question
too broad. The **app scope** narrows all of them at once.

It is one control in the header ("All apps ▾"), one query parameter
(`?app=`), and one block of config (`apps:`).

---

## The problem it solves

Every source names an app whatever its own console names it:

| Source | What it calls one app |
| --- | --- |
| App Store Connect | the report **Title** (`Nistis: Fasting Timer`) — or the numeric Apple ID |
| RevenueCat | the app id (`app5525946104`), or the display name you mapped it to |
| Google Play | the package name (`com.example.nistis`) |
| Flathub | the Flatpak id (`net.tidewatch.Tidewatch`) |
| GitHub | `owner/repo` |
| Snapcraft / Microsoft Store | the snap name / the Store ID |

Those are five names for one product. A dashboard that shows them as five apps
is a dashboard you cannot scope, so Loot resolves them to a **product**: the
canonical name you actually call the thing.

## Config

Top-level in `loot.yaml`:

```yaml
apps:
  - name: Nistis
    match:
      appstore: ["Nistis: Fasting Timer", "6763687102"]
      revenuecat: ["app5525946104", "Nistis"]
      googleplay: ["com.example.nistis"]
  - name: Macro Trainer
    match: {appstore: ["Macro Trainer"]}
```

`match` maps a **source id** to the raw app names that source uses. Every
source id Loot knows about works as a key: `appstore`, `googleplay`,
`revenuecat`, `microsoftstore`, `snapcraft`, `flathub`, `github`, `webhook`,
`crash`, `sentry`, `playvitals`.

Matching is **exact and case-insensitive** (surrounding whitespace is ignored).
There are no wildcards, on purpose: a pattern that silently swallowed a new app
would be a mapping you could not audit.

Three rules decide what an event's product is:

1. **A product's own name always resolves to itself.** Re-running the resolver
   over already-mapped rows is a no-op.
2. **A raw name listed under the source wins**; failing that, a raw name listed
   under *any* source resolves too. The fallback is not sloppiness — Loot mints
   events of its own (a settlement, a quest completion, a boss) under the
   reserved source `loot`, carrying the app name of whichever real source
   triggered them. Without it, a settlement found in an App Store row would
   invent a phantom product beside the real one.
3. **Anything unmapped keeps its raw name.** It still appears, still counts,
   and is still selectable in the scope selector — under a thin "unmapped"
   divider. The mapping is a convenience, never a gate.

An event with no app at all (an achievement) has **no product**. It is
*realm-wide*: it belongs to everything you ship, and it shows up in every
scope.

### Nothing else to do

`apps:` is read at every startup, and Loot recomputes `events.product` for the
whole database as it boots (`RemapProducts`). Editing the config is the whole
of the work: no migration, no re-import, no stale scope to discover a week
later. It is cheap however much history there is, because it works on the
distinct `(source, app)` pairs — a database with two hundred thousand events
has perhaps a dozen — and only writes the pairs whose answer actually moved.

## The CLI

```
loot apps          # what your sources have reported, and what it maps to
loot apps remap    # recompute every event's product, then print the same table
```

`loot apps` is the answer to "why is my dashboard showing four apps when I ship
two?":

```
PRODUCT          SOURCE      APP AS REPORTED          EVENTS  FIRST SEEN
Lumen Notes      appstore    Lumen Notes              2757    2026-04-21
                 googleplay  Lumen Notes              4937    2026-04-21
                 revenuecat  Lumen Notes              511     2026-04-21
Tidewatch *      flathub     net.tidewatch.Tidewatch  240     2026-04-21
                 googleplay  Tidewatch                4430    2026-04-21

* 2 app(s) are not mapped to a product. They still show up, under the
  name their source reported. To group them, add to loot.yaml:

apps:
  - name: Tidewatch
    match:
      flathub: ["net.tidewatch.Tidewatch"]
      googleplay: ["Tidewatch"]
```

The suggestion block at the bottom is meant to be pasted. Both commands read
the database directly (as `loot fx` does); `remap` writes to it, so do not run
that against a database a server is writing to — the server does the same remap
at every startup anyway.

## The API

Every read endpoint takes **`?app=<product>`**, URL-encoded. Absent or empty
means "all apps", so a client that has never heard of scoping keeps working.

| Endpoint | Scoped? |
| --- | --- |
| `GET /api/drops` | yes — this product **plus** realm-wide drops |
| `GET /api/stats` | yes (counts); `apps` and `app` always present |
| `GET /api/vault/summary` | yes — strictly, money only |
| `GET /api/hearth` | settlements, population, recent — yes; **era and XP — no** |
| `GET /api/quests` (and `POST`) | yes — this product's quests plus realm-wide ones |
| `GET /api/mysteries` | yes |
| `GET /api/codex` | records and totals yes; **achievements no** |
| `GET /api/recap` | yes |
| `GET /api/bosses` | yes |
| `GET /api/chest`, `POST /api/chest/open` | **no** — accepted and ignored |
| `GET /api/apps` | n/a — it is the list you pick a scope *from* |

An unknown product is **not an error**. It scopes to something with no events
and answers empty, which is what a link to an app you have since renamed should
do rather than 400 at somebody who followed a bookmark.

### `GET /api/apps`

```json
{
  "products": [
    {
      "name": "Nistis",
      "sources": {"appstore": ["Nistis: Fasting Timer"], "revenuecat": ["app5525946104"]},
      "configured": true,
      "events": 8448,
      "first_seen": "2026-04-21"
    }
  ],
  "unmapped": [
    {"source": "flathub", "app": "net.example.Unclaimed", "product": "net.example.Unclaimed",
     "events": 240, "first_seen": "2026-05-02"}
  ],
  "scope": ""
}
```

`products` lists configured products **even before they have an event**, so a
mapping that has not matched anything yet is visibly present rather than
mysteriously absent. `unmapped` is the interesting half: on a correctly
configured Loot it is empty, and on a fresh one it is the list to paste into
`apps:`.

## What scopes, and what deliberately does not

Two filters, and the difference is the whole design.

**Strict** (`product = ?`) — the ledger: revenue, units, refunds, installs,
settlements, population, per-country money. Every one of those rows came from a
store's report about one app, so there are no product-less rows to include, and
including them would be adding somebody else's revenue to yours.

**Loose** (`product = ? OR product = ''`) — drops and the feed. Loot's own
realm-wide drops (an achievement, a global quest completing) belong in every
scope: they are about the whole hoard, not about one app. Another product's
drops are excluded, which is the point.

Four things stay global on purpose:

- **XP, the level and the era.** They are the account's standing, earned across
  everything you ship. A level that halved when you looked at one of your apps
  would be telling you something untrue about yourself — and the header and the
  era bar are on screen together, so they have to agree.
- **Achievements.** A trophy is yours, not the app's. The wall is the same in
  every scope; the records and totals beside it are not.
- **Chests.** A chest is one daily ritual over everything you ship. Three
  chests a day, one per app, would be three times the ceremony for the same
  morning. `?app=` is accepted and ignored, so a scoped client needs no special
  case.
- **Quest *progress*.** A quest measures whatever its own `app` field says. A
  realm-wide quest measures the realm even when the board it appears on is
  scoped: narrowing its progress would change the goal depending on which tab
  you were looking at, which is not a goal at all.

## The UI

The selector sits between the logo and the tabs. It shows configured products
first, then anything a source reported that no mapping claims, under a thin
**unmapped** divider — selectable too.

- The scope is remembered in `localStorage` (`loot.scope`) **and** written into
  the URL hash (`#/vault?app=Nistis`), so a scoped view is a link you can send.
  The hash wins on load: a link is an explicit request and must beat a
  remembered preference.
- Changing it refetches every store at once — feed, stats, vault, hearth,
  quests, codex, bosses. The feed is *replaced* rather than merged, because the
  cards on screen belong to the scope you have just left.
- Tab links carry the scope, so navigating never loses it.
- When scoped, the product name appears beside "Loot" in the header, once and
  quietly, so a screenshot is unambiguous.

### Live drops out of scope

A live drop for another app **does not render and does not play a sound**.
Scope means focus. But it did happen, and a dashboard that silently swallowed
it would be lying by omission — so it becomes a small **"+3 elsewhere"** pill on
the selector. It clears when the scope changes (or when you re-pick the scope
you are already in).

Chest cascades are the exception: a chest is global, and dropping cards out of
the middle of a reveal the server is still pacing would strand it. The whole
cascade plays, whatever scope you are in.

## Quest timing (related, and the reason this shipped now)

Two fixes went in alongside the scope, both from the same real failure. Quests
were generated at startup, *before* the sources' first poll — so the generator
saw an empty database, and the App Store backfill that landed forty seconds
later completed the fresh quests instantly. "Settle 2 new countries" paid an
epic drop, a sound and XP for countries settled months ago.

1. **The scheduler now exposes `FirstRoundDone()`**, closed once every polling
   source has finished its first poll (or after a ten-minute cap; webhook-only
   sources count as done immediately). `quests.Service.Run` waits for it before
   the first generation. Demo mode seeds its own history first, so it does not
   wait.
2. **A metric with fewer than seven days of history is skipped.** "Beat last
   week by 5%" is only a target if there was a last week; seven days is the
   shortest window containing one of each weekday. The rule is per metric, so a
   long install history still gets an install quest while one day of revenue
   gets no revenue quest.
