package codex_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/core"
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
type recorder struct{ events []core.Event }

func (r *recorder) Ingest(_ context.Context, ev core.Event) (*core.Drop, error) {
	r.events = append(r.events, ev)
	return &core.Drop{ID: core.NewID(), EventID: ev.ID, Rarity: core.Uncommon, Title: ev.Kind, XP: 50}, nil
}

func (r *recorder) byKey(key string) (core.AchievementPayload, bool) {
	for _, ev := range r.events {
		var p core.AchievementPayload
		if json.Unmarshal(ev.Payload, &p) == nil && p.Key == key {
			return p, true
		}
	}
	return core.AchievementPayload{}, false
}

func (r *recorder) eventForKey(key string) (core.Event, bool) {
	for _, ev := range r.events {
		if ev.DedupeKey == core.AchievementDedupePfx+key {
			return ev, true
		}
	}
	return core.Event{}, false
}

// spy counts bus messages by type.
type spy struct{ seen map[string]int }

func newSpy() *spy { return &spy{seen: map[string]int{}} }

func (s *spy) Publish(msg bus.Message) { s.seen[msg.Type]++ }

// today is the clock every test runs on.
var today = time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

func newService(t *testing.T, st *store.Store, ing codex.Ingester, b codex.Publisher) *codex.Service {
	t.Helper()
	svc := codex.NewService(st, ing, b, "USD", quiet())
	svc.Now = func() time.Time { return today }
	return svc
}

