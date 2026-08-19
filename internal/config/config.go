// Package config loads Loot's YAML configuration and applies LOOT_* env
// overrides on top of it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the config file used when --config is not given.
const DefaultPath = "loot.yaml"

// DefaultDisplayCurrency is the currency the vault reports in when none is set.
const DefaultDisplayCurrency = "USD"

// DefaultChestAutoOpenHours is how old a chest's day must be before Loot opens
// it without being asked.
const DefaultChestAutoOpenHours = 36

// Config is the whole of Loot's configuration.
type Config struct {
	// Listen is the HTTP bind address, e.g. ":8080".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database (loot.db).
	DataDir string `yaml:"data_dir"`
	// RulesPath overrides the embedded rarity rules when non-empty.
	RulesPath string `yaml:"rules_path"`
	// DisplayCurrency is the currency every vault figure is converted into
	// (ISO 4217, e.g. "USD"). Events keep their original amount and currency;
	// amount_base is the converted copy.
	DisplayCurrency string `yaml:"display_currency"`
	// HomeCountry is where you are: the ISO 3166-1 alpha-2 code of the
	// Hearth's capital, the settlement every live drop arcs towards. Empty
	// means "the biggest settlement", which needs no configuring and is right
	// for most people.
	HomeCountry string `yaml:"home_country"`

	FX      FX      `yaml:"fx"`
	Chest   Chest   `yaml:"chest"`
	Sources Sources `yaml:"sources"`
	Dev     Dev     `yaml:"dev"`

	// Since optionally overrides the first-run backfill floor for polling
	// sources (YYYY-MM-DD). Set from `loot serve --since`, not from YAML.
	Since string `yaml:"-"`
}

// FX configures currency conversion.
type FX struct {
	// Enabled defaults to true. False pins Loot to the exchange-rate snapshot
	// embedded in the binary and never calls out to the network.
	Enabled *bool `yaml:"enabled"`
}

// Chest configures the daily chest: the pile of drops a ledger day produces,
// held back so the morning arrives as one event instead of a hundred.
type Chest struct {
	// Enabled defaults to true. False publishes every drop immediately, chest
	// hint or not.
	Enabled *bool `yaml:"enabled"`
	// AutoOpenAfterHours opens a chest by itself once its day is that many
	// hours old, measured from midnight UTC of the chest's own day. Defaults
	// to 36 (so yesterday's chest springs open around noon today). 0 disables
	// auto-opening and waits for a click.
	AutoOpenAfterHours *int `yaml:"auto_open_after_hours"`
}

// Sources holds per-source configuration.
type Sources struct {
	RevenueCat RevenueCat `yaml:"revenuecat"`
	Flathub    Flathub    `yaml:"flathub"`
	AppStore   AppStore   `yaml:"appstore"`
	GooglePlay GooglePlay `yaml:"googleplay"`
}

// AppStore configures the App Store Connect sales-report source. Loot needs an
// App Store Connect API key with the Sales and Reports role, plus the vendor
// number from App Store Connect > Payments and Financial Reports.
type AppStore struct {
	// KeyID is the API key identifier, e.g. "2X9R4HXF34".
	KeyID string `yaml:"key_id"`
	// IssuerID is the team's issuer UUID.
	IssuerID string `yaml:"issuer_id"`
	// PrivateKeyPath points at the downloaded AuthKey_<KeyID>.p8 file.
	PrivateKeyPath string `yaml:"private_key_path"`
	// VendorNumber identifies whose sales to fetch, e.g. "80123456".
	VendorNumber string `yaml:"vendor_number"`
	// Apps optionally restricts ingest to these Apple IDs (the numeric app
	// ids). Empty means every app in the report.
	Apps []string `yaml:"apps"`
	// BackfillDays limits how far back the first run reads. Defaults to 30.
	BackfillDays int `yaml:"backfill_days"`
}

// Configured reports whether every field the source cannot run without is set.
func (a AppStore) Configured() bool {
	return a.KeyID != "" && a.IssuerID != "" && a.PrivateKeyPath != "" && a.VendorNumber != ""
}

// GooglePlay configures the Google Play sales-report source. Play publishes
// monthly CSV reports into a private Cloud Storage bucket
// (pubsite_prod_rev_<developer id>), read with a service account.
type GooglePlay struct {
	// ServiceAccountJSONPath points at the service-account key file.
	ServiceAccountJSONPath string `yaml:"service_account_json_path"`
	// Bucket is the reporting bucket, e.g. "pubsite_prod_rev_01234567890".
	Bucket string `yaml:"bucket"`
	// Packages optionally restricts ingest to these package names.
	Packages []string `yaml:"packages"`
	// BackfillMonths limits how many monthly reports the first run reads.
	// Defaults to 2.
	BackfillMonths int `yaml:"backfill_months"`
}

