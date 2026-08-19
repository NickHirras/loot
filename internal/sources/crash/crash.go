// Package crash is the generic crash receiver: POST /hooks/crash, for every
// crash reporter that is not Play vitals and not Sentry.
//
// It exists mostly because of Crashlytics. Crashlytics has no data API at all
// — the only supported ways to read your own crash counts are a BigQuery
// export and the Firebase Alerts event stream — so the practical route into
// Loot is a twenty-line Cloud Function that relays velocity alerts here. See
// docs/sources/crashes.md for that function.
//
// One object, or an array of them:
//
//	curl -X POST http://localhost:8080/hooks/crash \
//	  -H 'Authorization: Bearer hunter2' \
//	  -d '{"app":"com.example.app","version":"2.3.1","crashes":312,
//	       "users_affected":91,"issue_id":"a1b2","title":"NPE in SyncWorker",
//	       "boss":true}'
//
// Counts *add up* within a day: each post contributes its `crashes` to that
// (app, version, issue, day) total, so a script may report deltas as often as
// it likes. Pass `id` to make a retry idempotent — without one, two identical
// posts are two genuine reports, because silently swallowing the second would
// be the worse mistake.
package crash

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

// Name is the source identifier, also the URL segment: /hooks/crash.
const Name = "crash"

// maxBody caps the request body; maxBatch how many reports one request may
// carry.
const (
	maxBody  = 1 << 20
	maxBatch = 500
)

// Source implements core.Source, core.WebhookHandler and core.Checker.
type Source struct {
	cfg config.Crash
	log *slog.Logger

	// Now is swappable for tests.
	Now func() time.Time
}

// New builds the source from its config.
func New(cfg config.Crash, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	return &Source{cfg: cfg, log: log, Now: time.Now}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source; 0 marks this source as webhook-only.
func (s *Source) PollInterval() time.Duration { return 0 }

// Poll implements core.Source. Crash reporters push; there is nothing to pull.
func (s *Source) Poll(context.Context, []byte) ([]core.Event, []byte, error) {
	return nil, nil, nil
}

// Check implements core.Checker. Nothing to call, so the only thing worth
// reporting is whether the door is locked — and an open crash endpoint is
// worse than an open drop endpoint, because anyone who finds it can spawn
// bosses in your dashboard.
func (s *Source) Check(context.Context) error {
	if strings.TrimSpace(s.cfg.Secret) == "" {
		return core.Warning{Msg: "no secret set: anyone who can reach /hooks/crash can spawn bosses"}
	}
	return nil
}

// Report is one posted crash report.
type Report struct {
	// App is which app crashed. It is the only genuinely required field: a
	// crash with no app cannot be grouped, baselined or fought.
	App string `json:"app"`
	// Version is the app version the crashes happened in. Loot keys a fight on
	// it, so reporting it is what makes "the fix rolled out" visible.
	Version string `json:"version"`
	// IssueID and Title identify one crash cluster inside a version. Omit them
	// and the whole version is one fight.
	IssueID string `json:"issue_id"`
	Title   string `json:"title"`
	// Crashes is how many crashes this report accounts for; it defaults to 1.
	Crashes *int `json:"crashes"`
	// UsersAffected is how many distinct people they hit, when you know.
	UsersAffected int `json:"users_affected"`
	// Kind is "crash" (the default) or "anr".
	Kind string `json:"kind"`
	// OccurredAt is RFC3339, or a bare YYYY-MM-DD day; it defaults to now. It
	// decides which day's total this report joins.
	OccurredAt string `json:"occurred_at"`
	// Boss forces a spawn regardless of the baseline. It is how a Crashlytics
	// velocity alert says "this one is bad" without Loot having to work it out
	// from counts it was never given.
	Boss bool `json:"boss"`
	// Resolved marks the fight fixed upstream, which slays the boss.
	Resolved bool `json:"resolved"`
	// URL links out to wherever the issue actually lives.
	URL string `json:"url"`
	// ID is the dedupe identity. Omit it and every post counts.
	ID string `json:"id"`
}

// ErrEmptyBody is returned for a request with nothing in it.
var ErrEmptyBody = errors.New("crash: body is empty")

// ParseBody decodes one report or an array of them. Exported so the parsing is
// testable without HTTP.
func ParseBody(body []byte) ([]Report, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, ErrEmptyBody
	}
	if strings.HasPrefix(trimmed, "[") {
		var reports []Report
		if err := json.Unmarshal(body, &reports); err != nil {
			return nil, fmt.Errorf("crash: decode batch: %w", err)
		}
		if len(reports) == 0 {
			return nil, ErrEmptyBody
		}
		if len(reports) > maxBatch {
			return nil, fmt.Errorf("crash: batch of %d exceeds the %d report limit", len(reports), maxBatch)
		}
		return reports, nil
	}
	var one Report
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, fmt.Errorf("crash: decode body: %w", err)
	}
	return []Report{one}, nil
}

