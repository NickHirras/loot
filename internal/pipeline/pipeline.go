// Package pipeline wires sources to storage: every event, whether polled or
// pushed by a webhook, travels the same path —
// dedupe -> persist -> classify -> persist drop -> publish.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

// Converter is the slice of internal/fx the pipeline needs. Keeping it an
// interface means tests can convert with three lines of arithmetic instead of
// standing up a rate feed.
type Converter interface {
	Convert(amount float64, from, to string) (float64, bool)
}

// CascadeDelay spaces the drops of an opened chest so the UI and `loot tail`
// play them as a run of sounds rather than one noise.
const CascadeDelay = 600 * time.Millisecond

// chestSweepInterval is how often the auto-open ticker looks for a chest whose
// day has gone stale.
const chestSweepInterval = 5 * time.Minute

// settlementSource and settlementKind identify the synthetic event Loot emits
// the first time a country appears. It is Loot's own event, not a source's,
// which is why it carries the reserved source name "loot".
const (
	settlementSource = "loot"
	settlementKind   = "settlement"
)

// Pipeline ingests events.
type Pipeline struct {
	Store  *store.Store
	Rules  *rules.Engine
	Bus    *bus.Bus
	Logger *slog.Logger

	// DisplayCurrency is what Event.AmountBase is denominated in.
	DisplayCurrency string
	// Products resolves each event's raw app name into the canonical product
	// it belongs to, which is what every scoped view filters on. Optional:
	// without it an app is its own product, which is exactly what an unmapped
	// app resolves to anyway.
	Products store.Resolver
	// FX converts amounts into DisplayCurrency at ingest. Optional: without
	// it, only amounts already in the display currency get a base amount.
	FX Converter

	// ChestEnabled holds chest-hinted drops back for the daily chest. When
	// false, a chest hint is ignored and the drop goes straight to the feed.
	ChestEnabled bool
	// ChestAutoOpenAfterHours opens a chest by itself once its day is that
	// many hours old. 0 waits for someone to click.
	ChestAutoOpenAfterHours int

	// Backdate mints each drop's id and created_at from the event's
	// OccurredAt rather than from the wall clock. Off by default: a live drop
	// happened now, whatever timestamp the source put on it. Backfills that
	// want the feed to read as a timeline — demo mode's seeded history — turn
	// it on, and because drop ids are ULIDs the feed then paginates in the
	// order the events actually happened.
	Backdate bool

	// AfterIngest is called once per *new* event, after its drop (if any) has
	// been published. It exists for the quest engine: a quest that crossed its
	// target should pay out while the drop that pushed it over is still on
	// screen, rather than up to a minute later.
	//
	// It must not block — the implementation is expected to nudge a goroutine
	// and return — and it is deliberately not called for duplicates or for
	// Loot's own synthetic events, which is what stops a quest completion from
	// triggering another quest check forever.
	AfterIngest func(ev core.Event)

	// Now is the clock, swappable in tests.
	Now func() time.Time

	// noCurrency remembers which sources have already been warned about money
	// with no currency on it, so the log says it once per source rather than
	// once per row. It is a pointer so the struct stays copyable — demo mode
	// copies a Pipeline to bind it to a transaction.
	noCurrency *warnedSources
}

// warnedSources is a set of source names that have already produced a given
// warning. It is safe for concurrent use: webhooks and polls ingest at once.
type warnedSources struct {
	mu   sync.Mutex
	seen map[string]bool
}

// first reports whether this is the first time source has been seen.
func (w *warnedSources) first(source string) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen[source] {
		return false
	}
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	w.seen[source] = true
	return true
}

// New returns a pipeline with chests disabled and no currency conversion;
// `loot serve` fills those in from the config.
func New(st *store.Store, engine *rules.Engine, b *bus.Bus, log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		Store:           st,
		Rules:           engine,
		Bus:             b,
		Logger:          log,
		DisplayCurrency: "USD",
		Now:             func() time.Time { return time.Now().UTC() },
		noCurrency:      &warnedSources{},
	}
}

func (p *Pipeline) now() time.Time {
	if p.Now == nil {
		return time.Now().UTC()
	}
	return p.Now().UTC()
}

