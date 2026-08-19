// Package appstore turns App Store Connect sales and financial reports into
// Loot events.
//
// # What it reads
//
// Two reports from GET /v1/salesReports, both DAILY and both keyed on a
// Pacific calendar day:
//
//   - SALES / SUMMARY, version 1_1 — every unit sold, refunded or downloaded,
//     one row per (app, SKU, country, product type, currency, device…). This
//     is the money.
//   - SUBSCRIPTION / SUMMARY, version 1_3 — how many subscriptions were
//     active that day. This is a state snapshot, not a transaction log.
//
// Both arrive as a gzipped TSV, and Apple signals "the report is not generated
// yet" and "nothing happened that day" with the same 404, which is why
// [Source.Poll] treats a missing report as "come back later" rather than as an
// error.
//
// # What it emits
//
// Per core.Source's ledger contract:
//
//   - one silent, ledger row event per meaningful report row (Silent: true,
//     IsLedger: true, Day = the report day, Country set, Amount = developer
//     proceeds × units in the row's proceeds currency, Quantity signed so a
//     refund is negative). Kinds: sale, iap, subscription, refund, download.
//     Update rows are counted into the summary but never emitted — an update
//     is not a sale.
//   - one non-silent core.SalesDaySummary event per (app, day) with Kind
//     "sales_day" and Chest: true, whose drop is held for the daily chest.
//   - one "subscription_snapshot" event per (app, day) whose Quantity is the
//     active subscriber count, which the vault reads.
//
// Dedupe keys are derived entirely from the report, never from time.Now, so a
// restated report replaces nothing and re-ingesting a day is free:
//
//	asc:sales:<date>:<appleID>:<sku>:<country>:<productType>:<currency>:<i>
//	appstore:sales_day:<appleID>:<date>
//	asc:subs:<date>:<appleID>
//
// The trailing index of a row key disambiguates rows that are identical in
// every keyed field — Apple does split one group across several rows.
//
// # Authentication
//
// A short-lived ES256 JWT signed with the .p8 key from App Store Connect
// (Users and Access > Integrations). See jwt.go; no third-party dependency is
// involved, and the token is cached until just before it expires.
package appstore
