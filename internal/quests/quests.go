// Package quests turns Loot's history into goals worth chasing: a small board
// of active quests, each one a metric, a target and a window of days.
//
// Two rules shape everything here, and both come from the same place —
// dashboards that nag are dashboards people close:
//
//   - **A quest never fails.** When a window ends unmet the quest quietly
//     becomes part of history, rendered as a neutral "ended · 62%". There is
//     no streak to break, no red, no "you missed it", and no event: expiry is
//     the only state change in Loot that makes no sound at all.
//   - **Completion is a real drop.** Finishing one ingests a `quest_complete`
//     event through the same pipeline everything else uses, so it lands in the
//     feed with a rarity, a sound and XP, exactly like a new subscriber.
package quests

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// Source and kinds of the events Loot mints for itself.
const (
	eventSource        = "loot"
	KindQuestComplete  = "quest_complete"
	completeDedupePfx  = "loot:quest_complete:"
	checkInterval      = time.Minute
	generateAfterMin   = 5 // generate the day's quests at 00:05 local
	defaultRecentLimit = 20
)

// Ingester is the slice of the pipeline this package needs: one event in, one
// drop out. Keeping it an interface is what stops quests and the pipeline from
// importing each other.
type Ingester interface {
	Ingest(ctx context.Context, ev core.Event) (*core.Drop, error)
}

// Publisher is the slice of the bus this package needs.
type Publisher interface {
	Publish(msg bus.Message)
}

// Board is the whole of GET /api/quests.
type Board struct {
	// Active is what is still running, soonest deadline first.
	Active []core.Quest `json:"active"`
	// Recent is the last few completed and ended quests, newest first.
	Recent []core.Quest `json:"recent"`
}

// Service keeps the board honest: it measures progress, completes quests that
// reach their target, retires quests whose window has passed, and asks the
// generator for new ones once a day.
type Service struct {
	Store     *store.Store
	Ingest    Ingester
	Bus       Publisher
	Logger    *slog.Logger
	Generator *Generator
	// Now is the clock, in *local* time: windows are weeks and months as the
	// person reading them lives them, not as UTC has them.
	Now func() time.Time
	// ReadyWhen delays the very first generation until it is closed. `loot
	// serve` hands it the scheduler's FirstRoundDone, so quests are written
	// against the history a first run has actually fetched rather than against
	// the empty database that exists a second after boot.
	//
	// It matters more than it sounds. Generating first meant the App Store
	// backfill that landed forty seconds later *completed* fresh quests
	// instantly: "Settle 2 new countries" paid out an epic drop, a sound and
	// XP for countries settled months ago. A quest you were handed already
	// finished is not a quest, it is a bug with confetti on it.
	//
	// nil means "generate immediately", which is right for demo mode (whose
	// history is seeded before the service starts) and for tests.
	ReadyWhen <-chan struct{}

	trigger chan struct{}
	lastGen string
}

