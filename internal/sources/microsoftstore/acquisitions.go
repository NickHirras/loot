package microsoftstore

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Event kinds this source emits. Row events are silent, so these names are
// what the vault and the rarity rules match on, not what a drop says.
const (
	KindSale                 = "sale"                  // a paid app acquisition
	KindIAP                  = "iap"                   // a paid add-on (in-app product) acquisition
	KindDownload             = "download"              // free, trial or promotional: units, no money
	KindSalesDay             = "sales_day"             // the one summary per (app, day)
	KindSubscriptionSnapshot = "subscription_snapshot" // active subscriptions, per (app, day)
)

// paidTypes are the acquisitionType values that mean money changed hands even
// when the response carries no price (the price fields are present in
// Microsoft's samples but absent from its documented field table, so an
// account that reports no prices must still book a sale as a sale).
//
// Everything else — Free, Trial, Promotional code, Private Audience, Xbox Game
// Pass — is a download unless it arrives with a positive price, in which case
// the money wins and it is booked as a sale. Names are matched lowercased, so
// the API's inconsistent casing ("Paid" in app acquisitions, "paid" in add-on
// acquisitions) makes no difference.
var paidTypes = map[string]bool{
	"paid":             true,
	"iap":              true,
	"subscription iap": true,
	"pre order":        true,
	"disk":             true,
	"prepaid code":     true,
}

// apiAcquisition is one row of appacquisitions or inappacquisitions.
//
// The two endpoints share every field that matters. inAppProductId is what the
// add-on documentation names, but Microsoft's own sample response for the same
// endpoint calls it addonProductId, so both are decoded. localCurrencyCode is
// likewise undocumented and absent from the samples: when it is missing the
// USD figures are used instead, which is the only self-consistent reading of a
// local amount whose currency is unknown.
type apiAcquisition struct {
	Date                     string  `json:"date"`
	ApplicationID            string  `json:"applicationId"`
	ApplicationName          string  `json:"applicationName"`
	InAppProductID           string  `json:"inAppProductId"`
	AddOnProductID           string  `json:"addonProductId"`
	InAppProductName         string  `json:"inAppProductName"`
	AcquisitionType          string  `json:"acquisitionType"`
	Market                   string  `json:"market"`
	DeviceType               string  `json:"deviceType"`
	StoreClient              string  `json:"storeClient"`
	OSVersion                string  `json:"osVersion"`
	OrderName                string  `json:"orderName"`
	AcquisitionQuantity      float64 `json:"acquisitionQuantity"`
	PurchasePriceUSDAmount   float64 `json:"purchasePriceUSDAmount"`
	PurchasePriceLocalAmount float64 `json:"purchasePriceLocalAmount"`
	PurchaseTaxUSDAmount     float64 `json:"purchaseTaxUSDAmount"`
	PurchaseTaxLocalAmount   float64 `json:"purchaseTaxLocalAmount"`
	LocalCurrencyCode        string  `json:"localCurrencyCode"`
}

// Acquisition is one normalized acquisition group: an API row reduced to the
// fields Loot keys, counts and reports on.
//
// Amount is the group's purchase price *in total*, not per unit. The
// acquisitions API returns aggregate rows — acquisitionQuantity is itself a
// count of acquisitions in the group — so its price fields are read as the
// group's total. Multiplying by the quantity would inflate every multi-unit
// row; see docs/sources/microsoftstore.md.
type Acquisition struct {
	Date            string  `json:"date"`
	StoreID         string  `json:"store_id"`
	App             string  `json:"app,omitempty"`
	AddOnID         string  `json:"add_on_id,omitempty"`
	AddOnName       string  `json:"add_on_name,omitempty"`
	AcquisitionType string  `json:"acquisition_type,omitempty"`
	Market          string  `json:"market,omitempty"`
	Quantity        int     `json:"quantity"`
	Amount          float64 `json:"amount"`
	Currency        string  `json:"currency,omitempty"`
	// Rows is how many API rows were folded into this group. Grouping happens
	// before anything is emitted so that re-reading a day produces the exact
	// same events, which is what makes the sliding re-sweep free.
	Rows int `json:"rows"`
	// Gross records that Amount is the customer's price, before the Store's
	// revenue share and before tax.
	Gross bool `json:"gross"`
	// AddOn marks a row that came from the add-on endpoint.
	AddOn bool `json:"-"`
}

