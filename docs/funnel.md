# Funnel: from ad spend to the store

If you pay for ads, the question every morning is the same: what did
yesterday's money buy, and how far did those people get? The **funnel** puts
the answer on one page, one column per day, from the top (spend) to the
bottom (revenue), and points out the few things in it worth a look.

It appears by itself once an ads source or a link-analytics source has
reported something. Before that there is no funnel page and nothing to
configure.

---

## The stages

| Stage | What it counts | Where it comes from |
| --- | --- | --- |
| Spend | What the ad platform charged, converted into your display currency | the ads source ([Meta ads](sources/metaads.md)) |
| Impressions | Times an ad was shown | the ads source |
| Link clicks | Clicks on an ad's link | the ads source |
| Landing page views | Clicks whose page actually loaded | the ads source |
| Conversions | Your chosen conversion (`funnel.conversion_label`), as the ad platform attributes it to an ad | the ads source |
| Link visits | Visits your own tracked links (a redirector, short links) counted | the link source ([Cloudflare analytics](sources/cloudflareanalytics.md)) |
| Site events | One named website event (`funnel.site_event`), across all visitors | your site analytics |
| Installs | New installs, for every customer | the stores |
| Purchases | Paid units, for every customer | the stores' financial reports |
| Revenue | Banked revenue, for every customer | the stores' financial reports |

Spend sits in the ad account's currency and is converted with the same
exchange rates as the vault (the cached ECB rates, or the snapshot built into
Loot). Spend in a currency with no known rate is left out rather than shown
in the wrong unit.

**Link visits** belong to an ad when the ad's destination URL contains the
link's label as a whole path piece: label `ad-14` belongs to
`https://example.com/go/ad-14?utm=…`, and label `4` does not. A label that two
ads both point at is counted for the day but for neither ad, because it cannot
be split honestly. Labels no ad points at are not counted at all; if no ad
reports a URL, the day counts every link instead.

**Installs, purchases and revenue** are the same numbers the rest of Loot
uses, by the same rules:

- Installs are a store's own install report where it has one (Google Play,
  Flathub). For a store without one (App Store, Microsoft Store), they are the
  first acquisitions in its financial report: free downloads and paid app
  sales. Updates and refunds are not installs.
- Purchases are the vault's paid units: app sales, in-app purchases and
  subscription payments.
- Revenue is the vault's revenue: store ledger rows only, refunds netted, and
  RevenueCat's realtime estimates left out.

## Why a stage can be blank

A blank cell means Loot has no reading for it yet. A zero means a reading
that came back zero. The page keeps the two apart, because "the store has not
reported Tuesday yet" read as "nobody installed on Tuesday" is the most
misleading thing a funnel can say.

A day is blank for a stage when nothing that reports that stage has reported
that day or any later one. Ad platforms report within hours; stores often take
one to three days. So the newest columns usually have spend and views but blank
installs. The report says how far each stage has got (`as_of` in the API).

With several stores, the newest one decides: a day the App Store has reported
but Google Play has not yet shows the App Store's installs alone until Play
catches up.

Ad numbers also change after the fact. A platform keeps revising a day for a
week or so as late conversions are attributed to earlier clicks. Loot stores
each revision and always shows the newest one, never a sum of them.

## Notes

Notes are observations, not advice. The page shows at most one of each, apart
from the per-ad one, which names up to three ads.

| Note | Shown when |
| --- | --- |
| `untracked` | There is spend, but the platform reports the chosen conversion for none of the ads. Zero conversions then says nothing about the ads. |
| `spend_no_conversions` | A tracked ad has spent at least `spend_without_conversion` in the range with no conversion. |
| `concentrated` | Three or more ads ran (spent, or were active), and one took at least `concentration_pct` of the spend. |
| `cost_rising` | The latest finished day with at least 10 landing page views cost at least 1.5 times as much per view as the up to three such days before it. Today never counts. |
| `store_lag` | The ads have been reported for newer days than the stores, so the newest installs are not in yet. |
| `budget_check` | Only if you set `checkpoint_spend`: spend in the range has reached it, and there were fewer than `min_conversions_per_100_views` conversions per 100 landing page views. |

## Config

Everything is optional.

```yaml
funnel:
  # Your name for the conversion the ad platform counts.
  conversion_label: "Store button tap"
  # A website event, as your site analytics names it, to show as a stage.
  site_event: ""
  notes:
    spend_without_conversion: 20     # display currency
    concentration_pct: 80
    checkpoint_spend: 0              # 0 leaves budget_check off
    min_conversions_per_100_views: 2
```

## API and CLI

`GET /api/funnel?days=14` returns the report for the trailing `days` (1 to
90, counting today, UTC). It takes `?app=` like every other read endpoint.

`loot funnel` prints yesterday's column (or the newest finished day with ad
data), the trailing seven days, any stage that is behind, and the notes.
`--days`, `--app`, `--json` and `--url` work as you would expect.

## What it cannot tell you

- **No store says which ad an install came from.** Installs, purchases and
  revenue are every customer's, ad or not. Read them next to the ad stages,
  not as their result. The CLI shows a cost per step for the ad stages only,
  for this reason.
- **Store data is late.** The newest days are blank at the bottom, and a
  conclusion drawn from them is premature.
- **Site events count everyone.** A site event fires for every visitor, paid
  or not, so it can be higher than the ad platform's conversions.
- **Conversions are the ad platform's attribution**, with its own windows and
  its own modelling. They are a useful signal and not a count of people.
