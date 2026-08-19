// Package core defines the domain types shared by every part of Loot: the
// normalized Event that sources emit, the Drop that the rules engine mints
// from an event, and the Source interface that plugins implement.
package core

import (
	"context"
	"encoding/json"
	"errors"
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

// DayLayout is the format of Event.Day and Drop.ChestDate: a calendar day in
// the reporting timezone (UTC for realtime events, the store's own report day
// for ledger data).
const DayLayout = "2006-01-02"

// DayOf renders t as a YYYY-MM-DD day string in UTC.
func DayOf(t time.Time) string { return t.UTC().Format(DayLayout) }

// Event is a single normalized fact observed from a store or service. Sources
// produce events; the pipeline dedupes, persists and classifies them.
type Event struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	App    string `json:"app"`
	// Product is the canonical app this event belongs to: App resolved through
	// the configured `apps:` mapping (see config.Products). It is what every
	// scoped view filters on, and it is "" for Loot's own global events — an
	// achievement belongs to the whole realm, not to one app.
	Product    string    `json:"product"`
	OccurredAt time.Time `json:"occurred_at"`
	ObservedAt time.Time `json:"observed_at"`
	// Day is the business day this event belongs to (YYYY-MM-DD). Ledger
	// sources set it to the report day of the financial report the row came
	// from; for realtime events the pipeline derives it from OccurredAt in UTC.
	// Every vault aggregate groups on this field, never on occurred_at.
	Day      string  `json:"day"`
	Country  string  `json:"country"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	// AmountBase is Amount converted into the configured display currency at
	// ingest time. The pipeline fills it in; sources leave it zero. It is 0
	// when Amount is 0 or when the currency has no known rate.
	AmountBase float64 `json:"amount_base"`
	Quantity   int     `json:"quantity"`
	DedupeKey  string  `json:"dedupe_key"`
	// IsLedger marks an event as authoritative money: only ledger events are
	// summed into vault revenue. Realtime estimates (RevenueCat) must not be.
	IsLedger bool `json:"is_ledger"`
	// Silent events are stored and counted in stats and the vault, but produce
	// no drop at all. Ledger row-level detail is silent; the one summary event
	// per (app, day) is not.
	Silent bool `json:"silent"`
	// Chest asks the pipeline to hold this event's drop for the daily chest
	// instead of publishing it to the live feed. Sources decide; ledger
	// summary events set it, realtime events normally do not.
	Chest   bool            `json:"chest"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// SalesDaySummary is the payload of a `sales_day` event: one per (app, day)
// per ledger source, JSON-encoded into Event.Payload. The event itself carries
// Quantity = Units and Amount = Proceeds so the rarity rules can match on
// min_amount / record_high without decoding the payload.
type SalesDaySummary struct {
	Units      int     `json:"units"`
	Refunds    int     `json:"refunds"`
	Proceeds   float64 `json:"proceeds"`
	Currency   string  `json:"currency"`
	Countries  int     `json:"countries"`
	TopCountry string  `json:"top_country"`
	// BySKU breaks the day down by product, keyed by SKU / product id.
	BySKU map[string]SKUTotals `json:"by_sku,omitempty"`
}

// SKUTotals is one product's share of a SalesDaySummary.
type SKUTotals struct {
	Units    int     `json:"units"`
	Proceeds float64 `json:"proceeds"`
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
	// ChestDate is the day whose chest holds this drop (YYYY-MM-DD), or "" for
	// an immediate drop that went straight to the feed.
	ChestDate string `json:"chest_date,omitempty"`
	// RevealedAt is when the chest holding this drop was opened. It is nil for
	// immediate drops (which were never held) and for chest drops still
	// waiting. A drop is hidden from the feed while ChestDate != "" and
	// RevealedAt is nil.
	RevealedAt *time.Time `json:"revealed_at,omitempty"`
}

// ChestSummary describes one unopened daily chest. It is what the chest API
// returns and what the `chest` bus message carries so the UI badge can update
// without refetching.
type ChestSummary struct {
	Date     string         `json:"date"`
	Count    int            `json:"count"`
	XP       int            `json:"xp"`
	ByRarity map[string]int `json:"by_rarity"`
}

// revealRank orders a chest reveal cascade. It is deliberately *not*
// Rarity.Rank(): cursed sorts first so an opened chest ends on the best news
// of the day rather than on a cancellation.
var revealRank = map[Rarity]int{
	Cursed:    0,
	Common:    1,
	Uncommon:  2,
	Rare:      3,
	Epic:      4,
	Legendary: 5,
}

// RevealRank returns the cascade position of r, ascending.
func RevealRank(r Rarity) int { return revealRank[r] }

// Source is a provider of events. Polling sources do their work in Poll and
// return a state blob that Loot persists and hands back on the next call.
// Webhook-only sources return a zero PollInterval and implement WebhookHandler.
//
// # Ledger sources
//
// A *ledger* source reports settled money from a store's own financial
// reports (App Store Connect, Google Play). Those reports arrive as many rows
// per day, and one drop per row would be unreadable, so ledger sources emit
// two kinds of event:
//
//   - one **silent row event** per meaningful row (Silent: true, IsLedger:
//     true, Day set to the report day). These are stored and counted in stats
//     and the vault but never produce a drop. Give each a stable DedupeKey
//     derived from the report, e.g. "appstore:2026-08-17:1234567890:US:sub_yr".
//   - one **non-silent summary event** per (app, day) with Kind "sales_day",
//     Chest: true, IsLedger: true, Quantity = units, Amount = proceeds,
//     Currency set, and a JSON-encoded SalesDaySummary in Payload. Its drop is
//     held for that day's chest instead of interrupting the live feed.
//
// Summary events must be re-emittable: a report that is restated later should
// reuse the same DedupeKey ("appstore:sales_day:<app>:<day>") so the day is
// not double counted.
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

// Checker is an optional interface for sources that can verify their own
// configuration and credentials without waiting for a poll. `loot check` calls
// it once per configured source and prints a tick or a cross. A nil error
// means "this source would work right now".
type Checker interface {
	Check(ctx context.Context) error
}

// Warning is an advisory Check result: the source works, but something is
// worth telling the operator (an unset webhook secret, say). `loot check`
// prints it as ⚠ and does not count it as a failure.
type Warning struct{ Msg string }

func (w Warning) Error() string { return w.Msg }

// IsWarning reports whether err is (or wraps) a Warning.
func IsWarning(err error) bool {
	var w Warning
	return errors.As(err, &w)
}

// sourceDisplayNames maps source identifiers to the names people use.
var sourceDisplayNames = map[string]string{
	"appstore":       "App Store",
	"googleplay":     "Google Play",
	"microsoftstore": "Microsoft Store",
	"snapcraft":      "Snapcraft",
	"flathub":        "Flathub",
	"revenuecat":     "RevenueCat",
	"github":         "GitHub",
	"webhook":        "webhook",
	"playvitals":     "Play vitals",
	"sentry":         "Sentry",
	"crash":          "crash reports",
	"loot":           "Loot",
	"dev":            "dev",
}

// SourceDisplayName returns the human name for a source id, or the id itself.
func SourceDisplayName(id string) string {
	if n, ok := sourceDisplayNames[id]; ok {
		return n
	}
	return id
}
