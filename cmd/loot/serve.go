package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/sources/flathub"
	"github.com/nickhirras/loot/internal/sources/revenuecat"
	"github.com/nickhirras/loot/internal/store"
	"github.com/nickhirras/loot/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var (
		configPath = fs.String("config", config.DefaultPath, "path to loot.yaml")
		listen     = fs.String("listen", "", "override listen address, e.g. :8080")
		dataDir    = fs.String("data-dir", "", "override data directory")
		since      = fs.String("since", "", "first-run backfill floor for polling sources (YYYY-MM-DD)")
		dev        = fs.Bool("dev", false, "enable the dev endpoints and UI panel")
		verbose    = fs.Bool("v", false, "verbose (debug) logging")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: loot serve [flags]\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// --config is "explicit" only when the user actually passed it, so a
	// missing default loot.yaml is fine but a missing named file is an error.
	explicit := flagSet(fs, "config")
	cfg, err := config.Load(*configPath, explicit)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *dev {
		cfg.Dev.Enabled = true
	}
	cfg.Since = *since

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("store ready", "path", cfg.DBPath())

	engine, err := rules.Load(cfg.RulesPath, st)
	if err != nil {
		return err
	}
	if cfg.RulesPath != "" {
		log.Info("rules loaded", "path", cfg.RulesPath)
	}

	b := bus.New(256)
	pipe := pipeline.New(st, engine, b, log)

	var sources []core.Source
	if cfg.RevenueCatEnabled() {
		rc := revenuecat.New(cfg.Sources.RevenueCat.Secret, log)
		sources = append(sources, rc)
		if cfg.Sources.RevenueCat.Secret == "" {
			log.Warn("revenuecat webhook has no secret; anyone who can reach /hooks/revenuecat can post drops")
		}
	}
	if len(cfg.Sources.Flathub.Apps) > 0 {
		sources = append(sources, flathub.New(
			cfg.Sources.Flathub.Apps,
			cfg.Sources.Flathub.BackfillDays,
			cfg.Since,
			log,
		))
		log.Info("flathub source configured",
			"apps", cfg.Sources.Flathub.Apps, "backfill_days", cfg.Sources.Flathub.BackfillDays)
	}
	if len(sources) == 0 {
		log.Warn("no sources configured; see configs/loot.example.yaml")
	}

	sched := pipeline.NewScheduler(pipe, st, sources, log)
	go sched.Run(ctx)

	srv := server.New(cfg, st, b, pipe, sources, web.DistFS(), log)
	return srv.ListenAndServe(ctx)
}

// flagSet reports whether the named flag was actually provided on the command
// line, as opposed to sitting at its default.
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
