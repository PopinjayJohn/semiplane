---
title: Secrets
---

## One callout, and one character

A Game Master needs somewhere to put what the players do not know yet, and a
semiplane page is a Markdown file, so the answer had to be something Markdown
already understands. It is a **block quote whose first line starts with
`[!secret]`**, and the character immediately after that word is the entire state:

| Written | State | Who sees the body |
| --- | --- | --- |
| `> [!secret]-` | collapsed | the Game Master only |
| `> [!secret]+` | revealed | everyone who may read the page |

Everything after the marker on the header line is optional and is **not** state: text
there is the callout's title, and an Obsidian block id (`^something`) at the very end
anchors it so a reveal can name it.

Here are both, live. This page has no `kind`, so it renders as prose — a callout is
available on any page and is not tied to a page type.

> [!secret]- The second entrance  ^second-entrance
> There is a second way into Greyhaven, and it is behind Marden Mill. Only the
> miller knows it, and he has been leaving it unlatched since the fire went out.

> [!secret]+ What the party already worked out  ^signal-fire
> The eastern signal fire has not been burning for three nights. The Wardens have
> established that much from the cold ash and the missing oil.

## What a secret is not

Three things it is **not**, and each one is a decision rather than an omission of
effort:

- **It is not `display: none`.** Nothing is drawn and then hidden from sight.
- **It is not an HTML comment.** There is nothing in the page for a reader to
  un-hide.
- **It is not a class a stylesheet can be asked to ignore.** The markup is never
  produced.

For a non-Game-Master the callout is **not in the response at all**. Redaction runs
on the file's own bytes *before* the page is rendered, so the text is never in a
renderer's buffer, never in the sanitiser's input, and never in the page cache. If
the two caches were reversed the secret would exist in memory and on disk, and a
defect in the sanitiser would turn into a disclosure rather than into a rendering
bug. Running in the other order is cheaper to implement and much more expensive to
get wrong.

The consequence a reader can check: view the source as a player, and the callout's
body is not there to find.

## Revealing one

Revealing is **one byte on disk** — `-` becomes `+`. Not a flag in the database, not
a per-reader preference: the reveal is a fact about the page, so a player who has read
it and a player who has not are reading the same file.

The reveal control the Game Master gets is a POST to
`/c/{slug}/secrets/{page-path}`, which is deliberately **not** the editor's route. The
editor moves a whole document under an `If-Match` validator; the reveal moves one byte
and writes an audit entry naming who revealed it and when. It carries its own
authorisation and its own trail, and giving it the editor's route would invite a
reader to assume they are the same write.

A page with several collapsed callouts gets a *reveal every secret on this page*
control as well, and both controls are ordinary focusable buttons with stated reasons
— a control that is merely greyed out is not reachable by keyboard, and a reason only
a sighted reader can see is not a reason.

## What a secret does not cover

Three gaps a vault author has to know about, because they are properties of the
product rather than mistakes in the vault:

**A title is not redacted.** `title:` is one line of front matter with no callout
structure in it, so there is no boundary for the pass to redact *to* — and the title
is served in the search index, the navigation tree and the `<h1>`. Never put a secret
in a title.

**A secret's words are not searchable.** The full-text index is built from the
**source**, never from rendered HTML, and it excludes secret callout content in *every*
state — collapsed and revealed alike. That is on purpose: an index that included a
callout once revealed would leak to the player who searches for it after the reveal,
and the reveal is exactly the moment a secret-aware mistake becomes a real
disclosure.

The bluntness has a cost, and this page pays it in a way that is worth seeing. The
marker is tested on **every line**, before and independently of any code-fence or
code-span tracking, because a fence opened and never closed must not be able to leak
one. A marker at the start of a block quote excludes that block quote. **A marker
anywhere else — including one written in backticks while describing the syntax — cuts
off the rest of the document.**

The first table on this page is where it happens — one of those rows is the first line
in the file carrying the marker's name, and it is not a block quote. So this page's
search entry is **a couple of hundred characters long**: the heading and the paragraph
above the table, and nothing after. Search this page for "revealed" and find nothing;
search it for "block quote" and find it. Everything below here — including this
paragraph — is absent, and the only way to tell from inside the vault is to notice
that a page about secrets is hard to find.

A vault that wants a page both about the syntax and searchable has to put the syntax
on its own page, which is the arrangement this campaign uses.

**A link inside a callout is still a link.** It is resolved and it is checked, but a
report about it prints the page, the line and the rule — and never the target, because
the target is author text inside something that was supposed to be hidden.

Back to [[index]].