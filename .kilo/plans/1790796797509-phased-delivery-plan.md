# semiplane — Phased Delivery Plan

Thirteen sequential phases, each on its own branch, each subdivided into work items
that separate sub-agents can execute concurrently and merge without conflict.

**Sources (authoritative, unmodified by this plan):**

- `.kilo/plans/1790774477695-semiplane-architecture-overview.md` — architecture overview (owns domain semantics)
- `.kilo/plans/1790778908232-responsive-ui-ux-design-spec.md` — UI/UX specification (owns pixels)

**Repo state at time of writing:** `main` at `4f322ad`, clean tree. Running code is
`cmd/server/main.go` (95 lines), `internal/config/` (75 lines), `internal/httpapi/router.go`
(70 lines, bare mux, `/healthz` + `/readyz` only). `internal/domain`, `internal/store` and
`internal/web` contain nothing but `.gitkeep`. Zero third-party dependencies in `go.mod`.

---

## 1. Decisions taken

| # | Decision | Why |
|---|---|---|
| D1 | **There is no `work/` tree — the request's `work/internal` and `work/cmd` were a transcription of `internal` and `cmd`.** The placeholders are `internal/domain/.gitkeep`, `internal/store/.gitkeep`, `internal/web/.gitkeep`, plus the bare `router.go` and thin `main.go`. Each `.gitkeep` is deleted by the phase that gives its package real code — not in one upfront move. | Restructuring is the architecture plan §11 module tree, delivered as each phase lands. See §3 for the path-by-path table. |
| D2 | **Branch topology:** `phase/NN-slug` per phase; sub-agents work on `phase/NN-slug/<work-item>` and PR into the phase branch; the phase branch PRs to `main`. | Parallelism lives *inside* a phase. Keeps `main` green between phases, which architecture §15 requires ("the server starts and serves `/healthz` from phase 1 onward"). `ci.yml` triggers on `pull_request` with no branch filter, so child PRs get the full gate. |
| D3 | **ADRs are Hugo content pages** at `docs/content/en/decisions/NNNN-kebab-title.md`, with a `Decisions` nav entry. | They pass `check-site-links.sh` and `check-site-structure.sh` for free, and are published by the existing `pages.yml` workflow. |
| D4 | **One project `spec.md`** at `docs/content/en/contributing/spec.md`, with numbered requirement IDs; phases carry task lists, not duplicated requirements. | Requirement text restated in three phases drifts. IDs let a PR cite `S-5.6.1`. |
| D5 | **13 phases, 0–12.** Architecture §15's phases 7–9 (plugin contracts, first systems, house rules) collapse into one. UI Slice A becomes its own phase 5, before the wiki surface. A demo vault phase is inserted immediately before hardening. | The architecture plan calls 7–9 "separable"; merging them turns three serial phases into one with a wider internal fan-out. UI §15.2 proves the shell cannot be styled before the CSS toolchain exists. The demo vault needs every surface to exist before it can demonstrate them. |
| D6 | **Tailwind v4 in CI:** `make css` downloads the pinned standalone release into `.toolbin/`, verifies a committed SHA256, runs it. `make check` depends on `css` and `templ` before `build`. | `contributing.md` already promises `check` will depend on `css` and `templ`. Mirrors the existing `GOBIN="$GITHUB_WORKSPACE/.toolbin"` pattern in `ci.yml`. Keeps the no-Node rule; commits no binary. |
| D7 | **The demo vault ships as a release artifact, not embedded.** Authored and committed at `demo-vault/`, built by `make demo-artifact` into `semiplane-demo-v<version>.tar.gz`, published to the GitHub Release beside the binary, downloaded and extracted by the install guide. | Keeps the vault out of the binary, allows images of any size — a map PNG is tens of megabytes (architecture §16.2) — and lets the vault ship independently of the Go build. The cost is version skew, mitigated by D11. |
| D8 | **The vault is the tutorial.** The showcase campaign's `kind: index` page introduces one feature per page in reading order, and the prose does the teaching. No HTML-comment annotations and no separate docs walkthrough page. | One place to maintain, and the demo content is the teaching content rather than a wrapper around it. The weakness — a feature with no natural prose home has nowhere to be demonstrated — is answered mechanically by `make demo-check` (D9), not by annotations. |
| D9 | **Completeness is a gate, not a review.** `make demo-check` asserts: every page reachable from the campaign index by `[[wikilink]]`; every registered `kind` present in front matter; every render-pipeline extension used; both `[!secret]` states present; the unresolved-link count equals exactly the deliberate breakage; every asset extension referenced. The kind list is read from the **plugin registry**, so adding a kind to a plugin turns the demo red until it is demonstrated. | This is what makes D8 safe. It also makes the demo self-maintaining: the gate is derived from the registry rather than from a hand-written checklist that drifts on the day a plugin adds a kind. |
| D10 | **Three campaigns.** `greyhaven` (private, the showcase, every feature), `public-post` (public, minimal — proves anonymous read, visibility-gated assets and search, and a cross-campaign wikilink that resolves within visibility), `forgotten-realm` (private, `system_id: pathfinder-2e`, unresolvable — proves §10.8: the game is refused and names the ID it wants while the wiki still serves 200). | The access matrix (§8) and the degraded path (§10.8) are exactly the boundaries a new user is most likely to get wrong, and prose does not demonstrate them. `forgotten-realm` exists because that failure is otherwise hard to explain and impossible to see. |
| D11 | **Seeded live state, and an explicit refusal on skew.** The seed writes a populated `campaign_state` for `greyhaven` — a scene with placements, partially-depleted HP, conditions, fog and an initiative order — so `/play` is not an empty grid. `ruleset_version` is **resolved through the house-rule path, never hardcoded**, or the resume path refuses to start and the demo breaks on first load. The seed refuses on binary/artifact version mismatch and on a second seed. `semiplane demo reset` deletes the three campaigns and their state, leaving the vault untouched, so the tutorial is replayable. | The map is the headline surface of a VTT; a demo that shows it empty demonstrates nothing. Resolving the ruleset through the real path is the difference between a seed and a landmine. Chat is in-memory and lost on restart, so it is **not** seedable — a seeded `kind: journal` page stands in for it, which also demonstrates the export path and its default exclusion of secret content. |

### 1.1 Two defects in the design records, recorded not edited

`docs/content/en/design/_index.md` states these records are immutable, and the architecture
plan §16.1 deliberately left the UI spec untouched "to avoid conflicting with its author". This
plan follows that rule: **neither record is edited.** Both corrections are carried as ADRs.

| Defect | Where | Correction |
|---|---|---|
| `ETag` = the content hash, with two body variants derived from it (§5.5) | architecture §5.5 vs UI §6.6 | `ETag = W/"<sha256(content_hash + ':' + include_secrets)>"`. Without the salt, a GM's unredacted response and a player's redacted one advertise the same validator, and any cache holding both serves whichever it stored first — a direct secret leak to a player. |
| Twelve-phase build order places the templ shell at phase 5 and Tailwind at phase 10 | architecture §15 vs UI §15.2 | Toolchain moves to phase 1; the shell becomes phase 5, built against fixture HTML, and lands before any route fills a slot (UI §13 constraint 1). |

Both are listed in the "Known staleness" section of `docs/content/en/design/_index.md`, which is
the file that already exists to carry exactly this.

### 1.2 Deferred questions, resolved

Eight questions the design records — and this plan — deferred. Each is now decided; none is
revisited in a phase.

| # | Question | Resolution | Lands in |
|---|---|---|---|
| D12 | May an author **write** a cross-campaign wikilink? (architecture §16.1) | **Yes, but never inline it.** A cross-campaign `[[Page]]` renders as a plain `<a href="/c/{slug}/wiki/{path}">` with the literal wikilink text as its label — no lookup, no inlining. Cross-campaign **embed** `![[Page]]` stays forbidden. | P3 (C4) |
| D13 | How are PixiJS and Datastar delivered to the browser? | **Committed, integrity-pinned, served from own origin.** Both under `internal/web/static/vendor/`, recorded in `tools/vendor.json` as {url, version, sha256}; `make vendor` re-fetches and verifies, `make vendor-check` re-hashes what is committed and fails on a mismatch. `go:embed` serves them. | P9 |
| D14 | The demo account's password? | **Random, printed once on seed**, with `--password` to override for scripted and CI use. The demo GM is **campaign-GM only, never an instance admin** — `is_admin` is for campaign registration and the demo does not need it. | P11 (V3) |
| D15 | "Revealed" marker default on the published page? (UI §14.1) | **Confirm §4.10.4:** on in the editor, off on the published page, a per-campaign setting. | P10 (S8) |
| D16 | Default `/play` rail tab? (UI §14.3) | **The spec's per-form-factor split** — `Initiative` on laptop, `Tokens` on TV and phone — **remembered per campaign in `localStorage`**, so the default applies only on a first visit. Falls back silently when storage is unavailable. | P9 (C2) |
| D17 | What is in the `ruleset_version` fingerprint? (architecture §16.5) | **System ID + the system's `RulesetVersion()` + base pack and overlay pack versions. House-rule enablement is excluded.** | P7 (R6), P8 (P2d) |
| D18 | How does a GM recover from drift? | **Refuse to resume, with an explicit audited discard.** `/c/{slug}/status` names both versions and the consequence; `/play` shows a GM alert; recovery is a separate GM-only, confirmation-gated action that discards persisted state and starts fresh, written to `audit_log`. | P7 (R6) |
| D19 | What map asset does the demo ship? | **A hand-authored SVG declaring a world extent several times the largest viewport** — say 4800×3200 units — so panning, zooming and the resize-preserves-world-centre rule are all exercised, in a few KB. | P11 (V1) |