// Ingest stores ev and, if it is new, mints a drop for it. A duplicate (same
// dedupe_key) returns a nil drop and no error: the whole point of the dedupe
// key is that a webhook retry is a no-op, not an error.
//
// Three things can happen to a new event:
//
//   - Silent events (ledger detail rows) are stored and never produce a drop.
//   - Chest-hinted events produce a drop that is filed under a chest date and
//     withheld from the feed until the chest is opened.
//   - Everything else is classified and published immediately.
//
// Whatever happens, an event carrying a country never seen before also
// synthesizes a settlement event, which gets its own drop.
func (p *Pipeline) Ingest(ctx context.Context, ev core.Event) (*core.Drop, error) {
	return p.ingest(ctx, ev, 0)
}

func (p *Pipeline) ingest(ctx context.Context, ev core.Event, depth int) (*core.Drop, error) {
	p.Normalize(&ev)

	exists, err := p.Store.InsertEvent(ctx, ev)
	if err != nil {
		return nil, fmt.Errorf("ingest %s/%s: %w", ev.Source, ev.Kind, err)
	}
	if exists {
		p.Logger.Debug("duplicate event ignored", "source", ev.Source, "dedupe_key", ev.DedupeKey)
		return nil, nil
	}

	var drop *core.Drop
	if !ev.Silent {
		drop, err = p.mintDrop(ctx, ev)
		if err != nil {
			return nil, err
		}
	}

	// Settlements are worth a drop even when the event that revealed the
	// country was a silent ledger row: the country is the news, not the row.
	// Depth guards against a settlement settling itself.
	if depth == 0 {
		if err := p.settle(ctx, ev); err != nil {
			// A failed settlement must not lose the event that caused it.
			p.Logger.Error("settlement failed", "error", err, "country", ev.Country)
		}
	}
	if p.AfterIngest != nil && ev.Source != settlementSource {
		p.AfterIngest(ev)
	}
	return drop, nil
}

// Normalize fills in everything a source is allowed to leave blank: ids,
// timestamps, the dedupe key, the business day and the base amount. Ingest
// calls it, and it is idempotent, so a caller that wants to see the event
// exactly as it will be stored may call it first.
func (p *Pipeline) Normalize(ev *core.Event) {
	if ev.ID == "" {
		ev.ID = core.NewID()
	}
	if ev.ObservedAt.IsZero() {
		ev.ObservedAt = p.now()
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = ev.ObservedAt
	}
	if ev.DedupeKey == "" {
		ev.DedupeKey = ev.Source + ":" + ev.ID
	}
	if ev.Day == "" {
		ev.Day = core.DayOf(ev.OccurredAt)
	}
	// The product is resolved at ingest so a scoped query is one indexed
	// column rather than a mapping applied per row at read time. It is
	// recomputed for the whole table by store.RemapProducts whenever the
	// mapping changes, so this is a cache and never the source of truth.
	//
	// An event with no app at all stays product-less on purpose: Loot's own
	// achievements are about the whole realm and appear in every scope.
	switch {
	case ev.App == "":
		ev.Product = ""
	case p.Products != nil:
		ev.Product = p.Products.Resolve(ev.Source, ev.App)
	case ev.Product == "":
		ev.Product = ev.App
	}
	if ev.Amount != 0 && ev.AmountBase == 0 {
		if ev.Currency == "" {
			// Money with no currency on it is not money in the display
			// currency, it is money of unknown denomination. Guessing meant a
			// report that omitted the column silently added its figures to the
			// vault at par — plausible, wrong, and invisible. AmountBase stays
			// zero, so the amount is stored and shown on the drop but never
			// summed into revenue, and the operator is told once.
			if p.noCurrency.first(ev.Source) {
				p.Logger.Warn("source reported an amount with no currency; it will not reach the vault",
					"source", ev.Source, "kind", ev.Kind, "dedupe_key", ev.DedupeKey)
			}
		} else if base, ok := p.toBase(ev.Amount, ev.Currency); ok {
			ev.AmountBase = base
		}
	}
}

// toBase converts into the display currency. Callers must have established
// that currency is set; see Normalize on why an empty one is not assumed to be
// the display currency.
func (p *Pipeline) toBase(amount float64, currency string) (float64, bool) {
	display := p.DisplayCurrency
	if display == "" {
		display = "USD"
	}
	if currency == "" {
		return 0, false
	}
	if strings.EqualFold(currency, display) {
		return amount, true
	}
	if p.FX == nil {
		return 0, false
	}
	return p.FX.Convert(amount, currency, display)
}

