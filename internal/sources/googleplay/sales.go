package googleplay

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// salesObjectRe matches the monthly estimated sales report,
// e.g. sales/salesreport_202608.zip.
var salesObjectRe = regexp.MustCompile(`^sales/salesreport_(\d{6})\.zip$`)

// salesPrefix is the bucket folder holding the estimated sales reports.
const salesPrefix = "sales/"

// grossNote travels in every sales payload. Estimated sales are what the
// customer was charged, before Play's 15–30% service fee and before any
// withholding: the monthly earnings report is the net truth.
const grossNote = "gross customer charge; Play's service fee is not deducted (see the earnings report for net)"

// SaleRow is one order line of the estimated sales report.
//
// The buyer's city, state and postal code are columns of the report and are
// deliberately not fields here: they are personal data that no drop needs.
type SaleRow struct {
	OrderNumber     string
	ChargedDate     string // as written by Play, Pacific Time
	Timestamp       string
	FinancialStatus string
	DeviceModel     string
	ProductTitle    string
	Package         string
	ProductType     string
	SKU             string
	Currency        string
	ItemPrice       float64
	TaxesCollected  float64
	ChargedAmount   float64
	Country         string
}

// ParseSalesZip reads the monthly sales zip and returns its order lines. The
// archive holds one CSV, but every CSV member is read so a month that Play
// splits does not lose half its money.
func ParseSalesZip(raw []byte) ([]SaleRow, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("googleplay: open sales zip: %w", err)
	}

	var rows []SaleRow
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || lowerExt(f.Name) != ".csv" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("googleplay: open %s in sales zip: %w", f.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxObjectBytes))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("googleplay: read %s in sales zip: %w", f.Name, err)
		}
		part, err := ParseSalesCSV(data)
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// lowerExt returns the lowercased extension of a zip member name.
func lowerExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i:])
	}
	return ""
}

