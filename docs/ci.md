# Semiplane — CI Plan

*Status: Proposed · Last updated: 2026-10-15*

**Principle: the Makefile is the contract; CI is a thin wrapper.** Every check that
runs in CI is runnable locally with one `make` target — CI runners add matrices,
caching, and artifacts, nothing else.

```
make lint        # gofmt -l, go vet, golangci-lint (config in repo), svelte-check, eslint
make test        # go test -race ./...  +  vitest run
make golden      # markdown→HTML pipeline golden-file tests (fails loudly on drift)
make fuzz        # short fuzz runs for dice + expr hooks (-fuzztime=30s)
make integration # httptest suite against testdata/vaults (seeds, migrations, ACLs)
make build       # web → embed → CGO_ENABLED=0 go build ./cmd/semiplane
make cross       # GOOS/GOARCH matrix build, all tagged plugin_dnd5e too
make e2e         # Playwright suite (docs/e2e.md)
make contract    # boots server, validates live routes against openapi.yaml
make doctor      # smoke: doctor bundle against a scratch vault
```

## Workflows

### `ci.yml` — push to main + PRs

```
┌─────────┐  ┌──────────┐  ┌──────────┐
│ lint    │  │ unit     │  │ web      │   (parallel, fail-fast off)
│ go+web  │  │ incl race│  │ lint+test│
└────┬────┘  └────┬─────┘  └────┬─────┘
     └─────────────┼────────────┘
                   ▼
        ┌────────────────────┐
        │ build (web→embed)  │
        └─────────┬──────────┘
      ┌───────────┼───────────────────┐
      ▼           ▼                   ▼
┌──────────┐ ┌─────────┐      ┌─────────────┐
│ e2e      │ │ contract│      │ cross-compile│  (matrix, see below)
│ Playwright│ │ OpenAPI │     │ + plugin tag │
└──────────┘ └─────────┘      └─────────────┘
      └───────────┴───────────────────┘
                   ▼
      ┌────────────────────┐
      │ fuzz (short) + doctor│  (scheduled nightly: -fuzztime=10m)
      └────────────────────┘
```

| Gate | Blocks merge | Notes |
|---|---|---|
| gofmt / vet / golangci-lint / svelte-check | yes | formatting-only diffs fail fast, no review noise |
| `go test -race ./...` + vitest | yes | race detector is non-negotiable (SSE hub + fsnotify goroutines) |
| golden (markdown→HTML) | yes | highest-risk pipeline per tech-stack §9; drift must be reviewed, never blindly regenerated |
| e2e (Playwright) | yes | see docs/e2e.md for smoke/full split |
| contract (OpenAPI vs live routes) | yes | boot server on random port, walk spec, compare paths/schemas |
| cross-compile (linux/darwin/windows × amd64/arm64, CGO_ENABLED=0, ±`plugin_dnd5e`) | yes | tech-stack §9 commitment; also proves single-binary promise |
| fuzz | no on PR (30s smoke with seed corpus in `testdata/fuzz`) | yes on nightly (10m), findings filed as issues |
| codeql / dependency-review / dependabot | warn, then enforce after baseline | security.txt not required; keep cheap |

### `release.yml` — tags only

- goreleaser-style cross builds → zip/tar.gz + SHA256SUMS
- **signing**: minisign (matches self-update §3.7) with key in `secrets/MINISIGN_KEY`;
  cosign/keyless is the documented alternative — pick before first release (ADR needed)
- SBOM (syft) attached; release notes auto-generated from conventional commits
- self-update safety: unsigned binaries are never accepted (§3.7); CI must never publish
  an unsigned artifact even on failure paths

## Matrix & caching

- `actions/setup-go` with `cache: true`; `actions/setup-node` with pnpm store cache
- Cross matrix: `goos: [linux, darwin, windows] × goarch: [amd64, arm64]`; darwin/arm64
  cross-builds fine from linux (CGO disabled) — VMs not needed for compile-only checks
- Node: pin via `packageManager` field (corepack) — decide pnpm vs npm before first commit
- Concurrency: `cancel-in-progress: true` per workflow+ref; e2e artifacts uploaded on
  failure only (traces/videos), retained 14 days

## Local parity

`act` is not installed locally (docker absent) — intentional: rely on `make` targets for
local truth, CI for environment truth. add `make ci` as an alias chain if the gap bites.
Any check that cannot run locally (e.g. cross-compile to windows) still has a `make`
target so CI just iterates its matrix.

## Secrets (CI)

`GITHUB_TOKEN` (automatic) · `MINISIGN_KEY`/`MINISIGN_PWD` (release, optional yet).
Everything else — test vaults, OAuth mock, LAN codes — must run with **zero secrets**:
e2e and integration use seeded/local-only config. A test that needs a real credential
is a broken test.
