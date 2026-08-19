package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
)

var testNow = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

func newSource(t *testing.T, secret string) *Source {
	t.Helper()
	s, err := New(config.Webhook{Enabled: true, Secret: secret}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Now = func() time.Time { return testNow }
	return s
}

// post delivers a body and returns the recorder plus whatever was emitted.
func post(t *testing.T, s *Source, body, auth string) (*httptest.ResponseRecorder, []core.Event) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()

	var got []core.Event
	s.HandleWebhook(rec, req, func(e core.Event) { got = append(got, e) })
	return rec, got
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body)
	}
	return m
}

func payloadOf(t *testing.T, e core.Event) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------- auth

func TestAuth(t *testing.T) {
	body := `{"kind":"sale"}`

	t.Run("no secret accepts anything", func(t *testing.T) {
		rec, events := post(t, newSource(t, ""), body, "")
		if rec.Code != http.StatusOK || len(events) != 1 {
			t.Fatalf("got %d (%d events)", rec.Code, len(events))
		}
	})

	s := newSource(t, "hunter2")
	for name, auth := range map[string]string{
		"missing":      "",
		"wrong":        "Bearer nope",
		"bare wrong":   "nope",
		"other scheme": "Basic hunter2",
	} {
		rec, events := post(t, s, body, auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s auth: got %d, want 401", name, rec.Code)
		}
		if len(events) != 0 {
			t.Errorf("%s auth: emitted %d events despite 401", name, len(events))
		}
	}
	for _, auth := range []string{"Bearer hunter2", "hunter2"} {
		rec, events := post(t, s, body, auth)
		if rec.Code != http.StatusOK || len(events) != 1 {
			t.Errorf("auth %q: got %d (%d events)", auth, rec.Code, len(events))
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := newSource(t, "")
	req := httptest.NewRequest(http.MethodGet, "/hooks/webhook", nil)
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req, func(core.Event) { t.Fatal("GET emitted an event") })
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", rec.Code)
	}
}

// -------------------------------------------------------------- single drops

