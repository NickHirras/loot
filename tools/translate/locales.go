package main

// Which languages exist, what they are called, and which plural categories
// they use.
//
// project.inlang/settings.json is the single source of truth for the list: the
// dashboard's catalogs and the rule overlays are keyed by exactly the tags it
// names. Adding a language is adding a tag there and running this tool.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BaseLocale is the language everything is written in and translated from.
const BaseLocale = "en"

// settingsPath is the inlang project file that lists the locales.
func settingsPath(root string) string {
	return filepath.Join(root, "web", "project.inlang", "settings.json")
}

// inlangSettings is the part of settings.json this tool reads.
type inlangSettings struct {
	BaseLocale string   `json:"baseLocale"`
	Locales    []string `json:"locales"`
}

// LoadLocales returns every locale except the base one, in the order
// settings.json lists them.
func LoadLocales(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s inlangSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	base := s.BaseLocale
	if base == "" {
		base = BaseLocale
	}
	if base != BaseLocale {
		return nil, fmt.Errorf("%s: baseLocale is %q, but this tool translates from %q", path, base, BaseLocale)
	}
	out := make([]string, 0, len(s.Locales))
	for _, l := range s.Locales {
		if l == base {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no locales to translate into", path)
	}
	return out, nil
}

// languageNames is what to call each locale in a prompt. A model translates
// better when told "Brazilian Portuguese" than when handed "pt-BR", and the
// distinction between pt-BR and pt is a real one.
var languageNames = map[string]string{
	"en":      "English",
	"de":      "German",
	"es":      "Spanish",
	"fr":      "French",
	"it":      "Italian",
	"pt-BR":   "Brazilian Portuguese",
	"ja":      "Japanese",
	"ko":      "Korean",
	"zh-Hans": "Simplified Chinese",
	"ru":      "Russian",
	"nl":      "Dutch",
}

// LanguageName returns a human name for a tag, falling back to the tag itself
// so an unknown language still translates rather than failing the run.
func LanguageName(locale string) string {
	if n, ok := languageNames[locale]; ok {
		return n
	}
	if base, _, ok := strings.Cut(locale, "-"); ok {
		if n, ok := languageNames[base]; ok {
			return n
		}
	}
	return locale
}

// pluralCategories is the set of CLDR plural categories each language actually
// uses, in CLDR's own order.
//
// This is the table the model is given and the table the validator checks
// against, and it has to agree with what `Intl.PluralRules` does in the
// browser, because that is what Paraglide compiles a variant message into. The
// values below are `new Intl.PluralRules(tag).resolvedOptions().pluralCategories`
// on current ICU data.
//
// The ones that surprise people: Spanish, Italian, Portuguese and French all
// have a `many` category (it fires on round millions — "1 millón"), and
// Japanese, Korean and Chinese have no plural at all, so a variant message in
// those languages is a single `other` arm.
var pluralCategories = map[string][]string{
	"en":      {"one", "other"},
	"de":      {"one", "other"},
	"nl":      {"one", "other"},
	"es":      {"one", "many", "other"},
	"fr":      {"one", "many", "other"},
	"it":      {"one", "many", "other"},
	"pt":      {"one", "many", "other"},
	"pt-BR":   {"one", "many", "other"},
	"ru":      {"one", "few", "many", "other"},
	"ja":      {"other"},
	"ko":      {"other"},
	"zh":      {"other"},
	"zh-Hans": {"other"},
	"zh-Hant": {"other"},
}

// PluralCategories returns the categories for a locale, falling back to the
// base language of a regional tag.
func PluralCategories(locale string) ([]string, error) {
	if cats, ok := pluralCategories[locale]; ok {
		return cats, nil
	}
	if base, _, ok := strings.Cut(locale, "-"); ok {
		if cats, ok := pluralCategories[base]; ok {
			return cats, nil
		}
	}
	return nil, fmt.Errorf("no CLDR plural categories known for %q: add it to pluralCategories in tools/translate/locales.go", locale)
}

// MatchKeys is every match arm a variant message must have in a language: the
// cross product of each selector's categories, joined the way the inlang
// message format writes it ("dropsPlural=one, chestsPlural=other").
//
// Selector order is the message's own, so the generated keys line up with the
// English ones arm for arm in the languages that have the same categories.
func MatchKeys(selectors, categories []string) []string {
	if len(selectors) == 0 {
		return nil
	}
	keys := []string{""}
	for _, sel := range selectors {
		next := make([]string, 0, len(keys)*len(categories))
		for _, prefix := range keys {
			for _, cat := range categories {
				part := sel + "=" + cat
				if prefix == "" {
					next = append(next, part)
					continue
				}
				next = append(next, prefix+", "+part)
			}
		}
		keys = next
	}
	return keys
}
