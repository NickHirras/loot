package rules_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
)

// fakeLookup stands in for the store so rule tests stay fast and hermetic.
type fakeLookup struct {
	countryCounts map[string]int
	record        bool
	err           error
}

func (f fakeLookup) CountryEventCount(_ context.Context, country string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.countryCounts[country], nil
}

func (f fakeLookup) IsRecordQuantity(_ context.Context, _ core.Event) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.record, nil
}

func defaultEngine(t *testing.T, lookup rules.Lookup) *rules.Engine {
	t.Helper()
	cfg, err := rules.Parse(rules.DefaultYAML)
	if err != nil {
		t.Fatalf("parse default rules: %v", err)
	}
	e, err := rules.New(cfg, lookup)
	if err != nil {
		t.Fatalf("compile default rules: %v", err)
	}
	return e
}

// floorEngine compiles a minimal rule set that still exercises the floor
// mechanism. The shipped defaults no longer use a floor rule (a first-ever
// country now gets its own settlement drop instead), but the engine still
// supports them, so they are tested against a config of their own.
const floorRules = `
rules:
  - name: renewal
    match: {source: revenuecat, kind: renewal}
    then: {rarity: common, title: "Renewal"}
  - name: churn
    match: {source: revenuecat, kind: cancellation}
    then: {rarity: cursed, title: "Subscriber lost"}
  - name: new-country
    floor: true
    match: {country_first: true}
    then: {rarity: rare, title: "New settlement: {{.Flag}} {{.Country}}", xp: 250}
fallback: {rarity: common, title: "{{.Source}} · {{.Kind}}"}
`

func floorEngine(t *testing.T, lookup rules.Lookup) *rules.Engine {
	t.Helper()
	cfg, err := rules.Parse([]byte(floorRules))
	if err != nil {
		t.Fatalf("parse floor rules: %v", err)
	}
	e, err := rules.New(cfg, lookup)
	if err != nil {
		t.Fatalf("compile floor rules: %v", err)
	}
	return e
}

func rcEvent(kind string, amount float64, payload string) core.Event {
	return core.Event{
		ID:        core.NewID(),
		Source:    "revenuecat",
		Kind:      kind,
		App:       "com.example.app",
		Country:   "US",
		Amount:    amount,
		Currency:  "USD",
		Quantity:  1,
		DedupeKey: "rc:" + kind,
		Payload:   []byte(payload),
	}
}

// seenCountry makes country_first false for the given codes.
func seenCountry(codes ...string) fakeLookup {
	m := map[string]int{}
	for _, c := range codes {
		m[c] = 5
	}
	return fakeLookup{countryCounts: m}
}

func TestDefaultRulesClassification(t *testing.T) {
	ctx := context.Background()
	e := defaultEngine(t, seenCountry("US"))

	cases := []struct {
		name       string
		event      core.Event
		wantRarity core.Rarity
		wantTitle  string
	}{
		{
			name:       "initial purchase is uncommon",
			event:      rcEvent("purchase", 4.99, `{"event":{"period_type":"NORMAL"}}`),
			wantRarity: core.Uncommon,
			wantTitle:  "New subscriber",
		},
		{
			name:       "expensive purchase is rare",
			event:      rcEvent("purchase", 24.99, `{"event":{"period_type":"NORMAL"}}`),
			wantRarity: core.Rare,
			wantTitle:  "New subscriber",
		},
		{
			name:       "annual purchase is rare regardless of price",
			event:      rcEvent("purchase", 3.00, `{"event":{"period_type":"ANNUAL"}}`),
			wantRarity: core.Rare,
			wantTitle:  "New annual subscriber",
		},
		{
			name:       "renewal is common",
			event:      rcEvent("renewal", 4.99, `{}`),
			wantRarity: core.Common,
			wantTitle:  "Renewal",
		},
		{
			name:       "cancellation is cursed",
			event:      rcEvent("cancellation", 0, `{}`),
			wantRarity: core.Cursed,
			wantTitle:  "Subscriber lost",
		},
		{
			name:       "billing issue is cursed",
			event:      rcEvent("billing_issue", 0, `{}`),
			wantRarity: core.Cursed,
			wantTitle:  "Subscriber lost",
		},
		{
			name:       "expiration is cursed",
			event:      rcEvent("expiration", 0, `{}`),
			wantRarity: core.Cursed,
			wantTitle:  "Subscriber lost",
		},
		{
			name:       "uncancellation is rare",
			event:      rcEvent("uncancellation", 0, `{}`),
			wantRarity: core.Rare,
			wantTitle:  "A subscriber returns",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drop, err := e.Classify(ctx, tc.event)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if drop.Rarity != tc.wantRarity {
				t.Errorf("rarity = %s, want %s", drop.Rarity, tc.wantRarity)
			}
			if drop.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", drop.Title, tc.wantTitle)
			}
			if drop.EventID != tc.event.ID {
				t.Errorf("drop is not linked to its event")
			}
			if drop.XP <= 0 {
				t.Errorf("xp = %d, want > 0", drop.XP)
			}
			if drop.ID == "" || drop.CreatedAt.IsZero() {
				t.Errorf("drop is missing id or timestamp: %+v", drop)
			}
		})
	}
}

