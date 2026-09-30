---
title: "0002 — The standard library router, not Chi"
description: "net/http ServeMux carries method and pattern routing; Chi carried two advisories."
lede: "Go 1.22 gave the standard library method-and-pattern routing, which removes the last thing a third-party router was needed for. Choosing Chi anyway would mean taking a vulnerability advisory in exchange for nothing."
weight: 20
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Every Go HTTP service reaches for a router in its first week. Chi was the default answer for
years, and it is a good library. The question is whether it is still needed.

`net/http.ServeMux` gained method-and-pattern matching in Go 1.22, including path wildcards
(`{id}`) and per-method routing:

```go
mux.HandleFunc("GET /c/{slug}/wiki/{path...}", wikiHandler)
```

That covers everything this project's route scheme needs, including the `{path...}` catch-all
the campaign content routes require. `r.PathValue("slug")` reads the wildcard, so no URL is
re-parsed by hand.

Two advisories settled it. **GO-2025-3770** and **GO-2026-4316** are open-redirect issues in
Chi. Both are in a router whose entire job is deciding which handler a URL reaches — that is,
in the exact component where a parsing mistake is a security bug rather than a correctness bug.
The fix may well be adequate, but the cost of avoiding the question entirely is one import.

## Decision

**`net/http.ServeMux`.** No third-party router.

Handlers are registered with method and pattern; path values come from `r.PathValue`. Middleware
is `func(http.Handler) http.Handler`, composed by wrapping, which needs no router support at
all.

## Consequences

- Pattern matching is `net/http`'s, so its semantics are the ones to reason about — including
  its precedence rules, where the more specific pattern wins. The tests in `internal/httpapi`
  assert routing outcomes rather than assuming precedence.
- Trailing-slash and encoded-path behaviour is Go's, not a library's. Any surprise here is a
  surprise everyone can read the documentation for.
- Middleware ordering is explicit in `cmd/server`, where the chain is assembled. That is
  readable, and it makes the order a thing a reviewer can check, which a framework's implicit
  ordering is not.

## Alternatives considered

**Chi.** Excellent ergonomics, and carried the two open-redirect advisories above. Retained as
the first thing to reconsider if `ServeMux` ever lacks something this route scheme needs — but
that has not happened, and an advisory in the routing layer is not a cost worth paying for
convenience we do not use.

**`gorilla/mux`.** Also third-party, also maintained less actively than its reputation implies,
and it would carry the same advisories for the same reason.

**No router at all — hand-rolled `switch` on `r.URL.Path`.** Would work and would be a
disaster to maintain. Pattern routing with method checking is precisely the kind of thing
that should not be reimplemented inside a project that has real work to do.