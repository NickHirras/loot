// Package googleanalytics polls the Google Analytics 4 (Firebase Analytics)
// Data API and emits daily active-user audience snapshots per app and country.
//
// Each completed day produces:
//   - one silent `audience` total row (Country == "") with the rolling 28-day
//     (or 7-day) distinct active users as of that day;
//   - one silent `audience` per-country row for each valid ISO country code.
//
// When a property is filtered to specific platforms (e.g. ["iOS"]), the event
// payload records `covers_android: false` so Google Play's Android active base
// and GA4's iOS active users add cleanly without overlap. When unfiltered (or
// including Android), `covers_android: true` tells the audience aggregate not
// to double-count Google Play on top of GA4.
package googleanalytics

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

// Name is the source identifier.
const Name = "googleanalytics"

const (
	defaultPollInterval = 6 * time.Hour
	defaultBackfillDays = 30
	defaultWindowDays   = 28
)

// Source implements core.Source and core.Checker.
type Source struct {
	Properties   []config.GoogleAnalyticsProperty
	WindowDays   int
	BackfillDays int
	PollEvery    time.Duration
	// Since, when set (from --since), overrides the first-run backfill floor.
	Since string

	TokenSource oauth2.TokenSource
	BaseURL     string
	Client      *http.Client
	Now         func() time.Time
	Log         *slog.Logger
}

// New builds a GA4 source authenticated with the service-account key at saPath.
func New(cfg config.GoogleAnalytics, saPath string, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.Properties) == 0 {
		return nil, fmt.Errorf("googleanalytics: no properties configured")
	}
	for _, p := range cfg.Properties {
		if normalizePropertyID(p.PropertyID) == "" {
			return nil, fmt.Errorf("googleanalytics: property entry has empty property_id")
		}
		if strings.TrimSpace(p.App) == "" {
			return nil, fmt.Errorf("googleanalytics: property %s has empty app name", p.PropertyID)
		}
	}
	if strings.TrimSpace(saPath) == "" {
		return nil, fmt.Errorf("googleanalytics: service_account_json_path is required (or set sources.googleplay.service_account_json_path)")
	}
	keyData, err := os.ReadFile(saPath)
	if err != nil {
		return nil, fmt.Errorf("googleanalytics: read service account key %s: %w", saPath, err)
	}
	ts, err := googleplay.TokenSourceForScopes(context.Background(), keyData, Scope)
	if err != nil {
		return nil, fmt.Errorf("googleanalytics: %w", err)
	}

	window := cfg.WindowDays
	if window != 7 && window != 28 {
		window = defaultWindowDays
	}
	backfill := cfg.BackfillDays
	if backfill <= 0 {
		backfill = defaultBackfillDays
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}

	return &Source{
		Properties:   cfg.Properties,
		WindowDays:   window,
		BackfillDays: backfill,
		PollEvery:    poll,
		TokenSource:  ts,
		BaseURL:      DefaultBaseURL,
		Client:       &http.Client{Timeout: 60 * time.Second},
		Now:          time.Now,
		Log:          log,
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration {
	if s.PollEvery > 0 {
		return s.PollEvery
	}
	return defaultPollInterval
}

func (s *Source) baseURL() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return DefaultBaseURL
}

func (s *Source) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) metricName() string {
	if s.WindowDays == 7 {
		return "active7DayUsers"
	}
	return "active28DayUsers"
}

func coversAndroid(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if strings.EqualFold(strings.TrimSpace(p), "android") {
			return true
		}
	}
	return false
}

// state is the persisted cursor blob: propertyID -> last completed day emitted.
type state struct {
	Cursor map[string]string `json:"cursor"`
}

func decodeState(raw []byte) state {
	st := state{Cursor: map[string]string{}}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	if st.Cursor == nil {
		st.Cursor = map[string]string{}
	}
	return st
}

