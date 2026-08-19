package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func crashEvent(st *store.Store, t *testing.T, source, app, day, version, issue string, count, users int) {
	t.Helper()
	payload, err := json.Marshal(core.CrashPayload{
		Version: version, IssueID: issue, IssueTitle: "Boom", UsersAffected: users,
		Kind: core.BossKindCrash, URL: "https://example.test/i",
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	at, _ := time.Parse(core.DayLayout, day)
	if _, err := st.InsertEvent(context.Background(), core.Event{
		ID: core.NewID(), Source: source, Kind: core.KindCrash, App: app,
		OccurredAt: at, ObservedAt: at, Day: day, Quantity: count,
		DedupeKey: source + ":" + app + ":" + day + ":" + version + ":" + issue + ":" + core.NewID(),
		Silent:    true, Payload: payload,
	}); err != nil {
		t.Fatalf("insert crash: %v", err)
	}
}

// A day's total is the *sum* of its rows, which is what lets a crash reporter
// post deltas as often as it likes.
func TestCrashSeriesGroupsAndSums(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "", 100, 30)
	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "", 20, 8)
	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.1.0", "", 5, 2)
	crashEvent(st, t, "playvitals", "app", "2026-05-31", "4.2.0", "", 60, 19)

	series, err := st.CrashSeries(ctx, "2026-05-01", "2026-05-31")
	if err != nil {
		t.Fatalf("crash series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("%d fights, want 2 (one per version)", len(series))
	}

	key := store.BossSeriesKey{Source: "playvitals", App: "app", Version: "4.2.0", Kind: core.BossKindCrash}
	days := series[key]
	if len(days) != 2 {
		t.Fatalf("%d days for 4.2.0, want 2", len(days))
	}
	if days[0].Crashes != 120 {
		t.Errorf("day one = %v crashes, want 120 (two rows summed)", days[0].Crashes)
	}
	if days[0].UsersAffected != 30 {
		t.Errorf("users = %d, want the largest row's 30", days[0].UsersAffected)
	}
	if days[0].Title != "Boom" || days[0].URL == "" {
		t.Errorf("day lost its title/url: %+v", days[0])
	}
	if key.Key() != core.BossKey("playvitals", "app", "4.2.0", "", core.BossKindCrash) {
		t.Errorf("series key renders as %q", key.Key())
	}
}

// A crash and an ANR are different bugs with different fixes, so they are
// different series — and the crash series keeps the key it has always had, so
// no live boss is orphaned from its own numbers.
func TestCrashSeriesSplitsCrashesFromANRs(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	crashKindEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", core.BossKindCrash, 100)
	crashKindEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", core.BossKindANR, 40)
	// A row that names no kind at all is a crash, not a third series.
	crashKindEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "", 5)
	// And so is one that shouts it.
	crashKindEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "ANR", 2)

	series, err := st.CrashSeries(ctx, "2026-05-01", "2026-05-31")
	if err != nil {
		t.Fatalf("crash series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("%d fights, want 2 (crashes and ANRs):\n%+v", len(series), series)
	}

	crashes := series[store.BossSeriesKey{Source: "playvitals", App: "app", Version: "4.2.0", Kind: core.BossKindCrash}]
	anrs := series[store.BossSeriesKey{Source: "playvitals", App: "app", Version: "4.2.0", Kind: core.BossKindANR}]
	if len(crashes) != 1 || crashes[0].Crashes != 105 {
		t.Errorf("crash series = %+v, want one day of 105", crashes)
	}
	if len(anrs) != 1 || anrs[0].Crashes != 42 {
		t.Errorf("anr series = %+v, want one day of 42", anrs)
	}

	crashKey := store.BossSeriesKey{Source: "playvitals", App: "app", Version: "4.2.0", Kind: core.BossKindCrash}
	if got := crashKey.Key(); got != "playvitals:app:4.2.0" {
		t.Errorf("crash key = %q, want the unchanged %q", got, "playvitals:app:4.2.0")
	}
	anrKey := store.BossSeriesKey{Source: "playvitals", App: "app", Version: "4.2.0", Kind: core.BossKindANR}
	if got := anrKey.Key(); got != "playvitals:app:4.2.0|anr" {
		t.Errorf("anr key = %q", got)
	}
}

