---
title: "0013 — templ, pinned exactly"
description: "Compiled Go templates. Still v0.x, so the version is pinned rather than ranged."
lede: "Rendering a shell that every route depends on is not the place for a pre-1.0 library's surprises. Pinning exactly costs nothing here and removes an entire class of upgrade risk."
weight: 130
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Every page is server-rendered: the shell, the wiki, the editor, the conflict diff, the play
page. The templating layer sits under all of them, and a change in it is a change in every
response the server produces.

The obvious alternative — `html/template` — is in the standard library, is genuinely good, and
has no dependency cost at all. It is not a serious option for a component tree of this size,
though: no compile-time checking that a component's parameters match its call site, no
composition, and template parsing at init time.

## Decision

**`github.com/a-h/templ`, pinned to exactly v0.3.1020.**

templ compiles `.templ` files to Go. Two properties make it the right fit:

- **Compiled, not interpreted.** A component's parameters and its call sites are checked by the
  Go compiler. A renamed parameter or a wrong argument count is a build error, not a 500 at
  runtime.
- **Concurrent component rendering.** v0.3.1020 specifically added safe concurrent rendering of
  components, which matters because the render cache can serve the same component to concurrent
  requests.

The pin is exact, and the Makefile owns it — the same single-source-of-truth rule as the Go
toolchain version in [0001]({{ "decisions/0001-go-as-implementation-language/" | relURL }}).
CI reads the version from the Makefile rather than repeating it, so the local gate and the CI
gate cannot drift apart.

### Generated code is not committed

`*_templ.go` files are produced by `make templ`, which `make check` runs before `build`. They
are gitignored. A generated file appearing in a diff means the gate is not being run, which is
the failure this prevents.

## Consequences

- templ's v0.x status is the main risk. The pin means an upgrade is a deliberate commit with a
  changelog read, not a `go get -u`.
- `make check` now depends on `templ` being available. That is a real change to the gate, and it
  is the reason the toolchain is set up in the foundations phase rather than when the first
  template is written — a shell cannot be styled before the CSS toolchain exists.
- The generated code is in the build graph, which means `gopls` diagnostics on a `.templ` file
  require the generated `.go` to exist. Run `make templ` before expecting editor support.
- Component-level tests are ordinary Go tests that render a component and assert on the output.

## Alternatives considered

**`html/template`.** Standard library, zero dependencies, and mature. Rejected for a component
tree of this size: no compile-time parameter checking, no composition model, and template
parsing cost at init. It remains the tool for a *small* server, and it would be a defensible
choice for anything outside `internal/web`.

**A JSX-like Go runtime (gojaeco/structr, or a Go implementation of a JSX syntax).** Rejected:
it moves rendering to run time, which is the opposite of what a static asset pipeline wants, and
none of them are stable.

**Embedding a full frontend framework (Inertia, Livewire-style patterns).** Rejected because the
frontend here is unusually specific — a canvas the server must not touch, Datastar patching
around it, and an accessibility contract about exactly how many live regions exist. A framework
that owns the document owns the parts of it this project has decided deliberately.