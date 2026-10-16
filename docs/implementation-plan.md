# Semiplane — Implementation Plan

*Status: Proposed · Last updated: 2026-10-16*

Goal: from the thin slice (`345fe00`: binary + SPA shell + CI + 3 smoke tests, all
green) to a playable v1 — wiki, GM→player reveals, dice with breakdowns, character
creator — built by multiple simultaneous agents without stepping on each other.

Ground rules (non-negotiable, from the planning docs):

- `main` stays green: every stream merges through PRs, `main-gate` required.
- The Makefile is the contract: new capabilities add `make` targets + tests first.
- Streams never depend on `fixtures/` (untracked, licensing-unresolved); only on
  `testdata/` seeds they own.
- Interfaces before implementations (tech-stack §9): `NoteRepository`,
  `SearchIndex`, `AuthProvider`, `DiceEvaluator` land as Go interfaces first so
  streams program against contracts, not each other's WIP.
- The rules-engine spike owns the ontology: `docs/rules-ontology.md` is canonical
  for `internal/events` + `internal/rules`; the spike prototype graduates, it is
  not re-derived.

## Version pins (research-verified 2026-10-16; re-verify with `go list -m` at build)

| Dep | Pin | Note |
|---|---|---|
| goldmark | `github.com/yuin/goldmark/v2 v2.1.6` (confirmed via module proxy 2026-10-16; v1 line is legacy) | parser/AST-transformer/renderer extension points |
| yaml | `gopkg.in/yaml.v3` | frontmatter (no goldmark-meta: no v2 port) |
| callouts | `github.com/yuin/goldmark-alert` (adapt) or hand-rolled `ParagraphTransformer` | `> [!note]` |
| sqlite | `modernc.org/sqlite v1.54.0` (+ matching `modernc.org/libc`) | driver `sqlite`; FTS5 compiled in — assert at startup via `sqlite_compile_options()` |
| fsnotify | `v1.9.0` | atomic-save = Rename+Create; Overflow → rescan |
| x/crypto, x/oauth2 | latest minor | Argon2id m=64MiB,t=3,p=4 (floor m=19MiB,t=2,p=1); PHC string storage |
| go-oidc | `github.com/coreos/go-oidc/v3 v3.15.0` | manual nonce check; S256 PKCE |
| participle | `github.com/alecthomas/participle/v2 v2.1.4` — verify via `go doc` at build | struct-tag dice grammar |
| expr | `github.com/expr-lang/expr v1.17.7` | struct Env (never map Env), `AsBool/AsInt`, patch out time |
| svelte / vite-plugin-svelte | `5.37.x` / `^5` | runes only; `svelte.config.js` + `vitePreprocess` mandatory (proven: svelte-check fails without it) |
| tailwind | `^4.1` via `@tailwindcss/vite` (static import — proven fine) | `@theme` tokens |
| tiptap | `@tiptap/{core,starter-kit,markdown}@^3.6` (official v3 pkg, not v2-era forks) | `getMarkdown()` round-trip; custom nodes need all three markdown hooks |
| playwright | `^1` (1.64.1 installed) | multi-context GM+player; `route.fulfill` fake OIDC |

## Phase 0 — Foundations (1 agent, ~1 day, unblocks everything)

1. `go get` the pins above; commit `go.mod`/`go.sum`.
2. `pkg/pluginapi/pluginapi.go`: `NoteRepository`, `SearchIndex`, `AuthProvider`,
   `DiceEvaluator` interfaces + capability negotiation stub (semver surface; empty
   impls live behind it later).
3. `internal/events/types.go`: closed ontology structs from `docs/rules-ontology.md`
   (spike deliverable) + `AdvDice` cancel helper. No deps.
4. `testdata/` growth: per-stream seeds (`vaults/e2e` ×3 notes now → ~10 with
   `:::gm`, wikilink pair, character page; `golden/` Obsidian fixture).
5. `openapi.yaml`: add planned `/api/v1` routes as stubs marked `x-slice: planned`
   so `make contract` keeps passing while the surface grows.

Gate: `make lint test build contract` green. Streams A–G start after this.

## Phase 1 — Parallel workstreams (up to 6 agents)

```
Phase 0 ─┬─► A content pipeline (markdown/render + golden)
         ├─► B storage core (migrate/index/vault/watch) ──► C auth/acl ──┐
         ├─► D dice+rules (grammar/roller/expr/hooks, graduates spike) ───┤
         ├─► E API surface (notes/search/dice/reveal/SSE/oplog) ─────────┤──► Phase 2
         └─► F web shell→wiki-read (api client, session, WikiReader) ─────┘
```

### A — Content pipeline (`internal/markdown`, `internal/render`)

Tasks (one agent, sequential): pipeline ctor → frontmatter transformer →
`[[wikilink]]` inline parser → `:::gm` block parser + role-strip transformer →
`ad-statblock`/`dice` fence renderer → callouts → role-aware `Render(doc, role)`
+ Obsidian golden fixture (GM vs Player HTML divergence). Acceptance: `make golden`
with committed `testdata/golden/*.md/*.html`; `html.WithUnsafe(false)`.

### B — Storage core (`internal/migrate`, `internal/index`, `internal/vault`, `internal/watch`)

