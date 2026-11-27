package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// hearthTTL is how long a Hearth aggregate is reused. The ambient globe polls
// every minute, but several tabs (and a wall-mounted iPad) can be pointed at
// the same server, and the aggregate walks the whole events table — so a few
// seconds of staleness buys immunity to a refresh storm. The globe merges live
// websocket drops on top anyway, so the cache is never what the eye sees.
const hearthTTL = 5 * time.Second

// hearthCache memoizes the Hearth aggregate for hearthTTL, per scope.
//
// Keyed by product because "all apps" and "just Nistis" are different globes
// and one would otherwise serve the other for five seconds — which, on a page
// where changing the scope is a click, is exactly the five seconds somebody is
// looking at it. The map is bounded by how many apps you ship.
type hearthCache struct {
	mu      sync.Mutex
	entries map[string]hearthEntry
}

type hearthEntry struct {
	at    time.Time
	value store.Hearth
}

// cached returns the memoized aggregate for one scope if it is still fresh.
func (c *hearthCache) cached(scope string) (store.Hearth, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[scope]; ok && time.Since(e.at) < hearthTTL {
		return e.value, true
	}
	return store.Hearth{}, false
}

// put stores a freshly computed aggregate.
func (c *hearthCache) put(scope string, h store.Hearth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]hearthEntry{}
	}
	c.entries[scope] = hearthEntry{at: time.Now(), value: h}
}

// handleHearth serves the globe: settlements, era, capital and the recent
// arrivals ticker.
//
// The lock is held only while the cache is read or written, never across the
// database read or the JSON write. Holding it for the whole handler meant one
// slow client — a phone on a bad connection reading a large aggregate — froze
// the globe for every other tab on the server, and a query error left the
// mutex held for the duration of the failing query as well.
//
// Two requests arriving on a cold cache both compute the aggregate. That is
// deliberate: it is one read of an indexed table, and it is a far better
// trade than serializing every reader behind the first one.
func (s *Server) handleHearth(w http.ResponseWriter, r *http.Request) {
	scope := scopeOf(r)
	lang := s.requestLang(r)
	// The memo is per scope, not per language: the aggregate is the same
	// numbers whoever is reading, and only the arrivals ticker is prose. It is
	// translated on the way out of the cache rather than into it, so one
	// German reader cannot leave a German ticker behind for everyone else.
	if hearth, ok := s.hearth.cached(scope); ok {
		writeJSON(w, http.StatusOK, s.localizeHearth(r, lang, hearth))
		return
	}

	// Settlements, population and the arrivals ticker narrow to the product;
	// the era and the XP behind it never do. See internal/store/hearth.go.
	today := core.DayOf(time.Now().UTC())
	hearth, err := s.scoped(r).HearthAt(r.Context(), s.Cfg.HomeCountry, s.displayCurrency(), today)
	if err != nil {
		s.fail(w, "hearth", err)
		return
	}
	s.hearth.put(scope, hearth)

	writeJSON(w, http.StatusOK, s.localizeHearth(r, lang, hearth))
}

// handleHearthTrend serves GET /api/hearth/trend: a daily time series for one
// Hearth metric over a trailing or calendar window (7d, 30d, 90d, ytd, 1y, all).
func (s *Server) handleHearthTrend(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	metric := strings.ToLower(strings.TrimSpace(qs.Get("metric")))
	if metric == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "metric is required (use audience, population, settlements, revenue, fleet, downloads, country or xp)",
		})
		return
	}
	rangeKey := strings.ToLower(strings.TrimSpace(qs.Get("range")))
	if rangeKey == "" {
		rangeKey = "30d"
	}
	if !store.ValidTrendRange(rangeKey) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown range " + rangeKey + " (use 7d, 30d, 90d, ytd, 1y or all)",
		})
		return
	}

	q := store.HearthTrendQuery{
		Metric:  metric,
		Range:   rangeKey,
		Product: strings.TrimSpace(qs.Get("product")),
		Source:  strings.ToLower(strings.TrimSpace(qs.Get("source"))),
		Format:  strings.ToLower(strings.TrimSpace(qs.Get("format"))),
		Country: strings.ToUpper(strings.TrimSpace(qs.Get("country"))),
	}

	switch metric {
	case "audience", "downloads":
		if q.Product == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product is required for " + metric})
			return
		}
	case "fleet":
		if q.Source == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "source is required for fleet"})
			return
		}
	case "country":
		if len(q.Country) != 2 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "country must be a 2-letter ISO code"})
			return
		}
	case "population", "settlements", "revenue", "xp":
		// No extra selector required.
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown metric " + metric + " (use audience, population, settlements, revenue, fleet, downloads, country or xp)",
		})
		return
	}

	today := core.DayOf(time.Now().UTC())
	trend, err := s.scoped(r).HearthTrend(r.Context(), q, s.displayCurrency(), today)
	if err != nil {
		s.fail(w, "hearth trend", err)
		return
	}
	writeJSON(w, http.StatusOK, trend)
}
