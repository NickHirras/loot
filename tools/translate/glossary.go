package main

// The glossary: Loot's own vocabulary, and the place to pin how a word should
// be said in a language.
//
// Most entries ship with every translation blank, and that is deliberate — on
// the first run the model picks, because it knows more about what sounds
// natural in Korean than this file does. The file exists for afterwards: when
// somebody decides that a "drop" is a *Fund* in German and not a *Drop*, they
// write it down here once, and every language's translation of every string
// containing the word is checked against it from then on.

import (
	_ "embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// glossaryYAML is embedded so the tool works from any working directory —
// `go -C tools/translate run .` from the repository root, or `go run .` from
// inside it.
//
//go:embed glossary.yaml
var glossaryYAML []byte

// Term is one word of Loot's vocabulary.
type Term struct {
	// Term is the English word, as it appears in the catalogs.
	Term string `yaml:"term"`
	// Gloss says what it means in Loot, for the model's benefit. This is the
	// part that does the work on a first run.
	Gloss string `yaml:"gloss"`
	// Translations pins a rendering per locale. Empty means "your call".
	Translations map[string]string `yaml:"translations"`
}

// Glossary is the whole vocabulary.
type Glossary struct {
	Terms []Term `yaml:"terms"`
}

// LoadGlossary parses the embedded glossary.
func LoadGlossary() (*Glossary, error) {
	return ParseGlossary(glossaryYAML)
}

// ParseGlossary decodes a glossary from YAML.
func ParseGlossary(data []byte) (*Glossary, error) {
	var g Glossary
	if err := yaml.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("parse glossary: %w", err)
	}
	for i, t := range g.Terms {
		if t.Term == "" {
			return nil, fmt.Errorf("glossary entry #%d has no term", i)
		}
	}
	return &g, nil
}

// Pinned returns the terms that have a rendering fixed for this locale.
func (g *Glossary) Pinned(locale string) []Term {
	var out []Term
	for _, t := range g.Terms {
		if t.Translations[locale] != "" {
			out = append(out, t)
		}
	}
	return out
}

// Prompt renders the glossary for the system prompt: every term with its
// gloss, so the model knows what the word means here, and — for the terms
// somebody has pinned — the rendering it must use in each language.
//
// The whole glossary goes in the cached prefix rather than one language's
// slice of it, so every batch of every language shares the same cache entry.
func (g *Glossary) Prompt() string {
	var b []byte
	b = append(b, "Loot's vocabulary. These words carry specific meanings in this product:\n"...)
	for _, t := range g.Terms {
		b = append(b, "- "...)
		b = append(b, t.Term...)
		if t.Gloss != "" {
			b = append(b, ": "...)
			b = append(b, t.Gloss...)
		}
		locales := make([]string, 0, len(t.Translations))
		for l, v := range t.Translations {
			if v != "" {
				locales = append(locales, l)
			}
		}
		sort.Strings(locales)
		if len(locales) > 0 {
			b = append(b, "\n  Fixed renderings you MUST use:"...)
			for _, l := range locales {
				b = append(b, ' ')
				b = append(b, l...)
				b = append(b, '=')
				b = append(b, t.Translations[l]...)
				b = append(b, ';')
			}
		}
		b = append(b, '\n')
	}
	return string(b)
}
