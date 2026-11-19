package github

import (
	"testing"
	"time"
)

func TestAssetFormat(t *testing.T) {
	cases := map[string]string{
		"MacroTrainer.flatpak":               "flatpak",
		"macrotrainer_amd64.deb":             "deb",
		"macrotrainer.x86_64.rpm":            "rpm",
		"MacroTrainer-x86_64.AppImage":       "appimage",
		"macrotrainer-linux-x64.tar.gz":      "archive",
		"loot_0.3.2_windows_amd64.zip":       "archive",
		"MacroTrainer-x86_64.AppImage.zsync": "",
		"SHA256SUMS":                         "",
		"SHA256SUMS.asc":                     "",
		"flatpak-demo.mp4":                   "",
	}
	for name, want := range cases {
		if got := assetFormat(name); got != want {
			t.Errorf("assetFormat(%q) = %q, want %q", name, got, want)
		}
	}
}

// GitHub reports a running total per file and nothing else, so the first
// reading is a baseline and everything after it is the growth between polls.
func TestAssetDownloadEvents(t *testing.T) {
	uploaded := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	rel := func(deb, sums int) []release {
		return []release{
			{ID: 1, TagName: "v1.0.0", Assets: []asset{
				{ID: 11, Name: "app_amd64.deb", DownloadCount: deb, CreatedAt: uploaded},
				{ID: 12, Name: "SHA256SUMS", DownloadCount: sums, CreatedAt: uploaded},
			}},
			{ID: 2, TagName: "v1.1.0-draft", Draft: true, Assets: []asset{
				{ID: 21, Name: "app_amd64.deb", DownloadCount: 99, CreatedAt: uploaded},
			}},
		}
	}

	rs := &repoState{}
	first := assetDownloadEvents("o/r", rel(40, 12), rs, now)
	if len(first) != 1 {
		t.Fatalf("first poll = %d events, want 1 (checksums and drafts are not downloads of the app)", len(first))
	}
	b := first[0]
	if b.Kind != "asset_download" || b.Quantity != 40 || !b.Silent || b.App != "o/r" {
		t.Errorf("baseline = %+v", b)
	}
	if !b.OccurredAt.Equal(uploaded) {
		t.Errorf("baseline dated %s, want the upload day %s", b.OccurredAt, uploaded)
	}
	if p := payloadOf(t, b); p["baseline"] != true || p["format"] != "deb" || p["tag"] != "v1.0.0" {
		t.Errorf("baseline payload = %v", p)
	}
	if !rs.AssetsSeeded || rs.AssetDownloads["11"] != 40 {
		t.Errorf("state = %+v", rs)
	}

	// Nothing moved: nothing to say.
	if again := assetDownloadEvents("o/r", rel(40, 30), rs, now); len(again) != 0 {
		t.Fatalf("unchanged counter emitted %d events", len(again))
	}

	later := now.Add(10 * time.Minute)
	next := assetDownloadEvents("o/r", rel(43, 30), rs, later)
	if len(next) != 1 || next[0].Quantity != 3 || !next[0].OccurredAt.Equal(later) {
		t.Fatalf("growth = %+v, want 3 downloads dated when they were seen", next)
	}
	if p := payloadOf(t, next[0]); p["baseline"] != false || p["total"] != float64(43) {
		t.Errorf("growth payload = %v", p)
	}
	if next[0].DedupeKey == b.DedupeKey {
		t.Error("two readings of one file share a dedupe key")
	}

	// A file that appears after the first reading is news from the moment it
	// is seen, not a baseline.
	added := append(rel(43, 30), release{ID: 3, TagName: "v1.1.0", Assets: []asset{
		{ID: 31, Name: "app.flatpak", DownloadCount: 5, CreatedAt: later},
	}})
	fresh := assetDownloadEvents("o/r", added, rs, later.Add(time.Hour))
	if len(fresh) != 1 || fresh[0].Quantity != 5 {
		t.Fatalf("new file = %+v", fresh)
	}
	if p := payloadOf(t, fresh[0]); p["baseline"] != false || p["format"] != "flatpak" {
		t.Errorf("new file payload = %v", p)
	}
}
