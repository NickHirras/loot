package appstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Event kinds this source emits. Row events are silent, so these names are
// what the vault and the rarity rules match on, not what a drop says.
const (
	KindSale                 = "sale"                  // a paid app download
	KindIAP                  = "iap"                   // a non-subscription in-app purchase
	KindSubscription         = "subscription"          // an auto-renewable subscription payment
	KindRefund               = "refund"                // any row with negative units
	KindDownload             = "download"              // a free app download, no money
	KindSalesDay             = "sales_day"             // the one summary per (app, day)
	KindSubscriptionSnapshot = "subscription_snapshot" // active subscribers, per (app, day)
)

// rowClass is what a Product Type Identifier means. Apple's identifiers are a
// small closed set of strings; anything unrecognised is treated as a sale so a
// new product type shows up as money rather than vanishing.
type rowClass int

const (
	classApp rowClass = iota
	classUpdate
	classIAP
	classSubscription
)

// productClasses maps the documented Product Type Identifiers of the summary
// sales report. Verified against
// https://developer.apple.com/help/app-store-connect/reference/summary-sales-report
var productClasses = map[string]rowClass{
	// Paid (and free) app downloads, per platform.
	"1": classApp, "1F": classApp, "1T": classApp, "1E": classApp,
	"1EP": classApp, "1EU": classApp, "F1": classApp,
	// App bundles are still an app purchase.
	"1-B": classApp, "F1-B": classApp,
	// Updates: units only, no money.
	"3": classUpdate, "3F": classUpdate, "3T": classUpdate, "3E": classUpdate,
	"7": classUpdate, "7F": classUpdate, "7T": classUpdate, "7E": classUpdate, "F7": classUpdate,
	// In-app purchases.
	"IA1": classIAP, "IAC": classIAP, "FI1": classIAP,
	// Auto-renewable subscriptions.
	"IA9": classSubscription, "IAY": classSubscription,
}

// classifyProductType resolves a Product Type Identifier. The prefix fallbacks
// mean a platform suffix Apple adds tomorrow ("1X", "IA1-M") still lands in the
// right bucket.
func classifyProductType(pt string) rowClass {
	key := strings.ToUpper(strings.TrimSpace(pt))
	if c, ok := productClasses[key]; ok {
		return c
	}
	switch {
	case strings.HasPrefix(key, "3"), strings.HasPrefix(key, "7"), strings.HasPrefix(key, "F7"):
		return classUpdate
	case strings.HasPrefix(key, "IA9"), strings.HasPrefix(key, "IAY"):
		return classSubscription
	case strings.HasPrefix(key, "IA"), strings.HasPrefix(key, "FI"):
		return classIAP
	default:
		return classApp
	}
}

// SalesRow is one row of the summary sales report. Field names follow Apple's
// column names; Proceeds is *per unit*, which is the single most expensive
// thing to get wrong in this file.
type SalesRow struct {
	Provider           string  `json:"provider,omitempty"`
	ProviderCountry    string  `json:"provider_country,omitempty"`
	SKU                string  `json:"sku,omitempty"`
	Developer          string  `json:"developer,omitempty"`
	Title              string  `json:"title,omitempty"`
	Version            string  `json:"version,omitempty"`
	ProductType        string  `json:"product_type,omitempty"`
	Units              int     `json:"units"`
	ProceedsPerUnit    float64 `json:"developer_proceeds_per_unit"`
	BeginDate          string  `json:"begin_date,omitempty"`
	EndDate            string  `json:"end_date,omitempty"`
	CustomerCurrency   string  `json:"customer_currency,omitempty"`
	Country            string  `json:"country,omitempty"`
	ProceedsCurrency   string  `json:"proceeds_currency,omitempty"`
	AppleID            string  `json:"apple_id,omitempty"`
	CustomerPrice      float64 `json:"customer_price"`
	PromoCode          string  `json:"promo_code,omitempty"`
	ParentIdentifier   string  `json:"parent_identifier,omitempty"`
	Subscription       string  `json:"subscription,omitempty"`
	Period             string  `json:"period,omitempty"`
	Category           string  `json:"category,omitempty"`
	CMB                string  `json:"cmb,omitempty"`
	Device             string  `json:"device,omitempty"`
	SupportedPlatforms string  `json:"supported_platforms,omitempty"`
	ProceedsReason     string  `json:"proceeds_reason,omitempty"`
	PreservedPricing   string  `json:"preserved_pricing,omitempty"`
	Client             string  `json:"client,omitempty"`
	OrderType          string  `json:"order_type,omitempty"`
}

