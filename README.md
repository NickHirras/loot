<div align="center">

# ◆ Loot

**A self-hosted, gamified dashboard for indie app developers.**

Every install, subscription and cancellation becomes a *drop* — with a rarity, a colour and a sound.

[![CI](https://github.com/NickHirras/loot/actions/workflows/ci.yml/badge.svg)](https://github.com/NickHirras/loot/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/NickHirras/loot?label=release&color=a855f7)](https://github.com/NickHirras/loot/releases/latest) [![Image](https://img.shields.io/badge/ghcr.io-nickhirras%2Floot-3b82f6?logo=docker&logoColor=white)](https://github.com/NickHirras/loot/pkgs/container/loot) [![License](https://img.shields.io/github/license/NickHirras/loot?color=64748b)](LICENSE)

<img src="docs/screenshots/hearth.png" alt="The Hearth: a turning globe with a lit settlement for every country you have sold in, beside an era ladder and a settlement list" width="100%">

<sub>The Hearth, on demo data. Every screenshot here is `loot serve --demo` — see below.</sub>

</div>

---

Shipping an indie app means watching numbers scattered across half a dozen dashboards that all look like accounting software. Loot collects those numbers into one place and treats them like what they actually feel like: loot. A new subscriber is an **uncommon** drop. Their first annual plan is **rare**. Your best install day ever on Flathub is **epic**. The first sale from a country you have never sold in before is a **new settlement**. A cancellation is **cursed**, and it sounds like it.

Money that arrives a whole day at a time — App Store and Play sales reports — does not interrupt the feed. It goes into a **daily chest** that you open when you are ready, and the drops cascade out one by one, quietest first, so the day ends on its best news. Every amount is also converted into one display currency, so the **vault** can answer "how did this month go?" in a single number.

Every country you sell in becomes a settlement on a slowly turning globe — the **Hearth** — that grows from an outpost to a metropolis as customers arrive, and flies each new drop home to your capital as an arc of light. Leave it full screen on a spare monitor and it will tell you how the day is going without you reading a single number.

Loot is a single Go binary with an embedded Svelte dashboard and a SQLite database. There is no cloud service, no account, and no telemetry — you run it on a VPS, a Raspberry Pi or your laptop, point your webhooks at it, and leave the feed open on a second monitor. Sources push or poll into one pipeline: every event is deduplicated, stored, classified into a rarity by a YAML rules engine, and streamed live to every connected browser over a websocket. `loot tail` does the same thing in your terminal, and rings the bell when something good lands.

<div align="center">

| rarity | meaning | example |
|---|---|---|
| ⬜ **common** | routine good news | a renewal, an ordinary install day |
| 🟩 **uncommon** | genuinely nice | a new subscriber |
| 🟦 **rare** | worth looking up for | an annual plan, a first-ever country |
| 🟪 **epic** | a record | best install day you have ever had |
| 🟨 **legendary** | reserved for the big ones | (yours to define) |
| 🟥 **cursed** | bad news | cancellation, billing issue, expiration |

</div>

## Try it in 10 seconds (demo mode)

Loot ships with a whole fictional business inside it. `--demo` fills a **separate** database with four months of plausible history for three invented apps — an App Store subscription business, a weather app that spikes at weekends, a Linux tide clock on Flathub — and then keeps emitting live events while you watch.

```bash
docker run --rm -p 8080:8080 -e LOOT_DEMO=1 ghcr.io/nickhirras/loot:latest
# or, from source:
make build && ./bin/loot serve --demo
```

Then open <http://localhost:8080>. You get a feed with a few thousand drops behind it, a vault with real curves in it, a globe with forty-odd settlements, an era well into **Kingdom**, and **yesterday's chest still shut**, because opening one is the best thing Loot does and you should get to do it.

What you are looking at:

- **Nothing real is touched.** Demo mode writes to `<data_dir>/demo.db` and never opens `loot.db`. It configures no sources, so it never polls a store, never reads a credential and never accepts a webhook. Every event it stores it invented — but it invented them in the shape the real App Store, Google Play, Flathub and RevenueCat sources emit, and pushed them through the same pipeline, so the drops, chests, settlements and XP are all genuinely Loot's.
- **The same binary and the same image.** There is no separate demo build; `LOOT_DEMO=1` (or `demo.enabled: true`) is all it takes, and the container needs no volume.
- **It is reproducible.** The world is generated from a fixed seed (`demo.seed`), so the same seed always produces the same history and the same screenshots.
- **It keeps up.** Leave it a week and the next start quietly generates the days you missed, up to yesterday.

A few knobs:

```bash
./bin/loot serve --demo --demo-pace 5   # five times faster, for recording a video
./bin/loot serve --demo --demo-reset    # rebuild the demo world from scratch first
./bin/loot demo reset                   # delete data/demo.db (asks nothing; it is generated data)
```

`demo.pace` also lives in the config file, along with `demo.seed` and `demo.days`; `LOOT_DEMO`, `LOOT_DEMO_PACE`, `LOOT_DEMO_SEED` and `LOOT_DEMO_DAYS` are the environment equivalents. The dashboard wears a small **demo** pill next to the live indicator the whole time, so nobody mistakes an invented number for one of yours.

## What it looks like

Six screens, all of them demo data at 1400×900. Because the demo world comes from a fixed seed, they are reproducible: [`docs/screenshots/capture.mjs`](docs/screenshots/capture.mjs) re-shoots every one of them against `loot serve --demo`.

<table>
<tr>
<td width="50%">
<img src="docs/screenshots/feed.png" alt="The live feed: drops stacked newest first, each with a rarity badge, an amount and a country">
<b>Feed</b> — every drop as it lands, rarest colours loudest. The chest in the top right is yesterday, still shut, with 8 drops in it.
</td>
<td width="50%">
<img src="docs/screenshots/chest.png" alt="The chest cascade: a rare drop revealed on top of the drops already opened">
<b>The cascade</b> — a chest opens one drop at a time, 600 ms apart, quietest first, so the day ends on its best news.
</td>
</tr>
<tr>
<td width="50%">
<img src="docs/screenshots/vault.png" alt="The vault: revenue, units and refunds over ninety days, with a stacked area chart and breakdowns">
<b>Vault</b> — ninety days of settled revenue in one display currency, stacked by source, with units underneath and the change against the window before it.
</td>
<td width="50%">
<img src="docs/screenshots/quests.png" alt="The quests tab: weekly and monthly goals with progress bars, and mysteries with sparklines">
<b>Quests</b> — goals generated from your own history, and the anomalies Loot could not explain, waiting for you to write down why.
</td>
</tr>
<tr>
<td width="50%">
<img src="docs/screenshots/ambient.png" alt="Ambient mode: a full-bleed globe on a starfield with an era label and a ticker of arrivals">
<b>Ambient</b> — the Hearth with everything else taken away, for the spare monitor. <kbd>Esc</kbd> leaves.
</td>
<td width="50%">
<img src="docs/screenshots/chest-haul.png" alt="The end of a chest: total XP, the best drop of the day, and the full haul listed underneath">
<b>The haul</b> — what the chest was worth, the best thing in it, and then it settles into the feed tagged 📦.
</td>
</tr>
</table>

## Quickstart

### A release binary

Every tag publishes one static binary per platform — macOS (Apple silicon and Intel), Linux (x86-64 and arm64) and Windows — each with the dashboard already embedded in it. Nothing else needs installing; there is no runtime and no cgo.

```bash
VERSION=v0.1.0                  # or whatever the latest release says
PLATFORM=darwin_arm64           # darwin_amd64, linux_amd64, linux_arm64
BASE=https://github.com/NickHirras/loot/releases/download/$VERSION

curl -fsSL "$BASE/loot_${VERSION}_${PLATFORM}.tar.gz" | tar -xz
sudo install "loot_${VERSION}_${PLATFORM}/loot" /usr/local/bin/loot

loot serve --demo               # or just `loot serve`
```

Windows gets `loot_<version>_windows_amd64.zip`. Every release also carries a `checksums.txt`:

```bash
curl -fsSLO "$BASE/checksums.txt"
shasum -a 256 -c checksums.txt --ignore-missing
```

### From source

```bash
git clone https://github.com/nickhirras/loot.git
cd loot
make build            # builds the Svelte app, then the binary with it embedded
./bin/loot serve --dev # dashboard on http://localhost:8080
```

`--dev` mounts a dev panel in the UI so you can fire synthetic drops of each rarity and hear what they sound like before any real data arrives.

To configure it properly, copy the example config:

```bash
cp configs/loot.example.yaml loot.yaml
$EDITOR loot.yaml
./bin/loot serve            # reads ./loot.yaml by default
./bin/loot serve --config /etc/loot/loot.yaml
```

### With Docker

The published image is multi-arch (`linux/amd64` and `linux/arm64`, so a Raspberry Pi 4 and a VPS pull the same tag):

```bash
docker run -d --name loot \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e LOOT_REVENUECAT_SECRET=choose-a-long-random-string \
  -e LOOT_FLATHUB_APPS=org.example.YourApp \
  ghcr.io/nickhirras/loot:latest
```

| tag | what it is |
|---|---|
| `latest` | the newest release |
| `1.2.3`, `1.2` | that exact release, and the newest patch of that minor |
| `edge` | the tip of `main`, rebuilt on every push — newer than `latest`, and less careful |

Building it yourself is `docker build -t loot .`, or `make docker`.

The image is a distroless static binary (~14 MB). Mount a volume at `/data` to keep `loot.db` across restarts. Every config field has a `LOOT_*` environment override, so no config file is required.

### Tail it in your terminal

```bash
./bin/loot tail                              # defaults to http://localhost:8080
./bin/loot tail --url https://loot.example.com
```

```
19:55:27 LEGENDARY A legendary drop · synthetic test event [+1000 xp dev BR]
19:55:27 CURSED    Subscriber lost · cancellation · app_indie_1 [+5 xp revenuecat JP]
19:55:28 COMMON    587 installs on Flathub · org.gnome.Calculator [+10 xp flathub]
```

Rare and above ring the terminal bell. Use `--no-bell` if your coworkers object, `--no-color` for logs. Drops that came out of a chest are marked 📦.

### Check your configuration

```bash
./bin/loot check
```

```
✓ flathub      2 app(s)
✗ revenuecat   no secret set: anyone who can reach /hooks/revenuecat can inject drops

1 of 2 sources ready
```

`loot check` builds every configured source, calls its `Check` (Flathub fetches one app's stats, RevenueCat reports whether the webhook is protected) and exits non-zero if anything is wrong — which makes it a usable container healthcheck or post-deploy smoke test.

## Configuring sources

### RevenueCat (webhook, real time)

Set a secret in your config (or `LOOT_REVENUECAT_SECRET`):

```yaml
sources:
  revenuecat:
    enabled: true
    secret: "choose-a-long-random-string"
```

Then in the RevenueCat dashboard go to **Project settings → Integrations → Webhooks** and add:

- **URL**: `https://your-loot-host/hooks/revenuecat`
- **Authorization header**: `Bearer choose-a-long-random-string`

Loot handles `INITIAL_PURCHASE`, `RENEWAL`, `NON_RENEWING_PURCHASE`, `CANCELLATION`, `UNCANCELLATION`, `BILLING_ISSUE`, `EXPIRATION`, `PRODUCT_CHANGE` and `TEST`. Unknown event types are still recorded rather than dropped, so a new RevenueCat event type will not go missing.

Test it without waiting for a real sale:

```bash
curl -X POST http://localhost:8080/hooks/revenuecat \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer choose-a-long-random-string' \
  -d '{
    "api_version": "1.0",
    "event": {
      "id": "evt-test-001",
      "type": "INITIAL_PURCHASE",
      "app_id": "app_indie_1",
      "app_user_id": "user-777",
      "product_id": "pro_annual",
      "period_type": "ANNUAL",
      "price": 29.99,
      "currency": "USD",
      "country_code": "JP",
      "store": "APP_STORE",
      "environment": "PRODUCTION",
      "event_timestamp_ms": 1755500000000,
      "transaction_id": "txn-777"
    }
  }'
```

```json
{"dedupe_key":"rc:evt-test-001","kind":"purchase","ok":true}
```

Send it twice. RevenueCat retries deliveries, so Loot keys every event on `rc:<event.id>`: the second POST still returns `200`, but no second event and no second drop are created. Watch `/api/drops` to confirm.

### Flathub (polling, hourly)

```yaml
sources:
  flathub:
    apps:
      - org.gnome.Calculator
      - org.example.YourApp
    backfill_days: 7
```

Loot polls `https://flathub.org/api/v2/stats/{app_id}` once an hour and, for each app and each **completed** day (today is still accumulating, so it is never emitted — its count would be frozen at a partial value), stores a silent `install` row (`flathub:<app>:<date>`) and mints one chest-bound `installs_day` drop (`flathub:installs_day:<app>:<date>`) — the same shape as Google Play and Snapcraft, so a day's installs land in that day's chest instead of the live feed. Re-polling is free.

`backfill_days` limits what the *first* poll emits. The API returns roughly 180 days of history, and replaying all of it would bury you in drops; the default of 7 gives the feed some history and gives the "best day ever" rule a baseline to beat. Set it to `0` to emit nothing on the first run and only report days from here on. For a one-off bootstrap from a specific date:

```bash
./bin/loot serve --since 2026-01-01
```

The Flathub API also returns `installs_per_country`, but those figures are cumulative and carry no dates, so they cannot be turned into events without double counting. Flathub installs therefore count toward the Hearth's "unknown lands", not toward any settlement.

### App Store Connect (polling, hourly)

The App Store source reads Apple's own Sales and Trends reports, so its figures are settled developer proceeds rather than estimates.

**1. Create an API key.** In App Store Connect go to **Users and Access → Integrations → App Store Connect API**, stay on **Team Keys**, and generate a key with the **Sales and Reports** role (older accounts show this role as **Sales**; Finance and Admin also work). Download the `AuthKey_<KEY_ID>.p8` file — Apple lets you download it exactly once — and note the **Key ID** and the **Issuer ID** shown at the top of the same page.

**2. Find your vendor number.** It is on **Payments and Financial Reports**, beside your legal entity name: an eight digit number, usually starting with `8`. It is not your team ID and not your Apple ID.

**3. Point Loot at both.**

```yaml
sources:
  appstore:
    key_id: "2X9R4HXF34"
    issuer_id: "11111111-2222-3333-4444-555555555555"
    private_key_path: "/etc/loot/AuthKey_2X9R4HXF34.p8"
    vendor_number: "80123456"
    apps: []            # optional: only these Apple IDs (the numeric app ids)
    backfill_days: 30
```

The section counts as *configured* only when `key_id`, `issuer_id`, `private_key_path` and `vendor_number` are all set, and Loot checks that the `.p8` exists at startup rather than at the first poll.

Every field also has an environment override (`LOOT_APPSTORE_KEY_ID`, `LOOT_APPSTORE_ISSUER_ID`, `LOOT_APPSTORE_PRIVATE_KEY_PATH`, `LOOT_APPSTORE_VENDOR_NUMBER`, `LOOT_APPSTORE_APPS`, `LOOT_APPSTORE_BACKFILL_DAYS`), so a container needs no config file. Keep the `.p8` mounted read-only; it is a credential for your whole team's sales data.

Then check it before you wait an hour for the first poll:

```bash
./bin/loot check
```

```
✓ appstore     vendor 80123456
```

`check` signs a real ES256 token (no JWT library — `crypto/ecdsa` and 40 lines) and asks Apple for a real report. A wrong key, issuer or vendor number fails immediately with Apple's own explanation; a day Apple has not published yet counts as a pass, because it proves the credentials were accepted.

**What it reads.** Two daily reports, both keyed on a **Pacific** calendar day:

| report | version | what Loot takes from it |
|---|---|---|
| `SALES` / `SUMMARY` | `1_1` | every unit sold, refunded or downloaded |
| `SUBSCRIPTION` / `SUMMARY` | `1_3` | how many subscriptions were active that day |

Loot polls hourly and walks forward one day at a time, oldest first. Apple publishes a day's report a few hours after that day closes in Pacific time — usually between 05:00 and 08:00 PT — and until then the endpoint answers `404`. That is **not** an error and never lights up `last_error`: the cursor simply stays put and the next poll tries again. Columns are addressed by name rather than by position, so Apple inserting a column (it does) cannot silently shift every value one place to the left.

**What it emits.** One **silent ledger row event** per report row — `sale`, `iap`, `subscription`, `refund` or `download` — carrying that row's country, units and developer proceeds in the row's own proceeds currency. Update rows are counted but never emitted; an update is not a sale. On top of those sits one **`sales_day` summary per app per day**, whose drop goes into that day's chest.

The first poll backfills `backfill_days` (30 by default) of history. Those days land as chests, not as a live-feed flood — one chest per day, oldest first, each opening into its own cascade. Set it lower if you would rather not spend an evening opening chests; `loot serve --since 2026-01-01` overrides it once for a one-off bootstrap.

Because Apple pays euros for the euro zone and yen for Japan, one day usually contains several proceeds currencies. Each row keeps its own currency and the vault converts every one of them into your `display_currency`, so revenue is always right. The single-currency `sales_day` headline reports the day's **dominant** proceeds currency and marks itself `proceeds_mixed`, with the full split in `payload.by_currency`.

Subscription counts arrive as one `subscription_snapshot` event per app per day, which is what the vault's `subscriptions.active` reads. An account with no subscriptions has no such report; after a few 404s Loot stops asking, and looks again a week later in case that changed.

### Google Play (polling, every 6 hours)

Play has no sales API. It writes its reports as files into a private Cloud Storage bucket that belongs to your developer account, and a service account with read access is how anything else gets at them. Setting it up is three steps in two consoles.

**1. Make a service account.** In the [Google Cloud console](https://console.cloud.google.com/iam-admin/serviceaccounts), pick or create any project, create a service account (no project roles are needed — the grant that matters is the Play one), then open it, go to **Keys → Add key → Create new key → JSON**, and save the file somewhere Loot can read it. That file is a credential: `chmod 600` it and keep it out of your repo.

**2. Grant it access in Play Console.** Go to **Play Console → Users and permissions → Invite new users** and invite the service account's email address (the long `…@….iam.gserviceaccount.com` one from the key file). Under **Account permissions**, tick:

- **View app information and download bulk reports** — the statistics files
- **View financial data, orders, and cancellation survey responses** — the sales files

Play can take up to 24 hours to propagate a new grant to the bucket, so a `403` right after inviting is usually patience rather than a mistake.

**3. Find the bucket id.** **Play Console → Download reports → Statistics**, then **Copy Cloud Storage URI**. It looks like `gs://pubsite_prod_rev_01234567890`. Loot accepts it with or without the `gs://` prefix.

```yaml
sources:
  googleplay:
    service_account_json_path: "/etc/loot/play-reports.json"
    bucket: "pubsite_prod_rev_01234567890"
    packages: []          # optional: only these package names
    backfill_months: 2
```

The section counts as *configured* only when both `service_account_json_path` and `bucket` are set, and Loot checks that the key file exists at startup rather than at the first poll.

Then check it before you wait six hours to find out:

```bash
./bin/loot check
```

```
✓ googleplay   bucket pubsite_prod_rev_01234567890
```

A failure says which of the two things went wrong: a `403` prints the exact Play Console permissions to tick, a `404` prints where the bucket id comes from.

**What Loot reads.** Two report families, both monthly files that Play rewrites in place every day:

| file | becomes |
|---|---|
| `sales/salesreport_YYYYMM.zip` | one **silent ledger row** per order line — `sale`, `iap`, `subscription` or `refund` — plus one `sales_day` chest summary per app per day |
| `stats/installs/installs_<package>_YYYYMM_overview.csv` | silent `install` and `active_devices` counters, plus one `installs_day` chest drop per day |
| `stats/installs/installs_<package>_YYYYMM_country.csv` | silent per-country `install` events, which is what founds **settlements** |

The other statistics dimensions (device, os_version, carrier, language, app_version…) are ignored. Buyer city, state and postal code are columns of the sales report and are deliberately never stored.

> [!IMPORTANT]
> **Estimated sales are gross.** `Charged Amount` is what the customer paid — Play's 15–30% service fee and any withheld tax are still in it, so the vault will read high against your bank statement. Every Play payload carries `"gross": true` and says so. The monthly **earnings** report is the net truth; ingesting it is a later quest.

Because the current month's file is still being rewritten and late rows genuinely arrive, a day only gets its `sales_day` summary once it is older than yesterday in Pacific Time (the timezone Play states financial reports in; statistics are UTC). Rows for an unsettled day are still stored the moment they appear — the vault sums rows, not summaries — they just do not mint a chest yet. If a row for an already-summarized day turns up later, it is stored and the summary is *not* re-emitted, so a day can never be counted twice.

The first poll reads `backfill_months` months (the current one and last, by default) and turns every settled day in them into a chest. That is roughly six weeks of history waiting to be opened, and it gives the "best day ever" rules a baseline to beat. Afterwards each poll re-lists the bucket but only downloads a file whose `md5Hash` has changed, so a quiet day costs two listings and nothing else.

### More sources: Microsoft Store, Snapcraft, GitHub, generic webhook

Each has its own guide with setup steps, config (YAML + env), what it emits, and caveats:

| Source | Guide | What it produces |
|---|---|---|
| Microsoft Store (Partner Center analytics, 6h) | [docs/sources/microsoftstore.md](docs/sources/microsoftstore.md) | ledger rows per app/market/day (gross prices, settled after 3 days), `sales_day` chests, subscription snapshots |
| Snapcraft (Snap Store metrics, 6h) | [docs/sources/snapcraft.md](docs/sources/snapcraft.md) | new/active/lost devices per snap/day, derived per-country installs (settlements), `installs_day` chests |
| GitHub (poll 10 min + `POST /hooks/github`) | [docs/sources/github.md](docs/sources/github.md) | stars, star milestones, forks, issues opened/closed ("a quest appears"), PRs, releases |
| Generic webhook (`POST /hooks/webhook`) | [docs/sources/webhook.md](docs/sources/webhook.md) | anything: name your kind, rarity, title; batch posts; optional ledger amounts |

`loot check` validates all of them.

## The daily chest

A ledger source does not report a sale when it happens; it reports yesterday, all at once, as hundreds of rows. Loot handles that with two kinds of event:

- **silent row events** — stored, counted in the vault and in stats, but they produce no drop at all. This is where the money lives.
- **one summary event per app per day** (`kind: sales_day`), whose drop is filed into that day's **chest** instead of being published.

A chest is opened from the dashboard, by `loot chest open`, or by itself once its day is `chest.auto_open_after_hours` old (36 by default, measured from midnight UTC of the chest's own day — so yesterday's chest springs open around noon). Opening it cascades the drops onto the websocket 600 ms apart, ordered cursed → common → … → legendary, so the reveal builds instead of dumping.

```bash
./bin/loot chest                      # what is waiting
./bin/loot chest open                 # open the oldest chest
./bin/loot chest open 2026-08-17      # open a specific day
```

```
📦 2026-08-17  4 drops  +1265 xp  1 common, 1 rare, 1 legendary, 1 cursed
📦 opened 2026-08-17 — 4 drops
   CURSED    A cursed drop · synthetic test event [+5 xp dev]
   COMMON    412 sales · 1,204.55 USD · com.example.app [+10 xp appstore]
   RARE      New settlement: 🇧🇷 BR · first customer via appstore [+250 xp loot]
   LEGENDARY Best day ever on appstore [+1000 xp appstore]
   total +1265 xp
```

In the dashboard a chest icon appears in the header with the number of drops waiting. Clicking it opens the chest overlay: the chests by date with their counts, XP and rarity dots, and an **Open** button per chest (or one for the oldest). The lid swings open, the drops cascade in one at a time as they arrive over the websocket — each with its rarity sound — and settle into a growing haul, ending on the total XP and the best drop. The haul then sits in the feed, tagged 📦.

Everything an unopened chest holds is invisible until it is opened: `GET /api/drops` excludes it, `GET /api/stats` does not count its XP, and the vault does not count its drops. Set `chest.enabled: false` to publish everything the moment it is ingested.

## Currency

Every event keeps the amount and currency its source reported. Loot stores a converted copy alongside it (`amount_base`) in `display_currency`, and it is that copy the vault sums.

```yaml
display_currency: "USD"
fx:
  enabled: true
```

Rates are the ECB daily reference rates, fetched from [frankfurter.app](https://frankfurter.app) (no API key) every 12 hours and cached in the database. A snapshot of ~30 currencies is embedded in the binary, so conversion works offline, on the first run, and with `fx.enabled: false`. A currency with no known rate leaves `amount_base` at 0 and logs once — it is never silently guessed at.

```bash
./bin/loot fx rates       # which rates would be used, and how old they are
./bin/loot fx recompute    # re-convert every stored amount (stop the server first)
```

## The vault

The dashboard's **Vault** tab draws this: a range picker (7d/30d/90d/365d, remembered), stat tiles with a change against the preceding window, revenue per day as an area chart stacked by source with units underneath, and breakdowns by source, app and country. `GET /api/vault/summary?range=7d|30d|90d|365d` is the money view behind it. Its cardinal rule is that **only ledger rows count as revenue**:

- RevenueCat amounts are pre-tax, pre-store-cut estimates. They are reported separately, as `realtime`, and never added to revenue.
- `sales_day` summaries are a rollup of the rows beside them, so they are excluded too — otherwise every ledger day would count twice.
- A refund is a ledger row whose kind is `refund` or whose quantity is negative, stored with a negative amount. Revenue nets out; `units` and `refunds` are reported as two positive counts, so a day with 10 sales and 2 refunds reads as "10 / 2", not "8".

```jsonc
{
  "display_currency": "USD",
  "range": { "from": "2026-07-21", "to": "2026-08-19", "days": 30 },
  "totals":      { "revenue_base": 4192.4, "units": 731, "refunds": 9,
                   "drops": 214, "events": 5120, "countries": 37 },
  "prev_totals": { /* the same shape, for the preceding 30 days */ },
  "series":    [ { "day": "2026-07-21", "revenue_base": 132.4, "units": 22,
                   "by_source": { "appstore": 110.0, "googleplay": 22.4 } } ],
  "by_source": [ { "source": "appstore", "revenue_base": 3100.0, "units": 540, "share": 0.7394 } ],
  "by_app":    [ { "app": "com.example.app", "revenue_base": 4192.4, "units": 731, "share": 1 } ],
  "by_country":[ { "country": "US", "revenue_base": 2100.0, "units": 380, "share": 0.5009 } ],
  "subscriptions": { "active": 412, "as_of": "2026-08-18" },
  "realtime":      { "revenuecat_purchases_today": 6, "revenuecat_amount_base_today": 178.94 }
}
```

`series` is zero-filled, one point per day, so a chart never has holes. `by_country` lists the top 25 and folds the tail into a row called `other`. `subscriptions` sums the newest `subscription_snapshot` event of each (source, app), and is `null` when no source has ever reported one.

## The Hearth

The **Hearth** tab is the map of what you have built: a slowly turning globe where every country you have ever sold in has a settlement, lit up like a city seen from orbit. The night side of the planet is real — the terminator is computed from the sun's actual position — and settlements on the dark side glow brighter, because that is when city lights show. Drag to turn the world, scroll to zoom, double click to go home, hover a city for its numbers.

When a drop lands, it *arrives*: an arc leaves its country and flies to your capital in its rarity's colour, bursting on impact while the origin pulses. A country's first ever customer founds a settlement, with an expanding ring and a label. A cancellation does not fly anywhere — it flashes red where it happened, and stays there.

### Population

A settlement's size is its **population**: not money, not events, but a count of people who arrived.

| counted as one person | where it comes from |
|---|---|
| each unit on a ledger row of kind `sale`, `iap`, `subscription` or `download` | App Store and Google Play financial reports |
| each `install` | Google Play's per-country install statistics, Flathub |
| each RevenueCat `purchase` | the RevenueCat webhook |

Only positive quantities count, so **a settlement never shrinks**: a refund nets out of revenue but does not evict a citizen. Deliberately excluded are the `sales_day` and `installs_day` rollups (they are a summary of the rows beside them), cancellations and expirations (bad news does not add people), `active_devices` and `subscription_snapshot` (they are absolute snapshots, not arrivals), and Loot's own synthetic `settlement` event. Revenue per country follows the vault's rule exactly: ledger rows only, signed, `sales_day` excluded.

### Tiers

A settlement's tier is **relative to your biggest country**, not an absolute headcount — so the map looks the same whether you have 300 customers or three million, and growth shows up as a changing skyline instead of everything maxing out at once.

| tier | share of the largest settlement |
|---|---|
| **metropolis** | ≥ 50% |
| **city** | ≥ 15% |
| **town** | ≥ 5% |
| **village** | ≥ 1% |
| **hamlet** | ≥ 0.2% |
| **outpost** | anything else — one customer is still a place on the map |

### Eras

The whole account sits on an era ladder, unlocked by total XP. Each rung costs roughly four times the last, so an era is a milestone rather than a weekly event.

| era | XP | era | XP |
|---|---|---|---|
| Camp | 0 | Kingdom | 75,000 |
| Village | 1,000 | Empire | 250,000 |
| Town | 5,000 | Dynasty | 1,000,000 |
| City | 20,000 | | |

Both tables live in [`internal/core/hearth.go`](internal/core/hearth.go), which is the only place to tune them.

### The capital, and unknown lands

Every arc flies to your **capital**. By default that is your biggest settlement, which needs no configuring; set `home_country` if you would rather it were where *you* are:

```yaml
home_country: "NZ"   # ISO 3166-1 alpha-2, or "" for the biggest settlement
```

`LOOT_HOME_COUNTRY` overrides it. A country you have never sold in is still a valid capital — it is where you are, not where your customers are.

Events with no country at all are counted apart, as **unknown lands**: Flathub reports how many people installed but never says from where, and those people should not be invented into a country or silently dropped.

### Ambient mode

<http://localhost:8080/#/ambient> (or the **⤢ Ambient** button, or `#/hearth?ambient=1`) is the Hearth with everything else taken away: full-bleed globe on a starfield, the era and its progress in one corner, a ticker of recent arrivals along the bottom, and a cursor that gets out of the way after three seconds. <kbd>Esc</kbd> leaves. It is meant to be left running on a spare monitor or an iPad all day, so the globe idles at 30fps, pauses its own rotation for five seconds after you touch it, and honours `prefers-reduced-motion` by not spinning at all.

`GET /api/hearth` is the aggregate behind all of it — settlements, capital, era, tier ladder and the last 30 country-bearing drops. It is cached for five seconds server-side; the page refetches once a minute and merges live websocket drops in between, so a new country appears the instant its first customer does.

## Quests & mysteries

The **Quests** tab gives you goals worth chasing and puzzles worth poking at — see [docs/quests.md](docs/quests.md) for the full design and API.

- **Quests** are generated from your own history each week and month ("Beat last week's revenue · $1,240", "Settle 2 new countries this month", "Earn 10,900 XP this week"), plus custom ones you create. Completing one mints a rare (weekly) or epic (monthly) drop. An unmet quest quietly ends as "ended · 62%" — never red, never a broken streak.
- **Mysteries** are anomalies Loot noticed in your data and turned into open questions: "Google Play installs tripled on Fri Aug 14 — why?", refund spikes, a cluster of new countries, or a source that has gone quiet (usually broken credentials — Loot allows for each store's normal reporting lag, asks once, and dismisses the question itself when data resumes). Each card has a 28-day sparkline; write a one-line explanation and **Solve** it for a drop, or dismiss it. Solved mysteries become a notebook of your own explanations.

## Rarity rules

Classification is a small ordered YAML rule list. The defaults are embedded in the binary ([`internal/rules/default.yaml`](internal/rules/default.yaml)); point `rules_path` at your own file to override them.

```yaml
rules:
  - name: revenuecat-annual
    match:
      source: revenuecat
      kinds: [purchase]
      payload_match:
        event.period_type: ANNUAL
    then:
      rarity: rare
      title: "New annual subscriber"
      subtitle: "{{.AmountFmt}} · {{.App}}"
      xp: 150

  # Floor rules do not terminate matching; they only ever RAISE rarity.
  - name: weekend-bonus
    floor: true
    match:
      min_amount: 500
    then:
      rarity: epic
      title: "A big one"

fallback:
  rarity: common
  title: "{{.Source}} · {{.Kind}}"
```

The first non-floor rule that matches wins. Then every matching floor rule may raise the rarity — which is how "anything over $500 is *at least* epic" works without repeating the condition in every rule. Because `cursed` outranks `legendary`, a floor rule can never relabel a cancellation as a celebration.

The first sale from a country Loot has never seen does not use a floor rule. The pipeline synthesizes a second event for it — `{source: loot, kind: settlement}`, carrying the country and `payload.via` — so the settlement gets its own **rare** drop and the event that revealed it keeps its own headline. Silent ledger rows found settlements too: the country is the news, not the row.

**Match fields**: `source`/`sources`, `kind`/`kinds`, `app`, `min_amount`, `max_amount`, `min_quantity`, `is_ledger`, `has_country`, `country_first`, `record_high`, `payload_match` (dotted JSON paths into the raw source payload).

`country_first` and `record_high` are answered by the database — the first is true when no earlier event carries that country, the second when the event's quantity beats every previous event for the same source, app and kind.

**Template fields** for `title` and `subtitle`: `.Source .Kind .App .Day .Country .Flag .Amount .AmountFmt .AmountBase .AmountBaseFmt .Currency .Quantity .QuantityFmt .Payload`, plus `.BaseTitle` in floor rules. `.AmountBaseFmt` is the amount in your display currency.

## Development

```bash
make dev      # Go server on :8080 + Vite dev server on :5173 (proxies /api, /ws, /hooks)
make test     # go test ./...
make check    # go vet + gofmt + svelte-check
make build    # frontend, then binary with the SPA embedded
make dist     # cross-compile every release archive into dist/
make help     # everything else
```

Prefer two terminals? `go run ./cmd/loot serve --config configs/loot.example.yaml --dev` in one, `cd web && npm run dev` in the other, then open <http://localhost:5173>.

The dashboard synthesizes all of its sounds with the Web Audio API — no audio files ship with Loot. Browsers block audio until a user gesture, so the UI shows a "click to enable drop sounds" banner until you do. Press <kbd>m</kbd> to mute; the setting persists in `localStorage`.

### CI and releases

Two workflows, and nothing to run by hand:

| workflow | when | what it does |
|---|---|---|
| [`ci.yml`](.github/workflows/ci.yml) | every push to `main`, every PR | `svelte-check`, `vite build`, `gofmt -l`, `go vet`, `go test`, then a `docker build` that pushes nowhere, purely so the Dockerfile cannot drift away from the tree |
| [`release.yml`](.github/workflows/release.yml) | push to `main` | builds `linux/amd64` + `linux/arm64` and pushes `ghcr.io/nickhirras/loot:edge` |
| | push of a `v*` tag | the same image as `latest`, `<semver>` and `<major.minor>`, plus the five release archives and their checksums, attached to a GitHub Release |

Cutting a release is one command:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

To see exactly what the tag will produce, build it locally first — same cross-compile, same archive layout, published nowhere:

```bash
make dist                 # dist/loot_<version>_<os>_<arch>.tar.gz|.zip + checksums.txt
make dist VERSION=v0.1.0  # with the version string the tag would stamp in
```

> [!NOTE]
> **One manual step, once.** A package GHCR creates from a workflow starts out private, and `GITHUB_TOKEN` cannot change that — there is no API for it. After the first successful push, go to the [package page](https://github.com/NickHirras/loot/pkgs/container/loot) → **Package settings** → **Danger Zone** → **Change visibility** → **Public**, and while you are there, **Manage Actions access** → add the `loot` repository with **Write** so later runs keep the permission. Until that is done, `docker pull ghcr.io/nickhirras/loot:edge` asks strangers for a login.

## Architecture

```
                    ┌──────────────┐
  RevenueCat ──POST─▶ /hooks/{src} │
                    └──────┬───────┘
                           │ emit(Event)
  Flathub ◀──poll── scheduler──────┤
                                   ▼
                            ┌─────────────┐
                            │  pipeline   │
                            └──────┬──────┘
              dedupe on dedupe_key │  (duplicate ⇒ stop here, no drop)
                                   ▼
                            store.InsertEvent
                                   ▼
                            rules.Classify ──▶ rarity, title, XP
                                   ▼
                            store.InsertDrop
                                   ▼
                              bus.Publish
                             ╱           ╲
                       /ws (browsers)   loot tail
```

| package | responsibility |
|---|---|
| `internal/core` | `Event`, `Drop`, `Rarity`, the `Source` interface, ULID generation |
| `internal/store` | SQLite via `modernc.org/sqlite` (pure Go, no cgo); migrations, dedupe, queries |
| `internal/rules` | YAML rarity engine with embedded defaults |
| `internal/fx` | currency conversion: ECB rates, SQLite cache, embedded fallback snapshot |
| `internal/sources/*` | one package per integration |
| `internal/pipeline` | ingest path and the polling scheduler |
| `internal/bus` | in-process pub/sub, non-blocking so a slow browser cannot stall ingest |
| `internal/server` | JSON API, websocket hub, embedded SPA with history fallback |
| `web` | Svelte 5 + Vite + TypeScript, embedded via `go:embed` |

Adding a source means implementing `core.Source` (and optionally `core.WebhookHandler`) and registering it in `cmd/loot/serve.go`. Everything downstream — dedupe, rarity, feed, sounds, tail — comes for free.

### HTTP API

| endpoint | purpose |
|---|---|
| `GET /api/drops?limit=100&before=<id>` | recent drops, newest first, with joined event fields; excludes unopened chests |
| `GET /api/stats` | XP total, drop counts by rarity and source, countries seen, chests waiting |
| `GET /api/sources` | source list with last poll time and last error |
| `GET /api/vault/summary?range=30d` | revenue, units, series and breakdowns in the display currency |
| `GET /api/hearth` | the globe: settlements with population and tier, capital, era, recent arrivals |
| `GET /api/chest` | the chests waiting to be opened, oldest first |
| `POST /api/chest/open` | `{"date":"2026-08-17"}` (or `{}` for the oldest) — reveals and returns the cascade |
| `GET /ws` | websocket stream, see below |
| `POST /hooks/{source}` | source webhook receiver |
| `POST /api/dev/fake` | synthetic drop, only when `dev.enabled` |

`GET /api/stats` also carries `"demo": true` when the server is running on synthetic data, which is what puts the **demo** pill in the header.

The websocket carries four kinds of message:

```jsonc
{"type": "hello"}                                   // on connect
{"type": "drop",  "drop": {…}, "event": {…}}        // a drop landed
{"type": "drop",  "chest": true, "drop": {…}, …}    // a drop revealed by opening a chest
{"type": "chest", "chests": [{"date": "2026-08-17", "count": 4, "xp": 1265,
                              "by_rarity": {"rare": 1, …}}]}   // the waiting set changed
```

In dev mode, `POST /api/dev/fake` takes `{"rarity","kind","app","country","amount","currency","quantity","day","chest","silent"}`. `{"kind":"sales_day"}` imitates a ledger source properly: it stores a silent revenue row *and* a chest-bound summary drop with a real `SalesDaySummary` payload, so the vault and the chest can both be exercised without an App Store account.

### Security note

Demo mode is the one exception worth stating plainly: it is safe to run anywhere, because it reads no credentials and writes only to `demo.db`.

Loot has no authentication of its own. The RevenueCat webhook secret protects that one endpoint, but the dashboard and API are open to anyone who can reach the port. Put it behind a reverse proxy with TLS and basic auth (or a tunnel like Tailscale) before exposing it to the internet, and keep `dev.enabled` off in production.

## Roadmap

Loot ships in quests.

- **Quest 1 — First Blood** ✅ — the scaffold: event pipeline, SQLite store, rarity rules engine, RevenueCat webhooks, Flathub polling, live feed with synthesized sounds, `loot tail`.
- **Quest 2 — The Vault Opens** ✅ — silent ledger events, the **daily chest** with its cascade, currency conversion and the vault API, settlement drops, `loot check` / `loot chest` / `loot fx`, and the dashboard's Vault page and chest-opening ritual.
- **Quest 4 — The Hearth** ✅ — the globe: a settlement per country that grows with every customer, tiers from outpost to metropolis, eras from Camp to Dynasty, live arcs flying home to your capital, a real day/night terminator, and **ambient mode** for the spare monitor.
- **Quest 3 — Know Thy Enemy** 🔨 *(next)* — Crashlytics and Sentry as *boss fights*: a crash spike spawns a named boss with a health bar that drains as you ship fixes and the crash-free rate recovers.
- **Quest 5 — Quests & Mysteries** ✅ — auto-generated weekly/monthly goals from your own history plus custom quests, and anomaly detection surfaced as puzzles with a notebook of your explanations.
- **Quest 6 — Codex & Season Recap** — a permanent record of everything that has ever dropped, and a shareable end-of-season summary of the year in loot.
- **Quest 7 — More Worlds** ✅ — Microsoft Store, Snapcraft and GitHub as sources, plus a generic webhook so anything that can POST JSON can drop loot.

Along the way: **demo mode** (`loot serve --demo`) landed with Quest 4's polish — a seeded 120-day world plus a live emitter in a separate `demo.db`, so the project can be tried, screenshotted and recorded without an App Store account.

Further out: RevenueCat as an authoritative ledger for real revenue totals, achievements with permanent trophies, and a shareable read-only "hoard" page.

## License

MIT — see [LICENSE](LICENSE).
