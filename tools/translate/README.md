# tools/translate

Generates Loot's translations from its English source.

English is the only catalog anybody writes by hand:

| source | target |
|---|---|
| `web/messages/en.json` | `web/messages/<locale>.json` |
| `internal/rules/default.yaml` | `internal/rules/locales/default.<lang>.yaml` |

The locale list is `web/project.inlang/settings.json` and nothing else. Both
halves use the same tags.

This is its own Go module, so the Anthropic SDK never becomes a dependency of
the `loot` binary.

## Running it

```bash
go -C tools/translate run . -dry-run          # what would be translated
go -C tools/translate run . -check            # validate what is there (no API)
go -C tools/translate run .                   # translate everything stale
go -C tools/translate run . -languages de,fr  # …in two languages only
```

Needs `ANTHROPIC_API_KEY`, except for `-dry-run`, `-check` and the two offline
flags, which never reach the network. `make translate` and `make translate-plan`
are the same thing.

## Flags

| flag | what it does |
|---|---|
| `-dry-run` | Print the plan — counts per locale, and with `-v` the key names — then exit 0 without calling the API. |
| `-check` | Validate every existing target file against the English source and exit non-zero on any problem. Never calls the API; this is what CI runs. |
| `-force` | Retranslate every key, ignoring the lock. Overwrites hand edits. |
| `-languages de,fr` | Restrict to these locales. They must be in `settings.json`. |
| `-only-messages` | Only `web/messages/<locale>.json`. |
| `-only-rules` | Only `internal/rules/locales/default.<lang>.yaml`. |
| `-export DIR` | Write the plan to `DIR` as request files and exit, instead of calling the API. See *Offline mode*. |
| `-import DIR` | Translate from the reply files in `DIR` instead of calling the API. See *Offline mode*. |
| `-summary FILE` | Also write the Markdown summary here, for a pull request body. |
| `-root DIR` | The checkout to work on. Found by walking up from the working directory by default. |
| `-v` | List every key in the plan, and print token usage per batch. |

## Offline mode

The first full run is the expensive one: every key of every language at once,
which is most of what this tool will ever translate. It is also the run you are
most likely to be doing while sitting in front of Claude Code with a
subscription rather than an API key. `-export` and `-import` let Claude Code
subagents do that run instead of the API, through the same pipeline.

```bash
go -C tools/translate run . -export /tmp/i18n      # write the requests
#   …a subagent answers each one…
go -C tools/translate run . -import /tmp/i18n      # validate, write, lock
```

`-export` builds exactly the plan `Run` would — `-languages`, `-force`,
`-only-messages` and `-only-rules` all apply — and writes one file per batch,
`<kind>-<locale>-<NN>.request.json`, plus a `README.md` explaining the job. Each
request holds the API call verbatim: the `system` prompt with the glossary in
it, the `user` turn, the reply `schema`, and the batch's `keys`. It writes
nothing else, touches no target file and never writes the lock.

The answer to `foo.request.json` is `foo.reply.json` beside it, containing only
a JSON object of the shape the API returns:

```json
{"translations": [
  {"key": "feed_empty_title", "text": "Noch keine Beute.", "variants": []},
  {"key": "chest_row_drops", "text": "", "variants": [
    {"match": "countPlural=one", "text": "{count} Fund"},
    {"match": "countPlural=other", "text": "{count} Funde"}
  ]}
]}
```

`-import` then runs the whole ordinary pipeline — plan, validate, write sorted
catalogs, update the lock, report — with those files where the API would be.
Nothing downstream knows the difference, so an offline translation is validated
by exactly the same rules as a real one.

Replies are matched to requests **by key**, never by file name or batch index,
so answering half the files and importing works, and so does re-exporting
afterwards even though the batches regroup. A key nothing answered is reported
as failed with *no reply for key* and left for the next round. A reply file that
will not parse is named in the report and its readable entries are still used; a
key answered by two files takes the later one, with a note.

The point of all this is the lock file `-import` leaves behind: from then on the
nightly workflow only translates what the English has actually changed, which is
a handful of keys and well within an API budget.

## What makes a run incremental

`i18n.lock.json` at the repository root records, per language and per key, a
hash of **the English value** the current translation was made from.

A key is skipped when that hash still matches *and* the target file has the
key. That is the whole mechanism, and it is what lets you fix a translation by
editing the locale file: nothing looks at the translation, only at the English
behind it, so your correction survives every later run until the English
changes. A key missing from the target is retranslated even if the lock says
otherwise, so deleting a file is a clean way to start it over.

A key English no longer has is deleted from both the target file and the lock.

## What is validated before anything is written

- valid JSON / YAML, and a rule template that `text/template` will parse;
- every `{placeholder}` from the English is present, and none is invented;
- a plural message keeps the English `declarations` and `selectors`, and its
  `match` arms are exactly the target language's CLDR categories — `other`
  always, `few`/`many` where the language has them, one arm only for Japanese,
  Korean and Chinese;
- the `{{…}}` expressions are the English ones, in any order, the same number
  of times. A bare `{{else}}` or `{{end}}` may be added or removed: German
  needs a branch to say *Verkauf*/*Verkäufe* where English appends an "s";
- non-empty, and no more than 4× the English in length;
- an overlay names only rules that exist in `default.yaml`.

A key that fails is reported and not written. The rest of the run still
succeeds.

## Pinning a word

`glossary.yaml` lists Loot's vocabulary with a gloss for each term, and a slot
per language. Most ship empty — the model picks, which is usually right. Fill
one in and every later request is told to use it, and `-check` warns when a
translation of a string containing the English term does not.

Pinning does not retranslate anything by itself. Change the English, run with
`-force`, or delete the key from `i18n.lock.json`.

## How it talks to the API

One request per batch of 40 keys, to `claude-sonnet-5` by default (set
`LOOT_TRANSLATE_MODEL=claude-opus-5` for a one-off run where quality is the
point), with the system prompt
and the glossary in a cached prefix that every batch and every language shares.
The reply is constrained by a JSON schema (`output_config.format`). A refusal
or a malformed reply retries the batch once in smaller pieces; keys that still
have nothing are reported as failed.

Thinking is left at its default — adaptive, which is on by default on this
model.
