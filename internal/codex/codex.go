package codex

import (
	"context"
	"encoding/json"
	"github.com/nickhirras/loot/internal/debounce"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// Ingester is the slice of the pipeline this package needs: one event in, one
// drop out. Keeping it an interface is what stops the Codex and the pipeline
// from importing each other.
type Ingester interface {
	Ingest(ctx context.Context, ev core.Event) (*core.Drop, error)
}

// Publisher is the slice of the bus this package needs.
type Publisher interface {
	Publish(msg bus.Message)
}

const (
	// eventSource is Loot's own reserved source name: an achievement is
	// something Loot noticed about your data, not something a store reported.
	eventSource = "loot"
	// sweepInterval is the unconditional pass. Most unlocks arrive through the
	// ingest nudge; this catches the ones that depend on the calendar rather
	// than on an event (a run of days completing at midnight, say).
	sweepInterval = 10 * time.Minute
	// debounceWait is how long a nudge waits before evaluating, so a chest
	// cascade of forty drops costs one pass rather than forty.
	debounceWait = 2 * time.Second
)

// Board is the whole of GET /api/codex.
type Board struct {
	DisplayCurrency string             `json:"display_currency"`
	Achievements    []core.Achievement `json:"achievements"`
	// Unlocked and Total are the "23 / 41" counter.
	Unlocked int     `json:"unlocked"`
	Total    int     `json:"total"`
	Records  Records `json:"records"`
	Totals   Totals  `json:"totals"`
}

// Service keeps the trophy wall honest: it re-reads the whole history, updates
// the progress bar on every locked achievement, and unlocks the ones that have
// been earned — paying a real drop for each.
//
// It is idempotent by construction. Unlocking is a guarded UPDATE and the
// unlock event's dedupe key is the achievement's own key, so an evaluation may
// run as often as it likes: the second pass over an earned achievement changes
// nothing and mints nothing.
type Service struct {
	Store  *store.Store
	Ingest Ingester
	Bus    Publisher
	Logger *slog.Logger
	// DisplayCurrency is what the money achievements and records are
	// denominated in.
	DisplayCurrency string
	// Now is the clock, in local time: "today" is the day the person reading
	// the Codex is living in.
	Now func() time.Time
	// OnChange is called whenever an unlock changes what List would answer.
	// The HTTP layer uses it to drop its five-second memo, so the refetch a
	// nudge provokes cannot be served a wall from before the trophy.
	OnChange func()

	trigger chan struct{}
}

// NewService returns a service over st.
func NewService(st *store.Store, ingest Ingester, b Publisher, displayCurrency string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		Store:           st,
		Ingest:          ingest,
		Bus:             b,
		Logger:          log,
		DisplayCurrency: displayCurrency,
		Now:             func() time.Time { return time.Now() },
		trigger:         make(chan struct{}, 1),
	}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *Service) today() string { return s.now().Format(core.DayLayout) }

// Trigger asks for an evaluation as soon as possible without blocking the
// caller. The pipeline calls it after every ingest: the drop that pushed you
// past your thousandth unit should hand you the trophy while it is still on
// screen.
func (s *Service) Trigger() {
	if s.trigger == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default: // a pass is already pending; one is enough
	}
}

// Run keeps the Codex current until ctx is cancelled. It blocks.
//
// A nudge is debounced rather than acted on immediately, because an evaluation
// reads the whole history and a chest cascade delivers forty drops in half a
// minute. The ten-minute sweep is the backstop for anything the calendar
// changes rather than an event.
func (s *Service) Run(ctx context.Context) {
	if err := s.Startup(ctx); err != nil {
		s.log().Error("codex startup failed", "error", err)
	}

	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

	// One timer, armed by the first nudge of a burst and reused forever after.
	// A timer per nudge would be simpler to read and would leak one every time
	// a chest cascaded, since this loop outlives the process's interest in it.
	quiet := debounce.New(debounceWait)
	defer quiet.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
			quiet.Arm()
			continue
		case <-quiet.C():
			quiet.Fired()
		case <-sweep.C:
			// The sweep is about to do the work a pending nudge asked for;
			// leaving the timer armed would swallow every later nudge.
			quiet.Disarm()
		}
		if _, err := s.Evaluate(ctx); err != nil {
			s.log().Error("codex evaluation failed", "error", err)
		}
	}
}

