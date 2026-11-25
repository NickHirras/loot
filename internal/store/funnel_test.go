package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// funnelEvent builds one funnel snapshot as a funnel source would: silent,
// never ledger, no amount, everything in the payload.
func funnelEvent(t *testing.T, source, kind, product, day, entity string, observed time.Time, payload any) core.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	occurred, _ := time.Parse(core.DayLayout, day)
	return core.Event{
		ID:         core.NewIDAt(observed),
		Source:     source,
		Kind:       kind,
		App:        product,
		Product:    product,
		Day:        day,
		OccurredAt: occurred,
		ObservedAt: observed,
		Silent:     true,
		DedupeKey:  core.FunnelSnapshotKey(source, kind, entity, day, observed.Format(time.RFC3339Nano)),
		Payload:    raw,
	}
}

func adDay(t *testing.T, product, day string, observed time.Time, ad core.AdDay) core.Event {
	return funnelEvent(t, "metaads", core.KindAdDay, product, day, ad.AdID, observed, ad)
}

func linkDay(t *testing.T, product, day string, observed time.Time, label string, visits int) core.Event {
	return funnelEvent(t, "links", core.KindLinkDay, product, day, label, observed,
		core.LinkDay{Label: label, Visits: visits})
}

func funnel(t *testing.T, st *store.Store, from, to string, opts store.FunnelOptions) core.FunnelReport {
	t.Helper()
	if opts.DisplayCurrency == "" {
		opts.DisplayCurrency = "USD"
	}
	rep, err := st.Funnel(context.Background(), from, to, opts)
	if err != nil {
		t.Fatalf("funnel: %v", err)
	}
	return rep
}

func dayOf(t *testing.T, rep core.FunnelReport, day string) core.FunnelDay {
	t.Helper()
	for _, d := range rep.Days {
		if d.Day == day {
			return d
		}
	}
	t.Fatalf("no day %s in report", day)
	return core.FunnelDay{}
}

