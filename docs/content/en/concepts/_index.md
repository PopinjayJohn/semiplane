---
title: "Concepts"
description: "The words semiplane uses, and why the obvious words are the wrong ones."
lede: "A wiki and a tabletop share most of their nouns, and the overlaps are where confusion starts. These are the definitions the code is written against."
weight: 20
---

## Read this before anything else

The word **session** is the problem this page exists to solve. In tabletop
software it usually means one sitting of a game. In semiplane it must not, because
two different things want the name and neither of them is a sitting of a game.

| Term | Means | Never means |
|---|---|---|
| **Campaign** | The tenancy boundary, and the slug in every URL. One content root, one membership list, one live tabletop, one gameplay system. | Not a workspace, a server, or a tenant |
| **User** | A global account. A member of many campaigns. | Not a campaign, not a seat |
| **Page** | One markdown file at `(campaign, path)`. Prose and game objects are the same thing. | Not a revision |
| **Game object** | A page with a registered `kind`: a token, a scene, a handout, a journal, an index. A *definition* — durable, editable in Obsidian. | Not an instance of anything |
| **Ruleset** | The resolved rule configuration for a campaign: gameplay system, data-pack overlays, enabled house-rule modules. Has a `ruleset_version`. | Not a settings screen |
| **Placement** | A game object *instance* on a live map. Position, current hit points, conditions. **Runtime only.** | Not a file, not a definition |
| **Campaign state** | The single in-memory and persisted row holding all live placements and turn state. | Not a save file per user |
| **Auth session** | A browser login. | Never rendered in the interface at all |

There is no `world` entity and no `session` game entity. A campaign has at most
one live tabletop, which is why `/c/{slug}/play` needs no session identifier.

**In the interface, "session" is never used.** The game route is labelled *Game*
in the navigation, the header, and the footer. The URL parameter keeps its
generic name so the route table stays unchanged; only the label differs.

## The one rule that explains the data model

**Definitions are files. Placements are runtime.**

A token's maximum hit points, armour class, portrait, and size live in the page's
front matter, written by the GM in Obsidian or the browser. A token's *current*
hit points and *current* position live in campaign state, written by the game.

The reason is collision, not tidiness. If current hit points lived in the file,
every damage event would rewrite it — and Obsidian Sync would overwrite the
change on its next sync, producing a permanent conflict storm between two writers
with no shared lock. Placements are never written to files. That is a hard rule.

## Pages, and the `kind` field

Everything is a markdown file with YAML front matter. A page with no `kind` is
prose. A page with a `kind` the loader recognises is a game object:

```markdown
---
title: Captain Aldric
kind: token
size: medium
ac: 16
hp_max: 45
portrait: assets/aldric.png
---

A retired harbour officer with too many secrets and a very good sword arm.
```

Only pages whose `kind` is registered are validated by the loader, so malformed
front matter in a lore page is inert — it cannot break the tabletop. The set of
recognised kinds is registry-backed rather than a fixed enum: built-ins register
`token`, `scene`, `handout`, `journal` and `index`, and a plugin may add more. An
unknown `kind` degrades to prose, which is why removing a plugin never destroys
content.

## Ownership of kinds

semiplane owns the kinds that are not about rules, because that is what lets a
system sharing nothing with D&D 5e register its own vocabulary without collision:

| Owner | Kinds | Why |
|---|---|---|
| semiplane — wiki | `journal`, `handout`, `index` | Content, not rules. No plugin should redeclare these. |
| semiplane — tabletop | `token`, `scene` | Present on every map in every system. Parameterised by the system, not defined by it. |
| **gameplay plugin** | `spell`, `class`, `feat`, `creature`, `ancestry`, … | Genuinely system-specific, declared by the plugin. |

`token` and `scene` being semiplane-owned is the load-bearing part: the map
renders placements, fog, and initiative order without knowing any rules, so a
new system needs no client work at all.

## Caching, and the one exception

Rendered content is **permission-neutral by construction** — the same bytes for
every viewer who is allowed to see the page at all. The cache key is
`(campaign, path, content hash, include_secrets)`, and `include_secrets` is true
only for the campaign's GM. That is why there are exactly two cached variants per
page rather than one per user.

Validity is keyed on the **content hash, not the modification time**: sync paths
preserve or coarsen mtime, and one-second filesystem granularity lets two
distinct writes collide.

The single exception is the `[!secret]` callout, covered next.

## Read next

- [Content model]({{ "concepts/content-model/" | relURL }}) — markdown, front matter, links, embeds
- [Secrets]({{ "concepts/secrets/" | relURL }}) — GM-only content, and what it does and does not protect
- [Plugins]({{ "concepts/plugins/" | relURL }}) — how a gameplay system is defined
