---
title: "Specification"
description: "The system's technical requirements, with stable identifiers."
lede: "Every requirement semiplane must satisfy, numbered so a test, a pull request, and a decision record can all cite the same thing. This is a living document: the phase that changes a requirement edits it here."
weight: 10
---

This is the contract the code is written against. Requirements carry stable identifiers — a
test, a pull request, and a
[decision record]({{ "decisions/" | relURL }}) can all cite `S-5.6.1` and mean the same thing.

**Identifiers are never reused and never renumbered.** If a requirement turns out to be wrong,
it is retired and a new one is added; the old identifier keeps pointing at the requirement it
originally described, so a historical reference stays honest.

Derived from the [architecture
overview]({{ "design/architecture/" | relURL }}). Where this document and a design record
disagree, **the decision records win**, and this document is wrong until someone fixes it.

---

## S-1 — Scope and the single-instance rule

- **S-1.1** semiplane is one binary, one process, one SQLite file, with campaign content in a
  local Obsidian vault.
- **S-1.2** **Authoritative game state lives in memory and the WebSocket hub is
  process-local. Two instances behind a load balancer will silently diverge.** There is no
  clustering, no leader election, and no configuration that permits a second instance.
  See [0004]({{ "decisions/0004-single-process-constraint/" | relURL }}).
- **S-1.3** Out of scope: voice and video, combat automation, native mobile clients, multi-
  instance HA, horizontal scaling, and third-party runtime plugin loading.
- **S-1.4** **A forwarding header is evidence of nothing until an operator names the proxy
  that sent it.** With no proxy configured, the client address is the transport address and
  `X-Forwarded-For` and `X-Real-IP` are ignored entirely — the default must be safe for a
  directly exposed server, because that address is the input to every access-log line, rate
  limit and ban that reads it later. Where a proxy *is* configured, the chain is walked
  right to left and the first hop that is not a configured proxy is the client; a left-to-right
  walk returns the value a caller chose. See
  [0020]({{ "decisions/0020-trusted-proxies-and-client-ip/" | relURL }}).
- **S-1.5** A `X-Request-Id` is honoured only when it is short printable ASCII, and is marked
  `fwd-` rather than `gen-`. The header is unauthenticated by construction, so an unvalidated
  value is a caller writing newlines into every log line carrying it.

## S-2 — Terminology and roles

These terms are fixed. `world` and `session` do not exist as game entities, and those strings
appear nowhere in the interface.

- **S-2.1** A **campaign** is a tenant: one content root, one membership list, one live
  tabletop, one gameplay system.
- **S-2.2** A **page** is one `.md` file. All content — prose *and* game objects — is a page.
- **S-2.3** A **game object** is a page with a registered `kind`. A *definition*: durable and
  Obsidian-editable.
- **S-2.4** A **placement** is a game object instance on a live map. **Runtime only.**
- **S-2.5** A **ruleset** is the resolved rule configuration for a campaign: gameplay plugin,
  data pack overlay, and enabled house-rule modules. It has a `ruleset_version`.
- **S-2.6** `campaign_members.role ∈ {gm, player}`. A campaign has at most one live tabletop.
- **S-2.7** `users.is_admin` is instance-level and exists for campaign registration. It is not
  a campaign role.

## S-3 — Content model

- **S-3.1** The **filesystem is the source of truth.** SQLite is a rebuildable index plus
  `campaign_state`. See [0006]({{ "decisions/0006-content-root-is-source-of-truth/" | relURL }}).
- **S-3.2** **Placements are never written to files.** Current hit points, position,
  conditions, and visibility live in `campaign_state`. A damage event must not rewrite a page.
- **S-3.3** `kind` is **registry-backed, not a fixed enum.** A page with no `kind` is prose;
  an unknown `kind` degrades to prose. Validation is opt-in, so a malformed front-matter block
  in a lore page is inert and cannot break the loader.
