package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Audience: how many people (strictly, devices) have each app *now*.
//
// Everything else the store aggregates is a sum of things that happened. This
// is the one place that reads a level instead: for a source that reports its
// own installed base (kind `audience`, see core.KindAudience) only the newest
// snapshot counts, and it replaces every arrival that source ever reported.
// A source that reports no base at all falls back to its arrivals, labelled as
// that, because a download count that can only grow is still better than
// leaving the App Store off the page.

// audienceSourced is true for an event whose (source, product) reports its own
// installed base. Such a pair's arrivals — installs, downloads, purchases — are
// not people in addition to its audience, they are (some of) the same people,
// so every population formula skips them and reads the snapshot instead.
//
// It is keyed on product rather than app because one product's ledger rows and
// its statistics rows do not always share a raw app name (a Play sale is filed
// under a SKU, its installs under a package), and on source because having the
// truth from Snapcraft says nothing about how many people Flathub reached.
//
// The subquery is uncorrelated, so SQLite evaluates it once per statement.
const audienceSourced = `(e.source || char(31) || e.product) IN (
    SELECT DISTINCT a.source || char(31) || a.product FROM events a WHERE a.kind = 'audience')`

// AudienceSource is one store's reading of one product's audience.
type AudienceSource struct {
	Source string `json:"source"`
	// Count is the newest audience total for an active basis, or every arrival
	// the source has ever reported for BasisArrivals.
	Count int                `json:"count"`
	Basis core.AudienceBasis `json:"basis"`
	// AllTime is lifetime installs recorded by an active-basis store (such as
	// Google Play) when the store reports both a current installed base and
	// lifetime installs. Zero for stores without lifetime installs (Snapcraft)
	// and for BasisArrivals rows whose Count is already all-time.
	AllTime int `json:"all_time,omitempty"`
	// AsOf is the business day the snapshot describes (YYYY-MM-DD). Empty for
	// BasisArrivals, which is not a reading of any one day.
	AsOf string `json:"as_of,omitempty"`
}

// AssetDownloads is how many times one kind of release file has been
// downloaded. Downloads are not an audience and are never added to one.
type AssetDownloads struct {
	Format string `json:"format"`
	Count  int    `json:"count"`
}

// ProductScorecard is the all-time business summary for one product:
// lifetime store downloads/installs, paid purchases, refunds, and net revenue.
type ProductScorecard struct {
	Downloads   int     `json:"downloads"`
	Purchases   int     `json:"purchases"`
	Refunds     int     `json:"refunds"`
	RevenueBase float64 `json:"revenue_base"`
}

// AppScorecard is one row of the all-time per-app business summary shown in
// the Vault: current active devices alongside lifetime store metrics.
type AppScorecard struct {
	Product     string  `json:"product"`
	Active      int     `json:"active"`
	Downloads   int     `json:"downloads"`
	Purchases   int     `json:"purchases"`
	Refunds     int     `json:"refunds"`
	RevenueBase float64 `json:"revenue_base"`
}

// ProductAudience is everything Loot knows about who has one product.
type ProductAudience struct {
	Product string `json:"product"`
	// Current is the sum of the sources that report a current installed base.
	// It is the honest answer to "how many do I have": it can go down.
	Current int `json:"current"`
	// Arrivals is the sum of the sources that only report arrivals. It is kept
	// apart from Current because it is a different kind of number, and adding
	// them would be the mistake this whole file exists to undo.
	Arrivals  int               `json:"arrivals"`
	Sources   []AudienceSource  `json:"sources"`
	Downloads []AssetDownloads  `json:"downloads"`
	Scorecard *ProductScorecard `json:"scorecard,omitempty"`
}

// audienceReading is one row of the newest audience snapshot.
type audienceReading struct {
	Source, App, Product, Country string
	Quantity                      int
	Day                           string
}

// audienceRecencyDays is how stale an audience snapshot may be and still be
// counted as current. Matches subscriptionRecencyDays in vault.go: a store
// that stops reporting (for example, a retired desktop build removed from
// config) must not stay frozen at its final device count forever.
const audienceRecencyDays = 14

