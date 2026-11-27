package revenuecat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// DefaultBaseURL is the RevenueCat REST API v2 root. Tests point BaseURL at an
// httptest server.
const DefaultBaseURL = "https://api.revenuecat.com"

const (
	defaultPollInterval = 6 * time.Hour
	maxOverviewBytes    = 1 << 20
)

type overviewResponse struct {
	Object  string           `json:"object"`
	Metrics []overviewMetric `json:"metrics"`
}

type overviewMetric struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
}

func (s *Source) pollingEnabled() bool {
	return strings.TrimSpace(s.APIKey) != "" && len(s.Projects) > 0
}

func (s *Source) baseURL() string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (s *Source) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) fetchOverview(ctx context.Context, projectID string) (overviewResponse, error) {
	endpoint := fmt.Sprintf("%s/v2/projects/%s/metrics/overview", s.baseURL(), url.PathEscape(projectID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return overviewResponse{}, fmt.Errorf("revenuecat: build overview request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.APIKey))
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return overviewResponse{}, fmt.Errorf("revenuecat: overview %s: %w", projectID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOverviewBytes))
	if err != nil {
		return overviewResponse{}, fmt.Errorf("revenuecat: read overview %s: %w", projectID, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return overviewResponse{}, fmt.Errorf("revenuecat: 401 Unauthorized for project %s — check sources.revenuecat.api_key (must be a v2 secret key)", projectID)
		case http.StatusForbidden:
			return overviewResponse{}, fmt.Errorf("revenuecat: 403 Forbidden for project %s — grant charts_metrics:overview:read on the v2 API key", projectID)
		case http.StatusNotFound:
			return overviewResponse{}, fmt.Errorf("revenuecat: 404 Not Found for project %s — check project id", projectID)
		default:
			return overviewResponse{}, fmt.Errorf("revenuecat: overview %s HTTP %d: %s", projectID, resp.StatusCode, msg)
		}
	}

	var out overviewResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return overviewResponse{}, fmt.Errorf("revenuecat: decode overview %s: %w", projectID, err)
	}
	return out, nil
}

// extractActiveUsers finds the 28-day active user count in an overview
// response. RevenueCat names the metric "active_users" (some older docs say
// "active_customers").
func extractActiveUsers(ov overviewResponse) (int, string, bool) {
	for _, want := range []string{"active_users", "active_customers"} {
		for _, m := range ov.Metrics {
			if strings.EqualFold(strings.TrimSpace(m.ID), want) {
				n := int(math.Round(m.Value))
				if n < 0 {
					n = 0
				}
				return n, m.ID, true
			}
		}
	}
	return 0, "", false
}

func (s *Source) checkOverview(ctx context.Context) error {
	for _, proj := range s.Projects {
		id := strings.TrimSpace(proj.ID)
		if id == "" {
			return fmt.Errorf("revenuecat: project entry has empty id")
		}
		ov, err := s.fetchOverview(ctx, id)
		if err != nil {
			return err
		}
		if _, _, ok := extractActiveUsers(ov); !ok {
			return fmt.Errorf("revenuecat: project %s overview returned no active_users metric", id)
		}
	}
	return nil
}

func (s *Source) pollOverview(ctx context.Context, rawState []byte) ([]core.Event, []byte, error) {
	now := s.now().UTC()
	today := core.DayOf(now)
	occurred, err := time.ParseInLocation(core.DayLayout, today, time.UTC)
	if err != nil {
		return nil, rawState, err
	}

	var (
		events []core.Event
		errs   []string
	)
	for _, proj := range s.Projects {
		id := strings.TrimSpace(proj.ID)
		app := strings.TrimSpace(proj.App)
		if id == "" || app == "" {
			continue
		}
		ov, err := s.fetchOverview(ctx, id)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		active, metricID, ok := extractActiveUsers(ov)
		if !ok {
			errs = append(errs, fmt.Sprintf("project %s overview had no active_users metric", id))
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"project_id":     id,
			"metric":         metricID,
			"window_days":    28,
			"covers_android": true,
		})
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindAudience,
			App:        app,
			OccurredAt: occurred,
			ObservedAt: now,
			Day:        today,
			Quantity:   active,
			DedupeKey:  fmt.Sprintf("rc:audience:%s:%s", id, today),
			Silent:     true,
			Payload:    payload,
		})
	}
	if len(errs) > 0 && len(events) == 0 {
		return nil, rawState, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if len(errs) > 0 && s.Log != nil {
		s.Log.Warn("revenuecat overview partial failure", "errors", strings.Join(errs, "; "))
	}
	return events, rawState, nil
}
