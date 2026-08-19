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

	ctx := context.Background()
	switch sub {
	case "", "list":
		return listChests(ctx, base)
	case "open":
		return openChest(ctx, base, date, *plain)
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

type chestOpenResponse struct {
	Opened string `json:"opened"`
	Count  int    `json:"count"`
	Drops  []struct {
		Rarity   core.Rarity `json:"rarity"`
		Title    string      `json:"title"`
		Subtitle string      `json:"subtitle"`
		XP       int         `json:"xp"`
		Source   string      `json:"source"`
		Country  string      `json:"country"`
	} `json:"drops"`
}

func openChest(ctx context.Context, base, date string, plain bool) error {
	body, err := json.Marshal(map[string]string{"date": date})
	if err != nil {
		return err
	}

	var out chestOpenResponse
	if err := httpJSON(ctx, http.MethodPost, base+"/api/chest/open", body, &out); err != nil {
		return err
	}
	if out.Count == 0 {
		fmt.Println("no chest to open")
		return nil
	}

	total := 0
	fmt.Printf("📦 opened %s — %s\n", out.Opened, plural(out.Count, "drop"))
	for _, d := range out.Drops {
		color, reset := rarityColor[d.Rarity], ansiReset
		dim := ansiDim
		if plain {
			color, reset, dim = "", "", ""
		}
		fmt.Printf("   %s%-9s%s %s", color, strings.ToUpper(string(d.Rarity)), reset, d.Title)
		if d.Subtitle != "" {
			fmt.Printf(" %s· %s%s", dim, d.Subtitle, reset)
		}
		fmt.Printf(" %s[+%d xp %s]%s\n", dim, d.XP, d.Source, reset)
		total += d.XP
	}
	fmt.Printf("   %stotal +%d xp%s\n", ansiDim, total, ansiReset)
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