// audienceLatest returns the newest audience snapshot of every (source, app):
// its total row, and its per-country rows. When today (YYYY-MM-DD) is given,
// snapshots older than audienceRecencyDays are ignored.
//
// The two are looked up separately because sources publish them separately —
// Google Play's overview and country reports are different files that land at
// different times — and pairing a Tuesday total with Monday's countries is far
// better than showing a Tuesday on which everybody is momentarily "at sea".
func (s *Store) audienceLatest(ctx context.Context, today string) ([]audienceReading, error) {
	inner, innerArgs := s.scopeStrict("x")
	outer, outerArgs := s.scopeStrict("e")

	floorClause := ""
	var args []any
	if t, err := time.Parse(core.DayLayout, today); err == nil {
		floor := core.DayOf(t.AddDate(0, 0, -(audienceRecencyDays - 1)))
		floorClause = " AND x.day >= ?"
		args = append(args, floor)
	}
	args = append(args, innerArgs...)
	args = append(args, outerArgs...)

	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.app, e.product, e.country, e.quantity, e.day
        FROM events e
        JOIN (SELECT x.source AS source, x.app AS app, (x.country <> '') AS placed, MAX(x.day) AS day
              FROM events x
              WHERE x.kind = 'audience'`+floorClause+inner+`
              GROUP BY x.source, x.app, (x.country <> '')) l
          ON l.source = e.source AND l.app = e.app
         AND l.placed = (e.country <> '') AND l.day = e.day
        WHERE e.kind = 'audience'`+outer+`
        ORDER BY e.source, e.app, e.country`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("audience latest: %w", err)
	}
	defer rows.Close()

	var out []audienceReading
	for rows.Next() {
		var r audienceReading
		if err := rows.Scan(&r.Source, &r.App, &r.Product, &r.Country, &r.Quantity, &r.Day); err != nil {
			return nil, fmt.Errorf("scan audience: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audience: %w", err)
	}
	return out, nil
}

// audienceApp is one (source, app)'s newest snapshot, folded.
type audienceApp struct {
	Source, Product string
	// Total is the store's own total; HasTotal is false when only country rows
	// have arrived so far, in which case the placed sum stands in for it.
	Total    int
	HasTotal bool
	Placed   int
	AsOf     string
}

// Count is the audience this app has on this source.
func (a audienceApp) Count() int {
	if a.HasTotal {
		return a.Total
	}
	return a.Placed
}

// Unplaced is how much of the audience the store could not put in a country.
// Never negative: a country file that is a day ahead of the overview can
// briefly place more devices than the older total had.
func (a audienceApp) Unplaced() int {
	if !a.HasTotal || a.Total <= a.Placed {
		return 0
	}
	return a.Total - a.Placed
}

// foldAudience turns the newest readings into per-country populations and one
// summary per (source, app).
func foldAudience(readings []audienceReading) (byCountry map[string]int, apps []audienceApp) {
	byCountry = map[string]int{}
	index := map[string]int{}
	for _, r := range readings {
		key := r.Source + "\x1f" + r.App
		i, ok := index[key]
		if !ok {
			i = len(apps)
			index[key] = i
			apps = append(apps, audienceApp{Source: r.Source, Product: r.Product})
		}
		if r.Country == "" {
			apps[i].Total, apps[i].HasTotal = r.Quantity, true
			apps[i].AsOf = r.Day
			continue
		}
		if r.Quantity <= 0 {
			continue
		}
		apps[i].Placed += r.Quantity
		byCountry[r.Country] += r.Quantity
		if apps[i].AsOf == "" {
			apps[i].AsOf = r.Day
		}
	}
	return byCountry, apps
}

// audienceArrivals returns, per (source, product), every arrival a source that
// reports no installed base has counted — the same people hearthPopulation
// counts, by the same rules, including the install de-duplication.
func (s *Store) audienceArrivals(ctx context.Context) (map[[2]string]int, error) {
	strict, strictArgs := s.scopeStrict("e")
	out := map[[2]string]int{}

	collect := func(query string, args []any, what string) error {
		rows, err := s.q.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("audience %s: %w", what, err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				source, product string
				n               int
			)
			if err := rows.Scan(&source, &product, &n); err != nil {
				return fmt.Errorf("scan audience %s: %w", what, err)
			}
			if n > 0 && product != "" {
				out[[2]string{source, product}] += n
			}
		}
		return rows.Err()
	}

	// Installs first: a (source, app, day) with an overview row is that row,
	// otherwise it is its country rows — see installValue.
	if err := collect(`
        SELECT source, product, COALESCE(SUM(v), 0) FROM (
            SELECT e.source AS source, MAX(e.product) AS product, `+installValue+` AS v
            FROM events e
            WHERE e.kind = 'install' AND NOT `+audienceSourced+strict+`
            GROUP BY e.source, e.app, e.day)
        GROUP BY source, product`, strictArgs, "installs"); err != nil {
		return nil, err
	}
	// Then everybody who arrived some other way: ledger units and purchases.
	if err := collect(`
        SELECT e.source, e.product, COALESCE(SUM(`+hearthPopulationNoInstalls+`), 0)
        FROM events e
        WHERE 1 = 1`+strict+`
        GROUP BY e.source, e.product`, strictArgs, "arrivals"); err != nil {
		return nil, err
	}
	return out, nil
}

// assetDownloads returns release-file downloads per product and format.
func (s *Store) assetDownloads(ctx context.Context) (map[string][]AssetDownloads, error) {
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.product, COALESCE(json_extract(e.payload, '$.format'), ''), COALESCE(SUM(e.quantity), 0)
        FROM events e
        WHERE e.kind = 'asset_download'`+strict+`
        GROUP BY 1, 2`, strictArgs...)
	if err != nil {
		return nil, fmt.Errorf("asset downloads: %w", err)
	}
	defer rows.Close()

	out := map[string][]AssetDownloads{}
	for rows.Next() {
		var (
			product, format string
			n               int
		)
		if err := rows.Scan(&product, &format, &n); err != nil {
			return nil, fmt.Errorf("scan asset downloads: %w", err)
		}
		if n <= 0 || product == "" {
			continue
		}
		if format == "" {
			format = "other"
		}
		out[product] = append(out[product], AssetDownloads{Format: format, Count: n})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset downloads: %w", err)
	}
	for product := range out {
		list := out[product]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Count != list[j].Count {
				return list[i].Count > list[j].Count
			}
			return list[i].Format < list[j].Format
		})
	}
	return out, nil
}