// mintDrop classifies ev, stores the drop and either publishes it or files it
// in a chest.
func (p *Pipeline) mintDrop(ctx context.Context, ev core.Event) (*core.Drop, error) {
	drop, err := p.Rules.Classify(ctx, ev)
	if err != nil {
		return nil, fmt.Errorf("classify %s/%s: %w", ev.Source, ev.Kind, err)
	}

	if p.Backdate && !ev.OccurredAt.IsZero() {
		drop.CreatedAt = ev.OccurredAt.UTC()
		drop.ID = core.NewIDAt(ev.OccurredAt)
	}

	if ev.Chest && p.ChestEnabled {
		drop.ChestDate = ev.Day
		if drop.ChestDate == "" {
			drop.ChestDate = core.DayOf(p.now())
		}
	}

	if err := p.Store.InsertDrop(ctx, drop); err != nil {
		return nil, fmt.Errorf("store drop: %w", err)
	}

	if drop.ChestDate != "" {
		p.Logger.Info("drop held for chest",
			"chest_date", drop.ChestDate, "rarity", drop.Rarity, "title", drop.Title, "source", ev.Source)
		p.publishChestState(ctx)
		return &drop, nil
	}

	if p.Bus != nil {
		evCopy := ev
		p.Bus.Publish(bus.Message{Type: "drop", Drop: &drop, Event: &evCopy})
	}
	p.Logger.Info("drop",
		"rarity", drop.Rarity, "title", drop.Title, "source", ev.Source, "xp", drop.XP)
	return &drop, nil
}

// settle emits a settlement event the first time a country is seen. The
// country count is taken after the insert, so a count of 1 means "ev was the
// first event ever from there".
func (p *Pipeline) settle(ctx context.Context, ev core.Event) error {
	country := strings.ToUpper(strings.TrimSpace(ev.Country))
	if country == "" {
		return nil
	}
	if ev.Source == settlementSource && ev.Kind == settlementKind {
		return nil
	}

	first, err := p.Store.CountryFirstEvent(ctx, country)
	if err != nil {
		return fmt.Errorf("country first event: %w", err)
	}
	if !first {
		return nil
	}

	payload, err := json.Marshal(map[string]string{"via": ev.Source})
	if err != nil {
		return fmt.Errorf("settlement payload: %w", err)
	}

	settlement := core.Event{
		Source:     settlementSource,
		Kind:       settlementKind,
		App:        ev.App,
		Country:    country,
		OccurredAt: ev.OccurredAt,
		Day:        ev.Day,
		// A settlement inherits the chest hint of the event that revealed it,
		// so a ledger backfill does not spray flags across the live feed.
		//
		// Silent counts as a chest hint too, and that is the important half:
		// the country-bearing rows of a financial report are *silent* ledger
		// detail with Chest false, so without this a 30 day App Store backfill
		// would mint forty immediate rare drops the moment it landed. A
		// settlement found in a silent row belongs in that row's own day's
		// chest. Realtime events (a RevenueCat purchase) are neither silent
		// nor chest-hinted, so they still settle live, which is the whole
		// point of watching the feed.
		Chest:     ev.Chest || ev.Silent,
		DedupeKey: settlementSource + ":" + settlementKind + ":" + country,
		Payload:   payload,
	}
	_, err = p.ingest(ctx, settlement, 1)
	return err
}

// publishChestState tells connected UIs what is waiting, so a chest badge can
// appear without polling.
func (p *Pipeline) publishChestState(ctx context.Context) {
	if p.Bus == nil {
		return
	}
	chests, err := p.Store.ChestSummaries(ctx)
	if err != nil {
		p.Logger.Error("chest summaries failed", "error", err)
		return
	}
	p.Bus.Publish(bus.Message{Type: "chest", Chests: chests})
}

