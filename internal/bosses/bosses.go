// Package bosses turns crash clusters into monsters.
//
// Crashes are the one number in Loot that nobody wants to look at, and the one
// number a dashboard usually handles worst: a red badge that goes up, sits
// there, and slowly teaches you to ignore it. So Loot does not badge crashes.
// It gives them a name, a health bar, and a fight you can win.
//
// When a day's crashes break away from the app's own baseline, a **boss**
// spawns: "The Null-Dereferencing Wyrm of v2.3.1", with hit points equal to
// that day's crash count. Every completed day after that sets HP to that day's
// count, so shipping a fix and watching it roll out visibly drains the bar. At
// a tenth of its opening strength for two days running, the boss is **slain**
// and pays an epic drop.
//
// The framing is load-bearing:
//
//   - the spawn is the only cursed moment, and it is worth no XP at all. It is
//     news, not a punishment.
//   - a boss that gets worse before it gets better **enrages**. That is a fact
//     about the crash, said once, quietly.
//   - a boss that is still standing after a fortnight is still standing. There
//     is no overdue state, no red, no nagging — because the day you finally
//     kill it should feel like a win, and a dashboard that has spent two weeks
//     scolding you has already spent that feeling.
//   - a boss whose source stops reporting **fades**: silent, no reward, no
//     blame. Loot lost sight of it; that is Loot's problem, not yours.
package bosses

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nickhirras/loot/internal/debounce"
	"log/slog"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The events Loot mints for itself as a fight plays out.
const (
	// eventSource is Loot's own reserved source name: a boss is something Loot
	// noticed in your crash data, not something a store reported.
	eventSource = "loot"

	KindSpawn  = "boss_spawn"
	KindEnrage = "boss_enrage"
	KindSlain  = "boss_slain"

	spawnDedupePfx  = "loot:boss_spawn:"
	enrageDedupePfx = "loot:boss_enrage:"
	slainDedupePfx  = "loot:boss_slain:"
)

const (
	// sweepInterval is the unconditional pass. Most of the work is calendar
	// driven — a day completes and the fight moves — so the ticker matters
	// more here than it does for quests.
	sweepInterval = time.Hour
	// debounceWait is how long a nudge waits, so a poll that ingests four hundred
	// crash rows costs one evaluation rather than four hundred.
	debounceWait = 3 * time.Second
	// recentLimit is how many finished fights the board remembers.
	recentLimit = 20
)

// Ingester is the slice of the pipeline this package needs: one event in, one
// drop out. Keeping it an interface is what stops bosses and the pipeline from
// importing each other.
type Ingester interface {
	Ingest(ctx context.Context, ev core.Event) (*core.Drop, error)
}

// Publisher is the slice of the bus this package needs.
type Publisher interface {
	Publish(msg bus.Message)
}

// Board is the whole of GET /api/bosses.
type Board struct {
	// Alive is every fight in progress, biggest first.
	Alive []core.Boss `json:"alive"`
	// Recent is the last few slain and faded, newest first — the trophy shelf.
	Recent []core.Boss `json:"recent"`
}

// Service watches the crash series, spawns and drives bosses, and pays the
// drops. It is idempotent by construction: a boss key is unique in the table,
// every event it mints is keyed on the boss id, and every state change is a
// guarded UPDATE, so an evaluation may run as often as it likes.
type Service struct {
	Store  *store.Store
	Ingest Ingester
	Bus    Publisher
	Logger *slog.Logger
	// Now is the clock, in UTC: a "completed day" is a UTC day, exactly as it
	// is for the mystery detector.
	Now func() time.Time
	// OnChange is called whenever an evaluation changed what List would
	// answer. The HTTP layer uses it to drop its memo, so the refetch a nudge
	// provokes cannot be served a board from before the kill.
	OnChange func()

	trigger chan struct{}
}

// NewService returns a service over st.
func NewService(st *store.Store, ingest Ingester, b Publisher, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		Store:   st,
		Ingest:  ingest,
		Bus:     b,
		Logger:  log,
		Now:     func() time.Time { return time.Now().UTC() },
		trigger: make(chan struct{}, 1),
	}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// Trigger asks for an evaluation as soon as possible without blocking the
// caller. The pipeline calls it after ingesting a crash event: a fix that just
// landed should drain the bar while you are still looking at it.
func (s *Service) Trigger() {
	if s.trigger == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default: // a pass is already pending; one is enough
	}
}

