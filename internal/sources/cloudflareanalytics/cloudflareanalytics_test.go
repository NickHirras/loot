package cloudflareanalytics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/cloudflareanalytics"
)

// Obviously fake values: this repository is public.
const (
	fakeToken   = "test-token-not-real"
	fakeAccount = "0123456789abcdef0123456789abcdef"
	fakeApp     = "com.example.app"
	testSQL     = `SELECT toDate(timestamp) AS day, blob1 AS label, SUM(_sample_interval) AS n
FROM my_links
WHERE timestamp > NOW() - INTERVAL '14' DAY
GROUP BY day, label`
)

// fakeCF is an httptest stand-in for the SQL API. It records what it was sent
// and answers with whatever body and status the test sets.
type fakeCF struct {
	mu     sync.Mutex
	status int
	body   string
	gotSQL []string
	gotAut []string
	gotURL []string
}

func (f *fakeCF) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.gotSQL = append(f.gotSQL, string(b))
	f.gotAut = append(f.gotAut, r.Header.Get("Authorization"))
	f.gotURL = append(f.gotURL, r.Method+" "+r.URL.Path)
	status, body := f.status, f.body
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeCF) set(status int, body string) {
	f.mu.Lock()
	f.status, f.body = status, body
	f.mu.Unlock()
}

func newSource(t *testing.T, f *fakeCF, sql string, log *slog.Logger) *cloudflareanalytics.Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s, err := cloudflareanalytics.New(cloudflareanalytics.Options{
		APIToken:   fakeToken,
		AccountID:  fakeAccount,
		App:        fakeApp,
		SQL:        sql,
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	}, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func result(rows string) string {
	return `{"meta":[{"name":"day","type":"Date"},{"name":"label","type":"String"},{"name":"n","type":"Float64"}],` +
		`"data":[` + rows + `],"rows":1}`
}

func linkDay(t *testing.T, ev core.Event) core.LinkDay {
	t.Helper()
	var ld core.LinkDay
	if err := json.Unmarshal(ev.Payload, &ld); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return ld
}

func TestPollStringNumericAndFractionalCounts(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`
		{"day":"2026-08-16","label":"spring-a","n":"16"},
		{"day":"2026-08-16","label":"spring-b","n":7},
		{"day":"2026-08-17","label":"spring-a","n":"12.6"},
		{"day":"2026-08-17","label":"spring-b","n":3.4}`))
	s := newSource(t, f, testSQL, nil)

	events, state, err := s.Poll(context.Background(), []byte("keep"))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if string(state) != "keep" {
		t.Errorf("state = %q, want it passed through", state)
	}
	want := map[string]int{
		"spring-a/2026-08-16": 16,
		"spring-b/2026-08-16": 7,
		"spring-a/2026-08-17": 13,
		"spring-b/2026-08-17": 3,
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for _, ev := range events {
		ld := linkDay(t, ev)
		key := ld.Label + "/" + ev.Day
		if ld.Visits != want[key] {
			t.Errorf("%s visits = %d, want %d", key, ld.Visits, want[key])
		}
		if ev.Kind != core.KindLinkDay || ev.Source != cloudflareanalytics.Name || ev.App != fakeApp {
			t.Errorf("kind/source/app = %s/%s/%s", ev.Kind, ev.Source, ev.App)
		}
		if !ev.Silent || ev.IsLedger || ev.Amount != 0 || ev.Quantity != 0 || ev.Chest {
			t.Errorf("%s: funnel snapshot must be silent, non-ledger, zero amount/quantity: %+v", key, ev)
		}
		if got := ev.OccurredAt.Format("2006-01-02 15:04:05 MST"); got != ev.Day+" 00:00:00 UTC" {
			t.Errorf("%s occurred_at = %s", key, got)
		}
		prefix := core.FunnelSnapshotKey(cloudflareanalytics.Name, core.KindLinkDay, ld.Label, ev.Day, "")
		if !strings.HasPrefix(ev.DedupeKey, prefix) || len(ev.DedupeKey) == len(prefix) {
			t.Errorf("dedupe key %q does not extend %q with a fingerprint", ev.DedupeKey, prefix)
		}
	}
}

