package codex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The season recap: one month (or one year) of loot, written up as the thing
// you would screenshot.
//
// It is a *recap*, not a report. Every number is stated plainly and nothing is
// scored: a month that earned less than the one before it says so as a fact —
// "$412 · $610 last month" — and then gets on with the countries you settled
// and the trophies you won. There is no red, no target you missed and no
// judgement anywhere in this file, which is why the deltas below carry a
// direction the UI can colour *neutrally* rather than a verdict.

// Period is the window a recap covers.
type Period struct {
	// Kind is "month" or "season" (a whole calendar year).
	Kind string `json:"kind"`
	// Key is what the API accepts to ask for it again: "2026-07" or "2026".
	Key string `json:"key"`
	// Label is how it reads: "July 2026", "2026".
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
	Days  int    `json:"days"`
	// Partial is true while the window is still running, so the UI can say
	// "so far" rather than implying a finished month.
	Partial bool `json:"partial"`
}

// Delta is a period-over-period change, stated neutrally: what it was, what
// changed, and which way — never whether that is good.
type Delta struct {
	Previous float64 `json:"previous"`
	Change   float64 `json:"change"`
	// Pct is the fractional change (0.12 for +12%), 0 without a basis.
	Pct float64 `json:"pct"`
	// Direction is "up", "down" or "flat".
	Direction string `json:"direction"`
	// HasBasis is false when the previous period had nothing in it, in which
	// case a percentage would be theatre rather than information.
	HasBasis bool `json:"has_basis"`
}

// newDelta compares two numbers without an opinion about either.
func newDelta(current, previous float64) Delta {
	d := Delta{Previous: roundMoney(previous), Change: roundMoney(current - previous), Direction: "flat"}
	if previous == 0 {
		return d
	}
	d.HasBasis = true
	d.Pct = roundN(d.Change/absF(previous), 4)
	switch {
	case d.Change > 0:
		d.Direction = "up"
	case d.Change < 0:
		d.Direction = "down"
	}
	return d
}

// RecapTop is one "the biggest of these" answer.
type RecapTop struct {
	Key         string  `json:"key"`
	RevenueBase float64 `json:"revenue_base"`
	Units       int     `json:"units"`
}

// Recap is the whole of GET /api/recap.
type Recap struct {
	Period          Period `json:"period"`
	DisplayCurrency string `json:"display_currency"`
	// Empty is true when nothing at all happened in the window, so the UI can
	// say so kindly instead of drawing a poster full of zeros.
	Empty bool `json:"empty"`

	RevenueBase  float64 `json:"revenue_base"`
	RevenueDelta Delta   `json:"revenue_delta"`
	Units        int     `json:"units"`
	UnitsDelta   Delta   `json:"units_delta"`
	Refunds      int     `json:"refunds"`
	Installs     int     `json:"installs"`

	NewCountries []store.RecapCountry `json:"new_countries"`
	BestDay      DayRecord            `json:"best_day"`
	TopApp       RecapTop             `json:"top_app"`
	TopCountry   RecapTop             `json:"top_country"`
	TopSource    RecapTop             `json:"top_source"`

	Drops         int            `json:"drops"`
	DropsByRarity map[string]int `json:"drops_by_rarity"`
	// TopRarity is the rarest rarity that actually dropped this period; the
	// poster takes its gradient from it.
	TopRarity string `json:"top_rarity"`
	XP        int    `json:"xp"`

	LevelStart int    `json:"level_start"`
	LevelEnd   int    `json:"level_end"`
	EraStart   string `json:"era_start"`
	EraEnd     string `json:"era_end"`

	ChestsOpened    int                `json:"chests_opened"`
	QuestsCompleted int                `json:"quests_completed"`
	MysteriesSolved int                `json:"mysteries_solved"`
	Achievements    []core.Achievement `json:"achievements_unlocked"`

	// Highlights are short, ordered, already-written lines: the caption of the
	// poster. Best news first.
	Highlights []string `json:"highlights"`
	// Series is revenue per day, zero-filled, for the sparkline.
	Series []store.DayValue `json:"series"`
}

