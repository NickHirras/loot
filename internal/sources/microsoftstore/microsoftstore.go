package microsoftstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "microsoftstore"

const (
	// pollInterval is unhurried on purpose: analytics is a daily aggregate
	// that is only read once it has settled, so nothing is gained by asking
	// more often than four times a day.
	pollInterval = 6 * time.Hour

	// defaultBackfillDays matches config.Default().
	defaultBackfillDays = 30

	// maxBackfillDays caps how far back a first run — or a long outage —
	// reaches.
	maxBackfillDays = 365

	// settleLagDays is how old a day must be before Loot will read it.
	// Partner Center revises a day's acquisition numbers upward for a day or
	// two after it first appears, and a summary drop that reported half a day
	// would be wrong forever, because a summary is only emitted once.
	settleLagDays = 3

	// resweepDays is how many of the most recently settled days are re-read on
	// every poll, in case a row arrived after the day settled. Rows are
	// grouped onto stable dedupe keys before they are emitted, so a re-sweep
	// that finds nothing new emits nothing new.
	resweepDays = 3

	// maxWindowDays bounds one request's date range so a year of backfill
	// arrives as a handful of moderate responses rather than one enormous one.
	maxWindowDays = 90

	// maxSubsUnavailable is how many consecutive empty or refused subscription
	// responses it takes before Loot stops asking. Most accounts have no
	// subscription add-ons at all.
	maxSubsUnavailable = 3

	// subsRecheckDays is how long a switched-off subscription query is left
	// alone before one more attempt, so an app that starts selling
	// subscriptions is picked up without a restart.
	subsRecheckDays = 7
)

// API paths, relative to /v1.0/my/.
const (
	appAcquisitionsPath = "analytics/appacquisitions"
	iapAcquisitionsPath = "analytics/inappacquisitions"
	subscriptionsPath   = "analytics/subscriptions"
	applicationsPath    = "applications"
)

// Page sizes. The acquisitions endpoints accept (and default to) 10000 rows;
// the subscriptions endpoint caps at 100. Both page with @nextLink.
const (
	acquisitionsPageSize  = 10000
	subscriptionsPageSize = 100
	applicationsPageSize  = 100
)

// state is the persisted cursor.
//
// LastSettledDay is the newest settled day already ingested, per Store ID.
// Everything after it — and the last [resweepDays] days regardless — is read
// on the next poll.
type state struct {
	LastSettledDay        map[string]string `json:"last_settled_day,omitempty"`
	SubsDay               map[string]string `json:"subs_day,omitempty"`
	SubsUnavailableStreak int               `json:"subs_unavailable_streak,omitempty"`
	SubsDisabledSince     string            `json:"subs_disabled_since,omitempty"`
	Seeded                bool              `json:"seeded"`
}

// app is one app to poll: its Store ID and, when Loot discovered it rather
// than being told about it, its name.
type app struct {
	ID   string `json:"id"`
	Name string `json:"primaryName"`
}

// Source implements core.Source over the Microsoft Store analytics API.
type Source struct {
	TenantID     string
	ClientID     string
	Apps         []string
	BackfillDays int

	// Since optionally pins the first-run floor to a fixed date
	// (YYYY-MM-DD), overriding BackfillDays. Set by `loot serve --since`.
	Since string

	BaseURL  string
	LoginURL string
	Client   *http.Client
	Log      *slog.Logger
	// Now is swappable for tests.
	Now func() time.Time

	tokens *tokenCache
}

