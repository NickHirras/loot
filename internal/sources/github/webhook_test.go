package github

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
)

// post delivers a webhook and returns the response plus whatever was emitted.
func post(t *testing.T, s *Source, event, body, signature string) (*httptest.ResponseRecorder, []core.Event) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rec := httptest.NewRecorder()

	var got []core.Event
	s.HandleWebhook(rec, req, func(e core.Event) { got = append(got, e) })
	return rec, got
}

func hookSource(t *testing.T, secret string) *Source {
	t.Helper()
	s, err := New(config.GitHub{Repos: []string{"o/r"}, WebhookSecret: secret},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Now = func() time.Time { return testNow }
	return s
}

const starHook = `{"action":"created","starred_at":"2026-08-17T00:00:00Z",
  "repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
  "sender":{"login":"u105"}}`

func TestWebhookSignature(t *testing.T) {
	s := hookSource(t, "s3cret")

	// Valid signature.
	rec, events := post(t, s, "star", starHook, Sign("s3cret", []byte(starHook)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid signature rejected: %d %s", rec.Code, rec.Body)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}

	// Wrong secret, malformed value and no header at all are all 401.
	for name, sig := range map[string]string{
		"wrong secret":  Sign("nope", []byte(starHook)),
		"not hex":       "sha256=zzzz",
		"no prefix":     strings.TrimPrefix(Sign("s3cret", []byte(starHook)), "sha256="),
		"absent":        "",
		"tampered body": Sign("s3cret", []byte(starHook+" ")),
	} {
		rec, events := post(t, s, "star", starHook, sig)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, rec.Code)
		}
		if len(events) != 0 {
			t.Errorf("%s: emitted %d events despite 401", name, len(events))
		}
	}
}

func TestWebhookWithoutSecretAccepts(t *testing.T) {
	s := hookSource(t, "")
	rec, events := post(t, s, "star", starHook, "")
	if rec.Code != http.StatusOK || len(events) != 1 {
		t.Fatalf("unsecured hook: %d %s (%d events)", rec.Code, rec.Body, len(events))
	}
}

func TestWebhookPing(t *testing.T) {
	s := hookSource(t, "")
	body := `{"zen":"Non-blocking is better than blocking.","hook_id":1,"repository":{"full_name":"o/r"}}`
	rec, events := post(t, s, "ping", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ping: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "pong" {
		t.Fatalf("ping body = %q, want pong", rec.Body.String())
	}
	if len(events) != 0 {
		t.Fatalf("ping emitted events: %v", keysOf(events))
	}
}

func TestWebhookEventMapping(t *testing.T) {
	s := hookSource(t, "")

	cases := []struct {
		name  string
		event string
		body  string
		want  []string
	}{
		{"star created", "star", starHook, []string{"github:star:o/r:u105"}},
		{"star deleted", "star", `{"action":"deleted","repository":{"full_name":"o/r"},"sender":{"login":"u105"}}`, nil},
		{"watch started", "watch", `{"action":"started","repository":{"full_name":"o/r"},"sender":{"login":"zed"}}`,
			[]string{"github:star:o/r:zed"}},
		{"fork", "fork", `{"repository":{"full_name":"o/r"},"sender":{"login":"alice"},
			"forkee":{"full_name":"alice/r","html_url":"https://github.com/alice/r","created_at":"2026-08-18T01:00:00Z","owner":{"login":"alice"}}}`,
			[]string{"github:fork:o/r:alice"}},
		{"issue opened", "issues", `{"action":"opened","repository":{"full_name":"o/r"},
			"issue":{"number":7,"title":"Crash on launch","html_url":"https://github.com/o/r/issues/7","created_at":"2026-08-18T10:00:00Z","user":{"login":"ann"}}}`,
			[]string{"github:issue_opened:o/r:7"}},
		{"issue reopened", "issues", `{"action":"reopened","repository":{"full_name":"o/r"},
			"issue":{"number":7,"title":"Crash","created_at":"2026-08-18T10:00:00Z","user":{"login":"ann"}}}`,
			[]string{"github:issue_opened:o/r:7"}},
		{"issue closed", "issues", `{"action":"closed","repository":{"full_name":"o/r"},
			"issue":{"number":5,"title":"Old bug","closed_at":"2026-08-18T09:00:00Z","user":{"login":"bob"}}}`,
			[]string{"github:issue_closed:o/r:5"}},
		{"issue labeled", "issues", `{"action":"labeled","repository":{"full_name":"o/r"},"issue":{"number":5}}`, nil},
		{"pr opened", "pull_request", `{"action":"opened","repository":{"full_name":"o/r"},
			"pull_request":{"number":9,"title":"Add dark mode","created_at":"2026-08-17T08:00:00Z","user":{"login":"cyd"}}}`,
			[]string{"github:pr_opened:o/r:9"}},
		{"pr merged", "pull_request", `{"action":"closed","repository":{"full_name":"o/r"},
			"pull_request":{"number":8,"title":"Fix the thing","merged":true,"merged_at":"2026-08-18T11:00:00Z","user":{"login":"dee"}}}`,
			[]string{"github:pr_merged:o/r:8"}},
		{"pr closed unmerged", "pull_request", `{"action":"closed","repository":{"full_name":"o/r"},
			"pull_request":{"number":4,"title":"Abandoned","merged":false,"closed_at":"2026-08-18T06:00:00Z"}}`, nil},
		{"release published", "release", `{"action":"published","repository":{"full_name":"o/r"},
			"release":{"id":3,"tag_name":"v1.2.0","name":"Sharpened","published_at":"2026-08-18T05:00:00Z"}}`,
			[]string{"github:release:o/r:v1.2.0"}},
		{"release edited", "release", `{"action":"edited","repository":{"full_name":"o/r"},
			"release":{"id":3,"tag_name":"v1.2.0"}}`, nil},
		{"release draft published", "release", `{"action":"published","repository":{"full_name":"o/r"},
			"release":{"id":4,"tag_name":"v2","draft":true}}`, nil},
		{"unknown event", "discussion", `{"action":"created","repository":{"full_name":"o/r"}}`, nil},
		{"no repository", "star", `{"action":"created","sender":{"login":"u1"}}`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, events := post(t, s, tc.event, tc.body, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			got := keysOf(events)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("keys = %v, want %v", got, tc.want)
			}
			for _, e := range events {
				if e.Source != Name || e.App != "o/r" || e.IsLedger || e.Silent {
					t.Fatalf("event shape wrong: %+v", e)
				}
			}
		})
	}
}

// TestWebhookAndPollingAgreeOnDedupeKeys is the property that lets both paths
// run at once: whichever sees an event first wins, and the other collapses.
func TestWebhookAndPollingAgreeOnDedupeKeys(t *testing.T) {
	api := &fakeAPI{
		stars:  []stargazer{star(t, "u105", "2026-08-17T00:00:00Z")},
		issues: issuesFixture,
		pulls: map[int]string{
			8: `{"number":8,"merged":true,"merged_at":"2026-08-18T11:00:00Z"}`,
			4: `{"number":4,"merged":false,"merged_at":null}`,
		},
		forks: `[{"full_name":"alice/r","created_at":"2026-08-18T01:00:00Z","owner":{"login":"alice"}}]`,
		releases: `[{"id":3,"tag_name":"v1.2.0","name":"Sharpened","draft":false,
			"published_at":"2026-08-18T05:00:00Z"}]`,
	}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	polled, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	polledKeys := map[string]bool{}
	for _, e := range polled {
		polledKeys[e.DedupeKey] = true
	}

	hooks := []struct {
		event, body, key string
	}{
		{"star", starHook, "github:star:o/r:u105"},
		{"issues", `{"action":"opened","repository":{"full_name":"o/r"},
			"issue":{"number":7,"title":"Crash on launch","created_at":"2026-08-18T10:00:00Z","user":{"login":"ann"}}}`,
			"github:issue_opened:o/r:7"},
		{"issues", `{"action":"closed","repository":{"full_name":"o/r"},
			"issue":{"number":5,"title":"Old bug","closed_at":"2026-08-18T09:00:00Z","user":{"login":"bob"}}}`,
			"github:issue_closed:o/r:5"},
		{"pull_request", `{"action":"opened","repository":{"full_name":"o/r"},
			"pull_request":{"number":9,"title":"Add dark mode","created_at":"2026-08-17T08:00:00Z","user":{"login":"cyd"}}}`,
			"github:pr_opened:o/r:9"},
		{"pull_request", `{"action":"closed","repository":{"full_name":"o/r"},
			"pull_request":{"number":8,"title":"Fix the thing","merged":true,"merged_at":"2026-08-18T11:00:00Z","user":{"login":"dee"}}}`,
			"github:pr_merged:o/r:8"},
		{"fork", `{"repository":{"full_name":"o/r"},
			"forkee":{"full_name":"alice/r","created_at":"2026-08-18T01:00:00Z","owner":{"login":"alice"}}}`,
			"github:fork:o/r:alice"},
		{"release", `{"action":"published","repository":{"full_name":"o/r"},
			"release":{"id":3,"tag_name":"v1.2.0","name":"Sharpened","published_at":"2026-08-18T05:00:00Z"}}`,
			"github:release:o/r:v1.2.0"},
	}

	for _, h := range hooks {
		rec, events := post(t, s, h.event, h.body, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s hook: %d %s", h.event, rec.Code, rec.Body)
		}
		if len(events) != 1 {
			t.Fatalf("%s hook emitted %d events", h.event, len(events))
		}
		if events[0].DedupeKey != h.key {
			t.Fatalf("%s hook key = %q, want %q", h.event, events[0].DedupeKey, h.key)
		}
		if !polledKeys[h.key] {
			t.Fatalf("polling never produced %q, so the two paths would double up", h.key)
		}
		// The kinds must agree too, or the rules would title them differently.
		polledEvent := findKey(t, polled, h.key)
		if polledEvent.Kind != events[0].Kind {
			t.Fatalf("%s: polled kind %q vs webhook kind %q", h.key, polledEvent.Kind, events[0].Kind)
		}
	}
}

func TestWebhookRejectsGetAndGarbage(t *testing.T) {
	s := hookSource(t, "")

	req := httptest.NewRequest(http.MethodGet, "/hooks/github", nil)
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req, func(core.Event) { t.Fatal("GET emitted an event") })
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", rec.Code)
	}

	rec2, events := post(t, s, "star", `{not json`, "")
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("garbage body: %d, want 400", rec2.Code)
	}
	if len(events) != 0 {
		t.Fatal("garbage body emitted events")
	}
}

func TestWebhookResponseBody(t *testing.T) {
	s := hookSource(t, "")
	rec, _ := post(t, s, "star", starHook, "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body)
	}
	if got["ok"] != true || got["event"] != "star" || got["emitted"] != float64(1) {
		t.Fatalf("response = %v", got)
	}
}
