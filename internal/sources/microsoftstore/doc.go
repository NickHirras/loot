// Package microsoftstore turns Microsoft Store (Partner Center) analytics
// into Loot events.
//
// # What it reads
//
// Two endpoints of the Microsoft Store analytics API, both aggregated at
// aggregationLevel=day over a date range:
//
//   - GET /v1.0/my/analytics/appacquisitions — app acquisitions, one row per
//     (date, app, acquisitionType, market) once grouped. This is app money.
//   - GET /v1.0/my/analytics/inappacquisitions — add-on ("in-app product")
//     acquisitions, the same shape plus the add-on's id and name.
//
// and, when the account has subscription add-ons, one more for state:
//
//   - GET /v1.0/my/analytics/subscriptions — active/churn counts per day. Only
//     the counts are read; its grossSalesBeforeTax is deliberately ignored
//     because the same money is already reported by inappacquisitions and
//     would be counted twice.
//
// The set of apps comes from config; with none configured, Loot discovers them
// from GET /v1.0/my/applications (the submission API, same token).
//
// Verified against
// https://learn.microsoft.com/en-us/windows/uwp/monetize/access-analytics-data-using-windows-store-services
// and the get-app-acquisitions, get-in-app-acquisitions,
// get-subscription-acquisitions and get-all-apps pages.
//
// # Settlement
//
// Partner Center analytics is not final when it first appears: a day's numbers
// are revised upward for a day or two afterwards. So a day is only read once
// it is [settleLagDays] old, and the last few settled days are re-swept on
// every poll in case a row appeared late. Rows are aggregated by their dedupe
// key *before* they are emitted, so re-reading the same day produces exactly
// the same event set and the pipeline's dedupe drops all of it.
//
// # What it emits
//
// Per core.Source's ledger contract:
//
//   - one silent ledger row event per (day, app, add-on, market,
//     acquisitionType, currency) group — kinds sale, iap and download —
//     with Quantity = acquisitionQuantity and Amount = the group's purchase
//     price. Free, trial and promotional acquisitions are emitted as
//     `download` with a zero amount so the units and the country still count.
//   - one non-silent core.SalesDaySummary event per (app, day) with Kind
//     "sales_day" and Chest: true, whose drop the daily chest holds.
//   - one silent, non-ledger "subscription_snapshot" per (app, day) carrying
//     the active subscription count, which the vault reads.
//
// Dedupe keys are derived entirely from the report:
//
//	msstore:acq:<date>:<storeID>:<market>:<acquisitionType>:<currency>
//	msstore:iap:<date>:<storeID>:<market>:<acquisitionType>:<currency>:<addOnID>
//	microsoftstore:sales_day:<storeID>:<date>
//	msstore:subs:<date>:<storeID>
//
// # Amounts are gross customer prices
//
// purchasePrice*Amount is what the customer paid, before the Store's revenue
// share and before tax. It is not developer proceeds, so the vault reads high
// against a Partner Center payout. The payout (financial) API is a separate
// service and is out of scope; every payload carries "gross": true and says
// so. The acquisitions API also has no notion of a refund, so refunds are
// never reported here — see docs/sources/microsoftstore.md.
//
// # Authentication
//
// A Microsoft Entra (Azure AD) client-credentials token for the resource
// https://manage.devcenter.microsoft.com, from the v1 token endpoint. It is a
// plain form POST — no third-party dependency — and the token is cached until
// shortly before it expires.
package microsoftstore
