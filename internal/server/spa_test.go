package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nickhirras/loot/internal/config"
)

// indexShell is web/index.html reduced to the one attribute that matters here.
const indexShell = `<!doctype html>
<html lang="en" data-loot-lang="auto"><body><div id="app"></div></body></html>
`

// fetchIndex serves the app shell from a Loot configured with the given
// language, both at "/" and through the history fallback, and returns the two
// bodies. They must agree: the language the dashboard reads cannot depend on
// whether the reader typed the root or deep-linked a tab.
func fetchIndex(t *testing.T, language string) (root, fallback string) {
	t.Helper()

	cfg := config.Default()
	cfg.Language = language
	h := newHarnessWith(t, cfg, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(indexShell)},
	})

	read := func(path string) string {
		t.Helper()
		resp, err := h.srv.Client().Get(h.srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d", path, resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(body)
	}

	return read("/"), read("/some/client/route")
}

func TestIndexKeepsAutoWhenNoLanguageIsConfigured(t *testing.T) {
	for _, language := range []string{"", "  ", "auto", "AUTO"} {
		root, fallback := fetchIndex(t, language)
		for _, body := range []string{root, fallback} {
			if !strings.Contains(body, `data-loot-lang="auto"`) {
				t.Errorf("language %q: shell = %q, want the auto placeholder", language, body)
			}
		}
	}
}

func TestIndexCarriesTheConfiguredLanguage(t *testing.T) {
	root, fallback := fetchIndex(t, "pt-BR")
	for _, body := range []string{root, fallback} {
		if !strings.Contains(body, `data-loot-lang="pt-BR"`) {
			t.Fatalf("shell = %q, want the configured language", body)
		}
		if strings.Contains(body, `data-loot-lang="auto"`) {
			t.Fatalf("the placeholder survived: %q", body)
		}
	}
	if root != fallback {
		t.Errorf("/ and the history fallback disagree:\n%q\n%q", root, fallback)
	}
}

// A language is configuration, not user input, but it lands in an HTML
// attribute — so it is escaped on the way in rather than trusted.
func TestIndexEscapesTheConfiguredLanguage(t *testing.T) {
	root, _ := fetchIndex(t, `de" onload="alert(1)`)
	if strings.Contains(root, `onload="alert(1)"`) {
		t.Fatalf("attribute injection survived: %q", root)
	}
	if !strings.Contains(root, `data-loot-lang="de&#34; onload=&#34;alert(1)"`) {
		t.Fatalf("shell = %q, want the value escaped", root)
	}
}
