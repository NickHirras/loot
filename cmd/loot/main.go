// Command loot runs the Loot server and the terminal drop tail.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `loot — a gamified dashboard for indie app metrics

Usage:
  loot serve [flags]        Run the API, websocket stream, sources and dashboard (default)
  loot tail  [flags]        Stream drops into your terminal
  loot check [flags]        Verify every configured source and exit
  loot chest [open [date]]  List the daily chests waiting, or open one
  loot fx    [recompute]    Show exchange rates, or re-convert stored amounts
  loot version              Print the version

Run "loot <command> -h" for the flags of a command.
`

func main() {
	args := os.Args[1:]

	// `loot` with no command, or with flags only, means `loot serve`.
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "tail":
		err = runTail(args)
	case "check":
		err = runCheck(args)
	case "chest":
		err = runChest(args)
	case "fx":
		err = runFX(args)
	case "version":
		fmt.Println("loot " + version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