func ledgerRow(source, day, country string, units int, amount float64, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID: core.NewID(), Source: source, Kind: "sale", App: "Notes",
		Day: day, OccurredAt: occurred, ObservedAt: occurred, Country: country,
		Amount: amount, AmountBase: amount, Currency: "USD", Quantity: units,
		IsLedger: true, Silent: true, DedupeKey: dedupe,
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

// dropFor inserts a visible drop for an event, which is what makes it count
// towards drops, XP and rarity records.
func dropFor(t *testing.T, st *store.Store, ev core.Event, rarity core.Rarity, xp int) {
	t.Helper()
	occurred, _ := time.Parse(core.DayLayout, ev.Day)
	err := st.InsertDrop(context.Background(), core.Drop{
		ID: core.NewIDAt(occurred), EventID: ev.ID, Rarity: rarity,
		Title: "drop", XP: xp, CreatedAt: occurred,
	})
	if err != nil {
		t.Fatalf("insert drop: %v", err)
	}
}

func find(t *testing.T, board codex.Board, key string) core.Achievement {
	t.Helper()
	for _, a := range board.Achievements {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("achievement %q not on the wall", key)
	return core.Achievement{}
}

// --------------------------------------------------------------- thresholds

func TestEvaluateUnlocksAtThreshold(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{}
	svc := newService(t, st, rec, newSpy())

	// Four countries: one short of Settler I.
	for i, country := range []string{"US", "GB", "DE", "FR"} {
		ev := ledgerRow("appstore", "2026-08-10", country, 1, 10, "row-"+country)
		insert(t, st, ev)
		dropFor(t, st, ev, core.Common, 10)
		_ = i
	}

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	settler := find(t, board, "settler_1")
	if settler.Unlocked() {
		t.Fatalf("settler_1 unlocked at 4 countries")
	}
	if settler.ProgressValue != 4 || settler.ProgressTarget != 5 {
		t.Fatalf("settler_1 progress = %v/%v, want 4/5", settler.ProgressValue, settler.ProgressTarget)
	}
	// A locked achievement must not have paid anything.
	if _, ok := rec.byKey("settler_1"); ok {
		t.Fatalf("a locked achievement minted a drop")
	}

	// The fifth country crosses it.
	ev := ledgerRow("appstore", "2026-08-12", "JP", 1, 10, "row-JP")
	insert(t, st, ev)
	dropFor(t, st, ev, core.Common, 10)

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, _ = svc.List(ctx)
	settler = find(t, board, "settler_1")
	if !settler.Unlocked() {
		t.Fatalf("settler_1 still locked at 5 countries")
	}
	// Earned on the day the fifth country arrived, not on the day Loot noticed.
	if got := settler.UnlockedAt.Format(core.DayLayout); got != "2026-08-12" {
		t.Errorf("settler_1 unlocked_at = %s, want 2026-08-12", got)
	}
	payload, ok := rec.byKey("settler_1")
	if !ok {
		t.Fatalf("settler_1 paid no drop")
	}
	if payload.Tier != core.TierBronze || payload.Title != "Settler I" {
		t.Errorf("payload = %+v, want bronze Settler I", payload)
	}
}

func TestEvaluateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{}
	b := newSpy()
	svc := newService(t, st, rec, b)

	ev := ledgerRow("appstore", "2026-08-10", "US", 1, 10, "row-1")
	insert(t, st, ev)
	dropFor(t, st, ev, core.Common, 10)

	for i := 0; i < 4; i++ {
		if _, err := svc.Evaluate(ctx); err != nil {
			t.Fatalf("evaluate %d: %v", i, err)
		}
	}

	// first_blood, first_sale — each exactly once, however many passes ran.
	counts := map[string]int{}
	for _, e := range rec.events {
		counts[e.DedupeKey]++
	}
	for key, n := range counts {
		if n != 1 {
			t.Errorf("%s ingested %d times, want 1", key, n)
		}
	}
	if _, ok := counts[core.AchievementDedupePfx+"first_blood"]; !ok {
		t.Errorf("first_blood never unlocked; got %v", counts)
	}

	board, _ := svc.List(ctx)
	if board.Total != len(codex.Catalog) {
		t.Errorf("wall has %d achievements, want %d", board.Total, len(codex.Catalog))
	}
}

func TestBackfilledUnlocksGoToTodaysChest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{}
	svc := newService(t, st, rec, newSpy())

	// History: five countries, months ago.
	for _, country := range []string{"US", "GB", "DE", "FR", "JP"} {
		ev := ledgerRow("appstore", "2026-05-04", country, 3, 40, "row-"+country)
		insert(t, st, ev)
		dropFor(t, st, ev, core.Common, 10)
	}

	res, err := svc.Evaluate(ctx)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !res.Backfilled {
		t.Fatalf("first pass over an existing database was not a backfill")
	}
	if len(res.Unlocked) == 0 {
		t.Fatalf("backfill unlocked nothing")
	}

	ev, ok := rec.eventForKey("settler_1")
	if !ok {
		t.Fatalf("settler_1 paid no drop")
	}
	if !ev.Chest {
		t.Errorf("a backfilled unlock went to the live feed; want today's chest")
	}
	// The trophy is dated when it was earned; its drop belongs to today.
	if ev.Day != core.DayOf(today) {
		t.Errorf("unlock event day = %s, want today %s", ev.Day, core.DayOf(today))
	}
	board, _ := svc.List(ctx)
	settler := find(t, board, "settler_1")
	if got := settler.UnlockedAt.Format(core.DayLayout); got != "2026-05-04" {
		t.Errorf("unlocked_at = %s, want the day it happened (2026-05-04)", got)
	}
	var meta struct {
		Backfilled bool `json:"backfilled"`
	}
	if err := json.Unmarshal(settler.Meta, &meta); err != nil || !meta.Backfilled {
		t.Errorf("meta = %s, want backfilled:true", settler.Meta)
	}
}

func TestLiveUnlockIsNotChested(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{}
	svc := newService(t, st, rec, newSpy())

	// An empty database: the first pass unlocks nothing, so it is not a
	// backfill of anything.
	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("first evaluate: %v", err)
	}

	ev := ledgerRow("appstore", core.DayOf(today), "US", 1, 10, "row-1")
	insert(t, st, ev)
	dropFor(t, st, ev, core.Common, 10)

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	unlock, ok := rec.eventForKey("first_blood")
	if !ok {
		t.Fatalf("first_blood paid no drop")
	}
	if unlock.Chest {
		t.Errorf("a live unlock was filed into a chest; it should land in the feed")
	}
}

