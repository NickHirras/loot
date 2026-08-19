package quests_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/store"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// recorder stands in for the pipeline: it remembers every event it was handed
// and answers with a drop, exactly as a real ingest would.
type recorder struct {
	events []core.Event
	xp     int
}

func (r *recorder) Ingest(_ context.Context, ev core.Event) (*core.Drop, error) {
	r.events = append(r.events, ev)
	return &core.Drop{ID: core.NewID(), EventID: ev.ID, Rarity: core.Rare,
		Title: "Quest complete", XP: r.xp}, nil
}

// spy counts bus messages by type.
type spy struct{ seen map[string]int }

func newSpy() *spy { return &spy{seen: map[string]int{}} }

func (s *spy) Publish(msg bus.Message) { s.seen[msg.Type]++ }

// wednesday is the clock every test runs on: mid-week, mid-month, so both
// windows are open and neither boundary is being tested by accident.
var wednesday = time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

func ledgerRow(source, day string, units int, amount float64, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID: core.NewID(), Source: source, Kind: "sale", App: "Notes",
		Day: day, OccurredAt: occurred, ObservedAt: occurred, Country: "US",
		Amount: amount, AmountBase: amount, Currency: "USD", Quantity: units,
		IsLedger: true, Silent: true, DedupeKey: dedupe,
	}
}

func installRow(source, day string, qty int, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID: core.NewID(), Source: source, Kind: "install", App: "Tide",
		Day: day, OccurredAt: occurred, ObservedAt: occurred,
		Quantity: qty, Silent: true, DedupeKey: dedupe,
	}
}

func insert(t *testing.T, st *store.Store, events ...core.Event) {
	t.Helper()
	for _, ev := range events {
		if _, err := st.InsertEvent(context.Background(), ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}
}

func newGenerator(st *store.Store) *quests.Generator {
	return &quests.Generator{
		Store:           st,
		Logger:          quiet(),
		DisplayCurrency: "USD",
		Now:             func() time.Time { return wednesday },
		// These tests seed a day or two of history on purpose — they are about
		// targets, idempotence and the cap, not about how much history is
		// enough. The minimum has its own tests below.
		MinHistoryDays: -1,
	}
}

// TestGenerateOnlyWhereThereIsData is the "no impossible goals" rule: a
// database that has only ever seen installs gets an install quest and nothing
// about revenue or stars.
func TestGenerateOnlyWhereThereIsData(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Last week (Mon 10th – Sun 16th) had installs, and nothing else.
	insert(t, st,
		installRow("flathub", "2026-08-11", 400, "fh:1"),
		installRow("flathub", "2026-08-13", 500, "fh:2"),
	)

	created, err := newGenerator(st).Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want exactly one quest, got %d: %+v", len(created), created)
	}
	q := created[0]
	if q.Metric != core.MetricInstalls {
		t.Errorf("metric = %s, want installs", q.Metric)
	}
	if q.WindowStart != "2026-08-17" || q.WindowEnd != "2026-08-23" {
		t.Errorf("window = %s..%s, want the current Mon..Sun", q.WindowStart, q.WindowEnd)
	}
	// 900 last week, +5%, rounded up to the nearest fifty.
	if q.Target != 950 {
		t.Errorf("target = %v, want 950", q.Target)
	}
	if q.Title != "Installs: beat last week · 950" {
		t.Errorf("title = %q", q.Title)
	}
	if q.Kind != core.QuestAuto {
		t.Errorf("kind = %q, want auto", q.Kind)
	}
}

func TestGenerateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	insert(t, st,
		ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"),
		ledgerRow("appstore", "2026-08-14", 30, 300, "as:2"),
		// Last month, so the monthly quest has a basis too.
		ledgerRow("appstore", "2026-07-14", 40, 400, "as:3"),
	)

	gen := newGenerator(st)
	first, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("nothing generated from a week with revenue in it")
	}

	second, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate again: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("a second run duplicated quests: %+v", second)
	}

	active, err := st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != len(first) {
		t.Fatalf("board has %d quests, generator claimed %d", len(active), len(first))
	}
}

