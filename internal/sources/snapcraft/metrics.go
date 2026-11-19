package snapcraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Metric names used by Loot. The store also publishes the installed base by
// channel, operating system, version and architecture, daily and weekly; none
// of them add a fact Loot shows, and every extra filter is another series to
// download every six hours.
const (
	metricDailyDeviceChange   = "daily_device_change"
	metricWeeklyBaseByCountry = "weekly_installed_base_by_country"
)

// kindDeviceNew is the kind of the daily "new" series: devices seen today that
// were not seen yesterday. It is deliberately not "install" — see
// EventsFromMetrics.
const kindDeviceNew = "device_new"

// Series names of daily_device_change.
const (
	seriesNew       = "new"
	seriesContinued = "continued"
	seriesLost      = "lost"
)

// Filter is one entry of the metrics request.
type Filter struct {
	SnapID     string `json:"snap_id"`
	MetricName string `json:"metric_name"`
	Start      string `json:"start"`
	End        string `json:"end"`
}

// Series is one timeseries of a metric. Values line up with Metric.Buckets
// index for index and are null for a bucket the store has no figure for, so
// they are pointers rather than plain numbers: a missing day and a genuine zero
// mean very different things here.
type Series struct {
	Name              string     `json:"name"`
	Values            []*float64 `json:"values"`
	CurrentlyReleased *bool      `json:"currently_released,omitempty"`
}

// Metric is one element of the metrics response.
type Metric struct {
	Status     string   `json:"status"`
	SnapID     string   `json:"snap_id"`
	MetricName string   `json:"metric_name"`
	Buckets    []string `json:"buckets"`
	Series     []Series `json:"series"`
}

// MetricsResponse is the body of POST /dev/api/snaps/metrics.
type MetricsResponse struct {
	Metrics   []Metric   `json:"metrics"`
	ErrorList []APIError `json:"error_list"`
}

// APIError is one entry of the store's error_list.
type APIError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Metric returns the metric with the given name, if the response carried one
// with usable data.
func (r MetricsResponse) Metric(name string) (Metric, bool) {
	for _, m := range r.Metrics {
		if m.MetricName != name {
			continue
		}
		// Status is "OK", "FAIL" or "NO_DATA"; only the first has buckets
		// worth reading, and a brand new snap legitimately answers NO_DATA.
		if m.Status != "" && !strings.EqualFold(m.Status, "OK") {
			return Metric{}, false
		}
		return m, true
	}
	return Metric{}, false
}

// at returns the value of the named series at bucket position i. ok is false
// when the series is absent or the sample is null.
func (m Metric) at(series string, i int) (float64, bool) {
	for _, s := range m.Series {
		if s.Name != series {
			continue
		}
		if i < 0 || i >= len(s.Values) || s.Values[i] == nil {
			return 0, false
		}
		return *s.Values[i], true
	}
	return 0, false
}

// ParseMetrics decodes a metrics response body. Exported so fixtures can be
// exercised without HTTP.
func ParseMetrics(body []byte) (MetricsResponse, error) {
	var out MetricsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("snapcraft: decode metrics: %w", err)
	}
	if len(out.Metrics) == 0 && len(out.ErrorList) > 0 {
		return out, fmt.Errorf("snapcraft: metrics: %s", joinErrors(out.ErrorList))
	}
	return out, nil
}

// fetchMetrics asks for both metrics in one request. The two ranges are
// separate because the two cursors are: a Loot that has been reading device
// changes for months still has every audience snapshot to backfill the first
// time it runs a version that knows about them.
func (s *Source) fetchMetrics(ctx context.Context, auth *credentials, snapID, start, audienceStart, end string) (MetricsResponse, error) {
	req := struct {
		Filters []Filter `json:"filters"`
	}{Filters: []Filter{
		{SnapID: snapID, MetricName: metricDailyDeviceChange, Start: start, End: end},
		{SnapID: snapID, MetricName: metricWeeklyBaseByCountry, Start: audienceStart, End: end},
	}}
	body, err := json.Marshal(req)
	if err != nil {
		return MetricsResponse{}, fmt.Errorf("snapcraft: encode metrics request: %w", err)
	}
	raw, err := s.do(ctx, http.MethodPost, "/dev/api/snaps/metrics", body, auth)
	if err != nil {
		return MetricsResponse{}, err
	}
	return ParseMetrics(raw)
}

