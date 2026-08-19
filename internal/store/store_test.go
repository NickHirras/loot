package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sampleEvent(dedupe string) core.Event {
	now := time.Now().UTC()
	return core.Event{
		ID:         core.NewID(),
		Source:     "revenuecat",
		Kind:       "purchase",
		App:        "com.example.app",
		OccurredAt: now,
		ObservedAt: now,
		Country:    "US",
		Amount:     9.99,
		Currency:   "USD",
		Quantity:   1,
		DedupeKey:  dedupe,
		Payload:    []byte(`{"hello":"world"}`),
	}
}

func TestInsertEventDedupe(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	first := sampleEvent("rc:evt-1")
	exists, err := st.InsertEvent(ctx, first)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if exists {
		t.Fatal("first insert reported the event as already existing")
	}

	// A retry of the same webhook: different event id, same dedupe key.
	second := sampleEvent("rc:evt-1")
	exists, err = st.InsertEvent(ctx, second)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if !exists {
		t.Fatal("duplicate dedupe_key was not reported as existing")
	}

	n, err := st.EventCount(ctx, "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("event count = %d, want 1 (duplicate must not be stored)", n)
	}
}

func TestInsertEventRequiresKeys(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	ev := sampleEvent("")
	if _, err := st.InsertEvent(ctx, ev); err == nil {
		t.Fatal("expected an error for an empty dedupe key")
	}

	ev = sampleEvent("k")
	ev.ID = ""
	if _, err := st.InsertEvent(ctx, ev); err == nil {
		t.Fatal("expected an error for an empty event id")
	}
}

func TestCountryEventCount(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	if n, err := st.CountryEventCount(ctx, "FR"); err != nil || n != 0 {
		t.Fatalf("empty store: got %d, %v; want 0, nil", n, err)
	}

	ev := sampleEvent("a")
	ev.Country = "fr" // stored uppercased
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	n, err := st.CountryEventCount(ctx, "FR")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("country count = %d, want 1", n)
	}

	// Country matching must be case-insensitive on the way in as well.
	if n, err := st.CountryEventCount(ctx, "fr"); err != nil || n != 1 {
		t.Fatalf("lowercase lookup: got %d, %v; want 1, nil", n, err)
	}
}

func TestIsRecordQuantity(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	mk := func(day string, qty int) core.Event {
		ev := sampleEvent("flathub:app:" + day)
		ev.Source = "flathub"
		ev.Kind = "install"
		ev.App = "org.example.App"
		ev.Country = ""
		ev.Quantity = qty
		return ev
	}

	// The very first day has no history to beat, so it is not a record.
	first := mk("2026-01-01", 100)
	if _, err := st.InsertEvent(ctx, first); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if ok, err := st.IsRecordQuantity(ctx, first); err != nil || ok {
		t.Fatalf("first ever day: got %v, %v; want false, nil", ok, err)
	}

	lower := mk("2026-01-02", 50)
	if _, err := st.InsertEvent(ctx, lower); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if ok, err := st.IsRecordQuantity(ctx, lower); err != nil || ok {
		t.Fatalf("lower day: got %v, %v; want false, nil", ok, err)
	}

	best := mk("2026-01-03", 101)
	if _, err := st.InsertEvent(ctx, best); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ok, err := st.IsRecordQuantity(ctx, best)
	if err != nil {
		t.Fatalf("record check: %v", err)
	}
	if !ok {
		t.Fatal("101 installs should beat the previous best of 100")
	}

	// A tie is not a record.
	tie := mk("2026-01-04", 101)
	if _, err := st.InsertEvent(ctx, tie); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if ok, err := st.IsRecordQuantity(ctx, tie); err != nil || ok {
		t.Fatalf("tie: got %v, %v; want false, nil", ok, err)
	}
}

