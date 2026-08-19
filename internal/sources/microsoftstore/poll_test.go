package microsoftstore_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/microsoftstore"
)

// The whole suite runs against a pinned clock: 2026-08-18 UTC. With a three
// day settlement lag that makes 2026-08-15 the newest readable day, so the
// fixtures' 16th and 17th are deliberately still in flight.
var testNow = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

const (
	testStoreID = "9NBLGGH4R315"
	testTenant  = "11111111-2222-3333-4444-555555555555"
	settledDay  = "2026-08-15"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakePartner stands in for Microsoft Entra and Partner Center: it mints
// tokens, serves the fixtures, filters them by the requested app and date
// range the way the real API does, and pages with @nextLink.
type fakePartner struct {
	mu sync.Mutex

	rows map[string][]json.RawMessage

	// pageSize, when non-zero, forces pagination regardless of `top`.
	pageSize int

	// tokenStatus/tokenBody override the token response.
	tokenStatus int
	tokenBody   string

	// apiStatus/apiBody override every analytics response.
	apiStatus int
	apiBody   string

	// notFound forces a 404 for one (path, applicationId) pair, which is how
	// Partner Center answers both "no data" and "no such app".
	notFound map[string]bool

	tokenForms []url.Values
	requests   []*url.URL
	auth       []string
}

func newFake(t *testing.T) *fakePartner {
	t.Helper()
	return &fakePartner{rows: map[string][]json.RawMessage{
		"/v1.0/my/analytics/appacquisitions":   fixture(t, "appacquisitions.json"),
		"/v1.0/my/analytics/inappacquisitions": fixture(t, "inappacquisitions.json"),
		"/v1.0/my/analytics/subscriptions":     fixture(t, "subscriptions.json"),
		"/v1.0/my/applications":                fixture(t, "applications.json"),
	}}
}

// fixture reads one testdata file and returns its rows, accepting the
// analytics API's "Value" and the applications list's "value".
func fixture(t *testing.T, name string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var envelope struct {
		Value      []json.RawMessage `json:"Value"`
		ValueLower []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	if len(envelope.Value) > 0 {
		return envelope.Value
	}
	return envelope.ValueLower
}

func (f *fakePartner) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			_ = r.ParseForm()
			f.tokenForms = append(f.tokenForms, r.PostForm)
			if f.tokenStatus != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.tokenStatus)
				_, _ = io.WriteString(w, f.tokenBody)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// expires_in arrives as a *string* from the v1 endpoint.
			_, _ = io.WriteString(w, `{"token_type":"Bearer","expires_in":"3599",`+
				`"resource":"https://manage.devcenter.microsoft.com","access_token":"tok-1"}`)
			return
		}

		f.requests = append(f.requests, r.URL)
		f.auth = append(f.auth, r.Header.Get("Authorization"))

		if f.apiStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.apiStatus)
			_, _ = io.WriteString(w, f.apiBody)
			return
		}

		rows, ok := f.rows[r.URL.Path]
		if !ok || f.notFound[r.URL.Path+"|"+r.URL.Query().Get("applicationId")] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"NotFound","message":"resource not found"}`)
			return
		}
		f.serveRows(w, r, filterRows(rows, r.URL.Query()))
	})
}

// filterRows applies the applicationId and date range filters the real
// endpoints apply, so a test can prove which days Loot actually asked for.
func filterRows(rows []json.RawMessage, q url.Values) []json.RawMessage {
	app, from, to := q.Get("applicationId"), q.Get("startDate"), q.Get("endDate")

	out := make([]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		var row struct {
			ApplicationID string `json:"applicationId"`
			Date          string `json:"date"`
		}
		_ = json.Unmarshal(raw, &row)
		if app != "" && row.ApplicationID != "" && row.ApplicationID != app {
			continue
		}
		if row.Date != "" {
			if from != "" && row.Date < from {
				continue
			}
			if to != "" && row.Date > to {
				continue
			}
		}
		out = append(out, raw)
	}
	return out
}

func (f *fakePartner) serveRows(w http.ResponseWriter, r *http.Request, rows []json.RawMessage) {
	q := r.URL.Query()
	skip, _ := strconv.Atoi(q.Get("skip"))
	top, _ := strconv.Atoi(q.Get("top"))
	if top <= 0 {
		top = 10000
	}
	if f.pageSize > 0 && f.pageSize < top {
		top = f.pageSize
	}
	if skip > len(rows) {
		skip = len(rows)
	}
	end := min(skip+top, len(rows))

	out := map[string]any{"Value": rows[skip:end], "TotalCount": len(rows)}
	if end < len(rows) {
		next := q
		next.Set("skip", strconv.Itoa(end))
		// The analytics API answers with an absolute URI; the applications
		// list answers with a path relative to /v1.0/my/. Exercise both.
		if r.URL.Path == "/v1.0/my/applications" {
			out["@nextLink"] = "applications?" + next.Encode()
		} else {
			out["@nextLink"] = "https://manage.devcenter.microsoft.com" + r.URL.Path + "?" + next.Encode()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakePartner) tokenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokenForms)
}

func (f *fakePartner) asked(path string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []url.Values
	for _, u := range f.requests {
		if u.Path == path {
			out = append(out, u.Query())
		}
	}
	return out
}

// newTestSource wires a source at the fake for both its token host and its API
// host, with the clock pinned.
func newTestSource(t *testing.T, fake *fakePartner, apps []string) *microsoftstore.Source {
	t.Helper()

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	src, err := microsoftstore.New(config.MicrosoftStore{
		TenantID:     testTenant,
		ClientID:     "client-abc",
		ClientSecret: "shhh",
		Apps:         apps,
		BackfillDays: 30,
	}, quietLogger())
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	src.BaseURL = srv.URL
	src.LoginURL = srv.URL
	src.Client = srv.Client()
	src.Now = func() time.Time { return testNow }
	return src
}

func eventsByKey(events []core.Event) map[string]core.Event {
	out := make(map[string]core.Event, len(events))
	for _, ev := range events {
		out[ev.DedupeKey] = ev
	}
	return out
}

func dedupeKeys(events []core.Event) []string {
	keys := make([]string, 0, len(events))
	for _, ev := range events {
		keys = append(keys, ev.DedupeKey)
	}
	sort.Strings(keys)
	return keys
}

func TestPollTokenExchange(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("poll returned no events")
	}
	if len(state) == 0 {
		t.Fatal("poll returned no state")
	}

	if got := fake.tokenCount(); got != 1 {
		t.Fatalf("token requests = %d, want 1 (the token must be cached)", got)
	}
	form := fake.tokenForms[0]
	for field, want := range map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     "client-abc",
		"client_secret": "shhh",
		"resource":      "https://manage.devcenter.microsoft.com",
	} {
		if got := form.Get(field); got != want {
			t.Errorf("token form %s = %q, want %q", field, got, want)
		}
	}

	fake.mu.Lock()
	auth := append([]string(nil), fake.auth...)
	fake.mu.Unlock()
	if len(auth) == 0 {
		t.Fatal("no API requests were made")
	}
	for _, header := range auth {
		if header != "Bearer tok-1" {
			t.Fatalf("Authorization = %q, want %q", header, "Bearer tok-1")
		}
	}

	// A second poll on the same source reuses the cached token.
	if _, _, err := src.Poll(context.Background(), state); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if got := fake.tokenCount(); got != 1 {
		t.Fatalf("token requests after two polls = %d, want 1", got)
	}
}

func TestPollPagination(t *testing.T) {
	fake := newFake(t)
	fake.pageSize = 1 // one row per page, so every @nextLink is followed
	src := newTestSource(t, fake, nil)

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	// Five settled app rows plus two add-on rows are in range; grouping folds
	// them onto five keys, so pagination must not lose or duplicate any.
	byKey := eventsByKey(events)
	for _, key := range []string{
		"msstore:acq:2026-08-14:9NBLGGH4R315:US:paid:USD",
		"msstore:acq:2026-08-14:9NBLGGH4R315:DE:paid:EUR",
		"msstore:acq:2026-08-14:9NBLGGH4R315:BR:free:",
		"msstore:iap:2026-08-14:9NBLGGH4R315:US:iap:USD:9NBLGGH4R316",
	} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("paginated poll lost %s", key)
		}
	}
	if got := byKey["msstore:acq:2026-08-14:9NBLGGH4R315:US:paid:USD"].Quantity; got != 4 {
		t.Errorf("US paid quantity = %d, want 4", got)
	}
	// Discovery paged too: the applications list was consulted (no apps were
	// configured) and produced the app name.
	if got := byKey["msstore:acq:2026-08-14:9NBLGGH4R315:US:paid:USD"].App; got != "Tide Clock" {
		t.Errorf("app = %q, want Tide Clock", got)
	}
}

func TestPollRowMapping(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	byKey := eventsByKey(events)

	tests := []struct {
		name     string
		key      string
		kind     string
		quantity int
		amount   float64
		currency string
		country  string
		day      string
	}{
		{
			name: "paid rows merge across device types", key: "msstore:acq:2026-08-14:9NBLGGH4R315:US:paid:USD",
			kind: "sale", quantity: 4, amount: 11.96, currency: "USD", country: "US", day: "2026-08-14",
		},
		{
			name: "a local currency row keeps its own currency", key: "msstore:acq:2026-08-14:9NBLGGH4R315:DE:paid:EUR",
			kind: "sale", quantity: 2, amount: 5.98, currency: "EUR", country: "DE", day: "2026-08-14",
		},
		{
			name: "free is a download worth nothing", key: "msstore:acq:2026-08-14:9NBLGGH4R315:BR:free:",
			kind: "download", quantity: 12, amount: 0, currency: "", country: "BR", day: "2026-08-14",
		},
		{
			name: "trial is a download too", key: "msstore:acq:2026-08-15:9NBLGGH4R315:US:trial:",
			kind: "download", quantity: 5, amount: 0, currency: "", country: "US", day: "2026-08-15",
		},
		{
			name: "add-ons are iap", key: "msstore:iap:2026-08-14:9NBLGGH4R315:US:iap:USD:9NBLGGH4R316",
			kind: "iap", quantity: 3, amount: 11.97, currency: "USD", country: "US", day: "2026-08-14",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := byKey[tc.key]
			if !ok {
				t.Fatalf("no event with dedupe key %s (got %v)", tc.key, dedupeKeys(events))
			}
			if ev.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", ev.Kind, tc.kind)
			}
			if ev.Quantity != tc.quantity {
				t.Errorf("quantity = %d, want %d", ev.Quantity, tc.quantity)
			}
			if ev.Amount != tc.amount {
				t.Errorf("amount = %v, want %v", ev.Amount, tc.amount)
			}
			if ev.Currency != tc.currency {
				t.Errorf("currency = %q, want %q", ev.Currency, tc.currency)
			}
			if ev.Country != tc.country {
				t.Errorf("country = %q, want %q", ev.Country, tc.country)
			}
			if ev.Day != tc.day {
				t.Errorf("day = %q, want %q", ev.Day, tc.day)
			}
			if !ev.IsLedger || !ev.Silent || ev.Chest {
				t.Errorf("row event flags: ledger=%v silent=%v chest=%v, want true/true/false",
					ev.IsLedger, ev.Silent, ev.Chest)
			}
			if ev.Source != microsoftstore.Name {
				t.Errorf("source = %q, want %q", ev.Source, microsoftstore.Name)
			}
			if ev.App != "Tide Clock" {
				t.Errorf("app = %q, want Tide Clock", ev.App)
			}
		})
	}

	// The store id travels in the payload, not in the app name.
	var payload struct {
		StoreID string `json:"store_id"`
		Gross   bool   `json:"gross"`
		Rows    int    `json:"rows"`
	}
	if err := json.Unmarshal(byKey["msstore:acq:2026-08-14:9NBLGGH4R315:US:paid:USD"].Payload, &payload); err != nil {
		t.Fatalf("decode row payload: %v", err)
	}
	if payload.StoreID != testStoreID {
		t.Errorf("payload store_id = %q, want %q", payload.StoreID, testStoreID)
	}
	if !payload.Gross {
		t.Error("payload gross = false, want true (these are customer prices)")
	}
	if payload.Rows != 2 {
		t.Errorf("payload rows = %d, want 2 (two API rows were folded)", payload.Rows)
	}
}

func TestPollSettledDaysOnly(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	events, raw, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	for _, q := range fake.asked("/v1.0/my/analytics/appacquisitions") {
		if got := q.Get("endDate"); got != settledDay {
			t.Errorf("endDate = %q, want %q (three days of settlement lag)", got, settledDay)
		}
		if got := q.Get("startDate"); got != "2026-07-19" {
			t.Errorf("startDate = %q, want 2026-07-19 (30 days of backfill)", got)
		}
		if got := q.Get("aggregationLevel"); got != "day" {
			t.Errorf("aggregationLevel = %q, want day", got)
		}
		if !strings.Contains(q.Get("groupby"), "market") {
			t.Errorf("groupby = %q, want it to include market", q.Get("groupby"))
		}
	}

	for _, ev := range events {
		if ev.Day > settledDay {
			t.Errorf("event for unsettled day %s (%s) must not be emitted", ev.Day, ev.DedupeKey)
		}
	}

	var state struct {
		LastSettledDay map[string]string `json:"last_settled_day"`
		Seeded         bool              `json:"seeded"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !state.Seeded {
		t.Error("state is not seeded after a poll")
	}
	if got := state.LastSettledDay[testStoreID]; got != settledDay {
		t.Errorf("last_settled_day = %q, want %q", got, settledDay)
	}
}