// normalize converts one API row into an Acquisition, or reports false for a
// row Loot cannot key (no date, or no quantity and no money).
func normalize(r apiAcquisition, addOn bool) (Acquisition, bool) {
	date := normalizeDate(r.Date)
	if date == "" {
		return Acquisition{}, false
	}

	amount, currency := rowAmount(r)
	quantity := int(math.Round(r.AcquisitionQuantity))
	if quantity == 0 && amount == 0 {
		return Acquisition{}, false
	}

	addOnID := strings.TrimSpace(r.InAppProductID)
	if addOnID == "" {
		addOnID = strings.TrimSpace(r.AddOnProductID)
	}

	return Acquisition{
		Date:            date,
		StoreID:         strings.TrimSpace(r.ApplicationID),
		App:             strings.TrimSpace(r.ApplicationName),
		AddOnID:         addOnID,
		AddOnName:       strings.TrimSpace(r.InAppProductName),
		AcquisitionType: normalizeType(r.AcquisitionType),
		Market:          strings.ToUpper(strings.TrimSpace(r.Market)),
		Quantity:        quantity,
		Amount:          amount,
		Currency:        currency,
		Rows:            1,
		Gross:           amount != 0,
		AddOn:           addOn,
	}, true
}

// rowAmount picks the money to book. A local amount is only usable when the
// response says what currency it is in; without localCurrencyCode the USD
// figures are the only ones with a known unit.
//
// Tax is deliberately not subtracted: purchasePrice*Amount is the price the
// customer paid, and purchaseTax*Amount is the tax already inside it. Netting
// tax out here would produce neither gross revenue nor developer proceeds.
func rowAmount(r apiAcquisition) (float64, string) {
	if code := strings.ToUpper(strings.TrimSpace(r.LocalCurrencyCode)); code != "" {
		return round2(r.PurchasePriceLocalAmount), code
	}
	if r.PurchasePriceUSDAmount != 0 {
		return round2(r.PurchasePriceUSDAmount), "USD"
	}
	return 0, ""
}

// normalizeDate accepts both "2026-08-14" and "2026-08-14T00:00:00" and
// returns a plain day, or "" if it is neither.
func normalizeDate(v string) string {
	day, _, _ := strings.Cut(strings.TrimSpace(v), "T")
	if _, err := time.Parse(core.DayLayout, day); err != nil {
		return ""
	}
	return day
}

// normalizeType lowercases an acquisitionType and hyphenates it, so that
// "Promotional code" and "promotional code" are one bucket and one dedupe key.
func normalizeType(v string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(v))), "-")
}

// kindFor names a row: money first, then whether it was an add-on.
func kindFor(a Acquisition) string {
	name := strings.ReplaceAll(a.AcquisitionType, "-", " ")
	if a.Amount <= 0 && !paidTypes[name] {
		return KindDownload
	}
	if a.AddOn || strings.Contains(name, "iap") {
		return KindIAP
	}
	return KindSale
}

// dedupeKey is the stable identity of an acquisition group. It is derived
// entirely from the report — never from time.Now or a row index — so the same
// day read twice produces the same key and the pipeline drops the second copy.
func (a Acquisition) dedupeKey() string {
	parts := []string{"msstore:acq", a.Date, a.StoreID, a.Market, a.AcquisitionType, a.Currency}
	if a.AddOn {
		parts[0] = "msstore:iap"
		parts = append(parts, a.AddOnID)
	}
	return strings.Join(parts, ":")
}

