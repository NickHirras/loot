// Package rules turns an Event into a Drop. Classification is driven by an
// ordered YAML rule list: the first matching rule wins, then any "floor" rules
// may raise the rarity (that is how "a first-ever country is at least rare"
// works without duplicating every rule).
package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nickhirras/loot/internal/core"
)

// Lookup is the slice of the store the rules engine needs for rules that
// depend on history rather than on the event alone.
type Lookup interface {
	// CountryEventCount counts stored events for a country. Because
	// classification runs after the event is inserted, a count of 1 means the
	// event being classified is the first ever from there.
	CountryEventCount(ctx context.Context, country string) (int, error)
	// IsRecordQuantity reports whether the event's quantity beats every
	// previous event for the same source/app/kind.
	IsRecordQuantity(ctx context.Context, ev core.Event) (bool, error)
}

// Match is the predicate half of a rule. Every set field must hold; unset
// fields are ignored. Pointer fields distinguish "unset" from "set to zero".
type Match struct {
	Source       string            `yaml:"source"`
	Sources      []string          `yaml:"sources"`
	Kind         string            `yaml:"kind"`
	Kinds        []string          `yaml:"kinds"`
	App          string            `yaml:"app"`
	MinAmount    *float64          `yaml:"min_amount"`
	MaxAmount    *float64          `yaml:"max_amount"`
	MinQuantity  *int              `yaml:"min_quantity"`
	CountryFirst *bool             `yaml:"country_first"`
	RecordHigh   *bool             `yaml:"record_high"`
	IsLedger     *bool             `yaml:"is_ledger"`
	HasCountry   *bool             `yaml:"has_country"`
	PayloadMatch map[string]string `yaml:"payload_match"`
}

// Then is the outcome half of a rule. Title and Subtitle are text/template
// strings rendered against the event.
type Then struct {
	Rarity   core.Rarity `yaml:"rarity"`
	Title    string      `yaml:"title"`
	Subtitle string      `yaml:"subtitle"`
	XP       *int        `yaml:"xp"`
}

// Rule is one entry in the ordered list.
type Rule struct {
	Name string `yaml:"name"`
	// Floor rules do not terminate matching. After a terminal rule has been
	// chosen, every matching floor rule may raise the rarity (and relabel the
	// drop) but never lower it.
	Floor bool  `yaml:"floor"`
	Match Match `yaml:"match"`
	Then  Then  `yaml:"then"`
}

// Config is the parsed rules file.
type Config struct {
	Rules    []Rule `yaml:"rules"`
	Fallback *Then  `yaml:"fallback"`
}

// Engine classifies events using a Config plus a Lookup for historical rules.
type Engine struct {
	cfg       Config
	lookup    Lookup
	templates map[string]*template.Template
	// displayCurrency is what {{.AmountBaseFmt}} is denominated in.
	displayCurrency string

	needsCountryFirst bool
	needsRecordHigh   bool
}

// New compiles cfg into an Engine. It returns an error if any title or
// subtitle template is malformed or any rarity is unknown, so a bad rules file
// fails at startup rather than at the first drop.
func New(cfg Config, lookup Lookup) (*Engine, error) {
	e := &Engine{
		cfg:             cfg,
		lookup:          lookup,
		templates:       map[string]*template.Template{},
		displayCurrency: "USD",
	}

	compile := func(ruleName, field, text string) error {
		if text == "" {
			return nil
		}
		key := ruleName + "/" + field
		t, err := template.New(key).Option("missingkey=zero").Parse(text)
		if err != nil {
			return fmt.Errorf("rule %q: bad %s template: %w", ruleName, field, err)
		}
		e.templates[key] = t
		return nil
	}

	for i, r := range cfg.Rules {
		name := r.Name
		if name == "" {
			name = "rule#" + strconv.Itoa(i)
			e.cfg.Rules[i].Name = name
		}
		if !r.Then.Rarity.Valid() {
			return nil, fmt.Errorf("rule %q: unknown rarity %q", name, r.Then.Rarity)
		}
		if err := compile(name, "title", r.Then.Title); err != nil {
			return nil, err
		}
		if err := compile(name, "subtitle", r.Then.Subtitle); err != nil {
			return nil, err
		}
		if r.Match.CountryFirst != nil {
			e.needsCountryFirst = true
		}
		if r.Match.RecordHigh != nil {
			e.needsRecordHigh = true
		}
	}

	if cfg.Fallback != nil {
		if !cfg.Fallback.Rarity.Valid() {
			return nil, fmt.Errorf("fallback: unknown rarity %q", cfg.Fallback.Rarity)
		}
		if err := compile("fallback", "title", cfg.Fallback.Title); err != nil {
			return nil, err
		}
		if err := compile("fallback", "subtitle", cfg.Fallback.Subtitle); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// SetDisplayCurrency tells the engine which currency {{.AmountBase}} is in, so
// {{.AmountBaseFmt}} can label it. Defaults to USD.
func (e *Engine) SetDisplayCurrency(cur string) {
	if cur = strings.TrimSpace(cur); cur != "" {
		e.displayCurrency = strings.ToUpper(cur)
	}
}

// Parse reads a rules config from YAML bytes.
func Parse(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse rules: %w", err)
	}
	return cfg, nil
}

// Load builds an Engine from the YAML file at path. An empty path loads the
// embedded defaults.
func Load(path string, lookup Lookup) (*Engine, error) {
	data := DefaultYAML
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read rules file %s: %w", path, err)
		}
		data = b
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return New(cfg, lookup)
}

