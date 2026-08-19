package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
)

var testNow = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return v
}

// fakeAPI is a stand-in for api.github.com: enough of the five endpoints Loot
// reads to exercise pagination, classification and rate limiting.
type fakeAPI struct {
	t *testing.T

	// stars is the full stargazer list, oldest first, exactly as GitHub
	// returns it.
	stars    []stargazer
	issues   string
	releases string
	forks    string
	pulls    map[int]string
	// issuePages/forkPages, when set, are served one per ?page=N and take
	// precedence over the single-page fixtures above. Anything past the last
	// entry is an empty list, exactly as GitHub answers.
	issuePages []string
	forkPages  []string

	// rateRemaining/rateReset, when set, are sent on every response.
	rateRemaining string
	rateReset     string
	// status, when non-zero, replaces the status of every response.
	status int
	// starsRequireAuth mirrors api.github.com since Aug 2026: the stargazers
	// listing answers 401 unless an Authorization header is present.
	starsRequireAuth bool

	// Observed traffic.
	starPages      []int
	issuePagesSeen []int
	forkPagesSeen  []int
	requests       int
	lastAuth       string
	lastUA         string
	starAccepts    []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++
	f.lastAuth = r.Header.Get("Authorization")
	f.lastUA = r.Header.Get("User-Agent")

	if f.rateRemaining != "" {
		w.Header().Set("X-RateLimit-Remaining", f.rateRemaining)
	}
	if f.rateReset != "" {
		w.Header().Set("X-RateLimit-Reset", f.rateReset)
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"message":"nope"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path

	switch {
	case path == "/repos/o/r":
		fmt.Fprintf(w, `{"full_name":"o/r","stargazers_count":%d,"forks_count":2}`, len(f.stars))

	case path == "/repos/o/r/stargazers":
		if f.starsRequireAuth && r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Requires authentication","status":"401"}`)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		f.starPages = append(f.starPages, page)
		f.starAccepts = append(f.starAccepts, r.Header.Get("Accept"))
		start := (page - 1) * perPage
		end := min(start+perPage, len(f.stars))
		if start >= len(f.stars) {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.stars[start:end])

	case path == "/repos/o/r/issues":
		page := pageOf(r)
		f.issuePagesSeen = append(f.issuePagesSeen, page)
		_, _ = io.WriteString(w, orEmpty(pageBody(f.issuePages, f.issues, page)))

	case path == "/repos/o/r/releases":
		_, _ = io.WriteString(w, orEmpty(f.releases))

	case path == "/repos/o/r/forks":
		page := pageOf(r)
		f.forkPagesSeen = append(f.forkPagesSeen, page)
		_, _ = io.WriteString(w, orEmpty(pageBody(f.forkPages, f.forks, page)))

	case strings.HasPrefix(path, "/repos/o/r/pulls/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(path, "/repos/o/r/pulls/"))
		body, ok := f.pulls[n]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = io.WriteString(w, body)

	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}
}

// pageOf reads ?page=N, defaulting to the first.
func pageOf(r *http.Request) int {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	return page
}

// pageBody serves the paged fixture when there is one, and otherwise the
// single-page fixture on page one and nothing after it.
func pageBody(pages []string, single string, page int) string {
	if len(pages) == 0 {
		if page == 1 {
			return single
		}
		return "[]"
	}
	if page > len(pages) {
		return "[]"
	}
	return pages[page-1]
}

func orEmpty(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

// newTestSource wires a Source at the fake API with a frozen clock.
func newTestSource(t *testing.T, api *fakeAPI, cfg config.GitHub) (*Source, *httptest.Server) {
	t.Helper()
	api.t = t
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	if len(cfg.Repos) == 0 {
		cfg.Repos = []string{"o/r"}
	}
	s, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.BaseURL = srv.URL
	s.Now = func() time.Time { return testNow }
	return s, srv
}

// star builds one stargazers entry.
func star(t *testing.T, login, at string) stargazer {
	t.Helper()
	var sg stargazer
	sg.StarredAt = mustTime(t, at)
	sg.User.Login = login
	sg.User.HTMLURL = "https://github.com/" + login
	return sg
}

// filler pads the stargazer list with ancient stars so pagination is real.
func filler(t *testing.T, n int) []stargazer {
	t.Helper()
	out := make([]stargazer, 0, n)
	for i := range n {
		out = append(out, star(t, fmt.Sprintf("old%d", i), "2020-01-01T00:00:00Z"))
	}
	return out
}

func keysOf(events []core.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.DedupeKey)
	}
	return out
}

func byKind(events []core.Event, kind string) []core.Event {
	var out []core.Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func payloadOf(t *testing.T, e core.Event) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatalf("payload of %s: %v", e.DedupeKey, err)
	}
	return m
}

