package main

import (
	"reflect"
	"testing"
)

// english is the fixture source catalog the plan tests diff against.
func englishFixture() Catalog {
	return Catalog{
		"a_unchanged": {Text: "Unchanged"},
		"a_edited":    {Text: "Edited in English"},
		"a_new":       {Text: "Brand new"},
		"a_missing":   {Text: "Translated once, then the file lost it"},
	}
}

// lockedFor builds a lock that is up to date for the named keys.
func lockedFor(locale string, english Catalog, keys ...string) *Lock {
	l := NewLock()
	for _, k := range keys {
		l.Set(KindMessages, locale, k, english[k].Hash())
	}
	return l
}

func TestBuildPlanIncremental(t *testing.T) {
	english := englishFixture()

	// The target has three of the four keys. One of them was translated from
	// an older English ("a_edited"), one is a hand correction of the current
	// English ("a_unchanged"), and one has no lock entry at all ("a_new").
	target := Catalog{
		"a_unchanged": {Text: "Unverändert — von Hand korrigiert"},
		"a_edited":    {Text: "Alte Übersetzung"},
		"a_new":       {Text: "Nagelneu"},
		"a_stale":     {Text: "Schlüssel, den es im Englischen nicht mehr gibt"},
	}
	lock := lockedFor("de", english, "a_unchanged", "a_missing")
	lock.Set(KindMessages, "de", "a_edited", (Value{Text: "the older English"}).Hash())

	p := BuildPlan(KindMessages, "de", "de.json", english, target, lock, false)

	want := []string{"a_edited", "a_missing", "a_new"}
	if !reflect.DeepEqual(p.Translate, want) {
		t.Errorf("translate = %v, want %v", p.Translate, want)
	}
	if p.Skip != 1 {
		t.Errorf("skip = %d, want 1 (the hand-corrected key whose English has not moved)", p.Skip)
	}
	if !reflect.DeepEqual(p.Delete, []string{"a_stale"}) {
		t.Errorf("delete = %v, want [a_stale]", p.Delete)
	}
}

func TestBuildPlanHandEditSurvives(t *testing.T) {
	english := englishFixture()
	target := Catalog{"a_unchanged": {Text: "etwas ganz anderes"}}
	lock := lockedFor("de", english, "a_unchanged")

	p := BuildPlan(KindMessages, "de", "de.json", english, target, lock, false)
	for _, k := range p.Translate {
		if k == "a_unchanged" {
			t.Fatal("a hand-edited translation must be left alone while its English is unchanged")
		}
	}
}

func TestBuildPlanRetranslatesWhenTargetLostTheKey(t *testing.T) {
	english := englishFixture()
	// The lock says it was translated; the file says otherwise. The file wins.
	lock := lockedFor("de", english, "a_unchanged")
	p := BuildPlan(KindMessages, "de", "de.json", english, Catalog{}, lock, false)
	if len(p.Translate) != len(english) {
		t.Errorf("translate = %v, want all four keys", p.Translate)
	}
}

func TestBuildPlanForce(t *testing.T) {
	english := englishFixture()
	target := Catalog{"a_unchanged": {Text: "Unverändert"}}
	lock := lockedFor("de", english, "a_unchanged")

	p := BuildPlan(KindMessages, "de", "de.json", english, target, lock, true)
	if len(p.Translate) != len(english) || p.Skip != 0 {
		t.Errorf("-force should retranslate everything: translate=%v skip=%d", p.Translate, p.Skip)
	}
}

func TestBuildPlanEmptyWhenEverythingIsCurrent(t *testing.T) {
	english := englishFixture()
	target := Catalog{}
	lock := NewLock()
	for k, v := range english {
		target[k] = Value{Text: "x"}
		lock.Set(KindMessages, "de", k, v.Hash())
	}
	p := BuildPlan(KindMessages, "de", "de.json", english, target, lock, false)
	if !p.Empty() {
		t.Errorf("expected nothing to do, got translate=%v delete=%v", p.Translate, p.Delete)
	}
}

func TestLockKeepsTheTwoCatalogsApart(t *testing.T) {
	l := NewLock()
	l.Set(KindMessages, "de", "k", "hash-a")
	l.Set(KindRules, "de", "k", "hash-b")
	if h, _ := l.Get(KindMessages, "de", "k"); h != "hash-a" {
		t.Errorf("messages hash = %q", h)
	}
	if h, _ := l.Get(KindRules, "de", "k"); h != "hash-b" {
		t.Errorf("rules hash = %q", h)
	}
	l.Delete(KindMessages, "de", "k")
	if _, ok := l.Get(KindMessages, "de", "k"); ok {
		t.Error("delete did not remove the messages entry")
	}
	if _, ok := l.Get(KindRules, "de", "k"); !ok {
		t.Error("deleting a messages key must not touch the rules half")
	}
}

func TestLockMarshalIsDeterministic(t *testing.T) {
	l := NewLock()
	for _, k := range []string{"z", "a", "m", "b"} {
		l.Set(KindMessages, "de", k, "h-"+k)
		l.Set(KindRules, "fr", k, "h-"+k)
	}
	first, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := l.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
	want := `{
  "messages": {
    "de": {
      "a": "h-a",
      "b": "h-b",
      "m": "h-m",
      "z": "h-z"
    }
  },
  "rules": {
    "fr": {
      "a": "h-a",
      "b": "h-b",
      "m": "h-m",
      "z": "h-z"
    }
  }
}
`
	if string(first) != want {
		t.Errorf("lock file is not sorted/indented as expected:\n%s", first)
	}
}

func TestBatches(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	got := Batches(keys, 2)
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Batches = %v, want %v", got, want)
	}
	if n := len(Batches(nil, 40)); n != 0 {
		t.Errorf("no keys should mean no batches, got %d", n)
	}
	if n := len(Batches(keys, 0)); n != 5 {
		t.Errorf("a zero size must not divide by zero, got %d batches", n)
	}
}