Four of these correct a gap in the design records rather than choosing between options they
offered, and are worth stating plainly:

- **D12 exists because §5.5's render cache is permission-neutral by construction.** It gives
  exactly two variants per page keyed on `include_secrets` and states "never a per-user cache".
  P10 already had to break that once — the salted `ETag` exists precisely because `[!secret]`
  made the body viewer-dependent. Inlining a cross-campaign link would break it a second time,
  and unboundedly, because the variant count would then depend on how many campaigns the viewer
  can see. Not inlining keeps the invariant with no new cache variant.
- **D17 corrects a literal reading of §10.5**, whose chain ends "→ effective ruleset
  (+ `ruleset_version`)". Read literally, the enabled house-rule set is in the fingerprint — and
  since house rules are data applied at resolution time, a GM enabling one mid-game would change
  the fingerprint and be refused on their own campaign. The set stays stored, ordered and audited;
  it simply is not part of the identity of the resolution semantics.
- **D18 exists because there is no session entity.** §10.8's "prompt the GM to start a new
  session" means discarding the current game, because a campaign has at most one live tabletop
  (§2.1). Read literally with no escape, a pack revision strands every live campaign. The
  refusal is never silent, which was §16.5's actual concern; the GM chooses the loss.
- **D19 optimises the right axis.** §7.6's camera rule is only demonstrated if the world is
  larger than the viewport — a map that fits on screen does not exercise it. A large *world* and
  many *bytes* are independent properties of a vector image, so the demo gets the first without
  the second. A realistic raster map would also blur the moment the camera zooms.

---

## 2. Governance model

### 2.1 Branches

```
main
└── phase/NN-slug                    one per phase, one PR to main
    ├── phase/NN-slug/<work-item>    one per sub-agent, one PR to the phase branch
    └── ...
```

Rules:

1. A phase branch is cut from `main` when the previous phase's PR has merged. Never from a stale `main`.
2. A work-item branch is cut from the phase branch, never from `main`.
3. A phase branch merges to `main` only when its Definition of Done (§2.3) is met in full.
4. Merge with a merge commit or squash per repository convention; squash is preferred — the phase
   branch's history is work-item bookkeeping, not a record worth preserving.
5. `main` has branch protection: no direct pushes, PRs required, head branches auto-deleted,
   required status checks `gate`, `labels`, `docs`. A phase is not done until all three are green
   on its PR against current `main`.

### 2.2 Conflict rules — what makes parallel work actually merge

These are the rules that turn "N sub-agents" into "N sub-agents that finish", rather than N
sub-agents that spend the phase resolving conflicts.

1. **Path ownership.** Every work item declares the paths it owns and touches nothing else. A
   drive-by edit is a review comment, not a merge conflict someone resolves later.
2. **One integrator per phase.** Exactly one work item owns the shared, cross-cutting files:
   `cmd/server/`, `internal/httpapi/router.go`, `Makefile`, `.github/workflows/`, `go.mod`,
   `go.sum`, `.golangci.yml`, `docs/hugo.toml`. Every other agent requests a change to one of
   those files from the integrator rather than making it. This is the single highest-value rule
   in this section: `go.sum` is regenerated by any dependency addition and is otherwise the most
   contended file in a Go repo of this size.
3. **Dependencies are pre-added.** `spec.md` lists every pinned dependency per phase. The
   integrator commits all of them to the phase branch in the phase's first commit, so no work
   item ever edits `go.mod` mid-phase.
4. **ADR numbers are pre-assigned** in the phase plan before any agent starts. A number is never
   reused, and never renumbered.
5. **Generated files are never in a diff.** `*_templ.go`, the built stylesheet, `docs/public/`,
   `docs/resources/` and `docs/assets/plans/` are produced by the gate or the Makefile and are
   gitignored. If a generated file shows up in a PR, that PR is wrong.
6. **Sub-agents report, they do not merge.** A work-item PR is opened against the phase branch;
   the integrator merges in the declared order. Agents never push to `main` and never open a PR
   against `main`.

### 2.3 Definition of Done — a phase is complete only when all of these hold

1. `make ci` green locally: `lint-verify`, format diff check, `go build`, `go vet`,
   `golangci-lint run`, `go test -race -count=1`.
2. `make site-check` green (the phase touched `docs/`).
3. `make lint-workflows` green (the phase touched `.github/workflows/`).
4. `make labels-check` green (the phase touched `.github/labels.yml` or an issue form).
5. All three CI checks — `gate`, `labels`, `docs` — green on the phase PR against current `main`.
6. Every work item in the phase is merged, or explicitly dropped with a reason recorded in the
   phase PR description. A silently dropped work item is an incomplete phase.
7. `spec.md` updated if the phase changed a stated requirement, an ID added if it added one.
8. An ADR added for every significant decision taken in the phase, per §2.4.
9. `AGENTS.md` updated if the phase changed the layout, the gate, or a convention.
10. The server still starts and answers `/healthz` — the independently-runnable invariant from
    architecture §15. A phase that leaves `main` unbootable fails the DoD regardless of CI.
11. No `.gitkeep` remains in any package that now has real source files.
12. The phase PR description records which architecture §14 validation rows and UI §10 checks
    the phase claims, so §11 hardening can audit coverage rather than re-derive it.

**Definition of Done — release:** the secret-redaction test is green, and it fails loudly if a
non-GM response ever contains secret text anywhere in the payload. Architecture §12 calls this
the highest-value security test in the project; architecture §14 says it gates the release.
`make demo-check` is also green, because a demo that does not match the product misleads every
reader who trusts it.

### 2.4 When an ADR is required

Write one when a work item makes a choice that a later reader could reasonably have made
differently, and where being wrong is expensive to undo. Concretely, in this project:

- A new third-party dependency, or dropping / upgrading / replacing one.
- A deviation from architecture §2's decision table.
- A change to the data model, the URL scheme, the WebSocket protocol, the intent vocabulary, the
  `System` interface, or plugin authority.