// sku is how a row shows up in the summary's by_sku breakdown: the add-on for
// an add-on row, the app itself for an app row.
func (a Acquisition) sku() string {
	switch {
	case a.AddOnName != "":
		return a.AddOnName
	case a.AddOnID != "":
		return a.AddOnID
	case a.App != "":
		return a.App
	default:
		return a.StoreID
	}
}

// normalizeDownloads zeroes the money on free acquisitions *before* they are
// grouped, which is the only place it can safely be done.
//
// A download is units and a country, never money: a stray price on a free
// acquisition would otherwise book revenue nobody was paid. But the currency
// is part of the dedupe key, so zeroing it after grouping meant two groups
// that differed only in whether the API had reported a localCurrencyCode
// collapsed onto one key at emit time — and the pipeline, doing exactly what
// it is told, dropped the second as a duplicate along with its units.
// Normalizing first makes those two rows one group, summed, with one key.
func normalizeDownloads(rows []Acquisition) []Acquisition {
	out := make([]Acquisition, 0, len(rows))
	for _, row := range rows {
		if kindFor(row) == KindDownload {
			row.Amount, row.Currency, row.Gross = 0, "", false
		}
		out = append(out, row)
	}
	return out
}

// group folds rows onto their dedupe key, summing quantities and money, and
// returns them sorted by that key. Grouping *before* emitting is what makes a
// re-sweep of an already-ingested day a no-op instead of a pile of near
// duplicates: two API rows that differ only in a dimension Loot does not key
// (device type, OS version, age group) become one event, deterministically.
func group(rows []Acquisition) []Acquisition {
	byKey := map[string]*Acquisition{}
	keys := make([]string, 0, len(rows))

	for _, row := range rows {
		key := row.dedupeKey()
		agg, ok := byKey[key]
		if !ok {
			copied := row
			byKey[key] = &copied
			keys = append(keys, key)
			continue
		}
		agg.Quantity += row.Quantity
		agg.Amount = round2(agg.Amount + row.Amount)
		agg.Rows += row.Rows
		agg.Gross = agg.Gross || row.Gross
		if agg.App == "" {
			agg.App = row.App
		}
		if agg.AddOnName == "" {
			agg.AddOnName = row.AddOnName
		}
	}

	sort.Strings(keys)
	out := make([]Acquisition, 0, len(keys))
	for _, key := range keys {
		out = append(out, *byKey[key])
	}
	return out
}

// SalesDayPayload is the payload of a `sales_day` event: core.SalesDaySummary,
// which every ledger source shares, plus the Microsoft Store specifics.
//
// A day can span several currencies when accounts report local prices, so the
// summary reports the *dominant* one — the currency carrying the most money —
// and ByCurrency carries the full truth. No revenue is lost by that: the vault
// sums the silent row events, each of which keeps its own currency.
type SalesDayPayload struct {
	core.SalesDaySummary
	StoreID   string `json:"store_id,omitempty"`
	Date      string `json:"date"`
	Rows      int    `json:"rows"`
	Downloads int    `json:"downloads"`
	IAPUnits  int    `json:"iap_units"`
	// Gross marks Proceeds as a customer price before the Store's revenue
	// share, not as developer proceeds.
	Gross         bool               `json:"gross"`
	ProceedsMixed bool               `json:"proceeds_mixed,omitempty"`
	ByCurrency    map[string]float64 `json:"by_currency,omitempty"`
	ByType        map[string]int     `json:"by_acquisition_type,omitempty"`
	ByCountry     map[string]int     `json:"by_country,omitempty"`
}

// dayAggregate accumulates one app's day while the grouped rows stream past.
type dayAggregate struct {
	storeID   string
	app       string
	rows      int
	units     int // paid units: app sales and add-on purchases
	downloads int // free, trial and promotional acquisitions
	iapUnits  int

	byCurrency    map[string]float64
	byType        map[string]int
	byCountryPaid map[string]int
	byCountryAll  map[string]int
	countries     map[string]bool
	skuUnits      map[string]int
	skuProceeds   map[string]map[string]float64
	skuOrder      []string
}

