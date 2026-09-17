package main

// The two catalogs, and the one value type they share.
//
// English is the only thing anybody writes by hand: web/messages/en.json for
// the dashboard and internal/rules/default.yaml for drop titles. Everything
// else in this package is a way of getting from one of those to the same words
// in another language, and back onto disk without churning the diff.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// schemaURL is the `$schema` line the inlang message format writes at the top
// of every catalog. It is editor support only — nothing validates against it —
// but leaving it out of a generated file would make the generated files look
// different from the hand-written one for no reason.
const schemaURL = "https://inlang.com/schema/inlang-message-format"

// Value is one translatable unit: either a plain string or a message with
// plural variants.
//
// A rule template is always plain; only the dashboard's catalog has variants.
type Value struct {
	// Text is the whole message, for a plain one.
	Text string
	// Variant is non-nil for a message with plural (or other) variants, in
	// which case Text is empty.
	Variant *Variant
}

// Variant is the inlang message format's "complex message": the declarations
// that name the inputs and the plural function, the selectors those
// declarations feed, and one pattern per combination of categories.
//
// Only Match is ever translated. Declarations and Selectors are machinery —
// they name variables the code passes in — so a translation keeps them exactly
// as English wrote them and only ever changes which categories Match covers.
// Field order is alphabetical because that is the order the inlang plugin
// itself writes them in, and a generated catalog that differs from a
// plugin-written one only in key order would churn the diff the first time
// anybody opened the project in an inlang editor.
type Variant struct {
	Declarations []string          `json:"declarations"`
	Match        map[string]string `json:"match"`
	Selectors    []string          `json:"selectors"`
}

// IsVariant reports whether v has plural variants.
func (v Value) IsVariant() bool { return v.Variant != nil }

// Patterns returns every string a reader could end up seeing: one for a plain
// message, one per match arm for a variant. Validators that care about the
// words rather than the structure walk this.
func (v Value) Patterns() []string {
	if v.Variant == nil {
		return []string{v.Text}
	}
	keys := make([]string, 0, len(v.Variant.Match))
	for k := range v.Variant.Match {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, v.Variant.Match[k])
	}
	return out
}

// Hash is the content hash the lock file records: sha256 over the value's
// canonical JSON.
//
// Canonical means what encoding/json already guarantees — map keys sorted,
// struct fields in declaration order — so the same value always hashes the
// same way, and any edit to the English source (including reordering a
// variant's declarations) shows up as a changed hash and re-translates.
func (v Value) Hash() string {
	var (
		b   []byte
		err error
	)
	if v.Variant != nil {
		b, err = json.Marshal(v.Variant)
	} else {
		b, err = json.Marshal(v.Text)
	}
	if err != nil {
		// json.Marshal of a string or of Variant cannot fail; hashing the
		// error text rather than panicking keeps a hypothetical failure
		// loud (nothing will ever match it) instead of fatal.
		b = []byte("unmarshalable:" + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Catalog is a set of translatable values keyed by a stable name.
//
// For messages the name is the message key ("feed_empty_title"). For rules it
// is "rules/<rule name>/title" or "fallback/subtitle" — a path rather than a
// flat key, because a rule's two fields are translated together and want to
// sort next to each other in the lock file.
type Catalog map[string]Value

// Keys returns the catalog's names in sorted order.
func (c Catalog) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- messages

// messagesPath is where a locale's dashboard catalog lives.
func messagesPath(root, locale string) string {
	return filepath.Join(root, "web", "messages", locale+".json")
}

// LoadMessages reads one web/messages/<locale>.json.
//
// A file that is not there is not an error: that is exactly the state of every
// locale before its first translation run, and the caller wants an empty
// catalog rather than a special case.
func LoadMessages(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Catalog{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseMessages(data)
}

// ParseMessages decodes an inlang message catalog.
func ParseMessages(data []byte) (Catalog, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse message catalog: %w", err)
	}
	out := Catalog{}
	for key, msg := range raw {
		if key == "$schema" {
			continue
		}
		v, err := parseMessageValue(msg)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", key, err)
		}
		out[key] = v
	}
	return out, nil
}

// parseMessageValue decodes one message: a bare string, or the single-element
// array the plugin uses to mark a message with variants.
func parseMessageValue(msg json.RawMessage) (Value, error) {
	trimmed := bytes.TrimSpace(msg)
	switch {
	case len(trimmed) == 0:
		return Value{}, fmt.Errorf("empty value")
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return Value{}, err
		}
		return Value{Text: s}, nil
	case trimmed[0] == '[':
		var arr []Variant
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return Value{}, err
		}
		if len(arr) != 1 {
			return Value{}, fmt.Errorf("expected exactly one variant object, got %d", len(arr))
		}
		v := arr[0]
		return Value{Variant: &v}, nil
	default:
		// Nested objects are legal in the plugin's format but Loot's catalog
		// is flat, and silently flattening one would put keys in the lock
		// file that no target file could round-trip.
		return Value{}, fmt.Errorf("unsupported message shape: nested objects are not used in this catalog")
	}
}

