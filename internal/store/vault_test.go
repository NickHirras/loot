package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// ledgerRow is a silent detail row from a store's financial report: the only
// kind of event the vault counts as revenue.
func ledgerRow(source, app, day, country string, units int, base float64, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID:         core.NewID(),
		Source:     source,
		Kind:       "sale",
		App:        app,
		Day:        day,
		OccurredAt: occurred,
		ObservedAt: occurred,
		Country:    country,
		Amount:     base,
		AmountBase: base,
		Currency:   "USD",
		Quantity:   units,
		IsLedger:   true,
		Silent:     true,
		DedupeKey:  dedupe,
	}
}

func TestVaultSummary(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	today := core.DayOf(time.Now().UTC())
	yesterday := core.DayOf(time.Now().UTC().AddDate(0, 0, -1))
	lastWeek := core.DayOf(time.Now().UTC().AddDate(0, 0, -9))

	events := []core.Event{
		ledgerRow("appstore", "com.example.a", today, "US", 10, 100, "as:1"),
		ledgerRow("appstore", "com.example.a", yesterday, "DE", 5, 50, "as:2"),
		ledgerRow("googleplay", "com.example.b", today, "US", 4, 25, "gp:1"),
		// A refund: negative quantity and a negative amount, so revenue nets.
		ledgerRow("appstore", "com.example.a", today, "US", -2, -20, "as:3"),
		// A free download: ledger (feeds settlements) but never a paid unit.
		func() core.Event {
			e := ledgerRow("appstore", "com.example.a", today, "FR", 7, 0, "as:dl")
			e.Kind = "download"
			return e
		}(),
		// Outside the 7 day window: it belongs to prev_totals, not totals.
		ledgerRow("appstore", "com.example.a", lastWeek, "US", 100, 999, "as:old"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// The summary event that rolls up today's rows must not be counted twice.
	summary := ledgerRow("appstore", "com.example.a", today, "", 8, 80, "as:sales_day")
	summary.Kind = "sales_day"
	summary.Silent = false
	summary.Chest = true
	if _, err := st.InsertEvent(ctx, summary); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	// RevenueCat is sighted, not banked: its money must stay out of revenue.
	rc := core.Event{
		ID: core.NewID(), Source: "revenuecat", Kind: "purchase", App: "com.example.a",
		Day: today, OccurredAt: time.Now().UTC(), ObservedAt: time.Now().UTC(),
		Country: "JP", Amount: 49.99, AmountBase: 49.99, Currency: "USD", Quantity: 1,
		DedupeKey: "rc:1",
	}
	if _, err := st.InsertEvent(ctx, rc); err != nil {
		t.Fatalf("insert rc: %v", err)
	}

	// And a subscription snapshot, which the vault reports separately.
	snap := core.Event{
		ID: core.NewID(), Source: "revenuecat", Kind: "subscription_snapshot",
		App: "com.example.a", Day: yesterday, OccurredAt: time.Now().UTC(),
		ObservedAt: time.Now().UTC(), Quantity: 412, DedupeKey: "rc:snap",
	}
	if _, err := st.InsertEvent(ctx, snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}

	from := core.DayOf(time.Now().UTC().AddDate(0, 0, -6))
	sum, err := st.VaultSummary(ctx, from, today, "USD", today)
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}

	if sum.Range.Days != 7 || sum.Range.To != today || sum.Range.From != from {
		t.Fatalf("range = %+v", sum.Range)
	}
	if sum.DisplayCurrency != "USD" {
		t.Fatalf("display currency = %q", sum.DisplayCurrency)
	}

	// 100 + 50 + 25 - 20 = 155. The RC purchase (49.99) and the sales_day
	// rollup (80) are both excluded.
	if sum.Totals.RevenueBase != 155 {
		t.Fatalf("revenue = %v, want 155 (no RevenueCat, no double-counted summary)", sum.Totals.RevenueBase)
	}
	if sum.Totals.Units != 19 {
		t.Fatalf("units = %d, want 19 (10 + 5 + 4, refunds and free downloads excluded)", sum.Totals.Units)
	}
	if sum.Totals.Refunds != 2 {
		t.Fatalf("refunds = %d, want 2", sum.Totals.Refunds)
	}
	if sum.Totals.Countries != 4 {
		t.Fatalf("countries = %d, want 4 (US, DE, JP, FR via free download)", sum.Totals.Countries)
	}

	// The older row lands in the preceding window.
	if sum.PrevTotals.RevenueBase != 999 {
		t.Fatalf("prev revenue = %v, want 999", sum.PrevTotals.RevenueBase)
	}

	if len(sum.Series) != 7 {
		t.Fatalf("series has %d points, want 7 zero-filled days", len(sum.Series))
	}
	if sum.Series[len(sum.Series)-1].Day != today {
		t.Fatalf("series ends on %s, want %s", sum.Series[len(sum.Series)-1].Day, today)
	}
	last := sum.Series[len(sum.Series)-1]
	if last.RevenueBase != 105 { // 100 - 20 + 25
		t.Fatalf("today's revenue = %v, want 105", last.RevenueBase)
	}
	if last.BySource["appstore"] != 80 || last.BySource["googleplay"] != 25 {
		t.Fatalf("today's by_source = %v", last.BySource)
	}
	if sum.Series[0].RevenueBase != 0 || sum.Series[0].BySource == nil {
		t.Fatalf("quiet days must be present and zeroed: %+v", sum.Series[0])
	}

	if len(sum.BySource) != 2 || sum.BySource[0].Source != "appstore" {
		t.Fatalf("by_source = %+v", sum.BySource)
	}
	if sum.BySource[0].RevenueBase != 130 || sum.BySource[0].Share != 0.8387 {
		t.Fatalf("appstore slice = %+v, want 130 and a 0.8387 share", sum.BySource[0])
	}
	if len(sum.ByApp) != 2 {
		t.Fatalf("by_app = %+v", sum.ByApp)
	}
	if len(sum.ByCountry) == 0 || sum.ByCountry[0].Country != "US" {
		t.Fatalf("by_country = %+v", sum.ByCountry)
	}

	if sum.Subscriptions.Active == nil || *sum.Subscriptions.Active != 412 {
		t.Fatalf("subscriptions = %+v, want 412 active", sum.Subscriptions)
	}
	if sum.Subscriptions.AsOf == nil || *sum.Subscriptions.AsOf != yesterday {
		t.Fatalf("subscriptions as_of = %+v, want %s", sum.Subscriptions, yesterday)
	}

	if sum.Realtime.RevenueCatPurchasesToday != 1 || sum.Realtime.RevenueCatAmountBaseToday != 49.99 {
		t.Fatalf("realtime = %+v, want the sighted RevenueCat purchase", sum.Realtime)
	}
}

func TestVaultSummaryEmptyStore(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	today := core.DayOf(time.Now().UTC())
	from := core.DayOf(time.Now().UTC().AddDate(0, 0, -29))

	sum, err := st.VaultSummary(ctx, from, today, "EUR", today)
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}
	if len(sum.Series) != 30 {
		t.Fatalf("series has %d points, want 30", len(sum.Series))
	}
	if sum.Totals.RevenueBase != 0 || sum.Totals.Units != 0 {
		t.Fatalf("totals = %+v, want zeroes", sum.Totals)
	}
	if sum.Subscriptions.Active != nil || sum.Subscriptions.AsOf != nil {
		t.Fatalf("subscriptions = %+v, want nulls when nothing was ever reported", sum.Subscriptions)
	}
	if sum.BySource == nil || sum.ByApp == nil || sum.ByCountry == nil {
		t.Fatal("breakdowns must marshal as [] rather than null")
	}
}