// NewService returns a service with a generator wired to the same store.
func NewService(st *store.Store, ingest Ingester, b Publisher, displayCurrency string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	now := func() time.Time { return time.Now() }
	return &Service{
		Store:  st,
		Ingest: ingest,
		Bus:    b,
		Logger: log,
		Generator: &Generator{
			Store:           st,
			Logger:          log,
			DisplayCurrency: displayCurrency,
			Now:             now,
		},
		Now:     now,
		trigger: make(chan struct{}, 1),
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

// today is the local business day quests are measured against.
func (s *Service) today() string { return s.now().Format(core.DayLayout) }

// Trigger asks for a check as soon as possible without blocking the caller.
// The pipeline calls it after every ingest: a quest that just crossed its
// target should pay out while the drop that pushed it over is still on screen.
func (s *Service) Trigger() {
	if s.trigger == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default: // a check is already pending; one is enough
	}
}

// Run keeps the board current until ctx is cancelled. It blocks.
//
// It checks progress every minute (and immediately whenever the pipeline
// nudges it), and generates the day's quests once a day just after midnight.
func (s *Service) Run(ctx context.Context) {
	if s.ReadyWhen != nil {
		select {
		case <-ctx.Done():
			return
		case <-s.ReadyWhen:
		}
	}
	if err := s.Startup(ctx); err != nil {
		s.log().Error("quest startup failed", "error", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Generation retires the closed windows itself before it counts
			// the board, so this order is safe: the new week's quests are
			// created and then measured by the Check below, in one pass.
			s.maybeGenerate(ctx)
		case <-s.trigger:
		}
		if _, err := s.Check(ctx); err != nil {
			s.log().Error("quest check failed", "error", err)
		}
	}
}

// Startup generates whatever the current windows are missing and then checks
// progress once. `loot serve` calls it before Run so a fresh boot has a board
// immediately, and demo mode calls it after seeding.
func (s *Service) Startup(ctx context.Context) error {
	created, err := s.Generator.Run(ctx)
	if err != nil {
		return err
	}
	if len(created) > 0 {
		s.log().Info("quests generated", "count", len(created))
	}
	s.lastGen = s.today()
	if _, err := s.Check(ctx); err != nil {
		return err
	}
	if len(created) > 0 {
		s.publish()
	}
	return nil
}

// maybeGenerate runs the generator the first time a new local day is a few
// minutes old, so a week's quests appear while nobody is looking.
func (s *Service) maybeGenerate(ctx context.Context) {
	now := s.now()
	day := now.Format(core.DayLayout)
	if day == s.lastGen {
		return
	}
	if now.Hour() == 0 && now.Minute() < generateAfterMin {
		return
	}
	s.lastGen = day

	created, err := s.Generator.Run(ctx)
	if err != nil {
		s.log().Error("quest generation failed", "error", err)
		return
	}
	if len(created) > 0 {
		s.log().Info("quests generated", "count", len(created), "day", day)
		s.publish()
	}
}

// CheckResult reports what one pass over the active quests changed.
type CheckResult struct {
	Checked   int
	Completed []core.Quest
	Expired   []core.Quest
}

// Check measures every active quest, completes the ones that have reached
// their target and retires the ones whose window has passed.
//
// Completion is where the reward lives: the quest is marked completed first
// (an UPDATE guarded on "still active", so two racing checks cannot both win)
// and only then is the `quest_complete` event ingested, keyed on the quest id
// so even a restart mid-flight cannot mint two drops.
func (s *Service) Check(ctx context.Context) (CheckResult, error) {
	var res CheckResult

	active, err := s.Store.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		return res, err
	}

	now := s.now()
	for _, q := range active {
		value, err := s.Store.QuestProgress(ctx, q)
		if err != nil {
			s.log().Error("quest progress failed", "error", err, "quest", q.ID, "metric", q.Metric)
			continue
		}
		res.Checked++
		if err := s.Store.SaveQuestProgress(ctx, q.ID, value, now.UTC()); err != nil {
			s.log().Error("quest progress save failed", "error", err, "quest", q.ID)
			continue
		}
		if value < q.Target {
			continue
		}

		done, err := s.Store.CompleteQuest(ctx, q.ID, value, now.UTC())
		if err != nil {
			s.log().Error("quest completion failed", "error", err, "quest", q.ID)
			continue
		}
		if !done {
			continue
		}
		q.Value = value
		q.Status = core.QuestCompleted
		completedAt := now.UTC()
		q.CompletedAt = &completedAt
		q.Pct = 1

		xp, err := s.award(ctx, q)
		if err != nil {
			s.log().Error("quest reward failed", "error", err, "quest", q.ID)
		} else {
			q.XP = xp
		}
		s.log().Info("quest complete", "title", q.Title, "metric", q.Metric,
			"value", value, "target", q.Target, "xp", q.XP)
		res.Completed = append(res.Completed, q)
	}

	expired, err := s.Store.ExpireQuests(ctx, s.today(), now.UTC())
	if err != nil {
		return res, err
	}
	// Deliberately quiet: an expiry is logged at debug, produces no event, no
	// drop and no sound. It is a fact, not a verdict.
	for _, q := range expired {
		s.log().Debug("quest ended", "title", q.Title, "pct", q.Pct)
	}
	res.Expired = expired

	if len(res.Completed) > 0 || len(res.Expired) > 0 {
		s.publish()
	}
	return res, nil
}

// questCompletePayload is what the completion event carries. `scope` is what
// the rules file matches on to make a month's quest epic rather than rare, and
// `detail` is a pre-formatted subtitle so the rule needs no arithmetic.
type questCompletePayload struct {
	QuestID string  `json:"quest_id"`
	Title   string  `json:"title"`
	Metric  string  `json:"metric"`
	Target  float64 `json:"target"`
	Value   float64 `json:"value"`
	Scope   string  `json:"scope"`
	Detail  string  `json:"detail"`
}

