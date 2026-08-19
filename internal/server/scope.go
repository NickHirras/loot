package server

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The app scope over HTTP: `?app=<product>` on every read endpoint.
//
// One parameter, one meaning, everywhere: narrow this answer to one product.
// Absent or empty is "all apps", which is what a client that has never heard
// of scoping sends, so every endpoint stays backwards compatible.
//
// The value is a *product* name — the canonical name from `apps:` in the
// config — or the raw app name of something nobody has mapped yet, since an
// unmapped app resolves to itself. Both are selectable in the UI and both work
// here, which is the point: the mapping is a convenience, not a gate.
//
// An unknown value is not an error. It scopes to a product with no events and
// answers empty, which is exactly what a link to an app you have since renamed
// should do — say "nothing here" rather than 400 at somebody who followed a
// bookmark.

// scopeOf reads the requested product from `?app=`.
func scopeOf(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("app"))
}

// scoped returns the store narrowed to the request's product.
func (s *Server) scoped(r *http.Request) *store.Store {
	return s.Store.Scoped(scopeOf(r))
}

// productOf resolves a raw (source, app) pair to its canonical product, so
// rows that carry no product column of their own — bosses, mysteries — can
// still be filtered.
func (s *Server) productOf(source, app string) string {
	return s.Cfg.Apps.Resolve(source, app)
}

// inScope reports whether a (source, app) pair belongs in the given scope.
// An empty scope takes everything; an empty app is realm-wide and shows in
// every scope, exactly as a product-less drop does.
func (s *Server) inScope(scope, source, app string) bool {
	if scope == "" {
		return true
	}
	if strings.TrimSpace(app) == "" {
		return true
	}
	return strings.EqualFold(s.productOf(source, app), scope)
}

// Bosses and mysteries are *readings* of events rather than events, so their
// rows carry the raw (source, app) the detector saw and no product column of
// their own. Both tables are small — the fights you are in the middle of, the
// days still unexplained — so a scoped count filters them in Go rather than
// paying for a third column that would need its own remap.

// countOpenMysteries is the Quests-tab badge, scoped.
func (s *Server) countOpenMysteries(ctx context.Context, scope string) (int, error) {
	if scope == "" {
		return s.Store.CountOpenMysteries(ctx)
	}
	open, err := s.Store.ListMysteries(ctx, store.MysteryQuery{Statuses: []string{core.MysteryOpen}})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range open {
		if s.inScope(scope, m.Source, m.App) {
			n++
		}
	}
	return n, nil
}

// countAliveBosses is the red badge, scoped.
func (s *Server) countAliveBosses(ctx context.Context, scope string) (int, error) {
	if scope == "" {
		return s.Store.CountAliveBosses(ctx)
	}
	alive, err := s.Store.ListBosses(ctx, store.BossQuery{Statuses: []string{core.BossAlive}})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range alive {
		if s.inScope(scope, b.Source, b.App) {
			n++
		}
	}
	return n, nil
}

// knownProducts lists every product the dashboard could be scoped to:
// the configured ones first, in configuration order, then anything a source
// has actually reported that no mapping claims, alphabetically.
//
// Both halves matter. The configured list is there before a single event
// arrives, so the selector is populated on a fresh install; the observed list
// is what stops a new app from being invisible until somebody edits the YAML.
func (s *Server) knownProducts(pairs []store.ProductPair) []string {
	out := s.Cfg.Apps.Names()
	seen := make(map[string]bool, len(out))
	for _, name := range out {
		seen[strings.ToLower(name)] = true
	}

	var extra []string
	for _, p := range pairs {
		if p.Product == "" || seen[strings.ToLower(p.Product)] {
			continue
		}
		seen[strings.ToLower(p.Product)] = true
		extra = append(extra, p.Product)
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// appProduct is one product as GET /api/apps reports it.
type appProduct struct {
	Name string `json:"name"`
	// Sources maps a source id to the raw app names it has used for this
	// product — the evidence behind the mapping, which is what makes a wrong
	// one obvious.
	Sources map[string][]string `json:"sources"`
	// Configured is false for a product Loot inferred from the data because
	// nothing in `apps:` claimed it.
	Configured bool   `json:"configured"`
	Events     int    `json:"events"`
	FirstSeen  string `json:"first_seen"`
}

// handleApps answers "what apps does this Loot know about, and did my mapping
// catch them?" — the same question `loot apps` prints.
//
// `unmapped` is the interesting half: every (source, app) pair that resolved
// to its own raw name rather than to a configured product. On a correctly
// configured Loot it is empty; on a fresh one it is the list to paste into
// `apps:`.
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	pairs, err := s.Store.ProductPairs(r.Context())
	if err != nil {
		s.fail(w, "apps", err)
		return
	}

	index := map[string]*appProduct{}
	order := []string{}
	add := func(name string) *appProduct {
		p, ok := index[name]
		if !ok {
			p = &appProduct{
				Name:       name,
				Sources:    map[string][]string{},
				Configured: s.Cfg.Apps.Has(name),
			}
			index[name] = p
			order = append(order, name)
		}
		return p
	}
	// Configured products are listed even before they have an event, so a
	// mapping that has not matched anything yet is visibly present rather
	// than mysteriously absent.
	for _, name := range s.Cfg.Apps.Names() {
		add(name)
	}

	unmapped := []store.ProductPair{}
	for _, pair := range pairs {
		if pair.Product == "" {
			continue
		}
		p := add(pair.Product)
		p.Events += pair.Events
		p.Sources[pair.Source] = append(p.Sources[pair.Source], pair.App)
		if p.FirstSeen == "" || (pair.FirstSeen != "" && pair.FirstSeen < p.FirstSeen) {
			p.FirstSeen = pair.FirstSeen
		}
		if !s.Cfg.Apps.Has(pair.Product) {
			unmapped = append(unmapped, pair)
		}
	}

	products := make([]appProduct, 0, len(order))
	for _, name := range order {
		p := index[name]
		for source := range p.Sources {
			sort.Strings(p.Sources[source])
		}
		products = append(products, *p)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"products": products,
		"unmapped": unmapped,
		"scope":    scopeOf(r),
	})
}
