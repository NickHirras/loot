package config_test

import (
	"strings"
	"testing"

	"github.com/nickhirras/loot/internal/config"
)

// An empty config gets quiet funnel thresholds and leaves the optional
// checkpoint off.
func TestFunnelDefaults(t *testing.T) {
	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	n := cfg.Funnel.Notes
	if n.SpendWithoutConversion != 20 || n.ConcentrationPct != 80 ||
		n.MinConversionsPer100Views != 2 || n.CheckpointSpend != 0 {
		t.Errorf("funnel notes defaults = %+v", n)
	}
	if cfg.Funnel.ConversionLabel != "" || cfg.Funnel.SiteEvent != "" {
		t.Errorf("funnel names should default empty: %+v", cfg.Funnel)
	}
}

func TestFunnelFromFile(t *testing.T) {
	path := writeConfig(t, `
funnel:
  conversion_label: "  Store tap "
  site_event: Lead
  notes:
    spend_without_conversion: 35.5
    checkpoint_spend: 100
`)
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	f := cfg.Funnel
	if f.ConversionLabel != "Store tap" || f.SiteEvent != "Lead" {
		t.Errorf("names = %q %q", f.ConversionLabel, f.SiteEvent)
	}
	if f.Notes.SpendWithoutConversion != 35.5 || f.Notes.CheckpointSpend != 100 {
		t.Errorf("notes = %+v", f.Notes)
	}
	// Keys the file leaves out keep their defaults.
	if f.Notes.ConcentrationPct != 80 || f.Notes.MinConversionsPer100Views != 2 {
		t.Errorf("unset thresholds lost their defaults: %+v", f.Notes)
	}
}

func TestFunnelRejectsBadThresholds(t *testing.T) {
	for _, body := range []string{
		"funnel:\n  notes:\n    spend_without_conversion: -1\n",
		"funnel:\n  notes:\n    concentration_pct: 120\n",
	} {
		if _, err := config.Load(writeConfig(t, body), true); err == nil || !strings.Contains(err.Error(), "funnel.notes.") {
			t.Errorf("%q: err = %v, want a funnel.notes error", body, err)
		}
	}
}
