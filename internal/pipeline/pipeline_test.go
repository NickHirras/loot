package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newPipeline(t *testing.T) (*pipeline.Pipeline, *store.Store, *bus.Bus) {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}

	b := bus.New(64)
	p := pipeline.New(st, engine, b, quietLogger())
	p.ChestEnabled = true
	p.FX = fixedRates{"EUR": 0.8}
	return p, st, b
}

// fixedRates converts with a hand-written table: rate[X] is units of X per one
// USD, the same convention internal/fx uses.
type fixedRates map[string]float64

func (f fixedRates) Convert(amount float64, from, to string) (float64, bool) {
	if from == to {
		return amount, true
	}
	rate := func(cur string) (float64, bool) {
		if cur == "USD" {
			return 1, true
		}
		r, ok := f[cur]
		return r, ok
	}
	rf, ok := rate(from)
	if !ok {
		return 0, false
	}
	rt, ok := rate(to)
	if !ok {
		return 0, false
	}
	return amount / rf * rt, true
}

// listDrops is the feed's view: revealed and immediate drops only.
func listDrops(t *testing.T, st *store.Store) []store.DropView {
	t.Helper()
	drops, err := st.ListDrops(context.Background(), store.DropQuery{Limit: 50})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	return drops
}

func purchase(dedupe string) core.Event {
	now := time.Now().UTC()
	return core.Event{
		ID:         core.NewID(),
		Source:     "revenuecat",
		Kind:       "purchase",
		App:        "com.example.app",
		OccurredAt: now,
		ObservedAt: now,
		Country:    "US",
		Amount:     4.99,
		Currency:   "USD",
		Quantity:   1,
		DedupeKey:  dedupe,
		Payload:    []byte(`{"event":{"period_type":"NORMAL"}}`),
	}
}

