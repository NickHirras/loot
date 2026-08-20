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

// hearthPopulationNoInstalls is hearthPopulation with the install branch taken
// out. It exists for the fleet, whose install population cannot be a plain
// sum — see hearthUnknownInstalls.
const hearthPopulationNoInstalls = `
CASE
    WHEN e.is_ledger = 1 AND e.kind IN ('sale', 'iap', 'subscription', 'download') AND e.quantity > 0 THEN e.quantity
    WHEN e.source = 'revenuecat' AND e.kind = 'purchase' THEN 1
    ELSE 0
END`

// hearthUnknownInstalls is how many of a day's installs could not be placed on
// the map, summed over every (source, app, day) and grouped by source — one
// row per source, which is one vessel of the fleet.
//
// Google Play reports its installs *twice*: once as an overview row with no
// country, and once per country. Adding both up gave every Play install a
// citizen in a country and a second one in unknown lands, so a hundred
// installs read as two hundred people and the unknown bucket grew as fast as
// the map did.
//
// The day's real total is installValue — the same rule quests and the mystery
// detector use: an overview row, where there is one, *is* the day, otherwise
// the country rows are it. The source's vessel then gets the remainder the
// country rows could not account for, never less than nothing. A Flathub day
// (one row, no country) is therefore entirely at sea, a Play day covered by
// its country file is entirely placed, and a Play day whose country file has
// not arrived yet is at sea until it does.
func hearthUnknownInstalls(scope string) string {
	return `
SELECT source, COALESCE(SUM(MAX(day_total - placed, 0)), 0) FROM (
    SELECT e.source AS source,
           ` + installValue + ` AS day_total,
           COALESCE(SUM(CASE WHEN e.country <> '' AND e.quantity > 0 THEN e.quantity ELSE 0 END), 0) AS placed
    FROM events e
    WHERE e.kind = 'install'` + scope + `
    GROUP BY e.source, e.app, e.day)
GROUP BY source`
}

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

// HearthUnknown is the whole fleet added up: every event that carries no
// country at all. Flathub reports installs per app but not per country, so its
// citizens are neither invented into a country nor silently dropped.
//
// It is kept alongside Fleet because it is the honest one-number answer to
// "how many people can you not place?", and because clients older than the
// fleet still read it.
type HearthUnknown struct {
	Population  int     `json:"population"`
	RevenueBase float64 `json:"revenue_base"`
}

// HearthVessel is one ship of the fleet: everybody a single source counted but
// never located. They are not a country and are never counted as one — no
// settlement, no continent, no share of the map. They are simply somewhere at
// sea, and the globe draws them there.
type HearthVessel struct {
	// Source is the source id ("flathub"), which is what picks the vessel's
	// name and its anchorage in the UI.
	Source      string  `json:"source"`
	Population  int     `json:"population"`
	RevenueBase float64 `json:"revenue_base"`
	// FirstSeen and LastSeen are business days (YYYY-MM-DD): when the first
	// and most recent countryless event from this source landed.
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	// Tier is how big the vessel reads, measured against the largest
	// settlement exactly as a country's tier is — a fleet bigger than every
	// country is a metropolis afloat.
	Tier core.Tier `json:"tier"`
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
	Population  int           `json:"population"`
	RevenueBase float64       `json:"revenue_base"`
	Unknown     HearthUnknown `json:"unknown"`
	// Fleet is the countryless population, one vessel per source. Unknown is
	// its sum.
	Fleet     []HearthVessel  `json:"fleet"`
	Countries []HearthCountry `json:"countries"`
	Tiers     []core.Tier     `json:"tiers"`
	Recent    []HearthDrop    `json:"recent"`
}

