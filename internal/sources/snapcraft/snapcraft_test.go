package snapcraft

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

const testSnapID = "SNAPID00000000000000000000000001"

// fixtureToday is the day after the fixture's last bucket, so 2026-08-17 counts
// as a completed (but still unpublished, all-null) day.
var fixtureToday = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func loadFixture(t *testing.T) MetricsResponse {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "metrics.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	resp, err := ParseMetrics(b)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return resp
}

func TestParseMetricsWithNulls(t *testing.T) {
	resp := loadFixture(t)
	if len(resp.Metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(resp.Metrics))
	}
	change, ok := resp.Metric(metricDailyDeviceChange)
	if !ok {
		t.Fatal("daily_device_change missing")
	}
	if got := len(change.Buckets); got != 3 {
		t.Errorf("buckets = %d, want 3", got)
	}
	if v, ok := change.at(seriesNew, 0); !ok || v != 12 {
		t.Errorf("new[0] = %v %v, want 12 true", v, ok)
	}
	if _, ok := change.at(seriesNew, 2); ok {
		t.Errorf("new[2] is null in the fixture but read as present")
	}
	if _, ok := change.at("nonexistent", 0); ok {
		t.Errorf("unknown series reported as present")
	}
	if _, ok := resp.Metric("installed_base_by_channel"); ok {
		t.Errorf("un-requested metric reported as present")
	}
}