func TestPollIsIdempotent(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	first, state1, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	second, state2, err := src.Poll(context.Background(), state1)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}

	if string(state1) != string(state2) {
		t.Errorf("state changed on an unchanged second poll:\n first: %s\nsecond: %s", state1, state2)
	}

	// The second poll re-sweeps the last few settled days, so it may emit
	// again — but every key it emits must be one the first poll already used,
	// which is what makes the pipeline's dedupe swallow the whole thing.
	known := eventsByKey(first)
	if len(second) == 0 {
		t.Fatal("the re-sweep asked for nothing at all")
	}
	for _, ev := range second {
		prev, ok := known[ev.DedupeKey]
		if !ok {
			t.Fatalf("second poll invented a new dedupe key %q", ev.DedupeKey)
		}
		if prev.Amount != ev.Amount || prev.Quantity != ev.Quantity || prev.Kind != ev.Kind {
			t.Errorf("%s changed between polls: %v/%d/%s -> %v/%d/%s", ev.DedupeKey,
				prev.Amount, prev.Quantity, prev.Kind, ev.Amount, ev.Quantity, ev.Kind)
		}
	}

	// The re-sweep starts three days before the settlement horizon, not at the
	// backfill floor: a settled day is read again only briefly.
	windows := fake.asked("/v1.0/my/analytics/appacquisitions")
	if len(windows) < 2 {
		t.Fatalf("expected a request per poll, got %d", len(windows))
	}
	if got := windows[len(windows)-1].Get("startDate"); got != "2026-08-13" {
		t.Errorf("re-sweep startDate = %q, want 2026-08-13", got)
	}
}

