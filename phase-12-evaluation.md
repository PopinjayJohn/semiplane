# Phase 12 evaluation (2026-10-05)

Branch `phase/12-hardening` at `b2dbc4d`, in sync with origin. Four of eight
work items merged; four stalled. `go build ./...` green; merged suites
(`internal/observability`, `internal/domain/rules`) pass.

## Merged (H1, H3, H5, H6)

| Item | Commit | Files |
|---|---|---|
| H1 secret-redaction gate | `2d0c18d` (#97) | `internal/httpapi/secrets/redaction_test.go` |
| H5 observability payloads | `3a97b30` (#98) | `internal/observability/payload_test.go` |
| H6 determinism / drift / views | `a9db244` (#99) | `internal/domain/rules/hardening_*_test.go` (4 files) |
| H3 race coverage | `b2dbc4d` (#100) | `internal/content/cache_concurrency_test.go`, `debounce_concurrency_test.go`, `internal/realtime/hub_concurrency_test.go` |
| H2 security tests | `8380052` (local merge 2026-10-05) | 6 files: traversal, XSS, YAML bomb, WS origin, cache partition, FTS visibility |

## Stalled

- **H2 security tests** — worktree `12-f` (`12-hardening/security`) holds 6
  untracked test files (~3.3k lines). Ran them 2026-10-05 with `-race`:
  all 24 tests pass (path traversal 4/4, XSS/DOM audit 4/4, cache
  partitioning + FTS visibility, WS origin/auth parity, and the three YAML
  bomb tests). NOTE: the bomb tests initially failed — root cause was a
  tampered module cache (see § "Module-cache incident" below), not the
  tests. With the pristine parser restored, the whole H2 suite is green
  and ready to commit.
- **H4 failure modes** —was: branches `failure-modes` / `failures` clean with
  zero new work. 2026-10-05: split into two halves with disjoint ownership
  and dispatched to background subagents (both branches merged current with
  phase first): 12-e `failure-modes` owns content/edit rows (missing root,
  inotify exhaustion, mid-write read, stale save, cache miss); 12-g
  `failures` owns realtime/store/play rows (mass disconnect, SQLite busy,
  crash resume). 2026-10-05 update: H4b DONE and merged (`8182778`):
  since/delta, stale-sweep, write-serialisation, single-generation floor
  and drift-refusal rows audited as already covered (hub, protocol, state,
  ruleset and store tests); four gaps filled —
  `failures_disconnect_test.go` ×2 (16-peer mass leave converges, stale
  gauge untouched, hub reusable; socket side over 8 real connections),
  `failures_busy_test.go` (busy_timeout=5000, pool==1, second Open refused
  then released, 16-writer campaign_state serialisation), and
  `failures_crash_test.go` (floor advances to last debounce, crashed
  incarnation gets full snapshot, sequence continues floor+1). Verified:
  gofmt clean, vet clean, `go test -race` green on owned packages.
  missing-root, inotify-exhaustion and stale-save rows audited as already
  covered (supervisor_test, save_test); two gaps filled —
  `debounce_timeout_retry_test.go` (timeout-drop re-arms on next event,
  exactly one timeout per episode) and `cache_failure_modes_test.go`
  (invalidation forces synchronous re-render, flight waiter/leader
  accounting, both-tier stats). Verified: gofmt clean, vet clean,
  `go test -race` green on owned packages.
- **H7 sweep conversion** — 2026-10-06 update: DONE and merged. Subagent
  produced the findings table (§10.3 matrix/keyboard/D-pad/media all held
  except 5 gaps; browser-only behaviors documented as untestable-in-Go
  rather than vacuo-tested) and closed the gaps with `internal/web/
  sweep_test.go` (5 built-stylesheet audits, each with violating-fixture +
  near-miss meta-tests and healthy acceptance) plus a `.css` mutation path
  in `scripts/mutate-route-a11y.sh` (rebuild after mutate and restore,
  dirty-guard extended to *.css). Verified: gofmt/vet clean, web package
  `-race` green, `make a11y` 15/15 green, full mutation script on the clean
  phase tree: every case fails as required, 0 survivors. No Makefile A11Y
  changes needed (new names match `BuiltStylesheet`). Known pre-existing
  gap recorded, not acted on: `internal/httpapi/shell/` sweep suites run
  under `make check` but are not in `A11Y_ROUTE_PKGS` / match `A11Y_TESTS` —
  expanding the gate is an integrator call left for the phase PR.
- **H8 docs truth-up** — 2026-10-06 update: DONE and merged. README
  pre-release banner retired (product described as running end to end);
  roadmap marked all 12 phases Landed with "What already runs" rewritten
  against the real product and the decided cross-campaign-link question
  updated; one "Known staleness" entry added (§15 phase table vs the shipped
  13-phase delivery). `make site-check` green (74 links, 74 pages).
  install/ and operating/ audited current, untouched.

## Recommendations (in order)

1. Land H2 now: commit the 6 green files from `12-f` as one PR. Unblocked —
   the YAML failures were environmental (see incident below), and the full
   suite is green with `-race` as of 2026-10-05.
2. Scope H4 before restarting: list the 8 failure modes as checklist rows in
   the phase PR, assign `failures` vs `failure-modes` disjoint file sets.
   The overlapping names are a mis-merge risk — rename or drop one.
3. H7 needs its input (the sweep-finding list) as its first deliverable;
   check `sweep` owners for offline notes, then staff or formally drop.
4. H8 goes last, owned by the integrator.
5. Hygiene: worktrees `12-a`–`12-d` are pre-merge copies of merged commits —
   removable with `git worktree remove` (branches untouched). Done 2026-10-05.
6. Phase cannot merge until H2/H4/H7 are each merged or explicitly dropped
   with reasons in the phase PR description, plus `make ci`,
   secret-redaction, and `make demo-check` green.

## Module-cache incident (found and fixed 2026-10-05)

`go mod verify` failed: `gopkg.in/yaml.v3 v3.0.1: dir has been modified`.
The extracted module cache at `/root/go/pkg/mod/gopkg.in/yaml.v3@v3.0.1/decode.go`
carried this edit:

```go
// MUTATION: the alias-expansion limit is raised out of the way.
if false {
        failf("document contains excessive aliasing")
}
_ = allowedAliasRatio
```

i.e. the billion-laughs protection was disabled. Every build and test on
this machine ran against a YAML parser with no alias-expansion limit, on a
project whose front matter is attacker-reachable (Obsidian Sync). The
`// MUTATION:` marker matches this repo's mutation-testing practice — most
likely a mutation experiment edited the shared cache instead of a local
copy and was never reverted. Consequences while it was in place:

- Merged test `TestParseRefusesAnAliasBomb` failed at 3 levels on
  `phase/12-hardening` itself (red gate), and its 9-levels subtest hung
  expanding ~9⁹ aliases — `go test ./internal/content/` could not complete.
- The stalled H2 `yaml_bomb_test.go` failures were caused by this, not by
  the tests or the product.

Fix applied: `rm -rf` the extracted dir, `go mod download gopkg.in/yaml.v3`
(re-extracted offline from the hash-verified zip — no network needed).
`go mod verify` now reports `all modules verified`, and both the merged
bomb test and the H2 bomb tests pass.

Follow-ups worth deciding: (a) who mutated the cache and whether other
machines/worktrees share it — `go mod verify` belongs in onboarding, not
just CI; (b) whether the 9-levels subtest (hundreds of millions of nodes
even against a working limit — it passed in 0.02s once the limit was live,
so no action) needs a guard; none needed, recorded here so nobody
re-investigates.

## Standing rules reminder

- Do not commit on work-item branches without being asked (`git commit`
  needs an explicit ask; the H2 files above are still uncommitted).
- H2/H4/H7 touch only test files — no integrator-owned paths involved.