func intv(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatv(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

var t0 = time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)

// An ad platform revises a day as late conversions arrive. Only the newest
// snapshot of an ad's day counts; the older one is never added to it.
func TestFunnelTakesTheNewestSnapshotOnly(t *testing.T) {
	st := newStore(t)
	ad := core.AdDay{AdID: "a1", Ad: "First name", Spend: 10, Currency: "USD", LandingPageViews: 40, Conversions: 1, Tracked: true}
	revised := ad
	revised.Ad, revised.Spend, revised.LandingPageViews, revised.Conversions = "Renamed", 12, 50, 3
	insertAll(t, st,
		adDay(t, "App", "2026-03-09", t0, ad),
		adDay(t, "App", "2026-03-09", t0.Add(6*time.Hour), revised),
	)

	rep := funnel(t, st, "2026-03-08", "2026-03-09", store.FunnelOptions{})
	if !rep.Enabled {
		t.Fatal("report should be enabled once a funnel snapshot exists")
	}
	d := dayOf(t, rep, "2026-03-09")
	if floatv(d.Spend) != 12.0 || intv(d.LandingPageViews) != 50 || intv(d.Conversions) != 3 {
		t.Errorf("day = spend %v views %v conversions %v, want the newer snapshot's 12/50/3",
			floatv(d.Spend), intv(d.LandingPageViews), intv(d.Conversions))
	}
	if len(rep.Ads) != 1 || rep.Ads[0].Spend != 12 || rep.Ads[0].Ad != "Renamed" {
		t.Fatalf("ads = %+v", rep.Ads)
	}
	if got := rep.Ads[0].Series; len(got) != 2 || got[0] != 0 || got[1] != 50 {
		t.Errorf("series = %v, want [0 50]", got)
	}
	// The day before the ad platform's newest report had nothing running.
	if floatv(dayOf(t, rep, "2026-03-08").Spend) != 0.0 {
		t.Errorf("an earlier, reported day should be zero spend, not nil")
	}
	if rep.AsOf["ads"] != "2026-03-09" {
		t.Errorf("as_of ads = %q", rep.AsOf["ads"])
	}
}

// A scope with no funnel data is not enabled, and an unknown day is nil.
func TestFunnelDisabledWithoutSnapshots(t *testing.T) {
	st := newStore(t)
	insertAll(t, st, adDay(t, "Other", "2026-03-09", t0, core.AdDay{AdID: "x", Spend: 5, Currency: "USD"}))

	rep := funnel(t, st.Scoped("App"), "2026-03-09", "2026-03-09", store.FunnelOptions{})
	if rep.Enabled {
		t.Error("another product's ads must not enable this product's funnel")
	}
	if d := rep.Days[0]; d.Spend != nil || d.Installs != nil {
		t.Errorf("unreported day should be nil: %+v", d)
	}
	if rep.Totals.Spend != nil {
		t.Errorf("total of nil days should be nil, got %v", floatv(rep.Totals.Spend))
	}
}

func TestFunnelConvertsSpend(t *testing.T) {
	st := newStore(t)
	insertAll(t, st,
		adDay(t, "App", "2026-03-09", t0, core.AdDay{AdID: "eu", Spend: 8, Currency: "EUR"}),
		adDay(t, "App", "2026-03-09", t0, core.AdDay{AdID: "us", Spend: 3, Currency: "usd"}),
		adDay(t, "App", "2026-03-09", t0, core.AdDay{AdID: "xx", Spend: 99, Currency: "XXX"}),
	)
	convert := func(amount float64, cur string) (float64, bool) {
		if cur == "EUR" {
			return amount / 0.8, true
		}
		return 0, false
	}
	rep := funnel(t, st, "2026-03-09", "2026-03-09", store.FunnelOptions{Convert: convert})
	// 8 EUR at 0.8 is 10 USD; USD needs no rate; XXX has none and is left out.
	if got := floatv(rep.Days[0].Spend); got != 13.0 {
		t.Errorf("spend = %v, want 13", got)
	}
	if rep.Ads[0].AdID != "eu" || rep.Ads[0].Spend != 10 {
		t.Errorf("ads should sort by converted spend: %+v", rep.Ads)
	}
}

func TestLabelMatchingAndLinkVisits(t *testing.T) {
	st := newStore(t)
	day := "2026-03-09"
	insertAll(t, st,
		adDay(t, "App", day, t0, core.AdDay{AdID: "a14", Spend: 5, Currency: "USD",
			URLs: []string{"https://example.com/go/AD-14?utm=x"}}),
		adDay(t, "App", day, t0, core.AdDay{AdID: "a2", Spend: 4, Currency: "USD",
			URLs: []string{"https://example.com/go/ad-2", "https://example.com/go/shared"}}),
		adDay(t, "App", day, t0, core.AdDay{AdID: "a3", Spend: 3, Currency: "USD",
			URLs: []string{"https://example.com/go/shared#x"}}),
		linkDay(t, "site", day, t0, "ad-14", 7),
		// A revised count for the same label replaces the old one.
		linkDay(t, "site", day, t0.Add(time.Hour), "ad-14", 9),
		// "4" is inside "ad-14" but not a whole piece of it.
		linkDay(t, "site", day, t0, "4", 100),
		// Two ads point at "shared": the day counts it, neither ad does.
		linkDay(t, "site", day, t0, "shared", 5),
		// Nobody's ad points here.
		linkDay(t, "site", day, t0, "newsletter", 50),
	)

	rep := funnel(t, st.Scoped("App"), day, day, store.FunnelOptions{})
	if got := intv(rep.Days[0].LinkVisits); got != 14 {
		t.Errorf("day link visits = %v, want 9 + 5", got)
	}
	visits := map[string]any{}
	for _, a := range rep.Ads {
		visits[a.AdID] = intv(a.LinkVisits)
	}
	if visits["a14"] != 9 || visits["a2"] != nil || visits["a3"] != nil {
		t.Errorf("per-ad visits = %v", visits)
	}

	for _, c := range []struct {
		label, url string
		want       bool
	}{
		{"ad-14", "https://x.com/go/ad-14", true},
		{"ad-14", "https://x.com/go/ad-14/", true},
		{"ad-14", "https://x.com/go/ad-14&b", true},
		{"ad-14", "https://x.com/go/ad-140", false},
		{"4", "https://x.com/go/ad-14", false},
		{"14", "https://x.com/go/ad-14", true},
		{"/go/ad-14", "https://x.com/go/ad-14?x", true},
		{"", "https://x.com/", false},
	} {
		if got := store.LabelMatchesURL(c.label, c.url); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.label, c.url, got, c.want)
		}
	}
}

// With no ad URLs to tie links by, the day counts every link in scope.
func TestLinkVisitsWithoutURLs(t *testing.T) {
	st := newStore(t)
	day := "2026-03-09"
	insertAll(t, st,
		adDay(t, "App", day, t0, core.AdDay{AdID: "a", Spend: 1, Currency: "USD"}),
		linkDay(t, "App", day, t0, "one", 3),
		linkDay(t, "App", day, t0, "two", 4),
		linkDay(t, "Other", day, t0, "three", 50),
	)
	rep := funnel(t, st.Scoped("App"), day, day, store.FunnelOptions{})
	if got := intv(rep.Days[0].LinkVisits); got != 7 {
		t.Errorf("link visits = %v, want 7", got)
	}
	if rep.Ads[0].LinkVisits != nil {
		t.Errorf("an ad with no URL gets no link visits")
	}
}

