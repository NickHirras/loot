package appstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// DefaultLookupURL is Apple's public iTunes Lookup endpoint used to read
// live averageUserRating and userRatingCount (including star-only ratings).
const DefaultLookupURL = "https://itunes.apple.com"

const maxReviewPages = 5

type ascApp struct {
	ID         string `json:"id"`
	Attributes struct {
		Name     string `json:"name"`
		BundleID string `json:"bundleId"`
	} `json:"attributes"`
}

type ascAppsResponse struct {
	Data []ascApp `json:"data"`
}

type ascReview struct {
	ID         string `json:"id"`
	Attributes struct {
		Rating           int    `json:"rating"`
		Title            string `json:"title"`
		Body             string `json:"body"`
		ReviewerNickname string `json:"reviewerNickname"`
		CreatedDate      string `json:"createdDate"`
		Territory        string `json:"territory"`
	} `json:"attributes"`
}

type ascReviewsResponse struct {
	Data  []ascReview `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

type itunesLookupResponse struct {
	Results []struct {
		TrackID           int64   `json:"trackId"`
		TrackName         string  `json:"trackName"`
		AverageUserRating float64 `json:"averageUserRating"`
		UserRatingCount   int     `json:"userRatingCount"`
	} `json:"results"`
}

func (s *Source) lookupURL() string {
	if s.LookupURL != "" {
		return strings.TrimSuffix(s.LookupURL, "/")
	}
	return DefaultLookupURL
}

func (s *Source) getASCJSON(ctx context.Context, tokens *tokenCache, endpoint string, out any) error {
	tok, err := tokens.bearer(s.now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("appstore: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("appstore: get %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("appstore: read %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		ae := parseAPIError(resp.StatusCode, body)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", errCredentials, ae.Error())
		}
		return fmt.Errorf("appstore: %s", ae.Error())
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("appstore: decode %s: %w", endpoint, err)
	}
	return nil
}

func (s *Source) listApps(ctx context.Context) ([]ascApp, error) {
	if s.reviewTokens == nil {
		return nil, nil
	}
	endpoint := s.baseURL() + "/v1/apps?limit=200&fields[apps]=name,bundleId"
	var resp ascAppsResponse
	if err := s.getASCJSON(ctx, s.reviewTokens, endpoint, &resp); err != nil {
		return nil, err
	}
	if len(s.Apps) == 0 {
		return resp.Data, nil
	}
	var out []ascApp
	for _, a := range resp.Data {
		if slices.Contains(s.Apps, a.ID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Source) checkReviews(ctx context.Context) error {
	if s.reviewTokens == nil {
		return nil
	}
	endpoint := s.baseURL() + "/v1/apps?limit=1&fields[apps]=name"
	var resp ascAppsResponse
	return s.getASCJSON(ctx, s.reviewTokens, endpoint, &resp)
}

func (s *Source) pollReviewsAndRatings(ctx context.Context, st *state, today string, observed time.Time) ([]core.Event, error) {
	if s.reviewTokens == nil {
		return nil, nil
	}
	apps, err := s.listApps(ctx)
	if err != nil {
		return nil, err
	}

	var events []core.Event
	for _, a := range apps {
		appName := strings.TrimSpace(a.Attributes.Name)
		if appName == "" {
			appName = a.ID
		}

		// 1. Written reviews via App Store Connect /v1/apps/{id}/customerReviews.
		revEvents, err := s.fetchAppReviews(ctx, a.ID, appName, observed)
		if err != nil {
			s.log().Warn("appstore: customerReviews failed", "app_id", a.ID, "app", appName, "error", err)
		} else {
			events = append(events, revEvents...)
		}

		// 2. Daily overall star rating & count via public iTunes Lookup API.
		if st.RatingDay != today {
			if ev, ok := s.fetchItunesRating(ctx, a.ID, appName, today, observed); ok {
				events = append(events, ev)
			}
		}
	}
	if len(apps) > 0 && st.RatingDay != today {
		st.RatingDay = today
	}
	return events, nil
}

func (s *Source) fetchAppReviews(ctx context.Context, appID, appName string, observed time.Time) ([]core.Event, error) {
	endpoint := fmt.Sprintf("%s/v1/apps/%s/customerReviews?sort=-createdDate&limit=200",
		s.baseURL(), url.PathEscape(appID))

	var events []core.Event
	for page := 0; page < maxReviewPages && endpoint != ""; page++ {
		var resp ascReviewsResponse
		if err := s.getASCJSON(ctx, s.reviewTokens, endpoint, &resp); err != nil {
			return events, err
		}
		for _, r := range resp.Data {
			if r.Attributes.Rating < 1 || r.Attributes.Rating > 5 {
				continue
			}
			created, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Attributes.CreatedDate))
			if err != nil {
				created = observed
			}
			created = created.UTC()
			day := core.DayOf(created)
			country := core.CountryFromISO3(r.Attributes.Territory)
			payload, _ := json.Marshal(core.ReviewPayload{
				ReviewID: r.ID,
				Rating:   r.Attributes.Rating,
				Title:    strings.TrimSpace(r.Attributes.Title),
				Body:     strings.TrimSpace(r.Attributes.Body),
				Author:   strings.TrimSpace(r.Attributes.ReviewerNickname),
			})
			events = append(events, core.Event{
				ID:         core.NewIDAt(created),
				Source:     Name,
				Kind:       core.KindReview,
				App:        appName,
				Country:    country,
				OccurredAt: created,
				ObservedAt: observed,
				Day:        day,
				Quantity:   r.Attributes.Rating,
				DedupeKey:  "asc:review:" + r.ID,
				Chest:      observed.Sub(created) > 48*time.Hour,
				Payload:    payload,
			})
		}
		endpoint = resp.Links.Next
	}
	return events, nil
}

func (s *Source) fetchItunesRating(ctx context.Context, appID, appName, today string, observed time.Time) (core.Event, bool) {
	endpoint := fmt.Sprintf("%s/lookup?id=%s&country=us", s.lookupURL(), url.QueryEscape(appID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return core.Event{}, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		s.log().Debug("appstore: itunes lookup failed", "app_id", appID, "error", err)
		return core.Event{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return core.Event{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return core.Event{}, false
	}
	var out itunesLookupResponse
	if err := json.Unmarshal(body, &out); err != nil || len(out.Results) == 0 {
		return core.Event{}, false
	}
	res := out.Results[0]
	if res.UserRatingCount <= 0 && res.AverageUserRating <= 0 {
		return core.Event{}, false
	}
	avg := math.Round(res.AverageUserRating*100) / 100
	occurred, err := time.ParseInLocation(core.DayLayout, today, time.UTC)
	if err != nil {
		occurred = observed
	}
	payload, _ := json.Marshal(core.RatingSnapshotPayload{
		AverageRating:   avg,
		RatingCount:     res.UserRatingCount,
		CumulativeCount: true,
	})
	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       core.KindRatingSnapshot,
		App:        appName,
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        today,
		Quantity:   res.UserRatingCount,
		DedupeKey:  fmt.Sprintf("asc:rating:%s:%s", appID, today),
		Silent:     true,
		Payload:    payload,
	}, true
}