// RevealChest opens the chest for date (empty means the oldest one), returns
// its drops in cascade order and publishes them on the bus, spaced by
// CascadeDelay so the reveal plays as a run of sounds. Publishing happens in
// the background: the caller gets the drops immediately.
func (p *Pipeline) RevealChest(ctx context.Context, date string) ([]store.DropView, error) {
	drops, err := p.Store.RevealChest(ctx, date, p.now())
	if err != nil {
		return nil, err
	}
	if len(drops) == 0 {
		return nil, nil
	}

	p.Logger.Info("chest opened", "chest_date", drops[0].ChestDate, "drops", len(drops))
	if p.Bus != nil {
		go p.cascade(context.WithoutCancel(ctx), drops)
	}
	p.publishChestState(ctx)
	return drops, nil
}

// cascade publishes revealed drops one at a time. Each carries chest:true so a
// client can tell a chest reveal from a live drop.
func (p *Pipeline) cascade(ctx context.Context, drops []store.DropView) {
	for i, d := range drops {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(CascadeDelay):
			}
		}
		drop := d.Drop
		ev := d.Event()
		p.Bus.Publish(bus.Message{Type: "drop", Chest: true, Drop: &drop, Event: &ev})
	}
}

// RunChestAutoOpen opens chests whose day has grown stale, until ctx is
// cancelled. It blocks. With auto-open off it simply waits, so the caller can
// start it unconditionally.
func (p *Pipeline) RunChestAutoOpen(ctx context.Context) {
	if !p.ChestEnabled || p.ChestAutoOpenAfterHours <= 0 {
		<-ctx.Done()
		return
	}

	p.Logger.Info("chest auto-open armed", "after_hours", p.ChestAutoOpenAfterHours)
	ticker := time.NewTicker(chestSweepInterval)
	defer ticker.Stop()

	for {
		p.OpenDueChests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// OpenDueChests opens every chest that is older than the configured delay,
// measured from midnight UTC of the chest's own day. Exported so tests and a
// future CLI can force a sweep.
func (p *Pipeline) OpenDueChests(ctx context.Context) {
	if p.ChestAutoOpenAfterHours <= 0 {
		return
	}
	chests, err := p.Store.ChestSummaries(ctx)
	if err != nil {
		p.Logger.Error("chest sweep failed", "error", err)
		return
	}

	deadline := time.Duration(p.ChestAutoOpenAfterHours) * time.Hour
	for _, c := range chests {
		day, err := time.Parse(core.DayLayout, c.Date)
		if err != nil {
			continue
		}
		if p.now().Sub(day.UTC()) < deadline {
			continue
		}
		if _, err := p.RevealChest(ctx, c.Date); err != nil {
			p.Logger.Error("auto-open failed", "error", err, "chest_date", c.Date)
		}
	}
}

// IngestSilently stores ev without minting a drop. Prefer setting Event.Silent,
// which travels with the event through the whole pipeline; this remains for
// callers that hold an event they did not build.
func (p *Pipeline) IngestSilently(ctx context.Context, ev core.Event) error {
	ev.Silent = true
	_, err := p.Ingest(ctx, ev)
	return err
}

// DefaultFirstRoundCap is how long FirstRoundDone waits for a slow or stuck
// source before declaring the first round over anyway. A source that is still
// downloading six months of App Store reports must not hold up the rest of
// Loot forever.
const DefaultFirstRoundCap = 10 * time.Minute

// Scheduler drives polling sources on their own intervals.
type Scheduler struct {
	Pipeline *Pipeline
	Store    *store.Store
	Sources  []core.Source
	Logger   *slog.Logger
	// FirstRoundCap overrides DefaultFirstRoundCap. Tests set it small.
	FirstRoundCap time.Duration

	// firstRound is closed once every polling source has finished its first
	// Poll (or the cap has expired). See FirstRoundDone.
	firstRound chan struct{}
	firstOnce  sync.Once
}

// NewScheduler returns a scheduler over the polling-capable sources in list.
// Webhook-only sources (PollInterval == 0) are ignored.
func NewScheduler(p *Pipeline, st *store.Store, sources []core.Source, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		Pipeline:   p,
		Store:      st,
		Sources:    sources,
		Logger:     log,
		firstRound: make(chan struct{}),
	}
}

// FirstRoundDone is closed once every polling source has completed its first
// Poll — that is, once the database holds whatever history a first run was
// going to backfill.
//
// It exists because of one specific, embarrassing bug: quests were generated
// at startup, before the sources had said anything, so the generator saw an
// empty database, and the App Store backfill that landed forty seconds later
// completed "Settle 2 new countries" instantly — with an epic drop, a sound
// and XP for countries that were settled months ago. A quest has to be set
// against what you already have, and at startup Loot does not yet know what
// that is.
//
// A webhook-only source has no first poll and is not waited for: it will speak
// when something happens, which may be next week. A polling source that hangs
// is waited for no longer than FirstRoundCap.
func (s *Scheduler) FirstRoundDone() <-chan struct{} { return s.firstRound }

// closeFirstRound is idempotent: the cap and the last source race, and either
// may win.
func (s *Scheduler) closeFirstRound() {
	s.firstOnce.Do(func() { close(s.firstRound) })
}

// Run polls every polling source until ctx is cancelled. It blocks.
func (s *Scheduler) Run(ctx context.Context) {
	done := make(chan struct{})
	started := 0

	var first sync.WaitGroup
	for _, src := range s.Sources {
		interval := src.PollInterval()
		if interval <= 0 {
			continue
		}
		started++
		first.Add(1)
		go func(src core.Source, interval time.Duration) {
			defer func() { done <- struct{}{} }()
			s.runSource(ctx, src, interval, &first)
		}(src, interval)
	}

	if started == 0 {
		// Nothing polls, so the first round is over before it began: a
		// webhook-only Loot (or demo mode) must not wait ten minutes for a
		// quest board.
		s.closeFirstRound()
		<-ctx.Done()
		return
	}

	go func() {
		first.Wait()
		s.Logger.Debug("first poll round complete", "sources", started)
		s.closeFirstRound()
	}()
	limit := s.FirstRoundCap
	if limit <= 0 {
		limit = DefaultFirstRoundCap
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.firstRound:
		case <-time.After(limit):
			s.Logger.Warn("first poll round timed out; carrying on", "after", limit.String())
			s.closeFirstRound()
		}
	}()

	for i := 0; i < started; i++ {
		<-done
	}
}

