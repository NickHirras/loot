package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

// The app scope over HTTP: `?app=` on the read endpoints, and GET /api/apps —
// the endpoint that answers "what could I scope to, and did my mapping catch
// everything?".

var scopeApps = config.Products{
	{Name: "Nistis", Match: map[string][]string{
		"appstore":   {"Nistis: Fasting Timer"},
		"revenuecat": {"app5525946104"},
	}},
	{Name: "Macro Trainer", Match: map[string][]string{
		"appstore": {"Macro Trainer"},
	}},
}

// newScopedHarness is newHarness with an `apps:` mapping in the config and a
// pipeline that resolves products at ingest, which is how a real server runs.
func newScopedHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}

	b := bus.New(64)
	p := pipeline.New(st, engine, b, quietLogger())
	p.Products = scopeApps

	cfg := config.Default()
	cfg.Apps = scopeApps

	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}
	s := server.New(cfg, st, b, p, nil, static, quietLogger())

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &harness{srv: ts, store: st, bus: b}
}

// seedScoped ingests one sale for each of two mapped products plus one for an
// app nothing claims, through the real pipeline so products are resolved the
// way they are in production.
func seedScoped(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()

	engine, err := rules.Load("", h.store)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	p := pipeline.New(h.store, engine, h.bus, quietLogger())
	p.Products = scopeApps

	today := core.DayOf(time.Now().UTC())
	rows := []core.Event{
		{Source: "appstore", Kind: "sale", App: "Nistis: Fasting Timer", Country: "US",
			Quantity: 3, Amount: 30, Currency: "USD", IsLedger: true, Day: today, DedupeKey: "as:n1"},
		{Source: "revenuecat", Kind: "purchase", App: "app5525946104", Country: "US",
			Quantity: 1, Day: today, DedupeKey: "rc:n1"},
		{Source: "appstore", Kind: "sale", App: "Macro Trainer", Country: "JP",
			Quantity: 2, Amount: 20, Currency: "USD", IsLedger: true, Day: today, DedupeKey: "as:m1"},
		{Source: "flathub", Kind: "install", App: "net.example.Unclaimed",
			Quantity: 7, Day: today, DedupeKey: "fh:u1"},
	}
	for _, ev := range rows {
		if _, err := p.Ingest(ctx, ev); err != nil {
			t.Fatalf("ingest %s: %v", ev.DedupeKey, err)
		}
	}
}