// ParseSalesReport decodes a decompressed summary sales report. Exported so
// the whole parsing path can be tested from a fixture without HTTP.
func ParseSalesReport(report []byte) ([]SalesRow, error) {
	t, err := parseTSV(report)
	if err != nil {
		return nil, err
	}
	if !t.has("Product Type Identifier") || !t.has("Units") {
		return nil, fmt.Errorf("appstore: sales report is missing Product Type Identifier / Units (got %q)",
			strings.Join(t.header, "|"))
	}

	rows := make([]SalesRow, 0, len(t.rows))
	for _, r := range t.rows {
		row := SalesRow{
			Provider:           t.get(r, "Provider"),
			ProviderCountry:    t.get(r, "Provider Country"),
			SKU:                t.get(r, "SKU"),
			Developer:          t.get(r, "Developer"),
			Title:              t.get(r, "Title"),
			Version:            t.get(r, "Version"),
			ProductType:        strings.ToUpper(t.get(r, "Product Type Identifier")),
			Units:              atoi(t.get(r, "Units")),
			ProceedsPerUnit:    atof(t.get(r, "Developer Proceeds")),
			BeginDate:          t.get(r, "Begin Date"),
			EndDate:            t.get(r, "End Date"),
			CustomerCurrency:   strings.ToUpper(t.get(r, "Customer Currency")),
			Country:            strings.ToUpper(t.get(r, "Country Code")),
			ProceedsCurrency:   strings.ToUpper(t.get(r, "Currency of Proceeds")),
			AppleID:            t.get(r, "Apple Identifier"),
			CustomerPrice:      atof(t.get(r, "Customer Price")),
			PromoCode:          t.get(r, "Promo Code"),
			ParentIdentifier:   t.get(r, "Parent Identifier"),
			Subscription:       t.get(r, "Subscription"),
			Period:             t.get(r, "Period"),
			Category:           t.get(r, "Category"),
			CMB:                t.get(r, "CMB"),
			Device:             t.get(r, "Device"),
			SupportedPlatforms: t.get(r, "Supported Platforms"),
			ProceedsReason:     t.get(r, "Proceeds Reason"),
			PreservedPricing:   t.get(r, "Preserved Pricing"),
			Client:             t.get(r, "Client"),
			OrderType:          t.get(r, "Order Type"),
		}
		// A row with no product and no units is padding, not data.
		if row.ProductType == "" && row.Units == 0 {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// SalesDayPayload is the payload of a `sales_day` event: core.SalesDaySummary,
// which every ledger source shares, plus the App Store specifics.
//
// Money on a multi-country day arrives in several proceeds currencies (Apple
// pays euros for the euro zone, yen for Japan and so on), but an Event carries
// exactly one Amount and one Currency. So the summary reports the *dominant*
// proceeds currency — the one with the largest absolute total — and
// ByCurrency carries the full truth. ProceedsMixed marks the days where that
// distinction matters. No revenue is lost by this: the vault sums the silent
// row events, each of which keeps its own currency, and the pipeline converts
// every one of them into the display currency.
type SalesDayPayload struct {
	core.SalesDaySummary
	AppleID       string             `json:"apple_id,omitempty"`
	Date          string             `json:"date"`
	Rows          int                `json:"rows"`
	Downloads     int                `json:"downloads"`
	Updates       int                `json:"updates"`
	IAPUnits      int                `json:"iap_units"`
	SubUnits      int                `json:"subscription_units"`
	ProceedsMixed bool               `json:"proceeds_mixed,omitempty"`
	ByCurrency    map[string]float64 `json:"by_currency,omitempty"`
	ByProductType map[string]int     `json:"by_product_type,omitempty"`
	ByCountry     map[string]int     `json:"by_country,omitempty"`
}

// dayAggregate accumulates one app's day while the rows stream past.
type dayAggregate struct {
	appleID   string
	title     string
	rows      int
	units     int // paid units: apps, IAPs and subscriptions, refunds excluded
	refunds   int // positive count of refunded units
	downloads int // free app downloads
	updates   int // update rows, which carry units but never money
	iapUnits  int
	subUnits  int

	byCurrency    map[string]float64
	byProductType map[string]int
	byCountryPaid map[string]int
	byCountryAll  map[string]int
	countries     map[string]bool
	skuUnits      map[string]int
	skuProceeds   map[string]map[string]float64 // sku -> currency -> proceeds
	skuOrder      []string
}

func newDayAggregate(appleID string) *dayAggregate {
	return &dayAggregate{
		appleID:       appleID,
		byCurrency:    map[string]float64{},
		byProductType: map[string]int{},
		byCountryPaid: map[string]int{},
		byCountryAll:  map[string]int{},
		countries:     map[string]bool{},
		skuUnits:      map[string]int{},
		skuProceeds:   map[string]map[string]float64{},
	}
}

// BuildSalesEvents turns one day's summary sales report into events: one
// silent ledger row event per meaningful row, then one `sales_day` summary per
// app whose drop the chest holds.
//
// Update rows are counted but never emitted — an update is not a sale and a
// hundred silent zero-money rows would only dilute the vault's event counts.
// Free downloads *are* emitted, with a zero amount, so unit counts and the
// countries a free app reached still exist.
//
// apps, when non-empty, restricts the report to those Apple IDs.
func BuildSalesEvents(report []byte, date string, apps []string, observed time.Time) ([]core.Event, error) {
	rows, err := ParseSalesReport(report)
	if err != nil {
		return nil, err
	}

	day, err := time.ParseInLocation(core.DayLayout, date, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("appstore: bad report date %q: %w", date, err)
	}

	allow := allowSet(apps)
	// seq disambiguates rows that are identical in every field of the dedupe
	// key. Apple does split a (day, app, country, product, currency) group
	// across several rows — different device or client, say — and two rows
	// must not collapse into one event.
	seq := map[string]int{}
	aggregates := map[string]*dayAggregate{}
	var order []string

	events := make([]core.Event, 0, len(rows)+1)

	for _, row := range rows {
		if len(allow) > 0 && !allow[row.AppleID] {
			continue
		}
		class := classifyProductType(row.ProductType)

		agg, ok := aggregates[row.AppleID]
		if !ok {
			agg = newDayAggregate(row.AppleID)
			aggregates[row.AppleID] = agg
			order = append(order, row.AppleID)
		}
		if agg.title == "" {
			agg.title = row.Title
		}
		agg.byProductType[row.ProductType] += row.Units

		if class == classUpdate {
			agg.updates += row.Units
			continue
		}

		amount := round2(row.ProceedsPerUnit * float64(row.Units))
		if row.Units < 0 {
			// A refund must be negative money whichever way Apple wrote it:
			// most rows keep a positive per-unit proceeds figure and negate
			// the units, but a negated proceeds figure has been seen too, and
			// two negatives would otherwise book a refund as income.
			amount = -absFloat(amount)
		}
		kind := rowKind(class, row.Units, amount)

		agg.rows++
		if row.Country != "" {
			agg.countries[row.Country] = true
			agg.byCountryAll[row.Country] += absInt(row.Units)
		}
		switch kind {
		case KindRefund:
			agg.refunds += absInt(row.Units)
		case KindDownload:
			agg.downloads += row.Units
		default:
			agg.units += row.Units
			if row.Country != "" {
				agg.byCountryPaid[row.Country] += row.Units
			}
			if kind == KindIAP {
				agg.iapUnits += row.Units
			}
			if kind == KindSubscription {
				agg.subUnits += row.Units
			}
		}
		if amount != 0 && row.ProceedsCurrency != "" {
			agg.byCurrency[row.ProceedsCurrency] = round2(agg.byCurrency[row.ProceedsCurrency] + amount)
		}
		sku := row.SKU
		if sku == "" {
			sku = row.ProductType
		}
		if _, seen := agg.skuUnits[sku]; !seen {
			agg.skuOrder = append(agg.skuOrder, sku)
		}
		if kind != KindRefund {
			agg.skuUnits[sku] += row.Units
		}
		if amount != 0 && row.ProceedsCurrency != "" {
			if agg.skuProceeds[sku] == nil {
				agg.skuProceeds[sku] = map[string]float64{}
			}
			agg.skuProceeds[sku][row.ProceedsCurrency] = round2(agg.skuProceeds[sku][row.ProceedsCurrency] + amount)
		}

		key := strings.Join([]string{
			"asc:sales", date, row.AppleID, row.SKU, row.Country, row.ProductType, row.ProceedsCurrency,
		}, ":")
		i := seq[key]
		seq[key]++

		payload, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("appstore: encode row payload: %w", err)
		}

		events = append(events, core.Event{
			ID:         core.NewIDAt(day),
			Source:     Name,
			Kind:       kind,
			App:        appName(row),
			OccurredAt: day,
			ObservedAt: observed,
			Day:        date,
			Country:    row.Country,
			Amount:     amount,
			Currency:   row.ProceedsCurrency,
			Quantity:   row.Units,
			DedupeKey:  fmt.Sprintf("%s:%d", key, i),
			IsLedger:   true,
			Silent:     true,
			Payload:    payload,
		})
	}

	for _, appleID := range order {
		agg := aggregates[appleID]
		summary, err := agg.summaryEvent(date, day, observed)
		if err != nil {
			return nil, err
		}
		events = append(events, summary)
	}
	return events, nil
}

// rowKind names a row. The order matters: a negative unit count is a refund
// whatever the product type was, and a zero-money app row is a free download
// rather than a sale worth nothing.
func rowKind(class rowClass, units int, amount float64) string {
	if units < 0 {
		return KindRefund
	}
	switch class {
	case classSubscription:
		return KindSubscription
	case classIAP:
		return KindIAP
	default:
		if amount == 0 {
			return KindDownload
		}
		return KindSale
	}
}

// summaryEvent builds the one non-silent event of the day: the chest drop.
func (a *dayAggregate) summaryEvent(date string, day, observed time.Time) (core.Event, error) {
	currency, amount := a.dominantCurrency()

	summary := core.SalesDaySummary{
		Units:      a.units,
		Refunds:    a.refunds,
		Proceeds:   amount,
		Currency:   currency,
		Countries:  len(a.countries),
		TopCountry: a.topCountry(),
		BySKU:      a.bySKU(currency),
	}
	payload := SalesDayPayload{
		SalesDaySummary: summary,
		AppleID:         a.appleID,
		Date:            date,
		Rows:            a.rows,
		Downloads:       a.downloads,
		Updates:         a.updates,
		IAPUnits:        a.iapUnits,
		SubUnits:        a.subUnits,
		ProceedsMixed:   len(a.byCurrency) > 1,
		ByCurrency:      a.byCurrency,
		ByProductType:   a.byProductType,
		ByCountry:       a.byCountryAll,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return core.Event{}, fmt.Errorf("appstore: encode sales_day payload: %w", err)
	}

	app := a.title
	if app == "" {
		app = a.appleID
	}
	return core.Event{
		ID:         core.NewIDAt(day),
		Source:     Name,
		Kind:       KindSalesDay,
		App:        app,
		OccurredAt: day,
		ObservedAt: observed,
		Day:        date,
		// A summary spans countries, so it carries none: the settlement drop
		// for a brand new country comes from the row that revealed it.
		Country:   "",
		Amount:    amount,
		Currency:  currency,
		Quantity:  a.units,
		DedupeKey: fmt.Sprintf("%s:%s:%s:%s", Name, KindSalesDay, a.appleID, date),
		IsLedger:  true,
		Silent:    false,
		Chest:     true,
		Payload:   encoded,
	}, nil
}

// dominantCurrency picks the proceeds currency carrying the most money, so the
// headline drop reads in the currency that actually earned the day. Ties break
// alphabetically to keep a re-ingest of the same report identical.
func (a *dayAggregate) dominantCurrency() (string, float64) {
	var (
		best    string
		bestAbs float64
	)
	currencies := make([]string, 0, len(a.byCurrency))
	for c := range a.byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	for _, c := range currencies {
		if abs := absFloat(a.byCurrency[c]); abs > bestAbs {
			best, bestAbs = c, abs
		}
	}
	if best == "" {
		return "", 0
	}
	return best, round2(a.byCurrency[best])
}

// topCountry is the country with the most paid units, falling back to units of
// any kind on a day that only produced free downloads.
func (a *dayAggregate) topCountry() string {
	if c := topKey(a.byCountryPaid); c != "" {
		return c
	}
	return topKey(a.byCountryAll)
}

// bySKU reports each product's units, and its proceeds in the summary's own
// currency so the breakdown adds up to the headline. A SKU that only earned in
// another currency shows its units with zero proceeds; ByCurrency has the rest.
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

// appName prefers the human title Apple ships in the report; a report row
// without one falls back to something a person can still recognise.
func appName(row SalesRow) string {
	switch {
	case row.Title != "":
		return row.Title
	case row.SKU != "":
		return row.SKU
	default:
		return row.AppleID
	}
}

func allowSet(apps []string) map[string]bool {
	if len(apps) == 0 {
		return nil
	}
	out := make(map[string]bool, len(apps))
	for _, a := range apps {
		if a = strings.TrimSpace(a); a != "" {
			out[a] = true
		}
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
