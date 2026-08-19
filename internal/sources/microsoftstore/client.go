package microsoftstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the Partner Center management API root, which serves
	// both the analytics endpoints and the applications list.
	DefaultBaseURL = "https://manage.devcenter.microsoft.com"

	// DefaultLoginURL is the Microsoft Entra (Azure AD) token host.
	DefaultLoginURL = "https://login.microsoftonline.com"

	// tokenResource is the audience the analytics API accepts. Microsoft's own
	// documentation is explicit that this exact string goes in the `resource`
	// form field of the v1 client-credentials request.
	tokenResource = "https://manage.devcenter.microsoft.com"

	// apiPrefix is the path every endpoint hangs off. The applications list
	// returns its @nextLink relative to it.
	apiPrefix = "/v1.0/my/"
)

const (
	// maxResponseBytes caps one JSON page. A day of acquisitions grouped down
	// to (market, acquisition type) is kilobytes; 32 MiB is a ceiling that
	// still refuses to eat all the memory on a Raspberry Pi.
	maxResponseBytes = 32 << 20

	// maxPages bounds one paged request. A @nextLink that never terminates —
	// a proxy rewriting the link, say — must not spin forever.
	maxPages = 200

	// tokenSkew is how long before real expiry a cached token is retired, so
	// a token never dies mid-request.
	tokenSkew = 2 * time.Minute
)

// errCredentials means Microsoft Entra refused to mint a token: a wrong
// tenant, client id or secret, or an expired secret. Retrying will not help.
var errCredentials = errors.New("microsoft entra rejected the credentials")

// errNotAssociated means the token was fine but Partner Center would not serve
// the data: the Entra application is not associated with the Partner Center
// account, or lacks the Manager role.
var errNotAssociated = errors.New("partner center refused the request")

// errNoData means the endpoint has nothing for that query — an app with no
// add-ons, or an account with no subscriptions. It is a normal outcome, not a
// failure, and never lights up `last_error`.
var errNoData = errors.New("no data for this query")

// tokenCache holds one client-credentials token until shortly before it
// expires. Microsoft issues 60 minute tokens, so a 6 hour poll would otherwise
// mint one per request.
type tokenCache struct {
	tenantID     string
	clientID     string
	clientSecret string

	mu      sync.Mutex
	token   string
	expires time.Time
}

// tokenResponse is the v1 endpoint's reply. expires_in is documented as a
// number but the v1 endpoint returns it as a JSON *string*, so it is decoded
// leniently rather than assumed.
type tokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   flexInt   `json:"expires_in"`
	ExpiresOn   flexInt   `json:"expires_on"`
	Resource    string    `json:"resource"`
	Error       string    `json:"error"`
	Description string    `json:"error_description"`
	ErrorCodes  []float64 `json:"error_codes"`
}

// flexInt decodes a JSON value that may be a number or a quoted number.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// A fractional or otherwise odd value is not worth failing a token
		// exchange over; the caller falls back to a conservative lifetime.
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}

// bearer returns a valid access token, minting one if the cached token is
// missing or about to expire.
func (t *tokenCache) bearer(ctx context.Context, now time.Time, loginURL string, client *http.Client) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && now.Before(t.expires.Add(-tokenSkew)) {
		return t.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", t.clientID)
	form.Set("client_secret", t.clientSecret)
	form.Set("resource", tokenResource)

	endpoint := strings.TrimSuffix(loginURL, "/") + "/" + url.PathEscape(t.tenantID) + "/oauth2/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("microsoftstore: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("microsoftstore: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("microsoftstore: read token response: %w", err)
	}

	var out tokenResponse
	// A non-JSON body (an HTML error page from a proxy) still has to produce a
	// usable message, so a decode failure falls through to the status check.
	_ = json.Unmarshal(body, &out)

	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("%w: %s", errCredentials, tokenErrorDetail(resp.StatusCode, out, body))
	}

	lifetime := time.Duration(out.ExpiresIn) * time.Second
	if lifetime <= 0 {
		// Microsoft's tokens last an hour; assume the documented lifetime
		// rather than treating the token as permanent.
		lifetime = time.Hour
	}
	t.token = out.AccessToken
	t.expires = now.Add(lifetime)
	return t.token, nil
}

// tokenErrorDetail turns Entra's reply into one line a person can act on.
// AADSTS codes are the useful part: 7000215 is a bad secret, 700016 an unknown
// client id, 90002 an unknown tenant.
func tokenErrorDetail(status int, out tokenResponse, body []byte) string {
	detail := strings.TrimSpace(out.Description)
	if detail == "" {
		detail = strings.TrimSpace(out.Error)
	}
	if detail == "" {
		detail = snippet(body)
	}
	// Entra's descriptions carry a correlation id, a timestamp and a URL after
	// the sentence that matters. Keep the first line only.
	if i := strings.IndexAny(detail, "\r\n"); i > 0 {
		detail = detail[:i]
	}
	hint := ""
	switch {
	case strings.Contains(detail, "AADSTS7000215"):
		hint = " — the client secret is wrong or has expired; add a new key in Partner Center"
	case strings.Contains(detail, "AADSTS700016"), strings.Contains(detail, "AADSTS700213"):
		hint = " — client_id is not an application in this tenant; check tenant_id and client_id"
	case strings.Contains(detail, "AADSTS90002"):
		hint = " — tenant_id does not name a Microsoft Entra directory"
	case strings.Contains(detail, "AADSTS500011"), strings.Contains(detail, "AADSTS650057"):
		hint = " — the resource https://manage.devcenter.microsoft.com was refused for this tenant"
	}
	if detail == "" {
		return fmt.Sprintf("http %d%s", status, hint)
	}
	return fmt.Sprintf("http %d: %s%s", status, detail, hint)
}

