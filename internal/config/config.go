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

// Config is the whole of Loot's configuration.
type Config struct {
	// Listen is the HTTP bind address, e.g. ":8080".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database (loot.db).
	DataDir string `yaml:"data_dir"`
	// RulesPath overrides the embedded rarity rules when non-empty.
	RulesPath string `yaml:"rules_path"`

	Sources Sources `yaml:"sources"`
	Dev     Dev     `yaml:"dev"`

	// Since optionally overrides the first-run backfill floor for polling
	// sources (YYYY-MM-DD). Set from `loot serve --since`, not from YAML.
	Since string `yaml:"-"`
}

// Sources holds per-source configuration.
type Sources struct {
	RevenueCat RevenueCat `yaml:"revenuecat"`
	Flathub    Flathub    `yaml:"flathub"`
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
		Listen:  ":8080",
		DataDir: "./data",
		Sources: Sources{
			Flathub: Flathub{BackfillDays: 7},
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
		var apps []string
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				apps = append(apps, a)
			}
		}
		cfg.Sources.Flathub.Apps = apps
	}
	if v := os.Getenv("LOOT_FLATHUB_BACKFILL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sources.Flathub.BackfillDays = n
		}
	}
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
