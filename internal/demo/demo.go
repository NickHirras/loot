// Package demo is Loot with the lights on and nobody home: a self-contained
// world of three fictional apps, four months of plausible history and a live
// emitter that keeps dropping loot while you watch.
//
// Demo mode exists so that "what does this actually look like?" costs one
// command rather than an App Store Connect key. Three rules make it safe:
//
//   - it writes to its own database (<data_dir>/demo.db) and never opens the
//     real one, so nothing it does can be confused with your data;
//   - it registers no real sources, so it never polls a store or accepts a
//     webhook — every event it stores it invented;
//   - it goes through the real pipeline, so what you see is genuinely Loot:
//     the same dedupe, the same rarity rules, the same chests, the same XP.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

// stateKey is the source_state row demo mode keeps its bookkeeping in. It is
// not a source, and never appears in /api/sources.
const stateKey = "demo"

// stateVersion lets a future change to the generated world invalidate an old
// demo.db instead of half-updating it.
const stateVersion = 1

// state is what demo mode remembers between runs.
type state struct {
	Version int `json:"version"`
	// Seed is the seed the stored history was generated with. It wins over a
	// changed config, because half a world from one seed and half from
	// another is not a world.
	Seed int64 `json:"seed"`
	// Start is the first day of the seeded window, kept so a catch-up run
	// years later still measures growth from the same origin.
	Start string `json:"start"`
	// LastDay is the most recent day that has been generated.
	LastDay string `json:"last_day"`
}

// Options tunes the demo world.
type Options struct {
	// Seed fixes the random number generator: the same seed always produces
	// the same history, which is what makes screenshots reproducible.
	Seed int64
	// Pace multiplies the live emitter's speed. 1 is real time, 5 is five
	// times faster.
	Pace float64
	// Days is how much history the first run seeds.
	Days int
	// Now is the clock, swappable in tests.
	Now func() time.Time
}

// Demo owns the seeded history and the live emitter.
type Demo struct {
	store *store.Store
	// live is the pipeline the emitter publishes through: the process's real
	// one, bus and all, so a live drop reaches every browser.
	live *pipeline.Pipeline
	// rulesPath is re-loaded for each transaction, because a rules engine
	// holds a store handle and the seeding transaction needs one bound to
	// itself. See store.WithTx.
	rulesPath string
	log       *slog.Logger
	opts      Options
}

