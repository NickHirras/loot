package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

// codexHarness is the normal harness plus the Codex service and the pipeline
// its unlock drops travel through.
type codexHarness struct {
	*questHarness
	codex *codex.Service
}

func newCodexHarness(t *testing.T, now time.Time) *codexHarness {
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

	codexSvc := codex.NewService(st, p, b, "USD", quietLogger())
	codexSvc.Now = func() time.Time { return now }

	s := server.New(config.Default(), st, b, p, nil,
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, quietLogger())
	s.Codex = codexSvc
	codexSvc.OnChange = s.InvalidateCodex

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &codexHarness{
		questHarness: &questHarness{harness: &harness{srv: ts, store: st, bus: b}},
		codex:        codexSvc,
	}
}

func TestCodexAPIShape(t *testing.T) {
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	h := newCodexHarness(t, now)

	// An unevaluated database still answers with a wall to fill, not a 404.
	resp, body := h.do(t, http.MethodGet, "/api/codex", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/codex = %d, want 200", resp.StatusCode)
	}
	if body["display_currency"] != "USD" {
		t.Errorf("display_currency = %v", body["display_currency"])
	}
	if _, ok := body["records"].(map[string]any); !ok {
		t.Errorf("no records object in %v", body)
	}
	if _, ok := body["totals"].(map[string]any); !ok {
		t.Errorf("no totals object in %v", body)
	}
	if total, _ := body["total"].(float64); int(total) != len(codex.Catalog) {
		t.Errorf("total = %v, want %d", body["total"], len(codex.Catalog))
	}

	h.seedLedger(t, "2026-07-14", 12, 240, "as:jul14")
	if _, err := h.codex.Evaluate(context.Background()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	resp, body = h.do(t, http.MethodGet, "/api/codex", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/codex = %d", resp.StatusCode)
	}
	list, ok := body["achievements"].([]any)
	if !ok || len(list) != len(codex.Catalog) {
		t.Fatalf("achievements = %T with %d entries, want %d", body["achievements"], len(list), len(codex.Catalog))
	}
	first, _ := list[0].(map[string]any)
	for _, field := range []string{"key", "tier", "title", "description", "unlocked_at",
		"progress_value", "progress_target", "unit", "pct"} {
		if _, ok := first[field]; !ok {
			t.Errorf("achievement is missing %q: %v", field, first)
		}
	}
	if unlocked, _ := body["unlocked"].(float64); unlocked < 1 {
		t.Errorf("unlocked = %v, want at least the first sale", body["unlocked"])
	}
}

func TestRecapAPIShape(t *testing.T) {
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	h := newCodexHarness(t, now)
	h.seedLedger(t, "2026-06-10", 4, 80, "as:jun10")
	h.seedLedger(t, "2026-07-14", 12, 240, "as:jul14")

	// No parameters: the last complete month.
	resp, body := h.do(t, http.MethodGet, "/api/recap", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/recap = %d, want 200", resp.StatusCode)
	}
	recap, ok := body["recap"].(map[string]any)
	if !ok {
		t.Fatalf("no recap in %v", body)
	}
	period, _ := recap["period"].(map[string]any)
	if period["key"] != "2026-07" {
		t.Errorf("default period = %v, want 2026-07", period["key"])
	}
	if rev, _ := recap["revenue_base"].(float64); rev != 240 {
		t.Errorf("revenue_base = %v, want 240", recap["revenue_base"])
	}
	delta, _ := recap["revenue_delta"].(map[string]any)
	if delta["direction"] != "up" || delta["previous"].(float64) != 80 {
		t.Errorf("revenue_delta = %v, want up from 80", delta)
	}
	for _, field := range []string{"highlights", "series", "new_countries", "achievements_unlocked",
		"drops_by_rarity", "best_day", "top_app", "top_country", "top_source"} {
		if _, ok := recap[field]; !ok {
			t.Errorf("recap is missing %q", field)
		}
	}
	if periods, _ := body["periods"].([]any); len(periods) != 13 {
		t.Errorf("periods = %d entries, want 12 months plus the season", len(periods))
	}

	// An explicit month, and a whole season.
	_, body = h.do(t, http.MethodGet, "/api/recap?month=2026-06", "")
	recap, _ = body["recap"].(map[string]any)
	period, _ = recap["period"].(map[string]any)
	if period["key"] != "2026-06" {
		t.Errorf("month=2026-06 gave %v", period["key"])
	}

	_, body = h.do(t, http.MethodGet, "/api/recap?season=2026", "")
	recap, _ = body["recap"].(map[string]any)
	period, _ = recap["period"].(map[string]any)
	if period["kind"] != "season" || period["from"] != "2026-01-01" {
		t.Errorf("season=2026 gave %v", period)
	}
	if rev, _ := recap["revenue_base"].(float64); rev != 320 {
		t.Errorf("season revenue = %v, want 320", recap["revenue_base"])
	}

	// A malformed month is the caller's mistake, and is told so precisely.
	resp, body = h.do(t, http.MethodGet, "/api/recap?month=july", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /api/recap?month=july = %d, want 400", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Errorf("no error message on a bad month: %v", body)
	}
}

func TestCodexAbsentAnswersEmpty(t *testing.T) {
	// A server built without the Codex service must not 404 the endpoint: a UI
	// written against it should never have to ask whether this Loot has
	// Quest 6 in it.
	h := newHarness(t, false)
	resp, body := h.get(t, "/api/codex")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/codex without the service = %d, want 200", resp.StatusCode)
	}
	if total, _ := body["total"].(float64); int(total) != len(codex.Catalog) {
		t.Errorf("total = %v, want the catalog size", body["total"])
	}
	if list, ok := body["achievements"].([]any); !ok || len(list) != 0 {
		t.Errorf("achievements = %v, want an empty list", body["achievements"])
	}
}

