package googleanalytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Google Analytics Data API root. Tests point BaseURL at
// an httptest server.
const DefaultBaseURL = "https://analyticsdata.googleapis.com"

// Scope is the read-only OAuth2 scope for the GA4 Data API.
const Scope = "https://www.googleapis.com/auth/analytics.readonly"

const (
	maxReportBytes = 16 << 20
	reportLimit    = "100000"
)

type runReportRequest struct {
	DateRanges      []dateRange       `json:"dateRanges"`
	Dimensions      []dimension       `json:"dimensions,omitempty"`
	Metrics         []metric          `json:"metrics"`
	DimensionFilter *filterExpression `json:"dimensionFilter,omitempty"`
	Limit           string            `json:"limit,omitempty"`
}

type dateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

type dimension struct {
	Name string `json:"name"`
}

type metric struct {
	Name string `json:"name"`
}

type filterExpression struct {
	Filter *fieldFilter `json:"filter,omitempty"`
}

type fieldFilter struct {
	FieldName    string        `json:"fieldName"`
	InListFilter *inListFilter `json:"inListFilter,omitempty"`
}

type inListFilter struct {
	Values        []string `json:"values"`
	CaseSensitive bool     `json:"caseSensitive"`
}

type runReportResponse struct {
	Rows []reportRow `json:"rows"`
}

type reportRow struct {
	DimensionValues []dimensionValue `json:"dimensionValues"`
	MetricValues    []metricValue    `json:"metricValues"`
}

type dimensionValue struct {
	Value string `json:"value"`
}

type metricValue struct {
	Value string `json:"value"`
}

func normalizePropertyID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "properties/")
	return strings.Trim(s, "/")
}

func platformFilter(platforms []string) *filterExpression {
	var clean []string
	for _, p := range platforms {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return &filterExpression{
		Filter: &fieldFilter{
			FieldName: "platform",
			InListFilter: &inListFilter{
				Values:        clean,
				CaseSensitive: false,
			},
		},
	}
}

// parseGADate turns GA4's YYYYMMDD date dimension value into YYYY-MM-DD.
func parseGADate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return "", false
	}
	t, err := time.Parse("20060102", s)
	if err != nil {
		return "", false
	}
	return t.Format("2006-01-02"), true
}

// validCountry accepts two-letter ISO 3166-1 alpha-2 country codes and rejects
// GA4's "(not set)" and "ZZ" unknown sentinel.
func validCountry(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s == "ZZ" {
		return ""
	}
	if s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return ""
	}
	return s
}

func parseCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f + 0.5)
	}
	return 0
}

func (s *Source) runReport(ctx context.Context, propertyID string, reqBody runReportRequest) (runReportResponse, error) {
	prop := normalizePropertyID(propertyID)
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return runReportResponse{}, fmt.Errorf("googleanalytics: encode request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1beta/properties/%s:runReport", strings.TrimSuffix(s.baseURL(), "/"), prop)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return runReportResponse{}, fmt.Errorf("googleanalytics: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if s.TokenSource != nil {
		tok, err := s.TokenSource.Token()
		if err != nil {
			return runReportResponse{}, fmt.Errorf("googleanalytics: oauth token: %w", err)
		}
		tok.SetAuthHeader(req)
	}

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return runReportResponse{}, fmt.Errorf("googleanalytics: property %s: %w", prop, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReportBytes))
	if err != nil {
		return runReportResponse{}, fmt.Errorf("googleanalytics: read property %s: %w", prop, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 280 {
			msg = msg[:280]
		}
		switch resp.StatusCode {
		case http.StatusForbidden:
			return runReportResponse{}, fmt.Errorf(
				"googleanalytics: 403 Forbidden on property %s — enable analyticsdata.googleapis.com and add the service account email as a Viewer on the GA4 property (%s)",
				prop, msg)
		case http.StatusNotFound:
			return runReportResponse{}, fmt.Errorf("googleanalytics: 404 Not Found for property %s — check property_id", prop)
		default:
			return runReportResponse{}, fmt.Errorf("googleanalytics: property %s HTTP %d: %s", prop, resp.StatusCode, msg)
		}
	}

	var out runReportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return runReportResponse{}, fmt.Errorf("googleanalytics: decode property %s: %w", prop, err)
	}
	return out, nil
}
