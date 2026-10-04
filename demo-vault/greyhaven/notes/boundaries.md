---
title: Where the boundaries are
demo:
  broken:
    - The Sentinel
    - /lost-realm/The Drowned Road
    - ../../../../etc/passwd
---

## Three references, three different failures

Everything else in this campaign shows a feature working. This page shows three
references failing, because a demo that demonstrates only the happy path demonstrates
the part nobody needs demonstrating — and each of these fails for a **different
reason**, which is the part worth reading.

## A page that was never written

[[The Sentinel]] names a page that does not exist.

The address it produces is still a real one *inside this campaign*, so following it
gives a **404** — for a reader who is already entitled to be here. Not a 403, and
never an answer about the filesystem. A 404 says "there is no page at this path", which
is exactly true and says nothing about whether the page was deleted, never written, or
renamed by somebody whose sync client has not finished.

## A campaign nobody has

[[/lost-realm/The Drowned Road]] names a page in another campaign.

Nothing is asked of that campaign. No content root is opened, no page index is read,
and **the answer is the same for a reader who may see that campaign as for one who may
not**. That indistinguishability is the entire point: a broken link sitting in front of
a report must not become an oracle for which campaigns an instance has. See
[[cross-campaign]].

## A path that leaves the campaign

[[../../../../etc/passwd]] climbs out of the content root.

This one is not unresolved. It is **refused**, before anything looks at a disk, by the
campaign's confinement boundary — an `os.Root`, not a cleaned-up string with a prefix
check, so a symlink cannot smuggle a path past it either. The distinction is worth its
own sentence: a reference naming nothing inside the campaign is a 404, and a reference
trying to leave it is a refusal, because telling an author "that path is outside" is
telling them something about a filesystem they were never granted.

Note that none of these three is an error in the vault, and none of them is silent
either. The first renders as a link carrying a broken marker. The second renders as
an ordinary link — deliberately, because the render path is not allowed to consult
another campaign, so it cannot know and a marker that appeared for a Game Master and
not for a player would be exactly the viewer-dependence the whole design forbids. The
third is **not a link at all**: it renders as inert text with a title saying the
reference points outside the campaign, because handing somebody a clickable address for
a path you refused would be an invitation to try it again somewhere less careful.

A reference that resolves to nothing is a fact about the vault that the Game Master is
told about and can fix.

## What the demo gate does with these three

`make demo-check` reads this page's front matter. The `demo.broken` list above is the
vault **declaring its own deliberate breakage**, and the gate's unresolved-link budget
is derived from the length of that list rather than from a number written into the
gate. A vault demonstrating one broken link and a vault demonstrating six both pass.

What the declaration buys is that it cannot rot in either direction:

- a reference that starts failing **without** a list entry is a finding — an accidental
  breakage the author did not mean; and
- a list entry that **starts resolving** is also a finding — a demonstration somebody
  repaired by accident, or a page somebody renamed.

Either rule alone would be satisfiable by a constant. Together they cannot be.

An entry is the reference **exactly as written between the brackets**, including the
leading slash on a cross-campaign one. That is because the gate compares the author's
bytes rather than a normalised path, so there is exactly one spelling to get right — the
one an author would type.

## The other things semiplane refuses

Not link-shaped, and all of them load-bearing:

- **Authorisation is a gate the route mounts, never a check inside a handler.** A
  handler that checked would eventually forget, and the failure would be a private page
  answering 200. Every refusal in the product is a mount in one list.
- **No access is 404, never 403.** A private campaign and a campaign that does not
  exist answer identically — same status, same body. A 403 becomes an existence oracle
  the moment the status differs.
- **Every refusal response is `private, no-store`.** Those answers are
  reader-dependent by design: the same URL is a 404 for an anonymous request and a 200
  for a member, because "no access" is answered without saying why. A response whose
  body cannot tell them apart is exactly the response that must never be cached by a
  proxy in front of the instance.
- **No event carries page text, file contents or dice results.** Enforced by the shape
  of the event attributes rather than by discipline at each call site: there is no field
  a page body could be passed through. Logging the thing that failed is the natural
  thing to do while debugging, and it is how a `[!secret]` callout ends up in a log
  aggregator.
- **One process.** The live state is in memory and the hub is process-local. A second
  instance does not fail; it diverges. See [[the-live-table]].

## And the words the interface does not use

Two words — the ones the design records use for two concepts semiplane does not have
as entities — appear nowhere in the interface. Not in a heading, not in an
`aria-label`, not in a `title`, not in an empty state, not in an error message.

This campaign obeys that, and it is worth being precise about who holds it, because
the honest answer is not "a gate". The audit that enforces it **parses the served
document** rather than scanning its bytes — which is the only way to catch the word
sitting in an HTML comment, an `aria-label`, a `data-` attribute or an attribute
*name* — and it is fed six ways of smuggling the word in, so that an audit which could
not fail cannot be mistaken for one that passed.

But it audits the product's own routes, from built fixtures. **It does not audit a
vault.** Nobody can enforce your prose from the outside, which is exactly why the rule
is written as it is and why keeping it is the author's job. So a search of this whole
campaign for either word finds nothing, and that is a promise the vault keeps rather
than a gate anybody can lean on.

Back to [[index]].