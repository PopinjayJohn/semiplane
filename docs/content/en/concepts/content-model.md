---
title: "Content model"
description: "Markdown, front matter, wikilinks, and embeds — what semiplane reads from your vault."
lede: "Your vault is the source of truth. semiplane builds an index beside it, and the index can be thrown away and rebuilt at any time."
weight: 21
---

## One markdown file is one page

A page is a `.md` file at `(campaign, path)`. Prose and game objects are the same
thing, discriminated by the optional `kind` in front matter. There is no separate
"content type", and there is no content that lives only in the database.

## Front matter

YAML, parsed with a parser that bounds alias expansion and document size. Front
matter is attacker-reachable — it comes from files an Obsidian sync wrote — so it
is treated as untrusted input, and every path inside it is resolved within the
campaign's `os.Root` rather than merely cleaned.

```markdown
---
title: The Drowned Lighthouse
kind: handout
tags: [mystery, coast]
summary: Read aloud to the party when they reach the causeway.
---

The beam still turns. Nobody has turned it on in eleven years.
```

Front matter is parsed separately and **never reaches the renderer**, so a value
that happens to contain HTML cannot become markup.

## Markdown dialect

CommonMark with GitHub extensions, footnotes and typographic replacement, plus
automatic heading identifiers. On top of that, four semiplane-specific
extensions:

| Syntax | Meaning |
|---|---|
| `[[Page]]` | Wikilink to another page. Obsidian-compatible. |
| `![[Page]]` | Embed another page's rendered content. |
| `![[image.png]]` | Embed an asset. |
| `{{statblock}}` | Render a referenced game object as a stat block. |
| `{{dice "1d20+5"}}` | Render a dice expression. Evaluated server-side. |

`{{dice}}` exists in the document and is never evaluated in the browser: a
client-side roll is unverifiable and would break the audit trail. The expression
grammar belongs to the gameplay system, so a d20 system and a 2d6-pool system
describe different grammars and the protocol never assumes d20.

## Link resolution

`[[Page]]` resolves Obsidian-style, in this order:

1. by relative path,
2. by basename within the campaign,
3. by basename within any other campaign the viewer is allowed to see — never
   within one they cannot.

Unresolved links are recorded at index time and reported as a broken-link list,
rather than being silently rendered as plain text.

## Raw HTML is not allowed

The renderer runs with raw HTML disabled, and sanitisation happens before output.
This is a security boundary rather than a style choice: the content root is
writable by an Obsidian sync, by Obsidian plugins, and by any device that syncs
that vault. Treating that as HTML-shaped input and shipping it to other people's
browsers would be indefensible.

## The content pipeline

Rendering is event-driven, not request-time. One watcher serves every campaign
and routes events by path prefix. A burst of writes is handled by a per-path
debounce plus a size-stable confirmation before the file is read, because
Obsidian Sync and iCloud both write in bursts and a half-written file parsed as
complete is worse than a slightly stale one.

Dotfiles and `.tmp`/`.swp`/`~` suffixes are ignored. Symlinks inside the tree are
rejected by default: the watcher's behaviour around them is documented as
uncertain.

Writes go through a temporary file in the same directory, then `fsync`, then
`Rename`. A watcher that watched individual files would stop watching a file the
moment an atomic write replaced it, so semiplane watches **directories** and
checks the file directly.

## Concurrency with Obsidian

Two writers with no shared lock — a browser tab and a sync client — cannot be
serialised with a file lock, because Obsidian is a separate process that knows
nothing about semiplane. Instead, every browser write carries an HTTP
precondition:

```
GET /c/{slug}/edit/{path}          → 200, ETag: "<hash>"
PUT /c/{slug}/edit/{path}  If-Match: "<hash>"
   → 200 + new ETag    (matched)
   → 412 + current body  (disk moved on)   ← RFC 9110
```

A mismatch returns 412 with the current file contents, records no revision, and
writes nothing. There is no last-write-wins fallback and no silent overwrite. The
editor shows the two versions side by side for per-hunk accept/reject, and every
prior revision is retained — so nothing is ever lost to a bad merge.

## Backups

Two artifacts, and they can desync. Back up both:

1. **The content root.** The source of truth for pages. A plain file copy is fine
   when nothing is writing.
2. **A consistent SQLite snapshot**, taken with the `.backup` API or
   `VACUUM INTO` — never a raw file copy while the server is running.

The database is a rebuildable index plus live game state. If you can restore the
vault alone, you can rebuild everything except the last few seconds of a game in
progress.

Details under [operating]({{ "operating/" | relURL }}).
