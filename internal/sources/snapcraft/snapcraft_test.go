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
	weekly, ok := resp.Metric(metricWeeklyBaseByCountry)
	if !ok {
		t.Fatal("weekly_installed_base_by_country missing")
	}
	if got := len(weekly.Buckets); got != 4 {
		t.Errorf("weekly buckets = %d, want 4", got)
	}
	// FR has no reading on the first day although the day is published.
	if _, ok := weekly.at("FR", 0); ok {
		t.Errorf("FR[0] is null in the fixture but read as present")
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

func f(v float64) *float64 { return &v }

func TestEventsFromMetrics(t *testing.T) {
	resp := loadFixture(t)
	events, newest := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday)

	if newest != "2026-08-16" {
		t.Errorf("newest = %q, want 2026-08-16 (08-17 is all null and must not move the cursor)", newest)
	}

	byKey := map[string]core.Event{}
	for _, e := range events {
		if _, dup := byKey[e.DedupeKey]; dup {
			t.Errorf("duplicate dedupe key %q", e.DedupeKey)
		}
		byKey[e.DedupeKey] = e
		if e.Source != Name || e.App != "tide-clock" {
			t.Errorf("%s: source = %q app = %q", e.DedupeKey, e.Source, e.App)
		}
		if e.IsLedger {
			t.Errorf("%s is marked ledger; device counts are not money", e.DedupeKey)
		}
		if !e.ObservedAt.Equal(fixtureToday) {
			t.Errorf("%s observed_at = %s", e.DedupeKey, e.ObservedAt)
		}
		if strings.HasPrefix(e.Day, "2026-08-17") {
			t.Errorf("emitted the unpublished day: %s", e.DedupeKey)
		}

		// `new` is churn between two consecutive days, not arrivals. Nothing
		// may come out under a kind that something downstream sums into a
		// population, and nothing here is news: no drop, no chest.
		if e.Kind == "install" || e.Kind == "installs_day" {
			t.Errorf("%s has kind %q; daily_device_change must never be recorded as installs",
				e.DedupeKey, e.Kind)
		}
		if !e.Silent || e.Chest {
			t.Errorf("%s = silent %v chest %v, want silent and not chest-bound",
				e.DedupeKey, e.Silent, e.Chest)
		}
		// Where the devices are is AudienceEvents' business.
		if e.Country != "" || e.Kind == core.KindAudience {
			t.Errorf("%s = kind %q country %q; the device counters carry no country",
				e.DedupeKey, e.Kind, e.Country)
		}
	}

	// The `installs` in the first key is deliberate: it predates the
	// device_new kind, and keeping it stops an old database growing a second
	// copy of every day it already holds.
	want := map[string]struct {
		kind string
		qty  int
		day  string
	}{
		"snapcraft:installs:tide-clock:2026-08-15": {kindDeviceNew, 12, "2026-08-15"},
		"snapcraft:active:tide-clock:2026-08-15":   {"active_devices", 312, "2026-08-15"},
		"snapcraft:lost:tide-clock:2026-08-15":     {"lost", 5, "2026-08-15"},
		"snapcraft:installs:tide-clock:2026-08-16": {kindDeviceNew, 20, "2026-08-16"},
		"snapcraft:active:tide-clock:2026-08-16":   {"active_devices", 330, "2026-08-16"},
		"snapcraft:lost:tide-clock:2026-08-16":     {"lost", 3, "2026-08-16"},
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
		if e.Kind != w.kind || e.Quantity != w.qty || e.Day != w.day {
			t.Errorf("%s = kind %q qty %d day %q", key, e.Kind, e.Quantity, e.Day)
		}
		if e.OccurredAt.Format(core.DayLayout) != w.day {
			t.Errorf("%s occurred_at = %s", key, e.OccurredAt)
		}
	}
	if kindDeviceNew != "device_new" {
		t.Errorf("kindDeviceNew = %q; the stored kind is part of the database's vocabulary", kindDeviceNew)
	}

	// Every event of a day carries the whole day, so any one row explains
	// the other two.
	var p dayPayload
	if err := json.Unmarshal(byKey["snapcraft:active:tide-clock:2026-08-16"].Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if (p != dayPayload{Snap: "tide-clock", SnapID: testSnapID, Date: "2026-08-16",
		New: 20, Continued: 310, Lost: 3, InstalledBase: 330}) {
		t.Errorf("day payload = %+v", p)
	}
}

// A response without daily_device_change (the store answers NO_DATA for a
// brand new snap) is nothing to emit and no reason to move the cursor.
func TestEventsFromMetricsWithoutTheMetric(t *testing.T) {
	events, newest := EventsFromMetrics("tide-clock", testSnapID, MetricsResponse{},
		"2026-08-14", "2026-08-18", fixtureToday)
	if len(events) != 0 || newest != "" {
		t.Errorf("got %d events and cursor %q from an empty response", len(events), newest)
	}
}

func TestEventsFromMetricsRespectsFloor(t *testing.T) {
	resp := loadFixture(t)
	events, newest := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-15", "2026-08-18", fixtureToday)
	if newest != "2026-08-16" {
		t.Errorf("newest = %q", newest)
	}
	if len(events) != 3 {
		t.Errorf("got %d events, want the three of 2026-08-16", len(events))
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

	events, newest := EventsFromMetrics("tide-clock", testSnapID, resp,
		"2026-08-11", "2026-08-16", fixtureToday)
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

// ------------------------------------------------------------ audience events

// weeklyMetric builds a weekly_installed_base_by_country metric. Series are
// given as name, values pairs so a test controls their order; a nil value is
// the store's "nobody there" (or, when the whole day is nil, "not published").
func weeklyMetric(buckets []string, series ...Series) Metric {
	for _, s := range series {
		if len(s.Values) != len(buckets) {
			panic("weeklyMetric: " + s.Name + " does not line up with the buckets")
		}
	}
	return Metric{
		Status:     "OK",
		SnapID:     testSnapID,
		MetricName: metricWeeklyBaseByCountry,
		Buckets:    buckets,
		Series:     series,
	}
}

func audiencePayloadOf(t *testing.T, e core.Event) audiencePayload {
	t.Helper()
	var p audiencePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", e.DedupeKey, err)
	}
	return p
}

func TestAudienceEvents(t *testing.T) {
	resp := loadFixture(t)
	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday)

	if newest != "2026-08-16" {
		t.Errorf("newest = %q, want 2026-08-16 (08-17 is all null and must not move the cursor)", newest)
	}

	byKey := map[string]core.Event{}
	for _, e := range events {
		if _, dup := byKey[e.DedupeKey]; dup {
			t.Errorf("duplicate dedupe key %q", e.DedupeKey)
		}
		byKey[e.DedupeKey] = e
		if e.Source != Name || e.App != "tide-clock" || e.Kind != core.KindAudience {
			t.Errorf("%s: source %q app %q kind %q", e.DedupeKey, e.Source, e.App, e.Kind)
		}
		// A snapshot is not news: the pipeline decides what a new country or
		// a round total is worth, not the source.
		if !e.Silent || e.Chest || e.IsLedger {
			t.Errorf("%s = silent %v chest %v ledger %v, want silent only",
				e.DedupeKey, e.Silent, e.Chest, e.IsLedger)
		}
		if e.OccurredAt.Format(core.DayLayout) != e.Day || !e.ObservedAt.Equal(fixtureToday) {
			t.Errorf("%s: day %q occurred %s observed %s", e.DedupeKey, e.Day, e.OccurredAt, e.ObservedAt)
		}
		// "??" is how the store names devices it could not place. They are
		// counted, but an invented country must never reach the map.
		if e.Country != "" && !validCountry(e.Country) {
			t.Errorf("%s was emitted for the non-ISO series %q", e.DedupeKey, e.Country)
		}
		p := audiencePayloadOf(t, e)
		if p.Snap != "tide-clock" || p.SnapID != testSnapID || p.Date != e.Day ||
			p.Basis != string(core.BasisWeeklyDevices) || p.Country != e.Country {
			t.Errorf("%s payload = %+v", e.DedupeKey, p)
		}
		// Placed and unplaced describe the total; on a country row they would
		// be a second, contradictory quantity.
		if e.Country != "" && (p.Placed != 0 || p.Unplaced != 0) {
			t.Errorf("%s is a country row carrying placed %d unplaced %d", e.DedupeKey, p.Placed, p.Unplaced)
		}
	}

	want := map[string]struct {
		country string
		qty     int
		day     string
	}{
		"snapcraft:audience:tide-clock:2026-08-15:DE": {"DE", 47, "2026-08-15"},
		"snapcraft:audience:tide-clock:2026-08-15:FR": {"FR", 5, "2026-08-15"},
		"snapcraft:audience:tide-clock:2026-08-15:US": {"US", 110, "2026-08-15"},
		// 47 + 5 + 110 placed, plus the 4 under "??".
		"snapcraft:audience:tide-clock:2026-08-15":    {"", 166, "2026-08-15"},
		"snapcraft:audience:tide-clock:2026-08-16:DE": {"DE", 47, "2026-08-16"},
		"snapcraft:audience:tide-clock:2026-08-16:FR": {"FR", 9, "2026-08-16"},
		"snapcraft:audience:tide-clock:2026-08-16:US": {"US", 118, "2026-08-16"},
		"snapcraft:audience:tide-clock:2026-08-16":    {"", 179, "2026-08-16"},
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
		if e.Country != w.country || e.Quantity != w.qty || e.Day != w.day {
			t.Errorf("%s = country %q qty %d day %q", key, e.Country, e.Quantity, e.Day)
		}
	}

	for day, w := range map[string]struct{ placed, unplaced int }{
		"2026-08-15": {162, 4},
		"2026-08-16": {174, 5},
	} {
		p := audiencePayloadOf(t, byKey["snapcraft:audience:tide-clock:"+day])
		if p.Placed != w.placed || p.Unplaced != w.unplaced {
			t.Errorf("%s total: placed %d unplaced %d, want %d and %d",
				day, p.Placed, p.Unplaced, w.placed, w.unplaced)
		}
	}

	// The total goes last within its day, after the countries in a fixed
	// order: the pipeline looks at the total with its parts already stored,
	// and a poll is reproducible rather than in Go's map order.
	var order []string
	for _, e := range events {
		order = append(order, e.Day+":"+e.Country)
	}
	wantOrder := []string{
		"2026-08-15:DE", "2026-08-15:FR", "2026-08-15:US", "2026-08-15:",
		"2026-08-16:DE", "2026-08-16:FR", "2026-08-16:US", "2026-08-16:",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order = %v\nwant    %v", order, wantOrder)
	}
}

// The floor is exclusive and so is today: a day at or before the cursor has
// been stored already, and today's week is still filling up.
func TestAudienceEventsRespectsFloorAndToday(t *testing.T) {
	resp := loadFixture(t)

	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-15", "2026-08-18", fixtureToday)
	if newest != "2026-08-16" {
		t.Errorf("newest = %q, want 2026-08-16", newest)
	}
	if len(events) != 4 {
		t.Errorf("got %d events, want the four of 2026-08-16", len(events))
	}
	for _, e := range events {
		if e.Day != "2026-08-16" {
			t.Errorf("floor 2026-08-15 let through %s", e.DedupeKey)
		}
	}

	// With today on the 16th, the 16th itself is not a completed day even
	// though the store has figures for it.
	events, newest = AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-13", "2026-08-16", fixtureToday)
	if newest != "2026-08-15" {
		t.Errorf("newest = %q, want 2026-08-15: today must not be read", newest)
	}
	days := map[string]int{}
	for _, e := range events {
		days[e.Day]++
	}
	// The 14th has no FR reading, so it is two countries and a total.
	if !reflect.DeepEqual(days, map[string]int{"2026-08-14": 3, "2026-08-15": 4}) {
		t.Errorf("events per day = %v, want 3 on the 14th and 4 on the 15th", days)
	}

	// An empty floor means "no cursor yet", not "before everything is
	// excluded": every completed day is read.
	events, _ = AudienceEvents("tide-clock", testSnapID, resp, "", "2026-08-18", fixtureToday)
	if len(events) != 11 {
		t.Errorf("no floor: got %d events, want 11", len(events))
	}
}

// Devices the store could not place are real, they are simply nowhere. They
// belong in the total — and in `unplaced`, which is what the Hearth draws at
// sea — and in no country, whatever the store chose to call their series.
func TestAudienceEventsCountsUnplacedSeriesInTheTotalOnly(t *testing.T) {
	resp := MetricsResponse{Metrics: []Metric{weeklyMetric(
		[]string{"2026-08-15"},
		Series{Name: "??", Values: []*float64{f(7)}},
		Series{Name: "_INTERNAL", Values: []*float64{f(2)}},
		Series{Name: "GBR", Values: []*float64{f(1)}},
		// Lower case is still a country; it is stored the way every other
		// source spells it.
		Series{Name: "nz", Values: []*float64{f(30)}},
		Series{Name: "AU", Values: []*float64{f(60)}},
	)}}

	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday)
	if newest != "2026-08-15" {
		t.Errorf("newest = %q", newest)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want AU, NZ and the total: %+v", len(events), events)
	}
	if e := events[0]; e.Country != "AU" || e.Quantity != 60 ||
		e.DedupeKey != "snapcraft:audience:tide-clock:2026-08-15:AU" {
		t.Errorf("first event = %q %d %q", e.Country, e.Quantity, e.DedupeKey)
	}
	if e := events[1]; e.Country != "NZ" || e.Quantity != 30 ||
		e.DedupeKey != "snapcraft:audience:tide-clock:2026-08-15:NZ" {
		t.Errorf("second event = %q %d %q", e.Country, e.Quantity, e.DedupeKey)
	}
	total := events[2]
	if total.Country != "" || total.Quantity != 100 ||
		total.DedupeKey != "snapcraft:audience:tide-clock:2026-08-15" {
		t.Errorf("total = country %q qty %d key %q, want 100 with no country",
			total.Country, total.Quantity, total.DedupeKey)
	}
	if p := audiencePayloadOf(t, total); p.Placed != 90 || p.Unplaced != 10 {
		t.Errorf("total payload: placed %d unplaced %d, want 90 and 10", p.Placed, p.Unplaced)
	}
}

