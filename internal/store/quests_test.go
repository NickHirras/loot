package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// installRow is one `install` event. An empty country makes it an *overview*
// row (Flathub, and Google Play's per-app file); a country makes it one of the
// per-country rows Play also publishes for the same day.
func installRow(source, app, day, country string, qty int, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID:         core.NewID(),
		Source:     source,
		Kind:       "install",
		App:        app,
		Day:        day,
		OccurredAt: occurred,
		ObservedAt: occurred,
		Country:    country,
		Quantity:   qty,
		Silent:     true,
		DedupeKey:  dedupe,
	}
}

func plainEvent(source, kind, app, day string, qty int, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID:         core.NewID(),
		Source:     source,
		Kind:       kind,
		App:        app,
		Day:        day,
		OccurredAt: occurred,
		ObservedAt: occurred,
		Quantity:   qty,
		DedupeKey:  dedupe,
	}
}

func mustInsert(t *testing.T, st *store.Store, events ...core.Event) {
	t.Helper()
	for _, ev := range events {
		if _, err := st.InsertEvent(context.Background(), ev); err != nil {
			t.Fatalf("insert event %s: %v", ev.DedupeKey, err)
		}
	}
}

// TestQuestProgressMetrics walks every metric a quest can count, including the
// install rule that keeps a source reporting both an overview and per-country
// rows from being counted twice.
func TestQuestProgressMetrics(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	const (
		d1 = "2026-08-10"
		d2 = "2026-08-11"
		d3 = "2026-08-12" // outside the window on purpose
	)

	mustInsert(t, st,
		// Ledger money and units, plus a refund and a free download.
		ledgerRow("appstore", "Notes", d1, "US", 10, 100, "as:1"),
		ledgerRow("appstore", "Notes", d2, "DE", 4, 40, "as:2"),
		ledgerRow("appstore", "Notes", d2, "US", -1, -10, "as:refund"),
		ledgerRow("googleplay", "Weather", d1, "US", 3, 30, "gp:1"),
		ledgerRow("appstore", "Notes", d3, "US", 99, 999, "as:outside"),

		// Google Play reports the same day twice: one overview row and three
		// country rows. Only the overview may count.
		installRow("googleplay", "Weather", d1, "", 100, "gp:inst:overview"),
		installRow("googleplay", "Weather", d1, "US", 60, "gp:inst:us"),
		installRow("googleplay", "Weather", d1, "DE", 40, "gp:inst:de"),
		// Flathub has no country at all, and no overview/country ambiguity.
		installRow("flathub", "Tide", d2, "", 25, "fh:inst"),
		// A source with country rows only: those are the day.
		installRow("snapcraft", "Tide", d2, "FR", 7, "snap:inst:fr"),
		installRow("snapcraft", "Tide", d2, "GB", 3, "snap:inst:gb"),

		// Subscriber snapshots: a level, so only the newest per app counts.
		plainEvent("appstore", "subscription_snapshot", "Notes", d1, 400, "subs:1"),
		plainEvent("appstore", "subscription_snapshot", "Notes", d2, 420, "subs:2"),
		plainEvent("appstore", "subscription_snapshot", "Weather", d2, 80, "subs:3"),

		plainEvent("loot", "settlement", "Notes", d1, 0, "settle:BR"),
		plainEvent("loot", "settlement", "Notes", d2, 0, "settle:JP"),

		plainEvent("github", "star", "loot", d1, 0, "gh:star:1"),
		plainEvent("github", "star", "loot", d2, 0, "gh:star:2"),
	)

	// Two visible drops and one still sealed in a chest, which must not count.
	visible := plainEvent("dev", "rare", "Notes", d1, 0, "drop:src:1")
	sealed := plainEvent("dev", "rare", "Notes", d2, 0, "drop:src:2")
	mustInsert(t, st, visible, sealed)
	if err := st.InsertDrop(ctx, core.Drop{ID: core.NewID(), EventID: visible.ID,
		Rarity: core.Rare, Title: "seen", XP: 100, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("insert drop: %v", err)
	}
	if err := st.InsertDrop(ctx, core.Drop{ID: core.NewID(), EventID: sealed.ID,
		Rarity: core.Epic, Title: "hidden", XP: 300, CreatedAt: time.Now().UTC(),
		ChestDate: d2}); err != nil {
		t.Fatalf("insert chest drop: %v", err)
	}

	cases := []struct {
		metric core.Metric
		app    string
		source string
		want   float64
	}{
		{metric: core.MetricRevenue, want: 160},                        // 100 + 40 - 10 + 30
		{metric: core.MetricRevenue, source: "appstore", want: 130},    // filtered
		{metric: core.MetricUnits, want: 17},                           // refunds excluded
		{metric: core.MetricUnits, app: "Weather", want: 3},            //
		{metric: core.MetricInstalls, want: 135},                       // 100 (overview) + 25 + 10
		{metric: core.MetricInstalls, source: "googleplay", want: 100}, // never 200
		{metric: core.MetricSubscribers, want: 500},                    // 420 (newest) + 80
		{metric: core.MetricSettlements, want: 2},
		{metric: core.MetricStars, want: 2},
		{metric: core.MetricDrops, want: 1}, // the sealed one is invisible
		{metric: core.MetricXP, want: 100},
	}

	for _, tc := range cases {
		got, err := st.QuestProgress(ctx, core.Quest{
			Metric: tc.metric, App: tc.app, Source: tc.source,
			WindowStart: d1, WindowEnd: d2,
		})
		if err != nil {
			t.Fatalf("%s progress: %v", tc.metric, err)
		}
		if got != tc.want {
			t.Errorf("%s (app=%q source=%q) = %v, want %v", tc.metric, tc.app, tc.source, got, tc.want)
		}
	}
}

func TestMetricsWithData(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	available, err := st.MetricsWithData(ctx)
	if err != nil {
		t.Fatalf("metrics with data: %v", err)
	}
	for _, m := range core.Metrics {
		if available[m] {
			t.Errorf("empty database claims data for %s", m)
		}
	}

	mustInsert(t, st, installRow("flathub", "Tide", "2026-08-10", "", 12, "fh:1"))
	available, err = st.MetricsWithData(ctx)
	if err != nil {
		t.Fatalf("metrics with data: %v", err)
	}
	if !available[core.MetricInstalls] {
		t.Error("installs should have data")
	}
	if available[core.MetricRevenue] || available[core.MetricStars] {
		t.Error("an install is not revenue and not a star")
	}
}

func newQuest(metric core.Metric, target float64, start, end, key string) core.Quest {
	return core.Quest{
		ID:          core.NewID(),
		Kind:        core.QuestAuto,
		Metric:      metric,
		Target:      target,
		WindowStart: start,
		WindowEnd:   end,
		Title:       "test quest",
		Status:      core.QuestActive,
		CreatedAt:   time.Now().UTC(),
		DedupeKey:   key,
	}
}

// TestQuestInsertIsIdempotent is the property the generator leans on entirely.
func TestQuestInsertIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	q := newQuest(core.MetricRevenue, 100, "2026-08-17", "2026-08-23", "auto:revenue:::2026-08-17:2026-08-23")
	created, err := st.InsertQuest(ctx, q)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}

	again := newQuest(core.MetricRevenue, 200, "2026-08-17", "2026-08-23", q.DedupeKey)
	created, err = st.InsertQuest(ctx, again)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created {
		t.Error("the same window was generated twice")
	}

	list, err := st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Target != 100 {
		t.Fatalf("want one quest with the original target, got %+v", list)
	}
}

func TestCompleteQuestOnlyOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	q := newQuest(core.MetricUnits, 10, "2026-08-17", "2026-08-23", "auto:units")
	if _, err := st.InsertQuest(ctx, q); err != nil {
		t.Fatalf("insert: %v", err)
	}

	now := time.Now().UTC()
	done, err := st.CompleteQuest(ctx, q.ID, 12, now)
	if err != nil || !done {
		t.Fatalf("first completion: done=%v err=%v", done, err)
	}
	done, err = st.CompleteQuest(ctx, q.ID, 14, now)
	if err != nil {
		t.Fatalf("second completion: %v", err)
	}
	if done {
		t.Error("a completed quest completed again; it would have paid two drops")
	}

	got, err := st.GetQuest(ctx, q.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != core.QuestCompleted || got.CompletedAt == nil || got.Value != 12 {
		t.Fatalf("unexpected completed quest: %+v", got)
	}
	if got.Pct != 1 {
		t.Errorf("pct = %v, want 1", got.Pct)
	}
}

// A quest whose window has closed is history, whatever its status column still
// says. Counting last week's six towards this week's cap is what used to leave
// a Monday board empty all day.
func TestCountActiveQuestsExcludesClosedWindows(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	lastWeek := newQuest(core.MetricRevenue, 100, "2026-08-10", "2026-08-16", "auto:last")
	thisWeek := newQuest(core.MetricUnits, 50, "2026-08-17", "2026-08-23", "auto:this")
	for _, q := range []core.Quest{lastWeek, thisWeek} {
		if _, err := st.InsertQuest(ctx, q); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	all, err := st.CountActiveQuests(ctx, "", "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if all != 2 {
		t.Errorf("count with no day = %d, want both rows", all)
	}

	open, err := st.CountActiveQuests(ctx, core.QuestAuto, "2026-08-17")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if open != 1 {
		t.Errorf("count on Monday = %d, want 1 — last week's window has closed", open)
	}
}

// TestExpireQuestsIsQuiet checks the shame-free ending: an unmet quest becomes
// history with its progress intact, and nothing else about it changes.
func TestExpireQuestsIsQuiet(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	over := newQuest(core.MetricRevenue, 100, "2026-08-03", "2026-08-09", "auto:old")
	running := newQuest(core.MetricRevenue, 100, "2026-08-17", "2026-08-23", "auto:now")
	if _, err := st.InsertQuest(ctx, over); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := st.InsertQuest(ctx, running); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.SaveQuestProgress(ctx, over.ID, 62, time.Now().UTC()); err != nil {
		t.Fatalf("progress: %v", err)
	}

	expired, err := st.ExpireQuests(ctx, "2026-08-19", time.Now().UTC())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != over.ID {
		t.Fatalf("want only the finished window expired, got %+v", expired)
	}

	got, err := st.GetQuest(ctx, over.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != core.QuestExpired {
		t.Errorf("status = %q, want expired", got.Status)
	}
	if got.CompletedAt != nil {
		t.Error("an expired quest must not look completed")
	}
	if got.Value != 62 || got.Pct != 0.62 {
		t.Errorf("progress lost: value=%v pct=%v", got.Value, got.Pct)
	}

	still, err := st.GetQuest(ctx, running.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if still.Status != core.QuestActive {
		t.Errorf("a live window was expired: %q", still.Status)
	}

	// Running it again finds nothing left to do.
	again, err := st.ExpireQuests(ctx, "2026-08-19", time.Now().UTC())
	if err != nil {
		t.Fatalf("expire again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("expiry is not idempotent: %+v", again)
	}
}

func TestDeleteQuest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	q := newQuest(core.MetricDrops, 5, "2026-08-17", "2026-08-23", "custom:1")
	if _, err := st.InsertQuest(ctx, q); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.DeleteQuest(ctx, q.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.DeleteQuest(ctx, q.ID); err != store.ErrQuestNotFound {
		t.Errorf("second delete: %v, want ErrQuestNotFound", err)
	}
	if _, err := st.GetQuest(ctx, q.ID); err != store.ErrQuestNotFound {
		t.Errorf("get after delete: %v, want ErrQuestNotFound", err)
	}
}
