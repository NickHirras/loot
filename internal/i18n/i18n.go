// Package i18n reads the dashboard's message catalogs from Go.
//
// The catalogs are the same web/messages/<lang>.json files Paraglide compiles
// into the frontend, and the frontend is where nearly every string belongs.
// This package exists for the handful the server has to know: an achievement's
// title lives under a key the server only learns at runtime (the achievement's
// own key, which travels in the unlock event's payload), so when a drop's
// sentence is re-rendered in the reader's language the trophy's name has to be
// looked up rather than compiled in.
//
// Only plain string messages are answered. A message with plural or gender
// variants is a JSON object rather than a string, and rendering one correctly
// means implementing the variant selection Paraglide does on the client —
// which is a much larger promise than this package makes. Those return false,
// and the caller keeps whatever text it already had.
package i18n

import (
	"encoding/json"
	"io/fs"
	"strings"
	"sync"

	"github.com/nickhirras/loot/web"
)

// BaseLang is the language every catalog falls back to. It is the only one
// that is hand-written; the rest are translations of it.
const BaseLang = "en"

var (
	mu      sync.RWMutex
	loaded        = map[string]map[string]json.RawMessage{}
	sources fs.FS = web.MessagesFS()
)

// Lookup returns the message for key in lang.
//
// It falls back to English when the language has no catalog or no entry for
// the key, so a half-translated catalog shows English where it is thin rather
// than blanks. It returns false when the key exists nowhere, or when it exists
// only as a variant message.
func Lookup(lang, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if s, ok := lookupIn(lang, key); ok {
		return s, true
	}
	if !strings.EqualFold(lang, BaseLang) {
		return lookupIn(BaseLang, key)
	}
	return "", false
}

// Has reports whether a catalog for lang is available at all.
func Has(lang string) bool { return catalog(lang) != nil }

func lookupIn(lang, key string) (string, bool) {
	c := catalog(lang)
	if c == nil {
		return "", false
	}
	raw, ok := c[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// A variant message ({"match": {...}}), which this package does not
		// know how to select between.
		return "", false
	}
	return s, true
}

// catalog returns the parsed catalog for lang, reading it once. A language
// with no file (or an unreadable one) caches a nil so the miss costs one map
// read rather than one failed file open per drop.
func catalog(lang string) map[string]json.RawMessage {
	name := normalize(lang)
	if name == "" {
		return nil
	}

	mu.RLock()
	c, ok := loaded[name]
	mu.RUnlock()
	if ok {
		return c
	}

	mu.Lock()
	defer mu.Unlock()
	if c, ok := loaded[name]; ok {
		return c
	}
	var parsed map[string]json.RawMessage
	if data, err := fs.ReadFile(sources, "messages/"+name+".json"); err == nil {
		if err := json.Unmarshal(data, &parsed); err != nil {
			parsed = nil
		}
	}
	loaded[name] = parsed
	return parsed
}

// normalize turns a BCP-47 tag into a catalog file name. Paraglide's own files
// are lowercase-language + original-case region ("pt-BR"), so only the
// language half is folded, and anything that is not a plausible tag is
// rejected rather than turned into a path.
func normalize(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ""
	}
	for _, r := range lang {
		if r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') {
			continue
		}
		return ""
	}
	parts := strings.Split(strings.ReplaceAll(lang, "_", "-"), "-")
	parts[0] = strings.ToLower(parts[0])
	return strings.Join(parts, "-")
}
