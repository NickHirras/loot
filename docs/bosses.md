# Bosses

*Quest 3 of Loot: crashes, but as a fight you can win.*

Crashes are the least fun data an app developer owns, and the thing dashboards handle worst. The usual treatment is a red badge that goes up, sits there, and slowly teaches you to ignore it. By the third week the badge is furniture.

So Loot does not badge crashes. It gives them a name, a health bar, and an ending.

When a day's crashes break away from an app's own baseline, a **boss** spawns:

> **The Null-Dereferencing Wyrm of v2.3.1**
> NullPointerException in SyncWorker.onRun
> ███████████░░░░ **123** / 312 crashes — −61% since it appeared

Its hit points are that day's crash count. Every completed day after that sets HP to *that* day's count — so shipping a fix and watching the new build reach your install base visibly drains the bar. Get it to a tenth of its opening strength for two days running and the boss is **slain**, which pays an epic drop with the same sound and the same fanfare as a very good sales day.

Four rules keep this from turning into the red badge it replaces.

- **The spawn is the only cursed moment, and it pays nothing.** A boss appearing is news, not a punishment, and paying you XP for a crash would be a strange thing to do.
- **A fight that gets worse *enrages* — once.** It is said quietly, one time per boss, ever. A crash that climbs for five days should not make five cursed noises about a thing you already know.
- **A boss that is still standing is just still standing.** There is no overdue state, no red, no escalation, no nagging. A dashboard that spends a fortnight scolding you has already spent the feeling the kill was supposed to give you.
- **A boss whose source goes quiet *fades*.** Silent, no reward, no blame. Loot lost sight of it; that is Loot's problem, not yours.

Everything lives in the **Boss fights** section at the top of the Quests tab, and the Quests tab grows a small red badge — the only red badge in Loot — while a fight is in progress.

## How a fight works

### The key

A fight is identified by `<source>:<app>:<version>|<issue>`, with `|anr` appended when the failure is an ANR rather than a crash. Whichever of version and issue a source knows about goes in; Play vitals knows versions, Sentry knows issues, the generic webhook can supply both or neither.

A crash and an ANR in the same version are two fights, not one: different symptom, different fix, and adding their counts together gave a health bar that measured neither. Only ANRs take the suffix, so every crash key ever written is spelled exactly as it always was.

The key is unique across the whole table, forever. A fight is one row for its entire life — from the cursed spawn to the epic kill — so history reads as *"you fought this and won"* rather than as a pile of near-identical monsters. A version that starts crashing again months later is a new fight only if it is a new version, which it almost always is.

### The name

The name is generated deterministically from the key: the same crash gets the same name on every machine, forever, with no stored state and no round trip. Three forms, drawn from a word list of technical menace and ordinary fantasy menace:

- *The Null-Dereferencing Wyrm of v2.3.1*
- *Grimjaw the ANR Lich*
- *The Segfault Hydra*

An ANR gets to be called one, because "why did it hang?" is a different question from "why did it die?". Play's numeric version codes render as *build 4812* rather than pretending to be a marketing version.

The word lists are ominous about the **bug** and never about the person who wrote it. There is no *Sloppy*, no *Careless*, no *Amateur*. The monster is the crash; you are the one holding the sword.

### Spawning

The baseline is the same robust one the mystery detector uses: the **median of the trailing 28 days**, measured per `(source, app)` rather than per version — a brand new version has no history of its own, and *"three times what this app usually does"* is the question that means something.

A completed day spawns a boss when any of these holds:

| condition | why |
|---|---|
| crashes ≥ max(**3×** baseline, **20**) | the ratio with a floor under it |
| users affected ≥ **50** *and* crashes ≥ 3× baseline | fifty people hitting the same crash is a boss whatever the ratio says |
| the source sent `"boss": true` | a Crashlytics velocity alert knows something Loot's counts do not |

Both halves of the first rule are load-bearing. A ratio alone would spawn a boss the first time an app that crashes twice a day crashed eight times, which is not a boss, it is a Tuesday. A floor alone would never notice a small app's disaster.

Three more guards:

