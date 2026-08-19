# Crash sources

Three ways to get crashes into Loot, where they become [bosses](../bosses.md) — named monsters with health bars that drain as your fix rolls out, and pay an epic drop when they die.

| | Mode | Knows about | Can say "it stopped" | Setup |
|---|---|---|---|---|
| **[Android vitals](#android-vitals)** (`playvitals`) | polls every 6h | app versions, crashes **and** ANRs, distinct users | **yes** | reuses your Play service account, plus one API to enable |
| **[Sentry](#sentry)** (`sentry`) | webhook | issues, titles, user counts, resolutions | no | an internal integration, five minutes |
| **[Generic](#generic-crash-webhook)** (`crash`) | webhook | whatever you post | no | `curl` |

Any one of them is enough. Android vitals is the one that makes the mechanic complete, and the reason is worth understanding before you choose.

### Why "can say it stopped" matters

A push-only reporter can tell Loot a crash *happened*. It can never tell Loot a crash *stopped*, because silence and a revoked API key look identical from here.

So a polling source that genuinely looked at a day emits a `crash_day` heartbeat — *"I checked, and the whole app's total was N"* — including when N is zero. That one event is the difference between a boss being **slain** (the source is fine, this crash is gone: you won) and merely **faded** (the source went quiet, Loot has no idea, and says so, silently, with no reward).

Sentry and the generic webhook cannot honestly claim to have looked, so their abandoned fights fade rather than claiming a kill nobody earned. Both can still be *slain* explicitly — by resolving the issue upstream, or by clicking **Mark slain**.

---

## Android vitals

Daily crash and ANR counts per app version, from the [Google Play Developer Reporting API](https://developers.google.com/play/developer/reporting).

It deliberately has **no credentials of its own**. The same service-account key that reads your Play reporting bucket reads vitals, so there is exactly one place a Play credential lives.

### Setup

You need three things, and the second one is the step everybody misses.

**1. A service account with a JSON key.** If you already have `sources.googleplay` working, you have this. Otherwise: Google Cloud console → IAM & Admin → Service Accounts → Create, then Keys → Add key → JSON.

**2. Enable the API.** In the *same Cloud project that owns the service account*, enable **`playdeveloperreporting.googleapis.com`**:

```sh
gcloud services enable playdeveloperreporting.googleapis.com --project=<your-project>
```

or the console's API Library. It takes a minute or two to propagate. Until it does, every request answers 403 `SERVICE_DISABLED` — Loot recognises that one and tells you exactly this.

**3. Grant the service account access to the app.** Play Console → **Users and permissions** → Invite user → the service account's email (`something@project.iam.gserviceaccount.com`) → add your apps → grant **"View app information and download bulk reports (read-only)"**.

Then:

```yaml
sources:
  googleplay:
    # Vitals borrows this. It is fine to configure it for vitals alone and
    # leave `bucket` empty; the sales source simply will not start.
    service_account_json_path: /etc/loot/play-reports.json

  playvitals:
    enabled: true
    # Package names. Empty falls back to sources.googleplay.packages.
    # Vitals are per app — the API has no "every app" query — so one of the
    # two lists must be set.
    packages:
      - com.example.yourapp
    # How far back the first poll reads. 30 is exactly enough for the boss
    # engine's 28-day baseline to exist on the very first evaluation.
    backfill_days: 30
```

Environment overrides: `LOOT_PLAYVITALS_ENABLED`, `LOOT_PLAYVITALS_PACKAGES` (comma separated), `LOOT_PLAYVITALS_BACKFILL_DAYS`.

Check it with `loot check`:

```
✓ playvitals   1 package(s)
```

### What it reads

Three queries per package per poll, all `POST …:query` against the metric sets:

| metric set | dimensions | metrics | what for |
|---|---|---|---|
| `errorCountMetricSet` | `versionCode`, `reportType` | `errorReportCount`, `distinctUsers` | the actual counts — the numbers a health bar is made of |
| `crashRateMetricSet` | — | `crashRate` | the per-user rate the Play Console shows you |
| `anrRateMetricSet` | — | `anrRate` | the same, for ANRs |

Plus a `GET` on `errorCountMetricSet` first, for its `freshnessInfo`. **Reading freshness first is not optional**: vitals lag the calendar by a day or two, and querying past the edge answers with a silently short result rather than an error — which would look exactly like a crash that stopped.

`reportType: NON_FATAL` is ignored. A handled exception is not a crash, and counting it as one would spawn bosses for logging.

### What it emits

Per `(package, day, versionCode, CRASH|ANR)`, a silent `crash` event with `Quantity` = `errorReportCount` and a payload of `{version, users_affected, crash_rate, anr_rate, kind}`.

Plus one silent `crash_day` heartbeat per `(package, day)` carrying the app-wide total — the event that lets a quiet day be told apart from a missing one. Heartbeats are never invented for days before the app had any data at all, so a brand new app does not arrive with thirty days of fabricated zeros.

Cursor is one settled day per package, so a six-hourly poll asks about nothing it has already read.

### When it goes wrong

Loot turns the two 403s everybody hits into instructions rather than a status code, because both are configuration mistakes made once, in a console, months before the error appears:

| what you see | what to do |
|---|---|
| *"the Google Play Developer Reporting API is not enabled in the Cloud project…"* | step 2 above; wait a minute after enabling |
| *"permission denied. In Play Console > Users and permissions, invite the service account's email…"* | step 3 above |
| *"no such app. Check the package name is exactly as it appears in Play Console"* | a typo in `packages` |

### A note on version codes

Play reports a numeric `versionCode`, not your marketing version. Boss names render it as *build 4812* rather than pretending to know a version string nobody told them. The `valueLabel` Play sends alongside it (usually *"4.2.0 (4812)"*) becomes the boss's subtitle.

---

## Sentry

Real-time crash issues over a webhook at `POST /hooks/sentry`.

### Setup

In Sentry: **Settings → Developer Settings → Custom Integrations → Create New Integration → Internal Integration**.

- **Webhook URL**: `https://<your loot>/hooks/sentry`
- **Permissions**: *Issue & Event* → **Read**
- **Webhooks**: tick **issue**
- Save, then copy the **Client Secret**.

```yaml
sources:
  sentry:
    enabled: true
    client_secret: "…the integration's Client Secret…"
```

Environment overrides: `LOOT_SENTRY_ENABLED`, `LOOT_SENTRY_CLIENT_SECRET`.

Leaving `client_secret` empty makes the endpoint accept unverified deliveries. Loot warns about it at startup and `loot check` reports it as ⚠ — fine on a private network, a bad idea anywhere else, because anyone who finds the URL can spawn bosses in your dashboard.

### Verification

Sentry signs each delivery with `Sentry-Hook-Signature`: an HMAC-SHA256 of the request body, hex encoded, keyed with the integration's Client Secret. Loot hashes **the exact bytes that arrived**, which is both simpler and more correct than Sentry's own documented sample (it re-serializes the parsed JSON, which works in Node only by coincidence of key ordering).

### What it maps

Loot reads the `Sentry-Hook-Resource` header:

| resource | action | becomes |
|---|---|---|
| `issue` | `created`, `unresolved`, `regressed`, `reopened` | a `crash` event, `Quantity` = the issue's `count` |
| `issue` | `resolved`, `archived`, `ignored` | a `crash_resolved` signal — **this slays the boss** |
| `issue` | `assigned`, anything else | nothing (200, no event) |
| `event_alert`, `error` | `triggered` | one `crash` event per event, deduped on Sentry's `event_id` |
| `installation`, `metric_alert`, `comment` | any | nothing (200) |

`archived` is the current spelling of what Sentry used to call `ignored`; both are honoured, because both still arrive in the wild.

The app is the project slug. `data.issue.count` is the issue's *lifetime* event count rather than today's, so issue deliveries are deduped per `(issue, day)` — which makes a day's number "how big this issue is" rather than "how many times it happened today". For a health bar that is the more useful of the two.

**For an honest daily count, add an issue alert rule** with a *"Send a notification via <your integration>"* action. Those arrive as `event_alert` deliveries, one per event, deduped on Sentry's own event id.

### Slaying from Sentry

Resolving an issue in Sentry slays the boss in Loot, immediately — the fix shipped, and waiting two more days for the graph to agree would be pedantry.

---

## Generic crash webhook

`POST /hooks/crash`, for everything else — including **Crashlytics**, which has no data API at all.

```yaml
sources:
  crash:
    enabled: true
    secret: "hunter2"
```

Environment overrides: `LOOT_CRASH_ENABLED`, `LOOT_CRASH_SECRET`. The secret is checked as `Authorization: Bearer <secret>` or a bare `<secret>`, because half the tools that can POST JSON cannot set a prefix.

### The body

One object, or an array of them (up to 500).

```json
{
  "app": "com.example.app",
  "version": "2.3.1",
  "issue_id": "a1b2c3",
  "title": "NullPointerException in SyncWorker.onRun",
  "crashes": 312,
  "users_affected": 91,
  "kind": "crash",
  "occurred_at": "2026-08-12",
  "boss": true,
  "resolved": false,
  "url": "https://console.firebase.google.com/…",
  "id": "velocity-alert-4812"
}
```

| field | required | notes |
|---|---|---|
| `app` | **yes** | a crash with no app cannot be grouped or baselined |
| `version` | no | Loot keys a fight on it, so reporting it is what makes "the fix rolled out" visible |
| `issue_id`, `title` | no | identify one cluster inside a version; omit and the whole version is one fight |
| `crashes` | no | defaults to 1. May be 0 only alongside `resolved` |
| `users_affected` | no | the other way a boss spawns: 50 distinct people in a day |
| `kind` | no | `crash` (default) or `anr` |
| `occurred_at` | no | RFC3339 **or** a bare `YYYY-MM-DD`. Defaults to now, and decides which day's total this joins |
| `boss` | no | force a spawn regardless of the baseline |
| `resolved` | no | slay the boss |
| `url` | no | the *Open the issue ↗* link on the card |
| `id` | no | makes a retry idempotent — see below |

**Counts add up within a day.** Each post contributes its `crashes` to that `(app, version, issue, day)` total, so a script may report deltas as often as it likes. Without an `id`, two identical posts are two genuine reports — silently swallowing the second would be the worse mistake. Supply `id` when you want a retry to collapse.

### Crashlytics

Crashlytics has **no REST API for reading crash data**. The only supported ways out are a BigQuery export and the Firebase Alerts event stream, so the practical route into Loot is a small Cloud Function relaying alerts.

```js
// functions/index.js — Firebase Functions v2
const {
  onVelocityAlertPublished,
  onNewFatalIssuePublished,
  onRegressionAlertPublished,
} = require("firebase-functions/v2/alerts/crashlytics");
const { defineSecret } = require("firebase-functions/params");

const LOOT_URL = "https://loot.example.com/hooks/crash";
const LOOT_SECRET = defineSecret("LOOT_CRASH_SECRET");

async function toLoot(body) {
  await fetch(LOOT_URL, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${LOOT_SECRET.value()}`,
    },
    body: JSON.stringify(body),
  });
}

