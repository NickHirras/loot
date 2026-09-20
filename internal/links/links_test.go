package links_test

import (
	"encoding/json"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/links"
)

// ev is a terse event builder: source, kind, app and a payload literal.
func ev(source, kind, app, payload string) core.Event {
	e := core.Event{Source: source, Kind: kind, App: app}
	if payload != "" {
		e.Payload = json.RawMessage(payload)
	}
	return e
}

func TestForPayloadURL(t *testing.T) {
	// Rule one: a real http(s) `url` in the payload wins, whatever the
	// source, and anything that is not one falls through to the source rules
	// rather than being handed to a browser.
	tests := []struct {
		name  string
		event core.Event
		want  string
	}{
		{
			"github issue",
			ev("github", "issue_opened", "nickhirras/loot",
				`{"number":7,"title":"It crashes","url":"https://github.com/nickhirras/loot/issues/7"}`),
			"https://github.com/nickhirras/loot/issues/7",
		},
		{
			"github pull request",
			ev("github", "pr_merged", "nickhirras/loot",
				`{"url":"https://github.com/nickhirras/loot/pull/12"}`),
			"https://github.com/nickhirras/loot/pull/12",
		},
		{
			"github release",
			ev("github", "release", "nickhirras/loot",
				`{"tag":"v1.2.0","url":"https://github.com/nickhirras/loot/releases/tag/v1.2.0"}`),
			"https://github.com/nickhirras/loot/releases/tag/v1.2.0",
		},
		{
			"github fork",
			ev("github", "fork", "nickhirras/loot",
				`{"user":"octocat","url":"https://github.com/octocat/loot"}`),
			"https://github.com/octocat/loot",
		},
		{
			"sentry issue",
			ev("sentry", core.KindCrash, "sprocket",
				`{"issue_id":"4507","url":"https://sprocket.sentry.io/issues/4507/"}`),
			"https://sprocket.sentry.io/issues/4507/",
		},
		{
			"crash webhook",
			ev("crash", core.KindCrash, "Sprocket",
				`{"version":"2.1","url":"http://crashes.internal/issue/9"}`),
			"http://crashes.internal/issue/9",
		},
		{
			"play vitals console link",
			ev("playvitals", core.KindCrash, "com.example.sprocket",
				`{"kind":"crash","url":"https://play.google.com/console/developers/app/com.example.sprocket/vitals/crashes"}`),
			"https://play.google.com/console/developers/app/com.example.sprocket/vitals/crashes",
		},
		{
			"generic webhook",
			ev("webhook", "ci_green", "Sprocket", `{"url":"https://ci.example.com/runs/41"}`),
			"https://ci.example.com/runs/41",
		},
		{
			// A boss carries whichever URL the crash source gave it, and that
			// beats the Quests tab: the ticket is where the event really is.
			"boss with an issue url",
			ev("loot", "boss_spawn", "Sprocket", `{"url":"https://sprocket.sentry.io/issues/4507/"}`),
			"https://sprocket.sentry.io/issues/4507/",
		},
		{
			"query string and fragment survive",
			ev("webhook", "build", "", `{"url":"https://ci.example.com/job?id=4#step-2"}`),
			"https://ci.example.com/job?id=4#step-2",
		},
		{
			"uppercase scheme is normalized",
			ev("webhook", "build", "", `{"url":"HTTPS://Example.com/x"}`),
			"https://Example.com/x",
		},
		{
			"surrounding whitespace is trimmed",
			ev("webhook", "build", "", `{"url":"  https://example.com/x  "}`),
			"https://example.com/x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := links.For(tt.event); got != tt.want {
				t.Errorf("For() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForRejectsUntrustedPayloadURL(t *testing.T) {
	// Every payload here could have been POSTed by anyone who can reach
	// /hooks/webhook or /hooks/crash. None of them may reach an href, and
	// each must fall *through* to the source rule rather than short-circuit
	// it — hence the github source, which has a fallback to land on.
	const repo = "https://github.com/nickhirras/loot"

	tests := []struct {
		name string
		url  string
	}{
		{"javascript", `javascript:alert(1)`},
		{"javascript mixed case", `JaVaScRiPt:alert(1)`},
		{"javascript with padding", `  javascript:alert(1)  `},
		{"data", `data:text/html;base64,PHNjcmlwdD4=`},
		{"vbscript", `vbscript:msgbox(1)`},
		{"file", `file:///etc/passwd`},
		{"mailto", `mailto:someone@example.com`},
		{"ftp", `ftp://files.example.com/x`},
		{"relative path", `/issues/7`},
		{"bare host", `example.com/issues/7`},
		{"protocol relative", `//evil.example.com/x`},
		{"http with no host", `http:issues/7`},
		{"empty", ``},
		{"whitespace only", `   `},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"url": tt.url})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			got := links.For(core.Event{
				Source:  "github",
				Kind:    "issue_opened",
				App:     "nickhirras/loot",
				Payload: json.RawMessage(payload),
			})
			if got != repo {
				t.Errorf("For() = %q, want the repo fallback %q — the payload url must not be used", got, repo)
			}
			if links.HTTPURL(tt.url) != "" {
				t.Errorf("HTTPURL(%q) = %q, want \"\"", tt.url, links.HTTPURL(tt.url))
			}
		})
	}
}

