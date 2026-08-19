// Package codex is Loot's permanent record: achievements that only ever
// unlock, records that only ever improve, and a season recap that celebrates
// what happened.
//
// The one rule, and it is the same rule quests are built on:
//
//	Nothing here can ever be lost.
//
// A trophy, once earned, is earned. There is no decay, no expiry, no "you
// dropped below the threshold" — a "Steady" achievement for revenue on thirty
// consecutive days celebrates the run that happened and never notices that it
// ended. A record is only ever beaten. A recap states a decline as a plain
// fact and never as a verdict.
//
// Unlocks pay a real drop through the real pipeline, so the reward is the same
// variable, noisy, satisfying thing every other drop is: bronze is uncommon,
// silver rare, gold epic, legendary legendary.
package codex

import (
	"fmt"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// Entry is one catalog achievement: what it is called, what it takes, and how
// to read progress towards it out of a Snapshot.
//
// Every entry is answered from the *same* snapshot — one pass over the history
// per evaluation, not one query per achievement — which is what makes running
// the whole catalog after every ingest affordable.
type Entry struct {
	// Key is the permanent identifier. It is what the unlock's dedupe key is
	// built from, so it must never be reused for a different achievement.
	Key   string
	Tier  core.AchievementTier
	Title string
	// Description is the one line the card shows: what it took.
	Description string
	// Target is what Progress must reach. A one-off ("the first time X
	// happened") has a target of 1.
	Target float64
	// Unit is the noun the progress line reads in: "countries", "chests".
	Unit string
	// Money marks progress denominated in the display currency.
	Money bool

	// Series is the cumulative progression this trophy measures, oldest day
	// first. The current value is its last point, and the day it was earned is
	// the first day the running total reached Target — which is what lets a
	// backfill unlock with the date it actually happened rather than today's.
	Series func(s *Snapshot) []store.DayValue
	// Day answers a one-off trophy directly: the business day the thing first
	// happened, or "" for never. Entries set exactly one of Series and Day.
	Day func(s *Snapshot) string
}

// Progress reads one entry against a snapshot: how far along it is, and — if
// it is finished — the day it was finished on.
func (e Entry) Progress(s *Snapshot) (value float64, earnedDay string, unlocked bool) {
	if e.Day != nil {
		day := e.Day(s)
		if day == "" {
			return 0, "", false
		}
		return 1, day, true
	}
	series := e.Series(s)
	if len(series) == 0 {
		return 0, "", false
	}
	value = series[len(series)-1].Value
	for _, p := range series {
		if p.Value >= e.Target {
			return value, p.Day, true
		}
	}
	return value, "", false
}

// tiers by threshold, so the catalog below reads as a table rather than as a
// paragraph of struct literals.
func count(key string, tier core.AchievementTier, title, unit, description string, target float64,
	series func(*Snapshot) []store.DayValue) Entry {
	return Entry{Key: key, Tier: tier, Title: title, Description: description,
		Target: target, Unit: unit, Series: series}
}

func money(key string, tier core.AchievementTier, title, description string, target float64,
	series func(*Snapshot) []store.DayValue) Entry {
	return Entry{Key: key, Tier: tier, Title: title, Description: description,
		Target: target, Unit: "revenue", Money: true, Series: series}
}

func first(key string, tier core.AchievementTier, title, description string,
	day func(*Snapshot) string) Entry {
	return Entry{Key: key, Tier: tier, Title: title, Description: description,
		Target: 1, Unit: "", Day: day}
}

// Catalog is every achievement Loot ships with, in the order the trophy wall
// lists them: the firsts, then the ladders, then the milestones, then the
// eras. Adding one is a new line here and nothing else — the evaluator, the
// API and the UI all read this list.
//
// Two rules for editing it:
//
//   - **Never reuse a key.** A key is the identity of a trophy somebody may
//     already own; changing what it means would rewrite their history.
//   - **Never raise a target.** Lowering one is fine (more people have it
//     already); raising one would un-earn a trophy, which the Codex does not
//     do. Ship a new key at the higher threshold instead.
var Catalog = []Entry{
	// ------------------------------------------------------------- firsts
	first("first_blood", core.TierBronze, "First blood",
		"Your first ever drop — the moment Loot had something to report.",
		func(s *Snapshot) string { return s.Agg.FirstDropDay }),
	first("first_sale", core.TierBronze, "First sale",
		"The first settled sale in a store's own financial report.",
		func(s *Snapshot) string { return s.Agg.FirstSaleDay }),
	first("first_subscriber", core.TierBronze, "First subscriber",
		"Somebody decided to keep paying you.",
		func(s *Snapshot) string { return s.Agg.FirstSubscriberDay }),
	first("legendary_hunter", core.TierGold, "Legendary hunter",
		"Your first legendary drop.",
		func(s *Snapshot) string { return s.Agg.FirstLegendaryDay }),
	first("cursed_but_unbowed", core.TierSilver, "Cursed but unbowed",
		"A cursed drop, and then something rare or better within the day. It turned around.",
		func(s *Snapshot) string { return s.Agg.CursedRecoveryDay }),

	// --------------------------------------------------------- settlements
	count("settler_1", core.TierBronze, "Settler I", "countries",
		"Customers in 5 countries.", 5, func(s *Snapshot) []store.DayValue { return s.Countries }),
	count("settler_2", core.TierBronze, "Settler II", "countries",
		"Customers in 10 countries.", 10, func(s *Snapshot) []store.DayValue { return s.Countries }),
	count("settler_3", core.TierSilver, "Settler III", "countries",
		"Customers in 25 countries.", 25, func(s *Snapshot) []store.DayValue { return s.Countries }),
	count("settler_4", core.TierGold, "Settler IV", "countries",
		"Customers in 50 countries.", 50, func(s *Snapshot) []store.DayValue { return s.Countries }),
	count("cartographer", core.TierLegendary, "Cartographer", "continents",
		"A settlement on every inhabited continent.", float64(len(InhabitedContinents)),
		func(s *Snapshot) []store.DayValue { return s.Continents }),

	// -------------------------------------------------------------- chests
	count("hoarder_1", core.TierBronze, "Hoarder", "chests",
		"Open 10 daily chests.", 10, func(s *Snapshot) []store.DayValue { return s.Chests }),
	count("hoarder_2", core.TierSilver, "Great hoarder", "chests",
		"Open 50 daily chests.", 50, func(s *Snapshot) []store.DayValue { return s.Chests }),
	count("hoarder_3", core.TierGold, "Dragon hoarder", "chests",
		"Open 200 daily chests.", 200, func(s *Snapshot) []store.DayValue { return s.Chests }),

	// ---------------------------------------------------------- currencies
	count("polyglot_1", core.TierBronze, "Polyglot", "currencies",
		"Take money in 5 different currencies.", 5,
		func(s *Snapshot) []store.DayValue { return s.Currencies }),
	count("polyglot_2", core.TierSilver, "Money changer", "currencies",
		"Take money in 10 different currencies.", 10,
		func(s *Snapshot) []store.DayValue { return s.Currencies }),

	// -------------------------------------------------------------- steady
	// Celebrated when reached, and never taken away. A run that ended still
	// happened; Loot has no streak to break.
	count("steady_7", core.TierBronze, "Steady", "days",
		"Ledger revenue on 7 consecutive days.", 7,
		func(s *Snapshot) []store.DayValue { return s.RevenueRun }),
	count("steady_30", core.TierSilver, "Steady as she goes", "days",
		"Ledger revenue on 30 consecutive days.", 30,
		func(s *Snapshot) []store.DayValue { return s.RevenueRun }),

	// ------------------------------------------------------------- revenue
	money("revenue_100", core.TierBronze, "First hundred",
		"$100 of lifetime revenue.", 100, func(s *Snapshot) []store.DayValue { return s.Revenue }),
	money("revenue_1k", core.TierSilver, "Four figures",
		"$1,000 of lifetime revenue.", 1_000, func(s *Snapshot) []store.DayValue { return s.Revenue }),
	money("revenue_10k", core.TierGold, "Five figures",
		"$10,000 of lifetime revenue.", 10_000, func(s *Snapshot) []store.DayValue { return s.Revenue }),
	money("revenue_100k", core.TierLegendary, "Six figures",
		"$100,000 of lifetime revenue.", 100_000, func(s *Snapshot) []store.DayValue { return s.Revenue }),

	// --------------------------------------------------------------- units
	count("units_100", core.TierBronze, "A hundred sold", "units",
		"100 lifetime paid units.", 100, func(s *Snapshot) []store.DayValue { return s.Units }),
	count("units_1k", core.TierSilver, "A thousand sold", "units",
		"1,000 lifetime paid units.", 1_000, func(s *Snapshot) []store.DayValue { return s.Units }),
	count("units_10k", core.TierGold, "Ten thousand sold", "units",
		"10,000 lifetime paid units.", 10_000, func(s *Snapshot) []store.DayValue { return s.Units }),
	count("units_100k", core.TierLegendary, "A hundred thousand sold", "units",
		"100,000 lifetime paid units.", 100_000, func(s *Snapshot) []store.DayValue { return s.Units }),

	// ------------------------------------------------------------ installs
	count("installs_1k", core.TierBronze, "Word of mouth", "installs",
		"1,000 lifetime installs.", 1_000, func(s *Snapshot) []store.DayValue { return s.Installs }),
	count("installs_10k", core.TierSilver, "Going around", "installs",
		"10,000 lifetime installs.", 10_000, func(s *Snapshot) []store.DayValue { return s.Installs }),
	count("installs_100k", core.TierGold, "Everywhere", "installs",
		"100,000 lifetime installs.", 100_000, func(s *Snapshot) []store.DayValue { return s.Installs }),

	// --------------------------------------------------------- subscribers
	count("subscribers_10", core.TierBronze, "A congregation", "subscribers",
		"10 active subscribers.", 10, func(s *Snapshot) []store.DayValue { return s.Subscribers }),
	count("subscribers_100", core.TierSilver, "A following", "subscribers",
		"100 active subscribers.", 100, func(s *Snapshot) []store.DayValue { return s.Subscribers }),
	count("subscribers_1000", core.TierGold, "A movement", "subscribers",
		"1,000 active subscribers.", 1_000, func(s *Snapshot) []store.DayValue { return s.Subscribers }),

	// -------------------------------------------------------------- quests
	count("quests_1", core.TierBronze, "Questing", "quests",
		"Complete a quest.", 1, func(s *Snapshot) []store.DayValue { return s.Quests }),
	count("quests_10", core.TierSilver, "Adventurer", "quests",
		"Complete 10 quests.", 10, func(s *Snapshot) []store.DayValue { return s.Quests }),
	count("quests_50", core.TierGold, "Hero of the realm", "quests",
		"Complete 50 quests.", 50, func(s *Snapshot) []store.DayValue { return s.Quests }),

	// ----------------------------------------------------------- mysteries
	count("mysteries_1", core.TierBronze, "Detective", "mysteries",
		"Explain a mystery.", 1, func(s *Snapshot) []store.DayValue { return s.Mysteries }),
	count("mysteries_10", core.TierSilver, "Lab notebook", "mysteries",
		"Explain 10 mysteries.", 10, func(s *Snapshot) []store.DayValue { return s.Mysteries }),

	// --------------------------------------------------------------- stars
	count("stars_10", core.TierBronze, "Stargazer", "stars",
		"10 GitHub stars.", 10, func(s *Snapshot) []store.DayValue { return s.Stars }),
	count("stars_100", core.TierSilver, "Constellation", "stars",
		"100 GitHub stars.", 100, func(s *Snapshot) []store.DayValue { return s.Stars }),
	count("stars_1000", core.TierGold, "Galaxy", "stars",
		"1,000 GitHub stars.", 1_000, func(s *Snapshot) []store.DayValue { return s.Stars }),

	// -------------------------------------------------------- many stores
	count("merchant_2", core.TierBronze, "Two worlds", "stores",
		"Ledger revenue from 2 different stores.", 2,
		func(s *Snapshot) []store.DayValue { return s.LedgerSources }),
	count("merchant_3", core.TierSilver, "Three worlds", "stores",
		"Ledger revenue from 3 different stores.", 3,
		func(s *Snapshot) []store.DayValue { return s.LedgerSources }),
	count("merchant_5", core.TierGold, "Merchant of many worlds", "stores",
		"Ledger revenue from 5 different stores.", 5,
		func(s *Snapshot) []store.DayValue { return s.LedgerSources }),

	// ------------------------------------------------------- record days
	count("record_1", core.TierBronze, "Best day ever", "record days",
		"A day that beat every day before it.", 1,
		func(s *Snapshot) []store.DayValue { return s.Records }),
	count("record_5", core.TierSilver, "Best day ever ×5", "record days",
		"Five days that each beat everything before them.", 5,
		func(s *Snapshot) []store.DayValue { return s.Records }),
	count("record_25", core.TierGold, "Best day ever ×25", "record days",
		"Twenty-five record days.", 25,
		func(s *Snapshot) []store.DayValue { return s.Records }),

	// ---------------------------------------------------------------- eras
	count("era_town", core.TierBronze, "Town", "XP",
		"Reach the Town era.", eraXP("Town"), func(s *Snapshot) []store.DayValue { return s.XP }),
	count("era_city", core.TierSilver, "City", "XP",
		"Reach the City era.", eraXP("City"), func(s *Snapshot) []store.DayValue { return s.XP }),
	count("era_kingdom", core.TierGold, "Kingdom", "XP",
		"Reach the Kingdom era.", eraXP("Kingdom"), func(s *Snapshot) []store.DayValue { return s.XP }),
	count("era_empire", core.TierLegendary, "Empire", "XP",
		"Reach the Empire era.", eraXP("Empire"), func(s *Snapshot) []store.DayValue { return s.XP }),
}

// eraXP reads a threshold out of the one place eras are defined, so tuning the
// ladder in internal/core/hearth.go moves these achievements with it.
func eraXP(name string) float64 {
	for _, e := range core.Eras {
		if e.Name == name {
			return float64(e.MinXP)
		}
	}
	panic(fmt.Sprintf("codex: unknown era %q", name))
}

// catalogIndex is the canonical display order, and the guard against a
// duplicate key slipping into the table above.
var catalogIndex = func() map[string]int {
	out := make(map[string]int, len(Catalog))
	for i, e := range Catalog {
		if _, dup := out[e.Key]; dup {
			panic("codex: duplicate achievement key " + e.Key)
		}
		if !e.Tier.Valid() {
			panic("codex: unknown tier for achievement " + e.Key)
		}
		if (e.Series == nil) == (e.Day == nil) {
			panic("codex: achievement " + e.Key + " must set exactly one of Series and Day")
		}
		out[e.Key] = i
	}
	return out
}()

// CatalogOrder returns an achievement key's position in the catalog, and
// len(Catalog) for a key the catalog no longer contains — so a retired trophy
// somebody owns still renders, at the end.
func CatalogOrder(key string) int {
	if i, ok := catalogIndex[key]; ok {
		return i
	}
	return len(Catalog)
}
