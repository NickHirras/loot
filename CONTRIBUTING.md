# Contributing to Loot

Thanks for wanting to help. Loot is maintained by one person, so the process
is deliberately small: open an issue or a pull request, and
[@NickHirras](https://github.com/NickHirras) reviews and merges it. Nobody
else can merge, and there is no CLA.

For anything bigger than a fix, open an issue first so you do not build
something that cannot land.

## Building and testing

You need Go (the version in `go.mod`) and Node.js.

```bash
make build   # frontend, then the single binary with the SPA embedded
make dev     # Go server and Vite dev server together
make ci      # everything CI runs: check, test, build
```

`./bin/loot serve --demo` runs against synthetic data, so you never need
store credentials to work on Loot. `make help` lists every target.

A pull request needs `make ci` to pass; the same checks run in GitHub Actions.
Workflows on a pull request from a fork wait for the maintainer's approval
before they run, so a pending check is not a failure.

## Things worth knowing

- **Translations are generated.** English is the only hand-edited language:
  `web/messages/en.json` and `internal/rules/default.yaml`. Do not edit the
  other locale files or `i18n.lock.json`; a workflow retranslates whatever
  English changed after it merges. To pin how a term is translated, edit
  `tools/translate/glossary.yaml`.
- **Honest data, no shame mechanics.** Loot is ambient and encouraging by
  design. It does not nag, guilt, or invent numbers, and features that do will
  not be merged.
- **Match the code around you.** Comments here explain *why*; keep that up.
  Run `make fmt` before you push.
- **New sources** implement `core.Source` (see `internal/core/core.go`) and
  come with tests and a page under `docs/sources/`.

## Security

Please report vulnerabilities privately; see [SECURITY.md](SECURITY.md).
