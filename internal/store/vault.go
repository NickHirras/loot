package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The vault is the money view, and money has exactly one rule here:
//
//	Only *ledger rows* are summed into revenue.
//
// ledgerRows is that rule in SQL. It excludes two things on purpose:
//
//   - non-ledger events (RevenueCat), whose amounts are pre-tax, pre-store-cut
//     estimates. They are shown separately as "sighted" realtime numbers and
//     never added to revenue.
//   - `sales_day` summary events, which are a rollup of the very rows they sit
//     beside. Counting both would double every ledger day.
//
// Signed math: a refund is a ledger row whose kind is "refund" or whose
// quantity is negative, and ledger sources must store its amount negative.
// Revenue therefore nets out by summing amount_base over every ledger row,
// while units (paid sales, excluding free downloads) and refunds are two positive
// counts so a day with 10 sales and 2 refunds reads as "10 / 2", not "8".
const ledgerRows = `e.is_ledger = 1 AND e.kind <> 'sales_day'`

const isRefund = `(e.kind = 'refund' OR e.quantity < 0)`

// Free downloads are ledger rows (they carry a country and feed settlements)
// but they are not sales, so units counts only paid, non-refund quantity.
const isPaidUnit = `NOT ` + isRefund + ` AND e.kind <> 'download'`

// VaultRange is the window a summary covers, inclusive of both days.
type VaultRange struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

// VaultTotals is one window's headline figures.
type VaultTotals struct {
	RevenueBase float64 `json:"revenue_base"`
	Units       int     `json:"units"`
	Refunds     int     `json:"refunds"`
	Drops       int     `json:"drops"`
	Events      int     `json:"events"`
	Countries   int     `json:"countries"`
}

// VaultPoint is one day of the series. BySource maps source name to that
// source's revenue in the display currency.
type VaultPoint struct {
	Day         string             `json:"day"`
	RevenueBase float64            `json:"revenue_base"`
	Units       int                `json:"units"`
	BySource    map[string]float64 `json:"by_source"`
}

// Slice is the shared body of every breakdown row. Share is the row's fraction
// of the window's revenue, 0 when the window earned nothing.
type Slice struct {
	RevenueBase float64 `json:"revenue_base"`
	Units       int     `json:"units"`
	Share       float64 `json:"share"`
}

// SourceSlice is one row of by_source.
type SourceSlice struct {
	Source string `json:"source"`
	Slice
}

// AppSlice is one row of by_app.
type AppSlice struct {
	App string `json:"app"`
	Slice
}

// CountrySlice is one row of by_country. The tail beyond the top 25 is folded
// into a single row whose Country is "other".
type CountrySlice struct {
	Country string `json:"country"`
	Slice
}

// VaultSubscriptions reports the latest known active subscriber count, summed
// over the most recent `subscription_snapshot` event of each (source, app).
// Both fields are nil when no source has ever reported one.
type VaultSubscriptions struct {
	Active *int    `json:"active"`
	AsOf   *string `json:"as_of"`
}

// VaultRealtime carries the numbers that are *sighted but not banked*:
// RevenueCat's realtime view of today. These are estimates and are deliberately
// kept out of every revenue figure above.
type VaultRealtime struct {
	RevenueCatPurchasesToday  int     `json:"revenuecat_purchases_today"`
	RevenueCatAmountBaseToday float64 `json:"revenuecat_amount_base_today"`
}

// VaultSummary is the whole of GET /api/vault/summary.
type VaultSummary struct {
	DisplayCurrency string             `json:"display_currency"`
	Range           VaultRange         `json:"range"`
	Totals          VaultTotals        `json:"totals"`
	PrevTotals      VaultTotals        `json:"prev_totals"`
	Series          []VaultPoint       `json:"series"`
	BySource        []SourceSlice      `json:"by_source"`
	ByApp           []AppSlice         `json:"by_app"`
	ByCountry       []CountrySlice     `json:"by_country"`
	Subscriptions   VaultSubscriptions `json:"subscriptions"`
	Realtime        VaultRealtime      `json:"realtime"`
}

// maxCountryRows is how many countries are listed before the tail is folded
// into "other".
const maxCountryRows = 25

