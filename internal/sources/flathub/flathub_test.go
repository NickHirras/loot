package flathub_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/flathub"
)

// fixture is a trimmed capture of a real GET /api/v2/stats/org.gnome.Calculator
// response (8 days of installs instead of ~180).
const fixturePath = "testdata/stats_org.gnome.Calculator.json"

// fixtureToday is the day after the fixture's last date, so every fixture day
// counts as complete.
var fixtureToday = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseStats(t *testing.T) {
	var stats flathub.Stats
	if err := flathub.ParseStats(loadFixture(t), &stats); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if stats.ID != "org.gnome.Calculator" {
		t.Errorf("id = %q", stats.ID)
	}
	if stats.InstallsTotal != 684456 {
		t.Errorf("installs_total = %d", stats.InstallsTotal)
	}
	if got := len(stats.InstallsPerDay); got != 8 {
		t.Errorf("installs_per_day has %d entries, want 8", got)
	}
	if stats.InstallsPerDay["2026-08-16"] != 587 {
		t.Errorf("2026-08-16 = %d, want 587", stats.InstallsPerDay["2026-08-16"])
	}
	if stats.InstallsLast7Days != 2830 || stats.InstallsLastMonth != 12648 {
		t.Errorf("rollups = %d / %d", stats.InstallsLast7Days, stats.InstallsLastMonth)
	}
	if stats.InstallsPerCountry["AE"] != 521 {
		t.Errorf("installs_per_country AE = %d", stats.InstallsPerCountry["AE"])
	}
}

func TestEventsFromStats(t *testing.T) {
	var stats flathub.Stats
	if err := flathub.ParseStats(loadFixture(t), &stats); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Two days survive the floor, and each one is a silent row plus a
	// chest-bound headline.
	events := flathub.EventsFromStats("org.gnome.Calculator", stats, "2026-08-15", fixtureToday)
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 (a row and a headline for the 16th and 17th)", len(events))
	}

	first := events[0]
	if first.Source != "flathub" || first.Kind != "install" {
		t.Errorf("source/kind = %s/%s", first.Source, first.Kind)
	}
	if first.App != "org.gnome.Calculator" {
		t.Errorf("app = %q", first.App)
	}
	if first.Quantity != 587 {
		t.Errorf("quantity = %d, want 587", first.Quantity)
	}
	if first.DedupeKey != "flathub:org.gnome.Calculator:2026-08-16" {
		t.Errorf("dedupe_key = %q", first.DedupeKey)
	}
	if first.Day != "2026-08-16" {
		t.Errorf("day = %q, want the report day", first.Day)
	}
	if first.Country != "" || first.Amount != 0 || first.IsLedger {
		t.Errorf("flathub events carry no country/amount and are not a ledger: %+v", first)
	}
	if !first.Silent || first.Chest {
		t.Errorf("the install row must be silent and never chest-bound: %+v", first)
	}
	if !first.OccurredAt.Equal(time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred_at = %v", first.OccurredAt)
	}

	// The headline is the day's one drop, and it goes into that day's chest
	// rather than onto the live feed.
	head := events[1]
	if head.Kind != "installs_day" {
		t.Fatalf("second event kind = %q, want installs_day", head.Kind)
	}
	if head.Silent || !head.Chest {
		t.Errorf("installs_day must be a chest-bound drop: %+v", head)
	}
	if head.Quantity != 587 {
		t.Errorf("headline quantity = %d, want 587", head.Quantity)
	}
	if head.DedupeKey != "flathub:installs_day:org.gnome.Calculator:2026-08-16" {
		t.Errorf("headline dedupe_key = %q", head.DedupeKey)
	}
	if head.Day != "2026-08-16" {
		t.Errorf("headline day = %q", head.Day)
	}

	// Events must be ordered oldest first so the feed reads chronologically.
	if !events[0].OccurredAt.Before(events[2].OccurredAt) {
		t.Errorf("events are not in chronological order")
	}

	var payload map[string]any
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	if payload["date"] != "2026-08-16" {
		t.Errorf("payload date = %v", payload["date"])
	}
}

func TestEventsFromStatsExcludesToday(t *testing.T) {
	var stats flathub.Stats
	if err := flathub.ParseStats(loadFixture(t), &stats); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Pretend "now" is the 17th: that day is still accumulating and must not
	// be emitted, or its partial count would be frozen forever.
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	events := flathub.EventsFromStats("org.gnome.Calculator", stats, "2026-08-15", now)
	if len(events) != 2 {
		t.Fatalf("got %d events, want only the 16th (row + headline)", len(events))
	}
	if events[0].DedupeKey != "flathub:org.gnome.Calculator:2026-08-16" {
		t.Fatalf("dedupe_key = %q", events[0].DedupeKey)
	}
}

