---
title: "Roadmap"
description: "The twelve build phases, in the order they will actually be built."
lede: "Sequenced by dependency, not by effort. Each phase leaves a running server, and phases 7 to 9 can be reordered or deferred without breaking what came before."
weight: 35
---

This is a summary. The
[architecture record]({{ "design/architecture/" | relURL }}) owns the reasoning;
this page exists so you can see the shape without reading 900 lines.

## Phases

| # | Phase | Lands |
|---|---|---|
| 1 | **Foundations** | The middleware chain; config for the SQLite path and content-root base; the `admin create` CLI; the migration skeleton. |
| 2 | **Identity and tenancy** | Users, auth sessions, campaigns, membership. Auth middleware, campaign registration, and `os.Root` creation. |
| 3 | **Content read path** | Path confinement; front matter and `kind` discrimination; markdown render and sanitise; wikilink and embed extensions; the render cache; `ETag`. |
| 4 | **Watcher** | Directory watches, debounce plus size-stable confirmation, full-text index maintenance, degraded mode, campaign-state schema. |
| 5 | **Wiki surface** | The templ shell and chrome; wiki and edit routes; the `If-Match` write path; the conflict view; change notification; search; assets with range requests. |
| 6 | **Realtime** | In-memory campaign state; the hub; the protocol codec; placements versus definitions; GM-only operations; debounced persistence; resume by version. |
| 7 | **Plugin contracts** | The system, intent and mutation interfaces; the registry, wired explicitly in the composition root; UI page-type and render-hook registration. |
| 8 | **First systems** | The 5e engine and base pack, then the 2014 and 2024 overlays and their hooks; two reference UI plugins to prove both contracts are usable. |
| 9 | **House rules** | The rule-module table, ordered application, and ruleset-version gating on resume. |
| 10 | **Client** | The map canvas; live chrome over a reactive transport; the stylesheet build; the theme layer served from content. |
| 11 | **Secret callouts** | The `[!secret]` extension, the two-variant render, the cache key, the GM-only reveal endpoint with `If-Match`, the reveal ledger, bounded sync reconciliation, search exclusion. |
| 12 | **Hardening** | The security tests, race coverage, failure-mode tests, and the determinism lint rule. |

## What already runs

`/healthz`, `/readyz`, the middleware chain, and the security headers. There is no
campaign, no content, and no interface yet.

## Why this order

**Each phase is independently runnable.** The server starts and answers
`/healthz` from phase 1 onward, so there is never a period where the tree does
not run.

**Phases 7–9 are separable.** A build with no gameplay plugin still serves the
wiki, and a campaign whose `system_id` no longer resolves degrades to wiki-only
rather than failing to boot. The plugin system is additive to the product, not a
prerequisite for it.

**Secrets come last because they need everything underneath.** The feature
refines an already-working render pipeline, and it needs access control (phase
2), the watcher (phase 4) and conflict resolution (phase 5) in place first. It is
also the highest-value test in the project, and a security test needs a working
pipeline to be worth anything.

## The open questions

Deliberately deferred, each with a default chosen for a first release:

1. **Cross-campaign wikilinks** — resolution already respects the viewer's
   permissions. Whether authors should be able to *write* them is a product
   decision. Default: allowed within campaigns the viewer can see.
2. **Storage beyond local disk** — maps are tens of megabytes. Local disk with
   range requests is the self-hosted answer; there is a seam for
   S3-compatible storage rather than a built-in integration.
3. **Dice expression scope** — expressions only, no system-specific semantics.
   The seed arrives through the call context rather than being fetched, so a
   per-game seed and roll log are auditable at no extra cost.
4. **Player wiki editing** — settled as GM-only. If a group wants shared notes,
   Obsidian already covers it, and an `editor` role can be added to the enum later
   without a migration.
5. **Ruleset-version drift on resume** — the policy is to refuse rather than
   silently misresolve. Default: refuse, because silently misresolving past game
   state is worse than an extra click.
6. **Plugin API stability** — plugins are compiled in, so there is no semver
   contract to honour. If they ever move out of tree, the intent, mutation and
   kind types need versioning first.

## How to influence it

Open a [feature request]({{ site.Params.repo }}/issues/new?template=02-feature.yml).
The design-impact checklist in that form is what routes a request to
`status: needs-design` instead of straight into work — which is the point. The
cheapest thing you can do for your own timeline is to write down a constraint the
proposal has to respect.
