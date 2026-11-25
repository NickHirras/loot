package metaads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// maxPages bounds every paged read. A poll asks for at most a few hundred
// rows; a cursor that never ends is a bug on one side or the other, and
// following it forever would spend the account's rate limit for nothing.
const maxPages = 200

// graphError is the body Graph answers every failure with, whatever the HTTP
// status:
//
//	{"error":{"message":"...","type":"OAuthException","code":190,"error_subcode":463,"fbtrace_id":"..."}}
type graphError struct {
	Message      string `json:"message"`
	Type         string `json:"type"`
	Code         int    `json:"code"`
	Subcode      int    `json:"error_subcode"`
	UserTitle    string `json:"error_user_title"`
	UserMessage  string `json:"error_user_msg"`
	FBTraceID    string `json:"fbtrace_id"`
	HTTPStatus   int    `json:"-"`
	RequestLabel string `json:"-"`
}

// APIError is a failure Graph explained. Code is Graph's own error code, so a
// caller can tell an expired token (190) from throttling (4, 17, 32, 613,
// 80000-80014) without matching on text.
type APIError struct {
	Code    int
	Subcode int
	Msg     string
}

func (e *APIError) Error() string { return e.Msg }

// ErrTokenExpired is wrapped by every error caused by Graph rejecting the
// token (code 190), so `loot check` and the poll log can say the one thing
// that fixes it.
var ErrTokenExpired = errors.New("access token expired or invalid")

func (g graphError) toError() error {
	msg := g.Message
	if g.UserMessage != "" && g.UserMessage != g.Message {
		msg += " (" + g.UserMessage + ")"
	}
	switch {
	case g.Code == 190:
		return fmt.Errorf("metaads: %s: %w: %s — generate a new token for the system user (see docs/sources/metaads.md)",
			g.RequestLabel, ErrTokenExpired, msg)
	case isRateLimit(g.Code):
		return &APIError{Code: g.Code, Subcode: g.Subcode, Msg: fmt.Sprintf(
			"metaads: %s: rate limited by Meta (code %d): %s; the next poll retries", g.RequestLabel, g.Code, msg)}
	case g.Code == 10 || (g.Code >= 200 && g.Code < 300): // Graph's permission codes
		return &APIError{Code: g.Code, Subcode: g.Subcode, Msg: fmt.Sprintf(
			"metaads: %s: permission denied (code %d): %s; the token needs ads_read and access to this ad account", g.RequestLabel, g.Code, msg)}
	default:
		return &APIError{Code: g.Code, Subcode: g.Subcode, Msg: fmt.Sprintf(
			"metaads: %s: graph error %d/%d (HTTP %d): %s", g.RequestLabel, g.Code, g.Subcode, g.HTTPStatus, msg)}
	}
}

// isRateLimit reports whether code is one of Graph's throttling codes: 4 and
// 17 (app and user call limits), 32 (page), 613 (custom limits) and the
// 80000-80014 business-use-case range the Marketing API uses.
func isRateLimit(code int) bool {
	return code == 4 || code == 17 || code == 32 || code == 613 || (code >= 80000 && code <= 80014)
}

// tokenParam matches an access_token query parameter wherever it turns up:
// in our own request URLs, in a paging.next link (Graph puts the caller's
// token in those), or echoed back inside an error message.
var tokenParam = regexp.MustCompile(`(?i)(access_token=)[^&\s"']*`)

// redact removes the token from anything on its way to a log or an error. It
// strips the parameter by pattern and the literal token by value, so a token
// that surfaces some other way (url-encoded differently, quoted in a message)
// is still caught.
func (s *Source) redact(text string) string {
	text = tokenParam.ReplaceAllString(text, "${1}REDACTED")
	if tok := s.opts.AccessToken; tok != "" {
		text = strings.ReplaceAll(text, tok, "REDACTED")
		if esc := url.QueryEscape(tok); esc != tok {
			text = strings.ReplaceAll(text, esc, "REDACTED")
		}
	}
	return text
}

