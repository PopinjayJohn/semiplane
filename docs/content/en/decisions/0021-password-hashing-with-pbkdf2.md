---
title: "0021 — A password is stored as PBKDF2 at OWASP's work factor, and carries its own iteration count"
description: "Password hashing is PBKDF2-HMAC-SHA256 at 600,000 iterations with a per-hash salt, encoded so the iteration count travels with the hash."
lede: "There is no bcrypt or Argon2 dependency to take, because Go 1.27 ships PBKDF2 in the standard library and OWASP puts its recommended work factor in writing. The part that matters is not the algorithm — it is that the hash records the parameters it was made with, so the cost of a stolen database can be raised later without stranding a single account."
weight: 200
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane stores a human-chosen password and must survive having its database file
stolen: a stolen laptop, a backup on a shared volume, a support ticket attachment, a
snapshot in a provider's object store. A plain hash of the password is worthless against
that, and so is a salted hash of a *fast* function — a GPU does billions of SHA-256 per
second, so salt alone buys nothing against an attacker who has the hash.

Two costs pull against each other. Verification happens on every login, so the work factor
is paid by every player who logs in. Attack happens once, offline, and is paid by the
attacker. The right answer is a deliberately slow function, with the work factor set by
someone who measured what a modern GPU does rather than by someone optimising for their
test suite.

The second problem is temporal, and it is the one that gets skipped. The recommended work
factor is a moving target: OWASP has revised it before and will again. A hash that does not
record the parameters it was created with cannot be re-verified after the factor is raised.
The two obvious workarounds are both bad — one strands every existing account, the other
means a deploy can never raise the factor without a migration that rewrites every row while
holding the plaintext it is trying to protect.

## Decision

**PBKDF2-HMAC-SHA256 at 600,000 iterations, a 16-byte per-hash salt from `crypto/rand`,
a 32-byte derived key, and an encoded form that carries the iteration count.**

- The encoded hash is a single self-describing string:
  `pbkdf2-sha256$<iterations>$<salt-b64>$<key-b64>`. Verification reads the count out of
  the string, never from a constant, so an old row keeps verifying at its own cost.
- Verification compares with `crypto/subtle` **after** a length check. A constant-time
  comparison over slices of different length leaks the length and panics otherwise; the
  length check is what makes the comparison safe, so it is not an optimisation.
- `pbkdf2MaxIterations` caps what the parser accepts, at 10,000,000. An honest hash never
  exceeds the work factor this build mints, so a larger number is a corrupted or hostile
  row, and accepting it would turn every login into a multi-second stall — a denial of
  service that reads like a slow disk.
- Raising the work factor is a constant change and nothing else. New hashes get the new
  count; old hashes keep verifying and get re-minted with the next successful login.

## Consequences

- The test suite is slow. This is the expected consequence of the decision and the next
  reader will be tempted to drop the count to make a table-driven test finish quickly. The
  tests that do not need the real work factor take a lower one through an unexported
  **parameter**, never a package variable — a variable would let one test leak a lowered
  factor into another and quietly weaken the assertion that matters.
- There is no dependency to upgrade. That is the whole reason for PBKDF2 over Argon2id,
  which is the better primitive and is not in the standard library: a self-hosted project
  gains a transitive supply-chain surface with every added module, and this is a decision
  that can be made once and then not maintained.
- A login on a slow single-board CPU takes longer than on a laptop. At 600,000 iterations
  this is hundreds of milliseconds, which is acceptable for a login and would not be for
  an authenticated request — which is exactly why sessions exist (0022).
- Verification is not constant-time with respect to *failure*: a row that does not exist
  fails faster than a row whose password is wrong. That difference is a user-enumeration
  signal, and it is handled where it belongs — the login route answers both identically.

## Alternatives considered

**Argon2id, the current OWASP first recommendation.** The better primitive: memory-hard, so
it resists GPU attack by construction rather than by iteration count. Rejected because it is
not in the standard library, and semiplane's dependency policy is "prefer the standard
library" precisely because a self-hosted install is a supply-chain decision made by one
person who did not write the dependency. Recorded here as a known shortfall rather than
hidden: if semiplane ever grows a dependency for a reason that outweighs this, Argon2id is
the migration, and the encoded format above has room for a second algorithm prefix.

**bcrypt.** Long the default, and its 72-byte input limit truncates a long passphrase
silently — two passphrases sharing a 72-byte prefix are the same account. Also a
dependency.

**A slow hash with no stored parameters.** Workable until the moment the factor is raised.
Rejected because it makes the cost of a stolen database permanently fixed at the moment it
was cheapest to steal.

**Salt only, with a fast hash.** Salt defeats precomputed dictionaries and rainbow tables.
It does nothing against an attacker who has the hash, which is the case that matters.

**Comparisons with `==`.** A byte-by-byte early exit leaks, through timing, how much of a
guess was correct. Cheap to get right and cheap to get wrong on a later refactor, so the
comparison is asserted by a test rather than left to review.
