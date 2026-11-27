package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// TrendKind distinguishes a level reading (how many devices right now, which
// can go up or down) from a cumulative series (how many ever arrived, earned,
// or were downloaded).
type TrendKind string

const (
	TrendLevel      TrendKind = "level"
	TrendCumulative TrendKind = "cumulative"
)

// TrendPoint is one business day of a Hearth trend series.
type TrendPoint struct {
	Day string `json:"day"`
	// Value is the level or running cumulative total on this day. Nil when a
	// level source had not yet published a reading (before its first snapshot
	// or during its reporting lag at the right edge of a fixed window).
	Value *float64 `json:"value"`
	// Daily is what changed on this day: daily additions/net flow for a
	// cumulative series, or the day-over-day delta for a level series.
	Daily float64 `json:"daily"`
	// BySeries optionally splits Value into named parts — stores for a
	// multi-store product audience, or package formats for release downloads.
	BySeries map[string]float64 `json:"by_series,omitempty"`
}

// HearthTrendQuery names which Hearth metric to plot and over which range.
type HearthTrendQuery struct {
	// Metric is one of: audience, population, settlements, revenue, fleet,
	// downloads, country, xp.
	Metric  string
	Range   string // 7d, 30d, 90d, ytd, 1y, all
	Product string // required for audience, downloads
	Source  string // optional for audience; required for fleet
	Format  string // optional for downloads
	Country string // required for country (ISO2)
}

// HearthTrend is the whole of GET /api/hearth/trend.
type HearthTrend struct {
	Metric          string             `json:"metric"`
	Range           string             `json:"range"`
	From            string             `json:"from"`
	To              string             `json:"to"`
	Days            int                `json:"days"`
	Kind            TrendKind          `json:"kind"`
	Unit            string             `json:"unit"` // "count", "money", "xp"
	Basis           core.AudienceBasis `json:"basis,omitempty"`
	DisplayCurrency string             `json:"display_currency,omitempty"`
	// Current is the latest value in the series (matching the Hearth card).
	// Start is the value at the start of the window (the day before From when
	// history predates the window, or the first reading inside it).
	// Change is Current - Start.
	Current     float64      `json:"current"`
	Start       float64      `json:"start"`
	Change      float64      `json:"change"`
	Peak        float64      `json:"peak"`
	PeakDay     string       `json:"peak_day,omitempty"`
	FirstDay    string       `json:"first_day,omitempty"`
	LastDay     string       `json:"last_day,omitempty"`
	SeriesNames []string     `json:"series_names,omitempty"`
	Points      []TrendPoint `json:"points"`
}

// ValidTrendRange reports whether r is an accepted Hearth trend range key.
func ValidTrendRange(r string) bool {
	switch r {
	case "7d", "30d", "90d", "ytd", "1y", "all":
		return true
	}
	return false
}

// HearthTrend builds a daily time series for one Hearth metric so the UI can
// drill into how a number changed over a week, month, quarter, YTD, year or
// all time. Every metric uses the exact same counting rules as Hearth() so the
// right edge of the chart always agrees with the card that opened it.
func (s *Store) HearthTrend(ctx context.Context, q HearthTrendQuery, displayCurrency, today string) (HearthTrend, error) {
	q.Metric = strings.ToLower(strings.TrimSpace(q.Metric))
	q.Range = strings.ToLower(strings.TrimSpace(q.Range))
	if q.Range == "" {
		q.Range = "30d"
	}
	if !ValidTrendRange(q.Range) {
		return HearthTrend{}, fmt.Errorf("unknown trend range %q", q.Range)
	}
	q.Product = strings.TrimSpace(q.Product)
	q.Source = strings.ToLower(strings.TrimSpace(q.Source))
	q.Format = strings.ToLower(strings.TrimSpace(q.Format))
	q.Country = strings.ToUpper(strings.TrimSpace(q.Country))

	if _, err := time.Parse(core.DayLayout, today); err != nil {
		return HearthTrend{}, fmt.Errorf("bad today %q: %w", today, err)
	}

	switch q.Metric {
	case "audience":
		if q.Product == "" {
			return HearthTrend{}, fmt.Errorf("product is required for audience trend")
		}
		return s.trendAudience(ctx, q, today)
	case "population":
		return s.trendPopulation(ctx, q, today)
	case "settlements":
		return s.trendSettlements(ctx, q, today)
	case "revenue":
		return s.trendRevenue(ctx, q, displayCurrency, today)
	case "fleet":
		if q.Source == "" {
			return HearthTrend{}, fmt.Errorf("source is required for fleet trend")
		}
		return s.trendFleet(ctx, q, today)
	case "downloads":
		if q.Product == "" {
			return HearthTrend{}, fmt.Errorf("product is required for downloads trend")
		}
		return s.trendDownloads(ctx, q, today)
	case "country":
		if q.Country == "" {
			return HearthTrend{}, fmt.Errorf("country is required for country trend")
		}
		return s.trendCountry(ctx, q, today)
	case "xp":
		return s.trendXP(ctx, q, today)
	default:
		return HearthTrend{}, fmt.Errorf("unknown trend metric %q", q.Metric)
	}
}

// --------------------------------------------------------- audience replay

// audienceDayTotal is one (source, app)'s authoritative total on one day.
type audienceDayTotal struct {
	day string
	val int
}

// audienceDayPlaced is one (source, app)'s per-country snapshot on one day.
// A newer country snapshot replaces the whole set of countries for that app,
// matching audienceLatest in audience.go.
type audienceDayPlaced struct {
	day       string
	sum       int
	byCountry map[string]int
}

