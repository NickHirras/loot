package pipeline_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func audienceTotal(day string, qty int) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		Source:     "snapcraft",
		Kind:       core.KindAudience,
		App:        "tuner",
		OccurredAt: occurred,
		Day:        day,
		Quantity:   qty,
		Silent:     true,
		DedupeKey:  fmt.Sprintf("snapcraft:audience:tuner:%s", day),
	}
}

func milestones(t *testing.T, st *store.Store) []int {
	t.Helper()
	var out []int
	rows, err := st.DB().Query(`SELECT quantity FROM events WHERE kind = ? ORDER BY occurred_at, quantity`,
		core.KindAudienceMilestone)
	if err != nil {
		t.Fatalf("query milestones: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var q int
		if err := rows.Scan(&q); err != nil {
			t.Fatal(err)
		}
		out = append(out, q)
	}
	return out
}

// A snapshot is silent — a level is not news — but reaching a rung of the
// ladder is, once, in that day's chest.
func TestAudienceMilestoneIsMintedOncePerRungAndOnlyUpwards(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	steps := []struct {
		day  string
		qty  int
		want []int
	}{
		// Wiring up an app that already has 170 devices says "150" once; it
		// does not replay 5, 10, 25… on the way there.
		{"2026-11-10", 170, []int{150}},
		{"2026-11-11", 172, []int{150}},
		// A dip below the rung, and the rung beneath it is not "reached".
		{"2026-11-12", 140, []int{150}},
		{"2026-11-13", 160, []int{150}},
		{"2026-11-14", 204, []int{150, 200}},
	}
	for _, s := range steps {
		drop, err := p.Ingest(ctx, audienceTotal(s.day, s.qty))
		if err != nil {
			t.Fatalf("%s: %v", s.day, err)
		}
		if drop != nil {
			t.Fatalf("%s: the snapshot itself produced a drop: %+v", s.day, drop)
		}
		if got := milestones(t, st); fmt.Sprint(got) != fmt.Sprint(s.want) {
			t.Fatalf("after %s (%d devices) milestones = %v, want %v", s.day, s.qty, got, s.want)
		}
	}

	// The feed's own view hides them, which is the point: they wait in a chest.
	if feed := listDrops(t, st); len(feed) != 0 {
		t.Fatalf("%d milestone drops reached the live feed", len(feed))
	}
	drops, err := st.ListDrops(ctx, store.DropQuery{Limit: 50, IncludeUnrevealed: true})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 2 {
		t.Fatalf("drops = %d, want one per milestone", len(drops))
	}
	for _, d := range drops {
		if d.ChestDate == "" {
			t.Errorf("milestone %q went straight to the feed; it belongs in its day's chest", d.Title)
		}
		if d.Rarity != core.Epic {
			t.Errorf("milestone %q is %s, want epic for a three-figure audience", d.Title, d.Rarity)
		}
	}
}

// Per-country rows are parts of the total, not audiences of their own.
func TestAudienceMilestoneIgnoresCountryRows(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := audienceTotal("2026-11-10", 500)
	ev.Country = "US"
	ev.DedupeKey += ":US"
	if _, err := p.Ingest(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := milestones(t, st); len(got) != 0 {
		t.Errorf("a country row minted milestones: %v", got)
	}
}