- **S-3.4** semiplane owns `journal`, `handout`, `index`, `token`, and `scene`. A gameplay
  plugin declares only rules-content kinds. `token` and `scene` being semiplane-owned is what
  keeps the client system-agnostic.
- **S-3.5** Every path in front matter resolves inside the campaign's `os.Root`. **Never**
  `filepath.Clean` plus a prefix check. Obsidian Sync is untrusted input.

## S-4 — Content pipeline

- **S-4.1** The pipeline is **event-driven**, not request-time. Web editor and Obsidian converge
  on one path.
- **S-4.2** Watch **directories, never files**. One watcher serves all campaigns, routing events
  by path prefix. A file watch does not survive an atomic rename.
- **S-4.3** Partial-write safety: per-path debounce with the timer reset on each event, **plus**
  size-stable confirmation across two `stat` samples. Time-debounce alone is documented as
  unreliable. **Zero bytes is not a stable size**: it is the state every in-place write *starts*
  in, so a page holding nothing is re-armed as though an event had arrived and is settled only
  once it has stayed that way for the whole settle budget — and then reported as
  `content.stable_read_timeout` as well as settled, because a blank note is legitimate and a
  stalled writer is not.
  See [0037]({{ "decisions/0037-zero-bytes-is-not-a-stable-size/" | relURL }}).
- **S-4.4** Symlinks in a content tree are **rejected by default**.
- **S-4.5** Watch-limit exhaustion logs an **error**, never a debug line, and falls back to
  periodic full rescan. A missing content root marks the campaign `degraded` and the server
  still starts.
- **S-4.6** **Never `html.WithUnsafe()`.** With a public tier and Obsidian sync as input, this
  is a security boundary, not a style choice.
- **S-4.7** Front matter is attacker-reachable. Parse with alias-expansion limits, or cap
  document size.

## S-5 — Rendering, caching, and secrets

- **S-5.1** Rendered output is **permission-neutral by construction**, with exactly one
  deliberate exception: `[!secret]`.
- **S-5.2** The render cache key is `(campaign_id, path, content_hash, include_secrets)`.
  Validity is by **content hash, not mtime** — sync paths preserve or coarsen mtime, and
  1-second granularity lets distinct writes collide.
- **S-5.3** `ETag = W/"<sha256(content_hash + ':' + include_secrets)>"`. **A GM response and a
  player response must never share a validator.**
  See [0016]({{ "decisions/0016-salted-etag/" | relURL }}).
- **S-5.4** `Cache-Control: private, no-store` on every `include_secrets=true` response.
  A redacted page is permission-neutral *within its tier*, so only the GM variant needs it.
- **S-5.5** Link resolution is Obsidian-compatible: relative path, then basename index within
  the campaign, then within campaigns the viewer can see. **Never probe invisible ones.**
- **S-5.6** **Secret redaction.** A `[!secret]-` body is **absent** from every non-GM response.
  Not `display:none`, not a `hidden` attribute, not a comment, not a CSS class. The callout is
  **removed entirely**; the default is omission, not a stub.
- **S-5.7** Redaction happens **before** sanitisation and before the value reaches any
  template, so no intermediate buffer holds an unredacted copy for a non-GM.
- **S-5.8** Revealing rewrites the file marker `-` → `+`. The **file** is authoritative for
  whether a secret is revealed; `secrets_revealed` is authoritative for **who revealed it and
  when**, and for re-applying a reveal that sync reverted.
- **S-5.9** Secret anchors prefer an Obsidian `^block-id`; otherwise
  `sha256(campaign_id, path, ordinal, first-line-of-body)[:12]`. An unresolvable anchor
  re-associates by ordinal and logs `secret_anchor_drift`. **A reveal is never silently
  dropped.**
- **S-5.10** Sync reconciliation is **capped** (3 passes per file per minute). On exhaustion,
  log `secret.reconcile_capped` as an **error** and **leave the secret hidden**. Every failure
  path resolves toward hiding.