func TestListDropsAndStats(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	rarities := []core.Rarity{core.Common, core.Rare, core.Legendary}
	var ids []string
	for i, r := range rarities {
		ev := sampleEvent("evt-" + string(rune('a'+i)))
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert event: %v", err)
		}
		d := core.Drop{
			ID:        core.NewID(),
			EventID:   ev.ID,
			Rarity:    r,
			Title:     "drop " + string(r),
			XP:        10,
			CreatedAt: time.Now().UTC(),
		}
		if err := st.InsertDrop(ctx, d); err != nil {
			t.Fatalf("insert drop: %v", err)
		}
		ids = append(ids, d.ID)
		time.Sleep(2 * time.Millisecond) // keep ULIDs strictly ordered
	}

	drops, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(drops) != 3 {
		t.Fatalf("got %d drops, want 3", len(drops))
	}
	if drops[0].ID != ids[2] {
		t.Fatalf("newest drop = %s, want %s (newest first)", drops[0].ID, ids[2])
	}
	if drops[0].Source != "revenuecat" || drops[0].Country != "US" {
		t.Fatalf("event fields not joined: %+v", drops[0])
	}

	// Pagination: everything strictly older than the newest.
	page, err := st.ListDrops(ctx, store.DropQuery{Before: ids[2]})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("paged list returned %d, want 2", len(page))
	}

	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalDrops != 3 || stats.TotalXP != 30 {
		t.Fatalf("stats totals = %d drops / %d xp, want 3 / 30", stats.TotalDrops, stats.TotalXP)
	}
	if stats.ByRarity["legendary"] != 1 || stats.ByRarity["uncommon"] != 0 {
		t.Fatalf("by_rarity = %v", stats.ByRarity)
	}
	if stats.BySource["revenuecat"] != 3 {
		t.Fatalf("by_source = %v", stats.BySource)
	}
	if stats.CountriesCount != 1 || stats.Countries[0] != "US" {
		t.Fatalf("countries = %v", stats.Countries)
	}
}

func TestSourceState(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	blob, err := st.GetSourceState(ctx, "flathub")
	if err != nil || blob != nil {
		t.Fatalf("unknown source: got %v, %v; want nil, nil", blob, err)
	}

	if err := st.SetSourceState(ctx, "flathub", []byte(`{"last_date":{"a":"2026-01-01"}}`)); err != nil {
		t.Fatalf("set state: %v", err)
	}
	blob, err = st.GetSourceState(ctx, "flathub")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if string(blob) != `{"last_date":{"a":"2026-01-01"}}` {
		t.Fatalf("state round trip = %s", blob)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := st.RecordPoll(ctx, "flathub", now, context.DeadlineExceeded); err != nil {
		t.Fatalf("record poll: %v", err)
	}

	states, err := st.SourceStates(ctx)
	if err != nil {
		t.Fatalf("source states: %v", err)
	}
	got := states["flathub"]
	if got.LastError != context.DeadlineExceeded.Error() {
		t.Fatalf("last_error = %q", got.LastError)
	}
	if got.LastPollAt == nil || !got.LastPollAt.Equal(now) {
		t.Fatalf("last_poll_at = %v, want %v", got.LastPollAt, now)
	}

	// Recording a poll must not clobber the cursor.
	blob, err = st.GetSourceState(ctx, "flathub")
	if err != nil || len(blob) == 0 {
		t.Fatalf("cursor lost after RecordPoll: %v, %v", blob, err)
	}

	// A successful poll clears the previous error.
	if err := st.RecordPoll(ctx, "flathub", now, nil); err != nil {
		t.Fatalf("record poll: %v", err)
	}
	states, _ = st.SourceStates(ctx)
	if states["flathub"].LastError != "" {
		t.Fatalf("last_error not cleared: %q", states["flathub"].LastError)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loot.db")

	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	ev := sampleEvent("k1")
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert: %v", err)
	}
	st.Close()

	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	if n, err := st2.EventCount(ctx, ""); err != nil || n != 1 {
		t.Fatalf("after reopen: %d events, %v; want 1", n, err)
	}
}
