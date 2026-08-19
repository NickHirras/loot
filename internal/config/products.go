package config

import (
	"sort"
	"strings"
)

// Products is the app scope: the canonical list of the things you actually
// ship, and the raw names each source calls them.
//
// A source names an app whatever its own console names it. App Store Connect
// reports a title ("Nistis: Fasting Timer") or an Apple ID ("6763687102"),
// Google Play a package name, RevenueCat an app id ("app5525946104") or the
// display name Loot mapped it to, GitHub an "owner/repo". Those are five names
// for one product, and a dashboard that shows them as five apps is a dashboard
// you cannot scope.
//
// A *product* is the canonical name; matching is exact and case-insensitive on
// the raw name a source reported. Anything unmapped keeps its raw name and is
// still selectable in the UI — the mapping is a convenience, never a filter
// that can hide data.
type Products []Product

// Product is one canonical app and the raw names it answers to.
type Product struct {
	// Name is the canonical name: what the scope selector shows.
	Name string `yaml:"name"`
	// Match maps a source id ("appstore", "revenuecat", "googleplay", …) to the
	// raw app names that source uses for this product.
	Match map[string][]string `yaml:"match"`
}

// Resolve returns the canonical product name for a raw (source, app) pair.
//
// Three rules, in order:
//
//   - an empty app stays empty. Loot's own global events (an achievement) have
//     no app, and inventing one for them would put a trophy inside a scope.
//   - a raw name listed under this source resolves to that product's name.
//   - failing that, a raw name listed under *any* source resolves too. This is
//     not sloppiness: Loot mints events of its own — a settlement, a quest
//     completion, a boss — under the reserved source "loot", carrying the app
//     name of whichever real source triggered them. Without the fallback, a
//     settlement found in an App Store row would resolve to the raw App Store
//     title and invent a phantom product beside the real one. A raw name that
//     identifies a product under one source identifies it everywhere.
//   - anything else keeps its raw name, so an app you have not mapped yet is
//     still visible and still selectable.
func (p Products) Resolve(source, app string) string {
	app = strings.TrimSpace(app)
	if app == "" {
		return ""
	}
	source = strings.ToLower(strings.TrimSpace(source))

	// A product's own name always resolves to itself, so re-running the
	// resolver over already-mapped rows is a no-op rather than a demotion.
	for _, prod := range p {
		if strings.EqualFold(strings.TrimSpace(prod.Name), app) {
			return strings.TrimSpace(prod.Name)
		}
	}
	// The source's own list first, so two products that genuinely share a raw
	// name on different stores are told apart by the source that reported it.
	if source != "" {
		for _, prod := range p {
			for key, names := range prod.Match {
				if !strings.EqualFold(strings.TrimSpace(key), source) {
					continue
				}
				if matches(names, app) {
					return strings.TrimSpace(prod.Name)
				}
			}
		}
	}
	for _, prod := range p {
		for _, names := range prod.Match {
			if matches(names, app) {
				return strings.TrimSpace(prod.Name)
			}
		}
	}
	return app
}

// matches reports whether app is one of names, ignoring case and padding.
func matches(names []string, app string) bool {
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), app) {
			return true
		}
	}
	return false
}

// Names lists the canonical product names in configuration order.
func (p Products) Names() []string {
	out := make([]string, 0, len(p))
	for _, prod := range p {
		if name := strings.TrimSpace(prod.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// Has reports whether name is a configured product (case-insensitive).
func (p Products) Has(name string) bool {
	for _, prod := range p {
		if strings.EqualFold(strings.TrimSpace(prod.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// Sources lists the source ids a product matches on, sorted, so `loot apps`
// prints them in a stable order.
func (p Product) Sources() []string {
	out := make([]string, 0, len(p.Match))
	for key := range p.Match {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