// audienceAppHistory holds every stored audience snapshot for one (source, app)
// in chronological order so a single forward sweep can evaluate what Hearth()
// would have shown on any historical day.
type audienceAppHistory struct {
	source        string
	app           string
	product       string
	coversAndroid bool
	totals        []audienceDayTotal
	placed        []audienceDayPlaced
}

type audienceCursor struct {
	h  *audienceAppHistory
	ti int
	pi int
}

func newAudienceCursors(list []*audienceAppHistory) []*audienceCursor {
	out := make([]*audienceCursor, len(list))
	for i, h := range list {
		out[i] = &audienceCursor{h: h, ti: -1, pi: -1}
	}
	return out
}

func (c *audienceCursor) advance(day string) {
	for c.ti+1 < len(c.h.totals) && c.h.totals[c.ti+1].day <= day {
		c.ti++
	}
	for c.pi+1 < len(c.h.placed) && c.h.placed[c.pi+1].day <= day {
		c.pi++
	}
}

func (c *audienceCursor) hasData() bool { return c.ti >= 0 || c.pi >= 0 }

func (c *audienceCursor) count() int {
	if c.ti >= 0 {
		return c.h.totals[c.ti].val
	}
	if c.pi >= 0 {
		return c.h.placed[c.pi].sum
	}
	return 0
}

func (c *audienceCursor) placedSum() int {
	if c.pi >= 0 {
		return c.h.placed[c.pi].sum
	}
	return 0
}

func (c *audienceCursor) unplaced() int {
	if c.ti < 0 {
		return 0
	}
	tot := c.h.totals[c.ti].val
	pl := c.placedSum()
	if tot <= pl {
		return 0
	}
	return tot - pl
}

func (c *audienceCursor) country(cc string) int {
	if c.pi < 0 {
		return 0
	}
	return c.h.placed[c.pi].byCountry[cc]
}

