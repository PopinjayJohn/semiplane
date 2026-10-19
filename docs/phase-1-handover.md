# Phase 1 handover (2026-10-19)

Status of the Phase 1 charter at handover, the decisions already taken, and
the open questions the next session(s) need to rule on. Canonical stack/rules
decisions stay in `docs/tech-stack.md` / `docs/rules-ontology.md`; stream-level
rulings from the earlier reviews remain in `docs/implementation-plan.md`
(§ Phase 1 review follow-ups). This doc covers everything after those.

## Landed state

Merged PRs (squash, conventional commits, branch deleted):

| PR | Stream | Content |
|---|---|---|
| #1 | 1/C auth & ACL | sessions, roles, ACL middleware |
| #2 | 1/E API surface | `/api/v1` handlers + `openapi.yaml` |
| #3 | integration | auth wired into `app.NewServer`, spec auth routes, rate limit 60 req/min, campaign-leak filtering |
| #4 | 1/F web | tokens/Shell, api client, WikiReader + GmBlock, SSE store, TipTap editor |
| #5 | 1/G wave 1 specs | 01–07 API-level + helpers (33 tests) |
| #6 | 1/G wave 2 specs | 07-editor, 10-tv, 11-a11y (47/47 total) |
| #7 | 1/F follow-up | dice UI + breakdown popover, TV NOW banner, TV 64px target fix |

Streams A/B/D (pipeline, storage, dice+rules) landed with the above; Phase 1
is complete per charter **except** the explicitly-dropped and deferred items
below.

## Decisions taken (rulings log)

1. **Session cookie name**: `semiplane_session` (unchanged).
2. **Dice breakdown**: opt-in `?breakdown=1` — unchanged; returns
   `kept`/`discarded`/per-tier `breakdown` lines.
3. **Rate limit**: 60 req/min, trusted-proxy policy still TODO (see follow-ups
   §3 of `docs/implementation-plan.md`).
4. **Contract**: `make contract` reports 17 spec operations served. A 1/F
   `GET /api/v1/packs` was pre-authorized but 1/F chose not to add it; S6 is
   now dropped (below), so the 17-op surface stands.
5. **Dice UI readout source of truth**: the `POST /api/v1/dice/roll` response,
   NOT the SSE `dice_rolled` broadcast. Self-rolls would double-announce and
   double-paint otherwise; the broadcast exists for other clients' rolls.
6. **Dice breakdown replay**: the popover re-requests the same formula with
   the last result's `seed` so kept/discarded faces reproduce the roll the
   user just saw. A fresh unseeded breakdown only happens when no result
   exists yet or the formula changed since the roll (component documents this
   in `DiceRoller.svelte`). Requires the server to treat `seed` as a replay
   knob — verify + Go-test that same `(formula, seed)` is deterministic
   (stream E/D; smallest open TODO on the API side).
7. **NOW banner semantics** (was under-specified in `docs/e2e.md`): a TV-only,
   fixed-bottom strip (`NowBanner.svelte`) showing current page, gm-reveal
   pending/done state, and last dice total, fed by the `now` rune store.
   Stream G will assert this in the next spec wave.
8. **TV 64px touch target**: won over bundle-order by double-scoping the
   selector (`[data-viewport='tv'][data-viewport='tv'] …`) in tokens.css —
   documented in place; revisit if a cleaner layering appears.
9. **SSE-in-e2e 401**: test-context artifact only (HttpOnly cookie can't be
   injected via `document.cookie`). The app's same-origin `EventSource` gets
   cookies normally. G's realtime spec uses explicit-cookie fetch reads; a
   future UI-level realtime spec should log in through the real login form.
10. **`:::gm` visibility**: role-based only, never viewport-based (ontology).

## User rulings at handover (this session)

11. **S6 creator wizard: DROPPED.** The wizard is no longer a Phase 1
    deliverable; any future character-creation work restarts in Phase 2 with
    its own scoping (see `docs/tech-stack.md` §builder intent).
12. **S7 plugins: GM-only install.** Plugins are installed only by the GM;
    players may see plugin-rendered UI. This narrows the trust model but does
    not remove the sandbox need (a plugin runs same-origin under the GM's
    session and its output reaches players) — research session's analysis.
