package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// codexDrop stores one event and its drop. A non-empty chestDate with no
// reveal is a drop still sealed in an unopened chest.
func codexDrop(t *testing.T, st *store.Store, day string, at time.Time,
	rarity core.Rarity, chestDate string, dedupe string,
) {
	t.Helper()
	ctx := context.Background()
	ev := core.Event{
		ID: core.NewID(), Source: "appstore", Kind: "sale", App: "Notes",
		Day: day, OccurredAt: at, ObservedAt: at, Quantity: 1,
		DedupeKey: dedupe,
	}
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := st.InsertDrop(ctx, core.Drop{
		ID: core.NewID(), EventID: ev.ID, Rarity: rarity, Title: "drop",
		XP: 10, CreatedAt: at, ChestDate: chestDate,
	}); err != nil {
		t.Fatalf("insert drop: %v", err)
	}
}

// "Cursed but unbowed" is a trophy for a turnaround you *watched* happen, so
// both halves of it have to be drops that have actually been seen. Every other
// Codex query already filters unrevealed drops out; this one did not, so a
// cursed drop and a legendary one sitting side by side in an unopened chest
// awarded a turnaround nobody had witnessed.
func TestCursedRecoveryIgnoresUnopenedChests(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	// A cursed drop and its recovery, both still sealed in today's chest.
	codexDrop(t, st, "2026-08-18", base, core.Cursed, "2026-08-19", "sealed:cursed")
	codexDrop(t, st, "2026-08-18", base.Add(time.Hour), core.Legendary, "2026-08-19", "sealed:legendary")

	agg, err := st.CodexAggregates(ctx)
	if err != nil {
		t.Fatalf("aggregates: %v", err)
	}
	if agg.CursedRecoveryDay != "" {
		t.Fatalf("cursed recovery = %q, want none — nobody has opened the chest",
			agg.CursedRecoveryDay)
	}

	// The same pair, in the feed rather than a chest.
	open := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	codexDrop(t, st, "2026-08-12", open, core.Cursed, "", "open:cursed")
	codexDrop(t, st, "2026-08-12", open.Add(2*time.Hour), core.Rare, "", "open:rare")

	agg, err = st.CodexAggregates(ctx)
	if err != nil {
		t.Fatalf("aggregates: %v", err)
	}
	if agg.CursedRecoveryDay != "2026-08-12" {
		t.Errorf("cursed recovery = %q, want 2026-08-12", agg.CursedRecoveryDay)
	}
}