// audienceHistory reads every stored `audience` snapshot matching the optional
// product and source filters (plus the store's strict scope) and folds them
// into per-(source, app) histories alongside the earliest and latest days seen.
func (s *Store) audienceHistory(ctx context.Context, product, source string) (apps []*audienceAppHistory, firstDay, lastDay string, err error) {
	strict, args := s.scopeStrict("e")
	var cond strings.Builder
	cond.WriteString("WHERE e.kind = 'audience'")
	cond.WriteString(strict)
	if product != "" {
		cond.WriteString(" AND e.product = ?")
		args = append(args, product)
	}
	if source != "" {
		cond.WriteString(" AND e.source = ?")
		args = append(args, source)
	}

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app, e.product, e.country, e.quantity, e.day,
               COALESCE(json_extract(e.payload, '$.covers_android'), -1)
        FROM events e
        `+cond.String()+`
        ORDER BY e.source, e.app, e.day, e.country`, args...)
	if err != nil {
		return nil, "", "", fmt.Errorf("audience history: %w", err)
	}
	defer rows.Close()

	index := map[string]*audienceAppHistory{}
	for rows.Next() {
		var (
			src, app, prod, cc, day string
			qty, rawCovers          int
		)
		if err := rows.Scan(&src, &app, &prod, &cc, &qty, &day, &rawCovers); err != nil {
			return nil, "", "", fmt.Errorf("scan audience history: %w", err)
		}
		if day == "" {
			continue
		}
		if firstDay == "" || day < firstDay {
			firstDay = day
		}
		if day > lastDay {
			lastDay = day
		}
		key := src + "\x1f" + app
		h, ok := index[key]
		if !ok {
			covers := rawCovers == 1
			if src == "revenuecat" {
				covers = true
			} else if src == "googleanalytics" {
				covers = rawCovers != 0
			}
			h = &audienceAppHistory{source: src, app: app, product: prod, coversAndroid: covers}
			index[key] = h
			apps = append(apps, h)
		}
		if cc == "" {
			if n := len(h.totals); n > 0 && h.totals[n-1].day == day {
				h.totals[n-1].val = qty
			} else {
				h.totals = append(h.totals, audienceDayTotal{day: day, val: qty})
			}
			continue
		}
		var entry *audienceDayPlaced
		if n := len(h.placed); n > 0 && h.placed[n-1].day == day {
			entry = &h.placed[n-1]
		} else {
			h.placed = append(h.placed, audienceDayPlaced{day: day, byCountry: map[string]int{}})
			entry = &h.placed[len(h.placed)-1]
		}
		if qty > 0 {
			entry.byCountry[cc] += qty
			entry.sum += qty
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", "", fmt.Errorf("iterate audience history: %w", err)
	}
	return apps, firstDay, lastDay, nil
}

// ------------------------------------------------------------ window math

func resolveTrendWindow(rangeKey, today, firstDay, lastDay string, pureLevel bool) (from, to string, days []string, err error) {
	t, err := time.Parse(core.DayLayout, today)
	if err != nil {
		return "", "", nil, fmt.Errorf("bad today %q: %w", today, err)
	}
	endDay := today
	if lastDay > endDay {
		endDay = lastDay
	}

	switch rangeKey {
	case "7d":
		from = core.DayOf(t.AddDate(0, 0, -6))
		to = endDay
	case "30d":
		from = core.DayOf(t.AddDate(0, 0, -29))
		to = endDay
	case "90d":
		from = core.DayOf(t.AddDate(0, 0, -89))
		to = endDay
	case "ytd":
		from = fmt.Sprintf("%04d-01-01", t.Year())
		to = endDay
	case "1y":
		from = core.DayOf(t.AddDate(0, 0, -364))
		to = endDay
	case "all":
		if firstDay == "" {
			from = core.DayOf(t.AddDate(0, 0, -29))
			to = today
		} else {
			from = firstDay
			if pureLevel && lastDay != "" && lastDay < endDay {
				to = lastDay
			} else {
				to = endDay
			}
		}
	default:
		return "", "", nil, fmt.Errorf("unknown trend range %q", rangeKey)
	}

	if from > to {
		from = to
	}
	n, err := dayCount(from, to)
	if err != nil {
		return "", "", nil, err
	}
	start, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return "", "", nil, err
	}
	days = make([]string, n)
	for i := 0; i < n; i++ {
		days[i] = core.DayOf(start.AddDate(0, 0, i))
	}
	return from, to, days, nil
}

func dayBefore(day string) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return ""
	}
	return core.DayOf(t.AddDate(0, 0, -1))
}

func ptrFloat(v float64) *float64 { return &v }

func minDay(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func maxDay(a, b string) string {
	if a > b {
		return a
	}
	return b
}

// buildCumulativeTrend assembles a cumulative HearthTrend from per-day
// additions (optionally split across named series).
func buildCumulativeTrend(
	metric, rangeKey, unit, displayCurrency string,
	basis core.AudienceBasis,
	today string,
	dailyTotal map[string]float64,
	dailyBySeries map[string]map[string]float64,
	roundMoney bool,
) (HearthTrend, error) {
	firstDay, lastDay := "", ""
	for d, v := range dailyTotal {
		if d == "" {
			continue
		}
		if v == 0 {
			continue
		}
		firstDay = minDay(firstDay, d)
		lastDay = maxDay(lastDay, d)
	}

	from, to, days, err := resolveTrendWindow(rangeKey, today, firstDay, lastDay, false)
	if err != nil {
		return HearthTrend{}, err
	}

	round := func(v float64) float64 {
		if roundMoney {
			return round2(v)
		}
		return math.Round(v)
	}

	// Accumulate everything strictly before the window into the starting level.
	// Keep raw sums while stepping across days and round only on output so
	// sub-cent FX amounts cannot drift over a long window.
	running := 0.0
	seriesRunning := map[string]float64{}
	seriesSeen := map[string]bool{}
	for d, v := range dailyTotal {
		if d < from {
			running += v
		}
	}
	for d, m := range dailyBySeries {
		for k, v := range m {
			seriesSeen[k] = true
			if d < from {
				seriesRunning[k] += v
			}
		}
	}
	startVal := round(running)

	points := make([]TrendPoint, len(days))
	peak := math.Inf(-1)
	peakDay := ""
	multi := len(seriesSeen) > 1

	for i, d := range days {
		rawDelta := dailyTotal[d]
		running += rawDelta
		val := round(running)
		pt := TrendPoint{
			Day:   d,
			Value: ptrFloat(val),
			Daily: round(rawDelta),
		}
		if multi {
			if m := dailyBySeries[d]; len(m) > 0 {
				for k, v := range m {
					seriesRunning[k] += v
				}
			}
			snap := make(map[string]float64, len(seriesSeen))
			for k := range seriesSeen {
				if v := round(seriesRunning[k]); v != 0 {
					snap[k] = v
				}
			}
			pt.BySeries = snap
		}
		points[i] = pt
		if val > peak {
			peak = val
			peakDay = d
		}
	}

	var seriesNames []string
	if multi {
		seriesNames = make([]string, 0, len(seriesSeen))
		for k := range seriesSeen {
			seriesNames = append(seriesNames, k)
		}
		sort.Slice(seriesNames, func(i, j int) bool {
			ai, aj := round(seriesRunning[seriesNames[i]]), round(seriesRunning[seriesNames[j]])
			if ai != aj {
				return ai > aj
			}
			return seriesNames[i] < seriesNames[j]
		})
	}

	if math.IsInf(peak, -1) {
		peak = 0
	}
	currentVal := round(running)
	return HearthTrend{
		Metric:          metric,
		Range:           rangeKey,
		From:            from,
		To:              to,
		Days:            len(days),
		Kind:            TrendCumulative,
		Unit:            unit,
		Basis:           basis,
		DisplayCurrency: displayCurrency,
		Current:         currentVal,
		Start:           startVal,
		Change:          round(currentVal - startVal),
		Peak:            peak,
		PeakDay:         peakDay,
		FirstDay:        firstDay,
		LastDay:         lastDay,
		SeriesNames:     seriesNames,
		Points:          points,
	}, nil
}

// ------------------------------------------------------------ metric queries

func (s *Store) trendAudience(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	apps, firstDay, lastDay, err := s.audienceHistory(ctx, q.Product, q.Source)
	if err != nil {
		return HearthTrend{}, err
	}
	if len(apps) > 0 {
		// When viewing a whole product (no source filter), drop superseded
		// googleplay series if a cross-platform active source covers Android,
		// so the trend's right edge agrees with ProductAudience.Current.
		if q.Source == "" {
			coversAndroid := false
			for _, a := range apps {
				if a.coversAndroid {
					coversAndroid = true
					break
				}
			}
			if coversAndroid {
				filtered := make([]*audienceAppHistory, 0, len(apps))
				firstDay, lastDay = "", ""
				for _, a := range apps {
					if a.source == "googleplay" {
						continue
					}
					filtered = append(filtered, a)
					for _, t := range a.totals {
						firstDay = minDay(firstDay, t.day)
						lastDay = maxDay(lastDay, t.day)
					}
					for _, p := range a.placed {
						firstDay = minDay(firstDay, p.day)
						lastDay = maxDay(lastDay, p.day)
					}
				}
				if len(filtered) > 0 {
					apps = filtered
				}
			}
		}
		return buildAudienceLevelTrend(q, today, apps, firstDay, lastDay)
	}
	return s.trendAudienceArrivals(ctx, q, today)
}

func buildAudienceLevelTrend(
	q HearthTrendQuery,
	today string,
	apps []*audienceAppHistory,
	firstDay, lastDay string,
) (HearthTrend, error) {
	from, to, days, err := resolveTrendWindow(q.Range, today, firstDay, lastDay, true)
	if err != nil {
		return HearthTrend{}, err
	}

	sourcesSet := map[string]bool{}
	for _, a := range apps {
		sourcesSet[a.source] = true
	}
	multi := q.Source == "" && len(sourcesSet) > 1

	var basis core.AudienceBasis
	if q.Source != "" {
		basis = core.AudienceBasisFor(q.Source)
	} else if len(sourcesSet) == 1 {
		for src := range sourcesSet {
			basis = core.AudienceBasisFor(src)
		}
	}

	cursors := newAudienceCursors(apps)
	preDay := dayBefore(from)
	hasPre := false
	preVal := 0.0
	if firstDay != "" && preDay >= firstDay {
		for _, c := range cursors {
			c.advance(preDay)
			if c.hasData() {
				preVal += float64(c.count())
				hasPre = true
			}
		}
	}

	points := make([]TrendPoint, len(days))
	finalBySource := map[string]float64{}
	var (
		hasAny        bool
		firstInWindow float64
		lastInWindow  float64
		prevVal       float64
		hasPrev       = hasPre
		peak          = math.Inf(-1)
		peakDay       string
	)
	if hasPre {
		prevVal = preVal
	}

	for i, d := range days {
		if firstDay == "" || d < firstDay || d > lastDay {
			points[i] = TrendPoint{Day: d}
			continue
		}
		total := 0.0
		var bySrc map[string]float64
		if multi {
			bySrc = map[string]float64{}
		}
		for _, c := range cursors {
			c.advance(d)
			if !c.hasData() {
				continue
			}
			v := float64(c.count())
			total += v
			if multi && v > 0 {
				bySrc[c.h.source] += v
			}
		}
		daily := 0.0
		if hasPrev {
			daily = total - prevVal
		}
		points[i] = TrendPoint{
			Day:      d,
			Value:    ptrFloat(total),
			Daily:    daily,
			BySeries: bySrc,
		}
		if !hasAny {
			firstInWindow = total
			hasAny = true
		}
		lastInWindow = total
		prevVal = total
		hasPrev = true
		if total > peak {
			peak = total
			peakDay = d
		}
		if multi {
			finalBySource = bySrc
		}
	}

	// If the store's latest reading predates the selected window (e.g. a
	// stale source viewed on 7d), still report the standing level honestly.
	current, startVal := lastInWindow, firstInWindow
	if !hasAny && hasPre {
		current, startVal = preVal, preVal
		peak = preVal
	} else if hasPre {
		startVal = preVal
	}
	if math.IsInf(peak, -1) {
		peak = 0
	}

	var seriesNames []string
	if multi {
		// If the window ended after lastDay and had no in-window points, grab
		// finalBySource from the latest snapshot so the legend order is stable.
		if len(finalBySource) == 0 {
			for _, c := range cursors {
				c.advance(lastDay)
				if c.hasData() {
					finalBySource[c.h.source] += float64(c.count())
				}
			}
		}
		seriesNames = make([]string, 0, len(sourcesSet))
		for src := range sourcesSet {
			seriesNames = append(seriesNames, src)
		}
		sort.Slice(seriesNames, func(i, j int) bool {
			ai, aj := finalBySource[seriesNames[i]], finalBySource[seriesNames[j]]
			if ai != aj {
				return ai > aj
			}
			return seriesNames[i] < seriesNames[j]
		})
	}

	return HearthTrend{
		Metric:      q.Metric,
		Range:       q.Range,
		From:        from,
		To:          to,
		Days:        len(days),
		Kind:        TrendLevel,
		Unit:        "count",
		Basis:       basis,
		Current:     current,
		Start:       startVal,
		Change:      current - startVal,
		Peak:        peak,
		PeakDay:     peakDay,
		FirstDay:    firstDay,
		LastDay:     lastDay,
		SeriesNames: seriesNames,
		Points:      points,
	}, nil
}

func (s *Store) trendAudienceArrivals(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")
	extra := " AND e.product = ?"
	extraArgs := []any{q.Product}
	if q.Source != "" {
		extra += " AND e.source = ?"
		extraArgs = append(extraArgs, q.Source)
	}
	args := append(append([]any{}, strictArgs...), extraArgs...)

	dailyTotal := map[string]float64{}
	dailyBySource := map[string]map[string]float64{}
	add := func(day, source string, n int) {
		if n <= 0 || day == "" {
			return
		}
		dailyTotal[day] += float64(n)
		if q.Source == "" {
			if dailyBySource[day] == nil {
				dailyBySource[day] = map[string]float64{}
			}
			dailyBySource[day][source] += float64(n)
		}
	}

	collect := func(query string, what string) error {
		rows, err := s.q.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("audience trend %s: %w", what, err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				day, source string
				n           int
			)
			if err := rows.Scan(&day, &source, &n); err != nil {
				return fmt.Errorf("scan audience trend %s: %w", what, err)
			}
			add(day, source, n)
		}
		return rows.Err()
	}

	if err := collect(`
        SELECT day, source, COALESCE(SUM(v), 0) FROM (
            SELECT e.day AS day, e.source AS source, `+installValue+` AS v
            FROM events e
            WHERE e.kind = 'install' AND NOT `+audienceSourced+strict+extra+`
            GROUP BY e.source, e.app, e.day)
        GROUP BY day, source`, "installs"); err != nil {
		return HearthTrend{}, err
	}
	if err := collect(`
        SELECT e.day, e.source, COALESCE(SUM(`+hearthPopulationNoInstalls+`), 0)
        FROM events e
        WHERE 1 = 1`+strict+extra+`
        GROUP BY e.day, e.source`, "arrivals"); err != nil {
		return HearthTrend{}, err
	}

	// RevenueCat webhooks mirror App Store / Google Play purchases. When the
	// product-level arrivals view already includes a real store, drop
	// revenuecat so the chart matches ProductAudience.Arrivals.
	if q.Source == "" {
		hasStore := false
		for _, bySrc := range dailyBySource {
			if bySrc["appstore"] > 0 || bySrc["googleplay"] > 0 {
				hasStore = true
				break
			}
		}
		if hasStore {
			for d, bySrc := range dailyBySource {
				if rc := bySrc["revenuecat"]; rc > 0 {
					dailyTotal[d] -= rc
					if dailyTotal[d] <= 0 {
						delete(dailyTotal, d)
					}
					delete(bySrc, "revenuecat")
				}
			}
		}
	}

	return buildCumulativeTrend(q.Metric, q.Range, "count", "", core.BasisArrivals, today, dailyTotal, dailyBySource, false)
}

func (s *Store) trendPopulation(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")

	arrivalsByDay := map[string]int{}
	firstArr, lastArr := "", ""
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, COALESCE(SUM(`+hearthPopulation+`), 0)
        FROM events e
        WHERE e.country <> ''`+strict+`
        GROUP BY e.day`, strictArgs...)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("population trend arrivals: %w", err)
	}
	for rows.Next() {
		var (
			day string
			n   int
		)
		if err := rows.Scan(&day, &n); err != nil {
			rows.Close()
			return HearthTrend{}, fmt.Errorf("scan population trend: %w", err)
		}
		if n > 0 && day != "" {
			arrivalsByDay[day] = n
			firstArr = minDay(firstArr, day)
			lastArr = maxDay(lastArr, day)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return HearthTrend{}, fmt.Errorf("iterate population trend: %w", err)
	}
	rows.Close()

	apps, firstAud, lastAud, err := s.audienceHistory(ctx, "", "")
	if err != nil {
		return HearthTrend{}, err
	}

	firstDay := minDay(firstArr, firstAud)
	lastDay := maxDay(lastArr, lastAud)
	hasAudience := len(apps) > 0
	pureLevel := hasAudience && len(arrivalsByDay) == 0

	if !hasAudience {
		daily := make(map[string]float64, len(arrivalsByDay))
		for d, n := range arrivalsByDay {
			daily[d] = float64(n)
		}
		return buildCumulativeTrend(q.Metric, q.Range, "count", "", "", today, daily, nil, false)
	}

	from, to, days, err := resolveTrendWindow(q.Range, today, firstDay, lastDay, pureLevel)
	if err != nil {
		return HearthTrend{}, err
	}

	cursors := newAudienceCursors(apps)
	cumArr := 0
	for d, n := range arrivalsByDay {
		if d < from {
			cumArr += n
		}
	}

	preDay := dayBefore(from)
	hasPre := firstDay != "" && preDay >= firstDay
	preVal := float64(cumArr)
	if hasPre {
		for _, c := range cursors {
			c.advance(preDay)
			preVal += float64(c.placedSum())
		}
	}

	points := make([]TrendPoint, len(days))
	var (
		hasAny        bool
		firstInWindow float64
		lastInWindow  float64
		prevVal       = preVal
		hasPrev       = hasPre
		peak          = math.Inf(-1)
		peakDay       string
	)

	for i, d := range days {
		if firstDay == "" || d < firstDay || (pureLevel && d > lastDay) {
			points[i] = TrendPoint{Day: d}
			continue
		}
		cumArr += arrivalsByDay[d]
		audPlaced := 0
		for _, c := range cursors {
			c.advance(d)
			audPlaced += c.placedSum()
		}
		val := float64(cumArr + audPlaced)
		daily := 0.0
		if hasPrev {
			daily = val - prevVal
		}
		points[i] = TrendPoint{Day: d, Value: ptrFloat(val), Daily: daily}
		if !hasAny {
			firstInWindow = val
			hasAny = true
		}
		lastInWindow = val
		prevVal = val
		hasPrev = true
		if val > peak {
			peak = val
			peakDay = d
		}
	}

	current, startVal := lastInWindow, firstInWindow
	if !hasAny && hasPre {
		current, startVal = preVal, preVal
		peak = preVal
	} else if hasPre {
		startVal = preVal
	}
	if math.IsInf(peak, -1) {
		peak = 0
	}

	return HearthTrend{
		Metric:   q.Metric,
		Range:    q.Range,
		From:     from,
		To:       to,
		Days:     len(days),
		Kind:     TrendLevel,
		Unit:     "count",
		Current:  current,
		Start:    startVal,
		Change:   current - startVal,
		Peak:     peak,
		PeakDay:  peakDay,
		FirstDay: firstDay,
		LastDay:  lastDay,
		Points:   points,
	}, nil
}

