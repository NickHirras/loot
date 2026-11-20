package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func TestHandleHearthTrendValidationAndScope(t *testing.T) {
	h := newScopedHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := core.DayOf(now)

	for _, ev := range []core.Event{
		{
			ID: core.NewID(), Source: "snapcraft", Kind: core.KindAudience,
			App: "nistis-snap", Product: "Nistis",
			OccurredAt: now, ObservedAt: now, Day: today, Country: "US",
			Quantity: 12, DedupeKey: "snap:nistis:us", Silent: true,
		},
		{
			ID: core.NewID(), Source: "snapcraft", Kind: core.KindAudience,
			App: "nistis-snap", Product: "Nistis",
			OccurredAt: now, ObservedAt: now, Day: today, Country: "",
			Quantity: 15, DedupeKey: "snap:nistis:tot", Silent: true,
		},
		{
			ID: core.NewID(), Source: "appstore", Kind: "download",
			App: "Macro Trainer", Product: "Macro Trainer",
			OccurredAt: now, ObservedAt: now, Day: today, Country: "US",
			Quantity: 4, DedupeKey: "as:mt:1", IsLedger: true, Silent: true,
		},
	} {
		if _, err := h.store.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	// Bad requests.
	bad := []string{
		"/api/hearth/trend",
		"/api/hearth/trend?metric=unknown",
		"/api/hearth/trend?metric=population&range=bogus",
		"/api/hearth/trend?metric=audience",
		"/api/hearth/trend?metric=downloads",
		"/api/hearth/trend?metric=fleet",
		"/api/hearth/trend?metric=country&country=USA",
	}
	for _, path := range bad {
		resp, err := http.Get(h.srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", path, resp.StatusCode)
		}
	}

	// Valid unscoped population: 12 (Nistis US) + 4 (Macro Trainer US) = 16.
	getTrend := func(path string) store.HearthTrend {
		t.Helper()
		resp, err := http.Get(h.srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
		}
		var out store.HearthTrend
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return out
	}

	allPop := getTrend("/api/hearth/trend?metric=population&range=7d")
	if allPop.Current != 16 {
		t.Errorf("all population = %v, want 16", allPop.Current)
	}

	scopedPop := getTrend("/api/hearth/trend?metric=population&range=7d&app=Nistis")
	if scopedPop.Current != 12 {
		t.Errorf("scoped population = %v, want 12", scopedPop.Current)
	}

	aud := getTrend("/api/hearth/trend?metric=audience&product=Nistis&range=all")
	if aud.Current != 15 || aud.Basis != core.BasisWeeklyDevices {
		t.Errorf("audience = %+v, want current=15 basis=weekly_devices", aud)
	}
}
