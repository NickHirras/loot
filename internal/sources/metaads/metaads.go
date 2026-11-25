// Package metaads polls the Meta (Facebook and Instagram) Marketing API for
// what each ad delivered on each day, and optionally for how often each event
// on the operator's tracking pixel fired.
//
// # What it reads (Graph API, verified against Meta's reference, Nov 2026)
//
//	GET /{v}/act_{id}?fields=name,currency,timezone_name,account_status
//	GET /{v}/act_{id}/insights?level=ad&time_increment=1
//	        &time_range={"since":"YYYY-MM-DD","until":"YYYY-MM-DD"}
//	        &fields=ad_id,ad_name,adset_id,adset_name,campaign_id,campaign_name,
//	                spend,impressions,reach,inline_link_clicks,actions,account_currency
//	        &use_account_attribution_setting=true
//	GET /{v}/act_{id}/ads?fields=id,name,effective_status,
//	        creative{object_story_spec,asset_feed_spec,link_url,object_url}
//	GET /{v}/{pixel_id}/stats?aggregation=event&start_time=<unix>&end_time=<unix>
//
// Insights rows arrive one per (ad, day) with every metric as a string
// ("spend": "12.34"), and with date_start/date_stop in the ad account's own
// timezone. actions is a list of {action_type, value}: landing page views
// are action_type "landing_page_view", and the operator's conversion is
// whatever action types they configured (a custom conversion reads as
// "offsite_conversion.custom.<id>"). Every list edge pages with paging.next,
// which carries the caller's token, so it is rebased onto our own base URL and
// never logged.
//
// Pixel stats are hourly AdsPixelStatsResult buckets ({start_time, aggregation,
// data: [{value, count}]}); with aggregation=event the value is the event
// name. Meta documents those inner field names only loosely and serves at most
// the last seven days, so the parser accepts a count as number or string and
// the pixel window never reaches further back than that.
//
// # Why every day is re-read
//
// Meta keeps attributing conversions back to the click that earned them for
// days after the click, so yesterday's numbers are not final today. Every poll
// re-reads a trailing window (LookbackDays) and emits a fresh snapshot for any
// (ad, day) whose numbers changed; an unchanged day has the same fingerprint,
// so the same dedupe key, and the pipeline drops it. See core/funnel.go.
//
// # What is emitted
//
//	ad_day          silent snapshot per (ad, day)            core.AdDay
//	site_event_day  silent snapshot per (pixel event, day)   core.SiteEventDay
//	ads_day         one chest drop per completed day with spend  core.AdsDaySummary
//
// Funnel events are never ledger events and carry Amount and Quantity zero:
// ad spend is not income, and nothing here may reach the vault.
package metaads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	// The account's day is its own timezone's day, and Loot's release image is
	// distroless with no /usr/share/zoneinfo. Embedding the tz database is
	// what keeps "yesterday" right in a container (appstore does the same).
	_ "time/tzdata"

	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier.
const Name = "metaads"

const (
	// DefaultAPIVersion is the Graph / Marketing API version requests are
	// pinned to. Meta's Marketing API versioning page lists v27.0 as current
	// (released 4 Nov 2026); each version lives roughly two years. Bump it
	// deliberately: a version change can rename insights fields.
	DefaultAPIVersion = "v27.0"
	// DefaultBaseURL is the Graph API host.
	DefaultBaseURL = "https://graph.facebook.com"

	DefaultPollInterval = time.Hour
	DefaultBackfillDays = 30
	DefaultLookbackDays = 7

	// pixelMaxDays is how far back the pixel stats edge serves data: "up to
	// seven days from the request time", per its reference page.
	pixelMaxDays = 7
	// maxURLs caps AdDay.URLs. A carousel can carry a link per card; past a
	// handful they add nothing to matching an ad to a tracked link.
	maxURLs = 10
)

const dayLayout = core.DayLayout

// Options configures the source. config/serve.go maps `sources.metaads:` onto
// it.
type Options struct {
	// AccessToken is a token with ads_read on the ad account (a Business
	// system user token, ideally). Required.
	AccessToken string
	// AdAccountID is "act_123..." or the bare digits; either is accepted.
	AdAccountID string
	// App is the app name events are filed under. Required.
	App string
	// PixelID, when set, adds per-event daily counts from that pixel.
	PixelID string
	// ConversionActions are the insights action_type values that count as
	// "the conversion", e.g. "offsite_conversion.custom.<id>" or
	// "offsite_conversion.fb_pixel_custom". Their values are summed.
	// Optional, but without them no conversion is credited to any ad.
	ConversionActions []string
	APIVersion        string
	BaseURL           string
	PollInterval      time.Duration
	// BackfillDays is how far back the first poll reads; LookbackDays is the
	// trailing window every later poll re-reads to catch revisions.
	BackfillDays int
	LookbackDays int
	HTTPClient   *http.Client
}

