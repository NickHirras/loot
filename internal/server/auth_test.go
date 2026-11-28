package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/store"
)

func newTestServerWithConfig(t *testing.T, cfg config.Config) (*server.Server, *store.Store, *pipeline.Pipeline) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	engine, err := rules.Load("", st)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := bus.New(16)
	pipe := pipeline.New(st, engine, b, log)
	pipe.DisplayCurrency = "USD"
	static := fstest.MapFS{"index.html": {Data: []byte("<html>ok</html>")}}

	srv := server.New(cfg, st, b, pipe, nil, static, log)
	srv.Version = "test-1.0"
	srv.Rules = engine
	return srv, st, pipe
}

func TestAPIAuthReadAndWriteTokens(t *testing.T) {
	cfg := config.Default()
	cfg.API.ReadTokens = []string{"read-secret-123"}
	cfg.API.WriteTokens = []string{"write-secret-456"}

	srv, _, _ := newTestServerWithConfig(t, cfg)
	h := srv.Handler()

	// Health check and SPA stay open even when API tokens are configured.
	for _, path := range []string{"/api/healthz", "/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s without token = %d, want 200", path, rec.Code)
		}
	}

	// Unauthenticated API read is rejected with 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/stats without token = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing WWW-Authenticate header: %q", rec.Header().Get("WWW-Authenticate"))
	}

	// Wrong token is rejected with 401.
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/stats with wrong token = %d, want 401", rec.Code)
	}

	// Read token works via Authorization: Bearer and via X-API-Key.
	for _, tc := range []struct {
		header, val string
	}{
		{"Authorization", "Bearer read-secret-123"},
		{"X-API-Key", "read-secret-123"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/report", nil)
		req.Header.Set(tc.header, tc.val)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/report with %s = %d, want 200 (%s)", tc.header, rec.Code, rec.Body.String())
		}
	}

	// Read token is forbidden on mutating endpoints.
	req = httptest.NewRequest(http.MethodPost, "/api/chest/open", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer read-secret-123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/chest/open with read token = %d, want 403", rec.Code)
	}

	// Write token can both read and write.
	req = httptest.NewRequest(http.MethodPost, "/api/chest/open", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer write-secret-456")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/chest/open with write token = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestReportEndpoint(t *testing.T) {
	cfg := config.Default()
	srv, _, pipe := newTestServerWithConfig(t, cfg)
	now := time.Now().UTC()
	today := core.DayOf(now)

	_, err := pipe.Ingest(context.Background(), core.Event{
		ID:         core.NewID(),
		Source:     "appstore",
		Kind:       "sale",
		App:        "Nistis",
		Product:    "Nistis",
		OccurredAt: now,
		ObservedAt: now,
		Day:        today,
		Country:    "US",
		Amount:     9.99,
		AmountBase: 9.99,
		Currency:   "USD",
		Quantity:   2,
		DedupeKey:  "test:sale:1",
		IsLedger:   true,
		Silent:     true,
	})
	if err != nil {
		t.Fatalf("ingest sale: %v", err)
	}

	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report?range=30d&app=Nistis", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/report = %d: %s", rec.Code, rec.Body.String())
	}

	var rep server.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if rep.App != "Nistis" {
		t.Errorf("app = %q, want Nistis", rep.App)
	}
	if rep.DisplayCurrency != "USD" {
		t.Errorf("currency = %q, want USD", rep.DisplayCurrency)
	}
	if rep.Vault.Totals.RevenueBase != 9.99 || rep.Vault.Totals.Units != 2 {
		t.Errorf("vault totals = %+v, want revenue 9.99 units 2", rep.Vault.Totals)
	}
	if len(rep.Vault.Series) != 0 {
		t.Errorf("series should be omitted by default, got %d points", len(rep.Vault.Series))
	}

	// include_series=1 restores daily series points.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report?range=7d&include_series=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/report?include_series=1 = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode report with series: %v", err)
	}
	if len(rep.Vault.Series) != 7 {
		t.Errorf("series len = %d, want 7", len(rep.Vault.Series))
	}

	// Invalid range returns 400.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report?range=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/report?range=bogus = %d, want 400", rec.Code)
	}
}

func TestWebLoginCookieAndCoexistenceWithAPITokens(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	cfg := config.Default()
	cfg.Auth.Username = "nick"
	cfg.Auth.PasswordHash = string(hash)
	cfg.API.ReadTokens = []string{"agent-ro-token"}

	srv, _, _ := newTestServerWithConfig(t, cfg)
	h := srv.Handler()

	// Unauthenticated browser visit to / redirects to /login.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET / = %d loc=%q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}

	// GET /login renders login page.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("GET /login = %d", rec.Code)
	}

	// Unauthenticated API request returns 401 JSON (never redirects).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/report unauth = %d, want 401", rec.Code)
	}

	// Agent read-only Bearer token accesses GET /api/report without browser login,
	// and is still blocked from POST /api/chest/open with 403.
	req := httptest.NewRequest(http.MethodGet, "/api/report", nil)
	req.Header.Set("Authorization", "Bearer agent-ro-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/report with read token = %d, want 200", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/chest/open", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer agent-ro-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/chest/open with read token = %d, want 403", rec.Code)
	}

	// Wrong password fails with 401.
	form := url.Values{"username": {"nick"}, "password": {"wrong"}}.Encode()
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /login bad password = %d, want 401", rec.Code)
	}

	// Valid login sets HttpOnly Secure cookie and sanitizes unsafe next URL.
	form = url.Values{"username": {"nick"}, "password": {"hunter2"}, "next": {"//evil.example"}}.Encode()
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /login ok = %d loc=%q, want 303 -> /", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "loot_session" || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}
	sessionCookie := cookies[0]

	// Session cookie grants dashboard, API read, and same-origin API write.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / with session = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/chest/open", bytes.NewBufferString(`{}`))
	req.Host = "loot.littletheta.com"
	req.Header.Set("Origin", "https://loot.littletheta.com")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/chest/open same-origin session = %d, want 200", rec.Code)
	}

	// Cross-origin POST with session cookie is rejected (CSRF guard).
	req = httptest.NewRequest(http.MethodPost, "/api/chest/open", bytes.NewBufferString(`{}`))
	req.Host = "loot.littletheta.com"
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/chest/open cross-origin = %d, want 403", rec.Code)
	}

	// Logout clears cookie and redirects to /login.
	req = httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout = %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Stale browser Authorization: Basic header from a former reverse proxy
	// must NOT bypass /login or keep authenticating after logout.
	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	req.SetBasicAuth("nick", "hunter2")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("GET /login with stale Basic Auth = %d loc=%q, want 200 login page", rec.Code, rec.Header().Get("Location"))
	}
	req = httptest.NewRequest(http.MethodGet, "/api/report", nil)
	req.SetBasicAuth("nick", "hunter2")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/report with Basic Auth = %d, want 401", rec.Code)
	}
}

func TestWebLoginRateLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Password = "right-pass"
	srv, _, _ := newTestServerWithConfig(t, cfg)
	h := srv.Handler()

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"password":"wrong"}`))
		req.RemoteAddr = "203.0.113.5:12345"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}

	// 11th attempt from same IP gets 429.
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"password":"right-pass"}`))
	req.RemoteAddr = "203.0.113.5:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt = %d, want 429", rec.Code)
	}
}