func TestCountryFirstRaisesRarity(t *testing.T) {
	ctx := context.Background()

	// Country count 1 == this very event, so it is the first from there.
	e := floorEngine(t, fakeLookup{countryCounts: map[string]int{"JP": 1}})

	ev := rcEvent("renewal", 4.99, `{}`) // renewal is normally common
	ev.Country = "JP"

	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Rare {
		t.Fatalf("rarity = %s, want rare (country_first floor)", drop.Rarity)
	}
	if !strings.Contains(drop.Title, "New settlement") || !strings.Contains(drop.Title, "JP") {
		t.Fatalf("title = %q, want a New settlement title mentioning JP", drop.Title)
	}
	if !strings.Contains(drop.Title, rules.FlagEmoji("JP")) {
		t.Fatalf("title = %q, want the JP flag emoji", drop.Title)
	}
	// The original headline survives as the subtitle.
	if drop.Subtitle != "Renewal" {
		t.Fatalf("subtitle = %q, want the base title %q", drop.Subtitle, "Renewal")
	}
}

func TestCountryFirstNeverLowersRarity(t *testing.T) {
	ctx := context.Background()
	e := floorEngine(t, fakeLookup{countryCounts: map[string]int{"JP": 1}})

	// Cursed outranks the rare floor: churn from a new country is still churn.
	ev := rcEvent("cancellation", 0, `{}`)
	ev.Country = "JP"

	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Cursed {
		t.Fatalf("rarity = %s, want cursed (floor must not lower rarity)", drop.Rarity)
	}
	if drop.Title != "Subscriber lost" {
		t.Fatalf("title = %q, want the churn title untouched", drop.Title)
	}
}

func TestCountryFirstIsFalseForSeenCountry(t *testing.T) {
	ctx := context.Background()
	e := floorEngine(t, seenCountry("US"))

	drop, err := e.Classify(ctx, rcEvent("renewal", 4.99, `{}`))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Common {
		t.Fatalf("rarity = %s, want common for an already-seen country", drop.Rarity)
	}
}

func TestEmptyCountrySkipsFloor(t *testing.T) {
	ctx := context.Background()
	// An empty country must not be treated as "the first event from ''".
	e := floorEngine(t, fakeLookup{countryCounts: map[string]int{"": 1}})

	ev := rcEvent("renewal", 4.99, `{}`)
	ev.Country = ""

	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Common {
		t.Fatalf("rarity = %s, want common when country is empty", drop.Rarity)
	}
}

func TestFlathubRules(t *testing.T) {
	ctx := context.Background()

	install := core.Event{
		ID:        core.NewID(),
		Source:    "flathub",
		Kind:      "install",
		App:       "org.example.App",
		Quantity:  1234,
		DedupeKey: "flathub:org.example.App:2026-01-01",
	}

	t.Run("ordinary day", func(t *testing.T) {
		e := defaultEngine(t, fakeLookup{record: false})
		drop, err := e.Classify(ctx, install)
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		if drop.Rarity != core.Common {
			t.Fatalf("rarity = %s, want common", drop.Rarity)
		}
		// The count is rendered with thousands separators by the template.
		if drop.Title != "1,234 installs on Flathub" {
			t.Fatalf("title = %q", drop.Title)
		}
		if drop.Subtitle != "org.example.App" {
			t.Fatalf("subtitle = %q", drop.Subtitle)
		}
	})

	t.Run("record day", func(t *testing.T) {
		e := defaultEngine(t, fakeLookup{record: true})
		drop, err := e.Classify(ctx, install)
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		if drop.Rarity != core.Epic {
			t.Fatalf("rarity = %s, want epic", drop.Rarity)
		}
		if drop.Title != "Best day ever on Flathub" {
			t.Fatalf("title = %q", drop.Title)
		}
	})
}

func TestDevRaritiesRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := defaultEngine(t, fakeLookup{})

	for _, r := range core.Rarities {
		ev := core.Event{
			ID:        core.NewID(),
			Source:    "dev",
			Kind:      string(r),
			App:       "com.example.loot",
			DedupeKey: "dev:" + string(r),
		}
		drop, err := e.Classify(ctx, ev)
		if err != nil {
			t.Fatalf("classify %s: %v", r, err)
		}
		if drop.Rarity != r {
			t.Fatalf("dev %s classified as %s", r, drop.Rarity)
		}
		if drop.XP != core.DefaultXP[r] {
			t.Fatalf("dev %s xp = %d, want the default %d", r, drop.XP, core.DefaultXP[r])
		}
	}
}

func TestFallbackForUnknownSource(t *testing.T) {
	ctx := context.Background()
	e := defaultEngine(t, fakeLookup{})

	ev := core.Event{
		ID:        core.NewID(),
		Source:    "mystery",
		Kind:      "sighting",
		DedupeKey: "m:1",
	}
	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Common {
		t.Fatalf("rarity = %s, want the common fallback", drop.Rarity)
	}
	if !strings.Contains(drop.Title, "mystery") || !strings.Contains(drop.Title, "sighting") {
		t.Fatalf("fallback title = %q, want source and kind", drop.Title)
	}
}