// award ingests the completion event and returns the XP its drop paid.
func (s *Service) award(ctx context.Context, q core.Quest) (int, error) {
	if s.Ingest == nil {
		return 0, nil
	}
	currency := ""
	if s.Generator != nil {
		currency = s.Generator.DisplayCurrency
	}
	payload, err := json.Marshal(questCompletePayload{
		QuestID: q.ID,
		Title:   q.Title,
		Metric:  string(q.Metric),
		Target:  q.Target,
		Value:   q.Value,
		Scope:   q.Scope(),
		Detail: fmt.Sprintf("%s · %s of %s", q.Metric.Label(),
			FormatValue(q.Metric, q.Value, currency), FormatValue(q.Metric, q.Target, currency)),
	})
	if err != nil {
		return 0, fmt.Errorf("quest payload: %w", err)
	}

	// Timestamps are UTC, as every stored instant is — but the *day* is the
	// local one. A quest window is a week or a month as the person reading it
	// lives them (see WeekWindow), and progress is measured over `day BETWEEN
	// window_start AND window_end`. Stamping the completion with the UTC day
	// would file a quest finished at 9pm in Auckland on tomorrow's business
	// day, outside the very window it just satisfied — so the XP it paid would
	// not count towards the XP quest running alongside it.
	now := s.now().UTC()
	drop, err := s.Ingest.Ingest(ctx, core.Event{
		Source:     eventSource,
		Kind:       KindQuestComplete,
		App:        q.App,
		OccurredAt: now,
		ObservedAt: now,
		Day:        s.today(),
		DedupeKey:  completeDedupePfx + q.ID,
		Payload:    payload,
	})
	if err != nil {
		return 0, err
	}
	if drop == nil {
		return 0, nil
	}
	if err := s.Store.SetQuestXP(ctx, q.ID, drop.XP); err != nil {
		return drop.XP, err
	}
	return drop.XP, nil
}

// publish tells connected browsers the board changed. It is a bare
// `{"type":"quests"}` nudge — the page refetches, exactly as it does for
// `chest` — so the websocket never carries a second copy of the board.
func (s *Service) publish() {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(bus.Message{Type: "quests"})
}

// List returns the board: what is running, and what recently finished or
// ended. Progress is read fresh for active quests so the page is never a
// minute behind the feed beside it.
func (s *Service) List(ctx context.Context) (Board, error) {
	return s.ListScoped(ctx, "")
}

// ListScoped is List narrowed to one product: that product's own quests plus
// the realm-wide ones the generator writes, which belong to every scope
// because they are about everything you ship. An empty product is List.
//
// Progress is *not* re-scoped: a quest measures whatever its own App says, and
// a realm-wide quest measures the realm even when the board it appears on is
// scoped. Narrowing its progress would change the goal depending on which tab
// you were looking at, which is not a goal at all.
func (s *Service) ListScoped(ctx context.Context, product string) (Board, error) {
	board := Board{Active: []core.Quest{}, Recent: []core.Quest{}}
	scoped := s.Store.Scoped(product)

	active, err := scoped.ListQuests(ctx, store.QuestQuery{Statuses: []string{core.QuestActive}})
	if err != nil {
		return board, err
	}
	today := s.today()
	for _, q := range active {
		if value, err := s.Store.QuestProgress(ctx, q); err == nil {
			q.Value = value
			q.Pct = core.QuestPct(value, q.Target)
		}
		q.DaysLeft = DaysLeft(today, q.WindowEnd)
		board.Active = append(board.Active, q)
	}

	recent, err := scoped.ListQuests(ctx, store.QuestQuery{
		Statuses: []string{core.QuestCompleted, core.QuestExpired},
		Limit:    defaultRecentLimit,
		Newest:   true,
	})
	if err != nil {
		return board, err
	}
	board.Recent = recent
	return board, nil
}

// DaysLeft counts today as a day left, and returns 0 once the window is over.
func DaysLeft(today, windowEnd string) int {
	t, err := time.Parse(core.DayLayout, today)
	if err != nil {
		return 0
	}
	end, err := time.Parse(core.DayLayout, windowEnd)
	if err != nil {
		return 0
	}
	days := int(end.Sub(t).Hours()/24) + 1
	if days < 0 {
		return 0
	}
	return days
}

// CustomRequest is the body of POST /api/quests.
type CustomRequest struct {
	Metric core.Metric
	Target float64
	App    string
	Source string
	// Window is "week", "month", or explicit inclusive days.
	Window string
	Start  string
	End    string
	Title  string
}