func TestChestQueries(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	mk := func(day string, rarity core.Rarity, xp int, dedupe string) string {
		ev := ledgerRow("appstore", "com.example.a", day, "US", 1, 1, dedupe)
		ev.Silent = false
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert event: %v", err)
		}
		d := core.Drop{
			ID: core.NewID(), EventID: ev.ID, Rarity: rarity,
			Title: string(rarity), XP: xp, CreatedAt: time.Now().UTC(), ChestDate: day,
		}
		if err := st.InsertDrop(ctx, d); err != nil {
			t.Fatalf("insert drop: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
		return d.ID
	}

	mk("2026-08-17", core.Epic, 300, "d1")
	mk("2026-08-17", core.Cursed, 5, "d2")
	mk("2026-08-17", core.Common, 10, "d3")
	mk("2026-08-16", core.Rare, 100, "d4")

	summaries, err := st.ChestSummaries(ctx)
	if err != nil {
		t.Fatalf("chest summaries: %v", err)
	}
	if len(summaries) != 2 || summaries[0].Date != "2026-08-16" {
		t.Fatalf("summaries = %+v, want the oldest chest first", summaries)
	}
	if summaries[1].Count != 3 || summaries[1].XP != 315 {
		t.Fatalf("2026-08-17 summary = %+v", summaries[1])
	}
	if summaries[1].ByRarity["epic"] != 1 {
		t.Fatalf("by_rarity = %v", summaries[1].ByRarity)
	}

	chests, err := st.ListChest(ctx)
	if err != nil {
		t.Fatalf("list chest: %v", err)
	}
	if len(chests) != 2 || len(chests[1].Drops) != 3 {
		t.Fatalf("chests = %+v", chests)
	}

	if oldest, err := st.OldestChestDate(ctx); err != nil || oldest != "2026-08-16" {
		t.Fatalf("oldest = %q (%v), want 2026-08-16", oldest, err)
	}

	// Nothing in a chest may reach the feed.
	feed, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(feed) != 0 {
		t.Fatalf("feed showed %d unopened drops", len(feed))
	}
	all, err := st.ListDrops(ctx, store.DropQuery{IncludeUnrevealed: true})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("IncludeUnrevealed returned %d drops, want 4", len(all))
	}

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	revealed, err := st.RevealChest(ctx, "2026-08-17", now)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if len(revealed) != 3 {
		t.Fatalf("revealed %d, want 3", len(revealed))
	}
	// Cursed first, then common, then epic: the cascade builds.
	want := []core.Rarity{core.Cursed, core.Common, core.Epic}
	for i, r := range want {
		if revealed[i].Rarity != r {
			t.Fatalf("reveal order = %s at %d, want %s", revealed[i].Rarity, i, r)
		}
	}
	if revealed[0].RevealedAt == nil || !revealed[0].RevealedAt.Equal(now) {
		t.Fatalf("revealed_at = %v, want %v", revealed[0].RevealedAt, now)
	}

	if feed, err := st.ListDrops(ctx, store.DropQuery{}); err != nil || len(feed) != 3 {
		t.Fatalf("feed has %d drops after the reveal (%v), want 3", len(feed), err)
	}

	// The other chest is untouched, and reopening the opened one is a no-op.
	if again, err := st.RevealChest(ctx, "2026-08-17", now); err != nil || len(again) != 0 {
		t.Fatalf("reopening returned %d drops (%v)", len(again), err)
	}
	if oldest, err := st.OldestChestDate(ctx); err != nil || oldest != "2026-08-16" {
		t.Fatalf("oldest = %q (%v) after the reveal", oldest, err)
	}

	// An empty date opens the oldest chest.
	rest, err := st.RevealChest(ctx, "", now)
	if err != nil {
		t.Fatalf("reveal oldest: %v", err)
	}
	if len(rest) != 1 || rest[0].ChestDate != "2026-08-16" {
		t.Fatalf("reveal oldest returned %+v", rest)
	}
	if empty, err := st.RevealChest(ctx, "", now); err != nil || len(empty) != 0 {
		t.Fatalf("revealing with nothing left returned %d drops (%v)", len(empty), err)
	}
}

