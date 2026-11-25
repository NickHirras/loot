package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

const funnelUsage = `Usage:
  loot funnel [flags]  Print the acquisition funnel: yesterday, the last 7 days, and the notes

Flags:
`

// runFunnel asks a running server rather than the database, like loot chest:
// the server already knows the display currency, the exchange rates and the
// funnel config, and a morning summary should say exactly what the page says.
func runFunnel(args []string) error {
	fs := flag.NewFlagSet("funnel", flag.ExitOnError)
	rawURL := fs.String("url", "http://localhost:8080", "Loot server base URL")
	days := fs.Int("days", 14, "how many days to fetch, counting today (1-90)")
	app := fs.String("app", "", "narrow to one product")
	asJSON := fs.Bool("json", false, "print the server's JSON instead of the summary")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), funnelUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	base, err := baseURL(*rawURL)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("days", strconv.Itoa(*days))
	if *app != "" {
		q.Set("app", *app)
	}
	endpoint := base + "/api/funnel?" + q.Encode()

	var raw json.RawMessage
	if err := httpJSON(context.Background(), http.MethodGet, endpoint, nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		_, err := fmt.Println(string(raw))
		return err
	}
	var rep core.FunnelReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return fmt.Errorf("decode funnel: %w", err)
	}
	printFunnel(os.Stdout, rep)
	return nil
}

// printFunnel writes the plain-text morning summary.
func printFunnel(w io.Writer, rep core.FunnelReport) {
	scope := "all apps"
	if rep.Product != "" {
		scope = rep.Product
	}
	if !rep.Enabled {
		fmt.Fprintf(w, "No ad or link data for %s yet. See docs/funnel.md to connect a source.\n", scope)
		return
	}
	label := rep.ConversionLabel
	if label == "" {
		label = "conversion"
	}

	// rep.To is today (UTC) on the server, which is still in progress; the
	// summary is about days that are over.
	day, ok := newestCompleteAdDay(rep)
	if !ok {
		fmt.Fprintf(w, "Funnel · %s · no complete day with ad data between %s and %s\n", scope, rep.From, rep.To)
	} else {
		fmt.Fprintf(w, "Funnel · %s · %s\n\n", scope, rep.Currency)
		heading := day.Day
		if day.Day == dayBefore(rep.To) {
			heading = "Yesterday, " + day.Day
		}
		fmt.Fprintln(w, heading)
		printFunnelDay(w, day, rep, label)

		// A window shorter than a week says how many days it actually holds.
		week, from, n := trailingWeek(rep, day.Day)
		fmt.Fprintf(w, "\nLast %s, %s to %s\n", plural(n, "day"), from, day.Day)
		printFunnelDay(w, week, rep, label)

		var lag []string
		for _, stage := range []string{"ads", "links", "site_events", "installs", "sales"} {
			if asOf, ok := rep.AsOf[stage]; ok && asOf < day.Day {
				lag = append(lag, fmt.Sprintf("%s up to %s", strings.ReplaceAll(stage, "_", " "), asOf))
			}
		}
		if len(lag) > 0 {
			fmt.Fprintf(w, "\nReported so far: %s.\n", strings.Join(lag, "; "))
		}
	}

	if len(rep.Notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range rep.Notes {
			fmt.Fprintf(w, "· %s\n", noteSentence(n, rep.Currency, label))
		}
	}
}

// newestCompleteAdDay is yesterday when the ad platform has reported it, or
// else the newest finished day it has.
func newestCompleteAdDay(rep core.FunnelReport) (core.FunnelDay, bool) {
	for i := len(rep.Days) - 1; i >= 0; i-- {
		d := rep.Days[i]
		if d.Day < rep.To && d.Spend != nil {
			return d, true
		}
	}
	return core.FunnelDay{}, false
}

// trailingWeek sums the up to seven days ending on last, and returns where it
// began and how many days it holds.
func trailingWeek(rep core.FunnelReport, last string) (core.FunnelDay, string, int) {
	end := -1
	for i, d := range rep.Days {
		if d.Day == last {
			end = i
		}
	}
	start := max(end-6, 0)
	var sum core.FunnelDay
	for _, d := range rep.Days[start : end+1] {
		sum.Spend = addF(sum.Spend, d.Spend)
		sum.Impressions = addI(sum.Impressions, d.Impressions)
		sum.LinkClicks = addI(sum.LinkClicks, d.LinkClicks)
		sum.LandingPageViews = addI(sum.LandingPageViews, d.LandingPageViews)
		sum.Conversions = addI(sum.Conversions, d.Conversions)
		sum.LinkVisits = addI(sum.LinkVisits, d.LinkVisits)
		sum.SiteEvents = addI(sum.SiteEvents, d.SiteEvents)
		sum.Installs = addI(sum.Installs, d.Installs)
		sum.Purchases = addI(sum.Purchases, d.Purchases)
		sum.Revenue = addF(sum.Revenue, d.Revenue)
	}
	return sum, rep.Days[start].Day, end - start + 1
}

