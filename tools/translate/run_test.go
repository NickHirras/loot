package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTranslator answers without a network: it prefixes every string with the
// locale, which is enough to be recognisably "translated" while keeping every
// placeholder and expression in place, and rebuilds a plural message's arms
// for whatever categories the target language was asked for.
//
// `broken` names keys it should deliberately answer badly, so the end-to-end
// test can watch one key fail validation without taking the rest with it.
type fakeTranslator struct {
	broken map[string]string
	// omit names keys it answers with nothing at all.
	omit map[string]bool
	// failOn is the number of items in a batch it refuses outright, so the
	// retry path can be exercised.
	failOn int
	calls  int
	sizes  []int
}

func (f *fakeTranslator) Translate(req BatchRequest) (map[string]Value, error) {
	f.calls++
	f.sizes = append(f.sizes, len(req.Items))
	if f.failOn > 0 && len(req.Items) == f.failOn {
		return nil, fmt.Errorf("pretend refusal")
	}
	out := map[string]Value{}
	for _, it := range req.Items {
		if f.omit[it.Key] {
			continue
		}
		if bad, ok := f.broken[it.Key]; ok {
			out[it.Key] = Value{Text: bad}
			continue
		}
		if !it.English.IsVariant() {
			out[it.Key] = Value{Text: "[" + req.Locale + "] " + it.English.Text}
			continue
		}
		// Collapse or expand the English arms onto the requested ones, in the
		// order MatchKeys generated them, so a language with fewer categories
		// gets a sensible arm rather than a missing one.
		en := it.English.Patterns()
		match := map[string]string{}
		for i, arm := range it.MatchKeys {
			src := en[len(en)-1]
			if i < len(en) {
				src = en[i]
			}
			match[arm] = "[" + req.Locale + "] " + src
		}
		out[it.Key] = Value{Variant: &Variant{
			Declarations: it.English.Variant.Declarations,
			Selectors:    it.English.Variant.Selectors,
			Match:        match,
		}}
	}
	return out, nil
}

// fixtureRepo builds a miniature checkout: the two English sources and the
// locale list, and nothing else.
func fixtureRepo(t *testing.T, locales ...string) string {
	t.Helper()
	root := t.TempDir()
	quoted := make([]string, 0, len(locales)+1)
	for _, l := range append([]string{"en"}, locales...) {
		quoted = append(quoted, `"`+l+`"`)
	}
	write(t, filepath.Join(root, "web", "project.inlang", "settings.json"),
		`{"baseLocale":"en","locales":[`+strings.Join(quoted, ",")+`]}`)
	write(t, filepath.Join(root, "web", "messages", "en.json"), sampleEN)
	write(t, filepath.Join(root, "internal", "rules", "default.yaml"), sampleRules)
	return root
}