// ParseSalesCSV reads one estimated sales CSV.
//
// Column names are resolved through the header with aliases, because Play has
// renamed them over the years: "Product ID" is now "Package ID", and older
// exports say "Buyer Country" where current ones say "Country of Buyer".
func ParseSalesCSV(raw []byte) ([]SaleRow, error) {
	h, records, err := readCSV(raw)
	if err != nil {
		return nil, fmt.Errorf("googleplay: sales report: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	if !h.has("order number", "order id") {
		return nil, fmt.Errorf("googleplay: sales report has no \"Order Number\" column (got %d columns)", len(h))
	}

	rows := make([]SaleRow, 0, len(records))
	for _, rec := range records {
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		row := SaleRow{
			OrderNumber:     h.get(rec, "order number", "order id"),
			ChargedDate:     h.get(rec, "order charged date", "transaction date"),
			Timestamp:       h.get(rec, "order charged timestamp", "transaction timestamp"),
			FinancialStatus: h.get(rec, "financial status", "transaction type"),
			DeviceModel:     h.get(rec, "device model"),
			ProductTitle:    h.get(rec, "product title"),
			Package:         h.get(rec, "package id", "product id", "package name"),
			ProductType:     h.get(rec, "product type"),
			SKU:             h.get(rec, "sku id", "sku"),
			Currency:        strings.ToUpper(h.get(rec, "currency of sale", "buyer currency")),
			ItemPrice:       parseFloat(h.get(rec, "item price")),
			TaxesCollected:  parseFloat(h.get(rec, "taxes collected")),
			ChargedAmount:   parseFloat(h.get(rec, "charged amount", "amount (merchant currency)")),
			Country:         strings.ToUpper(h.get(rec, "country of buyer", "buyer country")),
		}
		if row.OrderNumber == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Day returns the row's business day (YYYY-MM-DD in Pacific Time), or "" when
// the report carried neither a parsable date nor a parsable timestamp.
func (r SaleRow) Day(loc *time.Location) string {
	if d, ok := parseReportDate(r.ChargedDate); ok {
		return d
	}
	if t, ok := parseReportTime(r.Timestamp, loc); ok {
		return t.In(loc).Format(core.DayLayout)
	}
	return ""
}

// AppKey is the package the row belongs to — the identity Loot groups and
// filters on. The product title is the display name and is not stable.
func (r SaleRow) AppKey() string {
	if r.Package != "" {
		return r.Package
	}
	return r.ProductTitle
}

// AppName is what the feed shows: the product title if the report has one,
// otherwise the package name.
func (r SaleRow) AppName() string {
	if r.ProductTitle != "" {
		return r.ProductTitle
	}
	return r.Package
}

// Refund reports whether this line reverses money rather than taking it.
func (r SaleRow) Refund() bool {
	s := strings.ToLower(r.FinancialStatus)
	return strings.Contains(s, "refund") ||
		strings.Contains(s, "chargeback") ||
		strings.Contains(s, "charged-back") ||
		strings.Contains(s, "charged back") ||
		strings.Contains(s, "reversal")
}

// Kind maps the row onto the event vocabulary: a paid app download is a
// "sale", a one-off in-app product an "iap", a subscription payment a
// "subscription", and anything that gives money back a "refund".
func (r SaleRow) Kind() string {
	if r.Refund() {
		return "refund"
	}
	t := strings.ToLower(strings.TrimSpace(r.ProductType))
	switch {
	case strings.Contains(t, "subs"):
		return "subscription"
	case strings.Contains(t, "inapp"), strings.Contains(t, "in-app"), strings.Contains(t, "in app"):
		return "iap"
	default:
		return "sale"
	}
}

// signedAmount returns the row's contribution to revenue: negative for a
// refund, whichever sign the report itself used.
func (r SaleRow) signedAmount() float64 {
	amount := r.ChargedAmount
	if r.Refund() {
		if amount > 0 {
			amount = -amount
		}
	}
	return amount
}

// quantity is +1 for a charge and -1 for a refund. Play reports one line per
// item, so there is no quantity column to read.
func (r SaleRow) quantity() int {
	if r.Refund() {
		return -1
	}
	return 1
}

// rowEvent builds the silent ledger event for one order line.
func (s *Source) rowEvent(r SaleRow, day string, observed time.Time) core.Event {
	occurred := observed
	if t, ok := parseReportTime(r.Timestamp, s.location()); ok {
		occurred = t
	} else if d, err := time.ParseInLocation(core.DayLayout, day, s.location()); err == nil {
		occurred = d
	}

	payload, _ := json.Marshal(map[string]any{
		"order_number":     r.OrderNumber,
		"package":          r.Package,
		"product_title":    r.ProductTitle,
		"product_type":     r.ProductType,
		"sku":              r.SKU,
		"financial_status": r.FinancialStatus,
		"device_model":     r.DeviceModel,
		"currency":         r.Currency,
		"item_price":       r.ItemPrice,
		"taxes_collected":  r.TaxesCollected,
		"charged_amount":   r.ChargedAmount,
		"country":          r.Country,
		"day":              day,
		"gross":            true,
		"note":             grossNote,
	})

	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       r.Kind(),
		App:        r.AppName(),
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        day,
		Country:    validCountry(r.Country),
		Amount:     r.signedAmount(),
		Currency:   r.Currency,
		Quantity:   r.quantity(),
		DedupeKey: fmt.Sprintf("play:sale:%s:%s:%s",
			r.OrderNumber, r.SKU, strings.ToLower(strings.TrimSpace(r.FinancialStatus))),
		IsLedger: true,
		Silent:   true,
		Payload:  payload,
	}
}

// dayTotals accumulates one (package, day) while the report is walked.
type dayTotals struct {
	pkg      string
	day      string
	appName  string
	rows     int
	units    int // charged lines: paid apps, in-app products and subscriptions
	refunds  int // positive count of refunded and charged-back lines
	iapUnits int
	subUnits int
	// countries counts charged lines per country of the buyer.
	countries map[string]int
	// byCurrency holds net proceeds per currency. Play bills in the buyer's
	// currency, so one day is routinely a mix of them.
	byCurrency map[string]float64
	// bySKU is keyed currency -> sku, so the SKU breakdown reported alongside
	// the dominant currency's Proceeds is denominated in that same currency.
	bySKU map[string]map[string]*core.SKUTotals
}

func newDayTotals(pkg, day string) *dayTotals {
	return &dayTotals{
		pkg:        pkg,
		day:        day,
		countries:  map[string]int{},
		byCurrency: map[string]float64{},
		bySKU:      map[string]map[string]*core.SKUTotals{},
	}
}

// noCurrency stands in for a row whose currency column was blank, so it is
// still accounted for instead of being folded into a real currency.
const noCurrency = "?"

func (d *dayTotals) add(r SaleRow) {
	if d.appName == "" {
		d.appName = r.AppName()
	}
	d.rows++
	if r.Refund() {
		d.refunds++
	} else {
		d.units++
		switch r.Kind() {
		case "iap":
			d.iapUnits++
		case "subscription":
			d.subUnits++
		}
		if c := validCountry(r.Country); c != "" {
			d.countries[c]++
		}
	}

	cur := r.Currency
	if cur == "" {
		cur = noCurrency
	}
	d.byCurrency[cur] += r.signedAmount()

	skus, ok := d.bySKU[cur]
	if !ok {
		skus = map[string]*core.SKUTotals{}
		d.bySKU[cur] = skus
	}
	key := r.SKU
	if key == "" {
		key = r.Package
	}
	st, ok := skus[key]
	if !ok {
		st = &core.SKUTotals{}
		skus[key] = st
	}
	st.Units += r.quantity()
	st.Proceeds = round2(st.Proceeds + r.signedAmount())
}

// dominant returns the currency the day is reported in: the one with the
// largest absolute total. An Event carries exactly one Amount and one
// Currency, so a mixed day names its biggest currency and flags itself rather
// than adding yen to euros. Nothing is lost — the vault sums the silent rows,
// each of which kept its own currency and was converted at ingest.
func (d *dayTotals) dominant() string {
	best, bestAbs := "", -1.0
	for cur, proceeds := range d.byCurrency {
		abs := math.Abs(proceeds)
		// Ties break on the currency code, so the choice is the same on every
		// re-read of the same report.
		if abs > bestAbs || (abs == bestAbs && cur < best) {
			best, bestAbs = cur, abs
		}
	}
	return best
}

func (d *dayTotals) topCountry() string {
	best, bestN := "", 0
	for c, n := range d.countries {
		if n > bestN || (n == bestN && c < best) {
			best, bestN = c, n
		}
	}
	return best
}

// SalesDayPayload is the payload of a `sales_day` event: core.SalesDaySummary,
// which every ledger source shares, plus the Play specifics.
//
// Amounts are gross — what the customer was charged. Play's 15–30% service fee
// and any withheld tax are not deducted, because the estimated sales report
// does not know about them; the monthly earnings report does, and is the net
// truth. Gross and Note say so inside the payload so nobody reads a chest drop
// as take-home pay.
type SalesDayPayload struct {
	core.SalesDaySummary
	Package       string             `json:"package"`
	Date          string             `json:"date"`
	Rows          int                `json:"rows"`
	IAPUnits      int                `json:"iap_units"`
	SubUnits      int                `json:"subscription_units"`
	ProceedsMixed bool               `json:"proceeds_mixed,omitempty"`
	ByCurrency    map[string]float64 `json:"by_currency,omitempty"`
	ByCountry     map[string]int     `json:"by_country,omitempty"`
	Gross         bool               `json:"gross"`
	Note          string             `json:"note"`
}

// summaryEvent turns a day's totals into the non-silent chest event.
func (s *Source) summaryEvent(d *dayTotals, observed time.Time) core.Event {
	currency := d.dominant()
	proceeds := round2(d.byCurrency[currency])

	bySKU := map[string]core.SKUTotals{}
	for sku, t := range d.bySKU[currency] {
		bySKU[sku] = *t
	}

	byCurrency := make(map[string]float64, len(d.byCurrency))
	for cur, total := range d.byCurrency {
		byCurrency[cur] = round2(total)
	}
	if currency == noCurrency {
		currency = ""
	}

	occurred := observed
	if t, err := time.ParseInLocation(core.DayLayout, d.day, s.location()); err == nil {
		occurred = t
	}

	app := d.appName
	if app == "" {
		app = d.pkg
	}

	payload, _ := json.Marshal(SalesDayPayload{
		SalesDaySummary: core.SalesDaySummary{
			Units:      d.units,
			Refunds:    d.refunds,
			Proceeds:   proceeds,
			Currency:   currency,
			Countries:  len(d.countries),
			TopCountry: d.topCountry(),
			BySKU:      bySKU,
		},
		Package:       d.pkg,
		Date:          d.day,
		Rows:          d.rows,
		IAPUnits:      d.iapUnits,
		SubUnits:      d.subUnits,
		ProceedsMixed: len(d.byCurrency) > 1,
		ByCurrency:    byCurrency,
		ByCountry:     d.countries,
		Gross:         true,
		Note:          grossNote,
	})

	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       "sales_day",
		App:        app,
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        d.day,
		Amount:     proceeds,
		Currency:   currency,
		Quantity:   d.units,
		DedupeKey:  fmt.Sprintf("play:sales_day:%s:%s", d.pkg, d.day),
		IsLedger:   true,
		Chest:      true,
		Payload:    payload,
	}
}

// pollSales reads every monthly sales file in the window, emits a silent event
// per order line and one chest summary per settled (package, day).
func (s *Source) pollSales(ctx context.Context, st *state, months []string, now time.Time) ([]core.Event, error) {
	objects, err := s.List(ctx, salesPrefix, 0)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(months))
	for _, m := range months {
		wanted[m] = true
	}

	var (
		events []core.Event
		totals = map[string]*dayTotals{} // package + "\x00" + day
		order  []string
		seen   = map[string]bool{}
	)

	for _, obj := range objects {
		m := salesObjectRe.FindStringSubmatch(obj.Name)
		if m == nil || !wanted[m[1]] {
			continue
		}
		seen[obj.Name] = true
		if prev, ok := st.SalesFiles[obj.Name]; ok && prev != "" && prev == obj.MD5Hash {
			s.Log.Debug("googleplay: sales report unchanged", "object", obj.Name)
			continue
		}

		raw, err := s.Download(ctx, obj.Name)
		if err != nil {
			return events, err
		}
		rows, err := ParseSalesZip(raw)
		if err != nil {
			return events, err
		}
		s.Log.Debug("googleplay: sales report read", "object", obj.Name, "rows", len(rows))

		for _, r := range rows {
			pkg := r.AppKey()
			if pkg == "" || !s.wantPackage(pkg) {
				continue
			}
			day := r.Day(s.location())
			if day == "" {
				s.Log.Warn("googleplay: sales row has no usable date",
					"order", r.OrderNumber, "object", obj.Name)
				continue
			}
			events = append(events, s.rowEvent(r, day, now))

			key := pkg + "\x00" + day
			t, ok := totals[key]
			if !ok {
				t = newDayTotals(pkg, day)
				totals[key] = t
				order = append(order, key)
			}
			t.add(r)
		}
		st.SalesFiles[obj.Name] = obj.MD5Hash
	}

	// Forget files that have fallen out of the window, so the state blob does
	// not grow without bound.
	for name := range st.SalesFiles {
		if !seen[name] {
			delete(st.SalesFiles, name)
		}
	}

	// A day is only summarized once the report has stopped moving under it:
	// the monthly file is rewritten daily and late rows arrive for a day or
	// two. Rows for an unsettled day are still stored — the vault sums rows,
	// not summaries — they just do not get a chest yet.
	cutoff := now.In(s.location()).AddDate(0, 0, -1).Format(core.DayLayout)

	sort.Strings(order)
	advanced := map[string]string{}
	for _, key := range order {
		t := totals[key]
		if t.day >= cutoff {
			continue
		}
		if last := st.SummarizedDays[t.pkg]; last != "" && t.day <= last {
			continue
		}
		events = append(events, s.summaryEvent(t, now))
		if t.day > advanced[t.pkg] {
			advanced[t.pkg] = t.day
		}
	}
	for pkg, day := range advanced {
		if day > st.SummarizedDays[pkg] {
			st.SummarizedDays[pkg] = day
		}
	}
	return events, nil
}

// round2 trims the floating point dust that summing hundreds of prices
// leaves behind, so a day's proceeds read as money rather than as 40.99999998.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// validCountry keeps only ISO 3166-1 alpha-2 codes. Play writes "ZZ" and blank
// cells for orders it cannot place, and a settlement drop reading "New
// settlement: ZZ" would be a lie.
func validCountry(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if len(c) != 2 || c == "ZZ" {
		return ""
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	return c
}