// Source implements core.Source and core.Checker.
type Source struct {
	opts      Options
	accountID string // digits only
	convSet   map[string]bool
	log       *slog.Logger
	// Now is swappable for tests.
	Now func() time.Time
}

// New validates opts, fills in defaults and returns the source.
func New(opts Options, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	opts.AccessToken = strings.TrimSpace(opts.AccessToken)
	if opts.AccessToken == "" {
		return nil, errors.New("metaads: access_token is required")
	}
	opts.App = strings.TrimSpace(opts.App)
	if opts.App == "" {
		return nil, errors.New("metaads: app is required (the app name ad events are filed under)")
	}
	id, err := normalizeAccountID(opts.AdAccountID)
	if err != nil {
		return nil, err
	}
	opts.PixelID = strings.TrimSpace(opts.PixelID)
	if opts.PixelID != "" && !allDigits(opts.PixelID) {
		return nil, fmt.Errorf("metaads: pixel_id %q is not a numeric pixel id", opts.PixelID)
	}
	if opts.APIVersion == "" {
		opts.APIVersion = DefaultAPIVersion
	}
	if !strings.HasPrefix(opts.APIVersion, "v") {
		opts.APIVersion = "v" + opts.APIVersion
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.BackfillDays <= 0 {
		opts.BackfillDays = DefaultBackfillDays
	}
	if opts.LookbackDays <= 0 {
		opts.LookbackDays = DefaultLookbackDays
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	conv := map[string]bool{}
	for _, a := range opts.ConversionActions {
		if a = strings.TrimSpace(a); a != "" {
			conv[a] = true
		}
	}
	return &Source{opts: opts, accountID: id, convSet: conv, log: log, Now: time.Now}, nil
}

func normalizeAccountID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	id = strings.TrimPrefix(strings.TrimPrefix(id, "act_"), "ACT_")
	if id == "" {
		return "", errors.New("metaads: ad_account_id is required (\"act_<digits>\" from Ads Manager)")
	}
	if !allDigits(id) {
		return "", fmt.Errorf("metaads: ad_account_id %q is not act_<digits>", raw)
	}
	return id, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source. Meta refreshes insights through the
// day; hourly keeps "today" current without spending the account's rate
// limit.
func (s *Source) PollInterval() time.Duration { return s.opts.PollInterval }

// Check implements core.Checker with one cheap authenticated read of the ad
// account: it proves the token, the permission and the account id together.
func (s *Source) Check(ctx context.Context) error {
	acct, err := s.fetchAccount(ctx)
	if err != nil {
		return err
	}
	var warn []string
	// 1 is ACTIVE. Anything else (disabled, unsettled, in grace period...)
	// still reads, but will report no new delivery.
	if acct.AccountStatus != 0 && acct.AccountStatus != 1 {
		warn = append(warn, fmt.Sprintf("ad account status is %d, not active: it will report no new delivery", acct.AccountStatus))
	}
	if _, err := time.LoadLocation(acct.TimezoneName); acct.TimezoneName == "" || err != nil {
		warn = append(warn, fmt.Sprintf("unknown account timezone %q: days will be read as UTC", acct.TimezoneName))
	}
	if len(s.convSet) == 0 {
		warn = append(warn, "no conversion_actions configured: conversions will not be credited to ads")
	}
	if len(warn) > 0 {
		return core.Warning{Msg: "metaads: " + strings.Join(warn, "; ")}
	}
	return nil
}

// state is what Loot persists between polls.
type state struct {
	// LastSummarized is the newest account-local day an ads_day summary was
	// considered for (whether or not it had spend). Empty means never polled.
	LastSummarized string `json:"last_summarized,omitempty"`
}

func decodeState(raw []byte) state {
	var st state
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

// Poll implements core.Source.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	observed := s.Now().UTC()

	acct, err := s.fetchAccount(ctx)
	if err != nil {
		return nil, raw, err
	}
	loc := s.location(acct.TimezoneName)
	local := observed.In(loc)
	today := local.Format(dayLayout)
	yesterday := local.AddDate(0, 0, -1).Format(dayLayout)

	since := s.windowStart(st, local)

	rows, err := s.fetchInsights(ctx, since, today)
	if err != nil {
		return nil, raw, err
	}
	ads, err := s.fetchAds(ctx)
	if err != nil {
		// Status and URLs are part of every snapshot's fingerprint; emitting
		// without them would mint a spurious "revision" of every day now and
		// another when the ads read recovers.
		return nil, raw, err
	}

	days := s.buildAdDays(rows, ads, acct.Currency)
	events := make([]core.Event, 0, len(days)+2)
	for _, d := range days {
		ev, err := snapshotEvent(s.opts.App, core.KindAdDay, d.AdID, d.day, d.AdDay, observed)
		if err != nil {
			return nil, raw, err
		}
		events = append(events, ev)
	}

	for _, day := range s.summaryDays(st, since, yesterday) {
		sum, ok := summarize(days, day, acct.Currency)
		if !ok {
			continue
		}
		events = append(events, summaryEvent(s.opts.App, day, sum, observed))
	}

	// The pixel is optional and independent: its failure is reported, but
	// does not hold back the ad snapshots or the summary cursor.
	var pixelErr error
	if s.opts.PixelID != "" {
		pevs, err := s.pixelEvents(ctx, loc, local, since, observed)
		if err != nil {
			pixelErr = err
			s.log.Warn("metaads: pixel stats failed", "error", err)
		}
		events = append(events, pevs...)
	}

	if yesterday > st.LastSummarized {
		st.LastSummarized = yesterday
	}
	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("metaads: encode state: %w", err)
	}
	return events, out, pixelErr
}

func (s *Source) location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		s.log.Warn("metaads: unknown account timezone, using UTC", "timezone", name)
		return time.UTC
	}
	return loc
}

