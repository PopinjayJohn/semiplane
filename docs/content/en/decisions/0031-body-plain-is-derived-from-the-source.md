---
title: "0031 — `body_plain` is derived from the source, never from rendered HTML"
description: "The searchable text is flattened from the file's own bytes, with `[!secret]` callouts excluded in every reveal state, so a search snippet can never carry secret text to a player."
lede: "S-5.11 promises that secret text never enters the FTS index. Keeping that promise means choosing where the searchable text comes from — and every convenient source is the wrong one."
weight: 219
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

`pages.body_plain` is the searchable text, and it is the *redacted* text: S-5.11 says it excludes
`[!secret]` callout content in **every** reveal state, and migration 0007 records the same promise in
the column's own comment. It is not an optimisation. A search snippet is built from it
(`snippet(pages_fts, 1, '', '', '…', 12)`), which means a snippet is the one place a secret text can
reach a player without ever passing through a template's `include_secrets` decision.

That makes the *derivation* a security decision, and there are three plausible sources for it:

1. **The file's source text.** Flatten the markdown ourselves: strip front matter, strip syntax, strip
   the callouts.
2. **The rendered HTML.** The renderer already produces clean text — the work is done, and the result
   is exactly what a reader sees.
3. **The redacted source.** Run the redactor, then flatten.

The second is the one a reasonable implementer picks, and it is wrong in three separate ways that
compound.

**Rendered output is permission-variant by construction.** ADR 0028 fixes rendered output as
permission-neutral *except* for `[!secret]`. So "the render" is not one document — it is two, selected
by a flag that travels in from the request tier. A derivation that reads "the render" has to pick
one, and every way of picking is a bug: hard-coding `include_secrets=false` throws away half the page,
hard-coding `true` indexes the secret, and reading it from the caller means the index's correctness
depends on a value that arrives from a session cookie two layers away. The index is written by the
watcher, which has no viewer at all.

**The renderer's buffers are the wrong place to read text out of.** ADR 0029's argument is that
redaction runs *before* the render so that no buffer downstream of it holds an unredacted copy for a
non-GM — and the buffers between the file and the HTML are three of them: goldmark's, bluemonday's,
and the cache's. An index derived by rendering would be built by constructing one of those buffers
with the unredacted text, which is the exact thing ADR 0029 says never happens for a non-GM. The
guarantee is about *downstream of the redactor*; rendering to derive the index is upstream of it.

**A rule checked against rendered text is checked against two documents.** S-5.11 is a claim about a
column. A test can read the column and assert a secret is absent from it. A test cannot assert the
absence of a leak from a pipeline it did not run — and if the indexer renders, then the property that
no secret reached the index is only as good as whatever renderer's behaviour happened to be on the
day, including a plugin's extension and the sanitiser's allowlist.

There is a fourth option worth rejecting explicitly: index the raw source and let the *search* filter
secrets. That puts the exclusion at read time, which means every future query has to remember it, and
it means the secret text is in the FTS index — which is a file on disk, dumpable by anything that can
read the database, and the schema's comment says one column exists precisely so there is nowhere else
for it to be.

## Decision

`body_plain` is derived from the **file's source text**, by a flattening that runs in
`internal/content/index.go`, and that derivation excludes `[!secret]` callout content outright.

Four properties follow, and each is a consequence rather than an extra rule:

**Only the body is indexed.** Front matter is stripped. A front-matter value can be a path (S-3.5) and
the index does not need the host's directory layout to be searchable; and what a front-matter field is
worth to a search is not knowable from here, because a plugin's fields arrive in phase 8.

**The exclusion is structural, not conditional.** It is not "exclude the callout when it is hidden" —
which would require reading the fold marker, getting the reveal state wrong, and re-indexing on every
reveal (S-5.8 says the file records the state, so the index would follow it). It is "a line carrying
the marker excludes the callout", full stop. There is no reveal state to consult, which is why it
holds in both, and why the schema needs no second column for the revealed variant.

**Every branch of the exclusion fails toward hiding.** A `[!secret]` marker inside a blockquote
excludes the rest of that blockquote. A marker anywhere else — in prose, in a fenced block — excludes
everything below it, because nothing marks where a secret stops and guessing is the direction that
leaks. The cost is that a page *documenting* the callout syntax loses the text after it from search.
That is the right trade: the alternative is a heuristic that has to be right about an author's
intentions.

