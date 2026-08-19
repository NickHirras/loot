package sentry_test

import (
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
	"github.com/nickhirras/loot/internal/sources/sentry"
)

const secret = "cl13nt-s3cr3t"

var now = time.Date(2026, 6, 1, 15, 4, 5, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSource(t *testing.T, cfg config.Sentry) *sentry.Source {
	t.Helper()
	src, err := sentry.New(cfg, quiet())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	src.Now = func() time.Time { return now }
	return src
}

// deliver signs the body the way Sentry does and drives the handler.
func deliver(t *testing.T, src *sentry.Source, resource, body string, sign bool) (*http.Response, []core.Event) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/sentry", strings.NewReader(body))
	req.Header.Set(sentry.HeaderResource, resource)
	if sign {
		req.Header.Set(sentry.HeaderSignature, sentry.Sign(secret, []byte(body)))
	}
	rec := httptest.NewRecorder()

	var got []core.Event
	src.HandleWebhook(rec, req, func(ev core.Event) { got = append(got, ev) })
	return rec.Result(), got
}

func payloadOf(t *testing.T, ev core.Event) core.CrashPayload {
	t.Helper()
	var p core.CrashPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return p
}

// issueBody is the documented shape, trimmed to the fields Loot reads —
// including the two traps: `count` is a string and `userCount` a number.
func issueBody(action string) string {
	return `{
	  "action": "` + action + `",
	  "installation": {"uuid": "24b397fc-a86e-43ef-9297-949e21b82480"},
	  "data": {"issue": {
	    "id": "1234567890",
	    "shortId": "PYTHON-Y",
	    "title": "TypeError: undefined is not a function",
	    "culprit": "app/sync(worker)",
	    "level": "error",
	    "status": "unresolved",
	    "permalink": "https://example-org.sentry.io/issues/1234567890/",
	    "web_url": "https://example-org.sentry.io/issues/1234567890/",
	    "project": {"id": "112", "name": "python", "slug": "backend"},
	    "metadata": {"type": "TypeError", "value": "undefined is not a function"},
	    "count": "412",
	    "userCount": 96,
	    "firstSeen": "2026-05-30T20:56:00.679000+00:00",
	    "lastSeen": "2026-05-31T20:56:00.738000+00:00"
	  }},
	  "actor": {"type": "application", "id": "example-app", "name": "Example App"}
	}`
}

func TestSignatureIsRequired(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})

	resp, events := deliver(t, src, sentry.ResourceIssue, issueBody("created"), false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned status = %d, want 401", resp.StatusCode)
	}
	if len(events) != 0 {
		t.Fatal("an unsigned delivery emitted events")
	}

	resp, events = deliver(t, src, sentry.ResourceIssue, issueBody("created"), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 1 {
		t.Fatalf("signed delivery emitted %d events, want 1", len(events))
	}
}

// The HMAC is over the exact bytes that arrived. A body that differs by one
// character must not verify.
func TestSignatureCoversTheBody(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	body := issueBody("created")

	req := httptest.NewRequest(http.MethodPost, "/hooks/sentry", strings.NewReader(body+" "))
	req.Header.Set(sentry.HeaderResource, sentry.ResourceIssue)
	req.Header.Set(sentry.HeaderSignature, sentry.Sign(secret, []byte(body)))
	rec := httptest.NewRecorder()
	src.HandleWebhook(rec, req, func(core.Event) { t.Fatal("a tampered body emitted an event") })
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// Without a configured secret the endpoint is open — deliberately, so a first
// run is not a puzzle — but it says so, and Check reports it.
func TestNoSecretAcceptsAndWarns(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true})
	resp, events := deliver(t, src, sentry.ResourceIssue, issueBody("created"), false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if !core.IsWarning(src.Check(t.Context())) {
		t.Error("Check did not warn about the unverified endpoint")
	}
}

