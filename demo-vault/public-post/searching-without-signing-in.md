---
title: Searching without signing in
---

## The box on this page

Search is part of *reading*, so in a public campaign it is part of the public surface:

```
GET /c/public-post/search?q=toll
```

No account, no cookie, no membership — the same gate as the wiki answers it. That is
consistent, and it is also the reason this page exists: **search is the sharpest edge
of the decision to publish a campaign**, and it is sharpest in a way that is easy to
miss.

A wiki page is something a reader goes and looks at. A search result is a **fragment of
a page, in a list, on a page the reader did not choose**, assembled by a machine that
decided they might want it. If your campaign is public then this campaign's prose is
being quoted at strangers in a context you did not write and cannot recall. Anything
that should not be published should not be *written*, and
[[town-notice]] is the worked example of what that means in practice.

## Two things search will never show you

### The body of a secret callout, in any state

The index behind this box is built from a derived plain-text copy of each page, and
that copy is taken from the **source**, before rendering — not from the rendered HTML.

That is not an optimisation, and it is not a filter somebody remembered to apply. It is
what makes the following true:

> A `[!secret]` callout's text is **excluded from search in every reveal state**,
> including for the Game Master who has just revealed it.

Not "excluded for players". Excluded, period, for everybody, permanently. The reason is
the pipeline's shape: redaction operates on the source before anything renders it, so
the callout is *removed* rather than hidden, and once it has been removed there is no
text anywhere downstream for an index to have picked up. A secret that survives in a
search index is a secret with a second copy in the database that no amount of
un-revealing will ever delete.

So try it: the ferry rota on [[town-notice]] does not come up here. It will not come up
when the Game Master reveals it either.

### A page in a campaign you may not see

This route searches **exactly one campaign**, and the one is in the URL:

```
GET /c/{slug}/search?q=
```

There is a shape in the storage layer for "every campaign this requestor may read", and
this route deliberately refuses to use it. The reasoning is about where the danger
lives: a URL-scoped search is the shape in which a private title leaks, because a scope
the caller forgot is a scope of everything. The instance-wide search exists in the
storage layer, where its per-row visibility check is joined unconditionally, and it is
reachable only from there.

The consequence for this campaign is a boundary worth stating plainly: **a reader
searching the public campaign cannot discover the titles of a private one.** Search is
not a way to enumerate what an instance holds — no more than a link is
([[the-slash-and-the-line]]).

## Why the box does not suggest as you type

Semiplane's search is a form and a server round trip, and the URL you are looking at is
the query you ran. No type-ahead, no debounce, no partial document, no script anywhere
on the result page.

That is a decision about **who owns tokenisation**, and it is the reason this is not a
cleverness gap:

- The server's tokenizer strips accents and normalises diacritics, and deliberately
  applies **no stemming**. TTRPG proper nouns stem badly — folding a name into a word
  nobody typed is the failure mode — so the porter stemmer that would help ordinary
  English actively hurts a campaign wiki.
- A browser cannot reproduce that tokenizer. Any client-side renderer that *looked*
  like type-ahead would be answering a different question from the one the URL
  encodes, and the two would disagree on exactly the names that matter.

So the constraint is not "we could not build type-ahead". It is **the answer must be the
answer**, and a renderer that reproduces it badly is worse than no renderer at all. The
same reasoning forbids a live region anywhere on this page: a search result is a fresh
navigation, and there is nothing to announce.

## The four answers

The route renders one of four documents, and a reader should be able to tell which one
they got:

| You see | It means |
| --- | --- |
| the idle state — the form, nothing run | you have not searched yet |
| `no results for "…"` | the campaign has no page matching, and it will not tell you about another campaign |
| the result list, headed by the count | these pages matched, in this campaign only |
| a load error | the query could not be run at all |

The heading of the result list **is the count**, and it is the only `<h1>` on the
response — the same document you are reading this in, which renders the page title once
from the file's name and leaves the prose to its `<h2>`s.

## The part that is still not redacted

One last thing, and it is the reason [[town-notice]] spends a paragraph on it: **a
page's `title:` is not redacted and cannot be.**

A title is one line of front matter. It has no callout structure in it, and therefore
no boundary to redact *to* — a rule that stripped titles would have to guess which
words are secret, and a guess that fires breaks a real title while a guess that misses
publishes the secret. So the title is served verbatim to a stranger in the `<h1>`, in
the nav tree, and in **this result list**, which is built from the title and the plain
body together.

Which makes the title the one part of a public campaign's page where you must decide
before you write rather than after. It is also why the title of that page is "The notice
on the board" and not something more descriptive.

[[index]] · next: [[the-slash-and-the-line]].