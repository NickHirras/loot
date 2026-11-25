# Cloudflare Workers Analytics Engine (tracked links)

**Polling, every hour.** Loot runs one SQL query of yours against the
[Workers Analytics Engine SQL
API](https://developers.cloudflare.com/analytics/analytics-engine/sql-api/)
and turns the answer into *visits per tracked link per day*. Those visits fill
the "link visits" step of the funnel page, between what the ad platform says
it delivered and what the stores say was installed.

It is for counting visits to **your own** links. The usual setup is a small
redirect Worker in front of your store links: an ad points at
`https://example.com/go/spring-a`, the Worker writes one data point per hit
with the slug in a blob, and then redirects the visitor to the real store
page. Your redirector, not the ad platform, did the counting, so the number
does not depend on anyone's attribution model.

Loot does not care how the Worker is written. The query is the whole
contract.

## 1. The query

Your query must return exactly these three columns, aliased with `AS`:

| column | what it is |
|---|---|
| `day` | the day the visits belong to, `YYYY-MM-DD` (a midnight datetime such as `2026-08-17 00:00:00` is accepted too) |
| `label` | text identifying the link, typically the slug |
| `n` | how many visits; use `SUM(_sample_interval)` (see [Sampling](#sampling)) |

Everything else is yours to decide: how far back to look, which timezone
`day` is in, whether and how to filter bots, and how a label is derived from
what the Worker wrote. A typical query, for a dataset `my_links` whose Worker
writes the slug into `blob1`:

```sql
SELECT
  toDate(timestamp) AS day,
  blob1 AS label,
  SUM(_sample_interval) AS n
FROM my_links
WHERE timestamp > NOW() - INTERVAL '14' DAY
GROUP BY day, label
ORDER BY day, label
```

Things worth knowing:

- **Cover a trailing window**, like the 14 days above. Loot keeps no cursor:
  it re-runs the query every poll and stores a new snapshot only for a
  (label, day) whose count changed, so a window of a week or two keeps
  today's and yesterday's rows fresh without rewriting history. A query with
  no time filter works but reads your whole dataset every hour.
- **Timezone.** `timestamp` is UTC. To count days in your own timezone, shift
  it before taking the date, e.g. `toDate(toTimeZone(timestamp,
  'America/Chicago')) AS day` (`SHOW TIMEZONES` lists what the API accepts).
  Pick the same timezone your stores report in if you want the funnel's days
  to line up.
- **Bots.** If your Worker records a user agent or a bot score in another
  blob or double, filter on it in `WHERE`.
- **One statement.** The query must be a single `SELECT` (a `WITH … SELECT`
  is fine). Loot refuses anything else at startup rather than sending it. A
  trailing `;` is dropped.
- **Format.** Leave the `FORMAT` clause off; Loot appends `FORMAT JSON`. If
  you write one, it must be `FORMAT JSON`.
- Rows with an empty label, a `day` that is not a date, or an `n` that is not
  a non-negative number are skipped, and the poll logs one warning with how
  many. Two rows for the same (label, day) are added together.
- At most **5,000 rows** are read per poll; beyond that the rest is dropped
  with a warning. If you hit it, your label is probably something unbounded
  (a full URL with query strings) rather than a slug.

## 2. How a link is tied to an ad

On the funnel page, a link's visits are credited to the ad whose destination
URL **contains the label**, compared case-insensitively. An ad pointing at
`https://example.com/go/spring-a?utm_source=ads` picks up the visits labelled
`spring-a`.

So choose labels that appear verbatim in the ads' URLs, and that do not
contain one another: a label `spring` would also match an ad pointing at
`/go/spring-a`. A link that matches no ad is not shown against any ad.

## 3. Create a read-only API token

1. In the Cloudflare dashboard, open **My Profile → API Tokens** (or
   **Manage Account → Account API Tokens**) and choose **Create Token →
   Create Custom Token**.
2. Add one permission: **Account → Account Analytics → Read**. That is the
   permission Cloudflare's SQL API documentation names; check the exact
   wording in the dashboard, since Cloudflare renames permissions from time to
   time.
3. Under **Account Resources**, include only the account that holds the
   dataset.
4. Create it and copy the token. It is shown once.

Your **account id** is in the dashboard URL
(`dash.cloudflare.com/<account id>/…`) and on the account's overview page.

The token can read every Analytics Engine dataset on that account and
nothing else. It cannot change a Worker or write data.

## 4. Point Loot at it

```yaml
sources:
  cloudflareanalytics:
    api_token_path: /run/secrets/cloudflare_analytics_token  # or api_token: "…"
    account_id: "0123456789abcdef0123456789abcdef"
    app: com.example.app        # the app these visits are credited to
    poll_interval: 1h
    sql: |
      SELECT toDate(timestamp) AS day, blob1 AS label, SUM(_sample_interval) AS n
      FROM my_links
      WHERE timestamp > NOW() - INTERVAL '14' DAY
      GROUP BY day, label
```

`api_token` (or `api_token_path`, a file holding it), `account_id`, `app` and
`sql` are all required. `poll_interval` defaults to `1h`. Prefer
`api_token_path` or an environment variable to keeping the token in a config
file in your repo; Loot never logs it.

## 5. Check it

```bash
./bin/loot check
```

`check` runs your query once. Common failures:

| message | fix |
|---|---|
| `Cloudflare refused the token (403 …)` / `(401 …)` | the token is wrong, expired, lacks **Account Analytics: Read**, or is not scoped to this `account_id` |
| `query failed (…)` followed by Cloudflare's text | a SQL error; the text after the status is Cloudflare's own message (an unknown table is usually a dataset name typo) |
| `missing day, n (got …)` | the query's columns are not named `day`, `label` and `n`; alias them with `AS` |
| `sql must be a single SELECT statement` | the query is something else, or two statements |
| ⚠ `returned no rows` | it works, but nothing is in the window yet: check the dataset name and that the Worker is writing |

## Sampling

Analytics Engine
[samples](https://developers.cloudflare.com/analytics/analytics-engine/sampling/)
at high volume, on write and on read. Each stored row carries
`_sample_interval`, the number of original events it stands for, and that
number can differ from row to row. `COUNT()` counts stored rows and so
undercounts once sampling starts; `SUM(_sample_interval)` is Cloudflare's
recommended estimate of the true count, and it is what `n` should be.

A sampled count can be fractional. Loot rounds `n` to the nearest whole visit,
so a re-estimate that rounds to the same number is not stored as a new
snapshot. At the volumes a handful of ad links get, sampling is unlikely to
apply at all.

The API returns numbers as JSON strings (`"n":"16"`); Loot accepts either.

## What it emits

One `link_day` snapshot per (label, day) whose count changed since the last
poll, credited to `app`, dated that day at 00:00 UTC, with the label and
visits in the payload. Like every funnel event it is silent (never a drop),
never ledger money, and carries no amount or quantity: visits are traffic,
not customers. The funnel page reads the newest snapshot per (label, day).

## Limits

- Analytics Engine keeps data for **three months**, so a window longer than
  that returns nothing extra.
- The count is only as good as your Worker: a visitor who reaches the store
  without going through the redirect is not counted, and link previews,
  crawlers and prefetches are, unless your query filters them.
- Visits cannot be followed into the stores. The funnel shows visits next to
  installs; no store says which install came from which link.
