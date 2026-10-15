# Semiplane — Repository Layout

*Status: Proposed · Last updated: 2026-10-15*

Module: `github.com/PopinjayJohn/semiplane`. One Go module, one binary; the SPA is a
build input, not a deployed artifact. The Vault Layout (§10 of tech-stack.md) is
*runtime* data — this document is the *repository* layout.

```
semiplane/
├── cmd/
│   └── semiplane/            # main + CLI: serve, reindex, migrate, backup, user, plugin, doctor
├── internal/
│   ├── app/                  # config (flags > env > file > defaults), server bootstrap, wiring
│   ├── api/                  # /api/v1 handlers, ServeMux routes, problem+json, middleware chain
│   ├── middleware/           # request ID, logging, rate limit, recovery
│   ├── auth/                 # Argon2id, opaque sessions, LAN codes, OAuth2/OIDC + PKCE, API tokens
│   ├── acl/                  # gm/player roles + path-based ACL (characters/<user>/*)
│   ├── vault/                # NoteRepository: atomic writes, hash change detection, traversal guard
│   ├── watch/                # fsnotify → domain events (no echo loops, hash-suppressed)
│   ├── markdown/             # goldmark pipeline: wikilinks, :::gm nodes, statblocks, frontmatter
│   ├── render/               # role-aware HTML (server-side :::gm strip) + callouts
│   ├── index/                # SQLite (modernc), FTS5, backlinks, sessions/ACL tables, migrations
│   ├── webui/
│   │   └── dist/             # //go:embed target (build artifact, gitignored)
│   ├── dice/                 # Participle grammar, seeded roller, DiceEvaluator interface
│   ├── rules/                # hook engine: ontology types, expr eval, reducer, registry (§3.7)
│   ├── events/               # canonical event types shared by rules + SSE + future VTT
│   ├── sse/                  # /api/v1/events: flusher, Last-Event-ID replay
│   ├── oplog/                # append-only operation log (sync seed + replayable combat log)
│   ├── migrate/              # vault migrations (up-only, idempotent, refuse-newer)
│   ├── backup/               # vault zip + VACUUM INTO
│   └── doctor/               # diagnostics bundle for bug reports
├── pkg/
│   └── pluginapi/            # PUBLIC, semver'd: interfaces + capability negotiation
├── plugins/
│   └── dnd5e/                # compiled-in plugin, behind build tag `plugin_dnd5e` (registry demo)
├── web/                      # Svelte 5 + Vite + Tailwind SPA (own package.json, own tests)
│   ├── src/lib/{components,stores,api}/
│   ├── src/routes/
│   └── dist/                 # vite build output (gitignored; copied into internal/stash/dist)
├── e2e/                      # Playwright suite (see docs/e2e.md)
│   ├── tests/ specs/ helpers/
│   └── playwright.config.ts
├── testdata/                 # golden files for Markdown→HTML, dice/expr fixtures, fixture vaults
├── docs/                     # tech-stack.md, repo-layout.md, ci.md, e2e.md, rules-ontology.md, adr/N
├── tools/                    # out-of-tree helpers (fixture ingest etc.); no import from internal/*
├── Makefile                  # the single entrypoint; CI is a thin wrapper over it
├── go.mod go.sum
└── .github/workflows/        # ci.yml, release.yml
```

## Package boundaries (enforced by review, lint-checked where cheap)

| Rule | Why |
|---|---|
| `internal/*` never imports `pkg/pluginapi` — it *implements* it | Plugin API stays stable; internals refactor freely |
| `cmd/` imports `internal/` + `pkg/pluginapi` only | One binary, no frameworks |
| `pkg/` imports nothing from `internal/` | Semver surface; plugin authors depend only on `pkg/pluginapi` |
| `internal/rules` imports `internal/dice` + `internal/events`, never `internal/api` | Hook engine is transport-agnostic (§3.7 "one engine") |
| `web/` imports nothing Go; talks REST/SSE | API-first: the SPA is one client, mobile is an equal citizen |

## Boundaries defined as interfaces first (tech-stack §9)

`NoteRepository` (vault), `SearchIndex` (index), `AuthProvider` (auth), `DiceEvaluator`
(dice) — implemented inside `internal/`, consumed by `internal/api`. Fakes live in
`internal/<pkg>/testing` or `testdata`-adjacent so `httptest` integration tests run
without touching the real vault.

## Embedding the SPA (the one awkward seam)

`//go:embed` cannot cross the package boundary or follow directory symlinks, so:

1. `make web` builds `web/dist`
2. `make embed` copies it to `internal/webui/dist` (gitignored — never commit built assets)
3. `go build` embeds from there; a missing `dist` must fail the build loudly (empty vault
   embeds are a silent-404 class of bug)

CI and `make build` always run the full `web → embed → go build` chain; bare `go build`
without assets is developer-only and documented as such.

## Where the rules-engine spike lands

The spike (worktree `openchamber/spike-rules-ontology-*`) produces
`docs/rules-ontology.md` + a prototype under `spike/`. On graduation:

- prototype code → `internal/rules` (+ `internal/dice` glue), **not** `pkg/` (it is not
  the plugin surface; plugin authors consume `pkg/pluginapi` interfaces)
- `docs/rules-ontology.md` stays canonical, referenced by tech-stack §3.7
- ontology event/state types in `internal/events` are the VTT protocol seed (docs/e2e.md
  determinism rules depend on them)

## Conventions

- **Package names:** short and lowercase (Go convention). `internal/rules` and
  `internal/dice` deliberately keep their names rather than becoming `*engine` —
  the "Rules Engine" is the *subsystem* (`internal/rules` + `internal/dice` +
  `internal/events`), while the vault's `rules/` is user content. Disambiguate in
  package doc comments, never in package names.
- Go 1.22+ `ServeMux` only — no router frameworks (tech-stack §3.1)
- Errors: RFC 7807 `application/problem+json`; one error-handling path (`internal/api`)
- Logging: `log/slog`, JSON in prod/text local, request-scoped via middleware
- Tests colocated (`_test.go`); golden files in `testdata/golden/`, fuzz corpus in
  `testdata/fuzz/`; `testdata/vaults/` holds small curated vaults for `httptest`
- `fixtures/` (the 5e.tools corpus) stays untracked for now — licensing audit pending
  (tech-stack §10); e2e/tests must never depend on it, only `testdata/`
- Local entrypoint parity: everything CI runs is `make <target>` (docs/ci.md)
