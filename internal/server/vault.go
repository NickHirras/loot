package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// ranges maps the `range` query parameter of GET /api/vault/summary onto a
// number of days, counting today. Anything else is rejected rather than
// guessed at, so a typo does not silently show the wrong window.
var ranges = map[string]int{
	"7d":   7,
	"30d":  30,
	"90d":  90,
	"365d": 365,
}

// defaultRange is what /api/vault/summary answers with when asked for nothing.
const defaultRange = "30d"

// handleVaultSummary aggregates the money view over a trailing window.
func (s *Server) handleVaultSummary(w http.ResponseWriter, r *http.Request) {
	key := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if key == "" {
		key = defaultRange
	}
	days, ok := ranges[key]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown range " + key + " (use 7d, 30d, 90d or 365d)",
		})
		return
	}

	now := time.Now().UTC()
	today := core.DayOf(now)
	from := core.DayOf(now.AddDate(0, 0, -(days - 1)))

	summary, err := s.Store.VaultSummary(r.Context(), from, today, s.displayCurrency(), today)
	if err != nil {
		s.fail(w, "vault summary", err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) displayCurrency() string {
	if s.Cfg.DisplayCurrency == "" {
		return config.DefaultDisplayCurrency
	}
	return s.Cfg.DisplayCurrency
}

// handleChest lists the chests waiting to be opened, oldest first.
func (s *Server) handleChest(w http.ResponseWriter, r *http.Request) {
	chests, err := s.Store.ChestSummaries(r.Context())
	if err != nil {
		s.fail(w, "chest", err)
		return
	}
	if chests == nil {
		chests = []core.ChestSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chests": chests})
}

// chestOpenRequest is the body of POST /api/chest/open. An empty date opens
// the oldest chest, which is what a single "open" button should do.
type chestOpenRequest struct {
	Date string `json:"date"`
}

// handleChestOpen reveals a chest and returns its drops in cascade order. The
// same drops also go out on the bus, spaced apart, so every connected client
// plays the reveal rather than the opener alone.
func (s *Server) handleChestOpen(w http.ResponseWriter, r *http.Request) {
	var req chestOpenRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	}
	if v := r.URL.Query().Get("date"); v != "" {
		req.Date = v
	}
	req.Date = strings.TrimSpace(req.Date)
	if req.Date != "" {
		if _, err := time.Parse(core.DayLayout, req.Date); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "date must be YYYY-MM-DD"})
			return
		}
	}

	drops, err := s.Pipeline.RevealChest(r.Context(), req.Date)
	if err != nil {
		s.fail(w, "chest open", err)
		return
	}
	if drops == nil {
		writeJSON(w, http.StatusOK, map[string]any{"opened": "", "drops": []any{}, "count": 0})
		return
	}

	chests, err := s.Store.ChestSummaries(r.Context())
	if err != nil {
		s.fail(w, "chest open", err)
		return
	}
	if chests == nil {
		chests = []core.ChestSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"opened": drops[0].ChestDate,
		"count":  len(drops),
		"drops":  drops,
		"chests": chests,
	})
}
