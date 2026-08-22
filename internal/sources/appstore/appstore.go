package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	// Apple's report day is a Pacific day, and Loot's release image is
	// distroless: it has no /usr/share/zoneinfo. Embedding the tz database
	// costs ~450 KB and keeps the cursor honest in a container.
	_ "time/tzdata"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "appstore"

const (
	// pollInterval is deliberately unhurried: a daily report only appears
	// once a day, and Apple rate-limits the reports endpoint.
	pollInterval = time.Hour

	// defaultBackfillDays matches config.Default().
	defaultBackfillDays = 30

	// maxBackfillDays caps how far back a first run reaches. Apple keeps
	// daily reports for about a year; asking for older days only produces
	// 404s.
	maxBackfillDays = 365

	// maxDaysPerPoll bounds one poll's request count so a source that has
	// been offline for months catches up over a few hours instead of firing
	// hundreds of requests at once.
	maxDaysPerPoll = 60

	// assumeEmptyAfterDays is how long a missing report is given before Loot
	// concludes the day was simply empty and moves the cursor past it. Apple
	// answers "not generated yet" and "you sold nothing that day" with the
	// same 404, so without this a developer with one quiet day would stall
	// forever on it.
	assumeEmptyAfterDays = 3

	// maxSkipAttempts is how many times a stepped-over day is asked for again
	// before it is written off as genuinely empty. Moving the cursor past a
	// missing day is a guess, and the cheap way to check the guess is to ask
	// again tomorrow: a report Apple published late — which happens after an
	// outage on their side — would otherwise be lost for good, and a day of
	// real revenue would simply never appear in the vault.
	maxSkipAttempts = 5

	// maxSubsUnavailable is how many consecutive 404s it takes before Loot
	// stops asking for the subscription report. An account with no
	// subscriptions never has one, and asking daily forever is noise.
	maxSubsUnavailable = 3

	// subsRecheckDays is how long a switched-off subscription report is left
	// alone before one more attempt, so an app that starts selling
	// subscriptions is picked up without a restart.
	subsRecheckDays = 7
)

// reportTZ is the timezone Apple's report day is measured in. A "daily" report
// covers a Pacific day and is published a few hours after that day closes.
const reportTZ = "America/Los_Angeles"

var (
	locOnce sync.Once
	loc     *time.Location
)

// reportLocation returns the Pacific location, falling back to a fixed -08:00
// zone if the tz database is somehow unavailable. Being an hour out at the
// boundary is survivable; refusing to poll is not.
func reportLocation() *time.Location {
	locOnce.Do(func() {
		l, err := time.LoadLocation(reportTZ)
		if err != nil {
			l = time.FixedZone("PST", -8*60*60)
		}
		loc = l
	})
	return loc
}

// state is the persisted cursor.
//
// LastCompleteDay is the newest report day fully ingested. PendingDays is what
// the next poll will attempt: it is written when a day's report is not ready
// yet, so the state file explains a stalled cursor without needing the logs.
type state struct {
	LastCompleteDay string   `json:"last_complete_day,omitempty"`
	PendingDays     []string `json:"pending_days,omitempty"`
	// SkippedDays are report days the cursor was moved past without a report,
	// keyed by day. They are asked for again on later polls until Apple
	// answers or maxSkipAttempts is used up.
	SkippedDays           map[string]SkippedDay `json:"skipped_days,omitempty"`
	SubsUnavailableStreak int                   `json:"subs_unavailable_streak,omitempty"`
	SubsDisabledSince     string                `json:"subs_disabled_since,omitempty"`
	SubsDay               string                `json:"subs_day,omitempty"`
	Seeded                bool                  `json:"seeded"`
}

// SkippedDay records one stepped-over report day: how many times it has been
// asked for, and on which calendar day it was last tried. The last-try day is
// what spreads the attempts across days rather than burning all five inside
// one morning's polls.
type SkippedDay struct {
	Attempts int    `json:"attempts"`
	LastTry  string `json:"last_try,omitempty"`
}

