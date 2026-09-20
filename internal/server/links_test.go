package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// seedDrop stores one event and its drop directly, so a test can ask what
// /api/drops says about a source this harness does not wire up. It returns
// the drop id.
func seedDrop(t *testing.T, st *store.Store, source, kind, app, payload string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	ev := core.Event{
		ID: core.NewID(), Source: source, Kind: kind, App: app,
		OccurredAt: now, ObservedAt: now, Day: core.DayOf(now),
		DedupeKey: source + ":" + core.NewID(),
		Payload:   json.RawMessage(payload),
	}
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	d := core.Drop{
		ID: core.NewID(), EventID: ev.ID, Rarity: core.Rare,
		Title: "Something happened", XP: 100, CreatedAt: now,
	}
	if err := st.InsertDrop(ctx, d); err != nil {
		t.Fatalf("insert drop: %v", err)
	}
	return d.ID
}

// linksByID pulls {drop id: link} out of a /api/drops response. A drop with
// no link is present with "", because `link` is omitempty and its absence is
// exactly what several of these tests are asserting.
func linksByID(t *testing.T, body map[string]any) map[string]string {
	t.Helper()
	list, _ := body["drops"].([]any)
	out := make(map[string]string, len(list))
	for _, d := range list {
		m, ok := d.(map[string]any)
		if !ok {
			t.Fatalf("drop is not an object: %v", d)
		}
		id, _ := m["id"].(string)
		link, _ := m["link"].(string)
		out[id] = link
	}
	return out
}

func demoHarness(t *testing.T) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Demo.Enabled = true
	return newHarnessWith(t, cfg, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Loot</title><div id=app></div>")},
	})
}

// The feed tells a client where each drop came from, worked out from the
// event on the way out — nothing about this was stored when the drop was
// minted.
func TestDropsCarryTheirLink(t *testing.T) {
	h := newHarness(t, false)

	issue := seedDrop(t, h.store, "github", "issue_opened", "nickhirras/loot",
		`{"number":7,"title":"It crashes","url":"https://github.com/nickhirras/loot/issues/7"}`)
	star := seedDrop(t, h.store, "github", "star", "nickhirras/loot", `{"user":"octocat"}`)
	quest := seedDrop(t, h.store, "loot", "quest_complete", "Sprocket", `{"quest_id":"q1"}`)
	quiet := seedDrop(t, h.store, "revenuecat", "renewal", "Sprocket", `{"event":{"type":"RENEWAL"}}`)

	_, body := h.get(t, "/api/drops")
	got := linksByID(t, body)

	want := map[string]string{
		issue: "https://github.com/nickhirras/loot/issues/7",
		star:  "https://github.com/nickhirras/loot/stargazers",
		quest: "#/quests",
		quiet: "",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("link = %q, want %q", got[id], w)
		}
	}
}

// A payload url is attacker-controlled on every webhook source, so anything
// that is not http(s) must not reach the client at all.
func TestDropsRefuseAnUnsafePayloadURL(t *testing.T) {
	h := newHarness(t, false)

	// The generic webhook has no fallback of its own, so "no link" here is
	// unambiguous.
	hostile := seedDrop(t, h.store, "webhook", "ci_green", "Sprocket",
		`{"title":"Build passed","url":"javascript:alert(document.cookie)"}`)
	// GitHub does have a fallback, and must land on it rather than on the
	// payload's word.
	repo := seedDrop(t, h.store, "github", "issue_opened", "nickhirras/loot",
		`{"number":7,"url":"javascript:alert(1)"}`)

	_, body := h.get(t, "/api/drops")
	got := linksByID(t, body)

	if got[hostile] != "" {
		t.Errorf("link = %q, want no link at all for a javascript: payload url", got[hostile])
	}
	if got[repo] != "https://github.com/nickhirras/loot" {
		t.Errorf("link = %q, want the repo fallback", got[repo])
	}
}