func TestIngestCreatesDropAndPublishes(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	msgs, cancel := b.Subscribe()
	defer cancel()

	drop, err := p.Ingest(ctx, purchase("rc:1"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop == nil {
		t.Fatal("ingest returned no drop for a new event")
	}
	if drop.Rarity != core.Uncommon {
		t.Fatalf("rarity = %s, want uncommon", drop.Rarity)
	}

	select {
	case msg := <-msgs:
		if msg.Type != "drop" || msg.Drop == nil || msg.Drop.ID != drop.ID {
			t.Fatalf("published message = %+v", msg)
		}
		if msg.Event == nil || msg.Event.Source != "revenuecat" {
			t.Fatalf("published message is missing its event: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was published to the bus")
	}

	// The US had never been seen, so a settlement drop follows the purchase.
	select {
	case msg := <-msgs:
		if msg.Drop == nil || msg.Event == nil || msg.Event.Kind != "settlement" {
			t.Fatalf("second message = %+v, want the settlement", msg)
		}
		if msg.Drop.Rarity != core.Rare {
			t.Fatalf("settlement rarity = %s, want rare", msg.Drop.Rarity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no settlement was published")
	}

	if drops := listDrops(t, st); len(drops) != 2 {
		t.Fatalf("stored %d drops, want 2 (purchase + settlement)", len(drops))
	}
}

// TestIngestDuplicateCreatesNoDrop is the core guarantee: a redelivered webhook
// must not mint a second drop.
func TestIngestDuplicateCreatesNoDrop(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	msgs, cancel := b.Subscribe()
	defer cancel()

	if _, err := p.Ingest(ctx, purchase("rc:same")); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	<-msgs // drain the purchase
	<-msgs // drain the settlement it triggered

	// Same dedupe key, brand new event id, as a real retry would arrive.
	drop, err := p.Ingest(ctx, purchase("rc:same"))
	if err != nil {
		t.Fatalf("duplicate ingest returned an error: %v", err)
	}
	if drop != nil {
		t.Fatalf("duplicate produced a drop: %+v", drop)
	}

	select {
	case msg := <-msgs:
		t.Fatalf("duplicate published %+v to the bus", msg)
	case <-time.After(100 * time.Millisecond):
	}

	if drops := listDrops(t, st); len(drops) != 2 {
		t.Fatalf("stored %d drops, want 2 (purchase + settlement)", len(drops))
	}
	if n, err := st.EventCount(ctx, ""); err != nil || n != 2 {
		t.Fatalf("stored %d events, want 2 (%v)", n, err)
	}
}

func TestIngestFillsMissingFields(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := core.Event{Source: "mystery", Kind: "sighting"}
	drop, err := p.Ingest(ctx, ev)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop == nil {
		t.Fatal("no drop")
	}

	drops := listDrops(t, st)
	if len(drops) != 1 {
		t.Fatalf("stored %d drops, want 1", len(drops))
	}
	if drops[0].OccurredAt.IsZero() {
		t.Error("occurred_at was left unset")
	}
	if drops[0].Day == "" {
		t.Error("day was left unset")
	}
}

func TestSilentEventMakesNoDrop(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := purchase("rc:quiet")
	ev.Country = "" // no country: nothing to settle, so nothing at all should drop
	ev.Silent = true

	drop, err := p.Ingest(ctx, ev)
	if err != nil {
		t.Fatalf("silent ingest: %v", err)
	}
	if drop != nil {
		t.Fatalf("silent event produced a drop: %+v", drop)
	}
	if n, err := st.EventCount(ctx, ""); err != nil || n != 1 {
		t.Fatalf("stored %d events, want 1 (%v)", n, err)
	}
	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("silent ingest created %d drops, want 0", len(drops))
	}
}

func TestIngestSilentlyStoresWithoutDrop(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := purchase("rc:quiet")
	ev.Country = ""
	if err := p.IngestSilently(ctx, ev); err != nil {
		t.Fatalf("silent ingest: %v", err)
	}

	if n, err := st.EventCount(ctx, ""); err != nil || n != 1 {
		t.Fatalf("stored %d events, want 1 (%v)", n, err)
	}
	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("silent ingest created %d drops, want 0", len(drops))
	}
}

// stubSource is a polling source with scripted behaviour.
type stubSource struct {
	events   []core.Event
	state    []byte
	err      error
	gotState []byte
	polls    int
}

func (s *stubSource) Name() string { return "stub" }

func (s *stubSource) Poll(_ context.Context, state []byte) ([]core.Event, []byte, error) {
	s.polls++
	s.gotState = state
	return s.events, s.state, s.err
}

func (s *stubSource) PollInterval() time.Duration { return time.Hour }

func TestSchedulerPollOnce(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	src := &stubSource{
		events: []core.Event{purchase("rc:poll-1"), purchase("rc:poll-2")},
		state:  []byte(`{"cursor":"abc"}`),
	}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())

	sched.PollOnce(ctx, src)

	if src.polls != 1 {
		t.Fatalf("polled %d times, want 1", src.polls)
	}
	if src.gotState != nil {
		t.Fatalf("first poll received state %q, want nil", src.gotState)
	}

	// Two purchases plus the settlement the first one triggered.
	if drops := listDrops(t, st); len(drops) != 3 {
		t.Fatalf("got %d drops, want 3", len(drops))
	}

	saved, err := st.GetSourceState(ctx, "stub")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if string(saved) != `{"cursor":"abc"}` {
		t.Fatalf("saved state = %q", saved)
	}

	states, err := st.SourceStates(ctx)
	if err != nil {
		t.Fatalf("source states: %v", err)
	}
	if states["stub"].LastPollAt == nil {
		t.Fatal("last_poll_at was not recorded")
	}
	if states["stub"].LastError != "" {
		t.Fatalf("last_error = %q, want empty", states["stub"].LastError)
	}

	// A second cycle re-delivering the same events must not add drops, and the
	// stored cursor must be handed back to the source.
	sched.PollOnce(ctx, src)
	if string(src.gotState) != `{"cursor":"abc"}` {
		t.Fatalf("second poll received state %q", src.gotState)
	}
	if drops := listDrops(t, st); len(drops) != 3 {
		t.Fatalf("after replay: %d drops, want 3", len(drops))
	}
}

func TestSchedulerRecordsPollError(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	failing := errors.New("flathub is down")
	src := &stubSource{
		// A partial failure still delivers what it managed to fetch.
		events: []core.Event{purchase("rc:partial")},
		err:    failing,
	}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())
	sched.PollOnce(ctx, src)

	states, err := st.SourceStates(ctx)
	if err != nil {
		t.Fatalf("source states: %v", err)
	}
	if states["stub"].LastError != failing.Error() {
		t.Fatalf("last_error = %q, want %q", states["stub"].LastError, failing)
	}

	if drops := listDrops(t, st); len(drops) != 2 {
		t.Fatalf("got %d drops, want the partial result and its settlement", len(drops))
	}
}

func TestSchedulerRunStopsOnContextCancel(t *testing.T) {
	p, st, _ := newPipeline(t)
	src := &stubSource{}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop when the context was cancelled")
	}
}

