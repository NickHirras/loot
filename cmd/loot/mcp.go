package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickhirras/loot/internal/mcp"
)

const mcpUsage = `Usage:
  loot mcp [flags]

Run a Model Context Protocol (MCP) stdio server that exposes a running Loot
server's metrics and reports as tools for AI agents (Claude Code, Codex,
Cursor, Claude Desktop, T3 Code, etc.).

Read-only by default. Pass --allow-writes to also expose action tools
(open chests, create quests, solve mysteries, slay bosses).

Configure the target server with flags or environment variables:
  LOOT_URL          Loot server base URL (default: http://localhost:8080)
  LOOT_API_TOKEN    Bearer / X-API-Key token (optional)
  LOOT_API_HEADERS  Extra proxy headers separated by ';' or newlines (optional)

Flags:
`

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	cf := bindClientFlags(fs)
	allowWrites := fs.Bool("allow-writes", false, "also expose mutating tools (chest open, create quest, solve mystery, slay boss)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), mcpUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := cf.client()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := mcp.New(mcp.Options{
		Client:      client,
		Version:     version,
		AllowWrites: *allowWrites,
	})
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
