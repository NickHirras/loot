package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The acquisition funnel, read side. See internal/core/funnel.go for what the
// events mean and why every funnel number is a snapshot.
//
// The page puts two very different kinds of number on one column:
//
//   - the top of the funnel (spend, views, link visits, site events) comes
//     from snapshot events that an ad platform keeps revising, so each is the
//     newest snapshot of its key and is never summed with an older one;
//   - the bottom (installs, purchases, revenue) is the same ledger and install
//     data the vault and the quests already read, by exactly their rules, for
//     every customer of the product. No store says which ad an install came
//     from, so nothing here pretends to join the two halves per ad.

// FunnelOptions is what the funnel needs that the store does not know.
type FunnelOptions struct {
	// DisplayCurrency is what every money field is reported in.
	DisplayCurrency string
	// Convert turns an amount in currency into the display currency. Nil
	// converts only amounts already in it.
	Convert func(amount float64, currency string) (float64, bool)
	// ConversionLabel and SiteEvent are passed through from the config;
	// SiteEvent also names the event counted in FunnelDay.SiteEvents.
	ConversionLabel string
	SiteEvent       string
}

// funnelSnapshotKinds are the event kinds that make the funnel exist at all.
var funnelSnapshotKinds = []string{core.KindAdDay, core.KindLinkDay, core.KindSiteEventDay}

// installReported is true for an event whose (source, product) reports
// installs as `install` rows of its own (Google Play, Flathub). Such a pair's
// ledger rows are purchases, not installs: a paid Play app's sale is one of
// the installs Play already counted, and adding it would count that customer
// twice. Stores with no install report (App Store, Microsoft Store) count
// their first acquisitions, free `download` and paid `sale`, as the installs.
const installReported = `(e.source || char(31) || e.product) IN (
    SELECT DISTINCT i.source || char(31) || i.product FROM events i WHERE i.kind = 'install')`

// installsStageRows are the events whose presence on a day means a store has
// reported installs for it: the install rows and their daily summary, and the
// ledger rows of the stores whose installs are ledger rows.
const installsStageRows = `(e.kind IN ('install', 'installs_day') OR (e.is_ledger = 1 AND NOT ` + installReported + `))`

// funnelAdRow is one newest ad_day snapshot.
type funnelAdRow struct {
	source, day string
	ad          core.AdDay
}