- **S-5.11** **Secret text never enters the FTS index.** `body_plain` excludes callout content
  in *every* reveal state.
- **S-5.12** A cross-campaign wikilink renders as a plain hyperlink and is **never inlined**, so
  the linking page's HTML is byte-identical for every viewer.
  See [0017]({{ "decisions/0017-cross-campaign-links-never-inline/" | relURL }}).

## S-6 — Editing and conflict resolution

- **S-6.1** There is no file lock. Obsidian is a separate process unaware of semiplane.
- **S-6.2** Writes carry `If-Match`. A mismatch returns **412 with the current body and its
  hash, and records nothing**.
- **S-6.3** **No silent overwrite. No last-write-wins fallback.**
- **S-6.4** A successful write appends a `page_revisions` row, writes a temp file in the same
  directory, `fsync`s, and `os.Rename`s. The watcher's own event re-renders and re-indexes; the
  save path forces neither.
- **S-6.5** Content editing is **GM-only**.

## S-7 — Realtime plane

- **S-7.1** Clients send **intent, never state**. The server validates, applies, increments
  `version`, and broadcasts.
- **S-7.2** `version` is the server's monotonic version and the **only** ordering authority.
  Versioning is per-placement, not global.
- **S-7.3** Dice are evaluated **server-side**. The client never supplies a result.
- **S-7.4** WebSocket authentication is **identical to HTTP**, and `Origin` is checked explicitly
  on upgrade.
- **S-7.5** `campaign_state` is written on a trailing debounce of about two seconds after the
  last mutation, and on shutdown. The crash floor is the last debounced state.
- **S-7.6** Presence is ephemeral and **never persisted**.
- **S-7.7** `ruleset_version` fingerprints **resolution semantics** — system ID, the system's
  `RulesetVersion()`, and base and overlay pack versions. **House-rule enablement is excluded**,
  so toggling a house rule does not strand a campaign.
  See [0018]({{ "decisions/0018-ruleset-version-fingerprint/" | relURL }}).
- **S-7.8** On drift, **refuse to resume**. Recovery is an explicit, GM-only,
  confirmation-gated discard that writes to `audit_log`. Never silent.

## S-8 — Access control

| Tier | Scope | Capability |
|---|---|---|
| Instance admin | global | Register campaigns, manage users |
| Campaign GM | one campaign | Full content edit, game control, invite |
| Campaign player | one campaign | Play only; **no content write** |
| Anonymous | `visibility='public'` only | Read-only wiki and public assets |

- **S-8.1** Games always require membership. `public` grants wiki read only.
- **S-8.2** **Every FTS query joins `campaigns` on visibility.** A bare query returns private
  titles to an anonymous user.
- **S-8.3** Assets inherit campaign visibility. A cache entry is **never** shared across
  campaigns.

## S-9 — URL scheme

```
/c/{slug}/wiki/{path...}       page, ETag, cacheable, secret-redacted for non-GM
/c/{slug}/edit/{path...}       editor (GM only)
/c/{slug}/secrets/{path...}    reveal / unreveal, GM only, If-Match
/c/{slug}/assets/{path...}     assets, Range, visibility-gated
/c/{slug}/search?q=            FTS5
/c/{slug}/play                 VTT (members only)   — no session id
/c/{slug}/ws                   WebSocket upgrade
/c/{slug}/events               SSE (editors + live chrome)
/healthz  /readyz
```

- **S-9.1** The slug in the path partitions every cache key by visibility. **There is no
  `{session}` parameter anywhere.**

## S-10 — Plugin system

- **S-10.1** Plugins are **compiled-in Go modules**, registered explicitly in the composition
  root. **No `init()`**, and no runtime loading of third-party code.
- **S-10.2** `Apply` **returns** `[]Mutation` and never applies in place. A plugin therefore
  cannot write `campaign_state` or forge a broadcast. A panic inside `Apply` leaves
  `campaign_state` byte-identical.