// Check implements core.Checker by requesting yesterday's single-day active
// user count for every configured property.
func (s *Source) Check(ctx context.Context) error {
	yesterday := core.DayOf(s.now().UTC().AddDate(0, 0, -1))
	for _, prop := range s.Properties {
		_, err := s.runReport(ctx, prop.PropertyID, runReportRequest{
			DateRanges:      []dateRange{{StartDate: yesterday, EndDate: yesterday}},
			Metrics:         []metric{{Name: s.metricName()}},
			DimensionFilter: platformFilter(prop.Platforms),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Poll implements core.Source. Reads completed days up to yesterday UTC for
// every configured property and emits silent `audience` total and country rows.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now().UTC()
	endDay := core.DayOf(now.AddDate(0, 0, -1))

	backfill := s.BackfillDays
	if backfill <= 0 {
		backfill = defaultBackfillDays
	}
	defaultStart := core.DayOf(now.AddDate(0, 0, -backfill))
	if s.Since != "" {
		if _, err := time.Parse(core.DayLayout, s.Since); err == nil {
			defaultStart = s.Since
		}
	}

	var (
		events []core.Event
		errs   []string
	)
	for _, prop := range s.Properties {
		propID := normalizePropertyID(prop.PropertyID)
		app := strings.TrimSpace(prop.App)
		if propID == "" || app == "" {
			continue
		}

		startDay := defaultStart
		if prev := st.Cursor[propID]; prev != "" {
			if t, err := time.Parse(core.DayLayout, prev); err == nil {
				startDay = core.DayOf(t.AddDate(0, 0, 1))
			}
		}
		if startDay > endDay {
			continue
		}

		ev, lastDay, err := s.pollProperty(ctx, prop, propID, app, startDay, endDay, now)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		events = append(events, ev...)
		if lastDay > st.Cursor[propID] {
			st.Cursor[propID] = lastDay
		}
	}

	nextState, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("googleanalytics: encode state: %w", err)
	}
	if len(errs) > 0 && len(events) == 0 {
		return nil, nextState, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if len(errs) > 0 && s.Log != nil {
		s.Log.Warn("googleanalytics partial failure", "errors", strings.Join(errs, "; "))
	}
	return events, nextState, nil
}

func (s *Source) pollProperty(
	ctx context.Context,
	prop config.GoogleAnalyticsProperty,
	propID, app, startDay, endDay string,
	observed time.Time,
) ([]core.Event, string, error) {
	filter := platformFilter(prop.Platforms)
	metricNm := s.metricName()
	window := s.WindowDays
	if window != 7 && window != 28 {
		window = defaultWindowDays
	}

	// 1. Total rolling active users per day (authoritative deduplicated total).
	totResp, err := s.runReport(ctx, propID, runReportRequest{
		DateRanges:      []dateRange{{StartDate: startDay, EndDate: endDay}},
		Dimensions:      []dimension{{Name: "date"}},
		Metrics:         []metric{{Name: metricNm}},
		DimensionFilter: filter,
		Limit:           reportLimit,
	})
	if err != nil {
		return nil, "", err
	}

	// 2. Per-country rolling active users per day.
	ccResp, err := s.runReport(ctx, propID, runReportRequest{
		DateRanges:      []dateRange{{StartDate: startDay, EndDate: endDay}},
		Dimensions:      []dimension{{Name: "date"}, {Name: "countryId"}},
		Metrics:         []metric{{Name: metricNm}},
		DimensionFilter: filter,
		Limit:           reportLimit,
	})
	if err != nil {
		return nil, "", err
	}

	payload, _ := json.Marshal(map[string]any{
		"property_id":    propID,
		"metric":         metricNm,
		"window_days":    window,
		"platforms":      prop.Platforms,
		"covers_android": coversAndroid(prop.Platforms),
	})

	totals := map[string]int{}
	for _, r := range totResp.Rows {
		if len(r.DimensionValues) < 1 || len(r.MetricValues) < 1 {
			continue
		}
		day, ok := parseGADate(r.DimensionValues[0].Value)
		if !ok || day > endDay {
			continue
		}
		if n := parseCount(r.MetricValues[0].Value); n > 0 {
			totals[day] = n
		}
	}

	countries := map[string]map[string]int{}
	for _, r := range ccResp.Rows {
		if len(r.DimensionValues) < 2 || len(r.MetricValues) < 1 {
			continue
		}
		day, ok := parseGADate(r.DimensionValues[0].Value)
		if !ok || day > endDay {
			continue
		}
		cc := validCountry(r.DimensionValues[1].Value)
		if cc == "" {
			continue
		}
		n := parseCount(r.MetricValues[0].Value)
		if n <= 0 {
			continue
		}
		if countries[day] == nil {
			countries[day] = map[string]int{}
		}
		countries[day][cc] += n
	}

	daySet := map[string]bool{}
	for d := range totals {
		daySet[d] = true
	}
	for d := range countries {
		daySet[d] = true
	}
	days := make([]string, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Strings(days)

	var (
		events  []core.Event
		lastDay string
	)
	for _, day := range days {
		occurred, err := time.ParseInLocation(core.DayLayout, day, time.UTC)
		if err != nil {
			continue
		}
		if tot := totals[day]; tot > 0 {
			events = append(events, core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       core.KindAudience,
				App:        app,
				OccurredAt: occurred,
				ObservedAt: observed,
				Day:        day,
				Quantity:   tot,
				DedupeKey:  fmt.Sprintf("ga4:audience:%s:%s:", propID, day),
				Silent:     true,
				Payload:    payload,
			})
		}
		if byCC := countries[day]; len(byCC) > 0 {
			ccs := make([]string, 0, len(byCC))
			for cc := range byCC {
				ccs = append(ccs, cc)
			}
			sort.Strings(ccs)
			for _, cc := range ccs {
				events = append(events, core.Event{
					ID:         core.NewIDAt(occurred),
					Source:     Name,
					Kind:       core.KindAudience,
					App:        app,
					Country:    cc,
					OccurredAt: occurred,
					ObservedAt: observed,
					Day:        day,
					Quantity:   byCC[cc],
					DedupeKey:  fmt.Sprintf("ga4:audience:%s:%s:%s", propID, day, cc),
					Silent:     true,
					Payload:    payload,
				})
			}
		}
		if day > lastDay {
			lastDay = day
		}
	}
	return events, lastDay, nil
}