func newDayAggregate(storeID string) *dayAggregate {
	return &dayAggregate{
		storeID:       storeID,
		byCurrency:    map[string]float64{},
		byType:        map[string]int{},
		byCountryPaid: map[string]int{},
		byCountryAll:  map[string]int{},
		countries:     map[string]bool{},
		skuUnits:      map[string]int{},
		skuProceeds:   map[string]map[string]float64{},
	}
}

// BuildEvents turns one app's acquisitions into events: one silent ledger row
// event per group, then one `sales_day` summary per day whose drop the chest
// holds.
//
// Only days in [from, to] are emitted, so an API that answers with a wider
// range than it was asked for cannot smuggle an unsettled day into the ledger.
// storeID and appName name the app when a row does not (grouped responses omit
// applicationName unless it is in the groupby).
func BuildEvents(storeID, appName string, rows []Acquisition, from, to string, observed time.Time) ([]core.Event, error) {
	grouped := group(normalizeDownloads(rows))

	var (
		events     = make([]core.Event, 0, len(grouped)+1)
		aggregates = map[string]*dayAggregate{}
		days       []string
	)

	for _, row := range grouped {
		if row.Date < from || row.Date > to {
			continue
		}
		if row.StoreID == "" {
			row.StoreID = storeID
		}
		if row.App == "" {
			row.App = appName
		}

		day, err := time.ParseInLocation(core.DayLayout, row.Date, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("microsoftstore: bad acquisition date %q: %w", row.Date, err)
		}
		kind := kindFor(row)

		agg, ok := aggregates[row.Date]
		if !ok {
			agg = newDayAggregate(row.StoreID)
			aggregates[row.Date] = agg
			days = append(days, row.Date)
		}
		agg.add(row, kind)

		payload, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("microsoftstore: encode row payload: %w", err)
		}
		events = append(events, core.Event{
			ID:         core.NewIDAt(day),
			Source:     Name,
			Kind:       kind,
			App:        displayName(row.App, row.StoreID),
			OccurredAt: day,
			ObservedAt: observed,
			Day:        row.Date,
			Country:    row.Market,
			Amount:     row.Amount,
			Currency:   row.Currency,
			Quantity:   row.Quantity,
			DedupeKey:  row.dedupeKey(),
			IsLedger:   true,
			Silent:     true,
			Payload:    payload,
		})
	}

	sort.Strings(days)
	for _, date := range days {
		summary, err := aggregates[date].summaryEvent(date, appName, observed)
		if err != nil {
			return nil, err
		}
		events = append(events, summary)
	}
	return events, nil
}

// add folds one grouped row into the day's totals.
func (a *dayAggregate) add(row Acquisition, kind string) {
	if a.app == "" {
		a.app = row.App
	}
	a.rows += row.Rows
	if row.AcquisitionType != "" {
		a.byType[row.AcquisitionType] += row.Quantity
	}
	if row.Market != "" {
		a.countries[row.Market] = true
		a.byCountryAll[row.Market] += row.Quantity
	}

	if kind == KindDownload {
		a.downloads += row.Quantity
	} else {
		a.units += row.Quantity
		if kind == KindIAP {
			a.iapUnits += row.Quantity
		}
		if row.Market != "" {
			a.byCountryPaid[row.Market] += row.Quantity
		}
	}
	if row.Amount != 0 && row.Currency != "" {
		a.byCurrency[row.Currency] = round2(a.byCurrency[row.Currency] + row.Amount)
	}

	sku := row.sku()
	if _, seen := a.skuUnits[sku]; !seen {
		a.skuOrder = append(a.skuOrder, sku)
	}
	a.skuUnits[sku] += row.Quantity
	if row.Amount != 0 && row.Currency != "" {
		if a.skuProceeds[sku] == nil {
			a.skuProceeds[sku] = map[string]float64{}
		}
		a.skuProceeds[sku][row.Currency] = round2(a.skuProceeds[sku][row.Currency] + row.Amount)
	}
}

