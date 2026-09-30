# semiplane — Agent Guide

Self-hosted, system-agnostic TTRPG wiki and VTT. Go 1.27, `net/http`,
`log/slog`. This file is the operational contract for agents working in this
repo.

## Toolchain

The shell does not source Go by default. Either `make <target>` (which sources
it for you) or run `. /etc/profile.d/go.sh` first.

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.27.1 | `/usr/local/go`, sourced by `/etc/profile.d/go.sh` |
| gopls | v0.23.0 | `/root/go/bin/gopls`; Kilo discovers it as the Go LSP |
| golangci-lint | v2.14.0 | `/root/go/bin/golangci-lint`; **v2 config schema** |
| actionlint | v1.7.7 | lints `.github/workflows/` |
| yq | v4.47.2 | reads `.github/labels.yml` |
| Hugo | v0.167.0 | builds the docs site; **standard edition**, `CGO_ENABLED=0` |
| Node | 24.21.0 LTS | only for the Playwright MCP |
| Playwright MCP | 0.0.83 | `/usr/local/bin/playwright-mcp`, Chromium pre-installed |
| make | 4.4.1 | `make help` lists every target |

Every version above is pinned once, in the `Makefile`. CI installs from the
Makefile rather than repeating the pins, so the local gate and the CI gate
cannot drift apart. `make tools` installs all of them.

`CGO_ENABLED` needs `gcc` for `go test -race`. gcc is installed; if a fresh
container lacks it, `apt-get install -y --no-install-recommends gcc libc6-dev`.

## The gate

**No Go change is complete until `make check` passes.** It runs, in order:
format diff check, `go build`, `go vet`, `golangci-lint run`, `go test -race`.

```bash
make check          # the full gate
make lint-fix       # auto-fix what is fixable, then reformat
make lint-verify    # validate .golangci.yml against the v2 schema
make run            # dev server on :8080
make vuln           # govulncheck
make ci             # lint-verify + check
```

Run `make lint-verify` after **any** edit to `.golangci.yml`. In golangci-lint
v2, `linters` and `formatters` are separate top-level sections; a v1-style file
fails to load.

A change outside `internal/` has its own gates, and they are separate on
purpose — the Go gate must not go red because a docs toolchain is missing:

```bash
make site-check      # build the docs site; fails on any Hugo warning
make lint-workflows  # actionlint over .github/workflows/
make labels-check    # the repo's labels and .github/labels.yml agree
```

## Layout

```
cmd/server/       thin main: config, server wiring, signal handling
internal/config/  env parsing, no project deps
internal/domain/  pure types and rules, no I/O
internal/store/   persistence and migrations
internal/httpapi/ handlers, routing, middleware
internal/web/     templates and static assets
docs/             Hugo documentation site (its own project root)
scripts/          sync-labels.sh, check-site-links.sh, check-site-structure.sh
.github/          issue forms, PR template, label manifest, workflows
```

Dependencies point inward. `httpapi` → `domain`/`store`; `domain` imports
nothing from the project.


## Skills

Read the relevant one before writing code; they carry the detail this file
deliberately omits.

- **go-quality-gate** — the verify loop, and what each common lint failure means.
- **go-conventions** — error handling, context, HTTP, database, testing patterns.
- **browser-e2e** — driving the Playwright MCP, and the newline-delimited JSON
  transport it uses.

## MCP servers

Configured in `kilo.json`.

- `playwright` — 25 `browser_*` tools, Chromium, `--isolated`. Diagnostic
  use; anything that must persist belongs in a committed test.
- `context7` — current library documentation. Prefer it over recalling
  third-party API signatures from memory.

`browser_evaluate` and `browser_run_code_unsafe` require approval. There is no
Go-specific MCP: gopls already provides diagnostics, and Kilo consumes it
natively through its `lsp` tool.

## Conventions that the linter will not catch

- Migrations are forward-only, under `internal/store/migrations/`. Never edit a
  shipped migration.
- Prefer the standard library; check `go.mod` before adding a second library
  for the same job.
- Templates get `data-testid` on elements under test. Assert on that, never on
  CSS classes or DOM shape.
- Do not add a `gosec` or other linter `excludes` entry to silence a finding.
  Fix the code, or justify the suppression inline with `//nolint:lor // why`.

## GitHub plumbing

`.github/labels.yml` is the label source of truth, because GitHub has no
in-repo mechanism for declaring labels. Apply it with `make labels` **before**
opening a pull request that adds an issue form or a workflow referencing a
label: GitHub silently drops a form's `labels:` entries whose labels do not
exist, and nothing errors. `make labels-check` runs in CI and is the guard.

Issue taxonomy is four orthogonal axes — `type:` (exactly one), `area:` (at most
one), `status:` (at most one), and flags. There is deliberately no `priority:`
axis. A closing PR or a closing comment clears the `status:` label; without
that, the triage query stops meaning anything.

The docs site is deployed from `main` by `.github/workflows/pages.yml`. Its
workflow has **no `paths:` filter** on purpose: the site renders the design
records from `.kilo/plans/` at build time, so a filter covering only `docs/**`
would silently stop deploying when a record changes. Do not add one.

## Git

Branch and commit only when asked. Never commit `.env`, `*.db`, or
`.playwright-mcp/`. `git stash` is shared across worktrees in Agent Manager —
resolve conflicts in the worktree instead.