// Startup runs one evaluation before the server starts serving, so a fresh
// boot answers /api/codex with a filled-in wall rather than an empty one.
// Demo mode calls it after seeding, which is what puts trophies in the demo's
// first chest.
func (s *Service) Startup(ctx context.Context) error {
	_, err := s.Evaluate(ctx)
	return err
}

// Result reports what one evaluation pass did.
type Result struct {
	// Evaluated is how many catalog entries were measured.
	Evaluated int
	// Unlocked is what was earned this pass.
	Unlocked []core.Achievement
	// Backfilled is true when this pass was the first ever over this database,
	// in which case the unlocks were things that had already happened.
	Backfilled bool
}

// Evaluate measures the whole catalog against one pass over the history,
// refreshes the progress on everything still locked, and unlocks whatever has
// been earned.
//
// # Backfill
//
// The first pass over a database that already has history is special. Somebody
// who imports a year of App Store reports has genuinely earned twenty-five
// achievements, and firing twenty-five drops — several of them legendary —
// into the live feed at once would be a wall of noise rather than a
// celebration. So on that first pass:
//
//   - each unlock is dated with the day it was *actually* earned, derived from
//     the running total (the day the tenth country was settled), so the trophy
//     wall reads as a history rather than as "everything, today";
//   - each unlock's drop is filed into today's chest instead of the feed, and
//     marked `backfilled` in its meta and its payload. Opening that chest is
//     then a proper cascade, quietest first, exactly like any other chest.
//
// Every later pass unlocks live, one trophy at a time, with its own sound.
func (s *Service) Evaluate(ctx context.Context) (Result, error) {
	var res Result

	total, _, err := s.Store.CountAchievements(ctx)
	if err != nil {
		return res, err
	}
	// No rows at all means the catalog has never been written to this
	// database: this is the backfill pass.
	res.Backfilled = total == 0

	snap, err := s.snapshot(ctx)
	if err != nil {
		return res, err
	}

	stored, err := s.Store.ListAchievements(ctx)
	if err != nil {
		return res, err
	}
	unlockedKeys := make(map[string]bool, len(stored))
	for _, a := range stored {
		if a.Unlocked() {
			unlockedKeys[a.Key] = true
		}
	}

	now := s.now().UTC()
	today := s.today()

	for _, entry := range Catalog {
		res.Evaluated++
		value, earnedDay, earned := entry.Progress(snap)

		if err := s.Store.UpsertAchievement(ctx, core.Achievement{
			Key:            entry.Key,
			Tier:           entry.Tier,
			Title:          entry.Title,
			Description:    entry.Description,
			ProgressValue:  value,
			ProgressTarget: entry.Target,
		}, now); err != nil {
			return res, err
		}
		if !earned || unlockedKeys[entry.Key] {
			continue
		}

		// When it was earned, not when Loot noticed. A day in the past is
		// stamped at noon UTC: an exact hour is not knowable from a day, and
		// noon cannot fall out of its own day in any timezone the UI renders.
		at := now
		if earnedDay != "" && earnedDay != today {
			if t, ok := parseDay(earnedDay); ok {
				at = t.Add(12 * time.Hour)
			}
		}
		// Backfilled means "this pass was the first ever over this database",
		// and nothing else. Dating a trophy in the past is ordinary — a
		// milestone crossed on a day whose report only settled this morning is
		// earned yesterday and found today — but it is still live news, and it
		// should land in the feed with its own sound rather than being posted
		// silently into a chest.
		backfilled := res.Backfilled

		meta, err := json.Marshal(map[string]any{
			"earned_day": earnedDay,
			"backfilled": backfilled,
		})
		if err != nil {
			return res, err
		}
		claimed, err := s.Store.UnlockAchievement(ctx, entry.Key, at, meta, value)
		if err != nil {
			return res, err
		}
		if !claimed {
			continue
		}

		unlocked := core.Achievement{
			Key: entry.Key, Tier: entry.Tier, Title: entry.Title,
			Description: entry.Description, UnlockedAt: &at,
			ProgressValue: value, ProgressTarget: entry.Target,
			Unit: entry.Unit, Money: entry.Money, Pct: 1,
			Meta: meta,
		}
		if err := s.award(ctx, entry, backfilled); err != nil {
			s.log().Error("achievement reward failed", "error", err, "key", entry.Key)
		}
		s.log().Info("achievement unlocked", "key", entry.Key, "title", entry.Title,
			"tier", entry.Tier, "earned", earnedDay, "backfilled", backfilled)
		res.Unlocked = append(res.Unlocked, unlocked)
	}

	if len(res.Unlocked) > 0 {
		s.publish()
		// An unlock pays XP, which can carry the account into a new era — and
		// that is an achievement too. One more pass settles it; the pass after
		// that finds nothing and stops.
		s.Trigger()
	}
	return res, nil
}