- **Only completed days.** Today is still accumulating, and a half-finished day looks exactly like a fix that worked.
- **Only the last 14 days.** Older spikes are history; Loot does not reopen them.
- **At least 7 days of the source's own history** before the spawn day. Without it, connecting a crash source to a perfectly healthy app would spawn a boss for its ordinary Tuesday, because its 28-day baseline is 28 days of nothing.

At most five bosses spawn per evaluation pass, so a backfill that lands a month of crash history at once introduces a few monsters rather than forty.

### Draining, and enraging

Each completed day, HP becomes that day's crash count for the same key.

If a day is **worse** than the spawn day, HP rises — but never past **1.5× hp_max**, and the boss is marked *enraged*. Beyond 1.5× the bar has stopped being a health bar and started being a graph, and the number would say nothing the word "enraged" does not already say. The enrage mints exactly one cursed, zero-XP drop, ever.

### Dying

| how | what happens |
|---|---|
| **recovered** — two consecutive *attested* days at ≤ 10% of hp_max | slain |
| **resolved** — the source said the issue was closed (a Sentry `resolved`, a `"resolved": true` on the webhook) | slain |
| **manual** — you clicked *Mark slain* | slain |
| **faded** — the source said nothing at all for 14 days | closed quietly |

Two quiet days rather than one, because one quiet day is a weekend. *Attested* is the load-bearing word: only a `crash_day` heartbeat counts towards the run. A `crash` row about some other issue is not a statement that this one stopped — and counting it as one let a single noisy issue's daily rows spawn a boss for a different issue and slay it in the same pass, paying a legendary drop nobody earned.

The kill is **epic**, worth 500 XP — or **legendary**, worth 1,500, when the fight opened at 500 or more, hit 500 or more people, or took a week or longer to win. Either half qualifies on its own: a crash that hit five hundred people mattered, and so did one you chased for a week.

**Mark slain** exists because you know when you fixed something, and a dashboard that argues with you about it has misunderstood whose data this is. It is available for every boss, not only the ones from push-only sources.

### The difference between quiet and silent

This is the one subtle thing in the whole feature, and the reason Android vitals is the best crash source Loot has.

A push-only reporter can tell Loot a crash *happened*. It can never tell Loot a crash *stopped*, because silence and a broken credential look identical from here. So a polling source that can attest to a quiet day emits a daily `crash_day` heartbeat — *"I looked, and the whole app's total was N"* — including when N is zero. That single event is what makes the difference between:

- **slain**: the source is reporting fine, and this fight's number is now zero. You won.
- **faded**: the source has stopped reporting anything. Loot has no idea, and says so.

Play vitals emits the heartbeat. Sentry and the generic webhook cannot honestly claim to have looked, so their abandoned fights fade rather than claiming a kill nobody earned — and *Mark slain* covers the case where you know better.

## Drops

Bosses travel the same pipeline as everything else, so they land in the feed with a rarity, a colour and a sound.

| event | rarity | XP | title |
|---|---|---|---|
| `boss_spawn` | cursed | 0 | *A boss appears: {name}* |
| `boss_enrage` | cursed | 0 | *{name} enrages* |
| `boss_slain` | epic | 500 | *Boss slain: {name}* |
| `boss_slain` (`scale: legendary`) | legendary | 1,500 | *Boss slain: {name}* |

Fading emits nothing at all — no event, no drop, no sound. It is the second state change in Loot that is deliberately silent, after a quest expiring.

Crash events themselves (`crash`, `crash_day`, `crash_resolved`) are **silent**: stored, counted, never a drop. One drop per crash would be the exact dashboard this feature exists to avoid being.

All of this is in `internal/rules/default.yaml` under *Bosses*, so a custom rules file can retune the rarities and XP without touching Go.

## API

### `GET /api/bosses`

