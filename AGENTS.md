# semiplane — Agent Guide

Self-hosted, system-agnostic TTRPG wiki and VTT. Go 1.27, `net/http`, `log/slog`.
This file is the operational contract for agents working in this repo.

**Read it in this order.** The single-instance rule and the branch topology come first because
violating either is expensive in a way a failing build is not.

## The single-instance rule

**Authoritative game state lives in memory, and the WebSocket hub is process-local. Two
instances behind a load balancer will silently diverge** — not fail loudly, diverge, with each
client showing a coherent and wrong game.

SQLite's single-writer limit is **not** the constraint. In-memory state ownership is. Nothing
scales semiplane until a shared broker exists, and that is a different architecture, not a
configuration change. Do not add clustering, leader election, sticky-session requirements, or a
"second instance" flag. Full reasoning:
[0004](docs/content/en/decisions/0004-single-process-constraint.md).

## Where the contracts live

| Document | What it decides |
|---|---|
| [`docs/content/en/contributing/spec.md`](docs/content/en/contributing/spec.md) | **Requirements**, numbered `S-n.n`. Cite the ID; do not restate the rule. |
| [`docs/content/en/decisions/`](docs/content/en/decisions/) | **Decisions and their costs.** Check before choosing a library or a structure. |
| `.opencode/plans/1790774477695-semiplane-architecture-overview.md` | Domain semantics, data model, protocol. A dated record. |
| `.opencode/plans/1790778908232-responsive-ui-ux-design-spec.md` | Interface contract. A dated record. |

The design records are **immutable**. They contain open questions that have since been answered
and sections describing behaviour the code does not have yet. When one is wrong, the correction
is an ADR plus an entry in the design index's "Known staleness" — never an edit to the record.

## Vocabulary

The strings **"world" and "session" appear nowhere in the interface** — not in `aria-label`,
`title`, empty states, or error copy. Neither entity exists. A leftover is a bug, and a grep for
them is a test.

| Domain term | UI label |
|---|---|
| campaign | Campaign |
| page | Page |
| game object | its registered kind label — data, not a string |
| placement | the kind's label; a `token` placement is a **Token** |
| the live tabletop | **Table** (not an entity — a campaign has at most one, so there is no list) |
| campaign state | never surfaced |
| auth session | never surfaced |

Roles are `gm` and `player`. **Content editing is GM-only.** There is no `editor` role.

## Toolchain

The shell does not source Go by default. Either `make <target>` (which sources
it for you) or run `. /etc/profile.d/go.sh` first.

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.27.1 | `/usr/local/go`, sourced by `/etc/profile.d/go.sh` |
| gopls | v0.23.0 | `/root/go/bin/gopls`; OpenCode discovers it as the Go LSP |
| golangci-lint | v2.14.0 | `/root/go/bin/golangci-lint`; **v2 config schema** |
| actionlint | v1.7.7 | lints `.github/workflows/` |
| yq | v4.47.2 | reads `.github/labels.yml` |
| Hugo | v0.167.0 | builds the docs site; **standard edition**, `CGO_ENABLED=0` |
| templ | v0.3.1020 | compiles `.templ`; installed by `make templ-bin` |
| Tailwind | v4.3.3 | standalone binary, SHA256-pinned; installed by `make tailwind` |
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
format diff check, `make vendor-check`, `make css`, `make templ`, `go build`,
`go vet`, `golangci-lint run`, `make a11y`, `go test -race`.

`css` and `templ` precede `build` because `internal/web` embeds both outputs.
The binary is the first place a missing stylesheet shows up, so `build` also
checks for the file and says `run: make css` rather than failing on the embed
pattern. `make tailwind` downloads ~110MB once per checkout into `.toolbin/`
and verifies it against a committed digest of its release manifest;
[0019](docs/content/en/decisions/0019-tailwind-standalone-pinned.md).

`vendor-check` precedes `css` for the same reason one step earlier: `css` stages
`static/vendor/` into `static/dist/`, `internal/web` embeds `all:static/dist`,
and `build` therefore embeds **whatever vendored bytes are on disk** into the
shipped binary. It reaches no network — CI has no npm account, and a gate that
fetched there would make `make check` depend on a third party being up. It is a
`go test -run` and carries the same guard `a11y` does, because `go test -run`
exits 0 on a pattern matching nothing. `make vendor` re-fetches and `make
vendor-dry-run` re-fetches without writing; both need the network and neither is
in `check`. The pin and its three targets:
[0052](docs/content/en/decisions/0052-third-party-browser-assets-are-committed-and-digest-pinned.md).

`make a11y` runs UI §10.1 (contrast), §10.2 (structural a11y, every route) and
§10.6 (target size), and it is in `check` because those three are gate-blocking
and two of them read a **built artefact** — `static/dist/app.css` and the served
documents. A gate that can be skipped by not running a command is a gate nobody
runs.

**Naming a route in `A11Y_ROUTE_PKGS` is a claim that it has §10.2 audits, and the
target enforces it.** `go test -run` exits **0** when the pattern matches nothing,
so a listed package can hold fifty tests, contribute none of them, and the gate
prints `ok` — which is indistinguishable from a pass. It happened: the wiki route
held 23 tests and the assets route 49, and not one was a §10.2 audit, while
`make a11y` was green because `A11Y_PKGS` named only three packages. The guard
fails when a listed package contributes no test matching `A11Y_TESTS`, naming the
package. **It caught two more silent passes the moment it landed**, which is the
argument for it.