// A snap nobody could be placed for still has an audience: the total is
// emitted on its own, all of it at sea.
func TestAudienceEventsWithNothingPlaced(t *testing.T) {
	resp := MetricsResponse{Metrics: []Metric{weeklyMetric(
		[]string{"2026-08-15"},
		Series{Name: "??", Values: []*float64{f(4)}},
	)}}
	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday)
	if newest != "2026-08-15" || len(events) != 1 {
		t.Fatalf("cursor %q, %d events; want 2026-08-15 and the total alone", newest, len(events))
	}
	if e := events[0]; e.Country != "" || e.Quantity != 4 {
		t.Errorf("total = country %q qty %d", e.Country, e.Quantity)
	}
	if p := audiencePayloadOf(t, events[0]); p.Placed != 0 || p.Unplaced != 4 {
		t.Errorf("payload: placed %d unplaced %d, want 0 and 4", p.Placed, p.Unplaced)
	}
}

// A day on which every series is null has not been published. It must emit
// nothing, and the cursor must stop before it — the same contiguous-run rule
// as EventsFromMetrics, for the same reason: move past the hole and that day's
// snapshot is never read once it lands. The day after the hole is still
// emitted, because its dedupe keys make re-reading it free.
func TestAudienceEventsCursorStopsAtAnUnpublishedDay(t *testing.T) {
	resp := MetricsResponse{Metrics: []Metric{weeklyMetric(
		[]string{"2026-08-12", "2026-08-13", "2026-08-14", "2026-08-15"},
		Series{Name: "US", Values: []*float64{f(100), f(101), nil, f(103)}},
		Series{Name: "DE", Values: []*float64{f(50), f(51), nil, f(53)}},
		Series{Name: "??", Values: []*float64{f(2), f(2), nil, f(2)}},
	)}}

	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-11", "2026-08-16", fixtureToday)
	if newest != "2026-08-13" {
		t.Fatalf("cursor = %q, want 2026-08-13: the 14th was never published", newest)
	}

	days := map[string]int{}
	for _, e := range events {
		days[e.Day]++
	}
	if days["2026-08-14"] != 0 {
		t.Error("an unpublished day produced events")
	}
	if days["2026-08-15"] != 3 {
		t.Errorf("the day after the hole produced %d events, want 3; only the cursor should wait",
			days["2026-08-15"])
	}

	// A window that opens on the hole has no published run to advance over.
	_, newest = AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-13", "2026-08-16", fixtureToday)
	if newest != "" {
		t.Errorf("cursor = %q for a window that starts unpublished, want none", newest)
	}
}

