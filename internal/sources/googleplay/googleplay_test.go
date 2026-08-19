package googleplay_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/googleplay"
)

const testBucket = "pubsite_prod_rev_01234567890"

// fixtureNow sits on 2026-08-18 09:00 UTC — 02:00 Pacific, so "yesterday" in
// the reports' own timezone is the 17th and everything up to the 16th counts
// as settled.
var fixtureNow = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// zipOf wraps a CSV fixture the way Play does: one CSV inside a monthly zip.
func zipOf(t *testing.T, name string, csv []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(csv); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func md5Hash(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// fakeGCS stands in for the Cloud Storage JSON API: it serves a fixed set of
// objects, records every download, and refuses anything without a bearer token.
type fakeGCS struct {
	mu        sync.Mutex
	objects   map[string][]byte
	downloads map[string]int
	server    *httptest.Server
	// status, when non-zero, is returned for every request.
	status int
}

func newFakeGCS(t *testing.T) *fakeGCS {
	t.Helper()
	f := &fakeGCS{objects: map[string][]byte{}, downloads: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGCS) put(name string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[name] = body
}

func (f *fakeGCS) downloadCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads[name]
}

func (f *fakeGCS) totalDownloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.downloads {
		n += c
	}
	return n
}

func (f *fakeGCS) handle(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, `{"error":{"message":"nope"}}`, status)
		return
	}

	base := "/storage/v1/b/" + testBucket + "/o"
	path := r.URL.EscapedPath()

	switch {
	case path == base:
		f.list(w, r)
	case strings.HasPrefix(path, base+"/"):
		name, err := url.PathUnescape(strings.TrimPrefix(path, base+"/"))
		if err != nil {
			http.Error(w, "bad name", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		body, ok := f.objects[name]
		if ok {
			f.downloads[name]++
		}
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":{"message":"No such object"}}`, http.StatusNotFound)
			return
		}
		w.Write(body)
	default:
		http.Error(w, "unexpected path "+path, http.StatusNotFound)
	}
}