// --------------------------------------------------------------------- stars

func TestPollStarsWalksBackwardsAndHonoursBackfillWindow(t *testing.T) {
	// 105 stars: page 1 is the 100 oldest, page 2 the five newest. Only page 2
	// should ever be fetched, because its oldest entry predates the window.
	stars := append(filler(t, 100),
		star(t, "u101", "2026-06-01T00:00:00Z"), // outside the 30 day window
		star(t, "u102", "2026-07-25T00:00:00Z"),
		star(t, "u103", "2026-08-01T00:00:00Z"),
		star(t, "u104", "2026-08-10T00:00:00Z"),
		star(t, "u105", "2026-08-17T00:00:00Z"),
	)
	api := &fakeAPI{stars: stars}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, newState, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	got := keysOf(byKind(events, "star"))
	want := []string{
		"github:star:o/r:u102",
		"github:star:o/r:u103",
		"github:star:o/r:u104",
		"github:star:o/r:u105",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("star keys = %v, want %v", got, want)
	}

	// Page 1 must not be touched: walking it would cost a request per hundred
	// stars on every poll, forever.
	if len(api.starPages) != 1 || api.starPages[0] != 2 {
		t.Fatalf("star pages fetched = %v, want [2]", api.starPages)
	}
	if len(api.starAccepts) == 0 || !strings.Contains(api.starAccepts[0], "star+json") {
		t.Fatalf("stargazers Accept = %v, want the star+json media type", api.starAccepts)
	}
	if api.lastAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", api.lastAuth)
	}
	if api.lastUA != "loot" {
		t.Fatalf("User-Agent = %q, want loot", api.lastUA)
	}

	// First run records where the repo already stands instead of firing the
	// 10/50/100 milestones retroactively.
	if ms := byKind(events, "stars_milestone"); len(ms) != 0 {
		t.Fatalf("first run emitted milestones %v, want none", keysOf(ms))
	}

	first := byKind(events, "star")[0]
	if first.App != "o/r" || first.Quantity != 1 || first.IsLedger || first.Silent {
		t.Fatalf("star event shape wrong: %+v", first)
	}
	if !first.OccurredAt.Equal(mustTime(t, "2026-07-25T00:00:00Z")) {
		t.Fatalf("OccurredAt = %s, want the starred_at", first.OccurredAt)
	}
	if u := payloadOf(t, first)["user"]; u != "u102" {
		t.Fatalf("payload user = %v, want u102", u)
	}

	st := decodeState(newState)
	rs := st.Repos["o/r"]
	if rs == nil || rs.StarsCount != 105 || rs.LastMilestone != 100 {
		t.Fatalf("state = %+v, want 105 stars and milestone baseline 100", rs)
	}
	if rs.LastStarAt != "2026-08-17T00:00:00Z" {
		t.Fatalf("LastStarAt = %q, want the newest starred_at", rs.LastStarAt)
	}
}