// Hearth aggregates every country Loot has seen into settlements, places the
// account on the era ladder and returns the most recent country-bearing drops.
//
// homeCountry pins the capital; empty means "pick the largest settlement",
// which is almost always the right guess and never needs configuring.
func (s *Store) Hearth(ctx context.Context, homeCountry, displayCurrency string) (Hearth, error) {
	out := Hearth{
		DisplayCurrency: displayCurrency,
		Fleet:           []HearthVessel{},
		Countries:       []HearthCountry{},
		Tiers:           core.Tiers,
		Recent:          []HearthDrop{},
	}
	if out.Tiers == nil {
		out.Tiers = []core.Tier{}
	}

	countries, events, err := s.hearthCountries(ctx)
	if err != nil {
		return out, err
	}

	fleet, err := s.hearthFleet(ctx)
	if err != nil {
		return out, err
	}

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

	// A vessel is measured against the same yardstick as a settlement, so a
	// fleet carrying more people than your biggest country reads as big as one
	// — which, on the globe, is the whole point. A map with no countries on it
	// at all (a Flathub-only app) falls back to the largest vessel, so the
	// fleet is at least relative to itself rather than uniformly tiny.
	fleetMax := 0
	for _, v := range fleet {
		if v.Population > fleetMax {
			fleetMax = v.Population
		}
	}
	against := maxPop
	if against == 0 {
		against = fleetMax
	}
	for i := range fleet {
		share := 0.0
		if against > 0 {
			share = float64(fleet[i].Population) / float64(against)
		}
		fleet[i].Tier = core.TierForShare(share)
		out.Unknown.Population += fleet[i].Population
		out.Unknown.RevenueBase = round2(out.Unknown.RevenueBase + fleet[i].RevenueBase)
	}
	if fleet != nil {
		out.Fleet = fleet
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
	// hearthCountries returns nil for a database with no events at all, and a
	// nil slice serializes as JSON `null` rather than `[]` — which a client
	// that maps over it has to special-case. Every list on this struct is a
	// list, empty or not.
	if countries != nil {
		out.Countries = countries
	}

	out.Capital = pickCapital(homeCountry, countries, events)

	// XP and the era are deliberately *not* scoped. They are the account's
	// standing — how far you have come, across everything you ship — and an
	// era that went backwards when you narrowed the view to one app would be
	// telling you something untrue about yourself.
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

// hearthCountries returns one row per country that has ever produced an event
// and the per-country event counts (used to break capital ties). Events with
// no country belong to the fleet, not to the map, and are skipped here.
func (s *Store) hearthCountries(ctx context.Context) ([]HearthCountry, map[string]int, error) {
	// Settlements are arrivals and money, so the scope is strict: a scoped
	// Hearth is this app's map of the world, not this app's plus everyone
	// else's citizens.
	strict, strictArgs := s.scopeStrict("e")

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.country,
               COALESCE(SUM(`+hearthPopulation+`), 0),
               COALESCE(SUM(CASE WHEN `+ledgerRows+` THEN e.amount_base ELSE 0 END), 0),
               MIN(e.day), MAX(e.day), COUNT(*)
        FROM events e
        WHERE e.country <> ''`+strict+`
        GROUP BY e.country`, strictArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("hearth countries: %w", err)
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
			return nil, nil, fmt.Errorf("scan hearth country: %w", err)
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
		return nil, nil, fmt.Errorf("iterate hearth countries: %w", err)
	}
	return out, events, nil
}

// hearthFleet returns one vessel per source that has people it cannot place,
// biggest first. Tiers are left to the caller, which is the only place that
// knows how big the largest settlement is.
//
// The rules are the country rules, exactly: the same population formula, the
// same ledger-only revenue, refunds netting out of money but never evicting
// anybody, and the same installValue de-duplication — a Play install that a
// country row already placed must not board a ship as well.
//
// A vessel with no population but some money is still a vessel: an App Store
// refund that arrived without a country is a real (negative) number, and
// dropping it here would make the fleet stop adding up to Unknown.
func (s *Store) hearthFleet(ctx context.Context) ([]HearthVessel, error) {
	strict, strictArgs := s.scopeStrict("e")

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source,
               COALESCE(SUM(`+hearthPopulationNoInstalls+`), 0),
               COALESCE(SUM(CASE WHEN `+ledgerRows+` THEN e.amount_base ELSE 0 END), 0),
               MIN(e.day), MAX(e.day)
        FROM events e
        WHERE e.country = ''`+strict+`
        GROUP BY e.source`, strictArgs...)
	if err != nil {
		return nil, fmt.Errorf("hearth fleet: %w", err)
	}
	defer rows.Close()

	var (
		fleet []HearthVessel
		index = map[string]int{}
	)
	for rows.Next() {
		var (
			v           HearthVessel
			first, last sql.NullString
		)
		// Installs are added below, after the double counting a plain sum
		// would cause has been resolved.
		if err := rows.Scan(&v.Source, &v.Population, &v.RevenueBase, &first, &last); err != nil {
			return nil, fmt.Errorf("scan hearth vessel: %w", err)
		}
		v.RevenueBase = round2(v.RevenueBase)
		v.FirstSeen = first.String
		v.LastSeen = last.String
		index[v.Source] = len(fleet)
		fleet = append(fleet, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hearth fleet: %w", err)
	}
	// Close the cursor before the next query: SQLite is opened with a single
	// connection, so an open result set would block it.
	rows.Close()

	unplaced, err := s.q.QueryContext(ctx, hearthUnknownInstalls(strict), strictArgs...)
	if err != nil {
		return nil, fmt.Errorf("hearth unknown installs: %w", err)
	}
	defer unplaced.Close()

	for unplaced.Next() {
		var (
			source string
			people int
		)
		if err := unplaced.Scan(&source, &people); err != nil {
			return nil, fmt.Errorf("scan hearth unknown installs: %w", err)
		}
		if people == 0 {
			continue
		}
		if i, ok := index[source]; ok {
			fleet[i].Population += people
			continue
		}
		// A source whose only countryless rows are the installs themselves
		// never appeared in the query above — its remainder is a day the
		// country file has not covered yet, so it gets a vessel of its own.
		index[source] = len(fleet)
		fleet = append(fleet, HearthVessel{Source: source, Population: people})
	}
	if err := unplaced.Err(); err != nil {
		return nil, fmt.Errorf("iterate hearth unknown installs: %w", err)
	}
	unplaced.Close()

	out := fleet[:0]
	for _, v := range fleet {
		if v.Population <= 0 && v.RevenueBase == 0 {
			continue
		}
		if v.FirstSeen == "" || v.LastSeen == "" {
			first, last, err := s.hearthSourceDays(ctx, v.Source)
			if err != nil {
				return nil, err
			}
			v.FirstSeen, v.LastSeen = first, last
		}
		out = append(out, v)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Population != b.Population {
			return a.Population > b.Population
		}
		if a.RevenueBase != b.RevenueBase {
			return a.RevenueBase > b.RevenueBase
		}
		return a.Source < b.Source
	})
	return out, nil
}

// hearthSourceDays is the first and last countryless day one source reported,
// for the rare vessel built entirely out of an install remainder whose own
// rows all carry a country.
func (s *Store) hearthSourceDays(ctx context.Context, source string) (string, string, error) {
	strict, strictArgs := s.scopeStrict("e")
	var first, last sql.NullString
	err := s.q.QueryRowContext(ctx, `
        SELECT MIN(e.day), MAX(e.day) FROM events e
        WHERE e.source = ? AND e.kind = 'install'`+strict,
		append([]any{source}, strictArgs...)...).Scan(&first, &last)
	if err != nil {
		return "", "", fmt.Errorf("hearth vessel days: %w", err)
	}
	return first.String, last.String, nil
}

// hearthDropCounts counts the visible drops each country has produced. Drops
// still waiting in an unopened chest are excluded, exactly as they are from
// the feed and from XP.
func (s *Store) hearthDropCounts(ctx context.Context) (map[string]int, error) {
	loose, looseArgs := s.scopeLoose("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.country, COUNT(*)
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.country <> ''`+loose+`
        GROUP BY e.country`, looseArgs...)
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
	loose, looseArgs := s.scopeLoose("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT d.id, d.rarity, d.title, d.subtitle, d.created_at, e.country, e.kind
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.country <> ''`+loose+`
        ORDER BY d.id DESC
        LIMIT ?`, append(looseArgs, maxHearthRecent)...)
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
