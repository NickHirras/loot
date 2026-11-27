package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportCLIAuthAndSections(t *testing.T) {
	var (
		gotPath   string
		gotQuery  string
		gotAuth   string
		gotHeader string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("CF-Access-Client-Id")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/report":
			_, _ = io.WriteString(w, `{
				"display_currency": "USD",
				"app": "Nistis",
				"stats": {"total_xp": 1200, "level": 4, "countries_count": 5, "population": 42, "era": {"name": "Village"}},
				"vault": {"display_currency": "USD", "range": {"from":"2026-10-29","to":"2026-11-27","days":30}, "totals": {"revenue_base": 123.45, "units": 12, "refunds": 1}, "prev_totals": {"revenue_base": 100, "units": 10}},
				"audience": [{"product":"Nistis","current":38,"sources":[{"source":"googleplay","count":38,"basis":"active_devices"}]}],
				"reviews": {"range":"90d","average_rating":5,"total_ratings":4,"total_reviews":0,"by_app":[{"product":"Nistis","average_rating":5,"rating_count":4}]},
				"attention": {"chests_waiting":[],"bosses_alive":[],"open_mysteries":[],"active_quests":[],"source_errors":[]}
			}`)
		case "/api/hearth/trend":
			_, _ = io.WriteString(w, `{"metric":"audience","range":"30d","from":"2026-10-29","to":"2026-11-27","kind":"level","current":38,"start":30,"change":8,"peak":40,"peak_day":"2026-11-20","points":[]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runReport([]string{
			"--url", srv.URL,
			"--token", "tok-abc",
			"--header", "CF-Access-Client-Id: cf-id-1",
			"--app", "Nistis",
			"--range", "30d",
		})
	})
	if err != nil {
		t.Fatalf("runReport overview: %v", err)
	}
	if gotPath != "/api/report" {
		t.Errorf("path = %q, want /api/report", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", gotAuth)
	}
	if gotHeader != "cf-id-1" {
		t.Errorf("CF-Access-Client-Id = %q, want cf-id-1", gotHeader)
	}
	for _, want := range []string{"Loot Report · Nistis · USD", "123.45 USD", "active=38", "5.00★"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview output missing %q:\n%s", want, out)
		}
	}

	// Trend section with --json.
	out, err = captureStdout(t, func() error {
		return runReport([]string{
			"trend",
			"--url", srv.URL,
			"--metric", "audience",
			"--product", "Nistis",
			"--range", "30d",
			"--json",
		})
	})
	if err != nil {
		t.Fatalf("runReport trend --json: %v", err)
	}
	if gotPath != "/api/hearth/trend" || !strings.Contains(gotQuery, "metric=audience") || !strings.Contains(gotQuery, "product=Nistis") {
		t.Errorf("unexpected trend request: %s?%s", gotPath, gotQuery)
	}
	if !strings.Contains(out, `"current":38`) {
		t.Errorf("trend json output = %q", out)
	}
}

func TestClientTokenUsesAPIKeyWhenBasicAuthInURL(t *testing.T) {
	var gotAuth, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sources":[]}`)
	}))
	defer srv.Close()

	basicURL := strings.Replace(srv.URL, "http://", "http://alice:secret@", 1)
	_, err := captureStdout(t, func() error {
		return runReport([]string{"sources", "--url", basicURL, "--token", "loot-ro-key", "--json"})
	})
	if err != nil {
		t.Fatalf("runReport sources: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want Basic ...", gotAuth)
	}
	if gotKey != "loot-ro-key" {
		t.Errorf("X-API-Key = %q, want loot-ro-key", gotKey)
	}
}

func TestReportTrendRequiresMetric(t *testing.T) {
	err := runReport([]string{"trend", "--url", "http://127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "--metric") {
		t.Fatalf("expected --metric error, got %v", err)
	}
}
