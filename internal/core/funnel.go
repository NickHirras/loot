package core

// The acquisition funnel: what it cost to put an app in front of people, and
// how far those people got on the way to a store.
//
// Every other number in Loot is something that happened once and stays
// happened. Funnel numbers are different in one way that shapes everything
// here: an ad platform keeps *revising* a day for a week afterwards, as late
// conversions are attributed back to the click that earned them. So funnel
// sources do what the audience sources do (see audience.go): each poll that
// finds a day's numbers changed emits a fresh *snapshot* event, and every
// reader takes the newest snapshot per (source, entity, day) and ignores the
// rest. Nothing is ever updated in place, and nothing is ever summed across
// snapshots.
//
// Funnel snapshot events are always Silent, never IsLedger, and carry Amount
// and Quantity of zero: what they describe is spend and traffic, not income
// or customers, and it must never leak into the vault, the hearth or a
// population. Everything lives in the payload.

const (
	// KindAdDay is one ad's delivery on one day, as the ad platform reports
	// it. Payload is an AdDay. Entity is the platform's ad id.
	KindAdDay = "ad_day"

	// KindLinkDay is how many visits one tracked link took on one day, as
	// counted by the operator's own redirector or edge analytics. Payload is
	// a LinkDay. Entity is the link's label.
	KindLinkDay = "link_day"

	// KindSiteEventDay is how many times one named website event fired on one
	// day across *all* visitors, paid or not (a tracking pixel's own count).
	// Payload is a SiteEventDay. Entity is the event name.
	KindSiteEventDay = "site_event_day"

	// KindAdsDay is the one summary event per (product, completed day) that
	// becomes the day's ads drop in the chest. Payload is an AdsDaySummary.
	// It is emitted once, after the day is over, and is not a snapshot: it is
	// news, and news is not revised.
	KindAdsDay = "ads_day"
)

// AdDay is the payload of a KindAdDay snapshot.
type AdDay struct {
	AdID       string `json:"ad_id"`
	Ad         string `json:"ad"`
	AdSetID    string `json:"adset_id,omitempty"`
	AdSet      string `json:"adset,omitempty"`
	CampaignID string `json:"campaign_id,omitempty"`
	Campaign   string `json:"campaign,omitempty"`
	// Status is the platform's own delivery word for the ad at the time of
	// the snapshot ("ACTIVE", "PAUSED", ...), kept verbatim.
	Status string `json:"status,omitempty"`
	// URLs are the destinations the ad's creative points at. They are what
	// ties an ad to a tracked link: a LinkDay belongs to the ad whose URL
	// contains its label.
	URLs []string `json:"urls,omitempty"`

	Spend    float64 `json:"spend"`
	Currency string  `json:"currency"`

	Impressions      int `json:"impressions"`
	Reach            int `json:"reach"`
	LinkClicks       int `json:"link_clicks"`
	LandingPageViews int `json:"landing_page_views"`
	// Conversions is how many times the operator's chosen conversion (a
	// "store button tap", say) was attributed to this ad on this day. It is
	// 0 both when nobody converted and when the platform cannot attribute;
	// Tracked tells the two apart.
	Conversions int `json:"conversions"`
	// Tracked is true when the platform reported the chosen conversion for
	// this ad at all, on any day in the window it was asked about. An ad with
	// spend, Tracked false and zero conversions is more likely missing its
	// tracking than failing to convert.
	Tracked bool `json:"tracked"`
}

// LinkDay is the payload of a KindLinkDay snapshot.
type LinkDay struct {
	// Label is whatever the operator's query returned to identify the link,
	// typically a slug.
	Label  string `json:"label"`
	Visits int    `json:"visits"`
}

// SiteEventDay is the payload of a KindSiteEventDay snapshot.
type SiteEventDay struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AdsDaySummary is the payload of a KindAdsDay event.
type AdsDaySummary struct {
	Spend            float64 `json:"spend"`
	Currency         string  `json:"currency"`
	Impressions      int     `json:"impressions"`
	LinkClicks       int     `json:"link_clicks"`
	LandingPageViews int     `json:"landing_page_views"`
	Conversions      int     `json:"conversions"`
	Ads              int     `json:"ads"`
	// TopAd is the name of the ad with the most landing page views that day,
	// or "" when no ad had any.
	TopAd string `json:"top_ad,omitempty"`
}

// FunnelSnapshotKey builds the dedupe key every funnel snapshot uses:
//
//	<source>:<kind>:<entity>:<day>:<fingerprint>
//
// fingerprint is a short hash of the numbers, so an unchanged day dedupes
// away and a revised one is stored as a newer snapshot. The key only decides
// whether a poll is news. Readers never parse it: they group on the payload's
// own id (ad_id, label, name) together with source, product and day, and take
// the row observed last.
func FunnelSnapshotKey(source, kind, entity, day, fingerprint string) string {
	return source + ":" + kind + ":" + entity + ":" + day + ":" + fingerprint
}

