package bosses_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/store"
)

const (
	testSource = "playvitals"
	testApp    = "com.example.app"
)

// The clock every test runs against. Fixing it is what makes "six days ago"
// mean the same thing in June as it does in December.
var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type harness struct {
	t     *testing.T
	ctx   context.Context
	store *store.Store
	pipe  *pipeline.Pipeline
	svc   *bosses.Service
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newHarness wires the real pipeline and the real default rules behind the
// boss service, so a test that asserts "the kill paid 1,500 XP" is asserting
// about the rules file rather than about a mock.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	pipe := pipeline.New(st, engine, nil, quiet())

	svc := bosses.NewService(st, pipe, nil, quiet())
	svc.Now = func() time.Time { return testNow }

	return &harness{t: t, ctx: ctx, store: st, pipe: pipe, svc: svc}
}

// day renders a day `back` days before the fixed clock.
func day(back int) string { return core.DayOf(testNow.AddDate(0, 0, -back)) }

// crash stores one silent crash row, exactly as a crash source would.
func (h *harness) crash(day, version string, count, users int) {
	h.t.Helper()
	h.crashKind(day, version, "", core.BossKindCrash, count, users)
}

func (h *harness) crashKind(day, version, issue, kind string, count, users int) {
	h.t.Helper()
	payload, err := json.Marshal(core.CrashPayload{
		Version:       version,
		IssueID:       issue,
		IssueTitle:    "NullPointerException in Widget.draw",
		UsersAffected: users,
		Kind:          kind,
		URL:           "https://example.test/issue",
	})
	if err != nil {
		h.t.Fatalf("encode payload: %v", err)
	}
	at, _ := time.Parse(core.DayLayout, day)
	h.ingest(core.Event{
		Source: testSource, Kind: core.KindCrash, App: testApp,
		OccurredAt: at, ObservedAt: at, Day: day, Quantity: count,
		DedupeKey: fmt.Sprintf("t:crash:%s:%s:%s:%s", day, version, issue, kind),
		Silent:    true, Payload: payload,
	})
}

// heartbeat stores the daily "I looked" row, which is what tells the detector
// a quiet day was quiet rather than missing.
func (h *harness) heartbeat(day string) {
	h.t.Helper()
	at, _ := time.Parse(core.DayLayout, day)
	h.ingest(core.Event{
		Source: testSource, Kind: core.KindCrashDay, App: testApp,
		OccurredAt: at, ObservedAt: at, Day: day,
		DedupeKey: "t:crash_day:" + day, Silent: true,
	})
}

// resolved stores the "somebody closed it upstream" signal.
func (h *harness) resolved(day, version string) {
	h.t.Helper()
	payload, _ := json.Marshal(core.CrashPayload{Version: version})
	at, _ := time.Parse(core.DayLayout, day)
	h.ingest(core.Event{
		Source: testSource, Kind: core.KindCrashResolved, App: testApp,
		OccurredAt: at, ObservedAt: at, Day: day,
		DedupeKey: "t:resolved:" + day + ":" + version, Silent: true, Payload: payload,
	})
}

func (h *harness) ingest(ev core.Event) {
	h.t.Helper()
	if _, err := h.pipe.Ingest(h.ctx, ev); err != nil {
		h.t.Fatalf("ingest: %v", err)
	}
}

// background lays down the ordinary past: a couple of crashes a day, every
// day, from `from` days ago through `to` days ago. It is what gives a spawn a
// baseline to break away from.
func (h *harness) background(from, to, perDay int) {
	h.t.Helper()
	for i := from; i >= to; i-- {
		h.heartbeat(day(i))
		if perDay > 0 {
			h.crash(day(i), "1.0.0", perDay, 1)
		}
	}
}

func (h *harness) evaluate() bosses.Result {
	h.t.Helper()
	res, err := h.svc.Evaluate(h.ctx)
	if err != nil {
		h.t.Fatalf("evaluate: %v", err)
	}
	return res
}

func (h *harness) board() bosses.Board {
	h.t.Helper()
	b, err := h.svc.List(h.ctx)
	if err != nil {
		h.t.Fatalf("list: %v", err)
	}
	return b
}

