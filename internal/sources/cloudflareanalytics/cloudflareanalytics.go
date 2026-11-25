// Package cloudflareanalytics counts visits to the operator's own tracked
// links by running one operator-supplied SQL query against the Cloudflare
// Workers Analytics Engine SQL API, and turns the result into per-link,
// per-day funnel snapshots (core.KindLinkDay).
//
// The typical setup is a small redirect Worker that writes one data point per
// hit (the slug in a blob) and then sends the visitor on to the real
// destination. Loot does not know or care how that Worker is written: the
// query is the whole contract. It must return three columns,
//
//	day    YYYY-MM-DD, the day the visits belong to
//	label  text identifying the link, typically the slug
//	n      how many visits, normally SUM(_sample_interval)
//
// and the operator decides everything else in it: the trailing window, the
// timezone shift for day, bot filtering and how a label is derived. Loot
// re-runs it every poll and emits a fresh snapshot for each (label, day)
// whose count changed, exactly like the other funnel sources do for an ad
// platform's revised numbers (see core/funnel.go).
//
// The API (verified against
// https://developers.cloudflare.com/analytics/analytics-engine/sql-api/ and
// .../sql-reference/statements/) takes the SQL as the body of
//
//	POST /client/v4/accounts/{account_id}/analytics_engine/sql
//	Authorization: Bearer <token>
//
// and, with FORMAT JSON, answers
//
//	{"meta":[{"name":"day","type":"String"},...],"data":[{"day":"2026-08-17",...}],"rows":1}
//
// Cloudflare's documentation does not say how numbers are encoded. In
// practice they come back as JSON *strings* ("n":"16"), and a count built
// from SUM(_sample_interval) can be fractional once sampling kicks in, so
// both strings and numbers are accepted and rounded to the nearest int.
package cloudflareanalytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "cloudflareanalytics"

// DefaultBaseURL is Cloudflare's API root.
const DefaultBaseURL = "https://api.cloudflare.com"

// DefaultPollInterval is how often the query is re-run. Visit counts are only
// read on the funnel page, by day, so hourly keeps today's row fresh enough
// without spending the account's query budget.
const DefaultPollInterval = time.Hour

// MaxRows caps how many result rows one poll turns into snapshots. A query
// that groups on something unbounded (a full URL with query strings, say)
// would otherwise write a snapshot per junk label every hour; 5,000 is far
// more than any honest set of tracked links over a couple of weeks.
const MaxRows = 5000

// The three columns the query must return.
const (
	colDay   = "day"
	colLabel = "label"
	colCount = "n"
)

// Options configures a Source. APIToken, AccountID, App and SQL are required.
type Options struct {
	APIToken  string
	AccountID string
	// App is the configured app the visits are credited to (Event.App).
	App string
	// SQL is the operator's query. See the package comment for its contract.
	SQL string

	// BaseURL defaults to DefaultBaseURL; tests point it at httptest.
	BaseURL string
	// PollInterval defaults to DefaultPollInterval.
	PollInterval time.Duration
	// HTTPClient defaults to a client with a 60 second timeout.
	HTTPClient *http.Client
}

// Source implements core.Source and core.Checker.
type Source struct {
	token     string
	accountID string
	app       string
	// query is the operator's SQL with FORMAT JSON guaranteed at its end.
	query    string
	baseURL  string
	interval time.Duration
	client   *http.Client
	log      *slog.Logger
	// now is swappable for tests.
	now func() time.Time
}

// New validates opts and returns a Source. It fails early on a query that is
// not a single SELECT: the SQL API is read-only anyway, but a typo found at
// startup beats one found in last_error an hour later.
func New(opts Options, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	var missing []string
	if strings.TrimSpace(opts.APIToken) == "" {
		missing = append(missing, "api_token")
	}
	if strings.TrimSpace(opts.AccountID) == "" {
		missing = append(missing, "account_id")
	}
	if strings.TrimSpace(opts.App) == "" {
		missing = append(missing, "app")
	}
	if strings.TrimSpace(opts.SQL) == "" {
		missing = append(missing, "sql")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: missing %s", Name, strings.Join(missing, ", "))
	}
	query, err := PrepareSQL(opts.SQL)
	if err != nil {
		return nil, err
	}

	s := &Source{
		token:     strings.TrimSpace(opts.APIToken),
		accountID: strings.TrimSpace(opts.AccountID),
		app:       strings.TrimSpace(opts.App),
		query:     query,
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		interval:  opts.PollInterval,
		client:    opts.HTTPClient,
		log:       log,
		now:       time.Now,
	}
	if s.baseURL == "" {
		s.baseURL = DefaultBaseURL
	}
	if s.interval <= 0 {
		s.interval = DefaultPollInterval
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 60 * time.Second}
	}
	return s, nil
}

