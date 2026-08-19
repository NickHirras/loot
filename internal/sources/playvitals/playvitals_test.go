package playvitals_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/playvitals"
)

const pkg = "com.example.app"

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixture is a stand-in Play Developer Reporting API: it answers the freshness
// GET and the three metric-set queries with whatever the test hands it.
type fixture struct {
	// freshness is the exclusive end the API reports for DAILY data.
	freshness string
	// errorCounts is the errorCountMetricSet result, keyed by day, then by
	// (versionCode, reportType).
	errorCounts map[string]map[[2]string][2]float64
	// crashRates and anrRates are the app-wide daily rate series.
	crashRates map[string]float64
	anrRates   map[string]float64

	// calls records every path the source asked for, so a test can assert the
	// freshness endpoint really is consulted first.
	calls []string
	// fail, when set, makes that metric set answer with an error body.
	fail map[string]int
}

func (f *fixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)

		for set, status := range f.fail {
			if strings.Contains(r.URL.Path, set) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"status":"PERMISSION_DENIED","message":"The caller does not have permission."}}`)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			// As strict as the real API (seen on first contact): a DAILY query
			// whose timeline mentions hours at all is a 400.
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"hours"`) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"message":"Hours should be unset for DAILY aggregation period with 'start_time' and 'end_time'.","status":"INVALID_ARGUMENT"}}`)
				return
			}
		}
		switch {
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, f.freshnessJSON())
		case strings.HasSuffix(r.URL.Path, "errorCountMetricSet:query"):
			_, _ = io.WriteString(w, f.errorCountJSON())
		case strings.HasSuffix(r.URL.Path, "crashRateMetricSet:query"):
			_, _ = io.WriteString(w, rateJSON("crashRate", f.crashRates))
		case strings.HasSuffix(r.URL.Path, "anrRateMetricSet:query"):
			_, _ = io.WriteString(w, rateJSON("anrRate", f.anrRates))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func dateTimeJSON(day string) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return "{}"
	}
	return fmt.Sprintf(`{"year":%d,"month":%d,"day":%d,"hours":0,"timeZone":{"id":"America/Los_Angeles"}}`,
		t.Year(), int(t.Month()), t.Day())
}

func (f *fixture) freshnessJSON() string {
	return fmt.Sprintf(`{"name":"apps/%s/errorCountMetricSet","freshnessInfo":{"freshnesses":[
	  {"aggregationPeriod":"HOURLY","latestEndTime":%s},
	  {"aggregationPeriod":"DAILY","latestEndTime":%s}]}}`,
		pkg, dateTimeJSON(f.freshness), dateTimeJSON(f.freshness))
}

// errorCountJSON writes the generic rows[] shape, with the two traps the real
// API has: int64 dimensions and decimal metrics both arrive as *strings*.
func (f *fixture) errorCountJSON() string {
	var rows []string
	for _, day := range sortedDays(f.errorCounts) {
		for dims, metrics := range f.errorCounts[day] {
			rows = append(rows, fmt.Sprintf(`{
			  "startTime": %s,
			  "dimensions": [
			    {"dimension":"versionCode","int64Value":"%s","valueLabel":"%s (build %s)"},
			    {"dimension":"reportType","stringValue":"%s"}
			  ],
			  "metrics": [
			    {"metric":"errorReportCount","decimalValue":{"value":"%g"}},
			    {"metric":"distinctUsers","decimalValue":{"value":"%g"}}
			  ]}`, dateTimeJSON(day), dims[0], "4.2.0", dims[0], dims[1], metrics[0], metrics[1]))
		}
	}
	return `{"rows":[` + strings.Join(rows, ",") + `]}`
}

func rateJSON(metric string, series map[string]float64) string {
	var rows []string
	for _, day := range sortedFloatDays(series) {
		rows = append(rows, fmt.Sprintf(
			`{"startTime":%s,"dimensions":[],"metrics":[{"metric":"%s","decimalValue":{"value":"%g"}}]}`,
			dateTimeJSON(day), metric, series[day]))
	}
	return `{"rows":[` + strings.Join(rows, ",") + `]}`
}

func sortedDays(m map[string]map[[2]string][2]float64) []string {
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sortStrings(out)
	return out
}

func sortedFloatDays(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func newSource(t *testing.T, f *fixture) *playvitals.Source {
	t.Helper()
	ts := f.server(t)
	return &playvitals.Source{
		Packages:     []string{pkg},
		BackfillDays: 30,
		BaseURL:      ts.URL,
		Tokens:       oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"}),
		Client:       ts.Client(),
		Log:          quiet(),
		Now:          func() time.Time { return now },
	}
}

func payloadOf(t *testing.T, ev core.Event) core.CrashPayload {
	t.Helper()
	var p core.CrashPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return p
}

// A simple two-day window: two versions crashing, one of them ANRing.
func simpleFixture() *fixture {
	return &fixture{
		// Exclusive end, exactly as the freshness endpoint reports it.
		freshness: "2026-06-01",
		errorCounts: map[string]map[[2]string][2]float64{
			"2026-05-30": {
				{"4812", "CRASH"}: {120, 44},
				{"4811", "CRASH"}: {6, 3},
				{"4812", "ANR"}:   {9, 5},
				// NON_FATAL is a handled exception, not a crash.
				{"4812", "NON_FATAL"}: {900, 300},
			},
			"2026-05-31": {
				{"4812", "CRASH"}: {40, 18},
			},
		},
		crashRates: map[string]float64{"2026-05-30": 0.0121, "2026-05-31": 0.004},
		anrRates:   map[string]float64{"2026-05-30": 0.0009, "2026-05-31": 0.0002},
	}
}

func TestPollParsesRowsAndSkipsNonFatals(t *testing.T) {
	f := simpleFixture()
	src := newSource(t, f)

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	var crashes, heartbeats int
	byKey := map[string]core.Event{}
	for _, ev := range events {
		if !ev.Silent {
			t.Errorf("event %s is not silent; a crash never makes a drop of its own", ev.DedupeKey)
		}
		if ev.App != pkg {
			t.Errorf("app = %q, want %q", ev.App, pkg)
		}
		switch ev.Kind {
		case core.KindCrash:
			crashes++
			byKey[ev.DedupeKey] = ev
		case core.KindCrashDay:
			heartbeats++
			byKey[ev.DedupeKey] = ev
		default:
			t.Errorf("unexpected kind %q", ev.Kind)
		}
	}
	if crashes != 4 {
		t.Errorf("emitted %d crash rows, want 4 (NON_FATAL excluded)", crashes)
	}
	if heartbeats != 2 {
		t.Errorf("emitted %d heartbeats, want one per day", heartbeats)
	}

	ev, ok := byKey["playvitals:crash:"+pkg+":2026-05-30:4812:crash"]
	if !ok {
		t.Fatalf("no event for the expected dedupe key; got %v", keysOf(byKey))
	}
	if ev.Quantity != 120 {
		t.Errorf("quantity = %d, want 120", ev.Quantity)
	}
	p := payloadOf(t, ev)
	if p.UsersAffected != 44 {
		t.Errorf("users_affected = %d, want 44", p.UsersAffected)
	}
	if p.Version != "4812" {
		t.Errorf("version = %q, want the version code", p.Version)
	}
	if p.CrashRate != 0.0121 || p.ANRRate != 0.0009 {
		t.Errorf("rates = %v / %v, want the day's series values", p.CrashRate, p.ANRRate)
	}

	anr := byKey["playvitals:crash:"+pkg+":2026-05-30:4812:anr"]
	if got := payloadOf(t, anr).Kind; got != core.BossKindANR {
		t.Errorf("ANR row kind = %q, want anr", got)
	}

	// The heartbeat carries the whole day, so a quiet day is legible.
	beat := byKey["playvitals:crash_day:"+pkg+":2026-05-30"]
	if beat.Quantity != 135 {
		t.Errorf("heartbeat quantity = %d, want 120+6+9 = 135", beat.Quantity)
	}

	var st struct {
		Days map[string]string `json:"days"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if st.Days[pkg] != "2026-05-31" {
		t.Errorf("cursor = %q, want the last settled day", st.Days[pkg])
	}
}

