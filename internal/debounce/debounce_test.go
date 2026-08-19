package debounce

import (
	"testing"
	"time"
)

// The sweep and the nudge both want the same evaluation, so whichever arrives
// first has to leave the other's bookkeeping clean. A sweep that fired while a
// nudge was pending used to leave the debouncer armed forever: every later
// nudge was then swallowed as "already pending", the timer was never rearmed,
// and the service stopped reacting to the pipeline entirely.
func TestSweepDisarmsAPendingNudge(t *testing.T) {
	d := New(time.Hour)
	defer d.Stop()

	d.Arm()
	if !d.Armed() {
		t.Fatal("the first nudge did not arm the wait")
	}
	d.Disarm() // the sweep got there first
	if d.Armed() {
		t.Fatal("the sweep left the debouncer armed")
	}
	select {
	case <-d.C():
		t.Fatal("a disarmed debouncer still fired")
	default:
	}

	// And the next nudge must still be heard.
	d.wait = 5 * time.Millisecond // internal knob; same package
	d.Arm()
	if !d.Armed() {
		t.Fatal("a later nudge was swallowed")
	}
	select {
	case <-d.C():
		d.Fired()
	case <-time.After(2 * time.Second):
		t.Fatal("the rearmed wait never elapsed")
	}
	if d.Armed() {
		t.Error("fired() did not clear the armed flag")
	}
}

// disarm must not block when the timer beat it to the channel — the case that
// makes a naive Stop() deadlock the whole loop.
func TestDisarmDrainsATimerThatAlreadyFired(t *testing.T) {
	d := New(time.Millisecond)
	defer d.Stop()

	d.Arm()
	time.Sleep(20 * time.Millisecond) // the timer has certainly fired by now

	done := make(chan struct{})
	go func() {
		d.Disarm()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disarm blocked on an already-fired timer")
	}

	select {
	case <-d.C():
		t.Fatal("disarm left a stale value in the channel")
	default:
	}
}

// A burst of nudges is one wait, not one per nudge: that is the whole point of
// the thing, and it is what stops a poll that ingests four hundred crash rows
// from costing four hundred evaluations.
func TestABurstOfNudgesIsOneWait(t *testing.T) {
	d := New(5 * time.Millisecond)
	defer d.Stop()

	d.Arm()
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		d.Arm() // riders on the first nudge's deadline
	}
	select {
	case <-d.C():
	case <-time.After(2 * time.Second):
		t.Fatal("a burst of nudges pushed the deadline out forever")
	}
}
