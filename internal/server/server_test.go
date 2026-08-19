package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/sources/revenuecat"
	"github.com/nickhirras/loot/internal/store"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type harness struct {
	srv   *httptest.Server
	store *store.Store
	bus   *bus.Bus
}

func newHarness(t *testing.T, dev bool) *harness {
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

	b := bus.New(16)
	p := pipeline.New(st, engine, b, quietLogger())

	cfg := config.Default()
	cfg.Dev.Enabled = dev

	static := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("<!doctype html><title>Loot</title><div id=app></div>")},
		"assets/app.js":  &fstest.MapFile{Data: []byte("console.log('loot')")},
		"assets/app.css": &fstest.MapFile{Data: []byte("body{}")},
	}

	sources := []core.Source{revenuecat.New("", quietLogger())}
	s := server.New(cfg, st, b, p, sources, static, quietLogger())

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &harness{srv: ts, store: st, bus: b}
}

func (h *harness) get(t *testing.T, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := h.srv.Client().Get(h.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	var body map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp, body
}

func (h *harness) post(t *testing.T, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := h.srv.Client().Post(h.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp, out
}

const rcWebhook = `{"api_version":"1.0","event":{"id":"evt-abc","type":"INITIAL_PURCHASE",
  "app_id":"app1","product_id":"premium_monthly","period_type":"NORMAL","price":9.99,
  "currency":"USD","country_code":"NZ","event_timestamp_ms":1755500000000,
  "transaction_id":"txn-1"}}`

func TestWebhookIngestAndDedupe(t *testing.T) {
	h := newHarness(t, false)

	resp, body := h.post(t, "/hooks/revenuecat", rcWebhook)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["dedupe_key"] != "rc:evt-abc" {
		t.Fatalf("dedupe_key = %v", body["dedupe_key"])
	}

	_, drops := h.get(t, "/api/drops")
	list, _ := drops["drops"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d drops after one webhook, want 1", len(list))
	}

	// Redeliver the identical webhook: still 200, but no second drop.
	resp, _ = h.post(t, "/hooks/revenuecat", rcWebhook)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}

	_, drops = h.get(t, "/api/drops")
	list, _ = drops["drops"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d drops after a redelivery, want 1", len(list))
	}

	first, _ := list[0].(map[string]any)
	if first["source"] != "revenuecat" || first["country"] != "NZ" {
		t.Fatalf("drop is missing joined event fields: %v", first)
	}
	// NZ is the first country ever seen here, so the floor rule applies.
	if first["rarity"] != "rare" {
		t.Fatalf("rarity = %v, want rare", first["rarity"])
	}
}

func TestUnknownWebhookSource(t *testing.T) {
	h := newHarness(t, false)
	resp, _ := h.post(t, "/hooks/nope", "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStatsEndpoint(t *testing.T) {
	h := newHarness(t, true)
	h.post(t, "/hooks/revenuecat", rcWebhook)

	_, body := h.get(t, "/api/stats")
	if body["total_drops"].(float64) != 1 {
		t.Fatalf("total_drops = %v", body["total_drops"])
	}
	if body["total_xp"].(float64) <= 0 {
		t.Fatalf("total_xp = %v", body["total_xp"])
	}
	if body["dev"] != true {
		t.Fatalf("dev = %v, want true", body["dev"])
	}
	byRarity, _ := body["by_rarity"].(map[string]any)
	if byRarity["rare"].(float64) != 1 {
		t.Fatalf("by_rarity = %v", byRarity)
	}
	countries, _ := body["countries"].([]any)
	if len(countries) != 1 || countries[0] != "NZ" {
		t.Fatalf("countries = %v", countries)
	}
}

func TestSourcesEndpoint(t *testing.T) {
	h := newHarness(t, false)

	_, body := h.get(t, "/api/sources")
	list, _ := body["sources"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d sources, want 1", len(list))
	}
	src, _ := list[0].(map[string]any)
	if src["name"] != "revenuecat" || src["mode"] != "webhook" {
		t.Fatalf("source = %v", src)
	}
}

func TestDevFakeDisabledByDefault(t *testing.T) {
	h := newHarness(t, false)
	resp, _ := h.post(t, "/api/dev/fake", `{"rarity":"epic"}`)
	// With dev off the route is not registered, so the SPA fallback answers.
	if resp.StatusCode == http.StatusOK {
		_, drops := h.get(t, "/api/drops")
		list, _ := drops["drops"].([]any)
		if len(list) > 0 {
			t.Fatal("dev endpoint minted a drop while dev mode was disabled")
		}
	}
}

func TestDevFakeCreatesDrops(t *testing.T) {
	h := newHarness(t, true)

	for _, rarity := range core.Rarities {
		resp, body := h.post(t, "/api/dev/fake", `{"rarity":"`+string(rarity)+`"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d", rarity, resp.StatusCode)
		}
		drop, _ := body["drop"].(map[string]any)
		if drop["rarity"] != string(rarity) {
			t.Fatalf("%s: got rarity %v", rarity, drop["rarity"])
		}
	}

	_, body := h.get(t, "/api/drops")
	list, _ := body["drops"].([]any)
	if len(list) != len(core.Rarities) {
		t.Fatalf("got %d drops, want %d", len(list), len(core.Rarities))
	}
}

func TestDevFakeRepeatsAreNotDeduped(t *testing.T) {
	h := newHarness(t, true)

	h.post(t, "/api/dev/fake", `{"rarity":"common"}`)
	h.post(t, "/api/dev/fake", `{"rarity":"common"}`)

	_, body := h.get(t, "/api/drops")
	list, _ := body["drops"].([]any)
	if len(list) != 2 {
		t.Fatalf("got %d drops, want 2 (dev drops are always unique)", len(list))
	}
}

func TestDevFakeRejectsUnknownRarity(t *testing.T) {
	h := newHarness(t, true)
	resp, body := h.post(t, "/api/dev/fake", `{"rarity":"mythic"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Fatalf("body = %v", body)
	}
}

func TestDropsPagination(t *testing.T) {
	h := newHarness(t, true)
	for i := 0; i < 5; i++ {
		h.post(t, "/api/dev/fake", `{"rarity":"common"}`)
	}

	_, body := h.get(t, "/api/drops?limit=2")
	list, _ := body["drops"].([]any)
	if len(list) != 2 {
		t.Fatalf("limit=2 returned %d drops", len(list))
	}
	next, _ := body["next_before"].(string)
	if next == "" {
		t.Fatal("next_before was not set on a full page")
	}

	_, page2 := h.get(t, "/api/drops?limit=2&before="+next)
	list2, _ := page2["drops"].([]any)
	if len(list2) != 2 {
		t.Fatalf("second page returned %d drops", len(list2))
	}

	firstID := list[0].(map[string]any)["id"]
	for _, d := range list2 {
		if d.(map[string]any)["id"] == firstID {
			t.Fatal("pages overlap")
		}
	}
}

func TestSPAFallback(t *testing.T) {
	h := newHarness(t, false)

	t.Run("index", func(t *testing.T) {
		resp, _ := h.get(t, "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("asset is cached immutably", func(t *testing.T) {
		resp, _ := h.get(t, "/assets/app.js")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
			t.Fatalf("cache-control = %q", resp.Header.Get("Cache-Control"))
		}
	})

	t.Run("unknown path falls back to index", func(t *testing.T) {
		resp, err := h.srv.Client().Get(h.srv.URL + "/some/client/route")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "id=app") {
			t.Fatalf("did not serve index.html: %s", body)
		}
	})
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, false)
	resp, body := h.get(t, "/api/healthz")
	if resp.StatusCode != http.StatusOK || body["ok"] != true {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, body)
	}
}

// TestWebsocketStreamsDrops exercises the full path: connect, fire a dev drop,
// receive it on the socket.
func TestWebsocketStreamsDrops(t *testing.T) {
	h := newHarness(t, true)

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
	if hello.Type != "hello" {
		t.Fatalf("first message = %+v, want hello", hello)
	}

	h.post(t, "/api/dev/fake", `{"rarity":"legendary"}`)

	var msg bus.Message
	if err := readJSON(ctx, conn, &msg); err != nil {
		t.Fatalf("read drop: %v", err)
	}
	if msg.Type != "drop" || msg.Drop == nil {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Drop.Rarity != core.Legendary {
		t.Fatalf("rarity = %s, want legendary", msg.Drop.Rarity)
	}
	if msg.Event == nil || msg.Event.Source != "dev" {
		t.Fatalf("event = %+v", msg.Event)
	}
}