// The store writes null, not zero, for a country with nobody in it. On a day
// that other series do have figures for, that is an absence in one country and
// not a hole in the data: the country is skipped, the day counts as published
// and the cursor moves.
func TestAudienceEventsSkipsANullCountryOnAPublishedDay(t *testing.T) {
	resp := MetricsResponse{Metrics: []Metric{weeklyMetric(
		[]string{"2026-08-14", "2026-08-15"},
		Series{Name: "US", Values: []*float64{f(100), f(101)}},
		Series{Name: "FR", Values: []*float64{f(5), nil}},
		// A genuine zero is nobody too, and is not a row either.
		Series{Name: "DE", Values: []*float64{f(0), f(3)}},
	)}}

	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-13", "2026-08-16", fixtureToday)
	if newest != "2026-08-15" {
		t.Errorf("cursor = %q, want 2026-08-15: one null country does not unpublish a day", newest)
	}

	got := map[string]int{}
	for _, e := range events {
		got[e.Day+":"+e.Country] = e.Quantity
	}
	want := map[string]int{
		"2026-08-14:US": 100, "2026-08-14:FR": 5, "2026-08-14:": 105,
		"2026-08-15:US": 101, "2026-08-15:DE": 3, "2026-08-15:": 104,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v\nwant     %v", got, want)
	}
}

