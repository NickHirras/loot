// Package links works out where a drop's event actually lives — the GitHub
// issue, the store page, the Sentry ticket — so the feed can link out to it.
//
// The link is *derived*, never stored. There is no column for it, no
// migration, and nothing in the write path that could have got it wrong two
// years ago: For runs on the way out, over the event as it was recorded. That
// is the whole design. It means every drop Loot has ever minted becomes
// clickable the moment a rule here improves, and it means a rule that turns
// out to be wrong is fixed by editing this file rather than by rewriting
// history.
//
// A link Loot cannot vouch for is no link at all. Half the payloads this sees
// were POSTed by a stranger with curl (see internal/sources/webhook and
// internal/sources/crash), so "the payload said so" is not on its own a
// reason to put something in an href: HTTPURL is the gate, and everything
// interpolated into a path is validated and then escaped anyway.
package links

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/nickhirras/loot/internal/core"
)

// Source ids, spelled out rather than imported from the source packages.
// internal/sources/webhook imports *this* package to validate its own `url`
// field, so importing it back would be a cycle; the rest are literals for the
// same reason they should be — a table of strings is not worth a dependency
// on an App Store Connect client.
const (
	sourceGitHub     = "github"
	sourceSnapcraft  = "snapcraft"
	sourceFlathub    = "flathub"
	sourceGooglePlay = "googleplay"
	sourcePlayVitals = "playvitals"
	sourceAppStore   = "appstore"
	sourceMicrosoft  = "microsoftstore"
	sourceLoot       = "loot"
)

// For returns where ev happened: an absolute http(s) URL, one of the
// dashboard's own hash routes for Loot's own news, or "" when nothing
// trustworthy can be built — which is a perfectly ordinary answer, and the
// feed simply renders those drops unlinked.
func For(ev core.Event) string {
	// Rule one, and it applies to every source: if the event recorded a real
	// URL, that is the answer. GitHub issues, PRs, releases and forks carry
	// one, as do the crash webhook, Sentry and Play vitals, and so does a
	// generic webhook that chose to send one. It wins even for a boss drop
	// that would otherwise land on the Quests tab, because the external
	// ticket *is* the source of the event and the tab is only where Loot
	// happens to draw it.
	if u := payloadURL(ev.Payload); u != "" {
		return u
	}

	switch ev.Source {
	case sourceGitHub:
		return githubLink(ev)
	case sourceSnapcraft:
		if snap := snapName(ev.App); snap != "" {
			return "https://snapcraft.io/" + url.PathEscape(snap)
		}
	case sourceFlathub:
		if id := flatpakID(ev.App); id != "" {
			return "https://flathub.org/apps/" + url.PathEscape(id)
		}
	case sourceGooglePlay, sourcePlayVitals:
		if pkg := playPackage(ev); pkg != "" {
			return "https://play.google.com/store/apps/details?id=" + url.QueryEscape(pkg)
		}
	case sourceAppStore:
		// Event.App here is the app's human title, not an identifier; the
		// numeric Apple Identifier is in the payload of every row, day
		// summary and subscription snapshot.
		if id := digits(payloadString(ev.Payload, "apple_id")); id != "" {
			return "https://apps.apple.com/app/id" + url.PathEscape(id)
		}
	case sourceMicrosoft:
		if id := storeID(payloadString(ev.Payload, "store_id")); id != "" {
			return "https://apps.microsoft.com/detail/" + url.PathEscape(id)
		}
	case sourceLoot:
		return lootLink(ev.Kind)
	}
	// Everything else — revenuecat, the generic webhook, crash reports and
	// Sentry issues with no URL on them, dev drops — had its one chance at
	// rule one. RevenueCat in particular could have a dashboard link, but
	// only with a project id Loot is never told.
	return ""
}

// githubLink points at the repository, or at its stargazers for the kinds
// that are about the star count rather than about a thing with a page.
func githubLink(ev core.Event) string {
	repo := githubRepo(ev.App)
	if repo == "" {
		return ""
	}
	switch ev.Kind {
	case "star", "stars_total", "stars_milestone":
		return repo + "/stargazers"
	}
	return repo
}

