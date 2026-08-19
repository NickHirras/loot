// Package sentry receives Sentry webhooks and turns them into crash events,
// which the boss engine then turns into monsters.
//
// It is a webhook-only source: PollInterval is zero and Poll does nothing.
// Sentry's REST API would need a per-organization auth token and a poll loop
// with its own rate limit; the webhooks arrive for free the moment an issue
// appears, and "the moment it appears" is when a boss should spawn.
//
// # Setting it up
//
// In Sentry: Settings > Developer Settings > Custom Integrations > Create New
// Integration > Internal Integration. Set the webhook URL to
// https://<your loot>/hooks/sentry, tick the **issue** webhook, give it
// Issue & Event read permission, and copy the Client Secret into
// sources.sentry.client_secret. Add an issue alert rule with a "Send a
// notification via <your integration>" action if you also want event alerts.
//
// See docs/sources/crashes.md.
package sentry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, also the URL segment: /hooks/sentry.
const Name = "sentry"

// maxBody caps a delivery. Sentry issue payloads are a few KB; an event alert
// carries the whole event and can be larger, but not by orders of magnitude.
const maxBody = 4 << 20

// The headers Sentry signs and labels its deliveries with. Go canonicalizes
// header names on lookup, so the capitalization here is cosmetic — but it is
// the capitalization Sentry actually sends, which is what a reader checking
// this against the docs will be holding.
const (
	HeaderSignature = "Sentry-Hook-Signature"
	HeaderResource  = "Sentry-Hook-Resource"
	HeaderTimestamp = "Sentry-Hook-Timestamp"
)

// The `Sentry-Hook-Resource` values Loot acts on. Everything else (installation,
// metric_alert, comment) is acknowledged and ignored — a 200 with nothing done
// is the right answer to "an integration was installed".
const (
	ResourceIssue      = "issue"
	ResourceEventAlert = "event_alert"
	ResourceError      = "error"
)

// Source implements core.Source, core.WebhookHandler and core.Checker.
type Source struct {
	cfg config.Sentry
	log *slog.Logger

	// openHookWarned makes the "no secret" warning fire once per process
	// rather than once per delivery.
	openHookWarned sync.Once

	// Now is swappable for tests.
	Now func() time.Time
}

