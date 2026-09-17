package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPluralCategoryTable pins the table against CLDR. These are
// `new Intl.PluralRules(tag).resolvedOptions().pluralCategories` — the same
// data Paraglide compiles a variant message against — so a wrong entry here
// would mean every plural message in that language is generated with arms the
// browser never selects.
func TestPluralCategoryTable(t *testing.T) {
	want := map[string][]string{
		"en":      {"one", "other"},
		"de":      {"one", "other"},
		"nl":      {"one", "other"},
		"es":      {"one", "many", "other"},
		"fr":      {"one", "many", "other"},
		"it":      {"one", "many", "other"},
		"pt-BR":   {"one", "many", "other"},
		"ru":      {"one", "few", "many", "other"},
		"ja":      {"other"},
		"ko":      {"other"},
		"zh-Hans": {"other"},
	}
	for locale, cats := range want {
		got, err := PluralCategories(locale)
		if err != nil {
			t.Errorf("%s: %v", locale, err)
			continue
		}
		if !reflect.DeepEqual(got, cats) {
			t.Errorf("%s: got %v, want %v", locale, got, cats)
		}
		found := false
		for _, c := range got {
			if c == "other" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: every language must have `other`", locale)
		}
	}
}

// Every locale in the project must be in the table, or its first run would
// fail rather than translate.
func TestEveryProjectLocaleHasCategories(t *testing.T) {
	root := repoRoot(t)
	locales, err := LoadLocales(settingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range append(locales, BaseLocale) {
		if _, err := PluralCategories(l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
		if LanguageName(l) == l {
			t.Errorf("%s: no human name in languageNames; the prompt would say %q", l, l)
		}
	}
}

func TestPluralCategoriesFallsBackToTheBaseLanguage(t *testing.T) {
	got, err := PluralCategories("de-AT")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"one", "other"}) {
		t.Errorf("de-AT should fall back to de, got %v", got)
	}
	if _, err := PluralCategories("xx"); err == nil {
		t.Error("an unknown language must be an error, not a silent guess")
	}
}

func TestMatchKeys(t *testing.T) {
	got := MatchKeys([]string{"countPlural"}, []string{"one", "other"})
	want := []string{"countPlural=one", "countPlural=other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	got = MatchKeys([]string{"dropsPlural", "chestsPlural"}, []string{"one", "other"})
	want = []string{
		"dropsPlural=one, chestsPlural=one",
		"dropsPlural=one, chestsPlural=other",
		"dropsPlural=other, chestsPlural=one",
		"dropsPlural=other, chestsPlural=other",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if got := MatchKeys(nil, []string{"other"}); got != nil {
		t.Errorf("no selectors means no arms, got %v", got)
	}
}

// The generated arms must be spelled exactly the way the real English catalog
// spells them, separator and all, or every variant message would come back
// looking like it had the wrong arms.
func TestMatchKeysMatchTheRealCatalog(t *testing.T) {
	root := repoRoot(t)
	c, err := LoadMessages(messagesPath(root, BaseLocale))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, key := range c.Keys() {
		v := c[key]
		if !v.IsVariant() {
			continue
		}
		checked++
		want := setOf(MatchKeys(v.Variant.Selectors, []string{"one", "other"}))
		for arm := range v.Variant.Match {
			if !want[arm] {
				t.Errorf("%s: catalog has arm %q, MatchKeys would not generate it", key, arm)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no variant messages found in en.json; this test is checking nothing")
	}
}

// The writer has to produce what the inlang plugin produces, or a generated
// catalog would differ from the hand-written one in whitespace and key order
// and churn the diff the first time anybody opened the project in an editor.
// Reading en.json and writing it back must be a no-op, byte for byte.
func TestWriterReproducesTheHandWrittenCatalog(t *testing.T) {
	path := messagesPath(repoRoot(t), BaseLocale)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseMessages(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := MarshalMessages(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("round-tripping %s is not a no-op; a generated catalog would not look like this one", path)
	}
}

func TestLoadLocalesDropsTheBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	write(t, path, `{"baseLocale":"en","locales":["en","de","fr"]}`)
	got, err := LoadLocales(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"de", "fr"}) {
		t.Errorf("got %v, want [de fr]", got)
	}
}

func TestLoadLocalesRejectsANonEnglishBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	write(t, path, `{"baseLocale":"de","locales":["de","en"]}`)
	if _, err := LoadLocales(path); err == nil || !strings.Contains(err.Error(), "baseLocale") {
		t.Errorf("expected a complaint about baseLocale, got %v", err)
	}
}

// repoRoot finds the checkout from the test's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if isRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the Loot checkout")
		}
		dir = parent
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
