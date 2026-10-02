---
title: "0037 — Zero bytes is not a stable size, and the settle budget is the only clock that can tell a blank note from an unwritten one"
description: "S-4.3's two `stat` samples cannot distinguish a page that is empty from a page whose writer has not started yet, because both are `size == 0`. The filter believed the samples and settled an empty page, which is a wrong `pages` row rather than a merely early one."
lede: "Two `stat` samples that agree are evidence a writer finished — with one hole in the reasoning, and the hole is zero. `open(O_TRUNC)` empties a file and the `write` that refills it is a separate syscall, so between the two there is an interval in which the file is a page by name, holds nothing, and carries an armed deadline with no event to interrupt it. The filter emitted `OpUpsert` for it, and the index wrote a row with `content_hash` equal to the digest of the empty string."
weight: 228
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 4's settle filter implements S-4.3 literally: a per-path quiet period, and then a
size-stable confirmation across two `stat` samples taken a sample interval apart. Both mechanisms
are right, and the reasoning behind the second one is sound for every state it can be given — two
samples that agree on a size mean the writer is not currently changing it.

The reasoning has one hole, and it is the fixed point.

**A file at zero bytes has one `stat`, not two.** A page that is genuinely empty and a page whose
writer has not started yet are the same observation, so a confirmation that samples inside that
window gets two samples that agree, and agreement is the filter's entire evidence for a settle. It
settles an `OpUpsert` for a page with no content in it.

The window is not a theoretical slice between two adjacent instructions. It is a pair of syscalls
with an event gap across it:

1. `open(O_TRUNC)` empties the file. No bytes are produced.
2. The `write` that refills it is a **separate** call. Between them the writer may be descheduled
   for as long as it likes — a GC cycle, a preemption, or an entire network round trip.
3. The truncation need not deliver an event of its own, because the event a watcher sees is the
   one the **write** produces. So there is nothing to restart the quiet period inside the window,
   and the deadline armed by the *previous* event is still live over a file that is now empty.

On semiplane's own save path this cannot happen: S-6.4 stages a temporary file, `fsync`s it, and
`os.Rename`s it over the destination, so the destination only ever exists at full size. That is
precisely why the defect survived a phase's worth of testing — the product's own writer never
produces the input that breaks the filter. But **Obsidian Sync is untrusted input** (S-3.5), and
untrusted input is also *slow* input: a sync client writing a page it has just learned about
creates the file empty and fills it a moment later, and a sync client streaming a large page holds
it near zero for as long as the transfer takes.

### How it presented

Two tests in `internal/content/debounce_test.go` failed intermittently on CI, in two unrelated PRs,
and neither failure was reproducible locally — 60 consecutive runs of the two tests at 4× CPU load
passed clean. Both failures reported the same fact from two directions:

```
--- FAIL: TestTwoCampaignsDoNotInterfere (0.25s)
    debounce_test.go:614: a change settled that should not have: noisy upsert shared.md, 0 bytes read
--- FAIL: TestEverySettledReadIsAWholePage (0.39s)
    debounce_test.go:519: the settled page's title "" is not a version title
```

`0 bytes read` and a title of `""` are the same observation: an empty file parses to empty front
matter. The tests' own fixture explains why they are rare rather than absent — `tornWrite` puts a
`Touch` **between** its two writes, so its zero-length window is one millisecond wide and an armed
deadline rarely lands in it. The window the tests never produce is the one a real sync client
produces routinely.

### Why it is a wrong answer and not an early one

The file header's stated reason for existing is that "a watcher that indexes the third of ten events
is not *slightly early*, it is a wrong answer that looks right." That applies here with more force,
because of what the index does with the bytes it is handed. `Indexer.indexPath` computes
`content_hash` and `byte_size` from the same read and writes a row:

- `content_hash` becomes the digest of the empty string,
- `byte_size` becomes `0`,
- `title` becomes `""`,
- `body_plain` becomes nothing.

S-5.2 keys the render cache on `(campaign_id, path, content_hash, include_secrets)`. An empty row
is therefore not merely a missing page — it is a page the search index and the nav tree will list as
untitled, backed by a cache key nothing else will ever produce.

## Decision

**A sample that finds the path present, not a directory, and zero bytes long is not evidence of a
settled page.** `finishLocked` treats it as *not stable*, and — critically — not as "not stable
**yet**" in the way an ordinary size disagreement is handled.

An ordinary disagreement re-samples at the sample interval and re-baselines `first` on the new
size. That is not enough here, because the disagreement has to be manufactured first: the writer
must still be stalled when both samples are taken. So a zero-length sample instead re-arms the page
**the way an event re-arms it**:

- `phase` returns to `phaseQuiet` and `first` is discarded, so the **quiet period restarts** — a
  writer that resumes inside it is caught by the timer rather than by the budget;
- `page.budget` is **not** re-taken, so the time a path may spend empty stays bounded by the settle
  budget rather than being renewed by every observation of its own emptiness.

