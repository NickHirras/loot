package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
)

// The live emitter is what makes demo mode feel alive rather than archived: a
// steady trickle of real-time subscription events, an hourly install count,
// and — at midnight — yesterday's ledger day arriving as a fresh chest.

// Live emitter timings, before Options.Pace divides them.
const (
	// minGap and maxGap bound the wait between two real-time events. The
	// upper bound is a promise as much as a number: demo mode is never silent
	// for longer than this, whatever the hour.
	minGap = 20 * time.Second
	maxGap = 90 * time.Second
	// quietFactor is how much longer the wait gets in the small hours, and
	// quietCap is the ceiling that keeps even a quiet night inside the "never
	// silent for longer than this" promise.
	quietFactor = 1.6
	quietCap    = 150 * time.Second
	// installGap is how often a Flathub-style install count lands.
	installGap = time.Hour
	// rolloverCheck is how often the emitter looks for a day that has ended.
	rolloverCheck = time.Minute
)

// Run drives the live emitter until ctx is cancelled. It blocks, so `loot
// serve` starts it in a goroutine.
func (d *Demo) Run(ctx context.Context) {
	d.log.Info("demo emitter running",
		"pace", d.opts.Pace,
		"gap", fmt.Sprintf("%s..%s", d.scaled(minGap), d.scaled(maxGap)))

	rng := rand.New(rand.NewPCG(uint64(d.opts.Seed), uint64(d.now().UnixNano())))

	go d.runInstalls(ctx, rand.New(rand.NewPCG(uint64(d.opts.Seed)+1, uint64(d.now().UnixNano()))))
	go d.runRollover(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.nextGap(rng)):
		}
		if err := d.emitRealtime(ctx, rng); err != nil {
			d.log.Error("demo emit failed", "error", err)
		}
	}
}

// scaled applies the configured pace: at pace 5 a minute of demo time passes
// in twelve seconds.
func (d *Demo) scaled(v time.Duration) time.Duration {
	out := time.Duration(float64(v) / d.opts.Pace)
	if out < time.Millisecond {
		return time.Millisecond
	}
	return out
}

// nextGap picks how long to wait before the next real-time event. Business
// hours across the apps' biggest markets are busier than the small hours, but
// the wait is clamped so the feed never looks broken.
func (d *Demo) nextGap(rng *rand.Rand) time.Duration {
	gap := minGap + time.Duration(rng.Int64N(int64(maxGap-minGap)))
	if h := d.now().Hour(); h >= 1 && h < 7 {
		gap = time.Duration(float64(gap) * quietFactor)
	}
	if gap > quietCap {
		gap = quietCap
	}
	return d.scaled(gap)
}

// emitRealtime sends one RevenueCat-shaped event: mostly renewals and new
// subscribers, sometimes a cancellation, and just occasionally an annual plan
// from a country nobody has sold in before — which founds a settlement, and is
// the best thing that can happen to a feed while somebody is watching.
func (d *Demo) emitRealtime(ctx context.Context, rng *rand.Rand) error {
	a := apps[rng.IntN(len(apps))]
	for !a.RevenueCat {
		a = apps[rng.IntN(len(apps))]
	}

	var (
		s      = pickStorefront(rng, storefronts)
		pr     = pickSubscription(rng, a)
		rcType = "RENEWAL"
	)
	switch r := rng.Float64(); {
	case r < 0.04:
		// A brand new country, if there is one left to discover.
		if next, ok := d.frontierCountry(ctx); ok {
			s = next
			rcType = "INITIAL_PURCHASE"
			if annual, ok := annualProduct(a); ok {
				pr = annual
			}
		}
	case r < 0.12:
		rcType = "CANCELLATION"
	case r < 0.20:
		rcType = "INITIAL_PURCHASE"
		if annual, ok := annualProduct(a); ok {
			pr = annual
		}
	case r < 0.45:
		rcType = "INITIAL_PURCHASE"
	}

	ev, err := revenueCatEvent(a, pr, s, rcType, d.now(), rng)
	if err != nil {
		return err
	}
	_, err = d.live.Ingest(ctx, ev)
	return err
}

// annualProduct returns an app's annual plan, the one worth a rare drop.
func annualProduct(a app) (product, bool) {
	for _, pr := range a.Products {
		if pr.Period == "ANNUAL" {
			return pr, true
		}
	}
	return product{}, false
}

// frontierCountry returns a country the demo world has never sold in, or false
// once the whole frontier has been settled.
func (d *Demo) frontierCountry(ctx context.Context) (storefront, bool) {
	for _, s := range frontier {
		n, err := d.store.CountryEventCount(ctx, s.Code)
		if err != nil {
			d.log.Error("demo frontier lookup failed", "error", err, "country", s.Code)
			return storefront{}, false
		}
		if n == 0 {
			return s, true
		}
	}
	return storefront{}, false
}

