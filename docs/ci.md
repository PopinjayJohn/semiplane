# Semiplane — CI Plan

*Status: Proposed (rev. 2) · Last updated: 2026-10-15*

**Principle: the Makefile is the contract; CI is a thin wrapper.** Every check that
runs in CI is runnable locally with one `make` target — CI runners add matrices,
caching, and artifacts, nothing else.

**No scheduled workflows.** Three tiers, no crons:

| Tier | Trigger | Contents | Budget |
|---|---|---|---|
| **Light** (every PR, any target branch) | `pull_request` | lint (go+web), unit (`-race`), vitest, build (web→embed→go) | ≤ ~5 min |
| **Smoke** (advisory; PRs touching `e2e/`, `web/`, `testdata/`) | `pull_request` + `paths` | `make build` + Playwright `smoke` project | +~3 min |
| **Heavy** (only when the PR targets `main`) | `pull_request` with `base.ref == main` | Light + golden, contract (OpenAPI vs live routes), **full E2E**, cross-compile matrix, fuzz smoke, doctor | ≤ ~15 min (parallel) |
| **Release** (tag `v*`, or manual dispatch) | `push: tags` / `workflow_dispatch` | Heavy, re-verified on the tagged commit + cross-OS E2E smoke (windows + macos) + long fuzz + minisign signing + SBOM + publish | release-only cost |

Why this shape: feature-branch commits stay fast; the expensive suite runs exactly
where the ruleset gates (PR → main) and once more against the artifact being
shipped (tag). Ad-hoc heavy runs without a PR: `workflow_dispatch` on `ci.yml`.

## Workflows

### `ci.yml` — one workflow, tiered jobs

```yaml
on:
  pull_request:            # base doesn't matter: light jobs always run
  workflow_dispatch:       # ad-hoc heavy runs on any ref
```

```
always:                              only when base.ref == 'main' (or dispatch):
┌──────────┐ ┌──────────┐            ┌────────┐ ┌─────────┐ ┌─────────┐
│ lint     │ │ unit     │            │ golden │ │ contract│ │ doctor  │
│ go+web   │ │ -race    │            └───┬────┘ └────┬────┘ └────┬────┘
└────┬─────┘ └────┬─────┘                │           │           │
     └────────────┼──────────────────────┼───────────┼───────────┘
                  ▼                      ▼           ▼
            ┌──────────┐           ┌─────────────────────────┐
            │ build    │──────────►│ e2e (full, ubuntu)      │
            └──────────┘           ├─────────────────────────┤
                                   │ cross-compile matrix    │
                                   │ linux/darwin/windows    │
                                   │ × amd64/arm64, CGO=0    │
                                   │ ± tag plugin_dnd5e      │
                                   ├─────────────────────────┤
                                   │ fuzz-smoke (30s)        │
                                   └───────────┬─────────────┘
                                               ▼
                                      ┌─────────────────┐
                                      │ main-gate       │  ← the ONE required
                                      └─────────────────┘     status check
                  (light path ends in `pr-gate` similarly)
```

**Aggregate gates, not matrix contexts.** Matrix jobs produce per-cell check names
(`cross-compile (linux, arm64)`) that are miserable to require in a ruleset. Each
tier therefore ends in one aggregate job (`pr-gate` light, `main-gate` heavy) whose
only content is `needs: [...]` — and `main-gate` transitively requires the light set.
Require **`main-gate`** (and nothing else) in the branch ruleset → merge to main is
impossible until the full heavy suite passes on that exact commit.

Note the absence of `paths-ignore`: a skipped workflow never reports its required
context and would wedge the ruleset forever. Light is fast by design, not by
skipping. The one deliberate exception is `ci-e2e-smoke` below — safe precisely
because it is *not* a required context.

### `ci-e2e-smoke.yml` — cheap smoke, paths-gated

Advisory early-warning so a broken web/E2E change surfaces on the feature branch
instead of at PR-to-main. Lives in its own workflow because `paths` filters are
trigger-level: a separate workflow can be path-filtered without touching the gates.

