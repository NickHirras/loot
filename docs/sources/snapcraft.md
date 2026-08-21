# Snapcraft (Snap Store)

**Polling, every 6 hours.** Loot reads the Snap Store's own developer metrics —
the same numbers behind `snapcraft metrics` and the dashboard's graphs — and
turns each completed day into one chest drop per snap, plus the silent counters
that feed the vault and the Hearth.

There is no webhook and no public API here: everything comes from
`https://dashboard.snapcraft.io/dev/api/snaps/metrics`, authenticated with a
macaroon you export once from your own account.

> Verified against snapcraft 9.0.1: `export-login` writes base64 of `{"t":"u1-macaroon","v":{"r":…,"d":…}}`; Loot reads that, the older INI/JSON forms, and candid `{"t":"macaroon","v":…}` exports.

## 1. Export a login

Snapcraft can hand out a *scoped, expiring* credential that does nothing except
read metrics. Log in on a machine with a browser, then:

```bash
snapcraft login
snapcraft export-login \
  --snaps=my-snap,my-other-snap \
  --acls=package_access,package_metrics \
  --expires=2027-01-01T00:00:00 \
  ./snapcraft-login
```

| flag | why Loot wants it |
|---|---|
| `--snaps` | comma-separated snap names; the credential can touch nothing else |
| `--acls` | `package_metrics` reads the metrics, `package_access` resolves a snap **name** to its `snap_id` |
| `--expires` | ISO 8601 date/time; the credential stops working then, which is the point |

`--channels` also exists (it scopes releases) and is irrelevant here.

> [!NOTE]
> **Why two ACLs?** The metrics endpoint takes an opaque `snap_id`, not a name.
> Loot resolves the name once via `GET /dev/api/snaps/info/{name}`, which needs
> `package_access`, and caches the id in its state forever after. If you export
> with `--acls=package_metrics` alone, Loot falls back to `GET /dev/api/account`
> to find the id; that works on many accounts but not all, so exporting both
> ACLs is the reliable path.

The resulting file is a credential. `chmod 600` it, keep it out of your repo,
and mount it read-only in a container.

## 2. Point Loot at it

```yaml
sources:
  snapcraft:
    snaps:
      - my-snap
      - my-other-snap
    login_path: "/etc/loot/snapcraft-login"
    backfill_days: 30
```

Every field has an environment override, so a container needs no config file:

| env | field |
|---|---|
| `LOOT_SNAPCRAFT_SNAPS` | `snaps` (comma-separated) |
| `LOOT_SNAPCRAFT_LOGIN_PATH` | `login_path` |
| `LOOT_SNAPCRAFT_BACKFILL_DAYS` | `backfill_days` |

The section counts as *configured* only when both `snaps` and `login_path` are
set, and Loot checks that the login file exists at startup rather than at the
first poll.

## 3. Check it

```bash
./bin/loot check
```

```
✓ snapcraft    2 snap(s)
```

`check` reads the login file, resolves the first snap's id and asks for a single
day of `daily_device_change`. An empty answer is still a pass — it proves the
credentials were accepted, and a snap published yesterday has no metrics yet.

Failures name the fix:

| what you see | what happened |
|---|---|
| `401 … the exported login has expired or been revoked` | the `--expires` date passed, or you revoked the token — Ubuntu One would not refresh the discharge either (see below). Export a new one. |
| `403 … missing the package_metrics ACL` | you exported without `--acls=package_metrics`. |
| `403 … restricted to other snaps` | the snap is not in the `--snaps` list you exported with. |
| `unknown snap "…"` | the name is wrong — check `snapcraft list`. |
| `502 … the Snap Store is having trouble` | not you. Loot retries on the next poll. |

## Two macaroons, two lifetimes

An exported Ubuntu One login is **two** macaroons with completely different
lifetimes, and it is worth knowing which one you are looking at when something
returns a 401:

- the **root** macaroon, minted by the Snap Store, which lives until the
  `--expires` date you gave `export-login` — months, if you asked for months;
- the **discharge** macaroon, minted by Ubuntu One SSO to prove the root is
  yours, which goes stale in **a day or two** no matter what `--expires` said.

When the discharge goes stale the store answers

```
401 Expired macaroon (age: 139631 seconds) (macaroon-needs-refresh)
```

which reads like "your login expired" but is not: the root is fine, and the fix
is to ask SSO for a new discharge, not to export a new login. The snapcraft CLI
does that silently on every command, which is why nobody running `snapcraft`
ever sees this.

**Loot does the same.** On a `macaroon-needs-refresh` 401 it POSTs the *unbound*
discharge to `https://login.ubuntu.com/api/v2/tokens/refresh`, binds the new
discharge it gets back to the same root, and retries the request — during a poll
and in `loot check` alike. The refreshed discharge is kept in Loot's own source
state so a restart does not begin with another 401; **your login file is never
rewritten**, because it is your export, not Loot's cache. Replace it and Loot
notices at the next poll and throws away the discharge it had refreshed for the
old one.

So you re-run `export-login` only when the *root* is gone — the `--expires` date
passed, or you revoked the credential — which is the one case where the refresh
itself is refused and the `401 … expired or been revoked` message above is the
honest answer. Not every 1.6 days.

Two credential shapes cannot be refreshed, and for them that 401 is terminal:
candid tokens (no discharge at all) and a snapcraft 7+ export that is already a
finished `Macaroon root=…, discharge=…` header, whose discharge was bound at
export time. Prefer format 1 below if you want unattended refresh.

## Credential formats

`export-login` has changed shape across snapcraft releases. Loot recognises
three, and says which one it used in debug logs:

