package main

// Validation: what has to be true of a translation before it is allowed onto
// disk.
//
// Everything here is mechanical. None of it judges whether the German is any
// good — it catches the specific ways a machine translation breaks software:
// a dropped `{placeholder}`, an invented one, a rewritten `{{.AmountFmt}}`, a
// plural message that lost the category the language actually needs, a
// sentence four times longer than the button it has to fit in.
//
// A key that fails is not written. The run continues for the rest, because one
// bad string is not a reason to leave a whole language stale.

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

// maxLengthRatio is how much longer than the English a translation may be,
// counted in runes. German and Russian genuinely run long; 4× is loose enough
// never to fire on real prose and tight enough to catch a model that answered
// with an explanation instead of a translation.
const maxLengthRatio = 4

// Problem is one thing wrong with one key.
type Problem struct {
	Kind    Kind
	Locale  string
	Key     string
	Message string
	// Warning marks something worth telling a human about that is not a
	// reason to throw the translation away — a pinned glossary term that did
	// not come through, say.
	Warning bool
}

func (p Problem) String() string {
	level := "error"
	if p.Warning {
		level = "warn"
	}
	return fmt.Sprintf("%s: %s/%s %s: %s", level, p.Kind, p.Locale, p.Key, p.Message)
}

// Validator checks one translation against its English source.
type Validator struct {
	Kind     Kind
	Locale   string
	Glossary *Glossary
	// Categories are the locale's CLDR plural categories.
	Categories []string
}

// NewValidator builds a validator for one language and catalog.
func NewValidator(kind Kind, locale string, g *Glossary) (*Validator, error) {
	cats, err := PluralCategories(locale)
	if err != nil {
		return nil, err
	}
	return &Validator{Kind: kind, Locale: locale, Glossary: g, Categories: cats}, nil
}

// Check validates one translated value. It returns every problem it finds
// rather than the first, so a report names all the work rather than one round
// of it. `ok` is false when any non-warning problem was found.
func (v *Validator) Check(key string, english, translated Value) (problems []Problem, ok bool) {
	add := func(format string, args ...any) {
		problems = append(problems, Problem{Kind: v.Kind, Locale: v.Locale, Key: key, Message: fmt.Sprintf(format, args...)})
	}
	warn := func(format string, args ...any) {
		problems = append(problems, Problem{Kind: v.Kind, Locale: v.Locale, Key: key, Message: fmt.Sprintf(format, args...), Warning: true})
	}

	// Shape first: everything below assumes the two values are the same kind
	// of thing.
	if english.IsVariant() != translated.IsVariant() {
		if english.IsVariant() {
			add("English has plural variants and the translation does not")
		} else {
			add("the translation has plural variants and the English does not")
		}
		return problems, false
	}

	if english.IsVariant() {
		v.checkVariantStructure(english.Variant, translated.Variant, add)
	}

	// Emptiness, per arm: a blank arm renders as a blank label.
	for _, pat := range translated.Patterns() {
		if strings.TrimSpace(pat) == "" {
			add("translation is empty")
			break
		}
	}

	// Length, over the whole value. A variant message with fewer arms than
	// English (Japanese, say) is legitimately shorter; only the ceiling is
	// checked.
	enLen := totalRunes(english)
	trLen := totalRunes(translated)
	if enLen > 0 && trLen > enLen*maxLengthRatio {
		add("translation is %d characters for %d of English (more than %d×)", trLen, enLen, maxLengthRatio)
	}

	switch v.Kind {
	case KindMessages:
		v.checkPlaceholders(english, translated, add)
	case KindRules:
		v.checkTemplates(english, translated, add)
	}

	v.checkGlossary(english, translated, warn)

	for _, p := range problems {
		if !p.Warning {
			return problems, false
		}
	}
	return problems, true
}

