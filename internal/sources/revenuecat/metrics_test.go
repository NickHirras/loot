package revenuecat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

func TestOverviewPollingDisabledByDefault(t *testing.T) {
	s := New("whsec", nil)
	if s.PollInterval() != 0 {
		t.Fatalf("PollInterval = %v, want 0 when polling not configured", s.PollInterval())
	}
	ev, _, err := s.Poll(context.Background(), nil)
	if err != nil || len(ev) != 0 {
		t.Fatalf("Poll = %v, %v, want empty nil", ev, err)
	}
}

func TestOverviewPollEmitsActiveUsersAudience(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"metrics": [
				{"object": "metric", "id": "mrr", "value": 42.5},
				{"object": "metric", "id": "active_users", "value": 87}
			]
		}`))
	}))
	defer srv.Close()

	s := New("whsec", nil)
	s.APIKey = "sk_test_v2"
	s.Projects = []Project{{ID: "proj_nistis", App: "Nistis"}}
	s.BaseURL = srv.URL
	s.Client = srv.Client()
	s.Now = func() time.Time { return time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC) }

	if s.PollInterval() != 6*time.Hour {
		t.Fatalf("PollInterval = %v, want 6h", s.PollInterval())
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	events, _, err := s.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if gotAuth != "Bearer sk_test_v2" {
		t.Errorf("Authorization = %q, want Bearer sk_test_v2", gotAuth)
	}
	if gotPath != "/v2/projects/proj_nistis/metrics/overview" {
		t.Errorf("path = %q", gotPath)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Source != Name || ev.Kind != core.KindAudience || ev.App != "Nistis" ||
		ev.Day != "2026-11-27" || ev.Quantity != 87 || !ev.Silent {
		t.Errorf("unexpected event: %+v", ev)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["covers_android"] != true {
		t.Errorf("covers_android = %v, want true", payload["covers_android"])
	}
}

func TestOverviewCheckSurfacesPermissionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing scope", http.StatusForbidden)
	}))
	defer srv.Close()

	s := New("whsec", nil)
	s.APIKey = "sk_bad"
	s.Projects = []Project{{ID: "proj_1", App: "Nistis"}}
	s.BaseURL = srv.URL
	s.Client = srv.Client()

	err := s.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "charts_metrics:overview:read") {
		t.Fatalf("Check error = %v, want charts_metrics:overview:read hint", err)
	}
}
