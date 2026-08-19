package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The app scope, end to end at the repository level: two products in one
// database, and every aggregate asked about one of them.
//
// The isolation this file asserts is the whole feature. A vault that folded
// Orbit's revenue into Lumen's would not be a cosmetic bug — it would be a
// number somebody quotes.

// twoProducts is the mapping under test: one product named differently by two
// sources, one named the same everywhere.
var twoProducts = config.Products{
	{Name: "Lumen Notes", Match: map[string][]string{
		"appstore": {"Lumen Notes"},
		"flathub":  {"com.lumenlabs.notes"},
	}},
	{Name: "Orbit Weather", Match: map[string][]string{
		"appstore": {"Orbit Weather"},
	}},
}

func day(offset int) string {
	return core.DayOf(time.Now().UTC().AddDate(0, 0, offset))
}

// ledgerSale is a silent ledger row: the kind of event the vault sums.
func ledgerSale(source, app, product, country, when string, qty int, amount float64, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, when)
	return core.Event{
		ID: core.NewID(), Source: source, Kind: "sale", App: app, Product: product,
		Day: when, OccurredAt: occurred, ObservedAt: occurred,
		Country: country, Quantity: qty, Amount: amount, AmountBase: amount, Currency: "USD",
		IsLedger: true, Silent: true, DedupeKey: dedupe,
	}
}

// loudEvent is an event that mints a drop, so the feed and stats have
// something to count.
func loudEvent(source, app, product, country, when string, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, when)
	return core.Event{
		ID: core.NewID(), Source: source, Kind: "purchase", App: app, Product: product,
		Day: when, OccurredAt: occurred, ObservedAt: occurred,
		Country: country, Quantity: 1, DedupeKey: dedupe,
	}
}

