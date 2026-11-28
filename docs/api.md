# CLI, HTTP API & AI Agents (MCP)

Everything shown in the Loot dashboard is backed by a JSON HTTP API. External scripts, dashboards, cron jobs, and AI agents can pull every metric and report directly — via HTTP, via `loot report`, or over the Model Context Protocol (`loot mcp`) — without scraping the web UI.

---

## 1. Authentication (website login + scoped API tokens)

By default, Loot keeps its dashboard, `/api/*`, and `/ws` open (for localhost, Tailscale, or deployments behind an authenticating reverse proxy).

When exposing Loot behind a TLS reverse proxy (like Caddy or Nginx), the recommended setup is to let **Loot own both browser login and API tokens** so your reverse proxy is just a pure TLS forwarder (`reverse_proxy 127.0.0.1:8080`):

```yaml
auth:
  username: "nick"                             # optional; omit for password-only
  password_hash_path: "/etc/loot/auth-password-hash.txt"
  session_ttl: 720h                            # 30 days (default)

api:
  # Read-only tokens: allowed on GET/HEAD /api/* and GET /ws only.
  # Any POST or DELETE with a read-only token returns 403 Forbidden.
  read_tokens_path: "/etc/loot/api-read-tokens.txt"
  # Full-access tokens: allowed on both read and write endpoints.
  write_tokens_path: "/etc/loot/api-write-tokens.txt"
```

Generate hashes and tokens with the `loot` binary itself (existing bcrypt hashes from `caddy hash-password` also work drop-in):

```bash
loot auth hash > /etc/loot/auth-password-hash.txt
loot auth token >> /etc/loot/api-read-tokens.txt
```

And Caddy simplifies to three lines:

