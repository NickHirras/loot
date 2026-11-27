package appstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

func TestPollReviewsAndItunesRating(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}

	ascSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/apps":
			_, _ = w.Write([]byte(`{"data":[{"id":"6763687102","attributes":{"name":"Macro Trainer","bundleId":"com.mt"}}]}`))
		case "/v1/apps/6763687102/customerReviews":
			_, _ = w.Write([]byte(`{"data":[{"id":"rev-1","attributes":{"rating":5,"title":"Love it","body":"Simple and fast","reviewerNickname":"Alex","createdDate":"2026-11-26T12:00:00Z","territory":"USA"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ascSrv.Close()

	itunesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"trackId":6763687102,"trackName":"Macro Trainer","averageUserRating":4.9,"userRatingCount":14}]}`))
	}))
	defer itunesSrv.Close()

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := &Source{
		BaseURL:      ascSrv.URL,
		LookupURL:    itunesSrv.URL,
		Client:       ascSrv.Client(),
		Now:          func() time.Time { return now },
		reviewTokens: &tokenCache{keyID: "REVKEY", issuerID: "ISS", key: key},
	}

	if err := s.checkReviews(context.Background()); err != nil {
		t.Fatalf("checkReviews: %v", err)
	}

	st := state{}
	events, err := s.pollReviewsAndRatings(context.Background(), &st, "2026-11-27", now)
	if err != nil {
		t.Fatalf("pollReviewsAndRatings: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d (%+v), want 2 (1 review + 1 rating_snapshot)", len(events), events)
	}
	rev := events[0]
	if rev.Kind != core.KindReview || rev.Quantity != 5 || rev.Country != "US" || rev.App != "Macro Trainer" || rev.Chest {
		t.Errorf("unexpected review event: %+v", rev)
	}
	snap := events[1]
	if snap.Kind != core.KindRatingSnapshot || snap.Quantity != 14 || !snap.Silent {
		t.Errorf("unexpected rating snapshot: %+v", snap)
	}
	var sp core.RatingSnapshotPayload
	if err := json.Unmarshal(snap.Payload, &sp); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if sp.AverageRating != 4.9 || sp.RatingCount != 14 || !sp.CumulativeCount {
		t.Errorf("snapshot payload = %+v", sp)
	}
}