// A day the store published as all zeros is published — the cursor moves —
// but an audience of nobody is not a snapshot worth a row.
func TestAudienceEventsZeroDayAdvancesWithoutEmitting(t *testing.T) {
	resp := MetricsResponse{Metrics: []Metric{weeklyMetric(
		[]string{"2026-08-15"},
		Series{Name: "US", Values: []*float64{f(0)}},
	)}}
	events, newest := AudienceEvents("tide-clock", testSnapID, resp,
		"2026-08-14", "2026-08-18", fixtureToday)
	if len(events) != 0 || newest != "2026-08-15" {
		t.Errorf("got %d events and cursor %q, want none and 2026-08-15", len(events), newest)
	}
}

// NO_DATA is what a brand new snap answers; so is a response that simply does
// not carry the metric.
func TestAudienceEventsWithoutTheMetric(t *testing.T) {
	for name, resp := range map[string]MetricsResponse{
		"absent": {},
		"no data": {Metrics: []Metric{{
			Status: "NO_DATA", MetricName: metricWeeklyBaseByCountry,
		}}},
	} {
		events, newest := AudienceEvents("tide-clock", testSnapID, resp,
			"2026-08-14", "2026-08-18", fixtureToday)
		if len(events) != 0 || newest != "" {
			t.Errorf("%s: got %d events and cursor %q", name, len(events), newest)
		}
	}
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

// filtersByMetric indexes the last metrics request by metric name, and fails
// on anything but the two metrics Loot reads: every extra filter is a series
// downloaded every six hours for nothing.
func (s *storeServer) filtersByMetric(t *testing.T) (change, audience Filter) {
	t.Helper()
	s.mu.Lock()
	filters := append([]Filter(nil), s.lastFilters...)
	s.mu.Unlock()
	if len(filters) != 2 {
		t.Fatalf("filters = %+v, want exactly two", filters)
	}
	var haveChange, haveAudience bool
	for _, f := range filters {
		if f.SnapID != testSnapID {
			t.Errorf("filter %s carries snap_id %q", f.MetricName, f.SnapID)
		}
		switch f.MetricName {
		case metricDailyDeviceChange:
			change, haveChange = f, true
		case metricWeeklyBaseByCountry:
			audience, haveAudience = f, true
		default:
			// Notably the daily installed_base_by_country, which Loot used to
			// difference into "installs".
			t.Errorf("unexpected metric requested: %q", f.MetricName)
		}
	}
	if !haveChange || !haveAudience {
		t.Fatalf("filters = %+v, want one %s and one %s",
			filters, metricDailyDeviceChange, metricWeeklyBaseByCountry)
	}
	return change, audience
}

// countKinds splits a poll's events into the device counters and the audience
// snapshots.
func countKinds(events []core.Event) (device, audience int) {
	for _, e := range events {
		if e.Kind == core.KindAudience {
			audience++
		} else {
			device++
		}
	}
	return device, audience
}

func TestPollEmitsThenStopsRepeating(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	events, state1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	// Three device counters for each of the 15th and 16th; audience snapshots
	// for the 14th (two countries and a total) and the 15th and 16th (three
	// and a total).
	if device, audience := countKinds(events); device != 6 || audience != 11 {
		t.Fatalf("poll 1 emitted %d device and %d audience events, want 6 and 11", device, audience)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.DedupeKey] {
			t.Errorf("duplicate dedupe key %q", e.DedupeKey)
		}
		seen[e.DedupeKey] = true
		if !e.Silent || e.Chest || e.Kind == "install" || e.Kind == "installs_day" {
			t.Errorf("%s = kind %q silent %v chest %v; a poll produces no drop and no install",
				e.DedupeKey, e.Kind, e.Silent, e.Chest)
		}
	}
	if store.infoCalls != 1 {
		t.Errorf("snap id lookups = %d, want 1", store.infoCalls)
	}

	// The header must be the bound Ubuntu One credential.
	if store.lastAuth != wantHeader(t) {
		t.Errorf("authorization = %q\nwant %q", store.lastAuth, wantHeader(t))
	}

	// On a first run both cursors seed from the same backfill floor, so both
	// ranges open the day after it: 30 days of history before yesterday.
	change, audience := store.filtersByMetric(t)
	if change.Start != "2026-07-19" || audience.Start != "2026-07-19" {
		t.Errorf("starts = %q / %q, want 2026-07-19 for both", change.Start, audience.Start)
	}
	if change.End != "2026-08-17" || audience.End != "2026-08-17" {
		t.Errorf("ends = %q / %q, want yesterday 2026-08-17", change.End, audience.End)
	}

	var st state
	if err := json.Unmarshal(state1, &st); err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.SnapIDs["tide-clock"] != testSnapID {
		t.Errorf("snap_ids = %v", st.SnapIDs)
	}
	if st.Cursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursor = %q, want 2026-08-16", st.Cursor["tide-clock"])
	}
	if st.AudienceCursor["tide-clock"] != "2026-08-16" {
		t.Errorf("audience cursor = %q, want 2026-08-16", st.AudienceCursor["tide-clock"])
	}
	// The two cursors are separate keys of the blob; an older Loot reading
	// this state must still find its own under `cursor`.
	var blob map[string]json.RawMessage
	if err := json.Unmarshal(state1, &blob); err != nil {
		t.Fatalf("state blob: %v", err)
	}
	if string(blob["cursor"]) != `{"tide-clock":"2026-08-16"}` ||
		string(blob["audience_cursor"]) != `{"tide-clock":"2026-08-16"}` {
		t.Errorf("state blob: cursor = %s audience_cursor = %s", blob["cursor"], blob["audience_cursor"])
	}

	// Second poll over the same data must emit nothing new and must not look
	// the snap id up again. The 17th is still unpublished, so the store is
	// asked again — for that day alone, on both ranges.
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
	change, audience = store.filtersByMetric(t)
	if change.Start != "2026-08-17" || audience.Start != "2026-08-17" {
		t.Errorf("poll 2 starts = %q / %q, want 2026-08-17 for both", change.Start, audience.Start)
	}
	var st2 state
	if err := json.Unmarshal(state2, &st2); err != nil {
		t.Fatalf("state 2: %v", err)
	}
	if st2.Cursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursor moved to %q on a poll that emitted nothing", st2.Cursor["tide-clock"])
	}
	if st2.AudienceCursor["tide-clock"] != "2026-08-16" {
		t.Errorf("audience cursor moved to %q on a poll that emitted nothing", st2.AudienceCursor["tide-clock"])
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
	// The same goes for the audience cursor, and with one more consequence:
	// left unseeded, the next poll would take this snap for an upgrade and
	// backfill the very history backfill_days 0 said not to.
	if st.AudienceCursor["tide-clock"] != "2026-08-17" {
		t.Errorf("audience cursor = %q, want yesterday 2026-08-17", st.AudienceCursor["tide-clock"])
	}

	// A second poll the same day still has nothing to ask for.
	events, _, err = src.Poll(context.Background(), raw)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if len(events) != 0 || store.metricsCalls != 0 {
		t.Errorf("poll 2 emitted %d events over %d metrics calls; both cursors are at yesterday",
			len(events), store.metricsCalls)
	}
}