```caddyfile
loot.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

### How auth works

- **Browsers:** Unauthenticated visits to `/` redirect to `/login`. Signing in sets a signed `HttpOnly`, `SameSite=Lax` (`Secure` over HTTPS) cookie granting full read/write dashboard access. Sessions survive restarts and automatically invalidate if the password changes. Failed logins are rate-limited (10 attempts per IP per 10 minutes), and cookie-authenticated writes enforce same-origin `Origin` checks. Sign out via the `⎋` button in the header (`POST /logout`).
- **Agents, CLI & scripts:** Send `Authorization: Bearer <token>` or `X-API-Key: <token>` to `/api/*` or `/ws`. API endpoints always return JSON `401`/`403` (never HTML redirects).
- **Token files & env vars:** Token files contain one token per line (blank lines and `#` comments ignored). Environment equivalents:
  - Website login: `LOOT_AUTH_USERNAME`, `LOOT_AUTH_PASSWORD`, `LOOT_AUTH_PASSWORD_HASH`, `LOOT_AUTH_PASSWORD_HASH_PATH`, `LOOT_AUTH_SESSION_SECRET`, `LOOT_AUTH_SESSION_TTL`
  - API tokens: `LOOT_API_READ_TOKENS`, `LOOT_API_READ_TOKENS_PATH`, `LOOT_API_WRITE_TOKENS`, `LOOT_API_WRITE_TOKENS_PATH`

### What stays exempt

- `GET /api/healthz` and `GET /api/auth/status` stay unauthenticated for health checks.
- `GET /login`, `POST /login`, `POST /logout` handle browser authentication.
- `POST /hooks/{source}` keeps each source's own webhook secret/signature verification.
- Static PWA manifest and icon assets (`/manifest.webmanifest`, `/icon-*.png`, `/favicon*`, `/sw.js`, `/offline.html`) remain accessible before login so PWA install and background updates work cleanly.

---

## 2. CLI (`loot report`, `loot funnel`, `loot chest`, `loot tail`)

Every remote CLI command accepts the same connection flags and environment variables (prefer env vars so tokens do not appear in shell history or process lists):

| Flag | Environment variable | Default | Purpose |
|---|---|---|---|
| `--url` | `LOOT_URL` | `http://localhost:8080` | Loot server base URL |
| `--token` | `LOOT_API_TOKEN` | _(empty)_ | Bearer token (automatically sent as `X-API-Key` if `--url` carries Basic Auth) |
| `--header` (repeatable) | `LOOT_API_HEADERS` (`;` or newline separated) | _(empty)_ | Extra proxy headers, e.g. Cloudflare Access service-token headers |

### Pulling metrics and reports

```bash
export LOOT_URL="https://loot.example.com"
export LOOT_API_TOKEN="your-read-only-token"

# Combined executive report (stats, revenue, scorecards, audience, ratings, funnel, alerts)
loot report
loot report --range 30d --app "Nistis" --json

# Individual views (human-readable by default, or --json for scripts/agents)
loot report vault --range 90d --json
loot report hearth --app "Macro Trainer" --json
loot report trend --metric audience --product "Macro Trainer" --range 30d --json
loot report reviews --range 90d --stars low --json
loot report funnel --days 14 --json
loot report recap --month 2026-10 --json
loot report codex --json
loot report stats --json
loot report drops --limit 50 --json
loot report quests --json
loot report mysteries --json
loot report bosses --json
loot report sources --json
loot report apps --json
```

If your Loot server sits behind Cloudflare Access with a service token:

```bash
export LOOT_URL="https://loot.example.com"
export LOOT_API_HEADERS="CF-Access-Client-Id: <id>; CF-Access-Client-Secret: <secret>"
loot report --json
```

---

## 3. AI Agents via MCP (`loot mcp`)

Loot ships a zero-dependency **Model Context Protocol (MCP)** stdio server inside the same `loot` binary. It is **read-only by default**, so you can safely connect any MCP-capable agent (Claude Code, Codex, Cursor, Claude Desktop, T3 Code, or custom agent runtimes) to pull live metrics and reports.

### Example MCP client config

Add this to your MCP client configuration (for example `.mcp.json`, Claude Desktop's `claude_desktop_config.json`, or your agent harness):

```json
{
  "mcpServers": {
    "loot": {
      "command": "/usr/local/bin/loot",
      "args": ["mcp"],
      "env": {
        "LOOT_URL": "https://loot.example.com",
        "LOOT_API_TOKEN": "your-read-only-token"
      }
    }
  }
}
```

If you authenticate via reverse-proxy headers (such as Cloudflare Access), set `LOOT_API_HEADERS` in `env` instead of (or alongside) `LOOT_API_TOKEN`:

```json
"env": {
  "LOOT_URL": "https://loot.example.com",
  "LOOT_API_HEADERS": "CF-Access-Client-Id: <id>; CF-Access-Client-Secret: <secret>"
}
```

### Exposed MCP tools

Read-only tools (enabled by default):

| Tool | Endpoint | What it provides |
|---|---|---|
| `loot_report` | `GET /api/report` | Combined overview: stats, revenue, scorecards, audience, ratings, funnel, alerts, source health |
| `loot_vault` | `GET /api/vault/summary` | Settled revenue, units, refunds, daily series, breakdowns, active subscriptions, scorecards |
| `loot_hearth` | `GET /api/hearth` | Audience per product & store (active/weekly vs all-time), GitHub downloads, settlements, fleet, era |
| `loot_trend` | `GET /api/hearth/trend` | Daily time series for `audience`, `population`, `settlements`, `revenue`, `fleet`, `downloads`, `country`, or `xp` |
| `loot_reviews` | `GET /api/reviews` | Rating averages, per-app/store breakdown, star distribution, rating trend, written reviews |
| `loot_funnel` | `GET /api/funnel` | Ad spend, impressions, clicks, landing page views, conversions, installs, revenue, lag, notes |
| `loot_recap` | `GET /api/recap` | Monthly (`YYYY-MM`) or yearly (`YYYY`) recap with period deltas and highlights |
| `loot_codex` | `GET /api/codex` | Lifetime totals, all-time records, achievements |
| `loot_drops` | `GET /api/drops` | Paginated feed of recent drops/events |
| `loot_stats` | `GET /api/stats` | Headline counters, XP, waiting chests, known apps |
| `loot_quests` | `GET /api/quests` | Active and recent goals |
| `loot_mysteries` | `GET /api/mysteries` | Open anomalies and solved notebook entries |
| `loot_bosses` | `GET /api/bosses` | Active and recent crash-spike bosses |
| `loot_sources` | `GET /api/sources` | Source poll status, event counts, and errors |
| `loot_apps` | `GET /api/apps` | Canonical product mappings and unmapped source apps |
| `loot_chests` | `GET /api/chest` | Waiting unopened daily chests |

Pass `--allow-writes` (`"args": ["mcp", "--allow-writes"]`) and use a write token if you also want to expose action tools: `loot_chest_open`, `loot_quest_create`, `loot_mystery_solve`, and `loot_boss_slay`.

---

## 4. Direct HTTP JSON API reference

Every read endpoint accepts `?app=<product>` to scope results to one canonical product (matching `apps:` in `loot.yaml`).

### Read endpoints

| Endpoint | Query parameters | Notes |
|---|---|---|
| `GET /api/report` | `range=7d\|30d\|90d\|365d` (default `30d`), `reviews_range=30d\|90d\|1y\|all` (default `90d`), `funnel_days=1..90` (default `14`), `include_series=0\|1`, `app` | Single-call executive snapshot; omits daily series arrays by default for compact LLM context |
| `GET /api/vault/summary` | `range=7d\|30d\|90d\|365d` (default `30d`), `app` | Settled ledger revenue, units, refunds, `prev_totals`, daily `series`, `by_source`, `by_app`, `by_country`, `subscriptions`, `realtime`, `scorecards` |
| `GET /api/hearth` | `app` | `audience` (per product & store, with `basis` and `superseded`), GitHub `downloads`, `countries`, `fleet`, `capital`, `era`, `recent` |
| `GET /api/hearth/trend` | `metric=audience\|population\|settlements\|revenue\|fleet\|downloads\|country\|xp` (required), `range=7d\|30d\|90d\|ytd\|1y\|all`, `product`, `source`, `format`, `country`, `app` | Daily time series matching Hearth's counting rules |
| `GET /api/reviews` | `range=30d\|90d\|1y\|all` (default `90d`), `source=appstore\|googleplay\|microsoftstore`, `stars=1..5\|low`, `limit=1..500` (default `200`), `app` | Rating averages, distribution, `by_app`, daily `trend`, and written `reviews` |
| `GET /api/funnel` | `days=1..90` (default `14`), `app` | Acquisition funnel stages, `as_of` lag per stage, per-ad breakdown, and automated `notes` |
| `GET /api/recap` | `month=YYYY-MM` or `season=YYYY` (omit both for last complete month), `app` | Monthly or full-year summary + available `periods` |
| `GET /api/codex` | `app` | Achievements, lifetime `totals`, and all-time `records` |
| `GET /api/stats` | `app` | XP, totals by rarity/source, country count, chests waiting, badge counts, `apps` |
| `GET /api/drops` | `limit=1..500` (default `100`), `before=<drop_id>`, `app` | Cursor-paginated feed (`next_before`), excludes unopened chests |
| `GET /api/quests` | `app` | `active` and `recent` quests |
| `GET /api/mysteries` | `app` | `open` and `resolved` anomaly mysteries |
| `GET /api/bosses` | `app` | `alive` and `recent` crash-spike bosses |
| `GET /api/sources` | — | Configured sources, poll intervals, `last_poll_at`, `last_error`, event count |
| `GET /api/apps` | — | Configured `products` and `unmapped` `(source, app)` pairs |
| `GET /api/chest` | — | Unopened daily chests, oldest first |
| `GET /api/healthz` | — | Always returns `{"ok":true,"time":"..."}` (never requires API auth) |
| `GET /ws` | `lang` (optional) | Real-time WebSocket stream of drops and chest updates |

### Write endpoints (require a `write_token` when `api:` auth is enabled)

| Endpoint | Body | Purpose |
|---|---|---|
| `POST /api/chest/open` | `{}`, `{"date":"YYYY-MM-DD"}`, or `{"all":true}` | Reveal the oldest, a specific, or all waiting chests |
| `POST /api/quests` | `{"metric","target","window","app","source","title"}` | Create a custom goal (`window` is `"week"`, `"month"`, or `{"start":"YYYY-MM-DD","end":"YYYY-MM-DD"}`) |
| `DELETE /api/quests/{id}` | — | Delete a custom quest |
| `POST /api/mysteries/{id}/solve` | `{"note":"..."}` | Mark an anomaly solved with an explanation |
| `POST /api/mysteries/{id}/dismiss` | `{}` | Dismiss an anomaly quietly |
| `POST /api/bosses/{id}/slay` | `{}` | Mark a crash-spike boss slain |

### Quick `curl` examples

```bash
# Executive overview for the last 30 days
curl -fsS -H "Authorization: Bearer $LOOT_API_TOKEN" \
  "https://loot.example.com/api/report?range=30d"

# Revenue & scorecards for a single product
curl -fsS -H "Authorization: Bearer $LOOT_API_TOKEN" \
  "https://loot.example.com/api/vault/summary?range=90d&app=Nistis"

# Low-star reviews across all stores
curl -fsS -H "Authorization: Bearer $LOOT_API_TOKEN" \
  "https://loot.example.com/api/reviews?range=all&stars=low"
```
