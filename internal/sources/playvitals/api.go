package playvitals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The Play Developer Reporting API, in as few types as it takes.
//
// Everything interesting about this API is in the shapes rather than the
// endpoints: every metric set is queried through the same
// `{name}:query` POST, with the same `timelineSpec`, and answers with the same
// generic `rows[]` of dimensions and metrics. So there is one request type,
// one response type and one decoder here, and the three metric sets differ
// only by which strings go in the dimensions and metrics lists.
//
// Two traps are worth naming, because both are silent:
//
//   - `startTime` / `endTime` are `google.type.DateTime` *objects*, not
//     RFC3339 strings. Sending a string is accepted as a parse failure at the
//     far end and reads as an empty result set.
//   - `int64Value` and `decimalValue.value` arrive as JSON *strings*. Decoding
//     them into a float64 silently fails; they are parsed by hand below.

// DefaultBaseURL is the Play Developer Reporting root. Tests point this at an
// httptest server.
const DefaultBaseURL = "https://playdeveloperreporting.googleapis.com"

// Scope is the single OAuth2 scope the whole API uses. It was, for years,
// missing from Google's own published scope list, which is why a 403 saying
// "Request had insufficient authentication scopes" is the most common way this
// source fails.
const Scope = "https://www.googleapis.com/auth/playdeveloperreporting"

// The metric sets Loot reads. Crash and ANR *rates* come from their own sets;
// the actual crash *counts* — the numbers a health bar is made of — only exist
// in the error count set.
const (
	crashRateSet  = "crashRateMetricSet"
	anrRateSet    = "anrRateMetricSet"
	errorCountSet = "errorCountMetricSet"
)

// ReportingTimeZone is the zone Play states vitals in. The API asks for a
// timezone rather than an offset, and this is the one its own console uses.
const ReportingTimeZone = "America/Los_Angeles"

// maxPageSize is the largest page the API will hand back for a query.
const maxPageSize = 1000

// DateTime is google.type.DateTime, restricted to the fields Loot sets. The
// oneof at the end is honoured by only ever setting TimeZone.
type DateTime struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
	// Hours is omitted when zero: seen in the wild, the API answers
	// 400 "Hours should be unset for DAILY aggregation period" if a DAILY
	// query so much as mentions hours: 0.
	Hours  int `json:"hours,omitempty"`
	Minute int `json:"minutes,omitempty"`
	Second int `json:"seconds,omitempty"`
	// TimeZone and UTCOffset are a oneof; setting both is an error at the far
	// end, so Loot only ever sets the zone.
	TimeZone  *TimeZone `json:"timeZone,omitempty"`
	UTCOffset string    `json:"utcOffset,omitempty"`
}

// TimeZone is google.type.TimeZone.
type TimeZone struct {
	ID string `json:"id"`
}