func TestPollHonoursSince(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)
	src.Since = "2026-08-16"

	events, raw, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	// --since bounds both metrics: it is a statement about how much history
	// is wanted, not about one of the two series.
	change, audience := store.filtersByMetric(t)
	if change.Start != "2026-08-16" || audience.Start != "2026-08-16" {
		t.Errorf("--since 2026-08-16 asked for starts %q / %q", change.Start, audience.Start)
	}
	// The test server answers with the whole fixture whatever was asked, so
	// this is the floor doing its job rather than the store's range.
	for _, e := range events {
		if e.Day != "2026-08-16" {
			t.Errorf("--since 2026-08-16 let through %s", e.DedupeKey)
		}
	}
	if device, aud := countKinds(events); device != 3 || aud != 4 {
		t.Errorf("emitted %d device and %d audience events, want 3 and 4", device, aud)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Cursor["tide-clock"] != "2026-08-16" || st.AudienceCursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursors = %q / %q, want 2026-08-16 for both",
			st.Cursor["tide-clock"], st.AudienceCursor["tide-clock"])
	}
}

// The upgrade: a Loot that has read device changes for months runs a version
// that knows about audience snapshots for the first time. Its state has a
// `cursor` and no `audience_cursor`. It must backfill the audience for the
// whole window — and must not say one word about device days it has already
// stored, even though the store's answer includes them.
func TestPollBackfillsAudienceForStateWithoutAnAudienceCursor(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	old := `{"snap_ids":{"tide-clock":"` + testSnapID + `"},"cursor":{"tide-clock":"2026-08-16"}}`
	events, raw, err := src.Poll(context.Background(), []byte(old))
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	for _, e := range events {
		if e.Kind != core.KindAudience {
			t.Errorf("re-emitted a device event behind the cursor: %s (%s)", e.DedupeKey, e.Kind)
		}
	}
	if len(events) != 11 {
		t.Errorf("backfilled %d audience events, want 11 (the 14th, 15th and 16th)", len(events))
	}
	if store.infoCalls != 0 {
		t.Errorf("snap id was looked up %d times; the old state already had it", store.infoCalls)
	}

	// Two independent ranges in the one request: the device range picks up
	// where its cursor is, the audience range reaches back the full window.
	change, audience := store.filtersByMetric(t)
	if change.Start != "2026-08-17" {
		t.Errorf("device range starts %q, want 2026-08-17 (the day after its cursor)", change.Start)
	}
	if audience.Start != "2026-07-19" {
		t.Errorf("audience range starts %q, want 2026-07-19 (the backfill window)", audience.Start)
	}
	if change.End != "2026-08-17" || audience.End != "2026-08-17" {
		t.Errorf("ends = %q / %q, want yesterday 2026-08-17", change.End, audience.End)
	}

	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Cursor["tide-clock"] != "2026-08-16" {
		t.Errorf("device cursor = %q, want it left at 2026-08-16", st.Cursor["tide-clock"])
	}
	if st.AudienceCursor["tide-clock"] != "2026-08-16" {
		t.Errorf("audience cursor = %q, want 2026-08-16", st.AudienceCursor["tide-clock"])
	}

	// And it is a one-off: with the audience cursor persisted, the next poll
	// has nothing to backfill.
	events, _, err = src.Poll(context.Background(), raw)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("poll 2 re-emitted %d events", len(events))
	}
	if _, audience = store.filtersByMetric(t); audience.Start != "2026-08-17" {
		t.Errorf("poll 2 audience range starts %q, want 2026-08-17", audience.Start)
	}
}