// The cursor is what stops a six-hourly poll from re-emitting a month of rows.
func TestCursorStopsRework(t *testing.T) {
	f := simpleFixture()
	src := newSource(t, f)

	_, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	events, _, err := src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("second poll emitted %d events, want 0", len(events))
	}

	// A new settled day arrives.
	f.freshness = "2026-06-02"
	f.errorCounts["2026-06-01"] = map[[2]string][2]float64{{"4812", "CRASH"}: {11, 5}}
	f.crashRates["2026-06-01"] = 0.001
	events, _, err = src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("third poll: %v", err)
	}
	for _, ev := range events {
		if ev.Day <= "2026-05-31" {
			t.Errorf("re-emitted %s for %s", ev.DedupeKey, ev.Day)
		}
	}
	if len(events) == 0 {
		t.Fatal("the new day emitted nothing")
	}
}

// Freshness is read before anything is queried. Querying past the edge answers
// short rather than erroring, which would look exactly like a crash that
// stopped — the one mistake this source must never make.
func TestFreshnessIsConsultedFirst(t *testing.T) {
	f := simpleFixture()
	src := newSource(t, f)
	if _, _, err := src.Poll(context.Background(), nil); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(f.calls) == 0 || !strings.HasPrefix(f.calls[0], "GET ") {
		t.Fatalf("first call was %v, want the freshness GET", f.calls)
	}
}

