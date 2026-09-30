---
title: "Security"
description: "The trust boundary, what semiplane defends against, and how to report a problem."
lede: "A self-hosted wiki holds other people's campaign: rules, characters, and the things their players are not supposed to know yet."
weight: 31
---

## The trust boundary, stated once

> semiplane's content root is **untrusted input**. Anyone with filesystem access
> to the vault sees everything, including unrevealed secrets.

The vault is written by an Obsidian sync client, by Obsidian plugins, and by
every device that syncs it. Any of those may be shared, modified, or compromised
without the GM's involvement. semiplane treats that the way a web application
treats a database row: parsed defensively, resolved within an enforced boundary,
and never trusted to be well-formed.

Everything below follows from that.

## What is defended

| Boundary | Requirement |
|---|---|
| **Path traversal** | Every path — URLs, front matter, wikilinks, assets — is resolved inside a per-campaign `os.Root`, created at campaign registration. Not `filepath.Clean` plus a prefix comparison. |
| **Stored cross-site scripting** | Raw HTML in campaign content is never enabled, and output is sanitised before it is cached. A player cannot be made to execute script by a GM's sync. |
| **YAML bombs** | Front matter is attacker-reachable, so the parser bounds alias expansion and document size. |
| **Search leakage** | Search queries are joined against campaign visibility. A bare query must not return private page titles to an anonymous reader. |
| **Asset leakage** | Assets inherit campaign visibility, and no cache entry is ever shared across campaigns. |
| **Authorisation on the realtime plane** | A WebSocket upgrade authenticates identically to HTTP. Origin is checked explicitly on upgrade. |
| **Secret leakage** | A secret body is absent from every non-GM response — not hidden with CSS, not in a comment, not deferred to the client. |
| **Plugin authority** | A UI plugin cannot write campaign state. It dispatches intents that a gameplay system resolves, and cannot forge a broadcast. |
| **Content existence oracle** | A 404 and a private campaign are indistinguishable to an anonymous user. Both return the same body shape. |

## What is not defended, and why that is the design

- **Encryption at rest.** The vault is a normal directory of normal files. If you
  need the secrets protected from someone with disk access, disk encryption is
  the layer for that. See [secrets]({{ "concepts/secrets/" | relURL }}).
- **Multiple instances.** See [operating]({{ "operating/" | relURL }}) — in-memory
  game state is process-local, and a second instance diverges silently.
- **A compromised host.** semiplane runs with the privileges of the user you run
  it as. It does not sandbox itself against a root on the same machine.
- **Runtime third-party plugins.** Plugins are compiled in. There is no mechanism
  for loading untrusted code at runtime, which removes an entire category of
  attack surface at the cost of the flexibility a plugin marketplace would have
  offered.

## Supplied content is treated as hostile

Two defaults follow from the threat model and are worth stating because they look
over-cautious until you have been bitten:

- **Raw HTML in markdown is disabled.** Always. This is why `{{statblock}}` and
  `{{dice}}` emit sanitised HTML rather than trusting the document: the document
  is untrusted, so the extension's output is sanitised too.
- **Symlinks inside a content tree are rejected.** Following them would let a
  synced vault point at files outside its own root, and the watcher's behaviour
  around them is documented as uncertain.

## Reporting a vulnerability

Please report privately through
[GitHub's private vulnerability reporting](https://github.com/PopinjayJohn/semiplane/security/advisories/new).
**Do not open a public issue for a vulnerability** — not even a
"is this a problem?" question, because a public issue is a public disclosure.

**Worth reporting:**

- Secret text reaching any response a non-GM viewer can receive — in the body, a
  comment, a header, a JSON field, a search snippet, or a cached variant.
- Any path escaping a campaign's content root, including through front matter,
  wikilinks, or asset URLs.
- Authorisation failures across campaign visibility, or on the WebSocket plane.
- A missing or incorrect `Origin` check on the WebSocket upgrade.
- The secret reconciliation loop exceeding its cap and leaving a secret revealed,
  or converging on a revealed state after a conflict.
- Rule resolution that is not a pure function of *(state, intent, seed)*.
- A plugin being able to write campaign state or forge a broadcast.

**Not vulnerabilities, and reported as such:**

- Secrets not being encrypted on disk. That is the documented boundary.
- Any indication that secret text is present in a response to a **GM**. A GM is
  inside the trust boundary by definition.
- In-memory state being lost on an unclean stop. The debounce cadence and the
  crash floor are documented.
- Missing rate limiting on authentication. Not implemented yet; noted so the
  absence is a decision rather than an oversight.

## Response

semiplane is a single-maintainer project in its pre-release phase, so this is
honest rather than impressive: an acknowledgement within a few days, and a fix or
a clear statement that the report is by-design within a reasonable time. If a
report is accepted, credit is offered in the advisory unless you would rather not
be named.
