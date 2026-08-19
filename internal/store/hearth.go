package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The Hearth aggregate answers one question: how big is the settlement your
// app has founded in each country?
//
// # Population
//
// "Population" is a count of *people who arrived*, which is not the same as
// money and not the same as events. One person is:
//
//   - one unit on a ledger row whose kind is sale, iap, subscription or
//     download (App Store / Google Play financial reports). Quantity is summed
//     and only positive quantities count, so a refund does not evict a
//     citizen — a settlement never shrinks.
//   - one `install` (Google Play per-country installs, Flathub) — again by
//     quantity.
//   - one RevenueCat `purchase` event, counted as 1 regardless of quantity.
//     RevenueCat is not a ledger source and its money is an estimate, but its
//     purchases are real people, and for many apps they are the only per
//     country signal there is.
//
// Deliberately excluded: `sales_day` and `installs_day` summaries (they are a
// rollup of the rows beside them), `refund`, `cancellation`, `expiration` and
// friends (bad news does not add citizens), `active_devices` and
// `subscription_snapshot` (absolute snapshots, not arrivals), and the
// synthetic `settlement` event Loot mints for a first-ever country.
//
// # Revenue
//
// Revenue per country follows the vault's cardinal rule exactly: ledger rows
// only, `sales_day` excluded, signed so refunds net out.
const hearthPopulation = `
CASE
    WHEN e.is_ledger = 1 AND e.kind IN ('sale', 'iap', 'subscription', 'download') AND e.quantity > 0 THEN e.quantity
    WHEN e.kind = 'install' AND e.quantity > 0 THEN e.quantity
    WHEN e.source = 'revenuecat' AND e.kind = 'purchase' THEN 1
    ELSE 0
END`

// maxHearthRecent is how many recent country-bearing drops the ticker gets.
const maxHearthRecent = 30

// HearthCountry is one settlement: a country, how many people live there, and
// what they have paid.
type HearthCountry struct {
	Country     string  `json:"country"`
	Population  int     `json:"population"`
	RevenueBase float64 `json:"revenue_base"`
	// FirstSeen and LastSeen are business days (YYYY-MM-DD): when the first
	// and most recent event from this country landed.
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	// Drops is how many visible drops this country has produced.
	Drops int `json:"drops"`
	// Tier is the settlement size, derived from Share.
	Tier core.Tier `json:"tier"`
	// Share is this country's population as a fraction of the largest
	// country's, which is what the tier ladder is measured in.
	Share float64 `json:"share"`
}

// HearthUnknown is the bucket for events that carry no country at all —
// Flathub reports installs per app but not per country, so its citizens live
// in "unknown lands" rather than being silently dropped.
type HearthUnknown struct {
	Population  int     `json:"population"`
	RevenueBase float64 `json:"revenue_base"`
}

// HearthDrop is one row of the Hearth's arrivals ticker.
type HearthDrop struct {
	ID        string      `json:"id"`
	Rarity    core.Rarity `json:"rarity"`
	Country   string      `json:"country"`
	Title     string      `json:"title"`
	Subtitle  string      `json:"subtitle"`
	Kind      string      `json:"kind"`
	CreatedAt time.Time   `json:"created_at"`
}

// Hearth is the whole of GET /api/hearth.
type Hearth struct {
	// Capital is the ISO2 country every live drop arcs towards: the
	// configured home_country, or the biggest settlement when none is set.
	Capital         string           `json:"capital"`
	DisplayCurrency string           `json:"display_currency"`
	Era             core.EraProgress `json:"era"`
	TotalXP         int              `json:"total_xp"`
	// Population and RevenueBase are the totals across every country,
	// excluding the unknown bucket (which is reported separately).
	Population  int             `json:"population"`
	RevenueBase float64         `json:"revenue_base"`
	Unknown     HearthUnknown   `json:"unknown"`
	Countries   []HearthCountry `json:"countries"`
	Tiers       []core.Tier     `json:"tiers"`
	Recent      []HearthDrop    `json:"recent"`
}

