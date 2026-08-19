package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/sources/appstore"
	"github.com/nickhirras/loot/internal/sources/flathub"
	"github.com/nickhirras/loot/internal/sources/github"
	"github.com/nickhirras/loot/internal/sources/googleplay"
	"github.com/nickhirras/loot/internal/sources/microsoftstore"
	"github.com/nickhirras/loot/internal/sources/revenuecat"
	"github.com/nickhirras/loot/internal/sources/snapcraft"
	"github.com/nickhirras/loot/internal/sources/webhook"
)

// checkTimeout caps the whole run: a source that hangs should not hang the
// command.
const checkTimeout = 30 * time.Second

// runCheck verifies each configured source without starting the server. It
// exits non-zero if anything is wrong, so it works as a container healthcheck
// or a post-deploy smoke test.
func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to loot.yaml")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: loot check [flags]\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, flagSet(fs, "config"))
	if err != nil {
		return err
	}

	// Source constructors log at startup; a check should print its own report,
	// not a log stream.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	type candidate struct {
		name   string
		source core.Source
		err    error
		note   string
	}
	var candidates []candidate

	if cfg.RevenueCatEnabled() {
		candidates = append(candidates, candidate{
			name:   revenuecat.Name,
			source: revenuecat.New(cfg.Sources.RevenueCat.Secret, quiet),
			note:   "webhook at POST /hooks/revenuecat",
		})
	}
	if len(cfg.Sources.Flathub.Apps) > 0 {
		candidates = append(candidates, candidate{
			name: flathub.Name,
			source: flathub.New(cfg.Sources.Flathub.Apps, cfg.Sources.Flathub.BackfillDays,
				cfg.Since, quiet),
			note: fmt.Sprintf("%d app(s)", len(cfg.Sources.Flathub.Apps)),
		})
	}
	if cfg.Sources.AppStore.Configured() {
		src, err := appstore.New(cfg.Sources.AppStore, quiet)
		candidates = append(candidates, candidate{
			name: appstore.Name, source: src, err: err,
			note: "vendor " + cfg.Sources.AppStore.VendorNumber,
		})
	}
	if cfg.Sources.GooglePlay.Configured() {
		src, err := googleplay.New(cfg.Sources.GooglePlay, quiet)
		candidates = append(candidates, candidate{
			name: googleplay.Name, source: src, err: err,
			note: "bucket " + cfg.Sources.GooglePlay.Bucket,
		})
	}

	if cfg.Sources.MicrosoftStore.Configured() {
		src, err := microsoftstore.New(cfg.Sources.MicrosoftStore, quiet)
		candidates = append(candidates, candidate{name: microsoftstore.Name, source: src, err: err,
			note: fmt.Sprintf("%d app(s)", len(cfg.Sources.MicrosoftStore.Apps))})
	}
	if cfg.Sources.Snapcraft.Configured() {
		src, err := snapcraft.New(cfg.Sources.Snapcraft, quiet)
		candidates = append(candidates, candidate{name: snapcraft.Name, source: src, err: err,
			note: fmt.Sprintf("%d snap(s)", len(cfg.Sources.Snapcraft.Snaps))})
	}
	if cfg.Sources.GitHub.Configured() {
		src, err := github.New(cfg.Sources.GitHub, quiet)
		candidates = append(candidates, candidate{name: github.Name, source: src, err: err,
			note: fmt.Sprintf("%d repo(s)", len(cfg.Sources.GitHub.Repos))})
	}
	if cfg.Sources.Webhook.Enabled {
		src, err := webhook.New(cfg.Sources.Webhook, quiet)
		candidates = append(candidates, candidate{name: webhook.Name, source: src, err: err,
			note: "webhook at POST /hooks/webhook"})
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })

	if len(candidates) == 0 {
		fmt.Println("no sources configured; see configs/loot.example.yaml")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	failed := 0
	for _, c := range candidates {
		err := c.err
		note := c.note
		switch {
		case err != nil:
			// Constructing it already failed; nothing to call.
		case c.source == nil:
			err = fmt.Errorf("source did not start")
		default:
			checker, ok := c.source.(core.Checker)
			if !ok {
				note = "no check available"
				break
			}
			err = checker.Check(ctx)
		}

		if err != nil {
			failed++
			fmt.Printf("✗ %-12s %v\n", c.name, err)
			continue
		}
		fmt.Printf("✓ %-12s %s\n", c.name, note)
	}

	fmt.Printf("\n%d of %d sources ready\n", len(candidates)-failed, len(candidates))
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}