func (f *fakeGCS) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	f.mu.Lock()
	type item struct {
		Name    string    `json:"name"`
		Updated time.Time `json:"updated"`
		Size    string    `json:"size"`
		MD5Hash string    `json:"md5Hash"`
	}
	var items []item
	for name, body := range f.objects {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		items = append(items, item{
			Name:    name,
			Updated: fixtureNow,
			Size:    fmt.Sprint(len(body)),
			MD5Hash: md5Hash(body),
		})
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// newSource wires a Source at the fake bucket with a static credential.
func newSource(f *fakeGCS, packages ...string) *googleplay.Source {
	return &googleplay.Source{
		Bucket:         testBucket,
		Packages:       packages,
		BackfillMonths: 2,
		BaseURL:        f.server.URL,
		Tokens:         oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
		Client:         f.server.Client(),
		Log:            quietLogger(),
		Now:            func() time.Time { return fixtureNow },
	}
}

// seed loads the fixture reports into the fake bucket, plus one sales file
// from a month outside the backfill window that must be ignored.
func seed(t *testing.T, f *fakeGCS) {
	t.Helper()
	f.put("sales/salesreport_202608.zip",
		zipOf(t, "salesreport_202608.csv", readFixture(t, "salesreport_202608.csv")))
	f.put("sales/salesreport_202601.zip",
		zipOf(t, "salesreport_202601.csv", readFixture(t, "salesreport_202608.csv")))
	f.put("stats/installs/installs_com.example.app_202608_overview.csv",
		readFixture(t, "installs_com.example.app_202608_overview.csv"))
	f.put("stats/installs/installs_com.example.app_202608_country.csv",
		readFixture(t, "installs_com.example.app_202608_country.csv"))
	// A dimension Loot does not read; downloading it would be wasted bytes.
	f.put("stats/installs/installs_com.example.app_202608_os_version.csv",
		readFixture(t, "installs_com.example.app_202608_country.csv"))
}

func byDedupe(events []core.Event) map[string]core.Event {
	m := make(map[string]core.Event, len(events))
	for _, ev := range events {
		m[ev.DedupeKey] = ev
	}
	return m
}

func TestPollSalesRows(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f, "com.example.app")

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	got := byDedupe(events)

	paid, ok := got["play:sale:GPA.0001-0001-0001-00001:com.example.app:charged"]
	if !ok {
		t.Fatalf("no event for the charged paid app; keys: %v", keys(got))
	}
	if paid.Kind != "sale" {
		t.Errorf("paid app kind = %q, want sale", paid.Kind)
	}
	if paid.Source != "googleplay" {
		t.Errorf("source = %q", paid.Source)
	}
	if paid.App != "Dungeon Ledger" {
		t.Errorf("app = %q, want the product title", paid.App)
	}
	if !paid.Silent || !paid.IsLedger || paid.Chest {
		t.Errorf("row flags: silent=%v ledger=%v chest=%v, want true/true/false",
			paid.Silent, paid.IsLedger, paid.Chest)
	}
	if paid.Day != "2026-08-15" || paid.Country != "US" || paid.Currency != "USD" {
		t.Errorf("day/country/currency = %s/%s/%s", paid.Day, paid.Country, paid.Currency)
	}
	if paid.Amount != 4.99 || paid.Quantity != 1 {
		t.Errorf("amount/quantity = %v/%d, want 4.99/1", paid.Amount, paid.Quantity)
	}
	if paid.OccurredAt.IsZero() {
		t.Error("occurred_at not set from the report timestamp")
	}

	iap := got["play:sale:GPA.0002-0002-0002-00002:coin_pack_large:charged"]
	if iap.Kind != "iap" || iap.Currency != "EUR" || iap.Country != "DE" || iap.Amount != 2.99 {
		t.Errorf("iap row = %+v", summarize(iap))
	}
	// That row carried an epoch-millisecond timestamp rather than a formatted
	// one, so a parsed OccurredAt proves both shapes are handled.
	if want := time.UnixMilli(1786804353000).UTC(); !iap.OccurredAt.Equal(want) {
		t.Errorf("iap occurred_at = %s, want %s", iap.OccurredAt, want)
	}

	sub := got["play:sale:GPA.0003-0003-0003-00003:pro_monthly:charged"]
	if sub.Kind != "subscription" || sub.Amount != 9.99 {
		t.Errorf("subscription row = %+v", summarize(sub))
	}

	refund := got["play:sale:GPA.0001-0001-0001-00001:com.example.app:refund"]
	if refund.Kind != "refund" {
		t.Errorf("refund kind = %q", refund.Kind)
	}
	if refund.Amount != -4.99 || refund.Quantity != -1 {
		t.Errorf("refund amount/quantity = %v/%d, want -4.99/-1", refund.Amount, refund.Quantity)
	}

	// The 16th has no timestamp column value at all; the charged date still
	// decides the business day.
	noStamp := got["play:sale:GPA.0004-0004-0004-00004:com.example.app:charged"]
	if noStamp.Day != "2026-08-16" || noStamp.Country != "FR" {
		t.Errorf("timestamp-less row day/country = %s/%s", noStamp.Day, noStamp.Country)
	}

	// The allowlist is by package, not by product title.
	if _, ok := got["play:sale:GPA.0006-0006-0006-00006:com.other.app:charged"]; ok {
		t.Error("com.other.app was ingested despite the packages allowlist")
	}
}

func TestPollSalesPayloadHasNoBuyerPII(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f)

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	// City, state and postal code are columns of the report and must never
	// reach the database.
	forbidden := []string{"Springfield", "62704", "Austin", "73301", "Köln", "50667", "Reno", "89501"}
	for _, ev := range events {
		payload := string(ev.Payload)
		for _, bad := range forbidden {
			if strings.Contains(payload, bad) {
				t.Fatalf("payload of %s leaks buyer PII %q: %s", ev.DedupeKey, bad, payload)
			}
		}
	}

	got := byDedupe(events)
	var payload map[string]any
	if err := json.Unmarshal(got["play:sale:GPA.0001-0001-0001-00001:com.example.app:charged"].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, want := range []string{"order_number", "package", "sku", "charged_amount", "country", "gross", "note"} {
		if _, ok := payload[want]; !ok {
			t.Errorf("payload has no %q field: %v", want, payload)
		}
	}
	if payload["gross"] != true {
		t.Error("payload does not flag the amount as gross")
	}
}

