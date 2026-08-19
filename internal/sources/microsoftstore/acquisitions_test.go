package microsoftstore

import (
	"testing"
	"time"
)

func TestNormalizeRow(t *testing.T) {
	tests := []struct {
		name     string
		row      apiAcquisition
		addOn    bool
		want     Acquisition
		wantKind string
		drop     bool
	}{
		{
			name: "a paid app acquisition in USD",
			row: apiAcquisition{
				Date: "2026-08-14", ApplicationID: "9NB", ApplicationName: "Tide Clock",
				AcquisitionType: "Paid", Market: "us", AcquisitionQuantity: 2,
				PurchasePriceUSDAmount: 5.98, PurchasePriceLocalAmount: 5.98,
			},
			want: Acquisition{
				Date: "2026-08-14", StoreID: "9NB", App: "Tide Clock", AcquisitionType: "paid",
				Market: "US", Quantity: 2, Amount: 5.98, Currency: "USD", Rows: 1, Gross: true,
			},
			wantKind: KindSale,
		},
		{
			name: "a local currency wins when its code is known",
			row: apiAcquisition{
				Date: "2026-08-14T00:00:00", ApplicationID: "9NB", AcquisitionType: "Paid", Market: "DE",
				AcquisitionQuantity: 1, PurchasePriceUSDAmount: 4.31,
				PurchasePriceLocalAmount: 3.99, LocalCurrencyCode: "eur",
			},
			want: Acquisition{
				Date: "2026-08-14", StoreID: "9NB", AcquisitionType: "paid", Market: "DE",
				Quantity: 1, Amount: 3.99, Currency: "EUR", Rows: 1, Gross: true,
			},
			wantKind: KindSale,
		},
		{
			name: "a promotional code is a download",
			row: apiAcquisition{
				Date: "2026-08-14", ApplicationID: "9NB", AcquisitionType: "Promotional code",
				Market: "JP", AcquisitionQuantity: 4,
			},
			want: Acquisition{
				Date: "2026-08-14", StoreID: "9NB", AcquisitionType: "promotional-code",
				Market: "JP", Quantity: 4, Rows: 1,
			},
			wantKind: KindDownload,
		},
		{
			name: "a paid add-on with no price is still an iap",
			row: apiAcquisition{
				Date: "2026-08-14", ApplicationID: "9NB", InAppProductID: "9NC",
				InAppProductName: "Pro Pack", AcquisitionType: "paid", Market: "US",
				AcquisitionQuantity: 1,
			},
			addOn: true,
			want: Acquisition{
				Date: "2026-08-14", StoreID: "9NB", AddOnID: "9NC", AddOnName: "Pro Pack",
				AcquisitionType: "paid", Market: "US", Quantity: 1, Rows: 1, AddOn: true,
			},
			wantKind: KindIAP,
		},
		{
			name: "subscription add-on acquisitions are iap",
			row: apiAcquisition{
				Date: "2026-08-14", ApplicationID: "9NB", AcquisitionType: "Subscription Iap",
				Market: "US", AcquisitionQuantity: 1, PurchasePriceUSDAmount: 9.99,
			},
			want: Acquisition{
				Date: "2026-08-14", StoreID: "9NB", AcquisitionType: "subscription-iap",
				Market: "US", Quantity: 1, Amount: 9.99, Currency: "USD", Rows: 1, Gross: true,
			},
			wantKind: KindIAP,
		},
		{
			name: "an unparsable date is dropped",
			row:  apiAcquisition{Date: "not a date", AcquisitionQuantity: 3},
			drop: true,
		},
		{
			name: "an empty row is dropped",
			row:  apiAcquisition{Date: "2026-08-14", ApplicationID: "9NB"},
			drop: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalize(tc.row, tc.addOn)
			if tc.drop {
				if ok {
					t.Fatalf("row was kept: %+v", got)
				}
				return
			}
			if !ok {
				t.Fatal("row was dropped")
			}
			if got != tc.want {
				t.Errorf("normalize:\n got %+v\nwant %+v", got, tc.want)
			}
			if kind := kindFor(got); kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
		})
	}
}

