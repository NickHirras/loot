package fx_test

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickhirras/loot/internal/fx"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.005 {
		t.Fatalf("got %v, want ~%v", got, want)
	}
}

func TestSameCurrencyNeedsNoRates(t *testing.T) {
	c := fx.New(fx.Options{Base: "USD", Enabled: false, Logger: quiet()})

	got, ok := c.Convert(12.34, "USD", "USD")
	if !ok || got != 12.34 {
		t.Fatalf("same-currency convert = %v, %v", got, ok)
	}
	// Case and whitespace should not matter; a source may report "usd".
	if got, ok := c.Convert(5, " usd ", "USD"); !ok || got != 5 {
		t.Fatalf("sloppy currency convert = %v, %v", got, ok)
	}
}

func TestFallbackSnapshotConverts(t *testing.T) {
	c := fx.New(fx.Options{Base: "USD", Enabled: false, Logger: quiet()})

	// The embedded snapshot is quoted in USD, so EUR -> USD is a division.
	eur, ok := c.Convert(100, "EUR", "USD")
	if !ok {
		t.Fatal("EUR is missing from the embedded snapshot")
	}
	if eur <= 100 {
		t.Fatalf("100 EUR = %v USD, want more than 100", eur)
	}

	// Round tripping must come back where it started.
	back, ok := c.Convert(eur, "USD", "EUR")
	if !ok {
		t.Fatal("USD -> EUR failed")
	}
	near(t, back, 100)

	asOf, live := c.AsOf()
	if live {
		t.Fatal("AsOf reported live rates with fx disabled")
	}
	if asOf == "" {
		t.Fatal("the embedded snapshot has no date")
	}
}

// TestCrossRateFromSnapshot covers a display currency the snapshot is not
// quoted in: the rate has to be derived, not looked up.
func TestCrossRateFromSnapshot(t *testing.T) {
	c := fx.New(fx.Options{Base: "EUR", Enabled: false, Logger: quiet()})

	if got, ok := c.ToBase(10, "EUR"); !ok || got != 10 {
		t.Fatalf("EUR -> EUR = %v, %v", got, ok)
	}

	gbp, ok := c.ToBase(100, "GBP")
	if !ok {
		t.Fatal("GBP -> EUR failed")
	}
	if gbp <= 100 {
		t.Fatalf("100 GBP = %v EUR, want more than 100", gbp)
	}

	// USD is the snapshot's own base and must still work as a quote currency.
	usd, ok := c.ToBase(100, "USD")
	if !ok || usd <= 0 {
		t.Fatalf("100 USD = %v EUR (%v)", usd, ok)
	}
}

func TestUnknownCurrency(t *testing.T) {
	c := fx.New(fx.Options{Base: "USD", Enabled: false, Logger: quiet()})

	got, ok := c.Convert(10, "XYZ", "USD")
	if ok || got != 0 {
		t.Fatalf("unknown currency = %v, %v; want 0, false", got, ok)
	}
	// Repeated misses stay quiet but keep answering the same way.
	if _, ok := c.Convert(10, "XYZ", "USD"); ok {
		t.Fatal("unknown currency became known")
	}
	if _, ok := c.Convert(10, "", "USD"); ok {
		t.Fatal("an empty currency must not convert")
	}
}

func TestRefreshUsesLiveRatesAndCaches(t *testing.T) {
	ctx := context.Background()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amount":1.0,"base":"USD","date":"2026-08-18","rates":{"EUR":0.5,"XYZ":2}}`))
	}))
	defer srv.Close()

	cache := &memoryStore{}
	c := fx.New(fx.Options{
		Base: "USD", Enabled: true, BaseURL: srv.URL, Store: cache, Logger: quiet(),
	})
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if gotPath != "/latest?from=USD" {
		t.Fatalf("requested %q", gotPath)
	}

	// Live rates win over the embedded snapshot.
	got, ok := c.Convert(10, "EUR", "USD")
	if !ok || got != 20 {
		t.Fatalf("10 EUR = %v USD (%v), want 20 at the live rate", got, ok)
	}
	// A currency the snapshot has never heard of is now convertible.
	if got, ok := c.Convert(10, "XYZ", "USD"); !ok || got != 5 {
		t.Fatalf("10 XYZ = %v USD (%v), want 5", got, ok)
	}

	if asOf, live := c.AsOf(); !live || asOf != "2026-08-18" {
		t.Fatalf("AsOf = %q, live=%v", asOf, live)
	}
	if cache.base != "USD" || cache.rates["EUR"] != 0.5 || cache.asOf != "2026-08-18" {
		t.Fatalf("rates were not cached: %+v", cache)
	}

	// A fresh converter with no network reads the cache instead.
	offline := fx.New(fx.Options{Base: "USD", Enabled: false, Store: cache, Logger: quiet()})
	if err := offline.LoadCached(ctx); err != nil {
		t.Fatalf("load cached: %v", err)
	}
	if got, ok := offline.Convert(10, "EUR", "USD"); !ok || got != 20 {
		t.Fatalf("cached convert = %v (%v), want 20", got, ok)
	}
}

func TestRefreshDisabledDoesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a disabled converter called the network")
	}))
	defer srv.Close()

	c := fx.New(fx.Options{Base: "USD", Enabled: false, BaseURL: srv.URL, Logger: quiet()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

func TestRefreshErrorKeepsPreviousRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := fx.New(fx.Options{Base: "USD", Enabled: true, BaseURL: srv.URL, Logger: quiet()})
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("a 500 was not reported as an error")
	}
	// The snapshot still answers.
	if _, ok := c.Convert(10, "EUR", "USD"); !ok {
		t.Fatal("a failed refresh lost the embedded snapshot")
	}
}

// memoryStore is a RateStore that keeps one table in memory.
type memoryStore struct {
	base  string
	rates map[string]float64
	asOf  string
}

func (m *memoryStore) PutFXRates(_ context.Context, base string, rates map[string]float64, asOf string) error {
	m.base, m.rates, m.asOf = base, rates, asOf
	return nil
}

func (m *memoryStore) GetFXRates(_ context.Context, base string) (map[string]float64, string, error) {
	if m.base != base {
		return nil, "", nil
	}
	return m.rates, m.asOf, nil
}