func TestPollSalesDaySummaries(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f, "com.example.app")

	events, rawState, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	got := byDedupe(events)

	// 2026-08-18 is "today" and 08-17 is yesterday: neither is settled, so the
	// 18th's row is stored but never summarized.
	if _, ok := got["play:sale:GPA.0005-0005-0005-00005:com.example.app:charged"]; !ok {
		t.Error("today's row was not stored")
	}
	if _, ok := got["play:sales_day:com.example.app:2026-08-18"]; ok {
		t.Error("today's day was summarized; the monthly report is still moving under it")
	}

	day, ok := got["play:sales_day:com.example.app:2026-08-15"]
	if !ok {
		t.Fatalf("no summary for 2026-08-15; keys: %v", keys(got))
	}
	if day.Kind != "sales_day" || !day.Chest || !day.IsLedger || day.Silent {
		t.Errorf("summary flags: kind=%s chest=%v ledger=%v silent=%v",
			day.Kind, day.Chest, day.IsLedger, day.Silent)
	}
	if day.Quantity != 3 {
		t.Errorf("units = %d, want 3 charged lines", day.Quantity)
	}
	// USD nets 4.99 + 9.99 − 4.99; the EUR sale is a second currency and does
	// not join the total.
	if day.Currency != "USD" || day.Amount != 9.99 {
		t.Errorf("proceeds = %v %s, want 9.99 USD", day.Amount, day.Currency)
	}

	var payload googleplay.SalesDayPayload
	if err := json.Unmarshal(day.Payload, &payload); err != nil {
		t.Fatalf("decode summary payload: %v", err)
	}
	if payload.Units != 3 || payload.Refunds != 1 {
		t.Errorf("units/refunds = %d/%d, want 3/1", payload.Units, payload.Refunds)
	}
	if !payload.ProceedsMixed {
		t.Error("proceeds_mixed not set on a day that took both USD and EUR")
	}
	if payload.ByCurrency["USD"] != 9.99 || payload.ByCurrency["EUR"] != 2.99 {
		t.Errorf("by_currency = %v", payload.ByCurrency)
	}
	if payload.Countries != 2 || payload.TopCountry != "US" {
		t.Errorf("countries/top = %d/%s, want 2/US", payload.Countries, payload.TopCountry)
	}
	if payload.Package != "com.example.app" {
		t.Errorf("package = %q", payload.Package)
	}
	if payload.IAPUnits != 1 || payload.SubUnits != 1 {
		t.Errorf("iap/sub units = %d/%d, want 1/1", payload.IAPUnits, payload.SubUnits)
	}
	if !payload.Gross || payload.Note == "" {
		t.Error("summary does not say the figure is gross")
	}
	if sku, ok := payload.BySKU["pro_monthly"]; !ok || sku.Proceeds != 9.99 {
		t.Errorf("by_sku = %v", payload.BySKU)
	}

	// The 16th is a euro-only day, so nothing is mixed about it.
	sixteenth := got["play:sales_day:com.example.app:2026-08-16"]
	if sixteenth.Currency != "EUR" || sixteenth.Amount != 4.49 || sixteenth.Quantity != 1 {
		t.Errorf("16th summary = %+v", summarize(sixteenth))
	}

	var st struct {
		SalesFiles     map[string]string `json:"sales_files"`
		SummarizedDays map[string]string `json:"summarized_days"`
		InstallsCursor map[string]string `json:"installs_cursor"`
	}
	if err := json.Unmarshal(rawState, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if st.SummarizedDays["com.example.app"] != "2026-08-16" {
		t.Errorf("summarized_days = %v, want the 16th", st.SummarizedDays)
	}
	if st.SalesFiles["sales/salesreport_202608.zip"] == "" {
		t.Errorf("sales_files did not record the month's md5: %v", st.SalesFiles)
	}
	if _, ok := st.SalesFiles["sales/salesreport_202601.zip"]; ok {
		t.Error("a month outside the backfill window was read")
	}
	if st.InstallsCursor["com.example.app"] != "2026-08-16" {
		t.Errorf("installs_cursor = %v, want the 16th", st.InstallsCursor)
	}
}