// Source implements core.Source over the App Store Connect sales reports.
type Source struct {
	KeyID        string
	IssuerID     string
	VendorNumber string
	Apps         []string
	BackfillDays int

	// Since optionally pins the first-run floor to a fixed date
	// (YYYY-MM-DD), overriding BackfillDays. Set by `loot serve --since`.
	Since string

	BaseURL string
	Client  *http.Client
	Log     *slog.Logger
	// Now is swappable for tests.
	Now func() time.Time

	tokens *tokenCache
}

// New returns an App Store Connect source for cfg. The caller checks
// cfg.Configured() first; an unconfigured section means "the user did not ask
// for this source", not an error.
func New(cfg config.AppStore, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if !cfg.Configured() {
		return nil, errors.New("appstore: key_id, issuer_id, private_key_path and vendor_number are all required")
	}
	key, err := LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	backfill := cfg.BackfillDays
	if backfill <= 0 {
		backfill = defaultBackfillDays
	}
	if backfill > maxBackfillDays {
		backfill = maxBackfillDays
	}

	return &Source{
		KeyID:        cfg.KeyID,
		IssuerID:     cfg.IssuerID,
		VendorNumber: cfg.VendorNumber,
		Apps:         cfg.Apps,
		BackfillDays: backfill,
		BaseURL:      DefaultBaseURL,
		Client:       &http.Client{Timeout: 60 * time.Second},
		Log:          log,
		Now:          time.Now,
		tokens:       &tokenCache{keyID: cfg.KeyID, issuerID: cfg.IssuerID, key: key},
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return pollInterval }

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Source) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// today returns the current report day (a Pacific calendar day).
func (s *Source) today() string {
	return s.now().In(reportLocation()).Format(core.DayLayout)
}

// Check implements core.Checker: it mints a token and asks for a real report,
// which is the only way to prove that the key, the issuer and the vendor
// number belong together. A 404 counts as success — it means Apple accepted
// the credentials and simply has nothing for that day yet.
func (s *Source) Check(ctx context.Context) error {
	if _, err := s.tokens.bearer(s.now()); err != nil {
		return err
	}

	today := s.today()
	var lastErr error
	// Yesterday's report often does not exist before ~08:00 Pacific, so fall
	// back to the day before rather than reporting a healthy setup as broken.
	for _, day := range []string{addDays(today, -1), addDays(today, -2)} {
		_, err := s.fetchReport(ctx, salesSummaryDaily, day)
		switch {
		case err == nil, errors.Is(err, errNoSales):
			return nil
		case errors.Is(err, errNotReady):
			lastErr = nil
		case errors.Is(err, errCredentials):
			return err
		default:
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	// Both days answered "no report". Credentials were accepted, so this is a
	// working configuration that has not sold anything yet.
	return nil
}

// Poll fetches every report day between the cursor and yesterday.
//
// Days are walked oldest first. A day whose report Apple has not published yet
// stops the walk: the cursor stays put, the remaining days are recorded as
// pending, and the next poll picks up where this one left off. That is the
// normal state of affairs for a few hours every morning and is not an error —
// returning one would light up `last_error` in the UI every single day.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now()
	observed := now.UTC()

	today := s.today()
	yesterday := addDays(today, -1)

	days := s.daysToFetch(st, today)

	var (
		events    []core.Event
		newest    string
		pending   []string
		pollError error
	)

	for i, day := range days {
		report, err := s.fetchReport(ctx, salesSummaryDaily, day)
		if err != nil {
			if errors.Is(err, errNoSales) {
				// Apple says "there were no sales for the date specified" —
				// but, seen for real (Aug 2026, the owner's own $29.99
				// purchase), Apple gives that same answer for a day whose
				// report simply hasn't been generated yet. So the answer is
				// only provisional while the day is recent: step over it (an
				// empty day must not stall the walk) but keep asking on later
				// polls until it's old enough to believe.
				st.LastCompleteDay = maxDay(st.LastCompleteDay, day)
				if day >= addDays(yesterday, -assumeEmptyAfterDays) {
					s.recordSkipped(&st, day, today)
				} else {
					delete(st.SkippedDays, day)
				}
				continue
			}
			if errors.Is(err, errNotReady) {
				// Old enough that "not generated yet" is no longer credible:
				// the day was probably empty, so step over it — but write it
				// down and ask again over the next few days, because "probably"
				// is not the same as "certainly" and a late report is a day of
				// revenue that would otherwise never arrive.
				if day < addDays(yesterday, -assumeEmptyAfterDays) {
					st.LastCompleteDay = maxDay(st.LastCompleteDay, day)
					s.recordSkipped(&st, day, today)
					continue
				}
				s.log().Debug("appstore: report not ready yet", "day", day, "error", err)
				pending = days[i:]
				break
			}
			// A real failure (network, credentials, rate limit). Keep what we
			// already collected and report the error; the cursor stays where
			// the last good day left it.
			pollError = err
			pending = days[i:]
			break
		}

		dayEvents, err := BuildSalesEvents(report, day, s.Apps, observed)
		if err != nil {
			pollError = err
			pending = days[i:]
			break
		}
		events = append(events, dayEvents...)
		st.LastCompleteDay = maxDay(st.LastCompleteDay, day)
		// A day that was stepped over and has now answered is settled: stop
		// asking about it.
		delete(st.SkippedDays, day)
		newest = maxDay(newest, day)
	}

	st.Seeded = true
	st.PendingDays = pending

	// Snapshots are absolute counts and only the newest one is ever read, so
	// a 30 day backfill still asks for exactly one subscription report.
	if newest != "" && newest != st.SubsDay && s.wantSubscriptions(st, newest) {
		subEvents, err := s.fetchSubscriptions(ctx, newest, observed, &st)
		if err != nil && pollError == nil {
			pollError = err
		}
		events = append(events, subEvents...)
	}

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("appstore: encode state: %w", err)
	}
	return events, out, pollError
}