func TestIssueCreatedMapsToACrash(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	_, events := deliver(t, src, sentry.ResourceIssue, issueBody("created"), true)
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	ev := events[0]

	if ev.Kind != core.KindCrash {
		t.Errorf("kind = %q, want %q", ev.Kind, core.KindCrash)
	}
	if !ev.Silent {
		t.Error("a crash event must be silent")
	}
	if ev.App != "backend" {
		t.Errorf("app = %q, want the project slug", ev.App)
	}
	if ev.Quantity != 412 {
		t.Errorf("quantity = %d, want 412 (count arrives as a string)", ev.Quantity)
	}
	if ev.Day != "2026-05-31" {
		t.Errorf("day = %q, want the issue's lastSeen day", ev.Day)
	}

	p := payloadOf(t, ev)
	if p.IssueID != "1234567890" {
		t.Errorf("issue_id = %q", p.IssueID)
	}
	if p.UsersAffected != 96 {
		t.Errorf("users_affected = %d, want 96", p.UsersAffected)
	}
	if p.IssueTitle != "TypeError: undefined is not a function" {
		t.Errorf("issue_title = %q", p.IssueTitle)
	}
	if p.URL == "" || p.Project != "backend" || p.Action != "created" {
		t.Errorf("payload provenance = %+v", p)
	}
}

func TestResolvedActionsSlayTheBoss(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	// `archived` is the current spelling and `ignored` the pre-2023 one; both
	// still arrive in the wild.
	for _, action := range []string{"resolved", "archived", "ignored"} {
		_, events := deliver(t, src, sentry.ResourceIssue, issueBody(action), true)
		if len(events) != 1 {
			t.Fatalf("%s: emitted %d events, want 1", action, len(events))
		}
		if events[0].Kind != core.KindCrashResolved {
			t.Errorf("%s: kind = %q, want %q", action, events[0].Kind, core.KindCrashResolved)
		}
	}
}

// An assignment is a real delivery about an issue that does not change how
// much it is crashing.
func TestUninterestingActionsEmitNothing(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	resp, events := deliver(t, src, sentry.ResourceIssue, issueBody("assigned"), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 0 {
		t.Fatalf("emitted %d events for an assignment, want 0", len(events))
	}
}

const eventAlertBody = `{
  "action": "triggered",
  "data": {
    "event": {
      "event_id": "e4874d664c3540c1a32eab185f12c5ab",
      "issue_id": "1117540176",
      "title": "ReferenceError: heck is not defined",
      "culprit": "?(<anonymous>)",
      "level": "error",
      "platform": "javascript",
      "release": "frontend@4.2.0",
      "web_url": "https://sentry.io/organizations/test-org/issues/1117540176/events/e4874d664c3540c1a32eab185f12c5ab/",
      "timestamp": 1780000000.677
    },
    "triggered_rule": "Very Important Alert!"
  },
  "actor": {"type": "application"}
}`

// Each alerting event is one crash, deduped on Sentry's own event id — which
// is what makes a day's total a genuine count rather than a lifetime one.
func TestEventAlertMapsToOneCrash(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	_, events := deliver(t, src, sentry.ResourceEventAlert, eventAlertBody, true)
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Quantity != 1 {
		t.Errorf("quantity = %d, want 1", ev.Quantity)
	}
	if !strings.Contains(ev.DedupeKey, "e4874d664c3540c1a32eab185f12c5ab") {
		t.Errorf("dedupe key %q does not carry the event id", ev.DedupeKey)
	}
	if ev.App != "test-org" {
		t.Errorf("app = %q, want the organization dug out of web_url", ev.App)
	}
	p := payloadOf(t, ev)
	if p.Version != "frontend@4.2.0" {
		t.Errorf("version = %q, want the release", p.Version)
	}
	if p.IssueID != "1117540176" {
		t.Errorf("issue_id = %q", p.IssueID)
	}
}

// An installation webhook is a perfectly good delivery with nothing to ingest.
func TestUnknownResourceIsAcknowledged(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	resp, events := deliver(t, src, "installation", `{"action":"created","data":{}}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 0 {
		t.Fatalf("emitted %d events, want 0", len(events))
	}
}

func TestMalformedIssueIsRejected(t *testing.T) {
	src := newSource(t, config.Sentry{Enabled: true, ClientSecret: secret})
	resp, _ := deliver(t, src, sentry.ResourceIssue, `{"action":"created","data":{}}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