func TestSingleDropFullBody(t *testing.T) {
	s := newSource(t, "")
	body := `{
	  "kind":"sale","app":"My App","country":"de","amount":4.99,"currency":"eur",
	  "quantity":2,"occurred_at":"2026-08-18T09:30:00Z","id":"inv-1001",
	  "rarity":"epic","title":"First customer!","subtitle":"Berlin",
	  "ledger":true,"chest":true,
	  "payload":{"plan":"pro","seats":3}
	}`

	rec, events := post(t, s, body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decodeResponse(t, rec); got["ok"] != true || got["accepted"] != float64(1) {
		t.Fatalf("response = %v", got)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}

	e := events[0]
	if e.Source != "webhook" || e.Kind != "sale" || e.App != "My App" {
		t.Errorf("identity wrong: %+v", e)
	}
	if e.Country != "DE" {
		t.Errorf("country = %q, want the upper-cased DE", e.Country)
	}
	if e.Amount != 4.99 || e.Currency != "EUR" || e.Quantity != 2 {
		t.Errorf("money wrong: %v %v %v", e.Amount, e.Currency, e.Quantity)
	}
	if !e.OccurredAt.Equal(time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("OccurredAt = %s", e.OccurredAt)
	}
	if e.DedupeKey != "webhook:inv-1001" {
		t.Errorf("DedupeKey = %q", e.DedupeKey)
	}
	if !e.IsLedger {
		t.Error("ledger:true did not set IsLedger, so the vault would not count it")
	}
	if !e.Chest {
		t.Error("chest:true did not set Chest")
	}
	if e.Silent {
		t.Error("a posted drop should never be silent")
	}

	p := payloadOf(t, e)
	// The rarity rules read these three off the top level of the payload.
	if p["rarity"] != "epic" || p["title"] != "First customer!" || p["subtitle"] != "Berlin" {
		t.Errorf("payload top level = %v", p)
	}
	if p["plan"] != "pro" || p["seats"] != float64(3) {
		t.Errorf("extra payload was not merged: %v", p)
	}
}

func TestDefaults(t *testing.T) {
	s := newSource(t, "")
	_, events := post(t, s, `{"kind":"ci_green"}`, "")
	if len(events) != 1 {
		t.Fatalf("emitted %d events", len(events))
	}
	e := events[0]
	if !e.OccurredAt.Equal(testNow) {
		t.Errorf("OccurredAt = %s, want now (%s)", e.OccurredAt, testNow)
	}
	if e.Quantity != 1 {
		t.Errorf("Quantity = %d, want the default 1", e.Quantity)
	}
	if e.IsLedger || e.Chest || e.Country != "" || e.Currency != "" || e.Amount != 0 {
		t.Errorf("unexpected non-zero defaults: %+v", e)
	}
	if !strings.HasPrefix(e.DedupeKey, "webhook:sha256:") {
		t.Errorf("DedupeKey = %q, want a content hash", e.DedupeKey)
	}
	if len(payloadOf(t, e)) != 0 {
		t.Errorf("payload = %s, want an empty object", e.Payload)
	}
	// Quantity 0 must be respected, not defaulted away.
	_, zero := post(t, s, `{"kind":"ci_green","quantity":0}`, "")
	if zero[0].Quantity != 0 {
		t.Errorf("explicit quantity 0 became %d", zero[0].Quantity)
	}
}

func TestIDlessPostsAreAlwaysNewDrops(t *testing.T) {
	s := newSource(t, "")
	// Two identical bodies, posted at the same frozen instant: without an id,
	// each one is its own drop rather than a swallowed duplicate.
	_, first := post(t, s, `{"kind":"sale"}`, "")
	_, second := post(t, s, `{"kind":"sale"}`, "")
	if first[0].DedupeKey == second[0].DedupeKey {
		t.Fatalf("both posts got %q; a second real sale would vanish", first[0].DedupeKey)
	}

	// With an id, the key is stable, which is how a retry collapses.
	_, a := post(t, s, `{"kind":"sale","id":"abc"}`, "")
	_, b := post(t, s, `{"kind":"sale","id":"abc"}`, "")
	if a[0].DedupeKey != "webhook:abc" || b[0].DedupeKey != "webhook:abc" {
		t.Fatalf("keys = %q and %q, want webhook:abc twice", a[0].DedupeKey, b[0].DedupeKey)
	}
}

// --------------------------------------------------------------------- batch

func TestBatch(t *testing.T) {
	s := newSource(t, "")
	body := `[
	  {"kind":"sale","app":"A","id":"one"},
	  {"kind":"sale","app":"B","amount":9,"currency":"USD"},
	  {"kind":"signup","app":"A","rarity":"rare","title":"Someone signed up"}
	]`
	rec, events := post(t, s, body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decodeResponse(t, rec)["accepted"]; got != float64(3) {
		t.Fatalf("accepted = %v, want 3", got)
	}
	if len(events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(events))
	}
	if events[0].DedupeKey != "webhook:one" {
		t.Errorf("first key = %q", events[0].DedupeKey)
	}
	if events[1].Amount != 9 || events[1].Currency != "USD" {
		t.Errorf("second drop = %+v", events[1])
	}
	if p := payloadOf(t, events[2]); p["rarity"] != "rare" {
		t.Errorf("third payload = %v", p)
	}

	// Identical entries in one batch must not collapse into each other.
	_, twins := post(t, s, `[{"kind":"sale"},{"kind":"sale"}]`, "")
	if len(twins) != 2 || twins[0].DedupeKey == twins[1].DedupeKey {
		t.Fatalf("batch twins share a key: %v", twins)
	}
}

func TestBatchIsAllOrNothing(t *testing.T) {
	s := newSource(t, "")
	body := `[{"kind":"sale"},{"kind":"sale"},{"app":"no kind here"}]`
	rec, events := post(t, s, body, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if len(events) != 0 {
		t.Fatalf("emitted %d events from a rejected batch", len(events))
	}
	msg, _ := decodeResponse(t, rec)["error"].(string)
	if !strings.Contains(msg, "drop 2") || !strings.Contains(msg, "kind is required") {
		t.Fatalf("error = %q, want it to name the offending index and reason", msg)
	}
}

// ---------------------------------------------------------------- validation

func TestValidationErrors(t *testing.T) {
	s := newSource(t, "")
	cases := []struct {
		name, body, wantMsg string
	}{
		{"no kind", `{"app":"A"}`, "kind is required"},
		{"blank kind", `{"kind":"   "}`, "kind is required"},
		{"bad rarity", `{"kind":"sale","rarity":"mythic"}`, "not one of common"},
		{"amount without currency", `{"kind":"sale","amount":4.99}`, "amount requires currency"},
		{"bad occurred_at", `{"kind":"sale","occurred_at":"yesterday"}`, "RFC3339"},
		{"empty body", ``, "empty"},
		{"empty array", `[]`, "empty"},
		{"not json", `{"kind":`, "decode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, events := post(t, s, tc.body, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if len(events) != 0 {
				t.Fatalf("emitted %d events", len(events))
			}
			got := decodeResponse(t, rec)
			msg, _ := got["error"].(string)
			if got["ok"] != false || !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("error = %v, want it to mention %q", got, tc.wantMsg)
			}
		})
	}
}

