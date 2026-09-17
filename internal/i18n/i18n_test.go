package i18n_test

import (
	"testing"

	"github.com/nickhirras/loot/internal/i18n"
)

func TestLookupPlainMessage(t *testing.T) {
	got, ok := i18n.Lookup("en", "ach_cartographer_title")
	if !ok {
		t.Fatal("ach_cartographer_title is missing from the English catalog")
	}
	if got != "Cartographer" {
		t.Fatalf("lookup = %q, want Cartographer", got)
	}
}

// A language with no catalog — or with a catalog that has not been translated
// this far — answers in English rather than in blanks.
func TestLookupFallsBackToEnglish(t *testing.T) {
	got, ok := i18n.Lookup("de", "ach_cartographer_title")
	if !ok {
		t.Fatal("no fallback for a language without a catalog")
	}
	if got != "Cartographer" {
		t.Fatalf("lookup = %q, want the English fallback", got)
	}
	if i18n.Has("de") {
		t.Skip("a German catalog now exists; this test asserted the fallback path")
	}
}

// A message with plural or gender variants is an object rather than a string,
// and selecting between the variants is the dashboard's job, not this
// package's. It says so by refusing rather than by inventing one.
func TestLookupRefusesVariantMessages(t *testing.T) {
	// recap_highlight_unlocked_more is a countPlural message in web/messages:
	// a JSON array of declarations and a match table, not a string.
	if got, ok := i18n.Lookup("en", "recap_highlight_unlocked_more"); ok {
		t.Fatalf("a variant message was answered with %q", got)
	}
}

func TestLookupMisses(t *testing.T) {
	if _, ok := i18n.Lookup("en", "no_such_message_key"); ok {
		t.Fatal("a key that exists nowhere was answered")
	}
	if _, ok := i18n.Lookup("en", ""); ok {
		t.Fatal("the empty key was answered")
	}
	// A "language" that is really a path must never reach the filesystem.
	if _, ok := i18n.Lookup("../../secrets", "ach_cartographer_title"); !ok {
		t.Fatal("the English fallback should still answer")
	}
	if i18n.Has("../../secrets") {
		t.Fatal("a path-shaped language resolved to a catalog")
	}
}
