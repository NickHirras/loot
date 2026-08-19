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
	for _, key := range []string{"countries", "recent", "tiers"} {
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
