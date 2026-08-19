package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/codex"
	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/demo"
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/mysteries"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/rules"
	"github.com/nickhirras/loot/internal/server"
	"github.com/nickhirras/loot/internal/sources/appstore"
	"github.com/nickhirras/loot/internal/sources/flathub"
	"github.com/nickhirras/loot/internal/sources/github"
	"github.com/nickhirras/loot/internal/sources/googleplay"
	"github.com/nickhirras/loot/internal/sources/microsoftstore"
	"github.com/nickhirras/loot/internal/sources/revenuecat"
	"github.com/nickhirras/loot/internal/sources/snapcraft"
	"github.com/nickhirras/loot/internal/sources/webhook"
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
		demoMode   = fs.Bool("demo", false, "run on synthetic data in data/demo.db; never touches loot.db")
		demoReset  = fs.Bool("demo-reset", false, "delete data/demo.db before starting (implies --demo)")
		demoPace   = fs.Float64("demo-pace", 0, "how fast demo mode emits events (1 = real time, 5 = five times faster)")
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
	if *demoMode || *demoReset {
		cfg.Demo.Enabled = true
	}
	if *demoPace > 0 {
		cfg.Demo.Pace = *demoPace
	}
	cfg.Since = *since

	if cfg.Demo.Enabled {
		if *demoReset {
			if err := removeDemoDB(cfg); err != nil {
				return err
			}
			log.Info("demo database reset", "path", cfg.DemoDBPath())
		}
		log.Info("DEMO MODE — synthetic data in " + cfg.DemoDBPath() + "; your " + cfg.DBPath() + " is untouched")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.ActiveDBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("store ready", "path", cfg.ActiveDBPath())

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

	// Demo mode is pinned to the embedded rate snapshot: no fetch, no cache,
	// no network. Two things depend on it. A demo is reproducible — the same
	// seed has to build the same world, and a world whose amount_base came
	// from whatever the ECB published this morning is a different world every
	// day. And a demo is often shown on a laptop with no network at all, where
	// "makes no outbound requests" is a promise worth keeping literally.
	var rateCache fx.RateStore
	if !cfg.Demo.Enabled {
		rateCache = st
	}
	converter := fx.New(fx.Options{
		Base:    cfg.DisplayCurrency,
		Enabled: cfg.FXEnabled() && !cfg.Demo.Enabled,
		Store:   rateCache,
		Logger:  log,
	})
	if !cfg.Demo.Enabled {
		if err := converter.LoadCached(ctx); err != nil {
			log.Warn("could not read cached fx rates", "error", err)
		}
	}
	// Run is a no-op that simply waits for ctx when the converter is disabled,
	// so this is safe to start either way — and in demo mode it is what
	// guarantees the seed below runs against a converter that will never move
	// under it.
	go converter.Run(ctx)

	b := bus.New(256)
	pipe := pipeline.New(st, engine, b, log)
	pipe.DisplayCurrency = cfg.DisplayCurrency
	pipe.FX = converter
	pipe.ChestEnabled = cfg.ChestEnabled()
	pipe.ChestAutoOpenAfterHours = cfg.ChestAutoOpenAfterHours()
	if cfg.Demo.Enabled {
		// A demo that opens its own chest before anyone arrives has given away
		// the best thing it had to show.
		pipe.ChestEnabled = true
		pipe.ChestAutoOpenAfterHours = 0
	}
	go pipe.RunChestAutoOpen(ctx)

	// Quest 5: goals with windows, and the flagged days that ask why. The
	// quest service ingests its completion drops through the same pipeline as
	// everything else, so a finished quest lands in the feed with a sound.
	questSvc := quests.NewService(st, pipe, b, cfg.DisplayCurrency, log)
	mysterySvc := mysteries.NewService(st, pipe, b, log)
	detector := mysteries.NewDetector(st, b, cfg.DisplayCurrency, log)

	// Quest 6: the Codex. Achievements unlock through the same pipeline as
	// everything else, so a trophy lands in the feed with a sound — and, on a
	// first run over an existing database, in today's chest instead.
	codexSvc := codex.NewService(st, pipe, b, cfg.DisplayCurrency, log)

	// A drop that just pushed a quest over its target — or an achievement over
	// its threshold — should pay out now, not in a minute. Both Triggers only
	// nudge a goroutine, so ingest stays fast.
	pipe.AfterIngest = func(core.Event) {
		questSvc.Trigger()
		codexSvc.Trigger()
	}

	var sources []core.Source
	if cfg.Demo.Enabled {
		// Demo mode mounts no real source at all: no credentials are read, no
		// store is polled and /hooks/revenuecat receives nothing. These are
		// labels for the dashboard header, and their Poll returns nothing.
		sources = demo.Sources()

		d := demo.New(st, pipe, cfg.RulesPath, demo.Options{
			Seed: cfg.Demo.Seed,
			Pace: cfg.Demo.Pace,
			Days: cfg.Demo.Days,
		}, log)
		res, err := d.Seed(ctx)
		if err != nil {
			return err
		}
		if res.Generated > 0 {
			log.Info("demo history seeded",
				"days", res.Generated, "from", res.From, "to", res.To,
				"events", res.Events, "drops", res.Drops, "countries", res.Countries,
				"xp", res.XP, "took", res.Took.Round(time.Millisecond).String())
		}
		go d.Run(ctx)

		// A demo should open on a board with quests already in progress and a
		// few open questions about the history it just invented.
		if err := questSvc.Startup(ctx); err != nil {
			log.Warn("demo quest generation failed", "error", err)
		}
		if found, err := detector.Run(ctx); err != nil {
			log.Warn("demo mystery detection failed", "error", err)
		} else if len(found) > 0 {
			log.Info("demo mysteries found", "count", len(found))
		}
		// And a Codex with trophies already on the wall, dated to the days the
		// invented history actually earned them. Their drops go into today's
		// chest, so the demo opens with something to unwrap rather than with
		// fifteen achievement sounds firing at once.
		if res, err := codexSvc.Evaluate(ctx); err != nil {
			log.Warn("demo codex evaluation failed", "error", err)
		} else if len(res.Unlocked) > 0 {
			log.Info("demo achievements unlocked", "count", len(res.Unlocked), "backfilled", res.Backfilled)
		}
	}
	if !cfg.Demo.Enabled && cfg.RevenueCatEnabled() {
		rc := revenuecat.New(cfg.Sources.RevenueCat.Secret, log)
		sources = append(sources, rc)
		if cfg.Sources.RevenueCat.Secret == "" {
			log.Warn("revenuecat webhook has no secret; anyone who can reach /hooks/revenuecat can post drops")
		}
	}
	if !cfg.Demo.Enabled && len(cfg.Sources.Flathub.Apps) > 0 {
		sources = append(sources, flathub.New(
			cfg.Sources.Flathub.Apps,
			cfg.Sources.Flathub.BackfillDays,
			cfg.Since,
			log,
		))
		log.Info("flathub source configured",
			"apps", cfg.Sources.Flathub.Apps, "backfill_days", cfg.Sources.Flathub.BackfillDays)
	}
	if !cfg.Demo.Enabled && cfg.Sources.AppStore.Configured() {
		src, err := appstore.New(cfg.Sources.AppStore, log)
		if err != nil {
			// A source that cannot start is worth a loud warning, not a dead
			// dashboard: everything else still works without it.
			log.Warn("app store connect source unavailable", "error", err)
		} else {
			src.Since = cfg.Since
			sources = append(sources, src)
			log.Info("app store connect source configured",
				"vendor", cfg.Sources.AppStore.VendorNumber,
				"backfill_days", cfg.Sources.AppStore.BackfillDays)
		}
	}
	if !cfg.Demo.Enabled && cfg.Sources.GooglePlay.Configured() {
		src, err := googleplay.New(cfg.Sources.GooglePlay, log)
		if err != nil {
			log.Warn("google play source unavailable", "error", err)
		} else {
			sources = append(sources, src)
			log.Info("google play source configured",
				"bucket", src.Bucket,
				"packages", cfg.Sources.GooglePlay.Packages,
				"backfill_months", src.BackfillMonths)
		}
	}
	if !cfg.Demo.Enabled && cfg.Sources.MicrosoftStore.Configured() {
		src, err := microsoftstore.New(cfg.Sources.MicrosoftStore, log)
		if err != nil {
			log.Warn("microsoft store source unavailable", "error", err)
		} else {
			src.Since = cfg.Since
			sources = append(sources, src)
			log.Info("microsoft store source configured", "apps", cfg.Sources.MicrosoftStore.Apps)
		}
	}
	if !cfg.Demo.Enabled && cfg.Sources.Snapcraft.Configured() {
		src, err := snapcraft.New(cfg.Sources.Snapcraft, log)
		if err != nil {
			log.Warn("snapcraft source unavailable", "error", err)
		} else {
			src.Since = cfg.Since
			sources = append(sources, src)
			log.Info("snapcraft source configured", "snaps", cfg.Sources.Snapcraft.Snaps)
		}
	}
	if !cfg.Demo.Enabled && cfg.Sources.GitHub.Configured() {
		src, err := github.New(cfg.Sources.GitHub, log)
		if err != nil {
			log.Warn("github source unavailable", "error", err)
		} else {
			src.Since = cfg.Since
			sources = append(sources, src)
			log.Info("github source configured", "repos", cfg.Sources.GitHub.Repos)
		}
	}
	if !cfg.Demo.Enabled && cfg.Sources.Webhook.Enabled {
		src, err := webhook.New(cfg.Sources.Webhook, log)
		if err != nil {
			log.Warn("webhook source unavailable", "error", err)
		} else {
			sources = append(sources, src)
			log.Info("generic webhook configured at POST /hooks/webhook")
			if cfg.Sources.Webhook.Secret == "" {
				log.Warn("generic webhook has no secret; anyone who can reach /hooks/webhook can post drops")
			}
		}
	}
	if len(sources) == 0 {
		log.Warn("no sources configured; see configs/loot.example.yaml")
	}

	sched := pipeline.NewScheduler(pipe, st, sources, log)
	go sched.Run(ctx)
	go questSvc.Run(ctx)
	go detector.RunLoop(ctx)
	go codexSvc.Run(ctx)

	srv := server.New(cfg, st, b, pipe, sources, web.DistFS(), log)
	srv.Quests = questSvc
	srv.Mysteries = mysterySvc
	srv.Codex = codexSvc
	// An unlock invalidates the wall's memo, so the refetch its websocket nudge
	// provokes cannot be answered with a board from before the trophy.
	codexSvc.OnChange = srv.InvalidateCodex
	return srv.ListenAndServe(ctx)
}

// removeDemoDB deletes the demo database and its write-ahead log. It asks no
// questions: demo data is generated, and the real database is a different file
// that this function cannot name.
func removeDemoDB(cfg config.Config) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(cfg.DemoDBPath() + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", cfg.DemoDBPath()+suffix, err)
		}
	}
	return nil
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