// A subscription snapshot is a level, and a level goes stale. Without a
// recency bound the newest snapshot of an app that stopped reporting last
// spring kept contributing its final subscriber count to the headline forever.
func TestVaultSubscriptionsIgnoreStaleSnapshots(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	today := "2026-08-18"
	snapshot := func(source, app, day string, qty int) {
		occurred, _ := time.Parse(core.DayLayout, day)
		if _, err := st.InsertEvent(ctx, core.Event{
			ID:         core.NewID(),
			Source:     source,
			Kind:       "subscription_snapshot",
			App:        app,
			Day:        day,
			OccurredAt: occurred,
			ObservedAt: occurred,
			Quantity:   qty,
			Silent:     true,
			DedupeKey:  source + ":subs:" + app + ":" + day,
		}); err != nil {
			t.Fatalf("insert snapshot: %v", err)
		}
	}

	// Live: reported yesterday. Stale: last reported five weeks ago.
	snapshot("appstore", "Notes", "2026-08-17", 400)
	snapshot("appstore", "Notes", "2026-08-10", 390)
	snapshot("microsoftstore", "Tide Clock", "2026-07-10", 900)

	summary, err := st.VaultSummary(ctx, "2026-07-01", today, "USD", today)
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}
	if summary.Subscriptions.Active == nil {
		t.Fatal("no active subscriber count at all")
	}
	if got := *summary.Subscriptions.Active; got != 400 {
		t.Errorf("active = %d, want 400: the five-week-old snapshot is not news", got)
	}
	if summary.Subscriptions.AsOf == nil || *summary.Subscriptions.AsOf != "2026-08-17" {
		t.Errorf("as_of = %v, want 2026-08-17", summary.Subscriptions.AsOf)
	}
}
