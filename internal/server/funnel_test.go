package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// funnelAd is one ad_day snapshot as the ads source stores it.
func funnelAd(t *testing.T, product, day, adID string, spend float64, currency string, views int) core.Event {
	t.Helper()
	payload, err := json.Marshal(core.AdDay{
		AdID: adID, Ad: "Ad " + adID, Spend: spend, Currency: currency,
		LandingPageViews: views, Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return core.Event{
		ID: core.NewID(), Source: "metaads", Kind: core.KindAdDay, App: product, Product: product,
		Day: day, OccurredAt: now, ObservedAt: now, Silent: true, Payload: payload,
		DedupeKey: core.FunnelSnapshotKey("metaads", core.KindAdDay, adID, day, product),
	}
}

func TestFunnelEndpoint(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()

	t.Run("empty", func(t *testing.T) {
		resp, body := h.get(t, "/api/funnel")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if body["enabled"] != false {
			t.Errorf("enabled = %v, want false with no funnel data", body["enabled"])
		}
		if days, _ := body["days"].([]any); len(days) != 14 {
			t.Errorf("default window = %d days, want 14", len(days))
		}
		// Empty lists are lists, so the page never has to guard against null.
		for _, key := range []string{"ads", "notes"} {
			if _, ok := body[key].([]any); !ok {
				t.Errorf("%s = %#v, want []", key, body[key])
			}
		}
		if body["currency"] != "USD" {
			t.Errorf("currency = %v", body["currency"])
		}
	})

	t.Run("days are clamped", func(t *testing.T) {
		for q, want := range map[string]int{"0": 1, "-3": 1, "7": 7, "500": 90} {
			_, body := h.get(t, "/api/funnel?days="+q)
			if days, _ := body["days"].([]any); len(days) != want {
				t.Errorf("days=%s gave %d days, want %d", q, len(days), want)
			}
		}
		if resp, _ := h.get(t, "/api/funnel?days=week"); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("days=week status = %d, want 400", resp.StatusCode)
		}
	})

	yesterday := core.DayOf(time.Now().UTC().AddDate(0, 0, -1))
	if err := h.store.PutFXRates(ctx, "USD", map[string]float64{"EUR": 0.8}, yesterday); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []core.Event{
		funnelAd(t, "Alpha", yesterday, "a1", 8, "EUR", 40),
		funnelAd(t, "Beta", yesterday, "b1", 5, "USD", 10),
	} {
		if _, err := h.store.InsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("scoped and converted", func(t *testing.T) {
		_, all := h.get(t, "/api/funnel?days=3")
		if all["enabled"] != true {
			t.Fatalf("enabled = %v", all["enabled"])
		}
		totals := all["totals"].(map[string]any)
		// 8 EUR at the cached 0.8 is 10 USD, plus Beta's 5.
		if totals["spend"] != 15.0 {
			t.Errorf("total spend = %v, want 15", totals["spend"])
		}
		// Neither ad reports the conversion, which is worth one note.
		notes, _ := all["notes"].([]any)
		if len(notes) == 0 || notes[0].(map[string]any)["code"] != core.FunnelNoteUntracked {
			t.Errorf("notes = %#v, want untracked first", notes)
		}

		_, alpha := h.get(t, "/api/funnel?days=3&app=Alpha")
		if alpha["product"] != "Alpha" {
			t.Errorf("product = %v", alpha["product"])
		}
		ads := alpha["ads"].([]any)
		if len(ads) != 1 || ads[0].(map[string]any)["ad_id"] != "a1" {
			t.Errorf("Alpha ads = %#v", ads)
		}
		if got := alpha["totals"].(map[string]any)["spend"]; got != 10.0 {
			t.Errorf("Alpha spend = %v, want 10", got)
		}
		// Installs were never reported by anything: nil, not zero.
		if got := alpha["totals"].(map[string]any)["installs"]; got != nil {
			t.Errorf("installs = %v, want null", got)
		}
	})
}
