package appstore_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/appstore"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeFile(t *testing.T, path string, data []byte) error {
	t.Helper()
	return os.WriteFile(path, data, 0o600)
}

// fakeASC is a stand-in for App Store Connect: it serves gzipped fixtures for
// the days it has, and Apple's own 404 envelope for the days it does not.
type fakeASC struct {
	mu sync.Mutex
	// sales maps a report date to a TSV body. A date that is absent 404s.
	sales map[string][]byte
	// subs maps a report date to a subscription TSV body.
	subs map[string][]byte
	// status, when non-zero, overrides every response.
	status int
	// requests records every (reportType, date) asked for, in order.
	requests []string
	// tokens records the Authorization headers seen.
	tokens []string
}

func (f *fakeASC) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		q := r.URL.Query()
		reportType := q.Get("filter[reportType]")
		date := q.Get("filter[reportDate]")
		f.requests = append(f.requests, reportType+":"+date)
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))

		if f.status != 0 {
			writeAPIError(w, f.status, "FORBIDDEN_ERROR", "You do not have permission for this vendor number")
			return
		}
		if r.URL.Path != "/v1/salesReports" {
			writeAPIError(w, http.StatusNotFound, "PATH_ERROR", "The path is not valid")
			return
		}
		if q.Get("filter[vendorNumber]") != "80123456" {
			writeAPIError(w, http.StatusForbidden, "FORBIDDEN_ERROR", "wrong vendor number")
			return
		}

		var body []byte
		var ok bool
		switch reportType {
		case "SALES":
			if q.Get("filter[version]") != "1_1" {
				writeAPIError(w, http.StatusBadRequest, "PARAMETER_ERROR.INVALID",
					"The latest version for this report is 1_1")
				return
			}
			body, ok = f.sales[date]
		case "SUBSCRIPTION":
			body, ok = f.subs[date]
		}
		if !ok {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND",
				"There were no sales for the date specified")
			return
		}

		w.Header().Set("Content-Type", "application/a-gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(body)
		_ = zw.Close()
	})
}

func writeAPIError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{
			"status": fmt.Sprint(status), "code": code, "title": "An error occurred", "detail": detail,
		}},
	})
}

func (f *fakeASC) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// newTestSource builds a source pointed at fake, with a real generated key so
// the JWT path runs for every request, and a clock pinned to 2026-08-18 in
// Pacific time (so "yesterday" is the fixture's day).
func newTestSource(t *testing.T, fake *fakeASC, backfillDays int) *appstore.Source {
	t.Helper()

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	keyPath := writeKeyFile(t, dir, newKey(t))

	src, err := appstore.New(config.AppStore{
		KeyID:          "2X9R4HXF34",
		IssuerID:       "11111111-2222-3333-4444-555555555555",
		PrivateKeyPath: keyPath,
		VendorNumber:   "80123456",
		BackfillDays:   backfillDays,
	}, quietLogger())
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	src.BaseURL = srv.URL
	src.Client = srv.Client()
	// 09:00 UTC on the 18th is 02:00 Pacific on the 18th, so the report day is
	// still the 18th and yesterday is the 17th.
	src.Now = func() time.Time { return time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC) }
	return src
}

func decodeTestState(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return st
}

func TestPollIngestsAReadyDayAndAdvances(t *testing.T) {
	fake := &fakeASC{
		sales: map[string][]byte{"2026-08-17": readFixture(t, salesFixture)},
		subs:  map[string][]byte{"2026-08-17": readFixture(t, subscriptionFixture)},
	}
	src := newTestSource(t, fake, 1)

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	// 8 rows + 2 sales_day summaries + 2 subscription snapshots.
	if len(events) != 12 {
		t.Fatalf("got %d events, want 12", len(events))
	}

	var summaries, snapshots int
	for _, ev := range events {
		switch ev.Kind {
		case appstore.KindSalesDay:
			summaries++
		case appstore.KindSubscriptionSnapshot:
			snapshots++
		}
	}
	if summaries != 2 || snapshots != 2 {
		t.Errorf("got %d summaries and %d snapshots, want 2 and 2", summaries, snapshots)
	}

	st := decodeTestState(t, state)
	if st["last_complete_day"] != "2026-08-17" {
		t.Fatalf("cursor = %v, want 2026-08-17", st["last_complete_day"])
	}
	if _, pending := st["pending_days"]; pending {
		t.Errorf("a fully ingested poll should leave nothing pending: %v", st)
	}

	// Every request carried a bearer token.
	for _, tok := range fake.tokens {
		if !strings.HasPrefix(tok, "Bearer ey") {
			t.Fatalf("request authorization = %q", tok)
		}
	}

	// A second poll asks for nothing: the day is done and today is not over.
	before := len(fake.asked())
	events2, _, err := src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(events2) != 0 {
		t.Errorf("second poll emitted %d events, want 0", len(events2))
	}
	if got := len(fake.asked()) - before; got != 0 {
		t.Errorf("second poll made %d requests, want 0", got)
	}
}