// salesDay is the shape a ledger source emits once per (app, day): a non-silent
// summary that asks to be held for the chest.
func salesDay(app, day string, units int, amount float64, currency string) core.Event {
	return core.Event{
		Source:    "appstore",
		Kind:      "sales_day",
		App:       app,
		Day:       day,
		Amount:    amount,
		Currency:  currency,
		Quantity:  units,
		IsLedger:  true,
		Chest:     true,
		DedupeKey: "appstore:sales_day:" + app + ":" + day,
	}
}

func TestChestHoldsDropsOutOfTheFeed(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	msgs, cancel := b.Subscribe()
	defer cancel()

	drop, err := p.Ingest(ctx, salesDay("com.example.app", "2026-08-17", 12, 40, "USD"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop.ChestDate != "2026-08-17" {
		t.Fatalf("chest_date = %q, want the event's day", drop.ChestDate)
	}

	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("a chest drop leaked into the feed: %+v", drops)
	}

	// The badge update is the only thing that should reach the bus.
	select {
	case msg := <-msgs:
		if msg.Type != "chest" || len(msg.Chests) != 1 || msg.Chests[0].Count != 1 {
			t.Fatalf("bus message = %+v, want a chest summary", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no chest message was published")
	}
	select {
	case msg := <-msgs:
		t.Fatalf("a held drop was published anyway: %+v", msg)
	case <-time.After(100 * time.Millisecond):
	}

	// Stats must not spoil the chest either.
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalDrops != 0 || stats.TotalXP != 0 {
		t.Fatalf("stats counted a held drop: %+v", stats)
	}
	if stats.UnrevealedCount != 1 || len(stats.ChestDates) != 1 {
		t.Fatalf("stats did not report the waiting chest: %+v", stats)
	}
}

func TestRevealChestCascadesInOrder(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	// A week of quiet days for app c first, so its next day can be a record
	// (a series needs store.MinRecordHistory prior days before "best ever"
	// counts).
	for d := 1; d <= store.MinRecordHistory; d++ {
		if _, err := p.Ingest(ctx, salesDay("com.example.c", fmt.Sprintf("2026-08-%02d", d), 5, 10, "USD")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Three drops in one chest, deliberately minted out of reveal order.
	for i, ev := range []core.Event{
		salesDay("com.example.a", "2026-08-17", 5, 10, "USD"),    // common
		salesDay("com.example.b", "2026-08-17", 50, 500, "USD"),  // rare
		salesDay("com.example.c", "2026-08-17", 5000, 50, "USD"), // epic: best day ever
	} {
		if _, err := p.Ingest(ctx, ev); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	msgs, cancel := b.Subscribe()
	defer cancel()

	revealed, err := p.RevealChest(ctx, "2026-08-17")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if len(revealed) != 3 {
		t.Fatalf("revealed %d drops, want 3", len(revealed))
	}

	// Ascending reveal rank: the cascade climbs towards the best news.
	for i := 1; i < len(revealed); i++ {
		prev, cur := revealed[i-1].Rarity, revealed[i].Rarity
		if core.RevealRank(cur) < core.RevealRank(prev) {
			t.Fatalf("reveal order %s before %s is downhill", prev, cur)
		}
	}
	if revealed[len(revealed)-1].Rarity != core.Epic {
		t.Fatalf("cascade ends on %s, want the epic", revealed[len(revealed)-1].Rarity)
	}

	// Revealed drops now show in the feed, and each carries revealed_at.
	feed := listDrops(t, st)
	if len(feed) != 3 {
		t.Fatalf("feed has %d drops after the reveal, want 3 (the 16th is still shut)", len(feed))
	}
	for _, d := range feed {
		if d.RevealedAt == nil {
			t.Fatalf("drop %s has no revealed_at", d.ID)
		}
	}

	// Every revealed drop is republished, flagged as coming from a chest.
	seen := 0
	deadline := time.After(5 * time.Second)
	for seen < 3 {
		select {
		case msg := <-msgs:
			if msg.Type != "drop" {
				continue
			}
			if !msg.Chest {
				t.Fatalf("revealed drop was not marked chest:true: %+v", msg)
			}
			seen++
		case <-deadline:
			t.Fatalf("only %d of 3 drops cascaded onto the bus", seen)
		}
	}

	// A second open finds nothing.
	again, err := p.RevealChest(ctx, "2026-08-17")
	if err != nil {
		t.Fatalf("second reveal: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("reopening an empty chest returned %d drops", len(again))
	}
}

func TestRevealOldestChestByDefault(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newPipeline(t)

	if _, err := p.Ingest(ctx, salesDay("com.example.app", "2026-08-16", 3, 9, "USD")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := p.Ingest(ctx, salesDay("com.example.app", "2026-08-18", 4, 9, "USD")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	drops, err := p.RevealChest(ctx, "")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if len(drops) != 1 || drops[0].ChestDate != "2026-08-16" {
		t.Fatalf("opened %+v, want the 2026-08-16 chest", drops)
	}
}

func TestChestDisabledPublishesImmediately(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)
	p.ChestEnabled = false

	drop, err := p.Ingest(ctx, salesDay("com.example.app", "2026-08-17", 12, 40, "USD"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop.ChestDate != "" {
		t.Fatalf("chest_date = %q with chests disabled", drop.ChestDate)
	}
	if drops := listDrops(t, st); len(drops) != 1 {
		t.Fatalf("feed has %d drops, want 1", len(drops))
	}
}

func TestAutoOpenOnlyOpensStaleChests(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)
	p.ChestAutoOpenAfterHours = 36
	// 2026-08-18 10:00 UTC: the 17th's chest is 34h old, the 16th's is 58h.
	p.Now = func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC) }

	for _, day := range []string{"2026-08-16", "2026-08-17"} {
		if _, err := p.Ingest(ctx, salesDay("com.example.app", day, 3, 9, "USD")); err != nil {
			t.Fatalf("ingest %s: %v", day, err)
		}
	}

	p.OpenDueChests(ctx)

	chests, err := st.ChestSummaries(ctx)
	if err != nil {
		t.Fatalf("chest summaries: %v", err)
	}
	if len(chests) != 1 || chests[0].Date != "2026-08-17" {
		t.Fatalf("chests left = %+v, want only 2026-08-17", chests)
	}
}

func TestAmountBaseConversionAtIngest(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t) // display currency USD, EUR at 0.8

	ev := purchase("rc:eur")
	ev.Country = ""
	ev.Amount = 8
	ev.Currency = "EUR"

	if _, err := p.Ingest(ctx, ev); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	drops := listDrops(t, st)
	if len(drops) != 1 {
		t.Fatalf("got %d drops, want 1", len(drops))
	}
	if got := drops[0].AmountBase; got != 10 {
		t.Fatalf("amount_base = %v, want 10 (8 EUR at 0.8)", got)
	}
	if drops[0].Amount != 8 || drops[0].Currency != "EUR" {
		t.Fatalf("the original amount was rewritten: %+v", drops[0])
	}
}

func TestAmountBaseUnknownCurrencyStaysZero(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := purchase("rc:xyz")
	ev.Country = ""
	ev.Amount = 8
	ev.Currency = "XYZ"

	if _, err := p.Ingest(ctx, ev); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	drops := listDrops(t, st)
	if len(drops) != 1 || drops[0].AmountBase != 0 {
		t.Fatalf("amount_base = %v, want 0 for an unknown currency", drops[0].AmountBase)
	}
}

func TestSettlementEmittedOncePerCountryIncludingSilentEvents(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	// A silent ledger row is still allowed to found a settlement.
	row := core.Event{
		Source:    "appstore",
		Kind:      "sale",
		App:       "com.example.app",
		Day:       "2026-08-17",
		Country:   "jp",
		Amount:    3,
		Currency:  "USD",
		Quantity:  1,
		IsLedger:  true,
		Silent:    true,
		DedupeKey: "appstore:2026-08-17:jp:1",
	}
	if _, err := p.Ingest(ctx, row); err != nil {
		t.Fatalf("ingest row: %v", err)
	}

	// The settlement is real, but it belongs to the report day's chest rather
	// than to the live feed: a silent ledger row is backfilled history.
	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("a backfilled settlement leaked into the feed: %+v", drops)
	}
	chests, err := st.ListChest(ctx)
	if err != nil {
		t.Fatalf("list chest: %v", err)
	}
	if len(chests) != 1 || len(chests[0].Drops) != 1 {
		t.Fatalf("chest = %+v, want exactly the settlement", chests)
	}
	held := chests[0].Drops[0]
	if chests[0].Date != "2026-08-17" {
		t.Fatalf("settlement filed under %s, want the row's own day", chests[0].Date)
	}
	if held.Kind != "settlement" || held.Country != "JP" {
		t.Fatalf("drop = %+v, want a JP settlement", held)
	}
	if held.Rarity != core.Rare {
		t.Fatalf("settlement rarity = %s, want rare", held.Rarity)
	}
	if !strings.Contains(held.Subtitle, "appstore") {
		t.Fatalf("subtitle = %q, want the source that found the country", held.Subtitle)
	}

	// A second event from the same country settles nothing new.
	row2 := row
	row2.ID = ""
	row2.DedupeKey = "appstore:2026-08-17:jp:2"
	if _, err := p.Ingest(ctx, row2); err != nil {
		t.Fatalf("ingest second row: %v", err)
	}
	chests, err = st.ListChest(ctx)
	if err != nil {
		t.Fatalf("list chest: %v", err)
	}
	if len(chests) != 1 || chests[0].Count != 1 {
		t.Fatalf("chest = %+v, want the settlement to stay unique", chests)
	}
}

// A silent ledger row is a backfill, so the settlement it reveals is
// chest-bound under the row's own day — this is the bug that turned a 30 day
// App Store backfill into forty immediate rare drops. A realtime event is not
// silent, so its settlement still lands live.
func TestSettlementFromSilentRowIsChestBoundButRealtimeStaysLive(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	silent := core.Event{
		Source:    "appstore",
		Kind:      "sale",
		App:       "com.example.app",
		Day:       "2026-07-04",
		Country:   "SE",
		Amount:    3,
		Currency:  "USD",
		Quantity:  1,
		IsLedger:  true,
		Silent:    true,
		DedupeKey: "appstore:2026-07-04:se:1",
	}
	if _, err := p.Ingest(ctx, silent); err != nil {
		t.Fatalf("ingest silent row: %v", err)
	}
	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("silent-row settlement went straight to the feed: %+v", drops)
	}
	chests, err := st.ListChest(ctx)
	if err != nil {
		t.Fatalf("list chest: %v", err)
	}
	if len(chests) != 1 || chests[0].Date != "2026-07-04" {
		t.Fatalf("chests = %+v, want one for the row's day", chests)
	}

	// A live RevenueCat purchase from a new country is news right now.
	live := purchase("rc:se-live")
	live.Country = "PT"
	if _, err := p.Ingest(ctx, live); err != nil {
		t.Fatalf("ingest realtime: %v", err)
	}
	var settled bool
	for _, d := range listDrops(t, st) {
		if d.Kind == "settlement" && d.Country == "PT" {
			if d.ChestDate != "" {
				t.Fatalf("realtime settlement was held for chest %s", d.ChestDate)
			}
			settled = true
		}
	}
	if !settled {
		t.Fatal("a realtime purchase from a new country did not settle live")
	}
}

func TestSettlementInheritsTheChestHint(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := salesDay("com.example.app", "2026-08-17", 5, 10, "USD")
	ev.Country = "NZ"
	if _, err := p.Ingest(ctx, ev); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if drops := listDrops(t, st); len(drops) != 0 {
		t.Fatalf("a chest-bound settlement leaked into the feed: %+v", drops)
	}
	chests, err := st.ListChest(ctx)
	if err != nil {
		t.Fatalf("list chest: %v", err)
	}
	if len(chests) != 1 || chests[0].Count != 2 {
		t.Fatalf("chest = %+v, want the summary and its settlement", chests)
	}
}

// A source that reports an amount without saying which currency it is in is
// reporting money of unknown denomination, not money in the display currency.
// Assuming the latter quietly added a foreign report's figures to the vault at
// par: plausible, wrong, and invisible.
func TestAmountWithNoCurrencyDoesNotReachTheVault(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := core.Event{
		Source:    "webhook",
		Kind:      "sale",
		App:       "com.example.app",
		Day:       "2026-08-17",
		Amount:    42,
		Currency:  "",
		Quantity:  1,
		IsLedger:  true,
		DedupeKey: "webhook:nocurrency:1",
	}
	if _, err := p.Ingest(ctx, ev); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	drops := listDrops(t, st)
	if len(drops) != 1 {
		t.Fatalf("got %d drops, want 1", len(drops))
	}
	if drops[0].Amount != 42 {
		t.Errorf("amount = %v, want the reported 42 kept as-is", drops[0].Amount)
	}
	if drops[0].AmountBase != 0 {
		t.Errorf("amount_base = %v, want 0: nobody said what currency this is", drops[0].AmountBase)
	}

	vault, err := st.VaultSummary(ctx, "2026-08-01", "2026-08-31", "USD", "2026-08-31")
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}
	if vault.Totals.RevenueBase != 0 {
		t.Errorf("vault revenue = %v, want 0", vault.Totals.RevenueBase)
	}
	// The units are still counted: something was sold, we just do not know
	// what it was worth.
	if vault.Totals.Units != 1 {
		t.Errorf("vault units = %d, want 1", vault.Totals.Units)
	}

	// And an amount that *does* name its currency still converts.
	priced := ev
	priced.ID = ""
	priced.Currency = "EUR"
	priced.DedupeKey = "webhook:eur:1"
	priced.Amount = 8
	if _, err := p.Ingest(ctx, priced); err != nil {
		t.Fatalf("ingest priced: %v", err)
	}
	vault, err = st.VaultSummary(ctx, "2026-08-01", "2026-08-31", "USD", "2026-08-31")
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}
	if vault.Totals.RevenueBase != 10 {
		t.Errorf("vault revenue = %v, want 10 (8 EUR at 0.8)", vault.Totals.RevenueBase)
	}
}