// Day renders the DateTime's calendar day, which is the only part of it Loot
// ever uses: vitals are daily, and the hour is always midnight.
func (d DateTime) Day_() string {
	if d.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// dayDateTime builds midnight on a calendar day, in the reporting zone.
func dayDateTime(day time.Time) DateTime {
	return DateTime{
		Year:     day.Year(),
		Month:    int(day.Month()),
		Day:      day.Day(),
		TimeZone: &TimeZone{ID: ReportingTimeZone},
	}
}

// timelineSpec is the window and granularity of a query.
type timelineSpec struct {
	AggregationPeriod string   `json:"aggregationPeriod"`
	StartTime         DateTime `json:"startTime"`
	EndTime           DateTime `json:"endTime"`
}

// queryRequest is the body of every `{metricSet}:query` POST.
type queryRequest struct {
	TimelineSpec timelineSpec `json:"timelineSpec"`
	Dimensions   []string     `json:"dimensions,omitempty"`
	Metrics      []string     `json:"metrics"`
	PageSize     int          `json:"pageSize,omitempty"`
	PageToken    string       `json:"pageToken,omitempty"`
}

// queryResponse is the body every `{metricSet}:query` POST answers with.
type queryResponse struct {
	Rows          []rawRow `json:"rows"`
	NextPageToken string   `json:"nextPageToken"`
}

type rawRow struct {
	StartTime  DateTime       `json:"startTime"`
	Dimensions []rawDimension `json:"dimensions"`
	Metrics    []rawMetric    `json:"metrics"`
}

type rawDimension struct {
	Dimension string `json:"dimension"`
	// StringValue and Int64Value are a oneof, and Int64Value arrives as a
	// JSON string because it is an int64.
	StringValue string `json:"stringValue"`
	Int64Value  string `json:"int64Value"`
	// ValueLabel is the human rendering — the marketing version name beside a
	// version *code*, which is the one a person recognizes.
	ValueLabel string `json:"valueLabel"`
}

// Value is the dimension's identity: whichever half of the oneof arrived.
func (d rawDimension) Value() string {
	if d.Int64Value != "" {
		return d.Int64Value
	}
	return d.StringValue
}

type rawMetric struct {
	Metric       string `json:"metric"`
	DecimalValue struct {
		Value string `json:"value"`
	} `json:"decimalValue"`
	// NullValue means "no data for this cell", which is not the same as zero
	// and must not be read as one.
	NullValue bool `json:"nullValue"`
}

// Row is one decoded row of a query: a day, its dimension values and its
// metric values.
type Row struct {
	Day string
	// Dimensions maps a dimension name to its value, and Labels to its human
	// label where the API supplied one.
	Dimensions map[string]string
	Labels     map[string]string
	// Metrics maps a metric name to its value. A null cell is simply absent.
	Metrics map[string]float64
}

// Dim returns a dimension value, or "".
func (r Row) Dim(name string) string { return r.Dimensions[name] }

// Label returns a dimension's human label, falling back to its raw value.
func (r Row) Label(name string) string {
	if v := r.Labels[name]; v != "" {
		return v
	}
	return r.Dimensions[name]
}

// Metric returns a metric value and whether the row had one.
func (r Row) Metric(name string) (float64, bool) {
	v, ok := r.Metrics[name]
	return v, ok
}

func decodeRow(raw rawRow) Row {
	row := Row{
		Day:        raw.StartTime.Day_(),
		Dimensions: map[string]string{},
		Labels:     map[string]string{},
		Metrics:    map[string]float64{},
	}
	for _, d := range raw.Dimensions {
		row.Dimensions[d.Dimension] = d.Value()
		if d.ValueLabel != "" {
			row.Labels[d.Dimension] = d.ValueLabel
		}
	}
	for _, m := range raw.Metrics {
		if m.NullValue || m.DecimalValue.Value == "" {
			continue
		}
		if v, err := strconv.ParseFloat(m.DecimalValue.Value, 64); err == nil {
			row.Metrics[m.Metric] = v
		}
	}
	return row
}

// Query runs one metric set query over [from, to] and returns every row,
// following pagination. `to` is the *exclusive* end the API asks for, which is
// exactly what the freshness endpoint hands back.
func (s *Source) Query(ctx context.Context, pkg, metricSet string, dimensions, metrics []string, from time.Time, to DateTime) ([]Row, error) {
	req := queryRequest{
		TimelineSpec: timelineSpec{
			AggregationPeriod: "DAILY",
			StartTime:         dayDateTime(from),
			EndTime:           to,
		},
		Dimensions: dimensions,
		Metrics:    metrics,
		PageSize:   maxPageSize,
	}

	endpoint := fmt.Sprintf("%s/v1beta1/apps/%s/%s:query",
		strings.TrimSuffix(s.baseURL(), "/"), pkg, metricSet)

	var out []Row
	for {
		body, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("playvitals: encode %s query: %w", metricSet, err)
		}
		raw, err := s.do(ctx, http.MethodPost, endpoint, body, metricSet+" query")
		if err != nil {
			return nil, err
		}
		var page queryResponse
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("playvitals: decode %s query: %w", metricSet, err)
		}
		for _, r := range page.Rows {
			if row := decodeRow(r); row.Day != "" {
				out = append(out, row)
			}
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		req.PageToken = page.NextPageToken
	}
}

