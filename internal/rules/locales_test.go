package rules_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/i18n"
	"github.com/nickhirras/loot/internal/rules"
)

// translatingEngine is the shipped defaults plus the shipped overlays, which
// is what `loot serve` runs.
func translatingEngine(t *testing.T, lookup rules.Lookup) *rules.Engine {
	t.Helper()
	e, err := rules.Load("", lookup)
	if err != nil {
		t.Fatalf("load default rules: %v", err)
	}
	return e
}

func renewalEvent() core.Event {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	return core.Event{
		ID:         "EV-RENEWAL",
		Source:     "revenuecat",
		Kind:       "renewal",
		App:        "Nistis",
		OccurredAt: now,
		ObservedAt: now,
		Day:        "2026-05-02",
		Country:    "DE",
		Amount:     9.99,
		Currency:   "USD",
		Quantity:   1,
		DedupeKey:  "rc:renewal-1",
	}
}

// Every drop records what wrote it, so that it can be written again later.
func TestClassifyRecordsRule(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})

	drop, err := e.Classify(ctx, renewalEvent())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rule != "revenuecat-renewal" {
		t.Fatalf("rule = %q, want revenuecat-renewal", drop.Rule)
	}
	if drop.FloorRule != "" {
		t.Fatalf("floor_rule = %q, want empty: the defaults have no floor rules", drop.FloorRule)
	}
	if drop.Lang != "en" {
		t.Fatalf("lang = %q, want en", drop.Lang)
	}

	// A source no rule matches falls through to the fallback, and says so.
	odd := renewalEvent()
	odd.Source = "unheard-of"
	odd.Kind = "something"
	odd.DedupeKey = "odd:1"
	drop, err = e.Classify(ctx, odd)
	if err != nil {
		t.Fatalf("classify fallback: %v", err)
	}
	if drop.Rule != "fallback" {
		t.Fatalf("rule = %q, want fallback", drop.Rule)
	}
}

// The whole point: a drop minted in English reads as German for a German
// reader, rebuilt from the rule rather than translated after the fact.
func TestLocalizeRendersGerman(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})

	ev := renewalEvent()
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "Renewal" {
		t.Fatalf("english title = %q, want Renewal", drop.Title)
	}

	title, subtitle, ok := e.Localize(ctx, drop, ev, nil, "de")
	if !ok {
		t.Fatal("Localize declined a default rule it has an overlay for")
	}
	if title != "Verlängerung" {
		t.Fatalf("german title = %q, want Verlängerung", title)
	}
	if subtitle != "9.99 USD · Nistis" {
		t.Fatalf("german subtitle = %q, want the rendered amount and app", subtitle)
	}

	// The stored drop is untouched: the database keeps the English sentence.
	if drop.Title != "Renewal" {
		t.Fatalf("Localize rewrote the stored drop: %q", drop.Title)
	}
}

// German renders the numbers the same way English does — the expressions in an
// overlay are the English ones, reordered around the German sentence.
func TestLocalizeRendersTemplateData(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})

	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	ev := core.Event{
		ID: "EV-SALES", Source: "appstore", Kind: "sales_day", App: "Nistis",
		OccurredAt: now, ObservedAt: now, Day: "2026-05-02",
		Amount: 42.5, Currency: "USD", Quantity: 3, IsLedger: true,
		DedupeKey: "as:sales-1",
	}
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	title, subtitle, ok := e.Localize(ctx, drop, ev, nil, "de")
	if !ok {
		t.Fatal("Localize declined the sales-day rule")
	}
	if title != "3 Verkäufe · 42.50 USD" {
		t.Fatalf("german title = %q", title)
	}
	if subtitle != "Nistis · App Store" {
		t.Fatalf("german subtitle = %q", subtitle)
	}

	// One sale is one Verkauf: the overlay declines the German plural the way
	// the English template declines its own.
	one := ev
	one.Quantity = 1
	one.ID = "EV-SALES-1"
	one.DedupeKey = "as:sales-2"
	drop, err = e.Classify(ctx, one)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	title, _, _ = e.Localize(ctx, drop, one, nil, "de")
	if title != "1 Verkauf · 42.50 USD" {
		t.Fatalf("singular german title = %q", title)
	}
}

// customised builds an engine from the defaults with one rule's title
// rewritten, which is what copying default.yaml and editing it produces.
func customisedEngine(t *testing.T, ruleName, title string) *rules.Engine {
	t.Helper()
	cfg, err := rules.Parse(rules.DefaultYAML)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	found := false
	for i := range cfg.Rules {
		if cfg.Rules[i].Name == ruleName {
			cfg.Rules[i].Then.Title = title
			found = true
		}
	}
	if !found {
		t.Fatalf("no rule named %q in the defaults", ruleName)
	}
	e, err := rules.New(cfg, fakeLookup{})
	if err != nil {
		t.Fatalf("compile customised rules: %v", err)
	}
	e.LoadOverlays()
	return e
}