Tasks (one agent, sequential): `schema.sql` (notes, FTS5 + triggers, backlinks,
sessions, tokens, lan_codes, oidc_states, acls, users, `user_version`) → sqlite
open helper (DSN: WAL, busy_timeout 5000, FK on, txlock immediate; compile-option
assert) → FTS5 index/reindex/query (bm25) → backlinks-in-same-tx → atomic writer
(temp→fsync→rename + sha256 map) → fsnotify recursive watch (debounce, re-add on
Rename/Remove, Overflow→rescan). Acceptance: `make integration` covers
write→index→search→backlink→external-edit round-trips; `semiplane reindex`
rebuilds from `testdata/vaults/e2e`.

### C — Auth & ACL (`internal/auth`, `internal/acl`, `internal/backup`)

Depends on B's schema (start after B task 1 lands). Tasks: Argon2id PHC +
crypto/rand-32B sessions (`HttpOnly; Secure; SameSite=Lax`) → OIDC+PKCE with
server-side state/nonce store + manual nonce check → 6-digit LAN codes (hash,
10-min TTL, single-use, rate-limit) → path-prefix ACL (`characters/<user>/*`) +
token-scope middleware → `VACUUM INTO` backup. Acceptance: httptest matrix
(login/logout, cookie flags, scope rejection, cross-user 403, LAN join, OIDC
against fake provider); `make doctor` extended.

### D — Dice + rules (`internal/dice`, `internal/rules`, graduates spike)

Depends on Phase 0 task 3 only. Tasks: events types (done in Phase 0) → participle
grammar with input caps (≤64 chars, count/sides caps, paren depth ≤4) → seeded
`Roller` + `Result{Faces,Total,Successes}` + `DiceEvaluator` → `HookEnv` +
`CompileHook` (struct Env, `NoTimePatcher`, program cache) → registry + priority
`Reduce` (dice|flat slots, crit doubles dice only) → conditions registry → port
spike 7/7 scenario tests as `Faces`-exact goldens under `SEMIPLANE_TEST_SEED`.
Acceptance: `make fuzz` runs real fuzz targets on grammar + expr compile;
symbolic-dice seam (`Successes` view) present but numeric-only per spike report §7.

### E — API surface (`internal/api`, `internal/sse`, `internal/oplog`, `internal/app`)

Depends on A (render), B (index/vault), D (dice/rules) contracts — start against
the Phase-0 interfaces with fakes, swap to real impls as streams land. Tasks:
`GET/POST /api/v1/notes/{path...}` (ETag, atomic upsert, conflict detection) →
`/api/v1/search` (FTS5) + `/api/v1/graph` → `POST /api/v1/dice/roll`
(seeded iff `SEMIPLANE_TEST_SEED`) → `POST /api/v1/reveal` + `GET /api/v1/events`
(SSE, `Last-Event-ID` replay) → oplog append (replayable combat log) → middleware
(request ID, slog, rate limit, recovery, problem+json). Acceptance: `make
contract` against a grown `openapi.yaml`; `httptest` vault isolation per test.

### F — Web shell → wiki read (`web/src`, routes, stores)

Depends on E's route contracts (build against `openapi.yaml` + mocked fetch
until live). Tasks in order: tokens + responsive `Shell.svelte` → api client +
session store + layout → `WikiReader` + wiki routes → TipTap `Editor` + custom
nodes (dice button, gm block, statblock; `md→doc→md` round-trip fixtures) → SSE
rune store + `GmBlock` gating → dice UI with breakdown popover → creator wizard
(`rules/`-metadata driven) → plugin loader under CSP. Acceptance: `pnpm lint`
(svelte-check) + vitest per component; `web/dist` stays ≤300KB gzipped
(stack promise: table devices).

### G — E2E growth (`e2e/tests`, runs alongside F)

Depends on E+F landing incrementally. Grow 3 smoke → 9-file inventory in
spec order: auth → acl → wiki → gm-secrets → realtime → editor → dice →
creator → plugins. Each spec lands with the feature it covers (same PR where
possible). Keep `fixtures.ts` (seed + fake OIDC + SSE helpers) and the
anti-flake contract (testids only, no sleeps, zero external calls). Full suite
gates PR-to-main via `main-gate`; smoke stays advisory on feature branches.

## Phase 2 — Integration & release readiness (2 agents, after Phase 1)

1. **Vault-format freeze**: `semiplane:` frontmatter namespace + marker-block
   ingest tool (`tools/ingest-fixtures`) emitting hook stubs for the v1 rule
   subset; coverage report; corpus licensing audit (SRD-only default).
2. **Release pipeline**: `release.yml` (heavy re-run + cross-OS smoke + minisign
   + SBOM), `semiplane backup/user/plugin` CLI completion, `doctor` bundle,
   versioned OpenAPI + `pkg/pluginapi` semver tag.

## Conflict map (what agents must NOT touch simultaneously)

| Shared surface | Owner | Others |
|---|---|---|
| `go.mod`/`go.sum` | Phase 0, then announce-only adds | never commit lockfile changes inside feature PRs |
| `openapi.yaml` | E (adds routes) | F/G read-only until E merges |
| `internal/events` | D (+ spike ontology) | read-only for all others |
| `pkg/pluginapi` | Phase 0 skeleton; E extends | no breaking changes without ADR |
| `internal/webui/dist` | build output only | never commit |
| `web/src/lib/api/*` shapes | F | E2E G mirrors, doesn't redefine |

Merge order into main: Phase 0 → A, B, D (independent) → C (needs B schema) →
E (needs A/B/D) → F (needs E contracts) → G with F. D and the spike branch
coordinate: spike merges `docs/rules-ontology.md` first; code graduates after.
