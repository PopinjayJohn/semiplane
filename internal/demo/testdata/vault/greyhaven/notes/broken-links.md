---
title: Where the boundaries are
demo:
  broken:
    - The Sentinel
    - /lost-realm/The Drowned Road
    - ../../../../etc/passwd
---

# Where the boundaries are

A link that cannot be followed still renders, with a marker and a sentence saying
why. A vault that demonstrated none of this would be a demo of the happy path
only, and the happy path is the part nobody needs demonstrating.

Three references on this page are unresolved, and each fails differently.

## A page that was never written

[[The Sentinel]] names a page that does not exist. The address is still a real one
inside this campaign, so following it is a 404 for a reader who is already
entitled to be here — never a refusal, and never an answer about the filesystem.

## A campaign nobody has

[[/lost-realm/The Drowned Road]] names another campaign. Nothing is asked of that
campaign: no root is opened, no index is read, and the answer is the same for a
reader who may see it as for one who may not. That indistinguishability is the
point — a broken link must not become an existence oracle with a report in front
of it.

## A path that leaves the campaign

[[../../../../etc/passwd]] leaves the content root, and the confinement boundary
refuses it before anything looks at the filesystem.

## What the gate does with these three

`make demo-check` derives the unresolved-link budget from the `demo.broken` list
in this page's front matter rather than from a number written in the gate. The
list cannot rot silently: an entry that starts resolving is a finding, and a
reference that starts failing without a list entry is a finding. Both directions
are asserted, which is what stops the budget from being a constant with a comment.

An entry is the reference **exactly as written between the brackets** — including
the leading `/` on a cross-campaign reference, because that slash is what makes
the reference cross-campaign at all, and because the gate compares the author's
bytes rather than a normalised path so that there is one spelling to get right.

Back to [[index]].
