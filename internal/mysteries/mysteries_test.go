package mysteries_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/mysteries"
	"github.com/nickhirras/loot/internal/store"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// today is the clock every test runs on; the last completed day is the 18th.
var today = time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

type recorder struct{ events []core.Event }

func (r *recorder) Ingest(_ context.Context, ev core.Event) (*core.Drop, error) {
	r.events = append(r.events, ev)
	return &core.Drop{ID: core.NewID(), EventID: ev.ID, Rarity: core.Uncommon, XP: 75}, nil
}

type spy struct{ seen map[string]int }

func newSpy() *spy { return &spy{seen: map[string]int{}} }

func (s *spy) Publish(msg bus.Message) { s.seen[msg.Type]++ }

func newDetector(st *store.Store, b mysteries.Publisher) *mysteries.Detector {
	d := mysteries.NewDetector(st, b, "USD", quiet())
	d.Now = func() time.Time { return today }
	return d
}

// day returns the YYYY-MM-DD n days before the last completed day.
func day(n int) string { return core.DayOf(today.AddDate(0, 0, -1-n)) }

func insert(t *testing.T, st *store.Store, ev core.Event) {
	t.Helper()
	if _, err := st.InsertEvent(context.Background(), ev); err != nil {
		t.Fatalf("insert %s: %v", ev.DedupeKey, err)
	}
}

// installs writes one install row per day, oldest first: values[0] is the
// oldest day of the run, values[len-1] the last completed day.
func installs(t *testing.T, st *store.Store, source, app string, values []float64) {
	t.Helper()
	for i, v := range values {
		d := day(len(values) - 1 - i)
		occurred, _ := time.Parse(core.DayLayout, d)
		insert(t, st, core.Event{
			ID: core.NewID(), Source: source, Kind: "install", App: app,
			Day: d, OccurredAt: occurred, ObservedAt: occurred,
			Quantity: int(v), Silent: true,
			DedupeKey: source + ":inst:" + app + ":" + d,
		})
	}
}

// steady is a plausible flat series: alternating values, so the median
// absolute deviation is small but not zero.
func steady(n int, a, b float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = a
		} else {
			out[i] = b
		}
	}
	return out
}

func titles(found []core.Mystery) string {
	var b strings.Builder
	for _, m := range found {
		b.WriteString(m.Kind + " " + m.Day + " " + m.Title + "\n")
	}
	return b.String()
}

func find(found []core.Mystery, kind string) *core.Mystery {
	for i := range found {
		if found[i].Kind == kind {
			return &found[i]
		}
	}
	return nil
}

// TestDetectSpike covers the headline case, the sparkline that travels with
// it, and the rule that a two-day run asks its question once.
func TestDetectSpike(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	series := steady(40, 100, 110)
	series[36] = 900 // five days ago
	series[37] = 850 // and the day after: the same story
	installs(t, st, "googleplay", "Weather", series)

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	spike := find(found, core.MysterySpike)
	if spike == nil {
		t.Fatalf("no spike found in:\n%s", titles(found))
	}
	if spike.Day != day(3) {
		t.Errorf("spike day = %s, want %s", spike.Day, day(3))
	}
	if spike.Observed != 900 || spike.Expected < 100 || spike.Expected > 110 {
		t.Errorf("observed/expected = %v/%v", spike.Observed, spike.Expected)
	}
	if spike.Z < mysteries.ZThreshold {
		t.Errorf("z = %v, want at least %v", spike.Z, mysteries.ZThreshold)
	}
	if !strings.Contains(spike.Title, "Google Play installs") || !strings.HasSuffix(spike.Title, "— why?") {
		t.Errorf("title = %q", spike.Title)
	}
	for _, m := range found {
		if m.Kind == core.MysterySpike && m.Day == day(2) {
			t.Error("the second day of one spike was flagged as its own mystery")
		}
	}

	var detail core.MysteryDetail
	if err := json.Unmarshal(spike.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Series) != 28 {
		t.Errorf("sparkline has %d points, want 28", len(detail.Series))
	}
	if detail.Series[len(detail.Series)-1].Day != spike.Day {
		t.Errorf("sparkline ends on %s, want the flagged day", detail.Series[len(detail.Series)-1].Day)
	}
	if detail.Ratio < 8 {
		t.Errorf("ratio = %v, want ~8", detail.Ratio)
	}

	// Running again finds nothing new: every mystery is keyed on its own day.
	again, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("detection is not idempotent: %s", titles(again))
	}
}

func TestDetectDip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	series := steady(40, 200, 220)
	series[35] = 12
	installs(t, st, "flathub", "Tide", series)

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	dip := find(found, core.MysteryDip)
	if dip == nil {
		t.Fatalf("no dip found in:\n%s", titles(found))
	}
	if dip.Day != day(4) || dip.Observed != 12 {
		t.Errorf("dip = %s / %v", dip.Day, dip.Observed)
	}
	if !strings.Contains(dip.Title, "dropped") {
		t.Errorf("title = %q", dip.Title)
	}
	if strings.Contains(dip.Title, "why?") {
		t.Errorf("a dip should state what happened, not interrogate: %q", dip.Title)
	}
}

// TestSmallMovesAreLeftAlone is the other half of the threshold: a series that
// idles at one and jumps to four is statistically enormous and practically
// nothing.
func TestSmallMovesAreLeftAlone(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	series := steady(40, 1, 1)
	series[36] = 4
	installs(t, st, "flathub", "Tide", series)

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("flagged a move nobody would care about:\n%s", titles(found))
	}
}