func TestPollInstalls(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f, "com.example.app")

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	got := byDedupe(events)

	installs, ok := got["play:installs:com.example.app:2026-08-15"]
	if !ok {
		t.Fatalf("no overview install event; keys: %v", keys(got))
	}
	if installs.Kind != "install" || !installs.Silent || installs.IsLedger {
		t.Errorf("install flags: kind=%s silent=%v ledger=%v",
			installs.Kind, installs.Silent, installs.IsLedger)
	}
	// Daily User Installs, not Daily Device Installs: a person with a phone
	// and a tablet is one install.
	if installs.Quantity != 110 {
		t.Errorf("installs = %d, want 110 daily user installs", installs.Quantity)
	}
	if installs.App != "com.example.app" || installs.Country != "" {
		t.Errorf("app/country = %s/%s", installs.App, installs.Country)
	}

	active := got["play:active:com.example.app:2026-08-15"]
	if active.Kind != "active_devices" || active.Quantity != 4100 || !active.Silent {
		t.Errorf("active devices = %+v", summarize(active))
	}

	day, ok := got["play:installs_day:com.example.app:2026-08-16"]
	if !ok {
		t.Fatal("no installs_day event")
	}
	if day.Kind != "installs_day" || day.Silent || !day.Chest || day.Quantity != 130 {
		t.Errorf("installs_day = %+v", summarize(day))
	}

	// Statistics are stated in UTC and today's row is still accumulating.
	if _, ok := got["play:installs_day:com.example.app:2026-08-18"]; ok {
		t.Error("today's partial install day was emitted")
	}

	us := got["play:installs:com.example.app:2026-08-15:US"]
	if us.Country != "US" || us.Quantity != 72 || !us.Silent {
		t.Errorf("US country row = %+v", summarize(us))
	}
	de := got["play:installs:com.example.app:2026-08-15:DE"]
	if de.Country != "DE" || de.Quantity != 38 {
		t.Errorf("DE country row = %+v", summarize(de))
	}
	// "ZZ" is Play's placeholder for an unplaceable install and must not
	// found a settlement.
	if _, ok := got["play:installs:com.example.app:2026-08-16:ZZ"]; ok {
		t.Error("the ZZ pseudo-country was ingested")
	}

	// Only the overview and country dimensions are worth downloading.
	if n := f.downloadCount("stats/installs/installs_com.example.app_202608_os_version.csv"); n != 0 {
		t.Errorf("os_version dimension downloaded %d times", n)
	}
}

func TestPollSkipsUnchangedFilesAndAdvancesCursor(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f, "com.example.app")

	first, state1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first poll produced nothing")
	}
	firstDownloads := f.totalDownloads()
	if firstDownloads != 3 {
		t.Errorf("first poll downloaded %d files, want 3 (sales, overview, country)", firstDownloads)
	}

	second, state2, err := src.Poll(context.Background(), state1)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second poll re-emitted %d events; md5s were unchanged", len(second))
	}
	if f.totalDownloads() != firstDownloads {
		t.Errorf("second poll downloaded %d more files; nothing had changed",
			f.totalDownloads()-firstDownloads)
	}

	var st struct {
		SummarizedDays map[string]string `json:"summarized_days"`
		InstallsCursor map[string]string `json:"installs_cursor"`
	}
	if err := json.Unmarshal(state2, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if st.SummarizedDays["com.example.app"] != "2026-08-16" ||
		st.InstallsCursor["com.example.app"] != "2026-08-16" {
		t.Errorf("cursors moved on an empty poll: %+v", st)
	}
}

