// Package debounce is the one reusable timer behind the Codex's and the boss
// board's Run loops: a single timer, armed by the first nudge of a burst and
// disarmed by whatever gets there first.
//
// It is a package rather than three lines inline in each loop because the
// state machine has exactly one hazard and it is easy to get wrong twice.
// Stopping a timer that has already fired leaves a value sitting in its
// channel, and an `armed` flag left true after some other branch did the work
// means every later nudge is swallowed by "a pass is already pending" and the
// timer is never rearmed — which is a service that stops responding to the
// pipeline entirely, one sweep after the first to land on a pending nudge.
package debounce

import "time"

// Timer is a single reused time.Timer plus the armed flag it needs. It is not
// safe for concurrent use; it belongs to one select loop.
type Timer struct {
	timer *time.Timer
	wait  time.Duration
	armed bool
}

// New returns a stopped Timer with an empty channel.
func New(wait time.Duration) *Timer {
	t := time.NewTimer(wait)
	if !t.Stop() {
		<-t.C
	}
	return &Timer{timer: t, wait: wait}
}

// C is the channel that fires once a burst of nudges has gone quiet.
func (d *Timer) C() <-chan time.Time { return d.timer.C }

// Armed reports whether a wait is pending.
func (d *Timer) Armed() bool { return d.armed }

// Arm starts the wait, unless it is already running: the *first* nudge of a
// burst sets the deadline and the rest ride along behind it.
func (d *Timer) Arm() {
	if d.armed {
		return
	}
	d.timer.Reset(d.wait)
	d.armed = true
}

// Fired records that the wait elapsed and its value has been received from C.
func (d *Timer) Fired() { d.armed = false }

// Disarm cancels a pending wait because something else is about to do the
// work, draining the channel if the timer beat us to it.
func (d *Timer) Disarm() {
	if !d.armed {
		return
	}
	if !d.timer.Stop() {
		<-d.timer.C
	}
	d.armed = false
}

// Stop releases the timer for good.
func (d *Timer) Stop() {
	d.timer.Stop()
	d.armed = false
}