func TestUnlockNudgesTheBus(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	b := newSpy()
	svc := newService(t, st, &recorder{}, b)

	ev := ledgerRow("appstore", "2026-08-10", "US", 1, 10, "row-1")
	insert(t, st, ev)
	dropFor(t, st, ev, core.Common, 10)

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if b.seen["codex"] == 0 {
		t.Errorf("no codex message published; the wall would stay stale")
	}
}

func TestSteadyRunSurvivesTheRunEnding(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	// Seven consecutive earning days in May, then a fortnight of nothing.
	start, _ := time.Parse(core.DayLayout, "2026-05-01")
	for i := 0; i < 7; i++ {
		day := core.DayOf(start.AddDate(0, 0, i))
		insert(t, st, ledgerRow("appstore", day, "US", 1, 25, "row-"+day))
	}

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, _ := svc.List(ctx)
	steady := find(t, board, "steady_7")
	if !steady.Unlocked() {
		t.Fatalf("steady_7 did not unlock on a 7 day run")
	}
	if got := steady.UnlockedAt.Format(core.DayLayout); got != "2026-05-07" {
		t.Errorf("steady_7 earned %s, want the day the run completed (2026-05-07)", got)
	}
	// The run is long over and the trophy is still there, with its high-water
	// mark intact. Nothing in the Codex decays.
	if steady.ProgressValue < 7 {
		t.Errorf("steady_7 progress decayed to %v", steady.ProgressValue)
	}
	if board.Records.LongestRevenueRun.Days != 7 {
		t.Errorf("longest run = %d, want 7", board.Records.LongestRevenueRun.Days)
	}
}

func TestCartographerNeedsEveryContinent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	// One country per inhabited continent, minus Oceania.
	for _, c := range []string{"KE", "JP", "DE", "US", "BR"} {
		insert(t, st, ledgerRow("appstore", "2026-06-01", c, 1, 10, "row-"+c))
	}
	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, _ := svc.List(ctx)
	if find(t, board, "cartographer").Unlocked() {
		t.Fatalf("cartographer unlocked without Oceania")
	}

	insert(t, st, ledgerRow("appstore", "2026-06-09", "NZ", 1, 10, "row-NZ"))
	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, _ = svc.List(ctx)
	carto := find(t, board, "cartographer")
	if !carto.Unlocked() {
		t.Fatalf("cartographer locked with all six continents settled")
	}
	if carto.Tier != core.TierLegendary {
		t.Errorf("cartographer tier = %s, want legendary", carto.Tier)
	}
	if got := carto.UnlockedAt.Format(core.DayLayout); got != "2026-06-09" {
		t.Errorf("cartographer earned %s, want 2026-06-09", got)
	}
}

func TestContinentOf(t *testing.T) {
	cases := map[string]string{
		"US": codex.NorthAmerica,
		"br": codex.SouthAmerica,
		"JP": codex.Asia,
		"DE": codex.Europe,
		"KE": codex.Africa,
		"NZ": codex.Oceania,
		"AQ": "", // uninhabited, and deliberately not a continent you can settle
		"ZZ": "",
		"":   "",
	}
	for iso, want := range cases {
		if got := codex.ContinentOf(iso); got != want {
			t.Errorf("ContinentOf(%q) = %q, want %q", iso, got, want)
		}
	}
}

func TestTierRarityAndXP(t *testing.T) {
	cases := []struct {
		tier   core.AchievementTier
		rarity core.Rarity
		xp     int
	}{
		{core.TierBronze, core.Uncommon, 50},
		{core.TierSilver, core.Rare, 150},
		{core.TierGold, core.Epic, 400},
		{core.TierLegendary, core.Legendary, 1000},
	}
	for _, c := range cases {
		if got := c.tier.Rarity(); got != c.rarity {
			t.Errorf("%s rarity = %s, want %s", c.tier, got, c.rarity)
		}
		if got := c.tier.XP(); got != c.xp {
			t.Errorf("%s xp = %d, want %d", c.tier, got, c.xp)
		}
	}
}