func TestPollStarsMilestoneOnSecondRun(t *testing.T) {
	api := &fakeAPI{stars: append(filler(t, 100), star(t, "u101", "2026-08-17T00:00:00Z"))}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	_, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}

	// The repo grows past 250 before the next poll.
	api.stars = append(api.stars, filler(t, 148)...)
	api.stars = append(api.stars, star(t, "u250", "2026-08-18T09:00:00Z"))
	if len(api.stars) != 250 {
		t.Fatalf("fixture has %d stars, want 250", len(api.stars))
	}

	events, state2, err := s.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}

	ms := byKind(events, "stars_milestone")
	if len(ms) != 1 || ms[0].DedupeKey != "github:stars_milestone:o/r:250" {
		t.Fatalf("milestones = %v, want the 250 milestone only", keysOf(ms))
	}
	if ms[0].Quantity != 250 {
		t.Fatalf("milestone quantity = %d, want 250", ms[0].Quantity)
	}
	if got := keysOf(byKind(events, "star")); len(got) != 1 || got[0] != "github:star:o/r:u250" {
		t.Fatalf("stars = %v, want only the new one", got)
	}

	// A third poll with no growth must not repeat the milestone.
	events3, _, err := s.Poll(context.Background(), state2)
	if err != nil {
		t.Fatalf("third Poll: %v", err)
	}
	if ms := byKind(events3, "stars_milestone"); len(ms) != 0 {
		t.Fatalf("milestone repeated: %v", keysOf(ms))
	}
}

// -------------------------------------------------------------- issues & PRs

const issuesFixture = `[
  {"number":7,"title":"Crash on launch","state":"open","html_url":"https://github.com/o/r/issues/7",
   "created_at":"2026-08-18T10:00:00Z","closed_at":null,"user":{"login":"ann"}},
  {"number":5,"title":"Old bug","state":"closed","html_url":"https://github.com/o/r/issues/5",
   "created_at":"2026-06-01T00:00:00Z","closed_at":"2026-08-18T09:00:00Z","user":{"login":"bob"}},
  {"number":9,"title":"Add dark mode","state":"open","html_url":"https://github.com/o/r/issues/9",
   "created_at":"2026-08-17T08:00:00Z","closed_at":null,"user":{"login":"cyd"},
   "pull_request":{"url":"https://api.github.com/repos/o/r/pulls/9","html_url":"https://github.com/o/r/pull/9","merged_at":null}},
  {"number":8,"title":"Fix the thing","state":"closed","html_url":"https://github.com/o/r/issues/8",
   "created_at":"2026-05-01T00:00:00Z","closed_at":"2026-08-18T11:00:00Z","user":{"login":"dee"},
   "pull_request":{"url":"https://api.github.com/repos/o/r/pulls/8","html_url":"https://github.com/o/r/pull/8","merged_at":null}},
  {"number":6,"title":"Speed it up","state":"closed","html_url":"https://github.com/o/r/issues/6",
   "created_at":"2026-05-02T00:00:00Z","closed_at":"2026-08-18T07:00:00Z","user":{"login":"eve"},
   "pull_request":{"url":"https://api.github.com/repos/o/r/pulls/6","html_url":"https://github.com/o/r/pull/6","merged_at":"2026-08-18T07:00:00Z"}},
  {"number":4,"title":"Abandoned","state":"closed","html_url":"https://github.com/o/r/issues/4",
   "created_at":"2026-04-01T00:00:00Z","closed_at":"2026-08-18T06:00:00Z","user":{"login":"fay"},
   "pull_request":{"url":"https://api.github.com/repos/o/r/pulls/4","html_url":"https://github.com/o/r/pull/4","merged_at":null}},
  {"number":3,"title":"Ancient","state":"closed","html_url":"https://github.com/o/r/issues/3",
   "created_at":"2026-01-01T00:00:00Z","closed_at":"2026-01-02T00:00:00Z","user":{"login":"gus"}}
]`