// wantSubscriptions reports whether to ask for the subscription report at all.
// An account with no subscriptions never has one, so after a few refusals Loot
// stops asking — but it looks again once a week, because "no subscriptions" is
// a state a developer can change on any given Tuesday.
func (s *Source) wantSubscriptions(st state, day string) bool {
	if st.SubsUnavailableStreak < maxSubsUnavailable {
		return true
	}
	if st.SubsDisabledSince == "" {
		return true
	}
	return day >= addDays(st.SubsDisabledSince, subsRecheckDays)
}

// fetchSubscriptions asks for one day's subscription summary, tracking the
// consecutive-404 streak that eventually switches the report off.
func (s *Source) fetchSubscriptions(ctx context.Context, day string, observed time.Time, st *state) ([]core.Event, error) {
	report, err := s.fetchReport(ctx, subscriptionSummaryDaily, day)
	switch {
	case err == nil:
	case errors.Is(err, errNotReady), errors.Is(err, errNoSales):
		st.SubsUnavailableStreak++
		if st.SubsUnavailableStreak >= maxSubsUnavailable {
			st.SubsDisabledSince = day
			s.log().Info("appstore: no subscription report; assuming this account has no subscriptions and checking again in a week",
				"day", day, "tries", st.SubsUnavailableStreak)
		}
		return nil, nil
	default:
		// A credentials or network failure here is worth surfacing, but it
		// must not undo the sales events collected alongside it.
		return nil, err
	}

	events, err := BuildSubscriptionEvents(report, day, s.Apps, observed)
	if err != nil {
		return nil, err
	}
	st.SubsUnavailableStreak = 0
	st.SubsDisabledSince = ""
	st.SubsDay = day
	return events, nil
}