// checkVariantStructure enforces that a translated variant message is the same
// machine as the English one, pointed at this language's categories.
//
// Declarations and selectors name variables the calling code passes in, so
// they must be byte-identical. The match arms must be exactly the cross
// product of the selectors and this language's CLDR categories: no missing
// arm (Paraglide would have nothing to render), no arm in a category the
// language does not have (it could never be selected), and `other` always
// present because every language has it and it is the last resort.
func (v *Validator) checkVariantStructure(english, translated *Variant, add func(string, ...any)) {
	if !equalStrings(english.Declarations, translated.Declarations) {
		add("declarations changed: %v, want %v", translated.Declarations, english.Declarations)
	}
	if !equalStrings(english.Selectors, translated.Selectors) {
		add("selectors changed: %v, want %v", translated.Selectors, english.Selectors)
		// Without the English selectors there is nothing to build the
		// expected arms from.
		return
	}

	want := MatchKeys(english.Selectors, v.Categories)
	wantSet := map[string]bool{}
	for _, k := range want {
		wantSet[k] = true
	}
	var missing, extra []string
	for _, k := range want {
		if _, ok := translated.Match[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range translated.Match {
		if !wantSet[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(missing) > 0 {
		add("missing plural arms for %s: %s", v.Locale, strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		add("plural arms %s are not categories %s uses (%s)",
			strings.Join(extra, ", "), v.Locale, strings.Join(v.Categories, "/"))
	}
	hasOther := false
	for k := range translated.Match {
		if strings.HasSuffix(k, "=other") || strings.Contains(k, "=other,") {
			hasOther = true
			break
		}
	}
	if !hasOther && len(translated.Match) > 0 {
		add("no `other` arm: every language needs one as the last resort")
	}
}

// checkPlaceholders compares the `{name}` slots the dashboard fills in.
//
// A plain message must use exactly the English set. A variant message is
// looser by necessity: arms differ between languages, so the rule is that no
// arm may invent a placeholder English never had, and every arm must carry the
// placeholders that appear in *all* the English arms — the ones the sentence
// cannot be about without.
func (v *Validator) checkPlaceholders(english, translated Value, add func(string, ...any)) {
	if !english.IsVariant() {
		en := placeholders(english.Text)
		tr := placeholders(translated.Text)
		if missing := difference(en, tr); len(missing) > 0 {
			add("dropped placeholder(s) %s", strings.Join(missing, ", "))
		}
		if extra := difference(tr, en); len(extra) > 0 {
			add("invented placeholder(s) %s", strings.Join(extra, ", "))
		}
		return
	}

	enArms := english.Patterns()
	union := map[string]bool{}
	var required map[string]bool
	for _, arm := range enArms {
		set := setOf(placeholders(arm))
		for p := range set {
			union[p] = true
		}
		if required == nil {
			required = set
			continue
		}
		for p := range required {
			if !set[p] {
				delete(required, p)
			}
		}
	}
	for armKey, arm := range translated.Variant.Match {
		got := setOf(placeholders(arm))
		var missing []string
		for p := range required {
			if !got[p] {
				missing = append(missing, p)
			}
		}
		var extra []string
		for p := range got {
			if !union[p] {
				extra = append(extra, p)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			add("arm %q dropped placeholder(s) %s", armKey, strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			add("arm %q invented placeholder(s) %s", armKey, strings.Join(extra, ", "))
		}
	}
}

// checkTemplates compares the Go template expressions in a rule's title or
// subtitle.
//
// Two things are checked. The template must parse, because internal/rules
// refuses to load an overlay that does not and one bad entry would take the
// whole language with it. And the expressions must be the English ones: the
// same `{{.AmountFmt}}` and `{{if gt .Quantity 0}}`, the same number of times.
// They may be reordered — putting the verb last is half of what translating
// into German *is* — but not edited, because they render against an event that
// has not changed.
//
// `{{else}}` and `{{end}}` are excluded from that count. They are punctuation
// rather than expressions: German needs an extra `{{else}}` to say
// Verkauf/Verkäufe where English got away with appending an "s", and that is a
// translation doing its job, not one editing the data. text/template's own
// parse is what keeps them balanced.
func (v *Validator) checkTemplates(english, translated Value, add func(string, ...any)) {
	if _, err := template.New("t").Option("missingkey=zero").Parse(translated.Text); err != nil {
		add("template does not parse: %v", err)
		return
	}
	en := countActions(english.Text)
	tr := countActions(translated.Text)
	for action, want := range en {
		if got := tr[action]; got != want {
			add("expression %s appears %d time(s), want %d", action, got, want)
		}
	}
	for action, got := range tr {
		if _, ok := en[action]; !ok {
			add("expression %s is not in the English template (appears %d time(s))", action, got)
		}
	}
}

// checkGlossary warns when a term somebody pinned in glossary.yaml did not
// make it into a translation of a string that uses the English term.
//
// It is a warning, not an error. The glossary is a preference a human
// expressed about vocabulary; a translation that reached for a synonym is
// worth looking at, not worth discarding.
func (v *Validator) checkGlossary(english, translated Value, warn func(string, ...any)) {
	if v.Glossary == nil {
		return
	}
	enText := strings.Join(english.Patterns(), " ")
	trText := strings.Join(translated.Patterns(), " ")
	for _, term := range v.Glossary.Pinned(v.Locale) {
		// A capitalised term is a name — "Loot" — and only the name counts:
		// "no loot yet" is the common noun, and a translation of it that
		// does not say "Loot" is exactly right.
		if !containsWord(enText, term.Term, isCapitalised(term.Term)) {
			continue
		}
		want := term.Translations[v.Locale]
		if want == "" || strings.Contains(strings.ToLower(trText), strings.ToLower(want)) {
			continue
		}
		warn("glossary: %q should be rendered as %q", term.Term, want)
	}
}

// ---------------------------------------------------------------- helpers

// totalRunes is the length of everything a reader could see, in runes.
func totalRunes(v Value) int {
	n := 0
	for _, p := range v.Patterns() {
		n += utf8.RuneCountInString(p)
	}
	return n
}

// placeholders returns the `{…}` slots in a message pattern, in sorted order
// and without duplicates.
//
// It understands the two things the inlang format does with braces: `\{` is a
// literal brace and not a slot, and `{{` never occurs (a Go template is a rule
// template, not a message) but is skipped rather than half-matched if it does.
func placeholders(s string) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			// Skip the escaped character, whatever it is.
			i++
		case '{':
			if i+1 < len(s) && s[i+1] == '{' {
				i++
				continue
			}
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return sortedUnique(out)
			}
			token := s[i : i+end+1]
			if !seen[token] {
				seen[token] = true
				out = append(out, token)
			}
			i += end
		}
	}
	return sortedUnique(out)
}

// countActions counts each distinct `{{…}}` expression in a Go template,
// leaving out the bare `{{else}}` and `{{end}}` that only close a branch.
//
// The scan respects quoted strings, so `{{if ne (printf "%v" .X) "}}"}}`
// would not be cut in half — no template in default.yaml does that today, but
// a validator that silently mis-parses is worse than no validator.
func countActions(s string) map[string]int {
	out := map[string]int{}
	for _, a := range actions(s) {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(a, "{{"), "}}"))
		inner = strings.TrimPrefix(inner, "-")
		inner = strings.TrimSuffix(inner, "-")
		switch strings.TrimSpace(inner) {
		case "end", "else":
			continue
		}
		out[a]++
	}
	return out
}

// actions returns every `{{…}}` run in a template, in order.
func actions(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || i+1 >= len(s) || s[i+1] != '{' {
			continue
		}
		j := i + 2
		var quote byte
		for j < len(s) {
			c := s[j]
			if quote != 0 {
				if c == '\\' && quote == '"' {
					j += 2
					continue
				}
				if c == quote {
					quote = 0
				}
				j++
				continue
			}
			switch c {
			case '"', '\'', '`':
				quote = c
				j++
			case '}':
				if j+1 < len(s) && s[j+1] == '}' {
					out = append(out, s[i:j+2])
					i = j + 1
					j = len(s)
					continue
				}
				j++
			default:
				j++
			}
		}
	}
	return out
}

// isCapitalised reports whether s starts with an upper-case letter.
func isCapitalised(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// containsWord reports whether text contains term as a whole word, ignoring
// case unless matchCase is set. It is deliberately simple: the glossary holds
// single English nouns.
func containsWord(text, term string, matchCase bool) bool {
	lt, lterm := text, term
	if !matchCase {
		lt = strings.ToLower(text)
		lterm = strings.ToLower(term)
	}
	for i := 0; ; {
		j := strings.Index(lt[i:], lterm)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(lterm)
		if !isWordByte(lt, start-1) && !isWordByte(lt, end) {
			return true
		}
		i = start + 1
		if i >= len(lt) {
			return false
		}
	}
}

func isWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func setOf(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// difference returns the members of a that are not in b.
func difference(a, b []string) []string {
	have := setOf(b)
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

func sortedUnique(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	sort.Strings(ss)
	return ss
}