// windowStart is the first day the insights read covers. The first poll
// reads BackfillDays; later polls read LookbackDays, reaching further back
// only to cover days the summary cursor has not reached yet (after Loot was
// down for a while), and never past BackfillDays. The window always includes
// yesterday, the day the first poll summarizes.
func (s *Source) windowStart(st state, local time.Time) string {
	back := s.opts.LookbackDays
	if st.LastSummarized == "" {
		back = s.opts.BackfillDays
	}
	if back < 1 {
		back = 1
	}
	since := local.AddDate(0, 0, -back).Format(dayLayout)
	if st.LastSummarized != "" {
		if t, err := time.Parse(dayLayout, st.LastSummarized); err == nil {
			next := t.AddDate(0, 0, 1).Format(dayLayout)
			floor := local.AddDate(0, 0, -max(s.opts.BackfillDays, back)).Format(dayLayout)
			if next < since {
				since = max(next, floor)
			}
		}
	}
	return since
}

// summaryDays lists the completed days that get an ads_day summary this
// poll. The first poll summarizes only yesterday: a 30 day backfill is
// history for the funnel page, not thirty chests of news.
func (s *Source) summaryDays(st state, since, yesterday string) []string {
	if st.LastSummarized == "" {
		return []string{yesterday}
	}
	cursor, err := time.Parse(dayLayout, st.LastSummarized)
	if err != nil {
		return []string{yesterday}
	}
	start := cursor.AddDate(0, 0, 1)
	// After an outage longer than BackfillDays the window starts after the
	// cursor; days before it were never read and are skipped.
	if t, err := time.Parse(dayLayout, since); err == nil && t.After(start) {
		start = t
	}
	var out []string
	for t := start; t.Format(dayLayout) <= yesterday; t = t.AddDate(0, 0, 1) {
		out = append(out, t.Format(dayLayout))
	}
	return out
}

// adDay is an AdDay together with the day it belongs to.
type adDay struct {
	core.AdDay
	day string
}