func (s *Store) trendSettlements(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")

	type countryDelta struct {
		country string
		pop     int
		rev     float64
	}
	byDay := map[string][]countryDelta{}
	firstArr, lastArr := "", ""

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, e.country,
               COALESCE(SUM(`+hearthPopulation+`), 0),
               COALESCE(SUM(CASE WHEN `+ledgerRows+` THEN e.amount_base ELSE 0 END), 0)
        FROM events e
        WHERE e.country <> ''`+strict+`
        GROUP BY e.day, e.country`, strictArgs...)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("settlements trend: %w", err)
	}
	for rows.Next() {
		var (
			day, cc string
			pop     int
			rev     float64
		)
		if err := rows.Scan(&day, &cc, &pop, &rev); err != nil {
			rows.Close()
			return HearthTrend{}, fmt.Errorf("scan settlements trend: %w", err)
		}
		if day == "" || (pop == 0 && rev == 0) {
			continue
		}
		byDay[day] = append(byDay[day], countryDelta{country: cc, pop: pop, rev: rev})
		firstArr = minDay(firstArr, day)
		lastArr = maxDay(lastArr, day)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return HearthTrend{}, fmt.Errorf("iterate settlements trend: %w", err)
	}
	rows.Close()

	apps, firstAud, lastAud, err := s.audienceHistory(ctx, "", "")
	if err != nil {
		return HearthTrend{}, err
	}

	firstDay := minDay(firstArr, firstAud)
	lastDay := maxDay(lastArr, lastAud)
	hasAudience := len(apps) > 0
	pureLevel := hasAudience && len(byDay) == 0

	from, to, days, err := resolveTrendWindow(q.Range, today, firstDay, lastDay, pureLevel)
	if err != nil {
		return HearthTrend{}, err
	}

	cursors := newAudienceCursors(apps)
	cumPop := map[string]int{}
	cumRev := map[string]float64{}
	for d, deltas := range byDay {
		if d < from {
			for _, cd := range deltas {
				cumPop[cd.country] += cd.pop
				cumRev[cd.country] += cd.rev
			}
		}
	}

	countActive := func() float64 {
		active := map[string]bool{}
		for cc, pop := range cumPop {
			if pop > 0 {
				active[cc] = true
			}
		}
		for cc, rev := range cumRev {
			if round2(rev) != 0 {
				active[cc] = true
			}
		}
		for _, c := range cursors {
			if c.pi < 0 {
				continue
			}
			for cc, n := range c.h.placed[c.pi].byCountry {
				if n > 0 {
					active[cc] = true
				}
			}
		}
		return float64(len(active))
	}

	preDay := dayBefore(from)
	hasPre := firstDay != "" && preDay >= firstDay
	for _, c := range cursors {
		c.advance(preDay)
	}
	preVal := countActive()

	points := make([]TrendPoint, len(days))
	var (
		hasAny        bool
		firstInWindow float64
		lastInWindow  float64
		prevVal       = preVal
		hasPrev       = hasPre || !hasAudience
		peak          = math.Inf(-1)
		peakDay       string
	)

	for i, d := range days {
		if pureLevel && (firstDay == "" || d < firstDay || d > lastDay) {
			points[i] = TrendPoint{Day: d}
			continue
		}
		for _, cd := range byDay[d] {
			cumPop[cd.country] += cd.pop
			cumRev[cd.country] += cd.rev
		}
		for _, c := range cursors {
			c.advance(d)
		}
		val := countActive()
		daily := 0.0
		if hasPrev {
			daily = val - prevVal
		}
		points[i] = TrendPoint{Day: d, Value: ptrFloat(val), Daily: daily}
		if !hasAny {
			firstInWindow = val
			hasAny = true
		}
		lastInWindow = val
		prevVal = val
		hasPrev = true
		if val > peak {
			peak = val
			peakDay = d
		}
	}

	kind := TrendCumulative
	if hasAudience {
		kind = TrendLevel
	}
	current, startVal := lastInWindow, firstInWindow
	if !hasAny && hasPre {
		current, startVal = preVal, preVal
		peak = preVal
	} else if hasPre || !hasAudience {
		startVal = preVal
	}
	if math.IsInf(peak, -1) {
		peak = 0
	}

	return HearthTrend{
		Metric:   q.Metric,
		Range:    q.Range,
		From:     from,
		To:       to,
		Days:     len(days),
		Kind:     kind,
		Unit:     "count",
		Current:  current,
		Start:    startVal,
		Change:   current - startVal,
		Peak:     peak,
		PeakDay:  peakDay,
		FirstDay: firstDay,
		LastDay:  lastDay,
		Points:   points,
	}, nil
}

func (s *Store) trendRevenue(ctx context.Context, q HearthTrendQuery, displayCurrency, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, COALESCE(SUM(e.amount_base), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.country <> ''`+strict+`
        GROUP BY e.day`, strictArgs...)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("revenue trend: %w", err)
	}
	defer rows.Close()

	daily := map[string]float64{}
	for rows.Next() {
		var (
			day string
			amt float64
		)
		if err := rows.Scan(&day, &amt); err != nil {
			return HearthTrend{}, fmt.Errorf("scan revenue trend: %w", err)
		}
		if day != "" && amt != 0 {
			daily[day] = amt
		}
	}
	if err := rows.Err(); err != nil {
		return HearthTrend{}, fmt.Errorf("iterate revenue trend: %w", err)
	}
	return buildCumulativeTrend(q.Metric, q.Range, "money", displayCurrency, "", today, daily, nil, true)
}

