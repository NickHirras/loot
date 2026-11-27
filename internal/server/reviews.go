package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// handleReviews serves GET /api/reviews: rating summaries, star distribution,
// daily rating trend, and written customer reviews across stores.
func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	rangeKey := strings.ToLower(strings.TrimSpace(qs.Get("range")))
	if rangeKey == "" {
		rangeKey = "90d"
	}
	source := strings.ToLower(strings.TrimSpace(qs.Get("source")))

	stars := 0
	if raw := strings.ToLower(strings.TrimSpace(qs.Get("stars"))); raw != "" {
		if raw == "low" {
			stars = -1
		} else if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 5 {
			stars = n
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid stars filter (use 1..5 or low)",
			})
			return
		}
	}

	limit := 200
	if raw := strings.TrimSpace(qs.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	today := core.DayOf(time.Now().UTC())
	summary, err := s.scoped(r).Reviews(r.Context(), store.ReviewsQuery{
		Range:  rangeKey,
		Source: source,
		Stars:  stars,
		Limit:  limit,
	}, today)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown reviews range") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.fail(w, "reviews", err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