1. **Ubuntu One / legacy (snapcraft ≤ 6)** — an INI file with a
   `[login.ubuntu.com]` section holding `macaroon` and `unbound_discharge`. This
   is the format the v1 API documents, the one Loot is tested against, and the
   one to prefer. The exported discharge is *unbound*, so Loot rebinds it to the
   root macaroon (the same thing pymacaroons' `prepare_for_request` does) before
   sending `Authorization: Macaroon root=…, discharge=…`. That is the only
   cryptography involved and it uses nothing but the Go standard library. Since
   the unbound discharge is kept, this is the format Loot can refresh
   automatically.
2. **snapcraft 7+ (craft-store, Ubuntu One)** — the already-bound credential
   serialized as the finished header value, usually base64-wrapped for
   `SNAPCRAFT_STORE_CREDENTIALS`. Used verbatim. A base64-wrapped copy of format
   1, and a JSON object carrying `macaroon`/`unbound_discharge`, both work too.
3. **Candid (`SNAPCRAFT_STORE_AUTH=candid`)** — a bare macaroon token, sent as
   `Authorization: Macaroon <token>`. **Best effort only**: candid credentials
   are minted for the store's v2 API and the v1 metrics endpoint may refuse
   them. If it does, you will see the store's own 401/403 in `loot check`;
   export an Ubuntu One login instead.

## What Loot reads

One request per snap per poll, asking for two metrics:

| metric | what it is |
|---|---|
| `daily_device_change` | three series — `new`, `continued`, `lost` — one value per day |
| `installed_base_by_country` | one series per ISO 3166-1 alpha-2 country, each value the devices running the snap in that country **that day** |

The store also publishes `installed_base_by_channel`,
`installed_base_by_operating_system`, `installed_base_by_version`,
`weekly_device_change` and the `weekly_installed_base_by_*` family. None of them
carry a fact the feed can turn into a drop, and every extra filter is another
series downloaded four times a day, so Loot does not ask for them.

## What Loot emits

Per snap, per **completed** day (today is never emitted — the store has not
published it):

| kind | quantity | silent? | dedupe key |
|---|---|---|---|
| `install` | `new` devices | silent | `snapcraft:installs:<snap>:<day>` |
| `active_devices` | `new + continued` | silent | `snapcraft:active:<snap>:<day>` |
| `lost` | `lost` devices | silent | `snapcraft:lost:<snap>:<day>` |
| `install` (one per country) | derived country gain | silent | `snapcraft:installs:<snap>:<day>:<country>` |
| **`installs_day`** | `new` devices | **chest drop** | `snapcraft:installs_day:<snap>:<day>` |

`installs_day` is the only event that produces a drop, and it goes into that
day's chest rather than the live feed — the same treatment Google Play's install
days get, so a 30-day backfill fills chests instead of spraying a month of
installs across the feed. The `installs-day-record` rule makes your best install
day **epic** for free.

`lost` events are recorded but currently invisible: they carry no drop and the
UI does not read them yet. They are stored now so the history exists when it
does.

The first poll of a snap backfills `backfill_days` (30 by default). Set it to
`0` to emit nothing historical and only report days from here on; for a one-off
bootstrap from a fixed date, `./bin/loot serve --since 2026-01-01`.

## Caveats

### Metrics lag by a day or two

The store publishes a day's figures some hours — occasionally a couple of days —
after that day closes, and returns `null` values for the buckets it has not
filled in yet. Loot skips an all-null day **without moving its cursor**, so the
day is picked up on a later poll rather than being lost. A `NO_DATA` metric
status (a snap with no installs yet) is treated the same way. Neither is an
error and neither lights up `last_error`.

The cursor advances only to the end of the **contiguous run** of published days,
not to the newest day with data anywhere in the response. Given a published
Monday, an unpublished Tuesday and a published Wednesday, the cursor stops at
Monday — Wednesday's events are still emitted, and Tuesday is asked for again
until it lands. (Days the store has no bucket for at all are a different thing:
it truncates the range to what it has rather than padding it with nulls, so a
missing bucket is not treated as a hole.)

### Per-country installs are derived, and they undercount

This is the important one. `installed_base_by_country` is a **base**, not an
arrival count: it says how many devices in each country were running the snap
that day. Loot derives a country's daily installs as

```
installs[country][day] = max(0, base[country][day] − base[country][day−1])
```

which is why the country filter is always fetched one day further back than the
device filter.

Two consequences worth knowing:

- **Losses mask installs.** A country that gained 5 devices and lost 7 on the
  same day reads as **0**, not 5. The derived figure is a *net* gain, so it
  never overstates and frequently understates.
- **The country numbers do not sum to the day's installs.** The authoritative
  install count is the un-countried `daily_device_change` `new` series, which is
  emitted separately. Do not expect the per-country events to add up to it.

The derived events exist because they are what founds **settlements** on the
Hearth — "a country where the snap grew today" is exactly the question the globe
asks. Every such event's payload carries `"derived": true` along with the two
base readings it came from, so a surprising number can always be checked.

A day whose preceding bucket is missing (the first day of a window, or a gap in
the store's data) produces no country events at all: differencing across a gap
would turn a week's growth into a one-day spike.

### Devices, not people

Snap metrics count **devices**. Someone with a laptop and a desktop is two
installs here, where Google Play's "user installs" would call them one. There is
no user-level series to switch to.

### Country codes

Devices the store could not place are reported under a non-ISO series name
(`??`). Those are skipped rather than invented into a country; they still appear
in the day's total `install` event, which carries no country and therefore lands
aboard the Hearth's **fleet** — for Snapcraft, an offshore rig in the North Sea
called *Snapcraft Platform Nine*.