// runSource polls src forever. first is released after the very first poll,
// whether it succeeded or not: a source that is broken is not a source Loot
// should wait on, it is one that has already told us all it is going to.
func (s *Scheduler) runSource(ctx context.Context, src core.Source, interval time.Duration, first *sync.WaitGroup) {
	log := s.Logger.With("source", src.Name())
	log.Info("polling source started", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	firstDone := false
	for {
		s.PollOnce(ctx, src)
		if !firstDone {
			firstDone = true
			if first != nil {
				first.Done()
			}
		}
		select {
		case <-ctx.Done():
			log.Info("polling source stopped")
			return
		case <-ticker.C:
		}
	}
}

// PollOnce runs a single poll cycle for src: load cursor, poll, ingest each
// event, persist the new cursor and record health. Exported so `loot serve`
// can force an immediate poll and so tests can drive a cycle directly.
func (s *Scheduler) PollOnce(ctx context.Context, src core.Source) {
	log := s.Logger.With("source", src.Name())
	now := time.Now().UTC()

	prev, err := s.Store.GetSourceState(ctx, src.Name())
	if err != nil {
		log.Error("load source state", "error", err)
		_ = s.Store.RecordPoll(ctx, src.Name(), now, err)
		return
	}

	events, newState, pollErr := src.Poll(ctx, prev)

	// A source may return events *and* an error (e.g. one app of several
	// failed); ingest what we got before reporting the failure.
	for _, ev := range events {
		if _, err := s.Pipeline.Ingest(ctx, ev); err != nil {
			log.Error("ingest failed", "error", err, "dedupe_key", ev.DedupeKey)
		}
	}

	if newState != nil {
		if err := s.Store.SetSourceState(ctx, src.Name(), newState); err != nil {
			log.Error("save source state", "error", err)
		}
	}
	if err := s.Store.RecordPoll(ctx, src.Name(), now, pollErr); err != nil {
		log.Error("record poll", "error", err)
	}

	if pollErr != nil {
		log.Warn("poll finished with error", "error", pollErr, "events", len(events))
		return
	}
	if len(events) > 0 {
		log.Info("poll complete", "events", len(events))
	}
}
