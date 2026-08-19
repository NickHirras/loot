// Package playvitals reads Android vitals — daily crash and ANR counts per app
// version — from the Google Play Developer Reporting API, and hands them to the
// boss engine.
//
// It is the only crash source that *polls*, and that turns out to matter more
// than it sounds. A push-only reporter can tell Loot that a crash happened; it
// can never tell Loot that a crash *stopped*, because silence and a broken
// credential look identical. Play vitals answers a question every day, so this
// source can emit a daily heartbeat saying "I looked, and the total was N" —
// including when N is zero. That single event is what lets a boss be *slain*
// rather than merely fading from view.
//
// # Setting it up
//
//  1. Use the same service account as sources.googleplay (a Cloud service
//     account with a downloaded JSON key).
//  2. Enable **playdeveloperreporting.googleapis.com** in the Cloud project
//     that owns it. This is separate from anything the reporting bucket needs
//     and is the step everybody misses.
//  3. In Play Console > Users and permissions, invite the service account's
//     email and grant it "View app information and download bulk reports
//     (read-only)" for the apps you want.
//
// See docs/sources/crashes.md.
package playvitals

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/googleplay"
)

// Name is the source identifier, and the source name every event carries.
const Name = "playvitals"

// DefaultPollInterval is how often vitals are re-read. The data is daily and
// lags by a day or two, so six hours is "the same day it appears" without
// asking four times an hour for a number that changes once.
const DefaultPollInterval = 6 * time.Hour

// The report types the error count set breaks down by. NON_FATAL is
// deliberately ignored: a handled exception is not a crash, and counting it as
// one would spawn bosses for logging.
const (
	reportTypeCrash = "CRASH"
	reportTypeANR   = "ANR"
)

// Dimension and metric names, spelled once.
const (
	dimVersionCode = "versionCode"
	dimReportType  = "reportType"

	metricErrorReportCount = "errorReportCount"
	metricDistinctUsers    = "distinctUsers"
	metricCrashRate        = "crashRate"
	metricANRRate          = "anrRate"
)

// Source implements core.Source over the Play Developer Reporting API.
type Source struct {
	// Packages are the package names to watch. Vitals are per app; the API has
	// no "every app" query, so this list is required.
	Packages []string
	// BackfillDays is how far back the first poll reads. 30 is the default,
	// which is exactly enough for the boss engine's 28-day baseline to exist
	// on the very first evaluation.
	BackfillDays int

	// BaseURL is the API root; tests point it at httptest.
	BaseURL string
	// Tokens authenticates every request. Tests supply a static token.
	Tokens oauth2.TokenSource
	Client *http.Client
	Log    *slog.Logger

	// Now is swappable for tests.
	Now func() time.Time
}

// New returns the vitals source. keyPath is the same service-account key file
// sources.googleplay uses — the caller reads it out of the Play config, so
// there is exactly one place a Play credential is configured.
func New(cfg config.PlayVitals, keyPath string, packages []string, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if keyPath == "" {
		return nil, fmt.Errorf("playvitals: sources.googleplay.service_account_json_path is required")
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("playvitals: at least one package is required " +
			"(sources.playvitals.packages, or sources.googleplay.packages)")
	}

	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("playvitals: read %s: %w", keyPath, err)
	}
	tokens, err := googleplay.TokenSourceForScopes(context.Background(), key, Scope)
	if err != nil {
		return nil, err
	}

	days := cfg.BackfillDays
	if days <= 0 {
		days = 30
	}

	return &Source{
		Packages:     normalizePackages(packages),
		BackfillDays: days,
		BaseURL:      DefaultBaseURL,
		Tokens:       tokens,
		Client:       &http.Client{Timeout: 2 * time.Minute},
		Log:          log,
		Now:          time.Now,
	}, nil
}

func normalizePackages(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return DefaultPollInterval }

// Check implements core.Checker: it asks one package how fresh its data is,
// which exercises the key, the scope, the API being enabled and the Play
// Console grant in a single request — and costs nothing.
func (s *Source) Check(ctx context.Context) error {
	if len(s.Packages) == 0 {
		return fmt.Errorf("playvitals: no packages configured")
	}
	_, err := s.Freshness(ctx, s.Packages[0], errorCountSet)
	return err
}

// state is the persisted cursor: the newest completed day already emitted, per
// package.
type state struct {
	Days map[string]string `json:"days"`
}

func decodeState(raw []byte) state {
	var st state
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			st = state{}
		}
	}
	if st.Days == nil {
		st.Days = map[string]string{}
	}
	return st
}

