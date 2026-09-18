package main

// Offline mode: the same run, with the API replaced by a directory of files.
//
// It exists for the first full translation. Every key of every language at
// once is a lot of tokens to buy, and the person doing it is usually sitting in
// front of Claude Code with a subscription rather than an API key. So `-export`
// writes out exactly the requests the API would have received — same system
// prompt, same user turn, same schema — and subagents answer them into reply
// files; `-import` then runs the ordinary pipeline against those files, so the
// translations are validated, written and locked by the same code that a real
// run uses. Afterwards the lock is populated and the workflow only ever
// translates what actually changed.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExportRequest is one batch as it lands on disk. Everything the API would
// have been sent is in here verbatim: answering it well needs nothing but the
// file.
type ExportRequest struct {
	Kind   Kind   `json:"kind"`
	Locale string `json:"locale"`
	// Categories are the target language's CLDR plural categories, repeated
	// out of the user turn so a reader can see them without parsing prose.
	Categories []string `json:"categories"`
	// System is the cached prefix the API would get: the stable prompt and the
	// glossary.
	System string `json:"system"`
	// User is the per-batch turn.
	User string `json:"user"`
	// Schema is the JSON Schema the reply must satisfy.
	Schema map[string]any `json:"schema"`
	// Keys is the batch's keys, in request order.
	Keys []string `json:"keys"`
}

// requestFileName names one exported batch. The index is per language and per
// catalog, in plan order, and only for the reader's benefit: nothing on the way
// back in looks at it, because a reply is matched to its key and the batches
// regroup whenever the plan does.
func requestFileName(kind Kind, locale string, n int) string {
	return fmt.Sprintf("%s-%s-%02d.request.json", kind, locale, n)
}

// replyFileName is the file a request is answered in.
func replyFileName(requestName string) string {
	return strings.TrimSuffix(requestName, ".request.json") + ".reply.json"
}

// Export writes the plan out as request files instead of calling the API. It
// touches nothing else: no target catalog, no lock.
func (r *Runner) Export(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	system := SystemPrompt(r.glossary)

	// files is the README's table: one row per language, in plan order.
	type group struct {
		kind   Kind
		locale string
		names  []string
		keys   int
	}
	var groups []group
	total := 0

	for _, kind := range r.kinds() {
		english := r.english(kind)
		for _, locale := range r.locales {
			target, err := r.loadTarget(kind, locale)
			if err != nil {
				return fmt.Errorf("%s/%s: %w", kind, locale, err)
			}
			plan := BuildPlan(kind, locale, r.relPath(kind, locale), english, target, r.lock, r.opts.Force)
			if len(plan.Translate) == 0 {
				continue
			}
			cats, err := PluralCategories(locale)
			if err != nil {
				return err
			}
			g := group{kind: kind, locale: locale, keys: len(plan.Translate)}
			for i, batch := range Batches(plan.Translate, batchSize) {
				req := r.request(kind, locale, cats, english, batch)
				out := ExportRequest{
					Kind:       kind,
					Locale:     locale,
					Categories: cats,
					System:     system,
					User:       userPrompt(req),
					Schema:     replySchema(),
					Keys:       append([]string(nil), batch...),
				}
				body, err := json.MarshalIndent(out, "", "  ")
				if err != nil {
					return err
				}
				name := requestFileName(kind, locale, i+1)
				if err := os.WriteFile(filepath.Join(dir, name), append(body, '\n'), 0o644); err != nil {
					return err
				}
				g.names = append(g.names, name)
				total++
			}
			groups = append(groups, g)
		}
	}

	var readme strings.Builder
	readme.WriteString(exportReadmeHead)
	if len(groups) == 0 {
		readme.WriteString("\nThere is nothing to translate: every key is already up to date.\n")
	} else {
		readme.WriteString("\n## The requests\n\n")
		readme.WriteString("| catalog | locale | keys | request files |\n|---|---|---:|---|\n")
		for _, g := range groups {
			names := make([]string, 0, len(g.names))
			for _, n := range g.names {
				names = append(names, "`"+n+"`")
			}
			fmt.Fprintf(&readme, "| %s | `%s` | %d | %s |\n",
				g.kind, g.locale, g.keys, strings.Join(names, ", "))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme.String()), 0o644); err != nil {
		return err
	}

	fmt.Printf("wrote %d request file(s) for %d language/catalog pair(s) to %s\n", total, len(groups), dir)
	fmt.Printf("answer each one alongside itself, then: go -C tools/translate run . -import %s\n", dir)
	return nil
}

