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
	"text/tabwriter"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

const reportUsage = `Usage:
  loot report [section] [flags]

Pull metrics and reports from a running Loot server without scraping the UI.
Pass --json for machine-readable output (suitable for scripts and AI agents).

Sections:
  overview   (default) Combined report: stats, revenue, scorecards, audience, ratings, funnel, alerts
  vault      Settled revenue, units, refunds, breakdowns, subscriptions and scorecards
  hearth     Audience per app & store, GitHub downloads, settlements, fleet and era
  trend      Daily time series for one Hearth metric (--metric required)
  reviews    App store ratings summary, per-app breakdown and written reviews
  funnel     Acquisition funnel: ad spend, clicks, conversions, installs and revenue
  recap      Monthly (--month YYYY-MM) or yearly (--season YYYY) summary recap
  codex      Lifetime totals, all-time records and achievement counts
  stats      Headline counters (XP, totals, countries, chests, quests, mysteries, bosses)
  drops      Recent drop feed (--limit, --before)
  quests     Active and recently completed goals
  mysteries  Open and resolved anomalies
  bosses     Active and recently ended crash-spike bosses
  sources    Configured source poll status and errors
  apps       Canonical product mappings and unmapped source apps

Flags:
`

func runReport(args []string) error {
	section := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		section = strings.ToLower(strings.TrimSpace(args[0]))
		args = args[1:]
	}

	fs := flag.NewFlagSet("report", flag.ExitOnError)
	cf := bindClientFlags(fs)
	var (
		app           = fs.String("app", "", "narrow results to one product")
		asJSON        = fs.Bool("json", false, "print the raw JSON response")
		rangeKey      = fs.String("range", "", "time range (vault: 7d|30d|90d|365d; trend: 7d|30d|90d|ytd|1y|all; reviews: 30d|90d|1y|all)")
		reviewsRange  = fs.String("reviews-range", "", "ratings window for overview report (30d|90d|1y|all)")
		days          = fs.Int("days", 14, "funnel lookback in days (1-90)")
		includeSeries = fs.Bool("include-series", false, "include daily time series in overview JSON")
		metric        = fs.String("metric", "", "trend metric: audience|population|settlements|revenue|fleet|downloads|country|xp")
		product       = fs.String("product", "", "trend product name (for audience or downloads)")
		source        = fs.String("source", "", "source filter (reviews or trend)")
		format        = fs.String("format", "", "package format filter (downloads trend)")
		country       = fs.String("country", "", "2-letter ISO country code (country trend)")
		stars         = fs.String("stars", "", "star filter for reviews (1..5 or low)")
		month         = fs.String("month", "", "recap month as YYYY-MM")
		season        = fs.String("season", "", "recap year as YYYY")
		limit         = fs.Int("limit", 0, "max items to fetch (drops or reviews)")
		before        = fs.String("before", "", "pagination cursor for drops (next_before)")
	)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), reportUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if section == "" && fs.NArg() > 0 {
		section = strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	}
	if section == "" {
		section = "overview"
	}

	client, err := cf.client()
	if err != nil {
		return err
	}

	path, q, err := reportRequest(section, reportParams{
		app:           *app,
		rangeKey:      *rangeKey,
		reviewsRange:  *reviewsRange,
		days:          *days,
		includeSeries: *includeSeries,
		metric:        *metric,
		product:       *product,
		source:        *source,
		format:        *format,
		country:       *country,
		stars:         *stars,
		month:         *month,
		season:        *season,
		limit:         *limit,
		before:        *before,
	})
	if err != nil {
		return err
	}

	raw, err := client.DoJSON(context.Background(), http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	if *asJSON {
		_, err := fmt.Println(string(raw))
		return err
	}
	return printReportSection(os.Stdout, section, raw)
}

type reportParams struct {
	app           string
	rangeKey      string
	reviewsRange  string
	days          int
	includeSeries bool
	metric        string
	product       string
	source        string
	format        string
	country       string
	stars         string
	month         string
	season        string
	limit         int
	before        string
}