// lootLink maps Loot's own events onto the tab that renders them.
//
// The kind strings are owned by internal/quests, internal/bosses,
// internal/mysteries and internal/pipeline and are copied here rather than
// imported: those packages reach the store and the bus, and turning five
// strings into five hash routes is not worth pulling the service layer in
// behind it — nor worth standing between one of those packages and this one,
// should a boss ever want to name its own link.
func lootLink(kind string) string {
	switch kind {
	case "quest_complete", "mystery_solved", "boss_spawn", "boss_enrage", "boss_slain":
		// Bosses and mysteries are both drawn on the Quests page.
		return "#/quests"
	case core.KindAchievement:
		return "#/codex"
	case "settlement":
		return "#/hearth"
	}
	return ""
}

// HTTPURL returns raw as an absolute http(s) URL, or "" if it is not one. It
// is the only shape of link Loot will hand a browser, and it is exported so
// that a source accepting a URL from the outside world (the generic webhook)
// checks it with exactly the rule that will later render it.
//
// Everything else is refused on purpose: `javascript:` and `data:` are the
// attack, `mailto:` and `ftp:` are not what a feed card means by "open", and
// a relative or protocol-relative reference would resolve against whatever
// page happens to be showing the drop.
func HTTPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// url.Parse lowercases the scheme, so "JavaScript:" is caught here too.
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	// No host means "http:foo" or a bare path: nothing we can point at.
	if u.Host == "" {
		return ""
	}
	// Re-render rather than echo, so what is returned is exactly what was
	// parsed and approved.
	return u.String()
}

// payloadURL is rule one: the `url` string of a JSON-object payload, if it is
// a URL worth following. A payload that is not an object, or whose `url` is
// not a string, simply has no link in it.
func payloadURL(raw json.RawMessage) string {
	return HTTPURL(payloadString(raw, "url"))
}

// payloadString reads one top-level string field out of a JSON-object
// payload, or "" for anything else — a missing key, a number, an array
// payload, malformed JSON.
func payloadString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	s, _ := obj[key].(string)
	return strings.TrimSpace(s)
}

// playPackage is the Android package this event is about: the payload's own
// `package` where the source recorded one (installs and sales day summaries
// both do), otherwise Event.App.
//
// Either can be wrong. A Play sales CSV without a package column falls back
// to the product *title* for its app key (see SaleRow.AppKey in
// internal/sources/googleplay/sales.go), and that title ends up in both
// places — so the shape is checked rather than trusted, and a title that does
// not look like a package name gets no link instead of a wrong one.
func playPackage(ev core.Event) string {
	if pkg := packageName(payloadString(ev.Payload, "package")); pkg != "" {
		return pkg
	}
	return packageName(ev.App)
}

var (
	// GitHub owners and repositories: letters, digits, dot, hyphen and
	// underscore, which is a superset of what either actually allows.
	githubPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// A snap name: lowercase letters, digits and hyphens, starting and
	// ending with one of the former two.
	snapRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	// A Flatpak application id is reverse DNS: at least two dot-separated
	// segments, no spaces.
	flatpakRe = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+$`)
	// An Android package name: a dotted identifier of at least two segments,
	// opening with a letter. The point of the first-letter rule is that it
	// tells a package apart from a version number or a price.
	packageRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+$`)
	// A Microsoft Store ID: twelve alphanumerics, e.g. 9NBLGGH4R315.
	storeIDRe = regexp.MustCompile(`^[A-Za-z0-9]{12}$`)
	digitsRe  = regexp.MustCompile(`^[0-9]+$`)
)

// githubRepo turns an "owner/name" App into a repository URL, or "".
func githubRepo(app string) string {
	owner, name, found := strings.Cut(strings.TrimSpace(app), "/")
	if !found {
		return ""
	}
	if !githubPart.MatchString(owner) || !githubPart.MatchString(name) {
		return ""
	}
	// "." and ".." pass the character check and would climb out of the path
	// they were escaped into; url.PathEscape leaves dots alone.
	if isDots(owner) || isDots(name) {
		return ""
	}
	return "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func isDots(s string) bool { return s == "." || s == ".." }

func snapName(app string) string  { return matching(snapRe, app) }
func flatpakID(app string) string { return matching(flatpakRe, app) }
func packageName(s string) string { return matching(packageRe, s) }
func storeID(s string) string     { return matching(storeIDRe, s) }
func digits(s string) string      { return matching(digitsRe, s) }

// matching returns s trimmed when it matches re, and "" otherwise.
func matching(re *regexp.Regexp, s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !re.MatchString(s) {
		return ""
	}
	return s
}
