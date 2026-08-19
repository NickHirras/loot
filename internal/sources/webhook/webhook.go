// Package webhook is the escape hatch: a generic receiver at POST
// /hooks/webhook that turns any JSON anyone can POST into a drop.
//
// It exists because the interesting things that happen to a project are not
// all sold in an app store. A green CI run, a customer's first invoice, a
// server that came back up, a friend saying yes — if you can curl it, Loot can
// make it a drop:
//
//	curl -X POST http://localhost:8080/hooks/webhook \
//	  -H 'Authorization: Bearer hunter2' \
//	  -d '{"kind":"sale","rarity":"legendary","title":"First customer!"}'
//
// The body is one object, or an array of them for a batch. Only `kind` is
// required; everything else has a defensible default. `rarity`, `title` and
// `subtitle` are stored at the top level of the event payload because that is
// where the default rules look for them.
package webhook

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
	"sync/atomic"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, also the URL segment: /hooks/webhook.
const Name = "webhook"

// maxBody caps the request body.
const maxBody = 1 << 20

// maxBatch caps how many drops one request may carry, so a runaway script
// cannot fill the feed in a single POST.
const maxBatch = 500

// Source implements core.Source, core.WebhookHandler and core.Checker.
type Source struct {
	cfg config.Webhook
	log *slog.Logger
	// Since is unused: a webhook has no history to backfill. It exists so the
	// server can wire every source the same way.
	Since string

	// Now is swappable for tests.
	Now func() time.Time
}

// New builds the source from its config.
func New(cfg config.Webhook, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	return &Source{cfg: cfg, log: log, Now: time.Now}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source; 0 marks this source as webhook-only.
func (s *Source) PollInterval() time.Duration { return 0 }

// Poll implements core.Source. Nothing to pull: the world pushes to us.
func (s *Source) Poll(context.Context, []byte) ([]core.Event, []byte, error) {
	return nil, nil, nil
}

// Check implements core.Checker. There is nothing to call, so the only thing
// worth reporting is whether the door is locked.
func (s *Source) Check(context.Context) error {
	if strings.TrimSpace(s.cfg.Secret) == "" {
		return core.Warning{Msg: "no secret set: anyone who can reach /hooks/webhook can inject drops"}
	}
	return nil
}

// Drop is the JSON body of one posted drop. Pointer and string fields
// distinguish "absent" from "zero" wherever the difference changes behaviour.
type Drop struct {
	// Kind is the only required field: the event kind, free-form, e.g. "sale",
	// "signup", "ci_green". Rules match on it like any other source's kind.
	Kind string `json:"kind"`
	// App names the thing this happened to; it groups the vault and the feed.
	App string `json:"app"`
	// Country is an ISO 3166-1 alpha-2 code. Anything else is dropped rather
	// than rejected, so a stray "Germany" costs you a flag, not the drop.
	Country string `json:"country"`
	// Amount needs Currency to mean anything, so it is an error without one.
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	// Quantity defaults to 1.
	Quantity *int `json:"quantity"`
	// OccurredAt is RFC3339; it defaults to now.
	OccurredAt string `json:"occurred_at"`
	// ID is the dedupe identity. Omit it and every post is a new drop: the
	// fallback hash mixes in the clock and a counter, so two identical bodies
	// never collide.
	ID string `json:"id"`
	// Rarity, when set, is one of common | uncommon | rare | epic | legendary
	// | cursed and is matched by the default rules.
	Rarity string `json:"rarity"`
	// Title and Subtitle are what the rarity rules render into the drop.
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// Ledger marks this as settled money, so the vault sums it into revenue.
	// Leave it false for estimates and signals.
	Ledger bool `json:"ledger"`
	// Chest holds the drop for the day's chest instead of the live feed.
	Chest bool `json:"chest"`
	// Payload is merged into the stored event payload, under the top-level
	// rarity/title/subtitle the rules read.
	Payload map[string]any `json:"payload"`
}

// ErrEmptyBody is returned for a request with nothing to ingest.
var ErrEmptyBody = errors.New("webhook: body is empty")

// ParseBody decodes one drop or an array of them. Exported so the parsing is
// testable without HTTP.
func ParseBody(body []byte) ([]Drop, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, ErrEmptyBody
	}
	if strings.HasPrefix(trimmed, "[") {
		var drops []Drop
		if err := json.Unmarshal(body, &drops); err != nil {
			return nil, fmt.Errorf("webhook: decode batch: %w", err)
		}
		if len(drops) == 0 {
			return nil, ErrEmptyBody
		}
		if len(drops) > maxBatch {
			return nil, fmt.Errorf("webhook: batch of %d exceeds the %d drop limit", len(drops), maxBatch)
		}
		return drops, nil
	}
	var one Drop
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, fmt.Errorf("webhook: decode body: %w", err)
	}
	return []Drop{one}, nil
}