// runInstalls drops a Flathub-shaped install count once an hour.
//
// The real Flathub source reports one completed day at a time, and a demo
// session is much shorter than a day, so these carry the hour in their dedupe
// key: the same "flathub:<app>:<day>" shape, counted hourly, so a session sees
// installs arrive instead of waiting until tomorrow for one number.
func (d *Demo) runInstalls(ctx context.Context, rng *rand.Rand) {
	ticker := time.NewTicker(d.scaled(installGap))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := d.now()
		for _, a := range apps {
			if a.Flatpak == "" || a.FlathubBase == 0 {
				continue
			}
			installs := int(a.FlathubBase/18) + rng.IntN(12)
			payload, err := json.Marshal(map[string]any{
				"app": a.Flatpak, "date": core.DayOf(now), "installs": installs, "partial": true,
			})
			if err != nil {
				d.log.Error("demo install payload", "error", err)
				continue
			}
			ev := core.Event{
				ID:         core.NewIDAt(now),
				Source:     "flathub",
				Kind:       "install",
				App:        a.Flatpak,
				OccurredAt: now,
				ObservedAt: now,
				Day:        core.DayOf(now),
				Quantity:   installs,
				DedupeKey:  fmt.Sprintf("flathub:%s:%s:%02d", a.Flatpak, core.DayOf(now), now.Hour()),
				Payload:    payload,
			}
			if _, err := d.live.Ingest(ctx, ev); err != nil {
				d.log.Error("demo install emit failed", "error", err)
			}
		}
	}
}

// runRollover watches for the day ending. When it does, yesterday's ledger day
// is generated for every app, which fills a fresh chest — the same thing that
// happens in the morning with real sources, minus the wait for the store to
// publish its report.
func (d *Demo) runRollover(ctx context.Context) {
	ticker := time.NewTicker(rolloverCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		res, err := d.Seed(ctx)
		if err != nil {
			d.log.Error("demo day rollover failed", "error", err)
			continue
		}
		if res.Generated > 0 {
			d.log.Info("demo day rolled over", "days", res.Generated, "through", res.To)
			d.publishChests(ctx)
		}
	}
}

// publishChests tells connected browsers that a new chest is waiting, which
// the seeding transaction deliberately does not do for itself.
func (d *Demo) publishChests(ctx context.Context) {
	if d.live == nil || d.live.Bus == nil {
		return
	}
	chests, err := d.store.ChestSummaries(ctx)
	if err != nil {
		d.log.Error("demo chest summaries failed", "error", err)
		return
	}
	d.live.Bus.Publish(bus.Message{Type: "chest", Chests: chests})
}

// EmitOnce sends a single real-time event immediately. It exists for tests and
// for anyone who wants to poke the demo from code rather than waiting out a
// gap.
func (d *Demo) EmitOnce(ctx context.Context) error {
	return d.emitRealtime(ctx, rand.New(rand.NewPCG(uint64(d.opts.Seed), uint64(d.now().UnixNano()))))
}

// Sources returns the source list demo mode shows in the dashboard header.
//
// They are labels, not sources: each one names itself and its poll interval so
// the header reads like a configured install, and each Poll returns nothing at
// all. Demo mode has no credentials and makes no network calls — every event
// it stores comes from the emitter above.
func Sources() []core.Source {
	return []core.Source{
		&Source{name: "appstore", interval: time.Hour},
		&Source{name: "googleplay", interval: 6 * time.Hour},
		&Source{name: "flathub", interval: time.Hour},
		&Source{name: "revenuecat"},
	}
}

// Source is an inert stand-in for a real source: it exists so demo mode's
// header, source list and health rows look like the real thing.
type Source struct {
	name     string
	interval time.Duration
}

// Name implements core.Source.
func (s *Source) Name() string { return s.name }

// PollInterval implements core.Source. Zero marks the RevenueCat stand-in as
// webhook-shaped, exactly as the real one is.
func (s *Source) PollInterval() time.Duration { return s.interval }

// Poll implements core.Source and does nothing: demo events come from the
// emitter, never from a poll.
func (s *Source) Poll(context.Context, []byte) ([]core.Event, []byte, error) {
	return nil, nil, nil
}

// Check implements core.Checker so `loot check --demo` is honest about what it
// is looking at.
func (s *Source) Check(context.Context) error { return nil }

var _ core.Source = (*Source)(nil)
