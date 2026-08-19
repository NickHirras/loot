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
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/sources/appstore"
	"github.com/nickhirras/loot/internal/sources/flathub"
	"github.com/nickhirras/loot/internal/sources/googleplay"
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

	// Events stored before there was a base amount, but already in the display
	// currency, need no rates to catch up.
	if n, err := st.BackfillAmountBase(ctx, cfg.DisplayCurrency); err != nil {
		return err
	} else if n > 0 {
		log.Info("backfilled base amounts", "events", n, "currency", cfg.DisplayCurrency)
	}

	engine, err := rules.Load(cfg.RulesPath, st)
	if err != nil {
		return err
	}
	engine.SetDisplayCurrency(cfg.DisplayCurrency)
	if cfg.RulesPath != "" {
		log.Info("rules loaded", "path", cfg.RulesPath)
	}

	converter := fx.New(fx.Options{
		Base:    cfg.DisplayCurrency,
		Enabled: cfg.FXEnabled(),
		Store:   st,
		Logger:  log,
	})
	if err := converter.LoadCached(ctx); err != nil {
		log.Warn("could not read cached fx rates", "error", err)
	}
	go converter.Run(ctx)

	b := bus.New(256)
	pipe := pipeline.New(st, engine, b, log)
	pipe.DisplayCurrency = cfg.DisplayCurrency
	pipe.FX = converter
	pipe.ChestEnabled = cfg.ChestEnabled()
	pipe.ChestAutoOpenAfterHours = cfg.ChestAutoOpenAfterHours()
	go pipe.RunChestAutoOpen(ctx)

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
	if cfg.Sources.AppStore.Configured() {
		src, err := appstore.New(cfg.Sources.AppStore, log)
		if err != nil {
			// A source that cannot start is worth a loud warning, not a dead
			// dashboard: everything else still works without it.
			log.Warn("app store connect source unavailable", "error", err)
		} else {
			sources = append(sources, src)
			log.Info("app store connect source configured", "vendor", cfg.Sources.AppStore.VendorNumber)
		}
	}
	if cfg.Sources.GooglePlay.Configured() {
		src, err := googleplay.New(cfg.Sources.GooglePlay, log)
		if err != nil {
			log.Warn("google play source unavailable", "error", err)
		} else {
			sources = append(sources, src)
			log.Info("google play source configured", "bucket", cfg.Sources.GooglePlay.Bucket)
		}
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
