package codex

import (
	"sort"

	"github.com/nickhirras/loot/internal/store"
)

// A Snapshot is one pass over the whole history, reshaped into the cumulative
// series every achievement reads.
//
// Cumulative is the important word. A running total answers two questions at
// once: *how far along am I* (its last point) and *when did I get there* (the
// first point at or above the target). The second is what lets Loot unlock a
// trophy with the date it was actually earned when it first looks at a
// database full of history, instead of stamping twenty-five of them "today".
//
// Two series are running *maxima* rather than sums, for the same reason:
// subscribers are a level that can fall, and a run of consecutive revenue days
// ends. Both achievements celebrate the high-water mark, so both series only
// ever climb, and neither trophy can ever be un-earned.
type Snapshot struct {
	Agg store.CodexAggregates
	// Today is the local business day evaluation is running on.
	Today string
	// Currency is the display currency the money series is denominated in.
	Currency string

	Revenue    []store.DayValue
	Units      []store.DayValue
	Installs   []store.DayValue
	Drops      []store.DayValue
	XP         []store.DayValue
	Countries  []store.DayValue
	Continents []store.DayValue
	Currencies []store.DayValue
	Chests     []store.DayValue
	Quests     []store.DayValue
	Mysteries  []store.DayValue
	Stars      []store.DayValue
	Records    []store.DayValue
	// LedgerSources is the number of distinct stores that have ever reported
	// ledger money.
	LedgerSources []store.DayValue
	// Subscribers is the highest active subscriber level ever reported.
	Subscribers []store.DayValue
	// RevenueRun is the longest run of consecutive ledger-revenue days ending
	// at or before each day — a high-water mark, so the trophy survives the
	// run ending.
	RevenueRun []store.DayValue
}

// NewSnapshot reshapes one CodexAggregates into the series the catalog reads.
func NewSnapshot(agg store.CodexAggregates, today, currency string) *Snapshot {
	s := &Snapshot{Agg: agg, Today: today, Currency: currency}

	// Ledger money and units, summed across sources into one day each.
	revenueByDay := map[string]float64{}
	unitsByDay := map[string]float64{}
	for _, d := range agg.LedgerDays {
		revenueByDay[d.Day] += d.Revenue
		unitsByDay[d.Day] += float64(d.Units)
	}
	s.Revenue = cumulative(fromMap(revenueByDay))
	s.Units = cumulative(fromMap(unitsByDay))
	s.Installs = cumulative(agg.InstallDays)

	dropsByDay := make([]store.DayValue, 0, len(agg.DropDays))
	xpByDay := make([]store.DayValue, 0, len(agg.DropDays))
	recordsByDay := make([]store.DayValue, 0, len(agg.DropDays))
	for _, d := range agg.DropDays {
		dropsByDay = append(dropsByDay, store.DayValue{Day: d.Day, Value: float64(d.Count)})
		xpByDay = append(xpByDay, store.DayValue{Day: d.Day, Value: float64(d.XP)})
		// A record *day*, not a record drop: three apps each setting a high on
		// the same day is one "best day ever", not three.
		if d.Records > 0 {
			recordsByDay = append(recordsByDay, store.DayValue{Day: d.Day, Value: 1})
		}
	}
	store.SortDayValues(dropsByDay)
	store.SortDayValues(xpByDay)
	store.SortDayValues(recordsByDay)
	s.Drops = cumulative(dropsByDay)
	s.XP = cumulative(xpByDay)
	s.Records = cumulative(recordsByDay)

	s.Countries = cumulative(firstDayCounts(agg.CountryFirstDay))
	s.Currencies = cumulative(firstDayCounts(agg.CurrencyFirstDay))
	s.LedgerSources = cumulative(firstDayCounts(agg.LedgerSourceFirstDay))
	s.Continents = cumulative(firstDayCounts(continentFirstDays(agg.CountryFirstDay)))

	s.Chests = cumulative(agg.ChestDays)
	s.Quests = cumulative(agg.QuestDays)
	s.Mysteries = cumulative(agg.MysteryDays)
	s.Stars = cumulative(agg.StarDays)

	s.Subscribers = runningMax(agg.SubscriberDays)
	s.RevenueRun = runningMax(revenueRunLengths(fromMap(revenueByDay)))
	return s
}

// continentFirstDays maps each inhabited continent to the day its first
// settlement was founded. A country the continent table does not know
// contributes nothing rather than being filed somewhere plausible.
func continentFirstDays(countryFirst map[string]string) map[string]string {
	out := map[string]string{}
	for country, day := range countryFirst {
		continent := ContinentOf(country)
		if continent == "" {
			continue
		}
		if prev, ok := out[continent]; !ok || day < prev {
			out[continent] = day
		}
	}
	return out
}

// firstDayCounts turns a key -> first day map into "how many keys appeared for
// the first time on this day", oldest day first.
func firstDayCounts(firstDay map[string]string) []store.DayValue {
	byDay := map[string]float64{}
	for _, day := range firstDay {
		if day == "" {
			continue
		}
		byDay[day]++
	}
	return fromMap(byDay)
}

// fromMap turns a day -> value map into a series ordered oldest first.
func fromMap(byDay map[string]float64) []store.DayValue {
	out := make([]store.DayValue, 0, len(byDay))
	for day, v := range byDay {
		out = append(out, store.DayValue{Day: day, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// cumulative turns a per-day series into a running total.
func cumulative(series []store.DayValue) []store.DayValue {
	out := make([]store.DayValue, 0, len(series))
	total := 0.0
	for _, p := range series {
		total += p.Value
		out = append(out, store.DayValue{Day: p.Day, Value: total})
	}
	return out
}

// runningMax turns a per-day level into its high-water mark. A level that
// falls does not un-earn the trophy it once paid for.
func runningMax(series []store.DayValue) []store.DayValue {
	out := make([]store.DayValue, 0, len(series))
	best := 0.0
	for _, p := range series {
		if p.Value > best {
			best = p.Value
		}
		out = append(out, store.DayValue{Day: p.Day, Value: best})
	}
	return out
}

// revenueRunLengths returns, for each day that earned ledger revenue, how long
// the unbroken run of revenue days ending on it is. A gap resets the count; a
// day with zero (or negative, after refunds) revenue is not a revenue day.
func revenueRunLengths(series []store.DayValue) []store.DayValue {
	out := make([]store.DayValue, 0, len(series))
	run := 0
	prev := ""
	for _, p := range series {
		if p.Value <= 0 {
			run = 0
			prev = p.Day
			continue
		}
		if prev != "" && nextDay(prev) == p.Day {
			run++
		} else {
			run = 1
		}
		prev = p.Day
		out = append(out, store.DayValue{Day: p.Day, Value: float64(run)})
	}
	return out
}

// LongestRevenueRun is the longest run of consecutive ledger-revenue days in
// the whole history, and the day it ended on. It is both an achievement's
// series and one of the Codex's records.
func LongestRevenueRun(agg store.CodexAggregates) (length int, endedOn string) {
	byDay := map[string]float64{}
	for _, d := range agg.LedgerDays {
		byDay[d.Day] += d.Revenue
	}
	for _, p := range revenueRunLengths(fromMap(byDay)) {
		if int(p.Value) > length {
			length = int(p.Value)
			endedOn = p.Day
		}
	}
	return length, endedOn
}