// Your words stay your words. A rewritten title is never translated, and the
// rules you left alone still are.
func TestLocalizeLeavesCustomisedRulesAlone(t *testing.T) {
	ctx := context.Background()
	e := customisedEngine(t, "revenuecat-renewal", "Money arrived")

	ev := renewalEvent()
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "Money arrived" {
		t.Fatalf("customised title = %q", drop.Title)
	}
	title, _, ok := e.Localize(ctx, drop, ev, nil, "de")
	if ok {
		t.Fatalf("Localize translated a customised rule into %q", title)
	}
	if title != "Money arrived" {
		t.Fatalf("title = %q, want the operator's own words", title)
	}

	// The untouched rule beside it still translates.
	churn := renewalEvent()
	churn.Kind = "cancellation"
	churn.ID = "EV-CHURN"
	churn.DedupeKey = "rc:churn-1"
	drop, err = e.Classify(ctx, churn)
	if err != nil {
		t.Fatalf("classify churn: %v", err)
	}
	title, _, ok = e.Localize(ctx, drop, churn, nil, "de")
	if !ok || title != "Abonnent verloren" {
		t.Fatalf("untouched rule gave %q (ok=%v), want Abonnent verloren", title, ok)
	}
}

// floorOverlayRules is the defaults with one extra entry: the settlement rule
// again, this time as a floor rule. It is the smallest way to exercise the
// floor pass against rules whose wording is still the defaults' — the shipped
// default.yaml has no floor rule of its own.
func floorOverlayEngine(t *testing.T) *rules.Engine {
	t.Helper()
	cfg, err := rules.Parse(rules.DefaultYAML)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	var settlement rules.Rule
	for _, r := range cfg.Rules {
		if r.Name == "settlement" {
			settlement = r
		}
	}
	if settlement.Name == "" {
		t.Fatal("no settlement rule in the defaults")
	}
	settlement.Floor = true
	settlement.Match = rules.Match{CountryFirst: boolPtr(true)}
	cfg.Rules = append(cfg.Rules, settlement)

	e, err := rules.New(cfg, fakeLookup{countryCounts: map[string]int{"DE": 1}})
	if err != nil {
		t.Fatalf("compile floor rules: %v", err)
	}
	e.LoadOverlays()
	return e
}

func boolPtr(b bool) *bool { return &b }

// A relabelled drop is translated the way it was written: the floor rule's
// title wins, and a floor rule with nothing to say keeps the headline it
// replaced as its subtitle.
func TestLocalizeFloorRule(t *testing.T) {
	ctx := context.Background()
	e := floorOverlayEngine(t)

	ev := renewalEvent()
	ev.Payload = []byte(`{"via":"revenuecat"}`)
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rule != "revenuecat-renewal" || drop.FloorRule != "settlement" {
		t.Fatalf("rule/floor = %q/%q, want revenuecat-renewal/settlement", drop.Rule, drop.FloorRule)
	}
	if drop.Title != "New settlement: 🇩🇪 DE" {
		t.Fatalf("english title = %q", drop.Title)
	}

	title, subtitle, ok := e.Localize(ctx, drop, ev, nil, "de")
	if !ok {
		t.Fatal("Localize declined a relabelled drop it has both rules for")
	}
	if title != "Neue Siedlung: 🇩🇪 DE" {
		t.Fatalf("german title = %q", title)
	}
	if subtitle != "erster Kunde über revenuecat" {
		t.Fatalf("german subtitle = %q", subtitle)
	}

	// A floor overlay with a title and no subtitle: .BaseTitle is the headline
	// the winning rule wrote, and it becomes the subtitle, exactly as Classify
	// does it.
	if err := e.AddOverlay("de-AT", rules.Overlay{Rules: []rules.OverlayRule{
		{Name: "revenuecat-renewal", Then: rules.Then{Title: "Verlängerung"}},
		{Name: "settlement", Then: rules.Then{Title: "{{.BaseTitle}} · Neue Siedlung: {{.Country}}"}},
	}}); err != nil {
		t.Fatalf("add overlay: %v", err)
	}
	title, subtitle, ok = e.Localize(ctx, drop, ev, nil, "de-AT")
	if !ok {
		t.Fatal("Localize declined the de-AT overlay")
	}
	if title != "Verlängerung · Neue Siedlung: DE" {
		t.Fatalf("de-AT title = %q, want the base title folded in", title)
	}
	if subtitle != "Verlängerung" {
		t.Fatalf("de-AT subtitle = %q, want the headline it replaced", subtitle)
	}
}