func ledgerRow(t *testing.T, st *store.Store, source, app, d, kind string, units int, amount float64, seq int) {
	t.Helper()
	occurred, _ := time.Parse(core.DayLayout, d)
	insert(t, st, core.Event{
		ID: core.NewID(), Source: source, Kind: kind, App: app,
		Day: d, OccurredAt: occurred, ObservedAt: occurred, Country: "US",
		Amount: amount, AmountBase: amount, Currency: "USD", Quantity: units,
		IsLedger: true, Silent: true,
		DedupeKey: source + ":" + app + ":" + d + ":" + kind + ":" + core.HumanInt(int64(seq)),
	})
}

func TestDetectRefundSpike(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Forty days of steady business with the odd refund, then one day where
	// six came back at once.
	for i := 39; i >= 0; i-- {
		d := day(i)
		ledgerRow(t, st, "appstore", "Notes", d, "sale", 40, 400, 1)
		refunds := 1
		if i == 3 {
			refunds = 6
		}
		ledgerRow(t, st, "appstore", "Notes", d, "refund", -refunds, float64(-10*refunds), 2)
	}

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	spike := find(found, core.MysteryRefundSpike)
	if spike == nil {
		t.Fatalf("no refund spike found in:\n%s", titles(found))
	}
	if spike.Day != day(3) || spike.Observed != 6 || spike.Expected != 1 {
		t.Errorf("refund spike = %s, %v vs %v", spike.Day, spike.Observed, spike.Expected)
	}
	if !strings.Contains(spike.Title, "6 refunds on App Store") {
		t.Errorf("title = %q", spike.Title)
	}
}

func TestDetectNewCountryCluster(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	d := day(2)
	occurred, _ := time.Parse(core.DayLayout, d)
	for _, country := range []string{"BR", "CL", "PE"} {
		insert(t, st, core.Event{
			ID: core.NewID(), Source: "loot", Kind: "settlement", App: "Notes",
			Day: d, OccurredAt: occurred, ObservedAt: occurred, Country: country,
			DedupeKey: "loot:settlement:" + country,
		})
	}

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	cluster := find(found, core.MysteryNewCountryCluster)
	if cluster == nil {
		t.Fatalf("no cluster found in:\n%s", titles(found))
	}
	if cluster.Day != d || cluster.Observed != 3 {
		t.Errorf("cluster = %s / %v", cluster.Day, cluster.Observed)
	}
	if !strings.Contains(cluster.Title, "3 new countries settled") {
		t.Errorf("title = %q", cluster.Title)
	}
}

// TestDetectSilence is the one mystery with a usual answer: a source that
// reported every day and then stopped is almost always a broken credential.
func TestDetectSilence(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Daily rows up to four days ago, then nothing.
	for i := 39; i >= 4; i-- {
		ledgerRow(t, st, "appstore", "Notes", day(i), "sale", 20, 200, 1)
	}

	found, err := newDetector(st, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	quiet := find(found, core.MysterySilence)
	if quiet == nil {
		t.Fatalf("no silence found in:\n%s", titles(found))
	}
	if quiet.Day != day(3) {
		t.Errorf("silence day = %s, want the first silent day %s", quiet.Day, day(3))
	}
	if !strings.HasPrefix(quiet.Title, "App Store has gone quiet since") {
		t.Errorf("title = %q", quiet.Title)
	}

	// A source that only ever reported twice is not broken, just occasional.
	st2 := newStore(t)
	ledgerRow(t, st2, "appstore", "Notes", day(30), "sale", 5, 50, 1)
	ledgerRow(t, st2, "appstore", "Notes", day(20), "sale", 5, 50, 1)
	found, err = newDetector(st2, newSpy()).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if find(found, core.MysterySilence) != nil {
		t.Errorf("an occasional source was called quiet:\n%s", titles(found))
	}
}

func TestSolveAndDismiss(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	series := steady(40, 100, 110)
	series[36] = 900
	installs(t, st, "googleplay", "Weather", series)

	b := newSpy()
	found, err := newDetector(st, b).Run(ctx)
	if err != nil || len(found) == 0 {
		t.Fatalf("run: %d found, %v", len(found), err)
	}
	if b.seen["mysteries"] == 0 {
		t.Error("detection published no mysteries message")
	}

	rec := &recorder{}
	svc := mysteries.NewService(st, rec, b, quiet())
	solved, err := svc.Solve(ctx, found[0].ID, "a big newsletter linked us")
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if solved.Status != core.MysterySolved || solved.Note != "a big newsletter linked us" {
		t.Fatalf("solved = %+v", solved)
	}
	if len(rec.events) != 1 {
		t.Fatalf("want one reward event, got %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Source != "loot" || ev.Kind != mysteries.KindSolved {
		t.Errorf("event = %s/%s", ev.Source, ev.Kind)
	}
	if ev.DedupeKey != "loot:mystery_solved:"+found[0].ID {
		t.Errorf("dedupe key = %q", ev.DedupeKey)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["note"] != "a big newsletter linked us" {
		t.Errorf("payload = %v", payload)
	}

	// Solving again pays nothing.
	if _, err := svc.Solve(ctx, found[0].ID, "again"); err != nil {
		t.Fatalf("second solve: %v", err)
	}
	if len(rec.events) != 1 {
		t.Errorf("a solved mystery paid twice: %d events", len(rec.events))
	}

	// Dismissal is quiet: no event at all.
	if len(found) > 1 {
		if _, err := svc.Dismiss(ctx, found[1].ID); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
		if len(rec.events) != 1 {
			t.Errorf("dismissing made noise: %d events", len(rec.events))
		}
	}

	book, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(book.Resolved) == 0 {
		t.Error("the notebook is empty after solving")
	}
	for _, m := range book.Open {
		if m.ID == found[0].ID {
			t.Error("a solved mystery is still open")
		}
	}
}
