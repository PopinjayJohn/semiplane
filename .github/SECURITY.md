# Security policy

## Supported versions

semiplane is pre-release. There is no published version yet, and unreleased
`main` is not a supported target.

| Version | Supported |
|---|---|
| Latest release | Not published yet |
| `main` | No — development branch, expect breakage |

Fixes land on `main`. When a release is published, this table becomes
"latest release: yes, everything older: no", and a published release is the
first supported target.

## Reporting a vulnerability

Use [GitHub's private vulnerability reporting](https://github.com/PopinjayJohn/semiplane/security/advisories/new).

**Please do not open a public issue for a vulnerability** — not even to ask
whether something is a problem. A public issue is a public disclosure, and
disclosure timing is the reporter's decision, not the maintainer's.

The issue template's chooser links here rather than to a public issue, and the
bug report form asks you to say so explicitly if private reporting is not
available to you.

**Expected response:** an acknowledgement within a few days, and either a fix or
a clear statement that the report is by-design within a reasonable time. This is
a single-maintainer project, so that is a real estimate rather than a
best-practice number. If a report is accepted you will be credited in the
advisory unless you would rather not be named.

## What is in scope

The trust boundary this project defends is stated in full under
[security](https://popinjayjohn.github.io/semiplane/operating/security/). In
short: the campaign content root is untrusted input, because an Obsidian sync
client, an Obsidian plugin, or any device syncing the vault writes into it.

Worth reporting:

- **Secret text reaching a response a non-GM viewer can receive.** Anywhere — the
  HTML body, a comment, a header, a JSON field, a search snippet, a cached
  variant, an error message.
- **Any path escaping a campaign's content root**, including through front
  matter, wikilinks, embed targets, or asset URLs. The boundary is `os.Root`, so
  anything that reaches outside it is a bug.
- **Authorisation failures** across campaign visibility, on the HTTP surface or
  on the WebSocket plane — including a missing or incorrect `Origin` check on the
  upgrade.
- **A content-existence oracle.** A 404 and a private campaign must be
  indistinguishable to an anonymous user, including in response size and timing.
- **The secret reconciliation loop misbehaving**: exceeding its cap and leaving a
  secret revealed, or converging on revealed after a sync conflict. Every failure
  path is supposed to resolve toward hidden.
- **Rule resolution that is not a pure function of *(state, intent, seed)*** —
  which breaks replay, audit, and optimistic client application at once.
- **A plugin able to write campaign state**, or to forge a broadcast in place of
  the hub's version counter.
- **A cache entry shared across campaigns**, or a non-GM response served from an
  `include_secrets` variant.

## What is not a vulnerability

Stated up front, because a report framed as any of these is documentation rather
than a bug, and saying so plainly is kinder than a three-round exchange.

- **Secrets are not encrypted at rest.** They are hidden from *viewers of the
  rendered site*, not from anyone with filesystem access to the vault. That is
  the documented boundary, and it is a deliberate trade: the content root is
  meant to be a directory of plain files you can read, sync, and back up. If you
  need the secrets protected from someone with disk access, disk encryption is
  the layer for that.
- **A GM seeing a secret in their own response.** The GM is inside the trust
  boundary by definition.
- **Live game state lost on an unclean stop.** State persists on a trailing
  debounce and on shutdown; a crash loses at most the last interval. This is
  documented in [operating](https://popinjayjohn.github.io/semiplane/operating/).
- **The absence of rate limiting on authentication.** Not implemented yet, and
  noted here so the absence reads as a decision rather than an oversight.
- **Two instances diverging.** semiplane is single-instance by design, with
  in-memory authoritative state. Running a second replica is a deployment error,
  not a vulnerability.
