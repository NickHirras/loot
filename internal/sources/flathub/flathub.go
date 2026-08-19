// Package flathub polls Flathub's public stats API and emits one event per
// (app, completed day) of installs.
//
// The API (verified against https://flathub.org/api/v2/stats/{app_id}) returns:
//
//	{
//	  "installs_total": 684456,
//	  "installs_per_day": {"2026-08-17": 470, ...},   // ~180 days
//	  "installs_per_country": {"US": 12345, ...},     // cumulative, undated
//	  "installs_last_month": 12648,
//	  "installs_last_7_days": 2830,
//	  "id": "org.gnome.Calculator"
//	}
//
// Only installs_per_day is used for events: it is the one field with a date to
// dedupe on. installs_per_country carries no timestamps, so it cannot be turned
// into events without double counting.
package flathub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier.
const Name = "flathub"

// DefaultBaseURL is the public Flathub API root.
const DefaultBaseURL = "https://flathub.org"

const dateLayout = "2006-01-02"

// Stats is the shape of GET /api/v2/stats/{app_id}.
type Stats struct {
	ID                 string         `json:"id"`
	InstallsTotal      int            `json:"installs_total"`
	InstallsPerDay     map[string]int `json:"installs_per_day"`
	InstallsPerCountry map[string]int `json:"installs_per_country"`
	InstallsLastMonth  int            `json:"installs_last_month"`
	InstallsLast7Days  int            `json:"installs_last_7_days"`
}

// state is the persisted cursor: the last emitted day per app.
type state struct {
	// LastDate maps app id -> last emitted date (YYYY-MM-DD).
	LastDate map[string]string `json:"last_date"`
	// Seeded marks apps whose first run has completed, so the backfill window
	// is applied exactly once per app.
	Seeded map[string]bool `json:"seeded"`
}

// Source implements core.Source as an hourly poller.
type Source struct {
	Apps         []string
	BackfillDays int
	// Since optionally pins the first-run floor to a fixed date (YYYY-MM-DD),
	// overriding BackfillDays. Set by `loot serve --since`.
	Since   string
	BaseURL string
	Client  *http.Client
	Log     *slog.Logger
	// Now is swappable for tests.
	Now func() time.Time
}

