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
