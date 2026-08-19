// Package googleplay turns Google Play's monthly sales and earnings reports
// into Loot events.
//
// This package is a placeholder: New reports that the source is not
// implemented yet, and `loot serve` only wires it when the config names a
// bucket. Play publishes CSV reports into a private Cloud Storage bucket
// (pubsite_prod_rev_<developer id>), read with a service account over OAuth2.
//
// The finished source must follow the ledger contract documented on
// core.Source: silent row events plus one "sales_day" summary per (app, day)
// with Chest set, keyed on the report so re-reading a month is free.
package googleplay