// Hearth aggregates every country Loot has seen into settlements, places the
// account on the era ladder and returns the most recent country-bearing drops.
//
// homeCountry pins the capital; empty means "pick the largest settlement",
// which is almost always the right guess and never needs configuring.
func (s *Store) Hearth(ctx context.Context, homeCountry, displayCurrency string) (Hearth, error) {
	out := Hearth{
		DisplayCurrency: displayCurrency,
		Countries:       []HearthCountry{},
		Tiers:           core.Tiers,
		Recent:          []HearthDrop{},
	}

	countries, events, unknown, err := s.hearthCountries(ctx)
	if err != nil {
		return out, err
	}
	out.Unknown = unknown

	drops, err := s.hearthDropCounts(ctx)
	if err != nil {
		return out, err
	}
	for i := range countries {
		countries[i].Drops = drops[countries[i].Country]
	}

	// Tiers are relative to the biggest settlement, so one dominant country
	// does not make everything else look identical.
	maxPop := 0
	for _, c := range countries {
		if c.Population > maxPop {
			maxPop = c.Population
		}
	}
	for i := range countries {
		if maxPop > 0 {
			countries[i].Share = roundN(float64(countries[i].Population)/float64(maxPop), 4)
		}
		countries[i].Tier = core.TierForShare(countries[i].Share)
		out.Population += countries[i].Population
		out.RevenueBase = round2(out.RevenueBase + countries[i].RevenueBase)
	}

	sort.Slice(countries, func(i, j int) bool {
		a, b := countries[i], countries[j]
		if a.Population != b.Population {
			return a.Population > b.Population
		}
		if a.RevenueBase != b.RevenueBase {
			return a.RevenueBase > b.RevenueBase
		}
		return a.Country < b.Country
	})
	out.Countries = countries

	out.Capital = pickCapital(homeCountry, countries, events)

	if err := s.q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(xp), 0) FROM drops d WHERE NOT `+unrevealed).Scan(&out.TotalXP); err != nil {
		return out, fmt.Errorf("hearth xp: %w", err)
	}
	out.Era = core.EraFor(out.TotalXP)

	if out.Recent, err = s.hearthRecent(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// pickCapital resolves the capital: the configured country when it is set,
// otherwise the largest settlement. Ties (two countries of equal population,
// which is common early on) fall back to whichever has produced more events,
// so the capital does not flicker between two alphabetically adjacent codes.
func pickCapital(homeCountry string, countries []HearthCountry, events map[string]int) string {
	if home := strings.ToUpper(strings.TrimSpace(homeCountry)); home != "" {
		return home
	}
	best := ""
	bestPop, bestEvents := -1, -1
	for _, c := range countries {
		n := events[c.Country]
		if c.Population > bestPop || (c.Population == bestPop && n > bestEvents) {
			best, bestPop, bestEvents = c.Country, c.Population, n
		}
	}
	return best
}

// hearthCountries returns one row per country that has ever produced an event,
// the per-country event counts (used to break capital ties), and the bucket
// for events that carry no country.
func (s *Store) hearthCountries(ctx context.Context) ([]HearthCountry, map[string]int, HearthUnknown, error) {
	var unknown HearthUnknown

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.country,
               COALESCE(SUM(`+hearthPopulation+`), 0),
               COALESCE(SUM(CASE WHEN `+ledgerRows+` THEN e.amount_base ELSE 0 END), 0),
               MIN(e.day), MAX(e.day), COUNT(*)
        FROM events e
        GROUP BY e.country`)
	if err != nil {
		return nil, nil, unknown, fmt.Errorf("hearth countries: %w", err)
	}
	defer rows.Close()

	var (
		out    []HearthCountry
		events = map[string]int{}
	)
	for rows.Next() {
		var (
			c                  HearthCountry
			first, last        sql.NullString
			eventCount, popCol int
			revenue            float64
			country            string
		)
		if err := rows.Scan(&country, &popCol, &revenue, &first, &last, &eventCount); err != nil {
			return nil, nil, unknown, fmt.Errorf("scan hearth country: %w", err)
		}
		if country == "" {
			unknown.Population = popCol
			unknown.RevenueBase = round2(revenue)
			continue
		}
		c.Country = country
		c.Population = popCol
		c.RevenueBase = round2(revenue)
		c.FirstSeen = first.String
		c.LastSeen = last.String
		events[country] = eventCount
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, unknown, fmt.Errorf("iterate hearth countries: %w", err)
	}
	return out, events, unknown, nil
}

// hearthDropCounts counts the visible drops each country has produced. Drops
// still waiting in an unopened chest are excluded, exactly as they are from
// the feed and from XP.
func (s *Store) hearthDropCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.country, COUNT(*)
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.country <> ''
        GROUP BY e.country`)
	if err != nil {
		return nil, fmt.Errorf("hearth drop counts: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			country string
			n       int
		)
		if err := rows.Scan(&country, &n); err != nil {
			return nil, fmt.Errorf("scan hearth drop count: %w", err)
		}
		out[country] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hearth drop counts: %w", err)
	}
	return out, nil
}

// hearthRecent returns the newest visible drops that carry a country — the
// arrivals ticker, and what the globe replays as arcs on a cold load.
func (s *Store) hearthRecent(ctx context.Context) ([]HearthDrop, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT d.id, d.rarity, d.title, d.subtitle, d.created_at, e.country, e.kind
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.country <> ''
        ORDER BY d.id DESC
        LIMIT ?`, maxHearthRecent)
	if err != nil {
		return nil, fmt.Errorf("hearth recent: %w", err)
	}
	defer rows.Close()

	out := make([]HearthDrop, 0, maxHearthRecent)
	for rows.Next() {
		var (
			d         HearthDrop
			rarity    string
			createdAt int64
		)
		if err := rows.Scan(&d.ID, &rarity, &d.Title, &d.Subtitle, &createdAt, &d.Country, &d.Kind); err != nil {
			return nil, fmt.Errorf("scan hearth recent: %w", err)
		}
		d.Rarity = core.Rarity(rarity)
		d.CreatedAt = time.UnixMilli(createdAt).UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hearth recent: %w", err)
	}
	return out, nil
}