func (s *Store) trendFleet(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")
	args := append([]any{q.Source}, strictArgs...)

	arrivalsByDay := map[string]int{}
	firstArr, lastArr := "", ""
	add := func(day string, n int) {
		if n <= 0 || day == "" {
			return
		}
		arrivalsByDay[day] += n
		firstArr = minDay(firstArr, day)
		lastArr = maxDay(lastArr, day)
	}

	collect := func(query, what string) error {
		rows, err := s.q.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("fleet trend %s: %w", what, err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				day string
				n   int
			)
			if err := rows.Scan(&day, &n); err != nil {
				return fmt.Errorf("scan fleet trend %s: %w", what, err)
			}
			add(day, n)
		}
		return rows.Err()
	}

	if err := collect(`
        SELECT e.day, COALESCE(SUM(`+hearthPopulationNoInstalls+`), 0)
        FROM events e
        WHERE e.source = ? AND e.country = ''`+strict+`
        GROUP BY e.day`, "arrivals"); err != nil {
		return HearthTrend{}, err
	}
	if err := collect(`
        SELECT day, COALESCE(SUM(MAX(day_total - placed, 0)), 0) FROM (
            SELECT e.day AS day,
                   `+installValue+` AS day_total,
                   COALESCE(SUM(CASE WHEN e.country <> '' AND e.quantity > 0 THEN e.quantity ELSE 0 END), 0) AS placed
            FROM events e
            WHERE e.source = ? AND e.kind = 'install' AND NOT `+audienceSourced+strict+`
            GROUP BY e.app, e.day)
        GROUP BY day`, "installs"); err != nil {
		return HearthTrend{}, err
	}

	apps, firstAud, lastAud, err := s.audienceHistory(ctx, "", q.Source)
	if err != nil {
		return HearthTrend{}, err
	}

	if len(apps) == 0 {
		daily := make(map[string]float64, len(arrivalsByDay))
		for d, n := range arrivalsByDay {
			daily[d] = float64(n)
		}
		return buildCumulativeTrend(q.Metric, q.Range, "count", "", "", today, daily, nil, false)
	}

	firstDay := minDay(firstArr, firstAud)
	lastDay := maxDay(lastArr, lastAud)
	pureLevel := len(arrivalsByDay) == 0

	from, to, days, err := resolveTrendWindow(q.Range, today, firstDay, lastDay, pureLevel)
	if err != nil {
		return HearthTrend{}, err
	}

	cursors := newAudienceCursors(apps)
	cumArr := 0
	for d, n := range arrivalsByDay {
		if d < from {
			cumArr += n
		}
	}

	preDay := dayBefore(from)
	hasPre := firstDay != "" && preDay >= firstDay
	preVal := float64(cumArr)
	if hasPre {
		for _, c := range cursors {
			c.advance(preDay)
			preVal += float64(c.unplaced())
		}
	}

	points := make([]TrendPoint, len(days))
	var (
		hasAny        bool
		firstInWindow float64
		lastInWindow  float64
		prevVal       = preVal
		hasPrev       = hasPre
		peak          = math.Inf(-1)
		peakDay       string
	)
	for i, d := range days {
		if firstDay == "" || d < firstDay || (pureLevel && d > lastDay) {
			points[i] = TrendPoint{Day: d}
			continue
		}
		cumArr += arrivalsByDay[d]
		unplaced := 0
		for _, c := range cursors {
			c.advance(d)
			unplaced += c.unplaced()
		}
		val := float64(cumArr + unplaced)
		daily := 0.0
		if hasPrev {
			daily = val - prevVal
		}
		points[i] = TrendPoint{Day: d, Value: ptrFloat(val), Daily: daily}
		if !hasAny {
			firstInWindow = val
			hasAny = true
		}
		lastInWindow = val
		prevVal = val
		hasPrev = true
		if val > peak {
			peak = val
			peakDay = d
		}
	}

	current, startVal := lastInWindow, firstInWindow
	if !hasAny && hasPre {
		current, startVal = preVal, preVal
		peak = preVal
	} else if hasPre {
		startVal = preVal
	}
	if math.IsInf(peak, -1) {
		peak = 0
	}

	return HearthTrend{
		Metric:   q.Metric,
		Range:    q.Range,
		From:     from,
		To:       to,
		Days:     len(days),
		Kind:     TrendLevel,
		Unit:     "count",
		Current:  current,
		Start:    startVal,
		Change:   current - startVal,
		Peak:     peak,
		PeakDay:  peakDay,
		FirstDay: firstDay,
		LastDay:  lastDay,
		Points:   points,
	}, nil
}