// ------------------------------------------------------------------ records

func TestRecordsAndTotals(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	// Two stores, three days, one clearly best.
	insert(t, st,
		ledgerRow("appstore", "2026-08-01", "US", 5, 50, "a1"),
		ledgerRow("appstore", "2026-08-02", "US", 30, 400, "a2"),
		ledgerRow("appstore", "2026-08-03", "GB", 2, 20, "a3"),
		ledgerRow("googleplay", "2026-08-02", "DE", 4, 60, "g1"),
		ledgerRow("googleplay", "2026-08-03", "FR", 9, 90, "g2"),
	)
	// A visible, non-ledger-value drop: it is here for its XP, not its units.
	big := ledgerRow("appstore", "2026-08-02", "US", 0, 0, "big")
	big.Silent = false
	insert(t, st, big)
	dropFor(t, st, big, core.Legendary, 1000)

	board, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	rec := board.Records

	if rec.BestRevenueDay.Day != "2026-08-02" || rec.BestRevenueDay.Value != 460 {
		t.Errorf("best revenue day = %+v, want 2026-08-02 / 460", rec.BestRevenueDay)
	}
	if rec.BestUnitsDay.Day != "2026-08-02" || rec.BestUnitsDay.Value != 34 {
		t.Errorf("best units day = %+v, want 2026-08-02 / 34", rec.BestUnitsDay)
	}
	bySource := map[string]store.DayValue{}
	for _, s := range rec.BestRevenueDayBySource {
		bySource[s.Source] = store.DayValue{Day: s.Day, Value: s.Value}
	}
	if got := bySource["googleplay"]; got.Day != "2026-08-03" || got.Value != 90 {
		t.Errorf("googleplay best day = %+v, want 2026-08-03 / 90", got)
	}
	if rec.BiggestDrop == nil || rec.BiggestDrop.XP != 1000 {
		t.Errorf("biggest drop = %+v, want the 1000 XP legendary", rec.BiggestDrop)
	}
	if rec.FirstEventDay != "2026-08-01" {
		t.Errorf("first event day = %s, want 2026-08-01", rec.FirstEventDay)
	}

	tot := board.Totals
	if tot.RevenueBase != 620 {
		t.Errorf("lifetime revenue = %v, want 620", tot.RevenueBase)
	}
	if tot.Units != 50 {
		t.Errorf("lifetime units = %d, want 50", tot.Units)
	}
	if tot.Countries != 4 {
		t.Errorf("countries = %d, want 4", tot.Countries)
	}
	if tot.XP != 1000 {
		t.Errorf("xp = %d, want 1000", tot.XP)
	}
	if tot.Era.Name != core.EraFor(1000).Name {
		t.Errorf("era = %s, want %s", tot.Era.Name, core.EraFor(1000).Name)
	}
}

// -------------------------------------------------------------------- recap

