---
title: "0040 — A settle is judged by entitlement, not by a clock the test does not own"
description: "Three tests in the settle filter's suite asserted that a continuously written page never settles. That is a claim about the test's own goroutine, not about the filter, and no quiet period is long enough to make it enforceable. The fixture now records when it delivered each event and judges every arrival by whether S-4.3 permitted it."
lede: "The same assertion in the same test failed twice on CI with different byte counts — `0 bytes` under ADR 0037, `4196 bytes` after it — and both times the filter was right. `4196` is `pageVersion(100)`, the first version the test's noise generator ever writes, so the arrival could only have come from the generator pausing. The tests were reporting the scheduler."
weight: 230
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 7 merged green and left `main` red. The failure was one assertion:

```
--- FAIL: TestTwoCampaignsDoNotInterfere
    debounce_test.go:657: a change settled that should not have: noisy upsert shared.md, 4196 bytes read
```

It is not reproducible on a developer machine. It is not reproducible on this one either, under
3× CPU oversubscription, under `GOGC=1`, at `GOMAXPROCS=2`, or under eight `SIGSTOP`/`SIGCONT`
freezes of 70 ms each. What the measurements do say is that the fixture's own event interval is
very stable — a maximum of 10 ms idle, 14 ms with near-continuous stop-the-world GC, 15 ms inside
the full `-race` tree — against a 40 ms quiet period, a margin of roughly 4×. Reaching 40 ms takes
something this container does not produce and a throttled CI runner does: a cgroup freeze, a slice
of steal time, a scheduler stall wide enough to matter.

### The byte count is the diagnosis

`4196` is not a page. It is `len(pageVersion(100))`, and version 100 is the **first** version
`TestTwoCampaignsDoNotInterfere`'s noise generator writes. So the arrival was the earliest version
that could possibly have been believed — which is what a pause at the start of the burst produces,
and not what a filter that settles eagerly would produce at an arbitrary point in it.

That is the whole failure, and it is checkable rather than inferred. A writer that stops delivering
events for longer than the quiet period, over a file whose size then holds across two samples, is
the antecedent S-4.3 states. Settling it is the specified behaviour, and the settled page is whole
— the index receives a complete version, not a torn one. A test asserting that such a page must
*never* settle is asserting something stronger than the requirement, and it is asserting it about a
**goroutine it owns**.

This is the second recurrence. ADR [0037]({{ "decisions/0037-zero-bytes-is-not-a-stable-size/" | relURL }})
records the first, in this test, at this assertion:

```
--- FAIL: TestTwoCampaignsDoNotInterfere (0.25s)
    debounce_test.go:614: a change settled that should not have: noisy upsert shared.md, 0 bytes read
```

That one was a real defect and 0037 fixed it. This one is the same *symptom* with a different
cause, and 0037's fix could not have addressed it, because 0037 was about what two agreeing samples
mean and this is about whether they were ever asked for. Its "How it presented" section already
records the reproduction difficulty — *"60 consecutive runs of the two tests at 4× CPU load passed
clean"* — and left the general case open.

### Three tests, not one

Auditing the suite for the same assumption found three sites, and the one that failed had the
**second-tightest** margin:

| Test | Interval between its writer's steps | Margin over the quiet period |
|---|---|---|
| `TestBurstOfEventsSettlesOnceAndWhole` | ~22 ms (`quietPeriod / 2`) | **1.8×** |
| `TestTwoCampaignsDoNotInterfere` | ~10 ms (`sampleGap / 2`) | 4× |
| `TestStableReadTimeoutWhenTheSizeNeverSettles` | ~6 ms (`sampleGap / 3`) | 6.7× |

The burst test sleeps `quietPeriod / 2` between writes, which is 20 ms against a 40 ms quiet
period, and its assertion is that the settled version is the last one written. A pause inside the
burst settles an earlier version, legitimately, and the test reports a version mismatch. Since a
2.4× slowdown is reachable on an ordinary machine, that test was the more likely of the two to
fail and had not yet done so.

The third is different in kind: its writer appends a byte every few milliseconds and delivers **no
events at all**, so no arrival can be attributed to an event. A pause there makes the size hold,
S-4.3 settles the page, no `content.stable_read_timeout` is owed — and the test's
`awaitEvent` then waits the full ten seconds and fails with *"no content.stable_read_timeout event
within 10s"*, which points squarely at the filter when the cause was the fixture.

## Decision

**An arrival is judged by whether S-4.3 entitled the filter to make it, and a test that runs a
writer on its own goroutine must not assert silence over a wall-clock window.**

The fixture records the instant it delivers each event, per path, and stamps every arrival with the
instant its sink ran. `entitled` then answers the only question that matters: was there an
event-free window at least as long as the quiet period immediately before this arrival?

- **No** — the filter emitted without its quiet period. That is the defect S-4.3's first mechanism
  exists to prevent, it is what a filter that armed its timer once would do, and it fails on any
  machine.
- **Yes** — the writer stopped, the size held, and the filter did what the specification requires.
  Not a failure, and it does not end the window: the rest of the window is still watched, so a
  later unentitled arrival is still caught.