// crashKindEvent stores one crash row of a named kind.
func crashKindEvent(st *store.Store, t *testing.T, source, app, day, version, kind string, count int) {
	t.Helper()
	payload, err := json.Marshal(core.CrashPayload{Version: version, Kind: kind})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	at, _ := time.Parse(core.DayLayout, day)
	if _, err := st.InsertEvent(context.Background(), core.Event{
		ID: core.NewID(), Source: source, Kind: core.KindCrash, App: app,
		OccurredAt: at, ObservedAt: at, Day: day, Quantity: count,
		DedupeKey: source + ":" + day + ":" + kind + ":" + core.NewID(),
		Silent:    true, Payload: payload,
	}); err != nil {
		t.Fatalf("insert crash: %v", err)
	}
}

// The baseline is app-wide: a brand new issue has no history of its own, and
// "three times what this app usually does" is the question worth asking.
func TestCrashTotalsAreAppWide(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "", 100, 30)
	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.1.0", "", 5, 2)
	crashEvent(st, t, "sentry", "app", "2026-05-30", "", "abc", 7, 4)

	totals, err := st.CrashTotals(ctx, "2026-05-01", "2026-05-31")
	if err != nil {
		t.Fatalf("crash totals: %v", err)
	}
	if got := totals[store.SeriesKey{Source: "playvitals", App: "app"}]["2026-05-30"]; got != 105 {
		t.Errorf("playvitals total = %v, want 105", got)
	}
	// Sources are baselined apart: Sentry's counts mean something different
	// from Play's, and mixing them would make both meaningless.
	if got := totals[store.SeriesKey{Source: "sentry", App: "app"}]["2026-05-30"]; got != 7 {
		t.Errorf("sentry total = %v, want 7", got)
	}
}

// The heartbeat is what tells a quiet day apart from a missing one.
func TestCrashReportedDaysIncludeHeartbeats(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	crashEvent(st, t, "playvitals", "app", "2026-05-30", "4.2.0", "", 100, 30)
	at, _ := time.Parse(core.DayLayout, "2026-05-31")
	if _, err := st.InsertEvent(ctx, core.Event{
		ID: core.NewID(), Source: "playvitals", Kind: core.KindCrashDay, App: "app",
		OccurredAt: at, ObservedAt: at, Day: "2026-05-31", Quantity: 0,
		DedupeKey: "beat", Silent: true,
	}); err != nil {
		t.Fatalf("insert heartbeat: %v", err)
	}

	reported, err := st.CrashReportedDays(ctx, "2026-05-01", "2026-05-31")
	if err != nil {
		t.Fatalf("reported days: %v", err)
	}
	days := reported[store.SeriesKey{Source: "playvitals", App: "app"}]
	if len(days) != 2 || days[0] != "2026-05-30" || days[1] != "2026-05-31" {
		t.Fatalf("reported days = %v, want both days ascending", days)
	}
}

func TestCrashResolutionsAndForcedKeys(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	payload, _ := json.Marshal(core.CrashPayload{Version: "4.2.0", Kind: core.BossKindCrash})
	at, _ := time.Parse(core.DayLayout, "2026-05-31")
	if _, err := st.InsertEvent(ctx, core.Event{
		ID: core.NewID(), Source: "sentry", Kind: core.KindCrashResolved, App: "app",
		OccurredAt: at, ObservedAt: at, Day: "2026-05-31",
		DedupeKey: "res", Silent: true, Payload: payload,
	}); err != nil {
		t.Fatalf("insert resolution: %v", err)
	}

	resolved, err := st.CrashResolutions(ctx, "2026-05-01")
	if err != nil {
		t.Fatalf("resolutions: %v", err)
	}
	if got := resolved[core.BossKey("sentry", "app", "4.2.0", "", core.BossKindCrash)]; got != "2026-05-31" {
		t.Fatalf("resolutions = %v", resolved)
	}

	forced, _ := json.Marshal(core.CrashPayload{Version: "9.9", Kind: core.BossKindCrash, Boss: true})
	if _, err := st.InsertEvent(ctx, core.Event{
		ID: core.NewID(), Source: "crash", Kind: core.KindCrash, App: "app",
		OccurredAt: at, ObservedAt: at, Day: "2026-05-31", Quantity: 3,
		DedupeKey: "forced", Silent: true, Payload: forced,
	}); err != nil {
		t.Fatalf("insert forced: %v", err)
	}
	keys, err := st.ForcedBossKeys(ctx, "2026-05-01")
	if err != nil {
		t.Fatalf("forced keys: %v", err)
	}
	if got := keys[core.BossKey("crash", "app", "9.9", "", core.BossKindCrash)]; got != "2026-05-31" {
		t.Fatalf("forced keys = %v", keys)
	}
}

