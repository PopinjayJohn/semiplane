# Semiplane — E2E Plan

*Status: Proposed · Last updated: 2026-10-15*

**Stack: Playwright** — chosen because the product's core interactions are
multi-client (GM ↔ player over SSE), and Playwright gives first-class browser
*contexts* (two logged-in roles in one test), event-listener-based waits for SSE
(no sleep-based flake), and trace/video artifacts for failures.

## Where E2E sits in the test pyramid

| Layer | Covers | Where |
|---|---|---|
| Unit + golden | goldmark pipeline, wikilinks, `:::gm` AST | `internal/**/_test.go` + `testdata/golden/` |
| Property + simulation | dice engine, expr hooks, scenario traces (seeded RNG) | `internal/rules`, spike harness |
| `httptest` integration | API contract, ACLs, atomic writes, migrations, SSE replay | `internal/api` |
| **E2E** | the *product surface*: real binary + real vault + real browsers, the seams only a browser can exercise (TipTap editing, role-aware reveals, creator wizard, live updates) | `e2e/` |

Rule math is **never** asserted in E2E — a roll UI test asserts *what the user sees*
against a seeded engine output, not against recomputed rules. Test logic belongs as
low as it can go.

## Server lifecycle

```
make e2e
  → make build                                  # real binary, CGO_ENABLED=0
  → e2e/global-setup.ts:
        cp testdata/vaults/e2e ~/…scratch        # small, committed, deterministic
        semiplane user create (gm, player1, player2) with fixed passwords
        start N servers on a port pool (one per worker), health-wait /healthz
  → playwright runs specs against per-worker baseURL
  → global-teardown: kill servers, keep artifacts on failure
```

- **Vault seed is committed and tiny** (`testdata/vaults/e2e`, ~10 notes incl. one
  `:::gm` block, one wikilink pair, one character page). Never seed from `fixtures/`
  — that corpus is untracked and licensing-unresolved.
- **Determinism**: the server runs with a test-only seeded RNG (`SEMIPLANE_TEST_SEED`)
  per tech-stack §3.8 — dice rolls are then exact values E2E can assert. Without that
  flag the suite may only assert *ranges/patterns*.
- Port pool + per-worker scratch dir = workers are isolated and parallel-safe; no
  shared state between specs.

## Spec inventory (each maps to a tech-stack acceptance point)

| Spec | Verifies |
|---|---|
| `auth.spec.ts` | password login/logout, `HttpOnly`+`SameSite` cookie flags, LAN 6-digit join, API token scope rejection (`characters:write` on read-only token) |
| `acl.spec.ts` | `characters/<user>/*` ownership: player B cannot read/edit player A's sheet (403/404 contract) |
| `wiki.spec.ts` | wikilink resolution, backlinks panel, FTS5 search ranking, callout rendering |
| `gm-secrets.spec.ts` | `:::gm` block stripped for player context, visible for GM; GM clicks reveal → player sees the block appear via SSE without reload |
| `realtime.spec.ts` | two contexts on one note: edit in GM context → player context updates via SSE; external file edit on disk → UI offers overwrite/reload/save-copy (never silent last-write-wins) |
| `editor.spec.ts` | TipTap edit → clean Markdown persisted to disk (round-trip), live preview on cursor line |
| `dice.spec.ts` | seeded roll returns exact total; roll breakdown shows components (`+4 STR +2 weapon +2 rage`-style provenance per §3.8/ontology); `/api/v1/dice/roll` parity with UI |
| `creator.spec.ts` | wizard steps driven by `rules/` metadata (class/ancestry/background pick lists from the corpus), point-buy validation, creates a character page the player then owns |
| `plugins.spec.ts` | runtime JS plugin from `.semiplane/plugins/` loads under strict CSP; broken plugin fails soft |
| `tv.spec.ts` | TV viewport never renders `:::gm` content under any mode/viewport pairing; glanceable NOW banner present |

## Rules of the suite (anti-flake contract)

- **Selectors**: `data-testid` only. No CSS-structure or copy-text locators.
- **Waits**: SSE assertions use event listeners/`page.waitForResponse` patterns and
  Playwright auto-waiting — never `waitForTimeout` for correctness (only for
  debounce-tolerant UI like toasts, and those are bounded and signaled by testid).
- **Network**: zero external calls. OAuth/OIDC tested against a fake OIDC provider in
  `e2e/helpers/` (or feature-flagged mock); everything else local.
- **Retries**: 0 local, 1 in CI; a spec failing twice is quarantined with
  `@flake` tag + linked issue, not silenced.
- **Artifacts**: trace + screenshot on failure, video retained on failure only,
  14-day retention (matches ci.md).
- **Runtime budget**: zero E2E on feature-branch PRs (light tier skips it) — streaks
  stay fast. Full E2E gates PRs targeting `main` (heavy tier); the cross-OS smoke
  (windows + macos) runs at release or manual dispatch only — no scheduled runs
  (docs/ci.md). Full runtime parity belongs to the cross-compile matrix, not here.

## Out of scope (deliberate)

Visual regression across viewports (defer until design freeze), load/soak testing
(later, separate concern), mobile PWA installability flows (needs the proxy story
from §3.6 first), and *any* test that exercises the untracked `fixtures/` corpus.
