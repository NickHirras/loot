package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
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

	// Money is scoped strictly: `?app=` here means "this product's revenue",
	// with nothing of anyone else's folded in. See internal/store/scope.go.
	summary, err := s.scoped(r).VaultSummary(r.Context(), from, today, s.displayCurrency(), today)
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
//
// Chests are deliberately *not* scoped. A chest is a daily ritual over
// everything you ship — one lid, one cascade, the whole day's news — and three
// chests a day, one per app, would be three times the ceremony for the same
// morning. `?app=` is accepted and ignored here, which keeps every client's
// request builder uniform.
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

	// The response shape is the same whether or not there was anything to
	// open. A client that opened an already-open chest still needs to know
	// what is left waiting — that is exactly the case where its badge is out
	// of date — and leaving `chests` out made it look like nothing was.
	chests, err := s.Store.ChestSummaries(r.Context())
	if err != nil {
		s.fail(w, "chest open", err)
		return
	}
	if chests == nil {
		chests = []core.ChestSummary{}
	}

	opened := ""
	if len(drops) > 0 {
		opened = drops[0].ChestDate
	} else {
		drops = []store.DropView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"opened": opened,
		"count":  len(drops),
		"drops":  drops,
		"chests": chests,
	})
}
