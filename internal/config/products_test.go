package config_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nickhirras/loot/internal/config"
)

// nicksApps is the shape of a real `apps:` block: one product named by three
// different sources, one named by a single one.
var nicksApps = config.Products{
	{
		Name: "Nistis",
		Match: map[string][]string{
			"appstore":   {"Nistis: Fasting Timer", "6763687103"},
			"revenuecat": {"app5525946104", "Nistis"},
			"googleplay": {"com.example.nistis"},
		},
	},
	{
		Name:  "Macro Trainer",
		Match: map[string][]string{"appstore": {"Macro Trainer"}},
	},
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name   string
		source string
		app    string
		want   string
	}{
		{"a title from the App Store report", "appstore", "Nistis: Fasting Timer", "Nistis"},
		{"the numeric Apple ID for the same app", "appstore", "6763687103", "Nistis"},
		{"a RevenueCat app id", "revenuecat", "app5525946104", "Nistis"},
		{"the name RevenueCat was mapped to", "revenuecat", "Nistis", "Nistis"},
		{"a Play package name", "googleplay", "com.example.nistis", "Nistis"},
		{"matching is case-insensitive", "AppStore", "nistis: fasting timer", "Nistis"},
		{"surrounding space is not a different app", "appstore", "  Macro Trainer  ", "Macro Trainer"},
		{"the canonical name always resolves to itself", "loot", "Macro Trainer", "Macro Trainer"},

		// The two rules that keep the mapping a convenience rather than a gate.
		{"an unmapped app keeps its own name", "flathub", "net.example.Thing", "net.example.Thing"},
		{"no app at all stays realm-wide", "loot", "", ""},

		// Loot's own events carry the app name of whichever source triggered
		// them, under the reserved source "loot" — so a name listed for any
		// source has to resolve, or every settlement would invent a phantom
		// product beside the real one.
		{"a settlement inherits the triggering source's name", "loot", "Nistis: Fasting Timer", "Nistis"},
		{"a name listed for another source still resolves", "flathub", "com.example.nistis", "Nistis"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nicksApps.Resolve(tc.source, tc.app); got != tc.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q", tc.source, tc.app, got, tc.want)
			}
		})
	}
}

// TestResolveWithNoMapping is the default install: no `apps:` at all, so every
// app is its own product and the scope selector still works.
func TestResolveWithNoMapping(t *testing.T) {
	var none config.Products
	if got := none.Resolve("appstore", "Reson8 Tuner"); got != "Reson8 Tuner" {
		t.Errorf("Resolve = %q, want the raw name back", got)
	}
	if got := none.Resolve("loot", ""); got != "" {
		t.Errorf("Resolve of no app = %q, want empty", got)
	}
}

func TestNamesAndHas(t *testing.T) {
	names := nicksApps.Names()
	if len(names) != 2 || names[0] != "Nistis" || names[1] != "Macro Trainer" {
		t.Fatalf("Names() = %v, want configuration order", names)
	}
	if !nicksApps.Has("macro trainer") {
		t.Error("Has should be case-insensitive")
	}
	if nicksApps.Has("Reson8") {
		t.Error("Has claimed an unconfigured product")
	}
}

// TestProductsParseFromYAML pins the config shape people actually type, which
// is the half of this feature a user touches.
func TestProductsParseFromYAML(t *testing.T) {
	const doc = `
listen: ":8080"
apps:
  - name: Nistis
    match:
      appstore: ["Nistis: Fasting Timer"]
      revenuecat: ["app5525946104", "Nistis"]
  - name: Reson8
    match: {appstore: ["Reson8 Tuner"], github: ["NickHirras/reson8"]}
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("got %d products, want 2", len(cfg.Apps))
	}
	if got := cfg.Apps.Resolve("revenuecat", "app5525946104"); got != "Nistis" {
		t.Errorf("resolve = %q, want Nistis", got)
	}
	if got := cfg.Apps.Resolve("github", "NickHirras/reson8"); got != "Reson8" {
		t.Errorf("resolve = %q, want Reson8", got)
	}
	if got := cfg.Apps[1].Sources(); len(got) != 2 || got[0] != "appstore" || got[1] != "github" {
		t.Errorf("Sources() = %v, want a sorted list", got)
	}
}
