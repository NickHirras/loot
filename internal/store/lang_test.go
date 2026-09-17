package store_test

import (
	"context"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// A drop carries the rule that wrote it, the floor rule that relabelled it and
// the language it was written in, so a reader in another language can be shown
// the same sentence re-rendered rather than the stored one translated.
func TestDropRuleRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	ev := sampleEvent("rc:lang-1")
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	want := core.Drop{
		ID:        core.NewID(),
		EventID:   ev.ID,
		Rarity:    core.Uncommon,
		Title:     "New subscriber",
		Subtitle:  "9.99 USD",
		XP:        25,
		CreatedAt: ev.OccurredAt,
		Rule:      "revenuecat-purchase",
		FloorRule: "first-country",
		Lang:      "en",
	}
	if err := st.InsertDrop(ctx, want); err != nil {
		t.Fatalf("insert drop: %v", err)
	}

	drops, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("listed %d drops, want 1", len(drops))
	}
	got := drops[0]
	if got.Rule != want.Rule || got.FloorRule != want.FloorRule || got.Lang != want.Lang {
		t.Fatalf("rule/floor_rule/lang = %q/%q/%q, want %q/%q/%q",
			got.Rule, got.FloorRule, got.Lang, want.Rule, want.FloorRule, want.Lang)
	}

	// The event's payload comes back with the drop, because re-rendering a
	// title means re-running {{.Payload.…}} against it.
	if string(got.Payload) != string(ev.Payload) {
		t.Fatalf("payload = %q, want %q", got.Payload, ev.Payload)
	}
	if string(got.Event().Payload) != string(ev.Payload) {
		t.Fatalf("Event() lost the payload: %q", got.Event().Payload)
	}
}

// A drop stored before the columns existed reads back empty rather than
// guessing, which is what keeps it in the language it was written in forever.
func TestDropRuleDefaultsEmpty(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	ev := sampleEvent("rc:lang-2")
	if _, err := st.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	// Written the way migration 0006 and earlier wrote a drop: the new columns
	// are not mentioned at all, so the schema's defaults answer for them.
	if _, err := st.DB().ExecContext(ctx, `
        INSERT INTO drops (id, event_id, rarity, title, subtitle, xp, created_at, chest_date)
        VALUES ('DRLEGACY', ?, 'common', 'Renewal', '', 10, ?, '')`,
		ev.ID, ev.OccurredAt.UnixMilli()); err != nil {
		t.Fatalf("insert legacy drop: %v", err)
	}

	drops, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("listed %d drops, want 1", len(drops))
	}
	if got := drops[0]; got.Rule != "" || got.FloorRule != "" || got.Lang != "" {
		t.Fatalf("legacy drop = rule %q, floor %q, lang %q; want all empty",
			got.Rule, got.FloorRule, got.Lang)
	}
}

// The columns exist on a fresh database, with the right defaults.
func TestMigrationAddsRuleColumns(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	want := map[string]bool{"rule": false, "floor_rule": false, "lang": false}
	rows, err := st.DB().QueryContext(ctx, `SELECT name, "notnull", dflt_value FROM pragma_table_info('drops')`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name     string
			notNull  int
			deflt    any
			scanErr  = rows.Scan(&name, &notNull, &deflt)
			_, isNew = want[name]
		)
		if scanErr != nil {
			t.Fatalf("scan table info: %v", scanErr)
		}
		if !isNew {
			continue
		}
		if notNull != 1 {
			t.Fatalf("column %q is nullable, want NOT NULL", name)
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("migration did not add the %q column to drops", name)
		}
	}
}
