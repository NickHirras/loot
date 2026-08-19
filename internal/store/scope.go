package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The app scope, in one place.
//
// Loot is a dashboard for somebody who ships more than one app, and every
// question it answers has an implicit "…across everything". Scoping is how the
// same question is asked about one product instead: `Store.Scoped("Nistis")`
// returns a read view of exactly the same repository whose aggregates are
// narrowed to that product.
//
// It is a view rather than a parameter on thirty methods because scoping is a
// property of the *question*, not of each query: a scoped store hands the same
// Hearth, VaultSummary and Stats calls to the HTTP layer, and nothing above it
// has to remember which of them take a product and which do not.
//
// # Strict and loose
//
// Two filters, and the difference matters:
//
//   - **strict** (`product = ?`) is for the ledger: money, units, installs,
//     settlements. Every one of those rows came from a store's report about one
//     app, so there are no product-less rows to include and including them
//     would be adding somebody else's revenue to yours.
//   - **loose** (this product, or no product at all) is for drops and the feed.
//     Loot's own realm-wide drops — an achievement, a global quest completing —
//     have no product and belong in every scope: they are about the whole
//     hoard, not about one app. Another product's drops are still excluded,
//     which is the entire point of scoping.
//
// Writes are never scoped. A scoped store ingests exactly what an unscoped one
// would; the scope is a lens on reads.
type scope struct {
	product string
}

// active reports whether this scope narrows anything.
func (sc scope) active() bool { return sc.product != "" }

// Scoped returns a read view of the store narrowed to one product. An empty
// product returns a view over everything, so callers can pass a query
// parameter straight through without branching.
func (s *Store) Scoped(product string) *Store {
	c := *s
	c.scope = scope{product: strings.TrimSpace(product)}
	return &c
}

// Product is the scope's product, or "" for all apps.
func (s *Store) Product() string { return s.scope.product }

// scopeStrict renders the ledger filter onto an events alias: rows belonging
// to this product alone. It returns "" and no arguments when unscoped, so it
// can be concatenated into any WHERE clause unconditionally.
func (s *Store) scopeStrict(alias string) (string, []any) {
	if !s.scope.active() {
		return "", nil
	}
	return " AND " + alias + ".product = ?", []any{s.scope.product}
}

// scopeLoose renders the feed filter onto an events alias: this product's rows
// plus the realm-wide ones that belong to no product at all.
func (s *Store) scopeLoose(alias string) (string, []any) {
	if !s.scope.active() {
		return "", nil
	}
	return " AND (" + alias + ".product = ? OR " + alias + ".product = '')",
		[]any{s.scope.product}
}

// scopeQuests renders the scope onto the quests table, whose `app` column
// holds a product name (or "" for a realm-wide quest). Generated quests are
// realm-wide and show in every scope, exactly as global drops do.
func (s *Store) scopeQuests(alias string) (string, []any) {
	if !s.scope.active() {
		return "", nil
	}
	return " AND (" + alias + ".app = ? OR " + alias + ".app = '')", []any{s.scope.product}
}

// Resolver turns a raw (source, app) pair into a canonical product name. It is
// config.Products; the interface is here so the store does not import config.
type Resolver interface {
	Resolve(source, app string) string
}

// RemapProducts recomputes events.product for every stored event and returns
// how many rows changed.
//
// It is cheap however much history there is, because it works on the *distinct*
// (source, app) pairs rather than on rows: a database with two hundred thousand
// events has perhaps a dozen pairs, and only the pairs whose answer actually
// moved are written. That is what makes it safe to run at every startup, which
// in turn is what makes editing `apps:` in the config enough — there is no
// migration to remember and no stale scope to notice a week later.
func (s *Store) RemapProducts(ctx context.Context, resolver Resolver) (int, error) {
	if resolver == nil {
		return 0, nil
	}

	type pair struct{ source, app, product string }
	rows, err := s.q.QueryContext(ctx,
		`SELECT DISTINCT source, app, product FROM events`)
	if err != nil {
		return 0, fmt.Errorf("remap products: %w", err)
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.source, &p.app, &p.product); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan product pair: %w", err)
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, fmt.Errorf("iterate product pairs: %w", err)
	}

	changed := 0
	for _, p := range pairs {
		want := resolver.Resolve(p.source, p.app)
		if want == p.product {
			continue
		}
		res, err := s.q.ExecContext(ctx,
			`UPDATE events SET product = ? WHERE source = ? AND app = ? AND product <> ?`,
			want, p.source, p.app, want)
		if err != nil {
			return changed, fmt.Errorf("remap product %s/%s: %w", p.source, p.app, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("remap product rows: %w", err)
		}
		changed += int(n)
	}
	return changed, nil
}

// ProductPair is one raw (source, app) pair the database has seen, and what it
// currently resolves to. It is what `loot apps` prints and what GET /api/apps
// is assembled from — the answer to "what have my sources actually called
// things, and did my mapping catch it?".
type ProductPair struct {
	Source  string `json:"source"`
	App     string `json:"app"`
	Product string `json:"product"`
	Events  int    `json:"events"`
	// FirstSeen is the earliest business day this pair produced an event.
	FirstSeen string `json:"first_seen"`
}

// ProductPairs returns every distinct (source, app) pair in the database with
// its current product, ordered by product then source then app.
//
// Two kinds of row are left out, both because the question is "what have my
// *sources* called things, and did my mapping catch it?":
//
//   - events with no app at all, which are Loot's realm-wide bookkeeping;
//   - events from the reserved source "loot" — a settlement, a quest, a boss.
//     They carry the app name of whichever real source triggered them, so
//     listing them would suggest mapping `loot:` in the config, which is not a
//     thing.
func (s *Store) ProductPairs(ctx context.Context) ([]ProductPair, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT source, app, product, COUNT(*), COALESCE(MIN(day), '')
        FROM events
        WHERE app <> '' AND source <> 'loot'
        GROUP BY source, app, product`)
	if err != nil {
		return nil, fmt.Errorf("product pairs: %w", err)
	}
	defer rows.Close()

	var out []ProductPair
	for rows.Next() {
		var p ProductPair
		if err := rows.Scan(&p.Source, &p.App, &p.Product, &p.Events, &p.FirstSeen); err != nil {
			return nil, fmt.Errorf("scan product pair: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate product pairs: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Product != b.Product {
			return a.Product < b.Product
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.App < b.App
	})
	return out, nil
}
