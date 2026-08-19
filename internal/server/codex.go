package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/core"
)

// The Codex API: the trophy wall with its records and lifetime totals, and the
// season recap.
//
// Both are computed on read — a record is a query, not a row — so the wall is
// memoized for a few seconds exactly as the Hearth is. The page refetches on a
// websocket nudge rather than polling hard, so the cache is never what the eye
// sees; it is there so that three tabs and an unlock cascade cannot ask the
// same aggregate to be computed thirty times.
const codexTTL = 5 * time.Second

// codexCache memoizes the Codex board for codexTTL.
type codexCache struct {
	mu     sync.Mutex
	at     time.Time
	value  codex.Board
	loaded bool
}

func (c *codexCache) cached() (codex.Board, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.at) < codexTTL {
		return c.value, true
	}
	return codex.Board{}, false
}

func (c *codexCache) put(b codex.Board) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = b
	c.at = time.Now()
	c.loaded = true
}

// invalidate drops the memo. An unlock is precisely the moment a stale wall is
// most obvious — the drop is on screen, the page has been nudged to refetch,
// and a five-second-old answer would show the trophy still locked.
func (c *codexCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
}

// InvalidateCodex forgets the memoized trophy wall. `loot serve` hands it to
// the Codex service as its OnChange hook, which is what keeps the cache from
// outliving the unlock that made it wrong.
func (s *Server) InvalidateCodex() { s.codex.invalidate() }

// handleCodex answers the trophy wall: every achievement with its progress,
// the records, and the lifetime totals.
//
// As in handleHearth, the lock is held only while the cache is read or
// written, never across the database read or the JSON write: one slow client
// must not freeze the Codex for every other tab.
func (s *Server) handleCodex(w http.ResponseWriter, r *http.Request) {
	if s.Codex == nil {
		writeJSON(w, http.StatusOK, codex.Board{
			DisplayCurrency: s.displayCurrency(),
			Achievements:    []core.Achievement{},
			Total:           len(codex.Catalog),
		})
		return
	}
	if board, ok := s.codex.cached(); ok {
		writeJSON(w, http.StatusOK, board)
		return
	}

	board, err := s.Codex.List(r.Context())
	if err != nil {
		s.fail(w, "codex", err)
		return
	}
	s.codex.put(board)
	writeJSON(w, http.StatusOK, board)
}

// handleRecap answers the season recap for one period: `?month=YYYY-MM`,
// `?season=YYYY`, or neither for the last complete month.
//
// It carries the picker's own options with it (`periods`), so the page needs
// one request rather than two to draw a month picker that only offers months
// that exist.
func (s *Server) handleRecap(w http.ResponseWriter, r *http.Request) {
	if s.Codex == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "the codex is not enabled"})
		return
	}
	q := r.URL.Query()
	recap, err := s.Codex.Recap(r.Context(), q.Get("month"), q.Get("season"))
	if err != nil {
		// A bad month is the caller's mistake and worth saying so precisely;
		// anything else is ours.
		if _, perr := codex.ResolvePeriod(time.Now(), q.Get("month"), q.Get("season")); perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": perr.Error()})
			return
		}
		s.fail(w, "recap", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recap":   recap,
		"periods": s.Codex.Months(12),
	})
}
