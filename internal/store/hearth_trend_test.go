package store_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func trendOf(t *testing.T, st *store.Store, q store.HearthTrendQuery, today string) store.HearthTrend {
	t.Helper()
	got, err := st.HearthTrend(context.Background(), q, "USD", today)
	if err != nil {
		t.Fatalf("HearthTrend(%+v): %v", q, err)
	}
	return got
}

func pointVal(t *testing.T, tr store.HearthTrend, day string) (float64, bool) {
	t.Helper()
	for _, p := range tr.Points {
		if p.Day == day {
			if p.Value == nil {
				return 0, false
			}
			return *p.Value, true
		}
	}
	t.Fatalf("day %s not found in %s..%s", day, tr.From, tr.To)
	return 0, false
}

func pointOf(t *testing.T, tr store.HearthTrend, day string) store.TrendPoint {
	t.Helper()
	for _, p := range tr.Points {
		if p.Day == day {
			return p
		}
	}
	t.Fatalf("day %s not found in %s..%s", day, tr.From, tr.To)
	return store.TrendPoint{}
}

func TestHearthTrendAudienceLevelMultiStoreMatchesHearth(t *testing.T) {
	st := newStore(t)
	insertAll(t, st,
		// Snapcraft: Nov 15 -> 150, Nov 16 -> 160, Nov 18 -> 169 (Nov 17 gap, Nov 19-20 lag)
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-15", "US", 90),
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-15", "", 150),
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-16", "US", 95),
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-16", "", 160),
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-18", "US", 98),
		audienceEvent("snapcraft", "reson8", "Reson8", "2026-11-18", "", 169),
		// Google Play: Nov 16 -> 10, Nov 17 -> 12 (lags Snapcraft by a day)
		audienceEvent("googleplay", "com.reson8", "Reson8", "2026-11-16", "US", 8),
		audienceEvent("googleplay", "com.reson8", "Reson8", "2026-11-16", "", 10),
		audienceEvent("googleplay", "com.reson8", "Reson8", "2026-11-17", "US", 10),
		audienceEvent("googleplay", "com.reson8", "Reson8", "2026-11-17", "", 12),
		// App Store arrivals should never be mixed into the product's active base.
		func() core.Event {
			ev := hearthEvent("appstore", "download", "2026-11-17", "US", 10, 0, true, "as:dl:1")
			ev.App, ev.Product = "Reson8 iOS", "Reson8"
			return ev
		}(),
	)

	h := hearthOf(t, st, "")
	aud := productAudience(t, h, "Reson8")

	// 7d window ending Nov 20 (Nov 14..Nov 20).
	tr := trendOf(t, st, store.HearthTrendQuery{
		Metric:  "audience",
		Product: "Reson8",
		Range:   "7d",
	}, "2026-11-20")

	if tr.Kind != store.TrendLevel {
		t.Fatalf("kind = %q, want level", tr.Kind)
	}
	if int(tr.Current) != aud.Current || tr.Current != 181 {
		t.Errorf("current = %v, want %d (181)", tr.Current, aud.Current)
	}
	if tr.Start != 150 || tr.Change != 31 {
		t.Errorf("start/change = %v/%v, want 150/31", tr.Start, tr.Change)
	}
	if tr.FirstDay != "2026-11-15" || tr.LastDay != "2026-11-18" {
		t.Errorf("first/last = %s/%s, want 2026-11-15/2026-11-18", tr.FirstDay, tr.LastDay)
	}
	if len(tr.SeriesNames) != 2 || tr.SeriesNames[0] != "snapcraft" || tr.SeriesNames[1] != "googleplay" {
		t.Errorf("series_names = %v, want [snapcraft googleplay]", tr.SeriesNames)
	}

	// Before first snapshot: nil (never fabricate 0).
	if _, ok := pointVal(t, tr, "2026-11-14"); ok {
		t.Errorf("2026-11-14 should be nil before first snapshot")
	}
	// After newest snapshot (store lag): nil.
	if _, ok := pointVal(t, tr, "2026-11-19"); ok {
		t.Errorf("2026-11-19 should be nil after newest snapshot")
	}
	// Nov 17 carries forward Snapcraft (160) + Play's new reading (12) = 172.
	p17 := pointOf(t, tr, "2026-11-17")
	if p17.Value == nil || *p17.Value != 172 || p17.Daily != 2 {
		t.Errorf("2026-11-17 = %+v, want value 172 daily +2", p17)
	}
	if p17.BySeries["snapcraft"] != 160 || p17.BySeries["googleplay"] != 12 {
		t.Errorf("2026-11-17 by_series = %+v, want snapcraft=160 googleplay=12", p17.BySeries)
	}
	// Nov 18 has Snapcraft (169) + carried Play (12) = 181.
	p18 := pointOf(t, tr, "2026-11-18")
	if p18.Value == nil || *p18.Value != 181 || p18.Daily != 9 {
		t.Errorf("2026-11-18 = %+v, want value 181 daily +9", p18)
	}

	// Single-store drill-in: Snapcraft only.
	snap := trendOf(t, st, store.HearthTrendQuery{
		Metric:  "audience",
		Product: "Reson8",
		Source:  "snapcraft",
		Range:   "all",
	}, "2026-11-20")
	if snap.Basis != core.BasisWeeklyDevices || snap.Current != 169 || snap.From != "2026-11-15" || snap.To != "2026-11-18" {
		t.Errorf("snap all = %+v", snap)
	}
	if len(snap.SeriesNames) != 0 {
		t.Errorf("single-store series_names = %v, want empty", snap.SeriesNames)
	}

	// Single-store drill-in: App Store (arrivals-only).
	as := trendOf(t, st, store.HearthTrendQuery{
		Metric:  "audience",
		Product: "Reson8",
		Source:  "appstore",
		Range:   "7d",
	}, "2026-11-20")
	if as.Kind != store.TrendCumulative || as.Basis != core.BasisArrivals || as.Current != 10 || as.Change != 10 {
		t.Errorf("appstore = %+v", as)
	}
	if v, ok := pointVal(t, as, "2026-11-16"); !ok || v != 0 {
		t.Errorf("appstore 2026-11-16 = %v/%v, want 0/true", v, ok)
	}
	if p := pointOf(t, as, "2026-11-17"); p.Value == nil || *p.Value != 10 || p.Daily != 10 {
		t.Errorf("appstore 2026-11-17 = %+v, want value 10 daily 10", p)
	}
}

