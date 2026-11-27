package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// audienceEvent builds one snapshot row for a product.
func audienceEvent(source, app, product, day, country string, qty int) core.Event {
	ev := hearthEvent(source, core.KindAudience, day, country, qty, 0, false,
		source+":audience:"+app+":"+day+":"+country)
	ev.App, ev.Product = app, product
	return ev
}

func insertAll(t *testing.T, st *store.Store, events ...core.Event) {
	t.Helper()
	for _, ev := range events {
		if _, err := st.InsertEvent(context.Background(), ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}
}

func productAudience(t *testing.T, h store.Hearth, product string) store.ProductAudience {
	t.Helper()
	for _, p := range h.Audience {
		if p.Product == product {
			return p
		}
	}
	t.Fatalf("no audience for %s in %+v", product, h.Audience)
	return store.ProductAudience{}
}

// The bug this whole feature exists for. A snap with a steady base of devices
// reports some of them as "new" every single day, because the store's "new"
// means "seen today and not yesterday". Summed, that was the population. The
// population is the newest snapshot and nothing else.
func TestHearthPopulationIsTheNewestAudienceSnapshot(t *testing.T) {
	st := newStore(t)

	var events []core.Event
	for _, day := range []string{"2026-11-16", "2026-11-17", "2026-11-18"} {
		events = append(events,
			audienceEvent("snapcraft", "tuner", "Tuner", day, "US", 40),
			audienceEvent("snapcraft", "tuner", "Tuner", day, "DE", 10),
			audienceEvent("snapcraft", "tuner", "Tuner", day, "", 70),
		)
	}
	// Germany lost two devices on the last day. Settlements can shrink now.
	events[len(events)-2].Quantity = 8
	events[len(events)-1].Quantity = 68
	insertAll(t, st, events...)

	h := hearthOf(t, st, "")
	if h.Population != 48 {
		t.Errorf("population = %d, want 48: one snapshot, not three added up", h.Population)
	}
	if got := settlement(t, h, "DE").Population; got != 8 {
		t.Errorf("DE = %d, want 8: the newest reading replaces the older one", got)
	}
	// 68 in total, 48 placed: the store put 20 devices nowhere.
	if h.Unknown.Population != 20 {
		t.Errorf("at sea = %d, want 20 (total minus placed)", h.Unknown.Population)
	}
	if len(h.Fleet) != 1 || h.Fleet[0].Source != "snapcraft" || h.Fleet[0].Population != 20 {
		t.Errorf("fleet = %+v", h.Fleet)
	}

	a := productAudience(t, h, "Tuner")
	if a.Current != 68 || a.Arrivals != 0 {
		t.Errorf("current/arrivals = %d/%d, want 68/0", a.Current, a.Arrivals)
	}
	if len(a.Sources) != 1 || a.Sources[0].Basis != core.BasisWeeklyDevices ||
		a.Sources[0].Count != 68 || a.Sources[0].AsOf != "2026-11-18" {
		t.Errorf("sources = %+v", a.Sources)
	}
}

// A source that reports an audience has its arrivals ignored — installs and
// paying customers alike are already among the devices it counted — while a
// source that reports none keeps counting arrivals exactly as before.
func TestAudienceReplacesArrivalsOnlyForItsOwnSource(t *testing.T) {
	st := newStore(t)

	play := func(kind, country string, qty int, ledger bool, key string) core.Event {
		ev := hearthEvent("googleplay", kind, "2026-11-10", country, qty, 0, ledger, key)
		ev.App, ev.Product = "com.example.tuner", "Tuner"
		return ev
	}
	apple := hearthEvent("appstore", "download", "2026-11-10", "US", 7, 0, true, "as:dl")
	apple.App, apple.Product = "Tuner for iOS", "Tuner"

	insertAll(t, st,
		// Play: 300 installs ever, one sale, and 12 devices that still have it.
		play("install", "", 300, false, "play:overview"),
		play("install", "US", 300, false, "play:us"),
		play("subscription", "US", 1, true, "play:sub"),
		audienceEvent("googleplay", "com.example.tuner", "Tuner", "2026-11-10", "US", 9),
		audienceEvent("googleplay", "com.example.tuner", "Tuner", "2026-11-10", "", 12),
		// The App Store says who came and never who stayed.
		apple,
	)

	h := hearthOf(t, st, "")
	if got := settlement(t, h, "US").Population; got != 16 {
		t.Errorf("US = %d, want 16: 9 active Play devices + 7 App Store downloads", got)
	}
	a := productAudience(t, h, "Tuner")
	if a.Current != 12 || a.Arrivals != 7 {
		t.Errorf("current/arrivals = %d/%d, want 12/7 — never added together", a.Current, a.Arrivals)
	}
	if len(a.Sources) != 2 || a.Sources[0].Source != "googleplay" || a.Sources[1].Source != "appstore" ||
		a.Sources[1].Basis != core.BasisArrivals || a.Sources[1].AsOf != "" {
		t.Errorf("sources = %+v, want the current reading first and the arrivals after", a.Sources)
	}
}

// A country that only an audience source ever reported is as populated as the
// newest snapshot says. When that is nobody, it is not a settlement.
func TestSettlementWithNobodyLeftIsNotListed(t *testing.T) {
	st := newStore(t)
	insertAll(t, st,
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-17", "US", 5),
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-17", "NZ", 1),
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-17", "", 6),
		// The next day New Zealand is simply absent from the report.
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-18", "US", 5),
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-18", "", 5),
	)

	h := hearthOf(t, st, "")
	if len(h.Countries) != 1 || h.Countries[0].Country != "US" {
		t.Errorf("countries = %+v, want only US", h.Countries)
	}
	if h.Unknown.Population != 0 {
		t.Errorf("at sea = %d, want 0", h.Unknown.Population)
	}
}

// The overview and country reports are published separately. A newer total
// must be paired with the newest countries there are, not with none.
func TestAudienceTotalAndCountriesMayBeADayApart(t *testing.T) {
	st := newStore(t)
	insertAll(t, st,
		audienceEvent("googleplay", "com.example.tuner", "Tuner", "2026-11-13", "US", 8),
		audienceEvent("googleplay", "com.example.tuner", "Tuner", "2026-11-13", "", 10),
		audienceEvent("googleplay", "com.example.tuner", "Tuner", "2026-11-14", "", 11),
	)

	h := hearthOf(t, st, "")
	if got := settlement(t, h, "US").Population; got != 8 {
		t.Errorf("US = %d, want 8 from the newest country report", got)
	}
	if h.Unknown.Population != 3 {
		t.Errorf("at sea = %d, want 3 (11 − 8), not all 11", h.Unknown.Population)
	}
}

func TestAudienceIsScopedToTheProduct(t *testing.T) {
	st := newStore(t)
	insertAll(t, st,
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-18", "US", 5),
		audienceEvent("snapcraft", "tuner", "Tuner", "2026-11-18", "", 5),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-18", "US", 2),
		audienceEvent("snapcraft", "trainer", "Trainer", "2026-11-18", "", 2),
	)

	h := hearthOf(t, st.Scoped("Trainer"), "")
	if h.Population != 2 || len(h.Audience) != 1 || h.Audience[0].Product != "Trainer" {
		t.Errorf("scoped population = %d, audience = %+v", h.Population, h.Audience)
	}
	if all := hearthOf(t, st, ""); all.Population != 7 || len(all.Audience) != 2 ||
		all.Audience[0].Product != "Tuner" {
		t.Errorf("unscoped population = %d, audience = %+v (biggest first)", all.Population, all.Audience)
	}
}

// Downloads are shown as downloads. They are never anybody's audience.
func TestAssetDownloadsAreCountedApartFromPeople(t *testing.T) {
	st := newStore(t)

	file := func(format string, qty int, key string) core.Event {
		ev := hearthEvent("github", core.KindAssetDownload, "2026-11-18", "", qty, 0, false, key)
		ev.App, ev.Product = "o/tuner-releases", "Tuner"
		ev.Payload, _ = json.Marshal(map[string]any{"format": format})
		return ev
	}
	insertAll(t, st, file("deb", 40, "a"), file("deb", 2, "b"), file("flatpak", 66, "c"))

	h := hearthOf(t, st, "")
	if h.Population != 0 || h.Unknown.Population != 0 || len(h.Fleet) != 0 {
		t.Errorf("downloads became people: population %d, at sea %d, fleet %+v",
			h.Population, h.Unknown.Population, h.Fleet)
	}
	a := productAudience(t, h, "Tuner")
	if a.Current != 0 || a.Arrivals != 0 || len(a.Sources) != 0 {
		t.Errorf("downloads became an audience: %+v", a)
	}
	want := []store.AssetDownloads{{Format: "flatpak", Count: 66}, {Format: "deb", Count: 42}}
	if len(a.Downloads) != 2 || a.Downloads[0] != want[0] || a.Downloads[1] != want[1] {
		t.Errorf("downloads = %+v, want %+v", a.Downloads, want)
	}
}

func TestHighestAudienceMilestone(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	if got, err := st.HighestAudienceMilestone(ctx, "snapcraft", "tuner"); err != nil || got != 0 {
		t.Fatalf("empty = %d, %v", got, err)
	}
	for _, rung := range []int{10, 25} {
		ev := hearthEvent("snapcraft", core.KindAudienceMilestone, "2026-11-18", "", rung, 0, false,
			"m:"+string(rune('a'+rung)))
		ev.App = "tuner"
		insertAll(t, st, ev)
	}
	if got, _ := st.HighestAudienceMilestone(ctx, "snapcraft", "tuner"); got != 25 {
		t.Errorf("highest = %d, want 25", got)
	}
	if got, _ := st.HighestAudienceMilestone(ctx, "snapcraft", "other"); got != 0 {
		t.Errorf("another app's milestones leaked: %d", got)
	}
}

func TestAudiencePlayAllTimeScorecardAndRevenueCatDedupe(t *testing.T) {
	st := newStore(t)

	mk := func(source, kind, app, product, day, country string, qty int, amt float64, ledger bool, key string) core.Event {
		ev := hearthEvent(source, kind, day, country, qty, amt, ledger, key)
		ev.App, ev.Product = app, product
		return ev
	}

	insertAll(t, st,
		// Macro Trainer: Snapcraft weekly 26, Google Play active 12 (with 45 lifetime installs),
		// App Store 20 free downloads + 3 paid subs + 1 refund, Microsoft Store 1 free download,
		// plus RevenueCat purchases (which must be hidden because Play/App Store are present).
		audienceEvent("snapcraft", "macro-trainer", "Macro Trainer", "2026-11-18", "", 26),
		audienceEvent("googleplay", "com.mt.app", "Macro Trainer", "2026-11-18", "", 12),
		mk("googleplay", "install", "com.mt.app", "Macro Trainer", "2026-11-17", "", 30, 0, false, "gp:i1"),
		mk("googleplay", "install", "com.mt.app", "Macro Trainer", "2026-11-18", "", 15, 0, false, "gp:i2"),
		mk("googleplay", "subscription", "com.mt.app", "Macro Trainer", "2026-11-18", "US", 2, 19.98, true, "gp:sub"),
		mk("appstore", "download", "Macro Trainer", "Macro Trainer", "2026-11-18", "US", 20, 0, true, "as:dl"),
		mk("appstore", "subscription", "Macro Trainer", "Macro Trainer", "2026-11-18", "US", 3, 29.97, true, "as:sub"),
		mk("appstore", "refund", "Macro Trainer", "Macro Trainer", "2026-11-18", "US", -1, -9.99, true, "as:ref"),
		mk("microsoftstore", "download", "9NBLGGH4R315", "Macro Trainer", "2026-11-18", "US", 1, 0, true, "ms:dl"),
		mk("revenuecat", "purchase", "rc_mt", "Macro Trainer", "2026-11-18", "US", 1, 9.99, false, "rc:1"),
		mk("revenuecat", "purchase", "rc_mt", "Macro Trainer", "2026-11-18", "US", 1, 9.99, false, "rc:2"),
		// Standalone RevenueCat-only product: RevenueCat stays as the fallback.
		mk("revenuecat", "purchase", "rc_solo", "Solo App", "2026-11-18", "US", 1, 4.99, false, "rc:solo1"),
	)

	h := hearthOf(t, st, "")
	mt := productAudience(t, h, "Macro Trainer")
	if mt.Current != 38 {
		t.Errorf("Macro Trainer current = %d, want 38 (26 snap + 12 play)", mt.Current)
	}
	if mt.Arrivals != 24 {
		t.Errorf("Macro Trainer arrivals = %d, want 24 (23 appstore + 1 msstore, excluding revenuecat)", mt.Arrivals)
	}
	var sawPlay, sawRC bool
	for _, src := range mt.Sources {
		if src.Source == "googleplay" {
			sawPlay = true
			if src.Count != 12 || src.AllTime != 45 {
				t.Errorf("googleplay source = %+v, want count=12 all_time=45", src)
			}
		}
		if src.Source == "snapcraft" && src.AllTime != 0 {
			t.Errorf("snapcraft all_time = %d, want 0", src.AllTime)
		}
		if src.Source == "revenuecat" {
			sawRC = true
		}
	}
	if !sawPlay {
		t.Fatalf("missing googleplay in sources: %+v", mt.Sources)
	}
	if sawRC {
		t.Errorf("revenuecat should be omitted when appstore/googleplay are present: %+v", mt.Sources)
	}
	if mt.Scorecard == nil {
		t.Fatalf("Macro Trainer scorecard is nil")
	}
	// Downloads = 45 (Play installs) + 20 (App Store free download) + 1 (MS Store download) = 66
	// Purchases = 2 (Play sub) + 3 (App Store sub) = 5
	// Refunds = 1
	// Revenue = 19.98 + 29.97 - 9.99 = 39.96
	if mt.Scorecard.Downloads != 66 || mt.Scorecard.Purchases != 5 || mt.Scorecard.Refunds != 1 || mt.Scorecard.RevenueBase != 39.96 {
		t.Errorf("Macro Trainer scorecard = %+v, want downloads=66 purchases=5 refunds=1 revenue=39.96", mt.Scorecard)
	}

	solo := productAudience(t, h, "Solo App")
	if solo.Arrivals != 1 || len(solo.Sources) != 1 || solo.Sources[0].Source != "revenuecat" {
		t.Errorf("Solo App should keep revenuecat fallback: %+v", solo)
	}
}

func TestStaleAudienceSnapshotsAreDropped(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	insertAll(t, st,
		// Stale: 15 days before today (2026-11-27 -> floor is 2026-11-14).
		audienceEvent("snapcraft", "macro-trainer", "Macro Trainer", "2026-11-12", "US", 20),
		audienceEvent("snapcraft", "macro-trainer", "Macro Trainer", "2026-11-12", "", 26),
		// Fresh: on the exact floor day (13 days ago = within the 14-day window).
		audienceEvent("googleplay", "com.mt.app", "Macro Trainer", "2026-11-14", "US", 10),
		audienceEvent("googleplay", "com.mt.app", "Macro Trainer", "2026-11-14", "", 12),
	)

	h, err := st.HearthAt(ctx, "", "USD", "2026-11-27")
	if err != nil {
		t.Fatalf("hearth at: %v", err)
	}
	mt := productAudience(t, h, "Macro Trainer")
	if mt.Current != 12 {
		t.Errorf("current = %d, want 12 (stale snapcraft dropped, fresh googleplay kept)", mt.Current)
	}
	if len(mt.Sources) != 1 || mt.Sources[0].Source != "googleplay" {
		t.Errorf("sources = %+v, want only googleplay", mt.Sources)
	}
	if h.Population != 10 {
		t.Errorf("population = %d, want 10 (stale snapcraft US devices dropped)", h.Population)
	}

	// Scorecards use the same 14-day staleness cutoff.
	sc, err := st.AppScorecards(ctx, "2026-11-27")
	if err != nil {
		t.Fatalf("scorecards: %v", err)
	}
	if len(sc) != 1 || sc[0].Active != 12 {
		t.Errorf("scorecards = %+v, want active=12", sc)
	}
}
