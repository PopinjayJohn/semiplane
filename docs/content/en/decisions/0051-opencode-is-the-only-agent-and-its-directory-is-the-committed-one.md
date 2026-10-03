---
title: "0051 — OpenCode is the only agent, and its directory is the committed one"
description: "The repo carried two agent configs and one agent's directory, with `.opencode/skills` a symlink into `.kilo/skills` and a `.gitignore` rule holding that symlink out of diffs. Everything an agent reads now lives under `.opencode/`, and `kilo.json` is gone."
lede: "`.opencode/skills` was a symlink rather than a directory, which meant a skill the config resolved through a path that no diff ever showed. The arrangement worked and cost nothing on the day it was written, and it had one failure mode nobody could see: a file could be deleted, or a worktree created, and the resolution would change without a commit to point at."
weight: 3
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The repository was developed with Kilo and then adopted for OpenCode, and the
adoption was deliberately partial. `kilo.json` stayed the committed agent
config; `.opencode/opencode.jsonc` was committed beside it as its mirror. The
mirror was not independent — its `skills` array pointed at `./.kilo/skills`, and
a local `.opencode/skills` symlink existed so tooling that expects a
`.opencode/skills` path would find one. That symlink was untracked, and
`.gitignore` carried a rule to keep it that way:

```
# OpenCode resolves the skills through .opencode/opencode.jsonc, which points at
# the committed .kilo/skills. The local symlink is a convenience for tooling that
# expects a .opencode/skills path, not a tracked file.
.opencode/skills
```

So the repo described three locations for one set of files. `.kilo/skills` held
them, `.opencode/skills` pointed at them, and `opencode.jsonc` named the first
while the agent's own discovery convention names the second. Everything else —
three skills, four design records, the ignored-worktree path — lived under
`.kilo/`.

The mirroring also bought a second permission map. `kilo.json`'s `permission`
object and `opencode.jsonc`'s `permissions` array were the same policy written
in two dialects, and two answers to "what may this agent do" is one more than a
repository can check.

## Decision

**`.opencode/` is the only agent directory, and it holds the committed files.**

- `.kilo/plans` moved to `.opencode/plans`. Four dated design records, published
  verbatim by the `design-record` shortcode.
- `.kilo/skills` moved to `.opencode/skills`, which is OpenCode's native project
  skill location. This is the load-bearing part of the change: the files are now
  where discovery looks, not behind a pointer.
- `kilo.json` is deleted. `.opencode/opencode.jsonc` is the repository's only
  agent config and the only permission map in the tree.
- `.gitignore`'s `.opencode/skills` rule is removed, because the path is now
  tracked content rather than a local convenience, and `.kilo/worktrees/`
  becomes `.opencode/worktrees/`.

**The design records are not rewritten.** Their bodies still say `.kilo/plans/`
and `kilo.json`, and the two records published on `/design/architecture/` and
`/design/ui-ux/` will render those paths verbatim. That is the intended outcome.
AGENTS.md makes the design records immutable and names the remedy for a record
that has been overtaken: this record, plus an entry in the design index's "Known
staleness". Editing a record to agree with the present would destroy the one
property that makes it worth keeping.

## Consequences

**A checkout resolves its own skills with nothing outside it.** `.opencode/skills`
is an ordinary tracked directory, so `git worktree add` produces a worktree whose
skills exist. The symlink it replaces was invisible to every Git command, and a
symlink that is invisible is a symlink whose deletion is invisible too.

**`skills` in `opencode.jsonc` is now redundant.** `.opencode/skills` is
discovered without it. It is kept as an explicit statement of what the repo
ships, and it names the same directory discovery already finds, so it is a
description rather than a second door. Removing it would also be correct; leaving
it is what makes the intent legible to a reader who has not read this record.

**`make site-plans` is unchanged apart from its source.** `PLAN_SRC` follows the
move to `.opencode/plans`, and the records are still *copied* into
`docs/assets/plans/` rather than mounted. Hugo sandboxes `os.ReadFile` to the
project root, so `../.opencode/plans/...` returns empty rather than erroring —
the silent-empty-page failure the shortcode exists to prevent. See the comment
in `docs/hugo.toml`.

**Every phase-9 worktree picks this up when its branch merges.** Branches cut
before this record still carry `.kilo/` and `kilo.json`, and a worktree on one of
them resolves its skills through the old path. That is correct: the config is
per-branch, and a branch that has not taken the migration should not pretend it
has.

**The published design records now describe a directory that does not exist.**
The banner above each one already says a record may describe behaviour the code
does not have. This is the same class of statement applied to a path.

## Alternatives considered

**Keep `.kilo/` as the committed location and the symlink as the pointer.**
Rejected. It works, which is what made it tempting, and the cost is a class of
failure with no diff attached to it: the agent reads three skills that are not in
its checkout, through a path no commit mentions, and the first symptom is a
skill that will not load. The same layout also needed a `.gitignore` rule whose
only job was to hide a file the config depended on.

**Keep both agent configs and mirror forward.**
Rejected. The mirror already existed and had already drifted in the shape the
dialects allow — an `enabled: false` in one file and a `disabled: true` in the
other, permission maps ordered by different rules. Two files that must agree,
cannot be diffed against each other, and are only checked by whoever edits last.

**Rewrite the records' internal paths.**
Rejected, and this is the decision the record exists to prevent being made again
by accident. It compiles, it looks tidy, and it converts a dated record into a
living document — after which nobody can tell what was decided when, and the
"Known staleness" list loses the one entry that documents it. The staleness is
recorded in the design index instead, which is where a reader arrives.

**Move the records under `docs/content/en/design/` and drop `.opencode/plans`.**
Rejected. Two of the four records are published; the other two — the repository
layout plan and the phased delivery plan — are agent working documents that the
site never renders. Putting all four under `docs/content/` would make the site's
content tree the owner of files it does not publish, and `make site-plans` would
become a no-op for half its input.