func (s *Store) trendDownloads(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")
	args := append([]any{q.Product}, strictArgs...)

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, COALESCE(json_extract(e.payload, '$.format'), ''), COALESCE(SUM(e.quantity), 0)
        FROM events e
        WHERE e.kind = 'asset_download' AND e.product = ?`+strict+`
        GROUP BY e.day, 2`, args...)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("downloads trend: %w", err)
	}
	defer rows.Close()

	dailyTotal := map[string]float64{}
	dailyByFormat := map[string]map[string]float64{}
	for rows.Next() {
		var (
			day, format string
			n           int
		)
		if err := rows.Scan(&day, &format, &n); err != nil {
			return HearthTrend{}, fmt.Errorf("scan downloads trend: %w", err)
		}
		if n <= 0 || day == "" {
			continue
		}
		if format == "" {
			format = "other"
		}
		if q.Format != "" && format != q.Format {
			continue
		}
		dailyTotal[day] += float64(n)
		if q.Format == "" {
			if dailyByFormat[day] == nil {
				dailyByFormat[day] = map[string]float64{}
			}
			dailyByFormat[day][format] += float64(n)
		}
	}
	if err := rows.Err(); err != nil {
		return HearthTrend{}, fmt.Errorf("iterate downloads trend: %w", err)
	}

	return buildCumulativeTrend(q.Metric, q.Range, "count", "", "", today, dailyTotal, dailyByFormat, false)
}

func (s *Store) trendCountry(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	strict, strictArgs := s.scopeStrict("e")
	args := append([]any{q.Country}, strictArgs...)

	arrivalsByDay := map[string]int{}
	firstArr, lastArr := "", ""
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, COALESCE(SUM(`+hearthPopulation+`), 0)
        FROM events e
        WHERE e.country = ?`+strict+`
        GROUP BY e.day`, args...)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("country trend arrivals: %w", err)
	}
	for rows.Next() {
		var (
			day string
			n   int
		)
		if err := rows.Scan(&day, &n); err != nil {
			rows.Close()
			return HearthTrend{}, fmt.Errorf("scan country trend: %w", err)
		}
		if n > 0 && day != "" {
			arrivalsByDay[day] = n
			firstArr = minDay(firstArr, day)
			lastArr = maxDay(lastArr, day)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return HearthTrend{}, fmt.Errorf("iterate country trend: %w", err)
	}
	rows.Close()

	// Load all audience histories in scope rather than filtering SQL by country:
	// a country that vanishes from a newer snapshot drops to 0 on that day,
	// matching settleAudience in hearth.go.
	apps, _, allAudLast, err := s.audienceHistory(ctx, "", "")
	if err != nil {
		return HearthTrend{}, err
	}

	// Track whether this country ever appeared in any audience snapshot, and on
	// what day it first did.
	firstAud := ""
	hasAudienceForCountry := false
	for _, h := range apps {
		for _, p := range h.placed {
			if p.byCountry[q.Country] > 0 {
				hasAudienceForCountry = true
				firstAud = minDay(firstAud, p.day)
			}
		}
	}

	if !hasAudienceForCountry {
		daily := make(map[string]float64, len(arrivalsByDay))
		for d, n := range arrivalsByDay {
			daily[d] = float64(n)
		}
		return buildCumulativeTrend(q.Metric, q.Range, "count", "", "", today, daily, nil, false)
	}

	firstDay := minDay(firstArr, firstAud)
	lastDay := maxDay(lastArr, allAudLast)
	pureLevel := len(arrivalsByDay) == 0

	from, to, days, err := resolveTrendWindow(q.Range, today, firstDay, lastDay, pureLevel)
	if err != nil {
		return HearthTrend{}, err
	}

	cursors := newAudienceCursors(apps)
	cumArr := 0
	for d, n := range arrivalsByDay {
		if d < from {
			cumArr += n
		}
	}

	preDay := dayBefore(from)
	hasPre := firstDay != "" && preDay >= firstDay
	preVal := float64(cumArr)
	if hasPre {
		for _, c := range cursors {
			c.advance(preDay)
			preVal += float64(c.country(q.Country))
		}
	}

	points := make([]TrendPoint, len(days))
	var (
		hasAny        bool
		firstInWindow float64
		lastInWindow  float64
		prevVal       = preVal
		hasPrev       = hasPre
		peak          = math.Inf(-1)
		peakDay       string
	)
	for i, d := range days {
		if firstDay == "" || d < firstDay || (pureLevel && d > lastDay) {
			points[i] = TrendPoint{Day: d}
			continue
		}
		cumArr += arrivalsByDay[d]
		aud := 0
		for _, c := range cursors {
			c.advance(d)
			aud += c.country(q.Country)
		}
		val := float64(cumArr + aud)
		daily := 0.0
		if hasPrev {
			daily = val - prevVal
		}
		points[i] = TrendPoint{Day: d, Value: ptrFloat(val), Daily: daily}
		if !hasAny {
			firstInWindow = val
			hasAny = true
		}
		lastInWindow = val
		prevVal = val
		hasPrev = true
		if val > peak {
			peak = val
			peakDay = d
		}
	}

	current, startVal := lastInWindow, firstInWindow
	if !hasAny && hasPre {
		current, startVal = preVal, preVal
		peak = preVal
	} else if hasPre {
		startVal = preVal
	}
	if math.IsInf(peak, -1) {
		peak = 0
	}

	return HearthTrend{
		Metric:   q.Metric,
		Range:    q.Range,
		From:     from,
		To:       to,
		Days:     len(days),
		Kind:     TrendLevel,
		Unit:     "count",
		Current:  current,
		Start:    startVal,
		Change:   current - startVal,
		Peak:     peak,
		PeakDay:  peakDay,
		FirstDay: firstDay,
		LastDay:  lastDay,
		Points:   points,
	}, nil
}

func (s *Store) trendXP(ctx context.Context, q HearthTrendQuery, today string) (HearthTrend, error) {
	// XP is deliberately unscoped, matching Hearth() and Stats().
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, COALESCE(SUM(d.xp), 0)
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+`
        GROUP BY e.day`)
	if err != nil {
		return HearthTrend{}, fmt.Errorf("xp trend: %w", err)
	}
	defer rows.Close()

	daily := map[string]float64{}
	for rows.Next() {
		var (
			day string
			xp  int
		)
		if err := rows.Scan(&day, &xp); err != nil {
			return HearthTrend{}, fmt.Errorf("scan xp trend: %w", err)
		}
		if day != "" && xp != 0 {
			daily[day] = float64(xp)
		}
	}
	if err := rows.Err(); err != nil {
		return HearthTrend{}, fmt.Errorf("iterate xp trend: %w", err)
	}
	return buildCumulativeTrend(q.Metric, q.Range, "xp", "", "", today, daily, nil, false)
}