// tmplData is what title/subtitle templates are rendered against.
type tmplData struct {
	Source   string
	Kind     string
	App      string
	Day      string
	Country  string
	Currency string
	Amount   float64
	// AmountBase is Amount in the dashboard's display currency, which is what
	// a multi-currency ledger source should usually show.
	AmountBase    float64
	Quantity      int
	AmountFmt     string
	AmountBaseFmt string
	QuantityFmt   string
	// SourceName is the human name of the source ("Google Play"), for titles.
	SourceName string
	Flag       string
	BaseTitle  string
	Payload    map[string]any
}

// Classify picks a rarity, title and XP for ev and returns the resulting Drop.
func (e *Engine) Classify(ctx context.Context, ev core.Event) (core.Drop, error) {
	facts, err := e.gather(ctx, ev)
	if err != nil {
		return core.Drop{}, err
	}
	data := e.buildTmplData(ev, facts.payload)

	var (
		chosen  *Rule
		matched bool
	)
	for i := range e.cfg.Rules {
		r := &e.cfg.Rules[i]
		if r.Floor {
			continue
		}
		ok, err := e.matches(r.Match, ev, facts)
		if err != nil {
			return core.Drop{}, err
		}
		if ok {
			chosen = r
			matched = true
			break
		}
	}

	var then Then
	ruleName := "fallback"
	if matched {
		then = chosen.Then
		ruleName = chosen.Name
	} else if e.cfg.Fallback != nil {
		then = *e.cfg.Fallback
	} else {
		then = Then{Rarity: core.Common, Title: "{{.Source}} · {{.Kind}}"}
	}

	title := e.render(ruleName, "title", then.Title, data)
	subtitle := e.render(ruleName, "subtitle", then.Subtitle, data)
	rarity := then.Rarity
	xp := xpFor(then, rarity)

	// Floor pass: raise, never lower.
	for i := range e.cfg.Rules {
		r := &e.cfg.Rules[i]
		if !r.Floor {
			continue
		}
		ok, err := e.matches(r.Match, ev, facts)
		if err != nil {
			return core.Drop{}, err
		}
		if !ok || r.Then.Rarity.Rank() <= rarity.Rank() {
			continue
		}
		data.BaseTitle = title
		floorTitle := e.render(r.Name, "title", r.Then.Title, data)
		floorSub := e.render(r.Name, "subtitle", r.Then.Subtitle, data)
		if floorTitle != "" {
			// Keep the original headline as context rather than losing it.
			if floorSub == "" {
				floorSub = title
			}
			title = floorTitle
		}
		subtitle = floorSub
		rarity = r.Then.Rarity
		if fxp := xpFor(r.Then, rarity); fxp > xp {
			xp = fxp
		}
	}

	if title == "" {
		title = strings.TrimSpace(ev.Source + " " + ev.Kind)
	}

	return core.Drop{
		ID:        core.NewID(),
		EventID:   ev.ID,
		Rarity:    rarity,
		Title:     title,
		Subtitle:  subtitle,
		XP:        xp,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func xpFor(t Then, r core.Rarity) int {
	if t.XP != nil {
		return *t.XP
	}
	return core.DefaultXP[r]
}

// facts holds the lazily computed, store-backed inputs for a classification.
type facts struct {
	countryFirst bool
	recordHigh   bool
	payload      map[string]any
}

func (e *Engine) gather(ctx context.Context, ev core.Event) (facts, error) {
	var f facts

	if e.needsCountryFirst && ev.Country != "" && e.lookup != nil {
		n, err := e.lookup.CountryEventCount(ctx, ev.Country)
		if err != nil {
			return f, fmt.Errorf("country_first lookup: %w", err)
		}
		// The event under classification is already stored, so 1 == first ever.
		f.countryFirst = n <= 1
	}

	if e.needsRecordHigh && e.lookup != nil {
		ok, err := e.lookup.IsRecordQuantity(ctx, ev)
		if err != nil {
			return f, fmt.Errorf("record_high lookup: %w", err)
		}
		f.recordHigh = ok
	}

	if len(ev.Payload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(ev.Payload, &m); err == nil {
			f.payload = m
		}
	}
	return f, nil
}

func (e *Engine) matches(m Match, ev core.Event, f facts) (bool, error) {
	if m.Source != "" && !strings.EqualFold(m.Source, ev.Source) {
		return false, nil
	}
	if len(m.Sources) > 0 && !containsFold(m.Sources, ev.Source) {
		return false, nil
	}
	if m.Kind != "" && !strings.EqualFold(m.Kind, ev.Kind) {
		return false, nil
	}
	if len(m.Kinds) > 0 && !containsFold(m.Kinds, ev.Kind) {
		return false, nil
	}
	if m.App != "" && !strings.EqualFold(m.App, ev.App) {
		return false, nil
	}
	if m.MinAmount != nil && ev.Amount < *m.MinAmount {
		return false, nil
	}
	if m.MaxAmount != nil && ev.Amount > *m.MaxAmount {
		return false, nil
	}
	if m.MinQuantity != nil && ev.Quantity < *m.MinQuantity {
		return false, nil
	}
	if m.IsLedger != nil && ev.IsLedger != *m.IsLedger {
		return false, nil
	}
	if m.HasCountry != nil && (ev.Country != "") != *m.HasCountry {
		return false, nil
	}
	if m.CountryFirst != nil && f.countryFirst != *m.CountryFirst {
		return false, nil
	}
	if m.RecordHigh != nil && f.recordHigh != *m.RecordHigh {
		return false, nil
	}
	for path, want := range m.PayloadMatch {
		got, ok := lookupPath(f.payload, path)
		if !ok {
			return false, nil
		}
		if !strings.EqualFold(fmt.Sprint(got), want) {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) render(ruleName, field, text string, data tmplData) string {
	if text == "" {
		return ""
	}
	t, ok := e.templates[ruleName+"/"+field]
	if !ok {
		return text
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		// A template that fails at render time (rare, since parsing is
		// validated) should not lose the drop; fall back to the raw text.
		return text
	}
	return strings.TrimSpace(buf.String())
}

func (e *Engine) buildTmplData(ev core.Event, payload map[string]any) tmplData {
	d := tmplData{
		Source:      ev.Source,
		SourceName:  core.SourceDisplayName(ev.Source),
		Kind:        ev.Kind,
		App:         ev.App,
		Day:         ev.Day,
		Country:     ev.Country,
		Currency:    ev.Currency,
		Amount:      ev.Amount,
		AmountBase:  ev.AmountBase,
		Quantity:    ev.Quantity,
		QuantityFmt: humanInt(ev.Quantity),
		Flag:        FlagEmoji(ev.Country),
		Payload:     payload,
	}
	if ev.Amount > 0 {
		d.AmountFmt = FormatAmount(ev.Amount, ev.Currency)
	}
	if ev.AmountBase > 0 {
		d.AmountBaseFmt = FormatAmount(ev.AmountBase, e.displayCurrency)
	}
	return d
}

// humanInt formats n with thousands separators.
func humanInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// FlagEmoji converts an ISO 3166-1 alpha-2 code into its regional-indicator
// flag emoji. Anything that is not two ASCII letters returns "".
func FlagEmoji(iso2 string) string {
	c := strings.ToUpper(strings.TrimSpace(iso2))
	if len(c) != 2 {
		return ""
	}
	var r []rune
	for _, ch := range c {
		if ch < 'A' || ch > 'Z' {
			return ""
		}
		r = append(r, rune(0x1F1E6+(ch-'A')))
	}
	return string(r)
}

// lookupPath walks a dotted path through decoded JSON, e.g. "event.period_type".
func lookupPath(m map[string]any, path string) (any, bool) {
	if m == nil {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// zeroDecimalCurrencies are the ISO 4217 currencies with no minor unit, so
// "5028 JPY" rather than "5028.31 JPY".
var zeroDecimalCurrencies = map[string]bool{
	"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "HUF": true,
	"TWD": true, "PYG": true, "UGX": true, "RWF": true, "XAF": true, "XOF": true,
	"IDR": true, "COP": true,
}

// FormatAmount renders an amount with the decimals its currency actually has.
func FormatAmount(amount float64, currency string) string {
	if zeroDecimalCurrencies[strings.ToUpper(currency)] {
		return strings.TrimSpace(fmt.Sprintf("%.0f %s", amount, currency))
	}
	return strings.TrimSpace(fmt.Sprintf("%.2f %s", amount, currency))
}
