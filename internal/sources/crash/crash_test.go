package crash_test

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
	"github.com/nickhirras/loot/internal/sources/crash"
)

var now = time.Date(2026, 6, 1, 15, 4, 5, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSource(t *testing.T, cfg config.Crash) *crash.Source {
	t.Helper()
	src, err := crash.New(cfg, quiet())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	src.Now = func() time.Time { return now }
	return src
}

// post drives the handler and collects whatever it emitted.
func post(t *testing.T, src *crash.Source, body string, headers map[string]string) (*http.Response, []core.Event) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/crash", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
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

func TestOneReport(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	resp, events := post(t, src, `{
		"app":"com.example.app","version":"2.3.1","issue_id":"a1b2",
		"title":"NPE in SyncWorker","crashes":312,"users_affected":91,
		"url":"https://example.test/i/a1b2"}`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != core.KindCrash {
		t.Errorf("kind = %q, want %q", ev.Kind, core.KindCrash)
	}
	if !ev.Silent {
		t.Error("a crash event must be silent: the boss is the drop, not the crash")
	}
	if ev.Quantity != 312 {
		t.Errorf("quantity = %d, want 312", ev.Quantity)
	}
	if ev.Day != "2026-06-01" {
		t.Errorf("day = %q, want 2026-06-01", ev.Day)
	}

	p := payloadOf(t, ev)
	if p.Version != "2.3.1" || p.IssueID != "a1b2" || p.UsersAffected != 91 {
		t.Errorf("payload = %+v", p)
	}
	if p.Kind != core.BossKindCrash {
		t.Errorf("payload kind = %q, want crash", p.Kind)
	}
}

func TestBatch(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	resp, events := post(t, src, `[
		{"app":"a","crashes":3},
		{"app":"b","crashes":4,"kind":"anr"}]`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 2 {
		t.Fatalf("emitted %d events, want 2", len(events))
	}
	if got := payloadOf(t, events[1]).Kind; got != core.BossKindANR {
		t.Errorf("second report kind = %q, want anr", got)
	}
	// Two id-less reports in one batch are two genuine reports.
	if events[0].DedupeKey == events[1].DedupeKey {
		t.Error("batch members collapsed onto one dedupe key")
	}
}

// A whole batch is validated before anything is emitted, so a typo in the
// third report does not leave the first two half-ingested.
func TestBatchIsValidatedBeforeAnythingIsEmitted(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	resp, events := post(t, src, `[
		{"app":"a","crashes":3},
		{"app":"b","crashes":4},
		{"crashes":5}]`, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(events) != 0 {
		t.Fatalf("emitted %d events from a rejected batch, want 0", len(events))
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "report 2") {
		t.Errorf("error %q does not name the offending report", msg)
	}
}

func TestValidation(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	cases := map[string]string{
		"no app":            `{"crashes":3}`,
		"unknown kind":      `{"app":"a","crashes":3,"kind":"warning"}`,
		"negative crashes":  `{"app":"a","crashes":-1}`,
		"negative users":    `{"app":"a","crashes":1,"users_affected":-2}`,
		"zero and unfixed":  `{"app":"a","crashes":0}`,
		"unparseable stamp": `{"app":"a","crashes":1,"occurred_at":"last Tuesday"}`,
		"empty body":        ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, events := post(t, src, body, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
			if len(events) != 0 {
				t.Errorf("emitted %d events, want 0", len(events))
			}
		})
	}
}

// `boss: true` is how a Crashlytics velocity alert says "this one is bad"
// without Loot having to work it out from counts it was never given.
func TestBossFlagTravelsInThePayload(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	_, events := post(t, src, `{"app":"a","crashes":4,"boss":true}`, nil)
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if !payloadOf(t, events[0]).Boss {
		t.Error("boss flag did not reach the payload")
	}
}

func TestResolvedEmitsTheSlaySignal(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	_, events := post(t, src, `{"app":"a","version":"2.0","crashes":0,"resolved":true}`, nil)
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if events[0].Kind != core.KindCrashResolved {
		t.Errorf("kind = %q, want %q", events[0].Kind, core.KindCrashResolved)
	}

	// A report that both counts crashes and closes the issue emits both.
	_, events = post(t, src, `{"app":"a","version":"2.0","crashes":5,"resolved":true}`, nil)
	if len(events) != 2 {
		t.Fatalf("emitted %d events, want 2", len(events))
	}
}

// A day-only timestamp is enough: a crash reporter that knows the day should
// not have to invent a time.
func TestOccurredAtAcceptsABareDay(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	_, events := post(t, src, `{"app":"a","crashes":2,"occurred_at":"2026-05-20"}`, nil)
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if events[0].Day != "2026-05-20" {
		t.Errorf("day = %q, want 2026-05-20", events[0].Day)
	}
}

// An id makes a retry idempotent; without one, two posts are two reports.
func TestDedupeIdentity(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	body := `{"app":"a","version":"2.0","crashes":3,"id":"velocity-1"}`
	_, first := post(t, src, body, nil)
	_, second := post(t, src, body, nil)
	if first[0].DedupeKey != second[0].DedupeKey {
		t.Errorf("a retry with the same id produced two keys: %q vs %q",
			first[0].DedupeKey, second[0].DedupeKey)
	}

	body = `{"app":"a","version":"2.0","crashes":3}`
	_, first = post(t, src, body, nil)
	_, second = post(t, src, body, nil)
	if first[0].DedupeKey == second[0].DedupeKey {
		t.Error("two id-less posts collapsed onto one key; a second real crash would be lost")
	}
}

func TestSecret(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true, Secret: "hunter2"})

	resp, events := post(t, src, `{"app":"a","crashes":1}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}
	if len(events) != 0 {
		t.Fatal("an unauthenticated request emitted events")
	}

	for _, header := range []string{"Bearer hunter2", "hunter2"} {
		resp, events = post(t, src, `{"app":"a","crashes":1}`, map[string]string{"Authorization": header})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Authorization %q: status = %d, want 200", header, resp.StatusCode)
		}
		if len(events) != 1 {
			t.Errorf("Authorization %q: emitted %d events, want 1", header, len(events))
		}
	}
}

func TestCheckWarnsAboutAnOpenDoor(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	err := src.Check(t.Context())
	if !core.IsWarning(err) {
		t.Fatalf("Check() = %v, want a warning about the missing secret", err)
	}

	src = newSource(t, config.Crash{Enabled: true, Secret: "s"})
	if err := src.Check(t.Context()); err != nil {
		t.Fatalf("Check() with a secret = %v, want nil", err)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	src := newSource(t, config.Crash{Enabled: true})
	req := httptest.NewRequest(http.MethodGet, "/hooks/crash", nil)
	rec := httptest.NewRecorder()
	src.HandleWebhook(rec, req, func(core.Event) { t.Fatal("a GET emitted an event") })
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
