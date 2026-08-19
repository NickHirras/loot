// Package core defines the domain types shared by every part of Loot: the
// normalized Event that sources emit, the Drop that the rules engine mints
// from an event, and the Source interface that plugins implement.
package core

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Rarity classifies how exciting a drop is. Ordered from least to most
// noteworthy, with Cursed sitting outside the ladder as "bad news".
type Rarity string

const (
	Common    Rarity = "common"
	Uncommon  Rarity = "uncommon"
	Rare      Rarity = "rare"
	Epic      Rarity = "epic"
	Legendary Rarity = "legendary"
	Cursed    Rarity = "cursed"
)

// Rarities lists every valid rarity, in ladder order.
var Rarities = []Rarity{Common, Uncommon, Rare, Epic, Legendary, Cursed}

// rank orders rarities so rules can express "at least rare". Cursed is
// deliberately ranked above legendary: a cancellation should never be quietly
// downgraded into a cheerful drop by a floor rule.
var rank = map[Rarity]int{
	Common:    0,
	Uncommon:  1,
	Rare:      2,
	Epic:      3,
	Legendary: 4,
	Cursed:    5,
}

// Rank returns the ladder position of r. Unknown rarities rank as Common.
func (r Rarity) Rank() int { return rank[r] }

// Valid reports whether r is a known rarity.
func (r Rarity) Valid() bool { _, ok := rank[r]; return ok }

// DefaultXP is the XP awarded when a rule does not specify its own value.
var DefaultXP = map[Rarity]int{
	Common:    10,
	Uncommon:  25,
	Rare:      100,
	Epic:      300,
	Legendary: 1000,
	Cursed:    5,
}

// Event is a single normalized fact observed from a store or service. Sources
// produce events; the pipeline dedupes, persists and classifies them.
type Event struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Kind       string          `json:"kind"`
	App        string          `json:"app"`
	OccurredAt time.Time       `json:"occurred_at"`
	ObservedAt time.Time       `json:"observed_at"`
	Country    string          `json:"country"`
	Amount     float64         `json:"amount"`
	Currency   string          `json:"currency"`
	Quantity   int             `json:"quantity"`
	DedupeKey  string          `json:"dedupe_key"`
	IsLedger   bool            `json:"is_ledger"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Drop is the gamified presentation of an event: what the feed shows, what
// sound plays, and how much XP it is worth.
type Drop struct {
	ID        string    `json:"id"`
	EventID   string    `json:"event_id"`
	Rarity    Rarity    `json:"rarity"`
	Title     string    `json:"title"`
	Subtitle  string    `json:"subtitle"`
	XP        int       `json:"xp"`
	CreatedAt time.Time `json:"created_at"`
}

// Source is a provider of events. Polling sources do their work in Poll and
// return a state blob that Loot persists and hands back on the next call.
// Webhook-only sources return a zero PollInterval and implement WebhookHandler.
type Source interface {
	Name() string

	// Poll is called by the scheduler for polling sources; webhook sources may
	// return nil, nil, nil.
	Poll(ctx context.Context, state []byte) (events []Event, newState []byte, err error)

	// PollInterval reports how often Poll should run. 0 means webhook-only.
	PollInterval() time.Duration
}

// WebhookHandler is implemented by sources that receive pushes. The handler is
// mounted at /hooks/{source}; every event passed to emit runs the same
// dedupe -> classify -> publish pipeline as a polled event.
type WebhookHandler interface {
	HandleWebhook(w http.ResponseWriter, r *http.Request, emit func(Event))
}
