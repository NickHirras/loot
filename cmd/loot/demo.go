package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"github.com/nickhirras/loot/internal/config"
)

const demoUsage = `Usage: loot demo <command> [flags]

Commands:
  reset     Delete the demo database so the next "loot serve --demo" builds a fresh world

Demo mode runs Loot on synthetic data in <data_dir>/demo.db. It never reads or
writes your real database, so resetting it cannot lose anything you care about.

Start it with:
  loot serve --demo
`

func runDemo(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "reset":
		return runDemoReset(args)
	case "", "help", "-h", "--help":
		fmt.Print(demoUsage)
		return nil
	default:
		return fmt.Errorf("unknown demo command %q\n\n%s", cmd, demoUsage)
	}
}

// runDemoReset deletes the demo database without asking. There is no
// confirmation prompt on purpose: every byte in that file was invented by
// `loot serve --demo`, and the next run invents it again.
func runDemoReset(args []string) error {
	set := flag.NewFlagSet("demo reset", flag.ExitOnError)
	configPath := set.String("config", config.DefaultPath, "path to loot.yaml")
	dataDir := set.String("data-dir", "", "override data directory")
	set.Usage = func() {
		fmt.Fprintln(set.Output(), "Usage: loot demo reset [flags]\n\nFlags:")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, flagSet(set, "config"))
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	removed := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := cfg.DemoDBPath() + suffix
		switch err := os.Remove(path); {
		case err == nil:
			removed = true
		case errors.Is(err, fs.ErrNotExist):
		default:
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	if removed {
		fmt.Printf("removed %s — the next `loot serve --demo` will build a fresh world\n", cfg.DemoDBPath())
	} else {
		fmt.Printf("%s does not exist; nothing to reset\n", cfg.DemoDBPath())
	}
	return nil
}
