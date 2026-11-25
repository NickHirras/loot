package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A report with two complete days of ads, today in progress, stores two days
// behind, and two notes.
const funnelJSON = `{
  "enabled": true, "product": "Example", "from": "2026-03-04", "to": "2026-03-10",
  "currency": "USD", "conversion_label": "store tap", "site_event": "",
  "days": [
    {"day":"2026-03-04","spend":null,"impressions":null,"link_clicks":null,"landing_page_views":null,"conversions":null,"link_visits":null,"site_events":null,"installs":5,"purchases":1,"revenue":2.5},
    {"day":"2026-03-05","spend":null,"impressions":null,"link_clicks":null,"landing_page_views":null,"conversions":null,"link_visits":null,"site_events":null,"installs":6,"purchases":0,"revenue":0},
    {"day":"2026-03-06","spend":null,"impressions":null,"link_clicks":null,"landing_page_views":null,"conversions":null,"link_visits":null,"site_events":null,"installs":4,"purchases":0,"revenue":0},
    {"day":"2026-03-07","spend":null,"impressions":null,"link_clicks":null,"landing_page_views":null,"conversions":null,"link_visits":null,"site_events":null,"installs":7,"purchases":2,"revenue":5},
    {"day":"2026-03-08","spend":10,"impressions":12000,"link_clicks":80,"landing_page_views":50,"conversions":2,"link_visits":40,"site_events":null,"installs":9,"purchases":1,"revenue":2.5},
    {"day":"2026-03-09","spend":20,"impressions":20000,"link_clicks":100,"landing_page_views":80,"conversions":4,"link_visits":70,"site_events":null,"installs":null,"purchases":null,"revenue":null},
    {"day":"2026-03-10","spend":3,"impressions":1000,"link_clicks":5,"landing_page_views":4,"conversions":0,"link_visits":3,"site_events":null,"installs":null,"purchases":null,"revenue":null}
  ],
  "ads": [], "totals": {"day":""},
  "notes": [
    {"code":"store_lag","params":{"installs_as_of":"2026-03-08","days":2}},
    {"code":"spend_no_conversions","ad_id":"a1","ad":"Morning reel","params":{"spend":25}}
  ],
  "as_of": {"ads":"2026-03-10","links":"2026-03-10","installs":"2026-03-08","sales":"2026-03-08"}
}`

func TestFunnelSummary(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/funnel" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, funnelJSON)
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runFunnel([]string{"--url", srv.URL, "--days", "7", "--app", "Example"})
	})
	if err != nil {
		t.Fatalf("funnel: %v", err)
	}
	if query != "app=Example&days=7" {
		t.Errorf("query = %q", query)
	}
	for _, want := range []string{
		"Funnel · Example · USD",
		"Yesterday, 2026-03-09",
		"Spend                   20.00 USD",
		"Impressions                20,000   1.00 USD per 1,000",
		"Landing page views             80   0.25 USD each",
		"Store taps                      4   5.00 USD each",
		"Installs (all)         not in yet",
		"Last 6 days, 2026-03-04 to 2026-03-09",
		"Spend                   30.00 USD",
		"Installs (all)                 31",
		"Reported so far: installs up to 2026-03-08; sales up to 2026-03-08.",
		"The stores have reported up to 2026-03-08, 2 days behind the ads",
		"Morning reel has spent 25.00 USD without a store tap.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Store stages count every customer, so they get no cost per step.
	if strings.Contains(out, "per install") || strings.Contains(out, "31   ") {
		t.Errorf("store stages should carry no cost:\n%s", out)
	}
}

func TestFunnelJSONFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":false}`)
	}))
	defer srv.Close()
	out, err := captureStdout(t, func() error { return runFunnel([]string{"--url", srv.URL, "--json"}) })
	if err != nil || strings.TrimSpace(out) != `{"enabled":false}` {
		t.Errorf("--json = %q, %v", out, err)
	}
}