```json
{
  "alive": [
    {
      "id": "01M0CAWQSVQAY2FM9X0DEJM3Q8",
      "key": "playvitals:Lumen Notes:4.2.0",
      "source": "playvitals",
      "app": "Lumen Notes",
      "name": "Torbrak the Off-By-One Gargoyle",
      "title": "NullPointerException in SyncWorker.onRun",
      "version": "4.2.0",
      "issue_id": "",
      "hp_max": 312,
      "hp": 123,
      "users_affected": 91,
      "spawned_at": "2026-08-12T00:00:00Z",
      "spawned_day": "2026-08-12",
      "last_hit_at": "2026-08-19T06:21:16Z",
      "status": "alive",
      "peak_day": "2026-08-12",
      "xp_awarded": 0,
      "pct": 0.394,
      "down_pct": 0.606,
      "days_alive": 8,
      "enraged": false,
      "unit": "crashes",
      "url": "https://play.google.com/console/developers/app/…/vitals/crashes",
      "kind": "crash",
      "series": [{ "day": "2026-08-05", "value": 0 }, "…14 days…"],
      "detail": { "baseline": 6, "why": "312 crashes in a day against a usual 6", "quiet_days": 0 }
    }
  ],
  "recent": ["…the last 20 slain and faded, newest first…"]
}
```

`alive` is ordered biggest fight first — the one worth fixing. `recent` is newest first. Both are always arrays, never `null`.

### `POST /api/bosses/{id}/slay`

Answers `{"boss": {…}}` with the closed fight, including the XP its kill paid. Slaying an already-finished boss is a no-op that returns the stored row, so a double click cannot mint two drops.

### `GET /api/stats`

Grows one field: `bosses_alive`, so the tab badge can turn red before the board itself has been fetched.

### Websocket

`{"type":"bosses"}` — a bare nudge to refetch, exactly like `quests` and `codex`. The board never rides the socket.

## Wiring up crashes

Three sources feed the boss engine. See **[docs/sources/crashes.md](sources/crashes.md)** for the full setup of each.

| source | mode | knows about | can say "it stopped" |
|---|---|---|---|
| **Android vitals** (`playvitals`) | polls every 6h | versions, crashes *and* ANRs | **yes** |
| **Sentry** (`sentry`) | webhook | issues, and `resolved` | no — but resolving in Sentry slays |
| **Generic** (`crash`) | webhook | whatever you post | no — but `"resolved": true` slays |

Any of them alone is enough to get bosses. Android vitals is the one that makes the mechanic complete.

## Internals

| file | what it holds |
|---|---|
| `internal/core/bosses.go` | the `Boss` type, statuses, and the `CrashPayload` schema every crash source writes |
| `internal/bosses/naming.go` | the word lists and the deterministic name |
| `internal/bosses/detect.go` | baselines, spawning, draining, dying |
| `internal/bosses/bosses.go` | the service: the ticker, the debounce, the board and the rewards |
| `internal/store/bosses.go` | the `bosses` table and the crash series queries |
| `internal/server/bosses.go` | the API and its five-second memo |
| `web/src/lib/BossCard.svelte` | the card |

The service runs on the same shape as the Codex: an hourly sweep, plus a debounced nudge from the pipeline whenever a crash event is ingested — so a fix that just landed drains the bar while you are still looking at it, and a poll that ingests four hundred crash rows costs one evaluation rather than four hundred.

Everything is idempotent by construction. The boss key is unique in the table, every event is keyed on the boss id, and every state change is a guarded `UPDATE`. An evaluation may run as often as it likes; the second pass over a settled fight changes nothing and mints nothing.

## Trying it without a crash reporter

`loot serve --demo` opens on a fight already in progress at about 40% HP, and one the demo world won last month. Both are found by the real detector reading a plausible crash series the seeder invented — nothing about the live fight is hard-coded.

With `--dev` you can also drive the whole thing by hand:

```sh
# A month of quiet background, so there is a baseline to break away from.
for d in $(seq 30 -1 4); do
  curl -sX POST localhost:8080/hooks/crash \
    -d "{\"app\":\"com.example.app\",\"version\":\"1.0.0\",\"crashes\":2,
         \"occurred_at\":\"$(date -u -v-${d}d +%F)\"}" > /dev/null
done

# And then the bad build.
curl -X POST localhost:8080/hooks/crash \
  -d "{\"app\":\"com.example.app\",\"version\":\"2.0.0\",\"crashes\":300,
       \"users_affected\":90,\"title\":\"NPE in SyncWorker.onRun\",
       \"occurred_at\":\"$(date -u -v-3d +%F)\"}"
```

(Requires `sources.crash.enabled: true`. On Linux, `date -u -d "-3 days" +%F`.)