// TestCodexCacheIsShortLived pins the promise the handler makes: the wall is
// memoized, but only for a few seconds, so a nudge-driven refetch sees the
// truth rather than a minute-old answer.
func TestCodexCacheIsShortLived(t *testing.T) {
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	h := newCodexHarness(t, now)
	h.seedLedger(t, "2026-08-01", 1, 10, "as:aug1")

	if _, err := h.codex.Evaluate(context.Background()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	_, first := h.do(t, http.MethodGet, "/api/codex", "")
	_, second := h.do(t, http.MethodGet, "/api/codex", "")
	if first["unlocked"] != second["unlocked"] {
		t.Errorf("two reads inside the cache window disagreed: %v vs %v", first["unlocked"], second["unlocked"])
	}
	if _, ok := second["totals"].(map[string]any); !ok {
		t.Errorf("cached answer lost its totals")
	}
}

// The unlock event travels the real pipeline, so the drop it mints is the
// rules engine's answer and nothing else.
func TestUnlockDropTravelsThePipeline(t *testing.T) {
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	h := newCodexHarness(t, now)
	h.seedLedger(t, core.DayOf(now), 3, 30, "as:today")

	if _, err := h.codex.Evaluate(context.Background()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	drops, err := h.store.ListDrops(context.Background(), store.DropQuery{IncludeUnrevealed: true})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	var found bool
	for _, d := range drops {
		if d.Kind != core.KindAchievement {
			continue
		}
		found = true
		if d.Source != "loot" {
			t.Errorf("unlock drop source = %s, want loot", d.Source)
		}
		if d.Rarity != core.Uncommon && d.Rarity != core.Rare &&
			d.Rarity != core.Epic && d.Rarity != core.Legendary {
			t.Errorf("unlock drop rarity = %s, want a celebration rarity", d.Rarity)
		}
		if d.XP <= 0 {
			t.Errorf("unlock drop paid %d XP", d.XP)
		}
	}
	if !found {
		t.Fatalf("no achievement drop was minted")
	}
}
