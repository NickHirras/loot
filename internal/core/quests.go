package core

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Quests are goals worth chasing; mysteries are questions worth asking. Both
// are deliberately gentle:
//
//   - a quest never fails. When its window ends unmet it quietly expires into
//     history as "ended · 62%" — no red, no broken streak, no scolding. The
//     only loud moment is the one where you finished something.
//   - a mystery is an invitation, not a chore. Nothing bad happens if you
//     never open one, and "dismiss" costs nothing.
//
// Completing a quest and solving a mystery both pay a real drop through the
// real pipeline, so the reward is the same variable, noisy, satisfying thing
// every other drop is.

// Metric is what a quest counts. Each one is answered by a SQL aggregate over
// stored events and drops inside the quest's window; see store.QuestProgress.
type Metric string

const (
	// MetricRevenue sums ledger revenue in the display currency, exactly as
	// the vault does: ledger rows only, sales_day summaries excluded.
	MetricRevenue Metric = "revenue"
	// MetricUnits counts paid units on ledger rows (the vault's predicate).
	MetricUnits Metric = "units"
	// MetricInstalls sums `install` events, preferring a source's overview
	// rows over its per-country rows so a source that reports both is not
	// counted twice.
	MetricInstalls Metric = "installs"
	// MetricSubscribers is the latest subscription_snapshot inside the window,
	// summed over apps. It is a level, not a flow.
	MetricSubscribers Metric = "subscribers"
	// MetricDrops counts visible drops (a drop still sealed in a chest has not
	// happened yet).
	MetricDrops Metric = "drops"
	// MetricSettlements counts first-ever countries founded in the window.
	MetricSettlements Metric = "settlements"
	// MetricStars counts GitHub stars.
	MetricStars Metric = "stars"
	// MetricXP sums the XP of visible drops.
	MetricXP Metric = "xp"
)

// Metrics lists every metric a quest may count, in the order the UI offers
// them.
var Metrics = []Metric{
	MetricRevenue, MetricUnits, MetricInstalls, MetricSubscribers,
	MetricDrops, MetricSettlements, MetricStars, MetricXP,
}

// Valid reports whether m is a known metric.
func (m Metric) Valid() bool {
	for _, k := range Metrics {
		if k == m {
			return true
		}
	}
	return false
}

// IsMoney reports whether values of this metric are amounts in the display
// currency rather than counts.
func (m Metric) IsMoney() bool { return m == MetricRevenue }

// Label is the human name of a metric, used in titles and in the UI.
func (m Metric) Label() string {
	switch m {
	case MetricRevenue:
		return "revenue"
	case MetricUnits:
		return "units"
	case MetricInstalls:
		return "installs"
	case MetricSubscribers:
		return "subscribers"
	case MetricDrops:
		return "drops"
	case MetricSettlements:
		return "new countries"
	case MetricStars:
		return "stars"
	case MetricXP:
		return "XP"
	}
	return string(m)
}

// FormatMetric renders a metric's value the way a title should read: money in
// the display currency, everything else as a plain count.
func FormatMetric(m Metric, v float64, currency string) string {
	if m.IsMoney() {
		return FormatMoney(v, currency)
	}
	return FormatCount(v)
}

// currencySymbols covers the currencies a title is most likely to be written
// in; anything else reads as "1,240 CHF", which is unambiguous if less pretty.
var currencySymbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "AUD": "A$",
	"CAD": "C$", "NZD": "NZ$", "BRL": "R$", "INR": "₹", "KRW": "₩",
}

// FormatMoney renders a whole-unit amount in a currency, e.g. "$1,240".
func FormatMoney(v float64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "" {
		code = "USD"
	}
	digits := HumanInt(int64(math.Round(v)))
	if sym, ok := currencySymbols[code]; ok {
		return sym + digits
	}
	return digits + " " + code
}

// FormatCount renders a count with thousands separators.
func FormatCount(v float64) string { return HumanInt(int64(math.Round(v))) }

// HumanInt renders an integer with thousands separators.
func HumanInt(n int64) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return sign + s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return sign + b.String()
}

// Quest statuses. A quest is only ever active, completed or expired — there is
// deliberately no "failed".
const (
	QuestActive    = "active"
	QuestCompleted = "completed"
	QuestExpired   = "expired"
)

// Quest kinds: quests Loot generated for you, and quests you set yourself.
const (
	QuestAuto   = "auto"
	QuestCustom = "custom"
)