func TestDedupeKeyIsStable(t *testing.T) {
	app := Acquisition{Date: "2026-08-14", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD"}
	if got, want := app.dedupeKey(), "msstore:acq:2026-08-14:9NB:US:paid:USD"; got != want {
		t.Errorf("app key = %q, want %q", got, want)
	}

	addOn := app
	addOn.AddOn = true
	addOn.AddOnID = "9NC"
	addOn.AcquisitionType = "iap"
	if got, want := addOn.dedupeKey(), "msstore:iap:2026-08-14:9NB:US:iap:USD:9NC"; got != want {
		t.Errorf("add-on key = %q, want %q", got, want)
	}

	// The same group described with different casing and spacing must key the
	// same, because Microsoft spells acquisitionType differently in the two
	// endpoints.
	loud, _ := normalize(apiAcquisition{
		Date: "2026-08-14", ApplicationID: "9NB", AcquisitionType: "Promotional code",
		Market: "us", AcquisitionQuantity: 1,
	}, false)
	quiet, _ := normalize(apiAcquisition{
		Date: "2026-08-14T00:00:00", ApplicationID: "9NB", AcquisitionType: "promotional  CODE",
		Market: "US", AcquisitionQuantity: 1,
	}, false)
	if loud.dedupeKey() != quiet.dedupeKey() {
		t.Errorf("keys diverge on casing: %q vs %q", loud.dedupeKey(), quiet.dedupeKey())
	}
}

func TestGroupFoldsRows(t *testing.T) {
	rows := []Acquisition{
		{Date: "2026-08-14", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD", Quantity: 3, Amount: 8.97, Rows: 1, Gross: true},
		{Date: "2026-08-14", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD", Quantity: 1, Amount: 2.99, Rows: 1, Gross: true},
		{Date: "2026-08-14", StoreID: "9NB", App: "Tide Clock", Market: "DE", AcquisitionType: "paid", Currency: "EUR", Quantity: 2, Amount: 5.98, Rows: 1, Gross: true},
	}
	got := group(rows)
	if len(got) != 2 {
		t.Fatalf("groups = %d, want 2", len(got))
	}
	// Sorted by dedupe key: DE before US.
	if got[0].Market != "DE" || got[1].Market != "US" {
		t.Fatalf("groups are not ordered by key: %+v", got)
	}
	if got[1].Quantity != 4 || got[1].Amount != 11.96 || got[1].Rows != 2 {
		t.Errorf("folded US group = %+v, want 4 units / 11.96 / 2 rows", got[1])
	}

	// Grouping the same rows in a different order produces the same result,
	// which is what makes re-reading a settled day free.
	shuffled := []Acquisition{rows[2], rows[1], rows[0]}
	reordered := group(shuffled)
	for i := range got {
		if got[i] != reordered[i] {
			t.Errorf("group %d differs by input order:\n %+v\n %+v", i, got[i], reordered[i])
		}
	}
}

func TestBuildEventsIgnoresDaysOutsideTheWindow(t *testing.T) {
	observed := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	rows := []Acquisition{
		{Date: "2026-08-12", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD", Quantity: 1, Amount: 1, Rows: 1},
		{Date: "2026-08-14", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD", Quantity: 1, Amount: 2, Rows: 1},
		{Date: "2026-08-16", StoreID: "9NB", Market: "US", AcquisitionType: "paid", Currency: "USD", Quantity: 9, Amount: 99, Rows: 1},
	}

	events, err := BuildEvents("9NB", "Tide Clock", rows, "2026-08-13", "2026-08-15", observed)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (one row and its summary)", len(events))
	}
	for _, ev := range events {
		if ev.Day != "2026-08-14" {
			t.Errorf("event for %s escaped the window", ev.Day)
		}
		if ev.ObservedAt != observed {
			t.Errorf("observed_at = %v, want %v", ev.ObservedAt, observed)
		}
	}
	if events[0].App != "Tide Clock" {
		t.Errorf("app = %q, want the name passed in when rows carry none", events[0].App)
	}
}

func TestBuildEventsEmptyDayEmitsNothing(t *testing.T) {
	events, err := BuildEvents("9NB", "Tide Clock", nil, "2026-08-13", "2026-08-15", time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0: a day with no acquisitions mints no chest", len(events))
	}
}

func TestWindows(t *testing.T) {
	tests := []struct {
		from, to string
		size     int
		want     []window
	}{
		{from: "2026-08-01", to: "2026-08-03", size: 90, want: []window{{"2026-08-01", "2026-08-03"}}},
		{from: "2026-08-01", to: "2026-08-05", size: 2, want: []window{
			{"2026-08-01", "2026-08-02"}, {"2026-08-03", "2026-08-04"}, {"2026-08-05", "2026-08-05"},
		}},
		{from: "2026-08-05", to: "2026-08-01", size: 30, want: nil},
	}
	for _, tc := range tests {
		got := windows(tc.from, tc.to, tc.size)
		if len(got) != len(tc.want) {
			t.Fatalf("windows(%s,%s,%d) = %v, want %v", tc.from, tc.to, tc.size, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("window %d = %v, want %v", i, got[i], tc.want[i])
			}
		}
	}
}

func TestWindowStartResweepsSettledDays(t *testing.T) {
	s := &Source{BackfillDays: 30}

	// First run: the backfill floor.
	if got := s.windowStart(state{}, "9NB", "2026-08-18", "2026-08-15"); got != "2026-07-19" {
		t.Errorf("first run start = %q, want 2026-07-19", got)
	}

	// Caught up: re-read the last three settled days.
	st := state{Seeded: true, LastSettledDay: map[string]string{"9NB": "2026-08-15"}}
	if got := s.windowStart(st, "9NB", "2026-08-18", "2026-08-15"); got != "2026-08-13" {
		t.Errorf("caught up start = %q, want 2026-08-13", got)
	}

	// Behind: resume at the cursor rather than skipping forward.
	st.LastSettledDay["9NB"] = "2026-06-01"
	if got := s.windowStart(st, "9NB", "2026-08-18", "2026-08-15"); got != "2026-06-02" {
		t.Errorf("behind start = %q, want 2026-06-02", got)
	}

	// Very behind: clamped to the retention horizon.
	st.LastSettledDay["9NB"] = "2020-01-01"
	if got := s.windowStart(st, "9NB", "2026-08-18", "2026-08-15"); got != "2025-08-18" {
		t.Errorf("stale start = %q, want 2025-08-18", got)
	}
}

func TestDominantCurrency(t *testing.T) {
	agg := newDayAggregate("9NB")
	agg.byCurrency = map[string]float64{"USD": 10, "EUR": 40, "GBP": 40}
	currency, amount := agg.dominantCurrency()
	// A tie breaks alphabetically so a re-read reports the same headline.
	if currency != "EUR" || amount != 40 {
		t.Errorf("dominant = %v %s, want 40 EUR", amount, currency)
	}

	empty := newDayAggregate("9NB")
	if currency, amount := empty.dominantCurrency(); currency != "" || amount != 0 {
		t.Errorf("a day with no money = %v %s, want empty", amount, currency)
	}
}
