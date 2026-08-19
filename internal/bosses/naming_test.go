package bosses_test

import (
	"strings"
	"testing"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/core"
)

// A boss's name is regenerated from its key rather than migrated, so
// determinism is not a nicety: a name that drifted would rename a monster
// mid-fight.
func TestNameIsDeterministic(t *testing.T) {
	key := core.BossKey("playvitals", "com.example.app", "4.2.0", "")
	first := bosses.Name(key, "4.2.0", core.BossKindCrash)
	for i := 0; i < 50; i++ {
		if got := bosses.Name(key, "4.2.0", core.BossKindCrash); got != first {
			t.Fatalf("name %d = %q, want %q", i, got, first)
		}
	}
	if first == "" {
		t.Fatal("name is empty")
	}
}

// Two fights that differ in any part of the key must not collide, and the
// slots must be independent enough that a hundred keys do not all end up
// sharing a proper name.
func TestNameVariesWithKey(t *testing.T) {
	seen := map[string]int{}
	firstWord := map[string]int{}
	for _, app := range []string{"com.a.one", "com.b.two", "com.c.three", "com.d.four"} {
		for _, version := range []string{"1.0.0", "2.3.1", "4.2.0", "10.0.1", "4812"} {
			for _, issue := range []string{"", "abc123", "zz9"} {
				key := core.BossKey("playvitals", app, version, issue)
				name := bosses.Name(key, version, core.BossKindCrash)
				seen[name]++
				firstWord[strings.Fields(name)[0]]++
			}
		}
	}
	if len(seen) < 50 {
		t.Fatalf("only %d distinct names from 60 keys; the hash is not mixing", len(seen))
	}
	// "The" is a legitimate opener for two of the three forms, so it is
	// expected to dominate; no *other* opener may.
	for word, n := range firstWord {
		if word != "The" && n > 12 {
			t.Fatalf("opener %q used %d times out of 60; slots are correlated", word, n)
		}
	}
}

// An ANR is a different kind of failure from a crash and gets to say so.
func TestNameCallsANRsANRs(t *testing.T) {
	found := false
	for _, app := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		key := core.BossKey("playvitals", app, "1.0.0", "")
		if strings.Contains(bosses.Name(key, "1.0.0", core.BossKindANR), "ANR") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no ANR boss in eight tries was named as one")
	}
}

// No word list entry may be about the person who wrote the bug.
func TestNamesAreNeverMean(t *testing.T) {
	banned := []string{"sloppy", "careless", "lazy", "stupid", "idiot", "amateur", "incompetent"}
	for _, app := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
			name := strings.ToLower(bosses.Name(core.BossKey("s", app, v, ""), v, core.BossKindCrash))
			for _, word := range banned {
				if strings.Contains(name, word) {
					t.Fatalf("name %q contains %q", name, word)
				}
			}
		}
	}
}

func TestVersionLabel(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"2.3.1":  "v2.3.1",
		"v2.3.1": "v2.3.1",
		"4812":   "build 4812",
		"beta":   "vbeta",
		"1-rc2":  "v1-rc2",
	}
	for in, want := range cases {
		if got := bosses.VersionLabel(in); got != want {
			t.Errorf("VersionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
