package revenuecat_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/revenuecat"
)

// sampleWebhook mirrors a real RevenueCat INITIAL_PURCHASE delivery.
const sampleWebhook = `{
  "api_version": "1.0",
  "event": {
    "id": "9F1B4C2E-6A7D-4E3F-8B21-0C5D9E7A1F42",
    "type": "INITIAL_PURCHASE",
    "app_id": "app1a2b3c4d",
    "app_user_id": "user-42",
    "original_app_user_id": "user-42",
    "product_id": "premium_monthly",
    "period_type": "NORMAL",
    "price": 9.99,
    "price_in_purchased_currency": 8.49,
    "currency": "USD",
    "country_code": "de",
    "store": "APP_STORE",
    "environment": "PRODUCTION",
    "event_timestamp_ms": 1755500000000,
    "purchased_at_ms": 1755499000000,
    "expiration_at_ms": 1758092000000,
    "transaction_id": "1000000123456789"
  }
}`

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseEvent(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	ev, err := revenuecat.ParseEvent([]byte(sampleWebhook), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if ev.Source != "revenuecat" {
		t.Errorf("source = %q", ev.Source)
	}
	if ev.Kind != "purchase" {
		t.Errorf("kind = %q, want purchase", ev.Kind)
	}
	if ev.App != "app1a2b3c4d" {
		t.Errorf("app = %q, want the app_id", ev.App)
	}
	if ev.Country != "DE" {
		t.Errorf("country = %q, want DE (uppercased)", ev.Country)
	}
	if ev.Amount != 9.99 || ev.Currency != "USD" {
		t.Errorf("amount = %v %s, want 9.99 USD", ev.Amount, ev.Currency)
	}
	if ev.Quantity != 1 {
		t.Errorf("quantity = %d, want 1", ev.Quantity)
	}
	if ev.DedupeKey != "rc:9F1B4C2E-6A7D-4E3F-8B21-0C5D9E7A1F42" {
		t.Errorf("dedupe_key = %q", ev.DedupeKey)
	}
	if ev.IsLedger {
		t.Error("RevenueCat is not an authoritative revenue ledger")
	}
	want := time.UnixMilli(1755500000000).UTC()
	if !ev.OccurredAt.Equal(want) {
		t.Errorf("occurred_at = %v, want %v", ev.OccurredAt, want)
	}
	if !ev.ObservedAt.Equal(now) {
		t.Errorf("observed_at = %v, want %v", ev.ObservedAt, now)
	}

	// The raw body is retained so rules can match on payload paths.
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	inner, _ := payload["event"].(map[string]any)
	if inner["period_type"] != "NORMAL" {
		t.Errorf("payload period_type = %v", inner["period_type"])
	}
}

func TestKindMapping(t *testing.T) {
	cases := map[string]string{
		"INITIAL_PURCHASE":      "purchase",
		"RENEWAL":               "renewal",
		"NON_RENEWING_PURCHASE": "purchase",
		"CANCELLATION":          "cancellation",
		"UNCANCELLATION":        "uncancellation",
		"BILLING_ISSUE":         "billing_issue",
		"EXPIRATION":            "expiration",
		"PRODUCT_CHANGE":        "product_change",
		"TEST":                  "test",
		// Unknown types pass through lowercased rather than being discarded.
		"SUBSCRIPTION_PAUSED": "subscription_paused",
	}
	for in, want := range cases {
		if got := revenuecat.Kind(in); got != want {
			t.Errorf("Kind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEventFallbacks(t *testing.T) {
	now := time.Now().UTC()

	t.Run("falls back to purchased_at_ms", func(t *testing.T) {
		body := `{"event":{"type":"RENEWAL","id":"x","purchased_at_ms":1755499000000}}`
		ev, err := revenuecat.ParseEvent([]byte(body), now)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !ev.OccurredAt.Equal(time.UnixMilli(1755499000000).UTC()) {
			t.Errorf("occurred_at = %v", ev.OccurredAt)
		}
	})

	t.Run("falls back to purchased currency price", func(t *testing.T) {
		body := `{"event":{"type":"RENEWAL","id":"x","price_in_purchased_currency":4.5,"currency":"eur"}}`
		ev, err := revenuecat.ParseEvent([]byte(body), now)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Amount != 4.5 || ev.Currency != "EUR" {
			t.Errorf("amount = %v %s, want 4.5 EUR", ev.Amount, ev.Currency)
		}
	})

	t.Run("falls back to product_id for app", func(t *testing.T) {
		body := `{"event":{"type":"RENEWAL","id":"x","product_id":"premium_monthly"}}`
		ev, err := revenuecat.ParseEvent([]byte(body), now)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.App != "premium_monthly" {
			t.Errorf("app = %q", ev.App)
		}
	})

	t.Run("falls back to transaction id for dedupe", func(t *testing.T) {
		body := `{"event":{"type":"RENEWAL","transaction_id":"txn-9"}}`
		ev, err := revenuecat.ParseEvent([]byte(body), now)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.DedupeKey != "rc:txn:txn-9" {
			t.Errorf("dedupe_key = %q", ev.DedupeKey)
		}
	})

	t.Run("hashes the body when nothing identifies it", func(t *testing.T) {
		body := `{"event":{"type":"TEST"}}`
		a, err := revenuecat.ParseEvent([]byte(body), now)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		b, _ := revenuecat.ParseEvent([]byte(body), now)
		if !strings.HasPrefix(a.DedupeKey, "rc:sha256:") {
			t.Errorf("dedupe_key = %q, want a content hash", a.DedupeKey)
		}
		if a.DedupeKey != b.DedupeKey {
			t.Error("identical bodies must hash to the same dedupe key")
		}
	})
}

func TestParseEventRejectsGarbage(t *testing.T) {
	now := time.Now()

	if _, err := revenuecat.ParseEvent([]byte(`not json`), now); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
	if _, err := revenuecat.ParseEvent([]byte(`{"event":{}}`), now); err == nil {
		t.Error("expected an error for a payload with no event type")
	}
}

func TestHandleWebhook(t *testing.T) {
	src := revenuecat.New("", quietLogger())

	var got []core.Event
	emit := func(ev core.Event) { got = append(got, ev) }

	req := httptest.NewRequest(http.MethodPost, "/hooks/revenuecat", strings.NewReader(sampleWebhook))
	rec := httptest.NewRecorder()
	src.HandleWebhook(rec, req, emit)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("emitted %d events, want 1", len(got))
	}
	if got[0].Kind != "purchase" {
		t.Errorf("kind = %q", got[0].Kind)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not json: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("response = %v", resp)
	}
}

func TestHandleWebhookAuth(t *testing.T) {
	src := revenuecat.New("s3cret", quietLogger())
	emit := func(core.Event) {}

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong secret", "Bearer nope", http.StatusUnauthorized},
		{"bearer secret", "Bearer s3cret", http.StatusOK},
		{"bare secret", "s3cret", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/hooks/revenuecat", strings.NewReader(sampleWebhook))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			src.HandleWebhook(rec, req, emit)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHandleWebhookRejectsBadInput(t *testing.T) {
	src := revenuecat.New("", quietLogger())
	emitted := 0
	emit := func(core.Event) { emitted++ }

	t.Run("bad body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/revenuecat", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		src.HandleWebhook(rec, req, emit)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hooks/revenuecat", nil)
		rec := httptest.NewRecorder()
		src.HandleWebhook(rec, req, emit)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	if emitted != 0 {
		t.Fatalf("emitted %d events from bad requests, want 0", emitted)
	}
}

func TestSourceContract(t *testing.T) {
	src := revenuecat.New("", quietLogger())
	if src.Name() != "revenuecat" {
		t.Errorf("name = %q", src.Name())
	}
	if src.PollInterval() != 0 {
		t.Errorf("poll interval = %v, want 0 (webhook-only)", src.PollInterval())
	}
	events, state, err := src.Poll(t.Context(), nil)
	if events != nil || state != nil || err != nil {
		t.Errorf("Poll = %v, %v, %v; want all nil", events, state, err)
	}
}
