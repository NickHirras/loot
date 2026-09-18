package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// getLang is harness.get with an Accept-Language header.
func (h *harness) getLang(t *testing.T, path, accept string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if accept != "" {
		req.Header.Set("Accept-Language", accept)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return body
}

// titleOfRule returns the title of the one drop on a /api/drops page that the
// given rule wrote. The rule travels on the wire (it is what the server itself
// re-renders from), which makes it the sturdiest way to name a drop in a test
// whose language is the thing under test.
func titleOfRule(t *testing.T, body map[string]any, rule string) string {
	t.Helper()
	drops, ok := body["drops"].([]any)
	if !ok {
		t.Fatalf("body has no drops: %v", body)
	}
	found := ""
	seen := 0
	for _, d := range drops {
		m, _ := d.(map[string]any)
		if r, _ := m["rule"].(string); r != rule {
			continue
		}
		found, _ = m["title"].(string)
		seen++
	}
	if seen != 1 {
		t.Fatalf("found %d drops from rule %q, want exactly one", seen, rule)
	}
	return found
}

// titlesByID indexes a /api/drops page by drop id.
func titlesByID(t *testing.T, body map[string]any) map[string]string {
	t.Helper()
	drops, ok := body["drops"].([]any)
	if !ok {
		t.Fatalf("body has no drops: %v", body)
	}
	out := map[string]string{}
	for _, d := range drops {
		m, ok := d.(map[string]any)
		if !ok {
			t.Fatalf("drop is not an object: %v", d)
		}
		id, _ := m["id"].(string)
		title, _ := m["title"].(string)
		out[id] = title
	}
	return out
}

// legacyDrop stores a drop the way Loot did before it recorded which rule
// wrote it: no rule, no language, and so nothing to re-render from.
func legacyDrop(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ev := core.Event{
		ID: core.NewID(), Source: "revenuecat", Kind: "renewal", App: "Nistis",
		OccurredAt: now, ObservedAt: now, Day: core.DayOf(now), Country: "DE",
		Amount: 9.99, Currency: "USD", Quantity: 1, DedupeKey: "rc:legacy-1",
	}
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	d := core.Drop{
		ID: core.NewID(), EventID: ev.ID, Rarity: core.Common,
		Title: "Renewal", Subtitle: "9.99 USD · Nistis", XP: 10, CreatedAt: now,
	}
	if err := st.InsertDrop(ctx, d); err != nil {
		t.Fatalf("insert legacy drop: %v", err)
	}
	return d.ID
}

// A German reader sees the German sentence for a drop the default rules wrote,
// and the stored English for one that predates the rule being recorded.
func TestDropsAcceptLanguage(t *testing.T) {
	h := newHarness(t, false)

	legacyID := legacyDrop(t, h.store)
	// One webhook mints two drops: the purchase, and the settlement for a
	// country Loot had never seen. Both are default rules, so both translate.
	h.post(t, "/hooks/revenuecat", rcWebhook)

	english := h.getLang(t, "/api/drops", "")
	german := h.getLang(t, "/api/drops", "de-DE,de;q=0.9,en;q=0.5")

	if got := titleOfRule(t, english, "revenuecat-purchase"); got != "New subscriber" {
		t.Fatalf("english title = %q, want New subscriber", got)
	}
	if got := titleOfRule(t, german, "revenuecat-purchase"); got != "Neuer Abonnent" {
		t.Fatalf("german title = %q, want Neuer Abonnent", got)
	}
	if got := titleOfRule(t, german, "settlement"); got != "Neue Siedlung: 🇳🇿 NZ" {
		t.Fatalf("german settlement title = %q", got)
	}

	// The drop from before Loot recorded its rule keeps the sentence it was
	// written with, in every language.
	if got := titlesByID(t, german)[legacyID]; got != "Renewal" {
		t.Fatalf("legacy drop was rewritten to %q; it has no rule to re-render from", got)
	}

	// A language Loot has no overlay for is answered in English rather than in
	// the nearest thing it does have.
	french := h.getLang(t, "/api/drops", "xx-XX,xx;q=0.9")
	if got := titleOfRule(t, french, "revenuecat-purchase"); got != "New subscriber" {
		t.Fatalf("french title = %q, want the stored English", got)
	}
}

// The globe's arrivals ticker is prose too, and it is memoized — so it must be
// translated on the way out of the cache, never into it.
func TestHearthAcceptLanguage(t *testing.T) {
	h := newHarness(t, false)
	h.post(t, "/hooks/revenuecat", rcWebhook)

	// Two drops land in the same millisecond, so the ticker's order is not
	// worth asserting; what it says is.
	ticker := func(accept string) map[string]bool {
		t.Helper()
		body := h.getLang(t, "/api/hearth", accept)
		recent, ok := body["recent"].([]any)
		if !ok || len(recent) == 0 {
			t.Fatalf("hearth has no arrivals: %v", body["recent"])
		}
		titles := map[string]bool{}
		for _, row := range recent {
			m, _ := row.(map[string]any)
			title, _ := m["title"].(string)
			titles[title] = true
		}
		return titles
	}

	if got := ticker(""); !got["New subscriber"] {
		t.Fatalf("english ticker = %v", got)
	}
	// The second request is served from the memo, which must still be English
	// underneath it.
	if got := ticker("de"); !got["Neuer Abonnent"] || got["New subscriber"] {
		t.Fatalf("german ticker = %v", got)
	}
	if got := ticker(""); !got["New subscriber"] {
		t.Fatalf("the German reader left %v in the cache", got)
	}
}

// A socket says which language it wants on its URL, because the handshake is
// the one request the dashboard cannot put a header on.
func TestWebsocketLanguage(t *testing.T) {
	h := newHarness(t, false)

	wsURL := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws?lang=de"
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

	h.post(t, "/hooks/revenuecat", rcWebhook)

	var msg bus.Message
	if err := readJSON(ctx, conn, &msg); err != nil {
		t.Fatalf("read drop: %v", err)
	}
	if msg.Type != "drop" || msg.Drop == nil {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Drop.Title != "Neuer Abonnent" {
		t.Fatalf("socket title = %q, want Neuer Abonnent", msg.Drop.Title)
	}
	// The stored drop is still English: only this connection's copy changed.
	drops, err := h.store.ListDrops(context.Background(), store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	stored := ""
	for _, d := range drops {
		if d.Rule == "revenuecat-purchase" {
			stored = d.Title
		}
	}
	if stored != "New subscriber" {
		t.Fatalf("stored title = %q, want the English original", stored)
	}
}

// A Loot configured for one language answers every request in it, whatever the
// browser asks for: `language: de` means "this dashboard is in German", not
// "German is a preference to weigh".
func TestConfiguredLanguageWins(t *testing.T) {
	cfg := config.Default()
	cfg.Language = "de"
	h := newHarnessWith(t, cfg, fstest.MapFS{})

	h.post(t, "/hooks/revenuecat", rcWebhook)

	for _, accept := range []string{"", "en-US,en;q=0.9", "xx-XX"} {
		body := h.getLang(t, "/api/drops", accept)
		if got := titleOfRule(t, body, "revenuecat-purchase"); got != "Neuer Abonnent" {
			t.Fatalf("Accept-Language %q gave %q, want German", accept, got)
		}
		if got := titleOfRule(t, body, "settlement"); got != "Neue Siedlung: 🇳🇿 NZ" {
			t.Fatalf("Accept-Language %q gave %q, want German", accept, got)
		}
	}
}

// The negotiation itself, read off the wire: the tag, then the bare language,
// then any variant of the same language — the same three steps
// web/src/lib/locale.ts takes, so that the feed a tab fetches is in the
// language the page around it decided on.
func TestRequestLangNegotiation(t *testing.T) {
	h := newHarness(t, false)
	h.post(t, "/hooks/revenuecat", rcWebhook)

	const (
		english = "New subscriber"
		german  = "Neuer Abonnent"
	)
	cases := []struct {
		name   string
		accept string
		want   string
	}{
		{"no header", "", english},
		{"exact tag", "de", german},
		{"a variant of a language we have", "de-AT", german},
		{"a language we do not have", "xx-XX,xx;q=0.9", english},
		{"the best available, not the best asked for", "xx;q=0.9,de;q=0.8", german},
		{"weights are honoured", "de;q=0.2,xx;q=0.9", german},
		{"a wildcard is already English", "*", english},
		{"an explicit refusal", "de;q=0,en;q=0.9", english},
		{"nonsense", ";;;", english},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := h.getLang(t, "/api/drops", tc.accept)
			if got := titleOfRule(t, body, "revenuecat-purchase"); got != tc.want {
				t.Fatalf("Accept-Language %q gave %q, want %q", tc.accept, got, tc.want)
			}
		})
	}
}