// New returns a Flathub source watching the given app ids.
func New(apps []string, backfillDays int, since string, log *slog.Logger) *Source {
	if log == nil {
		log = slog.Default()
	}
	return &Source{
		Apps:         apps,
		BackfillDays: backfillDays,
		Since:        since,
		BaseURL:      DefaultBaseURL,
		Client:       &http.Client{Timeout: 30 * time.Second},
		Log:          log,
		Now:          time.Now,
	}
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source: Flathub aggregates daily, so hourly is
// plenty and stays polite to a free public API.
func (s *Source) PollInterval() time.Duration { return time.Hour }

// Check implements core.Checker: it fetches one app's stats so a
// misconfigured app id or a blocked network shows up in `loot check` rather
// than in an hour's time.
func (s *Source) Check(ctx context.Context) error {
	if len(s.Apps) == 0 {
		return fmt.Errorf("flathub: no apps configured")
	}
	app := s.Apps[0]
	stats, err := s.fetch(ctx, app)
	if err != nil {
		return err
	}
	if len(stats.InstallsPerDay) == 0 {
		return fmt.Errorf("flathub: %s returned no daily installs (is the app id right?)", app)
	}
	return nil
}

// Poll fetches stats for every configured app and emits install events for
// completed days newer than the stored cursor.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now().UTC()
	// Today is still accumulating; only emit days that are finished.
	today := now.Format(dateLayout)

	var (
		events   []core.Event
		firstErr error
	)

	for _, app := range s.Apps {
		stats, err := s.fetch(ctx, app)
		if err != nil {
			s.Log.Warn("flathub poll failed", "app", app, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		seeded := st.Seeded[app]
		floor := st.LastDate[app]
		if !seeded {
			floor = s.firstRunFloor(now)
		}

		events = append(events, EventsFromStats(app, stats, floor, now)...)

		// The cursor advances to the newest completed day the API reported,
		// even for days the floor suppressed, so a backfill window is not
		// re-evaluated on the next poll.
		newest := st.LastDate[app]
		for day := range stats.InstallsPerDay {
			if day < today && day > newest {
				newest = day
			}
		}

		if newest != "" {
			st.LastDate[app] = newest
		}
		st.Seeded[app] = true
	}

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("flathub: encode state: %w", err)
	}
	return events, out, firstErr
}

// firstRunFloor returns the exclusive lower bound for the very first poll of an
// app: days at or before this date are skipped. With BackfillDays == 0 nothing
// historical is emitted and the cursor simply seeds at yesterday.
func (s *Source) firstRunFloor(now time.Time) string {
	if s.Since != "" {
		if t, err := time.Parse(dateLayout, s.Since); err == nil {
			return t.AddDate(0, 0, -1).Format(dateLayout)
		}
		s.Log.Warn("flathub: ignoring unparsable --since", "since", s.Since)
	}
	days := s.BackfillDays
	if days < 0 {
		days = 0
	}
	return now.AddDate(0, 0, -(days + 1)).Format(dateLayout)
}

func buildEvent(app, day string, count int, observed time.Time) (core.Event, error) {
	d, err := time.ParseInLocation(dateLayout, day, time.UTC)
	if err != nil {
		return core.Event{}, err
	}
	payload, _ := json.Marshal(map[string]any{"app": app, "date": day, "installs": count})
	return core.Event{
		ID:         core.NewIDAt(d),
		Source:     Name,
		Kind:       "install",
		App:        app,
		OccurredAt: d,
		ObservedAt: observed,
		Country:    "",
		Amount:     0,
		Currency:   "",
		Quantity:   count,
		DedupeKey:  fmt.Sprintf("flathub:%s:%s", app, day),
		IsLedger:   false,
		Payload:    payload,
	}, nil
}

func (s *Source) fetch(ctx context.Context, app string) (Stats, error) {
	var stats Stats

	url := fmt.Sprintf("%s/api/v2/stats/%s", s.baseURL(), app)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stats, fmt.Errorf("flathub: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		return stats, fmt.Errorf("flathub: get %s: %w", app, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return stats, fmt.Errorf("flathub: %s returned %s: %s", app, resp.Status, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return stats, fmt.Errorf("flathub: read %s: %w", app, err)
	}
	if err := ParseStats(body, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// ParseStats decodes a stats response body. Exported for tests and fixtures.
func ParseStats(body []byte, out *Stats) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("flathub: decode stats: %w", err)
	}
	return nil
}

// EventsFromStats converts a stats payload into events for every completed day
// strictly after floor. Exported so the parsing path is testable without HTTP.
func EventsFromStats(app string, stats Stats, floor string, now time.Time) []core.Event {
	today := now.UTC().Format(dateLayout)
	days := make([]string, 0, len(stats.InstallsPerDay))
	for d := range stats.InstallsPerDay {
		days = append(days, d)
	}
	sort.Strings(days)

	var events []core.Event
	for _, day := range days {
		if day >= today || (floor != "" && day <= floor) {
			continue
		}
		count := stats.InstallsPerDay[day]
		if count <= 0 {
			continue
		}
		if ev, err := buildEvent(app, day, count, now); err == nil {
			events = append(events, ev)
		}
	}
	return events
}

func decodeState(raw []byte) state {
	st := state{LastDate: map[string]string{}, Seeded: map[string]bool{}}
	if len(raw) == 0 {
		return st
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return state{LastDate: map[string]string{}, Seeded: map[string]bool{}}
	}
	if st.LastDate == nil {
		st.LastDate = map[string]string{}
	}
	if st.Seeded == nil {
		st.Seeded = map[string]bool{}
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

var _ core.Source = (*Source)(nil)