// redactErr returns err with the token removed from its text. Graph and
// net/http both put URLs into errors, and those URLs carry the token.
func (s *Source) redactErr(err error) error {
	if err == nil {
		return nil
	}
	clean := s.redact(err.Error())
	if clean == err.Error() {
		return err
	}
	return redactedError{msg: clean, err: err}
}

// redactedError keeps errors.Is/As working (ErrTokenExpired, *APIError) while
// showing only the cleaned text.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

// endpoint builds an absolute URL for a Graph path ("act_1/insights") with
// the given query. The token is added here and nowhere else.
func (s *Source) endpoint(path string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("access_token", s.opts.AccessToken)
	return strings.TrimRight(s.opts.BaseURL, "/") + "/" + s.opts.APIVersion + "/" + strings.TrimLeft(path, "/") + "?" + q.Encode()
}

// rebase turns a paging.next link into a request against our own BaseURL.
// Only the path and query are kept, and the token in the link is replaced
// with ours, so a rewritten link can never send the token to another host.
func (s *Source) rebase(next string) (string, error) {
	u, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("metaads: unparsable paging link")
	}
	q := u.Query()
	q.Set("access_token", s.opts.AccessToken)
	return strings.TrimRight(s.opts.BaseURL, "/") + u.EscapedPath() + "?" + q.Encode(), nil
}