// A settled day the cursor already covers means there is nothing to ask for.
func TestNothingNewIsCheap(t *testing.T) {
	f := simpleFixture()
	src := newSource(t, f)
	state := []byte(`{"days":{"` + pkg + `":"2026-05-31"}}`)

	events, _, err := src.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("emitted %d events, want 0", len(events))
	}
	if len(f.calls) != 1 {
		t.Errorf("made %d calls for a settled cursor, want 1 (the freshness read)", len(f.calls))
	}
}

// The rate sets are a nicety. Losing them must not cost the counts, which are
// what the fight is actually made of.
func TestRateFailureDoesNotLoseTheCounts(t *testing.T) {
	f := simpleFixture()
	f.fail = map[string]int{"crashRateMetricSet": http.StatusInternalServerError}
	src := newSource(t, f)

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("a failed rate query lost every crash row")
	}
	for _, ev := range events {
		if ev.Kind == core.KindCrash && payloadOf(t, ev).CrashRate != 0 {
			t.Error("a crash rate survived a failed rate query")
		}
	}
}

// A permission failure is a configuration mistake made months ago in a
// console, so the error has to be instructions rather than a status code.
func TestPermissionErrorExplainsItself(t *testing.T) {
	f := simpleFixture()
	f.fail = map[string]int{"errorCountMetricSet": http.StatusForbidden}
	src := newSource(t, f)

	err := src.Check(context.Background())
	if err == nil {
		t.Fatal("Check succeeded against a 403")
	}
	msg := err.Error()
	for _, want := range []string{"Play Console", "Users and permissions"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestCheckSucceedsOnAHealthyApp(t *testing.T) {
	src := newSource(t, simpleFixture())
	if err := src.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func keysOf(m map[string]core.Event) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// Seen on first real contact: the API answers 400 "Hours should be unset for
// DAILY aggregation period" if the timeline's DateTime carries hours: 0.
func TestDailyDateTimeOmitsHours(t *testing.T) {
	b, err := json.Marshal(playvitals.DateTime{Year: 2026, Month: 8, Day: 19, TimeZone: &playvitals.TimeZone{ID: playvitals.ReportingTimeZone}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"hours"`) {
		t.Fatalf("DAILY DateTime must not mention hours: %s", b)
	}
	if !strings.Contains(string(b), `"timeZone":{"id":"America/Los_Angeles"}`) {
		t.Fatalf("DateTime lost its zone: %s", b)
	}
}