// Demo mode's apps and repositories are invented, so an external link would
// be a confident 404 on somebody else's site. Its own tabs are real, and
// stay clickable.
func TestDemoSuppressesExternalLinks(t *testing.T) {
	h := demoHarness(t)

	issue := seedDrop(t, h.store, "github", "issue_opened", "nickhirras/loot",
		`{"url":"https://github.com/nickhirras/loot/issues/7"}`)
	storePage := seedDrop(t, h.store, "flathub", "installs_day", "org.gnome.Podcasts", `{"installs":9}`)
	quest := seedDrop(t, h.store, "loot", "quest_complete", "Sprocket", `{"quest_id":"q1"}`)
	achievement := seedDrop(t, h.store, "loot", core.KindAchievement, "", `{"key":"first_blood"}`)

	_, body := h.get(t, "/api/drops")
	got := linksByID(t, body)

	if got[issue] != "" {
		t.Errorf("demo link = %q, want none for an external issue url", got[issue])
	}
	if got[storePage] != "" {
		t.Errorf("demo link = %q, want none for a store page", got[storePage])
	}
	if got[quest] != "#/quests" {
		t.Errorf("demo link = %q, want #/quests — in-app links still work in the demo", got[quest])
	}
	if got[achievement] != "#/codex" {
		t.Errorf("demo link = %q, want #/codex", got[achievement])
	}
}

// A live drop links too — and the copy on the bus, which every other
// connection and `loot tail` are still holding, is left exactly as it was.
func TestWebsocketDropCarriesItsLink(t *testing.T) {
	h := newHarness(t, false)

	wsURL := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocketDial(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	var hello bus.Message
	if err := readJSON(ctx, conn, &hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}

	drop := core.Drop{ID: core.NewID(), Rarity: core.Epic, Title: "v1.2.0 is out", XP: 300}
	event := core.Event{
		Source: "github", Kind: "release", App: "nickhirras/loot",
		Payload: json.RawMessage(`{"url":"https://github.com/nickhirras/loot/releases/tag/v1.2.0"}`),
	}
	h.bus.Publish(bus.Message{Type: "drop", Drop: &drop, Event: &event})

	var msg bus.Message
	if err := readJSON(ctx, conn, &msg); err != nil {
		t.Fatalf("read drop: %v", err)
	}
	if msg.Drop == nil {
		t.Fatalf("message = %+v", msg)
	}
	if want := "https://github.com/nickhirras/loot/releases/tag/v1.2.0"; msg.Drop.Link != want {
		t.Errorf("socket link = %q, want %q", msg.Drop.Link, want)
	}
	if drop.Link != "" {
		t.Errorf("the published drop was mutated: Link = %q", drop.Link)
	}
}

// The chest cascade is drops leaving by a different door; they link too.
func TestChestOpenDropsCarryTheirLink(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	now := time.Now().UTC()
	day := core.DayOf(now.AddDate(0, 0, -1))

	ev := core.Event{
		ID: core.NewID(), Source: "github", Kind: "release", App: "nickhirras/loot",
		OccurredAt: now, ObservedAt: now, Day: day, DedupeKey: "github:release:test",
		Payload: json.RawMessage(`{"tag":"v1.2.0","url":"https://github.com/nickhirras/loot/releases/tag/v1.2.0"}`),
	}
	if _, err := h.store.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	d := core.Drop{
		ID: core.NewID(), EventID: ev.ID, Rarity: core.Epic, Title: "v1.2.0 is out",
		XP: 300, CreatedAt: now, ChestDate: day,
	}
	if err := h.store.InsertDrop(ctx, d); err != nil {
		t.Fatalf("insert drop: %v", err)
	}

	_, body := h.post(t, "/api/chest/open", `{"date":"`+day+`"}`)
	got := linksByID(t, body)
	if got[d.ID] != "https://github.com/nickhirras/loot/releases/tag/v1.2.0" {
		t.Errorf("chest drop link = %q, want the release url", got[d.ID])
	}
}
