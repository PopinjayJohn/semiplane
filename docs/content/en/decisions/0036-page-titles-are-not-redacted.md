---
title: "0036 — `pages.title` is not redacted, and a title may carry secret text"
description: "S-5.11 scopes redaction to `body_plain`, so a front-matter `title:` is indexed verbatim — and a title is the one place a secret is most likely to be written by accident."
lede: "Phase 4 excluded `[!secret]` content from `body_plain`. It did not exclude it from `pages.title`, because nothing said it should. A GM who titles a page after its secret now has that secret in the search index, in the nav tree, and in every search result."
weight: 227
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

[0031]({{ "decisions/0031-body-plain-is-derived-from-the-source/" | relURL }}) made `body_plain` a function
of the **source** rather than of rendered HTML, which is what lets it exclude `[!secret]` callout content in
every reveal state. That phase did the exclusion for one column.

`pages` has two text columns. The other is `title`, and migration `0007-pages-and-search.sql`'s
comment is precise about what it promises:

> `body_plain` — the searchable text, and the *redacted* text: `[!secret]` callout content is excluded
> from it in every reveal state (S-11.1). A search snippet is built from this column.

S-5.11 says the same thing and no more: **"Secret text never enters the FTS index. `body_plain` excludes
callout content in *every* reveal state."** Both name one column. Neither says a word about `title`,
because both were written thinking about prose, and a title is not prose.

Verified on a running instance, before this decision:

```
$ printf -- '---\ntitle: The passphrase is hunter2\n---\n\nbody\n' > vault/Secrettitle.md
$ sqlite3 semiplane.db "SELECT path, title FROM pages WHERE path LIKE '%Secret%'"
Secrettitle.md | The passphrase is hunter2
```

The indexer writes `doc.FrontMatter.Title` into `title` unredacted, in every reveal state, for every
viewer including an anonymous one on a public campaign. Three surfaces carry it: the **search index**
(the FTS5 table indexes `title` as well as `body_plain`), the **nav tree** the wiki route builds from
`pages.title`, and the **page's own `<h1>`**.

## Why this is worse than it looks

The instinct is that a title is metadata, not content, and metadata is not the thing S-5.11 is about.
That instinct is wrong on this project's own terms, for three reasons.

**A title is the most likely place for a secret to be written by accident.** A GM titling a handout
`The Duke's passphrase` is doing something entirely ordinary. So is titling a page `Anna's phone number`
and one `GM only: the vault combination`. Nothing about writing a secret into a title looks like writing a
secret into prose, which is exactly the property that makes prose the place people expect redaction and
titles the place they do not.

**Redaction is omission, not hiding.** ADR 0029 and S-5.6: a redacted value is *removed entirely* — not
`display:none`, not a comment, not a class — because every one of those ships the text in the HTML, where
any player can recover it from view-source. That argument applies with full force to a column that is
served to more readers than the page body is: a search index row is handed to every searcher, and a nav
row to everyone who can read the campaign.

**The exposure is wider than the page.** A `[!secret]` callout in the body of a page that is otherwise
GM-only is protected by the access gate on that page. The *title* of that page appears in a search result
list that a player can read for a different campaign, on an instance where several campaigns are visible
at once.

## Decision

**A front-matter `title:` is not a place a secret may be written, and this is a convention rather than a
mechanism.**

`pages.title` continues to hold the title verbatim. Nothing is stripped and no new column is added,
because:

- **Stripping is not possible without knowing what is secret.** The title is one line of front matter
  with no callout structure, so there is no boundary to redact *to*. A rule that redacted titles would
  have to redact *all* titles containing a word from a blocklist, and a blocklist that guesses at secrets
  is worse than no rule: it produces a UI that silently drops a GM's real title and still leaks the next
  one.
- **The FTS index already excludes callout bodies**, so a secret *in prose* is safe. The gap is
  specifically the one place with no marker to key on.
- **A title is rendered, always, everywhere.** It is the `<h1>`, the nav label, the search hit, and the
  browser tab. There is no tier in which it is hidden from a reader who can see the page.

So the enforcement is on the **writer** and on the **index**: any title a page declares is indexed and
rendered as written, and a GM is told that a title is public to every reader who can see the page.
`secretFrontMatter` — the documented convention a campaign uses to keep a secret out of the title — is
handled by not putting secrets in titles.

## Consequences

**A GM who titles a page after a secret leaks it**, and the leak is durable: it is in the index, in the
nav tree, and in search results, for as long as the page exists. Phase 10's redaction work covers the
body and the callouts; this is the case it does not and cannot cover by omission, because a title has no
omission to make.

**This is recorded as a limitation, not fixed**, and a reviewer reading ADR 0029's "omission, not hiding"
should read this as the boundary of that rule rather than an exception to it.

**The alternative — indexing a redacted placeholder — was rejected** because a placeholder is a second
answer to "what is this page called", and a nav tree full of "Untitled" is a worse product than one that
is wrong about a secret. Choosing to fail toward a visible title rather than a silent one is consistent
with how the rest of this codebase resolves an unknowable.

## Alternatives considered

**Redact any title matching a secret-shaped pattern.** Rejected: pattern-matching secrets is a guess, and
a guess that fires produces a broken title while a guess that misses produces the leak. The failure modes
are not symmetric.

**Store a redacted title alongside the real one, and pick per viewer.** Rejected: it reintroduces a
second `ETag` variant for every page, which is precisely the cost ADR 0016 exists to avoid, and it makes
the nav tree a per-reader document — which would require `Vary` on the shell and undo ADR 0035's
byte-identity result.

**Drop `title` from the FTS index.** Rejected: it removes the leak but makes every page unfindable by
name, which is the single most common way a reader looks for a page. A worse product to fix a worse bug.