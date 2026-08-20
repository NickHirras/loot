package server

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Go's built-in table has no entry for .webmanifest, and the distroless image
// the release container is built on has no /etc/mime.types to fall back to, so
// the PWA manifest would go out sniffed as text/plain. Name it explicitly.
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// notBuiltPage is shown when the binary was built without a compiled frontend,
// which is the normal state of a fresh clone before `make web`.
const notBuiltPage = `<!doctype html>
<meta charset="utf-8">
<title>Loot</title>
<style>
  body { background:#0b0d12; color:#e6e8ef; font:16px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; padding:3rem; }
  code { background:#171a23; padding:.15rem .4rem; border-radius:4px; color:#8be9fd; }
  h1 { color:#ffd166; }
</style>
<h1>Loot</h1>
<p>The API is running, but no frontend was embedded in this binary.</p>
<p>Build it with <code>make web</code> (or <code>cd web &amp;&amp; npm install &amp;&amp; npm run build</code>) and rebuild,
   or run the Vite dev server with <code>make dev</code>.</p>
<p>The API is live at <code>/api/stats</code>, <code>/api/drops</code> and <code>/ws</code>.</p>
`

// spaHandler serves the embedded single-page app with history fallback: any
// path that is not a real file returns index.html so client-side routing works.
// apiPrefixes are the paths that belong to the API rather than to the app. A
// request under one of them that reached the catch-all is a request for
// something that does not exist, and the honest answer is 404 — not a page of
// HTML that a fetch() will try to parse as JSON and report as a syntax error
// somewhere in the client.
var apiPrefixes = []string{"/api/", "/hooks/"}

func (s *Server) spaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "no such endpoint: " + r.Method + " " + r.URL.Path,
				})
				return
			}
		}

		if s.Static == nil || !hasIndex(s.Static) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(notBuiltPage))
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		f, err := s.Static.Open(name)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				s.Logger.Debug("static open failed", "path", name, "error", err)
			}
			s.serveIndex(w, r)
			return
		}
		stat, statErr := f.Stat()
		f.Close()
		if statErr != nil || stat.IsDir() {
			s.serveIndex(w, r)
			return
		}

		// Vite emits content-hashed asset filenames, so /assets/* is immutable.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.FileServerFS(s.Static).ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	index, err := fs.ReadFile(s.Static, "index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}

func hasIndex(fsys fs.FS) bool {
	f, err := fsys.Open("index.html")
	if err != nil {
		return false
	}
	f.Close()
	return true
}