func TestRequestShape(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`{"day":"2026-08-16","label":"a","n":"1"}`))
	s := newSource(t, f, testSQL+" ;", nil)
	if _, _, err := s.Poll(context.Background(), nil); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got, want := f.gotURL[0], "POST /client/v4/accounts/"+fakeAccount+"/analytics_engine/sql"; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
	if f.gotAut[0] != "Bearer "+fakeToken {
		t.Errorf("Authorization = %q", f.gotAut[0])
	}
	sql := f.gotSQL[0]
	if n := strings.Count(strings.ToUpper(sql), "FORMAT JSON"); n != 1 {
		t.Errorf("FORMAT JSON appears %d times in %q", n, sql)
	}
	if strings.Contains(sql, ";") {
		t.Errorf("trailing semicolon was sent: %q", sql)
	}
}

func TestFormatJSONNotDuplicated(t *testing.T) {
	for _, in := range []string{
		testSQL + " FORMAT JSON",
		testSQL + "\nformat json;",
		"SELECT formatDateTime(timestamp, '%F') AS day, blob1 AS label, count() AS n FROM my_links GROUP BY day, label",
	} {
		out, err := cloudflareanalytics.PrepareSQL(in)
		if err != nil {
			t.Fatalf("PrepareSQL(%q): %v", in, err)
		}
		if n := strings.Count(strings.ToUpper(out), "FORMAT JSON"); n != 1 {
			t.Errorf("PrepareSQL(%q) = %q: FORMAT JSON appears %d times", in, out, n)
		}
	}
	if _, err := cloudflareanalytics.PrepareSQL(testSQL + " FORMAT JSONEachRow"); err == nil {
		t.Error("FORMAT JSONEachRow accepted; the result is parsed as JSON")
	}
}

func TestNonSelectRejected(t *testing.T) {
	for _, sql := range []string{
		"SHOW TABLES",
		"DROP TABLE my_links",
		"SELECT 1 AS n; SELECT 2 AS n",
		"-- just a comment",
	} {
		_, err := cloudflareanalytics.New(cloudflareanalytics.Options{
			APIToken: fakeToken, AccountID: fakeAccount, App: fakeApp, SQL: sql,
		}, nil)
		if err == nil {
			t.Errorf("New accepted %q", sql)
		}
	}
	// A CTE is still one SELECT.
	if _, err := cloudflareanalytics.PrepareSQL("WITH x AS (SELECT 1) SELECT * FROM x"); err != nil {
		t.Errorf("WITH rejected: %v", err)
	}
}

func TestMissingOptions(t *testing.T) {
	_, err := cloudflareanalytics.New(cloudflareanalytics.Options{SQL: testSQL}, nil)
	if err == nil || !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "account_id") ||
		!strings.Contains(err.Error(), "app") {
		t.Errorf("err = %v, want it to name the missing keys", err)
	}
}

func TestUnchangedPollDedupesChangedPollIsNew(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`{"day":"2026-08-16","label":"a","n":"16"},{"day":"2026-08-17","label":"a","n":"4"}`))
	s := newSource(t, f, testSQL, nil)

	keys := func() map[string]string {
		evs, _, err := s.Poll(context.Background(), nil)
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		m := map[string]string{}
		for _, ev := range evs {
			m[ev.Day] = ev.DedupeKey
		}
		return m
	}
	first, second := keys(), keys()
	for day, k := range first {
		if second[day] != k {
			t.Errorf("%s: unchanged poll gave key %q then %q", day, k, second[day])
		}
	}

	// A sampling wobble that rounds to the same count is not news either.
	f.set(200, result(`{"day":"2026-08-16","label":"a","n":"16.2"},{"day":"2026-08-17","label":"a","n":"5"}`))
	third := keys()
	if third["2026-08-16"] != first["2026-08-16"] {
		t.Errorf("16 -> 16.2 changed the key")
	}
	if third["2026-08-17"] == first["2026-08-17"] {
		t.Errorf("4 -> 5 kept the same key %q", third["2026-08-17"])
	}
}