**The marker test runs on every line, before and independently of any fence tracking.** This is the
property that makes the implementation safe rather than merely careful. Code-fence state tracking is
approximate — three characters of marker, not the run's length — and a fence that is opened and never
closed would, in any implementation where fence state gates the marker test, cause a `[!secret]` to be
indexed. Two independent concerns, two independent loops, so that a mistake in one cannot reach the
other.

Markdown is flattened by a sequence of regular expressions in this file rather than by the renderer.
That is a second, deliberately non-authoritative implementation of markdown structure: it is a *word
extractor*, not a parser, it is never used for display, and it has no privileges. What survives is the
content of the constructs — a reference contributes its alias or its target's name, a link its text, an
image its alt text — because a wiki that cannot be searched by `[[Svartalfheim]]` is a wiki nobody
uses. What does not survive is anything carrying a path, because S-3.5 makes every path
attacker-reachable and the index has no need of one.

An empty result is a legitimate outcome: a page whose whole body is one secret callout indexes to no
body text, and is still findable by its title. That is the point of S-5.11's cost — **secrets are not
full-text searchable**, and a secret-aware index would be an additive later change rather than a
replacement for this one.

## Consequences

- **Phase 10 changes nothing here.** The redactor removes `[!secret]` for a non-GM at render time;
  this removes it from the index at write time. The two are independent, and P10 does not have to know
  this file exists.
- **A reveal does not re-index.** S-5.8 says revealing rewrites the file's marker from `-` to `+`, and
  the watcher's own event re-indexes the page — which produces the same row, because both markers
  exclude the same content. A GM revealing a secret costs a no-op write rather than a reindex that
  would add the secret to the index.
- **A later phase that wants to search secrets must be additive.** The cheapest correct shape is a
  *second* FTS table over a *separate* store of the revealed text, gated by a column that records
  which pages have secrets. That is a new table and a new query, not a change to `body_plain`, and
  this decision is what keeps it that shape rather than a variant of one column.
- **The index is not a rendering.** Nothing downstream may treat `body_plain` as prose for display: a
  snippet from it has no sentence structure a template can rely on, and the search statement's empty
  excerpt markers are what keep it plain text rather than markup.
- **`skipDirectory` also became a dot rule.** Not this record's subject, but the same reasoning: an
  explicit list of `.git`, `.obsidian`, `.trash` was an enumeration of one fact, and it is now stated
  as that fact, which is what let the incremental path and the walk share a single predicate.
- **The test asserts a positive.** `body_plain` excluding secrets is satisfiable by indexing nothing,
  so the exclusion test also asserts that ordinary prose on both sides of the callouts *is* indexed.
  A test that only asserted the absence would pass against an index that stored nothing at all.

## Alternatives considered

**Derive `body_plain` from the rendered HTML.** Rejected for the three reasons in Context, and the
first is sufficient on its own: there is no single rendered document to derive from. It is also the
option that would let a plugin's render extension change what is searchable, which is not a property a
search index should have.

**Derive it from the redacted source** — run the `Redactor`, then flatten. Rejected because
`content.NoSecrets()` removes nothing until phase 10, and a derivation that called it would be correct
today and silently index every secret the day it stopped being a pass-through. Worse, it would make
the index's correctness depend on which `Redactor` the composition root happened to install, and the
watcher is constructed without one.

**Filter secrets at search time** — index everything, exclude callout content in the MATCH expression.
Rejected: the secret text would be in the FTS index and in `pages`, which is a file on disk that
anything able to read the database can read. S-5.11 says secret text never *enters* the index, and a
read-time filter does not satisfy that.

**Two columns, plain and unredacted.** Rejected by migration 0007 before this record existed, and its
reasoning holds: a second column is a second thing that has to remember to exclude, and the state it
would record — that a secret is currently revealed — is already in the file and in `secrets_revealed`
(S-5.8). What it would add is a column whose contents depend on a reveal, which means the index would
have to be rewritten on every reveal and every sync undo of one.

**Share the flattening with the renderer.** Rejected: it would make the renderer's output a dependency
of the index, which is the coupling this record exists to prevent. A second word extractor in a file
with no privileges is cheap; a shared one would have to be exactly right about markdown in both places.