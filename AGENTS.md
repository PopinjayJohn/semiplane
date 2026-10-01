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
| `.kilo/plans/1790774477695-semiplane-architecture-overview.md` | Domain semantics, data model, protocol. A dated record. |
| `.kilo/plans/1790778908232-responsive-ui-ux-design-spec.md` | Interface contract. A dated record. |

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
| gopls | v0.23.0 | `/root/go/bin/gopls`; Kilo discovers it as the Go LSP |
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
format diff check, `make css`, `make templ`, `go build`, `go vet`,
`golangci-lint run`, `go test -race`.

`css` and `templ` precede `build` because `internal/web` embeds both outputs.
The binary is the first place a missing stylesheet shows up, so `build` also
checks for the file and says `run: make css` rather than failing on the embed
pattern. `make tailwind` downloads ~110MB once per checkout into `.toolbin/`
and verifies it against a committed digest of its release manifest;
[0019](docs/content/en/decisions/0019-tailwind-standalone-pinned.md).

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

## Build phases

Thirteen phases, each on its own branch. The plan is
`.kilo/plans/1790796797509-phased-delivery-plan.md`.

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
  └─ wiki/       the read path, and the S-5.7 redaction ordering
internal/campaignroots/ opens one os.Root per campaign at startup
internal/web/     templ components and static assets
docs/             Hugo documentation site (its own project root)
demo-vault/       the demo campaigns (phase 11)
scripts/          sync-labels.sh, check-site-links.sh, check-site-structure.sh
```

Note two subdirectories the architecture overview's module tree requires and that do not
exist yet: `internal/domain/rules/` and `internal/domain/systems/` (phase 8), and
`internal/web/plugins/` (phase 8). `scripts/check-demo.sh` arrives with phase 11.

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

Configured in `kilo.json`, and mirrored for OpenCode in `.opencode/opencode.jsonc`.
Both point at the committed `.kilo/skills`, so the skills have one source of truth.

- **playwright** — 25 `browser_*` tools, Chromium, `--isolated`. Diagnostic
  use; anything that must persist belongs in a committed test.
- **context7** — current library documentation. Prefer it over recalling
  third-party API signatures from memory.

`browser_evaluate` and `browser_run_code_unsafe` require approval. There is no
Go-specific MCP: gopls already provides diagnostics, and Kilo consumes it
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
records from `.kilo/plans/` at build time, so a filter covering only `docs/**`
would silently stop deploying when a record changes. Do not add one.

`main` is protected: no direct pushes, pull requests required, head branches
auto-deleted, and `gate`, `labels` and `docs` required.

## Git

Branch and commit only when asked. Never commit `.env`, `*.db`, or
`.playwright-mcp/`. `git stash` is shared across worktrees in Agent Manager —
resolve conflicts in the worktree instead.