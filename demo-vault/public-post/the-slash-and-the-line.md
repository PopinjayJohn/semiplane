---
title: The slash, and the line it draws
demo:
  broken:
    - The Toll Bridge Keeper
    - /lamp-post/The Night Bell
---

## One character, and why it is not optional

A page in one campaign can point at a page in another. The spelling is a **leading
slash**:

```
[[/forgotten-realm/index]]     another campaign — write this
[[forgotten-realm/index]]      resolved against *this* campaign first
```

Write the slash, always. What it buys is that the question has only one answer, and it
is worth being exact about how the alternative is decided, because the fallback is
where vaults go wrong.

Without the slash, the reference is resolved **against the campaign it was written in
first**. Its first segment is looked for as a directory *here*:

- **if this campaign has one**, the reference is a relative path into this campaign
  and names a page *under this campaign's root* — which is not where the target is;
- **if this campaign has none**, the reference can only be read as naming a campaign.

That second case is why the slash-less form appears to work here: this campaign has no
directory called `forgotten-realm`, so the fallback reads it as a campaign qualifier
and finds the page. **That is a fallback, not a promise.** Add a folder called
`forgotten-realm` to this campaign — a real thing to do, if a campaign ever has a page
folder for a place in the Forgotten Realm — and the identical text silently becomes a
relative link into that folder and resolves to nothing.

The leading slash is Obsidian's own spelling for a link at the top of the vault, and
here the top of the vault *is this campaign*, which is what lets the two readings be
told apart at all. With it, the reference is vault-absolute and the campaign qualifier
is what it says. Without it, the meaning of your link depends on what directories your
campaign happens to contain, which is not a property you want a link to have.

## A link that works for one reader and not for another

Here is one, crossing into a campaign you cannot read:

[[/forgotten-realm/index]]

Three things happen to that link, and they are three different mechanisms.

**It renders as an ordinary link.** No warning, no "external" marker, no dimmed text.
The address it carries is built from the author's text alone — the target campaign is
not looked up, its content root is not opened, its page index is never read. That is
what keeps this page's HTML **byte-identical for every reader**, including the Game
Master and you.

**Clicking it depends entirely on who you are.** The Game Master, a member of that
campaign, gets the page. You get a **404**.

That 404 is the boundary this page is about, and it is worth being precise about why it
is not a bug. It is the access gate doing its job at the far end: no access is a 404,
never a 403, and the address disclosed nothing the author had not already written
down. The link *looks* the same for both of you because it has to, and it *lands*
differently because the campaign it names is private.

**The answer is computed against the reader, and only the answer is.** Whether a link
*works* is not a question the renderer answers. It is answered somewhere else, against
the set of campaigns the reader may see — and that set is an **input**, so a campaign
outside it is not consulted at all. It cannot be: the code was handed the set, and a
reference into a campaign that is not in it comes back unresolved, identically to a
reference that names nothing.

Two properties fall out of that, and both are the point:

- **The markup is per-page, the answer is per-reader.** A GM sees a working link where
  you see a dead one, and both of you are reading bytes that were rendered without
  knowing who either of you was.
- **A broken link is not an existence oracle.** The answer for a reader who may see the
  target campaign and the answer for a reader who may not is *the same*. Nothing in the
  report distinguishes them, so a link cannot be used to discover which campaigns an
  instance has.

If the cache were the other way round — if the renderer were handed the reader's
campaigns — the two answers would be two different documents, and the render cache
would stop being a cache. It holds exactly two variants of a page, keyed on whether
secrets are included. Anything more would be per-reader, and unbounded.

## Where it stops being a link

A cross-campaign **embed** is refused outright:

```
![[/forgotten-realm/index]]
```

That is not a broken link and not a dead page; it is a request semiplane declines. A
link is a pointer and pointing outside is fine. An embed is an *inline*, and inlining
another campaign's page would make this page's HTML depend on a document the reader may
not be able to read — the per-reader variant problem above, arriving in a worse form.

## Two links that go nowhere on purpose

Everything else in this campaign demonstrates something working. These two fail, and a
demo that only shows the happy path does not show the part anybody needs. They are also
**declared**, in this page's front matter above, because a reference that resolves to
nothing is a defect until somebody says otherwise.

**A page in this campaign that was never written:**

[[The Toll Bridge Keeper]]

The address it produces is a real one *inside this campaign*, so following it gives a
**404** — for a reader who is already entitled to be here. Not a 403, and never an
answer about the filesystem. A 404 says "there is no page at this path", which is
exactly true and says nothing about whether the page was deleted, never written, or
renamed by a sync client that has not finished.

**A page in a campaign that does not exist:**

[[/lamp-post/The Night Bell]]

Nothing is asked of `lamp-post`. No root is opened, no index is read, no directory is
looked for — and **the answer is identical to the answer for a campaign that does exist
but that this reader may not see.** Compare it with the link at the top of this page:
same markup, same report, same refusal. A stranger cannot tell those two apart, and
that indistinguishability is the entire security property.

This is the sharp end of the existence-oracle rule, and it is why a broken-link report
sitting in a public campaign is not a directory listing with extra steps.

## What the declaration is for

`make demo-check` reads `demo.broken` out of this page's front matter, and the two
entries above are the vault **declaring its own deliberate breakage**. The gate's
unresolved-link budget is derived from the length of that list rather than from a
number written into the gate — a vault demonstrating one broken link and a vault
demonstrating six both pass.

What the declaration buys is that it cannot rot in either direction:

- a reference that starts failing **without** an entry is a finding — an accidental
  breakage the author did not mean, which is the finding that matters; and
- an entry that **starts resolving** is also a finding — a demonstration somebody
  repaired by accident, or a page somebody renamed.

Either rule alone would be satisfiable by a hardcoded number. Together they cannot be.
Each campaign declares its own, which is why these two entries are here and not
somewhere else in the vault.

An entry is the reference **exactly as written between the brackets**, including the
leading slash on a cross-campaign one. The gate compares your bytes rather than a
normalised path, so there is exactly one spelling to get right and it is the one you
would type.

[[index]]