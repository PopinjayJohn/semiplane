---
title: "0023 — Work-item branch names drop the `phase/` prefix"
description: "A branch named `a/b` and a branch named `a/b/c` cannot both exist in git, so work-item branches are `NN-slug/<work-item>` rather than `phase/NN-slug/<work-item>`."
lede: "The documented branch topology was not expressible in git's ref namespace. The parent branch is a file, the child needs that same path to be a directory, and git refuses the combination rather than picking a winner."
weight: 23
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

`AGENTS.md` documented the branch topology as:

```
main
└── phase/NN-slug
    ├── phase/NN-slug/<work-item>
```

It was in the plan, in `AGENTS.md`, and it looked obviously fine. Phases 0 and 1 both
merged as a single branch with no children, so the topology was never exercised. Phase 2
was the first phase to actually fan out, and the first `git branch` of a work item failed:

```
fatal: cannot lock ref 'refs/heads/phase/02-identity/domain-types':
'refs/heads/phase/02-identity' exists; cannot create
'refs/heads/phase/02-identity/domain-types'
```

This is not a permissions problem, a dirty repository, or a stale ref. Git stores a loose
branch as a **file** whose path is the branch name, and packs loose refs otherwise. For
`phase/02-identity` to exist, `refs/heads/phase/02-identity` is a regular file. For
`phase/02-identity/domain-types` to exist, `refs/heads/phase/02-identity` must instead be a
**directory** containing a file named `domain-types`.

A single path cannot be both. Git enforces this rather than resolving it, and it is the
same constraint that makes a tag `v1` and a tag `v1.1` mutually exclusive. `git branch`
rejects the create; `git worktree add` propagates the same failure, which is what Agent
Manager reported as a worktree creation failure.

The `phase/` prefix was the only thing in the way. `refs/heads/phase` is a directory either
way, and there is nothing wrong with nesting beneath it — the conflict is specifically
between a branch and its own descendant.

## Decision

A work-item branch **drops the `phase/` prefix**:

```
main
└── phase/NN-slug                    one per phase, one PR to main
    └── NN-slug/<work-item>          one per sub-agent, one PR to the phase branch
```

`phase/02-identity/domain-types` becomes `02-identity/domain-types`. The phase slug is
still in the name, so the branch still sorts and reads as a child of its phase, and the
"cut from the phase branch, never from `main`" rule is unchanged — it is a naming
convention, not a ref-namespace workaround that changes where the commit comes from.

The phase branch itself keeps the `phase/` prefix. It has no children, so it is
unaffected.

## Consequences

- The topology in `AGENTS.md` and in the delivery plan is corrected. The plan is a dated
  record and is not edited; this record and the `AGENTS.md` correction are the correction.
- `02-identity/...` and `phase/02-identity` share a slug but no ref path, so there is no
  conflict. Verified before adopting the name, not after.
- A reader comparing a branch name against the plan will see a difference. That is why
  `AGENTS.md` now says this in the topology section itself, with a link here, and why the
  rule says never to tidy the name back. A future agent that "corrects" the naming to
  match the older diagram reintroduces an outage that costs a full fan-out to discover.
- The cost is cosmetic: a work-item branch does not literally start with `phase/`, so a
  `git branch --list 'phase/*'` no longer finds work in progress. `git branch --list
  '*/<slug>/*'` does, and the phase branch remains the thing that merges to `main`.

## Alternatives considered

**Keep `phase/NN-slug/<work-item>` and rename the phase branches** (for example
`p02-identity`, or `02-identity` outright, dropping `phase/` everywhere). This removes the
conflict too, and it is arguably more regular — one prefix rule instead of two. It was
rejected because the phase branch names are already in `AGENTS.md`, in the delivery plan's
thirteen-row phase table, in merged history, and in the branch-protection configuration a
repository administrator has set up. Renaming the thing that actually merges to `main` is
a much larger change than renaming the children, and the children are the part that has
not shipped yet.

**Nest one level deeper** (`phase/NN-slug/children/<work-item>`). This works, because
`phase/NN-slug/children/` never has to be a file. It was rejected as pure noise: the extra
level encodes nothing, since the parent is already unambiguous from the phase slug.

**Store the work-item branches only as remote refs, or use tags, or use a detached
`HEAD` per worktree.** All three dodge the namespace. All three were rejected: they make
the work invisible to `git branch`, they lose the association with the phase branch, and
the one-instance-per-worktree property that makes a worktree reviewable is exactly what
the branch name is for.
