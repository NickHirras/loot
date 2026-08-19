package pipeline_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
)

// FirstRoundDone is what stops Loot from asking questions about a database
// that has not been filled in yet. Everything downstream of it — quest
// generation, and the epic drop a quest completed at boot would pay — depends
// on it closing at the right moment and, just as importantly, on it closing at
// all.

// fakeSource is a source whose poll takes as long as the test says it does.
type fakeSource struct {
	name     string
	interval time.Duration
	delay    time.Duration
	polls    atomic.Int32
	// gate, when non-nil, holds the first poll until it is closed.
	gate chan struct{}
}

func (f *fakeSource) Name() string                { return f.name }
func (f *fakeSource) PollInterval() time.Duration { return f.interval }

func (f *fakeSource) Poll(ctx context.Context, _ []byte) ([]core.Event, []byte, error) {
	if f.polls.Add(1) == 1 && f.gate != nil {
		select {
		case <-ctx.Done():
		case <-f.gate:
		}
	}
	if f.delay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(f.delay):
		}
	}
	return nil, nil, nil
}

func newScheduler(t *testing.T, sources ...core.Source) *pipeline.Scheduler {
	t.Helper()
	p, st, _ := newPipeline(t)
	return pipeline.NewScheduler(p, st, sources, quietLogger())
}

// TestFirstRoundWaitsForEverySource: the channel closes only once every
// polling source has finished its first poll, never before.
func TestFirstRoundWaitsForEverySource(t *testing.T) {
	slow := &fakeSource{name: "appstore", interval: time.Hour, gate: make(chan struct{})}
	quick := &fakeSource{name: "flathub", interval: time.Hour}

	sched := newScheduler(t, slow, quick)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	select {
	case <-sched.FirstRoundDone():
		t.Fatal("the first round finished while a source was still polling")
	case <-time.After(150 * time.Millisecond):
	}

	close(slow.gate)
	select {
	case <-sched.FirstRoundDone():
	case <-time.After(3 * time.Second):
		t.Fatal("the first round never finished after every source polled")
	}
}

// TestFirstRoundWithNoPollingSources is a webhook-only Loot (and demo mode):
// there is no first poll to wait for, so the round is over immediately rather
// than ten minutes later.
func TestFirstRoundWithNoPollingSources(t *testing.T) {
	webhookOnly := &fakeSource{name: "revenuecat"} // PollInterval 0

	sched := newScheduler(t, webhookOnly)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	select {
	case <-sched.FirstRoundDone():
	case <-time.After(2 * time.Second):
		t.Fatal("a webhook-only Loot waited for a poll that will never happen")
	}
	if n := webhookOnly.polls.Load(); n != 0 {
		t.Errorf("a webhook-only source was polled %d times", n)
	}
}

// TestFirstRoundGivesUp: a source that hangs must not hold the rest of Loot
// hostage. The cap is ten minutes in production; here it is a moment.
func TestFirstRoundGivesUp(t *testing.T) {
	stuck := &fakeSource{name: "appstore", interval: time.Hour, gate: make(chan struct{})}

	sched := newScheduler(t, stuck)
	sched.FirstRoundCap = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	select {
	case <-sched.FirstRoundDone():
	case <-time.After(3 * time.Second):
		t.Fatal("a stuck source blocked the first round past its cap")
	}
	close(stuck.gate)
}
