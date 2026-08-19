package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/mysteries"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

// questHarness is the normal harness plus the quest and mystery services, and
// the pipeline they mint their reward drops through.
type questHarness struct {
	*harness
	quests    *quests.Service
	mysteries *mysteries.Service
	detector  *mysteries.Detector
}

func newQuestHarness(t *testing.T) *questHarness {
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

	questSvc := quests.NewService(st, p, b, "USD", quietLogger())
	mysterySvc := mysteries.NewService(st, p, b, quietLogger())
	detector := mysteries.NewDetector(st, b, "USD", quietLogger())

	s := server.New(config.Default(), st, b, p, nil,
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, quietLogger())
	s.Quests = questSvc
	s.Mysteries = mysterySvc

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &questHarness{
		harness:   &harness{srv: ts, store: st, bus: b},
		quests:    questSvc,
		mysteries: mysterySvc,
		detector:  detector,
	}
}

func (h *questHarness) do(t *testing.T, method, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp, out
}

func (h *questHarness) seedLedger(t *testing.T, day string, units int, amount float64, dedupe string) {
	t.Helper()
	occurred, _ := time.Parse(core.DayLayout, day)
	if _, err := h.store.InsertEvent(context.Background(), core.Event{
		ID: core.NewID(), Source: "appstore", Kind: "sale", App: "Notes",
		Day: day, OccurredAt: occurred, ObservedAt: occurred, Country: "US",
		Amount: amount, AmountBase: amount, Currency: "USD", Quantity: units,
		IsLedger: true, Silent: true, DedupeKey: dedupe,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestQuestsAPI(t *testing.T) {
	h := newQuestHarness(t)
	today := core.DayOf(time.Now().UTC())
	h.seedLedger(t, today, 12, 120, "as:today")

	// An empty board answers with empty lists rather than nulls.
	resp, body := h.get(t, "/api/quests")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(body["active"].([]any)) != 0 || len(body["recent"].([]any)) != 0 {
		t.Fatalf("a fresh board is not empty: %v", body)
	}

	resp, created := h.post(t, "/api/quests", `{"metric":"revenue","target":100,"window":"week","title":"Make $100"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %v", resp.StatusCode, created)
	}
	quest := created["quest"].(map[string]any)
	if quest["kind"] != "custom" || quest["metric"] != "revenue" {
		t.Fatalf("created quest = %v", quest)
	}
	// Progress is measured the moment it is set, from history already stored.
	if quest["value"].(float64) != 120 {
		t.Errorf("value = %v, want 120", quest["value"])
	}

	_, body = h.get(t, "/api/quests")
	active := body["active"].([]any)
	if len(active) != 1 {
		t.Fatalf("want one active quest, got %d", len(active))
	}
	first := active[0].(map[string]any)
	if first["pct"].(float64) != 1 {
		t.Errorf("pct = %v, want 1", first["pct"])
	}
	if first["days_left"].(float64) < 1 {
		t.Errorf("days_left = %v, want at least 1", first["days_left"])
	}

	// The stats endpoint carries the counts the header badge needs.
	_, stats := h.get(t, "/api/stats")
	if stats["active_quests"].(float64) != 1 {
		t.Errorf("active_quests = %v", stats["active_quests"])
	}
	if _, ok := stats["open_mysteries"]; !ok {
		t.Error("stats is missing open_mysteries")
	}

	// Bad requests are rejected with a reason, not a 500.
	resp, out := h.post(t, "/api/quests", `{"metric":"vibes","target":5,"window":"week"}`)
	if resp.StatusCode != http.StatusBadRequest || out["error"] == "" {
		t.Errorf("unknown metric: status %d, body %v", resp.StatusCode, out)
	}
	resp, _ = h.post(t, "/api/quests", `{"metric":"revenue","target":0,"window":"week"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("zero target: status %d", resp.StatusCode)
	}

	// An explicit window is accepted as an object.
	resp, created = h.post(t, "/api/quests",
		`{"metric":"units","target":5,"window":{"start":"2026-01-01","end":"2026-01-31"}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("explicit window: status %d, body %v", resp.StatusCode, created)
	}
	explicit := created["quest"].(map[string]any)
	if explicit["window_start"] != "2026-01-01" || explicit["window_end"] != "2026-01-31" {
		t.Errorf("window = %v..%v", explicit["window_start"], explicit["window_end"])
	}

	// Delete works for custom quests, and 404s for anything unknown.
	resp, _ = h.do(t, http.MethodDelete, "/api/quests/"+explicit["id"].(string), "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	resp, _ = h.do(t, http.MethodDelete, "/api/quests/nope", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete unknown status = %d, want 404", resp.StatusCode)
	}
}

// TestQuestCompletionReachesTheFeed is the whole reward loop over HTTP: a
// quest that is already met pays a drop the feed can see.
func TestQuestCompletionReachesTheFeed(t *testing.T) {
	h := newQuestHarness(t)
	today := core.DayOf(time.Now().UTC())
	h.seedLedger(t, today, 30, 300, "as:today")

	if _, body := h.post(t, "/api/quests",
		`{"metric":"revenue","target":100,"window":"week","title":"Make $100"}`); body["quest"] == nil {
		t.Fatalf("create failed: %v", body)
	}
	if _, err := h.quests.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}

	_, body := h.get(t, "/api/drops")
	drops := body["drops"].([]any)
	if len(drops) == 0 {
		t.Fatal("no drop reached the feed")
	}
	drop := drops[0].(map[string]any)
	if drop["kind"] != "quest_complete" {
		t.Fatalf("newest drop = %v", drop)
	}
	if drop["rarity"] != "rare" || drop["title"] != "Quest complete: Make $100" {
		t.Errorf("drop = %v", drop)
	}

	_, board := h.get(t, "/api/quests")
	recent := board["recent"].([]any)
	if len(recent) != 1 {
		t.Fatalf("want one recent quest, got %d", len(recent))
	}
	done := recent[0].(map[string]any)
	if done["status"] != "completed" || done["xp"].(float64) != 200 {
		t.Errorf("completed quest = %v", done)
	}
}

func TestMysteriesAPI(t *testing.T) {
	ctx := context.Background()
	h := newQuestHarness(t)

	// A flat series with one enormous day in it, three days ago.
	now := time.Now().UTC()
	for i := 40; i >= 1; i-- {
		day := core.DayOf(now.AddDate(0, 0, -i))
		occurred, _ := time.Parse(core.DayLayout, day)
		qty := 100
		if i%2 == 0 {
			qty = 110
		}
		if i == 3 {
			qty = 900
		}
		if _, err := h.store.InsertEvent(ctx, core.Event{
			ID: core.NewID(), Source: "googleplay", Kind: "install", App: "Weather",
			Day: day, OccurredAt: occurred, ObservedAt: occurred, Quantity: qty,
			Silent: true, DedupeKey: "gp:inst:" + day,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	found, err := h.detector.Run(ctx)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("nothing detected")
	}

	resp, body := h.get(t, "/api/mysteries")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	open := body["open"].([]any)
	if len(open) != len(found) {
		t.Fatalf("open = %d, want %d", len(open), len(found))
	}
	first := open[0].(map[string]any)
	if first["kind"] != "spike" {
		t.Errorf("kind = %v", first["kind"])
	}
	if first["detail"] == nil {
		t.Error("a mystery without its sparkline cannot be drawn")
	}

	id := first["id"].(string)
	resp, solved := h.post(t, "/api/mysteries/"+id+"/solve", `{"note":"a newsletter linked us"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("solve status = %d: %v", resp.StatusCode, solved)
	}
	m := solved["mystery"].(map[string]any)
	if m["status"] != "solved" || m["note"] != "a newsletter linked us" {
		t.Fatalf("solved = %v", m)
	}

	// Solving pays a drop through the real pipeline.
	_, drops := h.get(t, "/api/drops")
	list := drops["drops"].([]any)
	if len(list) == 0 {
		t.Fatal("solving paid no drop")
	}
	drop := list[0].(map[string]any)
	if drop["kind"] != "mystery_solved" || drop["rarity"] != "uncommon" {
		t.Fatalf("drop = %v", drop)
	}

	// The notebook keeps it, and the open list no longer does.
	_, body = h.get(t, "/api/mysteries")
	for _, raw := range body["open"].([]any) {
		if raw.(map[string]any)["id"] == id {
			t.Error("a solved mystery is still open")
		}
	}
	resolved := body["resolved"].([]any)
	if len(resolved) != 1 || resolved[0].(map[string]any)["note"] != "a newsletter linked us" {
		t.Errorf("resolved = %v", resolved)
	}

	// Dismissal is quiet: no new drop.
	before := len(list)
	if len(body["open"].([]any)) > 0 {
		other := body["open"].([]any)[0].(map[string]any)["id"].(string)
		resp, _ = h.post(t, "/api/mysteries/"+other+"/dismiss", "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("dismiss status = %d", resp.StatusCode)
		}
		_, drops = h.get(t, "/api/drops")
		if len(drops["drops"].([]any)) != before {
			t.Error("dismissing a mystery made a drop")
		}
	}

	resp, _ = h.post(t, "/api/mysteries/nope/solve", `{"note":"x"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown mystery: status %d, want 404", resp.StatusCode)
	}
}

// TestQuestEndpointsWithoutServices proves the API is honest on a server built
// without Quest 5 wired in: empty, not broken.
func TestQuestEndpointsWithoutServices(t *testing.T) {
	h := newHarness(t, false)

	resp, body := h.get(t, "/api/quests")
	if resp.StatusCode != http.StatusOK || len(body["active"].([]any)) != 0 {
		t.Errorf("quests = %d %v", resp.StatusCode, body)
	}
	resp, body = h.get(t, "/api/mysteries")
	if resp.StatusCode != http.StatusOK || len(body["open"].([]any)) != 0 {
		t.Errorf("mysteries = %d %v", resp.StatusCode, body)
	}
	resp, _ = h.post(t, "/api/quests", `{"metric":"revenue","target":10,"window":"week"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("create without a service: status %d, want 503", resp.StatusCode)
	}
}