func TestLateRowDoesNotResummarizeItsDay(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f, "com.example.app")

	_, state1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}

	// Play rewrites the month in place. A row for the already-summarized 15th
	// turns up a day late — which happens — and the file's md5 changes.
	restated := append(append([]byte{}, readFixture(t, "salesreport_202608.csv")...),
		lastLine(readFixture(t, "salesreport_late.csv"))...)
	f.put("sales/salesreport_202608.zip", zipOf(t, "salesreport_202608.csv", restated))

	events, _, err := src.Poll(context.Background(), state1)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	got := byDedupe(events)

	if _, ok := got["play:sale:GPA.0007-0007-0007-00007:coin_pack_small:charged"]; !ok {
		t.Fatalf("the late row was dropped; keys: %v", keys(got))
	}
	if _, ok := got["play:sales_day:com.example.app:2026-08-15"]; ok {
		t.Error("the 15th was summarized a second time; the vault would double count the day")
	}
}

func TestCheck(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f)

	if err := src.Check(context.Background()); err != nil {
		t.Fatalf("check on a healthy bucket: %v", err)
	}

	f.mu.Lock()
	f.status = http.StatusForbidden
	f.mu.Unlock()
	err := src.Check(context.Background())
	if err == nil {
		t.Fatal("a 403 was reported as healthy")
	}
	// The failure a first-time user hits is a missing Play Console grant, so
	// the error has to say where to fix it.
	for _, want := range []string{"Users and permissions", "View financial data"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("403 error does not mention %q: %v", want, err)
		}
	}

	f.mu.Lock()
	f.status = http.StatusNotFound
	f.mu.Unlock()
	err = src.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Copy Cloud Storage URI") {
		t.Errorf("404 error does not explain where the bucket id comes from: %v", err)
	}
}

func TestCheckWithoutCredentialsFails(t *testing.T) {
	f := newFakeGCS(t)
	seed(t, f)
	src := newSource(f)
	src.Tokens = nil

	if err := src.Check(context.Background()); err == nil {
		t.Fatal("an unauthenticated request was reported as healthy")
	}
}

func TestSourceContract(t *testing.T) {
	f := newFakeGCS(t)
	src := newSource(f)
	if src.Name() != "googleplay" {
		t.Errorf("name = %q", src.Name())
	}
	if src.PollInterval() != 6*time.Hour {
		t.Errorf("poll interval = %v, want 6h", src.PollInterval())
	}
}

func TestNormalizeBucket(t *testing.T) {
	for input, want := range map[string]string{
		"pubsite_prod_rev_01234567890":                                      "pubsite_prod_rev_01234567890",
		"gs://pubsite_prod_rev_01234567890":                                 "pubsite_prod_rev_01234567890",
		"gs://pubsite_prod_rev_01234567890/":                                "pubsite_prod_rev_01234567890",
		"  gs://pubsite_prod_rev_01234567890 ":                              "pubsite_prod_rev_01234567890",
		"gs://pubsite_prod_rev_01234567890/ear":                             "pubsite_prod_rev_01234567890",
		"https://storage.googleapis.com/pubsite_prod_rev_01234567890/sales": "pubsite_prod_rev_01234567890",
	} {
		if got := googleplay.NormalizeBucket(input); got != want {
			t.Errorf("NormalizeBucket(%q) = %q, want %q", input, got, want)
		}
	}
}

// lastLine returns the final non-empty line of a CSV fixture, with its newline.
func lastLine(b []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return []byte(lines[len(lines)-1] + "\n")
}

func keys(m map[string]core.Event) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// summarize renders the parts of an event a failure message needs.
func summarize(ev core.Event) string {
	return fmt.Sprintf("kind=%s app=%s day=%s country=%s amount=%v %s qty=%d silent=%v chest=%v",
		ev.Kind, ev.App, ev.Day, ev.Country, ev.Amount, ev.Currency, ev.Quantity, ev.Silent, ev.Chest)
}
