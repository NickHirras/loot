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
	mu     sync.Mutex
	at     time.Time
	value  bosses.Board
	loaded bool
}

func (c *bossCache) cached() (bosses.Board, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.at) < bossesTTL {
		return c.value, true
	}
	return bosses.Board{}, false
}

func (c *bossCache) put(b bosses.Board) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = b
	c.at = time.Now()
	c.loaded = true
}

func (c *bossCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
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
	if board, ok := s.bosses.cached(); ok {
		writeJSON(w, http.StatusOK, board)
		return
	}

	board, err := s.Bosses.List(r.Context())
	if err != nil {
		s.fail(w, "bosses", err)
		return
	}
	s.bosses.put(board)
	writeJSON(w, http.StatusOK, board)
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
