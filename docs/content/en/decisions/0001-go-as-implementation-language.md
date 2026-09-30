---
title: "0001 — Go 1.27 as the implementation language"
description: "One language, one process, one binary — and what a static toolchain costs."
lede: "semiplane ships as a single Go binary with no runtime and no external services. That is the whole deployment story, and everything else in this record exists to protect it."
weight: 10
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane is a self-hosted tool. A group runs it on a machine they control, keeps campaign
content in an Obsidian vault they already sync, and expects it to still work in three years
without a migration project. That is a different requirement from most software, and it is the
requirement that picks the language.

The alternatives were considered against three constraints:

- **One artifact.** A tabletop server is a long-running process holding authoritative game
  state. Anything that needs a runtime, a package manager, or a second process to start
  violates the deployment story.
- **Static linking.** The binary must run on a container base image with no libc surprises and
  no cgo toolchain.
- **A single contributor who is an AI agent.** The operational contract is `make check`. The
  language has to be one an agent can write correctly without asking.

## Decision

**Go 1.27.1.** Standard library `net/http` for HTTP and the router, `log/slog` for structured
logging, `os.Root` for path confinement, and `embed` for static assets.

The version is pinned in `go.mod` and nowhere else. CI reads it with `actions/setup-go`'s
`go-version-file` rather than repeating the number, so there is exactly one place to update and
no way for the local gate and the CI gate to disagree about which Go compiled the code.

## Consequences

- `go test -race` requires cgo and therefore gcc. The container ships it; `AGENTS.md` records
  the apt line for a fresh one. This is the only place cgo appears, and it is a test-only cost.
- One binary means one thing to back up beyond the vault: the SQLite file. See
  [0006 — the content root is the source of truth]({{ "decisions/0006-content-root-is-source-of-truth/" | relURL }}).
- The standard library is the default answer to "do I need a library for this". `AGENTS.md`
  requires checking `go.mod` before adding a second library for the same job, because the
  alternative is a dependency surface nobody chose deliberately.

## Alternatives considered

**Rust.** Better guarantees and a smaller binary. Rejected: the build toolchain is heavier
than the thing it builds, and an agent writing safe Rust for a project with a lot of string and
I/O glue produces worse results at this scale than one writing Go.

**TypeScript with a Node server.** The frontend half would have been native, and `Node 24` is
present in the development image. Rejected: it reintroduces a runtime and a package manager,
and the project pins "no Node" precisely so the asset pipeline stays a standalone binary.

**Python.** Fast to write, and the shortest path to a first prototype. Rejected on the same
grounds: a runtime, plus a packaging story, in a tool whose premise is that the file you copy
to your server is the file you run.