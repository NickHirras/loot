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

func TestQuest2Defaults(t *testing.T) {
	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DisplayCurrency != "USD" {
		t.Errorf("display_currency = %q, want USD", cfg.DisplayCurrency)
	}
	if !cfg.FXEnabled() {
		t.Error("fx should be enabled by default")
	}
	if !cfg.ChestEnabled() {
		t.Error("the chest should be enabled by default")
	}
	if cfg.ChestAutoOpenAfterHours() != 36 {
		t.Errorf("auto_open_after_hours = %d, want 36", cfg.ChestAutoOpenAfterHours())
	}
	if cfg.Sources.AppStore.BackfillDays != 30 {
		t.Errorf("appstore backfill_days = %d, want 30", cfg.Sources.AppStore.BackfillDays)
	}
	if cfg.Sources.GooglePlay.BackfillMonths != 2 {
		t.Errorf("googleplay backfill_months = %d, want 2", cfg.Sources.GooglePlay.BackfillMonths)
	}
	if cfg.Sources.AppStore.Configured() || cfg.Sources.GooglePlay.Configured() {
		t.Error("an empty config must not report a source as configured")
	}
}

func TestLedgerSourceConfig(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "AuthKey_ABC.p8")
	if err := os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sa := filepath.Join(dir, "service-account.json")
	if err := os.WriteFile(sa, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write service account: %v", err)
	}

	path := writeConfig(t, `
display_currency: eur
fx:
  enabled: false
chest:
  enabled: false
  auto_open_after_hours: 0
sources:
  appstore:
    key_id: ABC123
    issuer_id: 11111111-2222-3333-4444-555555555555
    private_key_path: `+key+`
    vendor_number: "80123456"
    apps: ["123456789"]
    backfill_days: 14
  googleplay:
    service_account_json_path: `+sa+`
    bucket: pubsite_prod_rev_01234567890
    packages: ["com.example.app"]
    backfill_months: 3
`)

	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DisplayCurrency != "EUR" {
		t.Errorf("display_currency = %q, want EUR (upper-cased)", cfg.DisplayCurrency)
	}
	if cfg.FXEnabled() {
		t.Error("fx.enabled: false was ignored")
	}
	if cfg.ChestEnabled() {
		t.Error("chest.enabled: false was ignored")
	}
	if cfg.ChestAutoOpenAfterHours() != 0 {
		t.Errorf("auto_open_after_hours = %d, want 0 (explicitly disabled)", cfg.ChestAutoOpenAfterHours())
	}
	if !cfg.Sources.AppStore.Configured() {
		t.Error("appstore should report as configured")
	}
	if !cfg.Sources.GooglePlay.Configured() {
		t.Error("googleplay should report as configured")
	}
	if cfg.Sources.AppStore.BackfillDays != 14 || len(cfg.Sources.AppStore.Apps) != 1 {
		t.Errorf("appstore = %+v", cfg.Sources.AppStore)
	}
	if cfg.Sources.GooglePlay.BackfillMonths != 3 || cfg.Sources.GooglePlay.Bucket == "" {
		t.Errorf("googleplay = %+v", cfg.Sources.GooglePlay)
	}
}

func TestMissingCredentialFileIsAnError(t *testing.T) {
	path := writeConfig(t, `
sources:
  appstore:
    key_id: ABC123
    issuer_id: iss
    private_key_path: /nope/AuthKey.p8
    vendor_number: "80123456"
`)
	if _, err := config.Load(path, true); err == nil {
		t.Fatal("expected an error for a missing private key file")
	}
}

func TestBadDisplayCurrency(t *testing.T) {
	path := writeConfig(t, "display_currency: dollars\n")
	if _, err := config.Load(path, true); err == nil {
		t.Fatal("expected an error for a non ISO 4217 display currency")
	}
}

