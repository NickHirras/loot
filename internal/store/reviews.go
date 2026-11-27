package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// ReviewsQuery filters GET /api/reviews.
type ReviewsQuery struct {
	Range  string // 30d, 90d, 1y, all
	Source string // optional source filter (appstore, googleplay, microsoftstore)
	Stars  int    // optional star filter: 1..5, or -1 for 1-2★ ("low"), 0 for all
	Limit  int    // max written reviews returned (default 200)
}

// ReviewItem is one written customer review returned by GET /api/reviews.
type ReviewItem struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Product    string    `json:"product"`
	Country    string    `json:"country,omitempty"`
	Rating     int       `json:"rating"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body,omitempty"`
	Author     string    `json:"author,omitempty"`
	AppVersion string    `json:"app_version,omitempty"`
	Device     string    `json:"device,omitempty"`
	Language   string    `json:"language,omitempty"`
	ReplyText  string    `json:"reply_text,omitempty"`
	ReplyDate  string    `json:"reply_date,omitempty"`
	URL        string    `json:"url,omitempty"`
	Day        string    `json:"day"`
	OccurredAt time.Time `json:"occurred_at"`
}

// StoreRatingSummary is one store's current rating state for one product.
type StoreRatingSummary struct {
	Source        string  `json:"source"`
	AverageRating float64 `json:"average_rating"`
	RatingCount   int     `json:"rating_count"`
	ReviewCount   int     `json:"review_count"`
	AsOf          string  `json:"as_of,omitempty"`
}

// AppRatingSummary aggregates ratings and reviews for one product across stores.
type AppRatingSummary struct {
	Product       string               `json:"product"`
	AverageRating float64              `json:"average_rating"`
	RatingCount   int                  `json:"rating_count"`
	ReviewCount   int                  `json:"review_count"`
	Distribution  [5]int               `json:"distribution"`
	Stores        []StoreRatingSummary `json:"stores"`
}

// RatingTrendPoint is one day of the rating trend chart.
type RatingTrendPoint struct {
	Day           string   `json:"day"`
	AverageRating *float64 `json:"average_rating"`
	ReviewCount   int      `json:"review_count"`
	RatingCount   int      `json:"rating_count"`
}

// ReviewsSummary is the whole of GET /api/reviews.
type ReviewsSummary struct {
	Range         string             `json:"range"`
	From          string             `json:"from"`
	To            string             `json:"to"`
	AverageRating float64            `json:"average_rating"`
	TotalRatings  int                `json:"total_ratings"`
	TotalReviews  int                `json:"total_reviews"`
	LowReviews    int                `json:"low_reviews"`
	FiveStarShare float64            `json:"five_star_share"`
	Distribution  [5]int             `json:"distribution"`
	ByApp         []AppRatingSummary `json:"by_app"`
	Trend         []RatingTrendPoint `json:"trend"`
	Reviews       []ReviewItem       `json:"reviews"`
}

