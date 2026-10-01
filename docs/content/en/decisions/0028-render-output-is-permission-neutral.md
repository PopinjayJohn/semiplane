---
title: "0028 — Rendered output is permission-neutral: raw HTML is never interpreted, and a block-level element loses its text"
description: "goldmark runs without `WithUnsafe()` and bluemonday is an allowlist, so two independent layers stand between a vault and a reader's DOM."
lede: "S-4.6 forbids `html.WithUnsafe()` and this is what compliance looks like in practice — including the part nobody likes, which is that a GM who pastes HTML from a web page loses the paragraph. That is CommonMark behaviour, and an accident nobody wrote down would be worse than the behaviour."
weight: 218
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

S-3.1 makes a campaign's vault untrusted input: it arrives by Obsidian Sync, from shared vaults,
from community plugins, and from whatever device last synced. So every byte of a page is
attacker-reachable by a GM who was persuaded to accept a shared vault, or who installed a plugin
unread. S-4.6 therefore forbids `html.WithUnsafe()` and calls the result a security boundary rather
than a style choice.

Compliance is not a setting. It is a pipeline with two independent layers, and the interesting
decisions are the ones where a layer is *tighter* than the obvious implementation would be.

## Decision

**Layer one: goldmark never interprets raw HTML.** The renderer does not call
`html.WithUnsafe()`. A raw HTML element in a markdown file is therefore never converted into markup
and never reaches a sanitiser as markup.

**Layer two: bluemonday, as an allowlist narrowed from an empty policy.** Every element and
attribute absent from the policy is removed, so an element nobody thought of is removed too. A
denylist has to be updated when the attack surface changes and nothing in this repository is in a
position to know when that is.

The second layer exists so a future change to the first is not immediately a stored XSS against
every public campaign. It is defence in depth, not redundancy for its own sake.

Four narrowing decisions are this project's rather than the library's:

- **`class` survives for exactly five values** — the three extension classes on `a`/`span`, goldmark's
  three footnote classes, and `language-<token>` on `code`. An author **cannot** invent a class that
  does anything: behaviour is bound to a *selector* in semiplane's own stylesheet, and an author
  cannot write a selector. A class of their choosing matches no rule. There is no
  attacker-reachable class that triggers behaviour, because there is no behaviour to trigger.
  `data-testid` is refused outright: a vault must not be able to name a test hook.
- **`style` survives nowhere.** The most dangerous attribute on any allowlist, and it buys nothing,
  because a page's appearance is the design system's. goldmark's table extension would have emitted
  `style="text-align:…"` for alignment, so alignment is carried by `align` against bluemonday's
  `CellAlign` instead. Losing `style` silently would lose table alignment silently, which is why a
  test asserts both halves.
- **`AllowDataAttributes()` is not called.** It allows every `data-*` on every element, and a
  `data-` attribute is inert until some code reads one — so the failure mode is that a later phase
  adds a client that reads one and vault content starts driving that client. Four names are allowed
  explicitly, which makes adding a fifth a reviewable change.
- **`id` is allowed, `name` is not, and authored ids are not enabled.** `id` must survive or every
  anchor link and `[[Page#Heading]]` is dead. goldmark's generator de-duplicates within a document
  (`the-coast`, `the-coast-1`), so uniqueness is a property of the *generator*; a post-pass would
  have to re-derive that numbering. `parser.WithAttribute` is **not** enabled, so `## Heading
  {#custom}` is literal text rather than an author-chosen id, which would make the anchor map the
  author's arithmetic instead of the generator's and plant an id of the vault's choosing.

### The consequence nobody likes

A **block-level** raw HTML element in a markdown file — `<p>text</p>`, `<h1>heading</h1>`, a
hand-written `<table>`, a `<script>` — is an *HTML block* in CommonMark terms. goldmark does not
convert it into the document AST; without `WithUnsafe()` it emits `<!-- raw HTML omitted -->` in
its place, and the element's text is never seen again.

An **inline** raw element is different: `a <span>b</span> c` is parsed, the text survives, and the
sanitiser strips the tag.

So the rule a GM needs is that prose goes in markdown, not in HTML. That is not a quirk of this
implementation — it is what CommonMark specifies, and what Obsidian and GitHub do. What would be
unacceptable is for it to be an accident nobody wrote down, so
`TestRawHTMLBlockTextIsDropped` states it with the inline case beside it, since that distinction is
the useful part.

**Recovery is not attempted.** Parsing an author's HTML well enough to extract its text is what
`WithUnsafe()` does, and it is the thing being refused.

### `{{dice}}` renders a placeholder

`{{dice}}` emits an empty element carrying the *expression* in an attribute
(`data-arg="1d20+5"`), so no number exists anywhere in the output. A computed result would differ
per request, be uncountable, and put a random number into a cache keyed on the content hash
(S-5.2). Dice are rolled server-side; architecture §7 and the audit trail both depend on it.

## Consequences

- A GM who pastes HTML from a web page finds their paragraph gone. They will report it. The answer
  is in this record, in the policy's package comment, and in a named test.
- The sanitiser is the **second** layer, and the layer that loses the text is the first. A comment
  that attributes the loss to the policy is wrong, and one did, until it was probed: `AllowNoAttrs`
  does not prevent `<p>text</p>` becoming `text`, because that text is gone before the policy runs.
  The reason for preferring `AllowNoAttrs` to `AllowElements` is different and is stated where the
  call is.
- Residual risk, accepted and bounded: `id` allows DOM clobbering by a heading-text-shaped id.
  Bounded because a clobbered name has to match a name semiplane's own script reads, and the
  element it can shadow must already be one the page contains.
- The extension parser sits at priority 150, **not** "low". goldmark's link parser sits at 200 and
  always returns a node for `[` and `!`, so a parser behind it is never called — and the symptom is
  `[[Page]]` rendering as the author's own brackets, which looks like working software.
- `extension.GFM` is decomposed into its four parts rather than registered whole, because it takes
  no options: registering it *and* a configured table renderer puts two renderers on one node kind
  and the winner depends on an unstable sort. Removing the possibility was cheaper than reproducing
  it.
- Because `ext` cannot import `internal/content`, the wikilink label rule exists in both packages,
  held in place by `TestLabelAgreesWithTheLinkLayer` rather than by discipline.

## Alternatives considered

**`html.WithUnsafe()` plus a sanitiser.** Rejected by S-4.6, and correctly: with a public tier and
Obsidian sync as an input, it is a security boundary. Worth noting that the sanitiser would *not*
have saved it, because the defect this guards is in what the renderer emits rather than in what the
sanitiser removes.

**Sanitise, do not suppress.** A defensible alternative: enable `WithUnsafe()`, then let bluemonday
strip what it does not allow. It is what many projects do and it is one layer instead of two. It was
rejected because the first layer's correctness then depends entirely on the second's completeness,
and because a raw HTML *block* still loses its text with unsafe rendering on, so the alternative
does not even buy back the content — it only buys back the markup that the sanitiser would strip.

**`UGCPolicy()` narrowed, rather than a fresh policy.** Rejected: bluemonday has no "remove element"
call, so narrowing a base means re-deriving its attribute rules anyway. Inheriting a library default
and then unpicking it is worse than stating the list.
