package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
)

// ANSI colors, one per rarity, matching the web UI's palette.
var rarityColor = map[core.Rarity]string{
	core.Common:    "\033[90m",   // grey
	core.Uncommon:  "\033[32m",   // green
	core.Rare:      "\033[36m",   // cyan/blue
	core.Epic:      "\033[35m",   // magenta/purple
	core.Legendary: "\033[33;1m", // bright gold
	core.Cursed:    "\033[31;1m", // bright red
}

const ansiReset = "\033[0m"
const ansiDim = "\033[2m"

func runTail(args []string) error {
	fs := flag.NewFlagSet("tail", flag.ExitOnError)
	var (
		rawURL = fs.String("url", "http://localhost:8080", "Loot server base URL")
		noBell = fs.Bool("no-bell", false, "do not ring the terminal bell for rare+ drops")
		plain  = fs.Bool("no-color", false, "disable ANSI colors")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: loot tail [flags]\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	wsURL, err := websocketURL(*rawURL)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "%sloot tail — %s%s\n", ansiDim, wsURL, ansiReset)

	// Reconnect forever: a tail that dies when the server restarts is useless.
	backoff := time.Second
	for {
		err := streamDrops(ctx, wsURL, *noBell, *plain)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "%sdisconnected (%v), retrying in %s%s\n", ansiDim, err, backoff, ansiReset)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func streamDrops(ctx context.Context, wsURL string, noBell, plain bool) error {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	// Drops are small, but a burst of backfill can be chatty.
	conn.SetReadLimit(1 << 20)

	for {
		var msg bus.Message
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err
		}
		if msg.Type != "drop" || msg.Drop == nil {
			// {"type":"chest"} and friends are for the dashboard badge.
			continue
		}
		fmt.Println(formatDrop(*msg.Drop, msg.Event, msg.Chest, plain))
		if !noBell && msg.Drop.Rarity.Rank() >= core.Rare.Rank() {
			fmt.Print("\a")
		}
	}
}

func formatDrop(d core.Drop, ev *core.Event, fromChest bool, plain bool) string {
	color, reset, dim := rarityColor[d.Rarity], ansiReset, ansiDim
	if plain {
		color, reset, dim = "", "", ""
	}

	ts := d.CreatedAt.Local().Format("15:04:05")
	badge := strings.ToUpper(string(d.Rarity))

	// A chest reveal is a drop that already happened; the box says so.
	prefix := ""
	if fromChest {
		prefix = "📦 "
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s %s%-9s%s %s%s", dim, ts, reset, color, badge, reset, prefix, d.Title)
	if d.Subtitle != "" {
		fmt.Fprintf(&b, " %s· %s%s", dim, d.Subtitle, reset)
	}

	meta := []string{fmt.Sprintf("+%d xp", d.XP)}
	if ev != nil {
		if ev.Source != "" {
			meta = append(meta, ev.Source)
		}
		if ev.Country != "" {
			meta = append(meta, ev.Country)
		}
	}
	fmt.Fprintf(&b, " %s[%s]%s", dim, strings.Join(meta, " "), reset)
	return b.String()
}

// websocketURL turns a base URL like http://localhost:8080 into ws://host/ws.
func websocketURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad --url %q: %w", raw, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// already a websocket URL
	default:
		return "", fmt.Errorf("unsupported scheme %q in --url", u.Scheme)
	}
	if !strings.HasSuffix(u.Path, "/ws") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/ws"
	}
	u.RawQuery = ""
	return u.String(), nil
}