func TestGenerateRespectsTheCap(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	insert(t, st,
		ledgerRow("appstore", "2026-08-11", 20, 200, "as:1"),
		ledgerRow("appstore", "2026-07-11", 20, 200, "as:2"),
		installRow("flathub", "2026-08-12", 300, "fh:1"),
	)

	gen := newGenerator(st)
	gen.MaxActive = 2
	created, err := gen.Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("cap not honoured: %d quests created", len(created))
	}
}

func newService(t *testing.T, st *store.Store, rec *recorder, b *spy) *quests.Service {
	t.Helper()
	svc := quests.NewService(st, rec, b, "USD", quiet())
	svc.Now = func() time.Time { return wednesday }
	svc.Generator.Now = svc.Now
	svc.Generator.MinHistoryDays = -1
	return svc
}

// TestCheckCompletesOnceAndPaysADrop covers the reward path end to end: the
// quest flips to completed, one event is ingested, its XP is recorded, and a
// second check does nothing at all.
func TestCheckCompletesOnceAndPaysADrop(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{xp: 200}
	b := newSpy()
	svc := newService(t, st, rec, b)

	insert(t, st, ledgerRow("appstore", "2026-08-18", 12, 120, "as:1"))

	quest, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricRevenue, Target: 100, Window: "week", Title: "Make $100",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := svc.Check(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Completed) != 1 {
		t.Fatalf("want one completion, got %d", len(res.Completed))
	}
	if len(rec.events) != 1 {
		t.Fatalf("want one event ingested, got %d", len(rec.events))
	}

	ev := rec.events[0]
	if ev.Source != "loot" || ev.Kind != quests.KindQuestComplete {
		t.Errorf("event = %s/%s, want loot/quest_complete", ev.Source, ev.Kind)
	}
	if ev.DedupeKey != "loot:quest_complete:"+quest.ID {
		t.Errorf("dedupe key = %q", ev.DedupeKey)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["quest_id"] != quest.ID || payload["title"] != "Make $100" {
		t.Errorf("payload = %v", payload)
	}
	if payload["scope"] != "week" {
		t.Errorf("scope = %v, want week", payload["scope"])
	}

	stored, err := st.GetQuest(ctx, quest.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != core.QuestCompleted || stored.XP != 200 {
		t.Fatalf("stored quest = %+v", stored)
	}

	// Checking again must not pay a second time.
	res, err = svc.Check(ctx)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if len(res.Completed) != 0 || len(rec.events) != 1 {
		t.Fatalf("a completed quest paid twice: %d completions, %d events",
			len(res.Completed), len(rec.events))
	}
	if b.seen["quests"] == 0 {
		t.Error("no quests message was published")
	}
}

// TestMonthlyCompletionIsEpicScope proves the scope the rules file matches on.
func TestMonthlyCompletionIsEpicScope(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{xp: 500}
	svc := newService(t, st, rec, newSpy())

	insert(t, st, ledgerRow("appstore", "2026-08-18", 12, 120, "as:1"))
	if _, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricRevenue, Target: 50, Window: "month",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("want one event, got %d", len(rec.events))
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.events[0].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["scope"] != "month" {
		t.Errorf("scope = %v, want month", payload["scope"])
	}
}

// TestExpiryIsSilent is the no-shame rule in test form: a window that ended
// unmet changes status and nothing else — no event, no drop, no sound.
func TestExpiryIsSilent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{xp: 200}
	svc := newService(t, st, rec, newSpy())

	if _, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricRevenue, Target: 1000, Window: "custom",
		Start: "2026-08-03", End: "2026-08-09", Title: "Last week's goal",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := svc.Check(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Expired) != 1 {
		t.Fatalf("want one expiry, got %d", len(res.Expired))
	}
	if len(rec.events) != 0 {
		t.Fatalf("an expiry made noise: %+v", rec.events)
	}

	board, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(board.Active) != 0 || len(board.Recent) != 1 {
		t.Fatalf("board = %d active, %d recent", len(board.Active), len(board.Recent))
	}
	if board.Recent[0].Status != core.QuestExpired {
		t.Errorf("status = %q, want expired", board.Recent[0].Status)
	}
}

func TestCreateCustomQuest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	q, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricUnits, Target: 40, App: "Notes", Window: "month",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if q.WindowStart != "2026-08-01" || q.WindowEnd != "2026-08-31" {
		t.Errorf("window = %s..%s, want the whole month", q.WindowStart, q.WindowEnd)
	}
	if q.Kind != core.QuestCustom {
		t.Errorf("kind = %q, want custom", q.Kind)
	}
	if q.Title != "40 units · Notes" {
		t.Errorf("default title = %q", q.Title)
	}

	if _, err := svc.Create(ctx, quests.CustomRequest{Metric: "vibes", Target: 5}); err == nil {
		t.Error("an unknown metric was accepted")
	}
	if _, err := svc.Create(ctx, quests.CustomRequest{Metric: core.MetricUnits, Target: 0}); err == nil {
		t.Error("a zero target was accepted")
	}
	if _, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricUnits, Target: 5, Window: "custom",
		Start: "2026-08-10", End: "2026-08-01",
	}); err == nil {
		t.Error("a backwards window was accepted")
	}

	// Only custom quests are deletable.
	if err := svc.Delete(ctx, q.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	auto := core.Quest{ID: core.NewID(), Kind: core.QuestAuto, Metric: core.MetricUnits,
		Target: 10, WindowStart: "2026-08-17", WindowEnd: "2026-08-23",
		Status: core.QuestActive, CreatedAt: wednesday, DedupeKey: "auto:units:x"}
	if _, err := st.InsertQuest(ctx, auto); err != nil {
		t.Fatalf("insert auto: %v", err)
	}
	if err := svc.Delete(ctx, auto.ID); err == nil {
		t.Error("a generated quest was deleted")
	}
}

