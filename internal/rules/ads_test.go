package rules_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nickhirras/loot/internal/core"
)

// The day's ads drop is a modest common summary, written from the payload
// because an ads_day event's own amount is always zero.
func TestAdsDayDrop(t *testing.T) {
	e := defaultEngine(t, fakeLookup{})
	ev := func(p core.AdsDaySummary) core.Event {
		raw, _ := json.Marshal(p)
		return core.Event{ID: "x", Source: "metaads", Kind: core.KindAdsDay, App: "Example", Payload: raw}
	}

	cases := []struct {
		name            string
		payload         core.AdsDaySummary
		title, subtitle string
	}{
		{
			name: "busy day",
			payload: core.AdsDaySummary{Spend: 18.4, Currency: "USD", LandingPageViews: 1234,
				Conversions: 3, TopAd: "Morning reel"},
			title:    "1234 landing page views from ads · 18.40 USD",
			subtitle: "3 conversions · top ad: Morning reel · Example",
		},
		{
			name:     "one of each",
			payload:  core.AdsDaySummary{Spend: 2, Currency: "EUR", LandingPageViews: 1, Conversions: 1},
			title:    "1 landing page view from ads · 2.00 EUR",
			subtitle: "1 conversion · Example",
		},
		{
			name:     "nothing landed",
			payload:  core.AdsDaySummary{Spend: 0.5, Currency: "USD"},
			title:    "A day of ads · 0.50 USD",
			subtitle: "Example",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drop, err := e.Classify(context.Background(), ev(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if drop.Rarity != core.Common {
				t.Errorf("rarity = %s, want common", drop.Rarity)
			}
			if drop.Title != tc.title || drop.Subtitle != tc.subtitle {
				t.Errorf("got %q / %q\nwant %q / %q", drop.Title, drop.Subtitle, tc.title, tc.subtitle)
			}
		})
	}
}
