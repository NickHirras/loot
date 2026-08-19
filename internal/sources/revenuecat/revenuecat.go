// Package revenuecat receives RevenueCat webhooks and turns them into events.
// It is a webhook-only source: PollInterval is zero and Poll does nothing.
package revenuecat

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, also the URL segment: /hooks/revenuecat.
const Name = "revenuecat"

// maxBody caps the webhook body. RevenueCat payloads are a few KB.
const maxBody = 1 << 20

// Source implements core.Source and core.WebhookHandler.
type Source struct {
	// Secret, when non-empty, must be presented as `Authorization: Bearer <secret>`.
	Secret string
	Log    *slog.Logger
}

// New returns a RevenueCat webhook source.
func New(secret string, log *slog.Logger) *Source {
	if log == nil {
		log = slog.Default()
	}
	return &Source{Secret: secret, Log: log}
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// Poll implements core.Source. RevenueCat pushes, so there is nothing to pull.
func (s *Source) Poll(context.Context, []byte) ([]core.Event, []byte, error) {
	return nil, nil, nil
}

// PollInterval implements core.Source; 0 marks this source as webhook-only.
func (s *Source) PollInterval() time.Duration { return 0 }

// Check implements core.Checker. There is nothing to call — RevenueCat pushes
// to us — so the only thing worth reporting is whether the endpoint is
// protected.
func (s *Source) Check(context.Context) error {
	if s.Secret == "" {
		return core.Warning{Msg: "no secret set: anyone who can reach /hooks/revenuecat can inject drops"}
	}
	return nil
}

// webhook is the subset of the RevenueCat webhook envelope Loot reads.
// Unknown fields are preserved in the raw payload stored with the event.
type webhook struct {
	APIVersion string `json:"api_version"`
	Event      struct {
		ID                       string  `json:"id"`
		Type                     string  `json:"type"`
		AppUserID                string  `json:"app_user_id"`
		OriginalAppUserID        string  `json:"original_app_user_id"`
		ProductID                string  `json:"product_id"`
		Price                    float64 `json:"price"`
		PriceInPurchasedCurrency float64 `json:"price_in_purchased_currency"`
		Currency                 string  `json:"currency"`
		CountryCode              string  `json:"country_code"`
		EventTimestampMS         int64   `json:"event_timestamp_ms"`
		PurchasedAtMS            int64   `json:"purchased_at_ms"`
		ExpirationAtMS           int64   `json:"expiration_at_ms"`
		TransactionID            string  `json:"transaction_id"`
		OriginalTransactionID    string  `json:"original_transaction_id"`
		PeriodType               string  `json:"period_type"`
		Store                    string  `json:"store"`
		Environment              string  `json:"environment"`
		AppID                    string  `json:"app_id"`
	} `json:"event"`
}

// kindFor maps RevenueCat event types onto Loot's normalized kinds.
var kindFor = map[string]string{
	"INITIAL_PURCHASE":      "purchase",
	"RENEWAL":               "renewal",
	"NON_RENEWING_PURCHASE": "purchase",
	"CANCELLATION":          "cancellation",
	"UNCANCELLATION":        "uncancellation",
	"BILLING_ISSUE":         "billing_issue",
	"EXPIRATION":            "expiration",
	"PRODUCT_CHANGE":        "product_change",
	"TEST":                  "test",
}

// Kind maps a RevenueCat event type to a Loot kind. Unknown types are
// lowercased and passed through so a new RevenueCat event type still lands in
// the feed instead of being dropped on the floor.
func Kind(rcType string) string {
	if k, ok := kindFor[strings.ToUpper(strings.TrimSpace(rcType))]; ok {
		return k
	}
	return strings.ToLower(strings.TrimSpace(rcType))
}

// ErrEmptyEvent is returned when a body carries no usable event object.
var ErrEmptyEvent = errors.New("revenuecat: payload has no event.type")

// ParseEvent converts a raw RevenueCat webhook body into a core.Event.
// Exported so the parsing is directly testable.
func ParseEvent(body []byte, now time.Time) (core.Event, error) {
	var wh webhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return core.Event{}, fmt.Errorf("revenuecat: decode body: %w", err)
	}
	if strings.TrimSpace(wh.Event.Type) == "" {
		return core.Event{}, ErrEmptyEvent
	}

	e := wh.Event

	occurred := now
	switch {
	case e.EventTimestampMS > 0:
		occurred = time.UnixMilli(e.EventTimestampMS).UTC()
	case e.PurchasedAtMS > 0:
		occurred = time.UnixMilli(e.PurchasedAtMS).UTC()
	}

	// price is in the app's display currency; price_in_purchased_currency is
	// what the customer actually paid. Prefer price, fall back to the latter.
	amount, currency := e.Price, strings.ToUpper(e.Currency)
	if amount == 0 && e.PriceInPurchasedCurrency != 0 {
		amount = e.PriceInPurchasedCurrency
	}
	if amount < 0 {
		amount = 0
	}

	app := e.AppID
	if app == "" {
		app = e.ProductID
	}

	quantity := 0
	if amount > 0 {
		quantity = 1
	}

	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       Kind(e.Type),
		App:        app,
		OccurredAt: occurred,
		ObservedAt: now.UTC(),
		Country:    strings.ToUpper(strings.TrimSpace(e.CountryCode)),
		Amount:     amount,
		Currency:   currency,
		Quantity:   quantity,
		DedupeKey:  dedupeKey(e.ID, e.TransactionID, body),
		// RevenueCat mirrors store data and is not itself the money ledger:
		// treat its amounts as signal, not accounting truth.
		IsLedger: false,
		Payload:  json.RawMessage(body),
	}, nil
}

// dedupeKey prefers RevenueCat's own event id, then the transaction id, and
// finally a content hash so a retried delivery still collapses.
func dedupeKey(eventID, txnID string, body []byte) string {
	if id := strings.TrimSpace(eventID); id != "" {
		return "rc:" + id
	}
	if id := strings.TrimSpace(txnID); id != "" {
		return "rc:txn:" + id
	}
	sum := sha256.Sum256(body)
	return "rc:sha256:" + hex.EncodeToString(sum[:16])
}

// HandleWebhook implements core.WebhookHandler.
func (s *Source) HandleWebhook(w http.ResponseWriter, r *http.Request, emit func(core.Event)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	ev, err := ParseEvent(body, time.Now())
	if err != nil {
		s.Log.Warn("revenuecat webhook rejected", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	emit(ev)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "kind": ev.Kind, "dedupe_key": ev.DedupeKey})
}

// authorized checks the optional shared secret in constant time.
func (s *Source) authorized(r *http.Request) bool {
	if s.Secret == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + s.Secret
	// RevenueCat sends whatever the user typed into the Authorization field, so
	// accept both "Bearer <secret>" and a bare "<secret>".
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Secret)) == 1
}

var (
	_ core.Source         = (*Source)(nil)
	_ core.WebhookHandler = (*Source)(nil)
)