13. **Security amend (IMPLEMENTED in commit af37397): "Secrets must not leave
    the server for anyone but their owner or GM."** Research exposed two real
    gaps in `internal/api/notes.go`:
    - `buildNoteDoc` returned `Content` (raw Markdown) **including `:::gm`
      fences for every role** — a player received secrets in the `content`
      field of any note they could ACL-read. Only `Fragment` was role-stripped.
    - `roleOf` parsed `?role=gm` from the query without checking the session
      — a player could request the unstripped GM fragment outright.
    Fixed: strip `:::gm` raw text from `Content` for non-owner/non-GM readers;
    reject `?role=gm` with 403 unless the session holds the GM role; resolve
    note ownership for `characters/<user>/*` (character owners see their own
    GM blocks). Added `StripGMBlocks` to `internal/markdown` and comprehensive
    Go tests asserting player responses lack `:::gm` in `content`.

## Research deliverable: S7 recommendation (for implementation later)

From the read-only research session (`ses_eac05644bffeqj7hWHm87tCUaH`,
worktree `openchamber/phase-2-s6s7-research`, pristine — no code/PR):

- **Recommended: sandboxed `<iframe sandbox="allow-scripts" …>` +
  `postMessage` RPC at the `PluginCard` boundary**, plugin assets served from
  vault static files `.semiplane/plugins/` via a read-only API endpoint
  (`GET /api/v1/plugins/{asset}`, path-traversal guarded). External git/npm
  sources and build-time baking were rejected (offline contract / not
  user-installable). Trusted Types alone is not a sandbox (no same-origin
  isolation); DOMPurify-only mount contradicts the runtime-JS plugin promise.
- **Constraint discovered: CSP headers are mandated but not implemented** —
  `internal/webui/webui.go` sets only `Cache-Control`. Net-new work, needed
  before any runtime plugin ships.
- `pkg/pluginapi` has the Slot vocabulary + capability negotiation but **no
  runtime-plugin hosting interface** yet. The ux-prototype branch has the
  soft-fail `svelte:boundary` plumbing to lift.

## Open questions for the next session(s)

1. **Secrets amend: FIXED (commit af37397).** Implementation complete with Go
   tests asserting player responses lack `:::gm` in `content`. The fix:
   - `roleOf` validates `?role=gm` against session identity; players get 403
   - `buildNoteDoc` strips `:::gm` from `Content` for non-GM/non-owner readers
   - Character owners (`characters/<user>/*`) see their own GM blocks
   - New `StripGMBlocks` in `internal/markdown` for raw text stripping
2. **Dice seed replay determinism:** confirm `(formula, seed)` → identical
   result server-side (Go test); contract note if `breakdown=1` + `seed`
   combined needs an example in `openapi.yaml`.
3. **S7 sandbox flags:** grant `allow-same-origin` to the plugin iframe or
   not (plugin asset loading vs strictness); decision after the E-side asset
   endpoint exists.
4. **S7 RPC contract shape:** which host capabilities a plugin gets
   (`getPage`, `rollDice`, `search`, `navigate` …) — defines the runtime API
   beyond `pkg/pluginapi` slots; must stay GM-scoped.
5. **S7 enforcement layers:** GM-only install gated purely at the API layer,
   or also UI-hidden for players?
6. **Owner definition for secrets:** `:::gm` blocks can appear in any note
   (e.g. `campaign/*`); is "owner" = GM role only, or does per-user ownership
   of `characters/<user>/*` matter for viewing user-authored secrets?
7. **Follow-up E2E waves (stream G):** UI-level dice spec (assert testids
   `dice-roller`, `dice-formula`, `dice-roll-btn`, `dice-result`,
   `dice-total`, `dice-error`, `dice-breakdown`, `dice-breakdown-panel` and
   seed-replayed breakdown under `SEMIPLANE_TEST_SEED`), NOW banner testids
   (`now-banner`, `now-page`, `now-reveal`, `now-roll`), 64px logout target,
   and an in-browser-login realtime spec (real cookies through the login
   form so `EventSource` works).
8. **Rate-limit trusted-proxy policy** (carried over: follow-ups §3).

## Session references

- F follow-up (implementation): `ses_eac05651fffeesIqczFkxzHkTZ` — wound down
  mid-cleanup; finished + gated + pushed by the integrating session (PR #7).
- S6/S7 research (read-only): `ses_eac05644bffeqj7hWHm87tCUaH` — decision
  brief for S7 (S6 dropped by ruling), pristine worktree
  `openchamber/phase-2-s6s7-research`.
- Working model IDs used for dispatches: `opencode/nemotron-3.5-super-vl-free`
  (implementation/research), `opencode/glm-5.3-flash-free` (verification).