func TestHearthTrendCivilizationFleetDownloadsCountryAndXP(t *testing.T) {
	st := newStore(t)

	mk := func(source, kind, app, product, day, country string, qty int, amt float64, ledger bool, key string) core.Event {
		ev := hearthEvent(source, kind, day, country, qty, amt, ledger, key)
		ev.App, ev.Product = app, product
		return ev
	}
	dl := func(product, day, format string, qty int, key string) core.Event {
		ev := hearthEvent("github", core.KindAssetDownload, day, "", qty, 0, false, key)
		ev.App, ev.Product = "o/repo", product
		ev.Payload, _ = json.Marshal(map[string]any{"format": format})
		return ev
	}

	insertAll(t, st,
		// Earlier baseline before the 7d window (Nov 10)
		mk("appstore", "sale", "Trainer iOS", "Trainer", "2026-11-10", "US", 2, 19.98, true, "as:1"),
		mk("revenuecat", "purchase", "Trainer", "Trainer", "2026-11-10", "", 1, 0, false, "rc:1"),
		dl("Trainer", "2026-11-10", "flatpak", 20, "gh:1"),
		dl("Trainer", "2026-11-10", "deb", 10, "gh:2"),
		// Inside 7d window (Nov 15..Nov 20)
		mk("appstore", "sale", "Trainer iOS", "Trainer", "2026-11-16", "DE", 3, 30.00, true, "as:2"),
		mk("appstore", "refund", "Trainer iOS", "Trainer", "2026-11-18", "DE", -1, -10.00, true, "as:3"),
		mk("revenuecat", "purchase", "Trainer", "Trainer", "2026-11-18", "", 1, 0, false, "rc:2"),
		// Snapcraft audience: Nov 16 has US=10, NZ=2, total=15 (unplaced=3);
		// Nov 18 has US=12 (NZ dropped to 0!), total=17 (unplaced=5).
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-16", "US", 10),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-16", "NZ", 2),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-16", "", 15),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-18", "US", 12),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-18", "", 17),
		// GitHub downloads inside window
		dl("Trainer", "2026-11-17", "flatpak", 5, "gh:3"),
		dl("Trainer", "2026-11-19", "deb", 7, "gh:4"),
	)

	// Add drops for XP: one revealed, one unrevealed (must be excluded).
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ev1 := mk("github", "star", "o/repo", "Trainer", "2026-11-10", "", 1, 0, false, "star:1")
	ev2 := mk("github", "star", "o/repo", "Trainer", "2026-11-18", "", 1, 0, false, "star:2")
	ev3 := mk("github", "star", "o/repo", "Trainer", "2026-11-19", "", 1, 0, false, "star:3")
	insertAll(t, st, ev1, ev2, ev3)
	for _, d := range []core.Drop{
		{ID: core.NewIDAt(now.AddDate(0, 0, -10)), EventID: ev1.ID, Rarity: core.Common, Title: "s1", XP: 100, CreatedAt: now},
		{ID: core.NewIDAt(now.AddDate(0, 0, -2)), EventID: ev2.ID, Rarity: core.Rare, Title: "s2", XP: 250, CreatedAt: now},
		{ID: core.NewIDAt(now.AddDate(0, 0, -1)), EventID: ev3.ID, Rarity: core.Epic, Title: "s3", XP: 500, CreatedAt: now, ChestDate: "2026-11-19"},
	} {
		if err := st.InsertDrop(context.Background(), d); err != nil {
			t.Fatalf("insert drop: %v", err)
		}
	}

	today := "2026-11-20"
	h := hearthOf(t, st, "")

	// 1. Population matches Hearth.Population (US: 2+12=14, DE: 3 -> 17).
	pop := trendOf(t, st, store.HearthTrendQuery{Metric: "population", Range: "7d"}, today)
	if int(pop.Current) != h.Population || pop.Current != 17 {
		t.Errorf("population current = %v, hearth = %d (want 17)", pop.Current, h.Population)
	}
	if pop.Start != 2 || pop.Change != 15 {
		t.Errorf("population start/change = %v/%v, want 2/15", pop.Start, pop.Change)
	}
	// On Nov 16: US(2+10) + DE(3) + NZ(2) = 17; on Nov 18: US(2+12) + DE(3) + NZ(0) = 17.
	if v, ok := pointVal(t, pop, "2026-11-16"); !ok || v != 17 {
		t.Errorf("population 2026-11-16 = %v/%v, want 17", v, ok)
	}

	// 2. Settlements matches len(Hearth.Countries): Nov 10 US(1), Nov 16 US+DE+NZ(3), Nov 18 US+DE(2).
	set := trendOf(t, st, store.HearthTrendQuery{Metric: "settlements", Range: "7d"}, today)
	if int(set.Current) != len(h.Countries) || set.Current != 2 {
		t.Errorf("settlements current = %v, hearth = %d (want 2)", set.Current, len(h.Countries))
	}
	if v, _ := pointVal(t, set, "2026-11-15"); v != 1 {
		t.Errorf("settlements 2026-11-15 = %v, want 1", v)
	}
	if v, _ := pointVal(t, set, "2026-11-16"); v != 3 {
		t.Errorf("settlements 2026-11-16 = %v, want 3", v)
	}
	if p := pointOf(t, set, "2026-11-18"); p.Value == nil || *p.Value != 2 || p.Daily != -1 {
		t.Errorf("settlements 2026-11-18 = %+v, want 2 daily -1", p)
	}

	// 3. Country NZ specifically drops from 2 on Nov 16 to 0 on Nov 18!
	nz := trendOf(t, st, store.HearthTrendQuery{Metric: "country", Country: "NZ", Range: "7d"}, today)
	if v, ok := pointVal(t, nz, "2026-11-16"); !ok || v != 2 {
		t.Errorf("NZ 2026-11-16 = %v/%v, want 2", v, ok)
	}
	if v, ok := pointVal(t, nz, "2026-11-18"); !ok || v != 0 {
		t.Errorf("NZ 2026-11-18 = %v/%v, want 0 (dropped when absent from newer snapshot)", v, ok)
	}

	// 4. Country US matches settlement(US).Population (14).
	us := trendOf(t, st, store.HearthTrendQuery{Metric: "country", Country: "US", Range: "7d"}, today)
	if int(us.Current) != settlement(t, h, "US").Population || us.Current != 14 {
		t.Errorf("US current = %v, want 14", us.Current)
	}

	// 5. Revenue matches Hearth.RevenueBase (19.98 + 30.00 - 10.00 = 39.98).
	rev := trendOf(t, st, store.HearthTrendQuery{Metric: "revenue", Range: "7d"}, today)
	if math.Abs(rev.Current-h.RevenueBase) > 0.001 || rev.Current != 39.98 {
		t.Errorf("revenue current = %v, hearth = %v (want 39.98)", rev.Current, h.RevenueBase)
	}
	if rev.Start != 19.98 || rev.Change != 20.00 || rev.Unit != "money" {
		t.Errorf("revenue start/change/unit = %v/%v/%s, want 19.98/20/money", rev.Start, rev.Change, rev.Unit)
	}
	if p := pointOf(t, rev, "2026-11-18"); p.Daily != -10 {
		t.Errorf("revenue 2026-11-18 daily = %v, want -10", p.Daily)
	}

	// 6. Fleet: snapcraft unplaced (Nov 16 -> 3, Nov 18 -> 5) and revenuecat (Nov 10 -> 1, Nov 18 -> 2).
	snapFleet := trendOf(t, st, store.HearthTrendQuery{Metric: "fleet", Source: "snapcraft", Range: "7d"}, today)
	if snapFleet.Current != 5 {
		t.Errorf("snapcraft fleet current = %v, want 5", snapFleet.Current)
	}
	if v, _ := pointVal(t, snapFleet, "2026-11-16"); v != 3 {
		t.Errorf("snapcraft fleet 2026-11-16 = %v, want 3", v)
	}
	rcFleet := trendOf(t, st, store.HearthTrendQuery{Metric: "fleet", Source: "revenuecat", Range: "7d"}, today)
	if rcFleet.Current != 2 || rcFleet.Start != 1 || rcFleet.Change != 1 {
		t.Errorf("revenuecat fleet = %+v, want current=2 start=1 change=1", rcFleet)
	}

	// 7. Downloads: product stacked (flatpak 25 + deb 17 = 42) and single format (flatpak 25).
	dls := trendOf(t, st, store.HearthTrendQuery{Metric: "downloads", Product: "Trainer", Range: "7d"}, today)
	if dls.Current != 42 || dls.Start != 30 || dls.Change != 12 {
		t.Errorf("downloads = %+v, want current=42 start=30 change=12", dls)
	}
	if len(dls.SeriesNames) != 2 || dls.SeriesNames[0] != "flatpak" || dls.SeriesNames[1] != "deb" {
		t.Errorf("downloads series_names = %v, want [flatpak deb]", dls.SeriesNames)
	}
	dlFlat := trendOf(t, st, store.HearthTrendQuery{Metric: "downloads", Product: "Trainer", Format: "flatpak", Range: "7d"}, today)
	if dlFlat.Current != 25 || dlFlat.Start != 20 || dlFlat.Change != 5 {
		t.Errorf("flatpak downloads = %+v, want current=25 start=20 change=5", dlFlat)
	}

	// 8. XP: cumulative revealed drops only (100 + 250 = 350; excludes 500 unrevealed), unscoped!
	xp := trendOf(t, st.Scoped("OtherApp"), store.HearthTrendQuery{Metric: "xp", Range: "7d"}, today)
	if int(xp.Current) != h.TotalXP || xp.Current != 350 || xp.Start != 100 || xp.Change != 250 || xp.Unit != "xp" {
		t.Errorf("xp = %+v, hearth = %d (want current=350 start=100 change=250)", xp, h.TotalXP)
	}
}