// Run keeps the board current until ctx is cancelled. It blocks, so
// `loot serve` starts it in a goroutine.
func (s *Service) Run(ctx context.Context) {
	if err := s.Startup(ctx); err != nil {
		s.log().Error("boss startup failed", "error", err)
	}

	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

	// One timer, armed by the first nudge of a burst and reused afterwards —
	// the same shape the Codex uses, and for the same reason: a timer per
	// nudge would leak one per ingested crash row.
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
			// The sweep is about to do exactly what a pending nudge was
			// waiting to ask for, so cancel it rather than evaluating twice
			// seconds apart.
			quiet.Disarm()
		}
		if _, err := s.Evaluate(ctx); err != nil {
			s.log().Error("boss evaluation failed", "error", err)
		}
	}
}

// Startup runs one evaluation before the server starts serving, so a fresh
// boot answers /api/bosses with the real state of the world.
func (s *Service) Startup(ctx context.Context) error {
	_, err := s.Evaluate(ctx)
	return err
}

// List returns the board: the fights in progress and the last few that ended.
func (s *Service) List(ctx context.Context) (Board, error) {
	board := Board{Alive: []core.Boss{}, Recent: []core.Boss{}}

	alive, err := s.Store.ListBosses(ctx, store.BossQuery{Statuses: []string{core.BossAlive}})
	if err != nil {
		return board, err
	}
	today := core.DayOf(s.now())
	for _, b := range alive {
		board.Alive = append(board.Alive, Decorate(b, today))
	}

	recent, err := s.Store.ListBosses(ctx, store.BossQuery{
		Statuses: []string{core.BossSlain, core.BossFaded},
		Limit:    recentLimit,
		Newest:   true,
	})
	if err != nil {
		return board, err
	}
	for _, b := range recent {
		board.Recent = append(board.Recent, Decorate(b, today))
	}
	return board, nil
}

// Slay ends a fight because you said so. It is the escape hatch for every
// source that cannot tell Loot the crash stopped: a Sentry issue you fixed
// without closing, a webhook you turned off, a version nobody runs any more.
//
// Slaying an already-finished boss is a no-op that returns the stored row, so
// a double click cannot mint two drops.
func (s *Service) Slay(ctx context.Context, id string) (core.Boss, error) {
	b, err := s.Store.GetBoss(ctx, id)
	if err != nil {
		return core.Boss{}, err
	}
	if b.Status != core.BossAlive {
		return Decorate(b, core.DayOf(s.now())), nil
	}

	detail := decodeDetail(b.Detail)
	detail.Slayer = "manual"
	raw, err := json.Marshal(detail)
	if err != nil {
		return core.Boss{}, fmt.Errorf("boss detail: %w", err)
	}

	changed, err := s.Store.CloseBoss(ctx, b.ID, core.BossSlain, 0, raw, s.now())
	if err != nil {
		return core.Boss{}, err
	}
	if !changed {
		stored, err := s.Store.GetBoss(ctx, id)
		if err != nil {
			return core.Boss{}, err
		}
		return Decorate(stored, core.DayOf(s.now())), nil
	}

	b.Status = core.BossSlain
	b.HP = 0
	b.Detail = raw
	slain := s.now()
	b.SlainAt = &slain

	if err := s.awardSlain(ctx, &b, core.DayOf(s.now())); err != nil {
		s.log().Error("boss reward failed", "error", err, "boss", b.ID)
	}
	s.log().Info("boss slain", "name", b.Name, "by", "you", "xp", b.XPAwarded)
	s.changed()
	return Decorate(b, core.DayOf(s.now())), nil
}

// Decorate fills in the computed fields the API and the UI want but the table
// does not store: the bar's fullness, how long the fight has run, and the
// series lifted out of the detail blob.
func Decorate(b core.Boss, today string) core.Boss {
	detail := decodeDetail(b.Detail)
	b.Pct = core.BossPct(b.HP, b.HPMax)
	b.DownPct = 1 - b.Pct
	if b.DownPct < 0 {
		b.DownPct = 0
	}
	end := today
	if b.Status != core.BossAlive && b.SlainAt != nil {
		end = core.DayOf(*b.SlainAt)
	}
	b.DaysAlive = core.DaysBetween(b.SpawnedDay, end)
	if b.DaysAlive < 1 {
		b.DaysAlive = 1
	}
	b.Enraged = detail.Enraged
	b.Unit = detail.Unit
	if b.Unit == "" {
		b.Unit = "crashes"
	}
	b.URL = detail.URL
	b.Kind = detail.Kind
	b.Series = detail.Series
	if b.Series == nil {
		b.Series = []core.BossPoint{}
	}
	return b
}

func decodeDetail(raw json.RawMessage) core.BossDetail {
	var d core.BossDetail
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &d)
	}
	return d
}

// ------------------------------------------------------------------ rewards