// snapshot reads the history once and reshapes it into the cumulative series
// the catalog measures against.
func (s *Service) snapshot(ctx context.Context) (*Snapshot, error) {
	agg, err := s.Store.CodexAggregates(ctx)
	if err != nil {
		return nil, err
	}
	return NewSnapshot(agg, s.today(), s.DisplayCurrency), nil
}

// award ingests the unlock event. Its dedupe key is the achievement's own key,
// so this can never mint a second drop however many times it is called.
//
// A backfilled unlock is filed into *today's* chest: the trophy is dated when
// it was earned, but its drop belongs to the day you found out.
func (s *Service) award(ctx context.Context, entry Entry, backfilled bool) error {
	if s.Ingest == nil {
		return nil
	}
	payload, err := json.Marshal(core.AchievementPayload{
		Key:         entry.Key,
		Title:       entry.Title,
		Tier:        entry.Tier,
		Description: entry.Description,
		Backfilled:  backfilled,
	})
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = s.Ingest.Ingest(ctx, core.Event{
		Source:     eventSource,
		Kind:       core.KindAchievement,
		OccurredAt: now,
		ObservedAt: now,
		Day:        core.DayOf(now),
		DedupeKey:  core.AchievementDedupePfx + entry.Key,
		Chest:      backfilled,
		Payload:    payload,
	})
	return err
}

// publish tells connected browsers the Codex changed. Like the quest board it
// is a bare nudge — the page refetches — so the websocket never carries a
// second copy of the wall.
func (s *Service) publish() {
	if s.OnChange != nil {
		s.OnChange()
	}
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(bus.Message{Type: "codex"})
}

// List answers GET /api/codex: the trophy wall, the records and the lifetime
// totals, all from one pass over the history.
func (s *Service) List(ctx context.Context) (Board, error) {
	board := Board{DisplayCurrency: s.DisplayCurrency, Achievements: []core.Achievement{}}

	snap, err := s.snapshot(ctx)
	if err != nil {
		return board, err
	}
	board.Records, board.Totals = BuildRecords(snap)

	stored, err := s.Store.ListAchievements(ctx)
	if err != nil {
		return board, err
	}
	decorate(stored)
	sort.Slice(stored, func(i, j int) bool {
		oi, oj := CatalogOrder(stored[i].Key), CatalogOrder(stored[j].Key)
		if oi != oj {
			return oi < oj
		}
		return stored[i].Key < stored[j].Key
	})
	board.Achievements = stored
	board.Total = len(stored)
	for _, a := range stored {
		if a.Unlocked() {
			board.Unlocked++
		}
	}
	// A database that has never been evaluated (a read before the first pass)
	// should still say how many trophies there are to win.
	if board.Total == 0 {
		board.Total = len(Catalog)
	}
	return board, nil
}

// Recap answers GET /api/recap for one period.
func (s *Service) Recap(ctx context.Context, month, season string) (Recap, error) {
	p, err := ResolvePeriod(s.now(), month, season)
	if err != nil {
		return Recap{}, err
	}
	return BuildRecap(ctx, s.Store, p, s.DisplayCurrency)
}

// Months lists the periods the recap picker offers: the last twelve months
// (newest first, the current one included so "so far this month" is
// reachable), plus the current year as a season.
func (s *Service) Months(limit int) []Period {
	if limit <= 0 {
		limit = 12
	}
	now := s.now()
	today := core.DayOf(now)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	out := make([]Period, 0, limit+1)
	for i := 0; i < limit; i++ {
		out = append(out, monthPeriod(first.AddDate(0, -i, 0), today))
	}
	start := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	out = append(out, period("season", strconv.Itoa(now.Year()), strconv.Itoa(now.Year()),
		start, start.AddDate(1, 0, -1), today))
	return out
}

// decorate fills in the fields that live in the catalog rather than in the
// database — the progress unit and whether it is money — so the UI can render
// "18 / 25 countries" without a second table.
func decorate(list []core.Achievement) {
	byKey := make(map[string]Entry, len(Catalog))
	for _, e := range Catalog {
		byKey[e.Key] = e
	}
	for i := range list {
		if e, ok := byKey[list[i].Key]; ok {
			list[i].Unit = e.Unit
			list[i].Money = e.Money
		}
	}
}