// FunnelReport is what GET /api/funnel returns: one product's (or the whole
// realm's) path from ad spend to store, over a run of days.
//
// A pointer field is nil when Loot has no reading for it, which is not the
// same as zero. A store that has not reported yesterday yet has nil installs
// for yesterday; a day with no ads running has a zero spend. The page must
// keep the two apart, because "no data yet" read as "nothing happened" is the
// single most misleading thing a funnel can say.
type FunnelReport struct {
	// Enabled is false when no funnel source has ever reported anything in
	// scope. The page hides itself rather than showing an empty funnel.
	Enabled bool   `json:"enabled"`
	Product string `json:"product"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Currency is the display currency every money field is in.
	Currency string `json:"currency"`
	// ConversionLabel is the operator's name for the chosen conversion
	// ("Store button tap"), or "" for the generic word.
	ConversionLabel string `json:"conversion_label"`
	// SiteEvent is the name of the website event counted in
	// FunnelDay.SiteEvents, or "" when none is configured.
	SiteEvent string `json:"site_event"`

	Days   []FunnelDay  `json:"days"`
	Ads    []FunnelAd   `json:"ads"`
	Totals FunnelDay    `json:"totals"`
	Notes  []FunnelNote `json:"notes"`
	// AsOf is the newest day each stage has any reading for, keyed by stage
	// ("ads", "links", "site_events", "installs", "sales"). A stage that has
	// never reported is absent.
	AsOf map[string]string `json:"as_of"`
}

// FunnelDay is one day of the funnel, top to bottom. Totals reuses it with
// Day "".
type FunnelDay struct {
	Day string `json:"day"`

	Spend            *float64 `json:"spend"`
	Impressions      *int     `json:"impressions"`
	LinkClicks       *int     `json:"link_clicks"`
	LandingPageViews *int     `json:"landing_page_views"`
	// Conversions are the ones the ad platform attributed to an ad.
	Conversions *int `json:"conversions"`
	// LinkVisits are counted by the operator's own redirector.
	LinkVisits *int `json:"link_visits"`
	// SiteEvents counts the configured website event across all visitors,
	// paid or not, so it can exceed Conversions.
	SiteEvents *int `json:"site_events"`
	// Installs and Purchases come from the stores, for every customer, not
	// only the ones an ad sent. No store says which ad an install came from.
	Installs  *int     `json:"installs"`
	Purchases *int     `json:"purchases"`
	Revenue   *float64 `json:"revenue"`
}

// FunnelAd is one ad's totals over the report's days.
type FunnelAd struct {
	Source           string  `json:"source"`
	AdID             string  `json:"ad_id"`
	Ad               string  `json:"ad"`
	AdSet            string  `json:"adset"`
	Campaign         string  `json:"campaign"`
	Status           string  `json:"status"`
	Spend            float64 `json:"spend"`
	Impressions      int     `json:"impressions"`
	LinkClicks       int     `json:"link_clicks"`
	LandingPageViews int     `json:"landing_page_views"`
	Conversions      int     `json:"conversions"`
	Tracked          bool    `json:"tracked"`
	// LinkVisits is nil when no tracked link could be tied to this ad.
	LinkVisits *int `json:"link_visits"`
	// Series is landing page views per day, aligned with FunnelReport.Days.
	Series []int `json:"series"`
}

// FunnelNote is one thing on the page worth a look. It carries a code and
// its numbers rather than a sentence, so the page can say it in the reader's
// language. Notes observe; they never scold (see CONTRIBUTING.md).
type FunnelNote struct {
	// Code is one of the FunnelNote* constants.
	Code string `json:"code"`
	// AdID and Ad name the ad the note is about, when it is about one.
	AdID string `json:"ad_id,omitempty"`
	Ad   string `json:"ad,omitempty"`
	// Params are the numbers the sentence needs (amounts, counts, days,
	// percentages), keyed by the names the message template uses.
	Params map[string]any `json:"params,omitempty"`
}

const (
	// FunnelNoteUntracked: ads have spend but the platform reports the chosen
	// conversion for none of them, so conversions cannot be credited to ads.
	FunnelNoteUntracked = "untracked"
	// FunnelNoteSpendNoConversions: one tracked ad has spent at least the
	// configured amount without a single conversion.
	FunnelNoteSpendNoConversions = "spend_no_conversions"
	// FunnelNoteConcentrated: one ad took most of the spend in the range.
	FunnelNoteConcentrated = "concentrated"
	// FunnelNoteCostRising: the latest complete day's cost per landing page
	// view is well above the days before it.
	FunnelNoteCostRising = "cost_rising"
	// FunnelNoteStoreLag: the stores have not reported the newest days yet.
	FunnelNoteStoreLag = "store_lag"
	// FunnelNoteBudgetCheck: spend has passed the configured checkpoint and
	// the conversion rate per landing page view is below the configured one.
	FunnelNoteBudgetCheck = "budget_check"
)
