package metaads

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The token is obviously fake, and long enough that finding it in a string is
// not a coincidence.
const token = "EAAfaketoken0000notreal"

const conv1 = "offsite_conversion.custom.999"
const conv2 = "offsite_conversion.fb_pixel_custom"

// now is 25 Nov 2026 at noon in the fixture account's timezone
// (America/Los_Angeles), so "today" is the 25th and yesterday the 24th.
var now = time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixture is a stand-in Graph API serving the testdata files. Every response
// passes through edit, so a test can revise a number between polls.
type fixture struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	edit     map[string]func(string) string
	fail     string // when set, every request answers with this testdata file and HTTP 400
	requests []*http.Request
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, edit: map[string]func(string) string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	fail := f.fail
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fail != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, f.file(fail))
		return
	}
	// Like the real API: no valid token, no data.
	if r.URL.Query().Get("access_token") != token {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid OAuth access token.","type":"OAuthException","code":190}}`)
		return
	}
	var name string
	switch {
	case r.URL.Path == "/v27.0/act_1234567890":
		name = "account.json"
	case r.URL.Path == "/v27.0/act_1234567890/insights":
		name = "insights_page1.json"
		if r.URL.Query().Get("after") == "QUZURVI" {
			name = "insights_page2.json"
		}
	case r.URL.Path == "/v27.0/act_1234567890/ads":
		name = "ads.json"
	case r.URL.Path == "/v27.0/5550001/stats":
		name = "pixel_stats.json"
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"Unknown path","code":803}}`)
		return
	}
	_, _ = io.WriteString(w, f.file(name))
}

func (f *fixture) file(name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.t.Fatalf("read testdata: %v", err)
	}
	s := strings.NewReplacer("{{BASE}}", f.srv.URL, "{{TOKEN}}", token).Replace(string(b))
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.edit[name]; e != nil {
		s = e(s)
	}
	return s
}

func (f *fixture) source(t *testing.T, mut ...func(*Options)) *Source {
	t.Helper()
	opts := Options{
		AccessToken:       token,
		AdAccountID:       "act_1234567890",
		App:               "Example App",
		PixelID:           "5550001",
		ConversionActions: []string{conv1, conv2},
		BaseURL:           f.srv.URL,
		HTTPClient:        f.srv.Client(),
	}
	for _, m := range mut {
		m(&opts)
	}
	s, err := New(opts, quiet())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Now = func() time.Time { return now }
	return s
}

func byKind(evs []core.Event, kind string) []core.Event {
	var out []core.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func find(t *testing.T, evs []core.Event, kind, entity, day string) core.Event {
	t.Helper()
	prefix := core.FunnelSnapshotKey(Name, kind, entity, day, "")
	for _, e := range evs {
		if e.Kind == kind && strings.HasPrefix(e.DedupeKey, prefix) {
			return e
		}
	}
	t.Fatalf("no %s event for %s on %s", kind, entity, day)
	return core.Event{}
}

func adDayOf(t *testing.T, e core.Event) core.AdDay {
	t.Helper()
	var d core.AdDay
	if err := json.Unmarshal(e.Payload, &d); err != nil {
		t.Fatalf("decode AdDay: %v", err)
	}
	return d
}

func keys(evs []core.Event) map[string]bool {
	m := map[string]bool{}
	for _, e := range evs {
		m[e.DedupeKey] = true
	}
	return m
}

func poll(t *testing.T, s *Source, st []byte) ([]core.Event, []byte) {
	t.Helper()
	evs, out, err := s.Poll(context.Background(), st)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	return evs, out
}

func TestFirstPollReadsAllPagesAndParsesRows(t *testing.T) {
	f := newFixture(t)
	evs, _ := poll(t, f.source(t), nil)

	ads := byKind(evs, core.KindAdDay)
	if len(ads) != 5 {
		t.Fatalf("ad_day events = %d, want 5 (both insights pages)", len(ads))
	}

	// The second page was asked for with our token, on our host, even though
	// the paging link carried a token of its own.
	var sawPage2 bool
	for _, r := range f.requests {
		if r.URL.Query().Get("after") == "QUZURVI" {
			sawPage2 = true
		}
		if r.URL.Path == "/v27.0/act_1234567890/insights" && r.URL.Query().Get("after") == "" {
			q := r.URL.Query()
			if q.Get("level") != "ad" || q.Get("time_increment") != "1" {
				t.Errorf("insights query = %v", q)
			}
			// First poll: BackfillDays (30) back from the account-local today.
			if q.Get("time_range") != `{"since":"2026-10-26","until":"2026-11-25"}` {
				t.Errorf("time_range = %s", q.Get("time_range"))
			}
			if !strings.Contains(q.Get("fields"), "actions") {
				t.Errorf("fields = %s", q.Get("fields"))
			}
		}
	}
	if !sawPage2 {
		t.Fatal("second insights page was never requested")
	}

	e := find(t, evs, core.KindAdDay, "101", "2026-11-24")
	if !e.Silent || e.IsLedger || e.Chest || e.Amount != 0 || e.Quantity != 0 {
		t.Errorf("ad_day flags: silent=%v ledger=%v chest=%v amount=%v qty=%v", e.Silent, e.IsLedger, e.Chest, e.Amount, e.Quantity)
	}
	if e.App != "Example App" || e.Source != Name || e.Day != "2026-11-24" {
		t.Errorf("ad_day identity: app=%q source=%q day=%q", e.App, e.Source, e.Day)
	}
	if !e.OccurredAt.Equal(time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("OccurredAt = %v", e.OccurredAt)
	}
	d := adDayOf(t, e)
	want := core.AdDay{
		AdID: "101", Ad: "Morning reel", AdSetID: "11", AdSet: "Broad", CampaignID: "1", Campaign: "Example launch",
		Status: "ACTIVE", URLs: []string{"https://example.com/get/?a=morning"},
		Spend: 5.25, Currency: "USD", Impressions: 500, Reach: 450,
		// inline_link_clicks wins over the link_click action (16).
		LinkClicks: 15, LandingPageViews: 12,
		// Both configured conversion action types, summed.
		Conversions: 3, Tracked: true,
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("AdDay\n got %+v\nwant %+v", d, want)
	}

	// Ad 202 never reports a conversion action: untracked, and its link
	// clicks fall back to the link_click action when the field is absent.
	d2 := adDayOf(t, find(t, evs, core.KindAdDay, "202", "2026-11-24"))
	if d2.Tracked || d2.Conversions != 0 || d2.LinkClicks != 25 || d2.Status != "PAUSED" {
		t.Errorf("ad 202: %+v", d2)
	}
	if !reflect.DeepEqual(d2.URLs, []string{"http://example.net/plain", "https://example.org/b"}) {
		t.Errorf("ad 202 URLs = %v (images must be skipped)", d2.URLs)
	}
	// Tracked is per ad across the window: ad 101's row for today carries
	// no conversion action but is still tracked.
	if d := adDayOf(t, find(t, evs, core.KindAdDay, "101", "2026-11-25")); !d.Tracked || d.Conversions != 0 {
		t.Errorf("ad 101 today: %+v", d)
	}
}

func TestFirstPollSummarizesOnlyYesterday(t *testing.T) {
	f := newFixture(t)
	evs, st := poll(t, f.source(t), nil)

	sums := byKind(evs, core.KindAdsDay)
	if len(sums) != 1 {
		t.Fatalf("ads_day events = %d, want 1 (yesterday only, never today or the backfill)", len(sums))
	}
	e := sums[0]
	if e.Day != "2026-11-24" || e.DedupeKey != "metaads:ads_day:Example App:2026-11-24" {
		t.Errorf("summary day=%q key=%q", e.Day, e.DedupeKey)
	}
	if e.Silent || !e.Chest || e.IsLedger || e.Amount != 0 {
		t.Errorf("summary flags: silent=%v chest=%v ledger=%v amount=%v", e.Silent, e.Chest, e.IsLedger, e.Amount)
	}
	var sum core.AdsDaySummary
	if err := json.Unmarshal(e.Payload, &sum); err != nil {
		t.Fatal(err)
	}
	want := core.AdsDaySummary{Spend: 12.25, Currency: "USD", Impressions: 1400, LinkClicks: 40,
		LandingPageViews: 32, Conversions: 3, Ads: 2, TopAd: "Static photo"}
	if sum != want {
		t.Errorf("summary\n got %+v\nwant %+v", sum, want)
	}
	if !strings.Contains(string(st), `"last_summarized":"2026-11-24"`) {
		t.Errorf("state = %s", st)
	}
}

func TestUnchangedPollEmitsTheSameKeys(t *testing.T) {
	f := newFixture(t)
	s := f.source(t)
	first, st := poll(t, s, nil)
	second, st2 := poll(t, s, st)

	if len(byKind(second, core.KindAdsDay)) != 0 {
		t.Error("a second poll on the same day emitted another ads_day summary")
	}
	snap := func(evs []core.Event) map[string]bool {
		return keys(append(byKind(evs, core.KindAdDay), byKind(evs, core.KindSiteEventDay)...))
	}
	if !reflect.DeepEqual(snap(first), snap(second)) {
		t.Errorf("unchanged data produced different snapshot keys:\n%v\n%v", snap(first), snap(second))
	}
	if string(st) != string(st2) {
		t.Errorf("state moved without a new day: %s -> %s", st, st2)
	}

	// Later polls re-read LookbackDays (7), not the backfill.
	last := f.requests[len(f.requests)-1]
	for _, r := range f.requests {
		if r.URL.Path == "/v27.0/act_1234567890/insights" && r.URL.Query().Get("after") == "" {
			last = r
		}
	}
	if got := last.URL.Query().Get("time_range"); got != `{"since":"2026-11-18","until":"2026-11-25"}` {
		t.Errorf("lookback time_range = %s", got)
	}
}

func TestRevisedDayIsANewSnapshot(t *testing.T) {
	f := newFixture(t)
	s := f.source(t)
	first, st := poll(t, s, nil)

	// Meta attributes one more conversion to ad 101 on the 24th.
	f.edit["insights_page1.json"] = func(b string) string {
		return strings.Replace(b, `{"action_type": "offsite_conversion.custom.999", "value": "1"}`,
			`{"action_type": "offsite_conversion.custom.999", "value": "2"}`, 1)
	}
	second, _ := poll(t, s, st)

	old := find(t, first, core.KindAdDay, "101", "2026-11-24")
	rev := find(t, second, core.KindAdDay, "101", "2026-11-24")
	if old.DedupeKey == rev.DedupeKey {
		t.Fatal("revised day kept its dedupe key; the revision would be dropped")
	}
	if d := adDayOf(t, rev); d.Conversions != 4 {
		t.Errorf("revised conversions = %d, want 4", d.Conversions)
	}
	// Every other day is untouched.
	for _, k := range []struct{ ad, day string }{{"101", "2026-11-23"}, {"202", "2026-11-24"}, {"202", "2026-11-25"}} {
		if find(t, first, core.KindAdDay, k.ad, k.day).DedupeKey != find(t, second, core.KindAdDay, k.ad, k.day).DedupeKey {
			t.Errorf("%s/%s changed key without changing numbers", k.ad, k.day)
		}
	}
	// And the summary is news, not a snapshot: no second drop for the 24th.
	if len(byKind(second, core.KindAdsDay)) != 0 {
		t.Error("revision minted a second ads_day summary")
	}
}

func TestSummaryForEachNewlyCompletedDay(t *testing.T) {
	f := newFixture(t)
	s := f.source(t)
	_, st := poll(t, s, nil)

	// A day later the 25th is complete; the 26th (today) has no rows and is
	// never summarized anyway.
	s.Now = func() time.Time { return now.Add(24 * time.Hour) }
	evs, st := poll(t, s, st)
	sums := byKind(evs, core.KindAdsDay)
	if len(sums) != 1 || sums[0].Day != "2026-11-25" {
		t.Fatalf("summaries = %+v, want exactly the 25th", sums)
	}
	var sum core.AdsDaySummary
	_ = json.Unmarshal(sums[0].Payload, &sum)
	if sum.Spend != 3 || sum.Ads != 2 || sum.TopAd != "Static photo" {
		t.Errorf("summary for the 25th: %+v", sum)
	}
	if !strings.Contains(string(st), `"2026-11-25"`) {
		t.Errorf("state = %s", st)
	}
}

func TestCatchUpSummariesSkipDaysWithoutSpend(t *testing.T) {
	f := newFixture(t)
	s := f.source(t)
	// The cursor is at the 21st: the 22nd (no rows), 23rd and 24th are due.
	evs, _ := poll(t, s, []byte(`{"last_summarized":"2026-11-21"}`))
	var days []string
	for _, e := range byKind(evs, core.KindAdsDay) {
		days = append(days, e.Day)
	}
	if !reflect.DeepEqual(days, []string{"2026-11-23", "2026-11-24"}) {
		t.Errorf("summary days = %v", days)
	}
}

func TestPixelEventsBucketToAccountDays(t *testing.T) {
	f := newFixture(t)
	evs, _ := poll(t, f.source(t), nil)

	got := map[string]int{}
	for _, e := range byKind(evs, core.KindSiteEventDay) {
		var p core.SiteEventDay
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if !e.Silent || e.IsLedger || e.Amount != 0 || e.Quantity != 0 {
			t.Errorf("site_event_day flags wrong: %+v", e)
		}
		got[p.Name+"@"+e.Day] = p.Count
	}
	// 07:00 UTC on the 24th is 23:00 on the 23rd in Los Angeles; 08:00 UTC
	// is midnight on the 24th. The "2" count arrives as a string.
	want := map[string]int{
		"PageView@2026-11-23": 5,
		"PageView@2026-11-24": 7,
		"Lead@2026-11-24":     2,
		"PageView@2026-11-25": 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pixel days\n got %v\nwant %v", got, want)
	}

	// The window starts at local midnight six days back (the edge serves
	// seven days) and ends now.
	for _, r := range f.requests {
		if strings.HasSuffix(r.URL.Path, "/stats") {
			q := r.URL.Query()
			start := time.Date(2026, 11, 19, 0, 0, 0, 0, mustLoc(t, "America/Los_Angeles")).Unix()
			if q.Get("aggregation") != "event" || q.Get("start_time") != itoa(start) || q.Get("end_time") != itoa(now.Unix()) {
				t.Errorf("pixel query = %v", q)
			}
		}
	}
}

func TestNoPixelConfiguredSkipsStats(t *testing.T) {
	f := newFixture(t)
	evs, _ := poll(t, f.source(t, func(o *Options) { o.PixelID = "" }), nil)
	if n := len(byKind(evs, core.KindSiteEventDay)); n != 0 {
		t.Errorf("site_event_day without a pixel: %d", n)
	}
	for _, r := range f.requests {
		if strings.HasSuffix(r.URL.Path, "/stats") {
			t.Error("pixel stats requested with no pixel configured")
		}
	}
}

func TestExpiredTokenIsClearAndNeverLeaksTheToken(t *testing.T) {
	f := newFixture(t)
	f.fail = "error_190.json"
	s := f.source(t)

	_, st, err := s.Poll(context.Background(), []byte(`{"last_summarized":"2026-11-20"}`))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Poll error = %v, want ErrTokenExpired", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("token in poll error: %v", err)
	}
	if !strings.Contains(err.Error(), "Session has expired") {
		t.Errorf("Graph's own message lost: %v", err)
	}
	if string(st) != `{"last_summarized":"2026-11-20"}` {
		t.Errorf("failed poll moved the state: %s", st)
	}

	cerr := s.Check(context.Background())
	if !errors.Is(cerr, ErrTokenExpired) || core.IsWarning(cerr) || strings.Contains(cerr.Error(), token) {
		t.Errorf("Check error = %v", cerr)
	}
}

func TestTransportErrorNeverLeaksTheToken(t *testing.T) {
	f := newFixture(t)
	s := f.source(t)
	f.srv.Close() // connection refused; net/http puts the full URL in the error
	_, _, err := s.Poll(context.Background(), nil)
	if err == nil {
		t.Fatal("want an error from a closed server")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("token in transport error: %v", err)
	}
	if !strings.Contains(err.Error(), "access_token=REDACTED") {
		t.Errorf("expected the redacted URL in %v", err)
	}
}

func TestRateLimitIsAnAPIError(t *testing.T) {
	f := newFixture(t)
	f.edit["account.json"] = func(string) string {
		return `{"error":{"message":"User request limit reached","type":"OAuthException","code":17}}`
	}
	_, _, err := f.source(t).Poll(context.Background(), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 17 || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %v", err)
	}
}

func TestCheck(t *testing.T) {
	f := newFixture(t)
	if err := f.source(t).Check(context.Background()); err != nil {
		t.Errorf("Check with conversions configured: %v", err)
	}
	err := f.source(t, func(o *Options) { o.ConversionActions = nil }).Check(context.Background())
	if !core.IsWarning(err) || !strings.Contains(err.Error(), "conversions will not be credited to ads") {
		t.Errorf("Check without conversions = %v, want a warning", err)
	}
}

func TestNewValidatesAndNormalizes(t *testing.T) {
	base := Options{AccessToken: token, AdAccountID: "1234567890", App: "Example App"}
	s, err := New(base, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if s.accountID != "1234567890" || s.opts.APIVersion != DefaultAPIVersion || s.opts.LookbackDays != 7 ||
		s.opts.BackfillDays != 30 || s.PollInterval() != time.Hour || s.opts.BaseURL != DefaultBaseURL {
		t.Errorf("defaults: %+v", s.opts)
	}
	s, _ = New(Options{AccessToken: token, AdAccountID: " act_42 ", App: "A"}, quiet())
	if s.accountID != "42" {
		t.Errorf("act_ prefix not stripped: %q", s.accountID)
	}
	for name, o := range map[string]Options{
		"no token":     {AdAccountID: "1", App: "A"},
		"no app":       {AccessToken: token, AdAccountID: "1"},
		"no account":   {AccessToken: token, App: "A"},
		"bad account":  {AccessToken: token, AdAccountID: "act_12x", App: "A"},
		"bad pixel id": {AccessToken: token, AdAccountID: "1", App: "A", PixelID: "px-1"},
	} {
		if _, err := New(o, quiet()); err == nil {
			t.Errorf("%s: New accepted %+v", name, o)
		} else if strings.Contains(err.Error(), token) {
			t.Errorf("%s: token in error", name)
		}
	}
}

func TestCreativeURLs(t *testing.T) {
	raw := json.RawMessage(`{
		"object_story_spec": {"link_data": {"link": "https://example.com/a",
			"child_attachments": [{"link": "https://example.com/c"}, {"link": "https://example.com/b", "picture": "https://example.com/pic.jpg"}],
			"image_url": "https://example.com/image.jpg"}},
		"thumbnail_url": "https://example.com/thumb.jpg",
		"link_url": "https://example.com/a",
		"object_url": "https://scontent-lax3-1.xx.fbcdn.net/x",
		"name": "not a url", "body": "see https://example.com in text"}`)
	got := creativeURLs(raw)
	want := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}

	var many []string
	for i := 0; i < 15; i++ {
		many = append(many, `"https://example.com/`+string(rune('a'+i))+`"`)
	}
	if got := creativeURLs(json.RawMessage(`{"links":[` + strings.Join(many, ",") + `]}`)); len(got) != maxURLs {
		t.Errorf("cap: %d URLs", len(got))
	}
	if creativeURLs(nil) != nil {
		t.Error("nil creative")
	}
}

func TestRedact(t *testing.T) {
	s, _ := New(Options{AccessToken: token, AdAccountID: "1", App: "A"}, quiet())
	in := "GET https://graph.facebook.com/v27.0/act_1?fields=x&access_token=" + token + "&after=Y; also " + token
	out := s.redact(in)
	if strings.Contains(out, token) || !strings.Contains(out, "access_token=REDACTED&after=Y") {
		t.Errorf("redact = %q", out)
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