**Adding a route to this gate is two steps, and the second is the one that is
skipped.** `A11Y_ROUTE_PKGS` is a `$(wildcard …)` over **named** paths, and
`$(wildcard a b c)` is `a b c` with the missing ones dropped — it is not a directory
scan. The wildcard buys exactly one thing: a package named here *before* it lands
cannot fail the target. It does **not** mean that creating a route's directory adds
it, and this file claimed the opposite until phase 8 proved it false.

`internal/httpapi/plugins` is the proof. #64 landed the directory carrying six
§10.2 audits, and the gate never looked at them, because nobody edited the
Makefile — and **nothing said so**, because `go test -run` exits 0 on a pattern
matching nothing, which is the same silent pass the guard above exists for. So:
**creating the directory is not the claim; naming it is.** Three rules follow, and
each cost a PR:

- **A test's name is what makes the gate find it.** `A11Y_TESTS` is a list of
  substrings; the search route had 33 tests including a landmark audit, a
  `.target` audit and a vocabulary audit, and matched **none** of them.
- **Renaming a test to match the pattern is not the fix** — it makes the guard
  quiet without making the gate stronger. Write the entry point, and add
  `TestEveryRouteAuditRejectsTheViolationItClaimsTo`, a meta-test proving each audit
  rejects a fixture that violates its rule.
- **An audit nobody can fail is not an audit**, and neither is one no fixture
  reaches. A `case "search"` branch asserting two landmark names was unreachable
  because no audited document carried a second `search` landmark; adding the
  fixture made it reachable and it immediately failed three unrelated rules, which
  is the audit working.

```bash
make check          # the full gate
make a11y           # just the UI §10.1/§10.2/§10.6 gate
make vendor-check   # re-hash the committed vendor bytes (no network)
make vendor-dry-run # re-fetch every npm package and verify it, writing nothing
make vendor         # re-fetch every npm package and verify it into the vendor tree
make lint-fix       # auto-fix what is fixable, then reformat
make lint-verify    # validate .golangci.yml against the v2 schema
make run            # dev server on :8080
make vuln           # govulncheck
make ci             # lint-verify + check
```

**A browser asset that is not staged into `static/dist/` is not served, and nothing
says so.** `internal/web` embeds `all:static/dist` and `/assets/` serves exactly that
tree, so a module or a vendored file living only under `static/js/` or
`static/vendor/` is a **404** — a silently absent behaviour, because the page
renders, the tabletop loads, and the feature that needed the module is simply not
there. `make stage-assets` copies both trees in with their relative layout preserved
(`scene.js` imports `../../vendor/pixi.min.mjs`, so a flattened copy is a module
graph that cannot resolve) and takes `*.js` only: those trees also hold the
`*_test.go` files that audit the modules beside them, and a copy that took everything
would embed a Go test file into the binary and serve it from a route with no gate on
it. Adding a browser asset means adding it to that staging in the same commit.

**The stylesheet is assembled in `internal/web/static/css/app.css` and nowhere
else.** `@import "tailwindcss"` is followed by `tokens.css`, `shell.css` and
`tv.css`, in that order, and the order is load-bearing: the `@theme` blocks need
Tailwind's layers, `shell.css` reads tokens `tokens.css` declares, and every
`tv.css` rule overrides a `shell.css` rule at equal specificity.

Omitting an import is **not** a compile error and not a test failure — it is a
product that renders unstyled while every other gate passes. `grep -c
'data-theme' internal/web/static/dist/app.css` must be non-zero, and
`TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` is what holds it, because
"the shell renders" and "the shell is unstyled" are the same observation from
every angle a test can take unless the test asks whether the bytes exist.

**The same failure, from the `@source` globs rather than the `@import`s.** Every root
holding rendered markup must be named there, and `internal/web/plugins` is one of them:
the reference plugins render their centre slot from Go string constants, so their classes
are in Go source under that tree and nowhere else. A glob missing it is not a build
failure, not an accessibility failure, and not anything a §10.2 audit can see — the
markup is correct and the browser finds no rule for the class.
`TestTheBuiltStylesheetScansThePluginSources` walks the plugin sources, collects the
classes their `class="…"` attributes declare, and requires a rule for each in the
**built** file. Three things make that assertion real rather than decorative, and each
was a failure before it was fixed:

- **The walk reads Go string literals through `go/parser`, not raw text.** A
  `class="untargeted"` inside a comment explaining that a rename would be caught is
  prose, and a walk that read comments reported a class the stylesheet was missing — a
  loud and *wrong* failure, which is worse than none, because the next real one gets
  ignored.
