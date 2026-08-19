package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

// bossHarness is the normal harness plus the boss service and the pipeline it
// mints its drops through.
type bossHarness struct {
	*harness
	bosses *bosses.Service
	pipe   *pipeline.Pipeline
	now    time.Time
}

func newBossHarness(t *testing.T) *bossHarness {
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

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := bosses.NewService(st, p, b, quietLogger())
	svc.Now = func() time.Time { return now }

	s := server.New(config.Default(), st, b, p, nil,
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, quietLogger())
	s.Bosses = svc
	svc.OnChange = s.InvalidateBosses

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &bossHarness{
		harness: &harness{srv: ts, store: st, bus: b},
		bosses:  svc, pipe: p, now: now,
	}
}

func (h *bossHarness) day(back int) string { return core.DayOf(h.now.AddDate(0, 0, -back)) }

func (h *bossHarness) crash(t *testing.T, day, version string, count, users int) {
	t.Helper()
	payload, err := json.Marshal(core.CrashPayload{
		Version: version, IssueTitle: "Boom", UsersAffected: users,
		Kind: core.BossKindCrash, URL: "https://example.test/i",
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	at, _ := time.Parse(core.DayLayout, day)
	if _, err := h.pipe.Ingest(context.Background(), core.Event{
		Source: "playvitals", Kind: core.KindCrash, App: "com.example.app",
		OccurredAt: at, ObservedAt: at, Day: day, Quantity: count,
		DedupeKey: "t:crash:" + day + ":" + version, Silent: true, Payload: payload,
	}); err != nil {
		t.Fatalf("ingest crash: %v", err)
	}
}

func (h *bossHarness) heartbeat(t *testing.T, day string) {
	t.Helper()
	at, _ := time.Parse(core.DayLayout, day)
	if _, err := h.pipe.Ingest(context.Background(), core.Event{
		Source: "playvitals", Kind: core.KindCrashDay, App: "com.example.app",
		OccurredAt: at, ObservedAt: at, Day: day,
		DedupeKey: "t:beat:" + day, Silent: true,
	}); err != nil {
		t.Fatalf("ingest heartbeat: %v", err)
	}
}

// spawn lays down enough history for one boss and evaluates.
func (h *bossHarness) spawn(t *testing.T) {
	t.Helper()
	for i := 40; i >= 4; i-- {
		h.heartbeat(t, h.day(i))
		h.crash(t, h.day(i), "1.0.0", 2, 1)
	}
	h.heartbeat(t, h.day(3))
	h.crash(t, h.day(3), "2.0.0", 300, 90)
	h.heartbeat(t, h.day(2))
	h.crash(t, h.day(2), "2.0.0", 180, 55)
	h.heartbeat(t, h.day(1))
	h.crash(t, h.day(1), "2.0.0", 120, 40)

	if _, err := h.bosses.Evaluate(context.Background()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
}

// An empty board answers "peace in the realm" rather than 404, so a UI built
// against it never has to ask whether this server has Quest 3 in it.
func TestBossesEmptyBoard(t *testing.T) {
	h := newBossHarness(t)
	resp, body := h.get(t, "/api/bosses")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, key := range []string{"alive", "recent"} {
		list, ok := body[key].([]any)
		if !ok {
			t.Fatalf("%s = %#v, want an array (never null)", key, body[key])
		}
		if len(list) != 0 {
			t.Errorf("%s has %d entries on an empty board", key, len(list))
		}
	}
}

func TestBossesBoardShape(t *testing.T) {
	h := newBossHarness(t)
	h.spawn(t)

	resp, body := h.get(t, "/api/bosses")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	alive, _ := body["alive"].([]any)
	if len(alive) != 1 {
		t.Fatalf("alive = %d, want 1", len(alive))
	}
	boss, _ := alive[0].(map[string]any)

	// Everything the card needs, present and computed server-side.
	for _, key := range []string{
		"id", "key", "source", "app", "name", "title", "version",
		"hp", "hp_max", "pct", "down_pct", "days_alive", "users_affected",
		"spawned_day", "status", "unit", "series", "enraged",
	} {
		if _, ok := boss[key]; !ok {
			t.Errorf("boss is missing %q", key)
		}
	}
	if boss["status"] != core.BossAlive {
		t.Errorf("status = %v, want alive", boss["status"])
	}
	if boss["hp"] != 120.0 || boss["hp_max"] != 300.0 {
		t.Errorf("hp/%v hp_max/%v, want 120/300", boss["hp"], boss["hp_max"])
	}
	if pct, _ := boss["pct"].(float64); pct < 0.39 || pct > 0.41 {
		t.Errorf("pct = %v, want ~0.4", pct)
	}
	series, _ := boss["series"].([]any)
	if len(series) != bosses.SeriesPoints {
		t.Errorf("series has %d points, want %d", len(series), bosses.SeriesPoints)
	}
	if name, _ := boss["name"].(string); name == "" {
		t.Error("boss has no name; the name is the whole feature")
	}
}

// The badge has to be able to turn red before the board itself is fetched.
func TestStatsCountsAliveBosses(t *testing.T) {
	h := newBossHarness(t)
	h.spawn(t)

	_, body := h.get(t, "/api/stats")
	n, ok := body["bosses_alive"].(float64)
	if !ok {
		t.Fatalf("bosses_alive = %#v, want a number", body["bosses_alive"])
	}
	if int(n) != 1 {
		t.Fatalf("bosses_alive = %v, want 1", n)
	}
}

func TestSlayEndpoint(t *testing.T) {
	h := newBossHarness(t)
	h.spawn(t)

	_, board := h.get(t, "/api/bosses")
	alive, _ := board["alive"].([]any)
	first, _ := alive[0].(map[string]any)
	id, _ := first["id"].(string)

	resp, body := h.post(t, "/api/bosses/"+id+"/slay", "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	boss, _ := body["boss"].(map[string]any)
	if boss["status"] != core.BossSlain {
		t.Fatalf("status = %v, want slain", boss["status"])
	}
	if xp, _ := boss["xp_awarded"].(float64); xp <= 0 {
		t.Errorf("xp_awarded = %v, want the kill's XP", xp)
	}

	// The memo must not outlive the kill that made it wrong.
	_, board = h.get(t, "/api/bosses")
	alive, _ = board["alive"].([]any)
	recent, _ := board["recent"].([]any)
	if len(alive) != 0 {
		t.Errorf("alive = %d immediately after the kill, want 0", len(alive))
	}
	if len(recent) != 1 {
		t.Errorf("recent = %d, want 1", len(recent))
	}
}

func TestSlayUnknownBoss(t *testing.T) {
	h := newBossHarness(t)
	resp, body := h.post(t, "/api/bosses/nope/slay", "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if body["error"] != "no such boss" {
		t.Errorf("error = %v", body["error"])
	}
}