// exportReadmeHead is the part of the README that does not depend on the plan.
// It is the whole brief for whoever — or whatever — answers these files.
const exportReadmeHead = `# Translation requests

Each ` + "`*.request.json`" + ` in this directory is one batch of keys, holding
exactly what the Anthropic API would have been sent for it:

| field | what it is |
|---|---|
| ` + "`system`" + ` | The system prompt, glossary included. Read it first: it is the brief. |
| ` + "`user`" + ` | The batch itself — the target language, its plural categories, and every key with its English. |
| ` + "`schema`" + ` | The JSON Schema your answer must satisfy. |
| ` + "`kind`" + `, ` + "`locale`" + `, ` + "`categories`" + `, ` + "`keys`" + ` | The same facts, already parsed, so you do not have to read them out of the prose. |

## What to write

For every ` + "`<name>.request.json`" + `, write ` + "`<name>.reply.json`" + ` next to it,
containing **only** a JSON object matching ` + "`schema`" + ` — no prose around it, no
Markdown fence:

` + "```json" + `
{
  "translations": [
    {"key": "feed_empty_title", "text": "Noch keine Beute.", "variants": []},
    {"key": "chest_row_drops", "text": "", "variants": [
      {"match": "countPlural=one", "text": "{count} Fund"},
      {"match": "countPlural=other", "text": "{count} Funde"}
    ]}
  ]
}
` + "```" + `

One entry per key you were given. A plain message fills ` + "`text`" + ` and leaves
` + "`variants`" + ` empty; a plural message leaves ` + "`text`" + ` empty and fills ` + "`variants`" + `
with exactly the arms the user turn asked for, ` + "`match`" + ` copied byte for byte.

## Then

` + "```bash" + `
go -C tools/translate run . -import <this directory>
` + "```" + `

Replies are matched to requests by key, never by file name, so a partial answer
is fine: import what you have, and the keys with no reply are reported and left
for the next round. Every translation is validated before it is written — a
dropped ` + "`{placeholder}`" + ` or a missing plural arm is rejected with the reason.
`

// FileTranslator answers batches out of a directory of reply files instead of
// the API.
//
// It works by key, not by file: on the first batch of a language it loads every
// reply file that language has, merges them into one map, and serves every
// later batch from it. That is what makes a partial import safe to repeat —
// re-exporting regroups the batches, and the files written against the old
// grouping still answer the right keys.
type FileTranslator struct {
	dir string
	// Items resolves every key of one catalog in one locale to the Item the
	// model would have been handed for it, which is what a reply is decoded
	// against. The Runner fills this in; it cannot be set at construction
	// because the Runner needs the translator first.
	Items func(kind Kind, locale string) (map[string]Item, error)

	// loaded caches one "<kind>/<locale>" worth of replies.
	loaded map[string]map[string]Value
	// notes are the problems with the files themselves, for the report.
	notes []string
}

// NewFileTranslator reads replies from dir.
func NewFileTranslator(dir string) *FileTranslator {
	return &FileTranslator{dir: dir, loaded: map[string]map[string]Value{}}
}

// Notes is what was wrong with the files: one line per unreadable reply and per
// key answered twice.
func (f *FileTranslator) Notes() []string { return f.notes }

func (f *FileTranslator) note(format string, args ...any) {
	f.notes = append(f.notes, fmt.Sprintf(format, args...))
}

// Translate answers one batch from the loaded replies. A key with no reply is
// simply absent, which is the same thing as a key the API did not answer: the
// run reports it and leaves the target alone.
func (f *FileTranslator) Translate(req BatchRequest) (map[string]Value, error) {
	vals, err := f.load(req.Kind, req.Locale)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Value, len(req.Items))
	for _, it := range req.Items {
		if v, ok := vals[it.Key]; ok {
			out[it.Key] = v
		}
	}
	// Deliberately no error on an empty answer: retrying a file in smaller
	// pieces would read the same file again and say the same thing.
	return out, nil
}

// MissingReason says why a key came back with nothing, for the report.
func (f *FileTranslator) MissingReason(Kind, string, string) string {
	return "no reply for key (nothing in the imported replies answered it)"
}

// load reads every reply file for one language and catalog, once.
func (f *FileTranslator) load(kind Kind, locale string) (map[string]Value, error) {
	id := string(kind) + "/" + locale
	if v, ok := f.loaded[id]; ok {
		return v, nil
	}
	if f.Items == nil {
		return nil, fmt.Errorf("file translator: no item source configured")
	}
	wanted, err := f.Items(kind, locale)
	if err != nil {
		return nil, err
	}

	paths, err := filepath.Glob(filepath.Join(f.dir, fmt.Sprintf("%s-%s-*.reply.json", kind, locale)))
	if err != nil {
		return nil, err
	}
	// Sorted, because "the last file wins" has to mean something stable.
	sort.Strings(paths)

	out := map[string]Value{}
	from := map[string]string{}
	for _, path := range paths {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			f.note("%s: %v", name, err)
			continue
		}
		vals, complaints, err := DecodeReplyFor(string(body), wanted)
		for _, c := range complaints {
			f.note("%s: %s", name, c)
		}
		if err != nil {
			f.note("%s: %v", name, err)
			continue
		}
		for _, key := range sortedKeys(vals) {
			if prev, ok := from[key]; ok {
				f.note("%s: %q was already answered by %s; the later file wins", name, key, prev)
			}
			from[key] = name
			out[key] = vals[key]
		}
	}
	if len(paths) == 0 {
		f.note("no %s-%s-*.reply.json in %s", kind, locale, f.dir)
	}
	f.loaded[id] = out
	return out, nil
}

// MissingReporter is a Translator that can say why a key came back with
// nothing. Offline mode implements it so that "you never wrote a reply for
// this" reads differently in the report from "the model did not answer".
type MissingReporter interface {
	MissingReason(kind Kind, locale, key string) string
}

// missingReason asks the translator why a key is missing, if it knows.
func missingReason(t Translator, kind Kind, locale, key string) string {
	m, ok := t.(MissingReporter)
	if !ok {
		return ""
	}
	return m.MissingReason(kind, locale, key)
}