func reportRequest(section string, p reportParams) (string, url.Values, error) {
	q := url.Values{}
	if p.app != "" {
		q.Set("app", p.app)
	}
	switch section {
	case "overview", "summary":
		if p.rangeKey != "" {
			q.Set("range", p.rangeKey)
		}
		if p.reviewsRange != "" {
			q.Set("reviews_range", p.reviewsRange)
		}
		if p.days > 0 {
			q.Set("funnel_days", strconv.Itoa(p.days))
		}
		if p.includeSeries {
			q.Set("include_series", "1")
		}
		return "/api/report", q, nil
	case "vault":
		if p.rangeKey != "" {
			q.Set("range", p.rangeKey)
		}
		return "/api/vault/summary", q, nil
	case "hearth":
		return "/api/hearth", q, nil
	case "trend":
		if strings.TrimSpace(p.metric) == "" {
			return "", nil, fmt.Errorf("--metric is required for 'loot report trend' (audience, population, settlements, revenue, fleet, downloads, country, or xp)")
		}
		q.Set("metric", p.metric)
		if p.rangeKey != "" {
			q.Set("range", p.rangeKey)
		}
		if p.product != "" {
			q.Set("product", p.product)
		}
		if p.source != "" {
			q.Set("source", p.source)
		}
		if p.format != "" {
			q.Set("format", p.format)
		}
		if p.country != "" {
			q.Set("country", p.country)
		}
		return "/api/hearth/trend", q, nil
	case "reviews", "ratings":
		if p.rangeKey != "" {
			q.Set("range", p.rangeKey)
		}
		if p.source != "" {
			q.Set("source", p.source)
		}
		if p.stars != "" {
			q.Set("stars", p.stars)
		}
		if p.limit > 0 {
			q.Set("limit", strconv.Itoa(p.limit))
		}
		return "/api/reviews", q, nil
	case "funnel":
		if p.days > 0 {
			q.Set("days", strconv.Itoa(p.days))
		}
		return "/api/funnel", q, nil
	case "recap":
		if p.month != "" {
			q.Set("month", p.month)
		}
		if p.season != "" {
			q.Set("season", p.season)
		}
		return "/api/recap", q, nil
	case "codex":
		return "/api/codex", q, nil
	case "stats":
		return "/api/stats", q, nil
	case "drops":
		if p.limit > 0 {
			q.Set("limit", strconv.Itoa(p.limit))
		}
		if p.before != "" {
			q.Set("before", p.before)
		}
		return "/api/drops", q, nil
	case "quests":
		return "/api/quests", q, nil
	case "mysteries":
		return "/api/mysteries", q, nil
	case "bosses":
		return "/api/bosses", q, nil
	case "sources":
		return "/api/sources", nil, nil
	case "apps":
		return "/api/apps", nil, nil
	default:
		return "", nil, fmt.Errorf("unknown report section %q\n\n%s", section, reportUsage)
	}
}