// TestAppsEndpointShape pins what GET /api/apps answers with, because both the
// scope selector and `loot apps` are written against it.
func TestAppsEndpointShape(t *testing.T) {
	h := newScopedHarness(t)
	seedScoped(t, h)

	resp, body := h.get(t, "/api/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	products, ok := body["products"].([]any)
	if !ok {
		t.Fatalf("products missing or not a list: %#v", body["products"])
	}
	// Two configured products plus the one nobody mapped.
	if len(products) != 3 {
		t.Fatalf("got %d products, want 3: %#v", len(products), products)
	}

	first, _ := products[0].(map[string]any)
	if first["name"] != "Nistis" {
		t.Errorf("first product = %v, want the first configured one", first["name"])
	}
	if first["configured"] != true {
		t.Errorf("Nistis is configured; got %v", first["configured"])
	}
	if first["events"].(float64) != 2 {
		t.Errorf("Nistis events = %v, want 2", first["events"])
	}
	// The evidence: which source called it what.
	sources, _ := first["sources"].(map[string]any)
	if len(sources) != 2 {
		t.Fatalf("Nistis sources = %#v, want appstore and revenuecat", sources)
	}
	if names, _ := sources["revenuecat"].([]any); len(names) != 1 || names[0] != "app5525946104" {
		t.Errorf("revenuecat raw names = %#v", sources["revenuecat"])
	}

	unmapped, ok := body["unmapped"].([]any)
	if !ok {
		t.Fatalf("unmapped missing: %#v", body["unmapped"])
	}
	if len(unmapped) != 1 {
		t.Fatalf("got %d unmapped pairs, want 1: %#v", len(unmapped), unmapped)
	}
	pair, _ := unmapped[0].(map[string]any)
	if pair["source"] != "flathub" || pair["app"] != "net.example.Unclaimed" {
		t.Errorf("unmapped pair = %#v", pair)
	}
	if pair["product"] != "net.example.Unclaimed" {
		t.Errorf("an unmapped app should resolve to its own name, got %v", pair["product"])
	}
}

// TestStatsCarriesTheScope: the header draws its selector from the stats it
// already fetches, and its counts narrow with `?app=`.
func TestStatsCarriesTheScope(t *testing.T) {
	h := newScopedHarness(t)
	seedScoped(t, h)

	_, all := h.get(t, "/api/stats")
	apps, _ := all["apps"].([]any)
	if len(apps) != 3 {
		t.Fatalf("stats apps = %#v, want three selectable products", apps)
	}
	if apps[0] != "Nistis" || apps[1] != "Macro Trainer" {
		t.Errorf("configured products should come first, in order: %#v", apps)
	}
	if all["app"] != "" {
		t.Errorf("unscoped stats says app = %v", all["app"])
	}
	allDrops := all["total_drops"].(float64)

	_, scoped := h.get(t, "/api/stats?app="+url.QueryEscape("Macro Trainer"))
	if scoped["app"] != "Macro Trainer" {
		t.Errorf("scoped stats says app = %v", scoped["app"])
	}
	if scoped["total_drops"].(float64) >= allDrops {
		t.Errorf("scoped drops (%v) should be fewer than all (%v)", scoped["total_drops"], allDrops)
	}
	// The selector's options never narrow: you have to be able to leave.
	if scopedApps, _ := scoped["apps"].([]any); len(scopedApps) != 3 {
		t.Errorf("scoped stats lost the app list: %#v", scopedApps)
	}
}

// TestScopedEndpoints walks the read endpoints with `?app=` and checks each
// one actually narrows.
func TestScopedEndpoints(t *testing.T) {
	h := newScopedHarness(t)
	seedScoped(t, h)

	t.Run("vault", func(t *testing.T) {
		_, all := h.get(t, "/api/vault/summary?range=30d")
		_, one := h.get(t, "/api/vault/summary?range=30d&app=Nistis")
		if total(all) != 50 {
			t.Fatalf("unscoped revenue = %v, want 50", total(all))
		}
		if total(one) != 30 {
			t.Errorf("Nistis revenue = %v, want 30", total(one))
		}
	})

	t.Run("hearth", func(t *testing.T) {
		_, all := h.get(t, "/api/hearth")
		_, one := h.get(t, "/api/hearth?app=Nistis")
		if got := len(all["countries"].([]any)); got != 2 {
			t.Fatalf("unscoped countries = %d, want 2", got)
		}
		countries := one["countries"].([]any)
		if len(countries) != 1 {
			t.Fatalf("Nistis countries = %#v, want one", countries)
		}
		if c, _ := countries[0].(map[string]any); c["country"] != "US" {
			t.Errorf("Nistis country = %v, want US", c["country"])
		}

		// The fleet narrows with the map: the unclaimed app's Flathub installs
		// belong to nobody else's globe.
		allFleet, _ := all["fleet"].([]any)
		if len(allFleet) != 1 {
			t.Fatalf("unscoped fleet = %#v, want the flathub vessel", allFleet)
		}
		v, _ := allFleet[0].(map[string]any)
		if v["source"] != "flathub" || v["population"].(float64) != 7 {
			t.Errorf("unscoped vessel = %v, want flathub with 7 people", v)
		}
		if fleet, _ := one["fleet"].([]any); len(fleet) != 0 {
			t.Errorf("Nistis fleet = %#v, want nothing afloat", fleet)
		}
	})

	t.Run("drops", func(t *testing.T) {
		_, all := h.get(t, "/api/drops")
		_, one := h.get(t, "/api/drops?app=Nistis")
		allDrops := all["drops"].([]any)
		oneDrops := one["drops"].([]any)
		if len(oneDrops) >= len(allDrops) {
			t.Fatalf("scoped feed (%d) is not narrower than the whole (%d)", len(oneDrops), len(allDrops))
		}
		for _, raw := range oneDrops {
			d, _ := raw.(map[string]any)
			product, _ := d["product"].(string)
			if product != "" && product != "Nistis" {
				t.Errorf("a drop for %q appeared in the Nistis feed: %v", product, d["title"])
			}
		}
	})

	t.Run("an unknown app answers empty rather than failing", func(t *testing.T) {
		resp, body := h.get(t, "/api/vault/summary?range=30d&app=Ghost+App")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if total(body) != 0 {
			t.Errorf("a scope with no data returned %v", total(body))
		}
	})

	t.Run("chests stay global", func(t *testing.T) {
		// Chests are one daily ritual over everything; `?app=` is accepted and
		// deliberately ignored, so a scoped client's request builder does not
		// need a special case.
		respAll, all := h.get(t, "/api/chest")
		_, one := h.get(t, "/api/chest?app=Nistis")
		if respAll.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", respAll.StatusCode)
		}
		if !jsonEqual(t, all["chests"], one["chests"]) {
			t.Errorf("the chest list changed with the scope: %v vs %v", all["chests"], one["chests"])
		}
	})
}

// total digs the revenue figure out of a vault summary.
func total(body map[string]any) float64 {
	totals, _ := body["totals"].(map[string]any)
	v, _ := totals["revenue_base"].(float64)
	return v
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(ja) == string(jb)
}