Making the second point work required one small change elsewhere: `armLocked` now clears
`page.budget`, and `beginLocked` takes a budget only when there is none. Previously `armLocked`
left a stale budget in place and `beginLocked` overwrote it unconditionally, which made the field
mean two different things depending on how a page had been armed. `armLocked` clearing it is
behaviour-preserving for the `Touch` and `Moved` paths — they both reach `beginLocked`, which takes
a fresh budget either way — and it is what makes "only when there is none" mean *this* confirmation.

The patience this buys, at the production defaults: **40 ms becomes 2 s**. The old filter believed
an empty page after one sample interval (50 ms); the new one will not believe it until the budget
has run out.

### The exception, and why it is an exception

A path that is *still* zero when the budget expires settles — as `outcomeSettledEmpty`, which
reports `content.stable_read_timeout` **and** emits the row. Refusing to settle it was the obvious
alternative and it is worse:

- An empty `.md` file is a page a user can legitimately create. Obsidian makes a blank note the
  first thing a user does in a vault, and semiplane's own editor can produce one.
- A filter that cannot settle an empty page drops it, and the periodic rescan (S-4.5) indexes it
  minutes later — so a blank note appears on its own, with no event explaining it.
- Every blank note would also log `content.stable_read_timeout`. That signal exists because "a
  writer that appears stuck is a thing an operator has to know about". A signal that fires on healthy
  input is an audit of nothing, and it is the same failure as a gate test that cannot fail.

So the row is emitted, because after a whole budget the file really is empty and the row describes
the bytes on disk — which is what `pages.content_hash` means. And the timeout is emitted, because
that is the only trace a writer stalled between its `open` and its `write` ever leaves. Both facts
are true and neither implies the other, which is why they are a distinct outcome rather than one
outcome with a flag on it.

The write this filter exists to prevent still cannot happen: a writer that resumes at any point
inside the budget produces a non-zero size, the next sample disagrees with the zero it was baselined
on, and the page settles whole.

## Consequences

- **S-4.3 gains a clause.** The requirement said "size-stable confirmation across two `stat`
  samples" and did not say that zero is a stable size, because zero being a stable size is true and
  useless. `spec.md` now states the exclusion and the budget bound.
- **A blank note costs a settle budget.** A page that is empty when it settles takes up to
  `SettleBudget` to reach the sink — 2 s at the defaults — where before it took one sample
  interval. Every genuine blank note in a vault now takes that long to appear, and reports a timeout
  while doing it. That is the price of telling a blank note from a stalled writer, and it is paid
  only by pages that are empty.
- **No new signal and no new event name.** `observability.AllEventNames()` stays at 24, so
  [0032]({{ "decisions/0032-index-signals-beyond-the-architecture-list/" | relURL }}) needed no
  amendment.
- **The exposure is bounded, not eliminated.** A writer that holds a path at zero for longer than
  the whole settle budget still yields an empty row. That is a deliberate, documented bound rather
  than an accident: the filter is bounded, and the only way to be unbounded is to wait forever,
  which the rejected alternative in `defaultSettleBudget` already rules out.
- **Three regression tests**, each verified to fail against the pre-fix `debounce.go` with the
  byte-for-byte CI message: `TestAZeroLengthSampleIsNotASettledSize` (a writer truncated in place
  and descheduled across both samples), `TestACreatedButUnwrittenPageIsNotSettledEarly` (a create
  with late bytes, asserted silent for half the settle budget), and
  `TestAnEmptyPageSettlesAndIsReported` (a blank note still settles, and still reports).

## Alternatives considered

**Reject zero-length samples outright, always.** Cleanest rule, one line, and it makes the filter's
contract crisp: *a settled page is a non-empty page.* Rejected for the reasons above — it makes the
blank note invisible to the watcher, defers it to the rescan, and turns `stable_read_timeout` into
noise. It also silently changes what the filter is for: it stops being "when has the writer
finished" and becomes "is this file worth indexing", which is the indexer's question.

**Add a third sample, or require the size to hold for N intervals.** Does not help. The
discriminator is not the number of agreeing samples; it is elapsed time. Two samples 50 ms apart and
five samples 250 ms apart agree on the same empty file, and the difference between "empty because
the writer is slow" and "empty because the note is blank" is only ever a duration.

**Require a non-zero `mtime`, or require `mtime` to be stable too.** Rejected: S-5.2 records that
sync paths preserve or coarsen mtime, so mtime is the one field in this decision that cannot be
trusted, and adding it would make the filter *less* correct on exactly the inputs it exists for.

**Have the indexer refuse to index a zero-byte page.** Rejected for two reasons. It puts the
judgement in the wrong component — S-4.3's whole job is deciding *when* to read, and the indexer's
job is to index what it read. And it would have to be applied to `ReindexCampaign` as well as to
`indexPath`, which means a blank note's row would appear and disappear depending on which code path
noticed it.

**Refuse the event instead: have the watcher ignore an `IN_MODIFY` whose `stat` shows zero bytes.**
Rejected because the watcher and the debouncer are separate components with separate jobs, and
because it would only help when the truncation *does* emit an event — the case the decision is about
is the one where it does not.
