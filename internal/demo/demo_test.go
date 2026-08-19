package demo

import (
	"context"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

// newDemo stands up a demo over a throwaway database in its own data
// directory, wired exactly as `loot serve --demo` wires it: the real rules,
// the real pipeline, and the embedded FX snapshot so the vault adds up without
// touching the network.
func newDemo(t *testing.T, dir string, seed int64, days int) (*Demo, *store.Store) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(dir, "demo.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	converter := fx.New(fx.Options{Base: "USD", Enabled: false, Logger: quietLogger()})

	pipe := pipeline.New(st, engine, bus.New(16), quietLogger())
	pipe.DisplayCurrency = "USD"
	pipe.FX = converter
	pipe.ChestEnabled = true

	return New(st, pipe, "", Options{Seed: seed, Days: days, Pace: 1}, quietLogger()), st
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// amountBaseTotal is the sum of every stored base amount, rounded so floating
// point dust cannot make two identical worlds look different.
func amountBaseTotal(t *testing.T, st *store.Store) float64 {
	t.Helper()
	var total float64
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(amount_base), 0) FROM events`).Scan(&total)
	if err != nil {
		t.Fatalf("sum amount_base: %v", err)
	}
	return math.Round(total*100) / 100
}

// dedupeKeys returns every stored dedupe key in a stable order.
func dedupeKeys(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT dedupe_key FROM events ORDER BY dedupe_key`)
	if err != nil {
		t.Fatalf("query dedupe keys: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan dedupe key: %v", err)
		}
		out = append(out, k)
	}
	return out
}

func TestSeedIsDeterministic(t *testing.T) {
	ctx := context.Background()

	first, firstStore := newDemo(t, t.TempDir(), 4242, 30)
	second, secondStore := newDemo(t, t.TempDir(), 4242, 30)

	a, err := first.Seed(ctx)
	if err != nil {
		t.Fatalf("seed one: %v", err)
	}
	b, err := second.Seed(ctx)
	if err != nil {
		t.Fatalf("seed two: %v", err)
	}

	if a.Events != b.Events {
		t.Errorf("same seed produced %d and %d events", a.Events, b.Events)
	}
	if a.Drops != b.Drops || a.XP != b.XP || a.Countries != b.Countries {
		t.Errorf("same seed produced different worlds: %+v vs %+v", a, b)
	}

	// The money has to match too, not just the event count. amount_base is
	// computed at ingest from the FX converter, so this is what would catch a
	// demo that had gone back to reading live rates: two runs a moment apart
	// would agree, but a run tomorrow would not, and the same assertion is the
	// one that fails first when a converter is fetching in the background.
	if a, b := amountBaseTotal(t, firstStore), amountBaseTotal(t, secondStore); a != b {
		t.Errorf("same seed produced %.2f and %.2f of base revenue", a, b)
	}
	if amountBaseTotal(t, firstStore) == 0 {
		t.Error("the seeded world converted nothing into the display currency")
	}

	keysA, keysB := dedupeKeys(t, firstStore), dedupeKeys(t, secondStore)
	if len(keysA) != len(keysB) {
		t.Fatalf("dedupe key counts differ: %d vs %d", len(keysA), len(keysB))
	}
	for i := 0; i < len(keysA) && i < 200; i++ {
		if keysA[i] != keysB[i] {
			t.Fatalf("dedupe key %d differs: %q vs %q", i, keysA[i], keysB[i])
		}
	}

	// A different seed must produce a different world, or "deterministic"
	// would be indistinguishable from "hard coded".
	other, _ := newDemo(t, t.TempDir(), 99, 30)
	c, err := other.Seed(ctx)
	if err != nil {
		t.Fatalf("seed three: %v", err)
	}
	if c.Events == a.Events && c.XP == a.XP {
		t.Errorf("seed 99 produced the same world as seed 4242 (%d events, %d xp)", c.Events, c.XP)
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d, st := newDemo(t, t.TempDir(), 7, 20)

	if _, err := d.Seed(ctx); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	before, err := st.EventCount(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	again, err := d.Seed(ctx)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if again.Generated != 0 {
		t.Errorf("second seed generated %d days, want 0", again.Generated)
	}

	after, err := st.EventCount(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("second seed added %d events", after-before)
	}
}

func TestSeedCatchesUpMissedDays(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A demo whose clock says it is a week ago, so the next run has six days
	// to catch up on.
	past := time.Now().UTC().AddDate(0, 0, -6)
	d, st := newDemo(t, dir, 11, 20)
	d.opts.Now = func() time.Time { return past }

	first, err := d.Seed(ctx)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if first.Generated != 20 {
		t.Fatalf("first seed generated %d days, want 20", first.Generated)
	}

	d.opts.Now = func() time.Time { return time.Now().UTC() }
	caught, err := d.Seed(ctx)
	if err != nil {
		t.Fatalf("catch-up seed: %v", err)
	}
	if caught.Generated != 6 {
		t.Errorf("catch-up generated %d days, want 6", caught.Generated)
	}
	if want := core.DayOf(time.Now().UTC().AddDate(0, 0, -1)); caught.To != want {
		t.Errorf("history ends at %s, want %s", caught.To, want)
	}

	// And the day before yesterday, which was open before the catch-up, is now
	// closed behind us.
	chests, err := st.ChestSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chests) != 1 {
		t.Fatalf("after catch-up there are %d unopened chests, want 1", len(chests))
	}
}

func TestSeedLeavesYesterdaysChestClosed(t *testing.T) {
	ctx := context.Background()
	d, st := newDemo(t, t.TempDir(), 3, 40)

	res, err := d.Seed(ctx)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	yesterday := core.DayOf(time.Now().UTC().AddDate(0, 0, -1))
	if res.To != yesterday {
		t.Errorf("history ends at %s, want yesterday (%s)", res.To, yesterday)
	}

	chests, err := st.ChestSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chests) != 1 {
		t.Fatalf("got %d unopened chests, want exactly one", len(chests))
	}
	if chests[0].Date != yesterday {
		t.Errorf("the waiting chest is for %s, want %s", chests[0].Date, yesterday)
	}
	if chests[0].Count == 0 {
		t.Error("yesterday's chest is empty")
	}

	// Every older chest was opened, which is what makes the feed and the XP
	// total meaningful on a cold start.
	var unrevealedOlder int
	err = st.DB().QueryRowContext(ctx, `
        SELECT COUNT(*) FROM drops
        WHERE chest_date <> '' AND chest_date < ? AND revealed_at IS NULL`, yesterday).
		Scan(&unrevealedOlder)
	if err != nil {
		t.Fatal(err)
	}
	if unrevealedOlder != 0 {
		t.Errorf("%d drops are still waiting in chests older than yesterday", unrevealedOlder)
	}

	// The feed reads as history: the newest drop is recent, the oldest is old.
	drops, err := st.ListDrops(ctx, store.DropQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) < 50 {
		t.Fatalf("only %d drops in the feed", len(drops))
	}
	oldest := drops[len(drops)-1].CreatedAt
	if age := time.Since(oldest); age < 24*time.Hour {
		t.Errorf("oldest drop is only %s old; drops were not backdated", age)
	}
}

