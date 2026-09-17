package server

// Which language a request is answered in.
//
// Drop titles are the one part of the API that is prose rather than data: they
// are sentences Loot wrote at ingest, and phase 3 re-renders them per reader
// (see internal/rules/locales.go). Everything else the dashboard says it says
// itself, from its own message catalog, so this file is the whole of the
// server's share of internationalization.
//
// Two ways a request gets a language, and only two:
//
//   - a Loot configured with a fixed `language` answers every request in it,
//     because that setting means "this dashboard is in German", not "German is
//     my preference";
//   - otherwise the Accept-Language header is negotiated against what Loot can
//     actually render, which is English plus whatever overlays the rules
//     engine loaded.
//
// The negotiation mirrors web/src/lib/locale.ts exactly — the tag itself, then
// the bare language, then any variant of the same language — so that a browser
// which made the dashboard speak pt-BR gets pt-BR drops inside it rather than
// a page in one language with a feed in another.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/i18n"
	"github.com/nickhirras/loot/internal/store"
)

// requestLang is the language this request's drops should be rendered in. It
// is always a language Loot can actually produce, and "en" — which means "the
// text as stored" — whenever nothing better is available.
func (s *Server) requestLang(r *http.Request) string {
	known := s.knownLangs()

	if fixed := strings.TrimSpace(s.Cfg.Language); fixed != "" && !strings.EqualFold(fixed, "auto") {
		if match := matchTag(fixed, known); match != "" {
			return match
		}
		return i18n.BaseLang
	}
	if r == nil {
		return i18n.BaseLang
	}
	if match := negotiate(r.Header.Get("Accept-Language"), known); match != "" {
		return match
	}
	return i18n.BaseLang
}

// knownLangs is English plus every language the rules engine has an overlay
// for. English is first because it is what every drop is already stored in.
func (s *Server) knownLangs() []string {
	known := []string{i18n.BaseLang}
	if s.Rules != nil {
		known = append(known, s.Rules.Languages()...)
	}
	return known
}

// negotiate picks the best of known for an Accept-Language header, or "".
func negotiate(header string, known []string) string {
	for _, tag := range acceptable(header) {
		if match := matchTag(tag, known); match != "" {
			return match
		}
	}
	return ""
}

// acceptable parses an Accept-Language header into tags, most wanted first.
// Malformed q values are treated as 1 and `*` is dropped: a wildcard means
// "anything", and the answer to that is already English.
func acceptable(header string) []string {
	type weighted struct {
		tag string
		q   float64
		at  int
	}
	var out []weighted

	for i, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.TrimSpace(fields[0])
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if !strings.HasPrefix(f, "q=") {
				continue
			}
			if v, err := strconv.ParseFloat(strings.TrimPrefix(f, "q="), 64); err == nil {
				q = v
			}
		}
		if q <= 0 {
			// q=0 is an explicit refusal of that language.
			continue
		}
		out = append(out, weighted{tag: tag, q: q, at: i})
	}

	// Stable by descending q, then by the order the header listed them.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].q != out[j].q {
			return out[i].q > out[j].q
		}
		return out[i].at < out[j].at
	})

	tags := make([]string, 0, len(out))
	for _, w := range out {
		tags = append(tags, w.tag)
	}
	return tags
}

// matchTag is web/src/lib/locale.ts's matchTag: the tag itself ("pt-BR"), then
// the bare language if we have it ("en-GB" → "en"), then any language we have
// in the same language ("pt-PT" → "pt-BR"). The middle step is what keeps a
// regional English on plain English rather than on the first en-* variant.
func matchTag(tag string, known []string) string {
	wanted := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, "_", "-")))
	if wanted == "" {
		return ""
	}
	for _, k := range known {
		if strings.EqualFold(k, wanted) {
			return k
		}
	}
	primary, _, _ := strings.Cut(wanted, "-")
	for _, k := range known {
		if strings.EqualFold(k, primary) {
			return k
		}
	}
	for _, k := range known {
		if kp, _, _ := strings.Cut(strings.ToLower(k), "-"); kp == primary {
			return k
		}
	}
	return ""
}

// localizeDrop rewrites one drop's title and subtitle in lang, in place. It is
// a no-op for English, for a drop with no rule recorded, and for a rule the
// language has nothing to say about — see rules.Engine.Localize, which decides
// all of that.
func (s *Server) localizeDrop(r *http.Request, lang string, d *core.Drop, ev core.Event) {
	if s.Rules == nil || d == nil {
		return
	}
	var ctx = r.Context()
	if title, subtitle, ok := s.Rules.Localize(ctx, *d, ev, nil, lang); ok {
		d.Title, d.Subtitle = title, subtitle
	}
}

// localizeDrops rewrites a page of feed drops in place. The slice always comes
// straight from a query — nothing memoizes /api/drops or a chest — so there is
// nothing shared to copy first.
func (s *Server) localizeDrops(r *http.Request, lang string, drops []store.DropView) {
	if s.Rules == nil || lang == "" || lang == i18n.BaseLang {
		return
	}
	for i := range drops {
		s.localizeDrop(r, lang, &drops[i].Drop, drops[i].Event())
	}
}

// localizeHearth returns h with its arrivals ticker rendered in lang.
//
// The Hearth *is* memoized, per scope, so the ticker is rebuilt into a new
// slice rather than rewritten: two readers in two languages share one cached
// aggregate, and the first must not translate it out from under the second.
func (s *Server) localizeHearth(r *http.Request, lang string, h store.Hearth) store.Hearth {
	if s.Rules == nil || lang == "" || lang == i18n.BaseLang || len(h.Recent) == 0 {
		return h
	}
	recent := make([]store.HearthDrop, len(h.Recent))
	copy(recent, h.Recent)
	for i := range recent {
		d := recent[i].Drop()
		if title, subtitle, ok := s.Rules.Localize(r.Context(), d, recent[i].Event, nil, lang); ok {
			recent[i].Title, recent[i].Subtitle = title, subtitle
		}
	}
	h.Recent = recent
	return h
}