// printFunnelDay prints one column of the funnel, top to bottom. Cost per step
// is shown for the stages the ads themselves drove; store stages count every
// customer, ad or not, so a cost per install would claim more than is known.
func printFunnelDay(w io.Writer, d core.FunnelDay, rep core.FunnelReport, label string) {
	spend := 0.0
	if d.Spend != nil {
		spend = *d.Spend
	}
	row := func(name string, n *int, costPer int) {
		if n == nil {
			fmt.Fprintf(w, "  %-22s %10s\n", name, "not in yet")
			return
		}
		line := fmt.Sprintf("  %-22s %10s", name, thousands(*n))
		if costPer > 0 && *n > 0 && spend > 0 {
			unit := "each"
			if costPer > 1 {
				unit = "per " + thousands(costPer)
			}
			line += fmt.Sprintf("   %s %s", money(spend*float64(costPer)/float64(*n), rep.Currency), unit)
		}
		fmt.Fprintln(w, line)
	}
	if d.Spend == nil {
		fmt.Fprintf(w, "  %-22s %10s\n", "Spend", "not in yet")
	} else {
		fmt.Fprintf(w, "  %-22s %10s\n", "Spend", money(*d.Spend, rep.Currency))
	}
	row("Impressions", d.Impressions, 1000)
	row("Link clicks", d.LinkClicks, 1)
	row("Landing page views", d.LandingPageViews, 1)
	row(capitalize(label)+"s", d.Conversions, 1)
	if rep.AsOf["links"] != "" {
		row("Link visits", d.LinkVisits, 1)
	}
	if rep.SiteEvent != "" {
		row(rep.SiteEvent+" (all visitors)", d.SiteEvents, 0)
	}
	row("Installs (all)", d.Installs, 0)
	row("Purchases (all)", d.Purchases, 0)
	if d.Revenue == nil {
		fmt.Fprintf(w, "  %-22s %10s\n", "Revenue (all)", "not in yet")
	} else {
		fmt.Fprintf(w, "  %-22s %10s\n", "Revenue (all)", money(*d.Revenue, rep.Currency))
	}
}

// noteSentence says a note in plain English. The page has its own, translated
// wording; the CLI, like loot chest, speaks English.
func noteSentence(n core.FunnelNote, currency, label string) string {
	num := func(key string) float64 {
		switch v := n.Params[key].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
		return 0
	}
	str := func(key string) string { s, _ := n.Params[key].(string); return s }
	switch n.Code {
	case core.FunnelNoteUntracked:
		return fmt.Sprintf("None of the %s with spend reports a %s, so %ss cannot be credited to any ad.",
			plural(int(num("ads")), "ad"), label, label)
	case core.FunnelNoteSpendNoConversions:
		return fmt.Sprintf("%s has spent %s without a %s.", n.Ad, money(num("spend"), currency), label)
	case core.FunnelNoteConcentrated:
		return fmt.Sprintf("%s took %.0f%% of the spend.", n.Ad, num("pct"))
	case core.FunnelNoteCostRising:
		return fmt.Sprintf("A landing page view cost %s on %s, against %s on the days before.",
			money(num("cost"), currency), str("day"), money(num("previous"), currency))
	case core.FunnelNoteStoreLag:
		return fmt.Sprintf("The stores have reported up to %s, %s behind the ads, so the newest installs are not in yet.",
			str("installs_as_of"), plural(int(num("days")), "day"))
	case core.FunnelNoteBudgetCheck:
		return fmt.Sprintf("Spend has reached %s, with %.2f %ss per 100 landing page views.",
			money(num("spend"), currency), num("rate"), label)
	}
	return n.Code
}

func addI(sum, v *int) *int {
	if v == nil {
		return sum
	}
	n := *v
	if sum != nil {
		n += *sum
	}
	return &n
}

func addF(sum, v *float64) *float64 {
	if v == nil {
		return sum
	}
	n := *v
	if sum != nil {
		n += *sum
	}
	return &n
}

func money(v float64, currency string) string { return fmt.Sprintf("%.2f %s", v, currency) }

// thousands renders 12345 as "12,345".
func thousands(n int) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// dayBefore returns the day before a YYYY-MM-DD day, or "" if it is not one.
func dayBefore(day string) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return ""
	}
	return core.DayOf(t.AddDate(0, 0, -1))
}