// getJSON performs one GET and decodes the body into out. Graph's token goes
// in the access_token query parameter: that is the form every Marketing API
// reference page documents. (An Authorization: Bearer header appears in one
// Marketing API example, but nowhere is it documented as accepted across the
// insights, ads and pixel edges, and a silently ignored header would read as
// a confusing "no token" error.) Because the token is in the URL, no URL
// leaves this function unredacted.
func (s *Source) getJSON(ctx context.Context, label, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return s.redactErr(fmt.Errorf("metaads: %s: build request: %w", label, err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		// *url.Error prints the full request URL, token included.
		return s.redactErr(fmt.Errorf("metaads: %s: %w", label, err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return s.redactErr(fmt.Errorf("metaads: %s: read body: %w", label, err))
	}

	// Graph sends its error object with a 4xx/5xx, but has been seen to wrap
	// one in a 200 too, so look for it before trusting the status.
	var envelope struct {
		Error *graphError `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil && (envelope.Error.Message != "" || envelope.Error.Code != 0) {
		envelope.Error.HTTPStatus = resp.StatusCode
		envelope.Error.RequestLabel = label
		return s.redactErr(envelope.Error.toError())
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return s.redactErr(fmt.Errorf("metaads: %s: HTTP %s: %s", label, resp.Status, bytes.TrimSpace(snippet)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return s.redactErr(fmt.Errorf("metaads: %s: decode: %w", label, err))
	}
	return nil
}

// page is the envelope of every Graph list edge.
type page[T any] struct {
	Data   []T `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

// getAll reads every page of a list edge. Graph's documented rule is to stop
// when paging.next is absent, not when a page is empty: an empty page can
// still carry a next link.
func getAll[T any](ctx context.Context, s *Source, label, first string) ([]T, error) {
	var all []T
	next := first
	for i := 0; next != ""; i++ {
		if i >= maxPages {
			return all, fmt.Errorf("metaads: %s: more than %d pages, giving up", label, maxPages)
		}
		var p page[T]
		if err := s.getJSON(ctx, label, next, &p); err != nil {
			return all, err
		}
		all = append(all, p.Data...)
		if p.Paging.Next == "" {
			break
		}
		var err error
		if next, err = s.rebase(p.Paging.Next); err != nil {
			return all, err
		}
	}
	return all, nil
}

// flexNum accepts a JSON number or a numeric string. The Marketing API sends
// insights metrics as strings ("spend": "12.34", "impressions": "361324");
// pixel stats counts are documented only loosely, so both shapes are read.
type flexNum struct {
	v   float64
	set bool
}

func (n *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		return nil
	}
	s = strings.Trim(s, `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %q", s)
	}
	n.v, n.set = f, true
	return nil
}

func (n flexNum) int() int { return int(n.v + 0.5) }

// account is the ad account itself: GET /act_{id}?fields=...
type account struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	TimezoneName  string `json:"timezone_name"`
	AccountStatus int    `json:"account_status"`
}

func (s *Source) fetchAccount(ctx context.Context) (account, error) {
	var a account
	q := url.Values{"fields": {"name,currency,timezone_name,account_status"}}
	err := s.getJSON(ctx, "ad account", s.endpoint("act_"+s.accountID, q), &a)
	return a, err
}

// insightRow is one (ad, day) row of GET /act_{id}/insights?level=ad&time_increment=1.
type insightRow struct {
	AdID             string   `json:"ad_id"`
	AdName           string   `json:"ad_name"`
	AdSetID          string   `json:"adset_id"`
	AdSetName        string   `json:"adset_name"`
	CampaignID       string   `json:"campaign_id"`
	CampaignName     string   `json:"campaign_name"`
	Spend            flexNum  `json:"spend"`
	Impressions      flexNum  `json:"impressions"`
	Reach            flexNum  `json:"reach"`
	InlineLinkClicks flexNum  `json:"inline_link_clicks"`
	Actions          []action `json:"actions"`
	AccountCurrency  string   `json:"account_currency"`
	DateStart        string   `json:"date_start"`
	DateStop         string   `json:"date_stop"`
}

// action is one AdsActionStats entry: {"action_type": "...", "value": "3"}.
// value is the count under the attribution window the request used.
type action struct {
	ActionType string  `json:"action_type"`
	Value      flexNum `json:"value"`
}

const insightFields = "ad_id,ad_name,adset_id,adset_name,campaign_id,campaign_name," +
	"spend,impressions,reach,inline_link_clicks,actions,account_currency"

func (s *Source) fetchInsights(ctx context.Context, since, until string) ([]insightRow, error) {
	tr, _ := json.Marshal(map[string]string{"since": since, "until": until})
	q := url.Values{
		"level":          {"ad"},
		"time_increment": {"1"},
		"time_range":     {string(tr)},
		"fields":         {insightFields},
		// Report under the ad account's own attribution setting, so the
		// numbers match what the operator sees in Ads Manager rather than
		// the API's default windows.
		"use_account_attribution_setting": {"true"},
		"limit":                           {"500"},
	}
	return getAll[insightRow](ctx, s, "insights", s.endpoint("act_"+s.accountID+"/insights", q))
}

// adInfo is one ad from GET /act_{id}/ads, with its creative left as raw
// JSON: creative shapes vary too much to model, and all Loot wants from one
// is the URLs it points at.
type adInfo struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	EffectiveStatus string          `json:"effective_status"`
	Creative        json.RawMessage `json:"creative"`
}

func (s *Source) fetchAds(ctx context.Context) ([]adInfo, error) {
	q := url.Values{
		"fields": {"id,name,effective_status,creative{object_story_spec,asset_feed_spec,link_url,object_url}"},
		"limit":  {"200"},
	}
	return getAll[adInfo](ctx, s, "ads", s.endpoint("act_"+s.accountID+"/ads", q))
}

// pixelBucket is one AdsPixelStatsResult: an hour (start_time, ISO 8601) and
// the per-value counts within it. With aggregation=event, value is the event
// name ("PageView", "Lead", a custom event...).
type pixelBucket struct {
	StartTime   string `json:"start_time"`
	Aggregation string `json:"aggregation"`
	Data        []struct {
		Value string  `json:"value"`
		Count flexNum `json:"count"`
	} `json:"data"`
}

func (s *Source) fetchPixelStats(ctx context.Context, startUnix, endUnix int64) ([]pixelBucket, error) {
	q := url.Values{
		"aggregation": {"event"},
		"start_time":  {strconv.FormatInt(startUnix, 10)},
		"end_time":    {strconv.FormatInt(endUnix, 10)},
	}
	return getAll[pixelBucket](ctx, s, "pixel stats", s.endpoint(s.opts.PixelID+"/stats", q))
}
