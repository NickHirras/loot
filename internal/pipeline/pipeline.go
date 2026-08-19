// Package pipeline wires sources to storage: every event, whether polled or
// pushed by a webhook, travels the same path —
// dedupe -> persist -> classify -> persist drop -> publish.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

// Pipeline ingests events.
type Pipeline struct {
	Store  *store.Store
	Rules  *rules.Engine
	Bus    *bus.Bus
	Logger *slog.Logger
}

// New returns a pipeline.
func New(st *store.Store, engine *rules.Engine, b *bus.Bus, log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{Store: st, Rules: engine, Bus: b, Logger: log}
}

// Ingest stores ev and, if it is new, mints and publishes a drop. A duplicate
// (same dedupe_key) returns a nil drop and no error: the whole point of the
// dedupe key is that a webhook retry is a no-op, not an error.
func (p *Pipeline) Ingest(ctx context.Context, ev core.Event) (*core.Drop, error) {
	if ev.ID == "" {
		ev.ID = core.NewID()
	}
	if ev.ObservedAt.IsZero() {
		ev.ObservedAt = time.Now().UTC()
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = ev.ObservedAt
	}
	if ev.DedupeKey == "" {
		ev.DedupeKey = ev.Source + ":" + ev.ID
	}

	exists, err := p.Store.InsertEvent(ctx, ev)
	if err != nil {
		return nil, fmt.Errorf("ingest %s/%s: %w", ev.Source, ev.Kind, err)
	}
	if exists {
		p.Logger.Debug("duplicate event ignored", "source", ev.Source, "dedupe_key", ev.DedupeKey)
		return nil, nil
	}

	drop, err := p.Rules.Classify(ctx, ev)
	if err != nil {
		return nil, fmt.Errorf("classify %s/%s: %w", ev.Source, ev.Kind, err)
	}
	if err := p.Store.InsertDrop(ctx, drop); err != nil {
		return nil, fmt.Errorf("store drop: %w", err)
	}

	if p.Bus != nil {
		evCopy := ev
		p.Bus.Publish(bus.Message{Type: "drop", Drop: &drop, Event: &evCopy})
	}

	p.Logger.Info("drop",
		"rarity", drop.Rarity, "title", drop.Title, "source", ev.Source, "xp", drop.XP)
	return &drop, nil
}

// IngestSilently stores ev without minting a drop. Used for backfill, where the
// history matters (for record-high comparisons) but the feed should stay quiet.
func (p *Pipeline) IngestSilently(ctx context.Context, ev core.Event) error {
	if ev.ID == "" {
		ev.ID = core.NewID()
	}
	_, err := p.Store.InsertEvent(ctx, ev)
	return err
}

// Scheduler drives polling sources on their own intervals.
type Scheduler struct {
	Pipeline *Pipeline
	Store    *store.Store
	Sources  []core.Source
	Logger   *slog.Logger
}

// NewScheduler returns a scheduler over the polling-capable sources in list.
// Webhook-only sources (PollInterval == 0) are ignored.
func NewScheduler(p *Pipeline, st *store.Store, sources []core.Source, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{Pipeline: p, Store: st, Sources: sources, Logger: log}
}

// Run polls every polling source until ctx is cancelled. It blocks.
func (s *Scheduler) Run(ctx context.Context) {
	done := make(chan struct{})
	started := 0

	for _, src := range s.Sources {
		interval := src.PollInterval()
		if interval <= 0 {
			continue
		}
		started++
		go func(src core.Source, interval time.Duration) {
			defer func() { done <- struct{}{} }()
			s.runSource(ctx, src, interval)
		}(src, interval)
	}

	if started == 0 {
		<-ctx.Done()
		return
	}
	for i := 0; i < started; i++ {
		<-done
	}
}

func (s *Scheduler) runSource(ctx context.Context, src core.Source, interval time.Duration) {
	log := s.Logger.With("source", src.Name())
	log.Info("polling source started", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		s.PollOnce(ctx, src)
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