func TestPollIssuesClassifiesIssuesAndPullRequests(t *testing.T) {
	api := &fakeAPI{
		issues: issuesFixture,
		pulls: map[int]string{
			8: `{"number":8,"merged":true,"merged_at":"2026-08-18T11:00:00Z"}`,
			4: `{"number":4,"merged":false,"merged_at":null}`,
		},
	}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, newState, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	want := map[string][]string{
		"issue_opened": {"github:issue_opened:o/r:7"},
		"issue_closed": {"github:issue_closed:o/r:5"},
		"pr_opened":    {"github:pr_opened:o/r:9"},
		"pr_merged":    {"github:pr_merged:o/r:6", "github:pr_merged:o/r:8"},
	}
	for kind, wantKeys := range want {
		got := keysOf(byKind(events, kind))
		sortStrings(got)
		if strings.Join(got, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("%s = %v, want %v", kind, got, wantKeys)
		}
	}

	// A pull request must never masquerade as a quest, and an abandoned PR is
	// not a merge.
	for _, e := range byKind(events, "issue_opened") {
		if strings.HasSuffix(e.DedupeKey, ":9") || strings.HasSuffix(e.DedupeKey, ":8") {
			t.Errorf("pull request %s emitted as issue_opened", e.DedupeKey)
		}
	}
	for _, e := range byKind(events, "issue_closed") {
		if !strings.HasSuffix(e.DedupeKey, ":5") {
			t.Errorf("unexpected issue_closed %s", e.DedupeKey)
		}
	}

	opened := byKind(events, "issue_opened")[0]
	p := payloadOf(t, opened)
	if p["number"] != float64(7) || p["title"] != "Crash on launch" || p["user"] != "ann" {
		t.Fatalf("issue payload = %v, want number/title/user for #7", p)
	}
	if p["url"] != "https://github.com/o/r/issues/7" {
		t.Fatalf("issue payload url = %v", p["url"])
	}
	if !opened.OccurredAt.Equal(mustTime(t, "2026-08-18T10:00:00Z")) {
		t.Fatalf("issue_opened OccurredAt = %s", opened.OccurredAt)
	}

	merged8 := findKey(t, events, "github:pr_merged:o/r:8")
	if !merged8.OccurredAt.Equal(mustTime(t, "2026-08-18T11:00:00Z")) {
		t.Fatalf("pr_merged #8 OccurredAt = %s, want the merged_at from /pulls/8", merged8.OccurredAt)
	}

	if lp := decodeState(newState).Repos["o/r"].LastIssuePoll; lp != formatTime(testNow) {
		t.Fatalf("LastIssuePoll = %q, want %q", lp, formatTime(testNow))
	}
}

// ------------------------------------------------------------------ releases

func TestPollReleases(t *testing.T) {
	api := &fakeAPI{releases: `[
	  {"id":9,"tag_name":"v9-draft","name":"draft","draft":true,"prerelease":false,"published_at":null},
	  {"id":3,"tag_name":"v1.2.0","name":"Sharpened","html_url":"https://github.com/o/r/releases/v1.2.0",
	   "draft":false,"prerelease":false,"published_at":"2026-08-18T05:00:00Z"},
	  {"id":2,"tag_name":"v1.2.0-rc1","name":"Release candidate","html_url":"https://github.com/o/r/releases/v1.2.0-rc1",
	   "draft":false,"prerelease":true,"published_at":"2026-08-01T05:00:00Z"},
	  {"id":1,"tag_name":"v1.0.0","name":"First","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z"}
	]`}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	got := keysOf(byKind(events, "release"))
	want := []string{"github:release:o/r:v1.2.0-rc1", "github:release:o/r:v1.2.0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("releases = %v, want %v (draft skipped, pre-window skipped)", got, want)
	}

	rc := findKey(t, events, "github:release:o/r:v1.2.0-rc1")
	if p := payloadOf(t, rc); p["prerelease"] != true || p["tag"] != "v1.2.0-rc1" {
		t.Fatalf("prerelease payload = %v", p)
	}
	final := findKey(t, events, "github:release:o/r:v1.2.0")
	if p := payloadOf(t, final); p["name"] != "Sharpened" || p["prerelease"] != false {
		t.Fatalf("release payload = %v", p)
	}

	rs := decodeState(state).Repos["o/r"]
	if rs.LastReleaseTag != "v1.2.0" || rs.LastReleaseID != 3 || rs.LastReleaseAt != "2026-08-18T05:00:00Z" {
		t.Fatalf("release cursor = %+v", rs)
	}

	// Nothing new the second time round.
	events2, _, err := s.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if got := byKind(events2, "release"); len(got) != 0 {
		t.Fatalf("re-emitted releases: %v", keysOf(got))
	}
}