// resolveSnapID turns a snap name into the opaque snap_id the metrics endpoint
// takes. The direct lookup needs the `package_access` ACL; a login exported
// with `--acls=package_metrics` alone gets a 403 there, so the account listing
// is tried as a fallback before giving up.
func (s *Source) resolveSnapID(ctx context.Context, auth *credentials, snap string) (string, error) {
	raw, err := s.do(ctx, http.MethodGet, "/dev/api/snaps/info/"+snap, nil, auth)
	if err == nil {
		var info struct {
			SnapID string `json:"snap_id"`
		}
		if jsonErr := json.Unmarshal(raw, &info); jsonErr == nil && info.SnapID != "" {
			return info.SnapID, nil
		}
		return "", fmt.Errorf("snapcraft: %s: info response carried no snap_id", snap)
	}

	var se *statusError
	if !errors.As(err, &se) || (se.Status != http.StatusForbidden && se.Status != http.StatusNotFound) {
		return "", err
	}
	id, listErr := s.snapIDFromAccount(ctx, auth, snap)
	if listErr == nil {
		return id, nil
	}
	// Report the first failure: it is the one that explains the credential.
	if se.Status == http.StatusNotFound {
		return "", fmt.Errorf("snapcraft: unknown snap %q — check the name in `snapcraft list` "+
			"and that the exported login covers it", snap)
	}
	return "", err
}

// snapIDFromAccount reads GET /dev/api/account, whose `snaps` object is keyed
// by series ("16") and then by snap name, with the id under `snap-id`.
func (s *Source) snapIDFromAccount(ctx context.Context, auth *credentials, snap string) (string, error) {
	raw, err := s.do(ctx, http.MethodGet, "/dev/api/account", nil, auth)
	if err != nil {
		return "", err
	}
	var acct struct {
		Snaps map[string]map[string]struct {
			SnapID string `json:"snap-id"`
		} `json:"snaps"`
	}
	if err := json.Unmarshal(raw, &acct); err != nil {
		return "", fmt.Errorf("snapcraft: decode account: %w", err)
	}
	// Series keys are sorted so the answer does not depend on map order when
	// an account has a snap in more than one series.
	series := make([]string, 0, len(acct.Snaps))
	for k := range acct.Snaps {
		series = append(series, k)
	}
	sort.Strings(series)
	for _, k := range series {
		if entry, ok := acct.Snaps[k][snap]; ok && entry.SnapID != "" {
			return entry.SnapID, nil
		}
	}
	return "", fmt.Errorf("snapcraft: %q is not among this account's snaps", snap)
}

// do performs one authenticated request, refreshing the Ubuntu One discharge
// macaroon and retrying once if the store says the discharge has gone stale.
//
// The refresh is the whole reason auth is a pointer: a poll makes several
// requests, and the caller's credentials are updated in place so the rest of
// them go out with the new discharge instead of each earning its own 401.
//
// A refresh that fails is not reported: the 401 the store already returned
// carries the right advice for the case a refresh cannot fix (the root macaroon
// really has expired or been revoked), and burying it under an SSO error would
// only make that harder to read.
func (s *Source) do(ctx context.Context, method, path string, body []byte, auth *credentials) ([]byte, error) {
	raw, err := s.doOnce(ctx, method, path, body, *auth)
	if err == nil || !needsDischargeRefresh(err) || !auth.refreshable() {
		return raw, err
	}
	fresh, refreshErr := s.refreshCredentials(ctx, *auth)
	if refreshErr != nil {
		s.log.Warn("snapcraft: could not refresh the ubuntu one discharge macaroon",
			"error", refreshErr, "store_error", err)
		return raw, err
	}
	*auth = fresh
	s.log.Info("snapcraft: refreshed the ubuntu one discharge macaroon", "retrying", method+" "+path)
	return s.doOnce(ctx, method, path, body, fresh)
}