// New returns a Microsoft Store source for cfg. The caller checks
// cfg.Configured() first; an unconfigured section means "the user did not ask
// for this source", not an error.
func New(cfg config.MicrosoftStore, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if !cfg.Configured() {
		return nil, errors.New("microsoftstore: tenant_id, client_id and client_secret are all required")
	}

	backfill := cfg.BackfillDays
	if backfill <= 0 {
		backfill = defaultBackfillDays
	}
	if backfill > maxBackfillDays {
		backfill = maxBackfillDays
	}

	client := &http.Client{Timeout: 60 * time.Second}
	return &Source{
		TenantID:     cfg.TenantID,
		ClientID:     cfg.ClientID,
		Apps:         trimmed(cfg.Apps),
		BackfillDays: backfill,
		BaseURL:      DefaultBaseURL,
		LoginURL:     DefaultLoginURL,
		Client:       client,
		Log:          log,
		Now:          time.Now,
		tokens: &tokenCache{
			tenantID:     cfg.TenantID,
			clientID:     cfg.ClientID,
			clientSecret: cfg.ClientSecret,
		},
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return pollInterval }

// Poll reads every settled day between each app's cursor and the settlement
// horizon, emitting silent ledger rows and one chest-bound summary per (app,
// day).
//
// One app failing does not stop the others: its cursor stays put, the rest
// carry on, and the error is returned alongside whatever was collected — which
// is exactly what the scheduler expects of a partially successful poll.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now()
	observed := now.UTC()
	today := core.DayOf(now)
	settled := s.settledThrough(today)

	apps, err := s.appList(ctx)
	if err != nil {
		return nil, raw, err
	}

	var (
		events    []core.Event
		pollError error
	)

	for _, a := range apps {
		from := s.windowStart(st, a.ID, today, settled)
		if from > settled {
			continue
		}

		appEvents, err := s.collect(ctx, a, from, settled, observed)
		events = append(events, appEvents...)
		if err != nil {
			s.logger().Warn("microsoftstore: app failed", "store_id", a.ID, "error", err)
			if pollError == nil {
				pollError = err
			}
			continue
		}
		st.LastSettledDay[a.ID] = maxDay(st.LastSettledDay[a.ID], settled)

		if st.SubsDay[a.ID] == settled || !s.wantSubscriptions(st, settled) {
			continue
		}
		subEvents, err := s.fetchSubscriptions(ctx, a, settled, observed, &st)
		if err != nil && pollError == nil {
			pollError = err
		}
		events = append(events, subEvents...)
	}

	st.Seeded = true
	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("microsoftstore: encode state: %w", err)
	}
	return events, out, pollError
}

// Check implements core.Checker. It mints a real token — which is where a
// wrong tenant, client id or secret shows up — and then proves that Partner
// Center will actually serve this account's data, either by listing the
// applications or by asking for one settled day of the first configured app.
func (s *Source) Check(ctx context.Context) error {
	if _, err := s.bearer(ctx); err != nil {
		return err
	}

	if len(s.Apps) > 0 {
		day := s.settledThrough(core.DayOf(s.now()))
		_, err := s.fetchAcquisitions(ctx, appAcquisitionsPath, s.Apps[0], day, day, false)
		switch {
		case err == nil, errors.Is(err, errNoData):
			// Credentials accepted. A quiet day is not a broken setup.
			return nil
		default:
			return err
		}
	}

	apps, err := s.discover(ctx)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return errors.New("microsoftstore: partner center returned no apps; " +
			"set sources.microsoftstore.apps to the Store IDs you want (Partner Center → your app → Product identity)")
	}
	return nil
}

// collect reads one app's window from both acquisition endpoints and turns it
// into events. The window is split so no single request spans more than
// [maxWindowDays].
func (s *Source) collect(ctx context.Context, a app, from, to string, observed time.Time) ([]core.Event, error) {
	var rows []Acquisition
	for _, w := range windows(from, to, maxWindowDays) {
		appRows, err := s.fetchAcquisitions(ctx, appAcquisitionsPath, a.ID, w.from, w.to, false)
		if err != nil && !errors.Is(err, errNoData) {
			return nil, err
		}
		rows = append(rows, appRows...)

		// An app with no add-ons answers with an empty list or a 404. Neither
		// is a failure, and neither should stop its app acquisitions landing.
		iapRows, err := s.fetchAcquisitions(ctx, iapAcquisitionsPath, a.ID, w.from, w.to, true)
		if err != nil && !errors.Is(err, errNoData) {
			return nil, err
		}
		rows = append(rows, iapRows...)
	}
	return BuildEvents(a.ID, a.Name, rows, from, to, observed)
}

