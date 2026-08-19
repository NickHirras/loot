package microsoftstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The subscriptions endpoint (GET /v1.0/my/analytics/subscriptions) is a
// *state* report: each row counts the subscriptions that were active, new,
// renewed or churned on the report day, split by add-on, SKU, market and
// device. Its fields are verified against
// https://learn.microsoft.com/en-us/windows/uwp/monetize/get-subscription-acquisitions
//
// Loot reads the counts and deliberately ignores grossSalesBeforeTax: a
// subscription add-on is an add-on, so the same money already arrived as an
// `iap` row from inappacquisitions, and booking it twice would double the
// vault's revenue.
type apiSubscription struct {
	Date                    string  `json:"date"`
	ApplicationID           string  `json:"applicationId"`
	ApplicationName         string  `json:"applicationName"`
	SubscriptionProductID   string  `json:"subscriptionProductId"`
	SubscriptionProductName string  `json:"subscriptionProductName"`
	SKUID                   string  `json:"skuId"`
	Market                  string  `json:"market"`
	DeviceType              string  `json:"deviceType"`
	CurrencyCode            string  `json:"currencyCode"`
	GrossSalesBeforeTax     float64 `json:"grossSalesBeforeTax"`
	TotalActiveCount        float64 `json:"totalActiveCount"`
	TotalChurnCount         float64 `json:"totalChurnCount"`
	NewCount                float64 `json:"newCount"`
	RenewCount              float64 `json:"renewCount"`
}

// SubscriptionSnapshot is the payload of a `subscription_snapshot` event.
type SubscriptionSnapshot struct {
	StoreID   string         `json:"store_id,omitempty"`
	App       string         `json:"app,omitempty"`
	Date      string         `json:"date"`
	Active    int            `json:"active"`
	New       int            `json:"new"`
	Renewed   int            `json:"renewed"`
	Churned   int            `json:"churned"`
	Rows      int            `json:"rows"`
	ByProduct map[string]int `json:"by_product,omitempty"`
	ByCountry map[string]int `json:"by_country,omitempty"`
}

// BuildSubscriptionEvents turns one day of subscription rows into one snapshot
// event per app.
//
// A snapshot is an absolute count, not a delta, so it is silent and *not* a
// ledger event: it is not money and must never be summed into revenue. The
// vault reads the newest snapshot per (source, app).
func BuildSubscriptionEvents(rows []apiSubscription, storeID, appName, date string, observed time.Time) ([]core.Event, error) {
	day, err := time.ParseInLocation(core.DayLayout, date, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("microsoftstore: bad subscription date %q: %w", date, err)
	}

	snapshots := map[string]*SubscriptionSnapshot{}
	var order []string

	for _, r := range rows {
		if normalizeDate(r.Date) != date {
			continue
		}
		id := strings.TrimSpace(r.ApplicationID)
		if id == "" {
			id = storeID
		}
		snap, ok := snapshots[id]
		if !ok {
			snap = &SubscriptionSnapshot{
				StoreID:   id,
				Date:      date,
				ByProduct: map[string]int{},
				ByCountry: map[string]int{},
			}
			snapshots[id] = snap
			order = append(order, id)
		}
		if snap.App == "" {
			snap.App = strings.TrimSpace(r.ApplicationName)
		}

		active := roundInt(r.TotalActiveCount)
		snap.Rows++
		snap.Active += active
		snap.New += roundInt(r.NewCount)
		snap.Renewed += roundInt(r.RenewCount)
		snap.Churned += roundInt(r.TotalChurnCount)
		if active == 0 {
			continue
		}
		if product := firstNonEmpty(r.SubscriptionProductName, r.SubscriptionProductID); product != "" {
			snap.ByProduct[product] += active
		}
		if market := strings.ToUpper(strings.TrimSpace(r.Market)); market != "" {
			snap.ByCountry[market] += active
		}
	}

	sort.Strings(order)
	events := make([]core.Event, 0, len(order))
	for _, id := range order {
		snap := snapshots[id]
		if len(snap.ByProduct) == 0 {
			snap.ByProduct = nil
		}
		if len(snap.ByCountry) == 0 {
			snap.ByCountry = nil
		}
		payload, err := json.Marshal(snap)
		if err != nil {
			return nil, fmt.Errorf("microsoftstore: encode subscription payload: %w", err)
		}
		app := snap.App
		if app == "" {
			app = appName
		}
		events = append(events, core.Event{
			ID:         core.NewIDAt(day),
			Source:     Name,
			Kind:       KindSubscriptionSnapshot,
			App:        displayName(app, id),
			OccurredAt: day,
			ObservedAt: observed,
			Day:        date,
			Quantity:   snap.Active,
			DedupeKey:  fmt.Sprintf("msstore:subs:%s:%s", date, id),
			// A count of subscribers is not a payment: keeping IsLedger false
			// is what stops the vault adding it to revenue.
			IsLedger: false,
			Silent:   true,
			Payload:  payload,
		})
	}
	return events, nil
}

func roundInt(f float64) int {
	if f > 0 {
		return int(f + 0.5)
	}
	if f < 0 {
		return int(f - 0.5)
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