// buildAdDays folds insights rows into one AdDay per (ad, day), sorted by day
// then ad id so a poll's output is deterministic.
func (s *Source) buildAdDays(rows []insightRow, ads []adInfo, fallbackCurrency string) []adDay {
	info := make(map[string]adInfo, len(ads))
	urls := make(map[string][]string, len(ads))
	for _, a := range ads {
		info[a.ID] = a
		urls[a.ID] = creativeURLs(a.Creative)
	}

	// Tracked is per ad over the whole window: Meta omits an action type on
	// a day nobody took it, so one day's row cannot tell "not tracked" from
	// "nobody converted".
	tracked := map[string]bool{}
	for _, r := range rows {
		for _, a := range r.Actions {
			if s.convSet[a.ActionType] {
				tracked[r.AdID] = true
			}
		}
	}

	byKey := map[string]*adDay{}
	var keys []string
	for _, r := range rows {
		if r.AdID == "" || r.DateStart == "" {
			continue
		}
		key := r.DateStart + "\x00" + r.AdID
		d, ok := byKey[key]
		if !ok {
			a := info[r.AdID]
			name := r.AdName
			if name == "" {
				name = a.Name
			}
			cur := r.AccountCurrency
			if cur == "" {
				cur = fallbackCurrency
			}
			d = &adDay{day: r.DateStart, AdDay: core.AdDay{
				AdID:       r.AdID,
				Ad:         name,
				AdSetID:    r.AdSetID,
				AdSet:      r.AdSetName,
				CampaignID: r.CampaignID,
				Campaign:   r.CampaignName,
				Status:     a.EffectiveStatus,
				URLs:       urls[r.AdID],
				Currency:   cur,
				Tracked:    tracked[r.AdID],
			}}
			byKey[key] = d
			keys = append(keys, key)
		}
		// One row per (ad, day) is what level=ad&time_increment=1 returns;
		// summing makes a duplicate harmless rather than a lost row.
		d.Spend = roundCents(d.Spend + r.Spend.v)
		d.Impressions += r.Impressions.int()
		d.Reach += r.Reach.int()
		var linkClickAction int
		var sawLinkClickAction bool
		for _, a := range r.Actions {
			switch {
			case a.ActionType == "landing_page_view":
				d.LandingPageViews += a.Value.int()
			case a.ActionType == "link_click":
				linkClickAction += a.Value.int()
				sawLinkClickAction = true
			}
			if s.convSet[a.ActionType] {
				d.Conversions += a.Value.int()
			}
		}
		// inline_link_clicks is the documented field; the link_click action
		// is the same count by another route, used only when the field is
		// missing.
		if r.InlineLinkClicks.set {
			d.LinkClicks += r.InlineLinkClicks.int()
		} else if sawLinkClickAction {
			d.LinkClicks += linkClickAction
		}
	}
	sort.Strings(keys)
	out := make([]adDay, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	return out
}

// roundCents keeps float sums of string amounts from drifting in the last
// digit, which would change a fingerprint without any number changing.
func roundCents(f float64) float64 {
	if f < 0 {
		return -roundCents(-f)
	}
	return float64(int64(f*100+0.5)) / 100
}

// summarize totals one day across every ad. ok is false when the day had no
// spend: a day the ads were off is not news.
func summarize(days []adDay, day, fallbackCurrency string) (core.AdsDaySummary, bool) {
	var sum core.AdsDaySummary
	var top *adDay
	for i := range days {
		d := &days[i]
		if d.day != day {
			continue
		}
		sum.Spend = roundCents(sum.Spend + d.Spend)
		sum.Impressions += d.Impressions
		sum.LinkClicks += d.LinkClicks
		sum.LandingPageViews += d.LandingPageViews
		sum.Conversions += d.Conversions
		if d.Spend > 0 || d.Impressions > 0 {
			sum.Ads++
		}
		if sum.Currency == "" {
			sum.Currency = d.Currency
		}
		if d.LandingPageViews > 0 && (top == nil ||
			d.LandingPageViews > top.LandingPageViews ||
			(d.LandingPageViews == top.LandingPageViews && (d.Spend > top.Spend ||
				(d.Spend == top.Spend && d.Ad < top.Ad)))) {
			top = d
		}
	}
	if sum.Spend <= 0 {
		return sum, false
	}
	if sum.Currency == "" {
		sum.Currency = fallbackCurrency
	}
	if top != nil {
		sum.TopAd = top.Ad
	}
	return sum, true
}

// pixelEvents reads the pixel's hourly per-event counts and folds them into
// account-local days. The window starts at local midnight so its first day is
// whole, and never reaches past the seven days the edge serves.
func (s *Source) pixelEvents(ctx context.Context, loc *time.Location, local time.Time, since string, observed time.Time) ([]core.Event, error) {
	startDay := local.AddDate(0, 0, -(pixelMaxDays - 1)).Format(dayLayout)
	if since > startDay {
		startDay = since
	}
	start, err := time.ParseInLocation(dayLayout, startDay, loc)
	if err != nil {
		return nil, err
	}
	buckets, err := s.fetchPixelStats(ctx, start.Unix(), observed.Unix())
	if err != nil {
		return nil, err
	}
	counts := map[[2]string]int{} // (day, event) -> count
	for _, b := range buckets {
		t, ok := parseTime(b.StartTime)
		if !ok {
			s.log.Warn("metaads: skipping pixel bucket with unreadable start_time", "start_time", b.StartTime)
			continue
		}
		day := t.In(loc).Format(dayLayout)
		if day < startDay {
			continue
		}
		for _, e := range b.Data {
			if e.Value == "" {
				continue
			}
			counts[[2]string{day, e.Value}] += e.Count.int()
		}
	}
	keys := make([][2]string, 0, len(counts))
	for k, n := range counts {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	events := make([]core.Event, 0, len(keys))
	for _, k := range keys {
		ev, err := snapshotEvent(s.opts.App, core.KindSiteEventDay, k[1], k[0],
			core.SiteEventDay{Name: k[1], Count: counts[k]}, observed)
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// parseTime reads a pixel bucket's start_time. Graph's datetime fields are
// ISO 8601 with a numeric offset and no colon ("2026-11-20T10:00:00+0000");
// RFC 3339 and Unix seconds are accepted too, since the shape is only loosely
// documented.
func parseTime(v string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	var n flexNum
	if err := n.UnmarshalJSON([]byte(v)); err == nil && n.set && n.v > 1e9 {
		return time.Unix(int64(n.v), 0), true
	}
	return time.Time{}, false
}

// snapshotEvent wraps a funnel payload in the event every funnel snapshot
// shares: silent, not ledger, zero amount and quantity, keyed by a
// fingerprint of the payload so only a changed day is news.
func snapshotEvent(app, kind, entity, day string, payload any, observed time.Time) (core.Event, error) {
	occurred, err := time.ParseInLocation(dayLayout, day, time.UTC)
	if err != nil {
		return core.Event{}, fmt.Errorf("metaads: bad day %q from the API", day)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.Event{}, fmt.Errorf("metaads: encode %s payload: %w", kind, err)
	}
	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       kind,
		App:        app,
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        day,
		DedupeKey:  core.FunnelSnapshotKey(Name, kind, entity, day, fingerprint(body)),
		Silent:     true,
		Payload:    body,
	}, nil
}

// summaryEvent is the day's one ads drop. It is news, emitted once, so its
// key carries no fingerprint: a later revision of the day must not mint a
// second chest drop.
func summaryEvent(app, day string, sum core.AdsDaySummary, observed time.Time) core.Event {
	occurred, _ := time.ParseInLocation(dayLayout, day, time.UTC)
	body, _ := json.Marshal(sum)
	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       core.KindAdsDay,
		App:        app,
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        day,
		DedupeKey:  fmt.Sprintf("%s:%s:%s:%s", Name, core.KindAdsDay, app, day),
		Chest:      true,
		Payload:    body,
	}
}

// fingerprint is the first 12 hex digits of the payload's SHA-256: plenty to
// tell one revision of a day from another, short enough to read in a key.
func fingerprint(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:12]
}

// creativeURLs walks a creative's JSON and collects every http(s) string in
// it. Creative shapes differ by format (link ad, carousel, dynamic asset
// feed, ...) and change between API versions, so nothing is modelled: a
// destination is wherever a URL turns up. Image and video subtrees are
// skipped, and so are Meta's own CDN hosts, because a picture's address is
// not where the ad sends anyone.
func creativeURLs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(key string, v any)
	walk = func(key string, v any) {
		k := strings.ToLower(key)
		for _, media := range []string{"image", "picture", "video", "thumbnail", "icon"} {
			if strings.Contains(k, media) {
				return
			}
		}
		switch x := v.(type) {
		case map[string]any:
			for ck, cv := range x {
				walk(ck, cv)
			}
		case []any:
			for _, cv := range x {
				walk(key, cv)
			}
		case string:
			u := strings.TrimSpace(x)
			lu := strings.ToLower(u)
			if (strings.HasPrefix(lu, "http://") || strings.HasPrefix(lu, "https://")) && !isMetaCDN(lu) {
				seen[u] = true
			}
		}
	}
	walk("", v)
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	if len(out) > maxURLs {
		out = out[:maxURLs]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isMetaCDN(lowerURL string) bool {
	rest := lowerURL[strings.Index(lowerURL, "//")+2:]
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	return strings.HasSuffix(host, ".fbcdn.net") || host == "fbcdn.net"
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