// Poll reads every package's vitals from its cursor up to whatever Play says
// is fresh, and returns the events they imply.
//
// A failure on one package does not lose the others: the cursor for a package
// that succeeded is still advanced, so a single app with a revoked grant
// cannot stall the rest of the account.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)

	var (
		events   []core.Event
		firstErr error
	)
	for _, pkg := range s.Packages {
		evs, newest, err := s.pollPackage(ctx, pkg, st.Days[pkg])
		events = append(events, evs...)
		if newest != "" && newest > st.Days[pkg] {
			st.Days[pkg] = newest
		}
		if err != nil {
			s.Log.Warn("playvitals: package failed", "package", pkg, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("playvitals: encode state: %w", err)
	}
	return events, out, firstErr
}

// pollPackage reads one app's window and returns its events plus the newest
// day they cover.
func (s *Source) pollPackage(ctx context.Context, pkg, cursor string) ([]core.Event, string, error) {
	end, err := s.Freshness(ctx, pkg, errorCountSet)
	if err != nil {
		return nil, "", err
	}
	newest := dayBefore(end)
	if newest == "" {
		return nil, "", fmt.Errorf("playvitals: %s reported an unreadable freshness", pkg)
	}
	if cursor != "" && cursor >= newest {
		return nil, cursor, nil // nothing new has settled
	}

	from := s.startDay(cursor, newest)
	if core.DayOf(from) > newest {
		return nil, cursor, nil
	}

	counts, err := s.Query(ctx, pkg, errorCountSet,
		[]string{dimVersionCode, dimReportType},
		[]string{metricErrorReportCount, metricDistinctUsers}, from, end)
	if err != nil {
		return nil, "", err
	}

	// The rate sets are a nicety, not a requirement: they put the same numbers
	// the Play Console shows on the drop's payload. A failure there must not
	// cost the counts, which are what the fight is actually made of.
	crashRates := s.rates(ctx, pkg, crashRateSet, metricCrashRate, from, end)
	anrRates := s.rates(ctx, pkg, anrRateSet, metricANRRate, from, end)

	return s.buildEvents(pkg, counts, crashRates, anrRates, cursor, newest), newest, nil
}

// startDay is where a poll begins: the day after the cursor, or the backfill
// floor on a first run.
func (s *Source) startDay(cursor, newest string) time.Time {
	now := s.now().UTC()
	floor := now.AddDate(0, 0, -s.BackfillDays)
	if cursor == "" {
		return floor
	}
	if t, err := time.Parse(core.DayLayout, cursor); err == nil {
		next := t.AddDate(0, 0, 1)
		if next.After(floor) {
			return next
		}
	}
	return floor
}

// rates reads one app-wide daily rate series, returning an empty map on
// failure rather than an error.
func (s *Source) rates(ctx context.Context, pkg, metricSet, metric string, from time.Time, end DateTime) map[string]float64 {
	rows, err := s.Query(ctx, pkg, metricSet, nil, []string{metric}, from, end)
	if err != nil {
		s.Log.Debug("playvitals: rate query failed", "package", pkg, "metric_set", metricSet, "error", err)
		return map[string]float64{}
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		if v, ok := r.Metric(metric); ok {
			out[r.Day] = v
		}
	}
	return out
}

// dayTotals accumulates one (package, day) while the rows are walked.
type dayTotals struct {
	crashes float64
	users   int
}

// buildEvents turns decoded rows into Loot events: one silent `crash` per
// (day, version, report type), and one `crash_day` heartbeat per day.
//
// Only days strictly newer than the cursor are emitted. Re-reading a day is
// harmless — every key is deduped — but emitting nothing is cheaper than
// emitting a hundred duplicates every six hours.
func (s *Source) buildEvents(pkg string, counts []Row, crashRates, anrRates map[string]float64, cursor, newest string) []core.Event {
	now := s.now().UTC()
	byDay := map[string]*dayTotals{}
	var events []core.Event

	for _, row := range counts {
		if row.Day == "" || row.Day > newest || (cursor != "" && row.Day <= cursor) {
			continue
		}
		kind := ""
		switch strings.ToUpper(row.Dim(dimReportType)) {
		case reportTypeCrash:
			kind = core.BossKindCrash
		case reportTypeANR:
			kind = core.BossKindANR
		default:
			continue // NON_FATAL and anything Play adds later
		}

		count, ok := row.Metric(metricErrorReportCount)
		if !ok || count <= 0 {
			continue
		}
		users := 0
		if u, ok := row.Metric(metricDistinctUsers); ok {
			users = int(u + 0.5)
		}

		version := row.Dim(dimVersionCode)
		payload := core.CrashPayload{
			Version:       version,
			IssueTitle:    versionTitle(row, kind),
			UsersAffected: users,
			Kind:          kind,
			CrashRate:     crashRates[row.Day],
			ANRRate:       anrRates[row.Day],
			URL:           consoleURL(pkg, kind),
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			s.Log.Error("playvitals: encode payload", "error", err)
			continue
		}

		occurred := dayTime(row.Day)
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindCrash,
			App:        pkg,
			OccurredAt: occurred,
			ObservedAt: now,
			Day:        row.Day,
			Quantity:   int(count + 0.5),
			DedupeKey:  fmt.Sprintf("%s:crash:%s:%s:%s:%s", Name, pkg, row.Day, version, kind),
			Silent:     true,
			Payload:    json.RawMessage(encoded),
		})

		t := byDay[row.Day]
		if t == nil {
			t = &dayTotals{}
			byDay[row.Day] = t
		}
		t.crashes += count
		if users > t.users {
			t.users = users
		}
	}

	// The heartbeat covers every day in the window, including the ones with no
	// crash rows at all — that is its whole purpose.
	observed := make(map[string]bool, len(byDay)+len(crashRates))
	for day := range byDay {
		observed[day] = true
	}
	for day := range crashRates {
		observed[day] = true
	}
	for _, day := range s.windowDays(cursor, newest, observed) {
		t := byDay[day]
		if t == nil {
			t = &dayTotals{}
		}
		payload := core.CrashPayload{
			UsersAffected: t.users,
			Kind:          core.BossKindCrash,
			CrashRate:     crashRates[day],
			ANRRate:       anrRates[day],
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		occurred := dayTime(day)
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindCrashDay,
			App:        pkg,
			OccurredAt: occurred,
			ObservedAt: now,
			Day:        day,
			Quantity:   int(t.crashes + 0.5),
			DedupeKey:  fmt.Sprintf("%s:crash_day:%s:%s", Name, pkg, day),
			Silent:     true,
			Payload:    json.RawMessage(encoded),
		})
	}

	sort.SliceStable(events, func(i, j int) bool { return events[i].Day < events[j].Day })
	return events
}