// eventPayload is what every boss event carries. One struct for all three
// kinds keeps the rules file's templates identical across them, so a custom
// rules file only has to learn `{{.Payload.name}}` once.
type eventPayload struct {
	BossID  string `json:"boss_id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	App     string `json:"app"`
	Version string `json:"version"`
	// Detail is a pre-formatted subtitle, so the rules file needs no
	// arithmetic to say something useful.
	Detail string `json:"detail"`
	// Kind is crash or anr; Unit is what HP counts.
	Kind string `json:"kind,omitempty"`
	Unit string `json:"unit,omitempty"`
	// HP and HPMax are the fight's numbers at the moment of the event.
	HP    float64 `json:"hp"`
	HPMax float64 `json:"hp_max"`
	Users int     `json:"users_affected,omitempty"`
	Days  int     `json:"days,omitempty"`
	// Scale is what the rules file matches on to make a long or wide fight
	// legendary rather than epic, exactly as a quest's `scope` does.
	Scale string `json:"scale,omitempty"`
	// Slayer says how it died: recovered, resolved or manual.
	Slayer string `json:"slayer,omitempty"`
	URL    string `json:"url,omitempty"`
}

// LegendaryUsers and LegendaryDays are the two ways a kill is legendary rather
// than epic: it was a big fight, or it was a long one.
const (
	LegendaryUsers = 500
	LegendaryDays  = 7
)

// scaleOf decides how grand a kill is. Either half qualifies on its own: a
// crash that hit five hundred people mattered, and so did one you chased for a
// week.
func scaleOf(b core.Boss, days int) string {
	if b.HPMax >= LegendaryUsers || b.UsersAffected >= LegendaryUsers || days >= LegendaryDays {
		return "legendary"
	}
	return "epic"
}

func (s *Service) basePayload(b core.Boss) eventPayload {
	detail := decodeDetail(b.Detail)
	return eventPayload{
		BossID:  b.ID,
		Key:     b.Key,
		Name:    b.Name,
		Title:   b.Title,
		App:     b.App,
		Version: b.Version,
		Kind:    detail.Kind,
		Unit:    detail.Unit,
		HP:      round1(b.HP),
		HPMax:   round1(b.HPMax),
		Users:   b.UsersAffected,
		URL:     detail.URL,
	}
}

func (s *Service) emit(ctx context.Context, kind, dedupe string, b core.Boss, payload eventPayload) (*core.Drop, error) {
	if s.Ingest == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("boss payload: %w", err)
	}
	now := s.now()
	return s.Ingest.Ingest(ctx, core.Event{
		Source:     eventSource,
		Kind:       kind,
		App:        b.App,
		OccurredAt: now,
		ObservedAt: now,
		Day:        core.DayOf(now),
		DedupeKey:  dedupe,
		Payload:    raw,
	})
}

// awardSpawn mints the cursed "a boss appears" drop. It is worth no XP: the
// crash is the news, and paying you for it would be a strange thing to do.
func (s *Service) awardSpawn(ctx context.Context, b core.Boss) error {
	p := s.basePayload(b)
	p.Detail = spawnDetailLine(b)
	_, err := s.emit(ctx, KindSpawn, spawnDedupePfx+b.ID, b, p)
	return err
}

// awardEnrage says, once, that the fight got worse. Once is the whole design:
// a boss that climbs for five days running should not produce five cursed
// sounds, because the fifth one is just noise about a thing you already know.
func (s *Service) awardEnrage(ctx context.Context, b core.Boss) error {
	p := s.basePayload(b)
	p.Detail = enrageDetailLine(b)
	_, err := s.emit(ctx, KindEnrage, enrageDedupePfx+b.ID, b, p)
	return err
}

// awardSlain mints the kill. It is the loudest drop in Loot that is not a
// legendary sale, and that is the point: the tedious work of chasing a crash
// down deserves the same fanfare as the fun work of making money.
func (s *Service) awardSlain(ctx context.Context, b *core.Boss, today string) error {
	detail := decodeDetail(b.Detail)
	end := today
	if b.SlainAt != nil {
		end = core.DayOf(*b.SlainAt)
	}
	days := core.DaysBetween(b.SpawnedDay, end)
	if days < 1 {
		days = 1
	}

	p := s.basePayload(*b)
	p.Days = days
	p.Scale = scaleOf(*b, days)
	p.Slayer = detail.Slayer
	p.Detail = slainDetailLine(*b, days)

	drop, err := s.emit(ctx, KindSlain, slainDedupePfx+b.ID, *b, p)
	if err != nil {
		return err
	}
	if drop == nil {
		return nil
	}
	b.XPAwarded = drop.XP
	return s.Store.SetBossXP(ctx, b.ID, drop.XP)
}

// ------------------------------------------------------------------ plumbing

// publish nudges connected browsers to refetch, exactly as quests and the
// casebook do. The board never rides the websocket.
func (s *Service) publish() {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(bus.Message{Type: "bosses"})
}

// changed drops the HTTP memo and then nudges, in that order — the other way
// round races the refetch against the cache it was meant to invalidate.
func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
	s.publish()
}