// VaultSummary aggregates the window [from, to] (inclusive, YYYY-MM-DD) and
// the equally long window immediately before it.
func (s *Store) VaultSummary(ctx context.Context, from, to, displayCurrency, today string) (VaultSummary, error) {
	days, err := dayCount(from, to)
	if err != nil {
		return VaultSummary{}, err
	}

	out := VaultSummary{
		DisplayCurrency: displayCurrency,
		Range:           VaultRange{From: from, To: to, Days: days},
		Series:          []VaultPoint{},
		BySource:        []SourceSlice{},
		ByApp:           []AppSlice{},
		ByCountry:       []CountrySlice{},
	}

	if out.Totals, err = s.vaultTotals(ctx, from, to); err != nil {
		return out, err
	}
	prevTo, prevFrom := shiftBack(from, days)
	if out.PrevTotals, err = s.vaultTotals(ctx, prevFrom, prevTo); err != nil {
		return out, err
	}
	if out.Series, err = s.vaultSeries(ctx, from, to, days); err != nil {
		return out, err
	}
	if out.BySource, out.ByApp, out.ByCountry, err = s.vaultBreakdowns(ctx, from, to, out.Totals.RevenueBase); err != nil {
		return out, err
	}
	if out.Subscriptions, err = s.vaultSubscriptions(ctx, today); err != nil {
		return out, err
	}
	if out.Realtime, err = s.vaultRealtime(ctx, today); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) vaultTotals(ctx context.Context, from, to string) (VaultTotals, error) {
	var t VaultTotals

	var (
		revenue sql.NullFloat64
		units   sql.NullInt64
		refunds sql.NullInt64
	)
	err := s.q.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(e.amount_base), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN `+isRefund+` THEN ABS(e.quantity) ELSE 0 END), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?`, from, to).Scan(&revenue, &units, &refunds)
	if err != nil {
		return t, fmt.Errorf("vault totals: %w", err)
	}
	t.RevenueBase = round2(revenue.Float64)
	t.Units = int(units.Int64)
	t.Refunds = int(refunds.Int64)

	err = s.q.QueryRowContext(ctx, `
        SELECT COUNT(*), COUNT(DISTINCT CASE WHEN country <> '' THEN country END)
        FROM events WHERE day BETWEEN ? AND ?`, from, to).Scan(&t.Events, &t.Countries)
	if err != nil {
		return t, fmt.Errorf("vault event totals: %w", err)
	}

	err = s.q.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.day BETWEEN ? AND ?`, from, to).Scan(&t.Drops)
	if err != nil {
		return t, fmt.Errorf("vault drop totals: %w", err)
	}
	return t, nil
}

