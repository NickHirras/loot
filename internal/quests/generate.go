package quests

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The generator writes the week's quests for you, from your own history:
// "beat last week" is a target you have already proved is reachable, which is
// the only kind of target worth putting on a dashboard somebody has to look at
// every day.
//
// Three properties keep it out of the way:
//
//   - **idempotent**: one quest per (metric, app, source, window), enforced by
//     a unique dedupe key, so it may run at every startup and every midnight.
//   - **only where there is data**: a database that has never seen a ledger
//     row gets no revenue quest, and one that has never seen a star gets no
//     star quest. An impossible goal is worse than no goal.
//   - **capped**: at most MaxActive generated quests are ever running at once,
//     because a board of twelve is a chore list.

// DefaultMaxActive is how many generated quests may run at the same time.
const DefaultMaxActive = 6

// growth factors: last window plus a nudge, not a leap.
const (
	growthStandard = 1.05
	growthXP       = 1.10
)

// newCountriesTarget is the monthly "sell somewhere new" goal.
const newCountriesTarget = 2

// floors keep a target meaningful when the previous window was tiny: beating
// $0.40 by 5% is not a quest.
var floors = map[core.Metric]float64{
	core.MetricRevenue:  10,
	core.MetricUnits:    3,
	core.MetricInstalls: 10,
	core.MetricStars:    3,
	core.MetricXP:       100,
}

// Generator writes auto quests for the current week and month.
type Generator struct {
	Store  *store.Store
	Logger *slog.Logger
	// DisplayCurrency is what a money target is written in.
	DisplayCurrency string
	// MaxActive caps how many auto quests may be running at once.
	MaxActive int
	// Now is the clock, in local time.
	Now func() time.Time
}

func (g *Generator) now() time.Time {
	if g.Now == nil {
		return time.Now()
	}
	return g.Now()
}

func (g *Generator) log() *slog.Logger {
	if g.Logger == nil {
		return slog.Default()
	}
	return g.Logger
}

// candidate is one quest the generator would like to create, before it knows
// whether there is history to justify it or room on the board for it.
type candidate struct {
	metric core.Metric
	start  string
	end    string
	// prevStart and prevEnd are the window the target is measured from; empty
	// for a fixed target like "two new countries".
	prevStart string
	prevEnd   string
	growth    float64
	// fixed is a target that does not come from history at all.
	fixed float64
	// title renders the final title once the target is known.
	title func(target string) string
}

// Run creates whatever the current week and month are missing. It returns the
// quests it actually created — an already-present quest is not an error and
// not a creation.
//
// It retires last window's quests *first*. Generation happens minutes after
// midnight, and on a Monday every one of last week's quests is still active at
// that moment: counting them towards MaxActive would leave the board with no
// room for the new week's, and because `lastGen` latches for the day the board
// would stay short until the next midnight. Expiry is idempotent and silent,
// so running it here as well as in Check costs nothing.
func (g *Generator) Run(ctx context.Context) ([]core.Quest, error) {
	now := g.now()
	today := now.Format(core.DayLayout)
	if _, err := g.Store.ExpireQuests(ctx, today, now.UTC()); err != nil {
		return nil, err
	}

	weekStart, weekEnd := WeekWindow(now)
	monthStart, monthEnd := MonthWindow(now)
	prevWeekStart, prevWeekEnd := WeekWindow(now.AddDate(0, 0, -7))
	prevMonthStart, prevMonthEnd := MonthWindow(now.AddDate(0, 0, -now.Day()))

	available, err := g.Store.MetricsWithData(ctx)
	if err != nil {
		return nil, err
	}

	// Priority order: the week first (it is the window somebody can still do
	// something about), then the month's slower goals.
	candidates := []candidate{
		{
			metric: core.MetricRevenue, start: weekStart, end: weekEnd,
			prevStart: prevWeekStart, prevEnd: prevWeekEnd, growth: growthStandard,
			title: func(t string) string { return "Beat last week's revenue · " + t },
		},
		{
			metric: core.MetricUnits, start: weekStart, end: weekEnd,
			prevStart: prevWeekStart, prevEnd: prevWeekEnd, growth: growthStandard,
			title: func(t string) string { return "Units: beat last week · " + t },
		},
		{
			metric: core.MetricInstalls, start: weekStart, end: weekEnd,
			prevStart: prevWeekStart, prevEnd: prevWeekEnd, growth: growthStandard,
			title: func(t string) string { return "Installs: beat last week · " + t },
		},
		{
			metric: core.MetricXP, start: weekStart, end: weekEnd,
			prevStart: prevWeekStart, prevEnd: prevWeekEnd, growth: growthXP,
			title: func(t string) string { return "Earn " + t + " XP this week" },
		},
		{
			metric: core.MetricRevenue, start: monthStart, end: monthEnd,
			prevStart: prevMonthStart, prevEnd: prevMonthEnd, growth: growthStandard,
			title: func(t string) string { return "Beat last month's revenue · " + t },
		},
		{
			metric: core.MetricSettlements, start: monthStart, end: monthEnd,
			fixed: newCountriesTarget,
			title: func(string) string {
				return fmt.Sprintf("Settle %d new countries this month", newCountriesTarget)
			},
		},
		{
			metric: core.MetricStars, start: weekStart, end: weekEnd,
			prevStart: prevWeekStart, prevEnd: prevWeekEnd, growth: growthStandard,
			title: func(t string) string { return "Beat last week's stars · " + t },
		},
	}

	maxActive := g.MaxActive
	if maxActive <= 0 {
		maxActive = DefaultMaxActive
	}
	// Only quests whose window is still open count towards the cap: a quest
	// whose week is over is history even if a crash stopped it being marked so.
	activeAuto, err := g.Store.CountActiveQuests(ctx, core.QuestAuto, today)
	if err != nil {
		return nil, err
	}

	var created []core.Quest
	for _, c := range candidates {
		if activeAuto >= maxActive {
			break
		}
		if !available[c.metric] {
			continue
		}

		target, ok, err := g.target(ctx, c)
		if err != nil {
			return created, err
		}
		if !ok {
			continue
		}

		q := core.Quest{
			ID:          core.NewID(),
			Kind:        core.QuestAuto,
			Metric:      c.metric,
			Target:      target,
			WindowStart: c.start,
			WindowEnd:   c.end,
			Title:       c.title(FormatValue(c.metric, target, g.DisplayCurrency)),
			Status:      core.QuestActive,
			CreatedAt:   now.UTC(),
			DedupeKey:   autoKey(c.metric, "", "", c.start, c.end),
		}
		madeNew, err := g.Store.InsertQuest(ctx, q)
		if err != nil {
			return created, err
		}
		if !madeNew {
			// Already on the board (or already completed this window): it
			// still counts towards the cap only while it is active, which
			// CountActiveQuests above already measured.
			continue
		}
		activeAuto++
		created = append(created, q)
		g.log().Debug("quest generated", "title", q.Title, "metric", q.Metric,
			"target", q.Target, "window", q.WindowStart+".."+q.WindowEnd)
	}
	return created, nil
}