// New builds the source from its config.
func New(cfg config.Sentry, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	return &Source{cfg: cfg, log: log, Now: time.Now}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source; 0 marks this source as webhook-only.
func (s *Source) PollInterval() time.Duration { return 0 }

// Poll implements core.Source. Sentry pushes.
func (s *Source) Poll(context.Context, []byte) ([]core.Event, []byte, error) {
	return nil, nil, nil
}

// Check implements core.Checker. There is nothing to call, so the only thing
// worth reporting is whether deliveries are verified.
func (s *Source) Check(context.Context) error {
	if strings.TrimSpace(s.cfg.ClientSecret) == "" {
		return core.Warning{Msg: "no client_secret set: /hooks/sentry accepts unverified deliveries"}
	}
	return nil
}

// Sign returns the value Sentry would send in Sentry-Hook-Signature for a
// body. Exported so tests — and anyone writing a relay — can produce a valid
// header.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifySignature checks Sentry's HMAC against the *raw* body.
//
// Sentry's own documented sample re-serializes the parsed JSON with
// JSON.stringify before hashing, which works in Node only by coincidence of
// key ordering. Hashing the bytes that actually arrived is both simpler and
// correct, so that is what this does.
func (s *Source) verifySignature(header string, body []byte) bool {
	secret := strings.TrimSpace(s.cfg.ClientSecret)
	if secret == "" {
		s.openHookWarned.Do(func() {
			s.log.Warn("sentry webhook has no client_secret set; anyone who can reach /hooks/sentry can spawn bosses " +
				"(set sources.sentry.client_secret or LOOT_SENTRY_CLIENT_SECRET)")
		})
		return true
	}
	want, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// hook is the subset of the Sentry webhook envelope Loot reads. Every resource
// shares the outer shape; only `data` differs.
type hook struct {
	Action string `json:"action"`
	Data   struct {
		Issue *issue `json:"issue"`
		Event *event `json:"event"`
	} `json:"data"`
	Installation struct {
		UUID string `json:"uuid"`
	} `json:"installation"`
}

// issue is `data.issue` on a resource=issue delivery. Note the deliberate
// mixture of conventions: Sentry sends `count` as a *string* and `userCount`
// as a number, and pairs camelCase fields with snake_case URLs. Both are
// copied faithfully rather than tidied, so a reader comparing this against a
// real payload finds what they expect.
type issue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	Permalink string `json:"permalink"`
	WebURL    string `json:"web_url"`
	Count     string `json:"count"`
	UserCount int    `json:"userCount"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Project   struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"project"`
	Metadata struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"metadata"`
	// Release fields are absent from the documented payload, but Sentry does
	// sometimes carry them; when present they key the fight to a version,
	// which is what makes "the fix rolled out" visible.
	LastRelease *struct {
		Version string `json:"version"`
	} `json:"lastRelease"`
}

// event is `data.event` on a resource=event_alert (or error) delivery. This
// one is snake_case throughout, and its timestamps are float epoch seconds.
type event struct {
	EventID   string  `json:"event_id"`
	IssueID   string  `json:"issue_id"`
	Title     string  `json:"title"`
	Message   string  `json:"message"`
	Culprit   string  `json:"culprit"`
	Level     string  `json:"level"`
	Platform  string  `json:"platform"`
	WebURL    string  `json:"web_url"`
	IssueURL  string  `json:"issue_url"`
	Timestamp float64 `json:"timestamp"`
	Release   string  `json:"release"`
	Tags      [][]any `json:"tags"`
}

// resolvedActions are the issue actions that mean "somebody dealt with it".
// `ignored` is the pre-2023 spelling of `archived`; both still arrive in the
// wild, so both are honoured.
var resolvedActions = map[string]bool{
	"resolved": true,
	"archived": true,
	"ignored":  true,
}

// activeActions are the issue actions that mean "this is happening".
var activeActions = map[string]bool{
	"created":    true,
	"unresolved": true,
	"regressed":  true,
	"reopened":   true,
}

// ErrUnknownResource is returned for a delivery Loot has no mapping for. It is
// not an error the caller should report as a failure: an installation webhook
// is a perfectly good delivery with nothing to ingest.
var ErrUnknownResource = errors.New("sentry: nothing to ingest for this resource")

// EventsFromHook maps one delivery onto Loot events. It is exported so the
// mapping is testable without HTTP, and it deliberately produces the same
// dedupe keys whichever route a delivery took.
func EventsFromHook(resource string, body []byte, now time.Time) ([]core.Event, error) {
	var h hook
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("sentry: decode body: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(h.Action))

	switch strings.ToLower(strings.TrimSpace(resource)) {
	case ResourceIssue:
		if h.Data.Issue == nil {
			return nil, fmt.Errorf("sentry: issue webhook with no data.issue")
		}
		return issueEvents(*h.Data.Issue, action, now)

	case ResourceEventAlert, ResourceError:
		if h.Data.Event == nil {
			return nil, fmt.Errorf("sentry: %s webhook with no data.event", resource)
		}
		return eventAlertEvents(*h.Data.Event, action, now)
	}
	return nil, ErrUnknownResource
}

// issueEvents maps an issue delivery. A `resolved` action produces the silent
// resolution signal *as well as* nothing else: Loot does not count a fix as a
// crash.
func issueEvents(is issue, action string, now time.Time) ([]core.Event, error) {
	id := strings.TrimSpace(is.ID)
	if id == "" {
		return nil, fmt.Errorf("sentry: issue webhook with no issue id")
	}
	app := is.Project.Slug
	if app == "" {
		app = is.Project.Name
	}

	occurred := parseSentryTime(is.LastSeen, now)
	day := core.DayOf(occurred)
	version := ""
	if is.LastRelease != nil {
		version = strings.TrimSpace(is.LastRelease.Version)
	}

	payload := core.CrashPayload{
		Version:       version,
		IssueID:       id,
		IssueTitle:    issueTitle(is),
		UsersAffected: is.UserCount,
		Kind:          core.BossKindCrash,
		URL:           issueURL(is),
		Project:       is.Project.Slug,
		Action:        action,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("sentry: encode payload: %w", err)
	}
	identity := fmt.Sprintf("%s:%s:%s", app, day, id)

	if resolvedActions[action] {
		return []core.Event{{
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
		}}, nil
	}
	if !activeActions[action] {
		// assigned, commented, and anything else Sentry adds later: a real
		// delivery about an issue that does not change how much it is
		// crashing.
		return nil, nil
	}

	// `count` is the issue's lifetime event count, not today's. Used as a
	// daily quantity it would be wrong to the tune of the issue's whole
	// history — so the delivery is deduped per (issue, day), which makes the
	// day's number "how big this issue is" rather than "how many times it
	// happened today". For a health bar that is the more useful of the two,
	// and the event-alert path below supplies the honest daily count when an
	// alert rule is wired up.
	quantity := 1
	if n, err := strconv.Atoi(strings.TrimSpace(is.Count)); err == nil && n > 0 {
		quantity = n
	}

	return []core.Event{{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       core.KindCrash,
		App:        app,
		OccurredAt: occurred,
		ObservedAt: now.UTC(),
		Day:        day,
		Quantity:   quantity,
		DedupeKey:  Name + ":issue:" + identity,
		Silent:     true,
		Payload:    json.RawMessage(encoded),
	}}, nil
}

// eventAlertEvents maps one alerting event. Each event is one crash, deduped
// on Sentry's own event id, which is what makes a day's total a genuine count.
func eventAlertEvents(ev event, action string, now time.Time) ([]core.Event, error) {
	if action != "" && action != "triggered" {
		return nil, nil
	}
	issueID := strings.TrimSpace(ev.IssueID)
	eventID := strings.TrimSpace(ev.EventID)
	if issueID == "" && eventID == "" {
		return nil, fmt.Errorf("sentry: event webhook with no issue_id or event_id")
	}

	occurred := now.UTC()
	if ev.Timestamp > 0 {
		sec := int64(ev.Timestamp)
		occurred = time.Unix(sec, int64((ev.Timestamp-float64(sec))*1e9)).UTC()
	}

	app := projectFromURL(ev.WebURL)
	payload := core.CrashPayload{
		Version:    strings.TrimSpace(ev.Release),
		IssueID:    issueID,
		IssueTitle: firstNonEmpty(ev.Title, ev.Message, ev.Culprit),
		Kind:       core.BossKindCrash,
		URL:        ev.WebURL,
		Project:    app,
		Action:     "triggered",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("sentry: encode payload: %w", err)
	}

	identity := eventID
	if identity == "" {
		identity = issueID + ":" + core.DayOf(occurred)
	}
	return []core.Event{{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       core.KindCrash,
		App:        app,
		OccurredAt: occurred,
		ObservedAt: now.UTC(),
		Day:        core.DayOf(occurred),
		Quantity:   1,
		DedupeKey:  Name + ":event:" + identity,
		Silent:     true,
		Payload:    json.RawMessage(encoded),
	}}, nil
}

// HandleWebhook implements core.WebhookHandler. Sentry gives a webhook a few
// seconds before it counts the delivery as failed, so nothing slow belongs
// here.
func (s *Source) HandleWebhook(w http.ResponseWriter, r *http.Request, emit func(core.Event)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if !s.verifySignature(r.Header.Get(HeaderSignature), body) {
		s.log.Warn("sentry webhook rejected: bad or missing " + HeaderSignature)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	resource := r.Header.Get(HeaderResource)
	events, err := EventsFromHook(resource, body, s.now())
	switch {
	case errors.Is(err, ErrUnknownResource):
		// A resource Loot has no use for is still a successful delivery.
		s.ok(w, resource, 0)
		return
	case err != nil:
		s.log.Warn("sentry webhook rejected", "error", err, "resource", resource)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	for _, ev := range events {
		emit(ev)
	}
	s.ok(w, resource, len(events))
}

func (s *Source) ok(w http.ResponseWriter, resource string, n int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "resource": resource, "emitted": n})
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// ------------------------------------------------------------------ helpers

// issueTitle prefers the exception type and value, which read as a crash
// ("TypeError: undefined is not a function"), over Sentry's own composed title.
func issueTitle(is issue) string {
	if is.Metadata.Type != "" && is.Metadata.Value != "" {
		return is.Metadata.Type + ": " + is.Metadata.Value
	}
	return firstNonEmpty(is.Title, is.Metadata.Type, is.Culprit)
}

func issueURL(is issue) string {
	return firstNonEmpty(is.WebURL, is.Permalink)
}

// projectFromURL digs the project slug out of an event's web_url. Event alerts
// do not carry the project object that issue webhooks do, and a fight with no
// app cannot be baselined.
func projectFromURL(raw string) string {
	// .../organizations/<org>/issues/... on old-style URLs, or
	// https://<org>.sentry.io/issues/... on new ones. Neither carries the
	// project, so fall back to the organization: it is at least stable, and
	// the issue title carries the rest of the story.
	trimmed := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if i := strings.Index(trimmed, "/organizations/"); i >= 0 {
		rest := trimmed[i+len("/organizations/"):]
		if j := strings.Index(rest, "/"); j > 0 {
			return rest[:j]
		}
		return rest
	}
	if i := strings.Index(trimmed, ".sentry.io"); i > 0 {
		return trimmed[:i]
	}
	return "sentry"
}

// parseSentryTime reads one of Sentry's ISO timestamps, falling back to now.
func parseSentryTime(v string, now time.Time) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return now.UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999-07:00"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return now.UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

var (
	_ core.Source         = (*Source)(nil)
	_ core.WebhookHandler = (*Source)(nil)
	_ core.Checker        = (*Source)(nil)
)
