---
title: Rules content
---

## Eight more kinds, from the data pack

Five kinds are semiplane's. The rest belong to the gameplay plugin, and this build
ships exactly one: **5e**, in two editions that are one system id and two data packs.
Its data pack declares eight kinds.

| Kind | What the page is | This campaign's |
| --- | --- | --- |
| `ancestry` | where a character is from | [[lantern-folk]] |
| `background` | what they did before the campaign | [[soldier]] |
| `class` | what they are | [[warden]] |
| `feat` | something they gained | [[steady-breath]] |
| `spell` | something cast | [[lantern-light]] |
| `item` | something carried, consumed or worn | [[mending-draught]] |
| `condition` | something a placement is in | [[poisoned]] |
| `creature` | a rules object with statistics | [[mirror-hound]] |

Each of those is **data in a pack, not Go**. A second system can declare its own
`ancestry` and `condition` without colliding with this one's, because §10.2.1 requires
a system to be able to describe its game in its own vocabulary and semiplane's job is
to not be in the way. What semiplane refuses is a pack claiming one of the five it
owns — the asymmetry is deliberate and it points one way: a client must be able to
draw a placement it has no rules for.

`condition` is the clearest example of why the split is at that line. The live
tablet applies a condition to a placement without knowing what the condition *does*,
because `condition` is a kind semiplane owns. A rules engine deciding whether being
prone helps or hurts is a different program, on the other side of the boundary.

## What happens to a kind this build does not have

A page whose `kind:` is not registered **degrades to prose**. It renders. Its links
still resolve, its embeds still inline, its tables still lay out — the only thing lost
is the page-type component that would have framed it.

The degradation is not a fallback in the page and not a warning banner. It is the
answer from the registry, and it is the *same* answer in both directions that matter:

- a page with **no** `kind:` is prose, and
- a page with a `kind:` this build has never heard of is prose.

Which means a typo in a kind name is silent. `kind: tokne` is not an error; it is a
prose page. The protection against it is not a validator but a gate: `make demo-check`
counts the kinds this build registers against the kinds pages actually carry, and it
counts the **honoured** kind rather than what the front matter claims — so a page
declaring a kind nothing registers does not satisfy the requirement, and a demo
missing a kind fails the build rather than shipping.

The same applies to a campaign whose plugin was removed. Its pages still answer 200,
with their content, because the wiki does not require a rules system to render a page.
The **game** refuses to start, and names the system id it wanted. A wiki and a
tabletop are two features and refusing one is not a reason to take the other down.

## Editing content

Only a Game Master can edit a page. A player reading a campaign gets the rendered page
and no editor, and the editor's route answers them the same way it answers a
non-existent URL — so a player cannot map the vault by watching which edit paths
return what.

An edit carries an `If-Match` validator, and a save against a stale one is a **409 with
a conflict view** showing both versions rather than a last-write-wins overwrite. A
vault is edited by a person and synced by a client, and the client is frequently
running against a copy from a minute ago.

The save itself is atomic: a temporary file beside the target, then a rename. That is
also why the file watcher watches **directories** and not files — a watch on a file
follows the inode, and an atomic rename replaces the inode, so a file watch would be
destroyed by semiplane's own write path.

Back to [[index]].