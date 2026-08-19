package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/demo"
	"github.com/nickhirras/loot/internal/store"
)

const appsUsage = `Usage:
  loot apps [flags]        Show every app your sources have reported, and what it maps to
  loot apps remap [flags]  Recompute every event's product from the current apps: mapping

Flags:
`

// runApps is the answer to "why is my dashboard showing four apps when I ship
// two?".
//
// Every source names an app whatever its own console names it — a title, an
// Apple ID, a package name, an "owner/repo" — and the `apps:` block in
// loot.yaml is how those become one product. This command prints what the
// database has actually seen and what each pair currently resolves to, so a
// mapping that missed something is one line of output rather than an
// afternoon.
//
// It reads the database directly, exactly as `loot fx` does. `remap` writes to
// it, so do not run that against a database a server is writing to — the
// server does the same remap at every startup anyway, which is normally all
// anybody needs.
func runApps(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("apps", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to loot.yaml")
	demoMode := fs.Bool("demo", false, "read the demo database instead of loot.db")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), appsUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, flagSet(fs, "config"))
	if err != nil {
		return err
	}
	if *demoMode {
		cfg.Demo.Enabled = true
	}
	products := cfg.Apps
	if cfg.Demo.Enabled && len(products) == 0 {
		products = demo.Products()
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.ActiveDBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	switch sub {
	case "", "list":
		return listApps(ctx, st, products)
	case "remap":
		n, err := st.RemapProducts(ctx, products)
		if err != nil {
			return err
		}
		fmt.Printf("remapped %d event(s) across %d configured product(s)\n", n, len(products))
		return listApps(ctx, st, products)
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", sub, appsUsage)
	}
}

// listApps prints the mapping status: one block per product, then the pairs
// nothing in the config claimed.
func listApps(ctx context.Context, st *store.Store, products config.Products) error {
	pairs, err := st.ProductPairs(ctx)
	if err != nil {
		return err
	}

	// Grouped by what the *current* config resolves each pair to, not by what
	// the database happens to say. Those differ exactly when the mapping has
	// been edited since the last startup, and showing the stale answer would
	// make a correct mapping look broken. The difference is called out per row
	// instead, which is also the only prompt anyone needs to run `remap`.
	byProduct := map[string][]store.ProductPair{}
	order := []string{}
	for _, name := range products.Names() {
		byProduct[name] = nil
		order = append(order, name)
	}
	configured := len(order)
	var unmapped []store.ProductPair
	pending := 0
	for _, p := range pairs {
		resolved := products.Resolve(p.Source, p.App)
		if _, ok := byProduct[resolved]; !ok {
			byProduct[resolved] = nil
			order = append(order, resolved)
		}
		if resolved != p.Product {
			pending++
		}
		row := p
		row.Product = resolved
		byProduct[resolved] = append(byProduct[resolved], row)
		if !products.Has(resolved) {
			unmapped = append(unmapped, row)
		}
	}
	sort.Strings(order[configured:])

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PRODUCT\tSOURCE\tAPP AS REPORTED\tEVENTS\tFIRST SEEN")
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		rows := byProduct[name]
		label := name
		if !products.Has(name) {
			// Unmapped products are the ones worth noticing, so they are
			// marked here as well as listed again below.
			label = name + " *"
		}
		if len(rows) == 0 {
			fmt.Fprintf(w, "%s\t—\t(nothing reported yet)\t0\t\n", label)
			continue
		}
		for i, p := range rows {
			shown := label
			if i > 0 {
				shown = ""
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", shown, p.Source, p.App, p.Events, p.FirstSeen)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if pending > 0 {
		fmt.Printf("\n%d (source, app) pair(s) are stored under a different product than the\n", pending)
		fmt.Println("current mapping gives them. `loot serve` fixes that at startup; to do it")
		fmt.Println("now, run: loot apps remap")
	}

	if len(unmapped) == 0 {
		fmt.Println("\nevery app your sources report is mapped to a product.")
		return nil
	}

	fmt.Printf("\n* %d app(s) are not mapped to a product. They still show up, under the\n", len(unmapped))
	fmt.Println("  name their source reported. To group them, add to loot.yaml:")
	fmt.Println("\napps:")

	// One block per unmapped product rather than one for all of them. The
	// output is meant to be pasted, and a single block merging three apps into
	// one product is a paste that quietly makes things worse.
	grouped := map[string]map[string][]string{}
	var names []string
	for _, p := range unmapped {
		if _, ok := grouped[p.Product]; !ok {
			grouped[p.Product] = map[string][]string{}
			names = append(names, p.Product)
		}
		grouped[p.Product][p.Source] = append(grouped[p.Product][p.Source], p.App)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  - name: %s\n    match:\n", name)
		bySource := grouped[name]
		sources := make([]string, 0, len(bySource))
		for source := range bySource {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		for _, source := range sources {
			apps := bySource[source]
			sort.Strings(apps)
			quoted := make([]string, 0, len(apps))
			for _, a := range apps {
				quoted = append(quoted, `"`+a+`"`)
			}
			fmt.Printf("      %s: [%s]\n", source, strings.Join(quoted, ", "))
		}
	}
	return nil
}
