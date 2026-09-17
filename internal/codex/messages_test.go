package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The catalog and the message file have to agree.
//
// A trophy's title and description live in two places now: here, where `loot
// tail` and the unlock event read them, and in web/messages/en.json, where the
// dashboard does. That is a duplication with a failure mode — add an
// achievement, forget the messages, and the wall shows a blank where a name
// should be — so the duplication is checked rather than trusted.
//
// The test reads the English catalogue directly rather than the compiled
// Paraglide output, because the compiled output is a build artefact and this
// has to fail in `go test` on a checkout that has never run npm.
const messagesPath = "../../web/messages/en.json"

func loadMessages(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(messagesPath)
	if err != nil {
		t.Fatalf("read %s: %v", messagesPath, err)
	}
	var messages map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatalf("parse %s: %v", messagesPath, err)
	}
	return messages
}

// TestCatalogHasMessages fails when an achievement has no title or description
// for the dashboard to print.
func TestCatalogHasMessages(t *testing.T) {
	messages := loadMessages(t)

	for _, e := range Catalog {
		for _, suffix := range []string{"_title", "_desc"} {
			key := "ach_" + e.Key + suffix
			raw, ok := messages[key]
			if !ok {
				t.Errorf("achievement %q has no %s message in %s", e.Key, key, messagesPath)
				continue
			}
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				t.Errorf("%s is not a plain string: %v", key, err)
				continue
			}
			want := e.Title
			if suffix == "_desc" {
				want = e.Description
			}
			// English is the source language: the message and the Go string
			// are the same sentence, and a drift between them would show one
			// wording in the UI and another in `loot tail`.
			if text != want {
				t.Errorf("%s = %q, want %q", key, text, want)
			}
		}
	}
}

// TestCatalogUnitsHaveMessages fails when a progress line would have to print
// a raw identifier ("record_days") for want of a message.
func TestCatalogUnitsHaveMessages(t *testing.T) {
	messages := loadMessages(t)

	for _, e := range Catalog {
		if e.Unit == "" {
			continue
		}
		if strings.ContainsAny(e.Unit, " -") {
			t.Errorf("achievement %q has unit %q: units are identifiers, so snake_case it",
				e.Key, e.Unit)
		}
		key := "ach_unit_" + strings.ToLower(e.Unit)
		if _, ok := messages[key]; !ok {
			t.Errorf("unit %q (achievement %q) has no %s message in %s",
				e.Unit, e.Key, key, messagesPath)
		}
	}
}