// ResolvePeriod turns the API's `month` and `season` parameters into a window.
// With neither, the answer is the last *complete* month: a recap of a month
// still in progress is a half-told story, and the default should be the one
// worth sharing.
func ResolvePeriod(now time.Time, month, season string) (Period, error) {
	today := core.DayOf(now)

	if season = strings.TrimSpace(season); season != "" {
		year, err := strconv.Atoi(season)
		if err != nil || year < 1970 || year > 9999 {
			return Period{}, fmt.Errorf("season must be a year, e.g. 2026")
		}
		start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(1, 0, -1)
		return period("season", season, strconv.Itoa(year), start, end, today), nil
	}

	if month = strings.TrimSpace(month); month != "" {
		t, err := time.Parse("2006-01", month)
		if err != nil {
			return Period{}, fmt.Errorf("month must be YYYY-MM")
		}
		return monthPeriod(t, today), nil
	}

	// The default: last month, whole.
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return monthPeriod(first.AddDate(0, -1, 0), today), nil
}

func monthPeriod(anyDayOfMonth time.Time, today string) Period {
	start := time.Date(anyDayOfMonth.Year(), anyDayOfMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	return period("month", start.Format("2006-01"), start.Format("January 2006"), start, end, today)
}

func period(kind, key, label string, start, end time.Time, today string) Period {
	p := Period{
		Kind:  kind,
		Key:   key,
		Label: label,
		From:  core.DayOf(start),
		To:    core.DayOf(end),
	}
	p.Days = int(end.Sub(start).Hours()/24) + 1
	p.Partial = p.To >= today
	return p
}

// previous returns the window immediately before p: the month before a month,
// the year before a year.
func (p Period) previous() (from, to string) {
	start, ok := parseDay(p.From)
	if !ok {
		return p.From, p.To
	}
	if p.Kind == "season" {
		prev := start.AddDate(-1, 0, 0)
		return core.DayOf(prev), core.DayOf(prev.AddDate(1, 0, -1))
	}
	prev := start.AddDate(0, -1, 0)
	return core.DayOf(prev), core.DayOf(prev.AddDate(0, 1, -1))
}

// BuildRecap aggregates a period and the one before it, and writes the
// highlights.
func BuildRecap(ctx context.Context, st *store.Store, p Period, displayCurrency string) (Recap, error) {
	agg, err := st.RecapWindow(ctx, p.From, p.To)
	if err != nil {
		return Recap{}, err
	}
	prevFrom, prevTo := p.previous()
	prev, err := st.RecapWindow(ctx, prevFrom, prevTo)
	if err != nil {
		return Recap{}, err
	}
	unlocked, err := st.AchievementsUnlockedBetween(ctx, p.From, p.To)
	if err != nil {
		return Recap{}, err
	}
	decorate(unlocked)

	r := Recap{
		Period:          p,
		DisplayCurrency: displayCurrency,
		RevenueBase:     agg.RevenueBase,
		RevenueDelta:    newDelta(agg.RevenueBase, prev.RevenueBase),
		Units:           agg.Units,
		UnitsDelta:      newDelta(float64(agg.Units), float64(prev.Units)),
		Refunds:         agg.Refunds,
		Installs:        agg.Installs,
		NewCountries:    agg.NewCountries,
		BestDay:         DayRecord{Day: agg.BestDay, Value: agg.BestDayRevenue},
		TopApp:          RecapTop{Key: agg.TopApp.Key, RevenueBase: agg.TopApp.RevenueBase, Units: agg.TopApp.Units},
		TopCountry:      RecapTop{Key: agg.TopCountry.Key, RevenueBase: agg.TopCountry.RevenueBase, Units: agg.TopCountry.Units},
		TopSource:       RecapTop{Key: agg.TopSource.Key, RevenueBase: agg.TopSource.RevenueBase, Units: agg.TopSource.Units},
		Drops:           agg.Drops,
		DropsByRarity:   agg.ByRarity,
		TopRarity:       topRarity(agg.ByRarity),
		XP:              agg.XP,
		ChestsOpened:    agg.ChestsOpened,
		QuestsCompleted: agg.QuestsCompleted,
		MysteriesSolved: agg.MysteriesSolved,
		Achievements:    unlocked,
		Series:          agg.Series,
	}

	r.LevelStart = core.LevelFor(agg.XPBefore)
	r.LevelEnd = core.LevelFor(agg.XPBefore + agg.XP)
	r.EraStart = core.EraFor(agg.XPBefore).Name
	r.EraEnd = core.EraFor(agg.XPBefore + agg.XP).Name

	r.Empty = agg.Drops == 0 && agg.RevenueBase == 0 && agg.Units == 0 &&
		agg.Installs == 0 && len(agg.NewCountries) == 0
	r.Highlights = highlights(r, agg, displayCurrency)
	return r, nil
}

// maxHighlights is how many lines the poster's caption gets. More than this
// stops being a highlight reel and becomes a table.
const maxHighlights = 7

// highlights writes the caption, best news first. Every line is a fact; none
// of them is a score.
func highlights(r Recap, agg store.RecapAggregates, currency string) []string {
	out := []string{}
	add := func(format string, args ...any) {
		if len(out) < maxHighlights {
			out = append(out, fmt.Sprintf(format, args...))
		}
	}

	if r.BestDay.Day != "" && r.BestDay.Value > 0 {
		add("Best day on %s: %s", dayLabel(r.BestDay.Day), core.FormatMoney(r.BestDay.Value, currency))
	}
	// Trophies before countries: an achievement is the rarer event.
	switch n := len(r.Achievements); {
	case n == 1:
		add("Unlocked %s", r.Achievements[0].Title)
	case n > 1:
		add("Unlocked %s and %d more achievement%s", r.Achievements[0].Title, n-1, plural(n-1))
	}
	if n := len(r.NewCountries); n > 0 {
		first := r.NewCountries[0]
		add("Settled %s %s on %s%s", core.FlagEmoji(first.Country), first.Country, dayLabel(first.Day),
			moreCountries(n-1))
	}
	if n := r.DropsByRarity["legendary"]; n > 0 {
		add("%d legendary drop%s", n, plural(n))
	} else if n := r.DropsByRarity["epic"]; n > 0 {
		add("%d epic drop%s", n, plural(n))
	}
	if r.EraEnd != r.EraStart {
		add("Reached the %s era", r.EraEnd)
	} else if r.LevelEnd > r.LevelStart {
		add("Level %d → %d", r.LevelStart, r.LevelEnd)
	}
	if agg.MostCountries > 1 {
		add("%d new countries on %s alone", agg.MostCountries, dayLabel(agg.MostCountriesDay))
	}
	if r.QuestsCompleted > 0 {
		add("%d quest%s completed", r.QuestsCompleted, plural(r.QuestsCompleted))
	}
	if r.MysteriesSolved > 0 {
		add("%d myster%s explained", r.MysteriesSolved, pluralY(r.MysteriesSolved))
	}
	if r.ChestsOpened > 0 {
		add("%d chest%s opened", r.ChestsOpened, plural(r.ChestsOpened))
	}
	if r.TopCountry.Key != "" && r.TopCountry.RevenueBase > 0 {
		add("%s %s was your biggest market", core.FlagEmoji(r.TopCountry.Key), r.TopCountry.Key)
	}
	return out
}

func moreCountries(more int) string {
	if more <= 0 {
		return ""
	}
	return fmt.Sprintf(" (and %d more countr%s)", more, pluralY(more))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// topRarity is the rarest rarity that actually dropped in the window, ignoring
// cursed — a poster does not take its colour from a cancellation.
func topRarity(byRarity map[string]int) string {
	best := ""
	bestRank := -1
	for _, r := range core.Rarities {
		if r == core.Cursed {
			continue
		}
		if byRarity[string(r)] == 0 {
			continue
		}
		if r.Rank() > bestRank {
			bestRank, best = r.Rank(), string(r)
		}
	}
	return best
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func roundN(v float64, places int) float64 {
	pow := 1.0
	for i := 0; i < places; i++ {
		pow *= 10
	}
	return float64(int64(v*pow+copySign(0.5, v))) / pow
}

func copySign(v, sign float64) float64 {
	if sign < 0 {
		return -v
	}
	return v
}