// Create adds a quest you set yourself. Custom quests never collide with
// generated ones: their dedupe key is their own id.
func (s *Service) Create(ctx context.Context, req CustomRequest) (core.Quest, error) {
	if !req.Metric.Valid() {
		return core.Quest{}, fmt.Errorf("unknown metric %q", req.Metric)
	}
	if req.Target <= 0 {
		return core.Quest{}, fmt.Errorf("target must be greater than zero")
	}

	start, end, err := resolveWindow(s.now(), req)
	if err != nil {
		return core.Quest{}, err
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		currency := ""
		if s.Generator != nil {
			currency = s.Generator.DisplayCurrency
		}
		title = fmt.Sprintf("%s %s", FormatValue(req.Metric, req.Target, currency), req.Metric.Label())
		if req.App != "" {
			title += " · " + req.App
		}
	}

	q := core.Quest{
		ID:          core.NewID(),
		Kind:        core.QuestCustom,
		Metric:      req.Metric,
		Target:      req.Target,
		App:         strings.TrimSpace(req.App),
		Source:      strings.TrimSpace(req.Source),
		WindowStart: start,
		WindowEnd:   end,
		Title:       title,
		Status:      core.QuestActive,
		CreatedAt:   s.now().UTC(),
	}
	q.DedupeKey = "custom:" + q.ID

	if _, err := s.Store.InsertQuest(ctx, q); err != nil {
		return core.Quest{}, err
	}
	if value, err := s.Store.QuestProgress(ctx, q); err == nil {
		q.Value = value
		q.Pct = core.QuestPct(value, q.Target)
		_ = s.Store.SaveQuestProgress(ctx, q.ID, value, s.now().UTC())
	}
	q.DaysLeft = DaysLeft(s.today(), q.WindowEnd)
	s.publish()
	// A quest that is already met the moment it is set should pay out now
	// rather than in a minute's time.
	s.Trigger()
	return q, nil
}

// resolveWindow turns a request's window into two inclusive days.
func resolveWindow(now time.Time, req CustomRequest) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(req.Window)) {
	case "", "week":
		start, end := WeekWindow(now)
		return start, end, nil
	case "month":
		start, end := MonthWindow(now)
		return start, end, nil
	case "custom", "range", "days":
		// fall through to the explicit days below
	default:
		return "", "", fmt.Errorf("unknown window %q (use week, month or explicit days)", req.Window)
	}

	start, err := time.Parse(core.DayLayout, strings.TrimSpace(req.Start))
	if err != nil {
		return "", "", fmt.Errorf("window start must be YYYY-MM-DD")
	}
	end, err := time.Parse(core.DayLayout, strings.TrimSpace(req.End))
	if err != nil {
		return "", "", fmt.Errorf("window end must be YYYY-MM-DD")
	}
	if end.Before(start) {
		return "", "", fmt.Errorf("window ends before it starts")
	}
	return start.Format(core.DayLayout), end.Format(core.DayLayout), nil
}

// Delete removes a custom quest. Generated quests are not deletable — they
// expire on their own, and deleting one would only bring it back tomorrow.
func (s *Service) Delete(ctx context.Context, id string) error {
	q, err := s.Store.GetQuest(ctx, id)
	if err != nil {
		return err
	}
	if q.Kind != core.QuestCustom {
		return fmt.Errorf("only custom quests can be deleted")
	}
	if err := s.Store.DeleteQuest(ctx, id); err != nil {
		return err
	}
	s.publish()
	return nil
}

// Window semantics, in one place because two clocks meet here.
//
// A quest window is a pair of inclusive *local* days: the week and the month as
// the person reading the board lives them, not as UTC has them. Progress is
// then measured with `events.day BETWEEN window_start AND window_end`, and
// `events.day` is whatever business day each source filed a row under — a
// store's own reporting day for a sale, and the local day for anything Loot
// mints for itself. The two agree because both are days rather than instants;
// what must never happen is a UTC *timestamp* being turned into a day here,
// which would put an evening completion in a timezone ahead of UTC outside its
// own window. See award.

// WeekWindow returns the Monday..Sunday window containing t, in local days.
func WeekWindow(t time.Time) (string, string) {
	offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -offset)
	return start.Format(core.DayLayout), start.AddDate(0, 0, 6).Format(core.DayLayout)
}

// MonthWindow returns the first..last day of the month containing t.
func MonthWindow(t time.Time) (string, string) {
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	end := start.AddDate(0, 1, -1)
	return start.Format(core.DayLayout), end.Format(core.DayLayout)
}