func TestEndToEnd(t *testing.T) {
	root := fixtureRepo(t, "de", "ja")
	fake := &fakeTranslator{}
	opts := Options{Root: root}

	r, err := NewRunner(opts, fake)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveLock(); err != nil {
		t.Fatal(err)
	}

	if !rep.Changed {
		t.Error("the report should say files changed")
	}
	if n := rep.FailedCount(); n != 0 {
		t.Errorf("%d key(s) failed:\n%s", n, rep.Text())
	}
	// 3 messages + 5 rule fields, for two languages.
	if got := rep.TranslatedCount(); got != (3+5)*2 {
		t.Errorf("translated %d keys, want %d\n%s", got, (3+5)*2, rep.Text())
	}

	// The German catalog is on disk, is valid JSON, and kept the placeholder.
	de, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if got := de["header_xp"].Text; got != "[de] {xp} XP" {
		t.Errorf("header_xp = %q", got)
	}
	if len(de) != 3 {
		t.Errorf("German catalog has %d keys, want 3", len(de))
	}

	// Japanese collapsed the plural message to a single `other` arm.
	ja, err := LoadMessages(messagesPath(root, "ja"))
	if err != nil {
		t.Fatal(err)
	}
	v := ja["chest_row_drops"]
	if !v.IsVariant() {
		t.Fatal("chest_row_drops should still be a variant message in Japanese")
	}
	if len(v.Variant.Match) != 1 {
		t.Errorf("Japanese should have one arm, got %v", v.Variant.Match)
	}
	if _, ok := v.Variant.Match["countPlural=other"]; !ok {
		t.Errorf("Japanese arm is not `other`: %v", v.Variant.Match)
	}

	// The overlay is on disk, parses, and only names rules default.yaml has.
	overlay, err := LoadOverlay(overlayPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if got := overlay["rules/revenuecat-churn/title"].Text; got != "[de] Subscriber lost" {
		t.Errorf("overlay title = %q", got)
	}
	if got := overlay["fallback/subtitle"].Text; got != "[de] {{.App}}" {
		t.Errorf("overlay fallback subtitle = %q", got)
	}

	// The lock records the English hash, per catalog, per language.
	lock, err := LoadLock(lockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	english, _ := ParseMessages([]byte(sampleEN))
	if h, ok := lock.Get(KindMessages, "de", "header_xp"); !ok || h != english["header_xp"].Hash() {
		t.Errorf("lock did not record the English hash for header_xp (%q)", h)
	}
	if _, ok := lock.Get(KindRules, "ja", "fallback/title"); !ok {
		t.Error("lock did not record the rules half for Japanese")
	}

	// -check passes over what we just wrote.
	r2, err := NewRunner(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Check(); err != nil {
		t.Errorf("the files we generated do not validate: %v", err)
	}

	// A second run with nothing changed does no work at all.
	fake2 := &fakeTranslator{}
	r3, err := NewRunner(opts, fake2)
	if err != nil {
		t.Fatal(err)
	}
	rep3, err := r3.Run()
	if err != nil {
		t.Fatal(err)
	}
	if fake2.calls != 0 {
		t.Errorf("a no-op run called the API %d time(s)", fake2.calls)
	}
	if rep3.Changed {
		t.Error("a no-op run should not report a change")
	}
}

func TestRunKeepsHandEditsAndRetranslatesChangedEnglish(t *testing.T) {
	root := fixtureRepo(t, "de")
	opts := Options{Root: root}

	r, err := NewRunner(opts, &fakeTranslator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveLock(); err != nil {
		t.Fatal(err)
	}

	// Somebody fixes a translation by hand, and separately the English behind
	// another key is reworded.
	de, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	de["feed_empty_title"] = Value{Text: "Noch keine Beute."}
	if err := WriteMessages(messagesPath(root, "de"), de); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(sampleEN, `"{xp} XP"`, `"{xp} experience"`, 1)
	write(t, messagesPath(root, BaseLocale), edited)

	fake := &fakeTranslator{}
	r2, err := NewRunner(opts, fake)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r2.Run()
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.SaveLock(); err != nil {
		t.Fatal(err)
	}

	after, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if got := after["feed_empty_title"].Text; got != "Noch keine Beute." {
		t.Errorf("the hand edit was overwritten: %q", got)
	}
	if got := after["header_xp"].Text; got != "[de] {xp} experience" {
		t.Errorf("the reworded English was not retranslated: %q", got)
	}
	if got := rep.TranslatedCount(); got != 1 {
		t.Errorf("translated %d keys, want exactly the one whose English moved\n%s", got, rep.Text())
	}
}

func TestRunDeletesKeysEnglishDropped(t *testing.T) {
	root := fixtureRepo(t, "de")
	opts := Options{Root: root}

	r, err := NewRunner(opts, &fakeTranslator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveLock(); err != nil {
		t.Fatal(err)
	}

	// The English loses a key.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sampleEN), &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "header_xp")
	trimmed, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	write(t, messagesPath(root, BaseLocale), string(trimmed))

	r2, err := NewRunner(opts, &fakeTranslator{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r2.Run()
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.SaveLock(); err != nil {
		t.Fatal(err)
	}

	after, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after["header_xp"]; ok {
		t.Error("a key English dropped should have been deleted from the target")
	}
	lock, err := LoadLock(lockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lock.Get(KindMessages, "de", "header_xp"); ok {
		t.Error("a key English dropped should have left the lock too")
	}
	if !rep.Changed {
		t.Error("a deletion is a change")
	}
}

func TestRunReportsABadTranslationWithoutLosingTheRest(t *testing.T) {
	root := fixtureRepo(t, "de")
	fake := &fakeTranslator{broken: map[string]string{
		// A dropped placeholder: the validator must catch it.
		"header_xp": "Erfahrungspunkte",
	}}
	r, err := NewRunner(Options{Root: root}, fake)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveLock(); err != nil {
		t.Fatal(err)
	}

	if got := rep.FailedCount(); got != 1 {
		t.Errorf("failed %d, want 1\n%s", got, rep.Text())
	}
	de, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := de["header_xp"]; ok {
		t.Error("a key that failed validation must not be written")
	}
	if _, ok := de["feed_empty_title"]; !ok {
		t.Error("the keys that passed should still have been written")
	}
	lock, err := LoadLock(lockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lock.Get(KindMessages, "de", "header_xp"); ok {
		t.Error("a failed key must not be locked, or it would never be retried")
	}

	md := rep.Markdown()
	if !strings.Contains(md, "header_xp") || !strings.Contains(md, "dropped placeholder") {
		t.Errorf("the summary should name the failure:\n%s", md)
	}
}

func TestRunRetriesASmallerBatch(t *testing.T) {
	root := fixtureRepo(t, "de")
	// Refuse any batch of three — which is every messages batch here — and
	// answer the smaller retry.
	fake := &fakeTranslator{failOn: 3}
	r, err := NewRunner(Options{Root: root, OnlyMessages: true}, fake)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if rep.TranslatedCount() != 3 {
		t.Errorf("the retry should have rescued all three keys\n%s", rep.Text())
	}
	// One refused call of three, then two smaller pieces.
	if fake.calls != 3 {
		t.Errorf("expected a refusal and two smaller retries, got %d calls (%v)", fake.calls, fake.sizes)
	}
	for _, n := range fake.sizes[1:] {
		if n >= 3 {
			t.Errorf("a retry was not smaller than the batch that failed: %v", fake.sizes)
		}
	}
}

func TestRunReportsAKeyThatNeverCameBack(t *testing.T) {
	root := fixtureRepo(t, "de")
	fake := &fakeTranslator{omit: map[string]bool{"feed_empty_title": true}}
	r, err := NewRunner(Options{Root: root, OnlyMessages: true}, fake)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if rep.FailedCount() != 1 {
		t.Errorf("an unanswered key should be reported as failed\n%s", rep.Text())
	}
	if !strings.Contains(rep.Markdown(), "no translation came back") {
		t.Errorf("the summary should say the key was never answered:\n%s", rep.Markdown())
	}
}

func TestOnlyFlagsAndLanguages(t *testing.T) {
	root := fixtureRepo(t, "de", "fr")

	r, err := NewRunner(Options{Root: root, OnlyRules: true, Languages: []string{"fr"}}, &fakeTranslator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(messagesPath(root, "fr")); !os.IsNotExist(err) {
		t.Error("-only-rules wrote a messages catalog")
	}
	if _, err := os.Stat(overlayPath(root, "de")); !os.IsNotExist(err) {
		t.Error("-languages fr wrote a German overlay")
	}
	if _, err := os.Stat(overlayPath(root, "fr")); err != nil {
		t.Errorf("the French overlay was not written: %v", err)
	}

	if _, err := NewRunner(Options{Root: root, Languages: []string{"xx"}}, nil); err == nil {
		t.Error("a locale that is not in settings.json must be rejected")
	}
}

func TestCheckCatchesABrokenOverlay(t *testing.T) {
	root := fixtureRepo(t, "de")
	// An overlay naming a rule default.yaml has never heard of.
	write(t, overlayPath(root, "de"), "rules:\n  - name: not-a-real-rule\n    then:\n      title: \"Hallo\"\n")
	r, err := NewRunner(Options{Root: root, OnlyRules: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Check(); err == nil {
		t.Fatal("-check should reject an overlay that invents a rule")
	}

	// A template whose expression was translated.
	write(t, overlayPath(root, "de"), "rules:\n  - name: revenuecat-churn\n    then:\n      subtitle: \"{{.Art}}\"\n")
	r2, err := NewRunner(Options{Root: root, OnlyRules: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Check(); err == nil {
		t.Fatal("-check should reject a translated template expression")
	}
}

func TestCheckPassesOnTheRealTree(t *testing.T) {
	// The hand-written German overlay in internal/rules/locales must validate
	// against default.yaml, since CI runs exactly this.
	r, err := NewRunner(Options{Root: repoRoot(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Check(); err != nil {
		t.Errorf("-check fails on the checked-in tree: %v", err)
	}
}

func TestGlossaryLoads(t *testing.T) {
	g, err := LoadGlossary()
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Terms) < 10 {
		t.Errorf("the glossary has only %d terms", len(g.Terms))
	}
	// "Loot" is the one term pinned everywhere: it is never translated.
	pinned := g.Pinned("ja")
	found := false
	for _, term := range pinned {
		if term.Term == "Loot" && term.Translations["ja"] == "Loot" {
			found = true
		}
	}
	if !found {
		t.Error(`"Loot" should be pinned to itself in every language`)
	}
	if p := g.Prompt(); !strings.Contains(p, "cursed") || !strings.Contains(p, "Fixed renderings") {
		t.Errorf("the glossary prompt is missing something:\n%s", p)
	}
}

func TestHintForCoversEveryAreaInTheRealCatalog(t *testing.T) {
	root := repoRoot(t)
	c, err := LoadMessages(messagesPath(root, BaseLocale))
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]int{}
	for _, key := range c.Keys() {
		if HintFor(KindMessages, key) == "" {
			area, _, _ := strings.Cut(key, "_")
			missing[area]++
		}
	}
	if len(missing) > 0 {
		t.Errorf("no hint for these key areas, so those keys go out with no context: %v", missing)
	}
	if HintFor(KindRules, "rules/settlement/title") == "" {
		t.Error("a rule title should get a hint")
	}
}
