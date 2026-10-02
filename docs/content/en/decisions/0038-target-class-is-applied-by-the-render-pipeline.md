---
title: "0038 — The `.target` class is applied by the render pipeline, after the sanitiser"
description: "semiplane adds one attribute to sanitised output rather than removing one, and it is safe because the value is a constant it owns, the element set is fixed, and the pass runs on a tree parsed from bytes the sanitiser already approved."
lede: "Every other decision on the render pipeline removes something. This one adds, which is the part worth arguing, and the argument is four properties rather than a promise: `target` is a literal in `internal/content`, the element set is derived from the sanitiser's own list, the value resolves to two minimum sizes and no behaviour, and the pass parses a tree the sanitiser emptied rather than matching the markup with a regexp."
weight: 228
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

**Renumbered.** This record was drafted as 0037 and is published as 0038. Two
independent work items were briefed to write a record without knowing of each
other, and both took 0037; the settle filter's
[0037]({{ "decisions/0037-zero-bytes-is-not-a-stable-size/" | relURL }}) holds the
number. `AGENTS.md` says a number is never reused and never renumbered, which
means a collision is resolved *before* either merges rather than by whoever
happened to merge first — and the surviving number is not re-derived from the
order work happened to land in. This record moved; 0037 did not.



UI §7.3 makes target size "enforced by construction", and says what the construction is: a shared
`.target` utility setting `min-inline-size` and `min-block-size` from `--target-min`, used by every
interactive element — **including plugin components** (§4.11.1). UI §10.6 makes it a gate: "grep
rendered markup for interactive elements missing the class, including plugin output".

`internal/web/static/css/shell.css` is the utility, and the shell's own chrome carries the class
because it is templ. The gate is
`TestEveryRouteCarriesTheTargetClassOnEveryFocusStop` in `internal/httpapi/wiki`, and when it was
written — on this branch's base — it failed on five focus stops inside
`div[data-testid=page-body]`, all in the one document a reader is most likely to read:

```
a.wikilink                       internal/content/ext, on [[Greyhaven]]
a                                goldmark's link renderer, on [the vault](…)
a.footnote-ref                   goldmark's footnote extension
a.footnote-backref               goldmark's footnote extension, inside the footnotes list
a                                goldmark's linkify extension, on a bare https:// URL
```

None of them is fixable in a `.templ` file. Three come out of renderers this repository does not
write, one out of `internal/content/ext`, and they all land in the page body — which
`components.WikiArticle` inserts as a single `@templ.Raw`. A wrapper can put a class on the
*container*; it cannot reach an element a renderer emitted inside it. So the class has to be applied
to the bytes, and the bytes are only ever whole at one point in the pipeline.

{{ "decisions/0028-render-output-is-permission-neutral/" | relURL }} is the record for that
pipeline, and everything in it removes something: two layers, both subtractive, and the whole of
`policy.go` reads as *what is not on the list does not reach a reader's browser*. This decision is
the exception, and an exception on the one surface `AGENTS.md` calls a security boundary needs its
own record.

## Decision

**`internal/content/target.go`'s `applyTargetClass` runs at the end of `Renderer.Render`, after
`policy.Sanitize`, and adds `class="target"` to every focusable element in the page body.**

### Why after the sanitiser, and not before

Because the class is not on the `class` allowlist, and it must not be. A pass in front of the
sanitiser would have the sanitiser take the token straight back off again, which is a pass that
cannot work and a gate that stays red. After, the sanitiser is still the last thing that decided
which elements and which attributes exist; this pass can add a class and nothing else.

`internal/httpapi/wiki`'s address attachment already runs after the sanitiser, for the same shape of
reason, and this record is consistent with it: everything between the sanitiser and the browser
writes markup the policy would also have allowed.

### Why it adds, and why that is safe

Four properties, each structural rather than a promise:

- **The value is a fixed constant semiplane owns.** `targetClass` is a literal in `target.go`.
  Nothing a vault can reach participates in it, so the only token this pass can add is that one, and
  it cannot be shaped by an author.
- **The element set is fixed**, and written out rather than computed, so the pass cannot reach an
  element the policy did not already allow and a reader can see the whole set without reading a policy.
  §10.6's list includes "anything with a `tabindex`", which is not an element name, so it could not be
  enumerated anyway; and a list nobody has to keep is the entire point of the exercise.
- **The value is a class name, not a behaviour.** `.target` resolves to two minimum sizes in
  semiplane's own stylesheet. This is {{ "decisions/0028-render-output-is-permission-neutral/" | relURL }}'s
  argument for allowing `class` at all — an author cannot write a selector, so a class of their
  choosing matches no rule — turned round: semiplane's own class cannot be triggered by anything.
