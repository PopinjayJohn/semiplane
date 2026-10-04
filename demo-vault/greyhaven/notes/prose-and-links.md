---
title: Prose and wikilinks
---

## Reading a page

This page has front matter and **no `kind`**, and that is the whole of its
demonstration: a page with no kind renders as prose. So does a page whose kind this
build has never heard of — the fallback is the same in both directions, deliberately,
because a page should never fail to render over a word in its front matter.

Front matter is a YAML block between two `---` lines at the top of the file. This
vault only uses two keys — `title:` and `kind:` — plus `demo:` on one page later,
which semiplane's own completeness gate reads. Every other key is ignored, and an
unknown key is a non-event rather than an error: a vault can carry its own metadata
without asking the product to understand it.

## Ordinary Markdown

Headings, **bold**, *italic*, lists, block quotes, tables, fenced code:

| Column | What it holds |
| --- | --- |
| `title` | The page's name. Never a secret — see below. |
| `kind` | What the page *is*, resolved through the registries. |
| `demo` | Read only by `make demo-check`. Semiplane ignores it. |

> Block quotes are ordinary Markdown. They are also the **only** thing a
> `[!secret]` callout is — see [[secrets]].

Raw HTML is never interpreted. A vault is untrusted input, because it arrives by sync
client as often as by hand, so a page that writes `<script>` gets the text and the
brackets and nothing else. That is a security boundary and not a formatting choice.

## Links

A `[[wikilink]]` is written with double square brackets. It resolves in three steps,
in this order, and stopping at the first that answers is what makes the next one
predictable:

1. **A relative path**, resolved against the directory of the page that wrote it. So
   `[[north-road]]` inside `table/` is `table/north-road`.
2. **A bare name**, looked up among *every* page of this campaign by its file's base
   name. This is the Obsidian rule and it is the one that surprises people: `[[rules]]`
   is not a link to a page called `rules`, it is the file `rules.md`, and from
   `notes/` a bare name is never relative to `notes/`.
3. **A campaign-qualified name**, written with a **leading slash** —
   `[[/public-post/town-notice]]`. The slash is the whole thing; without it,
   `[[public-post/town-notice]]` is a *relative* reference inside this campaign and
   means `notes/public-post/town-notice`. See [[cross-campaign]].

So from this page, in `notes/`:

- [[index]] links to this campaign's front page by bare name, from two directories
  down.
- [[north-road]] finds `table/north-road.md` by bare name.
- [[../index]] is the same target written as a relative path, and resolves from this
  page's own directory.

### The parts of a link

```
[[target|what the reader sees]]      an alias: one thing written, another displayed
[[target#a-heading]]                 a heading on the target page
[[#a-heading]]                       a heading on *this* page
[[target.md]]                        the extension is optional; [[index.md]] is [[index]]
```

An alias is the part after the pipe. `[[index|the campaign index]]` says one thing and
displays another, and the displayed text is the author's bytes — never a title read out
of the target page, because a label derived from the target would make the linking
page's bytes depend on another document, and the target's title is something a
front-matter edit can change without changing the linking page.

### Heading anchors are the generated id, not the heading

A heading anchor rides on the address as a fragment, and the fragment is **verbatim**:
`[[secrets#What a secret is not]]` produces `…/wiki/notes/secrets#What%20a%20secret%20is%20not`.
But a heading's `id` is **generated** — lower-cased, hyphenated, and de-duplicated with
a numeric suffix when two headings share their text.

So the two do not match, and writing the heading the way Obsidian wants it gives a
fragment the browser cannot find. The one that works is the id:

- [[secrets#what-a-secret-is-not]] — scrolls.
- A fragment written as the heading's own text does not, and nothing anywhere says so.

Worth knowing before you rely on it, and worth checking a heading anchor rather than
assuming it: the link is never reported broken either way, because the *page* resolves.
`[[#a-heading]]` on this page names this page and no target at all, so it can never
fail.

### What a broken link looks like

A reference that names nothing still renders, with a marker and a sentence saying why.
Following it gives a 404 **inside a campaign you are already entitled to read** —
never a 403, and never an answer about the filesystem. [[boundaries]] has three of
them and says which is which.

### What links are not

A link is not a way to reach outside the campaign. Every path a reference carries is
resolved inside this campaign's content root, and a reference that tries to climb out
is refused before anything touches a disk. That is `os.Root` rather than a cleaned-up
string with a prefix check, so a symlink cannot smuggle a path past it either.

## What a page is called, and where a title actually goes

The name a page is referred to by everywhere in the interface is **the file's base
name**. Not the front matter's `title:`, not the first heading, not the text of the
first line:

- the document's `<title>`, which reads `millers-letter — Greyhaven`;
- the page's `<h1>` above the article, which reads `millers-letter`;
- every row of the navigation tree.

So a page body starts at `##`, not at `#`. The structural rules are **exactly one
`<h1>`** per page and **heading levels that never skip**, and the shell already spends
the one `<h1>` on the page's name. A body that opened with `#` would put a second one
on the page — and if it repeated the base name it would also be a repeated heading in
the outline, which is the one of the three a screen-reader reader actually notices,
because the heading list *is* the table of contents.

This page is written that way on purpose: `prose-and-links` above is the page's
name, and `## Reading a page` is the first line of the article. Both are headings; only
one of them is the title.

So what is `title:` for? Exactly one thing, and it is worth knowing because it is the
only part of a page's front matter that reaches anywhere: **it is indexed.** A search
matches it, so a page is findable by the name you gave it even when nothing else on
the page carries the words. Nothing renders it. That asymmetry is deliberate in one
direction — see below — and it is also why a vault author should not expect to see
their title anywhere.

## Titles are not redacted

`title:` is stored verbatim and **never redacted**, and only the page's *body* is
subject to the secret pass at all. A title is one line of front matter with no callout
structure in it, so there is no boundary for the redaction pass to redact *to* — a rule
that stripped titles would have to guess which words are secret, and a guess that
fires breaks a real title while a guess that misses leaks the secret.

The interface's answer is to stop using the title as a label, which is what the
previous section describes: every name on the page is the base name, so unredacted
front-matter text never reaches a reader. **A secret must still never be in a title**,
because it is in the search index and a search result is a place a reader can see.

## And what the search index leaves out

One more thing to know before you write a vault, and it is not about secrets at all —
it is about the literal marker string. The pass that keeps callout content out of the
search index looks for the marker on **every line, before and independently of any
code-fence or code-span tracking**, because a fence that is opened and never closed
must not be able to leak one. The consequence is that writing the marker's name in
ordinary prose — even inside backticks — is enough to exclude **the rest of the
document** from that page's search entry.

That is a blunt rule failing toward hiding, and this vault pays for it deliberately:
[[secrets]] is the page that documents the marker, and it is also the page in this
campaign whose own search entry is nearly empty. It says so on itself.

Back to [[index]].