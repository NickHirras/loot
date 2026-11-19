# Snapcraft (Snap Store)

**Polling, every 6 hours.** Loot reads the Snap Store's own developer metrics —
the same numbers behind `snapcraft metrics` and the dashboard's graphs — and
stores each completed day's **weekly installed base**, in total and per
country, as the snap's audience: how many devices have it now.

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
| `weekly_installed_base_by_country` | one series per country, each value the distinct devices seen in that country **in the seven days ending that day** |
| `daily_device_change` | three series — `new`, `continued`, `lost` — comparing each day with the day before |

The store also publishes the installed base by channel, operating system,
version and architecture, daily and weekly. None of them carry a fact Loot
shows, and every extra filter is another series downloaded four times a day.

## What Loot emits

Per snap, per **completed** day (today is never emitted — the store has not
published it). Every one of these is silent:

| kind | quantity | dedupe key |
|---|---|---|
| `audience` (one per country) | weekly devices in that country | `snapcraft:audience:<snap>:<day>:<country>` |
| `audience` (no country) | weekly devices in total | `snapcraft:audience:<snap>:<day>` |
| `device_new` | devices seen that day and not the day before | `snapcraft:installs:<snap>:<day>` |
| `active_devices` | `new + continued`: devices seen that day | `snapcraft:active:<snap>:<day>` |
| `lost` | devices seen the day before and not that day | `snapcraft:lost:<snap>:<day>` |

The `audience` rows are the ones that matter. The Hearth's population for a
snap is its **newest** snapshot — nothing is summed — so a settlement is as big
as the store says it is this week, and shrinks when devices go away.

The source produces no drops of its own. The pipeline mints two kinds on its
behalf, both into that day's chest:

- a **settlement** the first time a country is seen at all;
- an **audience milestone** the first time a snap's weekly total reaches a rung
  of the ladder (5, 10, 25, 50, 75, 100, 150, 200, 250, 300, 400, 500, 750,
  1,000 …). Only upwards: a snap that dips below a rung and recovers is not
  congratulated twice.

The first poll of a snap backfills `backfill_days` (30 by default). Set it to
`0` to emit nothing historical; for a one-off bootstrap from a fixed date,
`./bin/loot serve --since 2026-01-01`.

## Caveats

### `new` is not installs

This is the important one, and Loot got it wrong for a while.

`daily_device_change` compares one day with the day before it and nothing else.
`new` means "seen today, not yesterday"; `lost` means "seen yesterday, not
today". A laptop that stays shut over the weekend is lost on Saturday and new
again on Monday. Nothing in the Snap Store's metrics says how many devices
installed a snap for the first time.

Loot used to store `new` as an `install` and add it up. A snap with a steady
base of about 170 devices, 15–25 of which come and go each day, read as well
over a thousand "installs" within three months, and the Hearth showed that as
its population. The series is still recorded, as `device_new`, because it is
what the store said — but nothing sums it, it feeds no quest or record, and it
produces no drop.

A database from before the change is migrated in place: its old Snapcraft
`install` rows become `device_new` (and `country_gain` for the per-country
differences Loot used to derive), so they stop counting everywhere at once. The
`installs_day` drops those days produced are left as they were; they are
history, and XP is never taken back.

### Why the weekly base, not the daily one

The daily installed base only counts machines that happened to be switched on
that day, and for a desktop app it swings by a third between a Tuesday and a
Sunday. The weekly base is the closest thing the store has to "how many devices
have this".

### Devices, not people

Snap metrics count **devices**. Someone with a laptop and a desktop is two
here, and so is every VM and CI machine that installs the snap. There is no
user-level series to switch to.

### Devices the store cannot place

The per-country series are named by ISO 3166-1 alpha-2 code, except for the
devices the store did not attribute to a country, which it files under names
that are not codes (`??`, `_INTERNAL`). Those can be a large share: the store
moved more than half of one snap's devices into `_INTERNAL` overnight in
November 2026, with the total unchanged.

Loot never invents a country for them. They are counted in the snap's total —
which is why the total is the authoritative figure — and the difference between
the total and the placed rows sails with the Hearth's **fleet**, aboard
*Snapcraft Platform Nine*. The total row's payload records both halves as
`placed` and `unplaced`.

### Metrics lag by a day or two

The store publishes a day's figures some hours — occasionally a couple of days —
after that day closes, and returns `null` for the buckets it has not filled in
yet. A day that is all null is skipped **without moving its cursor**, so it is
picked up on a later poll. A `null` for a single country on a day that is
otherwise published just means nobody was there. A `NO_DATA` metric status (a
snap with no installs yet) is treated the same way. None of it is an error.

Each metric has its own cursor, and each advances only to the end of its
**contiguous run** of published days. Given a published Monday, an unpublished
Tuesday and a published Wednesday, the cursor stops at Monday — Wednesday's
events are still emitted, and Tuesday is asked for again until it lands.

### Turning it on for a snap Loot already polls

Audience snapshots were added after the fact, so a snap with existing state has
a device cursor and no audience cursor. On the next poll it backfills
`backfill_days` of snapshots for that snap alone, without re-emitting anything
it already has. Expect its settlements to **drop** to their real size, the
fleet to change, and one milestone drop for the highest rung the snap has
already reached.
