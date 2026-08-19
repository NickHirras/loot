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

// Metric names used by Loot. The store also publishes
// installed_base_by_channel / _operating_system / _version and the weekly_*
// variants; none of them add a fact the feed can turn into a drop, and every
// extra filter is another series to download every six hours.
const (
	metricDailyDeviceChange = "daily_device_change"
	metricBaseByCountry     = "installed_base_by_country"
)

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

// bucketIndex maps each bucket day to its position in the values vectors.
func (m Metric) bucketIndex() map[string]int {
	idx := make(map[string]int, len(m.Buckets))
	for i, d := range m.Buckets {
		idx[d] = i
	}
	return idx
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

// fetchMetrics asks for both metrics in one request. The country filter starts
// one day earlier than the device-change filter because per-country installs
// are derived from the difference between two consecutive installed-base days.
func (s *Source) fetchMetrics(ctx context.Context, auth credentials, snapID, countryStart, start, end string) (MetricsResponse, error) {
	req := struct {
		Filters []Filter `json:"filters"`
	}{Filters: []Filter{
		{SnapID: snapID, MetricName: metricDailyDeviceChange, Start: start, End: end},
		{SnapID: snapID, MetricName: metricBaseByCountry, Start: countryStart, End: end},
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
func (s *Source) resolveSnapID(ctx context.Context, auth credentials, snap string) (string, error) {
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
func (s *Source) snapIDFromAccount(ctx context.Context, auth credentials, snap string) (string, error) {
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

// do performs one authenticated request and maps the store's error bodies onto
// messages that say what to actually do about them.
func (s *Source) do(ctx context.Context, method, path string, body []byte, auth credentials) ([]byte, error) {
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

// dayPayload is what every snapcraft event carries in Event.Payload.
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
	// Country and Installs are set only on the per-country events, where
	// Installs is a derived difference rather than a reported figure.
	Country  string `json:"country,omitempty"`
	Installs int    `json:"installs,omitempty"`
	// Derived marks a figure Loot computed rather than read. Per-country
	// installs are the only one.
	Derived bool `json:"derived,omitempty"`
	// BaseToday / BaseYesterday show the two installed-base readings the
	// derived figure came from, so a surprising number can be checked.
	BaseToday     int `json:"base_today,omitempty"`
	BaseYesterday int `json:"base_yesterday,omitempty"`
}

func (p dayPayload) raw() json.RawMessage {
	b, _ := json.Marshal(p)
	return b
}

// EventsFromMetrics turns one snap's metrics response into events for every
// completed day strictly after floor and strictly before today, and reports how
// far the caller may advance its cursor.
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
	country, hasCountry := resp.Metric(metricBaseByCountry)
	countryIdx := map[string]int{}
	if hasCountry {
		countryIdx = country.bucketIndex()
	}

	days := append([]string(nil), change.Buckets...)
	sort.Strings(days)

	var (
		events []core.Event
		newest string
		// broken records that an unpublished day has been passed, which is
		// where the contiguous run — and therefore the cursor — stops. Days
		// the store simply does not carry a bucket for are not holes: it
		// truncates the range to what it has rather than padding it, so
		// treating a missing bucket as a hole would stall the cursor forever
		// on a snap younger than the backfill window.
		broken bool
	)
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

		mk := func(kind string, qty int, key string, silent, chest bool, payload dayPayload, cc string) core.Event {
			return core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       kind,
				App:        snap,
				OccurredAt: occurred,
				ObservedAt: observed,
				Day:        day,
				Country:    cc,
				Quantity:   qty,
				DedupeKey:  key,
				IsLedger:   false,
				Silent:     silent,
				Chest:      chest,
				Payload:    payload.raw(),
			}
		}

		if base.New > 0 {
			events = append(events, mk("install", base.New,
				fmt.Sprintf("snapcraft:installs:%s:%s", snap, day), true, false, base, ""))
		}
		if base.InstalledBase > 0 {
			events = append(events, mk("active_devices", base.InstalledBase,
				fmt.Sprintf("snapcraft:active:%s:%s", snap, day), true, false, base, ""))
		}
		if base.Lost > 0 {
			events = append(events, mk("lost", base.Lost,
				fmt.Sprintf("snapcraft:lost:%s:%s", snap, day), true, false, base, ""))
		}

		if hasCountry {
			for _, d := range countryDeltas(country, countryIdx, day) {
				p := base
				p.Country = d.country
				p.Installs = d.delta
				p.Derived = true
				p.BaseToday = d.today
				p.BaseYesterday = d.yesterday
				events = append(events, mk("install", d.delta,
					fmt.Sprintf("snapcraft:installs:%s:%s:%s", snap, day, d.country),
					true, false, p, d.country))
			}
		}

		// The day's one headline, chest-bound so a 30 day backfill fills
		// chests instead of spraying a month of installs across the feed.
		if base.New > 0 {
			events = append(events, mk("installs_day", base.New,
				fmt.Sprintf("snapcraft:installs_day:%s:%s", snap, day), false, true, base, ""))
		}
	}
	return events, newest
}

// countryDelta is one country's derived install count for a day.
type countryDelta struct {
	country   string
	delta     int
	today     int
	yesterday int
}

// countryDeltas derives per-country installs for day from the installed-base
// metric.
//
// installed_base_by_country is a *base*: how many devices in each country were
// running the snap that day, not how many arrived. The daily arrival is
// therefore base[day] − base[day−1], clamped at zero.
//
// That clamp is the honest limit of this data. A country that gained 5 devices
// and lost 7 reads as 0, not 5, so the derived figure is a *net* gain and never
// exceeds the truth. It is used for the globe's settlements, where "a country
// that grew today" is the question being asked; the authoritative daily install
// count is the un-countried daily_device_change "new" series, which is emitted
// separately and is not the sum of these.
func countryDeltas(country Metric, idx map[string]int, day string) []countryDelta {
	i, ok := idx[day]
	if !ok || i == 0 {
		return nil
	}
	// Require the preceding bucket to be the preceding calendar day; a gap in
	// the buckets would turn a multi-day gain into a one-day spike.
	if country.Buckets[i-1] != addDays(day, -1) {
		return nil
	}

	out := make([]countryDelta, 0, 8)
	for _, s := range country.Series {
		cc := strings.ToUpper(strings.TrimSpace(s.Name))
		if !validCountry(cc) {
			continue
		}
		cur, okCur := country.at(s.Name, i)
		prev, okPrev := country.at(s.Name, i-1)
		if !okCur || !okPrev {
			continue
		}
		delta := int(cur) - int(prev)
		if delta <= 0 {
			continue
		}
		out = append(out, countryDelta{country: cc, delta: delta, today: int(cur), yesterday: int(prev)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].country < out[b].country })
	return out
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
