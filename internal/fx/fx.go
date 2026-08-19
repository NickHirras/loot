// Package fx converts money between currencies so the vault can show one
// number. Rates come from Frankfurter (the ECB reference rates, no API key),
// are cached in SQLite, and fall back to a snapshot embedded in the binary so
// an offline install still adds up.
//
// Rates are always quoted the ECB way: rate[X] is how many units of X buy one
// unit of the base. Converting therefore divides by the source rate and
// multiplies by the target rate, with the base as the pivot.
package fx

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is Frankfurter's public API. GET /latest?from=USD returns
// {"amount":1.0,"base":"USD","date":"2026-08-18","rates":{"EUR":0.86,...}}.
const DefaultBaseURL = "https://api.frankfurter.app"

// RefreshInterval is how often live rates are re-fetched. ECB publishes once a
// working day, so twice a day is already generous.
const RefreshInterval = 12 * time.Hour

//go:embed fallback.json
var fallbackJSON []byte

// table is one set of rates relative to Base.
type table struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

// rate returns how many units of cur buy one unit of t.Base.
func (t table) rate(cur string) (float64, bool) {
	if t.Rates == nil {
		return 0, false
	}
	if strings.EqualFold(cur, t.Base) {
		return 1, true
	}
	r, ok := t.Rates[strings.ToUpper(cur)]
	if !ok || r == 0 {
		return 0, false
	}
	return r, true
}

// RateStore persists the last good rate table. *store.Store implements it.
type RateStore interface {
	PutFXRates(ctx context.Context, base string, rates map[string]float64, asOf string) error
	GetFXRates(ctx context.Context, base string) (map[string]float64, string, error)
}

// Options configures a Converter.
type Options struct {
	// Base is the display currency every amount is converted into.
	Base string
	// Enabled false pins the converter to the embedded snapshot and never
	// touches the network.
	Enabled bool
	// Store caches fetched rates across restarts. Optional.
	Store RateStore
	// BaseURL overrides the Frankfurter endpoint (used by tests).
	BaseURL string
	// HTTPClient overrides the default 15 second client.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Converter converts amounts between currencies. It is safe for concurrent use.
type Converter struct {
	base     string
	enabled  bool
	store    RateStore
	baseURL  string
	client   *http.Client
	log      *slog.Logger
	fallback table

	mu     sync.RWMutex
	live   table
	warned map[string]bool
}

// New returns a Converter seeded with the embedded fallback snapshot.
func New(opts Options) *Converter {
	base := strings.ToUpper(strings.TrimSpace(opts.Base))
	if base == "" {
		base = "USD"
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	c := &Converter{
		base:    base,
		enabled: opts.Enabled,
		store:   opts.Store,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  client,
		log:     log,
		warned:  map[string]bool{},
	}
	if err := json.Unmarshal(fallbackJSON, &c.fallback); err != nil {
		// The snapshot is embedded and tested; a failure here means the file
		// was corrupted at build time, and every conversion will simply miss.
		log.Error("fx fallback snapshot is unreadable", "error", err)
	}
	return c
}

// Base returns the display currency.
func (c *Converter) Base() string { return c.base }

// AsOf returns the publication date of the rates currently in use, and whether
// they came from the live feed (false means the embedded snapshot).
func (c *Converter) AsOf() (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.live.Rates != nil {
		return c.live.Date, true
	}
	return c.fallback.Date, false
}

// Convert converts amount from one currency to another. It returns false when
// either currency has no known rate, so callers can tell "nothing to convert"
// from "converted to zero". Identical currencies convert without any rate at
// all, which is why a USD-only install works with FX disabled.
func (c *Converter) Convert(amount float64, from, to string) (float64, bool) {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if from == "" || to == "" {
		return 0, false
	}
	if from == to {
		return amount, true
	}
	if amount == 0 {
		return 0, true
	}

	rf, ok := c.rate(from)
	if !ok {
		c.warn(from)
		return 0, false
	}
	rt, ok := c.rate(to)
	if !ok {
		c.warn(to)
		return 0, false
	}
	return amount / rf * rt, true
}

// ToBase converts amount into the display currency.
func (c *Converter) ToBase(amount float64, currency string) (float64, bool) {
	return c.Convert(amount, currency, c.base)
}

// rate returns how many units of cur buy one unit of the display currency,
// preferring live rates and falling back to the embedded snapshot. When the
// snapshot is quoted in a different base (it ships in USD), the cross rate is
// derived from it rather than giving up.
func (c *Converter) rate(cur string) (float64, bool) {
	if strings.EqualFold(cur, c.base) {
		return 1, true
	}

	c.mu.RLock()
	live := c.live
	c.mu.RUnlock()
	if r, ok := live.rate(cur); ok && strings.EqualFold(live.Base, c.base) {
		return r, true
	}

	fb := c.fallback
	if strings.EqualFold(fb.Base, c.base) {
		return fb.rate(cur)
	}
	// Cross rate: (units of cur per snapshot base) / (units of display base per
	// snapshot base).
	rCur, okCur := fb.rate(cur)
	rBase, okBase := fb.rate(c.base)
	if !okCur || !okBase || rBase == 0 {
		return 0, false
	}
	return rCur / rBase, true
}

func (c *Converter) warn(cur string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warned[cur] {
		return
	}
	c.warned[cur] = true
	c.log.Warn("no exchange rate for currency; amounts in it will not reach the vault",
		"currency", cur, "display_currency", c.base)
}

// LoadCached seeds the converter from the rate cache, so a restart without
// network still uses the last good rates rather than the build-time snapshot.
func (c *Converter) LoadCached(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	rates, asOf, err := c.store.GetFXRates(ctx, c.base)
	if err != nil {
		return err
	}
	if len(rates) == 0 {
		return nil
	}
	c.mu.Lock()
	c.live = table{Base: c.base, Date: asOf, Rates: rates}
	c.mu.Unlock()
	c.log.Debug("fx rates loaded from cache", "base", c.base, "as_of", asOf, "count", len(rates))
	return nil
}

// Refresh fetches the current rates. It is a no-op when FX is disabled.
func (c *Converter) Refresh(ctx context.Context) error {
	if !c.enabled {
		return nil
	}

	url := c.baseURL + "/latest?from=" + c.base
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("fx request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fx fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("fx read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fx fetch: unexpected status %s", resp.Status)
	}

	var t table
	if err := json.Unmarshal(body, &t); err != nil {
		return fmt.Errorf("fx decode: %w", err)
	}
	if len(t.Rates) == 0 {
		return fmt.Errorf("fx fetch: no rates for %s", c.base)
	}
	if t.Base == "" {
		t.Base = c.base
	}

	c.mu.Lock()
	c.live = t
	// A currency that was unknown before may be present now; let it be
	// reported again if it is still missing.
	c.warned = map[string]bool{}
	c.mu.Unlock()

	if c.store != nil {
		if err := c.store.PutFXRates(ctx, t.Base, t.Rates, t.Date); err != nil {
			c.log.Warn("could not cache fx rates", "error", err)
		}
	}
	c.log.Info("fx rates updated", "base", t.Base, "as_of", t.Date, "currencies", len(t.Rates))
	return nil
}

// Run refreshes rates immediately and then every RefreshInterval until ctx is
// cancelled. A failed refresh keeps the previous rates and retries on the next
// tick: stale rates beat no rates.
func (c *Converter) Run(ctx context.Context) {
	if !c.enabled {
		c.log.Info("fx disabled; using the embedded rate snapshot",
			"base", c.base, "as_of", c.fallback.Date)
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()

	for {
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("fx refresh failed; keeping previous rates", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
