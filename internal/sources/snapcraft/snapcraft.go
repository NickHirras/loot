// Package snapcraft polls the Snap Store's dashboard metrics API and emits one
// set of events per (snap, completed day).
//
// # The API
//
// Everything comes from one call, verified against
// https://dashboard.snapcraft.io/docs/reference/v1/snap.html:
//
//	POST https://dashboard.snapcraft.io/dev/api/snaps/metrics
//	{"filters":[{"snap_id":"…","metric_name":"daily_device_change",
//	             "start":"2026-08-01","end":"2026-08-17"}, …]}
//
//	{"metrics":[{"status":"OK","snap_id":"…","metric_name":"daily_device_change",
//	             "buckets":["2026-08-01",…],
//	             "series":[{"name":"new","values":[12,…]},
//	                       {"name":"continued","values":[…]},
//	                       {"name":"lost","values":[…]}]}]}
//
// Loot asks for two metrics: `weekly_installed_base_by_country`, whose series
// are named by ISO 3166-1 alpha-2 code and say how many distinct devices were
// seen in each country in the trailing week, and `daily_device_change`, whose
// new/continued/lost series compare each day with the one before it.
//
// # What is emitted per (snap, day)
//
//	audience        silent   Quantity = weekly devices in one country, one per country
//	audience        silent   Quantity = weekly devices in total (no country)
//	device_new      silent   Quantity = devices seen today and not yesterday
//	active_devices  silent   Quantity = new + continued (the day's installed base)
//	lost            silent   Quantity = devices seen yesterday and not today
//
// `audience` is the number that means something: it is the store's own answer
// to "how many devices have this snap". Nothing here is an install and nothing
// here produces a drop by itself — the pipeline mints a settlement the first
// time a country appears and a milestone when the total reaches one.
//
// # Caveats
//
//   - **Metrics lag.** A day's figures appear a day or two late, and the store
//     returns nulls in the meantime. A day that is all null is skipped without
//     moving its cursor, so it is picked up later.
//   - **`new` is not installs.** It is churn between two consecutive days; see
//     EventsFromMetrics in metrics.go for why it is no longer counted as one.
//   - **Devices, not people.** Every figure counts machines.
//
// # Credentials
//
// Authentication is an exported login from `snapcraft export-login`; see
// login.go for the formats understood and docs/sources/snapcraft.md for how to
// make one.
package snapcraft

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier.
const Name = "snapcraft"

// DefaultBaseURL is the Snap Store developer dashboard API root.
const DefaultBaseURL = "https://dashboard.snapcraft.io"

const userAgent = "loot/0.1 (+https://github.com/nickhirras/loot)"

// Source implements core.Source as a six-hourly poller. Metrics are generated
// once a day, so anything more frequent only spends the store's CPU.
type Source struct {
	cfg config.Snapcraft
	log *slog.Logger
	// Since, when set (from --since), overrides the backfill start.
	Since string
	// BaseURL, AuthURL and Client are swappable for tests. AuthURL is Ubuntu
	// One SSO, which is a different host from the store and is used for
	// exactly one thing: refreshing the discharge macaroon (refresh.go).
	BaseURL string
	AuthURL string
	Client  *http.Client
	// Now is swappable for tests.
	Now func() time.Time

	// mu guards the refreshed discharge. Poll is driven one cycle at a time by
	// the scheduler, but `loot serve` can force a poll while one is running and
	// Check shares the Source, so the refresh is single-flighted rather than
	// assumed to be alone.
	mu sync.Mutex
	// discharge is the unbound discharge Ubuntu One last handed back, replacing
	// the login file's own. Empty until the first refresh.
	discharge string
	// dischargeFor is the Origin fingerprint of the login file discharge was
	// refreshed for; a mismatch means the user re-exported and the cached
	// discharge belongs to a login that is no longer on disk.
	dischargeFor string
}

// state is the persisted cursor.
type state struct {
	// SnapIDs caches name -> snap_id, so a poll is one request per snap
	// instead of two. Ids never change.
	SnapIDs map[string]string `json:"snap_ids"`
	// Cursor maps snap name -> last day emitted (YYYY-MM-DD). A snap with no
	// entry has never been polled and gets the backfill window.
	Cursor map[string]string `json:"cursor"`
	// AudienceCursor maps snap name -> last day an audience snapshot was
	// emitted for. It is separate from Cursor because the two metrics are
	// published separately and were adopted at different times.
	AudienceCursor map[string]string `json:"audience_cursor,omitempty"`
	// RefreshedDischarge is the unbound discharge macaroon Loot last obtained
	// from Ubuntu One SSO, and RefreshedFor fingerprints the login file it was
	// obtained for. They live here rather than in the login file because that
	// file is the user's export and Loot does not rewrite it; keeping them
	// means a restart does not start from a discharge that is already stale.
	RefreshedDischarge string `json:"refreshed_discharge,omitempty"`
	RefreshedFor       string `json:"refreshed_for,omitempty"`
}