func TestBadRowsSkippedWithOneWarning(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`
		{"day":"2026-08-16","label":"good","n":"3"},
		{"day":"2026-08-16","label":"","n":"3"},
		{"day":"2026-08-16","label":null,"n":"3"},
		{"day":"not-a-day","label":"x","n":"3"},
		{"day":"2026-08-16 05:00:00","label":"x","n":"3"},
		{"day":"2026-08-17 00:00:00","label":"midnight","n":"2"},
		{"day":"2026-08-16","label":"x","n":"lots"},
		{"day":"2026-08-16","label":"x","n":"-1"}`))
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	s := newSource(t, f, testSQL, log)

	evs, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (good, midnight)", len(evs))
	}
	if evs[1].Day != "2026-08-17" {
		t.Errorf("midnight datetime day = %q", evs[1].Day)
	}
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "skipped=6") {
		t.Errorf("want one warning with skipped=6, got:\n%s", out)
	}
	if strings.Contains(out, fakeToken) {
		t.Error("token appeared in the log")
	}
}

func TestDuplicateRowsAreSummed(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`{"day":"2026-08-16","label":"a","n":"2"},{"day":"2026-08-16","label":"a","n":"3"}`))
	s := newSource(t, f, testSQL, nil)
	evs, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(evs) != 1 || linkDay(t, evs[0]).Visits != 5 {
		t.Fatalf("events = %+v, want one with 5 visits", evs)
	}
}

func TestTruncatedAtMaxRows(t *testing.T) {
	var rows []string
	for i := 0; i < cloudflareanalytics.MaxRows+3; i++ {
		rows = append(rows, `{"day":"2026-08-16","label":"l`+itoa(i)+`","n":"1"}`)
	}
	f := &fakeCF{}
	f.set(200, result(strings.Join(rows, ",")))
	var buf bytes.Buffer
	s := newSource(t, f, testSQL, slog.New(slog.NewTextHandler(&buf, nil)))
	evs, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(evs) != cloudflareanalytics.MaxRows {
		t.Errorf("got %d events, want %d", len(evs), cloudflareanalytics.MaxRows)
	}
	if !strings.Contains(buf.String(), "dropped=3") {
		t.Errorf("no truncation warning:\n%s", buf.String())
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestMissingColumns(t *testing.T) {
	f := &fakeCF{}
	f.set(200, `{"meta":[{"name":"d","type":"Date"},{"name":"label","type":"String"},{"name":"hits","type":"UInt64"}],"data":[],"rows":0}`)
	s := newSource(t, f, testSQL, nil)
	err := s.Check(context.Background())
	if err == nil {
		t.Fatal("Check passed with missing columns")
	}
	for _, want := range []string{"missing day, n", "got d, label, hits"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not contain %q", err, want)
		}
	}
	if _, _, err := s.Poll(context.Background(), nil); err == nil {
		t.Error("Poll passed with missing columns")
	}
}

func TestCheckForbidden(t *testing.T) {
	f := &fakeCF{}
	f.set(403, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
	s := newSource(t, f, testSQL, nil)
	err := s.Check(context.Background())
	if err == nil {
		t.Fatal("Check passed on 403")
	}
	for _, want := range []string{"Account Analytics", "Read", "Authentication error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Error("token appeared in the error")
	}
}

func TestCheckSQLError(t *testing.T) {
	f := &fakeCF{}
	f.set(422, "unknown table: my_linkz")
	s := newSource(t, f, testSQL, nil)
	err := s.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown table: my_linkz") {
		t.Errorf("err = %v, want Cloudflare's message", err)
	}
}

func TestCheckOK(t *testing.T) {
	f := &fakeCF{}
	f.set(200, result(`{"day":"2026-08-16","label":"a","n":"1"}`))
	s := newSource(t, f, testSQL, nil)
	if err := s.Check(context.Background()); err != nil {
		t.Errorf("Check: %v", err)
	}

	f.set(200, result(``))
	if err := s.Check(context.Background()); !core.IsWarning(err) {
		t.Errorf("empty result: err = %v, want a warning", err)
	}
}
