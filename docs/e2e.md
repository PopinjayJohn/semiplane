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

| Spec | Verifies | Landed with PR |
|---|---|---|
| `01-auth.spec.ts` | password login/logout, `HttpOnly`+`SameSite` cookie flags, invalid creds = uninformative 401, LAN code single-use + 429 rate-limit on join, OIDC 503 when unconfigured | #5 |
| `02-acl.spec.ts` | cross-user `characters/<user>/*` write 403, `campaign/*` read 403 for players (200 for GM), token scope rejection = 401 (not 403) | #5 |
| `03-wiki.spec.ts` | note GET round-trip incl. ETag/304, 409 on empty-hash POST to existing path, search/graph served (player search shows no campaign/* rows), list endpoint works | #5 |
| `04-gm-secrets.spec.ts` | `role=player` strips `:::gm` in fragment; `role=gm` keeps it; bad role 400 (server-side property) | #5 |
| `05-realtime.spec.ts` | EventSource receives block_revealed/dice_rolled; Last-Event-ID replay on reconnect; reveal 403 for player | #5 |
| `06-dice.spec.ts` | default response omits breakdown; `?breakdown=1` includes kept/discarded/breakdown; seeded exact total+faces via SEMIPLANE_TEST_SEED; invalid formula 400 | #5 |
| `07-editor.spec.ts` | TipTap edit → clean Markdown persisted to disk (round-trip), live preview on cursor line | pending-blocked-on-F |
| `08-creator.spec.ts` | wizard steps driven by `rules/` metadata, point-buy validation, creates a character page the player then owns | pending-blocked-on-F |
| `09-plugins.spec.ts` | runtime JS plugin from `.semiplane/plugins/` loads under strict CSP; broken plugin fails soft | pending-blocked-on-F |
| `10-tv.spec.ts` | TV viewport (64px targets) never renders `:::gm` content under any mode/viewport pairing; glanceable NOW banner present | pending-blocked-on-F |
| `11-a11y.spec.ts` | axe scans (WCAG 2.2 AA) on shell + each route: zero serious/critical; keyboard-only pass; `aria-live` for SSE reveal + roll results; reduced-motion honored | pending-blocked-on-F |

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

## Accessibility (WCAG 2.2 AA)

Automated axe scans run inside the E2E suite (`@axe-core/playwright`,
`e2e/helpers/axe.ts`): `wcag2a/wcag2aa/wcag21a/wcag21aa/wcag22a/wcag22aa` tags,
**zero serious/critical violations** or the spec fails. Minor/best-practice
findings are filed as issues, never silently ignored; rule exclusions require a
comment linking the issue. Each route spec scans its own surface; `a11y.spec.ts`
covers the shell plus keyboard-only flows (nav, reveal, dice, wizard),
`aria-live` announcements for SSE reveals and roll results (pending states must
be text, not shimmer-only), and `prefers-reduced-motion`. Svelte compiler a11y
warnings are part of `pnpm lint`: zero new warnings. Automated scans do not
replace humans: a manual keyboard-only + screen-reader pass is required before
every release (append to the release checklist when `release.yml` lands).

## Out of scope (deliberate)

Visual regression across viewports (defer until design freeze), load/soak testing
(later, separate concern), mobile PWA installability flows (needs the proxy story
from §3.6 first), and *any* test that exercises the untracked `fixtures/` corpus.