// summaryEvent builds the one non-silent event of the day: the chest drop.
func (a *dayAggregate) summaryEvent(date, appName string, observed time.Time) (core.Event, error) {
	day, err := time.ParseInLocation(core.DayLayout, date, time.UTC)
	if err != nil {
		return core.Event{}, fmt.Errorf("microsoftstore: bad summary date %q: %w", date, err)
	}
	currency, amount := a.dominantCurrency()

	payload := SalesDayPayload{
		SalesDaySummary: core.SalesDaySummary{
			Units: a.units,
			// The acquisitions API has no concept of a refund or a chargeback,
			// so this is always zero rather than unknown. The subscriptions
			// endpoint counts refund churn, but not the money behind it.
			Refunds:    0,
			Proceeds:   amount,
			Currency:   currency,
			Countries:  len(a.countries),
			TopCountry: a.topCountry(),
			BySKU:      a.bySKU(currency),
		},
		StoreID:       a.storeID,
		Date:          date,
		Rows:          a.rows,
		Downloads:     a.downloads,
		IAPUnits:      a.iapUnits,
		Gross:         true,
		ProceedsMixed: len(a.byCurrency) > 1,
		ByCurrency:    a.byCurrency,
		ByType:        a.byType,
		ByCountry:     a.byCountryAll,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return core.Event{}, fmt.Errorf("microsoftstore: encode sales_day payload: %w", err)
	}

	app := a.app
	if app == "" {
		app = appName
	}
	return core.Event{
		ID:         core.NewIDAt(day),
		Source:     Name,
		Kind:       KindSalesDay,
		App:        displayName(app, a.storeID),
		OccurredAt: day,
		ObservedAt: observed,
		Day:        date,
		// A summary spans countries, so it carries none: the settlement drop
		// for a brand new country comes from the row that revealed it.
		Country:   "",
		Amount:    amount,
		Currency:  currency,
		Quantity:  a.units,
		DedupeKey: fmt.Sprintf("%s:%s:%s:%s", Name, KindSalesDay, a.storeID, date),
		IsLedger:  true,
		Silent:    false,
		Chest:     true,
		Payload:   encoded,
	}, nil
}

// dominantCurrency picks the currency carrying the most money, so the headline
// drop reads in the currency that actually earned the day. Ties break
// alphabetically to keep a re-read of the same day identical.
func (a *dayAggregate) dominantCurrency() (string, float64) {
	currencies := make([]string, 0, len(a.byCurrency))
	for c := range a.byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)

	var (
		best    string
		bestAbs float64
	)
	for _, c := range currencies {
		if abs := math.Abs(a.byCurrency[c]); abs > bestAbs {
			best, bestAbs = c, abs
		}
	}
	if best == "" {
		return "", 0
	}
	return best, round2(a.byCurrency[best])
}

// topCountry is the market with the most paid units, falling back to units of
// any kind on a day that only produced free downloads.
func (a *dayAggregate) topCountry() string {
	if c := topKey(a.byCountryPaid); c != "" {
		return c
	}
	return topKey(a.byCountryAll)
}

// bySKU reports each product's units, and its money in the summary's own
// currency so the breakdown adds up to the headline.
func (a *dayAggregate) bySKU(currency string) map[string]core.SKUTotals {
	if len(a.skuOrder) == 0 {
		return nil
	}
	out := make(map[string]core.SKUTotals, len(a.skuOrder))
	for _, sku := range a.skuOrder {
		out[sku] = core.SKUTotals{
			Units:    a.skuUnits[sku],
			Proceeds: round2(a.skuProceeds[sku][currency]),
		}
	}
	return out
}

// topKey returns the key with the highest value, ties broken alphabetically.
func topKey(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	best, bestN := "", 0
	for _, k := range keys {
		if m[k] > bestN {
			best, bestN = k, m[k]
		}
	}
	return best
}

// displayName prefers the human name Microsoft ships in the response; a
// response without one falls back to the Store ID, which is at least
// searchable.
func displayName(name, storeID string) string {
	if n := strings.TrimSpace(name); n != "" && !strings.EqualFold(n, "unknown") {
		return n
	}
	return storeID
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
