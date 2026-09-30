---
title: "Secrets"
description: "GM-only content in markdown, what hiding it actually protects, and what it does not."
lede: "semiplane can mark a block of markdown as visible only to the GM. This page is mostly about the boundary of that promise, because a security feature described more loosely than it works is worse than none."
weight: 22
---

## Marking a secret

An Obsidian-compatible callout. A trailing `-` means hidden; a trailing `+`
means revealed to the party:

```markdown
> [!secret]-
> The traitor is Captain Aldric. He replaced the eastern signal fire.

> [!secret]+
> *Revealed to the party on 3 Frostfall.*
```

The single character is the whole edit, which keeps the diff to one byte and
minimises the window for a collision with Obsidian's own line handling. Unknown
callout types render in Obsidian as ordinary callouts, so `secret` does not
require an Obsidian plugin to look correct anywhere else.

## What "hidden" means here

**The secret body is absent from the response.** It is not `display: none`, not a
`hidden` attribute, not an inline comment, and not a CSS class. Every one of
those ships the text in the HTML, where any player can recover it from
view-source or the inspector. Redaction happens in the render, before the value
reaches a template, so no intermediate buffer holds an unredacted copy for a
non-GM viewer.

A non-GM receives the callout **removed entirely** rather than replaced by a
placeholder. A stub would still disclose the existence and position of a secret,
which is itself information in many games — a player who knows the lighthouse
has a secret looks for it. A per-campaign option to render a redaction stub is
available for tables whose GMs want the prose to read naturally; it is opt-in and
never the default.

## What it does not protect against

> **Anyone with filesystem access to the vault can read every secret, revealed or
> not.**

The callout hides content from *viewers of the rendered site*. It is not
encryption at rest. The GM's vault, its sync provider, and every device syncing
to it are all inside the trust boundary — the same boundary the whole content
model already has.

A security report framed as "secrets are not encrypted on disk" is therefore
documentation, not a vulnerability. This is stated here on purpose: it is
better to say it once than to have the same three-round exchange with every
reporter who assumes otherwise.

## Revealing

Reveal and unreveal are GM-only, and go through the same authorization and
`If-Match` precondition as any other write. Every reveal is written to the audit
log with who and when.

### Why revealing is a file write

Because Obsidian will sync a copy of the vault that predates the reveal, and will
periodically overwrite a revealed callout back to hidden. That is not a bug to be
avoided; it is certain. So precedence is explicit rather than an ambiguous
two-source split:

| Question | Authority |
|---|---|
| Is this secret revealed right now? | the file |
| Who revealed it, when, and was it reverted? | the ledger |

The watcher reconciles: on any change, diff the current markers against the
ledger, and re-apply a reveal that sync reverted.

### It fails toward hidden

Re-application is **capped** — a few passes per file per minute. On exhaustion it
stops and leaves the secret hidden, loudly. An accidentally revealed secret is a
far worse outcome than a reveal that arrives late or is dropped, so every failure
path in this subsystem resolves toward hiding. There is no unbounded retry, and
no conflict is ever resolved by revealing.

The ledger records how many times a reveal was reverted, which turns a
frustrating invisible sync fight into a number you can look at.

## Interaction with search

**Secret text never enters the full-text index.** The indexed plain-text body
excludes callout content outright, in every reveal state, so a search snippet
cannot carry secret text to a player or to an anonymous reader. The cost is that
secrets are not full-text searchable; a secret-aware index is a possible later
addition, and it would be a deliberate trade rather than a default.

Journal exports exclude secret content by default.

## Who can see what

| Tier | Scope | Capability |
|---|---|---|
| Instance admin | global | Register campaigns, manage users |
| Campaign GM | one campaign | Full content edit, game control, invite |
| Campaign player | one campaign | Play only; **no content write at all** |
| Anonymous | `public` campaigns only | Read-only wiki and public assets |

Games always require membership. `public` grants wiki read access and nothing
else — a public campaign is not a public game.

The redaction behaviour above is enforced by the highest-value test in the
project: for any page containing a secret, the GM's response contains the body,
a player's and an anonymous viewer's do not, and **neither contains the secret
text anywhere in the payload** — not in the HTML, not in a comment, not in a
header, not in a JSON field.

## Related

- [Content model]({{ "concepts/content-model/" | relURL }}) — where the callout lives in the pipeline
- [Security]({{ "operating/security/" | relURL }}) — the full trust boundary
