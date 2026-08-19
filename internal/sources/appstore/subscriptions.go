package appstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The subscription summary report (SUBSCRIPTION / SUMMARY / DAILY, version
// 1_3) is a *state* report, not a transaction log: each row is a bucket of
// subscriptions that were active on the report day, split by app,
// subscription, offer type, state, country and device. Its columns are
// verified against
// https://developer.apple.com/help/app-store-connect/reference/reporting/subscription-report/
//
//	App Name, App Apple ID, Subscription Name, Subscription Apple ID,
//	Subscription Group ID, Standard Subscription Duration,
//	Subscription Offer Name, Promotional Offer ID, Customer Price,
//	Customer Currency, Developer Proceeds, Proceeds Currency,
//	Preserved Pricing, Proceeds Reason, Client, Device, State, Country,
//	Active Standard Price Subscriptions,
//	Active Free Trial Introductory Offer Subscriptions,
//	Active Pay Up Front Introductory Offer Subscriptions,
//	Active Pay As You Go Introductory Offer Subscriptions,
//	Free Trial Promotional Offer Subscriptions,
//	Pay Up Front Promotional Offer Subscriptions,
//	Pay As You Go Promotional Offer Subscriptions,
//	Free Trial Offer Code Subscriptions, Pay Up Front Offer Code Subscriptions,
//	Pay As You Go Offer Code Subscriptions, Marketing Opt-Ins, Billing Retry,
//	Grace Period, Subscribers, and three Win-Back Offers columns.
//
// Apple has added offer types to this report more than once (offer codes,
// then win-back offers), so the active count is computed from every column
// whose name ends in "Subscriptions" or "Offers" rather than from a hard-coded
// list. Marketing Opt-Ins, Billing Retry, Grace Period and Subscribers are
// excluded by that rule, which is what we want: they are not active paying
// state, and Subscribers would double count.

// activeColumn reports whether a column counts toward "active subscriptions".
func activeColumn(header string) bool {
	name := strings.TrimSpace(header)
	return strings.HasSuffix(name, "Subscriptions") || strings.HasSuffix(name, "Offers")
}

// SubscriptionSnapshot is the payload of a `subscription_snapshot` event.
type SubscriptionSnapshot struct {
	AppleID   string         `json:"apple_id,omitempty"`
	App       string         `json:"app,omitempty"`
	Date      string         `json:"date"`
	Active    int            `json:"active"`
	Rows      int            `json:"rows"`
	BySKU     map[string]int `json:"by_sku,omitempty"`
	ByCountry map[string]int `json:"by_country,omitempty"`
	ByState   map[string]int `json:"by_state,omitempty"`
}

// BuildSubscriptionEvents turns one day's subscription summary report into one
// snapshot event per app.
//
// A snapshot is an absolute count, not a delta, so it is silent and *not* a
// ledger event: it is not money and must never be summed into revenue. The
// vault reads the newest snapshot per (source, app).
func BuildSubscriptionEvents(report []byte, date string, apps []string, observed time.Time) ([]core.Event, error) {
	t, err := parseTSV(report)
	if err != nil {
		return nil, err
	}

	appleIDCol := t.index("App Apple ID", "Apple Identifier")
	if appleIDCol < 0 {
		return nil, fmt.Errorf("appstore: subscription report has no App Apple ID column (got %q)",
			strings.Join(t.header, "|"))
	}

	// Resolve the active-count columns once, not per row.
	var activeCols []int
	for i, name := range t.header {
		if activeColumn(name) {
			activeCols = append(activeCols, i)
		}
	}
	if len(activeCols) == 0 {
		return nil, fmt.Errorf("appstore: subscription report has no subscription count columns (got %q)",
			strings.Join(t.header, "|"))
	}

	day, err := time.ParseInLocation(core.DayLayout, date, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("appstore: bad report date %q: %w", date, err)
	}

	allow := allowSet(apps)
	snapshots := map[string]*SubscriptionSnapshot{}
	var order []string

	for _, r := range t.rows {
		if appleIDCol >= len(r) {
			continue
		}
		appleID := strings.TrimSpace(r[appleIDCol])
		if len(allow) > 0 && !allow[appleID] {
			continue
		}

		active := 0
		for _, c := range activeCols {
			if c < len(r) {
				active += atoi(r[c])
			}
		}

		snap, ok := snapshots[appleID]
		if !ok {
			snap = &SubscriptionSnapshot{
				AppleID:   appleID,
				Date:      date,
				BySKU:     map[string]int{},
				ByCountry: map[string]int{},
				ByState:   map[string]int{},
			}
			snapshots[appleID] = snap
			order = append(order, appleID)
		}
		if snap.App == "" {
			snap.App = t.get(r, "App Name", "Title")
		}
		snap.Rows++
		snap.Active += active
		if active == 0 {
			continue
		}
		if sku := t.get(r, "SKU", "Subscription Name", "Subscription Apple ID"); sku != "" {
			snap.BySKU[sku] += active
		}
		if country := strings.ToUpper(t.get(r, "Country", "Country Code")); country != "" {
			snap.ByCountry[country] += active
		}
		if state := t.get(r, "State"); state != "" {
			snap.ByState[state] += active
		}
	}

	sort.Strings(order)
	events := make([]core.Event, 0, len(order))
	for _, appleID := range order {
		snap := snapshots[appleID]
		pruneEmpty(snap)

		payload, err := json.Marshal(snap)
		if err != nil {
			return nil, fmt.Errorf("appstore: encode subscription payload: %w", err)
		}
		app := snap.App
		if app == "" {
			app = appleID
		}
		events = append(events, core.Event{
			ID:         core.NewIDAt(day),
			Source:     Name,
			Kind:       KindSubscriptionSnapshot,
			App:        app,
			OccurredAt: day,
			ObservedAt: observed,
			Day:        date,
			Quantity:   snap.Active,
			DedupeKey:  fmt.Sprintf("asc:subs:%s:%s", date, appleID),
			// A count of subscribers is not a payment: keeping IsLedger false
			// is what stops the vault adding it to revenue.
			IsLedger: false,
			Silent:   true,
			Payload:  payload,
		})
	}
	return events, nil
}

func pruneEmpty(s *SubscriptionSnapshot) {
	if len(s.BySKU) == 0 {
		s.BySKU = nil
	}
	if len(s.ByCountry) == 0 {
		s.ByCountry = nil
	}
	if len(s.ByState) == 0 {
		s.ByState = nil
	}
}