func printReportSection(w io.Writer, section string, raw json.RawMessage) error {
	switch section {
	case "overview", "summary":
		var rep server.Report
		if err := json.Unmarshal(raw, &rep); err != nil {
			return err
		}
		printOverviewReport(w, rep)
		return nil
	case "vault":
		var v store.VaultSummary
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		printVaultReport(w, v)
		return nil
	case "hearth":
		var h store.Hearth
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		printHearthReport(w, h)
		return nil
	case "trend":
		var tr store.HearthTrend
		if err := json.Unmarshal(raw, &tr); err != nil {
			return err
		}
		printTrendReport(w, tr)
		return nil
	case "reviews", "ratings":
		var rev store.ReviewsSummary
		if err := json.Unmarshal(raw, &rev); err != nil {
			return err
		}
		printReviewsReport(w, rev)
		return nil
	case "funnel":
		var rep core.FunnelReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			return err
		}
		printFunnel(w, rep)
		return nil
	case "recap":
		var resp struct {
			Recap codex.Recap `json:"recap"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		printRecapReport(w, resp.Recap)
		return nil
	case "codex":
		var board codex.Board
		if err := json.Unmarshal(raw, &board); err != nil {
			return err
		}
		printCodexReport(w, board)
		return nil
	case "stats":
		var st map[string]any
		if err := json.Unmarshal(raw, &st); err != nil {
			return err
		}
		fmt.Fprintf(w, "Stats · %s\n", strField(st, "app", "all apps"))
		fmt.Fprintf(w, "  XP: %v  Drops: %v  Events: %v  Countries: %v\n",
			st["total_xp"], st["total_drops"], st["total_events"], st["countries_count"])
		fmt.Fprintf(w, "  Chests waiting: %v  Active quests: %v  Open mysteries: %v  Alive bosses: %v\n",
			st["unrevealed_count"], st["active_quests"], st["open_mysteries"], st["bosses_alive"])
		return nil
	case "drops":
		var resp struct {
			Drops      []store.DropView `json:"drops"`
			NextBefore string           `json:"next_before"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		if len(resp.Drops) == 0 {
			fmt.Fprintln(w, "No drops.")
			return nil
		}
		for _, d := range resp.Drops {
			fmt.Fprintf(w, "%s  %-9s  %s", d.CreatedAt.Format("2006-01-02 15:04"), strings.ToUpper(string(d.Rarity)), d.Title)
			if d.Subtitle != "" {
				fmt.Fprintf(w, " · %s", d.Subtitle)
			}
			fmt.Fprintf(w, " [+%d xp %s]\n", d.XP, d.Source)
		}
		if resp.NextBefore != "" {
			fmt.Fprintf(w, "\nnext_before: %s\n", resp.NextBefore)
		}
		return nil
	case "quests":
		var board quests.Board
		if err := json.Unmarshal(raw, &board); err != nil {
			return err
		}
		fmt.Fprintf(w, "Quests · %d active, %d recent\n", len(board.Active), len(board.Recent))
		for _, q := range board.Active {
			fmt.Fprintf(w, "  [active] %s — %.1f / %.1f (%.0f%%) · ends %s\n",
				q.Title, q.Value, q.Target, q.Pct*100, q.WindowEnd)
		}
		for _, q := range board.Recent {
			fmt.Fprintf(w, "  [%s] %s — %.1f / %.1f\n", q.Status, q.Title, q.Value, q.Target)
		}
		return nil
	case "mysteries":
		var book struct {
			Open     []core.Mystery `json:"open"`
			Resolved []core.Mystery `json:"resolved"`
		}
		if err := json.Unmarshal(raw, &book); err != nil {
			return err
		}
		fmt.Fprintf(w, "Mysteries · %d open, %d resolved\n", len(book.Open), len(book.Resolved))
		for _, m := range book.Open {
			fmt.Fprintf(w, "  [open] %s (%s · %s) — %s\n", m.Day, m.Source, m.App, m.Title)
		}
		for _, m := range book.Resolved {
			fmt.Fprintf(w, "  [%s] %s (%s) — %s", m.Status, m.Day, m.Source, m.Title)
			if m.Note != "" {
				fmt.Fprintf(w, " · note: %s", m.Note)
			}
			fmt.Fprintln(w)
		}
		return nil
	case "bosses":
		var board bosses.Board
		if err := json.Unmarshal(raw, &board); err != nil {
			return err
		}
		fmt.Fprintf(w, "Bosses · %d alive, %d recent\n", len(board.Alive), len(board.Recent))
		for _, b := range board.Alive {
			fmt.Fprintf(w, "  [alive] %s (%s · %s) — HP %.0f/%.0f\n", b.Name, b.App, b.Source, b.HP, b.HPMax)
		}
		for _, b := range board.Recent {
			fmt.Fprintf(w, "  [%s] %s (%s · %s)\n", b.Status, b.Name, b.App, b.Source)
		}
		return nil
	case "sources":
		var resp struct {
			Sources []struct {
				Name         string `json:"name"`
				Mode         string `json:"mode"`
				PollInterval string `json:"poll_interval"`
				LastPollAt   string `json:"last_poll_at"`
				LastError    string `json:"last_error"`
				Events       int    `json:"events"`
			} `json:"sources"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SOURCE\tMODE\tEVENTS\tLAST POLL\tERROR")
		for _, s := range resp.Sources {
			mode := s.Mode
			if s.PollInterval != "" {
				mode += " (" + s.PollInterval + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, mode, thousands(s.Events), s.LastPollAt, s.LastError)
		}
		return tw.Flush()
	case "apps":
		var resp struct {
			Products []struct {
				Name       string              `json:"name"`
				Configured bool                `json:"configured"`
				Events     int                 `json:"events"`
				FirstSeen  string              `json:"first_seen"`
				Sources    map[string][]string `json:"sources"`
			} `json:"products"`
			Unmapped []store.ProductPair `json:"unmapped"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PRODUCT\tCONFIGURED\tEVENTS\tFIRST SEEN")
		for _, p := range resp.Products {
			fmt.Fprintf(tw, "%s\t%v\t%s\t%s\n", p.Name, p.Configured, thousands(p.Events), p.FirstSeen)
		}
		_ = tw.Flush()
		if len(resp.Unmapped) > 0 {
			fmt.Fprintf(w, "\nUnmapped (%d):\n", len(resp.Unmapped))
			for _, u := range resp.Unmapped {
				fmt.Fprintf(w, "  %s: %s (%s events)\n", u.Source, u.App, thousands(u.Events))
			}
		}
		return nil
	default:
		_, err := fmt.Fprintln(w, string(raw))
		return err
	}
}

func printOverviewReport(w io.Writer, rep server.Report) {
	scope := "all apps"
	if rep.App != "" {
		scope = rep.App
	}
	fmt.Fprintf(w, "Loot Report · %s · %s (%s to %s)\n", scope, rep.DisplayCurrency, rep.Vault.Range.From, rep.Vault.Range.To)
	fmt.Fprintf(w, "Era: %s (Level %d · %s XP) · %s countries · %s population\n\n",
		rep.Stats.Era.Name, rep.Stats.Level, thousands(rep.Stats.TotalXP),
		thousands(rep.Stats.CountriesCount), thousands(rep.Stats.Population))

	printVaultSummaryBlock(w, rep.Vault)

	if len(rep.Audience) > 0 {
		fmt.Fprintln(w, "\nAudience")
		for _, a := range rep.Audience {
			var parts []string
			for _, src := range a.Sources {
				parts = append(parts, fmt.Sprintf("%s=%s (%s)", src.Source, thousands(src.Count), src.Basis))
			}
			fmt.Fprintf(w, "  %-20s active=%s  [%s]\n", a.Product, thousands(a.Current), strings.Join(parts, ", "))
		}
	}

	if rep.Reviews.TotalRatings > 0 || rep.Reviews.TotalReviews > 0 {
		fmt.Fprintf(w, "\nRatings (%s)\n", rep.Reviews.Range)
		fmt.Fprintf(w, "  Average: %.2f★ · %s ratings · %s reviews (%s low)\n",
			rep.Reviews.AverageRating, thousands(rep.Reviews.TotalRatings),
			thousands(rep.Reviews.TotalReviews), thousands(rep.Reviews.LowReviews))
		for _, a := range rep.Reviews.ByApp {
			fmt.Fprintf(w, "  %-20s %.2f★ (%s ratings, %s reviews)\n",
				a.Product, a.AverageRating, thousands(a.RatingCount), thousands(a.ReviewCount))
		}
	}

	if rep.Funnel != nil && rep.Funnel.Enabled {
		fmt.Fprintf(w, "\nFunnel (%s to %s)\n", rep.Funnel.From, rep.Funnel.To)
		if rep.Funnel.Totals.Spend != nil {
			fmt.Fprintf(w, "  Spend: %s", money(*rep.Funnel.Totals.Spend, rep.Funnel.Currency))
			if rep.Funnel.Totals.Conversions != nil {
				fmt.Fprintf(w, " · Conversions: %s", thousands(*rep.Funnel.Totals.Conversions))
			}
			if rep.Funnel.Totals.Installs != nil {
				fmt.Fprintf(w, " · Installs: %s", thousands(*rep.Funnel.Totals.Installs))
			}
			fmt.Fprintln(w)
		}
	}

	if len(rep.Attention.BossesAlive) > 0 || len(rep.Attention.OpenMysteries) > 0 ||
		len(rep.Attention.ChestsWaiting) > 0 || len(rep.Attention.SourceErrors) > 0 {
		fmt.Fprintln(w, "\nAttention")
		if len(rep.Attention.ChestsWaiting) > 0 {
			fmt.Fprintf(w, "  Chests waiting: %d\n", len(rep.Attention.ChestsWaiting))
		}
		for _, b := range rep.Attention.BossesAlive {
			fmt.Fprintf(w, "  Boss alive: %s (%s · HP %.0f/%.0f)\n", b.Name, b.App, b.HP, b.HPMax)
		}
		for _, m := range rep.Attention.OpenMysteries {
			fmt.Fprintf(w, "  Open mystery: %s · %s\n", m.Day, m.Title)
		}
		for _, s := range rep.Attention.SourceErrors {
			fmt.Fprintf(w, "  Source error (%s): %s\n", s.Name, s.LastError)
		}
	}
}

func printVaultReport(w io.Writer, v store.VaultSummary) {
	fmt.Fprintf(w, "Vault · %s · %s to %s (%dd)\n\n", v.DisplayCurrency, v.Range.From, v.Range.To, v.Range.Days)
	printVaultSummaryBlock(w, v)
}

func printVaultSummaryBlock(w io.Writer, v store.VaultSummary) {
	fmt.Fprintf(w, "Revenue (%dd): %s (prev %s) · Units: %s (prev %s) · Refunds: %s\n",
		v.Range.Days,
		money(v.Totals.RevenueBase, v.DisplayCurrency),
		money(v.PrevTotals.RevenueBase, v.DisplayCurrency),
		thousands(v.Totals.Units), thousands(v.PrevTotals.Units),
		thousands(v.Totals.Refunds))
	if v.Subscriptions.Active != nil {
		asOf := ""
		if v.Subscriptions.AsOf != nil {
			asOf = " as of " + *v.Subscriptions.AsOf
		}
		fmt.Fprintf(w, "Active subscriptions: %s%s\n", thousands(*v.Subscriptions.Active), asOf)
	}
	if v.Realtime.RevenueCatPurchasesToday > 0 {
		fmt.Fprintf(w, "Realtime today (RevenueCat): %s purchases · %s\n",
			thousands(v.Realtime.RevenueCatPurchasesToday),
			money(v.Realtime.RevenueCatAmountBaseToday, v.DisplayCurrency))
	}
	if len(v.ByApp) > 0 {
		fmt.Fprintln(w, "By app:")
		for _, a := range v.ByApp {
			fmt.Fprintf(w, "  %-20s %14s  %6s units  (%.0f%%)\n",
				a.App, money(a.RevenueBase, v.DisplayCurrency), thousands(a.Units), a.Share*100)
		}
	}
	if len(v.BySource) > 0 {
		fmt.Fprintln(w, "By source:")
		for _, s := range v.BySource {
			fmt.Fprintf(w, "  %-20s %14s  %6s units  (%.0f%%)\n",
				s.Source, money(s.RevenueBase, v.DisplayCurrency), thousands(s.Units), s.Share*100)
		}
	}
}

func printHearthReport(w io.Writer, h store.Hearth) {
	fmt.Fprintf(w, "Hearth · Era %s (%s XP) · Capital %s · Population %s (Fleet %s)\n",
		h.Era.Name, thousands(h.TotalXP), h.Capital,
		thousands(h.Population), thousands(h.Unknown.Population))
	if len(h.Audience) > 0 {
		fmt.Fprintln(w, "\nAudience:")
		for _, a := range h.Audience {
			fmt.Fprintf(w, "  %s (active: %s, arrivals: %s)\n", a.Product, thousands(a.Current), thousands(a.Arrivals))
			for _, s := range a.Sources {
				extra := ""
				if s.Superseded {
					extra = " [superseded]"
				}
				fmt.Fprintf(w, "    %-16s %8s  %s%s\n", s.Source, thousands(s.Count), s.Basis, extra)
			}
			for _, d := range a.Downloads {
				fmt.Fprintf(w, "    download/%-7s %8s\n", d.Format, thousands(d.Count))
			}
		}
	}
	if len(h.Countries) > 0 {
		fmt.Fprintf(w, "\nTop settlements (%d total):\n", len(h.Countries))
		n := min(len(h.Countries), 10)
		for _, c := range h.Countries[:n] {
			fmt.Fprintf(w, "  %-4s %-11s pop=%-8s rev=%s\n",
				c.Country, c.Tier.Name, thousands(c.Population), money(c.RevenueBase, h.DisplayCurrency))
		}
	}
}

func printTrendReport(w io.Writer, tr store.HearthTrend) {
	fmt.Fprintf(w, "Trend · %s (%s, %s to %s, %s)\n", tr.Metric, tr.Range, tr.From, tr.To, tr.Kind)
	fmt.Fprintf(w, "  Current: %.2f  Start: %.2f  Change: %+.2f  Peak: %.2f",
		tr.Current, tr.Start, tr.Change, tr.Peak)
	if tr.PeakDay != "" {
		fmt.Fprintf(w, " (%s)", tr.PeakDay)
	}
	fmt.Fprintf(w, "  Points: %d\n", len(tr.Points))
}

func printReviewsReport(w io.Writer, rev store.ReviewsSummary) {
	fmt.Fprintf(w, "Reviews · %s (%s to %s)\n", rev.Range, rev.From, rev.To)
	fmt.Fprintf(w, "  Average: %.2f★ · %s ratings · %s reviews · %s low (1-2★)\n",
		rev.AverageRating, thousands(rev.TotalRatings), thousands(rev.TotalReviews), thousands(rev.LowReviews))
	for _, a := range rev.ByApp {
		fmt.Fprintf(w, "  %-20s %.2f★ (%s ratings, %s reviews)\n",
			a.Product, a.AverageRating, thousands(a.RatingCount), thousands(a.ReviewCount))
	}
	if len(rev.Reviews) > 0 {
		fmt.Fprintf(w, "\nLatest reviews (%d):\n", len(rev.Reviews))
		n := min(len(rev.Reviews), 10)
		for _, r := range rev.Reviews[:n] {
			fmt.Fprintf(w, "  %s %d★ [%s/%s] %s\n", r.Day, r.Rating, r.Product, r.Source, r.Title)
		}
	}
}

func printRecapReport(w io.Writer, r codex.Recap) {
	fmt.Fprintf(w, "Recap · %s (%s to %s)\n", r.Period.Label, r.Period.From, r.Period.To)
	if r.Empty {
		fmt.Fprintln(w, "  No activity in this period.")
		return
	}
	fmt.Fprintf(w, "  Revenue: %s  Units: %s  Refunds: %s  Installs: %s\n",
		money(r.RevenueBase, r.DisplayCurrency), thousands(r.Units), thousands(r.Refunds), thousands(r.Installs))
	fmt.Fprintf(w, "  Drops: %s  New countries: %d\n", thousands(r.Drops), len(r.NewCountries))
	if r.TopApp.Key != "" {
		fmt.Fprintf(w, "  Top app: %s (%s)\n", r.TopApp.Key, money(r.TopApp.RevenueBase, r.DisplayCurrency))
	}
	if r.TopCountry.Key != "" {
		fmt.Fprintf(w, "  Top country: %s (%s)\n", r.TopCountry.Key, money(r.TopCountry.RevenueBase, r.DisplayCurrency))
	}
}

func printCodexReport(w io.Writer, b codex.Board) {
	fmt.Fprintf(w, "Codex · %d / %d achievements unlocked\n", b.Unlocked, b.Total)
	fmt.Fprintf(w, "  Lifetime revenue: %s · Units: %s · Installs: %s · Countries: %s\n",
		money(b.Totals.RevenueBase, b.DisplayCurrency), thousands(b.Totals.Units),
		thousands(b.Totals.Installs), thousands(b.Totals.Countries))
	if b.Records.BestRevenueDay.Day != "" {
		fmt.Fprintf(w, "  Best revenue day: %s (%s)\n",
			b.Records.BestRevenueDay.Day, money(b.Records.BestRevenueDay.Value, b.DisplayCurrency))
	}
	if b.Records.BestInstallDay.Day != "" {
		fmt.Fprintf(w, "  Best installs day: %s (%s)\n",
			b.Records.BestInstallDay.Day, thousands(int(b.Records.BestInstallDay.Value)))
	}
}

func strField(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