// A snap is skipped only when *both* cursors are at yesterday. A device cursor
// that is fully up to date must not hide an audience cursor that is not.
func TestPollStillFetchesWhenOnlyOneCursorIsCurrent(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	st := `{"snap_ids":{"tide-clock":"` + testSnapID + `"},` +
		`"cursor":{"tide-clock":"2026-08-17"},"audience_cursor":{"tide-clock":"2026-08-15"}}`
	events, raw, err := src.Poll(context.Background(), []byte(st))
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if store.metricsCalls != 1 {
		t.Fatalf("metrics calls = %d, want 1: the audience is a day behind", store.metricsCalls)
	}
	if device, audience := countKinds(events); device != 0 || audience != 4 {
		t.Errorf("emitted %d device and %d audience events, want 0 and the 4 of 2026-08-16", device, audience)
	}
	// A range that has nothing left is still asked for, clamped to its last
	// day, never starting after its own end.
	change, audience := store.filtersByMetric(t)
	if change.Start != "2026-08-17" || audience.Start != "2026-08-16" {
		t.Errorf("starts = %q / %q, want 2026-08-17 and 2026-08-16", change.Start, audience.Start)
	}

	var decoded state
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Cursor["tide-clock"] != "2026-08-17" || decoded.AudienceCursor["tide-clock"] != "2026-08-16" {
		t.Errorf("cursors = %q / %q, want 2026-08-17 and 2026-08-16",
			decoded.Cursor["tide-clock"], decoded.AudienceCursor["tide-clock"])
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
	if st.SnapIDs == nil || st.Cursor == nil || st.AudienceCursor == nil {
		t.Fatal("decodeState returned nil maps")
	}
	// State written before audience snapshots existed has no audience_cursor;
	// Poll writes into the map, so it must come back empty rather than nil.
	st = decodeState([]byte(`{"snap_ids":{"a":"b"}}`))
	if st.SnapIDs["a"] != "b" || st.Cursor == nil || st.AudienceCursor == nil {
		t.Errorf("state = %+v", st)
	}
	st = decodeState([]byte(`{"cursor":{"a":"2026-08-16"},"audience_cursor":{"a":"2026-08-10"}}`))
	if st.Cursor["a"] != "2026-08-16" || st.AudienceCursor["a"] != "2026-08-10" {
		t.Errorf("state = %+v", st)
	}
}
