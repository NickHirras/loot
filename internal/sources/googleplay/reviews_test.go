package googleplay

import (
	"testing"
)

func TestParseRatingsAndReviewsCSV(t *testing.T) {
	ratingsCSV := "Date,Package Name,Daily Average Rating,Total Average Rating\n" +
		"2026-11-25,com.macrotrainer.macrotrainer,5.00,4.85\n" +
		"2026-11-26,com.macrotrainer.macrotrainer,NA,4.85\n"
	ratings, err := ParseRatingsCSV([]byte(ratingsCSV))
	if err != nil {
		t.Fatalf("ParseRatingsCSV: %v", err)
	}
	if len(ratings) != 2 || ratings[0].TotalAverageRating != 4.85 || ratings[0].DailyAverageRating != 5.0 || ratings[1].DailyAverageRating != 0 {
		t.Fatalf("ratings = %+v", ratings)
	}

	reviewsCSV := "Package Name,App Version Code,App Version Name,Reviewer Language,Device,Review Submit Date and Time,Review Last Update Date and Time,Star Rating,Review Title,Review Text,Developer Reply Date and Time,Developer Reply Text,Review Link\n" +
		"com.macrotrainer.macrotrainer,120,1.20.0,en,Pixel 9,2026-11-26T10:00:00Z,2026-11-26T10:00:00Z,5,Best macro tracker,Super fast and clean!,,,https://play.google.com/console/review?reviewId=gp-rev-1\n"
	reviews, err := ParseReviewsCSV([]byte(reviewsCSV))
	if err != nil {
		t.Fatalf("ParseReviewsCSV: %v", err)
	}
	if len(reviews) != 1 || reviews[0].StarRating != 5 || reviews[0].Title != "Best macro tracker" ||
		reviewIDFromRow(reviews[0].Package, reviews[0]) != "gp-rev-1" {
		t.Fatalf("reviews = %+v", reviews)
	}
}