func TestQuest2EnvOverrides(t *testing.T) {
	t.Setenv("LOOT_DISPLAY_CURRENCY", "gbp")
	t.Setenv("LOOT_FX_ENABLED", "false")
	t.Setenv("LOOT_CHEST_AUTO_OPEN_AFTER_HOURS", "12")
	t.Setenv("LOOT_APPSTORE_VENDOR_NUMBER", "80999999")
	t.Setenv("LOOT_GOOGLEPLAY_BUCKET", "pubsite_prod_rev_9")
	t.Setenv("LOOT_GOOGLEPLAY_PACKAGES", "com.a, com.b")

	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DisplayCurrency != "GBP" {
		t.Errorf("display_currency = %q", cfg.DisplayCurrency)
	}
	if cfg.FXEnabled() {
		t.Error("LOOT_FX_ENABLED=false was ignored")
	}
	if cfg.ChestAutoOpenAfterHours() != 12 {
		t.Errorf("auto_open_after_hours = %d", cfg.ChestAutoOpenAfterHours())
	}
	if cfg.Sources.AppStore.VendorNumber != "80999999" {
		t.Errorf("vendor = %q", cfg.Sources.AppStore.VendorNumber)
	}
	if len(cfg.Sources.GooglePlay.Packages) != 2 || cfg.Sources.GooglePlay.Packages[1] != "com.b" {
		t.Errorf("packages = %v", cfg.Sources.GooglePlay.Packages)
	}
}

func TestHomeCountryNormalizedAndValidated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loot.yaml")

	if err := os.WriteFile(path, []byte("home_country: \" us \"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HomeCountry != "US" {
		t.Fatalf("home_country = %q, want US", cfg.HomeCountry)
	}

	// The env override wins, and is normalized the same way.
	t.Setenv("LOOT_HOME_COUNTRY", "nz")
	if cfg, err = config.Load(path, true); err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.HomeCountry != "NZ" {
		t.Fatalf("home_country = %q, want NZ", cfg.HomeCountry)
	}

	// A country name instead of a code is a typo, not a silent fallback.
	t.Setenv("LOOT_HOME_COUNTRY", "Sweden")
	if _, err := config.Load(path, true); err == nil {
		t.Fatal("want an error for a non ISO 3166-1 alpha-2 home_country")
	}
}

func TestDemoDefaultsOff(t *testing.T) {
	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Demo.Enabled {
		t.Error("demo mode must default to off")
	}
	if cfg.Demo.Seed != config.DefaultDemoSeed {
		t.Errorf("demo seed = %d, want the default %d", cfg.Demo.Seed, config.DefaultDemoSeed)
	}
	if cfg.Demo.Pace != config.DefaultDemoPace || cfg.Demo.Days != config.DefaultDemoDays {
		t.Errorf("demo pace/days = %v/%d", cfg.Demo.Pace, cfg.Demo.Days)
	}
	if cfg.ActiveDBPath() != cfg.DBPath() {
		t.Errorf("without demo mode the active database is %q, want %q", cfg.ActiveDBPath(), cfg.DBPath())
	}
}

// The single most important property of demo mode: it cannot open the real
// database, because the path it is given is a different file.
func TestDemoUsesItsOwnDatabase(t *testing.T) {
	path := writeConfig(t, `
data_dir: /var/lib/loot
demo:
  enabled: true
  seed: 99
  pace: 5
`)
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Demo.Enabled {
		t.Fatal("demo.enabled was not read")
	}
	if cfg.Demo.Seed != 99 || cfg.Demo.Pace != 5 {
		t.Errorf("demo seed/pace = %d/%v", cfg.Demo.Seed, cfg.Demo.Pace)
	}
	if want := filepath.Join("/var/lib/loot", "demo.db"); cfg.ActiveDBPath() != want {
		t.Errorf("active db = %q, want %q", cfg.ActiveDBPath(), want)
	}
	if cfg.ActiveDBPath() == cfg.DBPath() {
		t.Error("demo mode is pointed at the real database")
	}
}

