// Package bus is a tiny in-process fan-out for drops. The pipeline publishes;
// websocket connections and `loot tail` subscribe.
package bus

import (
	"sync"

	"github.com/nickhirras/loot/internal/core"
)

// Message is what travels over the bus. Kind is the wire discriminator used by
// the websocket protocol.
type Message struct {
	Type string     `json:"type"`
	Drop *core.Drop `json:"drop,omitempty"`
	// Event carries the originating event so the UI can render source, country
	// and amount without a second round trip.
	Event *core.Event `json:"event,omitempty"`
}

// Bus fans messages out to every current subscriber. Publish never blocks: a
// subscriber that cannot keep up loses messages rather than stalling ingest.
type Bus struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]chan Message
	buffer int
}

// New returns a bus whose per-subscriber queue holds bufferSize messages.
func New(bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &Bus{subs: make(map[int]chan Message), buffer: bufferSize}
}

// Subscribe returns a channel of messages and a cancel func that closes it.
// Calling cancel more than once is safe.
func (b *Bus) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, b.buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers msg to every subscriber, skipping any whose queue is full.
func (b *Bus) Publish(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- msg:
		default: // slow consumer: drop rather than block the ingest path
		}
	}
}

// Subscribers reports the current subscriber count (used by /api/stats).
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