func TestWindows(t *testing.T) {
	start, end := quests.WeekWindow(wednesday)
	if start != "2026-08-17" || end != "2026-08-23" {
		t.Errorf("week = %s..%s", start, end)
	}
	// A Sunday belongs to the week that started the Monday before it.
	sunday := time.Date(2026, 8, 23, 23, 0, 0, 0, time.UTC)
	start, end = quests.WeekWindow(sunday)
	if start != "2026-08-17" || end != "2026-08-23" {
		t.Errorf("sunday week = %s..%s", start, end)
	}
	start, end = quests.MonthWindow(wednesday)
	if start != "2026-08-01" || end != "2026-08-31" {
		t.Errorf("month = %s..%s", start, end)
	}
	if left := quests.DaysLeft("2026-08-19", "2026-08-23"); left != 5 {
		t.Errorf("days left = %d, want 5 (today counts)", left)
	}
	if left := quests.DaysLeft("2026-08-19", "2026-08-09"); left != 0 {
		t.Errorf("days left on a finished window = %d, want 0", left)
	}
}

// TestFixedTargetCountsFromNow: a "settle two new countries this month" quest
// generated mid-month must ask for two *more*, not pay out for a fortnight
// that already happened.
func TestFixedTargetCountsFromNow(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	occurred, _ := time.Parse(core.DayLayout, "2026-08-04")
	for _, country := range []string{"BR", "CL", "PE"} {
		insert(t, st, core.Event{
			ID: core.NewID(), Source: "loot", Kind: "settlement", App: "Notes",
			Day: "2026-08-04", OccurredAt: occurred, ObservedAt: occurred, Country: country,
			DedupeKey: "loot:settlement:" + country,
		})
	}

	created, err := newGenerator(st).Run(ctx)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var quest *core.Quest
	for i := range created {
		if created[i].Metric == core.MetricSettlements {
			quest = &created[i]
		}
	}
	if quest == nil {
		t.Fatalf("no settlement quest generated: %+v", created)
	}
	if quest.Target != 5 {
		t.Errorf("target = %v, want 5 (three already settled, plus two)", quest.Target)
	}

	progress, err := st.QuestProgress(ctx, *quest)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress >= quest.Target {
		t.Errorf("the quest was already met the moment it was written: %v of %v", progress, quest.Target)
	}
}