func TestMetricStatusNotOK(t *testing.T) {
	resp, err := ParseMetrics([]byte(`{"metrics":[{"status":"NO_DATA","metric_name":"daily_device_change","buckets":[],"series":[]}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := resp.Metric(metricDailyDeviceChange); ok {
		t.Errorf("NO_DATA metric reported as usable")
	}
}

func TestCountryDeltaDerivation(t *testing.T) {
	resp := loadFixture(t)
	country, ok := resp.Metric(metricBaseByCountry)
	if !ok {
		t.Fatal("installed_base_by_country missing")
	}
	idx := country.bucketIndex()

	// 2026-08-15: US 100 -> 110 = +10. DE 50 -> 47 clamps to nothing (a net
	// loss is not a negative install). FR's previous reading is null, so it
	// cannot be differenced. "??" is not a country.
	got := countryDeltas(country, idx, "2026-08-15", nil)
	if len(got) != 1 || got[0].country != "US" || got[0].delta != 10 {
		t.Fatalf("2026-08-15 deltas = %+v, want one US +10", got)
	}
	if got[0].yesterday != 100 || got[0].today != 110 {
		t.Errorf("US delta lost its base readings: %+v", got[0])
	}

	// 2026-08-16: US +8, FR +4, DE flat (0 is not an arrival).
	got = countryDeltas(country, idx, "2026-08-16", nil)
	if len(got) != 2 {
		t.Fatalf("2026-08-16 deltas = %+v, want 2", got)
	}
	if got[0].country != "FR" || got[0].delta != 4 {
		t.Errorf("first delta = %+v, want FR +4 (sorted)", got[0])
	}
	if got[1].country != "US" || got[1].delta != 8 {
		t.Errorf("second delta = %+v, want US +8", got[1])
	}

	// The very first bucket has no predecessor to difference against.
	if got := countryDeltas(country, idx, "2026-08-14", nil); got != nil {
		t.Errorf("first bucket produced deltas: %+v", got)
	}
	// A day the metric never reported.
	if got := countryDeltas(country, idx, "2026-01-01", nil); got != nil {
		t.Errorf("unknown day produced deltas: %+v", got)
	}
}

func TestCountryDeltaRequiresContiguousBuckets(t *testing.T) {
	m := Metric{
		MetricName: metricBaseByCountry,
		Buckets:    []string{"2026-08-10", "2026-08-16"},
		Series:     []Series{{Name: "US", Values: []*float64{f(100), f(140)}}},
	}
	if got := countryDeltas(m, m.bucketIndex(), "2026-08-16", nil); got != nil {
		t.Errorf("a six day gap was treated as one day's installs: %+v", got)
	}
}

func f(v float64) *float64 { return &v }

// alreadyFounded is a baseline map old enough that no day in the fixture is on
// or before it, so the per-day derivation behaves exactly as it did before
// baselines existed. The founding itself is covered by the TestBaseline* tests
// below.
var alreadyFounded = map[string]string{
	"US": "2026-01-01", "DE": "2026-01-01", "FR": "2026-01-01",
}

func TestEventsFromMetrics(t *testing.T) {
	resp := loadFixture(t)
	events, newest, fresh := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday, alreadyFounded)

	if len(fresh) != 0 {
		t.Errorf("re-founded already-baselined countries: %v", fresh)
	}
	if newest != "2026-08-16" {
		t.Errorf("newest = %q, want 2026-08-16 (08-17 is all null and must not move the cursor)", newest)
	}

	byKey := map[string]core.Event{}
	for _, e := range events {
		if _, dup := byKey[e.DedupeKey]; dup {
			t.Errorf("duplicate dedupe key %q", e.DedupeKey)
		}
		byKey[e.DedupeKey] = e
		if e.Source != Name {
			t.Errorf("source = %q", e.Source)
		}
		if e.IsLedger {
			t.Errorf("%s is marked ledger; installs are not money", e.DedupeKey)
		}
		if strings.HasPrefix(e.Day, "2026-08-17") {
			t.Errorf("emitted the unpublished day: %s", e.DedupeKey)
		}
	}

	want := map[string]struct {
		kind     string
		qty      int
		country  string
		silent   bool
		chest    bool
		occurred string
	}{
		"snapcraft:installs:tide-clock:2026-08-15":     {"install", 12, "", true, false, "2026-08-15"},
		"snapcraft:active:tide-clock:2026-08-15":       {"active_devices", 312, "", true, false, "2026-08-15"},
		"snapcraft:lost:tide-clock:2026-08-15":         {"lost", 5, "", true, false, "2026-08-15"},
		"snapcraft:installs:tide-clock:2026-08-15:US":  {"install", 10, "US", true, false, "2026-08-15"},
		"snapcraft:installs_day:tide-clock:2026-08-15": {"installs_day", 12, "", false, true, "2026-08-15"},
		"snapcraft:installs:tide-clock:2026-08-16":     {"install", 20, "", true, false, "2026-08-16"},
		"snapcraft:active:tide-clock:2026-08-16":       {"active_devices", 330, "", true, false, "2026-08-16"},
		"snapcraft:lost:tide-clock:2026-08-16":         {"lost", 3, "", true, false, "2026-08-16"},
		"snapcraft:installs:tide-clock:2026-08-16:US":  {"install", 8, "US", true, false, "2026-08-16"},
		"snapcraft:installs:tide-clock:2026-08-16:FR":  {"install", 4, "FR", true, false, "2026-08-16"},
		"snapcraft:installs_day:tide-clock:2026-08-16": {"installs_day", 20, "", false, true, "2026-08-16"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(events), len(want), keysOf(byKey))
	}
	for key, w := range want {
		e, ok := byKey[key]
		if !ok {
			t.Errorf("missing event %q", key)
			continue
		}
		if e.Kind != w.kind || e.Quantity != w.qty || e.Country != w.country ||
			e.Silent != w.silent || e.Chest != w.chest || e.Day != w.occurred {
			t.Errorf("%s = kind %q qty %d country %q silent %v chest %v day %q",
				key, e.Kind, e.Quantity, e.Country, e.Silent, e.Chest, e.Day)
		}
		if e.OccurredAt.Format(core.DayLayout) != w.occurred {
			t.Errorf("%s occurred_at = %s", key, e.OccurredAt)
		}
	}

	// Exactly one non-silent, chest-bound drop per day.
	drops := 0
	for _, e := range events {
		if !e.Silent {
			drops++
			if e.Kind != "installs_day" || !e.Chest {
				t.Errorf("unexpected non-silent event %q (kind %q chest %v)", e.DedupeKey, e.Kind, e.Chest)
			}
		}
	}
	if drops != 2 {
		t.Errorf("non-silent events = %d, want 2 (one per day)", drops)
	}

	// The per-country payload must own up to being derived.
	var p dayPayload
	if err := json.Unmarshal(byKey["snapcraft:installs:tide-clock:2026-08-16:FR"].Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !p.Derived || p.Country != "FR" || p.Installs != 4 || p.BaseYesterday != 5 || p.BaseToday != 9 {
		t.Errorf("country payload = %+v", p)
	}
}

func TestEventsFromMetricsRespectsFloor(t *testing.T) {
	resp := loadFixture(t)
	events, newest, _ := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-15", "2026-08-18", fixtureToday, alreadyFounded)
	if newest != "2026-08-16" {
		t.Errorf("newest = %q", newest)
	}
	for _, e := range events {
		if e.Day != "2026-08-16" {
			t.Errorf("floor 2026-08-15 let through %s", e.DedupeKey)
		}
	}
}

// A day the store has not published yet sits between two days it has. The
// cursor must stop *before* the hole, or that day's figures are never read
// once they land — the events past the hole are still emitted, because their
// dedupe keys make re-reading them free.
func TestEventsFromMetricsCursorStopsAtAnUnpublishedDay(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	resp := MetricsResponse{Metrics: []Metric{{
		Status:     "OK",
		SnapID:     testSnapID,
		MetricName: metricDailyDeviceChange,
		Buckets:    []string{"2026-08-12", "2026-08-13", "2026-08-14", "2026-08-15"},
		Series: []Series{
			{Name: seriesNew, Values: []*float64{f(10), f(11), nil, f(13)}},
			{Name: seriesContinued, Values: []*float64{f(100), f(101), nil, f(103)}},
			{Name: seriesLost, Values: []*float64{f(1), f(1), nil, f(1)}},
		},
	}}}

	events, newest, _ := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-11", "2026-08-16", fixtureToday, nil)
	if newest != "2026-08-13" {
		t.Fatalf("cursor = %q, want 2026-08-13: the 14th was never published", newest)
	}

	days := map[string]bool{}
	for _, e := range events {
		days[e.Day] = true
	}
	if days["2026-08-14"] {
		t.Error("an unpublished day produced events")
	}
	if !days["2026-08-15"] {
		t.Error("the day after the hole was dropped; only the cursor should wait")
	}
}

func keysOf(m map[string]core.Event) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------- the source

// testSource builds a Source pointed at a test server, with a real (legacy
// format) login file on disk.
func testSource(t *testing.T, baseURL string, backfillDays int) *Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapcraft-login")
	if err := os.WriteFile(path, []byte(legacyINI()), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := New(config.Snapcraft{
		Snaps:        []string{"tide-clock"},
		LoginPath:    path,
		BackfillDays: backfillDays,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src.BaseURL = baseURL
	// Ubuntu One SSO is served by the same test mux; no test may reach the
	// real login.ubuntu.com.
	src.AuthURL = baseURL
	src.Now = func() time.Time { return fixtureToday }
	return src
}

// storeServer is a stand-in for dashboard.snapcraft.io. Handlers may be
// overridden per test.
type storeServer struct {
	t             *testing.T
	metricsBody   string
	metricsStatus int
	infoStatus    int
	accountBody   string
	metricsCalls  int
	infoCalls     int
	accountCalls  int
	lastAuth      string
	lastFilters   []Filter

	// Ubuntu One SSO stands on the same test server: a different host in
	// production, but the source reaches it through AuthURL, and pointing that
	// at the same mux keeps every test off the network. The zero value refuses
	// to refresh, so a test that does not opt in exercises "refresh is not
	// possible" rather than quietly reaching login.ubuntu.com.
	refreshWith   string // discharge to hand back; empty means refuse
	refreshStatus int    // status for a refusal, default 401
	refreshDelay  time.Duration
	refreshCalls  int
	refreshSent   string // the discharge_macaroon the source sent
	refreshBound  bool   // did the source send a *bound* discharge?

	// staleAuth, when set, is an Authorization header value the store answers
	// exactly as production did: 401 macaroon-needs-refresh. It is the header
	// built from the *exported* discharge, so a request only gets through once
	// the source has refreshed and rebound.
	staleAuth string
	staleHits int

	// mu guards the counters, which the single-flight test writes from two
	// request goroutines at once.
	mu sync.Mutex
}

// rejectStale answers the production 401 for a request still carrying the
// stale discharge, and reports whether it did.
func (s *storeServer) rejectStale(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	stale := s.staleAuth != "" && r.Header.Get("Authorization") == s.staleAuth
	if stale {
		s.staleHits++
	}
	s.mu.Unlock()
	if !stale {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, `{"error_list":[{"message":"Expired macaroon (age: 139631 seconds)",`+
		`"code":"macaroon-needs-refresh"}]}`)
	return true
}

// serve replaces the metrics body between polls, so one test can walk a snap
// through several days of the store's answers.
func (s *storeServer) serve(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metricsBody = body
}

// counts reads the call counters under the lock.
func (s *storeServer) counts() (metrics, info, refresh, stale int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metricsCalls, s.infoCalls, s.refreshCalls, s.staleHits
}

func (s *storeServer) start() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/dev/api/snaps/metrics", func(w http.ResponseWriter, r *http.Request) {
		if s.rejectStale(w, r) {
			return
		}
		s.mu.Lock()
		s.metricsCalls++
		s.lastAuth = r.Header.Get("Authorization")
		s.mu.Unlock()
		if r.Method != http.MethodPost {
			s.t.Errorf("metrics method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			s.t.Errorf("metrics content-type = %q", ct)
		}
		var body struct {
			Filters []Filter `json:"filters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Errorf("decode metrics request: %v", err)
		}
		s.mu.Lock()
		s.lastFilters = body.Filters
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		status, out := s.metricsStatus, s.metricsBody
		s.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		io.WriteString(w, out)
	})
	mux.HandleFunc("/dev/api/snaps/info/", func(w http.ResponseWriter, r *http.Request) {
		if s.rejectStale(w, r) {
			return
		}
		s.mu.Lock()
		s.infoCalls++
		s.lastAuth = r.Header.Get("Authorization")
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch s.infoStatus {
		case 0, http.StatusOK:
			io.WriteString(w, `{"snap_id":"`+testSnapID+`","snap_name":"tide-clock"}`)
		case http.StatusForbidden:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error_list":[{"message":"Permission 'package_access' is required","code":"macaroon-permission-required"}]}`)
		default:
			w.WriteHeader(s.infoStatus)
			io.WriteString(w, `{"error_list":[{"message":"snap not found","code":"resource-not-found"}]}`)
		}
	})
	mux.HandleFunc("/dev/api/account", func(w http.ResponseWriter, r *http.Request) {
		if s.rejectStale(w, r) {
			return
		}
		s.mu.Lock()
		s.accountCalls++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.accountBody == "" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error_list":[{"message":"nope","code":"user-not-ready"}]}`)
			return
		}
		io.WriteString(w, s.accountBody)
	})
	mux.HandleFunc(tokensRefreshPath, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.refreshCalls++
		s.mu.Unlock()
		if s.refreshDelay > 0 {
			time.Sleep(s.refreshDelay)
		}
		if r.Method != http.MethodPost {
			s.t.Errorf("refresh method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			s.t.Errorf("refresh content-type = %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			s.t.Errorf("refresh carried an Authorization header (%q); SSO takes the "+
				"discharge in the body", auth)
		}
		var body struct {
			DischargeMacaroon string `json:"discharge_macaroon"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Errorf("decode refresh request: %v", err)
		}
		s.mu.Lock()
		s.refreshSent = body.DischargeMacaroon
		s.refreshBound = body.DischargeMacaroon != "" && body.DischargeMacaroon != dischargeMac
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if s.refreshWith == "" {
			status := s.refreshStatus
			if status == 0 {
				status = http.StatusUnauthorized
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"code":"invalid-credentials","message":"Invalid credentials"}`)
			return
		}
		io.WriteString(w, `{"discharge_macaroon":"`+s.refreshWith+`"}`)
	})
	srv := httptest.NewServer(mux)
	s.t.Cleanup(srv.Close)
	return srv
}

func fixtureBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPollEmitsThenStopsRepeating(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	events, state1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	// 11 day events plus one baseline apiece for DE, FR and US.
	if len(events) != 14 {
		t.Fatalf("poll 1 emitted %d events, want 14", len(events))
	}
	if store.infoCalls != 1 {
		t.Errorf("snap id lookups = %d, want 1", store.infoCalls)
	}

	// The header must be the bound Ubuntu One credential.
	if store.lastAuth != wantHeader(t) {
		t.Errorf("authorization = %q\nwant %q", store.lastAuth, wantHeader(t))
	}

	// The country filter must reach one day further back than the device
	// filter, or the first day of the window has nothing to difference.
	if len(store.lastFilters) != 2 {
		t.Fatalf("filters = %+v", store.lastFilters)
	}
	var change, country Filter
	for _, f := range store.lastFilters {
		switch f.MetricName {
		case metricDailyDeviceChange:
			change = f
		case metricBaseByCountry:
			country = f
		}
	}
	if change.SnapID != testSnapID || country.SnapID != testSnapID {
		t.Errorf("filters carry snap_id %q / %q", change.SnapID, country.SnapID)
	}
	if country.Start != addDays(change.Start, -1) {
		t.Errorf("country start %q is not one day before change start %q", country.Start, change.Start)
	}
	if change.End != "2026-08-17" {
		t.Errorf("end = %q, want yesterday 2026-08-17", change.End)
	}

	var st state
	if err := json.Unmarshal(state1, &st); err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.SnapIDs["tide-clock"] != testSnapID {
		t.Errorf("snap_ids = %v", st.SnapIDs)
	}
	if got, want := st.Baselined["tide-clock"], map[string]string{
		"DE": "2026-08-14", "US": "2026-08-14", "FR": "2026-08-15",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("baselined = %v, want %v", got, want)
	}
	if st.Cursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursor = %q, want 2026-08-16", st.Cursor["tide-clock"])
	}

	// Second poll over the same data must emit nothing new and must not look
	// the snap id up again.
	events2, state2, err := src.Poll(context.Background(), state1)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if len(events2) != 0 {
		t.Errorf("poll 2 re-emitted %d events", len(events2))
	}
	if store.infoCalls != 1 {
		t.Errorf("snap id was resolved again (%d calls); it should be cached in state", store.infoCalls)
	}
	if store.metricsCalls != 2 {
		t.Errorf("metrics calls = %d, want 2", store.metricsCalls)
	}
	var st2 state
	if err := json.Unmarshal(state2, &st2); err != nil {
		t.Fatalf("state 2: %v", err)
	}
	if st2.Cursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursor moved to %q on a poll that emitted nothing", st2.Cursor["tide-clock"])
	}
}