func TestForMalformedPayload(t *testing.T) {
	// A payload that is not a JSON object with a string `url` is not an
	// error, it is simply a payload with no link in it.
	tests := []struct {
		name    string
		payload string
	}{
		{"no payload", ``},
		{"empty object", `{}`},
		{"array", `["https://example.com"]`},
		{"string", `"https://example.com"`},
		{"number", `42`},
		{"null", `null`},
		{"url is a number", `{"url":42}`},
		{"url is an object", `{"url":{"href":"https://example.com"}}`},
		{"url is null", `{"url":null}`},
		{"truncated json", `{"url":"https://example.com"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := links.For(ev("webhook", "thing", "Sprocket", tt.payload))
			if got != "" {
				t.Errorf("For() = %q, want \"\"", got)
			}
		})
	}
}

func TestForBySource(t *testing.T) {
	tests := []struct {
		name  string
		event core.Event
		want  string
	}{
		// --- github ------------------------------------------------------
		{"github star", ev("github", "star", "nickhirras/loot", ""),
			"https://github.com/nickhirras/loot/stargazers"},
		{"github stars_total", ev("github", "stars_total", "nickhirras/loot", `{"stars":500}`),
			"https://github.com/nickhirras/loot/stargazers"},
		{"github stars_milestone", ev("github", "stars_milestone", "nickhirras/loot", `{"stars":1000}`),
			"https://github.com/nickhirras/loot/stargazers"},
		{"github anything else", ev("github", "issue_closed", "nickhirras/loot", ""),
			"https://github.com/nickhirras/loot"},
		{"github dots and dashes", ev("github", "fork", "my-org/my.repo_2", ""),
			"https://github.com/my-org/my.repo_2"},
		{"github no slash", ev("github", "star", "loot", ""), ""},
		{"github two slashes", ev("github", "star", "a/b/c", ""), ""},
		{"github empty owner", ev("github", "star", "/loot", ""), ""},
		{"github empty name", ev("github", "star", "nickhirras/", ""), ""},
		{"github space", ev("github", "star", "nick hirras/loot", ""), ""},
		{"github traversal", ev("github", "star", "../../etc/passwd", ""), ""},
		{"github dot segment", ev("github", "star", "./loot", ""), ""},
		{"github parent segment", ev("github", "star", "nickhirras/..", ""), ""},
		{"github empty app", ev("github", "star", "", ""), ""},

		// --- snapcraft ---------------------------------------------------
		{"snapcraft", ev("snapcraft", "installs_day", "sprocket", `{"snap":"sprocket"}`),
			"https://snapcraft.io/sprocket"},
		{"snapcraft hyphens", ev("snapcraft", "install", "my-cool-snap-2", ""),
			"https://snapcraft.io/my-cool-snap-2"},
		{"snapcraft uppercase", ev("snapcraft", "install", "Sprocket", ""), ""},
		{"snapcraft dot", ev("snapcraft", "install", "spro.cket", ""), ""},
		{"snapcraft slash", ev("snapcraft", "install", "spro/cket", ""), ""},
		{"snapcraft leading hyphen", ev("snapcraft", "install", "-sprocket", ""), ""},
		{"snapcraft empty", ev("snapcraft", "install", "", ""), ""},

		// --- flathub -----------------------------------------------------
		{"flathub", ev("flathub", "installs_day", "org.gnome.Podcasts", ""),
			"https://flathub.org/apps/org.gnome.Podcasts"},
		{"flathub four segments", ev("flathub", "install", "io.github.me.Sprocket", ""),
			"https://flathub.org/apps/io.github.me.Sprocket"},
		{"flathub one segment", ev("flathub", "install", "Podcasts", ""), ""},
		{"flathub space", ev("flathub", "install", "org.gnome. Podcasts", ""), ""},
		{"flathub slash", ev("flathub", "install", "org/gnome.Podcasts", ""), ""},
		{"flathub empty", ev("flathub", "install", "", ""), ""},

		// --- googleplay and playvitals -----------------------------------
		{"play from app", ev("googleplay", "installs_day", "com.example.sprocket", ""),
			"https://play.google.com/store/apps/details?id=com.example.sprocket"},
		{"play prefers the payload package", ev("googleplay", "sales_day", "Sprocket Pro",
			`{"package":"com.example.sprocket","units":3}`),
			"https://play.google.com/store/apps/details?id=com.example.sprocket"},
		{"play falls back to app when the payload package is blank",
			ev("googleplay", "sale", "com.example.sprocket", `{"package":""}`),
			"https://play.google.com/store/apps/details?id=com.example.sprocket"},
		{"play product title app key", ev("googleplay", "sales_day", "Sprocket Pro", `{"package":""}`), ""},
		{"play title in both places", ev("googleplay", "sale", "Sprocket Pro", `{"package":"Sprocket Pro"}`), ""},
		{"play single segment", ev("googleplay", "install", "sprocket", ""), ""},
		{"play leading digit", ev("googleplay", "install", "1.99", ""), ""},
		{"playvitals", ev("playvitals", core.KindCrashDay, "com.example.sprocket", `{"kind":"crash"}`),
			"https://play.google.com/store/apps/details?id=com.example.sprocket"},
		{"playvitals human app", ev("playvitals", core.KindCrashDay, "Sprocket", ""), ""},

		// --- appstore ----------------------------------------------------
		{"appstore sales day", ev("appstore", "sales_day", "Sprocket",
			`{"apple_id":"1234567890","units":12}`),
			"https://apps.apple.com/app/id1234567890"},
		{"appstore subscription snapshot", ev("appstore", "subscription_snapshot", "Sprocket",
			`{"apple_id":"1234567890","active":40}`),
			"https://apps.apple.com/app/id1234567890"},
		{"appstore no apple id", ev("appstore", "sales_day", "Sprocket", `{"units":12}`), ""},
		{"appstore non-numeric apple id", ev("appstore", "sale", "Sprocket", `{"apple_id":"SPROCKET"}`), ""},
		{"appstore apple id with a dash", ev("appstore", "sale", "Sprocket", `{"apple_id":"123-456"}`), ""},
		{"appstore app is never the id", ev("appstore", "sale", "1234567890", ""), ""},

		// --- microsoftstore ----------------------------------------------
		{"microsoft store", ev("microsoftstore", "sales_day", "Sprocket", `{"store_id":"9NBLGGH4R315"}`),
			"https://apps.microsoft.com/detail/9NBLGGH4R315"},
		{"microsoft store as app name", ev("microsoftstore", "sale", "9NBLGGH4R315",
			`{"store_id":"9NBLGGH4R315"}`),
			"https://apps.microsoft.com/detail/9NBLGGH4R315"},
		{"microsoft too short", ev("microsoftstore", "sale", "Sprocket", `{"store_id":"9NBLGGH4"}`), ""},
		{"microsoft too long", ev("microsoftstore", "sale", "Sprocket", `{"store_id":"9NBLGGH4R315X"}`), ""},
		{"microsoft not alphanumeric", ev("microsoftstore", "sale", "Sprocket", `{"store_id":"9NBL-GGH4R31"}`), ""},
		{"microsoft missing", ev("microsoftstore", "sale", "Sprocket", `{}`), ""},

		// --- no link -----------------------------------------------------
		{"revenuecat", ev("revenuecat", "purchase", "com.example.sprocket",
			`{"event":{"type":"INITIAL_PURCHASE"}}`), ""},
		{"webhook with no url", ev("webhook", "ci_green", "Sprocket", `{"title":"Build passed"}`), ""},
		{"crash with no url", ev("crash", core.KindCrash, "Sprocket", `{"version":"2.1"}`), ""},
		{"sentry with no url", ev("sentry", core.KindCrash, "sprocket", `{"issue_id":"4507"}`), ""},
		{"dev", ev("dev", "legendary", "com.example.loot", `{"synthetic":true}`), ""},
		{"unknown source", ev("gumroad", "sale", "Sprocket", ""), ""},

		// --- loot's own news ---------------------------------------------
		{"quest complete", ev("loot", "quest_complete", "Sprocket", `{"quest_id":"q1"}`), "#/quests"},
		{"mystery solved", ev("loot", "mystery_solved", "Sprocket", `{"mystery_id":"m1"}`), "#/quests"},
		{"boss spawn", ev("loot", "boss_spawn", "Sprocket", `{"boss_id":"b1"}`), "#/quests"},
		{"boss enrage", ev("loot", "boss_enrage", "Sprocket", `{"boss_id":"b1"}`), "#/quests"},
		{"boss slain", ev("loot", "boss_slain", "Sprocket", `{"boss_id":"b1"}`), "#/quests"},
		{"achievement", ev("loot", core.KindAchievement, "", `{"key":"first_blood"}`), "#/codex"},
		{"settlement", ev("loot", "settlement", "Sprocket", `{"via":"appstore"}`), "#/hearth"},
		{"unknown loot kind", ev("loot", "level_up", "", ""), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := links.For(tt.event); got != tt.want {
				t.Errorf("For() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPURLAccepts(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://example.com", "https://example.com"},
		{"http://example.com", "http://example.com"},
		{"https://example.com:8443/a/b?c=d#e", "https://example.com:8443/a/b?c=d#e"},
		{"https://user@example.com/x", "https://user@example.com/x"},
		{"https://example.com/päfad", "https://example.com/p%C3%A4fad"},
		{"HTTP://example.com/", "http://example.com/"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := links.HTTPURL(tt.raw); got != tt.want {
				t.Errorf("HTTPURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// A link Loot derived must never need escaping by whoever renders it: either
// it is an absolute http(s) URL or it is one of the dashboard's own routes.
func TestForOnlyEverReturnsSafeShapes(t *testing.T) {
	events := []core.Event{
		ev("github", "star", "nickhirras/loot", ""),
		ev("snapcraft", "install", "sprocket", ""),
		ev("flathub", "install", "org.gnome.Podcasts", ""),
		ev("googleplay", "install", "com.example.sprocket", ""),
		ev("appstore", "sales_day", "Sprocket", `{"apple_id":"1234567890"}`),
		ev("microsoftstore", "sale", "Sprocket", `{"store_id":"9NBLGGH4R315"}`),
		ev("loot", "quest_complete", "Sprocket", ""),
		ev("webhook", "ci_green", "", `{"url":"https://ci.example.com/runs/41"}`),
	}

	for _, e := range events {
		got := links.For(e)
		if got == "" {
			t.Errorf("For(%s/%s) = \"\", want a link", e.Source, e.Kind)
			continue
		}
		okHTTP := links.HTTPURL(got) == got
		okHash := len(got) > 2 && got[:2] == "#/"
		if !okHTTP && !okHash {
			t.Errorf("For(%s/%s) = %q, which is neither an http(s) URL nor a #/ route", e.Source, e.Kind, got)
		}
	}
}
