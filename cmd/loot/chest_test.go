package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	runErr := fn()

	os.Stdout = saved
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	r.Close()
	return string(out), runErr
}

// `loot chest open --all` asks the server for every chest and prints the haul
// chest by chest with a grand total, rather than one chest at a time.
func TestChestOpenAllFlag(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chest/open" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
            "opened": "2026-08-16",
            "opened_dates": ["2026-08-16", "2026-08-17"],
            "count": 3,
            "drops": [
                {"rarity":"rare","title":"New settlement: BR","xp":250,"source":"loot","chest_date":"2026-08-16"},
                {"rarity":"common","title":"412 sales","subtitle":"com.example.app","xp":10,"source":"appstore","chest_date":"2026-08-17"},
                {"rarity":"legendary","title":"Best day ever","xp":1000,"source":"appstore","chest_date":"2026-08-17"}
            ],
            "chests": []
        }`)
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runChest([]string{"open", "--all", "--no-color", "--url", srv.URL})
	})
	if err != nil {
		t.Fatalf("chest open --all: %v", err)
	}

	if body["all"] != true {
		t.Errorf("request body = %v, want all:true", body)
	}
	if _, ok := body["date"]; ok {
		t.Errorf("request body carried a date: %v", body)
	}

	for _, want := range []string{
		"opened 2026-08-16 — 1 drop",
		"opened 2026-08-17 — 2 drops",
		"New settlement: BR",
		"total +250 xp",
		"total +1010 xp",
		"2 chests, 3 drops",
		"+1260 xp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Within a chest the cascade order the server sent is preserved.
	if strings.Index(out, "412 sales") > strings.Index(out, "Best day ever") {
		t.Errorf("bulk output reordered a chest:\n%s", out)
	}
}

// Nothing waiting is a quiet no-op, in the plural.
func TestChestOpenAllWithNothingWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"opened":"","opened_dates":[],"count":0,"drops":[],"chests":[]}`)
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runChest([]string{"open", "--all", "--url", srv.URL})
	})
	if err != nil {
		t.Fatalf("chest open --all: %v", err)
	}
	if strings.TrimSpace(out) != "no chests to open" {
		t.Errorf("output = %q, want \"no chests to open\"", out)
	}
}

// A single open still sends a date and prints exactly what it always did.
func TestChestOpenSingleStillSendsADate(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{
            "opened": "2026-08-17",
            "opened_dates": ["2026-08-17"],
            "count": 1,
            "drops": [{"rarity":"epic","title":"Best day ever","xp":500,"source":"appstore","chest_date":"2026-08-17"}],
            "chests": []
        }`)
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runChest([]string{"open", "2026-08-17", "--no-color", "--url", srv.URL})
	})
	if err != nil {
		t.Fatalf("chest open: %v", err)
	}
	if body["date"] != "2026-08-17" || body["all"] != nil {
		t.Errorf("request body = %v, want the date alone", body)
	}
	if !strings.Contains(out, "opened 2026-08-17 — 1 drop") || !strings.Contains(out, "total +500 xp") {
		t.Errorf("single-open output changed:\n%s", out)
	}
	// One chest needs no grand total line.
	if strings.Contains(out, "1 chest,") {
		t.Errorf("single open printed a bulk summary:\n%s", out)
	}
}

// --all names the chests by itself, so a date alongside it is a mistake worth
// naming rather than silently ignoring.
func TestChestOpenAllRejectsADate(t *testing.T) {
	err := runChest([]string{"open", "2026-08-17", "--all", "--url", "http://127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("error = %v, want a complaint about --all with a date", err)
	}
}