// New returns a Demo that seeds into st and emits through live.
func New(st *store.Store, live *pipeline.Pipeline, rulesPath string, opts Options, log *slog.Logger) *Demo {
	if log == nil {
		log = slog.Default()
	}
	if opts.Pace <= 0 {
		opts.Pace = 1
	}
	if opts.Days <= 0 {
		opts.Days = 120
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Demo{store: st, live: live, rulesPath: rulesPath, log: log, opts: opts}
}

func (d *Demo) now() time.Time { return d.opts.Now().UTC() }

// Result reports what a Seed call did.
type Result struct {
	// Generated is how many days of history this call invented: the whole
	// window on a first run, the days missed since the last run afterwards,
	// and zero when the database is already up to date.
	Generated int
	// From and To are the first and last day generated ("" when none were).
	From, To string
	// Events and Drops are how much the database grew.
	Events, Drops int
	// Countries is how many countries the demo world has now sold in.
	Countries int
	// XP is the total XP visible after seeding.
	XP   int
	Took time.Duration
}

// Seed brings the demo database up to date: the whole window on a first run,
// or just the days that have passed since the last one. It is idempotent —
// every generated event carries a stable dedupe key, so running it twice
// stores nothing the second time.
//
// History stops at yesterday. Every chest older than yesterday is opened
// (quietly, without a cascade, so the feed reads as a rich past rather than an
// avalanche of sound), and yesterday's is deliberately left shut: opening a
// chest is the best thing Loot does, and a new demo should have one waiting.
func (d *Demo) Seed(ctx context.Context) (Result, error) {
	started := time.Now()
	var res Result

	prev, err := d.loadState(ctx)
	if err != nil {
		return res, err
	}

	now := d.now()
	yesterday := now.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	next := state{Version: stateVersion, Seed: d.opts.Seed}

	switch {
	case prev.Version == 0:
		next.Start = core.DayOf(yesterday.AddDate(0, 0, -(d.opts.Days - 1)))
	default:
		next = prev
		if prev.Seed != d.opts.Seed {
			d.log.Warn("demo.seed differs from the seed this demo.db was built with; keeping the stored one",
				"stored", prev.Seed, "configured", d.opts.Seed, "hint", "run `loot demo reset` to start over")
		}
	}

	start, err := time.Parse(core.DayLayout, next.Start)
	if err != nil {
		return res, fmt.Errorf("demo: bad stored start day %q: %w", next.Start, err)
	}

	days := plannedDays(start, yesterday, prev.LastDay)
	if len(days) == 0 {
		// The rollover ticker asks this question every minute; only the answer
		// "yes, a day has passed" is worth saying out loud.
		d.log.Debug("demo history already up to date", "through", prev.LastDay)
		return res, nil
	}

	beforeEvents, err := d.store.EventCount(ctx, "")
	if err != nil {
		return res, err
	}

	// Everything below runs inside one transaction: tens of thousands of rows
	// committed one at a time would take the better part of a minute, and a
	// half-seeded world is worse than none.
	err = d.store.WithTx(ctx, func(tx *store.Store) error {
		pipe, err := d.txPipeline(tx)
		if err != nil {
			return err
		}
		// The window is always measured from the stored start day, so a
		// catch-up run places its days where they really fall in the history
		// rather than at the beginning of a one-day window.
		span := int(yesterday.Sub(start).Hours()/24+0.5) + 1
		for _, day := range days {
			index := int(day.Sub(start).Hours()/24 + 0.5)
			if err := d.generateDay(ctx, pipe, dayPlan{
				date:  core.DayOf(day),
				day:   day,
				index: index,
				span:  span,
				rng:   d.rngFor(core.DayOf(day)),
			}); err != nil {
				return err
			}
		}
		return d.revealPast(ctx, tx, core.DayOf(yesterday))
	})
	if err != nil {
		return res, err
	}

	next.LastDay = core.DayOf(days[len(days)-1])
	if err := d.saveState(ctx, next); err != nil {
		return res, err
	}

	afterEvents, err := d.store.EventCount(ctx, "")
	if err != nil {
		return res, err
	}
	stats, err := d.store.Stats(ctx)
	if err != nil {
		return res, err
	}

	res = Result{
		Generated: len(days),
		From:      core.DayOf(days[0]),
		To:        core.DayOf(days[len(days)-1]),
		Events:    afterEvents - beforeEvents,
		Drops:     stats.TotalDrops,
		Countries: stats.CountriesCount,
		XP:        stats.TotalXP,
		Took:      time.Since(started),
	}
	return res, nil
}

// plannedDays lists the days that still need generating: everything from start
// (or the day after last, when there is one) through end, inclusive.
func plannedDays(start, end time.Time, last string) []time.Time {
	from := start
	if last != "" {
		if t, err := time.Parse(core.DayLayout, last); err == nil && !t.Before(from) {
			from = t.AddDate(0, 0, 1)
		}
	}
	var out []time.Time
	for day := from; !day.After(end); day = day.AddDate(0, 0, 1) {
		out = append(out, day)
	}
	return out
}

// txPipeline builds a pipeline that writes through tx. It copies the live
// pipeline so currency, FX and chest settings match exactly, then swaps in the
// transaction's store, a rules engine bound to the same transaction, and no
// bus: a backfill has nobody to tell.
func (d *Demo) txPipeline(tx *store.Store) (*pipeline.Pipeline, error) {
	engine, err := rules.Load(d.rulesPath, tx)
	if err != nil {
		return nil, err
	}
	pipe := *d.live
	pipe.Store = tx
	pipe.Rules = engine
	pipe.Bus = nil
	pipe.Logger = slog.New(slog.DiscardHandler)
	// Chests are the point of the seeded history, and backdating is what makes
	// the feed read as a timeline instead of as one very busy minute.
	pipe.ChestEnabled = true
	pipe.Backdate = true
	engine.SetDisplayCurrency(pipe.DisplayCurrency)
	return &pipe, nil
}

// ingest pushes one generated event through the pipeline.
func (d *Demo) ingest(ctx context.Context, pipe *pipeline.Pipeline, ev core.Event) error {
	if _, err := pipe.Ingest(ctx, ev); err != nil {
		return fmt.Errorf("demo: ingest %s/%s: %w", ev.Source, ev.Kind, err)
	}
	return nil
}

// revealPast opens every chest older than the given day, stamping each with
// the morning after its own day so the history reads as somebody who kept up
// with their loot. Yesterday's chest is left shut on purpose.
func (d *Demo) revealPast(ctx context.Context, tx *store.Store, keep string) error {
	chests, err := tx.ChestSummaries(ctx)
	if err != nil {
		return err
	}
	for _, c := range chests {
		if c.Date >= keep {
			continue
		}
		opened := time.Now().UTC()
		if day, err := time.Parse(core.DayLayout, c.Date); err == nil {
			opened = day.AddDate(0, 0, 1).Add(9 * time.Hour)
		}
		if _, err := tx.RevealChest(ctx, c.Date, opened); err != nil {
			return err
		}
	}
	return nil
}

// rngFor returns the generator for one day. Keying it by (seed, day) rather
// than running one stream across the window is what lets a catch-up run
// produce exactly the day a full seed would have.
func (d *Demo) rngFor(day string) *rand.Rand {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(day); i++ {
		h ^= uint64(day[i])
		h *= 1099511628211
	}
	return rand.New(rand.NewPCG(uint64(d.opts.Seed), h))
}

func (d *Demo) loadState(ctx context.Context) (state, error) {
	var st state
	raw, err := d.store.GetSourceState(ctx, stateKey)
	if err != nil {
		return st, err
	}
	if len(raw) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		// An unreadable state row means an old or corrupt demo.db; start over
		// rather than refusing to boot.
		d.log.Warn("demo state unreadable; reseeding", "error", err)
		return state{}, nil
	}
	if st.Version != stateVersion {
		d.log.Warn("demo.db was built by another version of demo mode; run `loot demo reset` for a fresh world",
			"stored", st.Version, "current", stateVersion)
	}
	return st, nil
}

func (d *Demo) saveState(ctx context.Context, st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("demo: encode state: %w", err)
	}
	return d.store.SetSourceState(ctx, stateKey, raw)
}
