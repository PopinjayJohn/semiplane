---
title: "0029 — A redactor sees the whole source, and runs before the render"
description: "Redaction happens on the document text, not on a parsed Document and not on rendered HTML, so no buffer downstream of it ever holds a secret."
lede: "S-5.7 says redaction happens before sanitisation and before a value reaches a template. Choosing the *source* rather than the parse tree or the output is what turns that from a rule about ordering into a property of the types — and it changes what the secret phase is allowed to remove."
weight: 222
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

`[!secret]` is phase 10. Phase 3 built the read path the secret phase refines, and the risk is
that the ordering is left implicit and phase 10 gets it wrong — because a redactor that runs *after*
the render is the intuitive place to add one, and it is the one position that leaks.

S-5.6 says a secret body is **absent** from every non-GM response: not `display:none`, not a
`hidden` attribute, not a comment, not a class. Every one of those ships the text in the HTML, where
any player can recover it from view-source. S-5.7 says redaction happens **before** sanitisation and
before the value reaches any template, so **no intermediate buffer holds an unredacted copy for a
non-GM**.

The requirement is about buffers. A redactor after the render means the unredacted body exists in
the renderer's AST, in the sanitiser's input, and in whatever the render cache holds — three places
a bug, a log line, a core dump, or a future plugin's render hook would leak it from. A redactor
before the render means the non-GM pipeline **never constructs the string**.

## Decision

The redactor operates on the **source bytes**, before parsing, before rendering, before
sanitisation.

```go
type Redactor interface {
    Redact(body string, includeSecrets bool) (string, error)
}
```

Two structural mechanisms hold the order, so a reordering is not a silent regression:

1. **`source` has exactly one exit.** It holds the bytes and offers one method,
   `redact(Redactor, bool) (redacted, error)`. `redacted` holds what came back and offers one
   method, `parse(PageKindRegistry) content.Document`. Both types are unexported, there is no
   conversion between them that skips the redactor, and there is no method from `source` to `Parse`,
   to `Render`, or to the cache. For a non-GM the bytes cannot reach any of them — the type has no
   other exit.
2. **`include_secrets` comes from one place.** `access.Tier.CanEdit()` — that is, the campaign GM
   and nobody else. Not a query parameter, not a cookie, not a header: each is attacker-supplied.
   Not a re-read of the membership: the access gate already resolved it, and re-deriving it here
   would be a second copy of the S-8 matrix (ADR 0024). It feeds exactly two things, the cache
   variant and the redactor's flag.

### The whole source, not the body

The redactor receives the file's entire text, not the markdown body with its front matter removed.
That is a deliberate reading of the seam, and it is the one phase 10 most needs to know.

A redactor that saw only the prose could not remove a secret from **front matter** — and front
matter is attacker-reachable by definition (S-4.7), since the content root is writable by a sync, a
community plugin, and any device that syncs the vault. A secret in front matter would be rendered
into a `data-` attribute, a template parameter, or a page title, and a redactor that only saw the
body would never see it.

The cost is that the redactor must itself know where a callout starts and ends, including at the
very top of the document. That is a real constraint on phase 10, and it is the right trade: the
alternative is a redaction that is complete for the common case and silent for the reachable one.

## Consequences

- **The guarantee is about buffers, not about memory.** The bytes are necessarily in memory to be
  read and hashed. What is guaranteed is that every buffer *downstream of the redactor* is clean,
  which is what `pipeline.go` states rather than overclaiming.
- **Phase 10 must supply a whole new `Definition`** whose writer emits nothing for a hidden secret,
  because the render pipeline has already been handed text with no callout in it. A writer that
  emitted a styled span would defeat the omission; a writer that emits nothing is indistinguishable
  from a paragraph that was never written, which is exactly S-5.6's "removed entirely".
- **`content.NoSecrets()` is what phase 3 installs, and it removes nothing.** Its doc comment says
  so explicitly, because the name reads like a safety guarantee and it is the opposite: until phase
  10, `[!secret]` content is rendered in full to every viewer. A reader who finds that comment learns
  the real state of the feature; a reader who finds no comment does not.
- The seam is one interface and one call site. Phase 10 changes `Redactor`'s implementation and
  nothing else in the read path.
- The cache key carries `include_secrets`, so the two variants of a page are two entries and two
  validators (S-5.2, S-5.3, [ADR 0016]({{ "decisions/0016-salted-etag/" | relURL }})). That was
  phase 3's work; this record exists so phase 10 knows the variant is already plumbed and does not
  add a second one.

## Alternatives considered

**Redact the rendered HTML.** Rejected: the unredacted text has already been through the renderer,
the sanitiser, and potentially the cache. Every one of those is a place the text exists in full for
a non-GM, which is precisely what S-5.7 forbids.

**Redact the parsed `Document`'s body.** Reasonable, and it is what a first implementation would do
— but `Document` only exists after `Parse`, so front matter has already been read into a struct the
redactor cannot reach, and a secret in front matter reaches a template. Rejected for the reason
above.

**Redact after parsing but before rendering, by giving the redactor the `Document`.** Rejected for
the same reason, plus one: `Parse` is a total function with no error return, so a
`Document`-shaped redactor interface is a re-parse of the block rather than the bytes, and the
"before" in "before sanitisation" would no longer mean "before anything looked at the file".

**Pass the raw token and let the renderer skip the callout.** Rejected: the renderer would then know
what a secret is, so every extension writer would have a second thing to get right, and a plugin's
render hook would be a path to the unredacted text. One redactor, upstream of everything, is the only
shape where a new extension cannot leak.
