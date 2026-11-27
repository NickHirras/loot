package googleplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

const (
	ratingsPrefix = "stats/ratings/"
	reviewsPrefix = "reviews/"
)

var (
	ratingsObjectRe = regexp.MustCompile(`^stats/ratings/ratings_(.+)_(\d{6})_overview\.csv$`)
	reviewsObjectRe = regexp.MustCompile(`^reviews/reviews_(.+)_(\d{6})\.csv$`)
)

// RatingRow is one day of a Google Play overview ratings CSV.
type RatingRow struct {
	Date               string
	Package            string
	DailyAverageRating float64
	TotalAverageRating float64
}

// ReviewRow is one customer review from a Google Play monthly reviews CSV.
type ReviewRow struct {
	Package        string
	AppVersionName string
	AppVersionCode string
	Language       string
	Device         string
	SubmitTime     time.Time
	UpdateTime     time.Time
	StarRating     int
	Title          string
	Text           string
	ReplyTime      time.Time
	ReplyText      string
	ReviewLink     string
}

func parseRatingFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NA") || strings.EqualFold(s, "null") {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 5 {
		return 0
	}
	return v
}

func parseReviewTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ParseRatingsCSV reads one stats/ratings/ratings_<pkg>_YYYYMM_overview.csv file.
func ParseRatingsCSV(raw []byte) ([]RatingRow, error) {
	h, records, err := readCSV(raw)
	if err != nil {
		return nil, fmt.Errorf("googleplay: ratings report: %w", err)
	}
	if len(records) == 0 || !h.has("date") {
		return nil, nil
	}
	rows := make([]RatingRow, 0, len(records))
	for _, rec := range records {
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		day, ok := parseReportDate(h.get(rec, "date"))
		if !ok {
			continue
		}
		daily := parseRatingFloat(h.get(rec, "daily average rating"))
		total := parseRatingFloat(h.get(rec, "total average rating"))
		if daily == 0 && total == 0 {
			continue
		}
		rows = append(rows, RatingRow{
			Date:               day,
			Package:            h.get(rec, "package name", "package id"),
			DailyAverageRating: daily,
			TotalAverageRating: total,
		})
	}
	return rows, nil
}