func TestResolvePeriod(t *testing.T) {
	now := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)

	p, err := codex.ResolvePeriod(now, "", "")
	if err != nil {
		t.Fatalf("default period: %v", err)
	}
	if p.Key != "2026-07" || p.From != "2026-07-01" || p.To != "2026-07-31" || p.Partial {
		t.Errorf("default period = %+v, want the whole of July 2026", p)
	}

	p, err = codex.ResolvePeriod(now, "2026-02", "")
	if err != nil {
		t.Fatalf("february: %v", err)
	}
	if p.To != "2026-02-28" || p.Days != 28 || p.Label != "February 2026" {
		t.Errorf("february = %+v", p)
	}

	p, err = codex.ResolvePeriod(now, "", "2026")
	if err != nil {
		t.Fatalf("season: %v", err)
	}
	if p.Kind != "season" || p.From != "2026-01-01" || p.To != "2026-12-31" || !p.Partial {
		t.Errorf("season = %+v, want a partial 2026", p)
	}

	// The current month is partial, and says so rather than pretending to be
	// a finished story.
	p, _ = codex.ResolvePeriod(now, "2026-08", "")
	if !p.Partial {
		t.Errorf("the current month should be partial")
	}

	if _, err := codex.ResolvePeriod(now, "not-a-month", ""); err == nil {
		t.Errorf("a bad month was accepted")
	}
	if _, err := codex.ResolvePeriod(now, "", "twenty"); err == nil {
		t.Errorf("a bad season was accepted")
	}
}

func TestRecapAggregation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	// June: the month before, and the basis for July's delta.
	insert(t, st,
		ledgerRow("appstore", "2026-06-10", "US", 10, 100, "j1"),
		ledgerRow("appstore", "2026-06-20", "GB", 5, 100, "j2"),
	)
	// July: more money, one clearly best day, two brand new countries.
	insert(t, st,
		ledgerRow("appstore", "2026-07-02", "US", 4, 40, "u1"),
		ledgerRow("appstore", "2026-07-14", "US", 20, 260, "u2"),
		ledgerRow("googleplay", "2026-07-14", "JP", 3, 40, "u3"),
		ledgerRow("appstore", "2026-07-21", "BR", 2, 20, "u4"),
	)
	best := ledgerRow("appstore", "2026-07-14", "US", 0, 0, "best-drop")
	best.Silent = false
	insert(t, st, best)
	dropFor(t, st, best, core.Legendary, 1000)

	recap, err := svc.Recap(ctx, "2026-07", "")
	if err != nil {
		t.Fatalf("recap: %v", err)
	}

	if recap.Period.Key != "2026-07" {
		t.Fatalf("recap period = %s", recap.Period.Key)
	}
	if recap.RevenueBase != 360 {
		t.Errorf("revenue = %v, want 360", recap.RevenueBase)
	}
	// Stated as a fact, with a direction and no verdict.
	if recap.RevenueDelta.Previous != 200 || recap.RevenueDelta.Direction != "up" || !recap.RevenueDelta.HasBasis {
		t.Errorf("revenue delta = %+v, want up from 200", recap.RevenueDelta)
	}
	if recap.Units != 29 {
		t.Errorf("units = %d, want 29", recap.Units)
	}
	if recap.BestDay.Day != "2026-07-14" || recap.BestDay.Value != 300 {
		t.Errorf("best day = %+v, want 2026-07-14 / 300", recap.BestDay)
	}

	// New countries are countries whose *first ever* event fell in the window:
	// the US sold in June, so July's US rows found nothing.
	countries := []string{}
	for _, c := range recap.NewCountries {
		countries = append(countries, c.Country)
	}
	if len(countries) != 2 || countries[0] != "JP" || countries[1] != "BR" {
		t.Errorf("new countries = %v, want [JP BR] in the order they arrived", countries)
	}

	if recap.TopSource.Key != "appstore" {
		t.Errorf("top source = %s, want appstore", recap.TopSource.Key)
	}
	if recap.TopCountry.Key != "US" {
		t.Errorf("top country = %s, want US", recap.TopCountry.Key)
	}
	if recap.TopRarity != "legendary" {
		t.Errorf("top rarity = %s, want legendary", recap.TopRarity)
	}
	if len(recap.Series) != 31 {
		t.Errorf("series has %d points, want 31 zero-filled days", len(recap.Series))
	}
	if recap.Empty {
		t.Errorf("a month with money in it reported itself empty")
	}

	// Highlights are ordered: the best day first, then the settlements, and
	// each is a fact rather than a score.
	if len(recap.Highlights) == 0 {
		t.Fatalf("no highlights written")
	}
	if !strings.HasPrefix(recap.Highlights[0], "Best day on Jul 14") {
		t.Errorf("first highlight = %q, want the best day", recap.Highlights[0])
	}
	joined := strings.Join(recap.Highlights, "\n")
	if !strings.Contains(joined, "Settled") || !strings.Contains(joined, "JP") {
		t.Errorf("highlights never mention the new settlement: %q", joined)
	}
	if !strings.Contains(joined, "1 legendary drop") {
		t.Errorf("highlights never mention the legendary: %q", joined)
	}
	for _, h := range recap.Highlights {
		for _, banned := range []string{"failed", "missed", "down from", "worse"} {
			if strings.Contains(strings.ToLower(h), banned) {
				t.Errorf("highlight %q reads as a verdict", h)
			}
		}
	}
}

