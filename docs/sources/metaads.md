# Meta ads (Facebook and Instagram)

**Polling, every hour.** Loot reads the [Meta Marketing
API](https://developers.facebook.com/docs/marketing-api/insights/) for what
each of your ads delivered on each day (spend, impressions, clicks, landing
page views and the conversion you care about) and, if you give it a pixel,
how often each event on that pixel fired. It feeds the funnel page and puts
one ads drop in each finished day's chest.

Everything is read-only. Loot never creates, edits, pauses or pays for
anything in your ad account, and the token it needs cannot either.

## 1. Get a read-only token

Use a **system user** token from your Business portfolio rather than your own
login: it is not tied to your personal account, it does not expire when you
change your password, and it can be limited to exactly one ad account and one
permission. Meta moves these screens around, so treat the steps below as a
map and follow the labels in Meta's own UI.

1. In **Business settings** (Meta Business Suite → Settings), open **Users →
   System users** and add a system user with the **Employee** role (not
   Admin).
2. On that system user, **Assign assets**: pick your **ad account** with
   view-only access ("View performance" or the equivalent read-only option),
   and your **pixel / dataset** the same way if you want site events.
3. Make sure a Meta **app** is connected to the business (Business settings →
   Accounts → Apps). The token is issued through it; if Meta asks for a
   product, the Marketing API is the one this needs.
4. Back on the system user, **Generate new token**, choose that app, and tick
   **`ads_read`** only. Leave every other permission unticked. Choose a token
   expiry you can live with; a "never" token means one less thing to renew,
   and an expiring one means Loot will tell you when it lapses.
5. Copy the token. It is shown once.

You also need the **ad account id**: Ads Manager shows it in the account
picker and in the URL as `act=1234567890`. Loot accepts it with or without
the `act_` prefix. The **pixel id** (optional) is on Events Manager → Data
sources → your pixel → Settings.

## 2. Point Loot at it

```yaml
sources:
  metaads:
    access_token_path: /run/secrets/meta_ads_token   # or access_token: "..."
    ad_account_id: "act_1234567890"
    app: "My App"                 # the app these ads are filed under
    pixel_id: "5550001"           # optional
    conversion_actions:           # optional, but see below
      - offsite_conversion.custom.1234567890123456
    # api_version: v27.0          # optional; Loot pins a current version
    # backfill_days: 30           # first poll
    # lookback_days: 7            # every later poll
    # poll_interval: 1h
```

`access_token` is a key to your ad account's reporting. Prefer
`access_token_path` (a file holding only the token) or an environment
variable over pasting it into a config file in your repo. Loot never logs it:
it is stripped from every URL and error message before they reach a log or
`last_error`.

`app` is the app name the ad events are filed under. It goes through your
`apps:` mapping like any other source's app name, so use the same name the
stores report, or map it.

## 3. Check it

```bash
./bin/loot check
```

`check` makes one read of the ad account. It fails with a clear message when
the token has expired or been revoked (Graph error 190), when it lacks
`ads_read` or access to that account, or when the account id is wrong. It
passes with a **warning** when:

- no `conversion_actions` are configured: everything else works, but no
  conversion will be credited to any ad;
- the ad account is not active (disabled, unsettled...): it will read, but
  report no new delivery;
- the account's timezone is unknown to Loot: days are read as UTC.

## What `conversion_actions` means

Meta reports every outcome it attributes to an ad as an **action** with an
`action_type`: `link_click`, `landing_page_view`, `purchase`, and one per
custom conversion or custom pixel event. `conversion_actions` lists the
action types that count as *your* conversion (a store button tap on your
landing page, say). Their values are summed per ad per day into
`conversions`.

- A **custom conversion** reads as `offsite_conversion.custom.<id>`, where
  `<id>` is the custom conversion's id: open Events Manager → Custom
  conversions, select it, and copy the id from its details (or from the URL).
- A **custom pixel event** sent with `fbq('trackCustom', ...)` reads as
  `offsite_conversion.fb_pixel_custom` (all custom events together).
- A **standard event** reads as `offsite_conversion.fb_pixel_<event>`, for
  example `offsite_conversion.fb_pixel_lead`.

When in doubt, open Ads Manager, add the conversion as a column, and compare:
or call the insights edge yourself with `fields=actions` and look at which
`action_type` carries the number you expect.

An ad is **tracked** when Meta reported one of your conversion action types
for it on any day in the window Loot asked about. An ad with spend, zero
conversions and `tracked: false` is far more likely to be missing its
tracking than failing to convert, and the funnel page says so.

## What it reads

| request | what Loot takes from it |
|---|---|
| `GET /act_<id>?fields=name,currency,timezone_name,account_status` | the account's timezone and currency |
| `GET /act_<id>/insights?level=ad&time_increment=1&time_range=...` | one row per (ad, day): spend, impressions, reach, `inline_link_clicks`, `actions` |
| `GET /act_<id>/ads?fields=id,name,effective_status,creative{...}` | each ad's status and the URLs its creative points at |
| `GET /<pixel_id>/stats?aggregation=event` | hourly per-event counts, when `pixel_id` is set |

Insights are requested with `use_account_attribution_setting=true`, so the
numbers follow the attribution setting on your ad account and match what Ads
Manager shows. Link clicks are `inline_link_clicks`; landing page views are
the `landing_page_view` action.

Ad URLs are not modelled: creatives differ by format and change between API
versions, so Loot walks the creative and keeps every `http(s)` address it
finds, skipping image, video and thumbnail fields and Meta's own CDN, up to
ten per ad. They are what ties an ad to a tracked link on the funnel page.

Every list is paged with Graph's `paging.next`. Those links carry your
token; Loot keeps only their path and query, re-adds the token itself, and
sends them to the configured Graph host and nowhere else.

## What it emits

| event | kind | notes |
|---|---|---|
| one **snapshot** per (ad, day) | `ad_day` | silent, `core.AdDay` payload |
| one **snapshot** per (pixel event, day) | `site_event_day` | silent, `core.SiteEventDay` payload, only with `pixel_id` |
| one **summary** per completed day with spend | `ads_day` | non-silent, chest-bound, `core.AdsDaySummary` payload |

None of these is a ledger event, and all carry an amount and quantity of zero:
ad spend is not income and never reaches the vault.

Dedupe keys:

```
metaads:ad_day:<ad_id>:<day>:<fingerprint>
metaads:site_event_day:<event>:<day>:<fingerprint>
metaads:ads_day:<app>:<day>
```

The fingerprint is a short hash of the payload. A poll that finds a day
unchanged produces the same key and is dropped; a day Meta revised gets a
new key and is stored as a newer snapshot. Readers always take the newest
snapshot per (ad, day) and never add snapshots together.

The `ads_day` summary is news, so it is emitted once: for day D as soon as D
is over in the ad account's timezone, and only if the account spent anything
that day. The first poll after you configure the source summarizes yesterday
only, not the whole backfill. After an outage, the days missed are
summarized on the next poll (as far back as `backfill_days`).

## Limits

**Attribution keeps changing for a week.** Meta credits a conversion to the
click that earned it, sometimes days later. Loot re-reads the last
`lookback_days` (7) on every poll so those revisions arrive as new
snapshots, but the `ads_day` drop is written once, when the day ends, and
shows the numbers as they stood then. The funnel page shows the revised ones.

**Days are the ad account's days.** Meta reports in the timezone set on the
ad account, and so does Loot, so an ads day can be offset from a store's day
(App Store report days are Pacific time, for example). Pixel stats come in hourly
UTC buckets and are folded into the account's days; in a timezone with a
half-hour offset, one hour a day lands on the neighbouring day.

**Pixel stats only reach back seven days.** That is all the pixel stats edge
serves, so `site_event_day` history starts a week before you configure the
source.

**No per-ad installs.** Neither store tells anyone which ad an install came
from. Meta's own install numbers need its SDK in the app, which Loot does not
assume. The funnel page shows installs from the stores alongside ad numbers,
for the same days, and never claims an ad caused them.

**Archived and deleted ads.** The ads list may leave out ads that are archived
or deleted; their delivery still arrives from insights, but without a status
or URLs.

**Rate limits.** A poll makes four or five requests, plus one per extra page. If Meta throttles the
account anyway (Graph error codes 4, 17, 32, 613 or 80000-80014), the poll
fails with `rate limited by Meta`, nothing is lost, and the next poll tries
again.
