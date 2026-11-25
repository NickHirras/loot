// Package funnel decides what on the acquisition funnel is worth pointing at.
//
// Notes are observations, not advice: each says what the numbers show and
// nothing about what the reader should feel about it (see CONTRIBUTING.md,
// "Honest data, no shame mechanics"). They are also few on purpose. A page
// that always has five notes on it teaches its reader to skip the notes, so
// each code appears at most once, except spend_no_conversions, which names up
// to three ads.
//
// Notes are a pure function of a finished report, so the same report always
// says the same things and every rule is testable without a database.
package funnel

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// MinViewsForCost is how many landing page views a day needs before its cost
// per view means anything. Below it, one stray click moves the cost by half.
const MinViewsForCost = 10

// CostRisingFactor is how far above the previous days' cost per landing page
// view the latest complete day must be for cost_rising.
const CostRisingFactor = 1.5

// maxSpendNoConversions caps the per-ad spend_no_conversions notes.
const maxSpendNoConversions = 3

// Notes returns the report's notes in the order the page shows them. today is
// the current UTC day (YYYY-MM-DD); the funnel never judges a day that is not
// over yet.
func Notes(r core.FunnelReport, th config.FunnelNotes, today string) []core.FunnelNote {
	// A zero threshold is "not set" (a Config built in code rather than
	// loaded), never "always": config.Load applies the same defaults.
	if th.SpendWithoutConversion <= 0 {
		th.SpendWithoutConversion = config.DefaultFunnelSpendWithoutConversion
	}
	if th.ConcentrationPct <= 0 {
		th.ConcentrationPct = config.DefaultFunnelConcentrationPct
	}
	if th.MinConversionsPer100Views <= 0 {
		th.MinConversionsPer100Views = config.DefaultFunnelMinConversionsPer100Views
	}

	out := []core.FunnelNote{}
	totalSpend := deref(r.Totals.Spend)

	anyTracked := false
	adsWithSpend := 0
	for _, a := range r.Ads {
		anyTracked = anyTracked || a.Tracked
		if a.Spend > 0 {
			adsWithSpend++
		}
	}

	// Untracked: there is spend, and the platform reports the conversion for
	// no ad, so a zero in the conversions column says nothing about the ads.
	if totalSpend > 0 && !anyTracked {
		out = append(out, core.FunnelNote{
			Code:   core.FunnelNoteUntracked,
			Params: map[string]any{"ads": adsWithSpend},
		})
	}

	// Spend without conversions, per tracked ad, biggest spend first.
	var quiet []core.FunnelAd
	for _, a := range r.Ads {
		if a.Tracked && a.Conversions == 0 && a.Spend > 0 && a.Spend >= th.SpendWithoutConversion {
			quiet = append(quiet, a)
		}
	}
	sort.SliceStable(quiet, func(i, j int) bool { return quiet[i].Spend > quiet[j].Spend })
	for i, a := range quiet {
		if i == maxSpendNoConversions {
			break
		}
		out = append(out, core.FunnelNote{
			Code: core.FunnelNoteSpendNoConversions, AdID: a.AdID, Ad: a.Ad,
			Params: map[string]any{"spend": round(a.Spend, 2)},
		})
	}

	// Budget checkpoint: only when the operator asked for one, only with
	// tracking present (otherwise the rate is the platform's blind spot, which
	// untracked already says), and only with views to divide by.
	if th.CheckpointSpend > 0 && totalSpend >= th.CheckpointSpend && anyTracked {
		views := deref(r.Totals.LandingPageViews)
		if views > 0 {
			rate := float64(deref(r.Totals.Conversions)) * 100 / float64(views)
			if rate < th.MinConversionsPer100Views {
				out = append(out, core.FunnelNote{
					Code:   core.FunnelNoteBudgetCheck,
					Params: map[string]any{"spend": round(totalSpend, 2), "rate": round(rate, 2)},
				})
			}
		}
	}

	if n, ok := costRising(r.Days, today); ok {
		out = append(out, n)
	}

	// Concentrated: of three or more ads that ran, one took most of the spend.
	var running []core.FunnelAd
	for _, a := range r.Ads {
		if a.Spend > 0 || strings.EqualFold(a.Status, "ACTIVE") {
			running = append(running, a)
		}
	}
	if len(running) >= 3 && totalSpend > 0 {
		top := running[0]
		for _, a := range running[1:] {
			if a.Spend > top.Spend {
				top = a
			}
		}
		pct := top.Spend / totalSpend * 100
		if pct >= th.ConcentrationPct {
			out = append(out, core.FunnelNote{
				Code: core.FunnelNoteConcentrated, AdID: top.AdID, Ad: top.Ad,
				Params: map[string]any{"pct": math.Round(pct)},
			})
		}
	}

	// Store lag: the ads are reported further than the stores, so the newest
	// days' installs are blank rather than low.
	if ads, installs := r.AsOf["ads"], r.AsOf["installs"]; ads != "" && installs != "" && ads > installs {
		if days := daysBetween(installs, ads); days > 0 {
			out = append(out, core.FunnelNote{
				Code:   core.FunnelNoteStoreLag,
				Params: map[string]any{"installs_as_of": installs, "days": days},
			})
		}
	}
	return out
}

// costRising compares the latest complete day that had enough landing page
// views with up to three such days before it. The earlier days are pooled
// (their spend over their views) so one cheap day does not decide the
// baseline on its own.
func costRising(days []core.FunnelDay, today string) (core.FunnelNote, bool) {
	var usable []core.FunnelDay
	for _, d := range days {
		if d.Day >= today || d.Spend == nil || d.LandingPageViews == nil || *d.LandingPageViews < MinViewsForCost {
			continue
		}
		usable = append(usable, d)
	}
	if len(usable) < 2 {
		return core.FunnelNote{}, false
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].Day < usable[j].Day })
	last := usable[len(usable)-1]
	prev := usable[:len(usable)-1]
	if len(prev) > 3 {
		prev = prev[len(prev)-3:]
	}
	var spend float64
	var views int
	for _, d := range prev {
		spend += *d.Spend
		views += *d.LandingPageViews
	}
	if spend <= 0 || views == 0 {
		return core.FunnelNote{}, false
	}
	previous := spend / float64(views)
	cost := *last.Spend / float64(*last.LandingPageViews)
	if cost < previous*CostRisingFactor {
		return core.FunnelNote{}, false
	}
	return core.FunnelNote{
		Code:   core.FunnelNoteCostRising,
		Params: map[string]any{"cost": round(cost, 2), "previous": round(previous, 2), "day": last.Day},
	}, true
}

func daysBetween(from, to string) int {
	f, err1 := time.Parse(core.DayLayout, from)
	t, err2 := time.Parse(core.DayLayout, to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(t.Sub(f).Hours() / 24)
}

func deref[T int | float64](p *T) T {
	if p == nil {
		return 0
	}
	return *p
}

func round(v float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(v*pow) / pow
}