// target works out what a candidate should ask for: a fixed number, or the
// previous window's figure plus a nudge, rounded to something a human would
// have chosen. It returns ok=false when history gives no basis for a target.
func (g *Generator) target(ctx context.Context, c candidate) (float64, bool, error) {
	if c.fixed > 0 {
		// A fixed target counts from *now*, not from the first of the month:
		// "settle two new countries this month" generated on the fourteenth
		// should ask for two more, not hand out an epic drop for a fortnight
		// that already happened.
		done, err := g.Store.QuestProgress(ctx, core.Quest{
			Metric:      c.metric,
			WindowStart: c.start,
			WindowEnd:   c.end,
		})
		if err != nil {
			return 0, false, err
		}
		return done + c.fixed, true, nil
	}

	prev, err := g.Store.QuestProgress(ctx, core.Quest{
		Metric:      c.metric,
		WindowStart: c.prevStart,
		WindowEnd:   c.prevEnd,
	})
	if err != nil {
		return 0, false, err
	}
	if prev <= 0 {
		// Nothing to beat. A quest to beat zero is not a quest.
		return 0, false, nil
	}

	target := roundNice(prev * c.growth)
	if floor := floors[c.metric]; target < floor {
		// The previous window was too small for "beat it by 5%" to mean
		// anything; ask for the floor instead.
		target = floor
	}
	if target <= prev {
		// Rounding must never make a "beat it" quest already met.
		target = math.Ceil(prev + 1)
	}
	return target, true, nil
}

// autoKey is the idempotence key of a generated quest: one per metric, app,
// source and window.
func autoKey(metric core.Metric, app, source, start, end string) string {
	return strings.Join([]string{"auto", string(metric), app, source, start, end}, ":")
}

// roundNice rounds a target to a number somebody would have picked: whole
// units below a hundred, tens below a thousand, fifties below ten thousand,
// hundreds above that.
func roundNice(v float64) float64 {
	switch {
	case v <= 0:
		return 0
	case v < 100:
		return math.Ceil(v)
	case v < 1000:
		return math.Ceil(v/10) * 10
	case v < 10000:
		return math.Ceil(v/50) * 50
	default:
		return math.Ceil(v/100) * 100
	}
}

// FormatValue renders a metric's value the way its quest title should read.
// It is core.FormatMetric under a shorter name, kept here because every caller
// in this package spells it this way.
func FormatValue(metric core.Metric, v float64, currency string) string {
	return core.FormatMetric(metric, v, currency)
}