- **It needs a class nothing outside the plugin tree names** (`ScanSentinelClass`, held
  unique by its own package's test). Every class the shipped plugins use is a
  `shell.css` component class, so without a sentinel the walk passes with the glob
  deleted — the silent pass this exists to prevent.
- **`hasRule` matches a selector, not a substring.** `strings.Contains(css, ".notice")`
  answers `true` for a stylesheet carrying only `.notice--warning`, and the built file
  carries exactly three `.notice--*` rules — so a substring check reported the roller's
  `.notice` present on the strength of a modifier it never uses. Deleting the bare rule
  from the build is what found it.

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

## Build phases

Thirteen phases, each on its own branch. The plan is
`.opencode/plans/1790796797509-phased-delivery-plan.md`.

| # | Phase | Branch | Lands |
|---|---|---|---|
| 0 | Governance | `phase/00-governance` | ADRs, `spec.md`, this file |
| 1 | Foundations | `phase/01-foundations` | Middleware chain, config, `admin create`, migration skeleton, the templ + Tailwind toolchain |
| 2 | Identity and tenancy | `phase/02-identity` | Users, sessions, campaigns, membership, `os.Root` |
| 3 | Content read path | `phase/03-content-read` | Path confinement, front matter, render, sanitise, cache, `ETag` |
| 4 | Watcher and index | `phase/04-watcher` | Directory watches, debounce, FTS, degraded mode |
| 5 | Shell | `phase/05-shell` | templ shell, tokens, primitives, structural a11y gate |
| 6 | Wiki surface | `phase/06-wiki-surface` | Wiki and edit routes, `If-Match`, conflict view, search, assets |
| 7 | Realtime plane | `phase/07-realtime` | In-memory state, hub, protocol codec, debounced persistence |
| 8 | Plugins and systems | `phase/08-plugins` | `System` contract, registry, 5e engine and packs, house rules |
| 9 | Client | `phase/09-client` | PixiJS canvas, token list, Datastar live chrome, theme layer |
| 10 | Secret callouts | `phase/10-secrets` | `[!secret]`, two-variant render, reveal endpoint, ledger, reconciliation |
| 11 | Demo vault | `phase/11-demo-vault` | Three seeded campaigns as a release artifact |
| 12 | Hardening | `phase/12-hardening` | The security tests, race coverage, failure modes |

**Each phase is independently runnable.** The server starts and answers `/healthz` from phase 1
onward, and `main` stays green between phases.

### Branch topology

```
main
└── phase/NN-slug                    one per phase, one PR to main
    └── NN-slug/<work-item>          one per sub-agent, one PR to the phase branch
```

- A phase branch is cut from `main` when the previous phase's PR has merged. Never from a stale
  `main`.
- A work-item branch is cut from the phase branch, never from `main`.
- A phase branch merges to `main` only when its Definition of Done is met in full.
- Sub-agents open a PR against the **phase branch**. Never push to `main`, never open a PR
  against `main`.
- **A work-item branch drops the `phase/` prefix.** `phase/02-identity/domain-types` cannot
  exist: git stores a branch as a file under `refs/heads/`, so `refs/heads/phase/02-identity`
  is a file and cannot simultaneously be the directory holding
  `refs/heads/phase/02-identity/domain-types`. It is a directory/file conflict in the ref
  namespace and `git branch` refuses it outright. `02-identity/domain-types` has no such
  ancestor ref and works. Never "tidy" this back.
  [0023](docs/content/en/decisions/0023-work-item-branch-names-drop-the-phase-prefix.md).

### Definition of Done — a phase

1. `make ci` green locally.
2. `make site-check` green if `docs/` changed.
3. `make lint-workflows` green if `.github/workflows/` changed.
4. `make labels-check` green if labels or an issue form changed.
5. All three CI checks — `gate`, `labels`, `docs` — green on the phase PR.
6. Every work item merged, **or explicitly dropped with a reason in the phase PR description**.
7. `spec.md` updated if a requirement changed.
8. An ADR added for every significant decision (see `AGENTS.md`'s § below).
9. `AGENTS.md` updated if the layout, gate, or a convention changed.
10. The server still starts and answers `/healthz`.
11. No `.gitkeep` remains in a package that now has real source files.

**Definition of Done — release:** the secret-redaction test is green and fails loudly if a
non-GM response ever contains secret text. `make demo-check` is also green.

### Rules that make parallel work merge

These exist because without them, N sub-agents produce N merge conflicts instead of N merged
PRs.

1. **Path ownership.** Every work item declares the paths it owns and touches nothing else. A
   drive-by edit is a review comment, not a conflict someone resolves later.
2. **One worktree per parallel work item.** Two agents in one checkout cross their branch pointers:
   each `git checkout -b` moves the other's HEAD, and a commit lands on the wrong branch. Phase 4
   lost a commit this way — a commit's files were clean only by luck, because the agent happened to
   `git add` explicit paths instead of `-A`. Give every concurrent work item its own
   `git worktree add`, outside the repository directory so it can never appear in a diff. A
   worktree needs its own `.toolbin` (symlink it, or `make css` re-downloads ~110MB), and a
   subagent given a worktree must be told **which directory** it owns.
3. **One integrator per phase.** Exactly one work item owns `cmd/server/`,
   `internal/httpapi/router.go`, `Makefile`, `.github/workflows/`, `go.mod`, `go.sum`,
   `.golangci.yml`, and `docs/hugo.toml`. Every other agent **asks the integrator** for a change
   to one of those files rather than making it.
4. **Dependencies are pre-added.** Every pinned dependency for a phase is committed by the
   integrator in the phase's first commit, so no work item edits `go.mod` mid-phase. `go.sum`
   is regenerated by any dependency addition and is the most contended file in a Go repo.
5. **ADR numbers are pre-assigned.** A number is never reused, never renumbered.
6. **Generated files are never in a diff.** `*_templ.go`, the built stylesheet,
   `docs/public/`, `docs/resources/` and `docs/assets/plans/` are produced by the gate and are
   gitignored. If one appears in a PR, that PR is wrong. `.gitignore` spells `.toolbin`
   **without** a trailing slash for the same reason: with one, the pattern matches directories only,
   so a worktree's `.toolbin` *symlink* is not ignored, lands in a commit, and every CI job then
   fails at tool install with `mkdir .toolbin/: file exists`. Phase 4 committed one.

## Layout

**Target layout.** Only `cmd/`, `internal/config`, `internal/domain`, `internal/store` and
`internal/httpapi` exist today; the rest arrives with the phase that fills it. A path listed
here that does not yet exist is not a mistake — it is the destination of a phase in the table
above.

```
cmd/server/       thin main: config, wiring, signals. The composition root.
internal/config/  env parsing, no project deps
internal/domain/  pure types and rules, no I/O
internal/store/   persistence and migrations (forward-only)
internal/content/ os.Root confinement, front matter, render, sanitise, links, cache
  ├─ root.go        the confinement boundary; `os.Root` is the authority
  ├─ change.go      the settled-change contract: `Op`, `Change`, `ChangeSink`
  ├─ watch.go       one fsnotify watcher, routing by path prefix (S-4.2)
  ├─ debounce.go    per-path debounce + size-stable confirmation (S-4.3)
  ├─ index.go       the maintained `pages` index; convergence against the tree
  ├─ supervisor.go  S-4.5 degraded mode: slow-timer re-verify, rescan fallback
  ├─ ext/           the `[[wikilink]]`, `![[embed]]`, `{{statblock}}`, `{{dice}}` extensions
  └─ redact.go      the S-5.7 seam. P10 replaces NoSecrets()
internal/realtime/hub, campaign state, protocol codec
internal/plugin/  registry; explicit registration, NO init()
internal/httpapi/ handlers, routing, middleware
  ├─ middleware/ RequestID, Recoverer, RealIP, Log, Timeout
  ├─ auth/       credential primitives only — no database, no config
  ├─ identity/   session cookie → domain.Requestor, on the request context
  ├─ campaigns/  the S-8 access gates, and campaign registration
  ├─ accounts/   the sign-in and campaign-list routes
  ├─ plugins/    the two reference UI plugins, mounted behind RequirePlay/RequireRead
  └─ wiki/       the read path, and the S-5.7 redaction ordering
internal/campaignroots/ opens one os.Root per campaign at startup
internal/domain/rules/    the plugin contract, and the conformance suite
  ├─ conformance/ the audits every `rules.System` is held against
  ├─ determinism/ the scope a house-rule module may claim, and the lint rule
  └─ houserules/  keyed, first-match-wins settings; data-level only
internal/domain/systems/  gameplay systems; one id, packs as data
  └─ dnd5e/       the 5e engine, its base pack, and the two editions as overlays
internal/web/     templ components and static assets
  ├─ components/  the shell document, the auth pages, and their view models
  │  ├─ chrome/   the four landmarks: banner, nav, rail, contentinfo
  │  └─ ui/       §4.7's eleven states and the primitive library
  ├─ plugins/     the UI tier: `registry.go`, and each plugin's own component
  └─ static/css/  app.css imports tokens.css, shell.css and tv.css in that order
docs/             Hugo documentation site (its own project root)
demo-vault/       the demo campaigns (phase 11)
scripts/          sync-labels.sh, check-site-links.sh, check-site-structure.sh
tools/            install-tailwind.sh, and vendor.json + vendor/ (the vendor pin)
```

**`tools/vendor/` is a Go program inside the module, and that is deliberate.** It is
`make vendor`'s whole body — re-fetch, verify the npm integrity, extract the named
members, and refuse to write anything the pin does not describe — so `go vet`,
`golangci-lint` and `go test -race ./...` all reach it and its `httptest` TLS
registry means the fetcher is covered by `make check` **offline**. A shell
alternative would need its own harness. It has **no `check` subcommand**: the digest
check is `TestTheVendoredBytesMatchThePin` in `internal/web/static/js/map`, it reads
the whole manifest rather than one package, and a second implementation would be a
second answer to the same question.

**`cmd/server/systems.go` is where every registration in this process happens**, and it
is a separate file rather than part of `wiring.go` because the rule it embodies is one
of this repository's oldest: there is no `init()`, no package-level registry, and no
second door. The order there is the §10.5 chain — **edition → gameplay registry → UI
tier → house-rule registry** — and a reader asking "what does this binary resolve
under?" reads that one file. Two consequences worth stating:

- **`defaultEdition` is the line that chooses the edition**, and it is a constant
  rather than configuration or a build tag because ADR 0011's argument is that a
  choice a reviewer cannot see is a choice nobody can review. `dnd5e.SystemID` is one
  id for both editions (they differ by pack version, which is its own fingerprint
  component), so a build registers exactly one — and `plugin.Register` refuses a
  duplicate id, which is the correct refusal rather than an obstacle.
- **`kindRegistry` adapts the gameplay registry to `domain.PageKindRegistry`,** and it
  is the composition root's because that is where both types meet. `kind` is
  registry-backed (S-3.3, §10.7), so a page's `kind: ancestry` is a game object in a
  build shipping 5e and prose in one that is not — and a hand-written list beside the
  registry would be two vocabularies that agree until an overlay disagreed with one.

`scripts/check-demo.sh` arrives with phase 11.

Dependencies point inward. `httpapi` → `domain`/`store`; `domain` imports nothing from the
project. **`domain` must not import `content`, `store`, or anything with I/O.**

A `.gitkeep` marks a package that does not exist yet. **Delete it when the package gains its
first real file.**

## When to write a decision record

Write one when a work item makes a choice a later reader could reasonably have made differently,
and where being wrong is expensive to undo:

- A new third-party dependency, or dropping, upgrading, or replacing one.
- A deviation from the architecture overview's decision table.
- A change to the data model, URL scheme, WebSocket protocol, intent vocabulary, the `System`
  interface, or plugin authority.
- **Anything touching path confinement, sanitisation, authorisation, the render cache key, or
  secret redaction.**
- Any change to the build, the gate, or the CI matrix.

An ADR is **not** required for an implementation choice the records already settled.

Format, and the two non-obvious rules:

- Location: `docs/content/en/decisions/NNNN-kebab-title.md`, front matter with `title`,
  `description`, `lede`, `weight`, `date`, `status`, `supersedes`, `superseded_by`. Body
  sections: `Context`, `Decision`, `Consequences`, `Alternatives considered`.
- **No leading `# ` in the body.** The layout renders the title as the page's only `<h1>`; a
  second one fails `check-site-structure.sh`, which is gate-blocking. Start at `## Context`.
- Cross-reference with `{{ "decisions/0002-foo/" | relURL }}` — relative, trailing slash, no
  leading `/`. Anything else fails `check-site-links.sh`.

A reversed decision keeps its record: set `status: superseded` and `superseded_by`. Never delete
and never renumber.

## Security invariants

These are the expensive-to-undo surfaces. Each has a named test in `spec.md` §S-14.

- **Path confinement is `os.Root`**, per campaign, created at registration. Never
  `filepath.Clean` plus a prefix check. It applies to writes *and* to every path in front
  matter. A campaign's content root is created **mode 0700**: a directory any account on
  the host can read is a campaign any account on the host can read, bypassing §S-8 entirely.
- **No access is 404, never 403.** A private campaign and a campaign that does not exist answer
  identically — same status, same body — or a 403 becomes an existence oracle. Authorisation is
  a gate the route mounts (`campaigns.RequireRead` / `RequirePlay` / `RequireEdit`), never a
  check inside a handler. See [0024](docs/content/en/decisions/0024-authorisation-gates-mount-not-per-handler.md).
- **The first account is a command, not an environment variable.** No bootstrap config value
  creates an administrator, because a password in the environment cannot be withdrawn from.
  See [0025](docs/content/en/decisions/0025-no-first-account-bootstrap-environment-variable.md).
- **Never `html.WithUnsafe()`.** Never. With a public tier and Obsidian sync as an input, this
  is a security boundary. Two layers enforce it: goldmark never interprets raw HTML, and bluemonday
  is an allowlist that strips the rest. A **block-level** raw HTML element therefore loses its text
  — CommonMark behaviour, and the test that says so is
  `TestRawHTMLBlockTextIsDropped`. Do not "fix" it by enabling unsafe HTML.
  [0028](docs/content/en/decisions/0028-render-output-is-permission-neutral.md)
- **The one thing the pipeline *adds* to sanitised output is `.target`, and it is added after the
  sanitiser.** `content/target.go` writes `class="target"` onto every focusable element in the page
  body, which is safe because the value is a fixed constant semiplane owns, the element set is
  written out rather than derived from anything a vault can reach, the value resolves to two minimum
  sizes and no behaviour, and the pass mutates a **parsed tree** rather than matching markup as a
  string — so it cannot resurrect what the sanitiser removed, structurally rather than by promise.
  `target` is deliberately **absent** from `policy.go`'s class allowlist: adding it there would let an
  author mint a class that is semiplane's, and would be a second source of truth for one value. The
  pass is unexported and confined to the page body, which is why a plugin rendering through the
  pipeline inherits the class — the mechanism UI §4.11.1 asks for. Do not move it before the
  sanitiser: the class is not on the allowlist, so the sanitiser would strip it straight back off.
  [0038](docs/content/en/decisions/0038-target-class-is-applied-by-the-render-pipeline.md)
- **Redaction runs on the source, before the render.** A redactor that ran after would leave the
  unredacted text in the renderer's buffers, the sanitiser's input and the cache. The seam is
  `content.Redactor`, and `content.NoSecrets()` is a pass-through that **removes nothing** until
  phase 10 — its name reads like a guarantee and it is the opposite.
  [0029](docs/content/en/decisions/0029-redaction-operates-on-the-source.md)
- **A cross-campaign wikilink is `[[/campaign/Page]]`**, with the leading slash, and is never
  inlined. The slash is what makes the relative reading and the cross-campaign reading mutually
  exclusive. `[[Other/Foo]]` is a *relative* link inside the home campaign.
  [0026](docs/content/en/decisions/0026-cross-campaign-wikilinks-are-vault-absolute.md)
- **The page index is the watcher-maintained `pages` table.** It was a walk of the content root
  until phase 4 wrote it; the walk is gone. The startup index runs **before** the router serves,
  because an empty table resolves no reference at all and every `[[wikilink]]` in the product
  renders `data-broken="true"` — a link-resolution bug's symptom with a missing boot step as its
  cause. `pages` has **no `front_matter` column** — a second copy of a regenerable block is a
  second answer — and `body_plain` is derived from the **source**, never from rendered HTML, which
  is what lets it exclude `[!secret]` content in every reveal state.
  [0027](docs/content/en/decisions/0027-no-front-matter-column-and-the-index-is-a-cache.md),
  [0031](docs/content/en/decisions/0031-body-plain-is-derived-from-the-source.md)
- **A `stat` on a closed `os.Root` reports no error and no entries**, so a walk of a dead
  descriptor is indistinguishable from an empty vault. `Root.Walk` checks liveness first, because
  `ReindexCampaign` prunes what it did not find and would otherwise empty a campaign's index while
  reporting success at every step.
- **Watch directories, never files** (S-4.2). fsnotify's file watch is bound to the inode, so the
  atomic write this project's own save path performs destroys it. And a new directory is watched
  **before** it is read — the reverse order loses a page created in the ~30µs between the two.
- **Obsidian Sync is untrusted input.** Shared vaults, community plugins, compromised devices.
  Validate all YAML — front matter is attacker-reachable — and resolve all paths inside
  `os.Root`.
- **A page's `title:` is not redacted, and must not hold a secret.** S-5.11 and migration 0007's
  comment both scope redaction to `body_plain`, because a title is one line of front matter with no
  callout structure and therefore **no boundary to redact to** — a rule that stripped titles would have
  to guess at which words are secret, and a guess that fires breaks a real title while a guess that
  misses leaks the secret. `pages.title` is indexed verbatim, and it is served in three places: the
  **search index**, the **nav tree**, and the **`<h1>`**. A `[!secret]` body is protected by its page's
  access gate; the title of that page appears in a result list a different reader can see. Verified
  live: `title: The passphrase is hunter2` lands in `pages.title` in every reveal state.
  [0036](docs/content/en/decisions/0036-page-titles-are-not-redacted.md)
- **Every gate response is `private, no-store`.** `writeError` in `internal/httpapi/campaigns` answers
  404/401/403, and every one of those is **reader-dependent**: the same URL is a 404 for an anonymous
  requestor and a 200 for a member, because S-8 answers "no access" without saying why. Nothing in the
  body distinguishes them — deliberately, so the status cannot become an existence oracle — which is
  exactly what makes the response unsafe to store. A reverse proxy in front of a self-hosted instance is
  the ordinary deployment, and one that cached an anonymous 404 would serve it to an entitled member.
- **Secret redaction is omission, not hiding.** Not `display:none`, not a comment, not a class.
  The callout is removed entirely, before sanitisation and before any template sees it.
- **The `ETag` is salted with `include_secrets`.** A GM response and a player response must
  never share a validator, or a cache serves the unredacted page to a player.
  [0016](docs/content/en/decisions/0016-salted-etag.md).
- **Cross-campaign links are never inlined.** The linking page's HTML must be byte-identical
  for every viewer. [0017](docs/content/en/decisions/0017-cross-campaign-links-never-inline.md).
- **`ruleset_version` fingerprints resolution semantics, not house-rule configuration.**
  Toggling a house rule must not strand a campaign.
  [0018](docs/content/en/decisions/0018-ruleset-version-fingerprint.md).
- **Secret reconciliation fails toward hiding.** Capped, and on exhaustion it leaves the secret
  hidden and logs an error.
- **A maximum attack die hits, whatever the critical rule said.** The hit and the
  critical are two questions. 5e's natural 20 is an automatic critical hit that
  bypasses armour class, and 2024's `crit_ignored_by_incapacitated` denies only that
  it **counts as a critical** against an paralysed, unconscious or incapacitated
  target — which is a claim about the damage dice, not about whether the sword lands.
  Asking the critical rule "did it hit" made a natural 20 **miss** against such a
  defender under 2024 while the identical roll auto-hit under 2014 and against any
  unexempted target: measured at armour class 30, `unconscious`, a natural 20, a total
  of 27, `met: false` and no mutation reaching the defender at all. The exemption
  governs what a 20 does to the dice and nothing else, and the auto-hit is a resolver
  rule with no toggle — `crit_attack_die_max` switches the *critical* off, and a pack
  that sets it false must still hit.
  `TestANaturalTwentyHitsUnderBothEditionsAndTheExemptionDoesNotMakeItMiss` holds it
  over both editions, because either row alone is satisfiable by a constant.
- **An unknown `system_id` refuses the game and the wiki still serves.** §10.8, S-14.8:
  a campaign whose plugin was removed or renamed is a real state and not a fault in
  the row. Three claims, separately asserted because any one can hold while the others
  fail — the wiki answers **200** with the page's content; the dispatch is **refused**;
  and the refusal **names the id**. The last one reaches a caller as a typed
  `*plugin.UnknownSystemError.ID` and an operator as a `plugin.missing` boot line
  carrying the campaign's slug and the id, **not** through the error's text: that string
  reaches a browser and a log line, so it is the reason alone (S-12.3). Nothing else
  produces that line, which is why `reportMissingSystems` is a separate boot pass rather
  than a branch of the fingerprint one.
- **An unknown `kind` is inert, not fatal.** S-14.7, §10.8's last row: the registry is
  kind-backed, so a page's `kind` resolves through `plugin.Registry` and an unrecognised
  one degrades to `prose` **in the index as well as on the page** — a page rendering as
  prose while the index says it is a game object is two answers, and the nav tree would
  offer a link to something the page will not render. The assertion needs both
  directions, because before phase 8 every kind was prose and the requirement was
  vacuous: a page of a **registered** kind must not degrade, which is what keeps the
  degradation from being satisfied by a registry that knows nothing.
- **No event carries secret content, file contents, or dice results.** Asserted by a test,
  because logging the thing that failed is the natural thing to do when debugging. The
  enforcement is `observability.EventAttributes` having **no field a page body could be passed
  through** — so **emit through `observability.Watch` or `observability.Index`, never through
  `slog` at the call site.** A raw `slog.Any("detail", raw)` compiles, ships, and puts a
  `[!secret]` callout body in a log aggregator, which is the one thing S-12.3 forbids. Errors
  are classified by `errorClass`, never passed through: a Markdown or YAML parser quotes the line
  it choked on, and on a wiki page that line is routinely a callout body.
- **Six event names beyond the architecture record's §13.2 list**, recorded in
  [0032](docs/content/en/decisions/0032-index-signals-beyond-the-architecture-list.md) rather than
  added quietly: four `index.*`, `content.settle_failed`, and the uncounted `index.renamed`.
  `observability.AllEventNames()` is 24 where §13.2 lists 18, and its test asserts the count —
  so a seventh addition without a record fails the build.

## Accessibility invariants

These are gate-blocking in a way most conventions are not: `make a11y` runs in
`check`, and a change that breaks one does not merely get a review comment.
[0034](docs/content/en/decisions/0034-tv-mode-not-a-width-band.md) and
[0035](docs/content/en/decisions/0035-no-vary-cookie-and-one-blocking-script.md)
are the phase's records; [0033](docs/content/en/decisions/0033-two-conflicts-in-the-ui-record.md)
resolves two conflicts inside the UI record.

- **Parse the DOM; never substring-match the markup.** It is how "no `world`"
  passes while the word sits in an HTML comment, an `aria-label`, or a
  `data-` attribute. Every §10.2 rule runs against `golang.org/x/net/html`,
  and `TestTheVocabularyAuditFindsTheWordWhereverItIs` feeds the audit six ways
  of smuggling the word in and requires it to object to each. An audit that
  cannot fail is worse than no audit, because it is a green light wired to
  nothing.
- **`errorClass` falls back to `%T`, so a wrapped error with no `Class()` is classified as
  `*fmt.wrapError`.** `observability.Classed` is consulted first and a package's errors
  teach it a class by implementing one method, but the fallback is still the last resort and
  it is not useful: a boot that refuses to resume reported
  `realtime.ruleset_unreadable … class:"*fmt.wrapError"`, which is the one thing an alert
  cannot match. **Measured, not hypothesised** — registering a campaign with a `--ruleset`
  that is not an `sp1:` fingerprint produces it on every start.
- **Zero is a fixed point, so a zero-length sample is not a settled size.** The
  settle filter (S-4.3) confirms a path by two `stat` samples agreeing. A writer
  that truncates in place with `open(O_TRUNC)` and is descheduled before its
  `write` leaves a window in which the file is **0 bytes** — and the truncation
  delivers no fsnotify event, because the event belongs to the write. An empty page
  and an unwritten page have the same `stat`, so the two samples agree and
  agreement is the filter's only evidence. The indexer then wrote a **wrong row**:
  `content_hash` of the empty string, `byte_size` 0, `title` `""`. Semiplane's own
  atomic write (S-6.4) cannot produce it, which is why the product's writer never
  exposed it; a sync client writing in place produces it routinely, and this broke
  CI twice on tests belonging to a different phase. A zero-length sample is now
  re-armed **the way an event re-arms it** — the quiet period restarts, the first
  sample is discarded, and the **budget is not re-taken**, because this is the same
  confirmation being patient rather than a new one. A path still zero at budget
  expiry *does* settle, as `outcomeSettledEmpty`, because a blank note is a
  legitimate page.
  [0037](docs/content/en/decisions/0037-zero-bytes-is-not-a-stable-size.md)
- **The converse holds too: a non-zero size that holds *is* stable, so a writer that
  stops for a whole quiet period has its page settled whole — and the test suite must be
  able to tell that from a premature settle.** Three tests used to assert that a
  continuously written page *never* settles, which is a claim about the fixture's own
  goroutine rather than about the filter. The same assertion failed CI twice, at `0
  bytes` and then at `4196 bytes`, and the filter was right both times: `4196` is
  `len(pageVersion(100))`, the first version the noise generator writes, so the arrival
  could only have come from the generator pausing. **The fixture now records when it
  delivered each event and judges every arrival by entitlement** — an event-free window at
  least as long as the quiet period. Unentitled is a defect and fails on any machine;
  entitled is S-4.3 doing its job, and is logged rather than hidden. `silent` is sound
  only where no background writer is running; where one is, `silentWhileWriting`. Each
  assertion was verified by mutation, and the mutation removing the quiet period *and*
  the confirmation reproduces the reported CI failure verbatim with the cause attached.
  [0040](docs/content/en/decisions/0040-a-settle-is-judged-by-entitlement.md)
- **A gate test that cannot fail is not a gate.** Every rule added in phase 5 was
  checked by mutation: remove the import, lower `--target-min`, wrap a TV rule
  in a `min-width` band, drop a field from a conversion. Three of the first
  versions of those tests did **not** fail, and the reason is in the code: one
  scanned forward from a selector when the mutation wraps the selector *outside*,
  and one counted an absent attribute as an empty one.
- **TV is `[data-ui="tv"]`, never a width band.** A 55" television and a 12.9"
  tablet in landscape are indistinguishable to CSS and need opposite layouts, so
  no rule selecting TV mode may sit inside a `@media` naming `min-width`,
  `orientation`, `pointer` or `hover`. `prefers-*` and `forced-colors` are
  *required* in every mode and are deliberately not banned.
- **No `Vary` header names `sp_ui`**, and the document's independence from that
  cookie is held by asserting the bytes are byte-identical across five cookie
  values, not by asserting the header's absence. The absence catches today; the
  bytes catch a later phase that adds a cookie dependency with no header change
  to notice.
- **The wiki route does carry `Vary: Cookie`, and that is correct.** The shell
  carries the reader's name and a sign-out form, so two 200 responses to one
  public URL differ — measured at 4460 bytes for a GM and 4227 for an
  anonymous reader. "No `Vary` at all" was claimed in ADR 0035 and is **false**;
  the record is corrected. A test asserting a header's *absence* cannot see the
  variation that header was protecting, so byte-identity is the substantive
  check and header-absence is only a symptom.
- **`.target` is enforced by construction**, so §10.6's audit is a per-route walk
  over parsed HTML: every `a[href]`, `button`, `input`, `select`, `textarea`,
  `summary` and `[tabindex]` must carry the class. `--target-min` is 44px at
  compact and 56px at TV, and both are asserted from the **built** stylesheet —
  the markup carries the class and the stylesheet decides what it means.
- **A skip link and the landmark it names are one condition in two places.**
  §4.6 removes the navigation before a campaign exists and §7.2 puts the
  navigation's skip link in the document only where the navigation does. Both
  directions are asserted, because "a link with no landmark" and "a landmark with
  no link" are both failures and only one is visible in a rendering.

## Conventions that the linter will not catch

- Migrations are forward-only, under `internal/store/migrations/`. **Never edit a shipped
  migration.** Add a new one.
- Prefer the standard library; check `go.mod` before adding a second library for the same job.
- Registration happens in the composition root, never in `init()`. `init()` hides ordering and
  leaks state between tests.
- Rule code must be deterministic: no `time.Now`, no map iteration, no direct `crypto/rand`.
  There is no sandbox, so this is enforced by lint and tests.
- Templates get `data-testid` on elements under test. Assert on that, never on CSS classes or
  DOM shape.
- Do not add a `gosec` or other linter `excludes` entry to silence a finding. Fix the code, or
  justify the suppression inline with `//nolint:lor // why`.

## Skills

Read the relevant one before writing code; they carry the detail this file
deliberately omits.

- **go-quality-gate** — the verify loop, and what each common lint failure means.
- **go-conventions** — error handling, context, HTTP, database, testing patterns.
- **browser-e2e** — driving the Playwright MCP, and the newline-delimited JSON
  transport it uses.

## MCP servers

Configured in `.opencode/opencode.jsonc`, which is the repo's only agent config.
It points at the committed `.opencode/skills`, which is OpenCode's own project
skill location, so a checkout resolves its own skills with no second agent's
directory and no symlink.

- **playwright** — 25 `browser_*` tools, Chromium, `--isolated`. Diagnostic
  use; anything that must persist belongs in a committed test.
- **context7** — current library documentation. Prefer it over recalling
  third-party API signatures from memory.

`browser_evaluate` and `browser_run_code_unsafe` require approval. There is no
Go-specific MCP: gopls already provides diagnostics, and OpenCode consumes it
natively through its `lsp` tool.

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
records from `.opencode/plans/` at build time, so a filter covering only `docs/**`
would silently stop deploying when a record changes. Do not add one.

`main` is protected: no direct pushes, pull requests required, head branches
auto-deleted, and `gate`, `labels` and `docs` required.

## Git

Branch and commit only when asked. Never commit `.env`, `*.db`, or
`.playwright-mcp/`. `git stash` is shared across worktrees in Agent Manager —
resolve conflicts in the worktree instead.