// Package server exposes Loot over HTTP: the JSON API, the websocket drop
// stream, source webhooks, and the embedded single-page app.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/store"
)

// Server holds everything the HTTP layer needs.
type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Bus      *bus.Bus
	Pipeline *pipeline.Pipeline
	Sources  []core.Source
	Static   fs.FS
	Logger   *slog.Logger

	// hearth memoizes the globe aggregate; see hearth.go.
	hearth hearthCache
}

// New returns a server.
func New(cfg config.Config, st *store.Store, b *bus.Bus, p *pipeline.Pipeline, sources []core.Source, static fs.FS, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{Cfg: cfg, Store: st, Bus: b, Pipeline: p, Sources: sources, Static: static, Logger: log}
}

// Handler builds the router. Routing uses net/http's Go 1.22 method+pattern
// matching, so Loot needs no third-party router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/healthz", s.handleHealth)
	mux.HandleFunc("GET /api/drops", s.handleDrops)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.HandleFunc("GET /api/vault/summary", s.handleVaultSummary)
	mux.HandleFunc("GET /api/hearth", s.handleHearth)
	mux.HandleFunc("GET /api/chest", s.handleChest)
	mux.HandleFunc("POST /api/chest/open", s.handleChestOpen)
	mux.HandleFunc("GET /ws", s.handleWS)
	// Method-scoped so it does not collide with the catch-all SPA route below.
	mux.HandleFunc("POST /hooks/{source}", s.handleWebhook)

	if s.Cfg.Dev.Enabled {
		mux.HandleFunc("POST /api/dev/fake", s.handleDevFake)
	}

	mux.Handle("GET /", s.spaHandler())

	return logRequests(s.Logger, mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func (s *Server) handleDrops(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	before := r.URL.Query().Get("before")

	// Drops waiting in an unopened chest are deliberately absent: the feed is
	// the "already happened" list, and a chest has not happened yet.
	drops, err := s.Store.ListDrops(r.Context(), store.DropQuery{Limit: limit, Before: before})
	if err != nil {
		s.fail(w, "list drops", err)
		return
	}

	next := ""
	if len(drops) == limit && len(drops) > 0 {
		next = drops[len(drops)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"drops": drops, "next_before": next})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Stats(r.Context())
	if err != nil {
		s.fail(w, "stats", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_drops":      st.TotalDrops,
		"total_events":     st.TotalEvents,
		"total_xp":         st.TotalXP,
		"by_rarity":        st.ByRarity,
		"by_source":        st.BySource,
		"countries":        st.Countries,
		"countries_count":  st.CountriesCount,
		"unrevealed_count": st.UnrevealedCount,
		"chest_dates":      st.ChestDates,
		"display_currency": s.displayCurrency(),
		"dev":              s.Cfg.Dev.Enabled,
		// demo tells the dashboard to wear its "synthetic data" pill, so
		// nobody mistakes a demo for their own numbers.
		"demo":      s.Cfg.Demo.Enabled,
		"listeners": s.Bus.Subscribers(),
	})
}

// sourceInfo is one row of GET /api/sources.
type sourceInfo struct {
	Name         string     `json:"name"`
	Mode         string     `json:"mode"` // "poll" or "webhook"
	PollInterval string     `json:"poll_interval,omitempty"`
	LastPollAt   *time.Time `json:"last_poll_at"`
	LastError    string     `json:"last_error"`
	Events       int        `json:"events"`
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	states, err := s.Store.SourceStates(r.Context())
	if err != nil {
		s.fail(w, "source states", err)
		return
	}

	out := make([]sourceInfo, 0, len(s.Sources))
	for _, src := range s.Sources {
		info := sourceInfo{Name: src.Name(), Mode: "webhook"}
		if d := src.PollInterval(); d > 0 {
			info.Mode = "poll"
			info.PollInterval = d.String()
		}
		if st, ok := states[src.Name()]; ok {
			info.LastPollAt = st.LastPollAt
			info.LastError = st.LastError
		}
		if n, err := s.Store.EventCount(r.Context(), src.Name()); err == nil {
			info.Events = n
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}

// handleWebhook dispatches POST /hooks/{source} to the matching source's
// WebhookHandler, handing it an emit func wired to the same pipeline that
// polled events go through.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("source")

	var handler core.WebhookHandler
	for _, src := range s.Sources {
		if !strings.EqualFold(src.Name(), name) {
			continue
		}
		h, ok := src.(core.WebhookHandler)
		if !ok {
			http.Error(w, "source does not accept webhooks", http.StatusNotFound)
			return
		}
		handler = h
		break
	}
	if handler == nil {
		http.Error(w, "unknown source", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	emit := func(ev core.Event) {
		if _, err := s.Pipeline.Ingest(ctx, ev); err != nil {
			s.Logger.Error("webhook ingest failed", "source", name, "error", err)
		}
	}
	handler.HandleWebhook(w, r, emit)
}

// devFakeRequest is the body of POST /api/dev/fake.
type devFakeRequest struct {
	// Rarity picks the synthetic drop's rarity; the default rules map
	// source=dev + kind=<rarity> straight onto it.
	Rarity string `json:"rarity"`
	// Kind overrides the event kind. "sales_day" builds a realistic ledger
	// summary event instead, so the chest and the vault can be exercised
	// without an App Store account.
	Kind     string  `json:"kind"`
	App      string  `json:"app"`
	Country  string  `json:"country"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Quantity int     `json:"quantity"`
	// Day overrides the business day (YYYY-MM-DD), which is also the chest the
	// drop lands in.
	Day string `json:"day"`
	// Chest asks for the drop to be held for the daily chest.
	Chest bool `json:"chest"`
	// Silent stores the event without minting any drop at all.
	Silent bool `json:"silent"`
}

// handleDevFake emits a synthetic event whose kind is the requested rarity.
// The default rules map source=dev + kind=<rarity> straight onto that rarity,
// so fake drops travel the real pipeline instead of bypassing it. Pass
// kind=sales_day for a ledger-shaped day summary, and chest=true to have the
// drop held for a daily chest.
func (s *Server) handleDevFake(w http.ResponseWriter, r *http.Request) {
	var req devFakeRequest
	if r.Body != nil {
		// An empty body is fine; query params can carry everything instead.
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	}
	q := r.URL.Query()
	if v := q.Get("rarity"); v != "" {
		req.Rarity = v
	}
	if v := q.Get("kind"); v != "" {
		req.Kind = v
	}
	if v := q.Get("country"); v != "" {
		req.Country = v
	}
	if v := q.Get("currency"); v != "" {
		req.Currency = v
	}
	if v := q.Get("day"); v != "" {
		req.Day = v
	}
	if v := q.Get("amount"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			req.Amount = f
		}
	}
	if v := q.Get("quantity"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Quantity = n
		}
	}
	if v := q.Get("chest"); v != "" {
		req.Chest = truthy(v)
	}
	if v := q.Get("silent"); v != "" {
		req.Silent = truthy(v)
	}

	rarity := core.Rarity(strings.ToLower(strings.TrimSpace(req.Rarity)))
	if rarity == "" {
		rarity = core.Common
	}
	if !rarity.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown rarity " + string(rarity)})
		return
	}

	app := req.App
	if app == "" {
		app = "com.example.loot"
	}
	if req.Day != "" {
		if _, err := time.Parse(core.DayLayout, req.Day); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "day must be YYYY-MM-DD"})
			return
		}
	}
	now := time.Now().UTC()

	ev := core.Event{
		ID:         core.NewID(),
		Source:     "dev",
		Kind:       string(rarity),
		App:        app,
		OccurredAt: now,
		ObservedAt: now,
		Day:        req.Day,
		Country:    strings.ToUpper(strings.TrimSpace(req.Country)),
		Amount:     req.Amount,
		Currency:   strings.ToUpper(strings.TrimSpace(req.Currency)),
		Quantity:   req.Quantity,
		// Unique per request: firing the same button twice should produce two
		// drops, unlike a real webhook retry.
		DedupeKey: "dev:" + core.NewID(),
		IsLedger:  false,
		Silent:    req.Silent,
		Chest:     req.Chest,
		Payload:   json.RawMessage(`{"synthetic":true}`),
	}

	isSalesDay := strings.EqualFold(strings.TrimSpace(req.Kind), "sales_day")
	if isSalesDay {
		devSalesDay(&ev, req)
	} else if k := strings.TrimSpace(req.Kind); k != "" {
		ev.Kind = k
	}

	// Normalizing before ingest means the echoed event shows the business day
	// and base amount that were actually stored.
	s.Pipeline.Normalize(&ev)

	if isSalesDay {
		// Imitate a real ledger source properly: the silent detail row is what
		// the vault sums, and the summary is what the chest shows.
		row := ev
		row.ID = core.NewID()
		row.Kind = "sale"
		row.IsLedger = true
		row.Silent = true
		row.Chest = false
		row.DedupeKey = "dev:row:" + core.NewID()
		if _, err := s.Pipeline.Ingest(r.Context(), row); err != nil {
			s.fail(w, "dev fake row", err)
			return
		}
	}

	drop, err := s.Pipeline.Ingest(r.Context(), ev)
	if err != nil {
		s.fail(w, "dev fake", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drop": drop, "event": ev})
}

// devSalesDay reshapes a fake event into what a ledger source would emit for a
// completed sales day: a non-silent, chest-bound summary with a real
// core.SalesDaySummary payload.
func devSalesDay(ev *core.Event, req devFakeRequest) {
	ev.Kind = "sales_day"
	ev.IsLedger = true
	ev.Source = "dev"
	if !req.Silent {
		ev.Chest = true
	}
	if ev.Quantity == 0 {
		ev.Quantity = 42
	}
	if ev.Amount == 0 {
		ev.Amount = 128.5
	}
	if ev.Currency == "" {
		ev.Currency = "USD"
	}

	summary := core.SalesDaySummary{
		Units:      ev.Quantity,
		Refunds:    ev.Quantity / 20,
		Proceeds:   ev.Amount,
		Currency:   ev.Currency,
		Countries:  3,
		TopCountry: ev.Country,
		BySKU: map[string]core.SKUTotals{
			"pro_monthly": {Units: ev.Quantity, Proceeds: ev.Amount},
		},
	}
	if summary.TopCountry == "" {
		summary.TopCountry = "US"
	}
	if payload, err := json.Marshal(summary); err == nil {
		ev.Payload = payload
	}
}

// truthy parses the loose booleans that query strings carry.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true
	}
	return false
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.Logger.Error(what+" failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": what + " failed"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		slog.Debug("write json", "error", err)
	}
}

// logRequests logs non-static requests at debug level and errors at warn.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if rec.status >= 400 {
			log.Warn("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"dur", time.Since(start).String())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/hooks/") {
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"dur", time.Since(start).String())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController (used by the websocket upgrade) reach the
// underlying writer through the wrapper.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ListenAndServe runs the HTTP server until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: the websocket stream is long-lived.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.Logger.Info("loot listening", "addr", s.Cfg.Listen, "dev", s.Cfg.Dev.Enabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
