---
title: "0005 — modernc.org/sqlite, not mattn/go-sqlite3"
description: "Pure Go, FTS5 compiled in by default, no cgo — and mattn needs both a build tag and a C compiler."
lede: "The only full-text search index in this project is SQLite's FTS5. Choosing a driver that does not have it by default would mean the search feature silently does not exist in a build nobody noticed."
weight: 50
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Search is FTS5 and nothing else. The design record settles that: an external-content FTS5 table
over `pages`, tokenised with `unicode61 remove_diacritics 2`.

Given that, the driver is not really a free choice. There are two production-quality Go SQLite
drivers, and they differ on exactly the thing this project needs.

| | `mattn/go-sqlite3` | `modernc.org/sqlite` |
|---|---|---|
| Implementation | cgo binding to the C library | Pure Go translation of SQLite |
| FTS5 | **Off unless built with `-tags fts5`** | Compiled in by default |
| Build requirements | A C compiler | None |
| Static binary | Needs matching libc | Trivially |
| Cross-compile | Painful | Works |

The `fts5` detail is the load-bearing one. A `mattn` build without the tag compiles cleanly,
passes every test that does not touch search, and returns a table-not-found error the first
time someone searches. It is a feature that is absent in exactly the builds nobody checks.

## Decision

**`modernc.org/sqlite`, pinned to v1.58.0.** FTS5 is present in every build.

## Consequences

- No cgo anywhere in the build, so `go build` produces a static binary on any platform and
  cross-compilation needs no toolchain. This keeps [0001 — one static
  binary]({{ "decisions/0001-go-as-implementation-language/" | relURL }}) true without an
  exception for the database.
- `modernc.org/sqlite` is a large dependency — it is a translation of SQLite, not a thin
  wrapper. That is a real cost, paid once, for static linking and FTS5-by-default.
- `go test -race` still needs cgo, because the race detector does. That is the sole reason gcc
  is in the toolchain.
- Driver behaviour is not identical to the C library in every corner. Anything where it matters
  is pinned and covered by a test rather than assumed.

## Alternatives considered

**`mattn/go-sqlite3` with `-tags fts5`.** Faster — it is the C library, and `modernc`'s
translation is measurably slower on some workloads. Rejected because the FTS5 tag is a trap
whose failure mode is a missing feature rather than a build error, and because it drags cgo
into every cross-compile. If SQLite performance ever matters more than the static binary, this
is the record to revisit first.

**`ncruces/go-sqlite3`, WASM-based.** Also pure Go, no cgo. Rejected as less mature and with a
smaller ecosystem than `modernc`; the FTS5 and WAL behaviour that this project depends on is
better exercised there.

**PostgreSQL.** Better concurrent writes, real multi-process support, and a genuine escape from
[0004 — the single-process
constraint]({{ "decisions/0004-single-process-constraint/" | relURL }}). Rejected on the same
grounds as recording 0004: an operator has to run and back up a second service, which breaks
the one-process premise. It remains the first answer to "what if one instance is not enough".