// New builds the source from its config. The login file is checked for
// existence here rather than at the first poll, so a typo in login_path shows
// up at startup and in `loot check`.
func New(cfg config.Snapcraft, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.Snaps) == 0 {
		return nil, fmt.Errorf("snapcraft: no snaps configured")
	}
	if cfg.LoginPath == "" {
		return nil, fmt.Errorf("snapcraft: login_path is not set; %s", exportHint)
	}
	if _, err := os.Stat(cfg.LoginPath); err != nil {
		return nil, fmt.Errorf("snapcraft: login file %s: %w — %s", cfg.LoginPath, err, exportHint)
	}
	return &Source{
		cfg:     cfg,
		log:     log,
		BaseURL: DefaultBaseURL,
		AuthURL: DefaultAuthURL,
		Client:  &http.Client{Timeout: 60 * time.Second},
		Now:     time.Now,
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return 6 * time.Hour }

// Check implements core.Checker: it reads the login file, resolves one snap's
// id and asks for a single day of metrics, so a stale macaroon or a missing
// ACL is reported now rather than in six hours.
//
// It goes through the same do() as a poll, so a discharge that went stale since
// the export is refreshed here too and `loot check` reports the tick it would
// have reported the day it was set up. Check has no state blob to write the
// refreshed discharge back to; it is kept in memory for this process, and a
// serving Loot persists its own.
func (s *Source) Check(ctx context.Context) error {
	auth, err := s.auth()
	if err != nil {
		return err
	}
	if len(s.cfg.Snaps) == 0 {
		return fmt.Errorf("snapcraft: no snaps configured")
	}
	snap := s.cfg.Snaps[0]

	id, err := s.resolveSnapID(ctx, &auth, snap)
	if err != nil {
		return err
	}

	now := s.now().UTC()
	day := now.AddDate(0, 0, -2).Format(core.DayLayout)
	end := now.AddDate(0, 0, -1).Format(core.DayLayout)
	if _, err := s.fetchMetrics(ctx, &auth, id, day, day, end); err != nil {
		return err
	}
	// An empty or all-null answer is a pass: it proves the credentials were
	// accepted, and a snap published yesterday has no metrics yet.
	s.log.Debug("snapcraft check ok", "snap", snap, "snap_id", id, "credentials", auth.Format)
	return nil
}

