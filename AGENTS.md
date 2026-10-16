# AGENTS.md — working in this repo

Read this before writing code. Canonical decisions live in `docs/`; this file is
the operational layer on top: how to build, verify, commit, and what has bitten
agents before.

## Docs index (read the relevant one before starting)

| Doc | When |
|---|---|
| `docs/tech-stack.md` | **Canonical.** Stack decisions + rationale. If your work contradicts it, stop and ask — don't silently diverge. If a decision changes, update this doc in the same PR. |
| `docs/repo-layout.md` | Where code goes, package boundaries, the `web → embed → go build` chain. |
| `docs/ci.md` | CI tiers. `main-gate` is the required check; see below. |
| `docs/e2e.md` | E2E architecture, determinism, anti-flake contract, spec inventory. |
| `docs/rules-ontology.md` | **Canonical** event/state/hook spec. The rules engine's law. |
| `docs/implementation-plan.md` | Phases, workstreams, conflict map, merge order. |
| `docs/prototype-notes.md` | Does not exist on main (lives on the disposable UX branch) — see Branches. |

## The contract: `make` targets (CI wraps these, nothing else)

```
make lint        # gofmt+vet+golangci-lint, svelte-check (web part skips if node_modules absent)
make test        # go tests + vitest. -race only when a C compiler exists (CI always races)
make build       # web → embed → CGO_ENABLED=0 single binary in bin/
make contract    # boots bin/semiplane, diffs live routes vs openapi.yaml
make doctor      # doctor smoke against a scratch vault
make e2e [project=smoke]  # real binary + scratch vault + Playwright (Chromium)
make cross       # 3 OS × 2 arch × ±plugin tag, CGO_ENABLED=0
make golden | fuzz | integration  # wired; some are pass-through until their code lands
```

**Before opening a PR:** `make lint test build contract` must pass. Add `make e2e
project=smoke` if you touched `web/`, `e2e/`, `testdata/`, `cmd/`, or `internal/`.

## Green-main rules (enforced by ruleset)

- Every PR needs `main-gate` green to merge into `main`. Feature-branch PRs run
  the light tier only; keep them fast.
- `ci-e2e-smoke` is advisory, never required (it is paths-gated; skipped runs
  report nothing). Never add `paths-ignore` to `ci.yml` — a skipped workflow
  never reports `main-gate` and wedges the queue forever.
- Squash-merge, linear main, **conventional commits** (`feat:`, `fix:`, `docs:`…
  — release notes are generated from them). Delete branch on merge.

## Never commit

`bin/`, `dist/`, `web/dist`, `node_modules`, `e2e/test-results`,
`internal/webui/dist/*` (except the tracked `placeholder.html` — see below),
and **`fixtures/`** (untracked 5e.tools corpus: licensing-unresolved, 8.6 MB).
Tests and seeds must use `testdata/`, never `fixtures/`.

## Proven gotchas (learned the hard way — don't rediscover)

1. **`web/svelte.config.js` is mandatory.** Without `vitePreprocess`, svelte-check
   fails with "No Svelte configuration found in vite config" even when `vite build`
   works. Tailwind via static `@tailwindcss/vite` import is fine.
2. **Go `//go:embed` ignores dotfiles.** `internal/webui/dist/` needs the tracked
   `placeholder.html` or the package won't compile on a fresh checkout. The
   `.gitignore` entry must be root-anchored (`/dist/` for cross output) or the
   negation `!/internal/webui/dist/placeholder.html` silently fails (git can't
   re-include inside an excluded dir).
3. **Bash + python quoting in scripts.** Inside `$(...)`, single quotes nested in
   double quotes do NOT quote for bash — regex parens then unbalance the subshell.
   Single-quote the whole `python3 -c '...'` program and use double quotes inside
   Python. Run `bash -n` on any shell script you touch.
4. **Degradations are deliberate, keep them.** `make test` runs unraced (loudly)
   without a C compiler; `e2e/scripts/run.sh` installs browsers without OS deps
   without sudo. CI always has both. Don't "fix" these into hard failures.
5. **Web budget:** `web/dist` JS must stay ≤300 KB gzipped (table devices).
   Code-split TipTap per route; the slice shell is 34 KB, the full prototype with
   TipTap is ~118 KB — both fit only split.
6. **This sandbox lacks Chromium OS libs and sudo.** E2E here needs
   `LD_LIBRARY_PATH=/tmp/pwlibs/root/usr/lib/x86_64-linux-gnu` (see shell history
   for the `apt-get download` recipe). CI installs `--with-deps` normally.

## E2E rules (from `docs/e2e.md`, enforced in review)

`data-testid` selectors only · no `waitForTimeout` for correctness · seeded RNG
(`SEMIPLANE_TEST_SEED`) for exact assertions · fake OIDC via `route.fulfill`,
never real credentials · zero external network calls · rule math is asserted in
Go tests, never in E2E (E2E asserts what the user sees) · axe scans: zero
serious/critical violations (see Accessibility).

## Rules-engine rules (from `docs/rules-ontology.md`, non-negotiable)

Triggers are structural matches on canonical events — **no prose/NLP parsing at
roll time, ever**. Damage modifiers declare `slot: dice|flat`; only dice double
on crit. Advantage is event-level dice spec (cancels, never stacks). Hooks are
pure `(state, event) → (state, effects)` with threaded seeded RNG. Unknown
vocabulary fails at authoring time. Die sizes, crit thresholds, condition keys,
tier order live in **pack data** (`rules/systems/<id>/`), never in engine code.

## Frontend rules

Svelte 5 runes only (`$state/$derived/$effect`; no `$:`/`export let`/legacy).
TipTap custom nodes must implement all three Markdown hooks (tokenizer, parse,
render) or content silently drops on `getMarkdown()`. Design tokens are CSS
custom properties (`--sp-*`); community skins recolor via tokens, never
component CSS. Runtime plugins declare **slots, never positions** (closed list in
tech-stack §4). Strict CSP: no inline scripts/styles from user content.

## Accessibility (WCAG 2.2 AA — see `docs/e2e.md` + plan)

- Every interactive flow keyboard-operable with visible focus; no mouse-only paths.
- Async updates announce: SSE reveals and dice results go through `aria-live`
  regions. Pending states must be text, not shimmer-only.
- `prefers-reduced-motion` disables shimmer/transitions/parallax.
- Contrast AA against the token set — new `--sp-*` tokens ship with contrast
  ratios recorded. TV viewport: 64 px targets, never renders `:::gm`.
- Zero new Svelte compiler a11y warnings; axe gate in E2E (serious+critical).

## Branches and sessions

- `main` is the only long-lived branch. Active spike/prototype branches are
  disposable worktrees; code graduates through Phase-1 streams, never big-bang.
- If your work fixes or implements a tracked issue or an open PR/MR, link it to
  the session (OpenChamber `session.link`) as soon as you know it.
- Keep a worktree's `fixtures/` copy untracked and in sync by hand if you need
  the corpus; it is never committed anywhere.
