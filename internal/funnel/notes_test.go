package funnel_test

import (
	"testing"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/funnel"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

var defaults = config.FunnelNotes{
	SpendWithoutConversion:    20,
	ConcentrationPct:          80,
	MinConversionsPer100Views: 2,
}

func codes(notes []core.FunnelNote) map[string]int {
	out := map[string]int{}
	for _, n := range notes {
		out[n.Code]++
	}
	return out
}

func find(notes []core.FunnelNote, code string) (core.FunnelNote, bool) {
	for _, n := range notes {
		if n.Code == code {
			return n, true
		}
	}
	return core.FunnelNote{}, false
}

func report(ads ...core.FunnelAd) core.FunnelReport {
	var spend float64
	var views, convs int
	for _, a := range ads {
		spend += a.Spend
		views += a.LandingPageViews
		convs += a.Conversions
	}
	return core.FunnelReport{
		Ads:    ads,
		Totals: core.FunnelDay{Spend: f(spend), LandingPageViews: i(views), Conversions: i(convs)},
		AsOf:   map[string]string{},
	}
}

func TestNothingToSayAboutAQuietReport(t *testing.T) {
	if got := funnel.Notes(core.FunnelReport{AsOf: map[string]string{}}, defaults, "2026-03-10"); len(got) != 0 {
		t.Errorf("notes = %+v", got)
	}
	r := report(core.FunnelAd{AdID: "a", Spend: 5, Tracked: true, Conversions: 1, LandingPageViews: 10})
	if got := funnel.Notes(r, defaults, "2026-03-10"); len(got) != 0 {
		t.Errorf("a healthy small report has nothing to say: %+v", got)
	}
}

func TestUntracked(t *testing.T) {
	r := report(
		core.FunnelAd{AdID: "a", Spend: 30},
		core.FunnelAd{AdID: "b", Spend: 5},
		core.FunnelAd{AdID: "c", Spend: 0, Status: "PAUSED"},
	)
	notes := funnel.Notes(r, defaults, "2026-03-10")
	n, ok := find(notes, core.FunnelNoteUntracked)
	if !ok || n.Params["ads"] != 2 {
		t.Fatalf("untracked = %+v (%v)", n, ok)
	}
	// Untracked ads never get spend_no_conversions: their zero means nothing.
	if codes(notes)[core.FunnelNoteSpendNoConversions] != 0 {
		t.Errorf("untracked ads must not be called unconverting: %+v", notes)
	}
}

func TestSpendNoConversionsCapsAtThreeBiggestFirst(t *testing.T) {
	r := report(
		core.FunnelAd{AdID: "a", Spend: 25, Tracked: true},
		core.FunnelAd{AdID: "b", Spend: 60, Tracked: true},
		core.FunnelAd{AdID: "c", Spend: 40, Tracked: true},
		core.FunnelAd{AdID: "d", Spend: 30, Tracked: true},
		core.FunnelAd{AdID: "e", Spend: 19.99, Tracked: true},
		core.FunnelAd{AdID: "f", Spend: 90, Tracked: true, Conversions: 1},
	)
	var got []string
	for _, n := range funnel.Notes(r, defaults, "2026-03-10") {
		if n.Code == core.FunnelNoteSpendNoConversions {
			got = append(got, n.AdID)
		}
	}
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "d" {
		t.Errorf("spend_no_conversions ads = %v, want [b c d]", got)
	}
}

func TestConcentrated(t *testing.T) {
	r := report(
		core.FunnelAd{AdID: "big", Ad: "Big", Spend: 85, Tracked: true, Conversions: 1},
		core.FunnelAd{AdID: "b", Spend: 10, Tracked: true, Conversions: 1},
		core.FunnelAd{AdID: "c", Spend: 5, Tracked: true, Conversions: 1},
	)
	n, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteConcentrated)
	if !ok || n.AdID != "big" || n.Params["pct"] != 85.0 {
		t.Fatalf("concentrated = %+v (%v)", n, ok)
	}

	// Two ads are a pair, not a portfolio, and are never "concentrated".
	two := report(r.Ads[0], r.Ads[1])
	if _, ok := find(funnel.Notes(two, defaults, "2026-03-10"), core.FunnelNoteConcentrated); ok {
		t.Error("two ads should not be called concentrated")
	}
	// An active ad with no spend yet still counts as one that ran.
	active := report(r.Ads[0], r.Ads[1], core.FunnelAd{AdID: "new", Status: "ACTIVE"})
	if _, ok := find(funnel.Notes(active, defaults, "2026-03-10"), core.FunnelNoteConcentrated); !ok {
		t.Error("an active ad with no spend should count towards the three")
	}
}

func TestCostRising(t *testing.T) {
	day := func(d string, spend float64, views int) core.FunnelDay {
		return core.FunnelDay{Day: d, Spend: f(spend), LandingPageViews: i(views)}
	}
	r := core.FunnelReport{AsOf: map[string]string{}, Days: []core.FunnelDay{
		day("2026-03-04", 100, 10), // older than the three before the latest
		day("2026-03-05", 10, 20),
		day("2026-03-06", 10, 5), // too few views to count
		day("2026-03-07", 10, 20),
		day("2026-03-08", 10, 20),
		day("2026-03-09", 15, 20), // 0.75 per view against 0.50
		day("2026-03-10", 90, 20), // today: not over yet
	}}
	r.Totals = core.FunnelDay{Spend: f(0)}
	n, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteCostRising)
	if !ok || n.Params["cost"] != 0.75 || n.Params["previous"] != 0.5 || n.Params["day"] != "2026-03-09" {
		t.Fatalf("cost_rising = %+v (%v)", n, ok)
	}

	r.Days[5] = day("2026-03-09", 14, 20) // 0.70: under 1.5x
	if _, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteCostRising); ok {
		t.Error("1.4x is not a rise worth a note")
	}
}

func TestStoreLag(t *testing.T) {
	r := core.FunnelReport{AsOf: map[string]string{"ads": "2026-03-09", "installs": "2026-03-07"}}
	n, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteStoreLag)
	if !ok || n.Params["days"] != 2 || n.Params["installs_as_of"] != "2026-03-07" {
		t.Fatalf("store_lag = %+v (%v)", n, ok)
	}
	r.AsOf["installs"] = "2026-03-09"
	if _, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteStoreLag); ok {
		t.Error("caught-up stores do not lag")
	}
}

func TestBudgetCheck(t *testing.T) {
	r := report(
		core.FunnelAd{AdID: "a", Spend: 60, LandingPageViews: 200, Conversions: 2, Tracked: true},
		core.FunnelAd{AdID: "b", Spend: 50, LandingPageViews: 100, Conversions: 1, Tracked: true},
	)
	if _, ok := find(funnel.Notes(r, defaults, "2026-03-10"), core.FunnelNoteBudgetCheck); ok {
		t.Error("budget_check is off unless a checkpoint is configured")
	}
	th := defaults
	th.CheckpointSpend = 100
	n, ok := find(funnel.Notes(r, th, "2026-03-10"), core.FunnelNoteBudgetCheck)
	if !ok || n.Params["spend"] != 110.0 || n.Params["rate"] != 1.0 {
		t.Fatalf("budget_check = %+v (%v)", n, ok)
	}
	th.CheckpointSpend = 200
	if _, ok := find(funnel.Notes(r, th, "2026-03-10"), core.FunnelNoteBudgetCheck); ok {
		t.Error("spend below the checkpoint is not checked")
	}
}
