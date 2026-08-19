package googleplay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "googleplay"

// StorageScope is the OAuth2 scope needed to read the reporting bucket.
const StorageScope = "https://www.googleapis.com/auth/devstorage.read_only"

// DefaultPollInterval is how often the bucket is checked. Play rewrites the
// monthly files roughly daily, and every file carries an md5 that lets an
// unchanged month be skipped without downloading it, so four polls a day costs
// four listings and keeps a late-arriving report from waiting until tomorrow.
const DefaultPollInterval = 6 * time.Hour

// monthLayout is how Play names a month in a report filename.
const monthLayout = "200601"

// Source implements core.Source over the Play reporting bucket.
type Source struct {
	// Bucket is the reporting bucket id, without the gs:// scheme.
	Bucket string
	// Packages optionally restricts ingest to these package names.
	Packages []string
	// BackfillMonths is how many monthly reports a poll considers, counting
	// the current month. 2 means "this month and last".
	BackfillMonths int

	// BaseURL is the Cloud Storage JSON API root; tests point it at httptest.
	BaseURL string
	// Tokens authenticates every request. Tests supply a static token.
	Tokens oauth2.TokenSource
	Client *http.Client
	Log    *slog.Logger

	// Location is the timezone Play's financial reports are stated in
	// (Pacific). Nil means "look it up, and fall back to a fixed -08:00".
	Location *time.Location
	// Now is swappable for tests.
	Now func() time.Time
}

// New returns the Google Play source for cfg. The caller checks
// cfg.Configured() first; an unconfigured section means "the user did not ask
// for this source", not an error.
func New(cfg config.GooglePlay, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if !cfg.Configured() {
		return nil, fmt.Errorf("googleplay: service_account_json_path and bucket are both required")
	}
	bucket := NormalizeBucket(cfg.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("googleplay: bucket %q is empty once the gs:// prefix is stripped", cfg.Bucket)
	}

	key, err := os.ReadFile(cfg.ServiceAccountJSONPath)
	if err != nil {
		return nil, fmt.Errorf("googleplay: read %s: %w", cfg.ServiceAccountJSONPath, err)
	}
	tokens, err := TokenSourceFromJSON(context.Background(), key)
	if err != nil {
		return nil, err
	}

	months := cfg.BackfillMonths
	if months <= 0 {
		months = 2
	}

	return &Source{
		Bucket:         bucket,
		Packages:       cfg.Packages,
		BackfillMonths: months,
		BaseURL:        DefaultBaseURL,
		Tokens:         tokens,
		Client:         &http.Client{Timeout: 5 * time.Minute},
		Log:            log,
		Now:            time.Now,
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return DefaultPollInterval }

// Check implements core.Checker: it lists a single object under sales/, which
// exercises the key, the scope, the bucket id and the Play Console grant in
// one request. An empty bucket is not a failure — a developer who has not sold
// anything yet still has a working configuration.
func (s *Source) Check(ctx context.Context) error {
	if s.Bucket == "" {
		return fmt.Errorf("googleplay: no bucket configured")
	}
	if _, err := s.List(ctx, salesPrefix, 1); err != nil {
		return err
	}
	return nil
}

// state is the persisted cursor.
type state struct {
	// SalesFiles maps a monthly sales object to the md5Hash Loot last read, so
	// an unchanged month is never downloaded twice.
	SalesFiles map[string]string `json:"sales_files"`
	// SummarizedDays maps a package to the newest day it has a sales_day
	// summary for. Late rows for an earlier day are still stored; they just do
	// not mint a second summary, because the vault sums the rows.
	SummarizedDays map[string]string `json:"summarized_days"`
	// InstallsCursor maps a package to the newest statistics day emitted.
	InstallsCursor map[string]string `json:"installs_cursor"`
	// InstallsFiles is the md5 skip list for the statistics CSVs, the same
	// trick as SalesFiles.
	InstallsFiles map[string]string `json:"installs_files"`
}

func decodeState(raw []byte) state {
	var st state
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			st = state{}
		}
	}
	if st.SalesFiles == nil {
		st.SalesFiles = map[string]string{}
	}
	if st.SummarizedDays == nil {
		st.SummarizedDays = map[string]string{}
	}
	if st.InstallsCursor == nil {
		st.InstallsCursor = map[string]string{}
	}
	if st.InstallsFiles == nil {
		st.InstallsFiles = map[string]string{}
	}
	return st
}

