package appstore_test

import (
	"encoding/json"
	"testing"

	"github.com/nickhirras/loot/internal/sources/appstore"
)

// The fixture is one day of SUBSCRIPTION/SUMMARY/DAILY 1_3: one subscription
// sold in two countries, plus a second app.
const subscriptionFixture = "testdata/subscription_summary_daily.tsv"

func TestBuildSubscriptionEvents(t *testing.T) {
	events, err := appstore.BuildSubscriptionEvents(readFixture(t, subscriptionFixture), fixtureDay, nil, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want one snapshot per app", len(events))
	}

	snap := eventsByKey(events)["asc:subs:2026-08-17:1234567890"]
	if snap.Kind != appstore.KindSubscriptionSnapshot {
		t.Fatalf("kind = %q", snap.Kind)
	}
	if snap.App != "Widget Pro" {
		t.Errorf("app = %q", snap.App)
	}
	// US: 120 standard + 8 free trial + 4 offer code. DE: 45 + 5. The
	// Subscribers, Marketing Opt-Ins, Billing Retry and Grace Period columns
	// must not be added in.
	if snap.Quantity != 182 {
		t.Errorf("quantity = %d, want 182 active subscriptions", snap.Quantity)
	}
	if !snap.Silent {
		t.Error("a snapshot must be silent: it is a state reading, not news")
	}
	if snap.IsLedger {
		t.Error("a snapshot must not be a ledger event: it is a count, not money")
	}
	if snap.Chest {
		t.Error("a snapshot has no drop, so it has no business in a chest")
	}
	if snap.Day != fixtureDay || snap.Amount != 0 || snap.Currency != "" {
		t.Errorf("snapshot carries money it should not: %+v", snap)
	}

	var payload appstore.SubscriptionSnapshot
	if err := json.Unmarshal(snap.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Active != 182 || payload.Rows != 2 {
		t.Errorf("payload active/rows = %d/%d, want 182/2", payload.Active, payload.Rows)
	}
	if payload.ByCountry["US"] != 132 || payload.ByCountry["DE"] != 50 {
		t.Errorf("by_country = %v", payload.ByCountry)
	}
	if payload.BySKU["Widget Pro Yearly"] != 182 {
		t.Errorf("by_sku = %v", payload.BySKU)
	}

	other := eventsByKey(events)["asc:subs:2026-08-17:999999999"]
	if other.Quantity != 10 || other.App != "Tiny Timer" {
		t.Errorf("second app snapshot = %d for %q", other.Quantity, other.App)
	}
}

func TestBuildSubscriptionEventsAllowlist(t *testing.T) {
	events, err := appstore.BuildSubscriptionEvents(readFixture(t, subscriptionFixture), fixtureDay,
		[]string{"1234567890"}, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 1 || events[0].App != "Widget Pro" {
		t.Fatalf("allowlist produced %d events", len(events))
	}
}

func TestBuildSubscriptionEventsRejectsWrongReport(t *testing.T) {
	// A sales report has no App Apple ID column; failing loudly beats
	// emitting a snapshot of zero and wiping out the vault's subscriber count.
	if _, err := appstore.BuildSubscriptionEvents(readFixture(t, salesFixture), fixtureDay, nil, fixtureObserved); err == nil {
		t.Fatal("expected an error when handed a sales report")
	}
}