// Poll fetches metrics for every configured snap and emits events for
// completed days newer than the stored cursor.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	// A discharge refreshed by an earlier run is newer than the login file's,
	// and using it saves the first request of every restart being a 401.
	s.seedDischarge(st.RefreshedDischarge, st.RefreshedFor)

	auth, err := s.auth()
	if err != nil {
		return nil, raw, err
	}

	now := s.now().UTC()
	today := now.Format(core.DayLayout)
	// Today is still accumulating and the store has not published it, so
	// yesterday is the newest day worth asking for.
	yesterday := addDays(today, -1)

	var (
		events   []core.Event
		firstErr error
	)

	for _, snap := range s.cfg.Snaps {
		id := st.SnapIDs[snap]
		if id == "" {
			id, err = s.resolveSnapID(ctx, &auth, snap)
			if err != nil {
				s.log.Warn("snapcraft: could not resolve snap id", "snap", snap, "error", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			st.SnapIDs[snap] = id
		}

		floor, seeded := st.Cursor[snap]
		if !seeded || floor == "" {
			floor = s.firstRunFloor(now)
			// Persist the floor immediately: with backfill_days: 0 there is
			// nothing to fetch today, and without this the window would be
			// recomputed from "now" on every future poll and never advance.
			st.Cursor[snap] = floor
		}
		// The audience cursor is its own: a snap Loot has polled for months has
		// no entry here the first time it runs a version that reads audience
		// snapshots, and gets the backfill window for them alone.
		audFloor, audSeeded := st.AudienceCursor[snap]
		if !audSeeded || audFloor == "" {
			audFloor = s.firstRunFloor(now)
			st.AudienceCursor[snap] = audFloor
		}
		if floor >= yesterday && audFloor >= yesterday {
			continue
		}

		// A range that is already up to date is still asked for, as its last
		// day: one request shape is simpler than three, and the floor filters
		// out whatever comes back.
		resp, err := s.fetchMetrics(ctx, &auth, id,
			minDay(addDays(floor, 1), yesterday), minDay(addDays(audFloor, 1), yesterday), yesterday)
		if err != nil {
			s.log.Warn("snapcraft poll failed", "snap", snap, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		evs, newest := EventsFromMetrics(snap, id, resp, floor, today, now)
		events = append(events, evs...)
		if newest > st.Cursor[snap] {
			st.Cursor[snap] = newest
		}
		aud, audNewest := AudienceEvents(snap, id, resp, audFloor, today, now)
		events = append(events, aud...)
		if audNewest > st.AudienceCursor[snap] {
			st.AudienceCursor[snap] = audNewest
		}
		s.log.Debug("snapcraft polled", "snap", snap, "events", len(evs)+len(aud),
			"cursor", st.Cursor[snap], "audience_cursor", st.AudienceCursor[snap])
	}

	// Persist whatever discharge is current, so the next process starts with
	// it. The login file itself is left alone: it is the user's export.
	st.RefreshedDischarge, st.RefreshedFor = s.dischargeSnapshot()

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("snapcraft: encode state: %w", err)
	}
	return events, out, firstErr
}

// auth returns the credentials to send: the login file's, with the most
// recently refreshed discharge swapped in when one applies.
//
// The file is re-read every time rather than cached, so replacing it takes
// effect at the next poll without a restart — and when it is replaced, the
// fingerprint mismatch drops the refreshed discharge that belonged to the old
// export.
func (s *Source) auth() (credentials, error) {
	file, err := loadLogin(s.cfg.LoginPath)
	if err != nil {
		return credentials{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discharge != "" && s.dischargeFor != file.Origin {
		s.log.Info("snapcraft: login file changed; discarding the refreshed discharge")
		s.discharge, s.dischargeFor = "", ""
	}
	if s.discharge == "" || !file.refreshable() {
		return file, nil
	}
	refreshed, err := file.withDischarge(s.discharge)
	if err != nil {
		// A stored discharge that no longer binds is not worth failing over:
		// drop it and let the file's own credential try, refreshing if the
		// store says it is stale.
		s.log.Warn("snapcraft: stored discharge is unusable; falling back to the login file", "error", err)
		s.discharge, s.dischargeFor = "", ""
		return file, nil
	}
	return refreshed, nil
}

// seedDischarge adopts a discharge persisted by an earlier run. An in-memory
// refresh always wins: it is at least as new as anything on disk.
func (s *Source) seedDischarge(discharge, origin string) {
	if discharge == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discharge == "" {
		s.discharge, s.dischargeFor = discharge, origin
	}
}

// dischargeSnapshot reports the refreshed discharge to persist.
func (s *Source) dischargeSnapshot() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discharge, s.dischargeFor
}

func (s *Source) authURL() string {
	if s.AuthURL == "" {
		return DefaultAuthURL
	}
	return s.AuthURL
}

// firstRunFloor returns the exclusive lower bound for a snap's very first poll:
// days at or before it are skipped. With BackfillDays == 0 nothing historical
// is emitted and the cursor simply seeds at yesterday.
func (s *Source) firstRunFloor(now time.Time) string {
	if s.Since != "" {
		if t, err := time.Parse(core.DayLayout, s.Since); err == nil {
			return t.AddDate(0, 0, -1).Format(core.DayLayout)
		}
		s.log.Warn("snapcraft: ignoring unparsable --since", "since", s.Since)
	}
	days := s.cfg.BackfillDays
	if days < 0 {
		days = 0
	}
	return now.AddDate(0, 0, -(days + 1)).Format(core.DayLayout)
}

func newState() *state {
	return &state{
		SnapIDs:        map[string]string{},
		Cursor:         map[string]string{},
		AudienceCursor: map[string]string{},
	}
}

func decodeState(raw []byte) *state {
	st := newState()
	if len(raw) == 0 {
		return st
	}
	if err := json.Unmarshal(raw, st); err != nil {
		return newState()
	}
	if st.SnapIDs == nil {
		st.SnapIDs = map[string]string{}
	}
	if st.Cursor == nil {
		st.Cursor = map[string]string{}
	}
	// Absent on state written before audience snapshots existed, which is
	// exactly the case that wants a backfill: see Poll.
	if st.AudienceCursor == nil {
		st.AudienceCursor = map[string]string{}
	}
	return st
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

// parseDay reads a YYYY-MM-DD bucket as midnight UTC.
func parseDay(day string) (time.Time, error) {
	return time.ParseInLocation(core.DayLayout, day, time.UTC)
}

// minDay returns the earlier of two YYYY-MM-DD day strings.
func minDay(a, b string) string {
	if a < b {
		return a
	}
	return b
}

// addDays shifts a YYYY-MM-DD day string. An unparseable day is returned
// unchanged, which keeps the comparisons that use it conservative.
func addDays(day string, n int) string {
	t, err := parseDay(day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format(core.DayLayout)
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