```yaml
name: ci-e2e-smoke
on:
  pull_request:
    paths: ['e2e/**', 'web/**', 'testdata/**']
jobs:
  e2e-smoke:
    runs-on: ubuntu-latest
    steps:
      - checkout, setup-go, setup-node (pnpm store cache)
      - run: make build            # real binary, web embedded
      - run: pnpm exec playwright install --with-deps chromium
      - run: make e2e project=smoke   # auth, wiki, gm-secrets, dice
      - uses: actions/upload-artifact@v4   # trace/video on failure only
        if: failure()
```

- Not in the ruleset's required contexts — a paths-skipped run reports nothing,
  which is exactly why it must stay optional. On main-targeting PRs it runs
  redundantly beside the heavy tier's full E2E; that's fine (cancel-in-progress
  keeps it honest).
- If a server-behavior change (`cmd/**`, `internal/**`) should also smoke early,
  add those paths here — deliberately narrow by default per "keep commits fast."

### `release.yml` — tags + manual dispatch

- Same heavy suite re-run on the tagged commit (a tag can point anywhere — trust
  nothing inherited), then:
- cross-compile matrix → zip/tar.gz + SHA256SUMS
- **cross-OS E2E smoke** (windows-latest + macos-latest, `smoke` project)
- **signing: minisign** (decided 2026-10-15) — key + passphrase in
  `secrets/MINISIGN_KEY` / `secrets/MINISIGN_PWD`; `make release verify` checks the
  signature before upload; unsigned artifacts are never published or accepted
  (tech-stack §3.7 self-update). cosign/keyless remains the documented alternative,
  but would need an ADR.
- long fuzz (`-fuzztime=10m`) as the release gate for dice/expr
- SBOM (syft) attached; release notes from conventional commits

## Makefile parity

`ci.md` runs only what `make` already runs:

```
make lint        # gofmt -l, go vet, golangci-lint, svelte-check, eslint
make test        # go test ./... (+ web vitest); -race whenever a C toolchain
                 # exists — always in CI; local no-cc machines run unraced (loudly)
make golden      # markdown→HTML pipeline golden-file tests (drift must be reviewed, never blindly regenerated)
make fuzz        # short fuzz runs for dice + expr hooks (-fuzztime=30s)
make integration # httptest suite against testdata/vaults (seeds, migrations, ACLs)
make e2e         # Playwright suite — `make e2e project=smoke` for the smoke subset (docs/e2e.md)
make build       # web → embed → CGO_ENABLED=0 go build ./cmd/semiplane
make cross       # GOOS/GOARCH matrix build, also with -tags plugin_dnd5e
make contract    # boots server on a random port, validates live routes against openapi.yaml
make doctor      # smoke: doctor bundle against a scratch vault
```

Runner steps are literally `make <target>` (plus the tier conditionals above) — if a
check exists in CI without a make target, that's a bug in the plan.

## Matrix & caching

- `actions/setup-go` `cache: true`; pnpm store cache via `actions/setup-node`
  (pnpm via corepack, pinned in `web/package.json`)
- Cross matrix: `goos: [linux, darwin, windows] × goarch: [amd64, arm64]` — compile-only,
  no VMs needed beyond the runner that cross-builds
- E2E artifacts (trace/video/screenshot) uploaded on failure only, 14-day retention
- Concurrency: `cancel-in-progress: true` per workflow + `github.ref`; for
  `pull_request` on main use `github.event.pull_request.number` so force-pushes
  supersede cleanly
- Speed wins: cache `web/dist` between build→e2e/contract/cross jobs via
  `actions/cache` keyed on the web source hash; keep E2E per-worker seeded (docs/e2e.md)

## Ruleset checklist (what to mark required on `main`)

1. Require a pull request before merging (already set)
2. Required status check: **`main-gate`** only (strict, up-to-date branches)
3. Dismiss stale approvals on new pushes; require conversation resolution
4. Allow squash-merge, delete branch on merge (linear main, conventional commits
   feed release notes)
5. No `paths-ignore` anywhere in `ci.yml` — see aggregate-gate note above
6. `ci-e2e-smoke` is deliberately **not** required — path-filtered, so sometimes
   absent. Advisory early warning, not a gate

## Secrets (all tiers)

`GITHUB_TOKEN` (automatic) · `MINISIGN_KEY`/`MINISIGN_PWD` (release only; optional
until first release — CI must still refuse to publish unsigned). Everything else —
test vaults, OAuth mock, LAN codes — runs with **zero secrets**. A test that needs a
real credential is a broken test.
