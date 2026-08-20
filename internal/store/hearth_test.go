package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// hearthEvent builds an arbitrary event, so each Hearth case can say exactly
// which kind of arrival it is testing.
func hearthEvent(source, kind, day, country string, qty int, base float64, ledger bool, dedupe string) core.Event {
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID:         core.NewID(),
		Source:     source,
		Kind:       kind,
		App:        "com.example.a",
		Day:        day,
		OccurredAt: occurred,
		ObservedAt: occurred,
		Country:    country,
		Amount:     base,
		AmountBase: base,
		Currency:   "USD",
		Quantity:   qty,
		IsLedger:   ledger,
		Silent:     true,
		DedupeKey:  dedupe,
	}
}

func hearthOf(t *testing.T, st *store.Store, home string) store.Hearth {
	t.Helper()
	h, err := st.Hearth(context.Background(), home, "USD")
	if err != nil {
		t.Fatalf("hearth: %v", err)
	}
	return h
}

func settlement(t *testing.T, h store.Hearth, country string) store.HearthCountry {
	t.Helper()
	for _, c := range h.Countries {
		if c.Country == country {
			return c
		}
	}
	t.Fatalf("no settlement for %s in %+v", country, h.Countries)
	return store.HearthCountry{}
}

func TestHearthPopulationAndRevenue(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	events := []core.Event{
		// US: 100 ledger sales + 20 iap + 5 subscriptions = 125 citizens.
		hearthEvent("appstore", "sale", "2026-08-01", "US", 100, 500, true, "us:1"),
		hearthEvent("appstore", "iap", "2026-08-02", "US", 20, 60, true, "us:2"),
		hearthEvent("appstore", "subscription", "2026-08-03", "US", 5, 45, true, "us:3"),
		// A refund nets revenue down but must never evict citizens.
		hearthEvent("appstore", "refund", "2026-08-03", "US", -3, -15, true, "us:4"),
		// The sales_day rollup is a summary of the rows above: neither its
		// units nor its money may be counted a second time.
		hearthEvent("appstore", "sales_day", "2026-08-03", "US", 125, 590, true, "us:5"),

		// DE: 40 free downloads — people, but no money.
		hearthEvent("appstore", "download", "2026-08-02", "DE", 40, 0, true, "de:1"),
		// JP: 12 Play installs (not a ledger row) plus one settlement drop.
		hearthEvent("googleplay", "install", "2026-08-04", "JP", 12, 0, false, "jp:1"),
		hearthEvent("loot", "settlement", "2026-08-04", "JP", 0, 0, false, "jp:2"),
		// BR: three RevenueCat purchases, each one person, plus a renewal and a
		// cancellation which are neither arrivals nor ledger money.
		hearthEvent("revenuecat", "purchase", "2026-08-05", "BR", 1, 9.99, false, "br:1"),
		hearthEvent("revenuecat", "purchase", "2026-08-05", "BR", 1, 9.99, false, "br:2"),
		hearthEvent("revenuecat", "purchase", "2026-08-06", "BR", 1, 9.99, false, "br:3"),
		hearthEvent("revenuecat", "renewal", "2026-08-06", "BR", 1, 9.99, false, "br:4"),
		hearthEvent("revenuecat", "cancellation", "2026-08-07", "BR", 1, 0, false, "br:5"),
		// Flathub knows how many installed but not from where: unknown lands.
		hearthEvent("flathub", "install", "2026-08-02", "", 587, 0, false, "fh:1"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")

	us := settlement(t, h, "US")
	if us.Population != 125 {
		t.Errorf("US population = %d, want 125 (sales + iap + subscriptions, refund excluded)", us.Population)
	}
	if us.RevenueBase != 590 {
		t.Errorf("US revenue = %v, want 590 (500+60+45-15, sales_day excluded)", us.RevenueBase)
	}
	if us.FirstSeen != "2026-08-01" || us.LastSeen != "2026-08-03" {
		t.Errorf("US seen = %s..%s, want 2026-08-01..2026-08-03", us.FirstSeen, us.LastSeen)
	}

	de := settlement(t, h, "DE")
	if de.Population != 40 || de.RevenueBase != 0 {
		t.Errorf("DE = %d people / %v revenue, want 40 / 0", de.Population, de.RevenueBase)
	}

	jp := settlement(t, h, "JP")
	if jp.Population != 12 {
		t.Errorf("JP population = %d, want 12 (installs count, the settlement event does not)", jp.Population)
	}

	br := settlement(t, h, "BR")
	if br.Population != 3 {
		t.Errorf("BR population = %d, want 3 (purchases only: renewal and cancellation are not arrivals)", br.Population)
	}

	if h.Unknown.Population != 587 {
		t.Errorf("unknown population = %d, want 587 (Flathub has no country)", h.Unknown.Population)
	}
	for _, c := range h.Countries {
		if c.Country == "" {
			t.Fatalf("the empty country leaked into the settlement list: %+v", c)
		}
	}
	if h.Population != 125+40+12+3 {
		t.Errorf("total population = %d, want 180 (unknown lands are counted apart)", h.Population)
	}

	// Sorted by population, biggest first.
	if h.Countries[0].Country != "US" || h.Countries[1].Country != "DE" {
		t.Errorf("countries = %s, %s…, want US, DE…", h.Countries[0].Country, h.Countries[1].Country)
	}
}

func TestHearthTiersAreRelativeToTheBiggest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// 1000 / 200 / 30 / 5 / 1 against a 1000-strong capital:
	// shares 1, 0.2, 0.03, 0.005 and 0.001.
	pops := map[string]int{"US": 1000, "DE": 200, "JP": 30, "BR": 5, "NG": 1}
	i := 0
	for country, n := range pops {
		i++
		ev := hearthEvent("appstore", "sale", "2026-08-01", country, n, float64(n), true, country+":1")
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	h := hearthOf(t, st, "")
	want := map[string]string{
		"US": "metropolis",
		"DE": "city",
		"JP": "village",
		"BR": "hamlet",
		"NG": "outpost",
	}
	for country, tier := range want {
		got := settlement(t, h, country)
		if got.Tier.Name != tier {
			t.Errorf("%s (share %v) tier = %s, want %s", country, got.Share, got.Tier.Name, tier)
		}
	}
	if settlement(t, h, "US").Share != 1 {
		t.Errorf("the biggest settlement must have share 1, got %v", settlement(t, h, "US").Share)
	}
	if len(h.Tiers) != len(core.Tiers) {
		t.Errorf("tier ladder not published: %+v", h.Tiers)
	}
}

func TestHearthCapital(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	for _, ev := range []core.Event{
		hearthEvent("appstore", "sale", "2026-08-01", "DE", 40, 40, true, "de:1"),
		hearthEvent("appstore", "sale", "2026-08-01", "US", 10, 10, true, "us:1"),
	} {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Unconfigured: the biggest settlement is the capital, even though the
	// developer is presumably not German.
	if got := hearthOf(t, st, "").Capital; got != "DE" {
		t.Errorf("capital = %q, want DE (the biggest settlement)", got)
	}
	// Configured: home_country always wins, and is normalized.
	if got := hearthOf(t, st, " us ").Capital; got != "US" {
		t.Errorf("capital = %q, want US (home_country wins)", got)
	}
	// A home country nobody has bought from yet is still the capital: it is
	// where you are, not where your customers are.
	if got := hearthOf(t, st, "NZ").Capital; got != "NZ" {
		t.Errorf("capital = %q, want NZ", got)
	}
}

func TestHearthEmpty(t *testing.T) {
	st := newStore(t)
	h := hearthOf(t, st, "")

	if h.Capital != "" {
		t.Errorf("capital = %q, want empty on a fresh install", h.Capital)
	}
	if len(h.Countries) != 0 || len(h.Recent) != 0 {
		t.Errorf("fresh install is not empty: %+v", h)
	}
	if h.Era.Name != "Camp" || h.Era.NextName != "Village" {
		t.Errorf("era = %+v, want Camp heading for Village", h.Era)
	}
	if h.DisplayCurrency != "USD" {
		t.Errorf("display currency = %q, want USD", h.DisplayCurrency)
	}

	// Every list has to serialize as a list. A nil slice becomes JSON `null`,
	// which the globe would have to special-case before it could map over it.
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal hearth: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode hearth: %v", err)
	}
	for _, key := range []string{"countries", "fleet", "recent", "tiers"} {
		if got := string(decoded[key]); strings.HasPrefix(got, "null") {
			t.Errorf("%s serialized as null on a fresh install", key)
		}
	}
}

// Google Play reports installs twice — an overview row with no country and a
// row per country — so a plain sum gave every install a citizen on the map and
// a second one in unknown lands. The day's total is the overview row; unknown
// lands only get what the country rows could not place.
func TestHearthUnknownLandsDoNotDoubleCountPlayInstalls(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	events := []core.Event{
		// The overview file: 100 installs for the day, no country.
		hearthEvent("googleplay", "install", "2026-08-04", "", 100, 0, false, "play:overview"),
		// The country file for the same day, adding up to the same 100.
		hearthEvent("googleplay", "install", "2026-08-04", "US", 60, 0, false, "play:us"),
		hearthEvent("googleplay", "install", "2026-08-04", "DE", 40, 0, false, "play:de"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")
	if h.Population != 100 {
		t.Errorf("population = %d, want 100: the overview row is the same 100 people", h.Population)
	}
	if h.Unknown.Population != 0 {
		t.Errorf("unknown population = %d, want 0: every install was placed on the map", h.Unknown.Population)
	}
	if got := settlement(t, h, "US").Population; got != 60 {
		t.Errorf("US population = %d, want 60", got)
	}
	if got := settlement(t, h, "DE").Population; got != 40 {
		t.Errorf("DE population = %d, want 40", got)
	}
}

// The country file lags the overview file by a poll or two. Until it arrives,
// the day's installs are real people who cannot be placed — unknown lands is
// exactly where they belong.
func TestHearthUnknownLandsKeepUnplacedInstalls(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	events := []core.Event{
		hearthEvent("googleplay", "install", "2026-08-04", "", 100, 0, false, "play:overview"),
		hearthEvent("googleplay", "install", "2026-08-04", "US", 30, 0, false, "play:us"),
		// A different day whose country file has not been read at all.
		hearthEvent("googleplay", "install", "2026-08-05", "", 50, 0, false, "play:overview2"),
		// And Flathub, which never reports a country at all.
		hearthEvent("flathub", "install", "2026-08-04", "", 587, 0, false, "fh:1"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")
	if got := settlement(t, h, "US").Population; got != 30 {
		t.Errorf("US population = %d, want 30", got)
	}
	// 70 unplaced on the 4th, 50 on the 5th, 587 from Flathub.
	if h.Unknown.Population != 70+50+587 {
		t.Errorf("unknown population = %d, want %d", h.Unknown.Population, 70+50+587)
	}
}

func TestHearthDropsAndRecent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	now := time.Now().UTC()
	mk := func(country, chestDate string, xp int) {
		t.Helper()
		ev := hearthEvent("revenuecat", "purchase", "2026-08-01", country, 1, 5, false, country+":"+chestDate+core.NewID())
		ev.Silent = false
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
		d := core.Drop{
			ID: core.NewID(), EventID: ev.ID, Rarity: core.Uncommon,
			Title: "New subscriber", XP: xp, CreatedAt: now, ChestDate: chestDate,
		}
		if err := st.InsertDrop(ctx, d); err != nil {
			t.Fatalf("insert drop: %v", err)
		}
	}

	mk("US", "", 25)
	mk("US", "", 25)
	mk("JP", "", 100)
	// Waiting inside an unopened chest: invisible to XP, drop counts and the
	// ticker, exactly as it is to the feed.
	mk("JP", "2026-08-09", 1000)

	h := hearthOf(t, st, "")
	if got := settlement(t, h, "US").Drops; got != 2 {
		t.Errorf("US drops = %d, want 2", got)
	}
	if got := settlement(t, h, "JP").Drops; got != 1 {
		t.Errorf("JP drops = %d, want 1 (the chest drop is still sealed)", got)
	}
	if h.TotalXP != 150 {
		t.Errorf("total XP = %d, want 150 (the chest's 1000 XP is not banked yet)", h.TotalXP)
	}
	if len(h.Recent) != 3 {
		t.Errorf("recent = %d drops, want 3", len(h.Recent))
	}
	for _, d := range h.Recent {
		if d.Country == "" {
			t.Errorf("a countryless drop reached the arrivals ticker: %+v", d)
		}
	}
}

// vessel is one source's ship, or a failure if that source never put to sea.
func vessel(t *testing.T, h store.Hearth, source string) store.HearthVessel {
	t.Helper()
	for _, v := range h.Fleet {
		if v.Source == source {
			return v
		}
	}
	t.Fatalf("no vessel for %s in %+v", source, h.Fleet)
	return store.HearthVessel{}
}

// The fleet is the unknown bucket with the one fact it always had attached:
// which source could not place these people. One vessel per source, counted by
// exactly the rules a country is counted by.
func TestHearthFleetGroupsBySource(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	events := []core.Event{
		// A country to measure the fleet against: 600 people in the US.
		hearthEvent("appstore", "sale", "2026-08-04", "US", 600, 3000, true, "us:1"),

		// Flathub: installs per app per day, never a country.
		hearthEvent("flathub", "install", "2026-08-04", "", 500, 0, false, "fh:1"),
		hearthEvent("flathub", "install", "2026-08-06", "", 87, 0, false, "fh:2"),
		// Its own rollup of the rows above must not board a second time.
		hearthEvent("flathub", "installs_day", "2026-08-06", "", 87, 0, false, "fh:3"),

		// Google Play reports the day twice; only the remainder the country
		// file could not place is at sea.
		hearthEvent("googleplay", "install", "2026-08-04", "", 100, 0, false, "gp:overview"),
		hearthEvent("googleplay", "install", "2026-08-04", "US", 30, 0, false, "gp:us"),
		hearthEvent("googleplay", "install", "2026-08-05", "", 50, 0, false, "gp:overview2"),

		// A ledger sale that arrived without a country, and a refund of part
		// of it: money nets out, nobody is thrown overboard.
		hearthEvent("appstore", "sale", "2026-08-02", "", 12, 60, true, "as:1"),
		hearthEvent("appstore", "refund", "2026-08-03", "", -3, -15, true, "as:2"),
		hearthEvent("appstore", "sales_day", "2026-08-03", "", 12, 60, true, "as:3"),

		// One RevenueCat purchase with no country: one person, one vessel.
		hearthEvent("revenuecat", "purchase", "2026-08-07", "", 1, 4, false, "rc:1"),
	}
	for _, ev := range events {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")

	if len(h.Fleet) != 4 {
		t.Fatalf("fleet = %+v, want four vessels", h.Fleet)
	}
	// Biggest first, exactly as the settlement list is ordered.
	if got := []string{h.Fleet[0].Source, h.Fleet[1].Source, h.Fleet[2].Source, h.Fleet[3].Source}; got[0] != "flathub" ||
		got[1] != "googleplay" || got[2] != "appstore" || got[3] != "revenuecat" {
		t.Errorf("fleet order = %v, want flathub, googleplay, appstore, revenuecat", got)
	}

	fh := vessel(t, h, "flathub")
	if fh.Population != 587 {
		t.Errorf("flathub population = %d, want 587 (the installs_day rollup is a summary)", fh.Population)
	}
	if fh.FirstSeen != "2026-08-04" || fh.LastSeen != "2026-08-06" {
		t.Errorf("flathub sailed %s..%s, want 2026-08-04..2026-08-06", fh.FirstSeen, fh.LastSeen)
	}
	if fh.Tier.Name != "metropolis" {
		t.Errorf("flathub tier = %q, want metropolis: 587 of the biggest country's 600", fh.Tier.Name)
	}

	gp := vessel(t, h, "googleplay")
	if gp.Population != 70+50 {
		t.Errorf("googleplay population = %d, want 120: only what the country file could not place", gp.Population)
	}
	if gp.Tier.Name != "city" {
		t.Errorf("googleplay tier = %q, want city", gp.Tier.Name)
	}

	as := vessel(t, h, "appstore")
	if as.Population != 12 {
		t.Errorf("appstore population = %d, want 12: a refund does not throw anybody overboard", as.Population)
	}
	if as.RevenueBase != 45 {
		t.Errorf("appstore revenue = %v, want 45: 60 sold, 15 refunded, the rollup ignored", as.RevenueBase)
	}

	if rc := vessel(t, h, "revenuecat"); rc.Population != 1 || rc.Tier.Name != "outpost" {
		t.Errorf("revenuecat vessel = %+v, want one person in an outpost", rc)
	}

	// The old scalar bucket is still the honest one-number answer, and it is
	// exactly the fleet added up.
	sumPop, sumRevenue := 0, 0.0
	for _, v := range h.Fleet {
		sumPop += v.Population
		sumRevenue += v.RevenueBase
	}
	if h.Unknown.Population != sumPop || h.Unknown.RevenueBase != sumRevenue {
		t.Errorf("unknown = %+v, want the fleet's %d people and %v", h.Unknown, sumPop, sumRevenue)
	}
	if h.Unknown.Population != 587+120+12+1 {
		t.Errorf("unknown population = %d, want 720", h.Unknown.Population)
	}

	// None of this is a country. The map has one settlement and one only.
	if len(h.Countries) != 1 || h.Countries[0].Country != "US" {
		t.Errorf("countries = %+v, want the US and nothing else", h.Countries)
	}
	if got := settlement(t, h, "US").Population; got != 630 {
		t.Errorf("US population = %d, want 630 (600 sales + 30 placed installs)", got)
	}
	if h.Population != 630 {
		t.Errorf("total population = %d, want 630: the fleet is counted apart", h.Population)
	}
}

// A vessel that never puts anybody aboard is not drawn. Kinds that are
// summaries, snapshots or bad news carry nobody, whatever their quantity.
func TestHearthFleetIgnoresNonArrivals(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	for _, ev := range []core.Event{
		hearthEvent("googleplay", "installs_day", "2026-08-04", "", 900, 0, false, "x:1"),
		hearthEvent("revenuecat", "cancellation", "2026-08-04", "", 5, 0, false, "x:2"),
		hearthEvent("revenuecat", "expiration", "2026-08-04", "", 5, 0, false, "x:3"),
		hearthEvent("appstore", "active_devices", "2026-08-04", "", 4000, 0, false, "x:4"),
		hearthEvent("loot", "settlement", "2026-08-04", "", 1, 0, false, "x:5"),
	} {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")
	if len(h.Fleet) != 0 {
		t.Errorf("fleet = %+v, want nothing afloat", h.Fleet)
	}
	if h.Unknown.Population != 0 {
		t.Errorf("unknown population = %d, want 0", h.Unknown.Population)
	}
}

// With no country on the map at all — a Flathub-only app — the fleet is still
// measured, against itself.
func TestHearthFleetWithoutAnyCountry(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	for _, ev := range []core.Event{
		hearthEvent("flathub", "install", "2026-08-04", "", 400, 0, false, "fh:1"),
		hearthEvent("snapcraft", "install", "2026-08-04", "", 4, 0, false, "sc:1"),
	} {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	h := hearthOf(t, st, "")
	if len(h.Countries) != 0 || h.Capital != "" {
		t.Fatalf("a countryless database grew a map: %+v", h.Countries)
	}
	if got := vessel(t, h, "flathub").Tier.Name; got != "metropolis" {
		t.Errorf("flathub tier = %q, want metropolis: it is the whole fleet", got)
	}
	if got := vessel(t, h, "snapcraft").Tier.Name; got != "village" {
		t.Errorf("snapcraft tier = %q, want village: 4 of 400", got)
	}
}

// A scoped globe is this app's fleet, not this app's plus everyone else's.
func TestHearthFleetIsScopedToTheProduct(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	mk := func(product string, qty int, dedupe string) core.Event {
		ev := hearthEvent("flathub", "install", "2026-08-04", "", qty, 0, false, dedupe)
		ev.App = product
		ev.Product = product
		return ev
	}
	for _, ev := range []core.Event{mk("Lumen Notes", 300, "l:1"), mk("Orbit Weather", 20, "o:1")} {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("insert %s: %v", ev.DedupeKey, err)
		}
	}

	all := hearthOf(t, st, "")
	if got := vessel(t, all, "flathub").Population; got != 320 {
		t.Errorf("unscoped flathub vessel = %d, want 320", got)
	}

	lumen, err := st.Scoped("Lumen Notes").Hearth(ctx, "", "USD")
	if err != nil {
		t.Fatalf("scoped hearth: %v", err)
	}
	if got := vessel(t, lumen, "flathub").Population; got != 300 {
		t.Errorf("Lumen's flathub vessel = %d, want 300: Orbit's people are not aboard", got)
	}
	if lumen.Unknown.Population != 300 {
		t.Errorf("Lumen unknown = %d, want 300", lumen.Unknown.Population)
	}

	none, err := st.Scoped("Never Shipped").Hearth(ctx, "", "USD")
	if err != nil {
		t.Fatalf("unknown scope: %v", err)
	}
	if len(none.Fleet) != 0 {
		t.Errorf("an unknown scope has a fleet: %+v", none.Fleet)
	}
}