The measured window runs from the last event for **that path** to the arrival, which makes it a
lower bound on what the filter waited — the filter also spends a sample interval confirming, and may
have re-armed more than once. So a `false` is sound and a `true` means only *entitled*, never
*prompt*.

Three consequences follow, and each was verified by mutation rather than by argument:

1. **`silentWhileWriting` replaces `silent`** wherever a background writer is running. An entitled
   arrival is logged, not hidden, so a run that loses coverage says so.
2. **The burst test's cadence widens** from `quietPeriod / 2` to `sampleGap / 2` — margin 1.8× to
   4× — and its version assertion becomes sound by construction: it checks that the **first**
   arrival is entitled, and then that *some* arrival carries version 12, which is guaranteed because
   the burst's last event is version 12's. It no longer asserts which arrival that is, because that
   depends on the machine.
3. **`gapRecorder` covers the writer that delivers no events.** It measures the longest interval
   between the appender's own steps, and `awaitEvent` takes it as a premise: once a pause reaches the
   quiet period, the wait ends immediately with a message naming the fixture's premise as void,
   instead of ten seconds later blaming a missing event.

**`debounce.go` is unchanged.** The filter was correct in both CI failures, and the only honest
response to a test that misreports a correct filter is to fix the test.

## Consequences

- **`main` is green again**, which is what makes the next phase's CI worth reading.
- **A settle of a stalled writer is now documented as correct**, which is the converse of 0037. 0037
  decided that two *zero* samples are not evidence; this decides that two samples taken a whole
  quiet period after the last event *are*, and that the test suite must be able to tell the two
  situations apart rather than reporting both as failure.
- **The fixture's coverage now degrades visibly.** On a machine that pauses the writer, the affected
  tests assert less and log that they did. This is a real cost and it is bounded: an unentitled
  arrival still fails everywhere, always.
- **The margin is still a margin.** Entitlement does not remove the possibility of a pause; it makes
  a pause harmless instead of fatal. The cadence change is what buys the headroom.
- **No new event, no new signal, no production behaviour change.** `observability.AllEventNames()`
  stays at 24, so [0032]({{ "decisions/0032-index-signals-beyond-the-architecture-list/" | relURL }})
  needed no amendment. This is a test-only change to `internal/content/debounce_test.go` plus this
  record.
- **The failure message now carries a cause.** The reported line was
  `a change settled that should not have: noisy upsert shared.md, 4196 bytes read`. The same defect
  now reports
  `a change settled 52.453µs after its last event, which is shorter than the 40ms quiet period`,
  which distinguishes "the filter jumped ahead" from "the writer paused" without reading the source.

### Verification

Each assertion was checked by mutation, because an assertion that cannot fail is not a gate:

| Mutation | Caught by | Message |
|---|---|---|
| `Touch` does not restart a live deadline | `TestBurstOfEventsSettlesOnceAndWhole` | `settled 22.58ms after its last event, which is shorter than the 40ms quiet period` |
| No quiet period **and** no second sample | `TestTwoCampaignsDoNotInterfere` | `settled 52.453µs after its last event … noisy upsert shared.md, 4196 bytes read` |
| One sample is enough | `TestStableReadTimeoutWhenTheSizeNeverSettles` | no timeout event owed |
| Pending map keyed by path alone | `TestTwoCampaignsDoNotInterfere` | `no settled change within 10s` |

The second row is the reported failure reproduced deliberately, with the cause attached. The third
mutation is worth naming separately: removing the quiet period *alone* is caught by nothing, because
the size-stability check masks it while a writer is active. It takes removing both mechanisms to
produce an unentitled settle, which is a fair description of how much redundancy this filter has.

## Alternatives considered

**Make the test's quiet period long enough that no machine stalls for it.** Rejected: there is no
such value. A goroutine can be descheduled for a GC cycle, a cgroup freeze or a hypervisor
preemption, and the failure ADR 0037 could not reproduce across 60 loaded runs is the direct
evidence. Choosing a bigger number buys a lower probability and keeps the unsound assertion.

**Skip the assertion when the fixture detects its own premise was broken.** Rejected as the shape
AGENTS.md warns about — a gate that can be skipped by not running the check. On a slow runner, where
the premise *is* broken, the assertion would never execute, so the coverage would be absent exactly
where the machine is least able to give it up. Entitlement keeps the assertion running and narrows
only what it tolerates.

**Report the fixture's pauses as failures, with an accurate message.** Rejected: it makes a red
`main` a function of the runner's load, which is the problem being fixed. A defect is a defect; a
slow machine is not. The pause is reported on the log instead, where a human reads it.

**Make the filter more conservative — refuse to settle a path whose writer ever paused.** Rejected
outright: a writer that stops is a writer that finished, a sync client between chunks is a writer
that stopped, and refusing to settle those trades a bounded, whole page for an unindexed one plus a
`stable_read_timeout` line for every healthy save. That is the failure ADR 0037 already reasoned
about from the other side.

**Fix the tests by removing the background writer and driving events from a table.** Rejected: the
tests need a *concurrent* writer, because the property under test is what the filter does while
events keep arriving. A precomputed table of timestamps would have to be replayed through a real
clock, which is the fake clock this file's header already rejects — and on this evidence that
rejection was right.
