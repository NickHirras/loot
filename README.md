<div align="center">

# ◆ Loot

**A self-hosted, gamified dashboard for indie app developers.**

Every install, subscription and cancellation becomes a *drop* — with a rarity, a colour and a sound.

</div>

---

Shipping an indie app means watching numbers scattered across half a dozen dashboards that all look like accounting software. Loot collects those numbers into one place and treats them like what they actually feel like: loot. A new subscriber is an **uncommon** drop. Their first annual plan is **rare**. Your best install day ever on Flathub is **epic**. The first sale from a country you have never sold in before is a **new settlement**. A cancellation is **cursed**, and it sounds like it.

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

## Quickstart

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

```bash
docker build -t loot .
docker run -d --name loot \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e LOOT_REVENUECAT_SECRET=choose-a-long-random-string \
  -e LOOT_FLATHUB_APPS=org.example.YourApp \
  loot
```

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

Rare and above ring the terminal bell. Use `--no-bell` if your coworkers object, `--no-color` for logs.

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

Loot polls `https://flathub.org/api/v2/stats/{app_id}` once an hour and emits one `install` event per app per **completed** day (today is still accumulating, so it is never emitted — its count would be frozen at a partial value). Events are keyed `flathub:<app>:<date>`, so re-polling is free.

`backfill_days` limits what the *first* poll emits. The API returns roughly 180 days of history, and replaying all of it would bury you in drops; the default of 7 gives the feed some history and gives the "best day ever" rule a baseline to beat. Set it to `0` to emit nothing on the first run and only report days from here on. For a one-off bootstrap from a specific date:

```bash
./bin/loot serve --since 2026-01-01
```

The Flathub API also returns `installs_per_country`, but those figures are cumulative and carry no dates, so they cannot be turned into events without double counting. Country data currently comes from RevenueCat only.

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
  - name: new-country
    floor: true
    match:
      country_first: true
    then:
      rarity: rare
      title: "New settlement: {{.Flag}} {{.Country}}"
      xp: 250

fallback:
  rarity: common
  title: "{{.Source}} · {{.Kind}}"
```

The first non-floor rule that matches wins. Then every matching floor rule may raise the rarity — which is how "a first-ever country is *at least* rare" works without repeating the condition in every rule. Because `cursed` outranks `legendary`, a cancellation from a brand-new country stays cursed rather than being relabelled as a celebration.

**Match fields**: `source`/`sources`, `kind`/`kinds`, `app`, `min_amount`, `max_amount`, `min_quantity`, `is_ledger`, `has_country`, `country_first`, `record_high`, `payload_match` (dotted JSON paths into the raw source payload).

`country_first` and `record_high` are answered by the database — the first is true when no earlier event carries that country, the second when the event's quantity beats every previous event for the same source, app and kind.

**Template fields** for `title` and `subtitle`: `.Source .Kind .App .Country .Flag .Amount .AmountFmt .Currency .Quantity .QuantityFmt .Payload`, plus `.BaseTitle` in floor rules.

## Development

```bash
make dev      # Go server on :8080 + Vite dev server on :5173 (proxies /api, /ws, /hooks)
make test     # go test ./...
make check    # go vet + gofmt + svelte-check
make build    # frontend, then binary with the SPA embedded
make help     # everything else
```

Prefer two terminals? `go run ./cmd/loot serve --config configs/loot.example.yaml --dev` in one, `cd web && npm run dev` in the other, then open <http://localhost:5173>.

The dashboard synthesizes all of its sounds with the Web Audio API — no audio files ship with Loot. Browsers block audio until a user gesture, so the UI shows a "click to enable drop sounds" banner until you do. Press <kbd>m</kbd> to mute; the setting persists in `localStorage`.

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
| `internal/sources/*` | one package per integration |
| `internal/pipeline` | ingest path and the polling scheduler |
| `internal/bus` | in-process pub/sub, non-blocking so a slow browser cannot stall ingest |
| `internal/server` | JSON API, websocket hub, embedded SPA with history fallback |
| `web` | Svelte 5 + Vite + TypeScript, embedded via `go:embed` |

Adding a source means implementing `core.Source` (and optionally `core.WebhookHandler`) and registering it in `cmd/loot/serve.go`. Everything downstream — dedupe, rarity, feed, sounds, tail — comes for free.

### HTTP API

| endpoint | purpose |
|---|---|
| `GET /api/drops?limit=100&before=<id>` | recent drops, newest first, with joined event fields |
| `GET /api/stats` | XP total, drop counts by rarity and source, countries seen |
| `GET /api/sources` | source list with last poll time and last error |
| `GET /ws` | websocket stream of `{"type":"drop","drop":{…},"event":{…}}` |
| `POST /hooks/{source}` | source webhook receiver |
| `POST /api/dev/fake` | synthetic drop, only when `dev.enabled` |

### Security note

Loot has no authentication of its own. The RevenueCat webhook secret protects that one endpoint, but the dashboard and API are open to anyone who can reach the port. Put it behind a reverse proxy with TLS and basic auth (or a tunnel like Tailscale) before exposing it to the internet, and keep `dev.enabled` off in production.

## Roadmap

Loot ships in quests.

- **Quest 1 — First Blood** ✅ *(you are here)* — the scaffold: event pipeline, SQLite store, rarity rules engine, RevenueCat webhooks, Flathub polling, live feed with synthesized sounds, `loot tail`.
- **Quest 2 — The Vault Opens** — App Store Connect and Google Play sources, plus a **Daily Chest**: one summary drop each morning with yesterday's takings, streaks, and a bonus for beating your rolling average.
- **Quest 3 — Know Thy Enemy** — Crashlytics and Sentry as *boss fights*: a crash spike spawns a named boss with a health bar that drains as you ship fixes and the crash-free rate recovers.
- **Quest 4 — The Hearth** — a rotating globe where every country you have sold in grows a settlement, scaled by revenue and installs. New countries plant a flag with fanfare; lapsed ones dim.

Further out: Microsoft Store, Snapcraft and GitHub sources, RevenueCat as an authoritative ledger for real revenue totals, achievements with permanent trophies, and a shareable read-only "hoard" page.

## License

MIT — see [LICENSE](LICENSE).
