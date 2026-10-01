---
title: "0032 — Six signals beyond the architecture record's list"
description: "§13.2 fixes the observability contract and phase 4 adds six names to it, because the alternative was a hole in S-12.3 rather than a smaller surface."
lede: "The content pipeline emits six event names the architecture record does not list. That is a deviation from an immutable record, so it is corrected here. The alternative was not silence — it was emitting through `slog` at the call site, which is where `slog.Any` accepts anything."
weight: 223
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The
[architecture overview]({{ "design/architecture/" | relURL }}) §13.2 fixes the
observability contract: a set of named signals, with three never below error, and the rule that
every event carries `campaign_id` and, where applicable, `path`, and **no event carries secret
content, file contents, or dice results** (S-12.3).

Phase 4 builds the page indexer, which fails in ways §13.2 does not name. A settled change cannot
be applied. A file the walk found is not in the index. A page is indexed but its front matter did
not interpret. A rename left the old row behind. A directory rename moved a subtree.

Three answers were available, and two of them are the ones a reader would expect.

**Emit nothing.** The indexer would be the one subsystem in the content pipeline with no
telemetry, in a project whose stated position is that "something stopped working and the process
still answers `/healthz`" is the failure mode that matters. The four cases above are all instances
of exactly that: a GM saves a page, the save returns 200, and the page is absent from search until
the next rescan. From outside, that is indistinguishable from a save that worked.

**Fold them into the §13.2 names.** `index.change_failed` becomes `content.render_error`, which is
the closest fit and is wrong: it reports that a page would not render, when in fact the page
renders perfectly and the *index* is behind. A signal whose name misdescribes its cause is worse
than a missing one, because it sends an operator to look at the renderer.

**Add to the list.** This is a deviation from a published record.

## Decision

Six event names are added, and the deviation is recorded here rather than by editing §13.2.

| Name | Level | Counted | Meaning |
|---|---|---|---|
| `index.change_failed` | error | yes | A settled change could not be applied |
| `index.page_skipped` | warn | yes | A file the walk found is not in the index |
| `index.page_degraded` | warn | yes | Indexed, but its front matter was inert (S-3.3) |
| `index.rename_source_left` | warn | yes | A rename left the old row; the prune removes it |
| `index.renamed` | debug | no | A subtree moved; routine work |

And one on the watcher's side, added by the same phase for the same reason:

| Name | Level | Counted | Meaning |
|---|---|---|---|
| `content.settle_failed` | warn | yes | The settle filter could not `stat` a path for a reason other than its absence |

That one is worth its own paragraph, because it is the case where the alternative was
the worst kind of quiet. S-4.3's confirmation requires two `stat` samples, and a `stat`
fails for three different reasons that must not be confused: **the path is gone** (a
settled removal, delivered as `OpRemove`), **the path cannot be read** (a permission
change, an EIO, a mount that vanished), and the second is *not* a removal. Treating them
as the same thing means a page silently stops being indexed and nothing in the log says
so — which from outside is indistinguishable from a vault nobody is editing. The
settle filter reports the second case and drops the path; the next rescan repairs it.

The deviation is accepted on two conditions. First, every one of them is emitted through
`observability.Index`, a typed surface in the one package that decides what a log line may carry —
**not** through `slog` at the call site. Second, the decision is visible in one more place: the
`AllEventNames` list and its test, which is a deliberate count rather than a set, so a sixth
addition without a record fails the build.

### Why typed, given that this is a record about event names

The indexer was written first, against no such surface, and it emitted through `slog` at seven call
sites. That works, and it is how this repository's other signals were written before
`internal/observability` existed. It is also the hole S-12.3 is written against.

`EventAttributes` exists so that there is no field a page body could be passed through. A call site
that logs directly has `slog.Any` and `slog.String` and no such constraint — `slog.Any("detail",
raw)` compiles, ships, and carries a `[!secret]` callout body into a log aggregator, which is the
one thing S-12.3 forbids. A record that said "add these five names, wherever convenient" would have
recorded the hole rather than closed it. Emitting them through the surface is the condition that
makes them safe, so it is part of the decision rather than an implementation detail of it.

### Why five, and why `op` is not part of the name

The indexer distinguishes seven internal failure shapes and emits five signals. The difference is
that the operation is an **attribute** on `index.change_failed`, not part of its name: a remove that
failed and a rename that failed are one signal an operator alerts on.

The alternative — a name per failure shape — makes the *dashboard* the unit of counting. The
question an operator actually has is "is the index keeping up", and seven counters that each answer
"did this one thing happen" is seven queries to answer it, with the failure mode that the seventh
is the one that is broken.

`index.renamed` is the fifth and the only uncounted one. It is routine work: a sync client moving a
directory is the indexer succeeding. Were it counted it would share a panel with the four failures,
and a rising line would mean two opposite things depending on which of the five it was.

## Consequences

**S-12.3 holds for these signals by construction, not by review.** `observability.Index` classifies
the error rather than logging its text — `errorClass` reduces an error to a sentinel name, an errno,
or a dynamic type — because a Markdown or YAML parser quotes the line it choked on, and on a wiki
page that line is routinely a `[!secret]` callout body. `TestIndexNeverCarriesFileContents` asserts
this through every emitter, including an `*fs.PathError` whose `Path` field is the page body,
which is the shape most likely to leak.

**The `event` key gains five values.** Anything matching on `event` must tolerate them. Nothing in
the repository does; the counters are rendered on `/readyz` and nothing parses them yet.

**`AllEventNames` is now 23, of which 18 are §13.2's.** The count is asserted in a test, and
`TestAllEventNamesAreUniqueAndComplete` is the only place a deviation from this record becomes
visible to a build.

**A caller wanting a new index signal must come back here.** That is the cost, and it is the point:
the list is short because adding to it is visible.

## Alternatives considered

**No new names; emit through `slog` at the call site.** Rejected. It is what the indexer did first,
and it puts S-12.3 outside the type that enforces it. The observable failure is a log aggregator
holding secret callout bodies, which is the one consequence this project calls release-gating
(architecture §12, §14).

**Reuse `content.render_error` for every index failure.** Rejected above: the name misdescribes the
cause and sends an operator to the renderer instead of the index.

**One `index.failed` for all five cases.** Rejected. It merges "a write returned 200 and never
reached the index" with "one file in a vault of five hundred is over the size cap", and they want
opposite responses: the first is an incident, the second is a vault to look at. Merging them also
means a dashboard cannot separate them, which is the argument above.

**Amend §13.2 itself.** Not available. The design records are immutable and this plan follows that
rule; see the "Known staleness" list in the
[design records index]({{ "design/" | relURL }}), which exists for exactly this.