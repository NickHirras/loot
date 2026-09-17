package rules

// Drops in the reader's language.
//
// A drop's title is a sentence Loot wrote at ingest, from a template in the
// rules file, and it is stored as text — which is the right thing for a
// record of what happened, and the wrong thing for a dashboard two people
// read in two languages. So the sentence is written down once in English and
// re-rendered on the way out:
//
//   - `Classify` records which rule wrote the drop (and which floor rule
//     relabelled it, and what language it rendered in) alongside the text;
//   - an *overlay* — locales/default.<lang>.yaml — holds nothing but the
//     translated title and subtitle templates, keyed by rule name;
//   - `Localize` re-runs the overlay's templates against the original event
//     when a reader asks for a language the stored text is not in.
//
// Nothing is ever rewritten in the database. A reader who asks for no
// language, a drop from before Loot recorded its rule, and a rule the reader's
// overlay has never heard of all see exactly the stored English text.
//
// The one thing an overlay may not do is overwrite *your* words. An overlay
// entry applies only when the rule it names still has the same template text
// the embedded defaults ship: copy default.yaml, rewrite one title, and that
// title stays yours in every language while the rules you left alone still
// translate. See `saysTheDefault`.

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/i18n"
)

// localeFS holds one overlay per translated language. They are generated from
// default.yaml, so an overlay never invents a rule: it only restates the title
// and subtitle of one that is already there.
//
//go:embed locales/*.yaml
var localeFS embed.FS

// overlayPrefix and overlaySuffix bracket the language in a locale file name:
// locales/default.de.yaml is German.
const (
	overlayPrefix = "default."
	overlaySuffix = ".yaml"
)

// OverlayRule is one rule's translated text. Only Then.Title and
// Then.Subtitle are read; an overlay never touches rarity, XP or matching,
// which are decisions rather than words.
type OverlayRule struct {
	Name string `yaml:"name"`
	Then Then   `yaml:"then"`
}

// OverlayFallback is the translated fallback sentence. Both shapes parse — a
// bare `title:`/`subtitle:` pair, as default.yaml's own fallback is written,
// and a `then:` block, as every rule entry is — because an overlay is
// generated and read far more often than it is hand-edited, and being strict
// about which of the two a generator picked would buy nothing.
type OverlayFallback struct {
	Then     Then   `yaml:"then"`
	Title    string `yaml:"title"`
	Subtitle string `yaml:"subtitle"`
}

// text resolves the two shapes into one.
func (f OverlayFallback) text() Then {
	t := f.Then
	if t.Title == "" {
		t.Title = f.Title
	}
	if t.Subtitle == "" {
		t.Subtitle = f.Subtitle
	}
	return t
}

// Overlay is a parsed locales/default.<lang>.yaml: the translatable half of
// the default rules, and nothing else.
type Overlay struct {
	Rules    []OverlayRule    `yaml:"rules"`
	Fallback *OverlayFallback `yaml:"fallback"`
}

// ParseOverlay reads an overlay from YAML bytes.
func ParseOverlay(data []byte) (Overlay, error) {
	var o Overlay
	if err := yaml.Unmarshal(data, &o); err != nil {
		return o, fmt.Errorf("parse overlay: %w", err)
	}
	return o, nil
}

// compiledOverlay is an Overlay with its templates parsed, keyed exactly as
// Engine.templates is: "<rule>/title", "<rule>/subtitle".
type compiledOverlay struct {
	templates map[string]*template.Template
}

// AddOverlay compiles o and registers it as the translation of the default
// rules into lang. A template that will not parse fails here rather than at
// the first drop that would have used it.
func (e *Engine) AddOverlay(lang string, o Overlay) error {
	lang = normalizeLang(lang)
	if lang == "" {
		return fmt.Errorf("overlay: empty language")
	}

	co := &compiledOverlay{templates: map[string]*template.Template{}}
	compile := func(ruleName, field, text string) error {
		if text == "" {
			return nil
		}
		key := ruleName + "/" + field
		t, err := template.New(key).Option("missingkey=zero").Parse(text)
		if err != nil {
			return fmt.Errorf("overlay %s: rule %q: bad %s template: %w", lang, ruleName, field, err)
		}
		co.templates[key] = t
		return nil
	}

	for i, r := range o.Rules {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			return fmt.Errorf("overlay %s: rule #%d has no name", lang, i)
		}
		if err := compile(name, "title", r.Then.Title); err != nil {
			return err
		}
		if err := compile(name, "subtitle", r.Then.Subtitle); err != nil {
			return err
		}
	}
	if o.Fallback != nil {
		t := o.Fallback.text()
		if err := compile(fallbackRule, "title", t.Title); err != nil {
			return err
		}
		if err := compile(fallbackRule, "subtitle", t.Subtitle); err != nil {
			return err
		}
	}

	if e.overlays == nil {
		e.overlays = map[string]*compiledOverlay{}
	}
	e.overlays[lang] = co
	return nil
}