var (
	// leadingKeyword is the first word of the statement once comments are
	// gone. WITH is allowed because a CTE is still a single SELECT.
	leadingKeyword = regexp.MustCompile(`(?i)^\s*(select|with)\b`)
	// formatClause matches a trailing FORMAT <name>. It is anchored to the
	// end so formatDateTime(...) inside the query does not count.
	formatClause = regexp.MustCompile(`(?i)\bformat\s+([a-z]+)\s*$`)
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// PrepareSQL checks that sql looks like a single SELECT and returns it with
// " FORMAT JSON" appended when it has no FORMAT clause of its own. A FORMAT
// other than JSON is refused, because the response is parsed as JSON.
//
// The check is deliberately shallow: it is there to catch a pasted
// SHOW TABLES or two statements glued together, not to parse SQL.
func PrepareSQL(sql string) (string, error) {
	q := strings.TrimSpace(sql)
	q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	bare := strings.TrimSpace(blockComment.ReplaceAllString(lineComment.ReplaceAllString(q, ""), ""))
	if bare == "" {
		return "", fmt.Errorf("%s: sql is empty", Name)
	}
	if !leadingKeyword.MatchString(bare) {
		return "", fmt.Errorf("%s: sql must be a single SELECT statement (it starts with %q)", Name, firstWord(bare))
	}
	if strings.Contains(bare, ";") {
		return "", fmt.Errorf("%s: sql must be a single SELECT statement, but it contains ';'", Name)
	}
	if m := formatClause.FindStringSubmatch(bare); m != nil {
		if !strings.EqualFold(m[1], "JSON") {
			return "", fmt.Errorf("%s: sql ends in FORMAT %s; remove it or use FORMAT JSON", Name, m[1])
		}
		return q, nil
	}
	// A trailing line comment would swallow the appended clause.
	return q + "\nFORMAT JSON", nil
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return s.interval }

// Result is the FORMAT JSON response body.
type Result struct {
	Meta []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"meta"`
	Data []map[string]json.RawMessage `json:"data"`
	Rows int                          `json:"rows"`
}

// Columns returns the column names the result reported, from meta when
// present and otherwise from the first row.
func (r Result) Columns() []string {
	var cols []string
	if len(r.Meta) > 0 {
		for _, m := range r.Meta {
			cols = append(cols, m.Name)
		}
		return cols
	}
	if len(r.Data) > 0 {
		for k := range r.Data[0] {
			cols = append(cols, k)
		}
		sort.Strings(cols)
	}
	return cols
}

// checkColumns names what is missing and what came back instead, which is
// the one thing an operator needs to fix an alias.
func checkColumns(r Result) error {
	cols := r.Columns()
	if len(cols) == 0 {
		// An empty result with no meta proves nothing either way.
		return nil
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[c] = true
	}
	var missing []string
	for _, want := range []string{colDay, colLabel, colCount} {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: query must return columns day, label and n; missing %s (got %s) — alias them with AS",
			Name, strings.Join(missing, ", "), strings.Join(cols, ", "))
	}
	return nil
}

// Check implements core.Checker: it runs the query once, which exercises the
// token, the account id, the permission and the SQL in one request.
func (s *Source) Check(ctx context.Context) error {
	res, err := s.run(ctx)
	if err != nil {
		return err
	}
	if err := checkColumns(res); err != nil {
		return err
	}
	if len(res.Data) == 0 {
		return core.Warning{Msg: fmt.Sprintf("%s: the query ran but returned no rows; is the window wide enough and the dataset name right?", Name)}
	}
	return nil
}

// Poll implements core.Source. It keeps no state: every poll re-reads the
// operator's whole window, and the dedupe key (which carries a hash of the
// count) is what makes an unchanged day a no-op and a changed one news.
func (s *Source) Poll(ctx context.Context, state []byte) ([]core.Event, []byte, error) {
	res, err := s.run(ctx)
	if err != nil {
		return nil, state, err
	}
	if err := checkColumns(res); err != nil {
		return nil, state, err
	}
	events, stats := s.events(res, s.now().UTC())
	if stats.skipped > 0 {
		s.log.Warn("cloudflareanalytics: skipped rows with an empty label, unparseable day or bad count",
			"skipped", stats.skipped)
	}
	if stats.truncated > 0 {
		s.log.Warn("cloudflareanalytics: result truncated; narrow the query's window or labels",
			"max_rows", MaxRows, "dropped", stats.truncated)
	}
	return events, state, nil
}

type rowStats struct {
	skipped   int
	truncated int
}

type linkDay struct{ label, day string }

// events turns result rows into one KindLinkDay snapshot per (label, day).
func (s *Source) events(res Result, observed time.Time) ([]core.Event, rowStats) {
	var st rowStats
	rows := res.Data
	if len(rows) > MaxRows {
		st.truncated = len(rows) - MaxRows
		rows = rows[:MaxRows]
	}

	// Rows are summed per (label, day) rather than trusted to be unique: a
	// query that forgot a GROUP BY term would otherwise emit two snapshots
	// under the same key and the reader would keep whichever came last.
	counts := map[linkDay]float64{}
	var order []linkDay
	for _, row := range rows {
		label := strings.TrimSpace(stringValue(row[colLabel]))
		day, okDay := parseDay(stringValue(row[colDay]))
		n, okN := numberValue(row[colCount])
		if label == "" || !okDay || !okN || n < 0 {
			st.skipped++
			continue
		}
		k := linkDay{label, day}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k] += n
	}

	events := make([]core.Event, 0, len(order))
	for _, k := range order {
		visits := int(math.Round(counts[k]))
		d, _ := time.ParseInLocation(core.DayLayout, k.day, time.UTC)
		payload, _ := json.Marshal(core.LinkDay{Label: k.label, Visits: visits})
		events = append(events, core.Event{
			ID:         core.NewIDAt(d),
			Source:     Name,
			Kind:       core.KindLinkDay,
			App:        s.app,
			OccurredAt: d,
			ObservedAt: observed,
			Day:        k.day,
			DedupeKey:  core.FunnelSnapshotKey(Name, core.KindLinkDay, k.label, k.day, fingerprint(visits)),
			// Funnel snapshots are traffic, not income or customers: never
			// ledger, never a drop, no amount or quantity (core/funnel.go).
			IsLedger: false,
			Silent:   true,
			Payload:  payload,
		})
	}
	return events, st
}

// fingerprint is a short hash of the rounded count, so the dedupe key changes
// exactly when the number a reader would see changes.
func fingerprint(visits int) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(visits)))
	return hex.EncodeToString(sum[:4])
}

// stringValue reads a JSON string, or the literal text of any other scalar
// (a numeric label is still a label).
func stringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	t := strings.TrimSpace(string(raw))
	if t == "null" {
		return ""
	}
	return t
}

// numberValue accepts a JSON number or a string holding one, which is how the
// SQL API actually returns numeric columns.
func numberValue(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// parseDay accepts YYYY-MM-DD, and also a midnight datetime
// ("2026-08-17 00:00:00", what toStartOfDay returns), since that is an easy
// thing to write and means the same day.
func parseDay(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 10 {
		rest := strings.TrimLeft(s[10:], " T")
		rest = strings.TrimSuffix(rest, "Z")
		if rest != "00:00:00" && rest != "00:00:00.000" {
			return "", false
		}
		s = s[:10]
	}
	if _, err := time.Parse(core.DayLayout, s); err != nil {
		return "", false
	}
	return s, true
}

// run POSTs the query and decodes the JSON result.
func (s *Source) run(ctx context.Context) (Result, error) {
	var res Result
	url := fmt.Sprintf("%s/client/v4/accounts/%s/analytics_engine/sql", s.baseURL, s.accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(s.query))
	if err != nil {
		return res, fmt.Errorf("%s: build request: %w", Name, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client.Do(req)
	if err != nil {
		// The error carries the URL, which carries the account id but never
		// the token.
		return res, fmt.Errorf("%s: query: %w", Name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return res, fmt.Errorf("%s: read response: %w", Name, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return res, fmt.Errorf("%s: Cloudflare refused the token (%s): it needs the Account > Account Analytics > Read permission on account %s: %s",
			Name, resp.Status, s.accountID, errorMessage(body))
	case resp.StatusCode != http.StatusOK:
		return res, fmt.Errorf("%s: query failed (%s): %s", Name, resp.Status, errorMessage(body))
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&res); err != nil {
		return res, fmt.Errorf("%s: decode result (is the query's FORMAT JSON?): %w", Name, err)
	}
	return res, nil
}

// errorMessage pulls Cloudflare's own words out of an error body. The v4 API
// wraps errors as {"errors":[{"message":...}]}; the SQL engine sometimes
// answers in plain text instead, which is passed on trimmed.
func errorMessage(body []byte) string {
	var env struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err == nil && len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			if e.Code != 0 {
				msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
			} else {
				msgs = append(msgs, e.Message)
			}
		}
		return strings.Join(msgs, "; ")
	}
	t := strings.TrimSpace(string(body))
	if len(t) > 512 {
		t = t[:512] + "…"
	}
	if t == "" {
		return "(empty response)"
	}
	return t
}

var (
	_ core.Source  = (*Source)(nil)
	_ core.Checker = (*Source)(nil)
)