- Anything touching path confinement, sanitisation, authorization, the render cache key, or
  secret redaction. (This is the PR template's "Design impact" list; it is the trigger list.)
- A change to the build, the gate, or the CI matrix.

An ADR is **not** required for a routine implementation choice the design records already
settled. Phase 0 backfills the ones architecture §2 already decided; those are records of a
decision taken, not new decisions.

### 2.5 ADR format

```yaml
---
title: "0001 — Single process, in-memory campaign state"
description: "One short sentence naming the decision and its cost."
lede: "Two sentences: what was chosen, and what it forecloses."
weight: 10
date: "2026-09-30"
status: "accepted"          # proposed | accepted | superseded
supersedes: []              # ADR numbers
superseded_by: ""           # ADR number, set when this record is retired
---
```

Body sections: `Context`, `Decision`, `Consequences`, `Alternatives considered`
(with why each lost).

Two non-obvious constraints, both enforced by the docs CI job:

- **No leading `# ` in the body.** `docs/layouts/_default/single.html` renders `{{ .Title }}`
  as the page's only `<h1>`. A second one fails `scripts/check-site-structure.sh`, which is
  gate-blocking. Start the body at `## Context`.
- **Every internal link must resolve**, or `scripts/check-site-links.sh` fails. Cross-reference
  ADRs with `{{ "decisions/0002-foo/" | relURL }}` — relative, with a trailing slash, and never
  a leading `/` (a project site on GitHub Pages would resolve that to the server root).

A record is retired by editing its `status` to `superseded` and setting `superseded_by`. It is
never deleted and never renumbered.

### 2.6 `spec.md`

`docs/content/en/contributing/spec.md` holds the whole system's technical requirements, derived
from architecture §1–§14. Requirements carry stable IDs — `S-5.6.1` for "a non-GM response
contains no secret text" — so a test, a PR and an ADR can all cite the same thing.

**Creating this file requires one refactor first.** `docs/content/en/contributing.md` is a single
file, and Hugo cannot hold both `contributing.md` and a `contributing/` directory. Move
`contributing.md` to `docs/content/en/contributing/_index.md` (its URL `/contributing/` is
unchanged, and `list.html` auto-lists `RegularPagesRecursive` so the spec appears beneath it),
then add `contributing/spec.md` with `weight: 10`.

spec.md is a **living** document. It is edited in the phase that changes a requirement, in the
same PR. Phases cite requirement IDs; they do not restate requirements.

### 2.7 `AGENTS.md` — what phase 0 adds

`AGENTS.md` is the operational contract an autonomous agent reads first. Phase 0 appends:

- **Single-instance rule.** Architecture §1.1 explicitly asks for this: authoritative game state
  is in memory and the WebSocket hub is process-local, so *two instances behind a load balancer
  will silently diverge*. SQLite's single-writer limit is not the constraint. Nothing "helpfully
  scales" this without a shared broker first.
- **Phase table**: number, name, branch, status, blocking dependency.
- **Branch topology and the six conflict rules** (§2.2), since an agent that does not know them
  will produce an unmergeable PR.
- **Integration-owner rule**: an agent that needs `go.mod`, `Makefile`, `router.go`,
  `cmd/server/`, `ci.yml` or `hugo.toml` changed asks the phase integrator.
- **Where things live**: `spec.md` for requirements, `decisions/` for ADRs, and the rule for when
  each is written.
- **Vocabulary ban.** UI §1.2 requires: the strings "world" and "session" appear nowhere in the
  interface — not in `aria-label`, `title`, empty states or error copy. A leftover is a bug and a
  grep is a test.
- **UI label table** (UI §1.2): campaign → Campaign, page → Page, placement → its kind's label
  (a `token` placement is a **Token**), the live tabletop → **Table** (not a domain entity — a
  campaign has at most one, so there is no list of them).
- **ETag salt rule**, superseding architecture §5.5 (see §1.1).
- **`.gitkeep` rule**: delete the marker when a package gains its first real file.
- **Demo vault rules** (landed by phase 11):
  - The vault is committed at `demo-vault/` and is the **source**; the shipped artifact is built
    from it by `make demo-artifact`. Never edit the artifact, and never point a test at it —
    `make check` must not depend on a download.
  - `make demo-check` is gate-blocking: every page reachable from its campaign index, every
    registry-known `kind` demonstrated, every render extension used, both `[!secret]` states
    present, unresolved-link count equal to the deliberate breakage. Its kind list comes from
    the plugin registry, so **adding a kind to a plugin turns the demo red** until it is
    demonstrated. That is the intended behaviour, not a bug to suppress.
  - **`forgotten-realm` uses `system_id: pathfinder-2e` on purpose.** It exists to demonstrate the
    §10.8 degraded path. Do not "fix" the demo by registering a Pathfinder plugin; doing so
    destroys the only demonstration of that failure mode.
  - The seed resolves `ruleset_version` through the house-rule path. Never hardcode it — a
    mismatch makes the game refuse to resume, which is the correct behaviour and a broken demo.
- **Cross-campaign links are hyperlinks, never inlined (D12).** A `[[Page]]` that resolves outside
  its campaign renders as a plain `<a>` with the literal wikilink text as its label. Do not
  inline the target's content and do not look up its title: the linking page's HTML must be
  byte-identical for every viewer, or the two-variant render cache is gone.
- **`ruleset_version` is a fingerprint of resolution semantics (D17), not of house-rule
  configuration.** System ID + the system's `RulesetVersion()` + base and overlay pack versions.
  House-rule enablement is deliberately excluded — including it would refuse to resume a campaign
  whose GM merely toggled a house rule.

---

## 3. The refactor: placeholders to real structure

There is no `work/` tree. The placeholders are the three `.gitkeep`-only packages, plus two
near-empty packages that become real during the build. The architecture plan §11 module tree is
delivered incrementally — each package is created by the phase that first needs it, never as an
empty scaffold.

| Path | Today | Created / populated by | Note |
|---|---|---|---|
| `internal/domain/` | `.gitkeep` | P2 | delete `.gitkeep`; pure types, no I/O |
| `internal/domain/rules/` | absent | P8 | `System`, `Intent`, `Mutation`, `Expr`, `Kind`, `View` |
| `internal/domain/systems/` | absent | P8 | gameplay plugins; 5e engine + packs + overlays |
| `internal/store/` | `.gitkeep` | P1 | delete `.gitkeep`; `Open`, WAL, single-writer, `busy_timeout` |
| `internal/store/migrations/` | absent | P1 | forward-only; never edit a shipped migration |
| `internal/content/` | absent | P3 | `os.Root` confinement, front matter, render, cache |
| `internal/realtime/` | absent | P7 | hub, campaign state, protocol codec |
| `internal/plugin/` | absent | P8 | registry; explicit registration, no `init()` |
| `internal/web/` | `.gitkeep` | P5 | delete `.gitkeep`; templ components + embedded assets |
| `internal/web/plugins/` | absent | P8 | UI page-type and render-hook registration |
| `internal/httpapi/` | bare mux, 2 routes | P1 → P11 | grows into the §9 route table |
| `cmd/server/` | thin main | P1 → P11 | becomes the composition root; **no `init()` registration** |
| `internal/demo/` | absent | P11 | manifest schema, the seed, and the coverage gate |
| `demo-vault/` | absent | P11 | three committed campaigns; the source for the release artifact |

---

## 4. The phases

Dependency graph, `A → B` meaning A's branch must merge before B's branch is cut:

```
P0 ─► P1 ─► P2 ─► P3 ─► P4 ─► P6 ─► P7 ─► P8 ─► P9 ─► P10 ─► P11 ─► P12
            └─────────────► P5 ──┘                    ▲
                                                  demo vault needs
                                        every surface it demonstrates
```

`P5` (the shell) branches from `P1` and merges into `main` before `P6`. It is built against
fixture HTML and never blocks a backend phase — that is the whole point of building it early.

`P11` (the demo vault) sits immediately before hardening because it is an integration
showcase: it cannot demonstrate a feature that does not exist yet. It also gives `P12`'s
browser tests something complete and stable to drive.

---

### P0 — Governance and specification · `phase/00-governance`

**Goal:** the contracts every later phase cites, written before the code that has to satisfy them.
No Go. `make site-check` must stay green throughout.

Dependencies: none. Blocks: everything.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| G1 | ADR 0001–0004: runtime & infrastructure — Go 1.27.1; `net/http` ServeMux over Chi (Chi carries advisories `GO-2025-3770` / `GO-2026-4316`); `log/slog` as the only observability mechanism, no metrics server, no tracing; the single-process constraint and what it forecloses | `decisions/000{1,2,3,4}-*.md` | wave A |
| G2 | ADR 0005–0008: content & persistence — filesystem markdown is the source of truth and SQLite is a rebuildable index; `modernc.org/sqlite` v1.58.0 (pure Go, FTS5 compiled in; `mattn/go-sqlite3` needs both `-tags fts5` and gcc); FTS5 external-content table with `unicode61 remove_diacritics 2` and deliberately no `porter`; forward-only migrations and why FTS5 tables are drop-and-recreate | `decisions/000{5,6,7,8}-*.md` | wave A |
| G3 | ADR 0009–0012: realtime & plugins — server-authoritative intents, clients send intent and never state; `coder/websocket` over gorilla (advisory `GO-2026-6278` plus panics on concurrent writes); plugins are compiled-in Go modules, never dynamically loaded, and registration is explicit in the composition root rather than `init()`; plugin authority — a UI plugin may only emit operations a gameplay system already resolves | `decisions/000{9,10,11,12}-*.md` | wave A |
| G4 | ADR 0013–0016: interface & corrections — templ v0.3.1020 (v0.x, pin exactly); Datastar scoped to live UI only, never content delivery and never the map; PixiJS v8, with the client surface staying system-agnostic because `token` and `scene` are semiplane-owned; **ADR 0016: the salted `ETag`**, superseding architecture §5.5 | `decisions/001{3,4,5,6}-*.md` | wave A |
| **G5** | **Integrator.** `decisions/_index.md`; move `contributing.md` → `contributing/_index.md`; author `contributing/spec.md`; `Decisions` entry in `hugo.toml` nav; an optional `decision-meta` partial rendering `status` / `date` / `supersedes`; `AGENTS.md` rewrite per §2.7; PR template gains "Phase / branch", "ADR added", "Requirement IDs touched", and "work item dropped, with reason" | `decisions/_index.md`, `contributing/**`, `docs/hugo.toml`, `AGENTS.md`, `.github/PULL_REQUEST_TEMPLATE.md` | wave A |

Wave A is five agents with disjoint file sets. The only shared file is `hugo.toml` and
`AGENTS.md`, both owned solely by G5.

**DoD:** §2.3 items 2, 5, 6, 7, 8, 9. `make site-check` green — every ADR passes
`check-site-structure.sh` (no second `<h1>`, the layout supplies the landmarks) and
`check-site-links.sh` (every `relURL` cross-reference resolves).

**Watch for:** the stale comment in `docs/layouts/partials/nav.html`, which says the nav is
"built from the site's own section list" while the template ranges `site.Params.nav`. Fix the
comment rather than the code, or a future agent will trust the comment and delete the array.

---

### P1 — Foundations and toolchain · `phase/01-foundations`

**Goal:** architecture §15.1, plus the frontend toolchain that UI §15.2 says must precede the
shell. Nothing user-visible lands here; everything later lands on top of it.

Dependencies: P0 merged.

Pinned dependencies the integrator adds in the phase's first commit (rule §2.2.3):
`modernc.org/sqlite v1.58.0`, `github.com/a-h/templ v0.3.1020`, `gopkg.in/yaml.v3` *or*
`github.com/goccy/go-yaml` (G5 picks one in `spec.md` and records it), `github.com/coder/websocket`.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| F1 | Middleware chain: `RequestID`, `Recoverer`, `RealIP`, `Log`, `Timeout`, composed by wrapping. `RequestID` generates and echoes a request ID and puts it on the context; every `slog` line carries it | `internal/httpapi/middleware/*.go` | wave A |
| F2 | `store.Open` — one `*sql.DB` per process, never copied; WAL; `busy_timeout`; a **single writer goroutine**, never a pool of writers; the forward-only migration runner under `internal/store/migrations/`; the first migration establishing `schema_migrations`. **Delete `internal/store/.gitkeep`.** | `internal/store/**` | wave A |
| F3 | Frontend toolchain: `make templ`, `make css`; Tailwind v4 standalone downloaded to `.toolbin/` with a committed SHA256; `check` gains `css` and `templ` as prerequisites of `build`; `ci.yml` gains the install step and the tools. Record the decision as an ADR | `Makefile`, `.github/workflows/ci.yml`, `tools/**`, `.gitignore` | wave A |
| **F4** | **Integrator.** Extend `internal/config` with `SEMIPLANE_CONTENT_ROOT_BASE` and the SQLite path; rewire `cmd/server` as the composition root; assemble the middleware chain in `router.go`; `/readyz` reports the §13.2 counters as JSON | `internal/config/`, `cmd/server/`, `internal/httpapi/router.go` | wave B |
| F5 | `semiplane admin create --username X` as a CLI subcommand, runnable via `docker exec`. **Not** a bootstrap env var — no secret sits in the environment | `cmd/server/admin.go` | wave B |

Wave A: F1, F2, F3 concurrently, disjoint. Wave B: F4 and F5 (F5 needs F2's `store.Open`).

**DoD:** §2.3. Plus: `make check` now runs `css` and `templ` before `build`, verified on a clean
CI runner; the built stylesheet is embedded, so `go build` works only after `make css` and the
Makefile says so in the failure message.

**Risk:** F3 changes the shape of the gate, so every later phase inherits it. Land F3 first and
merge it on its own PR before F1 and F2 land, so a toolchain problem is not diagnosed through
eleven unrelated files.

---

### P2 — Identity and tenancy · `phase/02-identity`

**Goal:** architecture §15.2. `users`, `auth_sessions`, `campaigns`, `campaign_members`; auth
middleware; campaign registration and `os.Root` creation.

Dependencies: P1 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| I1 | `internal/domain`: `User`, `Campaign`, `Membership`, `Role` (`gm` \| `player` — a clean two-value enum, no ambiguous middle), `Visibility` (`private` \| `public`), and access-tier resolution (instance admin / campaign GM / campaign player / anonymous). **Delete `internal/domain/.gitkeep`.** | `internal/domain/**` | wave A |
| I2 | Store: the four migrations, plus queries. `auth_sessions` keys on `token_hash`, never the token. `campaigns.content_root` is an absolute path; `campaigns.system_id` and `ruleset_version` are added here so no later phase needs an `ALTER` (architecture §10.7) | `internal/store/**` | wave A |
| I3 | Auth primitives: password hashing (and a constant-time comparison test), session token minting and expiry, the session cookie, `sp_ui` read/write with exactly two fields — `theme` and `ui` — `SameSite=Lax; Path=/; Max-Age=31536000`, not HttpOnly, no sensitive data, **no density field** | `internal/httpapi/auth/**` | wave A |
| **I4** | **Integrator.** Auth middleware; campaign registration (slug uniqueness, `os.Root` created per campaign at registration, `campaign_members` seeded); the §8 access-control matrix enforced in one place; login and campaign-list routes | `cmd/server/`, `internal/httpapi/router.go`, `internal/httpapi/campaigns/**` | wave B |
| I5 | Auth-facing shell routes and empty/error/degraded states for the login and campaign-list screens, using the fixtures P5 will replace | `internal/web/**` | wave B |

Wave A: I1, I2, I3 concurrently. Wave B: I4 and I5.

**DoD:** §2.3. Plus the §14 role-enforcement row: a `player` `PUT` is 403, an anonymous `PUT` is
401 or 404. Plus the UI §1.2 grep test — neither "world" nor "session" appears in a rendered page.

**Note:** I5 builds shell markup against fixtures, ahead of P5. That is deliberate and matches
UI §15.4 — the accessibility contract is proven before the pipeline lands. It touches
`internal/web/`, which is also P5's surface, so **P5 must rebase onto P2's `main` before its
first child branch is cut**, and the two phases must not be worked concurrently.

---

### P3 — Content read path · `phase/03-content-read`

**Goal:** architecture §15.3. `os.Root` confinement; front matter and `kind` discrimination;
goldmark render and sanitise; wikilink and embed extensions; the render cache; `ETag`.

Dependencies: P2 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| C1 | `os.Root` confinement per campaign, created at registration. **Not** `filepath.Clean` plus a prefix check. Applies to writes and to every path in front matter | `internal/content/root.go` | wave A |
| C2 | Front matter parsing with a YAML parser that has alias-expansion limits, or a document-size cap — front matter is attacker-reachable. `kind` discrimination against the **registry-backed** kind set: a page with no `kind` is prose, an unknown `kind` degrades to prose. The `pages` and `pages_fts` schema | `internal/content/frontmatter.go`, `internal/domain/**`, `internal/store/**` | wave A |
| C3 | goldmark render: GFM + Footnote + Typographer, `WithAutoHeadingID`; the `[[wikilink]]`, `![[embed]]`, `{{statblock}}`, `{{dice}}` extensions. **Never `html.WithUnsafe()`** — with a public tier and Obsidian sync as an input, this is a security boundary, not a style choice | `internal/content/render.go`, `internal/content/ext/**` | wave B |
| C4 | Link resolution, Obsidian-compatible: relative path, then basename index within the campaign, then within campaigns the viewer can see — **never probe invisible ones**. Unresolved links recorded at index time for a broken-link report. **Per D12:** a cross-campaign link renders as a plain hyperlink with the literal wikilink text as its label, no lookup and no inlining, so the linking page's HTML is byte-identical for every viewer; cross-campaign **embed** is refused | `internal/content/links.go` | wave B |
| C5 | Render cache keyed `(campaign_id, path, content_hash, include_secrets)`; validity by **content hash, not mtime** (sync paths coarsen mtime, and 1-second granularity lets distinct writes collide); the **salted** `ETag`; `Cache-Control: private, no-store` on every `include_secrets=true` response | `internal/content/cache.go` | wave C |
| **C6** | **Integrator.** The `/c/{slug}/wiki/{path...}` handler; redaction ordered **before** sanitise and before the value ever reaches a template, so no intermediate buffer holds an unredacted copy for a non-GM | `internal/httpapi/router.go`, `internal/httpapi/wiki/**` | wave C |

Wave A: C1, C2. Wave B: C3, C4. Wave C: C5, C6.

**DoD:** §2.3. Plus §14: golden-file markdown rendering with raw HTML in the input stripped;
and the cache-partitioning security test — a private page body must never appear in a public
response.

---

### P4 — Watcher and index · `phase/04-watcher`

**Goal:** architecture §15.4. Directory watches, debounce plus size-stable confirmation, FTS
maintenance, degraded mode, the `campaign_state` schema.

Dependencies: P3 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| W1 | One `fsnotify.Watcher` for all campaigns, routing events by path prefix. **Watch directories, never files** — fsnotify stops watching a removed file, and an atomic-write rename cycle is exactly that. Symlinks in the tree are **rejected by default**: fsnotify's own docs call the behaviour "uncertain and definitely undocumented" | `internal/content/watch.go` | wave A |
| W2 | Partial-write safety: per-path debounce with the timer reset on each event, then size-stable confirmation — re-`stat`, require unchanged size across two samples. Ignore dotfiles and `.tmp` / `.swp` / `~`. Time-debounce alone is documented as unreliable | `internal/content/debounce.go` | wave A |
| W3 | FTS maintenance: upsert and delete rows, Obsidian-style rename / delete / undelete convergence. External-content tables cannot be `ALTER`ed, so bulk changes go through `INSERT INTO pages_fts(pages_fts) VALUES('rebuild')`. Every query joins `campaigns` on visibility — a bare query returns private titles to anonymous users | `internal/store/**`, `internal/content/index.go` | wave A |
| **W4** | **Integrator.** Degraded mode and the §5.3 failure table: watch-limit exhaustion (`Add` → ENOSPC/EMFILE) logs an **error**, never a debug line, and falls back to periodic full rescan; a root missing at boot marks the campaign `degraded` and the server still starts; a slow-timer tree re-verify catches watches bound to an inode rather than a name. The `campaign_state` schema: exactly one row per campaign | `internal/httpapi/router.go`, `internal/store/**` | wave B |
| W5 | The §13.2 observability events for this subsystem: `watch.add_failed`, `watch.degraded`, `watch.recovered`, `watch.rescan_fallback`, `content.stable_read_timeout`, `content.render_error`. Every event carries `campaign_id` and, where applicable, `path`; **no event carries secret content, file contents, or dice results** | `internal/observability/**` | wave B |

Wave A: W1, W2, W3. Wave B: W4, W5.

**DoD:** §2.3. Plus §14: burst-write a file and assert the cache never holds a truncated parse;
atomic-write a page and assert it still re-renders; rename, delete and undelete a page and
assert the index converges; anonymous search returns no private rows.

---

### P5 — The shell (UI Slice A) · `phase/05-shell`

**Goal:** UI spec §15.3. The five structural components, five width tiers plus TV mode, the
token layer, the primitive library, and the structural accessibility gate. Built against
**fixture HTML** — no route may hard-code its own three-column layout.

Dependencies: P0, P1, P2 merged. Branches from `main` after P2 (see the note under P2). Must
merge before P6.

This is the least parallel phase, and saying so is more useful than pretending otherwise:
every component draws from one token file and one grid, so the *writing* parallelises and the
*merging* does not. Five agents, strict file ownership, fixed merge order.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| S1 | Token layer: three layers (primitives, semantic, component), `oklch` primitives, both theme blocks, scale multipliers, callout and brand tokens; Tailwind v4 CSS-first `@theme`; `forced-colors` and `prefers-contrast: more` and `prefers-reduced-motion: reduce` fallbacks. `tokens_contrast_test.go` parses the token file and asserts every pair against its floor, asserts no token is missing from a theme, and re-runs the whole set with the fixture campaign theme applied | `internal/web/static/css/tokens.css`, `tokens_contrast_test.go` | wave A |
| S2 | Shell grid — `grid-template-areas`, `minmax(0, 1fr)` on the centre track (a plain `1fr` blows out to `min-content` on a long unbroken string in a heading), the two-column pre-campaign variant, the below-640px bottom sheets re-parented on resize rather than re-rendered. **The inline head resolver**: one blocking inline script before first paint resolving `data-theme` and `data-ui` from `sp_ui`, then `?ui=`, then UA hints, then media queries. Consequence: the document never varies by `sp_ui`, so **no `Vary: Cookie`** and the content-hash invalidation signal survives | `internal/web/static/css/shell.css`, `internal/web/static/js/head.js`, `internal/httpapi/shell/**` | wave A |
| S3 | Chrome: header (`role="banner"`, 56px), footer (`role="contentinfo"`, 32px), left nav (`role="navigation"`, `aria-label="Campaign"` — persistent ≥1024, icon rail in short landscape, sheet below; absent pre-campaign; **no Games list**, there is one Table per campaign), right rail (`role="complementary"`, `aria-label="Utilities"` — persistent ≥1280, drawer 640–1279, bottom sheet below 640) | `internal/web/components/chrome/*.templ` | wave B |
| S4 | Primitives and states: dialog, menu-button, tabs, disclosure, skip link, the target-size utility; the eleven empty / error / degraded states; the table component with its stacked form. Every element under test gets `data-testid` | `internal/web/components/ui/*.templ` | wave B |
| **S5** | **Integrator.** TV mode (a mode, not a width band: layout, scale, focus rules, home tile grid, non-typing equivalents); the structural a11y gate and target-size audit as `make a11y`; the agent-assisted sweeps (viewport and orientation, keyboard-only, D-pad, media-query matrix) with **every finding converted into a Go test**; merge order S1 → S2 → S3/S4 → S5 | `internal/web/static/css/tv.css`, `internal/web/a11y_test.go`, `Makefile`, `internal/httpapi/router.go` | wave C |

Wave A: S1, S2. Wave B: S3, S4. Wave C: S5.

**DoD:** §2.3. Plus UI §10.1 and §10.2, which are gate-blocking: contrast, exactly one `<h1>`,
landmarks present and distinctly labelled, no positive `tabindex`, every skip link first in tab
order, no horizontal scrollbar at 320px, both sheets leaving ≥40px of context. The served
document contains neither `data-theme` nor `data-ui` before script execution.

**ADRs:** TV mode as a mode rather than a width band; the no-`Vary: Cookie` result; the
one-blocking-inline-script rule as the only JavaScript on the critical path.

---

### P6 — Wiki surface · `phase/06-wiki-surface`

**Goal:** architecture §15.5. templ routes filling the shell's slots; the `If-Match` write path;
the conflict view; change notification; search; assets with range requests.

Dependencies: P3, P4, P5 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| W1 | `/c/{slug}/wiki/{path...}` — fills the centre slot; `ETag` revalidation; per §6.6 cache directives | `internal/httpapi/wiki/**` | wave A |
| W2 | Search: `GET /c/{slug}/search?q=` over FTS, joining visibility. **Submit-to-navigate over ordinary HTTP**, not type-ahead — content delivery is confined to two SSE uses and a client-side renderer could not reproduce the server's FTS tokenisation. The count lives in the `<h1>` ("12 results for *goblin*"). **No live region on this route**; the response carries an `ETag` | `internal/httpapi/search/**` | wave A |
| W3 | Assets: `/c/{slug}/assets/{path...}` with `Range` support, visibility-gated. Assets inherit campaign visibility; a cache entry is **never** shared across campaigns | `internal/httpapi/assets/**` | wave A |
| W4 | SSE change-notification: `/c/{slug}/events` for the editor's external-change notice. Content is never delivered over Datastar — it ships as ordinary HTTP with `ETag` | `internal/httpapi/events/**` | wave A |
| W5 | Editor write path: `GET` then `PUT … If-Match: "H1"`. Match → append `page_revisions` (`source='web'`), write temp in the same directory, `fsync`, `os.Rename`, return the new `ETag`. Mismatch → **412 with the current content and its hash, recording nothing**. **No silent overwrite, no last-write-wins fallback** — a file lock cannot help, because Obsidian is a separate process unaware of semiplane | `internal/httpapi/edit/**` | wave B |
| W6 | Conflict UI: side-by-side, the user's buffer from H1 against the current disk H2, per-hunk accept / reject, then save with `If-Match: "H2"`. Not a 3-way merge in v1 — `page_revisions` holds the common ancestor, so a real merge is additive later rather than a rewrite. The 412 lives in the **preview slot**, and it is `role="alert"`, `aria-live="assertive"` | `internal/web/components/edit/*.templ` | wave B |
| W7 | `/c/{slug}/theme.css` and the campaign manifest. UI §13 constraint 2: this route must exist **before** any campaign manifest is accepted, or registration would reference a 404 stylesheet. The brand pair is validated server-side and fails toward the default | `internal/httpapi/theme/**`, `internal/web/components/theme/**` | wave B |

Wave A: W1, W2, W3, W4 — four read paths, fully disjoint. Wave B: W5, W6, W7.

**DoD:** §2.3. Plus §14: a stale `If-Match` gives 412, the disk is unchanged, and no revision
row is written.

---

### P7 — Realtime plane · `phase/07-realtime`

**Goal:** architecture §15.6. In-memory `campaign_state`; the hub; the protocol codec;
placements versus definitions; GM-only operations; debounced persistence; resume by version.

Dependencies: P6 merged.

**Design decision that keeps this phase independently runnable:** intent resolution belongs to a
gameplay `System`, which is P8. Rather than stub it or reorder the phases, P7 registers an
inert `core` system in the registry — it parses and version-checks intents and resolves nothing
— so the hub, the monotonic version, the persistence cadence and the protocol are all testable
end to end against a real client. P8 replaces it with 5e. Record this as an ADR.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| R1 | In-memory `campaign_state`: exactly one per campaign, monotonic `version`, trailing-debounce write (~2s after the last mutation) and a write on shutdown. A table produces a handful of writes per minute instead of hundreds per second, which is the main reason SQLite is adequate here. Crash floor: the last debounced state | `internal/realtime/state.go` | wave A |
| R2 | Protocol codec: `hello` / `intent` / `presence` in, `snapshot` / `applied` / `rejected` / `delta` / `presence` / `clock` out. `seq` is the client sequence for optimistic reconcile; **`version` is the server's monotonic version and the ordering authority**. Reconnect sends `since` for a delta instead of a full snapshot | `internal/realtime/protocol.go` | wave A |
| **R3** | **Integrator.** Hub over `coder/websocket`: auth parity with HTTP, explicit `Origin` enforcement on upgrade, `ws.connected` / `ws.closed` / `ws.stale` gauges where `ws.stale` fires when a peer stops reading. `roll` is evaluated **server-side**; the client never supplies a result. Per-placement version, not global. GM-only ops (`pause`, `set_hp` on another token, secret reveal) enforced server-side — client-side hiding is cosmetic | `internal/realtime/hub.go`, `internal/httpapi/router.go`, `internal/httpapi/ws/**` | wave B |
| R4 | `/c/{slug}/play` route shell: templ renders the shell and the sidebar, PixiJS owns one `<div>` and everything inside it, and Datastar patches the DOM **around** the canvas. **No SSE for map state** — §7's "no SSE on the VTT page" is read as exactly that. **No `role="application"`** | `internal/httpapi/play/**`, `internal/web/components/play/*.templ` | wave B |
| R5 | Chat as an in-memory ring buffer per campaign, lost on restart, plus the explicit "export to journal" action writing a `kind: journal` markdown page — which creates a `page_revisions` row and is therefore recoverable and Obsidian-editable. Session exports exclude secret content by default | `internal/realtime/chat.go` | wave B |
| R6 | **Ruleset-version gating (D17, D18).** The fingerprint is system ID + the system's `RulesetVersion()` + base and overlay pack versions; **house-rule enablement is excluded**, so enabling a house rule mid-game does not strand the campaign. On mismatch: refuse to resume, name both versions and the consequence on `/c/{slug}/status`, show a GM alert on `/play`, and offer an explicit GM-only, confirmation-gated discard of persisted state that writes to `audit_log`. Plus the `state.write_ms` histogram | `internal/observability/**`, `internal/realtime/**` | wave B |

Wave A: R1, R2. Wave B: R3–R6.

**DoD:** §2.3. Plus §14: two clients issuing concurrent intents converge with monotonic versions;
killing the process mid-game resumes from the last debounce. Plus UI §7.5: the play page opens
**exactly one** WebSocket and **exactly one** SSE.

---

### P8 — Plugins and first systems · `phase/08-plugins`

**Goal:** architecture §15.7, §15.8 and §15.9 merged. The plugin contracts, the 5e engine and
data packs, the 2014 and 2024 overlays, two reference UI plugins, and house rules.

Dependencies: P7 merged.

**This is the most serialised phase, and the reason is the contract.** Everything downstream is
typed against `System`, `Intent` and `Mutation`, so the interfaces land first and alone. The
fan-out is in the middle, where three implementations are genuinely independent.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| P1a | `internal/domain/rules`: `System`, `Intent`, `Mutation`, `Expr`, `Grammar`, `Query`, `View`, `Kind`, `Context`. `Context` carries the seeded RNG, campaign identity, actor identity and actor role — **never a wall clock, never an ambient random source**. `Apply` **returns** mutations rather than applying in place, so no plugin can write state or forge a broadcast | `internal/domain/rules/**` | wave A, alone |
| P1b | `internal/plugin` registry + `internal/web/plugins` UI registration + `internal/domain/systems` shell. **No `init()`** — registration is explicit in the composition root, because `init()` hides ordering and leaks state between tests, both of which become real problems once rules packs are versioned. Kind ownership per §10.2.1: semiplane owns `journal`, `handout`, `index`, `token`, `scene`; a gameplay plugin declares only rules-content kinds via `ContentKinds()` | `internal/plugin/**`, `internal/web/plugins/**` | wave B |
| P1c | The published plugin conformance suite every `System` must pass: determinism, unknown-`kind` tolerance, version / migration refusal, GM-only enforcement, `Apply` panic containment with `campaign_state` byte-identical to before. **A system sharing nothing with 5e must pass it without touching `internal/domain/rules`** — that is the test that proves the plugin system generalises | `internal/domain/rules/conformance/**` | wave B |
| P1d | The determinism lint rule and its tests: rule code may not call `time.Now`, range over a map, or call `crypto/rand` directly. Enforced by lint and tests, because there is no sandbox — plugins are compiled in. Plus the `campaign_rule_modules` migration and store, and the house-rule scope check: a module that reorders resolution or introduces nondeterminism is rejected | `.golangci.yml` (via F4), `internal/store/**`, `internal/domain/rules/determinism/**` | wave B |
| P2a | 5e engine + `go:embed` base pack + resolver hooks for genuinely procedural rules only. The engine is **shared 5e mechanics**, not "the rules engine" — a system sharing nothing with 5e does not use it | `internal/domain/systems/dnd5e/**` | wave C |
| P2b | The 2014 and 2024 overlays. They are ~90% identical, so they are one agent: the differences should show up as reviewable **data** diffs (race naming, crit on 20 only, no mastery properties / species, mastery properties, crit on any natural 20), with Go only where data genuinely cannot express it | `internal/domain/systems/dnd5e/overlays/**` | wave D |
| P2c | Two reference UI plugins proving both contracts are usable: the **link preview** (a render hook on external links; the server-side unfurl helper is read-only and must not become a path to private campaign content) and the **graphical dice roller** (renders a die widget; on send emits `{"op":"roll"}` and renders the **server's** result — a client-side roll is unverifiable and would break the audit trail) | `internal/web/plugins/**` | wave D |
| P2d | House rules: modules applied at campaign load to produce an *effective* ruleset — `system ID → base pack → overlay → enabled modules in declared order → effective ruleset (+ ruleset_version)`. Conflicts resolve **first-match-wins by `position`**, logged with both module IDs, never last-write-wins. **Data-level only**: a module may toggle `flanking_optional`, change a DC constant, or disable a condition; it may not reorder resolution or introduce nondeterminism | `internal/domain/rules/houserules/**` | wave D |
| **P4** | **Integrator.** Wire the 5e system into the composition root, replacing the inert `core` system from P7. `Apply` panic containment at the boundary: `recover()`, return an error, and **state is not mutated** because mutations are returned, never applied in place | `cmd/server/`, `internal/httpapi/router.go` | wave D |

Waves: A {P1a} → B {P1b, P1c, P1d} → C {P2a} → D {P2b, P2c, P2d, P4}.

**DoD:** §2.3. Plus §14: the conformance suite green for both overlays; a campaign with an
unknown `system_id` refuses to start a game and **still serves the wiki 200**; a page with a
`kind` no plugin registers degrades to prose with content intact; a UI plugin attempting to
mutate state cannot, and no write path is reachable; a panic inside `Apply` leaves
`campaign_state` byte-identical to before.

---

### P9 — Client · `phase/09-client`

**Goal:** architecture §15.10 plus UI §4.9. The map canvas, the live chrome, the phone player
surface, and the theme layer served from content.

Dependencies: P8 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| C1 | PixiJS v8 map canvas: placements, fog, initiative. The canvas is `aria-hidden="true"` and a **decorative mirror**. Camera zoom is expressed in **world coordinate units, never pixels** — the client fits the world bounds to the viewport and preserves the world centre across resize and orientation change, or the map re-frames on every breakpoint crossing, which at the 14" target happens constantly. A map contrast overlay is first-class: `--map-dim` on the map layer, 3px `--map-outline` on every placement and grid line, because much VTT artwork is low-contrast by design. **Per D13:** PixiJS and Datastar are committed under `internal/web/static/vendor/`, integrity-pinned in `tools/vendor.json`; `make vendor` verifies, `make vendor-check` gates | `internal/web/static/js/map/**`, `internal/web/static/vendor/**`, `tools/vendor.json` | wave A |
| C2 | The token list — a `<ul>` of `<button>` elements, one per placement, naming it, its HP, its conditions and its layer. **This is the accessibility source of truth, not the canvas.** On TV it is the primary way to change what the map shows, because dragging is unavailable; on a phone the map is a readout and the list is the only representation. `ArrowUp`/`ArrowDown` move focus between placements. **Per D16:** the default rail tab is `Initiative` on laptop and `Tokens` on TV and phone, remembered per campaign in `localStorage`, applied client-side from `data-ui` so the document never varies by device | `internal/web/components/play/**`, `internal/web/static/js/tokens/**` | wave A |
| C3 | Datastar live chrome over `/c/{slug}/events`: initiative tracker, chat log, dice log, connection and degraded notices, patched as **rendered DOM fragments**. One hub, two egress representations of the same state. Patches must never touch the focused element; insertions into a live region are throttled to 1/second; the chat and dice logs must **not** replay history into a live region on connect | `internal/web/static/js/live/**`, `internal/httpapi/events/**` | wave B |
| C4 | Phone player surface and action bar; the 320 × 580 budget. Read-only map, no positional play | `internal/web/components/play/**` | wave B |
| C5 | Theme layer: the campaign manifest consumed server-side, brand tokens, overridable-versus-not enforcement. Per-campaign, not per-user — per-user would reintroduce the cookie-varying-document problem | `internal/web/static/css/theme/**`, `internal/httpapi/theme/**` | wave A |
| C6 | Committed browser end-to-end tests for the play page. Anything found through the Playwright MCP that must persist belongs in a committed test, not in an MCP session | `internal/web/e2e/**` | wave B |

Wave A: C1, C2, C5. Wave B: C3, C4, C6.

**DoD:** §2.3. Plus UI §7.6 and §7.5: the play page opens exactly one WS and one SSE; no
`role="application"` anywhere; the token list is operable by keyboard alone; the map survives a
resize without re-framing.

**Open item, resolved:** PixiJS vendoring is **D13** — committed, integrity-pinned, served from
the app's own origin. `make vendor` re-fetches and verifies; `make vendor-check` re-hashes the
committed files and fails on a mismatch, so a swapped blob is caught in CI rather than trusted.

---

### P10 — Secret callouts · `phase/10-secrets`

**Goal:** architecture §5.6. The `[!secret]` extension, the two-variant render, the salted cache
key, the GM-only reveal endpoint with `If-Match`, the `secrets_revealed` ledger, bounded sync
reconciliation, and search exclusion.

Dependencies: P2 (access control), P4 (watcher), P5 (shell), P6 (conflict resolution), P8.
This phase comes late because it refines an already-working render pipeline and needs all of the
above underneath it.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| S1 | The `[!secret]` goldmark extension. `-` is a secret, collapsed and GM-only; `+` is revealed and public. The single character is the whole edit, which keeps the diff to one byte and minimises the collision window with Obsidian's own line handling. Unknown callout types render as generic callouts, so `secret` does not need an Obsidian plugin to look right | `internal/content/ext/secret.go` | wave A |
| S2 | The `secrets_revealed` ledger and the audit trail. The **file** is authoritative for whether a secret is currently revealed; the table is authoritative for **who revealed it and when**, and for re-applying a reveal that sync reverted. `reverted_count` makes the sync fight visible rather than mysterious. The primary key is `(campaign_id, path, anchor)` | `internal/store/**`, `internal/domain/**` | wave A |
| S3 | Anchor identity: an Obsidian block reference (`^block-id`) when present, else `sha256(campaign_id, path, ordinal, first-line-of-body)[:12]`. The derived form is best-effort and self-healing — when a ledger row's anchor no longer resolves, re-associate by `ordinal`, carry the revealed state across, and log `secret_anchor_drift`. A page rename re-keys on `(campaign_id, new_path, anchor)` and leaves a tombstone. **It never silently drops a reveal** | `internal/content/anchor.go` | wave B |
| S4 | FTS exclusion: `body_plain` excludes callout content outright **in every reveal state**, so a search snippet can never carry secret text to a non-GM. The tradeoff is that secrets are not full-text searchable; a secret-aware index is an additive later change | `internal/content/index.go`, `internal/store/**` | wave B |
| S5 | Two-variant render: redaction is **omission, not hiding** — the callout is removed entirely for non-GM viewers. Not `display:none`, not a `hidden` attribute, not an inline comment, not a CSS class: every one of those ships the text in the HTML, where any player can recover it from view-source. Redaction happens **before** sanitise and before the value ever reaches a template. The opt-in per-campaign "render a redacted stub" is never the default | `internal/content/render.go`, `internal/content/cache.go` | wave C |
| S6 | `PUT /c/{slug}/secrets/{path...}` — GM-only, with the same authorization and `If-Match` precondition as any other write. Stale `If-Match` → 412, file unchanged, no ledger row. Every reveal is written to `audit_log` and to the ledger | `internal/httpapi/secrets/**` | wave C |
| S7 | Bounded sync reconciliation. On any change to a page: parse each callout's current marker, diff against the ledger for previously-revealed secrets that are now `-`, re-apply the `+` via the ordinary atomic write with `If-Match`; on conflict, back off and retry on the next event. **Capped at 3 passes per file per minute. On exhaustion, log `secret.reconcile_capped` as an error and leave the secret hidden.** An accidentally revealed secret is far worse than a late or dropped reveal, so every failure path here resolves toward hiding | `internal/content/reconcile.go` | wave D |
| S8 | The UI §4.10 disclosure surface: the GM reveal affordance (a control in the editor, distinct from the ordinary save path, because a disclosure is not a save); the player-facing redacted view and the prose gap "omit entirely" leaves mid-sentence; theming for a revealed callout distinct from a hidden one; and the accessibility decision — whether a redacted secret is announced to assistive technology at all, which risks disclosing its existence. Reveal and reconcile-capped are `role="alert"`, `aria-live="assertive"`. **Per D15:** the "Revealed" marker is on in the editor, off on the published page, a per-campaign setting | `internal/web/components/secret/**` | wave D |

Wave A: S1, S2. Wave B: S3, S4. Wave C: S5, S6. Wave D: S7, S8.

**DoD:** §2.3, and §14's secret rows, all of which are gating:

- GM response contains the secret body; player response does not; anonymous response does not;
  and **neither contains the secret text anywhere in the payload** — headers, comments, JSON
  payloads, or HTML source.
- A query matching only secret text returns nothing to any role; snippets never contain secret text.
- After a reveal, both cache variants invalidate on the new `content_hash`; a stale non-GM
  variant is never served. A GM and a player response never share an `ETag`.
- Simulate Obsidian reverting `+` → `-`: the watcher re-applies, counts the reversion, and
  **stops after the cap leaving it hidden**.

---

### P11 — Demo vault and tutorial content · `phase/11-demo-vault`

**Goal:** a committed vault that demonstrates every feature and teaches a new user the product,
shipped as a release artifact and seeded into a running instance with one command.

Dependencies: all of P0–P10 merged.

**Shape.** The vault is committed at `demo-vault/` and is the **source**. `make demo-artifact`
builds `semiplane-demo-v<version>.tar.gz` — the vault tree plus `demo.manifest.yml` — which the
release publishes beside the binary. The install guide downloads, extracts, and runs
`semiplane demo seed --root <dir>`, so a Docker-only user reaches a populated instance without
cloning anything. Nothing is embedded in the binary, and `make check` never touches the artifact
or the network.

**The measuring instrument lands before the thing it measures.** `make demo-check` (V6) and the
seed (V3) are built first, then the vault is authored against them. Authoring first and writing
the gate afterwards is how coverage checklists end up describing a vault that no longer exists.

**Waves:**

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| V3 | `demo.manifest.yml` schema + `semiplane demo seed` / `demo reset`. Users, campaigns, memberships, rule modules, and seeded `campaign_state`. `ruleset_version` resolved through the real house-rule path, never hardcoded. Refuses on binary/artifact version mismatch and on a second seed; `reset` deletes the three campaigns and their state and leaves the vault untouched. **Per D14:** a random password printed once, `--password` to override, and the demo GM is campaign-GM only — never an instance admin | `demo.manifest.yml`, `cmd/server/demo/**`, `internal/demo/**` | wave A |
| V6 | `make demo-check`: reachability from each campaign index, registry-backed kind coverage, render-extension coverage, both `[!secret]` states, unresolved-link budget equal to the deliberate breakage, asset coverage. Ships with its own `testdata` fixture so its tests are meaningful before the real vault exists | `scripts/check-demo.sh`, `internal/demo/**_test.go`, `Makefile` | wave A |
| V1 | `demo-vault/greyhaven/**` — the showcase. The index page introduces one feature per page in reading order and the prose does the teaching: start here → prose and wikilinks → secrets (one hidden, one revealed) → embeds → game objects (`token`, `scene`, `handout`, `journal`, `index`) → rules content (`spell`, `class`, `feat`, `creature`, `ancestry`) → statblocks and dice, with the note that dice are rolled server-side → the live table, noting there is no session in the URL → house rules, active and data-not-code → this campaign's theme → the cross-campaign link → where the boundaries are. Includes the theme manifest, the deliberately broken link, a seeded journal page shaped like a chat export with secret content excluded, and **per D19** a hand-authored SVG map declaring a world extent several times the largest viewport | `demo-vault/greyhaven/**` | wave B |
| V2 | `demo-vault/public-post/**` and `demo-vault/forgotten-realm/**` — the two boundary campaigns. Each declares its demo intent in the manifest; `demo-check` asserts coverage per declared intent, not uniformly across all three | `demo-vault/public-post/**`, `demo-vault/forgotten-realm/**` | wave B |
| V4 | `make demo-artifact`, the release publish step, and the install-guide walkthrough: download, extract, seed, open, reset — plus the README cross-reference | `Makefile`, `.github/workflows/**`, `docs/content/en/install/**`, `README.md` | wave C |
| **V8** | **Integrator.** The committed browser suite driving the demo vault: rendered pages for each kind, the two secret states as GM and as player, anonymous read on `public-post`, and `forgotten-realm` refusing the game while its wiki still returns 200. Anything found through the Playwright MCP that must persist belongs here, per UI §10.9 | `internal/web/e2e/**` | wave D |

Wave A: V3, V6 — disjoint. Wave B: V1, V2 — disjoint trees. Wave C: V4. Wave D: V8.

**DoD:** §2.3. Plus: `make demo-check` green; `make demo-artifact` produces a reproducible
tarball (same input, byte-identical output — sort the archive); a fresh instance reaches a
populated `greyhaven` map in one command; a second seed is refused with a message naming both
versions; `demo reset` restores the starting state; and the install guide's commands are the
ones that were actually run to produce it.

**ADRs:** the artifact-versus-embedded decision and its version-skew cost; completeness as a
registry-derived gate rather than a hand-written checklist; the self-teaching index as the
tutorial, with the gate carrying the weight a walkthrough page would otherwise carry.

---

### P12 — Hardening · `phase/12-hardening`

**Goal:** architecture §14 in full, plus UI §10. This phase mostly writes tests against code that
already exists, so it parallelises well.

Dependencies: P11 merged.

| ID | Work item | Owns | Parallel |
|---|---|---|---|
| H1 | **The secret-redaction test.** The one that gates the release. It must fail loudly if a non-GM response ever contains secret text. Written as a table over {GM, player, anonymous} × {HTML body, headers, comments, JSON payload} | `internal/httpapi/secrets/redaction_test.go` | wave A |
| H2 | Remaining security tests: cache partitioning, FTS visibility, role enforcement, path traversal via `os.Root`, YAML bomb in front matter, WebSocket `Origin` enforcement and auth parity, stored XSS (raw HTML in content is stripped) | `internal/**/**_test.go` | wave A |
| H3 | Race coverage across the hub, the cache and the watcher; `go test -race -count=1` clean on the whole tree | `internal/realtime/**`, `internal/content/**` | wave A |
| H4 | Failure-mode tests from §13: content root missing, inotify exhaustion, mid-write read, stale editor save, mass WebSocket disconnect, SQLite busy, crash mid-game, cache miss (re-render synchronously; the cache is never a dependency) | `internal/**/**_test.go` | wave A |
| H5 | Observability assertions: every §13.2 event is emitted by its trigger, and **no event payload contains secret or file content** | `internal/observability/**_test.go` | wave A |
| H6 | Rule determinism: a property test that the same `(state, intent, seed)` produces identical mutations across 100 runs; ruleset drift refuses to resume rather than silently re-resolving; view rendering for an empty, a typical and a maximal `Derive` output, with unknown fields ignored rather than fatal | `internal/domain/**_test.go` | wave A |
| H7 | Convert every finding from the P5 and P9 agent-assisted sweeps into a committed check, per UI §10.9: a finding that is not converted is a finding that comes back | `internal/web/**_test.go`, `scripts/**` | wave A |
| **H8** | **Integrator.** Documentation truth-up: `docs/content/en/roadmap.md` (mark phases landed), `install/`, `operating/`, `operating/security/`, `README.md` (remove the pre-release banner once the wiki surface and realtime plane are usable), and the "Known staleness" list in `design/_index.md` | `docs/**`, `README.md` | wave A |

Wave A is eight agents. Ownership is by test domain; H1, H3 and H8 additionally own files that
others must not touch, and H1 is merged first.

**DoD:** §2.3 in full, plus the release Definition of Done in §2.3.

---

## 5. Validation map

Which phase claims which row. **P12** audits this table rather than re-deriving it, so a row that
was never claimed is visible as an empty cell.

| Validation area | Phase |
|---|---|
| Markdown rendering golden files; raw HTML stripped | P3 |
| Cache partitioning (private body never in a public response) | P3 |
| Partial-write safety; watch loss on rename | P4 |
| FTS visibility; FTS lifecycle through rename/delete/undelete | P4 |
| Role enforcement (`player` 403, `anonymous` 401/404) | P2 |
| Conflict detection (stale `If-Match` → 412, disk unchanged, no revision) | P6 |
| Realtime convergence and monotonic versions | P7 |
| Persistence (kill mid-game → resume from last debounce) | P7 |
| Concurrency (`-race` across hub, cache, watcher) | P12 |
| Plugin isolation (panic in `Apply`, state byte-identical) | P8 |
| Plugin authority (a UI plugin cannot mutate state) | P8 |
| Unknown `kind` degrades to prose | P8 |
| Missing plugin (game refused, wiki still 200) | P8 |
| 2014 vs 2024 parity against the shared property suite | P8 |
| Plugin conformance suite | P8 |
| View rendering (empty / typical / maximal `Derive`) | P12 |
| Rule determinism property test | P12 |
| Ruleset drift refuses to resume | P12 |
| **Secret redaction** — gates the release | **P10, H1** |
| Secret reveal round trip; secret sync conflict; secret concurrency; secret search; secret + cache | P10 |
| Observability events emitted, no payload leaks | P12 |
| Contrast (UI §10.1) | P5 |
| Structural a11y and target size (UI §10.2, §10.6) | P5 |
| Viewport, keyboard-only, D-pad, media-query sweeps (UI §10.3–§10.7) | P5, P9, H7 |
| Route-specific checks (UI §10.8) | P6, P7, P10 |
| Demo coverage: reachability, kind coverage, extension coverage, both secret states, link budget | **P11** |
| Demo artifact reproducibility; version-skew refusal; double-seed refusal; `demo reset` | **P11** |
| End-to-end walk of the demo vault as GM, player and anonymous | **P11 (V8)** |
| Gate (`make ci`) | every phase |

---

## 6. Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| `go.sum` contention stalls a phase | high without a rule, near zero with one | Integrator owns `go.mod`/`go.sum` and commits every pinned dependency for the phase in its first commit (§2.2.3) |
| P5's merge surface is one stylesheet and one component tree | high | Strict file ownership per S1–S4, fixed merge order, five agents not seven |
| P8 serialises on the contract and cannot be parallelised further | certain | Accepted, and stated rather than hidden. Three waves of genuine fan-out around one unavoidable serialisation point |
| P5 and P2 both touch `internal/web/` | certain if worked concurrently | P5 rebases onto P2's `main` before its first child branch is cut; the phases are not worked at the same time |
| The design records stay internally inconsistent after the two corrections in §1.1 | medium | Neither record is edited. The corrections are ADRs, listed in the "Known staleness" section that already exists for this purpose |
| A phase merges green and leaves `main` unbootable | low | DoD item 10: the server starts and answers `/healthz` |
| The gate changes shape in P1 and every later phase inherits it | medium | Land P3-equivalent of the toolchain (F3) on its own PR first, before F1 and F2 |
| `make clean` removes `docs/assets/plans/`, which is gitignored and re-staged by `make site-plans` | low | Pre-existing and self-correcting; `site-check` depends on `site-plans`. No action |
| The demo vault drifts from the product as features land | high without a gate, low with one | `make demo-check` derives its kind list from the plugin registry, so a new kind turns the demo red until it is demonstrated (D9). The gate lands *before* the vault, so the vault is authored against it |
| Someone "fixes" `forgotten-realm` by registering a Pathfinder plugin | medium — it looks like a bug | Recorded in `AGENTS.md` as an intentional degraded demo. It is the only demonstration of §10.8 |
| Artifact and binary versions skew, and the seed fails in a way the user cannot diagnose | medium | The manifest carries the binary version; the seed refuses with a message naming both, and `demo reset` returns the instance to a known state. The install guide pins the two to the same release |
| The seed writes a `ruleset_version` that does not match the resolved ruleset, so the game refuses to resume | medium, and it looks like a broken demo | D11: resolve it through the real house-rule path. A refusal here is correct behaviour, and it is the same §10.8 case the third campaign already demonstrates |
| The demo vault rots because nobody maintains it | medium | It is gate-blocking rather than advisory, and `demo-check` fails loudly in CI. A demo allowed to rot is worse than no demo |
| D12 is implemented as an inline, "since the target resolves anyway" | medium — inlining is the intuitive implementation | The linking page's HTML must be byte-identical for GM, player and anonymous. Assert it in P3: render one page with a cross-campaign link as all three roles and diff the bodies |
| A later plugin adds house-rule enablement back into the `ruleset_version` fingerprint (D17), because §10.5's chain reads as though it belongs there | medium | Record D17 in `AGENTS.md` beside the demo rules. Consequence is a GM being refused on their own campaign after toggling a house rule, which reads as a bug and will be "fixed" the wrong way |
| The `localStorage` rail-tab memory (D16) is unavailable or disabled, and the play page renders no rail tab at all | low | Fall back to the form-factor default silently, and assert the fallback in the e2e suite rather than only the remembered path |

---

## 7. Deferred, not answered

**None blocking.** Every question the design records deferred is resolved in §1.2 and cited by
the phase work item that implements it:

| Deferred by | Question | Resolved as | Implemented by |
|---|---|---|---|
| architecture §16.1 | May an author write a cross-campaign wikilink? | D12 | P3 (C4) |
| architecture §16.5 | What is in the `ruleset_version` fingerprint? | D17 | P7 (R6), P8 (P2d) |
| architecture §16.5 | How does a GM recover from drift? | D18 | P7 (R6) |
| UI §14.1 | "Revealed" marker default on the published page? | D15 | P10 (S8) |
| UI §14.3 | Default `/play` rail tab per form factor? | D16 | P9 (C2) |
| this plan | How are PixiJS and Datastar delivered? | D13 | P9 (C1) |
| this plan | The demo account's credentials? | D14 | P11 (V3) |
| this plan | The demo's map asset? | D19 | P11 (V1) |

Three questions the design records raised remain genuinely unanswered and are **deliberately
not** answered here, because each needs evidence this project does not have yet:

1. **UI §14.2 — brand layer per-campaign or per-user.** §4.12.4 chose per-campaign; per-user
   would be nicer on a shared instance but reintroduces the cookie-varying-document problem.
   Settling it needs a real multi-user instance.
2. **UI §14.4 — left-rail scale past ten campaigns.** The rail assumes fewer; past that it needs
   grouping and a filter, and the filter is unusable on TV. Needs an instance with eleven
   campaigns.
3. **Architecture §16.2 — asset storage beyond local disk.** Maps are tens of MB; local disk with
   `Range` is the self-hosted answer, with a seam left for S3-compatible storage. Revisit only
   when someone reports a real limit.

Each is a tuning or product decision rather than an architectural one: none changes the data
model, the URL scheme, or a security boundary. They are recorded here so they are not
rediscovered from scratch.