// A velocity alert is Crashlytics saying "this one is bad" out loud, which is
// exactly what `boss: true` is for — it carries a real crash count, so Loot
// does not have to work the severity out from numbers it was never given.
exports.lootVelocity = onVelocityAlertPublished(
  { secrets: [LOOT_SECRET] },
  async (event) => {
    const { issue, crashCount, firstVersion } = event.data.payload;
    await toLoot({
      app: event.appId,
      version: firstVersion || issue.appVersion,
      issue_id: issue.id,
      title: issue.title,
      crashes: crashCount,
      boss: true,
      id: `velocity:${issue.id}`,
      url: `https://console.firebase.google.com/project/_/crashlytics/app/${event.appId}/issues/${issue.id}`,
    });
  },
);

// A brand new fatal issue is one crash, reported honestly. Enough of them in a
// day and the baseline notices on its own.
exports.lootNewFatal = onNewFatalIssuePublished(
  { secrets: [LOOT_SECRET] },
  async (event) => {
    const { issue } = event.data.payload;
    await toLoot({
      app: event.appId,
      version: issue.appVersion,
      issue_id: issue.id,
      title: issue.title,
      crashes: 1,
    });
  },
);

// A regression is the same crash coming back. It is a fight worth restarting.
exports.lootRegression = onRegressionAlertPublished(
  { secrets: [LOOT_SECRET] },
  async (event) => {
    const { issue } = event.data.payload;
    await toLoot({
      app: event.appId,
      version: issue.appVersion,
      issue_id: issue.id,
      title: issue.title,
      crashes: 1,
      boss: true,
      id: `regression:${issue.id}:${event.id}`,
    });
  },
);
```

Deploy with `firebase deploy --only functions`, and set the secret with `firebase functions:secrets:set LOOT_CRASH_SECRET`. The other handlers on the same module — `onNewNonfatalIssuePublished`, `onNewAnrIssuePublished` (post it with `"kind": "anr"`), `onStabilityDigestPublished` — relay the same way.

Because Crashlytics cannot tell Loot a crash stopped, its bosses fade rather than being slain. Either click **Mark slain** when you ship the fix, or post `{"app": "…", "version": "…", "issue_id": "…", "crashes": 0, "resolved": true}` from your release script — which is the more satisfying of the two, because the epic drop lands as the build goes out.

### Anything else

The endpoint has no opinion about where a crash came from. A Bugsnag webhook proxied through a two-line function, a `grep` over a server log in cron, a desktop app's own crash handler — if it can `curl`, it can spawn a boss.

```sh
curl -X POST https://loot.example.com/hooks/crash \
  -H 'Authorization: Bearer hunter2' \
  -d '{"app":"my-daemon","version":"1.4.2","crashes":1,"title":"panic: nil map write"}'
```