// The Monday problem: generation runs minutes after midnight, when every one
// of last week's quests is still active. Counting them towards the cap left no
// room for the new week's — and because `lastGen` latches for the day, the
// board stayed short until the next midnight. Expiry has to happen first.
func TestMondayGenerationRetiresLastWeekFirst(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Two weeks of history, so each week has a previous week to beat.
	insert(t, st,
		ledgerRow("appstore", "2026-08-05", 40, 200, "ls:1"),
		ledgerRow("appstore", "2026-08-12", 50, 250, "ls:2"),
		installRow("flathub", "2026-08-06", 400, "in:1"),
		installRow("flathub", "2026-08-13", 500, "in:2"),
	)

	g := newGenerator(st)
	// Revenue, units and installs are the only metrics with history, so three
	// is a full board.
	g.MaxActive = 3

	sunday := time.Date(2026, 8, 16, 18, 0, 0, 0, time.UTC)
	g.Now = func() time.Time { return sunday }
	created, err := g.Run(ctx)
	if err != nil {
		t.Fatalf("sunday generate: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("sunday created %d quests, want 3 (a full board)", len(created))
	}

	// Five past midnight on Monday, exactly when maybeGenerate fires.
	monday := time.Date(2026, 8, 17, 0, 5, 0, 0, time.UTC)
	g.Now = func() time.Time { return monday }
	created, err = g.Run(ctx)
	if err != nil {
		t.Fatalf("monday generate: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("monday created %d quests, want 3 — the board was left short", len(created))
	}
	for _, q := range created {
		if q.WindowStart != "2026-08-17" || q.WindowEnd != "2026-08-23" {
			t.Errorf("%s window = %s..%s, want the new week", q.Title, q.WindowStart, q.WindowEnd)
		}
	}

	active, err := st.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("%d active quests on Monday, want 3", len(active))
	}
	for _, q := range active {
		if q.WindowEnd < "2026-08-17" {
			t.Errorf("last week's quest %q is still active", q.Title)
		}
	}
}

// A quest window is a run of *local* days, so the completion Loot mints for
// itself has to be filed under the local day too. Stamped with the UTC day, a
// Sunday-evening completion west of Greenwich lands on Monday — outside the
// very window it just satisfied, where the XP quest running alongside it
// cannot see it.
func TestCompletionEventUsesTheLocalDay(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{xp: 200}
	svc := newService(t, st, rec, newSpy())

	// Sunday evening five hours behind UTC: locally the last day of the week,
	// in UTC already Monday.
	west := time.FixedZone("UTC-5", -5*60*60)
	sundayEvening := time.Date(2026, 8, 23, 21, 0, 0, 0, west)
	svc.Now = func() time.Time { return sundayEvening }
	svc.Generator.Now = svc.Now

	insert(t, st, ledgerRow("appstore", "2026-08-21", 12, 120, "as:west"))

	q, err := svc.Create(ctx, quests.CustomRequest{
		Metric: core.MetricRevenue, Target: 100, Window: "week", Title: "Make $100",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if q.WindowEnd != "2026-08-23" {
		t.Fatalf("window end = %q, want the local Sunday", q.WindowEnd)
	}

	if _, err := svc.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("want one event ingested, got %d", len(rec.events))
	}
	if got := rec.events[0].Day; got != "2026-08-23" {
		t.Errorf("completion day = %q, want the local day 2026-08-23", got)
	}
	if got := rec.events[0].Day; got > q.WindowEnd {
		t.Errorf("completion day %q falls outside its own window %s..%s", got, q.WindowStart, q.WindowEnd)
	}
}