// --------------------------------------------------------------------- forks

func TestPollForks(t *testing.T) {
	api := &fakeAPI{forks: `[
	  {"full_name":"alice/r","html_url":"https://github.com/alice/r","created_at":"2026-08-18T01:00:00Z","owner":{"login":"alice"}},
	  {"full_name":"bob/r","html_url":"https://github.com/bob/r","created_at":"2026-01-01T00:00:00Z","owner":{"login":"bob"}}
	]`}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	got := keysOf(byKind(events, "fork"))
	if len(got) != 1 || got[0] != "github:fork:o/r:alice" {
		t.Fatalf("forks = %v, want alice only", got)
	}
	if rs := decodeState(state).Repos["o/r"]; rs.LastForkAt != "2026-08-18T01:00:00Z" {
		t.Fatalf("fork cursor = %q", rs.LastForkAt)
	}

	events2, _, err := s.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if got := byKind(events2, "fork"); len(got) != 0 {
		t.Fatalf("re-emitted forks: %v", keysOf(got))
	}
}

// ---------------------------------------------------------------- rate limit

func TestPollBacksOffWhenRateLimited(t *testing.T) {
	api := &fakeAPI{
		status:        http.StatusForbidden,
		rateRemaining: "0",
		rateReset:     strconv.FormatInt(testNow.Add(time.Hour).Unix(), 10),
	}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, newState, err := s.Poll(context.Background(), []byte(`{"repos":{}}`))
	if err != nil {
		t.Fatalf("a spent rate limit must not surface as an error, got %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", keysOf(events))
	}
	if newState == nil {
		t.Fatal("state was dropped")
	}
	after := api.requests
	if after == 0 {
		t.Fatal("the first poll made no requests at all")
	}

	// While the window is open, the next poll must not touch the network.
	if _, _, err := s.Poll(context.Background(), newState); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if api.requests != after {
		t.Fatalf("made %d more requests while rate limited", api.requests-after)
	}

	// Once the reset passes, polling resumes.
	s.Now = func() time.Time { return testNow.Add(2 * time.Hour) }
	api.status, api.rateRemaining, api.rateReset = 0, "4999", ""
	if _, _, err := s.Poll(context.Background(), newState); err != nil {
		t.Fatalf("Poll after reset: %v", err)
	}
	if api.requests <= after {
		t.Fatal("polling did not resume after the rate limit reset")
	}
}

// --------------------------------------------------------------------- check

func TestCheck(t *testing.T) {
	api := &fakeAPI{}
	s, _ := newTestSource(t, api, config.GitHub{Token: "tok"})
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	api.status = http.StatusNotFound
	err := s.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repo not found or token lacks access") {
		t.Fatalf("404 Check error = %v", err)
	}

	api.status = http.StatusUnauthorized
	err = s.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("401 Check error = %v", err)
	}

	empty, cErr := New(config.GitHub{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if cErr != nil {
		t.Fatalf("New: %v", cErr)
	}
	if err := empty.Check(context.Background()); err == nil {
		t.Fatal("Check with no repos should fail")
	}
}

func TestNewRejectsBadRepoNames(t *testing.T) {
	for _, bad := range []string{"nickhirras", "", "a/b/c", "/r", "o/"} {
		if _, err := New(config.GitHub{Repos: []string{bad}}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("New accepted %q", bad)
		}
	}
	if _, err := New(config.GitHub{Repos: []string{"nickhirras/loot"}}, nil); err != nil {
		t.Errorf("New rejected a good repo: %v", err)
	}
}

func TestPollIntervalIsTenMinutes(t *testing.T) {
	s, _ := New(config.GitHub{}, nil)
	if got := s.PollInterval(); got != 10*time.Minute {
		t.Fatalf("PollInterval = %s, want 10m", got)
	}
	if s.Name() != "github" {
		t.Fatalf("Name = %q", s.Name())
	}
}

func TestSinceOverridesBackfill(t *testing.T) {
	stars := []stargazer{
		star(t, "old", "2026-07-01T00:00:00Z"),
		star(t, "new", "2026-08-10T00:00:00Z"),
	}
	api := &fakeAPI{stars: stars}
	// A 1 day backfill would suppress both; --since 2026-06-01 keeps both.
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 1, Token: "tok"})
	s.Since = "2026-06-01"

	events, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := keysOf(byKind(events, "star")); len(got) != 2 {
		t.Fatalf("stars = %v, want both", got)
	}
}