func TestPollSummary(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	summary, ok := eventsByKey(events)["microsoftstore:sales_day:9NBLGGH4R315:2026-08-14"]
	if !ok {
		t.Fatalf("no sales_day summary for 2026-08-14 (got %v)", dedupeKeys(events))
	}
	if summary.Kind != "sales_day" || !summary.Chest || summary.Silent || !summary.IsLedger {
		t.Fatalf("summary flags: kind=%s chest=%v silent=%v ledger=%v", summary.Kind,
			summary.Chest, summary.Silent, summary.IsLedger)
	}
	if summary.Country != "" {
		t.Errorf("summary country = %q, want empty (a day spans countries)", summary.Country)
	}
	// 6 paid app units (4 in the US, 2 in Germany) + 3 add-on units.
	if summary.Quantity != 9 {
		t.Errorf("summary quantity = %d, want 9", summary.Quantity)
	}
	// USD carries more than EUR, so USD is the headline: 11.96 + 11.97.
	if summary.Currency != "USD" || summary.Amount != 23.93 {
		t.Errorf("summary headline = %v %s, want 23.93 USD", summary.Amount, summary.Currency)
	}

	var payload struct {
		core.SalesDaySummary
		StoreID       string             `json:"store_id"`
		Downloads     int                `json:"downloads"`
		IAPUnits      int                `json:"iap_units"`
		Gross         bool               `json:"gross"`
		ProceedsMixed bool               `json:"proceeds_mixed"`
		ByCurrency    map[string]float64 `json:"by_currency"`
		ByType        map[string]int     `json:"by_acquisition_type"`
		ByCountry     map[string]int     `json:"by_country"`
	}
	if err := json.Unmarshal(summary.Payload, &payload); err != nil {
		t.Fatalf("decode summary payload: %v", err)
	}
	if payload.Units != 9 || payload.Refunds != 0 {
		t.Errorf("units/refunds = %d/%d, want 9/0", payload.Units, payload.Refunds)
	}
	if payload.Downloads != 12 {
		t.Errorf("downloads = %d, want 12", payload.Downloads)
	}
	if payload.IAPUnits != 3 {
		t.Errorf("iap units = %d, want 3", payload.IAPUnits)
	}
	if payload.Countries != 3 || payload.TopCountry != "US" {
		t.Errorf("countries/top = %d/%s, want 3/US", payload.Countries, payload.TopCountry)
	}
	if !payload.Gross || !payload.ProceedsMixed {
		t.Errorf("gross/proceeds_mixed = %v/%v, want true/true", payload.Gross, payload.ProceedsMixed)
	}
	if got := payload.ByCurrency["EUR"]; got != 5.98 {
		t.Errorf("by_currency[EUR] = %v, want 5.98", got)
	}
	if got := payload.ByCountry["BR"]; got != 12 {
		t.Errorf("by_country[BR] = %v, want 12", got)
	}
	if got := payload.ByType["promotional-code"]; got != 0 {
		t.Errorf("by_acquisition_type has an unexpected bucket: %v", payload.ByType)
	}
	if got := payload.BySKU["Pro Pack"]; got.Units != 3 || got.Proceeds != 11.97 {
		t.Errorf("by_sku[Pro Pack] = %+v, want 3 units / 11.97", got)
	}
	if payload.StoreID != testStoreID {
		t.Errorf("store_id = %q, want %q", payload.StoreID, testStoreID)
	}
}

