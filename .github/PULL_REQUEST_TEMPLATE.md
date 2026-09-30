<!--
Every section below is a question the reviewer would otherwise have to ask in a
comment. Delete nothing except the guidance italics; an unanswered box is a
better prompt than an absent heading.
-->

## What and why

<!-- Two sentences. The *why* is the part descriptions usually omit, and the part
     that makes a change reviewable. What it does is visible in the diff. -->

## Phase and branch

<!-- Which phase this belongs to, and the branch. A work-item branch is cut from
     its phase branch, never from main. State any work item you are dropping, and
     why — a silently dropped work item is an incomplete phase, not a detail. -->

- Phase:
- Work item (e.g. `P6/W3`):
- Dropped, with reason: <!-- or "no" -->

## Related issue

<!-- Closes #123, or "not user-visible" if there is no issue. State the
     no-issue case explicitly so a reviewer can tell "forgot" from "none". -->

## Requirement IDs touched

<!-- Spec IDs from docs/content/en/contributing/spec.md, e.g. S-5.6.1. An ADR
     for this change? Link it. "Neither" is a valid answer and a useful one —
     it means the choice was already settled by an existing record. -->

- Spec IDs: <!-- or "none" -->
- ADR: <!-- or "none, already settled" -->

## Gate

<!-- `make ci` is green. `make site-check` if docs/ changed,
     `make lint-workflows` if .github/workflows/ changed,
     `make labels-check` if labels or a form changed. -->

## Design impact

<!-- Tick every row this change touches. These are the expensive-to-undo surfaces;
     each one is irreversible in a way an ordinary refactor is not. -->

- [ ] Data model — a new table, column, or migration. Migrations are forward-only:
      a shipped migration is never edited, only added to.
- [ ] URL scheme, WebSocket protocol, or the intent vocabulary.
- [ ] The `System` interface, or what a plugin is permitted to do. A UI plugin must
      never be able to write `campaign_state`; it dispatches intents that a gameplay
      system resolves.
- [ ] `[!secret]` handling, or anything a non-GM viewer can receive.
- [ ] Access tiers, the render cache key, or the `ETag`. The body is permission-neutral
      by construction; `include_secrets` is the only exception, and the validator is
      salted with it.
- [ ] A dependency, the build, the gate, or the CI matrix.
- [ ] None of the above.

## Security

<!-- One line: did this touch path confinement (`os.Root`), sanitisation
     (`html.WithUnsafe()`), authorization, or secret redaction?

     If yes, name the test that covers it. For anything touching secrets, the
     redaction test is the one that must be green: it asserts that a non-GM response
     contains no secret text anywhere — not in the HTML, not in a comment, not in a
     header, not in a JSON payload. -->

## Accessibility

<!-- If any markup, CSS, or templ changed: which of the §10 checks did you run, and
     what did they assert? The structural tests are gate-blocking — exactly one <h1>,
     landmarks present and distinctly labelled, no positive tabindex, every skip
     link first in tab order. -->

## Agent assistance

<!-- This repository is agent-driven. State plainly whether an agent produced the
     change and which instructions it followed — AGENTS.md, a skill under
     .kilo/skills, or neither. An agent-authored change that skipped the
     quality-gate skill is exactly what this section exists to surface. -->

## For reviewers

<!-- What you want looked at hardest, and what you deliberately left out of scope. -->