func (s *Store) vaultSeries(ctx context.Context, from, to string, days int) ([]VaultPoint, error) {
	// Zero-fill first: a chart with holes in it lies about quiet days.
	series := make([]VaultPoint, 0, days)
	index := map[string]int{}
	start, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return nil, fmt.Errorf("vault series from: %w", err)
	}
	for i := 0; i < days; i++ {
		day := core.DayOf(start.AddDate(0, 0, i))
		index[day] = len(series)
		series = append(series, VaultPoint{Day: day, BySource: map[string]float64{}})
	}

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, e.source,
               COALESCE(SUM(e.amount_base), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?
        GROUP BY e.day, e.source`, from, to)
	if err != nil {
		return nil, fmt.Errorf("vault series: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			day, source string
			revenue     float64
			units       int
		)
		if err := rows.Scan(&day, &source, &revenue, &units); err != nil {
			return nil, fmt.Errorf("scan vault series: %w", err)
		}
		i, ok := index[day]
		if !ok {
			continue
		}
		series[i].RevenueBase = round2(series[i].RevenueBase + revenue)
		series[i].Units += units
		series[i].BySource[source] = round2(revenue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vault series: %w", err)
	}
	return series, nil
}

func (s *Store) vaultBreakdowns(ctx context.Context, from, to string, totalRevenue float64) ([]SourceSlice, []AppSlice, []CountrySlice, error) {
	group := func(column string) ([]struct {
		Key string
		Slice
	}, error) {
		rows, err := s.q.QueryContext(ctx, `
            SELECT `+column+`,
                   COALESCE(SUM(e.amount_base), 0),
                   COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0)
            FROM events e
            WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?
            GROUP BY `+column+`
            ORDER BY SUM(e.amount_base) DESC, SUM(e.quantity) DESC`, from, to)
		if err != nil {
			return nil, fmt.Errorf("vault breakdown %s: %w", column, err)
		}
		defer rows.Close()

		var out []struct {
			Key string
			Slice
		}
		for rows.Next() {
			var r struct {
				Key string
				Slice
			}
			if err := rows.Scan(&r.Key, &r.RevenueBase, &r.Units); err != nil {
				return nil, fmt.Errorf("scan vault breakdown %s: %w", column, err)
			}
			r.RevenueBase = round2(r.RevenueBase)
			r.Share = share(r.RevenueBase, totalRevenue)
			out = append(out, r)
		}
		return out, rows.Err()
	}

	sources, err := group("e.source")
	if err != nil {
		return nil, nil, nil, err
	}
	bySource := make([]SourceSlice, 0, len(sources))
	for _, r := range sources {
		bySource = append(bySource, SourceSlice{Source: r.Key, Slice: r.Slice})
	}

	apps, err := group("e.app")
	if err != nil {
		return nil, nil, nil, err
	}
	byApp := make([]AppSlice, 0, len(apps))
	for _, r := range apps {
		byApp = append(byApp, AppSlice{App: r.Key, Slice: r.Slice})
	}

	countries, err := group("e.country")
	if err != nil {
		return nil, nil, nil, err
	}
	byCountry := make([]CountrySlice, 0, len(countries))
	var other Slice
	for i, r := range countries {
		if i < maxCountryRows {
			byCountry = append(byCountry, CountrySlice{Country: r.Key, Slice: r.Slice})
			continue
		}
		other.RevenueBase = round2(other.RevenueBase + r.RevenueBase)
		other.Units += r.Units
	}
	if other.RevenueBase != 0 || other.Units != 0 {
		other.Share = share(other.RevenueBase, totalRevenue)
		byCountry = append(byCountry, CountrySlice{Country: "other", Slice: other})
	}

	return bySource, byApp, byCountry, nil
}

// subscriptionRecencyDays is how stale a subscription snapshot may be and
// still be counted. Snapshots are levels rather than flows, so the newest one
// is normally the answer — but "newest" with no floor means an app that
// stopped reporting last spring keeps contributing its final subscriber count
// to the headline forever, and the number quietly stops being true.
const subscriptionRecencyDays = 14

// vaultSubscriptions sums the newest subscription_snapshot per (source, app),
// ignoring any that has gone stale. Snapshots are absolute counts, not deltas,
// so the newest one per (source, app) wins and the apps are added together.
func (s *Store) vaultSubscriptions(ctx context.Context, today string) (VaultSubscriptions, error) {
	// An unparsable "today" (there is no such caller, but a nil answer here
	// would be a worse failure than a missing floor) simply drops the bound.
	floor := ""
	if t, err := time.Parse(core.DayLayout, today); err == nil {
		floor = core.DayOf(t.AddDate(0, 0, -(subscriptionRecencyDays - 1)))
	}

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.quantity, e.day
        FROM events e
        JOIN (
            SELECT source, app, MAX(day) AS day
            FROM events WHERE kind = 'subscription_snapshot' AND day >= ?
            GROUP BY source, app
        ) latest ON latest.source = e.source AND latest.app = e.app AND latest.day = e.day
        WHERE e.kind = 'subscription_snapshot'`, floor)
	if err != nil {
		return VaultSubscriptions{}, fmt.Errorf("vault subscriptions: %w", err)
	}
	defer rows.Close()

	var (
		total int
		asOf  string
		found bool
	)
	for rows.Next() {
		var (
			qty int
			day string
		)
		if err := rows.Scan(&qty, &day); err != nil {
			return VaultSubscriptions{}, fmt.Errorf("scan subscriptions: %w", err)
		}
		total += qty
		if day > asOf {
			asOf = day
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return VaultSubscriptions{}, fmt.Errorf("iterate subscriptions: %w", err)
	}
	if !found {
		return VaultSubscriptions{}, nil
	}
	return VaultSubscriptions{Active: &total, AsOf: &asOf}, nil
}

func (s *Store) vaultRealtime(ctx context.Context, today string) (VaultRealtime, error) {
	var rt VaultRealtime
	var amount sql.NullFloat64
	err := s.q.QueryRowContext(ctx, `
        SELECT COUNT(*), COALESCE(SUM(amount_base), 0)
        FROM events
        WHERE source = 'revenuecat' AND kind IN ('purchase', 'renewal') AND day = ?`, today).
		Scan(&rt.RevenueCatPurchasesToday, &amount)
	if err != nil {
		return rt, fmt.Errorf("vault realtime: %w", err)
	}
	rt.RevenueCatAmountBaseToday = round2(amount.Float64)
	return rt, nil
}

// dayCount returns the inclusive number of days between two YYYY-MM-DD days.
func dayCount(from, to string) (int, error) {
	f, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return 0, fmt.Errorf("bad from day %q: %w", from, err)
	}
	t, err := time.Parse(core.DayLayout, to)
	if err != nil {
		return 0, fmt.Errorf("bad to day %q: %w", to, err)
	}
	n := int(t.Sub(f).Hours()/24) + 1
	if n < 1 {
		return 0, fmt.Errorf("range %s..%s is empty", from, to)
	}
	return n, nil
}

// shiftBack returns the window of the same length ending the day before from.
func shiftBack(from string, days int) (prevTo, prevFrom string) {
	f, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return from, from
	}
	end := f.AddDate(0, 0, -1)
	return core.DayOf(end), core.DayOf(end.AddDate(0, 0, -(days - 1)))
}

func share(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return roundN(part/whole, 4)
}

func round2(v float64) float64 { return roundN(v, 2) }

func roundN(v float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(v*pow) / pow
}
