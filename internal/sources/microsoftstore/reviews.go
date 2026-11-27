package microsoftstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

const (
	ratingsPath = "analytics/ratings"
	reviewsPath = "analytics/reviews"
)

type ratingRow struct {
	Date            string `json:"date"`
	ApplicationID   string `json:"applicationId"`
	ApplicationName string `json:"applicationName"`
	Market          string `json:"market"`
	OneStar         int    `json:"oneStar"`
	TwoStars        int    `json:"twoStars"`
	ThreeStars      int    `json:"threeStars"`
	FourStars       int    `json:"fourStars"`
	FiveStars       int    `json:"fiveStars"`
}

type reviewRow struct {
	ID              string `json:"id"`
	Date            string `json:"date"`
	ApplicationID   string `json:"applicationId"`
	ApplicationName string `json:"applicationName"`
	Market          string `json:"market"`
	PackageVersion  string `json:"packageVersion"`
	DeviceModel     string `json:"deviceModel"`
	DeviceType      string `json:"deviceType"`
	ReviewerName    string `json:"reviewerName"`
	Rating          int    `json:"rating"`
	ReviewTitle     string `json:"reviewTitle"`
	ReviewText      string `json:"reviewText"`
	ResponseDate    string `json:"responseDate"`
	ResponseText    string `json:"responseText"`
}

func (s *Source) collectRatingsAndReviews(ctx context.Context, a app, from, to string, observed time.Time) ([]core.Event, error) {
	var events []core.Event

	ratingEv, err := s.collectRatings(ctx, a, from, to, observed)
	if err != nil && !errors.Is(err, errNoData) {
		s.logger().Warn("microsoftstore: ratings failed", "store_id", a.ID, "error", err)
	} else {
		events = append(events, ratingEv...)
	}

	reviewEv, err := s.collectReviews(ctx, a, from, to, observed)
	if err != nil && !errors.Is(err, errNoData) {
		s.logger().Warn("microsoftstore: reviews failed", "store_id", a.ID, "error", err)
	} else {
		events = append(events, reviewEv...)
	}
	return events, nil
}

func (s *Source) collectRatings(ctx context.Context, a app, from, to string, observed time.Time) ([]core.Event, error) {
	type dayAgg struct {
		stars [5]int
		name  string
	}
	byDay := map[string]*dayAgg{}

	for _, w := range windows(from, to, maxWindowDays) {
		q := url.Values{}
		q.Set("applicationId", a.ID)
		q.Set("startDate", w.from)
		q.Set("endDate", w.to)
		q.Set("aggregationLevel", "day")
		q.Set("top", fmt.Sprint(acquisitionsPageSize))

		rows, err := fetchPages[ratingRow](ctx, s, ratingsPath, q)
		if err != nil {
			if errors.Is(err, errNoData) {
				continue
			}
			return nil, err
		}
		for _, r := range rows {
			day := normalizeDate(r.Date)
			if day == "" || day < from || day > to {
				continue
			}
			agg, ok := byDay[day]
			if !ok {
				agg = &dayAgg{}
				byDay[day] = agg
			}
			if agg.name == "" && r.ApplicationName != "" {
				agg.name = strings.TrimSpace(r.ApplicationName)
			}
			agg.stars[0] += max(r.OneStar, 0)
			agg.stars[1] += max(r.TwoStars, 0)
			agg.stars[2] += max(r.ThreeStars, 0)
			agg.stars[3] += max(r.FourStars, 0)
			agg.stars[4] += max(r.FiveStars, 0)
		}
	}

	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)

	var events []core.Event
	for _, day := range days {
		agg := byDay[day]
		count := agg.stars[0] + agg.stars[1] + agg.stars[2] + agg.stars[3] + agg.stars[4]
		if count <= 0 {
			continue
		}
		weighted := float64(agg.stars[0]*1 + agg.stars[1]*2 + agg.stars[2]*3 + agg.stars[3]*4 + agg.stars[4]*5)
		avg := math.Round((weighted/float64(count))*100) / 100
		occurred, err := time.ParseInLocation(core.DayLayout, day, time.UTC)
		if err != nil {
			continue
		}
		appName := displayName(a.Name, a.ID)
		if a.Name == "" && agg.name != "" {
			appName = agg.name
		}
		payload, _ := json.Marshal(core.RatingSnapshotPayload{
			DailyAverage: avg,
			RatingCount:  count,
			Stars:        agg.stars,
		})
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       core.KindRatingSnapshot,
			App:        appName,
			OccurredAt: occurred,
			ObservedAt: observed,
			Day:        day,
			Quantity:   count,
			DedupeKey:  fmt.Sprintf("msstore:rating:%s:%s", a.ID, day),
			Silent:     true,
			Payload:    payload,
		})
	}
	return events, nil
}

func (s *Source) collectReviews(ctx context.Context, a app, from, to string, observed time.Time) ([]core.Event, error) {
	var events []core.Event
	for _, w := range windows(from, to, maxWindowDays) {
		q := url.Values{}
		q.Set("applicationId", a.ID)
		q.Set("startDate", w.from)
		q.Set("endDate", w.to)
		q.Set("top", fmt.Sprint(acquisitionsPageSize))

		rows, err := fetchPages[reviewRow](ctx, s, reviewsPath, q)
		if err != nil {
			if errors.Is(err, errNoData) {
				continue
			}
			return nil, err
		}
		for _, r := range rows {
			if r.Rating < 1 || r.Rating > 5 {
				continue
			}
			day := normalizeDate(r.Date)
			if day == "" {
				continue
			}
			occurred, err := time.ParseInLocation(core.DayLayout, day, time.UTC)
			if err != nil {
				continue
			}
			appName := displayName(a.Name, a.ID)
			if a.Name == "" && strings.TrimSpace(r.ApplicationName) != "" {
				appName = strings.TrimSpace(r.ApplicationName)
			}
			revID := strings.TrimSpace(r.ID)
			if revID == "" {
				revID = fmt.Sprintf("%s:%s:%d:%s", a.ID, day, r.Rating, strings.TrimSpace(r.ReviewerName))
			}
			device := strings.TrimSpace(r.DeviceModel)
			if device == "" {
				device = strings.TrimSpace(r.DeviceType)
			}
			payload, _ := json.Marshal(core.ReviewPayload{
				ReviewID:   revID,
				Rating:     r.Rating,
				Title:      strings.TrimSpace(r.ReviewTitle),
				Body:       strings.TrimSpace(r.ReviewText),
				Author:     strings.TrimSpace(r.ReviewerName),
				AppVersion: strings.TrimSpace(r.PackageVersion),
				Device:     device,
				ReplyText:  strings.TrimSpace(r.ResponseText),
				ReplyDate:  normalizeDate(r.ResponseDate),
			})
			events = append(events, core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       core.KindReview,
				App:        appName,
				Country:    core.CountryFromISO3(r.Market),
				OccurredAt: occurred,
				ObservedAt: observed,
				Day:        day,
				Quantity:   r.Rating,
				DedupeKey:  fmt.Sprintf("msstore:review:%s:%s", a.ID, revID),
				Chest:      observed.Sub(occurred) > 48*time.Hour,
				Payload:    payload,
			})
		}
	}
	return events, nil
}
