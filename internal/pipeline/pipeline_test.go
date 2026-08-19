package pipeline_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newPipeline(t *testing.T) (*pipeline.Pipeline, *store.Store, *bus.Bus) {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}

	b := bus.New(16)
	return pipeline.New(st, engine, b, quietLogger()), st, b
}

func purchase(dedupe string) core.Event {
	now := time.Now().UTC()
	return core.Event{
		ID:         core.NewID(),
		Source:     "revenuecat",
		Kind:       "purchase",
		App:        "com.example.app",
		OccurredAt: now,
		ObservedAt: now,
		Country:    "US",
		Amount:     4.99,
		Currency:   "USD",
		Quantity:   1,
		DedupeKey:  dedupe,
		Payload:    []byte(`{"event":{"period_type":"NORMAL"}}`),
	}
}

func TestIngestCreatesDropAndPublishes(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	msgs, cancel := b.Subscribe()
	defer cancel()

	drop, err := p.Ingest(ctx, purchase("rc:1"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop == nil {
		t.Fatal("ingest returned no drop for a new event")
	}
	// The US is unseen in a fresh store, so the country_first floor lifts this
	// uncommon purchase to rare.
	if drop.Rarity != core.Rare {
		t.Fatalf("rarity = %s, want rare (first US event)", drop.Rarity)
	}

	select {
	case msg := <-msgs:
		if msg.Type != "drop" || msg.Drop == nil || msg.Drop.ID != drop.ID {
			t.Fatalf("published message = %+v", msg)
		}
		if msg.Event == nil || msg.Event.Source != "revenuecat" {
			t.Fatalf("published message is missing its event: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was published to the bus")
	}

	drops, err := st.ListDrops(ctx, 10, "")
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("stored %d drops, want 1", len(drops))
	}
}

// TestIngestDuplicateCreatesNoDrop is the core guarantee: a redelivered webhook
// must not mint a second drop.
func TestIngestDuplicateCreatesNoDrop(t *testing.T) {
	ctx := context.Background()
	p, st, b := newPipeline(t)

	msgs, cancel := b.Subscribe()
	defer cancel()

	if _, err := p.Ingest(ctx, purchase("rc:same")); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	<-msgs // drain the first publish

	// Same dedupe key, brand new event id, as a real retry would arrive.
	drop, err := p.Ingest(ctx, purchase("rc:same"))
	if err != nil {
		t.Fatalf("duplicate ingest returned an error: %v", err)
	}
	if drop != nil {
		t.Fatalf("duplicate produced a drop: %+v", drop)
	}

	select {
	case msg := <-msgs:
		t.Fatalf("duplicate published %+v to the bus", msg)
	case <-time.After(100 * time.Millisecond):
	}

	drops, err := st.ListDrops(ctx, 10, "")
	if err != nil {
		t.Fatalf("list drops: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("stored %d drops, want 1", len(drops))
	}
	if n, err := st.EventCount(ctx, ""); err != nil || n != 1 {
		t.Fatalf("stored %d events, want 1 (%v)", n, err)
	}
}

func TestIngestFillsMissingFields(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	ev := core.Event{Source: "mystery", Kind: "sighting"}
	drop, err := p.Ingest(ctx, ev)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if drop == nil {
		t.Fatal("no drop")
	}

	drops, err := st.ListDrops(ctx, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("stored %d drops, want 1", len(drops))
	}
	if drops[0].OccurredAt.IsZero() {
		t.Error("occurred_at was left unset")
	}
}

func TestIngestSilentlyStoresWithoutDrop(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	if err := p.IngestSilently(ctx, purchase("rc:quiet")); err != nil {
		t.Fatalf("silent ingest: %v", err)
	}

	if n, err := st.EventCount(ctx, ""); err != nil || n != 1 {
		t.Fatalf("stored %d events, want 1 (%v)", n, err)
	}
	drops, err := st.ListDrops(ctx, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(drops) != 0 {
		t.Fatalf("silent ingest created %d drops, want 0", len(drops))
	}
}

// stubSource is a polling source with scripted behaviour.
type stubSource struct {
	events   []core.Event
	state    []byte
	err      error
	gotState []byte
	polls    int
}

func (s *stubSource) Name() string { return "stub" }

func (s *stubSource) Poll(_ context.Context, state []byte) ([]core.Event, []byte, error) {
	s.polls++
	s.gotState = state
	return s.events, s.state, s.err
}

func (s *stubSource) PollInterval() time.Duration { return time.Hour }

func TestSchedulerPollOnce(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	src := &stubSource{
		events: []core.Event{purchase("rc:poll-1"), purchase("rc:poll-2")},
		state:  []byte(`{"cursor":"abc"}`),
	}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())

	sched.PollOnce(ctx, src)

	if src.polls != 1 {
		t.Fatalf("polled %d times, want 1", src.polls)
	}
	if src.gotState != nil {
		t.Fatalf("first poll received state %q, want nil", src.gotState)
	}

	drops, err := st.ListDrops(ctx, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(drops) != 2 {
		t.Fatalf("got %d drops, want 2", len(drops))
	}

	saved, err := st.GetSourceState(ctx, "stub")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if string(saved) != `{"cursor":"abc"}` {
		t.Fatalf("saved state = %q", saved)
	}

	states, err := st.SourceStates(ctx)
	if err != nil {
		t.Fatalf("source states: %v", err)
	}
	if states["stub"].LastPollAt == nil {
		t.Fatal("last_poll_at was not recorded")
	}
	if states["stub"].LastError != "" {
		t.Fatalf("last_error = %q, want empty", states["stub"].LastError)
	}

	// A second cycle re-delivering the same events must not add drops, and the
	// stored cursor must be handed back to the source.
	sched.PollOnce(ctx, src)
	if string(src.gotState) != `{"cursor":"abc"}` {
		t.Fatalf("second poll received state %q", src.gotState)
	}
	drops, _ = st.ListDrops(ctx, 10, "")
	if len(drops) != 2 {
		t.Fatalf("after replay: %d drops, want 2", len(drops))
	}
}

func TestSchedulerRecordsPollError(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPipeline(t)

	failing := errors.New("flathub is down")
	src := &stubSource{
		// A partial failure still delivers what it managed to fetch.
		events: []core.Event{purchase("rc:partial")},
		err:    failing,
	}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())
	sched.PollOnce(ctx, src)

	states, err := st.SourceStates(ctx)
	if err != nil {
		t.Fatalf("source states: %v", err)
	}
	if states["stub"].LastError != failing.Error() {
		t.Fatalf("last_error = %q, want %q", states["stub"].LastError, failing)
	}

	drops, _ := st.ListDrops(ctx, 10, "")
	if len(drops) != 1 {
		t.Fatalf("got %d drops, want the 1 partial result", len(drops))
	}
}

func TestSchedulerRunStopsOnContextCancel(t *testing.T) {
	p, st, _ := newPipeline(t)
	src := &stubSource{}
	sched := pipeline.NewScheduler(p, st, []core.Source{src}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop when the context was cancelled")
	}
}
