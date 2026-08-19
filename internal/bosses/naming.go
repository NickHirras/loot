package bosses

import (
	"strings"

	"github.com/nickhirras/loot/internal/core"
)

// Naming a boss.
//
// The name is the whole trick. "Crash cluster #4812 in 2.3.1" is a chore;
// "The Null-Dereferencing Wyrm of v2.3.1" is a thing you want to kill. So
// every fight gets a name generated deterministically from its key: the same
// crash always has the same name, on every machine, forever, with no state and
// no round trip.
//
// The word lists are chosen to be ominous about the *bug* and never about the
// person who wrote it. There is no "Sloppy", no "Careless", no "Idiot" — the
// monster is the crash, and the developer is the one holding the sword.

// adjectives describe what the crash does. Half are jokes a programmer will
// recognize; the rest are ordinary fantasy menace, so the list does not read
// as one long in-joke.
var adjectives = []string{
	"Null-Dereferencing", "Segfaulting", "Off-By-One", "Unhandled", "Recursive",
	"Deadlocked", "Stack-Smashing", "Race-Conditioned", "Unbounded", "Flickering",
	"Thundering", "Cascading", "Silent", "Immortal", "Ravenous",
	"Restless", "Gilded", "Obstinate", "Wayward", "Spectral",
	"Untyped", "Unclosed", "Detached", "Starving", "Reentrant",
}

// creatures are what the crash *is*. Every one of them is something a party
// can plausibly defeat: no gods, no elder horrors, nothing unkillable.
var creatures = []string{
	"Wyrm", "Hydra", "Lich", "Golem", "Basilisk",
	"Gargoyle", "Kraken", "Chimera", "Behemoth", "Revenant",
	"Wraith", "Manticore", "Banshee", "Leviathan", "Direwolf",
	"Djinn", "Troll", "Griffin", "Ogre", "Warden",
}

// propers are the ones grand enough to have been given a name. They are all
// invented, so none of them can be somebody's handle.
var propers = []string{
	"Grimjaw", "Vexmoor", "Thrangul", "Morrowix", "Karrowyn",
	"Balgrund", "Duskrend", "Nyxhollow", "Skarn", "Ulgrath",
	"Vorlath", "Zephrys", "Malduin", "Rendwick", "Torbrak",
	"Ashvane", "Cindergore", "Hollowfen", "Marrowgast", "Sablewing",
}

// hash is FNV-1a over the key. Any stable hash would do; this one is four
// lines and needs no import.
//
// Each slot of the name is drawn from its own *salted* hash rather than from
// different bit ranges of one. Bit slicing looked fine and was not: two
// unrelated keys landed on the same proper name often enough that the demo's
// two bosses were both called Ulgrath, which is exactly the kind of thing that
// makes generated names feel generated.
func hash(key string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return h
}

// Name returns the monster name for a fight. It depends only on its arguments:
// the same key, version and kind always produce the same name, which is what
// lets the name be regenerated rather than migrated.
//
// version is rendered as-is when it already looks like a version ("2.3.1"
// becomes "v2.3.1"); Play's numeric version codes are passed through as
// "build 4812" so the name does not claim to know a marketing version it was
// never told.
func Name(key, version, kind string) string {
	adjective := adjectives[hash(key+"|adjective")%uint64(len(adjectives))]
	creature := creatures[hash(key+"|creature")%uint64(len(creatures))]
	proper := propers[hash(key+"|proper")%uint64(len(propers))]
	form := hash(key+"|form") % 3
	h := hash(key)

	// An ANR is Android's own species of failure and deserves to be called one
	// — it is the difference between "why did it die?" and "why did it hang?".
	if strings.EqualFold(kind, core.BossKindANR) && h%2 == 0 {
		adjective = "ANR"
	}

	label := VersionLabel(version)
	switch {
	case form == 0 && label != "":
		return "The " + adjective + " " + creature + " of " + label
	case form == 1:
		return proper + " the " + adjective + " " + creature
	default:
		return "The " + adjective + " " + creature
	}
}

// VersionLabel renders a version for a title. An empty version renders empty,
// a dotted version gains a "v", and a bare number is called a build, because
// that is what Play's versionCode is.
func VersionLabel(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, ".-") {
		if strings.HasPrefix(strings.ToLower(v), "v") {
			return v
		}
		return "v" + v
	}
	if isDigits(v) {
		return "build " + v
	}
	if strings.HasPrefix(strings.ToLower(v), "v") {
		return v
	}
	return "v" + v
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