func TestRarityPassthrough(t *testing.T) {
	s := newSource(t, "")
	for _, r := range core.Rarities {
		body := `{"kind":"sale","rarity":"` + string(r) + `","title":"t"}`
		rec, events := post(t, s, body, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", r, rec.Code, rec.Body)
		}
		if p := payloadOf(t, events[0]); p["rarity"] != string(r) {
			t.Fatalf("%s: payload rarity = %v", r, p["rarity"])
		}
	}
	// Case and spacing are the caller's business, not a reason to fail.
	_, events := post(t, s, `{"kind":"sale","rarity":" Legendary "}`, "")
	if p := payloadOf(t, events[0]); p["rarity"] != "legendary" {
		t.Fatalf("rarity = %v, want the normalized legendary", p["rarity"])
	}
}

func TestCountryIsNormalizedOrDropped(t *testing.T) {
	s := newSource(t, "")
	for body, want := range map[string]string{
		`{"kind":"sale","country":"de"}`:      "DE",
		`{"kind":"sale","country":" us "}`:    "US",
		`{"kind":"sale","country":"Germany"}`: "",
		`{"kind":"sale","country":"D1"}`:      "",
		`{"kind":"sale"}`:                     "",
	} {
		rec, events := post(t, s, body, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
		if events[0].Country != want {
			t.Errorf("%s: country = %q, want %q", body, events[0].Country, want)
		}
	}
}

func TestPayloadMergeDoesNotShadowTopLevelFields(t *testing.T) {
	s := newSource(t, "")
	body := `{"kind":"sale","title":"real title","payload":{"title":"payload title","note":"kept"}}`
	_, events := post(t, s, body, "")
	p := payloadOf(t, events[0])
	if p["title"] != "real title" {
		t.Fatalf("title = %v, want the top-level field to win", p["title"])
	}
	if p["note"] != "kept" {
		t.Fatalf("extra payload keys were lost: %v", p)
	}
}

// --------------------------------------------------------------- source glue

func TestSourceInterface(t *testing.T) {
	s := newSource(t, "")
	if s.Name() != "webhook" {
		t.Fatalf("Name = %q", s.Name())
	}
	if s.PollInterval() != 0 {
		t.Fatalf("PollInterval = %s, want 0 (webhook-only)", s.PollInterval())
	}
	events, state, err := s.Poll(context.Background(), nil)
	if err != nil || events != nil || state != nil {
		t.Fatalf("Poll = %v, %v, %v; want all nil", events, state, err)
	}
}

func TestCheckWarnsWithoutSecret(t *testing.T) {
	if err := newSource(t, "").Check(context.Background()); err == nil {
		t.Fatal("Check with no secret should complain")
	} else if !strings.Contains(err.Error(), "/hooks/webhook") {
		t.Fatalf("Check error = %v, want it to name the endpoint", err)
	}
	if err := newSource(t, "hunter2").Check(context.Background()); err != nil {
		t.Fatalf("Check with a secret: %v", err)
	}
}

// TestPostedDropsRenderWithTheDefaultRules pins the contract between this
// source and internal/rules/default.yaml: the six rarity rules select on
// payload_match {rarity: ...} and render {{.Payload.title}}, so a payload that
// buried those keys anywhere but the top level would produce blank drops.
func TestPostedDropsRenderWithTheDefaultRules(t *testing.T) {
	engine, err := rules.Load("", nil)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	s := newSource(t, "")

	for _, r := range core.Rarities {
		body := `{"kind":"sale","app":"Sprocket","rarity":"` + string(r) + `",
			"title":"A thing happened","subtitle":"to your app"}`
		rec, events := post(t, s, body, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", r, rec.Code, rec.Body)
		}
		drop, err := engine.Classify(context.Background(), events[0])
		if err != nil {
			t.Fatalf("%s: classify: %v", r, err)
		}
		if drop.Rarity != r {
			t.Errorf("posted rarity %s classified as %s", r, drop.Rarity)
		}
		if drop.Title != "A thing happened" || drop.Subtitle != "to your app" {
			t.Errorf("%s: drop = %q / %q, want the posted title and subtitle", r, drop.Title, drop.Subtitle)
		}
	}

	// Without a rarity the drop still classifies, via the fallback.
	_, events := post(t, s, `{"kind":"sale","app":"Sprocket"}`, "")
	drop, err := engine.Classify(context.Background(), events[0])
	if err != nil {
		t.Fatalf("classify rarity-less drop: %v", err)
	}
	if drop.Title == "" || !drop.Rarity.Valid() {
		t.Fatalf("rarity-less drop = %+v, want a usable fallback", drop)
	}
}
