package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// offlineEN is the fixture catalog for offline mode: four keys, so one can be
// answered, one left unanswered, one answered badly and one answered twice.
const offlineEN = `{
  "$schema": "https://inlang.com/schema/inlang-message-format",
  "feed_empty_title": "No loot yet.",
  "header_xp": "{xp} XP",
  "header_level": "Level {level}",
  "chest_row_drops": [
    {
      "declarations": ["input count", "local countPlural = count: plural"],
      "selectors": ["countPlural"],
      "match": {
        "countPlural=one": "{count} drop",
        "countPlural=other": "{count} drops"
      }
    }
  ]
}`

func TestExportWritesWhatTheAPIWouldHaveBeenSent(t *testing.T) {
	root := fixtureRepo(t, "de")
	dir := t.TempDir()

	r, err := NewRunner(Options{Root: root, OnlyMessages: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Export(dir); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "messages-de-01.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got ExportRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("the exported request is not JSON: %v", err)
	}

	// The same batch, built the way Run builds it. The whole point of the file
	// is that a subagent reading it is told exactly what the API would be.
	english := r.english(KindMessages)
	cats, err := PluralCategories("de")
	if err != nil {
		t.Fatal(err)
	}
	want := r.request(KindMessages, "de", cats, english, english.Keys())
	if got.User != userPrompt(want) {
		t.Errorf("the user turn is not the one the API would get:\n--- got ---\n%s\n--- want ---\n%s", got.User, userPrompt(want))
	}
	g, err := LoadGlossary()
	if err != nil {
		t.Fatal(err)
	}
	if got.System != SystemPrompt(g) {
		t.Error("the system prompt is not the cached prefix the API would get")
	}
	if got.Kind != KindMessages || got.Locale != "de" {
		t.Errorf("kind/locale = %s/%s", got.Kind, got.Locale)
	}
	if !equalStrings(got.Categories, cats) {
		t.Errorf("categories = %v, want %v", got.Categories, cats)
	}
	if !equalStrings(got.Keys, english.Keys()) {
		t.Errorf("keys = %v, want %v", got.Keys, english.Keys())
	}
	if got.Schema == nil || got.Schema["properties"] == nil {
		t.Errorf("the reply schema did not survive the round trip: %v", got.Schema)
	}

	// The README tells the reader what to write, and where.
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"messages-de-01.request.json", ".reply.json", "translations", "-import",
	} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the README never mentions %q:\n%s", want, readme)
		}
	}

	// Export writes the requests and the README, and nothing at all outside
	// the directory it was given.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("exported %v, want one request and a README", names)
	}
	if _, err := os.Stat(messagesPath(root, "de")); !os.IsNotExist(err) {
		t.Error("-export wrote a target catalog")
	}
	if _, err := os.Stat(lockPath(root)); !os.IsNotExist(err) {
		t.Error("-export wrote the lock")
	}
}

