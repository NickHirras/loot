# Microsoft Store (Partner Center)

**Polling, every 6 hours.** Loot reads the [Microsoft Store analytics
API](https://learn.microsoft.com/en-us/windows/uwp/monetize/access-analytics-data-using-windows-store-services)
— the same acquisitions numbers behind Partner Center's own reports — and turns
each *settled* day into one chest drop per app, plus the silent ledger rows that
feed the vault and the Hearth.

There is no webhook here. Everything comes from
`https://manage.devcenter.microsoft.com`, authenticated with a Microsoft Entra
(Azure AD) application that you associate with your Partner Center account once.

## 1. Associate a Microsoft Entra application

The analytics API does not accept a personal login. It authenticates as an
*application* in your organisation's Microsoft Entra directory, which Partner
Center then has to recognise.

1. In [Partner Center](https://partner.microsoft.com/dashboard), go to
   **Account settings → User management** and, if you have not already,
   [associate your account with your organisation's Microsoft Entra
   directory](https://learn.microsoft.com/en-us/windows/apps/publish/partner-center/associate-azure-ad-with-partner-center).
   You need Global administrator on the directory to do this. If you have no
   directory, Partner Center will create one for free.
2. Open the **Azure AD applications** tab (newer accounts label it **Microsoft
   Entra applications**) and add the application you want Loot to be — or
   create a new one right there.
3. Give it the **Manager** role. That is what Microsoft's own documentation
   requires for analytics, and it is the only role that reliably returns data;
   Developer and Business Contributor do not.
4. Click the application's name and copy the **Tenant ID** and **Client ID**.
5. Click **Add new key** and copy the **Key** value. It is shown exactly once —
   that value is your `client_secret`. Keys expire, so note the expiry
   somewhere you will see it.

> [!NOTE]
> The role lives in **Partner Center**, not in Azure. You do not need to grant
> the application any API permissions, admin consent or Graph scopes in the
> Azure portal; the token is issued for the resource
> `https://manage.devcenter.microsoft.com` and Partner Center decides what it
> may read from its own user list.

## 2. Find your Store IDs (optional)

A Store ID looks like `9NBLGGH4R315` and is on **Partner Center → your app →
Product identity** (older dashboards: **App identity**).

Listing them is optional. With `apps` empty, Loot calls
`GET /v1.0/my/applications` on the first poll and watches every app on the
account. That endpoint belongs to the Store *submission* API, and some accounts
will not serve it — if `loot check` says so, list the Store IDs by hand and Loot
never asks again.

## 3. Point Loot at it

```yaml
sources:
  microsoftstore:
    tenant_id: "11111111-2222-3333-4444-555555555555"
    client_id: "22222222-3333-4444-5555-666666666666"
    client_secret: "the key value from Partner Center"
    apps: []            # optional: only these Store IDs
    backfill_days: 30
```

The section counts as *configured* only when `tenant_id`, `client_id` and
`client_secret` are all set. Every field has an environment override, so a
container needs no config file:

| env | field |
|---|---|
| `LOOT_MICROSOFTSTORE_TENANT_ID` | `tenant_id` |
| `LOOT_MICROSOFTSTORE_CLIENT_ID` | `client_id` |
| `LOOT_MICROSOFTSTORE_CLIENT_SECRET` | `client_secret` |
| `LOOT_MICROSOFTSTORE_APPS` | `apps` (comma separated) |
| `LOOT_MICROSOFTSTORE_BACKFILL_DAYS` | `backfill_days` |

`client_secret` is a credential for your whole account's sales data. Prefer the
environment variable or a secrets file over a config file in your repo.

## 4. Check it

```bash
./bin/loot check
```

```
✓ microsoftstore  1 app(s)
```

`check` mints a real token and then asks Partner Center for real data: one
settled day of the first configured app, or the applications list when no apps
are configured. A day with no sales counts as a pass — it proves the
credentials were accepted.

Common failures and what they mean:

| message | fix |
|---|---|
| `AADSTS7000215` | the client secret is wrong or expired — add a new key in Partner Center |
| `AADSTS700016` | `client_id` is not an application in that directory |
| `AADSTS90002` | `tenant_id` does not name a Microsoft Entra directory |
| `403 … Partner Center` | the application is not in **Account settings → User management → Azure AD applications**, or does not have the **Manager** role |
| `no apps found` | discovery is unavailable on this account; set `apps` to your Store IDs |

## What it reads

| endpoint | what Loot takes from it |
|---|---|
| `analytics/appacquisitions` | app acquisitions per day, market and acquisition type |
| `analytics/inappacquisitions` | add-on (in-app product) acquisitions, same shape plus the add-on |
| `analytics/subscriptions` | how many subscriptions were active that day |
| `applications` | the app list, when `apps` is empty |

Every request asks for `aggregationLevel=day` and a `groupby` of exactly the
dimensions Loot keys on — `date`, `applicationName`, `acquisitionType`,
`market`, and the add-on name. That is not an optimisation: without `groupby`
the API collapses the whole range to one row per day and the **market** never
appears, which would leave the Hearth with no settlements. Age group and gender
are never requested.

Responses page with `@nextLink`, which Loot follows; only the path and query of
that link are used, so a rewritten link can never send your token elsewhere.

## What it emits

| event | kind | notes |
|---|---|---|
| one **silent ledger row** per (day, app, add-on, market, acquisition type, currency) | `sale`, `iap`, `download` | this is where the money lives |
| one **`sales_day` summary** per (app, day) | `sales_day` | non-silent, chest-bound, `core.SalesDaySummary` payload |
| one **snapshot** per (app, day) when the account has subscriptions | `subscription_snapshot` | silent, *not* a ledger event — an absolute count, not a payment |

Acquisition types map to kinds by whether money moved: **Paid**, **Iap**,
**Subscription Iap**, **Pre Order**, **Disk** and **Prepaid Code** are money
(`sale`, or `iap` for anything bought inside an app), and **Free**, **Trial**,
**Promotional code**, **Private Audience** and **Xbox Game Pass** are
`download` — units and a country, with the amount pinned at zero. An unfamiliar
acquisition type is money if it arrives with a price and a download if it does
not, so a type Microsoft adds tomorrow shows up rather than vanishing.

Dedupe keys are derived entirely from the report, never from the clock:

```
msstore:acq:<date>:<storeID>:<market>:<acquisitionType>:<currency>
msstore:iap:<date>:<storeID>:<market>:<acquisitionType>:<currency>:<addOnID>
microsoftstore:sales_day:<storeID>:<date>
msstore:subs:<date>:<storeID>
```

## Settlement, and why yesterday is not enough

Partner Center analytics is not final when it first appears: a day's numbers
keep being revised upward for a day or two. A `sales_day` summary is emitted
once and only once, so reporting a half-finished day would be wrong forever.

Loot therefore treats a day as **settled** only when it is **three days old**,
and reads nothing newer. On 18 August the newest day it will touch is the 15th.
It also re-reads the last three settled days on every poll, in case a row
arrived late: rows are aggregated onto their dedupe key *before* anything is
emitted, so a re-read that finds nothing new produces byte-identical events and
the pipeline's dedupe swallows all of them. Free acquisitions have their money
cleared before they are grouped, not after, so two free rows that differ only
in whether the API reported a `localCurrencyCode` fold into one group with one
key rather than into two groups that collide on the same key at emit time.

The persisted state is one cursor per Store ID (`last_settled_day`), so adding
an app later backfills only that app. That cursor moves **only when the app
acquisitions call actually succeeded**. Partner Center answers "no data for
this query" and "no such app" with the same 404, so a mistyped Store ID or a
lapsed Entra association would otherwise read as a very quiet month while the
cursor walked straight past it; instead the app's cursor stays put, the error
lands in `last_error`, and the other apps carry on. The add-on
(`inappacquisitions`) call is the exception — most apps have no add-ons, and
its 404 is the normal answer.

The first poll reads `backfill_days` (30 by default) of history, capped at a
year, and turns every settled day into a chest. `loot serve --since 2026-01-01`
overrides that once for a one-off bootstrap.

## Caveats

**Amounts are gross customer prices.** `purchasePriceLocalAmount` /
`purchasePriceUSDAmount` are what the customer paid, before the Store's revenue
share and before tax. They are *not* developer proceeds, so the vault will read
high against a Partner Center payout. Every payload carries `"gross": true` and
says so. The payout (financial) API is a different service and is out of scope.

**Amounts are row totals, not unit prices.** Each row is already an aggregate of
`acquisitionQuantity` acquisitions, so its price is the group's total and is
booked as-is. Multiplying by the quantity would inflate every multi-unit row.

**Currency.** Microsoft's documented field table for these endpoints lists no
currency at all, and its sample responses carry none. When a response does
include `localCurrencyCode`, Loot books `purchasePriceLocalAmount` in that
currency; when it does not, it books `purchasePriceUSDAmount` as USD, because a
local amount whose currency is unknown cannot be converted. Either way the
vault converts into your `display_currency`, and a day that mixed currencies is
marked `proceeds_mixed` with the split in `payload.by_currency`.

**Tax is not deducted.** `purchaseTax*Amount` is the tax already inside the
purchase price. Subtracting it would produce neither gross revenue nor
proceeds, so it is left in and carried in the payload for reference.

**There are no refunds.** The acquisitions API has no refund, return or
chargeback row, so `refunds` on every Microsoft Store summary is `0` — which
means zero *reported*, not zero *happened*. Revenue here is never netted down
by a return. (The subscriptions endpoint does count refund churn, but not the
money behind it.)

**A revised day is not re-revised.** Once a row's dedupe key has been ingested,
a later restatement of the same group carries the same key and is dropped. A
genuinely new group — a market that only reported later, say — is picked up by
the re-sweep. This is a deliberate trade: a day can never be double counted,
at the cost of missing a small upward revision of an already-counted group.

**Subscription money is deliberately ignored.** A subscription add-on is an
add-on, so its money already arrived as an `iap` row from `inappacquisitions`.
Loot reads only the counts from `analytics/subscriptions`
(`totalActiveCount`, new, renewed, churned), which is what the vault's
`subscriptions.active` reports. An **app** with no subscription add-ons stops
being asked after three empty answers, and is asked again a week later — the
streak is per Store ID, so one app without subscriptions cannot switch the
query off for one that has them. An app that has reported before and then
missed some polls catches up on the settled days it skipped, up to a week of
them; only the newest settled day counts towards the empty-answer streak.

**Desktop (MSIX/Win32) apps.** These endpoints cover Store apps and games. The
Windows Desktop Application program has its own installs and errors endpoints,
which Loot does not read.


## Field notes (first real setup, Aug 2026)

- A **personal (individual) developer account has no Entra tenant**. Partner Center → Account settings → Tenants → *Developer* tab → **Create Microsoft Entra ID** makes a free one-person tenant; then **Associate Microsoft Entra ID** on that same *Developer* tab (the tenant created from the Commercial side is *not* automatically associated with the developer program).
- The **Microsoft Entra applications** tab only appears in User management after you **Sign in with Microsoft Entra ID** (the tenant's admin) *and* the tenant is associated on the Developer tab. Creating the app from there shows it twice (the registration and its service principal) — same Client ID; open the first one for **Add new key**.
- Right after creation the analytics API answers `401 {"error":"User Unauthorized due to AMS call failure."}` (and sometimes 429/404) until the association propagates. `loot check` explains this; wait a few minutes and retry.
