package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/store"
)

const fxUsage = `Usage:
  loot fx rates [flags]      Show the exchange rates Loot would use
  loot fx recompute [flags]  Re-convert every stored amount into the display currency

Flags:
`

// runFX operates directly on the database: it is an offline maintenance
// command, and unlike opening a chest it produces nothing anyone needs to see
// live. Do not run it against a database a server is writing to.
func runFX(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("fx", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to loot.yaml")
	offline := fs.Bool("offline", false, "do not fetch rates; use the cache and the embedded snapshot")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), fxUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, flagSet(fs, "config"))
	if err != nil {
		return err
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.ActiveDBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	converter := fx.New(fx.Options{
		Base:    cfg.DisplayCurrency,
		Enabled: cfg.FXEnabled() && !*offline,
		Store:   st,
		Logger:  log,
	})
	if err := converter.LoadCached(ctx); err != nil {
		return err
	}
	if err := converter.Refresh(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "rate fetch failed (%v); using cached or embedded rates\n", err)
	}

	asOf, fetched := converter.AsOf()
	source := "embedded snapshot"
	if fetched {
		source = "ECB rates"
	}

	switch sub {
	case "", "rates":
		fmt.Printf("display currency %s · %s as of %s\n", converter.Base(), source, asOf)
		return nil
	case "recompute":
		updated, skipped, err := st.RecomputeAmountBase(ctx, func(amount float64, currency string) (float64, bool) {
			if currency == "" {
				// No currency recorded: the amount is already whatever the
				// dashboard displays.
				return amount, true
			}
			return converter.Convert(amount, currency, converter.Base())
		})
		if err != nil {
			return err
		}
		fmt.Printf("recomputed %d amounts into %s (%s as of %s)\n", updated, converter.Base(), source, asOf)
		if skipped > 0 {
			fmt.Printf("%d amounts skipped: no rate for their currency\n", skipped)
		}
		return nil
	default:
		return fmt.Errorf("unknown fx command %q\n\n%s", sub, fxUsage)
	}
}
