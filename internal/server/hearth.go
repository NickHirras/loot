package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/store"
)

// hearthTTL is how long a Hearth aggregate is reused. The ambient globe polls
// every minute, but several tabs (and a wall-mounted iPad) can be pointed at
// the same server, and the aggregate walks the whole events table — so a few
// seconds of staleness buys immunity to a refresh storm. The globe merges live
// websocket drops on top anyway, so the cache is never what the eye sees.
const hearthTTL = 5 * time.Second

// hearthCache memoizes the Hearth aggregate for hearthTTL.
type hearthCache struct {
	mu     sync.Mutex
	at     time.Time
	value  store.Hearth
	loaded bool
}

// cached returns the memoized aggregate if it is still fresh.
func (c *hearthCache) cached() (store.Hearth, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.at) < hearthTTL {
		return c.value, true
	}
	return store.Hearth{}, false
}

// put stores a freshly computed aggregate.
func (c *hearthCache) put(h store.Hearth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = h
	c.at = time.Now()
	c.loaded = true
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
	if hearth, ok := s.hearth.cached(); ok {
		writeJSON(w, http.StatusOK, hearth)
		return
	}

	hearth, err := s.Store.Hearth(r.Context(), s.Cfg.HomeCountry, s.displayCurrency())
	if err != nil {
		s.fail(w, "hearth", err)
		return
	}
	s.hearth.put(hearth)

	writeJSON(w, http.StatusOK, hearth)
}