// dropsOfKind counts the drops minted for one of Loot's own boss events.
func (h *harness) dropsOfKind(kind string) []core.Drop {
	h.t.Helper()
	rows, err := h.store.DB().QueryContext(h.ctx, `
        SELECT d.rarity, d.title, d.xp FROM drops d
        JOIN events e ON e.id = d.event_id
        WHERE e.kind = ? ORDER BY d.id`, kind)
	if err != nil {
		h.t.Fatalf("query drops: %v", err)
	}
	defer rows.Close()

	var out []core.Drop
	for rows.Next() {
		var d core.Drop
		if err := rows.Scan(&d.Rarity, &d.Title, &d.XP); err != nil {
			h.t.Fatalf("scan drop: %v", err)
		}
		out = append(out, d)
	}
	return out
}

// ------------------------------------------------------------------- spawning

// The floor is what stops an app that crashes twice a day from acquiring a
// boss the first time it crashes eight times.
func TestSpawnNeedsTheFloorAsWellAsTheRatio(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 12, 4) // six times the baseline, and still nothing
	h.heartbeat(day(2))
	h.heartbeat(day(1))

	if res := h.evaluate(); len(res.Spawned) != 0 {
		t.Fatalf("spawned %d bosses for a twelve-crash day", len(res.Spawned))
	}
}

func TestSpawnOnASpike(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 90)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 260, 80)
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 210, 70)

	res := h.evaluate()
	if len(res.Spawned) != 1 {
		t.Fatalf("spawned %d bosses, want 1", len(res.Spawned))
	}
	b := res.Spawned[0]
	if b.HPMax != 300 {
		t.Errorf("hp_max = %v, want 300", b.HPMax)
	}
	if b.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", b.Version)
	}
	if b.Name == "" || b.Name != bosses.Name(b.Key, b.Version, core.BossKindCrash) {
		t.Errorf("name %q is not the generated name for its key", b.Name)
	}

	// The spawn is cursed and pays nothing: it is news, not a punishment.
	drops := h.dropsOfKind(bosses.KindSpawn)
	if len(drops) != 1 {
		t.Fatalf("minted %d spawn drops, want 1", len(drops))
	}
	if drops[0].Rarity != core.Cursed {
		t.Errorf("spawn rarity = %s, want cursed", drops[0].Rarity)
	}
	if drops[0].XP != 0 {
		t.Errorf("spawn paid %d XP, want 0", drops[0].XP)
	}
}

// Fifty distinct people hit by one crash in a day is a boss whatever the ratio
// says.
func TestSpawnOnUsersAffected(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 1)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 14, 60) // under the floor, over the people line
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 12, 55)
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 11, 50)

	res := h.evaluate()
	if len(res.Spawned) != 1 {
		t.Fatalf("spawned %d bosses, want 1", len(res.Spawned))
	}
	if got := res.Spawned[0].UsersAffected; got != 60 {
		t.Errorf("users_affected = %d, want 60", got)
	}
}

// Connecting a crash source to a healthy app must not immediately hand it a
// monster for its ordinary Tuesday.
func TestNoSpawnWithoutHistory(t *testing.T) {
	h := newHarness(t)
	for i := 3; i >= 1; i-- {
		h.heartbeat(day(i))
		h.crash(day(i), "2.0.0", 300, 90)
	}
	if res := h.evaluate(); len(res.Spawned) != 0 {
		t.Fatalf("spawned %d bosses on three days of history", len(res.Spawned))
	}
}

// An explicit `boss: true` from a crash reporter beats the baseline: a
// Crashlytics velocity alert knows something Loot's counts do not.
func TestForcedSpawnSkipsTheBaseline(t *testing.T) {
	h := newHarness(t)
	payload, _ := json.Marshal(core.CrashPayload{Version: "9.9.9", Kind: core.BossKindCrash, Boss: true})
	at, _ := time.Parse(core.DayLayout, day(2))
	h.ingest(core.Event{
		Source: "crash", Kind: core.KindCrash, App: "relayed.app",
		OccurredAt: at, ObservedAt: at, Day: day(2), Quantity: 4,
		DedupeKey: "t:forced", Silent: true, Payload: payload,
	})

	res := h.evaluate()
	if len(res.Spawned) != 1 {
		t.Fatalf("spawned %d bosses, want 1 (forced)", len(res.Spawned))
	}
	if res.Spawned[0].HPMax != 4 {
		t.Errorf("hp_max = %v, want 4", res.Spawned[0].HPMax)
	}
}