// --------------------------------------------------------------------- utils

func findKey(t *testing.T, events []core.Event, key string) core.Event {
	t.Helper()
	for _, e := range events {
		if e.DedupeKey == key {
			return e
		}
	}
	t.Fatalf("no event with key %s in %v", key, keysOf(events))
	return core.Event{}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestEmittedEventsRenderWithTheDefaultRules pins the contract between this
// source and internal/rules/default.yaml: the rule templates read
// .Payload.number, .Payload.title, .Payload.tag and .Payload.user by name, so
// a renamed payload key would silently produce blank drops.
func TestEmittedEventsRenderWithTheDefaultRules(t *testing.T) {
	api := &fakeAPI{
		stars:  []stargazer{star(t, "u105", "2026-08-17T00:00:00Z")},
		issues: issuesFixture,
		pulls: map[int]string{
			8: `{"number":8,"merged":true,"merged_at":"2026-08-18T11:00:00Z"}`,
			4: `{"number":4,"merged":false,"merged_at":null}`,
		},
		forks:    `[{"full_name":"alice/r","created_at":"2026-08-18T01:00:00Z","owner":{"login":"alice"}}]`,
		releases: `[{"id":3,"tag_name":"v1.2.0","name":"Sharpened","published_at":"2026-08-18T05:00:00Z"}]`,
	}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// Force a milestone too, so its template is covered.
	events = append(events, s.milestoneEvent("o/r", 1000, testNow))

	engine, err := rules.Load("", nil)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}

	want := map[string]string{
		"star":            "New stargazer",
		"stars_milestone": "1,000 stars on o/r",
		"fork":            "Forked",
		"issue_opened":    "A quest appears: #7",
		"issue_closed":    "Issue slain: #5",
		"pr_opened":       "PR opened: #9",
		"pr_merged":       "PR merged: #8",
		"release":         "Release shipped: v1.2.0",
	}
	seen := map[string]bool{}
	for _, ev := range events {
		drop, err := engine.Classify(context.Background(), ev)
		if err != nil {
			t.Fatalf("classify %s: %v", ev.DedupeKey, err)
		}
		if strings.Contains(drop.Title, "<no value>") || strings.Contains(drop.Subtitle, "<no value>") {
			t.Errorf("%s rendered a missing payload key: %q / %q", ev.DedupeKey, drop.Title, drop.Subtitle)
		}
		if drop.Title == "" {
			t.Errorf("%s produced an empty title", ev.DedupeKey)
		}
		if w, ok := want[ev.Kind]; ok && !seen[ev.Kind] {
			seen[ev.Kind] = true
			if ev.Kind != "issue_closed" && ev.Kind != "pr_merged" && drop.Title != w {
				t.Errorf("%s title = %q, want %q", ev.Kind, drop.Title, w)
			}
		}
	}
	for kind := range want {
		if !seen[kind] {
			t.Errorf("no %s event was produced, so its rule went untested", kind)
		}
	}

	// The stargazer's login must reach the subtitle.
	starDrop, err := engine.Classify(context.Background(), findKey(t, events, "github:star:o/r:u105"))
	if err != nil {
		t.Fatalf("classify star: %v", err)
	}
	if !strings.Contains(starDrop.Subtitle, "u105") {
		t.Fatalf("star subtitle = %q, want the login in it", starDrop.Subtitle)
	}
}

// ------------------------------------------------------------- pagination