// dayLayouts are what occurred_at may be written as. A crash reporter that
// only knows the day should not have to invent a time of day.
var dayLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", core.DayLayout}

// reportSeq makes the id-less dedupe hash unique even for two identical
// reports inside the same clock tick.
var reportSeq atomic.Uint64

// ToEvents validates one report and converts it into the events it implies:
// the crash count, and — when the report says the issue is fixed — the silent
// resolution signal that slays the boss.
func (r Report) ToEvents(raw []byte, now time.Time) ([]core.Event, error) {
	app := strings.TrimSpace(r.App)
	if app == "" {
		return nil, errors.New("app is required")
	}

	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	switch kind {
	case "", core.BossKindCrash:
		kind = core.BossKindCrash
	case core.BossKindANR:
	default:
		return nil, fmt.Errorf("kind %q is not one of crash, anr", r.Kind)
	}

	occurred := now.UTC()
	if v := strings.TrimSpace(r.OccurredAt); v != "" {
		parsed := false
		for _, layout := range dayLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				occurred = t.UTC()
				parsed = true
				break
			}
		}
		if !parsed {
			return nil, fmt.Errorf("occurred_at %q is not an RFC3339 timestamp or a YYYY-MM-DD day", r.OccurredAt)
		}
	}

	crashes := 1
	if r.Crashes != nil {
		crashes = *r.Crashes
	}
	if crashes < 0 {
		return nil, errors.New("crashes cannot be negative")
	}
	if r.UsersAffected < 0 {
		return nil, errors.New("users_affected cannot be negative")
	}
	if crashes == 0 && !r.Resolved {
		return nil, errors.New("a report with no crashes must set resolved")
	}

	payload := core.CrashPayload{
		Version:       strings.TrimSpace(r.Version),
		IssueID:       strings.TrimSpace(r.IssueID),
		IssueTitle:    strings.TrimSpace(r.Title),
		UsersAffected: r.UsersAffected,
		Kind:          kind,
		URL:           strings.TrimSpace(r.URL),
		Boss:          r.Boss,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("payload is not encodable: %w", err)
	}

	day := core.DayOf(occurred)
	identity := fmt.Sprintf("%s:%s:%s:%s", app, day, payload.Version, payload.IssueID)

	var out []core.Event
	if crashes > 0 {
		out = append(out, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindCrash,
			App:        app,
			OccurredAt: occurred,
			ObservedAt: now.UTC(),
			Day:        day,
			Quantity:   crashes,
			DedupeKey:  Name + ":crash:" + r.dedupeID(identity, raw, now),
			// Crashes never make a drop of their own. The *boss* is the drop,
			// and one drop per crash would be the exact dashboard this feature
			// exists to avoid being.
			Silent:  true,
			Payload: json.RawMessage(encoded),
		})
	}
	if r.Resolved {
		out = append(out, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindCrashResolved,
			App:        app,
			OccurredAt: occurred,
			ObservedAt: now.UTC(),
			Day:        day,
			DedupeKey:  Name + ":resolved:" + identity,
			Silent:     true,
			Payload:    json.RawMessage(encoded),
		})
	}
	return out, nil
}

// dedupeID returns the caller's id, or a hash of the report, the current
// nanosecond and a counter.
func (r Report) dedupeID(identity string, raw []byte, now time.Time) string {
	if id := strings.TrimSpace(r.ID); id != "" {
		return identity + ":" + id
	}
	h := sha256.New()
	h.Write(raw)
	fmt.Fprintf(h, "|%d|%d", now.UTC().UnixNano(), reportSeq.Add(1))
	return identity + ":" + hex.EncodeToString(h.Sum(nil)[:12])
}

// HandleWebhook implements core.WebhookHandler. The whole batch is validated
// before anything is emitted, so a typo in the fourth report does not leave
// the first three half-ingested.
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

	reports, err := ParseBody(body)
	if err != nil {
		s.badRequest(w, err.Error())
		return
	}

	now := s.now()
	var events []core.Event
	for i, rep := range reports {
		raw, _ := json.Marshal(rep)
		evs, err := rep.ToEvents(raw, now)
		if err != nil {
			if len(reports) > 1 {
				s.badRequest(w, fmt.Sprintf("report %d: %s", i, err))
			} else {
				s.badRequest(w, err.Error())
			}
			return
		}
		events = append(events, evs...)
	}

	for _, ev := range events {
		emit(ev)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "accepted": len(events)})
}

func (s *Source) badRequest(w http.ResponseWriter, msg string) {
	s.log.Warn("crash report rejected", "error", msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// authorized checks the optional shared secret in constant time. Both
// "Bearer <secret>" and a bare "<secret>" are accepted, because half the tools
// that can POST JSON cannot set a prefix.
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