// Everything Localize declines to touch, it hands back exactly as stored.
func TestLocalizeDeclines(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})

	ev := renewalEvent()
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}

	cases := []struct {
		name string
		drop core.Drop
		lang string
	}{
		{"english", drop, "en"},
		{"no language", drop, ""},
		{"a language with no overlay", drop, "xx"},
		{"already in that language", core.Drop{
			Title: "Verlängerung", Rule: "revenuecat-renewal", Lang: "de",
		}, "de"},
		{"a drop from before rules were recorded", core.Drop{
			Title: "Renewal", Subtitle: "9.99 USD", Rule: "",
		}, "de"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, subtitle, ok := e.Localize(ctx, tc.drop, ev, nil, tc.lang)
			if ok {
				t.Fatalf("Localize claimed to have rendered %q / %q", title, subtitle)
			}
			if title != tc.drop.Title || subtitle != tc.drop.Subtitle {
				t.Fatalf("got %q / %q, want the stored %q / %q",
					title, subtitle, tc.drop.Title, tc.drop.Subtitle)
			}
		})
	}
}

// A Loot configured for one language writes its drops in it, so `loot tail`
// and the websocket say what the dashboard says.
func TestWriteTimeLanguage(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})
	e.SetLanguage("de")

	drop, err := e.Classify(ctx, renewalEvent())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "Verlängerung" {
		t.Fatalf("title = %q, want the German rendering", drop.Title)
	}
	if drop.Lang != "de" {
		t.Fatalf("lang = %q, want de", drop.Lang)
	}
	if drop.Rule != "revenuecat-renewal" {
		t.Fatalf("rule = %q", drop.Rule)
	}

	// A language with no overlay leaves the drop in English rather than
	// claiming to have written it in Finnish.
	e.SetLanguage("fi")
	drop, err = e.Classify(ctx, renewalEvent())
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "Renewal" || drop.Lang != "en" {
		t.Fatalf("title/lang = %q/%q, want Renewal/en", drop.Title, drop.Lang)
	}
}

// The shipped German overlay is loaded, parses, and is the only one so far.
func TestShippedOverlays(t *testing.T) {
	e := translatingEngine(t, fakeLookup{})
	langs := e.Languages()
	if len(langs) == 0 {
		t.Fatal("no overlays loaded")
	}
	if !e.HasOverlay("de") || !e.HasOverlay("DE") {
		t.Fatalf("German overlay missing from %v", langs)
	}
	if e.HasOverlay("en") {
		t.Fatal("English has an overlay; English is what drops are stored in")
	}
}

// An achievement's title lives in the dashboard's message catalog under the
// trophy's own key, so the drop looks it up rather than reprinting the
// English the event carried.
func TestLocalizeAchievementPayload(t *testing.T) {
	ctx := context.Background()
	e := translatingEngine(t, fakeLookup{})

	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	ev := core.Event{
		ID: "EV-ACH", Source: "loot", Kind: core.KindAchievement,
		OccurredAt: now, ObservedAt: now, Day: "2026-05-02", DedupeKey: "ach:cartographer",
		Payload: []byte(`{"key":"cartographer","title":"Cartographer","tier":"gold",` +
			`"description":"A settlement on every inhabited continent."}`),
	}
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rule != "achievement-gold" {
		t.Fatalf("rule = %q, want achievement-gold", drop.Rule)
	}
	title, subtitle, ok := e.Localize(ctx, drop, ev, nil, "de")
	if !ok {
		t.Fatal("Localize declined an achievement drop")
	}
	// The trophy's own name and description come from the German message
	// catalog, whatever it currently says — and English, were the catalog
	// ever missing them, which is what a half-translated catalog should look
	// like rather than a blank.
	wantTitle, _ := i18n.Lookup("de", "ach_cartographer_title")
	wantDesc, _ := i18n.Lookup("de", "ach_cartographer_desc")
	// The frame around the name ("Erfolg: …") is the overlay's to choose and
	// may change with every regeneration; the name inside it may not.
	if !strings.HasSuffix(title, ": "+wantTitle) || strings.HasPrefix(title, "Achievement:") {
		t.Fatalf("german title = %q, want a German frame around %q", title, wantTitle)
	}
	if subtitle != wantDesc {
		t.Fatalf("german subtitle = %q, want %q", subtitle, wantDesc)
	}
}

// A malformed overlay is refused at load, not at the first drop that would
// have used it.
func TestAddOverlayRejectsBadTemplate(t *testing.T) {
	e := translatingEngine(t, fakeLookup{})
	err := e.AddOverlay("xx", rules.Overlay{Rules: []rules.OverlayRule{
		{Name: "revenuecat-renewal", Then: rules.Then{Title: "{{.Unclosed"}},
	}})
	if err == nil {
		t.Fatal("a broken overlay template was accepted")
	}
	if e.HasOverlay("xx") {
		t.Fatal("a refused overlay was registered anyway")
	}
}