// Configured reports whether every field the source cannot run without is set.
func (g GooglePlay) Configured() bool {
	return g.ServiceAccountJSONPath != "" && g.Bucket != ""
}

// RevenueCat configures the webhook receiver at /hooks/revenuecat.
type RevenueCat struct {
	// Enabled defaults to true; set false to unmount the webhook.
	Enabled *bool `yaml:"enabled"`
	// Secret, when set, is required as `Authorization: Bearer <secret>`.
	Secret string `yaml:"secret"`
}

// Flathub configures the polling source.
type Flathub struct {
	// Apps is the list of Flatpak app IDs to watch, e.g. org.gnome.Calculator.
	Apps []string `yaml:"apps"`
	// BackfillDays limits how many past days the very first poll emits, so a
	// fresh install does not fire six months of drops. 0 seeds the cursor
	// without emitting anything.
	BackfillDays int `yaml:"backfill_days"`
}

// Dev gates the synthetic-drop endpoint and the UI's dev panel.
type Dev struct {
	Enabled bool `yaml:"enabled"`
}

// Default returns the configuration used when no file and no env are present.
func Default() Config {
	return Config{
		Listen:          ":8080",
		DataDir:         "./data",
		DisplayCurrency: DefaultDisplayCurrency,
		Sources: Sources{
			Flathub:    Flathub{BackfillDays: 7},
			AppStore:   AppStore{BackfillDays: 30},
			GooglePlay: GooglePlay{BackfillMonths: 2},
		},
	}
}