// Quest is one goal over one window of days.
type Quest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Metric is what is counted, Target how much of it finishes the quest.
	Metric Metric  `json:"metric"`
	Target float64 `json:"target"`
	// App and Source optionally narrow the count. Empty means "everything".
	App    string `json:"app"`
	Source string `json:"source"`
	// WindowStart and WindowEnd are inclusive days (YYYY-MM-DD).
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	// Value is the cached progress and UpdatedAt when it was measured.
	Value     float64    `json:"value"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// XP is what the completion drop paid, filled in on completion.
	XP          int        `json:"xp"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// DedupeKey makes auto-generation idempotent: one quest per
	// (metric, app, source, window). Custom quests key on their own id.
	DedupeKey string `json:"-"`
	// Pct is Value/Target clamped to 0..1, computed for the API.
	Pct float64 `json:"pct"`
	// DaysLeft counts today as a day left; it is 0 once the window has ended.
	DaysLeft int `json:"days_left"`
}

// QuestPct clamps a raw value/target ratio into 0..1. A zero target counts as
// finished rather than as a division by zero.
func QuestPct(value, target float64) float64 {
	if target <= 0 {
		return 1
	}
	p := value / target
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// WindowDays is the inclusive length of the quest's window in days, or 0 when
// either end is unparseable.
func (q Quest) WindowDays() int {
	start, err := time.Parse(DayLayout, q.WindowStart)
	if err != nil {
		return 0
	}
	end, err := time.Parse(DayLayout, q.WindowEnd)
	if err != nil {
		return 0
	}
	return int(end.Sub(start).Hours()/24) + 1
}

// Scope is "month" for a window of four weeks or more, "week" otherwise. It is
// what decides whether a completion is rare or epic, and it travels in the
// completion event's payload so the rules file can match on it.
func (q Quest) Scope() string {
	if q.WindowDays() >= 28 {
		return "month"
	}
	return "week"
}

// Mystery kinds.
const (
	// MysterySpike and MysteryDip are a day that broke away from its own
	// 28-day baseline, up or down.
	MysterySpike = "spike"
	MysteryDip   = "dip"
	// MysteryRefundSpike is a day of unusually many refunds.
	MysteryRefundSpike = "refund_spike"
	// MysteryRecord is a day that beat everything before it.
	MysteryRecord = "record"
	// MysteryNewCountryCluster is several countries founded on one day.
	MysteryNewCountryCluster = "new_country_cluster"
	// MysterySilence is a source that reported every day and then stopped —
	// usually the least mysterious mystery of all: broken credentials.
	MysterySilence = "silence"
)

// Mystery statuses.
const (
	MysteryOpen      = "open"
	MysterySolved    = "solved"
	MysteryDismissed = "dismissed"
)

// MysteryPoint is one day of the sparkline a mystery carries with it.
type MysteryPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// MysteryDetail is the context a mystery keeps so the card can be rendered
// (and understood) long after the numbers moved on.
type MysteryDetail struct {
	// Series is the trailing window ending on the flagged day, oldest first.
	Series []MysteryPoint `json:"series"`
	// Baseline and Deviation are the robust centre (median) and spread (MAD)
	// the flag was measured against.
	Baseline  float64 `json:"baseline"`
	Deviation float64 `json:"deviation"`
	// Ratio is observed / expected, 0 when expected is 0.
	Ratio float64 `json:"ratio"`
	// Unit is "money" or "count", so the UI knows how to format the numbers.
	Unit string `json:"unit"`
	// Note is a one-line explanation of what tripped the detector.
	Why string `json:"why,omitempty"`
}

// Mystery is one flagged day: something the numbers did that the numbers
// before it do not explain.
type Mystery struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	App    string `json:"app"`
	Metric string `json:"metric"`
	// Day is the flagged day (YYYY-MM-DD).
	Day      string  `json:"day"`
	Observed float64 `json:"observed"`
	Expected float64 `json:"expected"`
	// Z is the modified z-score of the flagged day, signed.
	Z      float64         `json:"z"`
	Title  string          `json:"title"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Status string          `json:"status"`
	// Note is your own explanation, written when solving it. Over time these
	// become a lab notebook of what actually moves your numbers.
	Note       string     `json:"note"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// DedupeKey is (kind, source, app, metric, day): the detector may run as
	// often as it likes without ever flagging the same day twice.
	DedupeKey string `json:"-"`
}
