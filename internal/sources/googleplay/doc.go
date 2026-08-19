// Package googleplay turns Google Play's monthly reports into Loot events.
//
// Play does not offer a sales API. It writes reports as files into a private
// Cloud Storage bucket named pubsite_prod_rev_<developer id>, which a service
// account with "View financial data" reads. Loot polls that bucket every six
// hours over the plain GCS JSON API — listing with
//
//	GET /storage/v1/b/{bucket}/o?prefix=...&fields=items(name,updated,size,md5Hash),nextPageToken
//
// and downloading with
//
//	GET /storage/v1/b/{bucket}/o/{urlencoded name}?alt=media
//
// Two report families are ingested:
//
//   - sales/salesreport_YYYYMM.zip — the *estimated sales* report, one CSV of
//     order lines, rewritten every day for the whole month. Each row becomes a
//     silent ledger event, and each settled (app, day) also gets one
//     non-silent "sales_day" summary whose drop is filed into that day's
//     chest. Amounts are what the customer was charged: Play's service fee is
//     not deducted, so these are gross figures. The monthly *earnings* report
//     is the net truth and is ingested in a later quest.
//
//   - stats/installs/installs_<package>_YYYYMM_<dimension>.csv — the install
//     statistics. Only the "overview" and "country" dimensions are read; the
//     rest (device, os_version, carrier, language, app_version, tablets) are
//     ignored. Overview rows produce silent "install" and "active_devices"
//     events plus one non-silent "installs_day" per day, so installs drop like
//     Flathub's do. Country rows produce silent per-country "install" events,
//     which is what founds settlements.
//
// Everything is keyed on the report, never on the clock: re-reading a month is
// free. A monthly file is only downloaded when its md5Hash has changed since
// the last poll, and the summary for a day is emitted once — but late rows for
// an already-summarized day are still stored, because the vault sums the rows
// rather than the summaries.
//
// Financial report dates are Pacific Time; statistics dates are UTC. Because
// the current month's sales file is still being rewritten, a day is only
// summarized once it is older than yesterday in Pacific Time.
//
// Buyer city, state and postal code are read past and never stored: a drop
// does not need to know which street the money came from.
package googleplay