- **It runs on a parsed tree, not on markup as a string.** `golang.org/x/net/html` parses the
  sanitised fragment, the pass mutates the tree, and the tree is re-rendered. See below.

### Why a parsed tree, and not a regexp

A string pass would have to *promise* it cannot resurrect what the sanitiser removed. A parsed tree
cannot: it is built out of bytes that already contain the answer, so a `<script>` is not "a script
element with its content stripped" that a later match might find — it is **absent**, and there is no
code path in the pass that puts one back. The difference between a promise and a property is the
whole of the reason this is not three lines of `strings.Replace`.

The reparse is safe for the same reason it is necessary. HTML parsing has error recovery, and the
places where recovery changes structure — foreign content in `<svg>`/`<math>`, raw text in
`<script>`/`<style>`/`<noscript>`/`<textarea>`, `<template>` contents, misnested table cells — all
live *inside* elements `policy.go` removes outright. With none of them present the tokenizer sees only
the phrasing and flow content the policy allows, and the tree it builds is the tree the sanitiser
saw. `TestTheTargetPassCannotResurrectSanitisedMarkup` asserts each of those removals holds after the
pass, and `TestTheRenderedBodyIsTheSanitisedBodyPlusOneTokenPerFocusStop` renders **every** golden
document through the pipeline with and without the pass and compares the two documents attribute by
attribute — which is this argument stated over the markup goldmark, `ext` and bluemonday actually
produce, rather than over markup a person imagined.

The parse is a **fragment** parse, in a `body` context. A document parse would wrap the page body in
`<html><head><body>`, which is a nested document inside a templ shell and a stylesheet that stops
applying; `TestARenderedPageBodyIsAFragmentAndNotADocument` is the gate on that, and it is the half
of the confinement claim that can actually fail (see *Consequences*).

The context is named `body` rather than left to `ParseFragment`'s `div` default, which is documentation
rather than a fix: `body` and `div` were measured to parse identically for every input a sanitised
page body can contain. It is written down as documentation *because* an earlier draft of the code
commented that `div` "would drop a `<li>`", which is not true, and a comment a reader has to go and
disprove is worse than one that says what was measured.

### Why `target` is deliberately absent from the `class` allowlist

Adding it to `extensionClasses` would look like it fixed something and it would not. An author
writing `class="target"` achieves nothing either way, because goldmark drops raw HTML before the
sanitiser sees it, so the class could only ever have come from the pipeline. It would also be a
second source of truth for one class — and `policy.go`'s own comment on `extensionClasses` calls that
list a maintenance obligation: a new extension adds its class there. A class applied by the pipeline
needs no entry.

So the class is *semiplane's, applied by semiplane*, and the allowlist stays an allowlist of values
the renderer emits.
`TestTheTargetClassIsNotOnTheClassAllowlist` asserts the absence, and it asserts it **through the
policy** rather than through a render, because no output assertion can distinguish a policy that
forbids `target` from one that allows it and never uses it.

### The pass reaches every `<a>`, including one with no `href`

§10.6's gate tests `a[href]`, because by the time a reader has the document every surviving anchor
has an address. Inside the render pipeline that is not yet true:
{{ "decisions/0017-cross-campaign-links-never-inline/" | relURL }} requires rendered output to be
permission-neutral, so `Renderer` emits
`<a class="wikilink" data-ext="wikilink" data-ref-index="0">` with **no** `href` at all, and
`internal/httpapi/wiki` writes the address in afterwards. An `href`-gated pass would skip every
wikilink in every campaign — three of the five the gate reported.

Being a **superset** of what the gate can see is the safe direction for that mismatch. The extra
elements are ones that will not be focusable: a `span` an address refusal turns a wikilink into, an
`<img>` an asset embed becomes, an `<a>` whose `href` the URL policy removed. `min-inline-size` on an
inline element computes to nothing, so a `target` class on one of those is inert. A set *narrower*
than the gate's is the unsafe direction: a live focus stop left below 44px.

### Idempotence, and why it is not an optimisation

The token is checked before it is written, and a class list that already holds it is left exactly as
it was. The render is cached (S-5.2) and a miss re-renders, so the pass never sees its own output —
but "the render is deterministic" is a claim about this package being careful, and a class list that
grew `target target target` would be an accumulating bug in the one place where accumulating is
visible in the bytes a reader gets.

The same check is what makes the *author-written* class a non-event: a fragment that already carries
`target` comes back unchanged rather than re-serialised.

### Confinement to the page body

`applyTargetClass` is **unexported**, `Renderer.Render` is its only caller, and `Rendered.HTML` is
its only argument. The shell's chrome is templ, carries the class by construction, and never passes
through here. That is structural: there is no exported API that runs this over a document, so
ADR 0038's confinement claim is enforced by the compiler rather than by discipline.

### One byte difference, stated rather than hidden