func TestPollBackfillsWholeWindowOldestFirst(t *testing.T) {
	fixture := readFixture(t, salesFixture)
	fake := &fakeASC{sales: map[string][]byte{
		"2026-08-15": fixture,
		"2026-08-16": fixture,
		"2026-08-17": fixture,
	}}
	src := newTestSource(t, fake, 3)

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 30 {
		t.Fatalf("got %d events, want 10 per day for 3 days", len(events))
	}
	if events[0].Day != "2026-08-15" {
		t.Errorf("first event day = %q, want the oldest day first", events[0].Day)
	}
	if st := decodeTestState(t, state); st["last_complete_day"] != "2026-08-17" {
		t.Errorf("cursor = %v", st["last_complete_day"])
	}

	// One subscription report, for the newest day only: snapshots are
	// absolute, so backfilling them would be 30 requests for one useful value.
	var subRequests int
	for _, r := range fake.asked() {
		if strings.HasPrefix(r, "SUBSCRIPTION:") {
			subRequests++
			if r != "SUBSCRIPTION:2026-08-17" {
				t.Errorf("subscription report asked for %q", r)
			}
		}
	}
	if subRequests != 1 {
		t.Errorf("made %d subscription requests, want 1", subRequests)
	}
}

func TestPollStopsAtTheFirstDayThatIsNotReady(t *testing.T) {
	fixture := readFixture(t, salesFixture)
	// The 16th is missing: Apple has not published it yet.
	fake := &fakeASC{sales: map[string][]byte{
		"2026-08-15": fixture,
		"2026-08-17": fixture,
	}}
	src := newTestSource(t, fake, 3)

	events, state, err := src.Poll(context.Background(), nil)
	// "Not ready" is the normal state of affairs for a few hours a day and
	// must never surface as a source error.
	if err != nil {
		t.Fatalf("a not-ready report must not be an error, got %v", err)
	}
	for _, ev := range events {
		if ev.Day != "2026-08-15" {
			t.Fatalf("event from %q, want only the 15th before the gap", ev.Day)
		}
	}

	st := decodeTestState(t, state)
	if st["last_complete_day"] != "2026-08-15" {
		t.Fatalf("cursor = %v, want to stop at the gap", st["last_complete_day"])
	}
	pending, _ := st["pending_days"].([]any)
	if len(pending) != 2 || pending[0] != "2026-08-16" || pending[1] != "2026-08-17" {
		t.Fatalf("pending days = %v, want the 16th and the 17th", st["pending_days"])
	}
	// The 17th was never requested: days are strictly ordered so a late report
	// cannot be skipped over.
	for _, r := range fake.asked() {
		if r == "SALES:2026-08-17" {
			t.Fatal("polled past a day that was not ready")
		}
	}

	// Apple publishes the missing day; the next poll picks up both.
	fake.mu.Lock()
	fake.sales["2026-08-16"] = fixture
	fake.mu.Unlock()

	events, state, err = src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	days := map[string]bool{}
	for _, ev := range events {
		days[ev.Day] = true
	}
	if !days["2026-08-16"] || !days["2026-08-17"] {
		t.Fatalf("second poll covered %v, want both remaining days", days)
	}
	if st := decodeTestState(t, state); st["last_complete_day"] != "2026-08-17" {
		t.Errorf("cursor = %v", st["last_complete_day"])
	}
}

func TestPollStepsOverAnOldEmptyDay(t *testing.T) {
	// Nothing at all for the whole window. Days older than the grace period
	// are taken to be genuinely empty, so the cursor cannot stall forever on
	// a quiet Sunday.
	fake := &fakeASC{sales: map[string][]byte{}}
	src := newTestSource(t, fake, 10)

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events from an empty window", len(events))
	}
	st := decodeTestState(t, state)
	// 2026-08-13 is the newest day beyond the 3 day grace window.
	if st["last_complete_day"] != "2026-08-13" {
		t.Fatalf("cursor = %v, want the last day old enough to be called empty", st["last_complete_day"])
	}
}

