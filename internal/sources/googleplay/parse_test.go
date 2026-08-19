package googleplay_test

import (
	"strings"
	"testing"

	"github.com/nickhirras/loot/internal/sources/googleplay"
)

func TestDecodeTextEncodings(t *testing.T) {
	// The statistics reports have historically been UTF-16LE with a BOM, while
	// the sales CSV is plain UTF-8; both turn up, so the encoding is sniffed.
	utf16le := []byte{0xFF, 0xFE, 'D', 0, 'a', 0, 't', 0, 'e', 0}
	utf16be := []byte{0xFE, 0xFF, 0, 'D', 0, 'a', 0, 't', 0, 'e'}
	utf8bom := append([]byte{0xEF, 0xBB, 0xBF}, []byte("Date")...)

	for name, input := range map[string][]byte{
		"utf-16le": utf16le,
		"utf-16be": utf16be,
		"utf-8bom": utf8bom,
		"utf-8":    []byte("Date"),
	} {
		if got := googleplay.DecodeText(input); got != "Date" {
			t.Errorf("DecodeText(%s) = %q, want %q", name, got, "Date")
		}
	}
}

func TestParseInstallsOverviewUTF16(t *testing.T) {
	raw := readFixture(t, "installs_com.example.app_202608_overview.csv")
	if raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatal("the overview fixture is not UTF-16LE with a BOM; the test proves nothing")
	}

	rows, err := googleplay.ParseInstallsCSV(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	first := rows[0]
	if first.Date != "2026-08-15" || first.Package != "com.example.app" {
		t.Errorf("date/package = %s/%s", first.Date, first.Package)
	}
	if first.DailyUserInstalls != 110 || first.DailyDeviceInstalls != 120 {
		t.Errorf("user/device installs = %d/%d, want 110/120",
			first.DailyUserInstalls, first.DailyDeviceInstalls)
	}
	if first.ActiveDeviceInstalls != 4100 || first.TotalUserInstalls != 5400 {
		t.Errorf("active/total = %d/%d", first.ActiveDeviceInstalls, first.TotalUserInstalls)
	}
	if !first.HasUserInstalls || first.Installs() != 110 {
		t.Errorf("Installs() = %d, want the user figure", first.Installs())
	}
	if first.Country != "" {
		t.Errorf("overview row has a country %q", first.Country)
	}
}

func TestParseInstallsCountry(t *testing.T) {
	rows, err := googleplay.ParseInstallsCSV(readFixture(t, "installs_com.example.app_202608_country.csv"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	if rows[0].Country != "US" || rows[0].Installs() != 72 {
		t.Errorf("first row = %s/%d", rows[0].Country, rows[0].Installs())
	}
	if rows[1].Country != "DE" || rows[1].Installs() != 38 {
		t.Errorf("second row = %s/%d", rows[1].Country, rows[1].Installs())
	}
	// "ZZ" is not a country; the parser drops it so no settlement is founded
	// on a placeholder.
	if rows[3].Country != "" {
		t.Errorf("ZZ survived as %q", rows[3].Country)
	}
}

func TestParseInstallsFallsBackToDeviceInstalls(t *testing.T) {
	// An older export with no "Daily User Installs" column at all.
	csv := "Date,Package Name,Daily Device Installs,Active Device Installs\n" +
		"2026-08-15,com.example.app,120,4100\n"
	rows, err := googleplay.ParseInstallsCSV([]byte(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].HasUserInstalls {
		t.Error("HasUserInstalls set on a report without the column")
	}
	if rows[0].Installs() != 120 {
		t.Errorf("Installs() = %d, want the device figure", rows[0].Installs())
	}
}

func TestParseInstallsAcceptsRenamedActiveColumn(t *testing.T) {
	// Current exports say "Installs on Active Devices" where older ones said
	// "Active Device Installs".
	csv := "Date,Package Name,Daily User Installs,Installs on Active Devices\n" +
		"2026-08-15,com.example.app,110,4100\n"
	rows, err := googleplay.ParseInstallsCSV([]byte(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rows[0].ActiveDeviceInstalls != 4100 {
		t.Errorf("active devices = %d, want 4100", rows[0].ActiveDeviceInstalls)
	}
}

func TestParseSalesCSVAcceptsLegacyColumnNames(t *testing.T) {
	// The pre-2023 report: "Product ID" instead of "Package ID", "Buyer
	// Country" instead of "Country of Buyer".
	csv := "Order Number,Order Charged Date,Financial Status,Product Title,Product ID," +
		"Product Type,SKU ID,Buyer Currency,Amount (Merchant Currency),Buyer Country\n" +
		"GPA.9,\"Aug 15, 2026\",Charged,Old Game,com.example.old,inapp,gems,GBP,\"1,234.50\",GB\n"

	rows, err := googleplay.ParseSalesCSV([]byte(csv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.Package != "com.example.old" || r.Country != "GB" || r.Currency != "GBP" {
		t.Errorf("row = %+v", r)
	}
	if r.ChargedAmount != 1234.50 {
		t.Errorf("charged amount = %v, want 1234.50 (thousands separator)", r.ChargedAmount)
	}
	if r.Kind() != "iap" {
		t.Errorf("kind = %q, want iap", r.Kind())
	}
	if day := r.Day(nil); day != "2026-08-15" {
		t.Errorf("day = %q, want the localized date normalized", day)
	}
}

func TestSaleRowKinds(t *testing.T) {
	for _, tc := range []struct {
		productType, status, want string
	}{
		{"paid app", "Charged", "sale"},
		{"inapp", "Charged", "iap"},
		{"subscription", "Charged", "subscription"},
		{"subs", "Charged", "subscription"},
		{"paid app", "Refund", "refund"},
		{"inapp", "Chargeback", "refund"},
		{"", "Charged", "sale"},
	} {
		r := googleplay.SaleRow{ProductType: tc.productType, FinancialStatus: tc.status, ChargedAmount: 5}
		if got := r.Kind(); got != tc.want {
			t.Errorf("%q/%q kind = %q, want %q", tc.productType, tc.status, got, tc.want)
		}
	}
}

func TestParseSalesCSVRejectsAForeignFile(t *testing.T) {
	_, err := googleplay.ParseSalesCSV([]byte("Something,Else\n1,2\n"))
	if err == nil || !strings.Contains(err.Error(), "Order Number") {
		t.Errorf("error = %v, want a complaint about the missing Order Number column", err)
	}
}