func TestPollSubscriptionSnapshot(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	events, _, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	snap, ok := eventsByKey(events)["msstore:subs:2026-08-15:9NBLGGH4R315"]
	if !ok {
		t.Fatalf("no subscription snapshot (got %v)", dedupeKeys(events))
	}
	if snap.Kind != "subscription_snapshot" {
		t.Errorf("kind = %q", snap.Kind)
	}
	if snap.Quantity != 412 {
		t.Errorf("active = %d, want 412", snap.Quantity)
	}
	if snap.IsLedger {
		t.Error("a subscriber count is not money and must not be a ledger event")
	}
	if !snap.Silent {
		t.Error("a snapshot must be silent")
	}
	if snap.Amount != 0 {
		t.Errorf("amount = %v, want 0: the same money already arrived as an iap row", snap.Amount)
	}
}

func TestPollSkipsUnsettledOnlyAccount(t *testing.T) {
	// An account whose cursor is already at the settlement horizon asks for
	// the re-sweep window and nothing older.
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	state, err := json.Marshal(map[string]any{
		"last_settled_day": map[string]string{testStoreID: settledDay},
		"seeded":           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Poll(context.Background(), state); err != nil {
		t.Fatalf("poll: %v", err)
	}
	asked := fake.asked("/v1.0/my/analytics/appacquisitions")
	if len(asked) != 1 {
		t.Fatalf("requests = %d, want 1", len(asked))
	}
	if got := asked[0].Get("startDate"); got != "2026-08-13" {
		t.Errorf("startDate = %q, want 2026-08-13", got)
	}
}

func TestCheck(t *testing.T) {
	t.Run("configured apps", func(t *testing.T) {
		fake := newFake(t)
		src := newTestSource(t, fake, []string{testStoreID})
		if err := src.Check(context.Background()); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	t.Run("discovers apps when none configured", func(t *testing.T) {
		fake := newFake(t)
		src := newTestSource(t, fake, nil)
		if err := src.Check(context.Background()); err != nil {
			t.Fatalf("check: %v", err)
		}
		if len(fake.asked("/v1.0/my/applications")) == 0 {
			t.Error("check did not list the applications")
		}
	})

	t.Run("a bad secret explains itself", func(t *testing.T) {
		fake := newFake(t)
		fake.tokenStatus = http.StatusBadRequest
		fake.tokenBody = `{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. ` +
			"\\r\\nTrace ID: 1234\\r\\nCorrelation ID: 5678\",\"error_codes\":[7000215]}"
		src := newTestSource(t, fake, []string{testStoreID})

		err := src.Check(context.Background())
		if err == nil {
			t.Fatal("check passed with an invalid secret")
		}
		msg := err.Error()
		for _, want := range []string{"AADSTS7000215", "client secret"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
		if strings.Contains(msg, "Trace ID") {
			t.Errorf("error keeps Entra's trace noise: %q", msg)
		}
	})

	t.Run("an unassociated application explains itself", func(t *testing.T) {
		fake := newFake(t)
		fake.apiStatus = http.StatusForbidden
		fake.apiBody = `{"code":"Forbidden","message":"The caller is not authorized to access this resource."}`
		src := newTestSource(t, fake, []string{testStoreID})

		err := src.Check(context.Background())
		if err == nil {
			t.Fatal("check passed on a 403")
		}
		msg := err.Error()
		for _, want := range []string{"Partner Center", "Azure AD applications", "Manager"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("an empty day is not a failure", func(t *testing.T) {
		fake := newFake(t)
		fake.apiStatus = http.StatusNotFound
		fake.apiBody = `{"code":"NotFound","message":"no data"}`
		src := newTestSource(t, fake, []string{testStoreID})

		if err := src.Check(context.Background()); err != nil {
			t.Fatalf("a quiet day should pass, got %v", err)
		}
	})

	t.Run("bad credentials are refused", func(t *testing.T) {
		fake := newFake(t)
		fake.apiStatus = http.StatusUnauthorized
		fake.apiBody = `{"code":"Unauthorized","message":"token expired"}`
		src := newTestSource(t, fake, []string{testStoreID})

		err := src.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "tenant_id") {
			t.Fatalf("error = %v, want a message naming the config fields", err)
		}
	})
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := microsoftstore.New(config.MicrosoftStore{TenantID: "t"}, quietLogger()); err == nil {
		t.Fatal("New accepted a half-configured section")
	}
	src, err := microsoftstore.New(config.MicrosoftStore{
		TenantID: "t", ClientID: "c", ClientSecret: "s",
	}, quietLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if src.Name() != "microsoftstore" {
		t.Errorf("name = %q", src.Name())
	}
	if src.PollInterval() != 6*time.Hour {
		t.Errorf("poll interval = %v, want 6h", src.PollInterval())
	}
	if src.BackfillDays != 30 {
		t.Errorf("backfill days = %d, want the default 30", src.BackfillDays)
	}
}

func TestPollHonoursSince(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})
	src.Since = "2026-08-10"

	if _, _, err := src.Poll(context.Background(), nil); err != nil {
		t.Fatalf("poll: %v", err)
	}
	asked := fake.asked("/v1.0/my/analytics/appacquisitions")
	if len(asked) == 0 {
		t.Fatal("no acquisitions request")
	}
	if got := asked[0].Get("startDate"); got != "2026-08-10" {
		t.Errorf("startDate = %q, want the --since date 2026-08-10", got)
	}
}

// Partner Center answers "no data for this query" and "no such app" with the
// same 404. Treating that as an empty window on the *app acquisitions* call
// advanced the cursor over days Loot never read, so a mistyped Store ID or a
// lapsed association read as a very quiet month instead of as a problem.
func TestPollAppAcquisitions404DoesNotAdvanceTheCursor(t *testing.T) {
	fake := newFake(t)
	fake.notFound = map[string]bool{"/v1.0/my/analytics/appacquisitions|" + testStoreID: true}
	src := newTestSource(t, fake, []string{testStoreID})

	events, state, err := src.Poll(context.Background(), nil)
	if err == nil {
		t.Fatal("a 404 on app acquisitions was swallowed; last_error would stay clean")
	}
	if len(events) != 0 {
		t.Errorf("events = %v, want none from a failed window", dedupeKeys(events))
	}

	var st struct {
		LastSettledDay map[string]string `json:"last_settled_day"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got := st.LastSettledDay[testStoreID]; got != "" {
		t.Errorf("cursor advanced to %q despite the fetch failing", got)
	}
}

// An add-on 404 is normal — most apps have none — and must not stop the app's
// own acquisitions landing.
func TestPollAddOn404IsTolerated(t *testing.T) {
	fake := newFake(t)
	fake.notFound = map[string]bool{"/v1.0/my/analytics/inappacquisitions|" + testStoreID: true}
	src := newTestSource(t, fake, []string{testStoreID})

	events, state, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("an app with no add-ons is not a failure: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the app's own acquisitions were lost with the add-on 404")
	}

	var st struct {
		LastSettledDay map[string]string `json:"last_settled_day"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got := st.LastSettledDay[testStoreID]; got != settledDay {
		t.Errorf("cursor = %q, want %q", got, settledDay)
	}
}

// One app failing must not stop the others, and must not move their cursors.
func TestPollOneFailedAppLetsTheOthersProgress(t *testing.T) {
	const otherID = "9NBLGGH4R999"
	fake := newFake(t)
	fake.notFound = map[string]bool{"/v1.0/my/analytics/appacquisitions|" + otherID: true}
	src := newTestSource(t, fake, []string{testStoreID, otherID})

	events, state, err := src.Poll(context.Background(), nil)
	if err == nil {
		t.Fatal("the failing app's error was not surfaced")
	}
	if len(events) == 0 {
		t.Fatal("the healthy app produced nothing")
	}

	var st struct {
		LastSettledDay map[string]string `json:"last_settled_day"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if st.LastSettledDay[testStoreID] != settledDay {
		t.Errorf("the healthy app's cursor = %q, want %q", st.LastSettledDay[testStoreID], settledDay)
	}
	if got := st.LastSettledDay[otherID]; got != "" {
		t.Errorf("the failing app's cursor advanced to %q", got)
	}
}

// The subscription backoff is per app and re-arms properly: an app with no
// subscription add-ons is switched off after maxSubsUnavailable empty answers
// and stamped with the day it happened, while an app that has them keeps being
// asked.
func TestSubscriptionBackoffIsPerAppAndStamped(t *testing.T) {
	const quietID = "9NBLGGH4R999"
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID, quietID})

	var state []byte
	for i := 0; i < 3; i++ {
		var err error
		if _, state, err = src.Poll(context.Background(), state); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}

	var st struct {
		SubsDay         map[string]string `json:"subs_day"`
		SubsUnavailable map[string]int    `json:"subs_unavailable"`
		SubsDisabled    map[string]string `json:"subs_disabled_day"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	if st.SubsUnavailable[quietID] < 3 {
		t.Errorf("quiet app streak = %d, want at least 3", st.SubsUnavailable[quietID])
	}
	if st.SubsDisabled[quietID] != settledDay {
		t.Errorf("quiet app was never stamped as disabled (got %q); it would be asked forever",
			st.SubsDisabled[quietID])
	}
	if st.SubsUnavailable[testStoreID] != 0 || st.SubsDisabled[testStoreID] != "" {
		t.Errorf("the app that *does* report subscriptions was penalised: %d / %q",
			st.SubsUnavailable[testStoreID], st.SubsDisabled[testStoreID])
	}
	if st.SubsDay[testStoreID] != settledDay {
		t.Errorf("subs_day = %q, want %q", st.SubsDay[testStoreID], settledDay)
	}

	// Now that the quiet app is stamped, it is left alone entirely.
	before := len(fake.asked("/v1.0/my/analytics/subscriptions"))
	if _, _, err := src.Poll(context.Background(), state); err != nil {
		t.Fatalf("fourth poll: %v", err)
	}
	after := fake.asked("/v1.0/my/analytics/subscriptions")
	for _, q := range after[before:] {
		if q.Get("applicationId") == quietID {
			t.Error("a disabled app was asked for subscriptions again inside the recheck window")
		}
	}
}

// After a gap — an outage, a restart, a missed poll — an app that has reported
// subscriptions before catches up on the settled days it missed rather than
// only taking the newest one.
func TestSubscriptionsCatchUpOnMissedDays(t *testing.T) {
	fake := newFake(t)
	src := newTestSource(t, fake, []string{testStoreID})

	state, err := json.Marshal(map[string]any{
		"last_settled_day": map[string]string{testStoreID: settledDay},
		"subs_day":         map[string]string{testStoreID: "2026-08-12"},
		"seeded":           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Poll(context.Background(), state); err != nil {
		t.Fatalf("poll: %v", err)
	}

	var days []string
	for _, q := range fake.asked("/v1.0/my/analytics/subscriptions") {
		days = append(days, q.Get("startDate"))
	}
	want := []string{"2026-08-13", "2026-08-14", "2026-08-15"}
	if len(days) != len(want) {
		t.Fatalf("asked for %v, want %v", days, want)
	}
	for i := range want {
		if days[i] != want[i] {
			t.Fatalf("asked for %v, want %v", days, want)
		}
	}
}