func TestRecapWithNothingInIt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	recap, err := svc.Recap(ctx, "2026-03", "")
	if err != nil {
		t.Fatalf("recap: %v", err)
	}
	if !recap.Empty {
		t.Errorf("an empty month did not say so")
	}
	if recap.RevenueDelta.HasBasis {
		t.Errorf("a delta was claimed against a month with nothing in it")
	}
	if len(recap.Series) != 31 {
		t.Errorf("series has %d points, want 31", len(recap.Series))
	}
}

func TestRecapCountsUnlockedAchievements(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	for _, c := range []string{"US", "GB", "DE", "FR", "JP"} {
		ev := ledgerRow("appstore", "2026-07-08", c, 2, 30, "row-"+c)
		insert(t, st, ev)
		dropFor(t, st, ev, core.Common, 10)
	}
	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	recap, err := svc.Recap(ctx, "2026-07", "")
	if err != nil {
		t.Fatalf("recap: %v", err)
	}
	found := false
	for _, a := range recap.Achievements {
		if a.Key == "settler_1" {
			found = true
		}
	}
	if !found {
		t.Errorf("July's recap missed the achievement earned in July: %+v", recap.Achievements)
	}
	// And a month it was not earned in must not claim it.
	other, err := svc.Recap(ctx, "2026-06", "")
	if err != nil {
		t.Fatalf("recap june: %v", err)
	}
	if len(other.Achievements) != 0 {
		t.Errorf("June claimed July's trophies: %+v", other.Achievements)
	}
}

func TestMonthsPicker(t *testing.T) {
	svc := newService(t, newStore(t), &recorder{}, newSpy())
	periods := svc.Months(12)
	if len(periods) != 13 {
		t.Fatalf("picker has %d periods, want 12 months plus the season", len(periods))
	}
	if periods[0].Key != "2026-08" || periods[11].Key != "2025-09" {
		t.Errorf("picker runs %s..%s, want 2026-08..2025-09", periods[0].Key, periods[11].Key)
	}
	if periods[12].Kind != "season" || periods[12].Key != "2026" {
		t.Errorf("last period = %+v, want the 2026 season", periods[12])
	}
}

