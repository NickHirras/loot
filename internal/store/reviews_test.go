package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

func TestReviewsSummaryAggregationAndFilters(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	mkReview := func(source, product, country, day string, rating int, title, body, key string) core.Event {
		occ, _ := time.Parse(core.DayLayout, day)
		payload, _ := json.Marshal(core.ReviewPayload{
			ReviewID: key, Rating: rating, Title: title, Body: body, Author: "Tester",
		})
		return core.Event{
			ID: core.NewIDAt(occ), Source: source, Kind: core.KindReview,
			App: product, Product: product, Country: country,
			OccurredAt: occ, ObservedAt: occ, Day: day,
			Quantity: rating, DedupeKey: key, Payload: payload,
		}
	}
	mkSnap := func(source, product, day string, p core.RatingSnapshotPayload, key string) core.Event {
		occ, _ := time.Parse(core.DayLayout, day)
		payload, _ := json.Marshal(p)
		return core.Event{
			ID: core.NewIDAt(occ), Source: source, Kind: core.KindRatingSnapshot,
			App: product, Product: product, OccurredAt: occ, ObservedAt: occ, Day: day,
			Quantity: p.RatingCount, DedupeKey: key, Silent: true, Payload: payload,
		}
	}

	insertAll(t, st,
		mkReview("appstore", "Macro Trainer", "US", "2026-11-20", 5, "Awesome", "Best app", "as:r1"),
		mkReview("googleplay", "Macro Trainer", "DE", "2026-11-22", 2, "Buggy", "Crashed once", "gp:r1"),
		mkReview("appstore", "Nistis", "GB", "2026-11-25", 5, "Clean", "Love it", "as:r2"),
		mkSnap("appstore", "Macro Trainer", "2026-11-26", core.RatingSnapshotPayload{
			AverageRating: 4.8, RatingCount: 10, CumulativeCount: true,
		}, "as:s1"),
		mkSnap("googleplay", "Macro Trainer", "2026-11-26", core.RatingSnapshotPayload{
			AverageRating: 4.5, DailyAverage: 4.5,
		}, "gp:s1"),
	)

	sum, err := st.Reviews(ctx, store.ReviewsQuery{Range: "30d"}, "2026-11-27")
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if sum.TotalReviews != 3 {
		t.Errorf("TotalReviews = %d, want 3", sum.TotalReviews)
	}
	// Macro Trainer: App Store (10 ratings) + Play (1 written review) + Nistis App Store (1 written) = 12
	if sum.TotalRatings != 12 {
		t.Errorf("TotalRatings = %d, want 12", sum.TotalRatings)
	}
	if sum.LowReviews != 1 {
		t.Errorf("LowReviews = %d, want 1", sum.LowReviews)
	}
	if sum.Distribution != [5]int{0, 1, 0, 0, 2} {
		t.Errorf("Distribution = %v, want [0 1 0 0 2]", sum.Distribution)
	}
	if len(sum.ByApp) != 2 || sum.ByApp[0].Product != "Macro Trainer" {
		t.Errorf("ByApp = %+v", sum.ByApp)
	}
	if len(sum.Trend) != 30 {
		t.Errorf("Trend len = %d, want 30", len(sum.Trend))
	}

	// Low-star filter returns only the 2★ review.
	low, err := st.Reviews(ctx, store.ReviewsQuery{Range: "30d", Stars: -1}, "2026-11-27")
	if err != nil {
		t.Fatalf("Reviews low: %v", err)
	}
	if len(low.Reviews) != 1 || low.Reviews[0].Rating != 2 {
		t.Errorf("low Reviews = %+v, want only the 2-star review", low.Reviews)
	}
}