func TestPollGivesUpOnTheSubscriptionReport(t *testing.T) {
	fixture := readFixture(t, salesFixture)
	fake := &fakeASC{
		sales: map[string][]byte{
			"2026-08-15": fixture, "2026-08-16": fixture, "2026-08-17": fixture,
		},
		subs: map[string][]byte{},
	}
	src := newTestSource(t, fake, 3)

	// Three polls, one report day each, all missing a subscription report.
	state := []byte(`{"seeded":true,"last_complete_day":"2026-08-14"}`)
	for i := 0; i < 3; i++ {
		src.Now = func() time.Time { return time.Date(2026, 8, 16+i, 9, 0, 0, 0, time.UTC) }
		var err error
		if _, state, err = src.Poll(context.Background(), state); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}

	st := decodeTestState(t, state)
	if got, _ := st["subs_unavailable_streak"].(float64); got != 3 {
		t.Fatalf("streak = %v, want 3", st["subs_unavailable_streak"])
	}

	// The next day's poll must not ask again.
	before := countRequests(fake, "SUBSCRIPTION:")
	src.Now = func() time.Time { return time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC) }
	fake.mu.Lock()
	fake.sales["2026-08-18"] = fixture
	fake.mu.Unlock()
	if _, state, err := src.Poll(context.Background(), state); err != nil {
		t.Fatalf("fourth poll: %v (state %s)", err, state)
	}
	if got := countRequests(fake, "SUBSCRIPTION:"); got != before {
		t.Fatalf("still asking for the subscription report after %d refusals", maxSubscriptionRefusals)
	}

	// A week later it tries once more, in case subscriptions launched in the
	// meantime. "No subscriptions" is not a permanent property of an account.
	fake.mu.Lock()
	for day := "2026-08-18"; day <= "2026-08-27"; day = nextDay(t, day) {
		fake.sales[day] = fixture
	}
	fake.subs["2026-08-27"] = readFixture(t, subscriptionFixture)
	fake.mu.Unlock()

	src.Now = func() time.Time { return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC) }
	events, state, err := src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("recheck poll: %v", err)
	}
	if got := countRequests(fake, "SUBSCRIPTION:"); got <= before {
		t.Fatal("the subscription report was never rechecked")
	}
	var snapshots int
	for _, ev := range events {
		if ev.Kind == appstore.KindSubscriptionSnapshot {
			snapshots++
		}
	}
	if snapshots != 2 {
		t.Fatalf("got %d snapshots after the recheck, want 2", snapshots)
	}
	if st := decodeTestState(t, state); st["subs_unavailable_streak"] != nil {
		t.Errorf("a successful report must clear the streak, got %v", st["subs_unavailable_streak"])
	}
}

// maxSubscriptionRefusals mirrors the package's own give-up threshold; it is
// only here to keep the failure message honest.
const maxSubscriptionRefusals = 3

func countRequests(f *fakeASC, prefix string) int {
	n := 0
	for _, r := range f.asked() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func nextDay(t *testing.T, day string) string {
	t.Helper()
	parsed, err := time.Parse(core.DayLayout, day)
	if err != nil {
		t.Fatalf("parse day %q: %v", day, err)
	}
	return parsed.AddDate(0, 0, 1).Format(core.DayLayout)
}

func TestPollSurfacesCredentialErrors(t *testing.T) {
	fake := &fakeASC{status: http.StatusUnauthorized}
	src := newTestSource(t, fake, 1)

	events, _, err := src.Poll(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an error from a 401")
	}
	if len(events) != 0 {
		t.Errorf("got %d events from a failed poll", len(events))
	}
	if !strings.Contains(err.Error(), "key_id") {
		t.Errorf("a 401 should point at the credentials, got %v", err)
	}
}

func TestCheck(t *testing.T) {
	fixture := readFixture(t, salesFixture)

	t.Run("ready report", func(t *testing.T) {
		fake := &fakeASC{sales: map[string][]byte{"2026-08-17": fixture}}
		if err := newTestSource(t, fake, 1).Check(context.Background()); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	t.Run("no report yet", func(t *testing.T) {
		// A brand new account, or the small hours of the morning: Apple
		// accepted the credentials and simply has nothing to hand over.
		fake := &fakeASC{sales: map[string][]byte{}}
		if err := newTestSource(t, fake, 1).Check(context.Background()); err != nil {
			t.Fatalf("a 404 means 'nothing yet', not 'broken': %v", err)
		}
		if got := len(fake.asked()); got != 2 {
			t.Errorf("made %d requests, want yesterday and the day before", got)
		}
	})

	t.Run("wrong credentials", func(t *testing.T) {
		fake := &fakeASC{status: http.StatusForbidden}
		err := newTestSource(t, fake, 1).Check(context.Background())
		if err == nil {
			t.Fatal("expected an error from a 403")
		}
		if !strings.Contains(err.Error(), "vendor_number") {
			t.Errorf("a 403 should mention the vendor number and the key role, got %v", err)
		}
	})
}

func TestGzipAndPlainBodiesBothParse(t *testing.T) {
	// Apple ships gzip, but a proxy that decompresses on the way must not
	// break ingest.
	fixture := readFixture(t, salesFixture)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(fixture)
	_ = zw.Close()

	fromGzip, err := appstore.ParseSalesReport(mustGunzip(t, gz.Bytes()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fromPlain, err := appstore.ParseSalesReport(fixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(fromGzip) != len(fromPlain) {
		t.Fatalf("gzip round trip changed the row count: %d vs %d", len(fromGzip), len(fromPlain))
	}
}

func mustGunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func TestSourceContract(t *testing.T) {
	src := newTestSource(t, &fakeASC{}, 1)
	if src.Name() != "appstore" {
		t.Errorf("name = %q", src.Name())
	}
	if src.PollInterval() != time.Hour {
		t.Errorf("poll interval = %v, want 1h", src.PollInterval())
	}
	var _ core.Source = src
	var _ core.Checker = src
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	if _, err := appstore.New(config.AppStore{KeyID: "2X9R4HXF34"}, quietLogger()); err == nil {
		t.Fatal("expected an error for a half-filled config")
	}
}
