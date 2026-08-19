package codex

import (
	"math"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Small shared helpers. Days are YYYY-MM-DD strings everywhere in Loot, so
// arithmetic on them goes through time.Parse rather than through string
// surgery — a month boundary is not something to be clever about.

// nextDay returns the day after a YYYY-MM-DD day, or "" if it cannot be parsed.
func nextDay(day string) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return ""
	}
	return core.DayOf(t.AddDate(0, 0, 1))
}

// parseDay parses a YYYY-MM-DD day in UTC.
func parseDay(day string) (time.Time, bool) {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// dayLabel renders a day the way a highlight reads it: "Aug 14".
func dayLabel(day string) string {
	t, ok := parseDay(day)
	if !ok {
		return day
	}
	return t.Format("Jan 2")
}

// roundMoney rounds to cents, matching what the store returns.
func roundMoney(v float64) float64 { return math.Round(v*100) / 100 }

// sortedKeys returns a map's keys in a stable order, so an API answer built
// from a map never shuffles between requests.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
