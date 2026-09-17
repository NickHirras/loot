package main

// What the model is told.
//
// The system prompt is identical for every batch of every language — Loot,
// the voice, the hard rules, the glossary — so it is one cached prefix that
// every request in a run reads instead of re-paying for. Everything that
// varies (the target language, its plural categories, the keys themselves)
// goes in the user turn.

import (
	"fmt"
	"sort"
	"strings"
)

// systemPrompt explains the product, the voice and the rules that must not be
// broken. It is deliberately about *what the words are for*: a model that
// understands the chest is something you open once a day writes better button
// labels than one following a style guide.
const systemPrompt = `You are translating the user interface of Loot.

Loot is a warm, playful, self-hosted dashboard that turns an indie developer's
app-store metrics into a game. Sales, installs, subscriptions, reviews and
crashes arrive as *drops* with rarities, from common to legendary, plus
*cursed* for bad news. Yesterday's drops are held back and opened together as a
daily *chest*. Countries you have sold in become lit *settlements* on a slowly
turning globe, and a country's first ever sale founds one. There are *quests*
with deadlines, *mysteries* — odd days in the numbers that Loot noticed and
invites you to explain — *boss fights* built out of crash clusters, and a
*codex* of achievements. XP, levels and a daily streak run underneath it all.

## Voice

- Second person, addressing one developer looking at their own numbers.
- Terse. This is UI copy: labels, buttons, tooltips, short sentences. A
  translation that is noticeably longer than the English will break a layout.
- Honest and never shaming. A bad day is reported plainly, with no
  encouragement, no exclamation marks and no blame. "No loot yet." is the
  register — not "Don't give up!"
- Occasionally wry, in the game's vocabulary rather than in jokes. The fantasy
  framing is steady and quiet; it is never winked at.
- Use the register a game in the target language would use with a single
  player: informal second person where that is normal (German du, French tu,
  Spanish tú, Dutch je), plain polite forms in Japanese and Korean.

## Hard rules

These are not style preferences. Breaking one breaks the software, and the
translation will be rejected by a validator before it is written.

1. Keep every {placeholder} exactly as written, including its spelling and
   case: {count}, {countFmt}, {source}, {day}. Every placeholder in the English
   must appear in your translation, and you may not invent one. You MAY move a
   placeholder to wherever the sentence needs it.
2. Keep every {{…}} Go template expression byte-identical: {{.AmountFmt}},
   {{if gt .Quantity 0}}, {{if ne (printf "%v" .Payload.downloads) "1"}}. You
   may reorder them around the sentence — that is often the whole job — and you
   may add or remove a bare {{else}} or {{end}} when the target language needs
   a different branch structure for its plurals. You may never edit what an
   expression says or invent one that is not in the English.
3. Keep every ·, →, ↗, emoji, digit and number exactly as it is.
4. Do not translate product names: App Store, Google Play, Microsoft Store,
   Snapcraft, Flathub, RevenueCat, GitHub, Sentry, Play vitals. Do not
   translate "Loot".
5. UI strings must stay short. Prefer the shorter of two good renderings.

## Plural messages

Some messages have plural variants. You will be given the English arms keyed by
a match expression, and the exact set of match keys the target language needs —
which is its CLDR plural categories, not English's. Return exactly those arms
and no others:

- Some languages need fewer arms than English. Japanese, Korean and Chinese
  have only "other": collapse the English arms into one natural sentence.
- Some need more. Russian needs one/few/many/other; French, Spanish, Italian
  and Portuguese need a "many" arm (it fires on round millions).
- Every arm must be a complete, natural sentence in its own right. Do not write
  an arm that only makes sense read next to another one.

## Output

Return one entry per key you were given, and nothing else — no commentary, no
keys you were not asked about. For a plain message, fill "text" and leave
"variants" empty. For a plural message, leave "text" empty and fill "variants"
with exactly one entry per requested match key.`

// Item is one key handed to the model.
type Item struct {
	Key string
	// Hint is a one-line note about what the key is for, derived from its area
	// prefix. A key name carries most of the context; this carries the rest.
	Hint string
	// English is the source value.
	English Value
	// MatchKeys are the arms a variant message must come back with, already
	// resolved for the target language.
	MatchKeys []string
}

// BatchRequest is one call's worth of work.
type BatchRequest struct {
	Kind Kind
	// Locale is the BCP-47 tag, LanguageName its human name.
	Locale       string
	LanguageName string
	// Categories are the target language's CLDR plural categories.
	Categories []string
	Items      []Item
}

// Translator turns a batch of English into a batch of the target language.
//
// The interface exists so the whole pipeline — plan, translate, validate,
// write, lock — can be run in a test against canned answers, without a network
// and without a key.
type Translator interface {
	Translate(req BatchRequest) (map[string]Value, error)
}