// page is one response envelope. The analytics API capitalises Value and
// TotalCount; the applications list does not, so both spellings are decoded.
type page[T any] struct {
	Value      []T    `json:"Value"`
	ValueLower []T    `json:"value"`
	NextLink   string `json:"@nextLink"`
	TotalCount int    `json:"TotalCount"`
	Freshness  string `json:"DataFreshnessTimestamp"`
}

func (p page[T]) rows() []T {
	if len(p.Value) > 0 {
		return p.Value
	}
	return p.ValueLower
}

// apiError is the analytics API's error envelope:
// {"code","message","source","details":[],"innererror":{"code","message","data":[]}}
type apiError struct {
	Status  int
	Code    string
	Message string
	Inner   string
}

func (e *apiError) Error() string {
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Inner != "" {
		parts = append(parts, e.Inner)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("http %d", e.Status)
	}
	return fmt.Sprintf("http %d: %s", e.Status, strings.Join(parts, ": "))
}

func parseAPIError(status int, body []byte) *apiError {
	out := &apiError{Status: status}
	var envelope struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		InnerError struct {
			Code    string   `json:"code"`
			Message string   `json:"message"`
			Data    []string `json:"data"`
		} `json:"innererror"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Code != "" || envelope.Message != "") {
		out.Code, out.Message = envelope.Code, envelope.Message
		inner := envelope.InnerError.Message
		if len(envelope.InnerError.Data) > 0 {
			inner = strings.TrimSpace(inner + " (" + strings.Join(envelope.InnerError.Data, "; ") + ")")
		}
		out.Inner = inner
		return out
	}
	out.Message = snippet(body)
	return out
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// fetchPages GETs one endpoint and follows @nextLink until the data runs out,
// returning every row. path is relative to the API prefix ("analytics/
// appacquisitions"); q is the first page's query.
func fetchPages[T any](ctx context.Context, s *Source, path string, q url.Values) ([]T, error) {
	next := s.baseURL() + apiPrefix + path
	if encoded := q.Encode(); encoded != "" {
		next += "?" + encoded
	}

	var rows []T
	for i := 0; next != "" && i < maxPages; i++ {
		var p page[T]
		if err := s.getJSON(ctx, next, &p); err != nil {
			return rows, err
		}
		rows = append(rows, p.rows()...)
		next = s.resolveNext(p.NextLink)
	}
	return rows, nil
}

// resolveNext turns a @nextLink into an absolute URL on the configured host.
// The analytics API returns an absolute URI and the applications list a path
// relative to /v1.0/my/, so both are handled — and only the path and query are
// ever taken from the response, so a rewritten link cannot send a Partner
// Center token to another host.
func (s *Source) resolveNext(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		s.logger().Warn("microsoftstore: ignoring unparsable @nextLink", "link", link)
		return ""
	}
	path := u.EscapedPath()
	if !strings.HasPrefix(path, "/") {
		path = apiPrefix + path
	}
	out := s.baseURL() + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// getJSON performs one authenticated GET and decodes the body into out,
// classifying the failures a user can actually do something about.
func (s *Source) getJSON(ctx context.Context, endpoint string, out any) error {
	token, err := s.bearer(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("microsoftstore: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("microsoftstore: get %s: %w", redact(endpoint), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("microsoftstore: read %s: %w", redact(endpoint), err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("microsoftstore: decode %s: %w", redact(endpoint), err)
		}
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// "No data" and "no such app" arrive the same way; the caller decides
		// which of the two it is from context.
		return fmt.Errorf("%w (%s)", errNoData, parseAPIError(resp.StatusCode, body).Error())
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s — the access token was refused; check tenant_id, client_id and client_secret",
			errCredentials, parseAPIError(resp.StatusCode, body).Error())
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s — add this Microsoft Entra application in Partner Center under "+
			"Account settings → User management → Microsoft Entra applications (Azure AD applications) and give it the Manager role",
			errNotAssociated, parseAPIError(resp.StatusCode, body).Error())
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("microsoftstore: rate limited by partner center (%s)", redact(endpoint))
	default:
		return fmt.Errorf("microsoftstore: %s: %s", redact(endpoint), parseAPIError(resp.StatusCode, body).Error())
	}
}

// redact strips the query string from a URL before it lands in a log line or
// an error the UI shows. Nothing secret rides in it today, but an error
// message is the wrong place to learn that a future parameter did.
func redact(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		return endpoint[:i]
	}
	return endpoint
}
