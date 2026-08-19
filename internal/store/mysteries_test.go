package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// TestDailySeries covers the shapes the detector reads, including the install
// rule (an overview row wins over the per-country rows of the same day) and
// the refund predicate the vault also uses.
func TestDailySeries(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	const (
		d1 = "2026-08-10"
		d2 = "2026-08-11"
	)

	mustInsert(t, st,
		installRow("googleplay", "Weather", d1, "", 90, "gp:ov"),
		installRow("googleplay", "Weather", d1, "US", 50, "gp:us"),
		installRow("googleplay", "Weather", d1, "DE", 40, "gp:de"),
		installRow("flathub", "Tide", d2, "", 30, "fh:1"),

		ledgerRow("appstore", "Notes", d1, "US", 8, 80, "as:1"),
		ledgerRow("appstore", "Notes", d2, "US", 5, 50, "as:2"),
		func() core.Event {
			e := ledgerRow("appstore", "Notes", d2, "US", -3, -30, "as:refund")
			e.Kind = "refund"
			return e
		}(),
		plainEvent("revenuecat", "cancellation", "Notes", d2, 0, "rc:cancel:1"),
		plainEvent("revenuecat", "cancellation", "Notes", d2, 0, "rc:cancel:2"),
	)

	installs, err := st.DailySeries(ctx, store.SeriesInstalls, d1, d2)
	if err != nil {
		t.Fatalf("installs: %v", err)
	}
	if got := installs[store.SeriesKey{Source: "googleplay", App: "Weather"}][d1]; got != 90 {
		t.Errorf("play installs = %v, want 90 (the overview row, not 180)", got)
	}
	if got := installs[store.SeriesKey{Source: "flathub", App: "Tide"}][d2]; got != 30 {
		t.Errorf("flathub installs = %v, want 30", got)
	}

	revenue, err := st.DailySeries(ctx, store.SeriesRevenue, d1, d2)
	if err != nil {
		t.Fatalf("revenue: %v", err)
	}
	appstore := store.SeriesKey{Source: "appstore", App: "Notes"}
	if got := revenue[appstore][d2]; got != 20 { // 50 - 30
		t.Errorf("revenue = %v, want 20", got)
	}

	refunds, err := st.DailySeries(ctx, store.SeriesRefunds, d1, d2)
	if err != nil {
		t.Fatalf("refunds: %v", err)
	}
	if got := refunds[appstore][d2]; got != 3 {
		t.Errorf("refunds = %v, want 3 (a positive count)", got)
	}

	cancels, err := st.DailySeries(ctx, store.SeriesCancellations, d1, d2)
	if err != nil {
		t.Fatalf("cancellations: %v", err)
	}
	if got := cancels[store.SeriesKey{Source: "revenuecat", App: "Notes"}][d2]; got != 2 {
		t.Errorf("cancellations = %v, want 2", got)
	}
}

func TestSettlementsAndSilenceInputs(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	mustInsert(t, st,
		plainEvent("loot", "settlement", "Notes", "2026-08-10", 0, "settle:BR"),
		plainEvent("loot", "settlement", "Notes", "2026-08-10", 0, "settle:CL"),
		plainEvent("appstore", "sale", "Notes", "2026-08-10", 1, "as:1"),
		plainEvent("appstore", "sale", "Notes", "2026-08-11", 1, "as:2"),
	)

	settlements, err := st.SettlementsByDay(ctx, "2026-08-01", "2026-08-19")
	if err != nil {
		t.Fatalf("settlements: %v", err)
	}
	if settlements["2026-08-10"] != 2 {
		t.Errorf("settlements = %v, want 2", settlements["2026-08-10"])
	}

	bySource, err := st.EventsBySourceDay(ctx, "2026-08-01", "2026-08-19")
	if err != nil {
		t.Fatalf("events by source: %v", err)
	}
	if bySource["appstore"]["2026-08-11"] != 1 {
		t.Errorf("appstore rows = %v, want 1", bySource["appstore"]["2026-08-11"])
	}
	if _, ok := bySource["loot"]; ok {
		t.Error("Loot's own events must not be watched for silence")
	}
}

func newMystery(kind, source, app, metric, day string) core.Mystery {
	detail, _ := json.Marshal(core.MysteryDetail{
		Series: []core.MysteryPoint{{Day: day, Value: 12}},
		Unit:   "count",
	})
	return core.Mystery{
		ID:        core.NewID(),
		Kind:      kind,
		Source:    source,
		App:       app,
		Metric:    metric,
		Day:       day,
		Observed:  12,
		Expected:  3,
		Z:         6.1,
		Title:     "something happened",
		Detail:    detail,
		Status:    core.MysteryOpen,
		CreatedAt: time.Now().UTC(),
		DedupeKey: kind + ":" + source + ":" + app + ":" + metric + ":" + day,
	}
}

func TestMysteryInsertIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	m := newMystery(core.MysterySpike, "googleplay", "Weather", "installs", "2026-08-14")
	created, err := st.InsertMystery(ctx, m)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}

	dup := newMystery(core.MysterySpike, "googleplay", "Weather", "installs", "2026-08-14")
	created, err = st.InsertMystery(ctx, dup)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created {
		t.Error("the same day was flagged twice")
	}

	// A different metric on the same day is a different question.
	other := newMystery(core.MysterySpike, "googleplay", "Weather", "revenue_base", "2026-08-14")
	if created, err = st.InsertMystery(ctx, other); err != nil || !created {
		t.Fatalf("different metric: created=%v err=%v", created, err)
	}

	open, err := st.ListMysteries(ctx, store.MysteryQuery{Statuses: []string{core.MysteryOpen}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("want 2 open mysteries, got %d", len(open))
	}
	if n, err := st.CountOpenMysteries(ctx); err != nil || n != 2 {
		t.Fatalf("count open = %d (%v), want 2", n, err)
	}
}

func TestResolveMysteryOnlyOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	m := newMystery(core.MysteryRefundSpike, "appstore", "Notes", "refunds", "2026-08-06")
	if _, err := st.InsertMystery(ctx, m); err != nil {
		t.Fatalf("insert: %v", err)
	}

	now := time.Now().UTC()
	changed, err := st.ResolveMystery(ctx, m.ID, core.MysterySolved, "a bad build shipped", now)
	if err != nil || !changed {
		t.Fatalf("solve: changed=%v err=%v", changed, err)
	}
	changed, err = st.ResolveMystery(ctx, m.ID, core.MysterySolved, "again", now)
	if err != nil {
		t.Fatalf("second solve: %v", err)
	}
	if changed {
		t.Error("a solved mystery was solved again; it would have paid two drops")
	}

	got, err := st.GetMystery(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != core.MysterySolved || got.Note != "a bad build shipped" || got.ResolvedAt == nil {
		t.Fatalf("unexpected resolved mystery: %+v", got)
	}

	if _, err := st.GetMystery(ctx, "nope"); err != store.ErrMysteryNotFound {
		t.Errorf("get unknown: %v, want ErrMysteryNotFound", err)
	}
}