// areaHints maps a key's prefix to a line of context. Key names in this
// catalog are `area_thing`, and the area is usually enough on its own; these
// fill in the cases where it is not — "vessel" and "hearth" mean nothing
// without Loot, and "scope" could be half a dozen things.
var areaHints = map[string]string{
	"ach":       "A Codex achievement: its name (_title) or its one-line description (_desc). Trophy names are short and evocative.",
	"app":       "Application chrome: the page's own controls, sound prompt and footer.",
	"boss":      "A boss fight: a named crash cluster with a health bar.",
	"breakdown": "A breakdown table: one metric split by source, app or country.",
	"chest":     "The daily chest: the drops it holds, the countdown to it, and opening it.",
	"codex":     "The Codex tab: the collection of achievements, its filters and its progress.",
	"delta":     "A change against the previous period, shown beside a number.",
	"dev":       "Developer tools for firing synthetic drops. Seen only by someone testing Loot.",
	"drop":      "A single drop in the feed: its label, rarity and XP.",
	"era":       "An era name: the settlement's stage of growth (Camp → Dynasty).",
	"feed":      "The live feed of drops, and its empty and loading states.",
	"globe":     "The globe on the Hearth tab: settlements, arriving arcs, the fleet at sea.",
	"header":    "The dashboard header: level, XP, streak, and how many sources are connected.",
	"hearth":    "The Hearth tab: the map of everywhere you have sold, and its ambient mode.",
	"metric":    "The name of a measured quantity (revenue, units, installs). A column heading.",
	"mystery":   "A mystery: an odd day in the numbers, its evidence, and the explanation the user writes.",
	"quest":     "A single quest: its title, its progress and its deadline.",
	"quests":    "The Quests tab: the list, its filters and the form for adding one.",
	"rarity":    "A rarity name. Use the word a game in this language would use.",
	"recap":     "A written recap of a week or a month: headline, highlights, and how it compared.",
	"scope":     "The app scope picker: which of your apps the dashboard is showing.",
	"spark":     "A sparkline: a tiny inline chart and its accessible label.",
	"stat":      "A stat tile: one number with a label.",
	"tab":       "A navigation tab name. One or two words.",
	"tier":      "A settlement tier (outpost → metropolis) or an achievement tier (bronze → legendary).",
	"time":      "A short relative time, often a single letter beside a number (3d, 2h).",
	"vault":     "The Vault tab: revenue, units, refunds and the charts over them.",
	"vessel":    "A ship's name on the globe, one per store. These are jokes; keep them jokes.",
}

// HintFor returns the one-line hint for a key, or "" when its name says
// enough.
func HintFor(kind Kind, key string) string {
	if kind == KindRules {
		// A rule template is a whole drop headline; the key is the rule's own
		// name, which says what triggered it.
		switch {
		case strings.HasSuffix(key, "/"+fieldTitle):
			return "A drop's headline, written when the event arrived. One line, no full stop."
		case strings.HasSuffix(key, "/"+fieldSubtitle):
			return "The smaller second line under a drop's headline: the numbers or the source."
		}
		return ""
	}
	area, _, ok := strings.Cut(key, "_")
	if !ok {
		return ""
	}
	return areaHints[area]
}

// userPrompt renders the per-batch turn: the target language, its plural
// categories, and the keys with their English.
func userPrompt(req BatchRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Translate into %s (%s).\n\n", req.LanguageName, req.Locale)
	fmt.Fprintf(&b, "Plural categories for %s: %s.\n", req.Locale, strings.Join(req.Categories, ", "))

	switch req.Kind {
	case KindRules:
		b.WriteString(`
These are Go text/template strings that render a drop's headline the moment an
event arrives. The {{…}} expressions are filled in from that event: {{.App}} is
the app's name, {{.SourceName}} the store's, {{.AmountFmt}} and {{.QuantityFmt}}
pre-formatted money and counts, {{.Payload.…}} fields from the store's own
payload. Keep every expression exactly as written and put it where the sentence
needs it.

`)
	default:
		b.WriteString("\nThese are dashboard strings. Key names carry their area as a prefix.\n\n")
	}

	fmt.Fprintf(&b, "%d key(s):\n\n", len(req.Items))
	for _, it := range req.Items {
		fmt.Fprintf(&b, "### %s\n", it.Key)
		if it.Hint != "" {
			fmt.Fprintf(&b, "context: %s\n", it.Hint)
		}
		if it.English.IsVariant() {
			b.WriteString("plural message. English arms:\n")
			for _, arm := range sortedMatchKeys(it.English.Variant) {
				fmt.Fprintf(&b, "  %s = %s\n", arm, quote(it.English.Variant.Match[arm]))
			}
			fmt.Fprintf(&b, "return exactly these arms: %s\n", strings.Join(it.MatchKeys, " | "))
		} else {
			fmt.Fprintf(&b, "english: %s\n", quote(it.English.Text))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// quote renders a source string on one line, visibly delimited, so trailing
// spaces and empty strings are not invisible in the prompt.
func quote(s string) string { return "«" + s + "»" }

// sortedMatchKeys lists a variant's arms in a stable order.
func sortedMatchKeys(v *Variant) []string {
	out := make([]string, 0, len(v.Match))
	for k := range v.Match {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
