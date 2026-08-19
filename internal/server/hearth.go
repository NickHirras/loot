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

// handleHearth serves the globe: settlements, era, capital and the recent
// arrivals ticker.
func (s *Server) handleHearth(w http.ResponseWriter, r *http.Request) {
	s.hearth.mu.Lock()
	defer s.hearth.mu.Unlock()

	if s.hearth.loaded && time.Since(s.hearth.at) < hearthTTL {
		writeJSON(w, http.StatusOK, s.hearth.value)
		return
	}

	hearth, err := s.Store.Hearth(r.Context(), s.Cfg.HomeCountry, s.displayCurrency())
	if err != nil {
		s.fail(w, "hearth", err)
		return
	}
	s.hearth.value = hearth
	s.hearth.at = time.Now()
	s.hearth.loaded = true

	writeJSON(w, http.StatusOK, hearth)
}