func TestPollBackfillZeroSeedsCursorWithoutEmitting(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	// `backfill_days: 0` means "emit nothing historical"; config.Default()
	// supplies the 30 when the key is absent, so a literal 0 must survive.
	src := testSource(t, srv.URL, 0)

	events, raw, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("backfill_days 0 emitted %d events", len(events))
	}
	if store.metricsCalls != 0 {
		t.Errorf("metrics fetched %d times for an empty window", store.metricsCalls)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	// Without a persisted floor the window would be recomputed from "now"
	// forever and the source would never emit anything at all.
	if st.Cursor["tide-clock"] != "2026-08-17" {
		t.Errorf("cursor = %q, want yesterday 2026-08-17", st.Cursor["tide-clock"])
	}
}

func TestPollHonoursSince(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)
	src.Since = "2026-08-16"

	if _, _, err := src.Poll(context.Background(), nil); err != nil {
		t.Fatalf("poll: %v", err)
	}
	for _, f := range store.lastFilters {
		if f.MetricName == metricDailyDeviceChange && f.Start != "2026-08-16" {
			t.Errorf("--since 2026-08-16 asked for start %q", f.Start)
		}
	}
}

func TestSnapIDFallsBackToAccountListing(t *testing.T) {
	store := &storeServer{
		t:           t,
		metricsBody: fixtureBody(t),
		infoStatus:  http.StatusForbidden,
		accountBody: `{"snaps":{"16":{"tide-clock":{"snap-id":"` + testSnapID + `","status":"Approved"}}}}`,
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if store.accountCalls != 1 {
		t.Errorf("account listing called %d times", store.accountCalls)
	}
	if len(events) == 0 {
		t.Errorf("no events after falling back to the account listing")
	}
}

func TestCheckOK(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	if err := src.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if store.infoCalls != 1 || store.metricsCalls != 1 {
		t.Errorf("check made %d info and %d metrics calls", store.infoCalls, store.metricsCalls)
	}
}

func TestCheckErrorMapping(t *testing.T) {
	cases := []struct {
		name        string
		infoStatus  int
		metricsCode int
		metricsBody string
		want        []string
	}{
		{
			name:        "expired macaroon",
			metricsCode: http.StatusUnauthorized,
			metricsBody: `{"error_list":[{"message":"Macaroon has expired","code":"macaroon-needs-refresh"}]}`,
			want:        []string{"expired", "snapcraft export-login"},
		},
		{
			name:        "missing package_metrics acl",
			metricsCode: http.StatusForbidden,
			metricsBody: `{"error_list":[{"message":"Permission 'package_metrics' is required","code":"macaroon-permission-required"}]}`,
			want:        []string{"package_metrics", "snapcraft export-login"},
		},
		{
			name:        "login restricted to other snaps",
			metricsCode: http.StatusForbidden,
			metricsBody: `{"error_list":[{"message":"Not authorized to access snap_ids: abc","code":"macaroon-permission-required"}]}`,
			want:        []string{"restricted to other snaps", "--snaps"},
		},
		{
			name:       "unknown snap",
			infoStatus: http.StatusNotFound,
			want:       []string{`unknown snap "tide-clock"`, "snapcraft list"},
		},
		{
			name:        "store outage",
			metricsCode: http.StatusBadGateway,
			metricsBody: `<html>bad gateway</html>`,
			want:        []string{"Snap Store is having trouble"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &storeServer{
				t:             t,
				metricsBody:   tc.metricsBody,
				metricsStatus: tc.metricsCode,
				infoStatus:    tc.infoStatus,
			}
			srv := store.start()
			src := testSource(t, srv.URL, 30)

			err := src.Check(context.Background())
			if err == nil {
				t.Fatal("check succeeded, want an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestCheckMissingLoginFile(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)
	src.cfg.LoginPath = filepath.Join(t.TempDir(), "gone")

	err := src.Check(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "export-login") {
		t.Errorf("error = %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(config.Snapcraft{LoginPath: "x"}, quietLogger()); err == nil {
		t.Error("New with no snaps succeeded")
	}
	if _, err := New(config.Snapcraft{Snaps: []string{"a"}}, quietLogger()); err == nil {
		t.Error("New with no login_path succeeded")
	}
	if _, err := New(config.Snapcraft{Snaps: []string{"a"}, LoginPath: "/nope/nothing"}, quietLogger()); err == nil {
		t.Error("New with a missing login file succeeded")
	}
}

func TestPollIntervalAndName(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	src := testSource(t, store.start().URL, 30)
	if src.Name() != "snapcraft" {
		t.Errorf("name = %q", src.Name())
	}
	if src.PollInterval() != 6*time.Hour {
		t.Errorf("poll interval = %s", src.PollInterval())
	}
}

func TestDecodeStateTolerablesGarbage(t *testing.T) {
	st := decodeState([]byte("not json"))
	if st.SnapIDs == nil || st.Cursor == nil {
		t.Fatal("decodeState returned nil maps")
	}
	st = decodeState([]byte(`{"snap_ids":{"a":"b"}}`))
	if st.SnapIDs["a"] != "b" || st.Cursor == nil {
		t.Errorf("state = %+v", st)
	}
}

// ------------------------------------------------------- per-country baselines

// changeMetric builds a daily_device_change metric whose figures are plausible
// but beside the point: these tests are about the country series.
func changeMetric(buckets []string, newDev ...int) Metric {
	if len(newDev) != len(buckets) {
		panic("changeMetric: one new-device figure per bucket")
	}
	var nv, cv, lv []*float64
	for i := range buckets {
		nv = append(nv, f(float64(newDev[i])))
		cv = append(cv, f(float64(100+i)))
		lv = append(lv, f(0))
	}
	return Metric{
		Status:     "OK",
		SnapID:     testSnapID,
		MetricName: metricDailyDeviceChange,
		Buckets:    buckets,
		Series: []Series{
			{Name: seriesNew, Values: nv},
			{Name: seriesContinued, Values: cv},
			{Name: seriesLost, Values: lv},
		},
	}
}

// countryMetric builds an installed_base_by_country metric. Each series' values
// line up with buckets; a nil is the store's "no reading for that day".
func countryMetric(buckets []string, series map[string][]*float64) Metric {
	names := make([]string, 0, len(series))
	for cc := range series {
		names = append(names, cc)
	}
	sort.Strings(names)
	m := Metric{
		Status:     "OK",
		SnapID:     testSnapID,
		MetricName: metricBaseByCountry,
		Buckets:    buckets,
	}
	for _, cc := range names {
		if len(series[cc]) != len(buckets) {
			panic("countryMetric: " + cc + " does not line up with the buckets")
		}
		m.Series = append(m.Series, Series{Name: cc, Values: series[cc]})
	}
	return m
}

func metricsBody(t *testing.T, metrics ...Metric) string {
	t.Helper()
	b, err := json.Marshal(MetricsResponse{Metrics: metrics})
	if err != nil {
		t.Fatalf("encode metrics: %v", err)
	}
	return string(b)
}

// baselineEvents indexes the emitted baselines by country.
func baselineEvents(t *testing.T, events []core.Event) map[string]core.Event {
	t.Helper()
	out := map[string]core.Event{}
	for _, e := range events {
		if !strings.HasPrefix(e.DedupeKey, "snapcraft:baseline:") {
			continue
		}
		if prev, dup := out[e.Country]; dup {
			t.Errorf("duplicate baseline for %s in one batch: %q and %q",
				e.Country, prev.DedupeKey, e.DedupeKey)
		}
		out[e.Country] = e
	}
	return out
}

// deltaEvents indexes the derived per-country installs by "<country>@<day>".
func deltaEvents(events []core.Event) map[string]core.Event {
	out := map[string]core.Event{}
	for _, e := range events {
		if e.Country == "" || strings.HasPrefix(e.DedupeKey, "snapcraft:baseline:") {
			continue
		}
		out[e.Country+"@"+e.Day] = e
	}
	return out
}

func onDay(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 9, 0, 0, 0, time.UTC) }

// The first poll must found every country the snap is already installed in,
// with the standing base as the quantity — that is the whole fix. The poll
// after it must not say it again.
func TestBaselineFoundsEveryCountryOnTheFirstPoll(t *testing.T) {
	store := &storeServer{t: t, metricsBody: metricsBody(t,
		changeMetric([]string{"2026-08-16", "2026-08-17"}, 1, 1),
		countryMetric([]string{"2026-08-15", "2026-08-16", "2026-08-17"}, map[string][]*float64{
			"AU": {f(40), f(40), f(40)},
			"GR": {f(5), f(5), f(5)},
		}),
	)}
	src := testSource(t, store.start().URL, 30)

	events, st1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll 1: %v", err)
	}

	base := baselineEvents(t, events)
	if len(base) != 2 {
		t.Fatalf("poll 1 founded %d countries, want AU and GR: %+v", len(base), base)
	}
	for _, want := range []struct {
		cc  string
		qty int
	}{{"AU", 40}, {"GR", 5}} {
		e, ok := base[want.cc]
		if !ok {
			t.Errorf("%s was never founded", want.cc)
			continue
		}
		if e.Kind != "install" || e.Quantity != want.qty {
			t.Errorf("%s baseline = kind %q qty %d, want install %d", want.cc, e.Kind, e.Quantity, want.qty)
		}
		if e.DedupeKey != "snapcraft:baseline:tide-clock:"+want.cc {
			t.Errorf("%s dedupe key = %q", want.cc, e.DedupeKey)
		}
		// The oldest contiguous bucket: the day the deltas will count from.
		if e.Day != "2026-08-15" || e.OccurredAt.Format(core.DayLayout) != "2026-08-15" {
			t.Errorf("%s baseline dated %q / %s, want 2026-08-15", want.cc, e.Day, e.OccurredAt)
		}
		if !e.Silent || e.IsLedger || e.Chest {
			t.Errorf("%s baseline = silent %v ledger %v chest %v, want silent only",
				want.cc, e.Silent, e.IsLedger, e.Chest)
		}
		var p baselinePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("%s payload: %v", want.cc, err)
		}
		if !p.Baseline || p.Base != want.qty || p.AsOf != "2026-08-15" || p.Country != want.cc {
			t.Errorf("%s payload = %+v", want.cc, p)
		}
	}
	if got := deltaEvents(events); len(got) != 0 {
		t.Errorf("a flat first window still produced deltas: %v", got)
	}

	// One day later: AU gains a device, GR is flat.
	src.Now = func() time.Time { return onDay(2026, 8, 19) }
	store.serve(metricsBody(t,
		changeMetric([]string{"2026-08-18"}, 1),
		countryMetric([]string{"2026-08-17", "2026-08-18"}, map[string][]*float64{
			"AU": {f(40), f(41)},
			"GR": {f(5), f(5)},
		}),
	))

	events2, st2, err := src.Poll(context.Background(), st1)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if got := baselineEvents(t, events2); len(got) != 0 {
		t.Fatalf("poll 2 re-baselined founded countries: %+v", got)
	}
	deltas := deltaEvents(events2)
	if len(deltas) != 1 {
		t.Fatalf("poll 2 country events = %+v, want exactly AU +1", deltas)
	}
	au, ok := deltas["AU@2026-08-18"]
	if !ok || au.Quantity != 1 {
		t.Fatalf("poll 2 AU delta = %+v, want +1 on 2026-08-18", au)
	}

	var decoded state
	if err := json.Unmarshal(st2, &decoded); err != nil {
		t.Fatalf("state: %v", err)
	}
	if got, want := decoded.Baselined["tide-clock"], map[string]string{
		"AU": "2026-08-15", "GR": "2026-08-15",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("baselined = %v, want %v", got, want)
	}
}

// A country that shows up mid-life is founded on the day it appears, and that
// same day must not also be differenced: base[day0] − 0 is the baseline again.
func TestBaselineForACountryThatAppearsLater(t *testing.T) {
	store := &storeServer{t: t, metricsBody: metricsBody(t,
		changeMetric([]string{"2026-08-16"}, 1),
		countryMetric([]string{"2026-08-15", "2026-08-16"}, map[string][]*float64{
			"AU": {f(40), f(40)},
		}),
	)}
	src := testSource(t, store.start().URL, 30)

	_, st1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll 1: %v", err)
	}

	// Brazil appears on the 17th with 3 devices and reaches 4 on the 18th.
	src.Now = func() time.Time { return onDay(2026, 8, 19) }
	store.serve(metricsBody(t,
		changeMetric([]string{"2026-08-17", "2026-08-18"}, 3, 1),
		countryMetric([]string{"2026-08-16", "2026-08-17", "2026-08-18"}, map[string][]*float64{
			"AU": {f(40), f(40), f(40)},
			"BR": {nil, f(3), f(4)},
		}),
	))

	events, st2, err := src.Poll(context.Background(), st1)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}

	base := baselineEvents(t, events)
	if len(base) != 1 {
		t.Fatalf("founded %d countries, want only BR: %+v", len(base), base)
	}
	br := base["BR"]
	if br.Quantity != 3 || br.Day != "2026-08-17" {
		t.Errorf("BR baseline = qty %d on %q, want 3 on 2026-08-17", br.Quantity, br.Day)
	}

	deltas := deltaEvents(events)
	if _, dup := deltas["BR@2026-08-17"]; dup {
		t.Errorf("BR was counted twice on its founding day: baseline 3 plus %+v", deltas["BR@2026-08-17"])
	}
	next, ok := deltas["BR@2026-08-18"]
	if !ok || next.Quantity != 1 {
		t.Fatalf("BR delta the day after founding = %+v, want +1", next)
	}
	if len(deltas) != 1 {
		t.Errorf("country deltas = %+v, want only BR +1", deltas)
	}

	var decoded state
	if err := json.Unmarshal(st2, &decoded); err != nil {
		t.Fatalf("state: %v", err)
	}
	if decoded.Baselined["tide-clock"]["BR"] != "2026-08-17" {
		t.Errorf("baselined = %v", decoded.Baselined["tide-clock"])
	}
}

// A gap in the store's buckets must not re-found a country that already has a
// settlement — the drift that costs is accepted and documented — but a country
// first seen after the gap is new and must still be founded.
func TestBaselineAcrossADataGap(t *testing.T) {
	store := &storeServer{t: t, metricsBody: metricsBody(t,
		changeMetric([]string{"2026-08-16"}, 1),
		countryMetric([]string{"2026-08-15", "2026-08-16"}, map[string][]*float64{
			"AU": {f(40), f(40)},
		}),
	)}
	src := testSource(t, store.start().URL, 30)

	first, st1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if got := baselineEvents(t, first)["AU"]; got.Quantity != 40 {
		t.Fatalf("AU was not founded on poll 1: %+v", got)
	}

	// Five days later, with the 18th through the 21st missing entirely.
	src.Now = func() time.Time { return onDay(2026, 8, 24) }
	store.serve(metricsBody(t,
		changeMetric([]string{"2026-08-22", "2026-08-23"}, 2, 1),
		countryMetric([]string{"2026-08-17", "2026-08-22", "2026-08-23"}, map[string][]*float64{
			"AU": {f(40), f(60), f(61)},
			"CA": {nil, f(7), f(8)},
		}),
	))

	events, _, err := src.Poll(context.Background(), st1)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}

	base := baselineEvents(t, events)
	if _, again := base["AU"]; again {
		t.Errorf("AU was re-baselined after a gap: %+v", base["AU"])
	}
	ca, ok := base["CA"]
	if !ok {
		t.Fatalf("CA appeared after the gap and was never founded: %+v", base)
	}
	// The oldest bucket of the contiguous run, not the stale 08-17 reading.
	if ca.Quantity != 7 || ca.Day != "2026-08-22" {
		t.Errorf("CA baseline = qty %d on %q, want 7 on 2026-08-22", ca.Quantity, ca.Day)
	}

	deltas := deltaEvents(events)
	// 2026-08-22's predecessor bucket is 2026-08-17, so nothing is differenced
	// across the gap at all.
	for key := range deltas {
		if strings.HasSuffix(key, "@2026-08-22") {
			t.Errorf("differenced across the gap: %s = %+v", key, deltas[key])
		}
	}
	if got := deltas["AU@2026-08-23"]; got.Quantity != 1 {
		t.Errorf("AU delta after the gap = %+v, want +1", got)
	}
	if got := deltas["CA@2026-08-23"]; got.Quantity != 1 {
		t.Errorf("CA delta the day after founding = %+v, want +1", got)
	}
}

// The point of dating the baseline on the oldest contiguous bucket: everything
// Loot attributes to a country adds up to the base the store reports, exactly.
func TestBaselinePlusDeltasEqualTheReportedBase(t *testing.T) {
	store := &storeServer{t: t, metricsBody: metricsBody(t,
		changeMetric([]string{"2026-08-15", "2026-08-16"}, 2, 1),
		countryMetric([]string{"2026-08-14", "2026-08-15", "2026-08-16"}, map[string][]*float64{
			"AU": {f(40), f(42), f(43)},
		}),
	)}
	src := testSource(t, store.start().URL, 30)

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	if got := baselineEvents(t, events)["AU"]; got.Quantity != 40 || got.Day != "2026-08-14" {
		t.Fatalf("AU baseline = qty %d on %q, want 40 on 2026-08-14", got.Quantity, got.Day)
	}
	deltas := deltaEvents(events)
	if got := deltas["AU@2026-08-15"]; got.Quantity != 2 {
		t.Errorf("AU 2026-08-15 = %+v, want +2", got)
	}
	if got := deltas["AU@2026-08-16"]; got.Quantity != 1 {
		t.Errorf("AU 2026-08-16 = %+v, want +1", got)
	}

	attributed := 0
	for _, e := range events {
		if e.Country == "AU" {
			attributed += e.Quantity
		}
	}
	// baseline(2026-08-14) + Σ deltas(08-15, 08-16) == base[2026-08-16].
	if attributed != 43 {
		t.Errorf("attributed %d devices to AU, want 43 — the base the store reports "+
			"on the last bucket (40 + 2 + 1)", attributed)
	}
}