// WriteMessages writes a locale's catalog: sorted, two-space indent, one
// trailing newline, and `$schema` first the way the plugin writes it.
func WriteMessages(path string, c Catalog) error {
	data, err := MarshalMessages(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// MarshalMessages renders a catalog the way WriteMessages would.
func MarshalMessages(c Catalog) ([]byte, error) {
	doc := map[string]any{"$schema": schemaURL}
	for key, v := range c {
		if v.Variant != nil {
			doc[key] = []*Variant{v.Variant}
			continue
		}
		doc[key] = v.Text
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// The catalog is full of `→`, `·` and `&`; HTML-escaping them would make
	// a generated file that no human would ever have written by hand.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ------------------------------------------------------------------- rules

// Rule field names, and the pseudo-rule the fallback sentence is filed under.
const (
	fieldTitle    = "title"
	fieldSubtitle = "subtitle"
	fallbackName  = "fallback"
)

// rulesPath is the hand-written English rules file.
func rulesPath(root string) string {
	return filepath.Join(root, "internal", "rules", "default.yaml")
}

// overlayPath is where a language's rule overlay lives. The tag is written
// exactly as project.inlang/settings.json spells it, which is what
// internal/rules/locales.go parses back out of the file name.
func overlayPath(root, lang string) string {
	return filepath.Join(root, "internal", "rules", "locales", "default."+lang+".yaml")
}

// ruleKey names one translatable rule field in the catalog and the lock.
func ruleKey(rule, field string) string {
	if rule == fallbackName {
		return fallbackName + "/" + field
	}
	return "rules/" + rule + "/" + field
}

// then is the half of a rule this tool is allowed to touch. Rarity, XP and
// matching are decisions rather than words and never appear here.
type then struct {
	Title    string `yaml:"title,omitempty"`
	Subtitle string `yaml:"subtitle,omitempty"`
}

// ruleDoc is the shape of both default.yaml and an overlay, read for its words
// alone. Every other key in default.yaml is ignored.
type ruleDoc struct {
	Rules []struct {
		Name string `yaml:"name"`
		Then then   `yaml:"then"`
	} `yaml:"rules"`
	// Fallback parses both shapes: default.yaml writes `title:` directly under
	// `fallback:`, while an overlay may nest it under `then:` like every rule
	// entry. internal/rules/locales.go accepts both for the same reason.
	Fallback *struct {
		Then     then   `yaml:"then"`
		Title    string `yaml:"title"`
		Subtitle string `yaml:"subtitle"`
	} `yaml:"fallback"`
}

// RuleOrder is the order the rules appear in default.yaml. Overlays are
// written in this order rather than alphabetically: an overlay is read
// side-by-side with default.yaml, and source order keeps the RevenueCat block
// together. It is just as deterministic as sorting, which is what the diff
// cares about.
type RuleOrder []string

// LoadRules reads default.yaml into a catalog of translatable fields, and
// returns the order its rules appear in.
func LoadRules(path string) (Catalog, RuleOrder, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseRules(data)
}

// ParseRules decodes a rules file — default.yaml or an overlay — keeping only
// the titles and subtitles.
func ParseRules(data []byte) (Catalog, RuleOrder, error) {
	var doc ruleDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse rules: %w", err)
	}
	out := Catalog{}
	var order RuleOrder
	seen := map[string]bool{}
	for i, r := range doc.Rules {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			return nil, nil, fmt.Errorf("rule #%d has no name", i)
		}
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
		// An empty field is not a translatable unit: there is nothing to
		// translate and nothing for the overlay to say.
		if r.Then.Title != "" {
			out[ruleKey(name, fieldTitle)] = Value{Text: r.Then.Title}
		}
		if r.Then.Subtitle != "" {
			out[ruleKey(name, fieldSubtitle)] = Value{Text: r.Then.Subtitle}
		}
	}
	if doc.Fallback != nil {
		title := doc.Fallback.Then.Title
		if title == "" {
			title = doc.Fallback.Title
		}
		sub := doc.Fallback.Then.Subtitle
		if sub == "" {
			sub = doc.Fallback.Subtitle
		}
		if title != "" {
			out[ruleKey(fallbackName, fieldTitle)] = Value{Text: title}
		}
		if sub != "" {
			out[ruleKey(fallbackName, fieldSubtitle)] = Value{Text: sub}
		}
	}
	return out, order, nil
}

// LoadOverlay reads one internal/rules/locales/default.<lang>.yaml. A missing
// file is an empty catalog, as with messages.
func LoadOverlay(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Catalog{}, nil
	}
	if err != nil {
		return nil, err
	}
	c, _, err := ParseRules(data)
	return c, err
}

// overlayHeader explains, at the top of every generated overlay, where the
// file came from and what happens to a hand edit.
const overlayHeader = `# Loot default rules — %s.
#
# GENERATED by tools/translate. Do not restructure this file by hand; the next
# run rewrites it from internal/rules/default.yaml.
#
# Editing a translation *is* supported, and is the intended way to fix a
# wrong one: an entry you rewrite is left exactly as you wrote it until the
# English title or subtitle it translates changes, at which point it is
# retranslated. (tools/translate records a hash of the English source in
# i18n.lock.json and only touches entries whose hash moved.)
#
# An overlay is the translatable half of default.yaml and nothing else. Each
# entry names a rule from default.yaml and gives its title and subtitle in this
# language; rarity, XP and matching are decisions rather than words and live in
# default.yaml alone.
#
# Every ` + "`{{…}}`" + ` expression is the English one, because it renders against the
# same event. Reordering them around the sentence is fine and often necessary;
# changing what one says is not.
#
# An entry applies only while the rule it names still has default.yaml's own
# wording. Copy default.yaml, rewrite a title, and that title stays yours in
# every language — see internal/rules/locales.go.
`

// WriteOverlay writes a language's rule overlay.
func WriteOverlay(path, langName string, c Catalog, order RuleOrder) error {
	data, err := MarshalOverlay(langName, c, order)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// MarshalOverlay renders an overlay the way WriteOverlay would: the header,
// then the rules in default.yaml's order, then the fallback.
func MarshalOverlay(langName string, c Catalog, order RuleOrder) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}

	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, name := range order {
		title, hasTitle := c[ruleKey(name, fieldTitle)]
		sub, hasSub := c[ruleKey(name, fieldSubtitle)]
		if !hasTitle && !hasSub {
			continue
		}
		entry := &yaml.Node{Kind: yaml.MappingNode}
		// A rule name is an identifier, not prose: leave it unquoted, the way
		// default.yaml writes it.
		entry.Content = append(entry.Content, keyNode("name"), keyNode(name))
		thenNode := &yaml.Node{Kind: yaml.MappingNode}
		if hasTitle {
			appendScalar(thenNode, fieldTitle, title.Text)
		}
		if hasSub {
			appendScalar(thenNode, fieldSubtitle, sub.Text)
		}
		entry.Content = append(entry.Content,
			keyNode("then"), thenNode)
		seq.Content = append(seq.Content, entry)
	}
	if len(seq.Content) > 0 {
		root.Content = append(root.Content, keyNode("rules"), seq)
	}

	fbTitle, hasFbTitle := c[ruleKey(fallbackName, fieldTitle)]
	fbSub, hasFbSub := c[ruleKey(fallbackName, fieldSubtitle)]
	if hasFbTitle || hasFbSub {
		thenNode := &yaml.Node{Kind: yaml.MappingNode}
		if hasFbTitle {
			appendScalar(thenNode, fieldTitle, fbTitle.Text)
		}
		if hasFbSub {
			appendScalar(thenNode, fieldSubtitle, fbSub.Text)
		}
		fb := &yaml.Node{Kind: yaml.MappingNode}
		fb.Content = append(fb.Content, keyNode("then"), thenNode)
		root.Content = append(root.Content, keyNode(fallbackName), fb)
	}

	var body bytes.Buffer
	enc := yaml.NewEncoder(&body)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, overlayHeader, langName)
	out.WriteString("\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// keyNode is a plain scalar used as a mapping key.
func keyNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// appendScalar adds key: value to a mapping, quoting the value explicitly so
// the output does not depend on the emitter's guess about what needs quotes —
// every template starts with `{{`, which must be quoted anyway.
func appendScalar(m *yaml.Node, key, value string) {
	style := yaml.DoubleQuotedStyle
	if strings.Contains(value, `"`) && !strings.Contains(value, `'`) {
		// Single quotes keep a template's own "%v" readable instead of
		// turning it into \"%v\".
		style = yaml.SingleQuotedStyle
	}
	m.Content = append(m.Content,
		keyNode(key),
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: style, Value: value},
	)
}