func TestSeedNeverTouchesTheRealDatabase(t *testing.T) {
	dir := t.TempDir()
	d, _ := newDemo(t, dir, 5, 10)
	if _, err := d.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name == "loot.db" || name == "loot.db-wal" || name == "loot.db-shm" {
			t.Fatalf("demo mode created %s", name)
		}
	}
}

func TestSeededWorldFillsTheVaultAndTheHearth(t *testing.T) {
	ctx := context.Background()
	d, st := newDemo(t, t.TempDir(), 20260818, 120)

	res, err := d.Seed(ctx)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Seeding is one transaction over ~20k events; on a laptop it lands around
	// a second. The bound is loose enough for a busy CI box and tight enough
	// to catch a query that has gone quadratic — but the race detector costs
	// roughly ten times the CPU, so under it the number measures the detector
	// rather than the code.
	budget := 5 * time.Second
	if raceEnabled {
		budget = 60 * time.Second
	}
	if res.Took > budget {
		t.Errorf("seeding 120 days took %s (budget %s)", res.Took, budget)
	}
	if res.Events < 10000 {
		t.Errorf("120 days produced only %d events", res.Events)
	}

	to := core.DayOf(time.Now().UTC())
	from := core.DayOf(time.Now().UTC().AddDate(0, 0, -119))
	vault, err := st.VaultSummary(ctx, from, to, "USD", to)
	if err != nil {
		t.Fatalf("vault summary: %v", err)
	}
	if vault.Totals.RevenueBase < 1000 {
		t.Errorf("120 days earned %.2f, want a business worth showing", vault.Totals.RevenueBase)
	}
	if vault.Totals.Refunds == 0 {
		t.Error("no refunds in 120 days; the bad week never happened")
	}

	// Revenue is a ledger figure, so it comes from the two sources that report
	// settled money; every source should still show up in the drop feed.
	earning := 0
	for _, s := range vault.BySource {
		if s.RevenueBase > 0 {
			earning++
		}
	}
	if earning < 2 {
		t.Errorf("only %d sources earned money: %+v", earning, vault.BySource)
	}
	if len(vault.ByApp) < 3 {
		t.Errorf("only %d apps sold anything", len(vault.ByApp))
	}
	if vault.Subscriptions.Active == nil || *vault.Subscriptions.Active == 0 {
		t.Error("no active subscribers reported")
	}

	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"appstore", "googleplay", "flathub", "revenuecat", "loot"} {
		if stats.BySource[want] == 0 {
			t.Errorf("no drops from %s: %+v", want, stats.BySource)
		}
	}
	if stats.ByRarity["epic"] == 0 {
		t.Error("no epic drops: no record day was ever beaten")
	}
	if stats.ByRarity["cursed"] == 0 {
		t.Error("no cursed drops: nothing ever went wrong")
	}

	hearth, err := st.Hearth(ctx, "", "USD")
	if err != nil {
		t.Fatalf("hearth: %v", err)
	}
	if len(hearth.Countries) < 25 {
		t.Errorf("the hearth has %d settlements, want at least 25", len(hearth.Countries))
	}
	if hearth.Era.Index < core.Eras[3].Index {
		t.Errorf("era is %s (%d xp); a seeded demo should reach City", hearth.Era.Name, hearth.TotalXP)
	}
}

func TestEmitterProducesEventsWithinItsWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// Pace 400 turns the 20..90 second gap into 50..225 milliseconds, so the
	// emitter's real timing logic is exercised without a four minute test.
	d, st := newDemo(t, t.TempDir(), 8, 5)
	d.opts.Pace = 400
	if _, err := d.Seed(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	before, err := st.EventCount(ctx, "revenuecat")
	if err != nil {
		t.Fatal(err)
	}

	go d.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		after, err := st.EventCount(ctx, "revenuecat")
		if err != nil {
			t.Fatal(err)
		}
		if after > before {
			return
		}
	}
	t.Fatal("the live emitter produced no event within its own pace window")
}

// TestSeededWorldFillsTheCodex is Quest 6's half of the demo promise: a demo
// should open on a Codex with trophies already on the wall, dated to the days
// the invented history actually earned them, and with their drops waiting in
// today's chest rather than firing fifteen achievement sounds at once.
func TestSeededWorldFillsTheCodex(t *testing.T) {
	ctx := context.Background()
	d, st := newDemo(t, t.TempDir(), 20260818, 120)

	if _, err := d.Seed(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := codex.NewService(st, d.live, bus.New(16), "USD", quietLogger())
	res, err := svc.Evaluate(ctx)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !res.Backfilled {
		t.Fatalf("the first pass over a seeded world was not a backfill")
	}
	if len(res.Unlocked) < 15 {
		t.Errorf("a 120 day demo unlocked only %d achievements, want at least 15", len(res.Unlocked))
	}

	board, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	today := core.DayOf(time.Now().UTC())
	for _, a := range board.Achievements {
		if !a.Unlocked() {
			continue
		}
		if a.UnlockedAt.Format(core.DayLayout) > today {
			t.Errorf("%s is dated in the future (%s)", a.Key, a.UnlockedAt)
		}
	}

	// Every unlock drop is sealed in today's chest, so nothing was spilled
	// onto the live feed.
	var loose int
	if err := st.DB().QueryRowContext(ctx, `
        SELECT COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id
        WHERE e.kind = 'achievement' AND d.chest_date = ''`).Scan(&loose); err != nil {
		t.Fatalf("count loose achievement drops: %v", err)
	}
	if loose > 0 {
		t.Errorf("%d achievement drops went straight to the feed", loose)
	}

	// And the previous month's recap is worth screenshotting.
	lastMonth := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	recap, err := svc.Recap(ctx, lastMonth, "")
	if err != nil {
		t.Fatalf("recap %s: %v", lastMonth, err)
	}
	if recap.Empty {
		t.Fatalf("the seeded month %s recapped as empty", lastMonth)
	}
	if recap.RevenueBase <= 0 || recap.Drops == 0 {
		t.Errorf("recap %s = %.2f over %d drops", lastMonth, recap.RevenueBase, recap.Drops)
	}
	if len(recap.Highlights) < 3 {
		t.Errorf("recap %s has only %d highlights: %v", lastMonth, len(recap.Highlights), recap.Highlights)
	}
	if len(recap.Series) < 28 {
		t.Errorf("recap sparkline has %d points", len(recap.Series))
	}
}