// Poll reads the sales and statistics reports for the months in the backfill
// window and returns the events they imply.
//
// Sales and statistics are independent: a failure in one still returns the
// other's events and the advanced cursor, because losing a day of installs to
// a transient 500 on the sales file would be a poor trade.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now().UTC()
	months := s.months(now)

	var (
		events   []core.Event
		firstErr error
	)

	sales, err := s.pollSales(ctx, &st, months, now)
	events = append(events, sales...)
	if err != nil {
		s.Log.Warn("googleplay: sales reports failed", "error", err)
		firstErr = err
	}

	installs, err := s.pollInstalls(ctx, &st, months, now)
	events = append(events, installs...)
	if err != nil {
		s.Log.Warn("googleplay: install statistics failed", "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("googleplay: encode state: %w", err)
	}
	return events, out, firstErr
}

// months lists the report months a poll considers, newest first. Both clocks
// are consulted: statistics are stated in UTC and sales in Pacific Time, and
// for a few hours a day those disagree about which month it is.
func (s *Source) months(now time.Time) []string {
	n := s.BackfillMonths
	if n < 1 {
		n = 1
	}

	seen := map[string]bool{}
	var out []string
	add := func(t time.Time) {
		m := t.Format(monthLayout)
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}

	add(now.UTC())
	add(now.In(s.location()))
	// Step month by month from the first of the month, so subtracting a month
	// from the 31st does not skip February.
	base := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		add(base.AddDate(0, -i, 0))
	}
	return out
}

// wantPackage applies the optional allowlist.
func (s *Source) wantPackage(pkg string) bool {
	if len(s.Packages) == 0 {
		return true
	}
	for _, p := range s.Packages {
		if strings.EqualFold(strings.TrimSpace(p), pkg) {
			return true
		}
	}
	return false
}

// location returns the reporting timezone for financial data. Loot ships as a
// distroless static binary with no tzdata, so a missing zone database falls
// back to a fixed -08:00 rather than to UTC: the only thing the zone decides
// is which day is settled, and that has a day of slack in it.
func (s *Source) location() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		s.Location = loc
	} else {
		s.Location = time.FixedZone("PT", -8*60*60)
	}
	return s.Location
}

func (s *Source) baseURL() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return s.BaseURL
}

func (s *Source) client() *http.Client {
	if s.Client == nil {
		return http.DefaultClient
	}
	return s.Client
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// dateLayouts are the shapes a report date has arrived in. Play has used at
// least three over the years and localizes some exports, so the date is tried
// against all of them before the row is given up on.
var dateLayouts = []string{
	core.DayLayout,
	"2006/01/02",
	"Jan 2, 2006",
	"January 2, 2006",
	"1/2/2006",
	"1/2/06",
	"02 Jan 2006",
}

// parseReportDate normalizes a report date to YYYY-MM-DD.
func parseReportDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(core.DayLayout), true
		}
	}
	return "", false
}

// timeLayouts are the shapes "Order Charged Timestamp" has arrived in.
var timeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",
	"Jan 2, 2006 3:04:05 PM MST",
	"Jan 2, 2006 3:04:05 PM",
	"1/2/2006 15:04:05",
	"1/2/2006 3:04:05 PM",
}

// parseReportTime reads an order timestamp. Zone-less values are read in loc,
// the report's own timezone, rather than in UTC.
func parseReportTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	// Some exports write epoch milliseconds instead of a formatted stamp.
	if n, err := parseDigits(s); err == nil {
		switch {
		case n > 1e11:
			return time.UnixMilli(n).UTC(), true
		case n > 1e8:
			return time.Unix(n, 0).UTC(), true
		}
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseDigits(s string) (int64, error) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