func TestDemoEnvOverrides(t *testing.T) {
	t.Setenv("LOOT_DEMO", "1")
	t.Setenv("LOOT_DEMO_SEED", "1234")
	t.Setenv("LOOT_DEMO_PACE", "2.5")
	t.Setenv("LOOT_DEMO_DAYS", "45")

	cfg, err := config.Load("does-not-exist.yaml", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Demo.Enabled {
		t.Error("LOOT_DEMO=1 did not enable demo mode")
	}
	if cfg.Demo.Seed != 1234 || cfg.Demo.Pace != 2.5 || cfg.Demo.Days != 45 {
		t.Errorf("demo = %+v", cfg.Demo)
	}
	if cfg.ActiveDBPath() != cfg.DemoDBPath() {
		t.Errorf("active db = %q, want the demo database", cfg.ActiveDBPath())
	}
}

// Android vitals deliberately has no credential of its own: the same service
// account that reads the Play reporting bucket reads vitals, so pointing Loot
// at two key files for one Play Console account would be a trap rather than a
// feature.
func TestPlayVitalsBorrowsPlaysCredential(t *testing.T) {
	cfg := config.Default()
	cfg.Sources.PlayVitals.Enabled = true
	cfg.Sources.GooglePlay.Packages = []string{"com.example.one", "com.example.two"}

	if cfg.PlayVitalsConfigured() {
		t.Error("configured without a service-account key")
	}
	cfg.Sources.GooglePlay.ServiceAccountJSONPath = "/tmp/key.json"
	if !cfg.PlayVitalsConfigured() {
		t.Error("not configured with a key and Play's package list")
	}
	if got := cfg.PlayVitalsPackages(); len(got) != 2 || got[0] != "com.example.one" {
		t.Errorf("packages = %v, want Play's list", got)
	}

	// Its own list wins when it has one.
	cfg.Sources.PlayVitals.Packages = []string{"com.example.three"}
	if got := cfg.PlayVitalsPackages(); len(got) != 1 || got[0] != "com.example.three" {
		t.Errorf("packages = %v, want the vitals list", got)
	}

	// Vitals are per app; the API has no "every app" query.
	cfg.Sources.PlayVitals.Packages = nil
	cfg.Sources.GooglePlay.Packages = nil
	if cfg.PlayVitalsConfigured() {
		t.Error("configured with no packages at all")
	}

	// And it stays off until it is asked for: it needs an extra API enabled
	// and an extra Play Console grant, so it cannot be on by default.
	cfg.Sources.PlayVitals.Enabled = false
	cfg.Sources.PlayVitals.Packages = []string{"com.example.one"}
	if cfg.PlayVitalsConfigured() {
		t.Error("configured while disabled")
	}
}

func TestCrashSourceEnv(t *testing.T) {
	t.Setenv("LOOT_PLAYVITALS_ENABLED", "1")
	t.Setenv("LOOT_PLAYVITALS_PACKAGES", "com.a, com.b")
	t.Setenv("LOOT_PLAYVITALS_BACKFILL_DAYS", "45")
	t.Setenv("LOOT_SENTRY_ENABLED", "true")
	t.Setenv("LOOT_SENTRY_CLIENT_SECRET", "s3cr3t")
	t.Setenv("LOOT_CRASH_ENABLED", "yes")
	t.Setenv("LOOT_CRASH_SECRET", "hunter2")

	cfg, err := config.Load("", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Sources.PlayVitals.Enabled || cfg.Sources.PlayVitals.BackfillDays != 45 {
		t.Errorf("playvitals = %+v", cfg.Sources.PlayVitals)
	}
	if got := cfg.Sources.PlayVitals.Packages; len(got) != 2 || got[1] != "com.b" {
		t.Errorf("packages = %v, want the trimmed list", got)
	}
	if !cfg.Sources.Sentry.Enabled || cfg.Sources.Sentry.ClientSecret != "s3cr3t" {
		t.Errorf("sentry = %+v", cfg.Sources.Sentry)
	}
	if !cfg.Sources.Crash.Enabled || cfg.Sources.Crash.Secret != "hunter2" {
		t.Errorf("crash = %+v", cfg.Sources.Crash)
	}
}
