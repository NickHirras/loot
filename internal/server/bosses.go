package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The bosses API: the fights in progress, the ones you have already won, and
// the one button that says you fixed it.
//
// Like the Codex wall, the board is memoized for a few seconds — a chest
// cascade or three open tabs should not make the same aggregate be computed
// thirty times — and the memo is dropped the instant an evaluation changes
// anything, so the refetch a websocket nudge provokes can never be answered
// with a board from before the kill.
const bossesTTL = 5 * time.Second

type bossCache struct {
	mu      sync.Mutex
	entries map[string]bossEntry
}

type bossEntry struct {
	at    time.Time
	value bosses.Board
}

func (c *bossCache) cached(scope string) (bosses.Board, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[scope]; ok && time.Since(e.at) < bossesTTL {
		return e.value, true
	}
	return bosses.Board{}, false
}

func (c *bossCache) put(scope string, b bosses.Board) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]bossEntry{}
	}
	c.entries[scope] = bossEntry{at: time.Now(), value: b}
}

func (c *bossCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

// InvalidateBosses forgets the memoized board. `loot serve` hands it to the
// boss service as its OnChange hook.
func (s *Server) InvalidateBosses() { s.bosses.invalidate() }

// handleBosses answers the board: every fight in progress, and the last few
// that ended.
func (s *Server) handleBosses(w http.ResponseWriter, r *http.Request) {
	if s.Bosses == nil {
		writeJSON(w, http.StatusOK, bosses.Board{Alive: []core.Boss{}, Recent: []core.Boss{}})
		return
	}
	scope := scopeOf(r)
	if board, ok := s.bosses.cached(scope); ok {
		writeJSON(w, http.StatusOK, board)
		return
	}

	board, err := s.Bosses.List(r.Context())
	if err != nil {
		s.fail(w, "bosses", err)
		return
	}
	// A boss row records the raw (source, app) the crash arrived with, so the
	// scope is applied here rather than in SQL. See internal/server/scope.go.
	board = bosses.Board{
		Alive:  s.bossesInScope(scope, board.Alive),
		Recent: s.bossesInScope(scope, board.Recent),
	}
	s.bosses.put(scope, board)
	writeJSON(w, http.StatusOK, board)
}

// bossesInScope keeps the fights that belong to one product. An empty scope
// keeps everything.
func (s *Server) bossesInScope(scope string, list []core.Boss) []core.Boss {
	out := make([]core.Boss, 0, len(list))
	for _, b := range list {
		if s.inScope(scope, b.Source, b.App) {
			out = append(out, b)
		}
	}
	return out
}

// handleBossSlay ends a fight because you said so.
//
// It is deliberately available for every boss, not only the ones from
// push-only sources: you know when you fixed something, and a dashboard that
// argues with you about it has misunderstood whose data this is.
func (s *Server) handleBossSlay(w http.ResponseWriter, r *http.Request) {
	if s.Bosses == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bosses are not enabled"})
		return
	}
	boss, err := s.Bosses.Slay(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrBossNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such boss"})
		return
	}
	if err != nil {
		s.fail(w, "boss slay", err)
		return
	}
	s.bosses.invalidate()
	writeJSON(w, http.StatusOK, map[string]any{"boss": boss})
}