// doOnce performs one authenticated request and maps the store's error bodies
// onto messages that say what to actually do about them.
func (s *Source) doOnce(ctx context.Context, method, path string, body []byte, auth credentials) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL()+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("snapcraft: build request: %w", err)
	}
	req.Header.Set("Authorization", auth.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("snapcraft: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("snapcraft: read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return raw, newStatusError(resp.StatusCode, raw)
	}
	return raw, nil
}

// statusError carries the store's HTTP status so callers can branch (the
// snap_id fallback needs to tell 403 from a network failure) while still
// printing a human answer.
type statusError struct {
	Status  int
	Code    string
	Message string
	hint    string
}

func (e *statusError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.hint != "" {
		return fmt.Sprintf("snapcraft: %d %s — %s", e.Status, msg, e.hint)
	}
	return fmt.Sprintf("snapcraft: %d %s", e.Status, msg)
}

func newStatusError(status int, body []byte) *statusError {
	e := &statusError{Status: status}

	var payload struct {
		ErrorList []APIError `json:"error_list"`
		// Very old responses used these two instead of error_list.
		ErrorMessage string `json:"error_message"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		switch {
		case len(payload.ErrorList) > 0:
			e.Message = joinErrors(payload.ErrorList)
			e.Code = payload.ErrorList[0].Code
		case payload.ErrorMessage != "":
			e.Message = payload.ErrorMessage
		case payload.Error != "":
			e.Message = payload.Error
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300] + "…"
		}
	}

	lower := strings.ToLower(e.Message + " " + e.Code)
	switch {
	case status == http.StatusUnauthorized,
		strings.Contains(lower, "needs-refresh"), strings.Contains(lower, "expired"):
		e.hint = "the exported login has expired or been revoked; " + exportHint
	case status == http.StatusForbidden && strings.Contains(lower, "package_metrics"):
		e.hint = "the exported login is missing the package_metrics ACL; " + exportHint
	case status == http.StatusForbidden && strings.Contains(lower, "not authorized to access"):
		e.hint = "the exported login was restricted to other snaps; " + exportHint +
			" with the right --snaps list"
	case status == http.StatusForbidden:
		e.hint = "the exported login does not carry the permission this call needs; " + exportHint
	case status == http.StatusNotFound:
		e.hint = "check the snap name in `snapcraft list`"
	case status >= 500:
		e.hint = "the Snap Store is having trouble; Loot will retry on the next poll"
	}
	return e
}

func joinErrors(list []APIError) string {
	parts := make([]string, 0, len(list))
	for _, e := range list {
		if e.Code != "" && e.Message != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", e.Message, e.Code))
		} else if e.Message != "" {
			parts = append(parts, e.Message)
		} else if e.Code != "" {
			parts = append(parts, e.Code)
		}
	}
	return strings.Join(parts, "; ")
}

// dayPayload is what every per-day device event carries in Event.Payload.
type dayPayload struct {
	Snap   string `json:"snap"`
	SnapID string `json:"snap_id,omitempty"`
	Date   string `json:"date"`
	// New, Continued and Lost are the daily_device_change series for the day.
	New       int `json:"new"`
	Continued int `json:"continued"`
	Lost      int `json:"lost"`
	// InstalledBase is new + continued: the devices running the snap that day.
	InstalledBase int `json:"installed_base"`
}

func (p dayPayload) raw() json.RawMessage {
	b, _ := json.Marshal(p)
	return b
}

// audiencePayload is what an `audience` event carries. Placed and Unplaced are
// set on the total row only: how much of the weekly base the store put in a
// country, and how much it could not.
type audiencePayload struct {
	Snap     string `json:"snap"`
	SnapID   string `json:"snap_id,omitempty"`
	Date     string `json:"date"`
	Basis    string `json:"basis"`
	Country  string `json:"country,omitempty"`
	Placed   int    `json:"placed,omitempty"`
	Unplaced int    `json:"unplaced,omitempty"`
}

func (p audiencePayload) raw() json.RawMessage {
	b, _ := json.Marshal(p)
	return b
}

// EventsFromMetrics turns one snap's daily_device_change into the silent
// per-day counters, for every completed day strictly after floor and strictly
// before today, and reports how far the caller may advance its cursor.
//
// # None of these are installs
//
// The three series compare one day with the day before it and nothing else:
// `new` is "seen today, not yesterday", `lost` is "seen yesterday, not today".
// A laptop that stays shut over the weekend is lost on Saturday and new again
// on Monday. Loot used to record `new` as an `install` and add it up, which
// turned a steady base of a couple of hundred devices into thousands of
// "arrivals" — so they are stored under their own kinds, which nothing sums
// into a population, a quest or a record, and they produce no drop. What an
// app's audience actually is comes from AudienceEvents.
//
// Metric availability lags the calendar by a day or two and the store fills the
// gap with nulls; a day whose daily_device_change is entirely null is left
// alone (no events) so the next poll picks it up once the figures land.
//
// The cursor therefore advances only to the end of the *contiguous* run of
// published days — not to the newest day with data anywhere in the window.
// Given an unpublished Tuesday between a published Monday and a published
// Wednesday, the old rule moved the cursor to Wednesday and Tuesday was never
// read again once its figures landed. Events for the days beyond the gap are
// still emitted (their dedupe keys make the re-read free); it is only the
// cursor that waits.
func EventsFromMetrics(snap, snapID string, resp MetricsResponse, floor, today string, observed time.Time) ([]core.Event, string) {
	change, ok := resp.Metric(metricDailyDeviceChange)
	if !ok {
		return nil, ""
	}

	var (
		events []core.Event
		newest string
	)

	days := append([]string(nil), change.Buckets...)
	sort.Strings(days)

	// broken records that an unpublished day has been passed, which is where
	// the contiguous run — and therefore the cursor — stops. Days the store
	// simply does not carry a bucket for are not holes: it truncates the range
	// to what it has rather than padding it, so treating a missing bucket as a
	// hole would stall the cursor forever on a snap younger than the backfill
	// window.
	var broken bool
	for _, day := range days {
		if day >= today || (floor != "" && day <= floor) {
			continue
		}
		i := indexOf(change.Buckets, day)
		newDev, hasNew := change.at(seriesNew, i)
		continued, hasContinued := change.at(seriesContinued, i)
		lost, hasLost := change.at(seriesLost, i)
		if !hasNew && !hasContinued && !hasLost {
			// Not published yet. Leave the cursor behind it, and behind
			// everything after it too.
			broken = true
			continue
		}
		// Past a hole the day is still emitted, but the cursor stays behind it.
		if !broken && day > newest {
			newest = day
		}

		occurred, err := parseDay(day)
		if err != nil {
			continue
		}
		base := dayPayload{
			Snap:          snap,
			SnapID:        snapID,
			Date:          day,
			New:           int(newDev),
			Continued:     int(continued),
			Lost:          int(lost),
			InstalledBase: int(newDev) + int(continued),
		}

		mk := func(kind string, qty int, key string) core.Event {
			return core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       kind,
				App:        snap,
				OccurredAt: occurred,
				ObservedAt: observed,
				Day:        day,
				Quantity:   qty,
				DedupeKey:  key,
				Silent:     true,
				Payload:    base.raw(),
			}
		}

		// The dedupe keys predate the kinds: "installs" is what `device_new`
		// was called when it was mistaken for one. Keeping the key means a
		// database from before the rename does not grow a second copy of every
		// day it already has.
		if base.New > 0 {
			events = append(events, mk(kindDeviceNew, base.New,
				fmt.Sprintf("snapcraft:installs:%s:%s", snap, day)))
		}
		if base.InstalledBase > 0 {
			events = append(events, mk("active_devices", base.InstalledBase,
				fmt.Sprintf("snapcraft:active:%s:%s", snap, day)))
		}
		if base.Lost > 0 {
			events = append(events, mk("lost", base.Lost,
				fmt.Sprintf("snapcraft:lost:%s:%s", snap, day)))
		}
	}
	return events, newest
}

// AudienceEvents turns one snap's weekly_installed_base_by_country into
// `audience` snapshots for every completed day strictly after floor and
// strictly before today, and reports how far the audience cursor may advance.
//
// Each day yields one event per country the store placed devices in, and one
// total with no country. The total is the sum of *every* series, including the
// ones the store names with something other than an ISO code ("??") for
// devices it could not place: those devices are real, they are simply nowhere,
// and the Hearth shows the difference between the total and the placed rows as
// the snap's vessel at sea.
//
// The weekly base is used rather than the daily one because it is the closest
// thing the store has to "how many devices have this": a daily base only counts
// the machines that happened to be switched on that day, and swings by a third
// between a Tuesday and a Sunday for a desktop app.
//
// Nothing here is a difference between two days, so there is nothing to
// baseline and nothing to drift: a snapshot is right or wrong on its own, and
// tomorrow's replaces it.
func AudienceEvents(snap, snapID string, resp MetricsResponse, floor, today string, observed time.Time) ([]core.Event, string) {
	weekly, ok := resp.Metric(metricWeeklyBaseByCountry)
	if !ok {
		return nil, ""
	}

	days := append([]string(nil), weekly.Buckets...)
	sort.Strings(days)

	var (
		events []core.Event
		newest string
		broken bool
	)
	for _, day := range days {
		if day >= today || (floor != "" && day <= floor) {
			continue
		}
		i := indexOf(weekly.Buckets, day)

		// A country with nobody in it is reported as null rather than as zero,
		// so a day is unpublished only when *every* series is null.
		var (
			published bool
			total     int
			placed    = map[string]int{}
		)
		for _, s := range weekly.Series {
			// The series' own value, not a lookup by name: two series that
			// shared a name would otherwise both read the first one's.
			if i < 0 || i >= len(s.Values) || s.Values[i] == nil {
				continue
			}
			published = true
			n := int(*s.Values[i])
			if n <= 0 {
				continue
			}
			total += n
			if cc := strings.ToUpper(strings.TrimSpace(s.Name)); validCountry(cc) {
				placed[cc] += n
			}
		}
		if !published {
			broken = true
			continue
		}
		if !broken && day > newest {
			newest = day
		}
		if total <= 0 {
			continue
		}
		occurred, err := parseDay(day)
		if err != nil {
			continue
		}

		countries := make([]string, 0, len(placed))
		placedTotal := 0
		for cc, n := range placed {
			countries = append(countries, cc)
			placedTotal += n
		}
		// Sorted so a poll is reproducible rather than in Go's map order.
		sort.Strings(countries)

		mk := func(cc string, qty int, key string, p audiencePayload) core.Event {
			return core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       core.KindAudience,
				App:        snap,
				OccurredAt: occurred,
				ObservedAt: observed,
				Day:        day,
				Country:    cc,
				Quantity:   qty,
				DedupeKey:  key,
				// Silent: a snapshot is not news. The pipeline still founds a
				// settlement for a country it has never seen, and mints a
				// milestone when the total reaches one.
				Silent:  true,
				Payload: p.raw(),
			}
		}
		base := audiencePayload{Snap: snap, SnapID: snapID, Date: day, Basis: string(core.BasisWeeklyDevices)}

		for _, cc := range countries {
			p := base
			p.Country = cc
			events = append(events, mk(cc, placed[cc],
				fmt.Sprintf("snapcraft:audience:%s:%s:%s", snap, day, cc), p))
		}
		// The total goes last so that, within a day, the countries it is the
		// sum of are already stored when the pipeline looks at it.
		p := base
		p.Placed = placedTotal
		p.Unplaced = total - placedTotal
		events = append(events, mk("", total,
			fmt.Sprintf("snapcraft:audience:%s:%s", snap, day), p))
	}
	return events, newest
}

// validCountry accepts ISO 3166-1 alpha-2 only. The store also reports "??"
// for devices it could not place, and those belong in unknown lands rather than
// in an invented country.
func validCountry(cc string) bool {
	if len(cc) != 2 {
		return false
	}
	for _, r := range cc {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}