// isAlpha2 reports whether c is two ASCII letters.
func isAlpha2(c string) bool {
	if len(c) != 2 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}

// ToEvent validates one posted drop and converts it into an event. now is the
// observation time; raw is the exact bytes the drop arrived in, used for the
// fallback dedupe hash.
func (d Drop) ToEvent(raw []byte, now time.Time) (core.Event, error) {
	kind := strings.TrimSpace(d.Kind)
	if kind == "" {
		return core.Event{}, errors.New("kind is required")
	}

	rarity := strings.ToLower(strings.TrimSpace(d.Rarity))
	if rarity != "" && !core.Rarity(rarity).Valid() {
		return core.Event{}, fmt.Errorf("rarity %q is not one of common, uncommon, rare, epic, legendary, cursed", d.Rarity)
	}

	// A country that is not a two-letter code is noise, not a reason to reject
	// the drop; the flag is a nicety and the event is the point.
	country := strings.ToUpper(strings.TrimSpace(d.Country))
	if !isAlpha2(country) {
		country = ""
	}

	currency := strings.ToUpper(strings.TrimSpace(d.Currency))
	if d.Amount != 0 && currency == "" {
		return core.Event{}, errors.New("amount requires currency")
	}

	occurred := now.UTC()
	if v := strings.TrimSpace(d.OccurredAt); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return core.Event{}, fmt.Errorf("occurred_at %q is not an RFC3339 timestamp", d.OccurredAt)
		}
		occurred = t.UTC()
	}

	quantity := 1
	if d.Quantity != nil {
		quantity = *d.Quantity
	}

	// The payload the rules see: the caller's extra keys first, then the
	// fields the default rules read by name, so those always win.
	payload := map[string]any{}
	for k, v := range d.Payload {
		payload[k] = v
	}
	if rarity != "" {
		payload["rarity"] = rarity
	}
	if d.Title != "" {
		payload["title"] = d.Title
	}
	if d.Subtitle != "" {
		payload["subtitle"] = d.Subtitle
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return core.Event{}, fmt.Errorf("payload is not encodable: %w", err)
	}

	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       kind,
		App:        strings.TrimSpace(d.App),
		OccurredAt: occurred,
		ObservedAt: now.UTC(),
		Country:    country,
		Amount:     d.Amount,
		Currency:   currency,
		Quantity:   quantity,
		DedupeKey:  "webhook:" + d.dedupeID(raw, now),
		IsLedger:   d.Ledger,
		Chest:      d.Chest,
		Payload:    json.RawMessage(encoded),
	}, nil
}

// dropSeq makes the id-less dedupe hash unique even for two identical bodies
// that arrive inside the same clock tick.
var dropSeq atomic.Uint64

// dedupeID returns the caller's id, or a hash of the drop, the current
// nanosecond and a counter. The fallback is deliberately unique per drop:
// without an id there is no way to tell a retry from a second identical sale,
// and silently swallowing the second sale would be the worse mistake.
func (d Drop) dedupeID(raw []byte, now time.Time) string {
	if id := strings.TrimSpace(d.ID); id != "" {
		return id
	}
	h := sha256.New()
	h.Write(raw)
	fmt.Fprintf(h, "|%d|%d", now.UTC().UnixNano(), dropSeq.Add(1))
	return "sha256:" + hex.EncodeToString(h.Sum(nil)[:16])
}

// HandleWebhook implements core.WebhookHandler.
//
// The whole batch is validated before anything is emitted, so a typo in the
// fourth drop does not leave the first three half-ingested.
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

	drops, err := ParseBody(body)
	if err != nil {
		s.badRequest(w, err.Error())
		return
	}

	now := s.now()
	events := make([]core.Event, 0, len(drops))
	for i, d := range drops {
		// Each drop hashes its own bytes, so an id-less drop in a batch is
		// still distinct from its neighbours.
		raw, _ := json.Marshal(d)
		ev, err := d.ToEvent(raw, now)
		if err != nil {
			if len(drops) > 1 {
				s.badRequest(w, fmt.Sprintf("drop %d: %s", i, err))
			} else {
				s.badRequest(w, err.Error())
			}
			return
		}
		events = append(events, ev)
	}

	for _, ev := range events {
		emit(ev)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "accepted": len(events)})
}

func (s *Source) badRequest(w http.ResponseWriter, msg string) {
	s.log.Warn("webhook drop rejected", "error", msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// authorized checks the optional shared secret in constant time. Both
// "Bearer <secret>" and a bare "<secret>" are accepted, because half the
// tools that can POST JSON cannot set a prefix.
func (s *Source) authorized(r *http.Request) bool {
	secret := strings.TrimSpace(s.cfg.Secret)
	if secret == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+secret)) == 1 {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

var (
	_ core.Source         = (*Source)(nil)
	_ core.WebhookHandler = (*Source)(nil)
	_ core.Checker        = (*Source)(nil)
)
