package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// chestUsage documents both forms of the command.
const chestUsage = `Usage:
  loot chest [flags]              List the chests waiting to be opened
  loot chest open [date] [flags]  Open a chest (default: the oldest one)
  loot chest open --all           Open every chest waiting, oldest first

Flags:
`

// runChest talks to a running server over HTTP rather than to the database, so
// that opening a chest reaches every connected dashboard and terminal tail —
// the reveal is the point, and a direct database write would be silent.
func runChest(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	date := ""
	if sub == "open" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		date = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("chest", flag.ExitOnError)
	rawURL := fs.String("url", "http://localhost:8080", "Loot server base URL")
	plain := fs.Bool("no-color", false, "disable ANSI colors")
	all := fs.Bool("all", false, "open every chest waiting, oldest first")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), chestUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	base, err := baseURL(*rawURL)
	if err != nil {
		return err
	}
	if date != "" {
		if _, err := time.Parse(core.DayLayout, date); err != nil {
			return fmt.Errorf("date must be YYYY-MM-DD, got %q", date)
		}
	}
	if *all && date != "" {
		return fmt.Errorf("--all opens every chest; drop the %s argument", date)
	}
	if *all && sub != "open" {
		return fmt.Errorf("--all only applies to `loot chest open`")
	}

	ctx := context.Background()
	switch sub {
	case "", "list":
		return listChests(ctx, base)
	case "open":
		return openChest(ctx, base, date, *all, *plain)
	default:
		return fmt.Errorf("unknown chest command %q\n\n%s", sub, chestUsage)
	}
}

type chestListResponse struct {
	Chests []core.ChestSummary `json:"chests"`
}

func listChests(ctx context.Context, base string) error {
	var out chestListResponse
	if err := httpJSON(ctx, http.MethodGet, base+"/api/chest", nil, &out); err != nil {
		return err
	}
	if len(out.Chests) == 0 {
		fmt.Println("no chests waiting")
		return nil
	}
	for _, c := range out.Chests {
		fmt.Printf("📦 %s  %s  +%d xp  %s\n", c.Date, plural(c.Count, "drop"), c.XP, rarityBreakdown(c.ByRarity))
	}
	return nil
}

type chestOpenDrop struct {
	Rarity    core.Rarity `json:"rarity"`
	Title     string      `json:"title"`
	Subtitle  string      `json:"subtitle"`
	XP        int         `json:"xp"`
	Source    string      `json:"source"`
	Country   string      `json:"country"`
	ChestDate string      `json:"chest_date"`
}

type chestOpenResponse struct {
	// Opened is the first (oldest) chest opened; OpenedDates is all of them,
	// which is only ever longer than one for --all.
	Opened      string          `json:"opened"`
	OpenedDates []string        `json:"opened_dates"`
	Count       int             `json:"count"`
	Drops       []chestOpenDrop `json:"drops"`
}

// openChest opens one chest, or — with all — every chest waiting. The bulk
// haul comes back as one flat list already grouped by day in reveal order, so
// it is printed chest by chest with a grand total underneath.
func openChest(ctx context.Context, base, date string, all, plain bool) error {
	req := map[string]any{}
	if all {
		req["all"] = true
	} else {
		req["date"] = date
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	var out chestOpenResponse
	if err := httpJSON(ctx, http.MethodPost, base+"/api/chest/open", body, &out); err != nil {
		return err
	}
	if out.Count == 0 {
		if all {
			fmt.Println("no chests to open")
		} else {
			fmt.Println("no chest to open")
		}
		return nil
	}

	dates := out.OpenedDates
	if len(dates) == 0 && out.Opened != "" {
		dates = []string{out.Opened}
	}

	dim, reset := ansiDim, ansiReset
	if plain {
		dim, reset = "", ""
	}

	grand := 0
	for _, day := range dates {
		drops := make([]chestOpenDrop, 0, len(out.Drops))
		for _, d := range out.Drops {
			// A single open of an older server answers without chest_date on
			// every drop; with one chest open, they are all its own.
			if d.ChestDate == day || (len(dates) == 1 && d.ChestDate == "") {
				drops = append(drops, d)
			}
		}
		fmt.Printf("📦 opened %s — %s\n", day, plural(len(drops), "drop"))
		total := 0
		for _, d := range drops {
			color := rarityColor[d.Rarity]
			if plain {
				color = ""
			}
			fmt.Printf("   %s%-9s%s %s", color, strings.ToUpper(string(d.Rarity)), reset, d.Title)
			if d.Subtitle != "" {
				fmt.Printf(" %s· %s%s", dim, d.Subtitle, reset)
			}
			fmt.Printf(" %s[+%d xp %s]%s\n", dim, d.XP, d.Source, reset)
			total += d.XP
		}
		fmt.Printf("   %stotal +%d xp%s\n", dim, total, reset)
		grand += total
	}
	if len(dates) > 1 {
		fmt.Printf("📦 %s, %s, %s+%d xp%s\n",
			plural(len(dates), "chest"), plural(out.Count, "drop"), dim, grand, reset)
	}
	return nil
}

// plural renders "1 drop" and "4 drops".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// rarityBreakdown renders {"rare":2,"common":1} as "1 common, 2 rare" in
// ladder order, so a chest reads the same way every time.
func rarityBreakdown(byRarity map[string]int) string {
	parts := make([]string, 0, len(byRarity))
	for _, r := range core.Rarities {
		if n := byRarity[string(r)]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, r))
		}
	}
	return strings.Join(parts, ", ")
}

// httpJSON performs one JSON request against the server.
func httpJSON(ctx context.Context, method, url string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

// baseURL normalizes a --url flag into a scheme://host prefix.
func baseURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad --url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q in --url", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery = ""
	return u.String(), nil
}