// metricSetInfo is what GET on a metric set answers with.
type metricSetInfo struct {
	Name          string `json:"name"`
	FreshnessInfo struct {
		Freshnesses []struct {
			AggregationPeriod string   `json:"aggregationPeriod"`
			LatestEndTime     DateTime `json:"latestEndTime"`
		} `json:"freshnesses"`
	} `json:"freshnessInfo"`
}

// Freshness asks how far a metric set's daily data actually runs. Reading it
// first is not optional: vitals lag the calendar by a day or two and vary by
// metric set, and querying past the edge answers with a silently short result
// rather than an error — which would look exactly like a crash that stopped.
func (s *Source) Freshness(ctx context.Context, pkg, metricSet string) (DateTime, error) {
	endpoint := fmt.Sprintf("%s/v1beta1/apps/%s/%s",
		strings.TrimSuffix(s.baseURL(), "/"), pkg, metricSet)

	raw, err := s.do(ctx, http.MethodGet, endpoint, nil, metricSet+" freshness")
	if err != nil {
		return DateTime{}, err
	}
	var info metricSetInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return DateTime{}, fmt.Errorf("playvitals: decode %s freshness: %w", metricSet, err)
	}
	for _, f := range info.FreshnessInfo.Freshnesses {
		if f.AggregationPeriod == "DAILY" {
			return f.LatestEndTime, nil
		}
	}
	return DateTime{}, fmt.Errorf("playvitals: %s reports no DAILY freshness for %s", metricSet, pkg)
}

// maxResponseBytes caps a single response. A month of per-version rows is a
// few hundred KB; the cap is there so a wrong endpoint cannot exhaust memory.
const maxResponseBytes = 32 << 20

// do performs one authenticated request and returns the body, translating the
// two failures that actually happen into advice rather than a status code.
func (s *Source) do(ctx context.Context, method, endpoint string, body []byte, what string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("playvitals: %s: %w", what, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.Tokens != nil {
		tok, err := s.Tokens.Token()
		if err != nil {
			return nil, fmt.Errorf("playvitals: authenticate: %w", err)
		}
		tok.SetAuthHeader(req)
	}

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("playvitals: %s: %w", what, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("playvitals: read %s: %w", what, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, apiError(resp.StatusCode, what, raw)
	}
	return raw, nil
}

// apiError turns the two 403s everybody hits into instructions.
//
// Both of them are configuration mistakes made once, in a console, months
// before the error appears — so a bare "403 Forbidden" is close to useless,
// and the fix is two sentences long.
func apiError(status int, what string, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	lower := strings.ToLower(msg)

	switch {
	case status == http.StatusForbidden && strings.Contains(lower, "insufficient authentication scopes"):
		return fmt.Errorf("playvitals: %s: the access token is missing the %s scope "+
			"(this is Loot's bug if you see it — please report it)", what, Scope)

	case status == http.StatusForbidden && (strings.Contains(lower, "service_disabled") ||
		strings.Contains(lower, "has not been used in project")):
		return fmt.Errorf("playvitals: %s: the Google Play Developer Reporting API is not enabled "+
			"in the Cloud project that owns your service account. Enable "+
			"playdeveloperreporting.googleapis.com in the Cloud console, wait a minute, and retry", what)

	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return fmt.Errorf("playvitals: %s: permission denied. In Play Console > Users and permissions, "+
			"invite the service account's email and grant it "+
			"\"View app information and download bulk reports (read-only)\" for this app: %s", what, msg)

	case status == http.StatusNotFound:
		return fmt.Errorf("playvitals: %s: no such app. Check the package name is exactly as it appears "+
			"in Play Console (%s)", what, msg)
	}
	return fmt.Errorf("playvitals: %s: %s: %s", what, http.StatusText(status), msg)
}

// dayBefore returns the calendar day immediately before an exclusive end. It
// is the newest day a query over [_, end) actually returns.
func dayBefore(end DateTime) string {
	day := end.Day_()
	if day == "" {
		return ""
	}
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return ""
	}
	return core.DayOf(t.AddDate(0, 0, -1))
}