// The whole pass is idempotent: it re-reads the same fortnight every hour.
func TestEvaluateIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 90)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 250, 80)
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 200, 70)

	for i := 0; i < 5; i++ {
		res := h.evaluate()
		if i > 0 && res.Changed() {
			t.Fatalf("pass %d changed something: %+v", i, res)
		}
	}
	if n := len(h.board().Alive); n != 1 {
		t.Fatalf("%d bosses alive after five passes, want 1", n)
	}
	if n := len(h.dropsOfKind(bosses.KindSpawn)); n != 1 {
		t.Fatalf("minted %d spawn drops over five passes, want 1", n)
	}
}

// -------------------------------------------------------------------- fighting

func TestHPDrainsAsTheFixRollsOut(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 90)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 150, 45)
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 90, 30)

	h.evaluate()
	alive := h.board().Alive
	if len(alive) != 1 {
		t.Fatalf("%d bosses alive, want 1", len(alive))
	}
	b := alive[0]
	if b.HP != 90 {
		t.Errorf("hp = %v, want 90 (the newest completed day)", b.HP)
	}
	if b.HPMax != 300 {
		t.Errorf("hp_max = %v, want 300", b.HPMax)
	}
	if got := int(b.DownPct*100 + 0.5); got != 70 {
		t.Errorf("down_pct = %d%%, want 70%%", got)
	}
	if b.DaysAlive != 4 {
		t.Errorf("days_alive = %d, want 4", b.DaysAlive)
	}
}

// A fight that gets worse enrages — once, and never past 1.5×, because beyond
// that the bar has stopped being a health bar and started being a graph.
func TestEnrageIsCappedAndSaidOnce(t *testing.T) {
	h := newHarness(t)
	h.background(40, 5, 2)
	h.heartbeat(day(4))
	h.crash(day(4), "2.0.0", 100, 30)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 400, 120)
	h.evaluate()

	b := h.board().Alive[0]
	if b.HP != 150 {
		t.Errorf("hp = %v, want 150 (capped at 1.5 × hp_max)", b.HP)
	}
	if !b.Enraged {
		t.Error("boss did not record the enrage")
	}
	if b.Pct != 1 {
		t.Errorf("pct = %v, want a full bar while enraged", b.Pct)
	}

	// Worse again, on another day: still one enrage drop.
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 500, 150)
	h.evaluate()
	if n := len(h.dropsOfKind(bosses.KindEnrage)); n != 1 {
		t.Fatalf("minted %d enrage drops, want exactly 1", n)
	}
	if drops := h.dropsOfKind(bosses.KindEnrage); drops[0].XP != 0 {
		t.Errorf("enrage paid %d XP, want 0", drops[0].XP)
	}
}

// --------------------------------------------------------------------- dying

func TestSlainByTwoQuietDays(t *testing.T) {
	h := newHarness(t)
	h.background(40, 5, 2)
	h.heartbeat(day(4))
	h.crash(day(4), "2.0.0", 200, 60)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 60, 20)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 15, 5) // under a tenth of 200
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 4, 2) // and again

	res := h.evaluate()
	if len(res.Slain) != 1 {
		t.Fatalf("slew %d bosses, want 1", len(res.Slain))
	}
	if n := len(h.board().Alive); n != 0 {
		t.Fatalf("%d bosses still alive after the kill", n)
	}

	drops := h.dropsOfKind(bosses.KindSlain)
	if len(drops) != 1 {
		t.Fatalf("minted %d kill drops, want 1", len(drops))
	}
	// hp_max 200 and four days: a good fight, not an epic saga.
	if drops[0].Rarity != core.Epic {
		t.Errorf("kill rarity = %s, want epic", drops[0].Rarity)
	}
	if drops[0].XP != 500 {
		t.Errorf("kill paid %d XP, want 500", drops[0].XP)
	}
	if got := h.board().Recent[0].XPAwarded; got != 500 {
		t.Errorf("boss row records %d XP, want 500", got)
	}
}