The pass re-renders through `html.Render`, which writes a void element as `<hr/>` where the sanitiser
writes `<hr>`. That is HTML5's canonical serialisation and every browser reads it identically. It is
mentioned here because it moved the golden files, and a record that does not mention why six fixtures
changed is a record that leaves a reviewer to find out.

## Consequences

- **§10.6's gate is green for the page body**, and it was red for five focus stops on a page a GM
  would write in ten seconds. That is the defect this closes.
- **A plugin rendering through this pipeline gets the class for free**, which is the mechanism §4.11.1
  means by "a plugin component inherits `.target`". A plugin that emits its markup through
  `internal/content` is covered by the gate without knowing this record exists.
- **Confining the pass to the page body means the shell is not covered by it**, and that is correct
  rather than a gap: the shell is templ and its own audit in `internal/httpapi` already walks it.
  What is worth knowing is that this confinement is not observable in the *output*, because an
  idempotent pass run over a shell that already carries the class changes nothing a reader can see. The
  falsifiable half is the input — the unexported function, and
  `TestARenderedPageBodyIsAFragmentAndNotADocument` against the document-parse mutation.
- **The `input` element gets the class**, even though a GFM task-list checkbox is
  `disabled` and therefore not focusable in a browser. §10.6's list says `input`, the gate counts
  `input` without reading its `disabled`, and a `min-block-size` on a picture of a box is harmless. A
  pass that disagreed with the gate about an inert element would be a pass maintaining a second opinion
  about what is interactive.
- **A `target` class can survive on a `span`.** An address refusal rewrites a wikilink's `<a>` into a
  `<span>` with the refusal's sentence in `title`, and it keeps the class. §10.6 is about interactive
  elements, a `span` is not one, and the class is inert on it. The alternative — stripping the class at
  refusal time — is a second pass over markup the route is already rewriting, for no reader-visible
  gain.
- **`internal/content` re-serialises the page body.** Six golden files changed. Any *other* assertion
  in this repository that matched a `class` attribute as an exact string had to be taught to match a
  class **token** instead; that is a correctness improvement, and it is the same distinction
  `route_a11y_test.go` makes when it splits on whitespace rather than using `strings.Contains`.
- **The set is written out and will drift from §10.6.** The `tabindex` branch exists because §10.6's
  list has one and an element map cannot. Three copies of the focus-stop set now exist — `target.go`,
  `route_a11y_test.go`, `target_test.go` — and the right fix is one exported audit package imported by
  `httpapi_test`, `wiki_test` and `assets_test`, which `route_a11y_test.go` already names as its own
  eventual answer and which is not this change's to build.

## Alternatives considered

**Add `target` to the `class` allowlist in `policy.go`.** The obvious move, and wrong twice over. It
does not fix the defect — the five elements fail because nothing writes the class, not because the
sanitiser strips it, and an author cannot write it either. And it makes the class author-reachable in
principle and semiplane-owned in fact, which is the wrong direction for a boundary. It is also the
second-source-of-truth problem `extensionClasses`'s own comment warns about: a list that has to be
kept in step with the code that writes the values.

**A regexp pass over the sanitised HTML.** Cheaper, and it is what a first draft of this looks like.
Rejected because "cannot resurrect what the sanitiser removed" would be a comment rather than a
property: a regexp that adds a `class` before the first `>` of a tag has no notion of which bytes are
markup and which are text, so the safety claim rests entirely on nobody later running it over the
wrong thing. The tree is not slower in any way that matters — a page body is kilobytes and the pass
does not run at all when it has nothing to write.

**Fix it in `components.WikiArticle`'s template.** Impossible: the page body is one `@templ.Raw`, and a
templ component cannot add an attribute to an element a renderer emitted inside it. Named here because
it is the first thing a reader of this record will try.

**Run the pass in `internal/httpapi/wiki`, after the addresses are attached.** It would see the final
document, with every `href` in place, and could use §10.6's rule exactly as written. Rejected: it
puts a third post-sanitisation pass in the transport layer, it breaks the confinement to a single place
(`internal/content`), and a plugin rendering through the pipeline would not get the class — which is
the mechanism §4.11.1 asks for.

**Strip `.target` from an element that the address attachment turns into a `span` or an `img`.**
Rejected as a second pass for no reader-visible gain, and noted under *Consequences* rather than
implemented.

**Widen §10.6's gate to exclude the page body, on the grounds that author markup is the author's
business.** Rejected. §10.6 says "including plugin output" precisely because the plugin layer's markup
is in the same tree as everyone else's, and the gate's own message names the owner of a finding for
exactly this reason — a focus stop the shell wrote is `internal/web`'s, and one inside
`div[data-testid=page-body]` is the render pipeline's. The gate was right and the pipeline was
missing a step.