// seedTwoProducts fills a store with a day of each product's business, plus
// one realm-wide drop that belongs to neither.
func seedTwoProducts(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()

	today := day(0)
	events := []core.Event{
		// Lumen Notes: $100 from the US, one live purchase.
		ledgerSale("appstore", "Lumen Notes", "Lumen Notes", "US", today, 10, 100, "lumen:1"),
		loudEvent("revenuecat", "Lumen Notes", "Lumen Notes", "US", today, "lumen:2"),
		// Orbit Weather: $40 from Japan, one live purchase.
		ledgerSale("appstore", "Orbit Weather", "Orbit Weather", "JP", today, 4, 40, "orbit:1"),
		loudEvent("revenuecat", "Orbit Weather", "Orbit Weather", "JP", today, "orbit:2"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	// Loot's own realm-wide news: an achievement, which belongs to no app and
	// must therefore appear in every scope.
	occurred, _ := time.Parse(core.DayLayout, today)
	trophy := core.Event{
		ID: core.NewID(), Source: "loot", Kind: core.KindAchievement,
		Day: today, OccurredAt: occurred, ObservedAt: occurred,
		DedupeKey: "loot:achievement:first_sale",
	}
	if _, err := st.InsertEvent(ctx, trophy); err != nil {
		t.Fatalf("insert trophy: %v", err)
	}

	// One drop per non-silent event, minted by hand so the test does not need
	// the rules engine.
	for _, ev := range []core.Event{events[1], events[3], trophy} {
		d := core.Drop{
			ID: core.NewID(), EventID: ev.ID, Rarity: core.Uncommon,
			Title: ev.Kind + " " + ev.App, XP: 25, CreatedAt: occurred,
		}
		if err := st.InsertDrop(ctx, d); err != nil {
			t.Fatalf("insert drop: %v", err)
		}
	}
}

// TestScopedVaultIsolatesProducts is the money rule: a scoped vault contains
// this product's revenue and nothing else's.
func TestScopedVaultIsolatesProducts(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedTwoProducts(t, st)

	from, to := day(-6), day(0)

	all, err := st.VaultSummary(ctx, from, to, "USD", to)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if all.Totals.RevenueBase != 140 {
		t.Fatalf("unscoped revenue = %v, want 140", all.Totals.RevenueBase)
	}

	lumen, err := st.Scoped("Lumen Notes").VaultSummary(ctx, from, to, "USD", to)
	if err != nil {
		t.Fatalf("scoped vault: %v", err)
	}
	if lumen.Totals.RevenueBase != 100 {
		t.Errorf("Lumen revenue = %v, want 100", lumen.Totals.RevenueBase)
	}
	if lumen.Totals.Units != 10 {
		t.Errorf("Lumen units = %d, want 10", lumen.Totals.Units)
	}
	if lumen.Totals.Countries != 1 {
		t.Errorf("Lumen countries = %d, want 1 (Japan is Orbit's)", lumen.Totals.Countries)
	}
	for _, row := range lumen.ByCountry {
		if row.Country == "JP" {
			t.Errorf("Orbit's country leaked into Lumen's vault: %+v", row)
		}
	}

	orbit, err := st.Scoped("Orbit Weather").VaultSummary(ctx, from, to, "USD", to)
	if err != nil {
		t.Fatalf("scoped vault: %v", err)
	}
	if orbit.Totals.RevenueBase != 40 {
		t.Errorf("Orbit revenue = %v, want 40", orbit.Totals.RevenueBase)
	}
	if lumen.Totals.RevenueBase+orbit.Totals.RevenueBase != all.Totals.RevenueBase {
		t.Errorf("the parts do not add up to the whole: %v + %v != %v",
			lumen.Totals.RevenueBase, orbit.Totals.RevenueBase, all.Totals.RevenueBase)
	}

	// An unknown scope is not an error; it is an empty answer, which is what a
	// stale bookmark deserves.
	none, err := st.Scoped("Never Shipped").VaultSummary(ctx, from, to, "USD", to)
	if err != nil {
		t.Fatalf("unknown scope: %v", err)
	}
	if none.Totals.RevenueBase != 0 || none.Totals.Units != 0 {
		t.Errorf("unknown scope returned data: %+v", none.Totals)
	}
}

// TestScopedHearthIsolatesSettlements: a scoped globe shows the countries this
// app sold in, and not the ones another app did.
func TestScopedHearthIsolatesSettlements(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedTwoProducts(t, st)

	all, err := st.Hearth(ctx, "", "USD")
	if err != nil {
		t.Fatalf("hearth: %v", err)
	}
	if len(all.Countries) != 2 {
		t.Fatalf("unscoped countries = %d, want 2", len(all.Countries))
	}

	lumen, err := st.Scoped("Lumen Notes").Hearth(ctx, "", "USD")
	if err != nil {
		t.Fatalf("scoped hearth: %v", err)
	}
	if len(lumen.Countries) != 1 || lumen.Countries[0].Country != "US" {
		t.Fatalf("Lumen countries = %+v, want only US", lumen.Countries)
	}
	if lumen.RevenueBase != 100 {
		t.Errorf("Lumen hearth revenue = %v, want 100", lumen.RevenueBase)
	}

	// XP and the era are the account's standing, not the app's: they must not
	// shrink when the view narrows.
	if lumen.TotalXP != all.TotalXP {
		t.Errorf("scoped XP = %d, want the realm's %d", lumen.TotalXP, all.TotalXP)
	}
}

// TestScopedDropsKeepRealmWideNews is the loose-filter rule: another product's
// drops are hidden, Loot's own are not.
func TestScopedDropsKeepRealmWideNews(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedTwoProducts(t, st)

	all, err := st.ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unscoped feed = %d drops, want 3", len(all))
	}

	lumen, err := st.Scoped("Lumen Notes").ListDrops(ctx, store.DropQuery{})
	if err != nil {
		t.Fatalf("scoped drops: %v", err)
	}
	if len(lumen) != 2 {
		t.Fatalf("Lumen feed = %d drops, want 2 (its own, plus the trophy)", len(lumen))
	}
	sawTrophy, sawOrbit := false, false
	for _, d := range lumen {
		if d.Product == "Orbit Weather" {
			sawOrbit = true
		}
		if d.Kind == core.KindAchievement {
			sawTrophy = true
			if d.Product != "" {
				t.Errorf("a realm-wide drop carries a product: %q", d.Product)
			}
		}
	}
	if sawOrbit {
		t.Error("another product's drop appeared in a scoped feed")
	}
	if !sawTrophy {
		t.Error("the realm-wide achievement was hidden by the scope; it is about everything")
	}
}

// TestScopedStats narrows the header's counts the same way the feed is
// narrowed: this product plus the realm.
func TestScopedStats(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedTwoProducts(t, st)

	all, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if all.TotalDrops != 3 {
		t.Fatalf("unscoped drops = %d, want 3", all.TotalDrops)
	}

	lumen, err := st.Scoped("Lumen Notes").Stats(ctx)
	if err != nil {
		t.Fatalf("scoped stats: %v", err)
	}
	if lumen.TotalDrops != 2 {
		t.Errorf("Lumen drops = %d, want 2", lumen.TotalDrops)
	}
	if len(lumen.Countries) != 1 || lumen.Countries[0] != "US" {
		t.Errorf("Lumen countries = %v, want [US]", lumen.Countries)
	}
	if lumen.BySource["appstore"] != 0 {
		t.Errorf("silent ledger rows should mint no drops: %v", lumen.BySource)
	}
	// XP is the account's standing, not the app's: it does not shrink when the
	// view narrows, and it agrees with the Hearth's era bar beside it.
	if lumen.TotalXP != all.TotalXP {
		t.Errorf("scoped XP = %d, want the realm's %d", lumen.TotalXP, all.TotalXP)
	}
}

// TestRemapProducts is the "editing apps: is the whole of the work" promise:
// a mapping applied after the fact rewrites the product of every row it now
// claims, and says how many it moved.
func TestRemapProducts(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Ingested before anybody wrote a mapping: every event is its own product.
	today := day(0)
	rows := []core.Event{
		ledgerSale("appstore", "Lumen Notes", "", "US", today, 10, 100, "as:1"),
		ledgerSale("flathub", "com.lumenlabs.notes", "", "DE", today, 1, 0, "fh:1"),
		ledgerSale("appstore", "Orbit Weather", "", "JP", today, 4, 40, "as:2"),
	}
	for _, ev := range rows {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	before, err := st.Scoped("Lumen Notes").VaultSummary(ctx, day(-6), today, "USD", today)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if len(before.ByCountry) != 1 {
		t.Fatalf("before the remap Lumen should be one country, got %+v", before.ByCountry)
	}

	changed, err := st.RemapProducts(ctx, twoProducts)
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	// Only the Flathub row moves: the two App Store rows already carried the
	// canonical name, and a remap that rewrote them would be churn.
	if changed != 1 {
		t.Errorf("remapped %d rows, want 1", changed)
	}

	after, err := st.Scoped("Lumen Notes").VaultSummary(ctx, day(-6), today, "USD", today)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if after.Totals.Countries != 2 {
		t.Errorf("after the remap Lumen should span 2 countries, got %d", after.Totals.Countries)
	}

	// Idempotent: running it again moves nothing.
	changed, err = st.RemapProducts(ctx, twoProducts)
	if err != nil {
		t.Fatalf("second remap: %v", err)
	}
	if changed != 0 {
		t.Errorf("a second remap moved %d rows, want 0", changed)
	}
}

// TestProductPairs is what `loot apps` and GET /api/apps print: every raw name
// a source used, and what it currently resolves to.
func TestProductPairs(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedTwoProducts(t, st)

	pairs, err := st.ProductPairs(ctx)
	if err != nil {
		t.Fatalf("product pairs: %v", err)
	}
	// Two products, each seen by two sources; the realm-wide achievement has
	// no app and is deliberately absent.
	if len(pairs) != 4 {
		t.Fatalf("got %d pairs, want 4: %+v", len(pairs), pairs)
	}
	for _, p := range pairs {
		if p.App == "" {
			t.Errorf("a pair with no app got in: %+v", p)
		}
		if p.Events == 0 || p.FirstSeen == "" {
			t.Errorf("pair is missing its evidence: %+v", p)
		}
	}
	if pairs[0].Product != "Lumen Notes" {
		t.Errorf("pairs are not ordered by product: %+v", pairs)
	}
}