// recordSkipped notes one more failed attempt at a stepped-over day, or writes
// the day off once the attempts are used up. At most one attempt is counted
// per calendar day, so the five tries are spread over five days rather than
// spent in five consecutive hourly polls.
func (s *Source) recordSkipped(st *state, day, today string) {
	if st.SkippedDays == nil {
		st.SkippedDays = map[string]SkippedDay{}
	}
	rec := st.SkippedDays[day]
	// A recent day is retried on every poll — a report published at noon
	// must not wait a day for the next attempt. Once the day is older than
	// the grace window, attempts throttle to one per calendar day.
	recent := day >= addDays(addDays(today, -1), -assumeEmptyAfterDays)
	if !recent && rec.LastTry == today && rec.Attempts > 0 {
		return
	}
	if recent {
		// Recent retries also don't consume the write-off budget: the five
		// counted attempts start once the day is old enough to distrust.
		rec.LastTry = today
		st.SkippedDays[day] = rec
		s.log().Debug("appstore: report still absent for a recent day; will ask again next poll", "day", day)
		return
	}
	rec.Attempts++
	rec.LastTry = today

	if rec.Attempts >= maxSkipAttempts {
		delete(st.SkippedDays, day)
		s.log().Info("appstore: still no report for this day after several tries; treating it as an empty day",
			"day", day, "attempts", rec.Attempts)
		return
	}
	st.SkippedDays[day] = rec
	s.log().Debug("appstore: no report for an old day; will ask again",
		"day", day, "attempts", rec.Attempts)
}

// daysToFetch lists the report days this poll should try, oldest first: the
// pending days from last time, everything from the cursor to yesterday, and
// any stepped-over day that is due for another attempt.
func (s *Source) daysToFetch(st state, today string) []string {
	yesterday := addDays(today, -1)
	retention := addDays(today, -maxBackfillDays)

	start := s.firstRunFloor(today)
	if st.Seeded && st.LastCompleteDay != "" {
		start = maxDay(start, addDays(st.LastCompleteDay, 1))
	}
	// Never reach past Apple's retention window, however long Loot was off.
	if start < retention {
		start = retention
	}
	for _, p := range st.PendingDays {
		if p < start && p >= retention {
			start = p
		}
	}

	seen := map[string]bool{}
	var days []string
	add := func(day string) {
		if seen[day] {
			return
		}
		seen[day] = true
		days = append(days, day)
	}

	// Retries first: they are older than the cursor, and the per-poll cap must
	// not be spent on the forward window before they get a look in.
	for day, rec := range st.SkippedDays {
		// A recent day retries every poll; only older days throttle to one
		// attempt per calendar day.
		recent := day >= addDays(yesterday, -assumeEmptyAfterDays)
		if rec.Attempts >= maxSkipAttempts || (!recent && rec.LastTry == today) {
			continue
		}
		if day > yesterday || day < retention {
			continue
		}
		add(day)
	}
	for day := start; day <= yesterday; day = addDays(day, 1) {
		add(day)
		if len(days) >= maxDaysPerPoll {
			break
		}
	}
	sort.Strings(days)
	return days
}

// firstRunFloor is the oldest day the very first poll reads. backfill_days of
// 0 or 1 both mean "just yesterday": there is no useful sense in which a
// ledger source reports nothing at all, since today's report does not exist.
func (s *Source) firstRunFloor(today string) string {
	if s.Since != "" {
		if _, err := time.Parse(core.DayLayout, s.Since); err == nil {
			return s.Since
		}
		s.log().Warn("appstore: ignoring unparsable --since", "since", s.Since)
	}
	days := s.BackfillDays
	if days <= 0 {
		days = defaultBackfillDays
	}
	if days > maxBackfillDays {
		days = maxBackfillDays
	}
	return addDays(today, -days)
}

func decodeState(raw []byte) state {
	var st state
	if len(raw) == 0 {
		st.SkippedDays = map[string]SkippedDay{}
		return st
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return state{SkippedDays: map[string]SkippedDay{}}
	}
	if st.SkippedDays == nil {
		st.SkippedDays = map[string]SkippedDay{}
	}
	return st
}

// addDays shifts a YYYY-MM-DD day. An unparsable day is returned unchanged,
// which keeps a corrupt state file from panicking the scheduler.
func addDays(day string, n int) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format(core.DayLayout)
}

// maxDay returns the later of two YYYY-MM-DD days, which compare correctly as
// strings.
func maxDay(a, b string) string {
	if a > b {
		return a
	}
	return b
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