// ParseReviewsCSV reads one reviews/reviews_<pkg>_YYYYMM.csv file.
func ParseReviewsCSV(raw []byte) ([]ReviewRow, error) {
	h, records, err := readCSV(raw)
	if err != nil {
		return nil, fmt.Errorf("googleplay: reviews report: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	rows := make([]ReviewRow, 0, len(records))
	for _, rec := range records {
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		stars := parseInt(h.get(rec, "star rating"))
		if stars < 1 || stars > 5 {
			continue
		}
		submit := parseReviewTime(h.get(rec, "review submit date and time"))
		update := parseReviewTime(h.get(rec, "review last update date and time"))
		if submit.IsZero() {
			submit = update
		}
		if update.IsZero() {
			update = submit
		}
		if submit.IsZero() {
			continue
		}
		rows = append(rows, ReviewRow{
			Package:        h.get(rec, "package name"),
			AppVersionName: h.get(rec, "app version name"),
			AppVersionCode: h.get(rec, "app version code"),
			Language:       h.get(rec, "reviewer language"),
			Device:         h.get(rec, "device"),
			SubmitTime:     submit,
			UpdateTime:     update,
			StarRating:     stars,
			Title:          strings.TrimSpace(h.get(rec, "review title")),
			Text:           strings.TrimSpace(h.get(rec, "review text")),
			ReplyTime:      parseReviewTime(h.get(rec, "developer reply date and time")),
			ReplyText:      strings.TrimSpace(h.get(rec, "developer reply text")),
			ReviewLink:     strings.TrimSpace(h.get(rec, "review link")),
		})
	}
	return rows, nil
}

func (s *Source) pollRatings(ctx context.Context, st *state, months []string, now time.Time) ([]core.Event, error) {
	objects, err := s.List(ctx, ratingsPrefix, 0)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(months))
	for _, m := range months {
		wanted[m] = true
	}
	today := now.UTC().Format(core.DayLayout)

	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	var (
		events []core.Event
		seen   = map[string]bool{}
	)
	for _, obj := range objects {
		m := ratingsObjectRe.FindStringSubmatch(obj.Name)
		if m == nil {
			continue
		}
		pkg, month := m[1], m[2]
		if !wanted[month] || !s.wantPackage(pkg) {
			continue
		}
		seen[obj.Name] = true
		if prev, ok := st.RatingsFiles[obj.Name]; ok && prev != "" && prev == obj.MD5Hash {
			continue
		}

		raw, err := s.Download(ctx, obj.Name)
		if err != nil {
			return events, err
		}
		rows, err := ParseRatingsCSV(raw)
		if err != nil {
			return events, err
		}
		for _, r := range rows {
			rowPkg := r.Package
			if rowPkg == "" {
				rowPkg = pkg
			}
			if !s.wantPackage(rowPkg) || r.Date >= today {
				continue
			}
			occurred, err := time.ParseInLocation(core.DayLayout, r.Date, time.UTC)
			if err != nil {
				continue
			}
			payload, _ := json.Marshal(core.RatingSnapshotPayload{
				AverageRating: r.TotalAverageRating,
				DailyAverage:  r.DailyAverageRating,
			})
			events = append(events, core.Event{
				ID:         core.NewIDAt(occurred),
				Source:     Name,
				Kind:       core.KindRatingSnapshot,
				App:        rowPkg,
				OccurredAt: occurred,
				ObservedAt: now,
				Day:        r.Date,
				DedupeKey:  fmt.Sprintf("play:rating:%s:%s", rowPkg, r.Date),
				Silent:     true,
				Payload:    payload,
			})
		}
		st.RatingsFiles[obj.Name] = obj.MD5Hash
	}
	for name := range st.RatingsFiles {
		if !seen[name] {
			delete(st.RatingsFiles, name)
		}
	}
	return events, nil
}

func (s *Source) pollReviews(ctx context.Context, st *state, months []string, now time.Time) ([]core.Event, error) {
	objects, err := s.List(ctx, reviewsPrefix, 0)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(months))
	for _, m := range months {
		wanted[m] = true
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	var (
		events []core.Event
		seen   = map[string]bool{}
	)
	for _, obj := range objects {
		m := reviewsObjectRe.FindStringSubmatch(obj.Name)
		if m == nil {
			continue
		}
		pkg, month := m[1], m[2]
		if !wanted[month] || !s.wantPackage(pkg) {
			continue
		}
		seen[obj.Name] = true
		if prev, ok := st.ReviewsFiles[obj.Name]; ok && prev != "" && prev == obj.MD5Hash {
			continue
		}

		raw, err := s.Download(ctx, obj.Name)
		if err != nil {
			return events, err
		}
		rows, err := ParseReviewsCSV(raw)
		if err != nil {
			return events, err
		}
		for _, r := range rows {
			rowPkg := r.Package
			if rowPkg == "" {
				rowPkg = pkg
			}
			if !s.wantPackage(rowPkg) {
				continue
			}
			revID := reviewIDFromRow(rowPkg, r)
			day := core.DayOf(r.UpdateTime)
			version := r.AppVersionName
			if version == "" {
				version = r.AppVersionCode
			}
			replyDate := ""
			if !r.ReplyTime.IsZero() {
				replyDate = core.DayOf(r.ReplyTime)
			}
			payload, _ := json.Marshal(core.ReviewPayload{
				ReviewID:   revID,
				Rating:     r.StarRating,
				Title:      r.Title,
				Body:       r.Text,
				AppVersion: version,
				Device:     r.Device,
				Language:   r.Language,
				ReplyText:  r.ReplyText,
				ReplyDate:  replyDate,
				URL:        r.ReviewLink,
			})
			events = append(events, core.Event{
				ID:         core.NewIDAt(r.UpdateTime),
				Source:     Name,
				Kind:       core.KindReview,
				App:        rowPkg,
				OccurredAt: r.UpdateTime,
				ObservedAt: now,
				Day:        day,
				Quantity:   r.StarRating,
				DedupeKey:  fmt.Sprintf("play:review:%s:%s", rowPkg, revID),
				Chest:      now.Sub(r.UpdateTime) > 48*time.Hour,
				Payload:    payload,
			})
		}
		st.ReviewsFiles[obj.Name] = obj.MD5Hash
	}
	for name := range st.ReviewsFiles {
		if !seen[name] {
			delete(st.ReviewsFiles, name)
		}
	}
	return events, nil
}

func reviewIDFromRow(pkg string, r ReviewRow) string {
	if r.ReviewLink != "" {
		if i := strings.LastIndex(r.ReviewLink, "reviewId="); i >= 0 {
			id := r.ReviewLink[i+len("reviewId="):]
			if j := strings.IndexByte(id, '&'); j >= 0 {
				id = id[:j]
			}
			if id != "" {
				return id
			}
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%s",
		pkg, r.SubmitTime.UTC().Format(time.RFC3339), r.StarRating, r.Title, r.Text)))
	return hex.EncodeToString(sum[:12])
}