func TestExportSkipsWhatIsAlreadyUpToDate(t *testing.T) {
	root := fixtureRepo(t, "de")
	opts := Options{Root: root, OnlyMessages: true}

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

	dir := t.TempDir()
	r2, err := NewRunner(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Export(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "README.md" {
		t.Errorf("an up-to-date tree should export nothing but a README, got %d entries", len(entries))
	}

	// -force ignores the lock, here as everywhere else.
	forced := t.TempDir()
	r3, err := NewRunner(Options{Root: root, OnlyMessages: true, Force: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r3.Export(forced); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(forced, "messages-de-01.request.json")); err != nil {
		t.Errorf("-force should have exported the whole catalog again: %v", err)
	}
}

// importRun does what main does for -import: build the runner, point the file
// translator at its English, run, and carry the notes into the report.
func importRun(t *testing.T, opts Options, dir string) *Report {
	t.Helper()
	files := NewFileTranslator(dir)
	r, err := NewRunner(opts, files)
	if err != nil {
		t.Fatal(err)
	}
	files.Items = r.AllItems
	rep, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	rep.Notes = files.Notes()
	if rep.Changed {
		if err := r.SaveLock(); err != nil {
			t.Fatal(err)
		}
	}
	return rep
}

func TestImportRoundTrip(t *testing.T) {
	root := fixtureRepo(t, "de")
	write(t, messagesPath(root, BaseLocale), offlineEN)
	opts := Options{Root: root, OnlyMessages: true}
	dir := t.TempDir()

	// Export first, so the reply files are named after real requests.
	r, err := NewRunner(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Export(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, requestFileName(KindMessages, "de", 1))); err != nil {
		t.Fatal(err)
	}

	// The reply the subagent would write: two good keys, one whose translation
	// drops a placeholder, and nothing at all for header_level.
	write(t, filepath.Join(dir, replyFileName(requestFileName(KindMessages, "de", 1))), `{"translations":[
	  {"key":"feed_empty_title","text":"Noch nichts.","variants":[]},
	  {"key":"chest_row_drops","text":"","variants":[
	    {"match":"countPlural=one","text":"{count} Fund"},
	    {"match":"countPlural=other","text":"{count} Funde"}
	  ]},
	  {"key":"header_xp","text":"Erfahrungspunkte","variants":[]}
	]}`)

	// A second pass at the same language: it answers one key again, and has one
	// entry that does not match the schema.
	write(t, filepath.Join(dir, "messages-de-02.reply.json"), `{"translations":[
	  {"key":"feed_empty_title","text":"Noch keine Beute.","variants":[]},
	  {"key":"header_level","text":7,"variants":[]}
	]}`)

	// And a file that is not JSON at all.
	write(t, filepath.Join(dir, "messages-de-03.reply.json"), "sorry, I got distracted")

	rep := importRun(t, opts, dir)

	de, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate key: the later file in sorted order wins.
	if got := de["feed_empty_title"].Text; got != "Noch keine Beute." {
		t.Errorf("feed_empty_title = %q, want the answer from the later file", got)
	}
	v := de["chest_row_drops"]
	if !v.IsVariant() || len(v.Variant.Match) != 2 {
		t.Errorf("chest_row_drops did not come through as a two-armed variant: %#v", v)
	}
	if !equalStrings(v.Variant.Selectors, []string{"countPlural"}) {
		t.Errorf("the variant machinery was not copied from the English: %v", v.Variant.Selectors)
	}
	if _, ok := de["header_xp"]; ok {
		t.Error("a translation that dropped its placeholder must not be written")
	}
	if _, ok := de["header_level"]; ok {
		t.Error("a key nothing answered must not be written")
	}

	// The lock remembers only what was written.
	lock, err := LoadLock(lockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lock.Get(KindMessages, "de", "feed_empty_title"); !ok {
		t.Error("the lock did not record an imported key")
	}
	for _, key := range []string{"header_xp", "header_level"} {
		if _, ok := lock.Get(KindMessages, "de", key); ok {
			t.Errorf("%s failed and must not be locked, or it would never be retried", key)
		}
	}

	if got := rep.FailedCount(); got != 2 {
		t.Errorf("failed %d, want 2 (one unanswered, one invalid)\n%s", got, rep.Text())
	}
	text, md := rep.Text(), rep.Markdown()
	for _, want := range []string{"no reply for key", "header_level"} {
		if !strings.Contains(text, want) || !strings.Contains(md, want) {
			t.Errorf("the report should say %q:\n%s\n%s", want, text, md)
		}
	}
	if !strings.Contains(md, "dropped placeholder") {
		t.Errorf("the validation failure should be reported:\n%s", md)
	}
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "messages-de-03.reply.json") {
		t.Errorf("the unreadable file should be named:\n%s", notes)
	}
	if !strings.Contains(notes, "messages-de-02.reply.json") || !strings.Contains(notes, "the later file wins") {
		t.Errorf("the duplicated key should be noted:\n%s", notes)
	}
	if !strings.Contains(notes, "does not match the schema") {
		t.Errorf("the malformed entry should be noted:\n%s", notes)
	}
	if !strings.Contains(md, "note(s) about the imported replies") {
		t.Errorf("the summary should carry the notes:\n%s", md)
	}

	// -check passes over what was written, and a second import of the same
	// directory now has only the two failures left to do.
	r2, err := NewRunner(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Check(); err != nil {
		t.Errorf("the imported catalog does not validate: %v", err)
	}
}

func TestImportMatchesByKeyNotByBatch(t *testing.T) {
	root := fixtureRepo(t, "de")
	write(t, messagesPath(root, BaseLocale), offlineEN)
	opts := Options{Root: root, OnlyMessages: true}
	dir := t.TempDir()

	// A reply that never had a request of its own — the batches regrouped
	// between the export and the answer. It must still be honoured.
	write(t, filepath.Join(dir, "messages-de-17.reply.json"),
		`{"translations":[{"key":"header_level","text":"Stufe {level}","variants":[]}]}`)

	rep := importRun(t, opts, dir)
	de, err := LoadMessages(messagesPath(root, "de"))
	if err != nil {
		t.Fatal(err)
	}
	if got := de["header_level"].Text; got != "Stufe {level}" {
		t.Errorf("header_level = %q", got)
	}
	if got := rep.FailedCount(); got != 3 {
		t.Errorf("failed %d, want the three keys nothing answered\n%s", got, rep.Text())
	}
}

func TestImportWithNoRepliesWritesNothing(t *testing.T) {
	root := fixtureRepo(t, "de")
	opts := Options{Root: root, OnlyMessages: true}
	dir := t.TempDir()

	rep := importRun(t, opts, dir)
	if rep.Changed {
		t.Error("an empty import changed something")
	}
	if got := rep.FailedCount(); got != 3 {
		t.Errorf("failed %d, want every key\n%s", got, rep.Text())
	}
	if !strings.Contains(rep.Text(), "no reply for key") {
		t.Errorf("every key should be reported with its reason:\n%s", rep.Text())
	}
	if _, err := os.Stat(messagesPath(root, "de")); !os.IsNotExist(err) {
		t.Error("an import with nothing to import wrote a catalog")
	}
	if _, err := os.Stat(lockPath(root)); !os.IsNotExist(err) {
		t.Error("an import with nothing to import wrote the lock")
	}
}

func TestDecodeReplyForKeepsTheEntriesThatParse(t *testing.T) {
	en, err := ParseMessages([]byte(offlineEN))
	if err != nil {
		t.Fatal(err)
	}
	wanted := ItemsByKey([]Item{
		{Key: "feed_empty_title", English: en["feed_empty_title"]},
		{Key: "header_xp", English: en["header_xp"]},
	})
	got, complaints, err := DecodeReplyFor(`{"translations":[
	  {"key":"header_xp","text":[],"variants":[]},
	  {"key":"feed_empty_title","text":"Noch keine Beute.","variants":[]}
	]}`, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 1 {
		t.Errorf("complaints = %v, want one about the bad entry", complaints)
	}
	if got["feed_empty_title"].Text != "Noch keine Beute." {
		t.Errorf("the good entry was lost: %v", got)
	}
	if _, ok := got["header_xp"]; ok {
		t.Error("the bad entry should not have produced a value")
	}
}
