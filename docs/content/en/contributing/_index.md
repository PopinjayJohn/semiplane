---
title: "Contributing"
description: "How to build, gate, and propose a change to semiplane."
lede: "One command decides whether a change is finished. Run it before you open a pull request, not after a review asks."
weight: 40
---

## The gate

```bash
make ci
```

That is `lint-verify` and then `check`, which runs in order: a format diff check,
`go build`, `go vet`, `golangci-lint run`, and `go test -race`. **No Go change is
complete until it passes.**

`make check` deliberately does not include the docs site. The Go gate must not go
red because a docs toolchain is missing; a separate `make site-check` gates the
site. Contrast `css` and `templ`, which `check` *will* depend on once they exist
— those produce runtime assets the binary serves, unlike Hugo.

## The toolchain

The shell does not source Go by default. Either use `make <target>`, which sources
it for you, or run `. /etc/profile.d/go.sh` first.

| Tool | Version |
|---|---|
| Go | 1.27.1 |
| golangci-lint | v2.14.0 |
| Hugo (docs) | v0.167.0 |
| actionlint | v1.7.7 |
| yq (label manifest) | v4.47.2 |

`make tools` installs them all. Versions live in the `Makefile` and nowhere else,
so the local gate and the CI gate cannot drift apart.

## Layout

```
cmd/server/       thin main: config, wiring, signals
internal/config/  env parsing, no project dependencies
internal/domain/  pure types and rules, no I/O
internal/store/   persistence and migrations
internal/httpapi/ handlers, routing, middleware
internal/web/     templates and static assets
docs/             this site
```

Dependencies point inward. `httpapi` → `domain`/`store`; `domain` imports nothing
from the project.

## House rules the linter will not catch

- **Migrations are forward-only.** Add a new one; never edit a shipped one.
- **Prefer the standard library.** Check `go.mod` before adding a second library
  for the same job.
- **Templates get `data-testid`** on anything under test. Assert on that, never on
  CSS classes or DOM shape.
- **Do not silence a linter.** Fix the code, or justify the suppression inline
  with `//nolint:lor // why`. A linter `excludes` entry added to quiet a finding
  is a finding that will come back.

## Working with agents

This repository is developed with AI agents and the `AGENTS.md` file is the
operational contract they read: toolchain, the gate, the layout, and the
conventions. There are three skills under `.opencode/skills/` carrying the
detail `AGENTS.md` deliberately omits — the quality gate, Go conventions, and
browser end-to-end work.

The pull request template asks whether an agent produced a change and which
instructions it followed. That is not ceremony: an agent-authored change that
skipped the quality-gate skill is exactly the failure the question exists to
surface.

## Proposing a change

1. **Check the [roadmap]({{ "roadmap/" | relURL }}) and the
   [design records]({{ "design/" | relURL }}) first.** Many questions are already
   answered, and a few are already open questions you can unblock.
2. **Open an issue** using one of the forms. A feature request wants the
   *problem*, not the solution — a solution written early tends to lock in an
   approach before anyone has checked whether it is the cheapest one.
3. **Open a pull request.** The template's design-impact checklist is not
   boilerplate: data model, URL scheme, realtime protocol, plugin authority,
   secret handling, access tiers and cache keys are the surfaces that are
   expensive to undo.

## Label vocabulary

Issues carry four orthogonal label axes, defined in `.github/labels.yml` and
applied with `make labels`:

| Axis | Cardinality | Meaning |
|---|---|---|
| `type:` | **exactly one** | bug, feature, docs, question, chore |
| `area:` | at most one | the product surface you were touching |
| `status:` | at most one | the lifecycle: needs-triage, ready, in-progress, blocked, needs-design, wontfix |
| flags | zero or more | `breaking`, `security`, `good-first-issue` |

There is deliberately no `priority:` axis. Without a published triage cadence it
rots into noise, and a stale `priority:P0` on a two-year-old issue is actively
misleading.

`make labels-check` runs in CI and fails if the repository's labels and the
manifest disagree. The manifest is the source of truth; the sync script is the
writer, because GitHub offers no way to declare labels in a repository.