// Reviews aggregates ratings snapshots, star distributions, daily trends, and
// written reviews across App Store, Google Play and Microsoft Store.
func (s *Store) Reviews(ctx context.Context, q ReviewsQuery, today string) (ReviewsSummary, error) {
	if q.Range == "" {
		q.Range = "90d"
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 200
	}

	strict, strictArgs := s.scopeStrict("e")

	// 1. Read all written reviews (strictly scoped by product).
	revRows, err := s.q.QueryContext(ctx, `
        SELECT e.id, e.source, COALESCE(NULLIF(e.product, ''), e.app), e.country,
               e.quantity, e.day, e.occurred_at, e.payload
        FROM events e
        WHERE e.kind = ?`+strict+`
        ORDER BY e.occurred_at DESC, e.id DESC`,
		append([]any{core.KindReview}, strictArgs...)...)
	if err != nil {
		return ReviewsSummary{}, fmt.Errorf("reviews query: %w", err)
	}
	defer revRows.Close()

	var allReviews []ReviewItem
	var firstDay, lastDay string
	for revRows.Next() {
		var (
			id, src, prod, cc, day string
			qty                    int
			occMS                  int64
			rawPayload             string
		)
		if err := revRows.Scan(&id, &src, &prod, &cc, &qty, &day, &occMS, &rawPayload); err != nil {
			return ReviewsSummary{}, fmt.Errorf("scan review: %w", err)
		}
		occ := time.UnixMilli(occMS).UTC()
		var p core.ReviewPayload
		_ = json.Unmarshal([]byte(rawPayload), &p)
		rating := p.Rating
		if rating < 1 || rating > 5 {
			rating = qty
		}
		if rating < 1 || rating > 5 {
			continue
		}
		if day != "" {
			if firstDay == "" || day < firstDay {
				firstDay = day
			}
			if day > lastDay {
				lastDay = day
			}
		}
		allReviews = append(allReviews, ReviewItem{
			ID:         id,
			Source:     src,
			Product:    prod,
			Country:    cc,
			Rating:     rating,
			Title:      p.Title,
			Body:       p.Body,
			Author:     p.Author,
			AppVersion: p.AppVersion,
			Device:     p.Device,
			Language:   p.Language,
			ReplyText:  p.ReplyText,
			ReplyDate:  p.ReplyDate,
			URL:        p.URL,
			Day:        day,
			OccurredAt: occ,
		})
	}
	if err := revRows.Err(); err != nil {
		return ReviewsSummary{}, fmt.Errorf("iterate reviews: %w", err)
	}

	// 2. Read all rating snapshots (strictly scoped by product).
	type snapRow struct {
		Source  string
		Product string
		Day     string
		Payload core.RatingSnapshotPayload
	}
	snapQueryRows, err := s.q.QueryContext(ctx, `
        SELECT e.source, COALESCE(NULLIF(e.product, ''), e.app), e.day, e.payload
        FROM events e
        WHERE e.kind = ?`+strict+`
        ORDER BY e.day ASC, e.source ASC`,
		append([]any{core.KindRatingSnapshot}, strictArgs...)...)
	if err != nil {
		return ReviewsSummary{}, fmt.Errorf("rating snapshots query: %w", err)
	}
	defer snapQueryRows.Close()

	var snaps []snapRow
	for snapQueryRows.Next() {
		var (
			src, prod, day, rawPayload string
		)
		if err := snapQueryRows.Scan(&src, &prod, &day, &rawPayload); err != nil {
			return ReviewsSummary{}, fmt.Errorf("scan rating snapshot: %w", err)
		}
		var p core.RatingSnapshotPayload
		_ = json.Unmarshal([]byte(rawPayload), &p)
		if day != "" {
			if firstDay == "" || day < firstDay {
				firstDay = day
			}
			if day > lastDay {
				lastDay = day
			}
		}
		snaps = append(snaps, snapRow{Source: src, Product: prod, Day: day, Payload: p})
	}
	if err := snapQueryRows.Err(); err != nil {
		return ReviewsSummary{}, fmt.Errorf("iterate rating snapshots: %w", err)
	}

	// 3. Resolve date window for the trend chart.
	from, to, days, err := resolveReviewsWindow(q.Range, today, firstDay, lastDay)
	if err != nil {
		return ReviewsSummary{}, err
	}

	// 4. Aggregate per-(product, source) state across all time for the overview.
	type pair struct{ product, source string }
	type storeAgg struct {
		product       string
		source        string
		reviewCount   int
		reviewSum     int
		reviewStars   [5]int
		snapAvg       float64
		snapAvgDay    string
		snapCumCount  int
		snapDayCount  int
		snapDaySum    float64
		snapStars     [5]int
		hasSnapStars  bool
		latestSnapDay string
	}
	pairs := map[pair]*storeAgg{}
	getPair := func(prod, src string) *storeAgg {
		k := pair{prod, src}
		a, ok := pairs[k]
		if !ok {
			a = &storeAgg{product: prod, source: src}
			pairs[k] = a
		}
		return a
	}

	for _, r := range allReviews {
		if q.Source != "" && r.Source != q.Source {
			continue
		}
		a := getPair(r.Product, r.Source)
		a.reviewCount++
		a.reviewSum += r.Rating
		a.reviewStars[r.Rating-1]++
	}
	for _, sn := range snaps {
		if q.Source != "" && sn.Source != q.Source {
			continue
		}
		a := getPair(sn.Product, sn.Source)
		if sn.Day > a.latestSnapDay {
			a.latestSnapDay = sn.Day
		}
		if sn.Payload.AverageRating > 0 && sn.Day >= a.snapAvgDay {
			a.snapAvg = sn.Payload.AverageRating
			a.snapAvgDay = sn.Day
		}
		if sn.Payload.CumulativeCount && sn.Payload.RatingCount > 0 {
			if sn.Day >= a.snapAvgDay || sn.Payload.RatingCount > a.snapCumCount {
				a.snapCumCount = sn.Payload.RatingCount
			}
		} else if sn.Payload.RatingCount > 0 {
			a.snapDayCount += sn.Payload.RatingCount
			if sn.Payload.DailyAverage > 0 {
				a.snapDaySum += sn.Payload.DailyAverage * float64(sn.Payload.RatingCount)
			}
		}
		starSum := sn.Payload.Stars[0] + sn.Payload.Stars[1] + sn.Payload.Stars[2] + sn.Payload.Stars[3] + sn.Payload.Stars[4]
		if starSum > 0 {
			a.hasSnapStars = true
			for i := 0; i < 5; i++ {
				a.snapStars[i] += sn.Payload.Stars[i]
			}
		}
	}

	// Fold per-store aggregates into per-app summaries and overall headline.
	type appAcc struct {
		product     string
		stores      []StoreRatingSummary
		dist        [5]int
		reviewCount int
		ratingCount int
		weightedSum float64
		weightDenom float64
	}
	appsMap := map[string]*appAcc{}
	for _, a := range pairs {
		// Determine best rating count & average for this (product, source).
		ratingCount := a.reviewCount
		if a.snapCumCount > ratingCount {
			ratingCount = a.snapCumCount
		}
		if a.snapDayCount > ratingCount {
			ratingCount = a.snapDayCount
		}
		avg := 0.0
		switch {
		case a.snapAvg > 0:
			avg = a.snapAvg
		case a.snapDayCount > 0 && a.snapDaySum > 0:
			avg = a.snapDaySum / float64(a.snapDayCount)
		case a.reviewCount > 0:
			avg = float64(a.reviewSum) / float64(a.reviewCount)
		}
		avg = round2(avg)

		acc, ok := appsMap[a.product]
		if !ok {
			acc = &appAcc{product: a.product}
			appsMap[a.product] = acc
		}
		acc.stores = append(acc.stores, StoreRatingSummary{
			Source:        a.source,
			AverageRating: avg,
			RatingCount:   ratingCount,
			ReviewCount:   a.reviewCount,
			AsOf:          a.latestSnapDay,
		})
		acc.reviewCount += a.reviewCount
		acc.ratingCount += ratingCount
		if avg > 0 {
			w := float64(ratingCount)
			if w <= 0 {
				w = 1
			}
			acc.weightedSum += avg * w
			acc.weightDenom += w
		}
		// Distribution: prefer snapshot star histogram if larger than written reviews.
		if a.hasSnapStars {
			for i := 0; i < 5; i++ {
				acc.dist[i] += max(a.snapStars[i], a.reviewStars[i])
			}
		} else {
			for i := 0; i < 5; i++ {
				acc.dist[i] += a.reviewStars[i]
			}
		}
	}

	byApp := make([]AppRatingSummary, 0, len(appsMap))
	var (
		totalWeightedSum float64
		totalWeightDenom float64
		totalRatings     int
		totalReviews     int
		totalDist        [5]int
	)
	for _, acc := range appsMap {
		sort.Slice(acc.stores, func(i, j int) bool {
			if acc.stores[i].RatingCount != acc.stores[j].RatingCount {
				return acc.stores[i].RatingCount > acc.stores[j].RatingCount
			}
			return acc.stores[i].Source < acc.stores[j].Source
		})
		appAvg := 0.0
		if acc.weightDenom > 0 {
			appAvg = round2(acc.weightedSum / acc.weightDenom)
		}
		byApp = append(byApp, AppRatingSummary{
			Product:       acc.product,
			AverageRating: appAvg,
			RatingCount:   acc.ratingCount,
			ReviewCount:   acc.reviewCount,
			Distribution:  acc.dist,
			Stores:        acc.stores,
		})
		totalWeightedSum += acc.weightedSum
		totalWeightDenom += acc.weightDenom
		totalRatings += acc.ratingCount
		totalReviews += acc.reviewCount
		for i := 0; i < 5; i++ {
			totalDist[i] += acc.dist[i]
		}
	}
	sort.Slice(byApp, func(i, j int) bool {
		if byApp[i].RatingCount != byApp[j].RatingCount {
			return byApp[i].RatingCount > byApp[j].RatingCount
		}
		if byApp[i].ReviewCount != byApp[j].ReviewCount {
			return byApp[i].ReviewCount > byApp[j].ReviewCount
		}
		return byApp[i].Product < byApp[j].Product
	})

	overallAvg := 0.0
	if totalWeightDenom > 0 {
		overallAvg = round2(totalWeightedSum / totalWeightDenom)
	}
	distTotal := totalDist[0] + totalDist[1] + totalDist[2] + totalDist[3] + totalDist[4]
	fiveShare := 0.0
	if distTotal > 0 {
		fiveShare = roundN(float64(totalDist[4])/float64(distTotal), 4)
	}
	lowReviews := totalDist[0] + totalDist[1]

	// 5. Build daily rating trend over [from..to].
	// For each day: carry forward the latest cumulative average snapshot per
	// (product, source), or fall back to cumulative running average of written
	// reviews up to that day so even review-only stores plot a smooth trend.
	revByDay := map[string][]int{}
	for _, r := range allReviews {
		if q.Source != "" && r.Source != q.Source {
			continue
		}
		if r.Day != "" {
			revByDay[r.Day] = append(revByDay[r.Day], r.Rating)
		}
	}
	type snapPoint struct {
		key   pair
		avg   float64
		count int
	}
	snapsByDay := map[string][]snapPoint{}
	dailyRatingAdds := map[string]int{}
	for _, sn := range snaps {
		if q.Source != "" && sn.Source != q.Source {
			continue
		}
		if sn.Day == "" {
			continue
		}
		avg := sn.Payload.AverageRating
		if avg == 0 {
			avg = sn.Payload.DailyAverage
		}
		if avg > 0 {
			snapsByDay[sn.Day] = append(snapsByDay[sn.Day], snapPoint{
				key:   pair{sn.Product, sn.Source},
				avg:   avg,
				count: sn.Payload.RatingCount,
			})
		}
		if !sn.Payload.CumulativeCount && sn.Payload.RatingCount > 0 {
			dailyRatingAdds[sn.Day] += sn.Payload.RatingCount
		}
	}

	// Seed running state strictly before `from`.
	latestSnap := map[pair]snapPoint{}
	runRevSum := 0
	runRevCnt := 0
	preDays := map[string]bool{}
	for d := range revByDay {
		if d < from {
			preDays[d] = true
		}
	}
	for d := range snapsByDay {
		if d < from {
			preDays[d] = true
		}
	}
	preList := make([]string, 0, len(preDays))
	for d := range preDays {
		preList = append(preList, d)
	}
	sort.Strings(preList)
	for _, d := range preList {
		for _, r := range revByDay[d] {
			runRevSum += r
			runRevCnt++
		}
		for _, sp := range snapsByDay[d] {
			latestSnap[sp.key] = sp
		}
	}

	trend := make([]RatingTrendPoint, len(days))
	for i, d := range days {
		revsToday := revByDay[d]
		for _, r := range revsToday {
			runRevSum += r
			runRevCnt++
		}
		for _, sp := range snapsByDay[d] {
			latestSnap[sp.key] = sp
		}

		var avgPtr *float64
		if len(latestSnap) > 0 {
			var wSum, wDen float64
			for _, sp := range latestSnap {
				w := float64(sp.count)
				if w <= 0 {
					w = 1
				}
				wSum += sp.avg * w
				wDen += w
			}
			if wDen > 0 {
				v := round2(wSum / wDen)
				avgPtr = &v
			}
		} else if runRevCnt > 0 {
			v := round2(float64(runRevSum) / float64(runRevCnt))
			avgPtr = &v
		}

		added := max(len(revsToday), dailyRatingAdds[d])
		trend[i] = RatingTrendPoint{
			Day:           d,
			AverageRating: avgPtr,
			ReviewCount:   len(revsToday),
			RatingCount:   added,
		}
	}

	// 6. Filter written reviews list by source and star filter.
	filtered := make([]ReviewItem, 0, min(len(allReviews), q.Limit))
	for _, r := range allReviews {
		if q.Source != "" && r.Source != q.Source {
			continue
		}
		if q.Stars == -1 {
			if r.Rating > 2 {
				continue
			}
		} else if q.Stars >= 1 && q.Stars <= 5 {
			if r.Rating != q.Stars {
				continue
			}
		}
		filtered = append(filtered, r)
		if len(filtered) >= q.Limit {
			break
		}
	}

	return ReviewsSummary{
		Range:         q.Range,
		From:          from,
		To:            to,
		AverageRating: overallAvg,
		TotalRatings:  totalRatings,
		TotalReviews:  totalReviews,
		LowReviews:    lowReviews,
		FiveStarShare: fiveShare,
		Distribution:  totalDist,
		ByApp:         byApp,
		Trend:         trend,
		Reviews:       filtered,
	}, nil
}

func resolveReviewsWindow(rangeKey, today, firstDay, lastDay string) (from, to string, days []string, err error) {
	key := strings.ToLower(strings.TrimSpace(rangeKey))
	switch key {
	case "30d", "90d", "1y", "all":
		return resolveTrendWindow(key, today, firstDay, lastDay, false)
	default:
		return "", "", nil, fmt.Errorf("unknown reviews range %q (use 30d, 90d, 1y or all)", rangeKey)
	}
}