// newestSnapshots returns the payload of the newest snapshot of kind per
// (source, product, day, payload id) in [from, to]. scope is an already
// rendered filter on alias e.
//
// "Newest" is by observed_at, then id: two polls inside one millisecond still
// have an order, and the ULID-like id is minted later for the later one.
func (s *Store) newestSnapshots(ctx context.Context, kind, idPath, from, to, scope string, scopeArgs []any, extra string, extraArgs ...any) ([]snapshotRow, error) {
	args := append([]any{kind, from, to}, scopeArgs...)
	args = append(args, extraArgs...)
	rows, err := s.q.QueryContext(ctx, `
        SELECT source, product, day, payload FROM (
            SELECT e.source AS source, e.product AS product, e.day AS day, e.payload AS payload,
                   ROW_NUMBER() OVER (
                       PARTITION BY e.source, e.product, e.day, json_extract(e.payload, '`+idPath+`')
                       ORDER BY e.observed_at DESC, e.id DESC) AS rn
            FROM events e
            WHERE e.kind = ? AND e.day BETWEEN ? AND ?`+scope+extra+`)
        WHERE rn = 1
        ORDER BY day, source`, args...)
	if err != nil {
		return nil, fmt.Errorf("funnel %s: %w", kind, err)
	}
	defer rows.Close()

	var out []snapshotRow
	for rows.Next() {
		var r snapshotRow
		if err := rows.Scan(&r.source, &r.product, &r.day, &r.payload); err != nil {
			return nil, fmt.Errorf("scan funnel %s: %w", kind, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate funnel %s: %w", kind, err)
	}
	return out, nil
}

type snapshotRow struct {
	source, product, day string
	payload              []byte
}

// Funnel builds the funnel for the inclusive day range [from, to] on this
// store's scope.
func (s *Store) Funnel(ctx context.Context, from, to string, opts FunnelOptions) (core.FunnelReport, error) {
	n, err := dayCount(from, to)
	if err != nil {
		return core.FunnelReport{}, err
	}
	display := strings.ToUpper(strings.TrimSpace(opts.DisplayCurrency))
	rep := core.FunnelReport{
		Product:         s.Product(),
		From:            from,
		To:              to,
		Currency:        display,
		ConversionLabel: opts.ConversionLabel,
		SiteEvent:       opts.SiteEvent,
		Days:            make([]core.FunnelDay, n),
		Ads:             []core.FunnelAd{},
		Notes:           []core.FunnelNote{},
		AsOf:            map[string]string{},
	}
	start, _ := time.Parse(core.DayLayout, from)
	index := make(map[string]int, n)
	for i := range rep.Days {
		day := core.DayOf(start.AddDate(0, 0, i))
		rep.Days[i].Day = day
		index[day] = i
	}

	if rep.Enabled, err = s.funnelEnabled(ctx); err != nil {
		return rep, err
	}
	if err := s.funnelAsOf(ctx, opts.SiteEvent, rep.AsOf); err != nil {
		return rep, err
	}

	ads, err := s.funnelAds(ctx, from, to, display, opts.Convert, &rep, index)
	if err != nil {
		return rep, err
	}
	if err := s.funnelLinks(ctx, from, to, &rep, index, ads); err != nil {
		return rep, err
	}
	sortFunnelAds(rep.Ads)
	if err := s.funnelSiteEvents(ctx, from, to, opts.SiteEvent, &rep, index); err != nil {
		return rep, err
	}
	if err := s.funnelStores(ctx, from, to, &rep, index); err != nil {
		return rep, err
	}
	rep.Totals = funnelTotals(rep.Days)
	return rep, nil
}

// funnelEnabled reports whether any funnel snapshot exists in scope, ever.
func (s *Store) funnelEnabled(ctx context.Context) (bool, error) {
	strict, strictArgs := s.scopeStrict("e")
	var found bool
	err := s.q.QueryRowContext(ctx, `
        SELECT EXISTS (SELECT 1 FROM events e WHERE e.kind IN (?, ?, ?)`+strict+`)`,
		append([]any{funnelSnapshotKinds[0], funnelSnapshotKinds[1], funnelSnapshotKinds[2]}, strictArgs...)...).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("funnel enabled: %w", err)
	}
	return found, nil
}

// funnelAsOf records, per stage, the newest day anything has reported. It is
// all-time rather than range-bound on purpose: "the store has reported up to
// last Tuesday" is true whatever window the page is looking at.
func (s *Store) funnelAsOf(ctx context.Context, siteEvent string, asOf map[string]string) error {
	strict, strictArgs := s.scopeStrict("e")
	stages := []struct {
		stage, where string
		args         []any
		scoped       bool
	}{
		{"ads", `e.kind = ?`, []any{core.KindAdDay}, true},
		// Links are tied to ads by URL, not by product (see funnelLinks), so
		// a scoped view reads them from every product.
		{"links", `e.kind = ?`, []any{core.KindLinkDay}, false},
		{"installs", installsStageRows, nil, true},
		{"sales", `e.is_ledger = 1`, nil, true},
	}
	if siteEvent != "" {
		stages = append(stages, struct {
			stage, where string
			args         []any
			scoped       bool
		}{"site_events", `e.kind = ? AND LOWER(json_extract(e.payload, '$.name')) = LOWER(?)`,
			[]any{core.KindSiteEventDay, siteEvent}, true})
	}
	for _, st := range stages {
		q := `SELECT COALESCE(MAX(e.day), '') FROM events e WHERE ` + st.where
		args := st.args
		if st.scoped {
			q += strict
			args = append(append([]any{}, args...), strictArgs...)
		}
		var day string
		if err := s.q.QueryRowContext(ctx, q, args...).Scan(&day); err != nil {
			return fmt.Errorf("funnel as of %s: %w", st.stage, err)
		}
		if day != "" {
			asOf[st.stage] = day
		}
	}
	return nil
}

// funnelAdState is one ad while the report is being folded.
type funnelAdState struct {
	ad    core.FunnelAd
	urls  []string
	newer string // day of the snapshot the name and status came from
}

// funnelAds folds the newest ad_day snapshots into the days and one row per
// (source, ad_id), appended to rep.Ads in the same order as the returned
// states, which carry the URLs the link matching needs.
func (s *Store) funnelAds(ctx context.Context, from, to, display string,
	convert func(float64, string) (float64, bool), rep *core.FunnelReport, index map[string]int,
) ([]*funnelAdState, error) {
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.newestSnapshots(ctx, core.KindAdDay, "$.ad_id", from, to, strict, strictArgs, "")
	if err != nil {
		return nil, err
	}

	byKey := map[string]*funnelAdState{}
	var order []*funnelAdState
	type dayAgg struct {
		spend                    float64
		impr, clicks, lpv, convs int
	}
	agg := make([]dayAgg, len(rep.Days))
	for _, r := range rows {
		var p core.AdDay
		if err := json.Unmarshal(r.payload, &p); err != nil || p.AdID == "" {
			continue
		}
		i, ok := index[r.day]
		if !ok {
			continue
		}
		spend := convertSpend(p.Spend, p.Currency, display, convert)

		key := r.source + "\x1f" + p.AdID
		st, ok := byKey[key]
		if !ok {
			st = &funnelAdState{ad: core.FunnelAd{Source: r.source, AdID: p.AdID, Series: make([]int, len(rep.Days))}}
			byKey[key] = st
			order = append(order, st)
		}
		// Rows arrive in day order, so the last one seen is the newest day's.
		if r.day >= st.newer {
			st.newer = r.day
			st.ad.Ad, st.ad.AdSet, st.ad.Campaign, st.ad.Status = p.Ad, p.AdSet, p.Campaign, p.Status
		}
		st.urls = appendUnique(st.urls, p.URLs...)
		st.ad.Spend += spend
		st.ad.Impressions += p.Impressions
		st.ad.LinkClicks += p.LinkClicks
		st.ad.LandingPageViews += p.LandingPageViews
		st.ad.Conversions += p.Conversions
		st.ad.Tracked = st.ad.Tracked || p.Tracked
		st.ad.Series[i] += p.LandingPageViews

		agg[i].spend += spend
		agg[i].impr += p.Impressions
		agg[i].clicks += p.LinkClicks
		agg[i].lpv += p.LandingPageViews
		agg[i].convs += p.Conversions
	}

	// A day the ad platform has already reported past, with nothing on it, had
	// no ads running: zero. A day after its newest report is not known yet.
	asOf := rep.AsOf["ads"]
	for i := range rep.Days {
		if !reported(rep.Days[i].Day, asOf) {
			continue
		}
		a := agg[i]
		rep.Days[i].Spend = ptr(round2(a.spend))
		rep.Days[i].Impressions = ptr(a.impr)
		rep.Days[i].LinkClicks = ptr(a.clicks)
		rep.Days[i].LandingPageViews = ptr(a.lpv)
		rep.Days[i].Conversions = ptr(a.convs)
	}

	for _, st := range order {
		st.ad.Spend = round2(st.ad.Spend)
		rep.Ads = append(rep.Ads, st.ad)
	}
	return order, nil
}

// convertSpend puts an ad account's spend into the display currency. Funnel
// events carry their money in the payload rather than in Amount (so it can
// never leak into the vault), which means the pipeline's ingest-time
// conversion never sees it and it is converted here, at read time, with the
// same rates. Spend in a currency with no known rate is left out rather than
// shown in the wrong unit.
func convertSpend(amount float64, currency, display string, convert func(float64, string) (float64, bool)) float64 {
	if amount == 0 {
		return 0
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" || currency == display {
		return amount
	}
	if convert == nil {
		return 0
	}
	v, ok := convert(amount, currency)
	if !ok {
		return 0
	}
	return v
}

// funnelLinks folds the newest link_day snapshots into the days and the ads.
//
// A link belongs to an ad when the ad's URL contains the link's label (see
// LabelMatchesURL). That tie, not the product, is what scopes a link: link
// analytics usually know nothing about which app a link is for, so a scoped
// view reads links from every product and keeps the ones its own ads point
// at. Only when no ad has a URL at all does the day fall back to every link
// in scope, because then there is nothing to tie by.
func (s *Store) funnelLinks(ctx context.Context, from, to string, rep *core.FunnelReport, index map[string]int, ads []*funnelAdState) error {
	rows, err := s.newestSnapshots(ctx, core.KindLinkDay, "$.label", from, to, "", nil, "")
	if err != nil {
		return err
	}

	anyURL := false
	for _, a := range ads {
		if len(a.urls) > 0 {
			anyURL = true
			break
		}
	}

	totals := make([]int, len(rep.Days))
	perAd := map[*funnelAdState]int{}
	matchCache := map[string][]*funnelAdState{}
	for _, r := range rows {
		var p core.LinkDay
		if err := json.Unmarshal(r.payload, &p); err != nil || p.Label == "" {
			continue
		}
		i, ok := index[r.day]
		if !ok {
			continue
		}
		if !anyURL {
			if s.scope.active() && r.product != s.scope.product {
				continue
			}
			totals[i] += p.Visits
			continue
		}
		matches, ok := matchCache[p.Label]
		if !ok {
			for _, a := range ads {
				for _, u := range a.urls {
					if LabelMatchesURL(p.Label, u) {
						matches = append(matches, a)
						break
					}
				}
			}
			matchCache[p.Label] = matches
		}
		if len(matches) == 0 {
			// A link none of these ads points at: another campaign, another
			// app, or a link shared by hand. It is not this funnel's traffic.
			continue
		}
		totals[i] += p.Visits
		// A label two ads share cannot be split between them honestly, so it
		// counts for the day and for neither ad.
		if len(matches) == 1 {
			perAd[matches[0]] += p.Visits
		}
	}

	asOf := rep.AsOf["links"]
	for i := range rep.Days {
		if reported(rep.Days[i].Day, asOf) {
			rep.Days[i].LinkVisits = ptr(totals[i])
		}
	}
	for i, a := range ads {
		if n, ok := perAd[a]; ok {
			rep.Ads[i].LinkVisits = ptr(n)
		}
	}
	return nil
}

// LabelMatchesURL reports whether a link label appears in url as a whole
// path piece, case-insensitively. "ad-14" matches ".../go/ad-14?x=1", but "4"
// does not: the label must start the URL or follow a non-alphanumeric
// character, and end it or be followed by one of / ? # &. A label that itself
// begins or ends with punctuation ("/go/ad-14") brings its own boundary.
func LabelMatchesURL(label, url string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	u := strings.ToLower(url)
	if l == "" {
		return false
	}
	openStart := !isAlnum(l[0])
	openEnd := !isAlnum(l[len(l)-1])
	for from := 0; from <= len(u)-len(l); {
		j := strings.Index(u[from:], l)
		if j < 0 {
			return false
		}
		j += from
		end := j + len(l)
		before := j == 0 || openStart || !isAlnum(u[j-1])
		after := end == len(u) || openEnd || strings.IndexByte("/?#&", u[end]) >= 0
		if before && after {
			return true
		}
		from = j + 1
	}
	return false
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// funnelSiteEvents counts the configured website event per day.
func (s *Store) funnelSiteEvents(ctx context.Context, from, to, name string, rep *core.FunnelReport, index map[string]int) error {
	if name == "" {
		return nil
	}
	strict, strictArgs := s.scopeStrict("e")
	rows, err := s.newestSnapshots(ctx, core.KindSiteEventDay, "$.name", from, to, strict, strictArgs,
		` AND LOWER(json_extract(e.payload, '$.name')) = LOWER(?)`, name)
	if err != nil {
		return err
	}
	totals := make([]int, len(rep.Days))
	for _, r := range rows {
		var p core.SiteEventDay
		if err := json.Unmarshal(r.payload, &p); err != nil {
			continue
		}
		if i, ok := index[r.day]; ok {
			totals[i] += p.Count
		}
	}
	asOf := rep.AsOf["site_events"]
	for i := range rep.Days {
		if reported(rep.Days[i].Day, asOf) {
			rep.Days[i].SiteEvents = ptr(totals[i])
		}
	}
	return nil
}

// funnelStores fills installs, purchases and revenue from the store data the
// rest of Loot already reads, for every customer of the product.
//
//   - Installs: a store's own install report where it has one (installValue,
//     the same per-(source, app, day) rule the installs quest uses, so Play's
//     overview and country rows are not counted twice), otherwise the
//     store's first acquisitions in its ledger: free `download` and paid
//     `sale` units. Updates, refunds and Snapcraft's "new devices" are not
//     installs; RevenueCat is not a store report.
//   - Purchases: the vault's paid units (isPaidUnit over ledgerRows): sales,
//     in-app purchases and subscription payments, refunds not netted.
//   - Revenue: the vault's revenue, amount_base over ledgerRows, refunds
//     netted. RevenueCat's estimates stay out, as they do in the vault.
//
// When a stage is blank: stores report late, and a day they have not reported
// is not a day nothing happened. So a day is nil for a stage when nothing that
// reports that stage has any event on or after it (the day is after the
// stage's AsOf), and 0 when a later day has been reported and this one simply
// had nothing. With several stores the newest of them decides, so the last
// day or two can hold one store's figures and not yet another's; AsOf and the
// store_lag note say so.
func (s *Store) funnelStores(ctx context.Context, from, to string, rep *core.FunnelReport, index map[string]int) error {
	strict, strictArgs := s.scopeStrict("e")
	args := append([]any{from, to}, strictArgs...)
	args = append(args, args...)

	installs := make([]int, len(rep.Days))
	rows, err := s.q.QueryContext(ctx, `
        SELECT day, COALESCE(SUM(v), 0) FROM (
            SELECT e.day AS day, `+installValue+` AS v
            FROM events e
            WHERE e.kind = 'install' AND e.day BETWEEN ? AND ?`+strict+`
            GROUP BY e.source, e.app, e.day
            UNION ALL
            SELECT e.day AS day, e.quantity AS v
            FROM events e
            WHERE `+ledgerRows+` AND e.kind IN ('download', 'sale') AND e.quantity > 0
              AND NOT `+installReported+` AND e.day BETWEEN ? AND ?`+strict+`)
        GROUP BY day`, args...)
	if err != nil {
		return fmt.Errorf("funnel installs: %w", err)
	}
	for rows.Next() {
		var (
			day string
			n   int
		)
		if err := rows.Scan(&day, &n); err != nil {
			rows.Close()
			return fmt.Errorf("scan funnel installs: %w", err)
		}
		if i, ok := index[day]; ok {
			installs[i] = n
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("iterate funnel installs: %w", err)
	}

	units := make([]int, len(rep.Days))
	revenue := make([]float64, len(rep.Days))
	rows, err = s.q.QueryContext(ctx, `
        SELECT e.day,
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(e.amount_base), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?`+strict+`
        GROUP BY e.day`, append([]any{from, to}, strictArgs...)...)
	if err != nil {
		return fmt.Errorf("funnel sales: %w", err)
	}
	for rows.Next() {
		var (
			day string
			n   int
			rev float64
		)
		if err := rows.Scan(&day, &n, &rev); err != nil {
			rows.Close()
			return fmt.Errorf("scan funnel sales: %w", err)
		}
		if i, ok := index[day]; ok {
			units[i], revenue[i] = n, rev
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("iterate funnel sales: %w", err)
	}

	for i := range rep.Days {
		day := rep.Days[i].Day
		if reported(day, rep.AsOf["installs"]) {
			rep.Days[i].Installs = ptr(installs[i])
		}
		if reported(day, rep.AsOf["sales"]) {
			rep.Days[i].Purchases = ptr(units[i])
			rep.Days[i].Revenue = ptr(round2(revenue[i]))
		}
	}
	return nil
}

// reported is true when a stage whose newest reading is asOf has reported day:
// it has a reading on day or after it. A stage that never reported has not
// reported any day.
func reported(day, asOf string) bool { return asOf != "" && day <= asOf }

// funnelTotals sums the days. A total is nil only when every day is.
func funnelTotals(days []core.FunnelDay) core.FunnelDay {
	var t core.FunnelDay
	addI := func(dst **int, v *int) {
		if v == nil {
			return
		}
		if *dst == nil {
			*dst = ptr(0)
		}
		**dst += *v
	}
	addF := func(dst **float64, v *float64) {
		if v == nil {
			return
		}
		if *dst == nil {
			*dst = ptr(0.0)
		}
		**dst = round2(**dst + *v)
	}
	for _, d := range days {
		addF(&t.Spend, d.Spend)
		addI(&t.Impressions, d.Impressions)
		addI(&t.LinkClicks, d.LinkClicks)
		addI(&t.LandingPageViews, d.LandingPageViews)
		addI(&t.Conversions, d.Conversions)
		addI(&t.LinkVisits, d.LinkVisits)
		addI(&t.SiteEvents, d.SiteEvents)
		addI(&t.Installs, d.Installs)
		addI(&t.Purchases, d.Purchases)
		addF(&t.Revenue, d.Revenue)
	}
	return t
}

// sortFunnelAds orders ads by spend, biggest first, then by landing page
// views, then by name, so the order is stable for equal spend.
func sortFunnelAds(ads []core.FunnelAd) {
	sort.SliceStable(ads, func(i, j int) bool {
		a, b := ads[i], ads[j]
		if a.Spend != b.Spend {
			return a.Spend > b.Spend
		}
		if a.LandingPageViews != b.LandingPageViews {
			return a.LandingPageViews > b.LandingPageViews
		}
		if a.Ad != b.Ad {
			return a.Ad < b.Ad
		}
		return a.AdID < b.AdID
	})
}

func appendUnique(list []string, add ...string) []string {
	for _, v := range add {
		found := false
		for _, have := range list {
			if have == v {
				found = true
				break
			}
		}
		if !found && v != "" {
			list = append(list, v)
		}
	}
	return list
}

func ptr[T any](v T) *T { return &v }
