package codex

import (
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// Records and lifetime totals: the half of the Codex that is computed on read.
//
// Nothing here is stored. A record is a query, so a restated App Store report
// *improves* the record rather than leaving a stale one behind, and a record
// can never disagree with the vault printed next to it. It also means records
// can only ever go up: there is no row to overwrite with a smaller number.

// DayRecord is a "best ever" day: which day, and what it was worth.
type DayRecord struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// SourceDayRecord is a best-ever day scoped to one store.
type SourceDayRecord struct {
	Source string  `json:"source"`
	Day    string  `json:"day"`
	Value  float64 `json:"value"`
}

// RunRecord is the longest unbroken run of days that earned money, and the day
// it ended on. It is historic and celebratory: a run that ended still happened,
// and Loot never mentions that it did.
type RunRecord struct {
	Days    int    `json:"days"`
	EndedOn string `json:"ended_on"`
}

// Records is every "best ever" the Codex knows.
type Records struct {
	BestRevenueDay         DayRecord         `json:"best_revenue_day"`
	BestRevenueDayBySource []SourceDayRecord `json:"best_revenue_day_by_source"`
	BestUnitsDay           DayRecord         `json:"best_units_day"`
	BestInstallDay         DayRecord         `json:"best_install_day"`
	MostDropsDay           DayRecord         `json:"most_drops_day"`
	MostXPDay              DayRecord         `json:"most_xp_day"`
	MostCountriesDay       DayRecord         `json:"most_countries_day"`
	LongestRevenueRun      RunRecord         `json:"longest_revenue_run"`
	BiggestDrop            *store.BestDrop   `json:"biggest_drop"`
	// FirstEventDay is where the whole history starts.
	FirstEventDay string `json:"first_event_day"`
	// DaysSinceFirstEvent is how long Loot has been watching, in days.
	DaysSinceFirstEvent int `json:"days_since_first_event"`
}

// Totals is the lifetime column: everything that has ever happened, counted.
type Totals struct {
	RevenueBase     float64          `json:"revenue_base"`
	Units           int              `json:"units"`
	Refunds         int              `json:"refunds"`
	Installs        int              `json:"installs"`
	Drops           int              `json:"drops"`
	XP              int              `json:"xp"`
	Era             core.EraProgress `json:"era"`
	ChestsOpened    int              `json:"chests_opened"`
	Countries       int              `json:"countries"`
	Continents      int              `json:"continents"`
	Currencies      int              `json:"currencies"`
	QuestsCompleted int              `json:"quests_completed"`
	MysteriesSolved int              `json:"mysteries_solved"`
	Stars           int              `json:"stars"`
	RecordDays      int              `json:"record_days"`
}

// BuildRecords derives every record and lifetime total from one snapshot pass.
func BuildRecords(s *Snapshot) (Records, Totals) {
	agg := s.Agg

	var rec Records
	rec.FirstEventDay = agg.FirstEventDay
	rec.BiggestDrop = agg.BestDrop

	// Best revenue day, overall and per store. Both come out of the same
	// (source, day) grid, so they can never disagree about which day it was.
	revenueByDay := map[string]float64{}
	unitsByDay := map[string]float64{}
	bestBySource := map[string]DayRecord{}
	for _, d := range agg.LedgerDays {
		revenueByDay[d.Day] += d.Revenue
		unitsByDay[d.Day] += float64(d.Units)
		if best, ok := bestBySource[d.Source]; !ok || d.Revenue > best.Value {
			bestBySource[d.Source] = DayRecord{Day: d.Day, Value: d.Revenue}
		}
	}
	rec.BestRevenueDay = bestOf(fromMap(revenueByDay))
	rec.BestUnitsDay = bestOf(fromMap(unitsByDay))
	rec.BestRevenueDayBySource = []SourceDayRecord{}
	for _, src := range sortedKeys(bestBySource) {
		best := bestBySource[src]
		if best.Value <= 0 {
			continue
		}
		rec.BestRevenueDayBySource = append(rec.BestRevenueDayBySource,
			SourceDayRecord{Source: src, Day: best.Day, Value: best.Value})
	}

	rec.BestInstallDay = bestOf(agg.InstallDays)

	drops := make([]store.DayValue, 0, len(agg.DropDays))
	xp := make([]store.DayValue, 0, len(agg.DropDays))
	for _, d := range agg.DropDays {
		drops = append(drops, store.DayValue{Day: d.Day, Value: float64(d.Count)})
		xp = append(xp, store.DayValue{Day: d.Day, Value: float64(d.XP)})
	}
	rec.MostDropsDay = bestOf(drops)
	rec.MostXPDay = bestOf(xp)
	rec.MostCountriesDay = bestOf(firstDayCounts(agg.CountryFirstDay))

	length, endedOn := LongestRevenueRun(agg)
	rec.LongestRevenueRun = RunRecord{Days: length, EndedOn: endedOn}

	if agg.FirstEventDay != "" && s.Today != "" {
		if first, err := time.Parse(core.DayLayout, agg.FirstEventDay); err == nil {
			if today, err := time.Parse(core.DayLayout, s.Today); err == nil {
				rec.DaysSinceFirstEvent = int(today.Sub(first).Hours()/24) + 1
			}
		}
	}

	var tot Totals
	tot.RevenueBase = roundMoney(last(s.Revenue))
	tot.Units = int(last(s.Units))
	tot.Installs = int(last(s.Installs))
	tot.Drops = int(last(s.Drops))
	tot.XP = int(last(s.XP))
	tot.Era = core.EraFor(tot.XP)
	tot.ChestsOpened = agg.ChestsOpened
	tot.Countries = len(agg.CountryFirstDay)
	tot.Continents = int(last(s.Continents))
	tot.Currencies = len(agg.CurrencyFirstDay)
	tot.QuestsCompleted = int(last(s.Quests))
	tot.MysteriesSolved = int(last(s.Mysteries))
	tot.Stars = int(last(s.Stars))
	tot.RecordDays = int(last(s.Records))
	for _, d := range agg.LedgerDays {
		tot.Refunds += d.Refunds
	}
	return rec, tot
}

// bestOf returns the highest point of a series, ties going to the earlier day
// (the record belongs to whoever set it first).
func bestOf(series []store.DayValue) DayRecord {
	var best DayRecord
	for _, p := range series {
		if p.Value > best.Value {
			best = DayRecord{Day: p.Day, Value: roundMoney(p.Value)}
		}
	}
	return best
}

// last is the final value of a series, 0 for an empty one.
func last(series []store.DayValue) float64 {
	if len(series) == 0 {
		return 0
	}
	return series[len(series)-1].Value
}
