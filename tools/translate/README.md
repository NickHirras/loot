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

Needs `ANTHROPIC_API_KEY`, except for `-dry-run` and `-check`, which never
reach the network. `make translate` and `make translate-plan` are the same
thing.

## Flags

| flag | what it does |
|---|---|
| `-dry-run` | Print the plan — counts per locale, and with `-v` the key names — then exit 0 without calling the API. |
| `-check` | Validate every existing target file against the English source and exit non-zero on any problem. Never calls the API; this is what CI runs. |
| `-force` | Retranslate every key, ignoring the lock. Overwrites hand edits. |
| `-languages de,fr` | Restrict to these locales. They must be in `settings.json`. |
| `-only-messages` | Only `web/messages/<locale>.json`. |
| `-only-rules` | Only `internal/rules/locales/default.<lang>.yaml`. |
| `-summary FILE` | Also write the Markdown summary here, for a pull request body. |
| `-root DIR` | The checkout to work on. Found by walking up from the working directory by default. |
| `-v` | List every key in the plan, and print token usage per batch. |

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

One request per batch of 40 keys, to `claude-opus-5`, with the system prompt
and the glossary in a cached prefix that every batch and every language shares.
The reply is constrained by a JSON schema (`output_config.format`). A refusal
or a malformed reply retries the batch once in smaller pieces; keys that still
have nothing are reported as failed.

Thinking is left at its default — adaptive, which is on by default on this
model.