// LoadOverlays registers every embedded overlay. A file that will not parse is
// logged and skipped: a broken translation of one language must never keep
// Loot from starting in the others, let alone in English.
func (e *Engine) LoadOverlays() {
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		lang := overlayLang(name)
		if lang == "" {
			continue
		}
		data, err := localeFS.ReadFile("locales/" + name)
		if err != nil {
			e.log().Error("could not read rules overlay", "file", name, "error", err)
			continue
		}
		o, err := ParseOverlay(data)
		if err != nil {
			e.log().Error("skipping rules overlay", "file", name, "error", err)
			continue
		}
		if err := e.AddOverlay(lang, o); err != nil {
			e.log().Error("skipping rules overlay", "file", name, "error", err)
		}
	}
}

// overlayLang extracts "de" from "default.de.yaml", or "" from a file name
// that is not an overlay.
func overlayLang(file string) string {
	if !strings.HasPrefix(file, overlayPrefix) || !strings.HasSuffix(file, overlaySuffix) {
		return ""
	}
	return normalizeLang(file[len(overlayPrefix) : len(file)-len(overlaySuffix)])
}

// Languages lists the languages this engine can re-render a default drop into,
// sorted the way the files are read. English is not among them: English is
// what the drops are already stored in.
func (e *Engine) Languages() []string {
	out := make([]string, 0, len(e.overlays))
	for lang := range e.overlays {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// HasOverlay reports whether this engine can render drops in lang.
func (e *Engine) HasOverlay(lang string) bool {
	return e.overlays[normalizeLang(lang)] != nil
}

// SetLanguage fixes the language new drops are *written* in, the way
// SetDisplayCurrency fixes the currency they are denominated in. It is set
// from the `language` config key when that names a fixed language, so that
// `loot tail` and the websocket — neither of which negotiates anything — speak
// it too. A language with no overlay leaves drops in English.
func (e *Engine) SetLanguage(lang string) {
	e.lang = normalizeLang(lang)
}

// Language is the language new drops are written in, or "" for English.
func (e *Engine) Language() string { return e.lang }

// Localize re-renders a stored drop's title and subtitle in lang.
//
// It returns the stored text unchanged, and false, whenever re-rendering is
// not possible or not wanted:
//
//   - lang is empty, English, or the language the drop was already written in;
//   - the drop predates Loot recording which rule wrote it (Rule == "");
//   - there is no overlay for lang;
//   - the rule that wrote the drop has been customised, so the words are the
//     operator's rather than the defaults' (see saysTheDefault);
//   - a floor rule relabelled the drop and the overlay does not have it, in
//     which case translating half the sentence would be worse than none.
//
// Otherwise it rebuilds exactly the data Classify rendered against — the
// event, its payload, the display currency — and re-runs the overlay's
// templates, including the floor pass, so the German drop is assembled the
// same way the English one was rather than pieced together from it.
//
// payload may be nil, in which case ev.Payload is decoded.
func (e *Engine) Localize(ctx context.Context, d core.Drop, ev core.Event, payload map[string]any, lang string) (string, string, bool) {
	_ = ctx // Localize touches no store: everything it needs is already here.

	lang = normalizeLang(lang)
	if lang == "" || lang == i18n.BaseLang || lang == normalizeLang(d.Lang) {
		return d.Title, d.Subtitle, false
	}
	if d.Rule == "" {
		return d.Title, d.Subtitle, false
	}
	ov := e.overlays[lang]
	if ov == nil {
		return d.Title, d.Subtitle, false
	}
	// Nothing to say in this language about the rule that wrote the drop —
	// either the overlay has never heard of it, or its words are the
	// operator's rather than the defaults'.
	if !e.translatable(ov, d.Rule) {
		return d.Title, d.Subtitle, false
	}
	// A relabelled drop is one sentence written by two rules, and the floor
	// rule's half is written *against* the other half (.BaseTitle). If the
	// overlay cannot supply both, leave the whole thing alone rather than
	// build a sentence half of which is quoting English at itself.
	if d.FloorRule != "" && !e.translatable(ov, d.FloorRule) {
		return d.Title, d.Subtitle, false
	}

	if payload == nil && len(ev.Payload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(ev.Payload, &m); err == nil {
			payload = m
		}
	}
	payload = localizePayload(ev, payload, lang)
	data := e.buildTmplData(ev, payload)

	// A field the overlay left out keeps the stored text: a translation may
	// legitimately be thin, and a German headline over an English subtitle is
	// still better than a blank one.
	title := d.Title
	subtitle := d.Subtitle
	if t, ok := e.renderOverlay(ov, d.Rule, "title", data); ok {
		title = t
	}
	if s, ok := e.renderOverlay(ov, d.Rule, "subtitle", data); ok {
		subtitle = s
	}

	if d.FloorRule != "" {
		// The floor pass, exactly as Classify runs it: the headline the
		// winning rule wrote becomes .BaseTitle, and a floor rule with no
		// subtitle of its own keeps that headline as context.
		data.BaseTitle = title
		floorTitle, hasTitle := e.renderOverlay(ov, d.FloorRule, "title", data)
		floorSub, _ := e.renderOverlay(ov, d.FloorRule, "subtitle", data)
		if hasTitle && floorTitle != "" {
			if floorSub == "" {
				floorSub = title
			}
			title = floorTitle
		}
		subtitle = floorSub
	}

	if title == "" {
		title = strings.TrimSpace(ev.Source + " " + ev.Kind)
	}
	return title, subtitle, true
}

// renderOverlay runs one overlay template. The second result is false when the
// overlay has nothing to say about that rule and field, which is different
// from a template that rendered to nothing.
func (e *Engine) renderOverlay(ov *compiledOverlay, ruleName, field string, data tmplData) (string, bool) {
	t, ok := ov.templates[ruleName+"/"+field]
	if !ok {
		return "", false
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", false
	}
	return strings.TrimSpace(buf.String()), true
}

// translatable reports whether the overlay may speak for ruleName: it must
// know the rule, and this engine's text for it must still be the default's.
func (e *Engine) translatable(ov *compiledOverlay, ruleName string) bool {
	_, hasTitle := ov.templates[ruleName+"/title"]
	_, hasSub := ov.templates[ruleName+"/subtitle"]
	if !hasTitle && !hasSub {
		return false
	}
	return e.saysTheDefault(ruleName)
}

// saysTheDefault reports whether this engine's title and subtitle for ruleName
// are byte-identical to the ones default.yaml ships.
//
// This is the whole guard against translating somebody else's words. A rules
// file of your own may reuse a default rule's name — copying default.yaml and
// editing it is the documented way to customise Loot — and the sentence under
// that name is then yours. Comparing the template text, rather than trusting
// the name, is what tells the two apart: your rewritten title stays exactly as
// you wrote it in every language, and the rules you did not touch still
// translate.
func (e *Engine) saysTheDefault(ruleName string) bool {
	want, ok := defaultText()[ruleName]
	if !ok {
		return false
	}
	got, ok := e.ruleText(ruleName)
	if !ok {
		return false
	}
	return got.Title == want.Title && got.Subtitle == want.Subtitle
}

// ruleText returns this engine's title and subtitle for one rule name.
func (e *Engine) ruleText(ruleName string) (Then, bool) {
	if ruleName == fallbackRule {
		if e.cfg.Fallback == nil {
			return Then{}, false
		}
		return *e.cfg.Fallback, true
	}
	for i := range e.cfg.Rules {
		if e.cfg.Rules[i].Name == ruleName {
			return e.cfg.Rules[i].Then, true
		}
	}
	return Then{}, false
}

var (
	defaultTextOnce sync.Once
	defaultTextMap  map[string]Then
)

// defaultText is the embedded defaults' title and subtitle per rule name,
// parsed once. A default.yaml that will not parse is a bug that every other
// test in this package would catch, so the failure is simply an empty map:
// nothing translates, and nothing breaks.
func defaultText() map[string]Then {
	defaultTextOnce.Do(func() {
		defaultTextMap = map[string]Then{}
		cfg, err := Parse(DefaultYAML)
		if err != nil {
			return
		}
		for _, r := range cfg.Rules {
			if r.Name != "" {
				defaultTextMap[r.Name] = r.Then
			}
		}
		if cfg.Fallback != nil {
			defaultTextMap[fallbackRule] = *cfg.Fallback
		}
	})
	return defaultTextMap
}

// localizePayload translates the parts of an event payload that are words
// rather than data, returning a copy when it changes anything.
//
// Today that is one thing: an achievement unlock carries the trophy's English
// title and description, and the rule that turns it into a drop is
// "Achievement: {{.Payload.title}}". The trophy's real name lives in the
// dashboard's message catalog under the achievement's own key, which is how
// the Codex page shows it translated, so the drop looks it up the same way.
//
// A quest completion's payload title is left alone: a quest title is either
// something you typed, which is not ours to translate, or an auto-generated
// one the dashboard already renders client-side.
func localizePayload(ev core.Event, payload map[string]any, lang string) map[string]any {
	if ev.Kind != core.KindAchievement || payload == nil {
		return payload
	}
	key, _ := payload["key"].(string)
	if key == "" {
		return payload
	}

	out := payload
	copied := false
	for field, suffix := range map[string]string{"title": "_title", "description": "_desc"} {
		text, ok := i18n.Lookup(lang, "ach_"+key+suffix)
		if !ok || text == "" {
			continue
		}
		if cur, _ := out[field].(string); cur == text {
			continue
		}
		if !copied {
			out = make(map[string]any, len(payload))
			for k, v := range payload {
				out[k] = v
			}
			copied = true
		}
		out[field] = text
	}
	return out
}

// normalizeLang folds a BCP-47 tag to the form overlays are keyed by: lowercase
// language, region left as written ("pt-BR"), underscores accepted.
func normalizeLang(lang string) string {
	lang = strings.TrimSpace(strings.ReplaceAll(lang, "_", "-"))
	if lang == "" {
		return ""
	}
	parts := strings.Split(lang, "-")
	parts[0] = strings.ToLower(parts[0])
	return strings.Join(parts, "-")
}

// log is where an overlay that will not load complains. Loading happens once
// at startup, so the engine takes no logger of its own for it.
func (e *Engine) log() *slog.Logger { return slog.Default() }
