---
title: Assets follow the campaign's visibility
---

## A file a stranger can fetch

Here is one, embedded by this page and by [[town-notice]]:

![[maps/hollowfield-and-the-fen-road.svg]]

It is a real file at a real path inside this campaign, and a stranger with the URL can
fetch it:

```
GET /c/public-post/assets/maps/hollowfield-and-the-fen-road.svg
```

There is no separate permission for files. There is no per-file access list, no
"unlisted" flag, no draft directory. **An asset inherits its campaign's visibility,
entirely and without exception** — and that is the correct design, because a second
gate on files would be a second implementation of the access matrix, and the failure
mode of that is a private handout served to an anonymous reader.

Which means the honest statement of this page is the uncomfortable one:

> **Every file in this campaign is public. Including the ones you have not thought to
> put in it yet.**

The access matrix has one dimension — *which campaign* — and the file is not a second
one. A semiplane campaign is a directory, and the directory's visibility is the
directory's visibility.

## The mistake this page exists to prevent

An operator who decides "this campaign should be readable without an account" is
publishing the campaign. Not the wiki — **the campaign.** The battle map with the
secret door drawn on it. The handout with the passphrase on it. The data pack export
sitting in the vault because it was convenient at the time.

The instinct that makes this go wrong is reasonable and worth naming: *the wiki pages
are the product, so making the wiki public publishes the wiki.* It does not. The pages
are one kind of thing in the directory. Before the wiki there were files, and they are
still there, and they are now in front of anybody.

The two controls semiplane does offer are both about **the writing**, not about the
files:

1. **Do not put it in this campaign.** Files follow the campaign, so the separation
   that matters is between campaigns. A map only the table should see belongs in a
   private one. Note that a *page* is a file too, which is why
   [[town-notice]]'s unpublished paragraph is a `[!secret]` callout and not a second
   directory.
2. **Do not write it down yet.** A callout is a curtain rather than a lock — see the
   bottom of [[town-notice]] for why — but it is better than nothing and it is the
   control that exists for exactly this case.

## What the route actually checks, in what order

The asset route is almost entirely about refusals, so the order of its checks is the
argument, and each step exists because the ones after it would be the wrong answer:

1. **Confinement, first, and it is an operating-system root handle.** Every path is
   opened relative to the campaign's root, never cleaned-and-prefix-checked. The
   string version is wrong in four separate ways — `..`, an absolute path, a symlink,
   and a case-insensitive filesystem — and a root handle is wrong in none of them.
2. **The name policies, shared with the page index.** A dotfile, a `node_modules`
   directory, and editor leftovers (`.tmp`, `.swp`, a trailing `~`) are refused by the
   same rules the index uses, so the index and the file route cannot disagree about
   what a campaign's content *is*.
3. **The extension, from a closed table.** The media type is never sniffed out of
   attacker-controlled bytes, and nothing is ever served as `text/html`. An unknown
   extension is a 404, not a guess.
4. **Existence, and only then the bytes.** A stat before an open, so a directory or a
   device node is refused rather than served.

Step 3 is the one that shapes what you can see in the map above. This campaign's map
is an SVG, and an SVG is a **document a browser can run** — it can carry a script, an
`onload`, and a `<foreignObject>`. Served inline from semiplane's own origin that is
stored cross-site scripting against every reader of the campaign. So the same file
carries three separate answers:

| Header | Value | Why |
| --- | --- | --- |
| `Content-Type` | `image/svg+xml` | so `<img>` draws it; octet-stream renders as a broken image |
| `Content-Disposition` | `attachment` | so *navigating* to it downloads instead of executing |
| `Content-Security-Policy` | `sandbox` | so a browser that renders it anyway cannot reach this origin |

`nosniff` is on there too, which is what makes the closed table a boundary rather than
a suggestion: with sniffing forbidden, the declared type is the only type a browser
will use.

## Two headers worth copying into your own deployment notes

**The validator is salted with the campaign.** The `ETag` for a file is a digest of the
campaign's id, the file's path, its size and its modification time. Two campaigns with
byte-identical files named `map.png` get **different validators**, so a cache entry
cannot be replayed from one tenant into another — which is the same argument as the
salted page validator, arriving by a different route.

**`Cache-Control: private, no-cache`, on every campaign — including the public ones.**

That one is worth pausing on, because the natural design is "private if the campaign is
private". Semiplane sends `private` to public campaigns as well, and the reason is a
bug this avoids rather than one this fixes: **a shared cache that stored an asset for a
public campaign goes on serving it after the campaign is made private.** Nothing tells
it otherwise, and by then the change is indistinguishable from a stale copy.

A public file is cheap to fetch. Publishing something *permanently* to every proxy
between your readers and you is not. `no-cache` means "revalidate before you use it",
which is cheap, and `private` means "you may not store this for somebody else at all",
which is the part that actually matters.

[[index]] · next: [[searching-without-signing-in]].