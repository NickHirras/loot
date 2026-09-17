// Package web embeds the compiled Svelte single-page app so `loot` ships as
// one binary. The dist directory is git-ignored apart from .gitkeep, which is
// what lets this package compile before the frontend has ever been built.
package web

import (
	"embed"
	"io/fs"
)

// The `all:` prefix includes dotfiles, so the placeholder .gitkeep satisfies
// the embed on a fresh clone with no build output.
//
//go:embed all:dist
var distFS embed.FS

// DistFS returns the built frontend rooted at dist/. When the app has not been
// built, the returned FS simply has no index.html and the server falls back to
// a "not built yet" page.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return distFS
	}
	return sub
}

// The message catalog, embedded as its source JSON rather than as the compiled
// Paraglide output: the dashboard reads the compiled functions, but the server
// needs to answer "what is this key in German?" for a key it only learns at
// runtime — an achievement's own key — and a flat JSON file is the only form
// that can be indexed. See internal/i18n. These files are checked in, so
// unlike dist/ this embed never needs a build to have run.
//
//go:embed messages/*.json
var messagesFS embed.FS

// MessagesFS returns the Paraglide message catalogs, one messages/<lang>.json
// per language.
func MessagesFS() fs.FS { return messagesFS }