// Audience returns one entry per product: who has it now, per store, and how
// often its release files have been downloaded. Biggest audience first.
func (s *Store) Audience(ctx context.Context, today string) ([]ProductAudience, error) {
	readings, err := s.audienceLatest(ctx, today)
	if err != nil {
		return nil, err
	}
	_, apps := foldAudience(readings)
	return s.audienceFrom(ctx, apps)
}

// installTotals returns lifetime deduped installs per (source, product),
// including sources that also emit audience snapshots (such as Google Play).
// Snapcraft emits no `install` events, so it does not appear here.
func (s *Store) installTotals(ctx context.Context) (map[[2]string]int, error) {
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT source, product, COALESCE(SUM(v), 0) FROM (
            SELECT e.source AS source,
                   MAX(COALESCE(NULLIF(e.product, ''), e.app)) AS product,
                   `+installValue+` AS v
            FROM events e
            WHERE e.kind = 'install'`+strict+`
            GROUP BY e.source, e.app, e.day)
        GROUP BY source, product`, strictArgs...)
	if err != nil {
		return nil, fmt.Errorf("install totals: %w", err)
	}
	defer rows.Close()

	out := map[[2]string]int{}
	for rows.Next() {
		var (
			source, product string
			n               int
		)
		if err := rows.Scan(&source, &product, &n); err != nil {
			return nil, fmt.Errorf("scan install totals: %w", err)
		}
		if n > 0 && product != "" {
			out[[2]string{source, product}] += n
		}
	}
	return out, rows.Err()
}

type ledgerProductRow struct {
	Source       string
	Product      string
	AppDownloads int
	Purchases    int
	Refunds      int
	RevenueBase  float64
}

// productLedgerBySource aggregates all-time ledger counts per (source, product).
// AppDownloads counts free and paid app acquisitions (download + sale);
// Purchases counts paid ledger units (sale + iap + subscription); Refunds
// counts refunded units as a positive count; RevenueBase nets signed revenue.
func (s *Store) productLedgerBySource(ctx context.Context) ([]ledgerProductRow, error) {
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source,
               COALESCE(NULLIF(e.product, ''), e.app) AS product,
               COALESCE(SUM(CASE WHEN e.kind IN ('download', 'sale') AND e.quantity > 0 THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN `+isRefund+` THEN ABS(e.quantity) ELSE 0 END), 0),
               COALESCE(SUM(e.amount_base), 0)
        FROM events e
        WHERE `+ledgerRows+strict+`
        GROUP BY 1, 2`, strictArgs...)
	if err != nil {
		return nil, fmt.Errorf("product ledger: %w", err)
	}
	defer rows.Close()

	var out []ledgerProductRow
	for rows.Next() {
		var r ledgerProductRow
		if err := rows.Scan(&r.Source, &r.Product, &r.AppDownloads, &r.Purchases, &r.Refunds, &r.RevenueBase); err != nil {
			return nil, fmt.Errorf("scan product ledger: %w", err)
		}
		if r.Product == "" {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// buildScorecards folds lifetime installs and ledger rows into one
// ProductScorecard per product. When a (source, product) has dedicated install
// events (e.g. Google Play), those represent its app downloads rather than its
// ledger `sale` rows so paid apps are not counted twice.
func buildScorecards(installs map[[2]string]int, ledger []ledgerProductRow) map[string]ProductScorecard {
	out := map[string]ProductScorecard{}
	for k, n := range installs {
		if n <= 0 || k[1] == "" {
			continue
		}
		sc := out[k[1]]
		sc.Downloads += n
		out[k[1]] = sc
	}
	for _, r := range ledger {
		sc := out[r.Product]
		if installs[[2]string{r.Source, r.Product}] == 0 {
			sc.Downloads += r.AppDownloads
		}
		sc.Purchases += r.Purchases
		sc.Refunds += r.Refunds
		sc.RevenueBase = round2(sc.RevenueBase + r.RevenueBase)
		out[r.Product] = sc
	}
	return out
}

// AppScorecards returns the all-time business summary for every product:
// current active devices (where reported and not stale), lifetime store
// downloads/installs, paid purchases, refunds, and net revenue.
func (s *Store) AppScorecards(ctx context.Context, today string) ([]AppScorecard, error) {
	readings, err := s.audienceLatest(ctx, today)
	if err != nil {
		return nil, err
	}
	_, apps := foldAudience(readings)

	installs, err := s.installTotals(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := s.productLedgerBySource(ctx)
	if err != nil {
		return nil, err
	}
	scMap := buildScorecards(installs, ledger)

	activeByProduct := map[string]int{}
	for _, a := range apps {
		if a.Product != "" && a.Count() > 0 {
			activeByProduct[a.Product] += a.Count()
		}
	}

	products := map[string]bool{}
	for p := range activeByProduct {
		products[p] = true
	}
	for p := range scMap {
		products[p] = true
	}

	out := make([]AppScorecard, 0, len(products))
	for p := range products {
		sc := scMap[p]
		row := AppScorecard{
			Product:     p,
			Active:      activeByProduct[p],
			Downloads:   sc.Downloads,
			Purchases:   sc.Purchases,
			Refunds:     sc.Refunds,
			RevenueBase: sc.RevenueBase,
		}
		if row.Active == 0 && row.Downloads == 0 && row.Purchases == 0 && row.Refunds == 0 && row.RevenueBase == 0 {
			continue
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RevenueBase != b.RevenueBase {
			return a.RevenueBase > b.RevenueBase
		}
		if a.Purchases != b.Purchases {
			return a.Purchases > b.Purchases
		}
		if a.Active != b.Active {
			return a.Active > b.Active
		}
		if a.Downloads != b.Downloads {
			return a.Downloads > b.Downloads
		}
		return a.Product < b.Product
	})
	return out, nil
}

// audienceFrom builds the per-product summary from already-folded snapshots,
// so the Hearth does not read the newest snapshot twice.
func (s *Store) audienceFrom(ctx context.Context, apps []audienceApp) ([]ProductAudience, error) {
	arrivals, err := s.audienceArrivals(ctx)
	if err != nil {
		return nil, err
	}
	installs, err := s.installTotals(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := s.productLedgerBySource(ctx)
	if err != nil {
		return nil, err
	}
	scorecards := buildScorecards(installs, ledger)

	downloads, err := s.assetDownloads(ctx)
	if err != nil {
		return nil, err
	}

	index := map[string]int{}
	out := []ProductAudience{}
	entry := func(product string) *ProductAudience {
		i, ok := index[product]
		if !ok {
			i = len(out)
			index[product] = i
			out = append(out, ProductAudience{
				Product: product, Sources: []AudienceSource{}, Downloads: []AssetDownloads{},
			})
		}
		return &out[i]
	}

	// Two raw apps of one product on one source (a snap that was renamed, two
	// Play packages mapped to one product) are one line, not two.
	type pair struct{ source, product string }
	current := map[pair]*AudienceSource{}
	hasMobileStore := map[string]bool{}
	var order []pair
	for _, a := range apps {
		if a.Product == "" || a.Count() <= 0 {
			continue
		}
		k := pair{a.Source, a.Product}
		row, ok := current[k]
		if !ok {
			row = &AudienceSource{
				Source:  a.Source,
				Basis:   core.AudienceBasisFor(a.Source),
				AllTime: installs[[2]string{a.Source, a.Product}],
			}
			current[k] = row
			order = append(order, k)
		}
		row.Count += a.Count()
		if a.AsOf > row.AsOf {
			row.AsOf = a.AsOf
		}
		if a.Source == "googleplay" || a.Source == "appstore" {
			hasMobileStore[a.Product] = true
		}
	}
	for k := range arrivals {
		if k[0] == "appstore" || k[0] == "googleplay" {
			hasMobileStore[k[1]] = true
		}
	}

	for _, k := range order {
		p := entry(k.product)
		p.Current += current[k].Count
		p.Sources = append(p.Sources, *current[k])
	}
	for k, n := range arrivals {
		// A pair that reports an audience never reaches here for its installs,
		// but its ledger units would: a paying customer is already one of the
		// active devices.
		if _, ok := current[pair{k[0], k[1]}]; ok {
			continue
		}
		// RevenueCat is a webhook layer over App Store and Google Play, not a
		// separate storefront. Keep it only when a product has no mobile store
		// reporting directly, so paying users are not double-counted beside
		// App Store or Google Play.
		if k[0] == "revenuecat" && hasMobileStore[k[1]] {
			continue
		}
		p := entry(k[1])
		p.Arrivals += n
		p.Sources = append(p.Sources, AudienceSource{Source: k[0], Count: n, Basis: core.BasisArrivals})
	}
	for product, list := range downloads {
		entry(product).Downloads = list
	}
	for i := range out {
		if sc, ok := scorecards[out[i].Product]; ok &&
			(sc.Downloads > 0 || sc.Purchases > 0 || sc.Refunds > 0 || sc.RevenueBase != 0) {
			cp := sc
			out[i].Scorecard = &cp
		}
	}

	for i := range out {
		list := out[i].Sources
		sort.Slice(list, func(a, b int) bool {
			// Current readings before arrivals, then biggest first.
			ca, cb := list[a].Basis != core.BasisArrivals, list[b].Basis != core.BasisArrivals
			if ca != cb {
				return ca
			}
			if list[a].Count != list[b].Count {
				return list[a].Count > list[b].Count
			}
			return list[a].Source < list[b].Source
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Current != b.Current {
			return a.Current > b.Current
		}
		if a.Arrivals != b.Arrivals {
			return a.Arrivals > b.Arrivals
		}
		return a.Product < b.Product
	})
	return out, nil
}

// HighestAudienceMilestone returns the biggest audience milestone already
// minted for (source, app), or 0 when there is none. The pipeline asks before
// minting another, so an audience that dips below a rung it has passed does not
// then "reach" the rung beneath it.
func (s *Store) HighestAudienceMilestone(ctx context.Context, source, app string) (int, error) {
	var best int
	// events_record_idx is (source, app, kind, quantity): this is one seek.
	err := s.q.QueryRowContext(ctx, `
        SELECT COALESCE(MAX(quantity), 0) FROM events
        WHERE source = ? AND app = ? AND kind = ?`,
		source, app, core.KindAudienceMilestone).Scan(&best)
	if err != nil {
		return 0, fmt.Errorf("highest audience milestone: %w", err)
	}
	return best, nil
}
