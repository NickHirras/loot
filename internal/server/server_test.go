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

// fixedRates is a hand-written rate table: rate[X] is units of X per one USD.
type fixedRates map[string]float64

func (f fixedRates) Convert(amount float64, from, to string) (float64, bool) {
	if from == to {
		return amount, true
	}
	rate := func(cur string) (float64, bool) {
		if cur == "USD" {
			return 1, true
		}
		r, ok := f[cur]
		return r, ok
	}
	rf, okFrom := rate(from)
	rt, okTo := rate(to)
	if !okFrom || !okTo {
		return 0, false
	}
	return amount / rf * rt, true
}

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

	b := bus.New(64)
	p := pipeline.New(st, engine, b, quietLogger())
	p.ChestEnabled = true
	p.FX = fixedRates{"EUR": 0.8}

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

	// The purchase drop plus the settlement drop for a brand new country.
	_, drops := h.get(t, "/api/drops")
	list, _ := drops["drops"].([]any)
	if len(list) != 2 {
		t.Fatalf("got %d drops after one webhook, want 2 (purchase + settlement)", len(list))
	}

	// Redeliver the identical webhook: still 200, but no second drop.
	resp, _ = h.post(t, "/hooks/revenuecat", rcWebhook)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}

	_, drops = h.get(t, "/api/drops")
	list, _ = drops["drops"].([]any)
	if len(list) != 2 {
		t.Fatalf("got %d drops after a redelivery, want 2", len(list))
	}

	var purchase, settlement map[string]any
	for _, d := range list {
		m, _ := d.(map[string]any)
		switch m["kind"] {
		case "purchase":
			purchase = m
		case "settlement":
			settlement = m
		}
	}
	if purchase == nil || settlement == nil {
		t.Fatalf("drops = %v, want a purchase and a settlement", list)
	}
	if purchase["source"] != "revenuecat" || purchase["country"] != "NZ" {
		t.Fatalf("drop is missing joined event fields: %v", purchase)
	}
	if purchase["rarity"] != "uncommon" {
		t.Fatalf("rarity = %v, want uncommon", purchase["rarity"])
	}
	if settlement["source"] != "loot" || settlement["rarity"] != "rare" {
		t.Fatalf("settlement = %v, want a rare loot settlement", settlement)
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
	if body["total_drops"].(float64) != 2 {
		t.Fatalf("total_drops = %v", body["total_drops"])
	}
	if body["unrevealed_count"].(float64) != 0 {
		t.Fatalf("unrevealed_count = %v", body["unrevealed_count"])
	}
	if body["display_currency"] != "USD" {
		t.Fatalf("display_currency = %v", body["display_currency"])
	}
	if body["total_xp"].(float64) <= 0 {
		t.Fatalf("total_xp = %v", body["total_xp"])
	}
	if body["dev"] != true {
		t.Fatalf("dev = %v, want true", body["dev"])
	}
	byRarity, _ := body["by_rarity"].(map[string]any)
	if byRarity["rare"].(float64) != 1 || byRarity["uncommon"].(float64) != 1 {
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

func TestChestEndpoints(t *testing.T) {
	h := newHarness(t, true)

	// Two chest-bound days, minted through the real pipeline.
	h.post(t, "/api/dev/fake", `{"rarity":"epic","chest":true,"day":"2026-08-17"}`)
	h.post(t, "/api/dev/fake", `{"rarity":"common","chest":true,"day":"2026-08-17"}`)
	h.post(t, "/api/dev/fake", `{"rarity":"rare","chest":true,"day":"2026-08-16"}`)

	// Nothing held may appear in the feed.
	_, drops := h.get(t, "/api/drops")
	if list, _ := drops["drops"].([]any); len(list) != 0 {
		t.Fatalf("feed showed %d unopened chest drops", len(list))
	}

	_, body := h.get(t, "/api/chest")
	chests, _ := body["chests"].([]any)
	if len(chests) != 2 {
		t.Fatalf("got %d chests, want 2", len(chests))
	}
	oldest, _ := chests[0].(map[string]any)
	if oldest["date"] != "2026-08-16" || oldest["count"].(float64) != 1 {
		t.Fatalf("oldest chest = %v", oldest)
	}
	newest, _ := chests[1].(map[string]any)
	if newest["count"].(float64) != 2 || newest["xp"].(float64) <= 0 {
		t.Fatalf("newest chest = %v", newest)
	}
	byRarity, _ := newest["by_rarity"].(map[string]any)
	if byRarity["epic"].(float64) != 1 {
		t.Fatalf("by_rarity = %v", byRarity)
	}

	// Opening with no date opens the oldest.
	resp, opened := h.post(t, "/api/chest/open", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open status = %d", resp.StatusCode)
	}
	if opened["opened"] != "2026-08-16" || opened["count"].(float64) != 1 {
		t.Fatalf("open response = %v", opened)
	}

	// Opening a named chest returns its drops in cascade order.
	_, opened = h.post(t, "/api/chest/open", `{"date":"2026-08-17"}`)
	list, _ := opened["drops"].([]any)
	if len(list) != 2 {
		t.Fatalf("opened %d drops, want 2", len(list))
	}
	first, _ := list[0].(map[string]any)
	last, _ := list[1].(map[string]any)
	if first["rarity"] != "common" || last["rarity"] != "epic" {
		t.Fatalf("cascade order = %v then %v, want common then epic", first["rarity"], last["rarity"])
	}
	if remaining, _ := opened["chests"].([]any); len(remaining) != 0 {
		t.Fatalf("chests remaining = %v, want none", remaining)
	}

	// Revealed drops now show up in the feed.
	_, drops = h.get(t, "/api/drops")
	if list, _ := drops["drops"].([]any); len(list) != 3 {
		t.Fatalf("feed has %d drops after opening, want 3", len(list))
	}

	// Opening an empty chest is a quiet no-op, not an error.
	resp, empty := h.post(t, "/api/chest/open", `{"date":"2026-08-17"}`)
	if resp.StatusCode != http.StatusOK || empty["count"].(float64) != 0 {
		t.Fatalf("reopening returned %d / %v", resp.StatusCode, empty)
	}

	resp, _ = h.post(t, "/api/chest/open", `{"date":"nonsense"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad date status = %d, want 400", resp.StatusCode)
	}
}

func TestVaultSummaryEndpoint(t *testing.T) {
	h := newHarness(t, true)

	// A ledger day in euros: the vault reports it in the display currency.
	h.post(t, "/api/dev/fake", `{"kind":"sales_day","amount":80,"currency":"EUR","quantity":12,"country":"DE"}`)

	resp, body := h.get(t, "/api/vault/summary?range=7d")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["display_currency"] != "USD" {
		t.Fatalf("display_currency = %v", body["display_currency"])
	}
	rng, _ := body["range"].(map[string]any)
	if rng["days"].(float64) != 7 {
		t.Fatalf("range = %v", rng)
	}
	series, _ := body["series"].([]any)
	if len(series) != 7 {
		t.Fatalf("series has %d points, want 7", len(series))
	}

	// The euro row converts: 80 EUR at 0.8 to the dollar is 100 USD. The
	// sales_day summary rolls up that same row and must not be added again.
	totals, _ := body["totals"].(map[string]any)
	if totals["revenue_base"].(float64) != 100 {
		t.Fatalf("revenue = %v, want 100 (80 EUR converted, counted once)", totals["revenue_base"])
	}
	if totals["units"].(float64) != 12 {
		t.Fatalf("units = %v, want 12", totals["units"])
	}
	// Nothing is visible yet: the summary drop is shut inside its chest, and
	// so is the settlement, because the row that revealed the country was a
	// silent ledger row — backfilled history, not live news.
	if totals["drops"].(float64) != 0 {
		t.Fatalf("drops = %v, want none while the chest is shut", totals["drops"])
	}

	h.post(t, "/api/chest/open", `{}`)
	_, body = h.get(t, "/api/vault/summary?range=7d")
	totals, _ = body["totals"].(map[string]any)
	if totals["drops"].(float64) != 2 {
		t.Fatalf("drops = %v after opening the chest, want the summary and its settlement", totals["drops"])
	}

	bySource, _ := body["by_source"].([]any)
	if len(bySource) != 1 {
		t.Fatalf("by_source = %v", bySource)
	}
	src, _ := bySource[0].(map[string]any)
	if src["source"] != "dev" || src["share"].(float64) != 1 {
		t.Fatalf("by_source[0] = %v", src)
	}
	byCountry, _ := body["by_country"].([]any)
	if len(byCountry) != 1 || byCountry[0].(map[string]any)["country"] != "DE" {
		t.Fatalf("by_country = %v", byCountry)
	}

	realtime, _ := body["realtime"].(map[string]any)
	if realtime["revenuecat_purchases_today"].(float64) != 0 {
		t.Fatalf("realtime = %v", realtime)
	}
	subs, _ := body["subscriptions"].(map[string]any)
	if subs["active"] != nil {
		t.Fatalf("subscriptions = %v, want nulls", subs)
	}

	resp, _ = h.get(t, "/api/vault/summary?range=decade")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown range status = %d, want 400", resp.StatusCode)
	}
}

func TestDevFakeSalesDayLandsInAChest(t *testing.T) {
	h := newHarness(t, true)

	_, body := h.post(t, "/api/dev/fake", `{"kind":"sales_day","amount":250,"currency":"USD","quantity":40}`)
	drop, _ := body["drop"].(map[string]any)
	if drop["chest_date"] == "" || drop["chest_date"] == nil {
		t.Fatalf("sales_day drop was not filed into a chest: %v", drop)
	}
	if drop["rarity"] != "rare" {
		t.Fatalf("rarity = %v, want rare for a 250 dollar day", drop["rarity"])
	}
	event, _ := body["event"].(map[string]any)
	if event["is_ledger"] != true || event["chest"] != true {
		t.Fatalf("event = %v, want a ledger chest event", event)
	}

	_, chest := h.get(t, "/api/chest")
	if chests, _ := chest["chests"].([]any); len(chests) != 1 {
		t.Fatalf("got %d chests, want 1", len(chests))
	}
}

func TestHearthEndpoint(t *testing.T) {
	h := newHarness(t, true)

	// Two ledger days from two countries, one of them twice as large.
	h.post(t, "/api/dev/fake", `{"kind":"sales_day","amount":80,"currency":"EUR","quantity":200,"country":"DE"}`)
	h.post(t, "/api/dev/fake", `{"kind":"sales_day","amount":40,"currency":"EUR","quantity":100,"country":"JP"}`)
	// A realtime purchase with no country at all: unknown lands.
	h.post(t, "/api/dev/fake", `{"rarity":"uncommon"}`)

	resp, body := h.get(t, "/api/hearth")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["capital"] != "DE" {
		t.Fatalf("capital = %v, want DE (the biggest settlement)", body["capital"])
	}
	if body["display_currency"] != "USD" {
		t.Fatalf("display_currency = %v", body["display_currency"])
	}

	countries, _ := body["countries"].([]any)
	if len(countries) != 2 {
		t.Fatalf("countries = %v, want DE and JP", countries)
	}
	de, _ := countries[0].(map[string]any)
	if de["country"] != "DE" || de["population"].(float64) != 200 {
		t.Fatalf("first settlement = %v, want DE with 200 people", de)
	}
	// 80 EUR at 0.8 to the dollar; the sales_day rollup must not double it.
	if de["revenue_base"].(float64) != 100 {
		t.Fatalf("DE revenue = %v, want 100", de["revenue_base"])
	}
	deTier, _ := de["tier"].(map[string]any)
	if deTier["name"] != "metropolis" {
		t.Fatalf("DE tier = %v, want metropolis", deTier)
	}
	jp, _ := countries[1].(map[string]any)
	jpTier, _ := jp["tier"].(map[string]any)
	if jp["share"].(float64) != 0.5 || jpTier["name"] != "metropolis" {
		t.Fatalf("JP = %v, want half of DE and still a metropolis", jp)
	}

	era, _ := body["era"].(map[string]any)
	if era["name"] != "Camp" || era["next_name"] != "Village" {
		t.Fatalf("era = %v, want Camp heading for Village", era)
	}
	if tiers, _ := body["tiers"].([]any); len(tiers) != len(core.Tiers) {
		t.Fatalf("tier ladder = %v", body["tiers"])
	}

	// Both settlements were found in silent ledger rows, so they are waiting
	// in today's chest rather than on the arrivals ticker.
	if recent, _ := body["recent"].([]any); len(recent) != 0 {
		t.Fatalf("recent = %d drops, want none while the chest is shut", len(recent))
	}

	// They are really there: today's chest holds the two settlements and the
	// two sales days. (The Hearth aggregate is memoized for a few seconds, so
	// this asks the chest rather than re-reading the globe.)
	_, opened := h.post(t, "/api/chest/open", `{}`)
	if n, _ := opened["count"].(float64); n != 4 {
		t.Fatalf("chest held %v drops, want the two settlements and the two sales days", opened["count"])
	}
}

// An unknown path under /api/ or /hooks/ is a missing endpoint, not a page of
// the app. Falling through to the SPA handed a fetch() a chunk of HTML and the
// caller reported it as a JSON parse error somewhere else entirely.
func TestUnknownAPIPathsAnswer404JSON(t *testing.T) {
	h := newHarness(t, false)

	for _, path := range []string{"/api/nope", "/api/vault/nope", "/hooks/nope/deeper"} {
		resp, body := h.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s content-type = %q, want JSON", path, ct)
		}
		if body["error"] == nil {
			t.Errorf("GET %s body = %v, want an error message", path, body)
		}
	}

	// A real app route still gets the app (or, with no frontend embedded, the
	// "not built" page) rather than a 404.
	resp, _ := h.get(t, "/vault")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /vault = %d, want the SPA", resp.StatusCode)
	}
}

// The page-size ceiling has to be applied where next_before is decided. Asking
// for more than the maximum returned a full page that did not equal the
// requested limit, so no cursor was issued and the feed stopped after one page.
func TestDropsLimitIsClampedConsistently(t *testing.T) {
	h := newHarness(t, true)

	// Three drops, then ask for a page of two: there is another page.
	for i := 0; i < 3; i++ {
		h.post(t, "/api/dev/fake", `{"rarity":"common"}`)
	}
	_, body := h.get(t, "/api/drops?limit=2")
	drops, _ := body["drops"].([]any)
	if len(drops) != 2 {
		t.Fatalf("limit=2 returned %d drops", len(drops))
	}
	if body["next_before"] == "" {
		t.Error("a full page issued no next_before cursor")
	}

	// An absurd limit is clamped, and because the handler clamps to the same
	// number the query does, a short page correctly reports no next page.
	_, body = h.get(t, "/api/drops?limit=100000")
	drops, _ = body["drops"].([]any)
	if len(drops) != 3 {
		t.Fatalf("limit=100000 returned %d drops, want all 3", len(drops))
	}
	if body["next_before"] != "" {
		t.Errorf("next_before = %v on a page shorter than the clamped limit", body["next_before"])
	}
}

// Opening a chest that is already open still has to say what is left waiting:
// that is exactly the case where the caller's badge is out of date.
func TestChestOpenAlwaysReportsWhatIsLeft(t *testing.T) {
	h := newHarness(t, true)

	h.post(t, "/api/dev/fake", `{"kind":"sales_day","day":"2026-08-16","amount":10,"currency":"USD","quantity":2}`)
	h.post(t, "/api/dev/fake", `{"kind":"sales_day","day":"2026-08-17","amount":20,"currency":"USD","quantity":4}`)

	_, opened := h.post(t, "/api/chest/open", `{"date":"2026-08-16"}`)
	if opened["opened"] != "2026-08-16" {
		t.Fatalf("opened = %v", opened["opened"])
	}
	if chests, _ := opened["chests"].([]any); len(chests) != 1 {
		t.Fatalf("chests = %v, want the 17th still waiting", opened["chests"])
	}

	// Open it again: nothing comes out, but the response still describes the
	// world — including the chest that is genuinely still shut.
	_, again := h.post(t, "/api/chest/open", `{"date":"2026-08-16"}`)
	if again["count"].(float64) != 0 {
		t.Errorf("re-opening handed out %v drops", again["count"])
	}
	if again["opened"] != "" {
		t.Errorf("opened = %v, want empty", again["opened"])
	}
	chests, ok := again["chests"].([]any)
	if !ok {
		t.Fatalf("the response left `chests` out entirely: %v", again)
	}
	if len(chests) != 1 {
		t.Fatalf("chests = %v, want the 17th still waiting", chests)
	}
	if drops, ok := again["drops"].([]any); !ok || len(drops) != 0 {
		t.Errorf("drops = %v, want an empty list", again["drops"])
	}
}