// A trophy earned on a day that is not today is *dated* then — a milestone
// crossed on a day whose report only settled overnight is genuinely
// yesterday's — but it is still news. Only the very first pass over a database
// full of history is a backfill; treating every past-dated unlock as one
// posted live achievements silently into a chest instead of the feed.
func TestPastDatedUnlockOnALaterPassIsLive(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	rec := &recorder{}
	svc := newService(t, st, rec, newSpy())

	// An empty database, so the first pass backfills nothing.
	res, err := svc.Evaluate(ctx)
	if err != nil {
		t.Fatalf("first evaluate: %v", err)
	}
	if len(res.Unlocked) != 0 {
		t.Fatalf("an empty database unlocked %d achievements", len(res.Unlocked))
	}

	// Now a report lands for *yesterday*: five countries, all settled then.
	yesterday := core.DayOf(today.AddDate(0, 0, -1))
	for _, country := range []string{"US", "GB", "DE", "FR", "JP"} {
		ev := ledgerRow("appstore", yesterday, country, 3, 40, "row-"+country)
		insert(t, st, ev)
		dropFor(t, st, ev, core.Common, 10)
	}

	res, err = svc.Evaluate(ctx)
	if err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	if res.Backfilled {
		t.Fatal("a later pass reported itself as a backfill")
	}

	ev, ok := rec.eventForKey("settler_1")
	if !ok {
		t.Fatalf("settler_1 paid no drop")
	}
	if ev.Chest {
		t.Error("a live unlock earned yesterday was filed into a chest, not the feed")
	}
	var meta struct {
		EarnedDay  string `json:"earned_day"`
		Backfilled bool   `json:"backfilled"`
	}
	board, _ := svc.List(ctx)
	settler := find(t, board, "settler_1")
	if err := json.Unmarshal(settler.Meta, &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta.Backfilled {
		t.Errorf("meta = %s, want backfilled:false", settler.Meta)
	}
	// It is still dated when it was actually earned.
	if meta.EarnedDay != yesterday {
		t.Errorf("earned_day = %q, want %q", meta.EarnedDay, yesterday)
	}
	if got := settler.UnlockedAt.Format(core.DayLayout); got != yesterday {
		t.Errorf("unlocked_at = %s, want %s", got, yesterday)
	}
}

// starsTotal is the daily "this repo has N stars" snapshot the GitHub poller
// emits, which is where a repo that was already popular before Loot existed
// gets its star count from.
func starsTotal(repo, day string, total int) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID: core.NewID(), Source: "github", Kind: "stars_total", App: repo,
		Day: day, OccurredAt: occurred, ObservedAt: occurred,
		Quantity: total, Silent: true,
		DedupeKey: "github:stars_total:" + repo + ":" + day,
	}
}

func starEvent(repo, day, user string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID: core.NewID(), Source: "github", Kind: "star", App: repo,
		Day: day, OccurredAt: occurred, ObservedAt: occurred, Quantity: 1,
		DedupeKey: "github:star:" + repo + ":" + user,
	}
}

// Star achievements used to count only the stars Loot watched arrive, so
// pointing it at a repo with three thousand stars left "1,000 GitHub stars"
// locked at 0/1,000 next to a repo that had passed it years earlier.
func TestStarsCountTheRepoAndNotOnlyWhatLootWatched(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	insert(t, st,
		starsTotal("o/big", core.DayOf(today.AddDate(0, 0, -1)), 900),
		starsTotal("o/big", core.DayOf(today), 1_100),
		starsTotal("o/small", core.DayOf(today), 40),
	)

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	galaxy := find(t, board, "stars_1000")
	if galaxy.ProgressValue < 1_140 {
		t.Errorf("stars = %v, want the repos' own totals summed (1,140)", galaxy.ProgressValue)
	}
	if !galaxy.Unlocked() {
		t.Error("a repo with more than a thousand stars did not unlock Galaxy")
	}
}

// Neither source of truth may take a star away: a poll that missed a day, or a
// repo that lost a star, must not un-earn a trophy.
func TestStarsTakeTheKinderOfTheTwoCounts(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	svc := newService(t, st, &recorder{}, newSpy())

	// Twelve stars watched arriving, and a snapshot that only ever saw three.
	for i := 0; i < 12; i++ {
		insert(t, st, starEvent("o/r", core.DayOf(today.AddDate(0, 0, -2)), core.NewID()))
	}
	insert(t, st, starsTotal("o/r", core.DayOf(today), 3))

	if _, err := svc.Evaluate(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	board, _ := svc.List(ctx)
	if got := find(t, board, "stars_10").ProgressValue; got < 12 {
		t.Errorf("stars = %v, want the 12 events rather than the 3 reported", got)
	}
	if !find(t, board, "stars_10").Unlocked() {
		t.Error("Stargazer was not unlocked by twelve watched stars")
	}
}