func TestFunnelSiteEvents(t *testing.T) {
	st := newStore(t)
	day := "2026-03-09"
	ev := func(name string, n int, at time.Time) core.Event {
		return funnelEvent(t, "web", core.KindSiteEventDay, "App", day, name, at, core.SiteEventDay{Name: name, Count: n})
	}
	insertAll(t, st, ev("Lead", 3, t0), ev("Lead", 5, t0.Add(time.Hour)), ev("PageView", 400, t0))

	if rep := funnel(t, st, day, day, store.FunnelOptions{}); rep.Days[0].SiteEvents != nil {
		t.Error("site events must be nil when none is configured")
	}
	rep := funnel(t, st, day, day, store.FunnelOptions{SiteEvent: "lead"})
	if got := intv(rep.Days[0].SiteEvents); got != 5 {
		t.Errorf("site events = %v, want the newest 5", got)
	}
	if rep.AsOf["site_events"] != day {
		t.Errorf("as_of = %v", rep.AsOf)
	}
}

// Installs, purchases and revenue are the store data the vault reads, by its
// rules, and a day the store has not reported yet is nil rather than zero.
func TestFunnelStoreStages(t *testing.T) {
	st := newStore(t)
	ledger := func(source, kind, day string, units int, amount float64, key string) core.Event {
		e := ledgerRow(source, "App", day, "US", units, amount, key)
		e.Kind = kind
		return e
	}
	install := func(source, day, country string, n int, key string) core.Event {
		occurred, _ := time.Parse(core.DayLayout, day)
		return core.Event{ID: core.NewID(), Source: source, Kind: "install", App: "App", Day: day,
			OccurredAt: occurred, ObservedAt: occurred, Country: country, Quantity: n, Silent: true, DedupeKey: key}
	}
	insertAll(t, st,
		adDay(t, "App", "2026-03-12", t0, core.AdDay{AdID: "a", Spend: 1, Currency: "USD"}),
		// App Store: free downloads and paid sales are its installs.
		ledger("appstore", "download", "2026-03-09", 6, 0, "as:dl:9"),
		ledger("appstore", "sale", "2026-03-09", 2, 5, "as:sale:9"),
		ledger("appstore", "iap", "2026-03-09", 1, 3, "as:iap:9"),
		ledger("appstore", "refund", "2026-03-09", -1, -2.5, "as:ref:9"),
		ledger("appstore", "download", "2026-03-11", 4, 0, "as:dl:11"),
		// Play: its install report is the installs; overview and country
		// rows are one day, not two; a paid sale is not a second install.
		install("googleplay", "2026-03-09", "", 10, "gp:i:9"),
		install("googleplay", "2026-03-09", "US", 10, "gp:i:9:us"),
		ledger("googleplay", "sale", "2026-03-09", 3, 6, "gp:sale:9"),
	)
	rep := funnel(t, st.Scoped("App"), "2026-03-09", "2026-03-12", store.FunnelOptions{})

	d9 := dayOf(t, rep, "2026-03-09")
	if got := intv(d9.Installs); got != 6+2+10 {
		t.Errorf("installs = %v, want 18", got)
	}
	if got := intv(d9.Purchases); got != 2+1+3 {
		t.Errorf("purchases = %v, want 6 paid units", got)
	}
	if got := floatv(d9.Revenue); got != 5+3-2.5+6.0 {
		t.Errorf("revenue = %v, want 11.5", got)
	}
	// The 10th is before the newest report, so it is a reported quiet day.
	d10 := dayOf(t, rep, "2026-03-10")
	if intv(d10.Installs) != 0 || intv(d10.Purchases) != 0 || floatv(d10.Revenue) != 0.0 {
		t.Errorf("a reported quiet day should be zero: %+v", d10)
	}
	if got := intv(dayOf(t, rep, "2026-03-11").Installs); got != 4 {
		t.Errorf("installs on the 11th = %v", got)
	}
	// Nothing has reported the 12th yet: nil, not zero.
	d12 := dayOf(t, rep, "2026-03-12")
	if d12.Installs != nil || d12.Purchases != nil || d12.Revenue != nil {
		t.Errorf("an unreported day should be nil: %+v", d12)
	}
	if rep.AsOf["installs"] != "2026-03-11" || rep.AsOf["sales"] != "2026-03-11" || rep.AsOf["ads"] != "2026-03-12" {
		t.Errorf("as_of = %v", rep.AsOf)
	}
	if got := intv(rep.Totals.Installs); got != 22 {
		t.Errorf("total installs = %v", got)
	}
}
