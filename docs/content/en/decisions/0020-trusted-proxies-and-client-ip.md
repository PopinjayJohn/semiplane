---
title: "0020 — A forwarding header is evidence of nothing until an operator names the proxy"
description: "Client IP resolution trusts no proxy by default, and walks the forwarding chain right to left when one is configured."
lede: "A forwarding header is a request header, so anyone who can open a socket chooses its value. That address is the input to every rate limit and ban read out of the access log later, so the default has to be the one that cannot be forged."
weight: 200
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane is self-hosted. Most operators will put it behind a reverse proxy for TLS, and some
will expose it directly. In both cases something arrives at the HTTP server claiming to be a
client address: `X-Forwarded-For`, `X-Real-IP`.

That claim is worthless on its own. `X-Forwarded-For` is a request header, so anyone who can
open a socket to the server chooses its value. This matters more than it first appears,
because the resolved client address is not only a log field. It is the input to every rate
limit, audit query, abuse report and ban that reads the access log later. A forgeable client
IP means a ban can be evaded by sending a header.

There is a second failure, and it is the one most implementations get wrong. A proxy *appends*
the address it saw, so a chain reads nearest-proxy-last:

```
X-Forwarded-For: 203.0.113.7, 198.51.100.4
                 └─ what an earlier hop believed ─┘  └─ what the nearest proxy saw ─┘
```

Walking left to right returns `203.0.113.7` — the oldest, and least verifiable, entry, and
precisely the one a client prepended. Walking right to left returns the address the nearest
proxy actually saw, which is the one thing in the header that the client did not choose.

## Decision

**No configured proxy means no header is read. A configured proxy list is the only thing that
makes a forwarding header evidence, and the chain is walked right to left.**

Concretely, in `internal/httpapi/middleware`:

- `SEMIPLANE_TRUSTED_PROXIES` is empty by default. When it is empty, the resolved address is
  the transport address and both forwarding headers are ignored without being parsed.
- With proxies configured, the chain is walked from the nearest hop backwards, across header
  lines as well as within them, and the first address outside the trusted set is the client.
- Every hop in the trusted set is skipped rather than terminating the walk, so a deployment
  with two proxies in front still resolves correctly.
- A malformed hop ends the walk and falls back to the transport address. It does *not* fall
  through to a hop further left: that value is the client-chosen one this record exists to
  avoid.
- `SEMIPLANE_TRUSTED_PROXIES` accepts CIDRs and bare addresses, and unparseable entries are
  dropped with a warning rather than refusing to boot. A typo must not take down a running
  server, and dropping an entry fails toward the transport address, which is the safe side.

## Consequences

- An operator who puts semiplane behind a proxy and forgets to configure the list gets the
  proxy's address in the logs rather than a client's. That is visible and wrong, and it is
  better than the alternative: the configuration mistake produces obviously-wrong log lines,
  while the permissive default produces plausible ones.
- The empty-list case is a real code path with a test, not a degenerate one. The first
  implementation of this walked the header and *then* asked whether each hop was trusted —
  which, with an empty list, means every hop is untrusted and the header wins. The code, its
  own comment, and the install documentation all claimed the safe behaviour; the tests are
  what caught the discrepancy.
- The transport address is used for anything the headers do not cover, including the absence
  of `RemoteAddr`, which is the normal case for in-process tests.
- IPv4-mapped IPv6 addresses are unmapped, so an operator's allowlist written in one form
  matches a client arriving in the other.

## Alternatives considered

**Trust `X-Forwarded-For` unconditionally.** What most frameworks do by default, and the
reason a deployment behind a proxy "just works" without configuration. Rejected because the
value is caller-controlled, and because the failure is silent: the access log looks correct
and the ban list does not work.

**Trust the leftmost entry, the way `X-Forwarded-For: <client>` was originally specified.**
This is the left-to-right walk, and it returns the most spoofable value in the header. It is
the right answer only if every proxy in the chain is trusted *and* the first entry cannot be
prepended — which no proxy guarantees.

**Take the rightmost entry unconditionally.** Simpler, and correct for the common
single-proxy deployment. Rejected because it breaks the moment there are two proxies, and
because it means a client that reaches the server directly controls the answer outright.

**A single trusted-proxy setting rather than a list.** Enough for one deployment and not for
the next; the list is not meaningfully more code than the setting, and the second proxy is
always the one that arrives after the first is deployed.

**Drop client IP from the access log entirely.** The most secure answer available, and
rejected because the field is genuinely useful for diagnosing a player's connection problems,
and because every framework's log tooling is better with it present. Withholding it would
also be a strange thing to do while still writing the header the client controls.