func TestHearthTrendRangesYTDYearAll(t *testing.T) {
	st := newStore(t)
	ev := hearthEvent("appstore", "download", "2025-12-01", "US", 5, 0, true, "as:old")
	ev.App, ev.Product = "Tuner", "Tuner"
	ev2 := hearthEvent("appstore", "download", "2026-02-15", "US", 7, 0, true, "as:ytd")
	ev2.App, ev2.Product = "Tuner", "Tuner"
	insertAll(t, st, ev, ev2)

	today := "2026-03-10"

	ytd := trendOf(t, st, store.HearthTrendQuery{Metric: "population", Range: "ytd"}, today)
	if ytd.From != "2026-01-01" || ytd.To != "2026-03-10" || ytd.Start != 5 || ytd.Current != 12 || ytd.Change != 7 {
		t.Errorf("ytd = %+v", ytd)
	}

	yr := trendOf(t, st, store.HearthTrendQuery{Metric: "population", Range: "1y"}, today)
	if yr.Days != 365 || yr.Current != 12 || yr.Change != 12 {
		t.Errorf("1y = %+v", yr)
	}

	all := trendOf(t, st, store.HearthTrendQuery{Metric: "population", Range: "all"}, today)
	if all.From != "2025-12-01" || all.To != "2026-03-10" || all.Start != 0 || all.Current != 12 {
		t.Errorf("all = %+v", all)
	}
}