// A big fight, or a long one, is legendary.
func TestBigKillIsLegendary(t *testing.T) {
	h := newHarness(t)
	h.background(40, 5, 4)
	h.heartbeat(day(4))
	h.crash(day(4), "2.0.0", 900, 600)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 200)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 40, 12)
	h.heartbeat(day(1))
	h.crash(day(1), "2.0.0", 10, 3)

	h.evaluate()
	drops := h.dropsOfKind(bosses.KindSlain)
	if len(drops) != 1 {
		t.Fatalf("minted %d kill drops, want 1", len(drops))
	}
	if drops[0].Rarity != core.Legendary {
		t.Errorf("kill rarity = %s, want legendary", drops[0].Rarity)
	}
	if drops[0].XP != 1500 {
		t.Errorf("kill paid %d XP, want 1500", drops[0].XP)
	}
}

// Closing the issue upstream kills the boss without waiting two days for the
// graph to agree. The graph is slower than you are.
func TestSlainByResolvedSignal(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 90)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 280, 85)
	h.evaluate()
	if len(h.board().Alive) != 1 {
		t.Fatal("boss did not spawn")
	}

	h.resolved(day(1), "2.0.0")
	res := h.evaluate()
	if len(res.Slain) != 1 {
		t.Fatalf("slew %d bosses on the resolved signal, want 1", len(res.Slain))
	}
	if hp := h.board().Recent[0].HP; hp != 0 {
		t.Errorf("hp = %v after a resolution, want 0", hp)
	}
}

func TestManualSlay(t *testing.T) {
	h := newHarness(t)
	h.background(40, 4, 2)
	h.heartbeat(day(3))
	h.crash(day(3), "2.0.0", 300, 90)
	h.heartbeat(day(2))
	h.crash(day(2), "2.0.0", 290, 88)
	h.evaluate()

	alive := h.board().Alive
	if len(alive) != 1 {
		t.Fatal("boss did not spawn")
	}
	slain, err := h.svc.Slay(h.ctx, alive[0].ID)
	if err != nil {
		t.Fatalf("slay: %v", err)
	}
	if slain.Status != core.BossSlain {
		t.Fatalf("status = %s, want slain", slain.Status)
	}
	if slain.XPAwarded == 0 {
		t.Error("a manual kill paid no XP")
	}

	// A double click must not mint a second drop.
	if _, err := h.svc.Slay(h.ctx, alive[0].ID); err != nil {
		t.Fatalf("second slay: %v", err)
	}
	if n := len(h.dropsOfKind(bosses.KindSlain)); n != 1 {
		t.Fatalf("minted %d kill drops for two clicks, want 1", n)
	}
}

// A source that stops reporting is a fact about the source. The boss closes
// quietly: no drop, no XP, no blame.
func TestFadesWhenTheSourceGoesSilent(t *testing.T) {
	h := newHarness(t)
	h.background(40, 21, 2)
	h.heartbeat(day(20))
	h.crash(day(20), "2.0.0", 300, 90)
	h.heartbeat(day(19))
	h.crash(day(19), "2.0.0", 280, 85)
	// …and then nothing at all, for three weeks.

	// The spike is inside the detection window on the first pass only if we
	// look while it is fresh, so spawn it from a clock that can still see it.
	early := *h.svc
	early.Now = func() time.Time { return testNow.AddDate(0, 0, -18) }
	if _, err := early.Evaluate(h.ctx); err != nil {
		t.Fatalf("early evaluate: %v", err)
	}
	if len(h.board().Alive) != 1 {
		t.Fatal("boss did not spawn while the spike was fresh")
	}

	res := h.evaluate()
	if len(res.Faded) != 1 {
		t.Fatalf("faded %d bosses, want 1", len(res.Faded))
	}
	if n := len(h.dropsOfKind(bosses.KindSlain)); n != 0 {
		t.Errorf("a faded boss minted %d kill drops, want 0", n)
	}
	recent := h.board().Recent
	if len(recent) != 1 || recent[0].Status != core.BossFaded {
		t.Fatalf("recent = %+v, want one faded boss", recent)
	}
}

// A crash that goes quiet while the *app* keeps reporting is a kill, not a
// fade: the source is fine, the crash is gone.
func TestQuietFightWithALiveSourceIsAKill(t *testing.T) {
	h := newHarness(t)
	h.background(40, 5, 2)
	h.heartbeat(day(4))
	h.crash(day(4), "2.0.0", 200, 60)
	// The app keeps crashing a little on the old version; the bad one stops.
	h.background(3, 1, 2)

	res := h.evaluate()
	if len(res.Slain) != 1 {
		t.Fatalf("slew %d bosses, want 1", len(res.Slain))
	}
}