// fullIssuePage builds a page of `perPage` items so the poller has to ask for
// the next one, numbered downwards from `from` and updated a minute apart so
// the list reads newest-first the way `sort=updated&direction=desc` does.
func fullIssuePage(t *testing.T, from int, base time.Time) string {
	t.Helper()
	items := make([]string, 0, perPage)
	for i := 0; i < perPage; i++ {
		at := base.Add(-time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
		items = append(items, fmt.Sprintf(
			`{"number":%d,"title":"Issue %d","state":"open","html_url":"https://github.com/o/r/issues/%d",
			  "created_at":%q,"updated_at":%q,"user":{"login":"alice"}}`,
			from-i, from-i, from-i, at, at))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// One page was read and the cursor jumped to now, so everything on page two
// was thrown away without ever producing a drop — and the next poll, asking
// only for what changed since now, could never find it again.
func TestPollIssuesWalksEveryPage(t *testing.T) {
	first := fullIssuePage(t, 300, testNow.Add(-time.Hour))
	second := `[
	  {"number":42,"title":"Missed entirely","state":"open","html_url":"https://github.com/o/r/issues/42",
	   "created_at":"2026-08-17T09:00:00Z","updated_at":"2026-08-17T09:00:00Z","user":{"login":"bob"}}
	]`
	api := &fakeAPI{issuePages: []string{first, second}}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	opened := byKind(events, "issue_opened")
	if len(opened) != perPage+1 {
		t.Fatalf("emitted %d issue_opened events, want %d (both pages)", len(opened), perPage+1)
	}
	if !hasKey(opened, "github:issue_opened:o/r:42") {
		t.Errorf("the second page was never read: %v", keysOf(opened))
	}
	if len(api.issuePagesSeen) != 2 || api.issuePagesSeen[0] != 1 || api.issuePagesSeen[1] != 2 {
		t.Errorf("pages requested = %v, want [1 2] — a short page ends the walk", api.issuePagesSeen)
	}
	// The list ran out rather than the cap, so the cursor may safely advance.
	if rs := decodeState(state).Repos["o/r"]; rs.LastIssuePoll != formatTime(testNow) {
		t.Errorf("issue cursor = %q, want now", rs.LastIssuePoll)
	}
}

// When the cap stops the walk, the cursor must not step over what was never
// read: it stays at the oldest item actually seen, so the next poll picks up
// from there instead of skipping the rest for good.
func TestPollIssuesCappedHoldsTheCursorBack(t *testing.T) {
	pages := make([]string, maxListPages+2)
	base := testNow.Add(-time.Hour)
	for i := range pages {
		pages[i] = fullIssuePage(t, 10_000-i*perPage, base.Add(-time.Duration(i*perPage)*time.Minute))
	}
	api := &fakeAPI{issuePages: pages}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	_, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := len(api.issuePagesSeen); got != maxListPages {
		t.Fatalf("requested %d pages, want the cap of %d", got, maxListPages)
	}
	cursor := decodeState(state).Repos["o/r"].LastIssuePoll
	if cursor == formatTime(testNow) {
		t.Fatal("the cursor advanced past pages that were never read")
	}
	oldestSeen := base.Add(-time.Duration(maxListPages*perPage-1) * time.Minute).UTC()
	if cursor != formatTime(oldestSeen) {
		t.Errorf("cursor = %q, want the oldest item seen %q", cursor, formatTime(oldestSeen))
	}
}

// Forks page the same way: a hundred at a time, newest first, and the walk
// stops at the first page that reaches back past the cursor.
func TestPollForksWalksEveryPage(t *testing.T) {
	items := make([]string, 0, perPage)
	for i := 0; i < perPage; i++ {
		at := testNow.Add(-time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
		items = append(items, fmt.Sprintf(
			`{"full_name":"u%d/r","html_url":"https://github.com/u%d/r","created_at":%q,"owner":{"login":"u%d"}}`,
			i, i, at, i))
	}
	first := "[" + strings.Join(items, ",") + "]"
	second := `[
	  {"full_name":"zoe/r","html_url":"https://github.com/zoe/r","created_at":"2026-08-17T12:00:00Z","owner":{"login":"zoe"}}
	]`
	api := &fakeAPI{forkPages: []string{first, second}}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, state, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	forks := byKind(events, "fork")
	if len(forks) != perPage+1 {
		t.Fatalf("emitted %d fork events, want %d (both pages)", len(forks), perPage+1)
	}
	if !hasKey(forks, "github:fork:o/r:zoe") {
		t.Errorf("the second page of forks was never read: %v", keysOf(forks))
	}
	if rs := decodeState(state).Repos["o/r"]; rs.LastForkAt != formatTime(testNow) {
		t.Errorf("fork cursor = %q, want the newest fork", rs.LastForkAt)
	}

	// An ordinary poll now finds one full page of already-known forks and
	// stops there rather than walking the whole list again.
	api.forkPagesSeen = nil
	if _, _, err := s.Poll(context.Background(), state); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if got := len(api.forkPagesSeen); got != 1 {
		t.Errorf("second poll read %d fork pages, want 1", got)
	}
}

func hasKey(events []core.Event, key string) bool {
	for _, ev := range events {
		if ev.DedupeKey == key {
			return true
		}
	}
	return false
}

// Star events only exist for stars Loot watched arrive, so a repo that was
// already popular before it was installed would forever count as having none.
// One silent snapshot a day says where the repo actually stands.
func TestPollEmitsAStarsTotalSnapshot(t *testing.T) {
	api := &fakeAPI{stars: []stargazer{}}
	// 2,500 stars, none of them new.
	api.stars = make([]stargazer, 2500)
	for i := range api.stars {
		api.stars[i].StarredAt = mustTime(t, "2020-01-01T00:00:00Z").Add(time.Duration(i) * time.Minute)
		api.stars[i].User.Login = fmt.Sprintf("u%d", i)
	}
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30, Token: "tok"})

	events, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	totals := byKind(events, "stars_total")
	if len(totals) != 1 {
		t.Fatalf("emitted %d stars_total events, want exactly 1 a poll", len(totals))
	}
	ev := totals[0]
	if ev.Quantity != 2500 {
		t.Errorf("quantity = %d, want the stargazers_count", ev.Quantity)
	}
	if !ev.Silent {
		t.Error("the snapshot must be silent: the star count is context, not news")
	}
	if want := "github:stars_total:o/r:" + core.DayOf(testNow); ev.DedupeKey != want {
		t.Errorf("dedupe key = %q, want %q", ev.DedupeKey, want)
	}
	if ev.App != "o/r" {
		t.Errorf("app = %q, want the repo", ev.App)
	}
	// No star drops: none of these stars is new.
	if got := byKind(events, "star"); len(got) != 0 {
		t.Errorf("emitted %d star events for a repo with no new stars", len(got))
	}
}

// GitHub now answers the stargazers listing with 401 for anonymous callers
// (seen in the wild, Aug 2026). Without a token the walk is skipped — no
// failed poll, no per-star drops — while stars_total still flows from the
// repository metadata.
func TestTokenlessPollSkipsStargazersButKeepsTotals(t *testing.T) {
	api := &fakeAPI{}
	for i := 0; i < 42; i++ {
		api.stars = append(api.stars, star(t, fmt.Sprintf("u%d", i), "2026-08-10T00:00:00Z"))
	}
	api.starsRequireAuth = true
	s, _ := newTestSource(t, api, config.GitHub{BackfillDays: 30})

	events, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("tokenless poll must not fail: %v", err)
	}
	var sawStar, sawTotal bool
	for _, ev := range events {
		if ev.Kind == "star" {
			sawStar = true
		}
		if ev.Kind == "stars_total" && ev.Quantity == 42 {
			sawTotal = true
		}
	}
	if sawStar {
		t.Fatal("per-star events without a token should be impossible")
	}
	if !sawTotal {
		t.Fatal("stars_total should still be emitted from repo metadata")
	}
}
