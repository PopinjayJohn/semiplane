---
title: "0027 — There is no `front_matter` column, and the database index is a cache of the filesystem"
description: "The file is the source of truth; a second copy of a regenerable front-matter block is a second copy that can disagree with the first."
lede: "The architecture record lists a `front_matter` column on `pages`. This omits it, because the parser regenerates the block in a millisecond from the file that already holds it, and a second copy of derived data is a second answer. It also makes the page index a walk of the content root until the watcher maintains it."
weight: 215
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The [architecture overview]({{ "design/architecture/" | relURL }}) §11 lists `front_matter` among
the `pages` columns. Phase 3 created the table and did not include it. That is a deviation from a
published record, and the record is immutable, so it is corrected here rather than edited there.

The argument is [ADR 0006]({{ "decisions/0006-content-root-is-source-of-truth/" | relURL }})
applied one column further than the record applied it. That record says the filesystem is the
source of truth and SQLite is "a rebuildable index plus the state that has no home in a file". A
`front_matter` column is neither: it is a third copy of data that is already in the file, and it
is the copy with the shortest life, because it is only correct until the next sync.

A second copy of derived data is a second answer to a question. `front_matter` answers "what does
this page declare" — and a page with two answers to that is a page whose `kind` may disagree with
its own body, which is exactly the class of defect the two-variant render cache and the salted
`ETag` exist to prevent. The failure is silent and it is per-page: one bad row, no error, a page
that renders as prose in search and as a token on disk.

There is no performance argument for the column. Parsing a block out of a file the request already
read is not a cost. The only argument for it is convenience — not having to call the parser — and
convenience is what a second copy always claims.

### The index, and why it is a walk

The same principle decides how the page index is built. [ADR 0006]({{ "decisions/0006-content-root-is-source-of-truth/" | relURL }})
makes the walk the *definition* of correct: a page exists if and only if its file does. And in
this phase it is also the only thing that works, because nothing writes `pages` until the watcher
(phase 4) indexes the tree.

That second point was found by running the server, not by reading the plan. The first
implementation read the index from `pages` on a request-time miss, and every wikilink in the
product rendered `data-broken="true"` — because the table was empty. A wiki where no link resolves
is not an unimplemented optimisation, it is a broken product, and a phase that reports "the content
read path works" while every link is dead has not reported its DoD honestly.

So the route walks the content root through the confined `os.Root` and builds the index from what
is on disk. One `ReadDir` per directory per cache miss, which is not a cost worth optimising
before the thing that makes it irrelevant exists.

## Decision

- **No `front_matter` column.** Anything needing the front matter re-reads it through the content
  package, which is where confinement and the document-size cap already live.
- **`body_plain` is one column, not a plain and an unredacted pair.** A second column is a second
  thing that has to remember to exclude callout content, and what it would record — that a secret
  is *currently* revealed — is already in the file and in `secrets_revealed`. The schema therefore
  cannot represent "indexed with secrets", which is the point: the impossible state is the one that
  has to be unrepresentable (S-5.11).
- **The page index is a walk of the content root** until the watcher maintains the table, and after
  that it is the maintained table. The seam is one interface in the composition root.
- **`pages.id` is `AUTOINCREMENT`** for a harder reason than the other two tables. A bare rowid is
  reused after deletion, and a page's whole lifecycle is deleted-and-reinserted by the watcher on
  every sync removal. A reused rowid would bind the orphaned `pages_fts` entries of a deleted page
  to whatever page now owns the id, so a search would name a file that never contained the words.

## Consequences

- The architecture record's §11 column list is wrong in one row, and the correction is this record
  plus an entry in the
  [known staleness]({{ "design/" | relURL }}#known-staleness) note. The record itself is not
  edited.
- A request-time index build is `O(pages)` per cache miss. For the vault sizes this project targets
  — a campaign is tens or hundreds of pages, and a miss is a page that changed — that is not worth
  optimising. When the watcher maintains the table, `internal/content/index.go` becomes a test
  fixture rather than a code path, and the walk disappears.
- One function is the swap point: `PagesForCampaign` on the composition root's `pageLister`. When
  phase 4's index exists, it becomes the store query and the walk goes away. The route does not
  move.
- A page's `content_hash` is **not** stored by the walk. The hash in the route's cache key is
  computed from the bytes the route read, because a hash from a partial read would not be the hash
  the route uses, and two disagreeing hashes would mean the cache never hits.
- The walk reads a file's head to learn its title, capped at 1 MiB, because a vault is untrusted
  input (S-4.7) and an index build must not open a 4 GB file named `.md`. A page over the cap is
  still readable through the route, which enforces its own limit on the read path.

## Alternatives considered

**Keep the `front_matter` column for query convenience.** Rejected: it is a second answer to "what
does this page declare", and the failure is a per-page disagreement between the index and the file
with no error anywhere. The convenience is a parser call.

**Store the raw front-matter *text* rather than a decoded struct.** Rejected for the same reason,
and it is worse: an unparsed blob has to be re-parsed to be useful, so it is a copy that is neither
authoritative nor convenient.

**Build the index from `pages` anyway and accept broken links until phase 4.** Rejected. A phase
that ships a wiki where every wikilink is marked broken has not met its own DoD, and the fix is
one function.

**Maintain the index in the route** — build and persist on first read. Rejected: it makes a *read*
request write to the database, so two concurrent first reads race, and a crawler becomes a writer.
The watcher is the right owner and it is four phases away.