// windowDays lists the days a heartbeat should cover: everything after the
// cursor up to and including the newest settled day.
//
// `observed` is every day the API said anything about at all, and it is the
// floor. A heartbeat is an assertion — "I looked, and the total was N" — so it
// must never be invented for a day before the app had any data, or a brand new
// app would arrive with thirty days of fabricated zeros and its first boss
// could be declared slain on evidence nobody gathered.
func (s *Source) windowDays(cursor, newest string, observed map[string]bool) []string {
	end, err := time.Parse(core.DayLayout, newest)
	if err != nil || len(observed) == 0 {
		return nil
	}
	earliest := ""
	for day := range observed {
		if earliest == "" || day < earliest {
			earliest = day
		}
	}

	start := end.AddDate(0, 0, -(s.BackfillDays - 1))
	if cursor != "" {
		if t, err := time.Parse(core.DayLayout, cursor); err == nil && t.AddDate(0, 0, 1).After(start) {
			start = t.AddDate(0, 0, 1)
		}
	}
	if earliest != "" {
		if t, err := time.Parse(core.DayLayout, earliest); err == nil && t.After(start) {
			start = t
		}
	}

	var out []string
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		out = append(out, core.DayOf(day))
	}
	return out
}

// versionTitle is the line under the boss's name for a version-wide fight.
func versionTitle(row Row, kind string) string {
	noun := "Crashes"
	if kind == core.BossKindANR {
		noun = "ANRs"
	}
	label := row.Label(dimVersionCode)
	if label == "" {
		return noun
	}
	return noun + " in " + label
}

// consoleURL points at the Play Console page that actually shows the crash.
// Play has no per-issue deep link Loot can construct from a version code, so
// this is the app's vitals overview — which is one click from the answer.
func consoleURL(pkg, kind string) string {
	page := "crashes"
	if kind == core.BossKindANR {
		page = "anrs"
	}
	return "https://play.google.com/console/developers/app/" + pkg + "/vitals/" + page
}

// dayTime is midnight UTC on a day string, used as the event's occurred_at.
// Vitals are stated in Pacific time, but Event.Day carries the report day
// verbatim and the vault never sums a crash, so the clock here only has to be
// stable.
func dayTime(day string) time.Time {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

func (s *Source) baseURL() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return s.BaseURL
}

func (s *Source) client() *http.Client {
	if s.Client == nil {
		return http.DefaultClient
	}
	return s.Client
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
