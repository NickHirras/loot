// Package appstore turns App Store Connect sales and financial reports into
// Loot events.
//
// This package is a placeholder: New reports that the source is not
// implemented yet, and `loot serve` only wires it when the config names a key.
// The contract the finished source must honour is in core.Source:
//
//   - one silent, ledger row event per report row (Silent: true, IsLedger:
//     true, Day set to the report day, Country set, Amount/Currency in the
//     report's currency, Quantity signed so refunds are negative);
//   - one non-silent core.SalesDaySummary event per (app, day) with Kind
//     "sales_day" and Chest: true, whose drop is held for the daily chest;
//   - optionally one "subscription_snapshot" event per (app, day) whose
//     Quantity is the active subscriber count, which the vault reads.
//
// Dedupe keys must be derived from the report, never from time.Now, so a
// restated report replaces nothing and re-ingesting is free.
package appstore