- **S-10.3** A UI plugin may only emit operations that some gameplay system already resolves.
  It cannot register a new `op`.
- **S-10.4** Rule code is deterministic: no `time.Now`, no map iteration, no direct
  `crypto/rand`. Enforced by a lint rule and tests — there is no sandbox.
- **S-10.5** House rules are **data-level only**. A module that reorders resolution or
  introduces nondeterminism is rejected. Conflicts resolve first-match-wins by `position`,
  logged, never last-write-wins.
- **S-10.6** A campaign whose `system_id` resolves to nothing **still serves its wiki**. Only
  the game refuses to start, and it names the ID it wanted.
- **S-10.7** System-specific views render **server-side** by templ. A `ClientView` may be added
  later without changing `Derive`.

## S-11 — Search

- **S-11.1** FTS5 external-content table over `pages`,
  `tokenize='unicode61 remove_diacritics 2'`, **deliberately no `porter`** — TTRPG proper nouns
  stem badly. See [0007]({{ "decisions/0007-fts5-tokenizer/" | relURL }}).
- **S-11.2** External-content tables cannot be `ALTER`ed. Bulk changes use
  `INSERT INTO pages_fts(pages_fts) VALUES('rebuild')`.
- **S-11.3** Search is **submit-to-navigate over ordinary HTTP**, not type-ahead. A client-side
  renderer is prohibited: it cannot reproduce the server's tokenisation.

## S-12 — Observability

- **S-12.1** `slog` with a stable `event` key is the only observability mechanism. No metrics
  server, no tracing stack. Counters are exposed on `/readyz` as JSON.
- **S-12.2** `watch.add_failed`, `content.render_error`, and `secret.reconcile_capped` are
  **never below error level**. The third is security-relevant: it means the system *chose* to
  hide something.
- **S-12.3** Every event carries `campaign_id` and, where applicable, `path`. **No event
  carries secret content, file contents, or dice results.**

## S-13 — Accessibility

The interface contract is in the
[UI specification]({{ "design/ui-ux/" | relURL }}). The requirements that cross into the server:

- **S-13.1** Target is WCAG 2.2 AA, with AAA claimed only where stated.
- **S-13.2** The map canvas is `aria-hidden="true"` and a **decorative mirror**. The accessible
  representation is the token list in the rail. `role="application"` is prohibited.
- **S-13.3** Live regions: `role="status"` / `polite` for state changes; `role="alert"` /
  `assertive` for a 412 conflict, a secret reveal, and reconcile-capped.
- **S-13.4** **No live region on the search route, and search responses carry an `ETag`.**
- **S-13.5** The document never varies by the theme cookie. **No `Vary: Cookie` is emitted.**
- **S-13.6** Search is not a Datastar surface and the map is not a Datastar element.

## S-14 — Validation

The gate is `make ci`. Beyond that:

- **S-14.1** **Secret redaction is a release-gating test.** For a page containing a secret: the
  GM response contains the body; the player response does not; the anonymous response does not;
  and neither contains the secret text **anywhere** — headers, comments, JSON payloads, or HTML
  source.
- **S-14.2** A GM response and a player response never share an `ETag`.
- **S-14.3** A private page body never appears in a public response.
- **S-14.4** `player` `PUT` → 403. `anonymous` `PUT` → 401 or 404.
- **S-14.5** Two clients issuing concurrent intents converge with monotonic versions.
- **S-14.6** Identical `(state, intent, seed)` yields identical mutations across 100 runs.
- **S-14.7** A page with a `kind` no plugin registers degrades to prose, content intact.
- **S-14.8** A campaign with an unknown `system_id` refuses the game and **still serves the wiki
  200**.
- **S-14.9** A system sharing nothing with 5e passes the conformance suite **without touching
  `internal/domain/rules`**. This is the test that proves the plugin system generalises.