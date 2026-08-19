package appstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the App Store Connect API root.
const DefaultBaseURL = "https://api.appstoreconnect.apple.com"

// maxReportBytes caps a decompressed report. A busy day for a busy developer
// is a few megabytes of TSV; 64 MiB is a generous ceiling that still refuses
// to eat all the memory on a Raspberry Pi if Apple ever returns something odd.
const maxReportBytes = 64 << 20

// reportSpec identifies one Sales and Trends report. The version numbers are
// not cosmetic: Apple rejects a request whose version is not the current one
// for that report type, and the column set changes between versions.
//
//	SALES / SUMMARY / DAILY        -> 1_1
//	SUBSCRIPTION / SUMMARY / DAILY -> 1_3
type reportSpec struct {
	Type      string
	SubType   string
	Version   string
	Frequency string
}

var (
	salesSummaryDaily        = reportSpec{Type: "SALES", SubType: "SUMMARY", Version: "1_1", Frequency: "DAILY"}
	subscriptionSummaryDaily = reportSpec{Type: "SUBSCRIPTION", SubType: "SUMMARY", Version: "1_3", Frequency: "DAILY"}
)

func (r reportSpec) String() string {
	return fmt.Sprintf("%s/%s/%s v%s", r.Type, r.SubType, r.Frequency, r.Version)
}

// errNotReady means Apple has no report for that day *yet*. Daily reports
// appear a few hours after the day closes (roughly 05:00-08:00 Pacific), and
// Apple signals both "not generated yet" and "nothing sold that day" with the
// same 404. It is a normal, expected outcome, not a failure: the caller stops
// advancing its cursor and tries again on the next poll.
var errNotReady = errors.New("report not available yet")

// errNoSales is Apple's definitive "nothing happened that day": the 404 whose
// detail reads "There were no sales for the date specified." Unlike
// errNotReady it is final — the walk can step over the day at once.
var errNoSales = errors.New("no sales for that day")

// errCredentials means the key, the issuer or the vendor number is wrong, or
// the key lacks the reports role. Retrying will not help; `loot check` and the
// sources API surface it so the user can fix the config.
var errCredentials = errors.New("app store connect rejected the credentials")

// apiError is one entry of Apple's JSON error envelope, which every non-2xx
// response carries: {"errors":[{"status","code","title","detail"}]}.
type apiError struct {
	Status int
	Code   string
	Title  string
	Detail string
}

func (e *apiError) Error() string {
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	} else if e.Title != "" {
		parts = append(parts, e.Title)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("http %d", e.Status)
	}
	return fmt.Sprintf("http %d: %s", e.Status, strings.Join(parts, ": "))
}

// parseAPIError pulls the first error out of Apple's envelope. A body that is
// not the envelope (an HTML error page from a proxy, say) still produces a
// usable message rather than an empty one.
func parseAPIError(status int, body []byte) *apiError {
	out := &apiError{Status: status}
	var envelope struct {
		Errors []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		out.Code, out.Title, out.Detail = first.Code, first.Title, first.Detail
		return out
	}
	if snippet := strings.TrimSpace(string(body)); snippet != "" {
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		out.Detail = snippet
	}
	return out
}

// fetchReport downloads one report for one day and returns the decompressed
// TSV. Errors are classified so callers can tell "come back later" from
// "your credentials are wrong" without matching on strings.
func (s *Source) fetchReport(ctx context.Context, spec reportSpec, date string) ([]byte, error) {
	token, err := s.tokens.bearer(s.now())
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("filter[frequency]", spec.Frequency)
	q.Set("filter[reportType]", spec.Type)
	q.Set("filter[reportSubType]", spec.SubType)
	q.Set("filter[vendorNumber]", s.VendorNumber)
	q.Set("filter[reportDate]", date)
	q.Set("filter[version]", spec.Version)
	endpoint := s.baseURL() + "/v1/salesReports?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("appstore: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Apple returns a gzipped TSV under its own vendor media type; asking for
	// anything else gets a 406.
	req.Header.Set("Accept", "application/a-gzip")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("appstore: get %s for %s: %w", spec, date, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReportBytes))
	if err != nil {
		return nil, fmt.Errorf("appstore: read %s for %s: %w", spec, date, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return gunzip(body)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// Both "the report is not generated yet" and "nothing happened that
		// day" arrive as a 404, but Apple's detail text tells them apart.
		// Seen in the wild: "There were no sales for the date specified."
		// for an empty day versus "Report is not available yet…" for a day
		// still being generated.
		detail := parseAPIError(resp.StatusCode, body).Error()
		if strings.Contains(strings.ToLower(detail), "no sales for the date") {
			return nil, fmt.Errorf("%w (%s %s: %s)", errNoSales, spec, date, detail)
		}
		return nil, fmt.Errorf("%w (%s %s: %s)", errNotReady, spec, date, detail)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s — check key_id, issuer_id and that private_key_path is the matching .p8",
			errCredentials, parseAPIError(resp.StatusCode, body).Error())
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s — the key needs the Sales and Reports role, and vendor_number %s must belong to the same team",
			errCredentials, parseAPIError(resp.StatusCode, body).Error(), s.VendorNumber)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("appstore: rate limited by app store connect (%s %s)", spec, date)
	case resp.StatusCode == http.StatusBadRequest && spec.Type != "SALES" && strings.Contains(string(body), "Invalid vendor number"):
		// Seen in the wild: the SALES report accepts the vendor number but the
		// SUBSCRIPTION report answers 400 "Invalid vendor number specified"
		// for a vendor with no subscription data. Treat it like the 404
		// Apple uses for the same situation elsewhere.
		return nil, fmt.Errorf("%w (%s %s: %s)", errNotReady, spec, date, parseAPIError(resp.StatusCode, body).Error())
	default:
		return nil, fmt.Errorf("appstore: %s for %s: %s", spec, date, parseAPIError(resp.StatusCode, body).Error())
	}
}

// gunzip decompresses a report body. Apple sends gzip without setting
// Content-Encoding (the *content* is a gzip file, it is not transfer
// compression), so net/http hands it over untouched. The magic-number check
// keeps the path working if that ever changes, or if a proxy decompresses it.
func gunzip(body []byte) ([]byte, error) {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("appstore: open gzip report: %w", err)
	}
	defer zr.Close()

	out, err := io.ReadAll(io.LimitReader(zr, maxReportBytes))
	if err != nil {
		return nil, fmt.Errorf("appstore: read gzip report: %w", err)
	}
	return out, nil
}

func (s *Source) baseURL() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(s.BaseURL, "/")
}

func (s *Source) client() *http.Client {
	if s.Client == nil {
		return &http.Client{Timeout: 60 * time.Second}
	}
	return s.Client
}