// Load reads the YAML file at path, applies env overrides and validates the
// result. A missing file is only an error when explicit is true (that is, when
// the user actually passed --config).
func Load(path string, explicit bool) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			// Decoding into the defaulted struct leaves absent keys untouched,
			// so YAML only overrides what it actually specifies.
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config %s: %w", path, err)
			}
		case errors.Is(err, fs.ErrNotExist) && !explicit:
			// Fine: run on defaults + env.
		default:
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	if cfg.Sources.Flathub.BackfillDays < 0 {
		cfg.Sources.Flathub.BackfillDays = 0
	}
	if cfg.Sources.AppStore.BackfillDays < 0 {
		cfg.Sources.AppStore.BackfillDays = 0
	}
	if cfg.Sources.GooglePlay.BackfillMonths < 0 {
		cfg.Sources.GooglePlay.BackfillMonths = 0
	}
	cfg.DisplayCurrency = strings.ToUpper(strings.TrimSpace(cfg.DisplayCurrency))
	if cfg.DisplayCurrency == "" {
		cfg.DisplayCurrency = DefaultDisplayCurrency
	}
	if len(cfg.DisplayCurrency) != 3 {
		return cfg, fmt.Errorf("display_currency %q is not a three letter ISO 4217 code", cfg.DisplayCurrency)
	}
	cfg.HomeCountry = strings.ToUpper(strings.TrimSpace(cfg.HomeCountry))
	if cfg.HomeCountry != "" && len(cfg.HomeCountry) != 2 {
		return cfg, fmt.Errorf("home_country %q is not a two letter ISO 3166-1 alpha-2 code", cfg.HomeCountry)
	}
	if cfg.Sources.AppStore.PrivateKeyPath != "" {
		if _, err := os.Stat(cfg.Sources.AppStore.PrivateKeyPath); err != nil {
			return cfg, fmt.Errorf("sources.appstore.private_key_path %s: %w", cfg.Sources.AppStore.PrivateKeyPath, err)
		}
	}
	if cfg.Sources.GooglePlay.ServiceAccountJSONPath != "" {
		if _, err := os.Stat(cfg.Sources.GooglePlay.ServiceAccountJSONPath); err != nil {
			return cfg, fmt.Errorf("sources.googleplay.service_account_json_path %s: %w", cfg.Sources.GooglePlay.ServiceAccountJSONPath, err)
		}
	}
	if cfg.RulesPath != "" {
		if _, err := os.Stat(cfg.RulesPath); err != nil {
			return cfg, fmt.Errorf("rules_path %s: %w", cfg.RulesPath, err)
		}
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("LOOT_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("LOOT_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LOOT_RULES_PATH"); v != "" {
		cfg.RulesPath = v
	}
	if v := os.Getenv("LOOT_DEV_ENABLED"); v != "" {
		cfg.Dev.Enabled = truthy(v)
	}
	if v := os.Getenv("LOOT_REVENUECAT_SECRET"); v != "" {
		cfg.Sources.RevenueCat.Secret = v
	}
	if v := os.Getenv("LOOT_REVENUECAT_ENABLED"); v != "" {
		b := truthy(v)
		cfg.Sources.RevenueCat.Enabled = &b
	}
	if v := os.Getenv("LOOT_FLATHUB_APPS"); v != "" {
		cfg.Sources.Flathub.Apps = splitList(v)
	}
	if v := os.Getenv("LOOT_FLATHUB_BACKFILL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sources.Flathub.BackfillDays = n
		}
	}
	if v := os.Getenv("LOOT_DISPLAY_CURRENCY"); v != "" {
		cfg.DisplayCurrency = v
	}
	if v := os.Getenv("LOOT_HOME_COUNTRY"); v != "" {
		cfg.HomeCountry = v
	}
	if v := os.Getenv("LOOT_FX_ENABLED"); v != "" {
		b := truthy(v)
		cfg.FX.Enabled = &b
	}
	if v := os.Getenv("LOOT_CHEST_ENABLED"); v != "" {
		b := truthy(v)
		cfg.Chest.Enabled = &b
	}
	if v := os.Getenv("LOOT_CHEST_AUTO_OPEN_AFTER_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Chest.AutoOpenAfterHours = &n
		}
	}
	if v := os.Getenv("LOOT_APPSTORE_KEY_ID"); v != "" {
		cfg.Sources.AppStore.KeyID = v
	}
	if v := os.Getenv("LOOT_APPSTORE_ISSUER_ID"); v != "" {
		cfg.Sources.AppStore.IssuerID = v
	}
	if v := os.Getenv("LOOT_APPSTORE_PRIVATE_KEY_PATH"); v != "" {
		cfg.Sources.AppStore.PrivateKeyPath = v
	}
	if v := os.Getenv("LOOT_APPSTORE_VENDOR_NUMBER"); v != "" {
		cfg.Sources.AppStore.VendorNumber = v
	}
	if v := os.Getenv("LOOT_APPSTORE_APPS"); v != "" {
		cfg.Sources.AppStore.Apps = splitList(v)
	}
	if v := os.Getenv("LOOT_APPSTORE_BACKFILL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sources.AppStore.BackfillDays = n
		}
	}
	if v := os.Getenv("LOOT_GOOGLEPLAY_SERVICE_ACCOUNT_JSON_PATH"); v != "" {
		cfg.Sources.GooglePlay.ServiceAccountJSONPath = v
	}
	if v := os.Getenv("LOOT_GOOGLEPLAY_BUCKET"); v != "" {
		cfg.Sources.GooglePlay.Bucket = v
	}
	if v := os.Getenv("LOOT_GOOGLEPLAY_PACKAGES"); v != "" {
		cfg.Sources.GooglePlay.Packages = splitList(v)
	}
	if v := os.Getenv("LOOT_GOOGLEPLAY_BACKFILL_MONTHS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sources.GooglePlay.BackfillMonths = n
		}
	}
}

// splitList parses a comma separated env value into a trimmed list.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true
	}
	return false
}

// DBPath returns the SQLite file path.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "loot.db") }

// RevenueCatEnabled reports whether the webhook should be mounted. It defaults
// to on, because an unmounted hook is a confusing 404 for a first-time user.
func (c Config) RevenueCatEnabled() bool {
	if c.Sources.RevenueCat.Enabled == nil {
		return true
	}
	return *c.Sources.RevenueCat.Enabled
}

// FXEnabled reports whether Loot may fetch live exchange rates. Defaults to on.
func (c Config) FXEnabled() bool {
	if c.FX.Enabled == nil {
		return true
	}
	return *c.FX.Enabled
}

// ChestEnabled reports whether chest-hinted drops are held back. Defaults to on.
func (c Config) ChestEnabled() bool {
	if c.Chest.Enabled == nil {
		return true
	}
	return *c.Chest.Enabled
}

// ChestAutoOpenAfterHours returns the auto-open delay in hours, or 0 for never.
func (c Config) ChestAutoOpenAfterHours() int {
	if c.Chest.AutoOpenAfterHours == nil {
		return DefaultChestAutoOpenHours
	}
	if *c.Chest.AutoOpenAfterHours < 0 {
		return 0
	}
	return *c.Chest.AutoOpenAfterHours
}