// newTestSource wires a Source against an httptest server serving the fixture.
func newTestSource(t *testing.T, backfillDays int, since string) (*flathub.Source, *int) {
	t.Helper()
	body := loadFixture(t)
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v2/stats/org.gnome.Calculator" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	s := flathub.New([]string{"org.gnome.Calculator"}, backfillDays, since, quietLogger())
	s.BaseURL = srv.URL
	s.Client = srv.Client()
	s.Now = func() time.Time { return fixtureToday }
	return s, &calls
}

func TestPollFirstRunHonoursBackfillWindow(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestSource(t, 3, "")

	// First run: only the last 3 complete days, not all 8.
	events, state, err := s.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("first run emitted %d events, want 6 (3 days x row + headline)", len(events))
	}
	if events[len(events)-1].DedupeKey != "flathub:installs_day:org.gnome.Calculator:2026-08-17" {
		t.Fatalf("last event = %q", events[len(events)-1].DedupeKey)
	}
	if len(state) == 0 {
		t.Fatal("poll returned no state")
	}

	// Second run against unchanged data emits nothing.
	events2, _, err := s.Poll(ctx, state)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(events2) != 0 {
		t.Fatalf("second poll emitted %d events, want 0", len(events2))
	}
}

func TestPollZeroBackfillOnlySeeds(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestSource(t, 0, "")

	events, state, err := s.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("backfill_days=0 emitted %d events, want 0", len(events))
	}

	// The cursor still advanced to the newest complete day, so the next poll
	// does not suddenly replay the whole window.
	var decoded struct {
		LastDate map[string]string `json:"last_date"`
		Seeded   map[string]bool   `json:"seeded"`
	}
	if err := json.Unmarshal(state, &decoded); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if decoded.LastDate["org.gnome.Calculator"] != "2026-08-17" {
		t.Fatalf("cursor = %q, want 2026-08-17", decoded.LastDate["org.gnome.Calculator"])
	}
	if !decoded.Seeded["org.gnome.Calculator"] {
		t.Fatal("app was not marked as seeded")
	}
}

func TestPollSinceOverridesBackfill(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestSource(t, 0, "2026-08-16")

	events, _, err := s.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("--since 2026-08-16 emitted %d events, want 4 (2 days x row + headline)", len(events))
	}
	if events[0].DedupeKey != "flathub:org.gnome.Calculator:2026-08-16" {
		t.Fatalf("first event = %q, want the since date inclusive", events[0].DedupeKey)
	}
}

func TestPollResumesFromCursor(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestSource(t, 0, "")

	state := []byte(`{"last_date":{"org.gnome.Calculator":"2026-08-14"},"seeded":{"org.gnome.Calculator":true}}`)
	events, _, err := s.Poll(ctx, state)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6 (15th, 16th, 17th, each a row + a headline)", len(events))
	}
}

// A first fetch that carried no installs_per_day at all must still seed the
// cursor. Without it the app was marked seeded with an empty LastDate, and the
// next poll — against a stats response that had filled in by then — replayed
// the whole ~180 day window into the feed.
func TestPollSeedsCursorWhenFirstFetchIsEmpty(t *testing.T) {
	ctx := context.Background()

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s := flathub.New([]string{"org.gnome.Calculator"}, 0, "", quietLogger())
	s.BaseURL = srv.URL
	s.Client = srv.Client()
	s.Now = func() time.Time { return fixtureToday }

	// A brand new app: the endpoint answers, but with no daily installs yet.
	body = []byte(`{"id":"org.gnome.Calculator","installs_total":0,"installs_per_day":{}}`)
	events, state, err := s.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("an empty stats response emitted %d events", len(events))
	}

	var decoded struct {
		LastDate map[string]string `json:"last_date"`
		Seeded   map[string]bool   `json:"seeded"`
	}
	if err := json.Unmarshal(state, &decoded); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !decoded.Seeded["org.gnome.Calculator"] {
		t.Fatal("app was not marked as seeded")
	}
	if got := decoded.LastDate["org.gnome.Calculator"]; got != "2026-08-17" {
		t.Fatalf("cursor = %q, want the first-run floor 2026-08-17", got)
	}

	// The stats now carry the full window. Only days after the seeded floor
	// may be emitted — which, with backfill_days 0, is none of them.
	body = loadFixture(t)
	events, _, err = s.Poll(ctx, state)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("second poll replayed %d events; the backfill window escaped", len(events))
	}
}

func TestPollReportsHTTPFailure(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := flathub.New([]string{"org.example.Missing"}, 7, "", quietLogger())
	s.BaseURL = srv.URL
	s.Client = srv.Client()
	s.Now = func() time.Time { return fixtureToday }

	events, _, err := s.Poll(ctx, nil)
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}
	if len(events) != 0 {
		t.Fatalf("got %d events on failure, want 0", len(events))
	}
}

func TestSourceContract(t *testing.T) {
	s := flathub.New(nil, 7, "", quietLogger())
	if s.Name() != "flathub" {
		t.Errorf("name = %q", s.Name())
	}
	if s.PollInterval() != time.Hour {
		t.Errorf("poll interval = %v, want 1h", s.PollInterval())
	}
	var _ core.Source = s
}
