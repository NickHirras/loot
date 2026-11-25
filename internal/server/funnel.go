package server

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/funnel"
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/store"
)

// Funnel windows, in days counting today. Ad platforms keep a few weeks of
// daily breakdowns readily available and the page is a morning glance, not an
// archive, so the window is clamped rather than rejected: a bookmark with
// ?days=365 still shows something sensible.
const (
	defaultFunnelDays = 14
	maxFunnelDays     = 90
)

// handleFunnel answers GET /api/funnel?days=N&app=<product>: the acquisition
// funnel over the trailing N days, ending today UTC, with its notes.
func (s *Server) handleFunnel(w http.ResponseWriter, r *http.Request) {
	days := defaultFunnelDays
	if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "days must be a whole number"})
			return
		}
		days = min(max(n, 1), maxFunnelDays)
	}

	now := time.Now().UTC()
	today := core.DayOf(now)
	from := core.DayOf(now.AddDate(0, 0, -(days - 1)))
	display := s.displayCurrency()

	// Funnel spend sits in event payloads in the ad account's currency, out
	// of reach of the pipeline's ingest-time conversion (see
	// store.convertSpend), so it is converted on the way out. The converter
	// is built per request from the rate cache the running converter keeps in
	// the database, falling back to the embedded snapshot; it never touches
	// the network, and its once-per-currency warning is discarded because a
	// page reload is not news.
	conv := fx.New(fx.Options{
		Base:   display,
		Store:  s.Store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := conv.LoadCached(r.Context()); err != nil {
		s.Logger.Warn("funnel: could not read cached fx rates", "error", err)
	}

	cfg := s.Cfg.Funnel
	// Funnel numbers are one product's money and traffic, so `?app=` scopes
	// strictly, as the vault does. See internal/store/scope.go.
	rep, err := s.scoped(r).Funnel(r.Context(), from, today, store.FunnelOptions{
		DisplayCurrency: display,
		Convert:         conv.ToBase,
		ConversionLabel: cfg.ConversionLabel,
		SiteEvent:       cfg.SiteEvent,
	})
	if err != nil {
		s.fail(w, "funnel", err)
		return
	}
	rep.Notes = funnel.Notes(rep, cfg.Notes, today)
	writeJSON(w, http.StatusOK, rep)
}
