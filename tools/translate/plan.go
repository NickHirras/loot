package main

// The incremental diff: which keys, in which languages, actually need the API.

import (
	"fmt"
	"sort"
)

// Kind distinguishes the two catalogs.
type Kind string

const (
	// KindMessages is web/messages/<locale>.json, the dashboard's strings.
	KindMessages Kind = "messages"
	// KindRules is internal/rules/locales/default.<lang>.yaml, the drop
	// title and subtitle templates.
	KindRules Kind = "rules"
)

// Plan is one language's work in one catalog.
type Plan struct {
	Kind   Kind
	Locale string
	// Path is the target file, for the report.
	Path string
	// Translate are keys with no usable translation: never translated,
	// missing from the target file, or translated from an English source that
	// has since changed.
	Translate []string
	// Delete are keys the target file still has and English no longer does.
	Delete []string
	// Skip is the count left exactly as it is, hand edits included.
	Skip int
}

// Empty reports whether this plan would touch nothing.
func (p Plan) Empty() bool { return len(p.Translate) == 0 && len(p.Delete) == 0 }

// BuildPlan works out what to do for one language, given the English source,
// what the target file already says, and what the lock remembers.
//
// The rule is one line long: a key is left alone when the lock's hash equals
// the English hash *and* the target actually has the key. The first half is
// what lets a hand-corrected translation survive — nothing about the
// translation itself is inspected, only the English behind it. The second half
// is what recovers from a target file that was deleted, truncated or never
// written while the lock said otherwise.
func BuildPlan(kind Kind, locale, path string, english, target Catalog, lock *Lock, force bool) Plan {
	p := Plan{Kind: kind, Locale: locale, Path: path}
	for _, key := range english.Keys() {
		_, inTarget := target[key]
		if !force && inTarget {
			if h, ok := lock.Get(kind, locale, key); ok && h == english[key].Hash() {
				p.Skip++
				continue
			}
		}
		p.Translate = append(p.Translate, key)
	}
	for key := range target {
		if _, ok := english[key]; !ok {
			p.Delete = append(p.Delete, key)
		}
	}
	sort.Strings(p.Delete)
	return p
}

// String renders a plan as one line of `-dry-run` output.
func (p Plan) String() string {
	return fmt.Sprintf("%-8s %-8s translate %3d · skip %3d · delete %3d  (%s)",
		p.Kind, p.Locale, len(p.Translate), p.Skip, len(p.Delete), p.Path)
}

// Batches splits the keys to translate into requests of at most size keys.
// Batching exists to keep one bad response from costing a whole language, and
// to keep each response small enough that a truncated one is obvious.
func Batches(keys []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	var out [][]string
	for i := 0; i < len(keys); i += size {
		end := i + size
		if end > len(keys) {
			end = len(keys)
		}
		out = append(out, keys[i:end])
	}
	return out
}