func sampleBoss(key string) core.Boss {
	return core.Boss{
		ID: core.NewID(), Key: key, Source: "playvitals", App: "app",
		Name: "The Segfaulting Hydra", Title: "Crashes in v4.2.0", Version: "4.2.0",
		HPMax: 300, HP: 300, UsersAffected: 90,
		SpawnedAt: time.Now().UTC(), SpawnedDay: "2026-05-28", PeakDay: "2026-05-28",
		Status: core.BossAlive, Detail: json.RawMessage(`{"unit":"crashes"}`),
	}
}

// One alive boss per key, forever: the key is unique across the whole table,
// so a fight is one row for its whole life rather than a pile of near-identical
// monsters.
func TestInsertBossIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	created, err := st.InsertBoss(ctx, sampleBoss("k"))
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}
	created, err = st.InsertBoss(ctx, sampleBoss("k"))
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created {
		t.Fatal("a second boss was created for the same key")
	}
	if n, err := st.CountAliveBosses(ctx); err != nil || n != 1 {
		t.Fatalf("alive = %d (err %v), want 1", n, err)
	}
}

// Closing is guarded on "still alive", which is what stops a manual click and
// the hourly sweep from both minting a kill drop.
func TestCloseBossIsGuarded(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	b := sampleBoss("k")
	if _, err := st.InsertBoss(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}

	at := time.Now().UTC()
	changed, err := st.CloseBoss(ctx, b.ID, core.BossSlain, 0, []byte(`{"slayer":"manual"}`), at)
	if err != nil || !changed {
		t.Fatalf("first close: changed=%v err=%v", changed, err)
	}
	changed, err = st.CloseBoss(ctx, b.ID, core.BossSlain, 0, nil, at)
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if changed {
		t.Fatal("a second close reported a change; two kill drops would have been minted")
	}

	stored, err := st.GetBoss(ctx, b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != core.BossSlain {
		t.Errorf("status = %s, want slain", stored.Status)
	}
	if stored.SlainAt == nil {
		t.Error("slain_at was not stamped")
	}
	if n, _ := st.CountAliveBosses(ctx); n != 0 {
		t.Errorf("alive = %d after the kill, want 0", n)
	}
}

// A fight is only updated while it is alive: a pass that started before a kill
// must not resurrect the health bar.
func TestUpdateBossFightIgnoresTheDead(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	b := sampleBoss("k")
	if _, err := st.InsertBoss(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := st.CloseBoss(ctx, b.ID, core.BossSlain, 0, nil, time.Now()); err != nil {
		t.Fatalf("close: %v", err)
	}

	b.HP = 275
	if err := st.UpdateBossFight(ctx, b, time.Now()); err != nil {
		t.Fatalf("update: %v", err)
	}
	stored, _ := st.GetBoss(ctx, b.ID)
	if stored.HP != 0 {
		t.Fatalf("hp = %v after updating a slain boss, want 0", stored.HP)
	}
}

func TestGetBossNotFound(t *testing.T) {
	if _, err := newStore(t).GetBoss(context.Background(), "nope"); err != store.ErrBossNotFound {
		t.Fatalf("err = %v, want ErrBossNotFound", err)
	}
}

// The live list leads with the biggest fight — the one worth fixing — and the
// finished list with the most recent.
func TestListBossesOrdering(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	small := sampleBoss("small")
	small.HP, small.HPMax = 10, 10
	big := sampleBoss("big")
	big.HP, big.HPMax = 500, 500
	for _, b := range []core.Boss{small, big} {
		if _, err := st.InsertBoss(ctx, b); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	alive, err := st.ListBosses(ctx, store.BossQuery{Statuses: []string{core.BossAlive}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(alive) != 2 || alive[0].Key != "big" {
		t.Fatalf("order = %v, want the biggest fight first", keysOfBosses(alive))
	}
}

func keysOfBosses(list []core.Boss) []string {
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, b.Key)
	}
	return out
}
