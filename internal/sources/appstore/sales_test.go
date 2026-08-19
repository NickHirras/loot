package appstore_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/appstore"
)

// The fixture is one day of SALES/SUMMARY/DAILY 1_1 for two apps: paid
// downloads split over two devices, an in-app purchase, an auto-renewable
// subscription, a refund, a free promo-code download, an update row, and a
// German row whose proceeds are in euros.
const salesFixture = "testdata/sales_summary_daily.tsv"

const fixtureDay = "2026-08-17"

var fixtureObserved = time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC)

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestParseSalesReport(t *testing.T) {
	rows, err := appstore.ParseSalesReport(readFixture(t, salesFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 9 {
		t.Fatalf("got %d rows, want 9", len(rows))
	}

	first := rows[0]
	if first.SKU != "widget-pro" || first.Title != "Widget Pro" || first.AppleID != "1234567890" {
		t.Errorf("row 0 identity = %+v", first)
	}
	if first.ProductType != "1" || first.Units != 3 {
		t.Errorf("row 0 product/units = %q/%d", first.ProductType, first.Units)
	}
	if first.ProceedsPerUnit != 0.70 || first.ProceedsCurrency != "USD" {
		t.Errorf("row 0 proceeds = %v %s", first.ProceedsPerUnit, first.ProceedsCurrency)
	}
	if first.CustomerPrice != 0.99 || first.CustomerCurrency != "USD" || first.Country != "US" {
		t.Errorf("row 0 customer side = %v %s %s", first.CustomerPrice, first.CustomerCurrency, first.Country)
	}
	// Columns are addressed by name, so the trailing ones still land right.
	if first.Device != "iPhone" || first.OrderType != "PURCHASE" || first.Category != "Productivity" {
		t.Errorf("row 0 tail columns = %q/%q/%q", first.Device, first.OrderType, first.Category)
	}
	if rows[4].Units != -1 {
		t.Errorf("refund row units = %d, want -1", rows[4].Units)
	}
}

// eventsByKey indexes emitted events by dedupe key.
func eventsByKey(events []core.Event) map[string]core.Event {
	out := make(map[string]core.Event, len(events))
	for _, ev := range events {
		out[ev.DedupeKey] = ev
	}
	return out
}

func TestBuildSalesEventsRows(t *testing.T) {
	events, err := appstore.BuildSalesEvents(readFixture(t, salesFixture), fixtureDay, nil, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// 8 row events (the update row is counted, never emitted) + 2 summaries.
	if len(events) != 10 {
		t.Fatalf("got %d events, want 10", len(events))
	}

	byKey := eventsByKey(events)
	cases := []struct {
		key      string
		kind     string
		quantity int
		amount   float64
		currency string
		country  string
	}{
		{"asc:sales:2026-08-17:1234567890:widget-pro:US:1:USD:0", appstore.KindSale, 3, 2.10, "USD", "US"},
		// Same app, SKU, country, product type and currency: only the trailing
		// index keeps the iPad row from colliding with the iPhone one.
		{"asc:sales:2026-08-17:1234567890:widget-pro:US:1:USD:1", appstore.KindSale, 2, 1.40, "USD", "US"},
		{"asc:sales:2026-08-17:1234567890:widget.pro.tip:US:IA1:USD:0", appstore.KindIAP, 2, 7.00, "USD", "US"},
		{"asc:sales:2026-08-17:1234567890:widget.pro.yearly:US:IAY:USD:0", appstore.KindSubscription, 4, 27.96, "USD", "US"},
		{"asc:sales:2026-08-17:1234567890:widget-pro:US:1:USD:2", appstore.KindRefund, -1, -0.70, "USD", "US"},
		{"asc:sales:2026-08-17:1234567890:widget-pro:US:1F:USD:0", appstore.KindDownload, 12, 0, "USD", "US"},
		{"asc:sales:2026-08-17:1234567890:widget-pro:DE:1:EUR:0", appstore.KindSale, 5, 3.25, "EUR", "DE"},
		{"asc:sales:2026-08-17:999999999:tiny-timer:JP:1:JPY:0", appstore.KindSale, 2, 200, "JPY", "JP"},
	}

	for _, c := range cases {
		ev, ok := byKey[c.key]
		if !ok {
			t.Errorf("no event with dedupe key %q", c.key)
			continue
		}
		if ev.Kind != c.kind {
			t.Errorf("%s: kind = %q, want %q", c.key, ev.Kind, c.kind)
		}
		if ev.Quantity != c.quantity {
			t.Errorf("%s: quantity = %d, want %d", c.key, ev.Quantity, c.quantity)
		}
		if ev.Amount != c.amount {
			t.Errorf("%s: amount = %v, want %v", c.key, ev.Amount, c.amount)
		}
		if ev.Currency != c.currency || ev.Country != c.country {
			t.Errorf("%s: currency/country = %s/%s", c.key, ev.Currency, ev.Country)
		}
		if !ev.Silent || !ev.IsLedger || ev.Chest {
			t.Errorf("%s: row events must be silent ledger rows outside the chest: %+v", c.key, ev)
		}
		if ev.Day != fixtureDay {
			t.Errorf("%s: day = %q", c.key, ev.Day)
		}
		if !ev.OccurredAt.Equal(time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: occurred_at = %v, want the report day at midnight UTC", c.key, ev.OccurredAt)
		}
		if ev.Source != appstore.Name {
			t.Errorf("%s: source = %q", c.key, ev.Source)
		}
	}

	// The App field is the human name, not the numeric id.
	if got := byKey["asc:sales:2026-08-17:1234567890:widget-pro:US:1:USD:0"].App; got != "Widget Pro" {
		t.Errorf("app = %q, want the report Title", got)
	}

	// Update rows produce no event at all.
	for _, ev := range events {
		if ev.Kind == "update" {
			t.Errorf("an update row was emitted as an event: %+v", ev)
		}
	}

	// Row payloads keep the customer-facing price alongside the proceeds.
	var row appstore.SalesRow
	if err := json.Unmarshal(byKey["asc:sales:2026-08-17:1234567890:widget-pro:US:1:USD:0"].Payload, &row); err != nil {
		t.Fatalf("row payload: %v", err)
	}
	if row.CustomerPrice != 0.99 || row.CustomerCurrency != "USD" {
		t.Errorf("payload customer price = %v %s", row.CustomerPrice, row.CustomerCurrency)
	}
	if row.AppleID != "1234567890" || row.SKU != "widget-pro" {
		t.Errorf("payload identity = %+v", row)
	}
}

func TestBuildSalesEventsSummary(t *testing.T) {
	events, err := appstore.BuildSalesEvents(readFixture(t, salesFixture), fixtureDay, nil, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	summary, ok := eventsByKey(events)["appstore:sales_day:1234567890:2026-08-17"]
	if !ok {
		t.Fatal("no sales_day summary for the first app")
	}
	if summary.Kind != appstore.KindSalesDay {
		t.Errorf("kind = %q, want sales_day", summary.Kind)
	}
	if !summary.Chest || !summary.IsLedger || summary.Silent {
		t.Errorf("a summary must be a non-silent ledger event bound for the chest: %+v", summary)
	}
	if summary.App != "Widget Pro" {
		t.Errorf("app = %q", summary.App)
	}
	if summary.Country != "" {
		t.Errorf("country = %q, want empty: a summary spans countries", summary.Country)
	}
	// Paid units: 3 + 2 app, 2 IAP, 4 subscription, 5 German. Refunds and the
	// 12 free downloads are excluded, as are the 40 updates.
	if summary.Quantity != 16 {
		t.Errorf("quantity = %d, want 16 paid units", summary.Quantity)
	}
	// USD is the dominant proceeds currency: 2.10 + 1.40 + 7.00 + 27.96 - 0.70.
	if summary.Amount != 37.76 || summary.Currency != "USD" {
		t.Errorf("amount = %v %s, want 37.76 USD", summary.Amount, summary.Currency)
	}

	var payload appstore.SalesDayPayload
	if err := json.Unmarshal(summary.Payload, &payload); err != nil {
		t.Fatalf("summary payload: %v", err)
	}
	if payload.Units != 16 || payload.Refunds != 1 {
		t.Errorf("units/refunds = %d/%d, want 16/1", payload.Units, payload.Refunds)
	}
	if payload.Proceeds != 37.76 || payload.Currency != "USD" {
		t.Errorf("proceeds = %v %s", payload.Proceeds, payload.Currency)
	}
	if payload.Countries != 2 || payload.TopCountry != "US" {
		t.Errorf("countries = %d, top = %q, want 2 / US", payload.Countries, payload.TopCountry)
	}
	if payload.Downloads != 12 || payload.Updates != 40 {
		t.Errorf("downloads/updates = %d/%d, want 12/40", payload.Downloads, payload.Updates)
	}
	if payload.IAPUnits != 2 || payload.SubUnits != 4 {
		t.Errorf("iap/subscription units = %d/%d, want 2/4", payload.IAPUnits, payload.SubUnits)
	}
	if !payload.ProceedsMixed {
		t.Error("a day paid in both USD and EUR must be marked as mixed")
	}
	if got := payload.ByCurrency["EUR"]; got != 3.25 {
		t.Errorf("by_currency EUR = %v, want 3.25", got)
	}
	if got := payload.ByCurrency["USD"]; got != 37.76 {
		t.Errorf("by_currency USD = %v, want 37.76", got)
	}
	if payload.AppleID != "1234567890" || payload.Date != fixtureDay {
		t.Errorf("payload identity = %s / %s", payload.AppleID, payload.Date)
	}
	// by_sku proceeds are stated in the summary's own currency, so they add up
	// to the headline; the euros live in by_currency.
	if sku := payload.BySKU["widget.pro.yearly"]; sku.Units != 4 || sku.Proceeds != 27.96 {
		t.Errorf("by_sku yearly = %+v, want 4 units / 27.96", sku)
	}
	if sku := payload.BySKU["widget-pro"]; sku.Proceeds != 2.80 {
		t.Errorf("by_sku widget-pro proceeds = %v, want 2.80 (USD only)", sku.Proceeds)
	}
	if payload.ByProductType["7"] != 40 {
		t.Errorf("by_product_type 7 = %d, want the 40 updates", payload.ByProductType["7"])
	}
	if payload.ByCountry["DE"] != 5 {
		t.Errorf("by_country DE = %d, want 5", payload.ByCountry["DE"])
	}

	// The second app gets its own summary, in its own currency.
	other, ok := eventsByKey(events)["appstore:sales_day:999999999:2026-08-17"]
	if !ok {
		t.Fatal("no sales_day summary for the second app")
	}
	if other.App != "Tiny Timer" || other.Currency != "JPY" || other.Amount != 200 {
		t.Errorf("second summary = %s %v %s", other.App, other.Amount, other.Currency)
	}
}

func TestBuildSalesEventsAppsAllowlist(t *testing.T) {
	events, err := appstore.BuildSalesEvents(readFixture(t, salesFixture), fixtureDay,
		[]string{"999999999"}, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 1 row + 1 summary for the allowed app", len(events))
	}
	for _, ev := range events {
		if ev.App != "Tiny Timer" {
			t.Errorf("allowlist leaked %q", ev.App)
		}
	}
}

func TestBuildSalesEventsRejectsGarbage(t *testing.T) {
	if _, err := appstore.BuildSalesEvents([]byte("<html>nope</html>"), fixtureDay, nil, fixtureObserved); err == nil {
		t.Fatal("expected an error for a body that is not a report")
	}
}

func TestBuildSalesEventsReordersColumnsSafely(t *testing.T) {
	// Two columns swapped and one appended: parsing by name must survive it.
	report := "Units\tProduct Type Identifier\tCountry Code\tCurrency of Proceeds\tDeveloper Proceeds (per unit)\tApple Identifier\tSKU\tTitle\tSomething New\n" +
		"7\t1\tGB\tGBP\t0.50\t1234567890\twidget-pro\tWidget Pro\tignored\n"

	events, err := appstore.BuildSalesEvents([]byte(report), fixtureDay, nil, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want a row and a summary", len(events))
	}
	if events[0].Amount != 3.50 || events[0].Currency != "GBP" || events[0].Quantity != 7 {
		t.Errorf("row = %v %s x%d", events[0].Amount, events[0].Currency, events[0].Quantity)
	}
}

func TestUpdatesOnlyDayMintsNoSummary(t *testing.T) {
	// Seen on first real contact: a day whose only row was an update (7F)
	// produced a "0 sales · 0.00" chest drop. Updates carry no money and no
	// new customer, so the day should stay silent.
	report := "Units\tProduct Type Identifier\tCountry Code\tCurrency of Proceeds\tDeveloper Proceeds (per unit)\tApple Identifier\tSKU\tTitle\n" +
		"1\t7F\tUS\tUSD\t0\t6763687102\tmacro\tMacro Trainer\n"

	events, err := appstore.BuildSalesEvents([]byte(report), fixtureDay, nil, fixtureObserved)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == appstore.KindSalesDay {
			t.Fatalf("updates-only day produced a sales_day summary: %+v", ev)
		}
	}
}