// fetchAcquisitions reads one endpoint for one app over one date window.
//
// groupby is not an optimisation here, it is required: without it the API
// aggregates the whole range down to one row per day and the market — the
// field every settlement on the Hearth is founded on — never appears. The
// dimensions asked for are exactly the ones Loot keys on, which keeps the
// response small and leaves the age and gender columns unrequested.
func (s *Source) fetchAcquisitions(ctx context.Context, path, storeID, from, to string, addOn bool) ([]Acquisition, error) {
	q := url.Values{}
	q.Set("applicationId", storeID)
	q.Set("startDate", from)
	q.Set("endDate", to)
	q.Set("aggregationLevel", "day")
	q.Set("top", strconv.Itoa(acquisitionsPageSize))
	q.Set("skip", "0")
	q.Set("orderby", "date")
	if addOn {
		q.Set("groupby", "date,applicationName,inAppProductName,acquisitionType,market")
	} else {
		q.Set("groupby", "date,applicationName,acquisitionType,market")
	}

	raw, err := fetchPages[apiAcquisition](ctx, s, path, q)
	if err != nil {
		return nil, err
	}

	rows := make([]Acquisition, 0, len(raw))
	for _, r := range raw {
		row, ok := normalize(r, addOn)
		if !ok {
			continue
		}
		if row.StoreID == "" {
			row.StoreID = storeID
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// fetchSubscriptions asks for one settled day of subscription counts, tracking
// the streak of empty answers that eventually switches the query off. An
// account with no subscription add-ons never has any, and asking four times a
// day forever is noise.
func (s *Source) fetchSubscriptions(ctx context.Context, a app, date string, observed time.Time, st *state) ([]core.Event, error) {
	q := url.Values{}
	q.Set("applicationId", a.ID)
	q.Set("startDate", date)
	q.Set("endDate", date)
	q.Set("aggregationLevel", "day")
	q.Set("top", strconv.Itoa(subscriptionsPageSize))
	q.Set("skip", "0")

	rows, err := fetchPages[apiSubscription](ctx, s, subscriptionsPath, q)
	switch {
	case err == nil:
	case errors.Is(err, errNoData):
		s.markSubsUnavailable(st, date)
		return nil, nil
	default:
		// A credentials or network failure is worth surfacing, but it must not
		// undo the acquisitions collected alongside it.
		return nil, err
	}

	events, err := BuildSubscriptionEvents(rows, a.ID, a.Name, date, observed)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		s.markSubsUnavailable(st, date)
		return nil, nil
	}

	st.SubsUnavailableStreak = 0
	st.SubsDisabledSince = ""
	st.SubsDay[a.ID] = date
	return events, nil
}

func (s *Source) markSubsUnavailable(st *state, date string) {
	st.SubsUnavailableStreak++
	if st.SubsUnavailableStreak == maxSubsUnavailable {
		st.SubsDisabledSince = date
		s.logger().Info("microsoftstore: no subscription data; assuming this account has no subscription add-ons and checking again in a week",
			"day", date, "tries", st.SubsUnavailableStreak)
	}
}

// wantSubscriptions reports whether to ask for subscription counts at all.
func (s *Source) wantSubscriptions(st state, day string) bool {
	if st.SubsUnavailableStreak < maxSubsUnavailable {
		return true
	}
	if st.SubsDisabledSince == "" {
		return true
	}
	return day >= addDays(st.SubsDisabledSince, subsRecheckDays)
}

// appList is the set of apps to poll: the configured Store IDs, or every app
// on the account when none are configured.
func (s *Source) appList(ctx context.Context) ([]app, error) {
	if len(s.Apps) > 0 {
		apps := make([]app, 0, len(s.Apps))
		for _, id := range s.Apps {
			apps = append(apps, app{ID: id})
		}
		return apps, nil
	}

	apps, err := s.discover(ctx)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, errors.New("microsoftstore: no apps found on this account; " +
			"set sources.microsoftstore.apps to the Store IDs you want")
	}
	return apps, nil
}

// discover lists the apps registered to the Partner Center account. The
// endpoint belongs to the submission API rather than the analytics API, and
// some accounts will not serve it, so its failure explains the way out:
// configure the Store IDs by hand.
func (s *Source) discover(ctx context.Context) ([]app, error) {
	q := url.Values{}
	q.Set("top", strconv.Itoa(applicationsPageSize))
	q.Set("skip", "0")

	apps, err := fetchPages[app](ctx, s, applicationsPath, q)
	if err != nil {
		if errors.Is(err, errNoData) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w — or list your apps' Store IDs in sources.microsoftstore.apps "+
			"(Partner Center → your app → Product identity) so Loot does not need to discover them", err)
	}

	out := make([]app, 0, len(apps))
	for _, a := range apps {
		if a.ID != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// windowStart is the oldest day this poll reads for one app: the day after the
// cursor, pulled back over the recently settled days so a late row is still
// picked up, and clamped to the retention horizon.
func (s *Source) windowStart(st state, storeID, today, settled string) string {
	start := s.firstRunFloor(today)
	if st.Seeded {
		if last := st.LastSettledDay[storeID]; last != "" {
			start = addDays(last, 1)
			if sweep := addDays(settled, -(resweepDays - 1)); sweep < start {
				start = sweep
			}
		}
	}
	if retention := addDays(today, -maxBackfillDays); start < retention {
		start = retention
	}
	return start
}

// firstRunFloor is the oldest day the very first poll reads.
func (s *Source) firstRunFloor(today string) string {
	if s.Since != "" {
		if _, err := time.Parse(core.DayLayout, s.Since); err == nil {
			return s.Since
		}
		s.logger().Warn("microsoftstore: ignoring unparsable --since", "since", s.Since)
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

// settledThrough is the newest day considered final. Analytics dates are
// calendar days in UTC.
func (s *Source) settledThrough(today string) string { return addDays(today, -settleLagDays) }

// window is one request's date range.
type window struct{ from, to string }

// windows splits [from, to] into consecutive ranges of at most size days.
func windows(from, to string, size int) []window {
	if from > to {
		return nil
	}
	var out []window
	for start := from; start <= to; start = addDays(start, size) {
		end := addDays(start, size-1)
		if end > to {
			end = to
		}
		out = append(out, window{from: start, to: end})
		if end == to {
			break
		}
	}
	return out
}

func decodeState(raw []byte) state {
	var st state
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			st = state{}
		}
	}
	if st.LastSettledDay == nil {
		st.LastSettledDay = map[string]string{}
	}
	if st.SubsDay == nil {
		st.SubsDay = map[string]string{}
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

func trimmed(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Source) logger() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Source) client() *http.Client {
	if s.Client == nil {
		return &http.Client{Timeout: 60 * time.Second}
	}
	return s.Client
}

// bearer returns a valid access token for the configured application.
func (s *Source) bearer(ctx context.Context) (string, error) {
	return s.tokens.bearer(ctx, s.now(), s.loginURL(), s.client())
}

// loginURL is the Microsoft Entra host to mint tokens against; tests point it
// at an httptest server.
func (s *Source) loginURL() string {
	if s.LoginURL == "" {
		return DefaultLoginURL
	}
	return strings.TrimSuffix(s.LoginURL, "/")
}

func (s *Source) baseURL() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(s.BaseURL, "/")
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
