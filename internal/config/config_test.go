package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nickhirras/loot/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loot.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
	if cfg.Sources.Flathub.BackfillDays != 7 {
		t.Errorf("backfill_days = %d, want 7", cfg.Sources.Flathub.BackfillDays)
	}
	if !cfg.RevenueCatEnabled() {
		t.Error("revenuecat should be enabled by default")
	}
	if cfg.Dev.Enabled {
		t.Error("dev mode must default to off")
	}
	if cfg.DBPath() != filepath.Join("./data", "loot.db") {
		t.Errorf("db path = %q", cfg.DBPath())
	}
}

func TestMissingExplicitConfigIsAnError(t *testing.T) {
	if _, err := config.Load("nope.yaml", true); err == nil {
		t.Fatal("expected an error when --config names a missing file")
	}
}

func TestLoadFile(t *testing.T) {
	path := writeConfig(t, `
listen: ":9999"
data_dir: /var/lib/loot
dev:
  enabled: true
sources:
  revenuecat:
    secret: hunter2
  flathub:
    apps:
      - org.gnome.Calculator
      - org.example.App
`)

	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":9999" || cfg.DataDir != "/var/lib/loot" {
		t.Errorf("cfg = %+v", cfg)
	}
	if !cfg.Dev.Enabled {
		t.Error("dev.enabled was not read")
	}
	if cfg.Sources.RevenueCat.Secret != "hunter2" {
		t.Errorf("secret = %q", cfg.Sources.RevenueCat.Secret)
	}
	if len(cfg.Sources.Flathub.Apps) != 2 {
		t.Errorf("apps = %v", cfg.Sources.Flathub.Apps)
	}
	// Unspecified keys keep their defaults.
	if cfg.Sources.Flathub.BackfillDays != 7 {
		t.Errorf("backfill_days = %d, want the default 7", cfg.Sources.Flathub.BackfillDays)
	}
}

func TestExplicitZeroBackfillIsRespected(t *testing.T) {
	path := writeConfig(t, "sources:\n  flathub:\n    backfill_days: 0\n")
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Sources.Flathub.BackfillDays != 0 {
		t.Fatalf("backfill_days = %d, want 0", cfg.Sources.Flathub.BackfillDays)
	}
}

func TestEnvOverrides(t *testing.T) {
	path := writeConfig(t, "listen: \":9999\"\nsources:\n  flathub:\n    apps: [a.b.C]\n")

	t.Setenv("LOOT_LISTEN", ":7070")
	t.Setenv("LOOT_DATA_DIR", "/tmp/loot-data")
	t.Setenv("LOOT_DEV_ENABLED", "true")
	t.Setenv("LOOT_REVENUECAT_SECRET", "from-env")
	t.Setenv("LOOT_FLATHUB_APPS", "org.one.App, org.two.App")
	t.Setenv("LOOT_FLATHUB_BACKFILL_DAYS", "30")

	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":7070" {
		t.Errorf("listen = %q, env should win over the file", cfg.Listen)
	}
	if cfg.DataDir != "/tmp/loot-data" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
	if !cfg.Dev.Enabled {
		t.Error("LOOT_DEV_ENABLED was not applied")
	}
	if cfg.Sources.RevenueCat.Secret != "from-env" {
		t.Errorf("secret = %q", cfg.Sources.RevenueCat.Secret)
	}
	if len(cfg.Sources.Flathub.Apps) != 2 || cfg.Sources.Flathub.Apps[1] != "org.two.App" {
		t.Errorf("apps = %v (comma list should be split and trimmed)", cfg.Sources.Flathub.Apps)
	}
	if cfg.Sources.Flathub.BackfillDays != 30 {
		t.Errorf("backfill_days = %d", cfg.Sources.Flathub.BackfillDays)
	}
}

func TestRevenueCatCanBeDisabled(t *testing.T) {
	path := writeConfig(t, "sources:\n  revenuecat:\n    enabled: false\n")
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RevenueCatEnabled() {
		t.Fatal("revenuecat should be disabled")
	}
}

func TestBadYAMLIsRejected(t *testing.T) {
	path := writeConfig(t, "listen: [not, a, string]\n")
	if _, err := config.Load(path, true); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestMissingRulesPathIsRejected(t *testing.T) {
	path := writeConfig(t, "rules_path: /nowhere/rules.yaml\n")
	if _, err := config.Load(path, true); err == nil {
		t.Fatal("expected an error when rules_path does not exist")
	}
}
