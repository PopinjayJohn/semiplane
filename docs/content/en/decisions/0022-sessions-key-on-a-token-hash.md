---
title: "0022 — `auth_sessions` keys on a hash of the token, because the database is the thing that gets stolen"
description: "The session token lives in exactly two places, a cookie and the response that set it; the database stores only its SHA-256."
lede: "A bearer token stored as itself means a copied database file is a list of working sessions. Storing its hash instead means the same file authenticates nobody, at the cost of one hash lookup on every request — which is not a cost at all next to the alternative."
weight: 200
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane has no session entity in the domain sense — the string "session" appears nowhere
in the interface, because an auth session is never surfaced to a user. What exists is the
mechanism: a random token in a cookie, and a row that says whether it is still valid.

That makes the row the highest-value table in the database. `campaign_state` holds a
snapshot that is worthless on its own; `auth_sessions` holds credentials that authenticate
as their owner to the full extent of their role. The threat is not exotic: the database is
a single SQLite file, which is exactly the artefact that ends up in a backup, a volume
snapshot, a `docker cp`, a support bundle, or a stolen laptop.

Storing the token means that file is a list of live bearer tokens. Anyone who reads it has
every session, and there is no way to tell an expired token from a live one without
contacting the server — at which point they have already used them.

## Decision

**The raw token exists in exactly two places: the `Set-Cookie` header that issued it, and
the browser's cookie jar. `auth_sessions.token_hash` stores the hex SHA-256 of the token,
and it is the primary key.**

- The token is 32 bytes from `crypto/rand`, base64url without padding so it is a cookie
  value needing no quoting.
- `NewSessionToken` returns the raw token **and** its hash **together**, as one return
  value's worth of two parts. This is the actual mechanism of the decision: a caller
  cannot mint a token and then forget to store its hash, because the hash is already in
  hand and the token is already the thing that belongs in the cookie. Two functions — one
  that mints and one that hashes — would be one refactor away from a token with no row.
- Hashing is a plain SHA-256, **not** the PBKDF2 of [0021]({{ "decisions/0021-password-hashing-with-pbkdf2/" | relURL }}),
  and that difference is deliberate. There is nothing to brute-force: the input is 32 bytes
  of CSPRNG output, so the search space is the whole token space, not a dictionary of human
  guesses. Stretching would add latency to every authenticated request in exchange for
  defending against an attack that does not exist.
- The token is never logged, never placed in a URL, and never included in an error message.
  A URL puts it in an access log, a `Referer` header, and a browser history entry — three
  copies that no hashing discipline can take back.

## Consequences

- Logout is a `DELETE`, not a flag. There is nothing to disable in the token's own
  representation, so revocation means removing the row, and a removed row is a token that
  cannot be presented usefully even if it is still in someone's cookie jar.
- A leaked token cannot be revoked selectively by changing a stored value; the row must be
  deleted. That is the intended trade — deleting a row is unambiguous, whereas a scheme
  that stores the token could also not hide that the token was once valid.
- Every authenticated request costs one hash and one indexed lookup. The hash is cheaper
  than the database round trip that follows it.
- Comparing a presented token is a hash and a primary-key probe, so it is not a
  constant-time comparison problem: the lookup either finds the row or does not, and there
  is nothing to leak byte-by-byte.

## Alternatives considered

**Store the token, protected by encryption at rest.** Rejected: it preserves the property
that matters least — a reader of the file still obtains working tokens — and it adds a key
to protect, store, rotate, and lose. The database's confidentiality is not something a
self-hosted single-file install can actually promise anyway; the access log at the proxy is
a larger exposure than the SQLite file.

**A signed token, with no server-side row.** JWT-shaped, and rejected on the same grounds
the single-instance rule ([0004]({{ "decisions/0004-single-process-constraint/" | relURL }}))
rejects a second server: it makes revocation impossible without keeping the server-side
list anyway, so the stateless win is not real and the bearer exposure is identical.

**PBKDF2 for the token as well.** Uniformity for its own sake. It would add roughly half a
second to every authenticated request to slow an attack against 32 bytes of randomness that
cannot be conducted. Consistency with [0021]({{ "decisions/0021-password-hashing-with-pbkdf2/" | relURL }})
is worth having where the input is human-chosen, and is not worth having where it is not.

**Opaque random session IDs in the URL rather than a cookie.** Rejected earlier in the UI
spec's own terms, and this record does not reopen it: a token in a path is a token in every
log on the way to it.
