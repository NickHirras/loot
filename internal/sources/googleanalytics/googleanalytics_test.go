package googleanalytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

func TestPollEmitsTotalAndCountryAudienceAndAdvancesCursor(t *testing.T) {
	var requests []runReportRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req runReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, req)

		w.Header().Set("Content-Type", "application/json")
		if len(req.Dimensions) == 1 {
			// Total per day
			_, _ = w.Write([]byte(`{"rows":[
				{"dimensionValues":[{"value":"20261125"}],"metricValues":[{"value":"20"}]},
				{"dimensionValues":[{"value":"20261126"}],"metricValues":[{"value":"24"}]}
			]}`))
			return
		}
		// Per country per day (includes "(not set)" and "ZZ" which must be skipped)
		_, _ = w.Write([]byte(`{"rows":[
			{"dimensionValues":[{"value":"20261125"},{"value":"US"}],"metricValues":[{"value":"15"}]},
			{"dimensionValues":[{"value":"20261125"},{"value":"(not set)"}],"metricValues":[{"value":"5"}]},
			{"dimensionValues":[{"value":"20261126"},{"value":"US"}],"metricValues":[{"value":"18"}]},
			{"dimensionValues":[{"value":"20261126"},{"value":"DE"}],"metricValues":[{"value":"4"}]},
			{"dimensionValues":[{"value":"20261126"},{"value":"ZZ"}],"metricValues":[{"value":"2"}]}
		]}`))
	}))
	defer srv.Close()

	src := &Source{
		Properties: []config.GoogleAnalyticsProperty{
			{PropertyID: "properties/123456789", App: "Macro Trainer", Platforms: []string{"iOS"}},
		},
		WindowDays:   28,
		BackfillDays: 7,
		BaseURL:      srv.URL,
		Client:       srv.Client(),
		Now:          func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
	}

	events, nextState, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].Metrics[0].Name != "active28DayUsers" {
		t.Errorf("metric = %s, want active28DayUsers", requests[0].Metrics[0].Name)
	}
	if requests[0].DimensionFilter == nil || requests[0].DimensionFilter.Filter.FieldName != "platform" {
		t.Errorf("expected platform dimensionFilter, got %+v", requests[0].DimensionFilter)
	}
	// Expect 2 total rows + 3 valid country rows (US on 25, DE & US on 26) = 5 events.
	if len(events) != 5 {
		t.Fatalf("events = %d (%+v), want 5", len(events), events)
	}
	for _, ev := range events {
		if ev.Source != Name || ev.Kind != core.KindAudience || ev.App != "Macro Trainer" || !ev.Silent {
			t.Errorf("unexpected event: %+v", ev)
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if payload["covers_android"] != false {
			t.Errorf("iOS-only property should set covers_android=false, got %v", payload["covers_android"])
		}
	}

	// Second poll on the same day should make no requests because cursor reached 2026-11-26.
	requests = nil
	events2, _, err := src.Poll(context.Background(), nextState)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(requests) != 0 || len(events2) != 0 {
		t.Errorf("second poll fetched %d requests / %d events, want 0/0", len(requests), len(events2))
	}
}

func TestCoversAndroidDefaultAndCheckError(t *testing.T) {
	if !coversAndroid(nil) {
		t.Error("empty platforms should cover Android")
	}
	if !coversAndroid([]string{"iOS", "Android"}) {
		t.Error("iOS+Android should cover Android")
	}
	if coversAndroid([]string{"iOS"}) {
		t.Error("iOS-only should not cover Android")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	}))
	defer srv.Close()

	src := &Source{
		Properties: []config.GoogleAnalyticsProperty{{PropertyID: "999", App: "Reson8"}},
		WindowDays: 7,
		BaseURL:    srv.URL,
		Client:     srv.Client(),
		Now:        func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
	}
	err := src.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Viewer") {
		t.Fatalf("Check error = %v, want Viewer hint", err)
	}
}
