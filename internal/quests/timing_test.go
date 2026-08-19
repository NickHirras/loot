package quests_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/store"
)

// When a quest is written matters as much as what it says.
//
// Both tests here come from the same real failure: a first run generated
// quests against an empty database, the App Store backfill landed seconds
// later, and "Settle 2 new countries" completed instantly — an epic drop, a
// sound and XP, for countries settled months ago. The fixes are the wait
// (below) and the history minimum (further down).

// TestRunWaitsForTheFirstPollRound: with ReadyWhen set, nothing is generated
// until it closes.
func TestRunWaitsForTheFirstPollRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	ready := make(chan struct{})
	svc.ReadyWhen = ready
	go svc.Run(ctx)

	// The history a backfill would deliver has not arrived yet, so a quest
	// written now would be written against nothing.
	time.Sleep(120 * time.Millisecond)
	active, err := st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("quests were generated before the first poll round: %+v", active)
	}

	// Now the backfill lands, and only then is the round declared over.
	insert(t, st,
		ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"),
		ledgerRow("appstore", "2026-08-12", 20, 200, "as:2"),
		ledgerRow("appstore", "2026-08-13", 20, 200, "as:3"),
		ledgerRow("appstore", "2026-08-14", 20, 200, "as:4"),
		ledgerRow("appstore", "2026-08-15", 20, 200, "as:5"),
		ledgerRow("appstore", "2026-08-16", 20, 200, "as:6"),
		ledgerRow("appstore", "2026-08-10", 20, 200, "as:7"),
	)
	close(ready)

	deadline := time.Now().Add(3 * time.Second)
	for {
		active, err = st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(active) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing was generated after the first poll round finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, q := range active {
		if q.Status != core.QuestActive {
			t.Errorf("quest %q is already %s the moment it was written", q.Title, q.Status)
		}
	}
}

// TestRunGeneratesImmediatelyWithoutAGate is demo mode and every test: no
// ReadyWhen means the history is already there, so do not wait for a channel
// nobody is going to close.
func TestRunGeneratesImmediatelyWithoutAGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newStore(t)
	insert(t, st,
		ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"),
		ledgerRow("appstore", "2026-08-14", 30, 300, "as:2"),
	)

	svc := newService(t, st, &recorder{}, newSpy())
	go svc.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		active, err := st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(active) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an ungated service never generated anything")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestThinHistoryGeneratesNothing is the second half of the same fix: even
// once the backfill has landed, a metric Loot has only seen for a day or two
// has no baseline worth extrapolating from.
func TestThinHistoryGeneratesNothing(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Two days of revenue last week. Real money, but not a week of it.
	insert(t, st,
		ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"),
		ledgerRow("appstore", "2026-08-12", 30, 300, "as:2"),
	)

	gen := newGenerator(st)
	gen.MinHistoryDays = quests.MinHistoryDays // the real rule, not the test escape hatch

	created, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("two days of history produced %d quest(s): %+v", len(created), created)
	}
}

// TestEnoughHistoryGenerates: seven days of a metric is a baseline, and the
// generator writes about it.
func TestEnoughHistoryGenerates(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Seven distinct days, spanning last week and the week before, so there is
	// both a baseline and a previous window to beat.
	days := []string{
		"2026-08-04", "2026-08-05", "2026-08-06",
		"2026-08-10", "2026-08-11", "2026-08-12", "2026-08-13",
	}
	rows := make([]core.Event, 0, len(days))
	for i, day := range days {
		rows = append(rows, ledgerRow("appstore", day, 20, 200, "as:"+day+":"+string(rune('a'+i))))
	}
	insert(t, st, rows...)

	gen := newGenerator(st)
	gen.MinHistoryDays = quests.MinHistoryDays

	created, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("a week of revenue produced no quest at all")
	}
	for _, q := range created {
		if q.Metric != core.MetricRevenue && q.Metric != core.MetricUnits {
			t.Errorf("generated a %s quest from revenue-only history", q.Metric)
		}
	}
}

// TestThinHistorySkipsOnlyTheThinMetric: the minimum is per metric, not per
// database. A long install history and two days of revenue gets an install
// quest and no revenue quest.
func TestThinHistorySkipsOnlyTheThinMetric(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	var rows []core.Event
	for i := 0; i < 8; i++ {
		day := time.Date(2026, 8, 4+i, 0, 0, 0, 0, time.UTC).Format(core.DayLayout)
		rows = append(rows, installRow("flathub", day, 100+i, "fh:"+day))
	}
	rows = append(rows, ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"))
	insert(t, st, rows...)

	gen := newGenerator(st)
	gen.MinHistoryDays = quests.MinHistoryDays

	created, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("a long install history produced nothing")
	}
	for _, q := range created {
		if q.Metric == core.MetricRevenue {
			t.Errorf("one day of revenue produced a revenue quest: %+v", q)
		}
	}
}