func TestFirstMatchWins(t *testing.T) {
	ctx := context.Background()

	cfg, err := rules.Parse([]byte(`
rules:
  - name: first
    match: {source: test}
    then: {rarity: legendary, title: "first"}
  - name: second
    match: {source: test}
    then: {rarity: common, title: "second"}
fallback:
  rarity: common
  title: "fallback"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e, err := rules.New(cfg, fakeLookup{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	drop, err := e.Classify(ctx, core.Event{ID: "x", Source: "test", Kind: "k"})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "first" || drop.Rarity != core.Legendary {
		t.Fatalf("got %s/%s, want legendary/first", drop.Rarity, drop.Title)
	}
}

func TestMatchPredicates(t *testing.T) {
	ctx := context.Background()

	cfg, err := rules.Parse([]byte(`
rules:
  - name: ledger-only
    match: {source: test, is_ledger: true}
    then: {rarity: legendary, title: "ledger"}
  - name: banded
    match: {source: test, min_amount: 10, max_amount: 20}
    then: {rarity: epic, title: "banded"}
  - name: quantity
    match: {source: test, min_quantity: 100}
    then: {rarity: rare, title: "bulk"}
  - name: no-country
    match: {source: test, has_country: false}
    then: {rarity: uncommon, title: "anonymous"}
fallback: {rarity: common, title: "fallback"}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e, err := rules.New(cfg, fakeLookup{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	cases := []struct {
		name  string
		event core.Event
		want  string
	}{
		{"ledger", core.Event{ID: "1", Source: "test", IsLedger: true, Country: "US"}, "ledger"},
		{"in band", core.Event{ID: "2", Source: "test", Amount: 15, Country: "US"}, "banded"},
		{"above band", core.Event{ID: "3", Source: "test", Amount: 25, Country: "US", Quantity: 100}, "bulk"},
		{"no country", core.Event{ID: "4", Source: "test", Amount: 25}, "anonymous"},
		{"nothing matches", core.Event{ID: "5", Source: "test", Amount: 25, Country: "US"}, "fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drop, err := e.Classify(ctx, tc.event)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if drop.Title != tc.want {
				t.Fatalf("title = %q, want %q", drop.Title, tc.want)
			}
		})
	}
}

func TestBadConfigIsRejected(t *testing.T) {
	t.Run("unknown rarity", func(t *testing.T) {
		cfg, err := rules.Parse([]byte("rules:\n  - name: x\n    match: {source: a}\n    then: {rarity: mythic, title: t}\n"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, err := rules.New(cfg, fakeLookup{}); err == nil {
			t.Fatal("expected an error for an unknown rarity")
		}
	})

	t.Run("bad template", func(t *testing.T) {
		cfg, err := rules.Parse([]byte("rules:\n  - name: x\n    match: {source: a}\n    then: {rarity: common, title: \"{{ .Broken\"}\n"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, err := rules.New(cfg, fakeLookup{}); err == nil {
			t.Fatal("expected an error for a malformed template")
		}
	})
}

func TestDefaultRulesCompile(t *testing.T) {
	// Guards the embedded YAML against typos that would only show at runtime.
	defaultEngine(t, fakeLookup{})
}

func TestFlagEmoji(t *testing.T) {
	cases := map[string]string{
		"US":  "\U0001F1FA\U0001F1F8",
		"jp":  "\U0001F1EF\U0001F1F5",
		"":    "",
		"USA": "",
		"1A":  "",
	}
	for in, want := range cases {
		if got := rules.FlagEmoji(in); got != want {
			t.Errorf("FlagEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func salesDay(app string, units int, amount float64, currency string) core.Event {
	return core.Event{
		ID:        core.NewID(),
		Source:    "appstore",
		Kind:      "sales_day",
		App:       app,
		Day:       "2026-08-17",
		Amount:    amount,
		Currency:  currency,
		Quantity:  units,
		IsLedger:  true,
		Chest:     true,
		DedupeKey: "appstore:sales_day:" + app + ":2026-08-17",
	}
}

func TestSalesDayRules(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name       string
		lookup     rules.Lookup
		event      core.Event
		wantRarity core.Rarity
		wantTitle  string
	}{
		{
			name:       "quiet day is common",
			lookup:     seenCountry("US"),
			event:      salesDay("com.example.app", 12, 41.50, "USD"),
			wantRarity: core.Common,
			wantTitle:  "12 sales · 41.50 USD",
		},
		{
			name:       "a hundred or more is rare",
			lookup:     seenCountry("US"),
			event:      salesDay("com.example.app", 90, 240, "USD"),
			wantRarity: core.Rare,
			wantTitle:  "90 sales · 240.00 USD",
		},
		{
			name:       "a record day is epic and names the source",
			lookup:     fakeLookup{record: true},
			event:      salesDay("com.example.app", 900, 2400, "USD"),
			wantRarity: core.Epic,
			wantTitle:  "Best day ever on appstore",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := defaultEngine(t, tc.lookup)
			drop, err := e.Classify(ctx, tc.event)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if drop.Rarity != tc.wantRarity {
				t.Errorf("rarity = %s, want %s", drop.Rarity, tc.wantRarity)
			}
			if drop.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", drop.Title, tc.wantTitle)
			}
			if !strings.Contains(drop.Subtitle, "com.example.app") {
				t.Errorf("subtitle = %q, want it to name the app", drop.Subtitle)
			}
		})
	}
}

func TestSettlementRule(t *testing.T) {
	ctx := context.Background()
	e := defaultEngine(t, seenCountry("JP"))

	ev := core.Event{
		ID:        core.NewID(),
		Source:    "loot",
		Kind:      "settlement",
		App:       "com.example.app",
		Country:   "JP",
		DedupeKey: "loot:settlement:JP",
		Payload:   []byte(`{"via":"appstore"}`),
	}

	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Rarity != core.Rare {
		t.Fatalf("rarity = %s, want rare", drop.Rarity)
	}
	if !strings.Contains(drop.Title, "New settlement") || !strings.Contains(drop.Title, rules.FlagEmoji("JP")) {
		t.Fatalf("title = %q, want a flagged settlement headline", drop.Title)
	}
	if !strings.Contains(drop.Subtitle, "appstore") {
		t.Fatalf("subtitle = %q, want it to credit the source that found the country", drop.Subtitle)
	}
	if drop.XP != 250 {
		t.Fatalf("xp = %d, want 250", drop.XP)
	}
}

func TestAmountBaseTemplateField(t *testing.T) {
	ctx := context.Background()

	cfg, err := rules.Parse([]byte(`
rules:
  - name: base
    match: {kind: sales_day}
    then: {rarity: common, title: "{{.AmountBaseFmt}}", subtitle: "{{.Day}}"}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e, err := rules.New(cfg, seenCountry("US"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e.SetDisplayCurrency("eur")

	ev := salesDay("com.example.app", 3, 30, "USD")
	ev.AmountBase = 25.5

	drop, err := e.Classify(ctx, ev)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if drop.Title != "25.50 EUR" {
		t.Fatalf("title = %q, want the base amount in the display currency", drop.Title)
	}
	if drop.Subtitle != "2026-08-17" {
		t.Fatalf("subtitle = %q, want the business day", drop.Subtitle)
	}
}
