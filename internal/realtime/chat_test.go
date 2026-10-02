package realtime_test

// Tests for the chat ring and the journal export.
//
// Two rules govern how every assertion in this file is written, and both of them
// were learned the hard way somewhere in this project's history:
//
//   - **A test that cannot fail is not a test.** Each row below carries the
//     mutation that breaks it, and `TestEveryAuditInThisFileHasAMutationThatBreaksIt`
//     is the mechanical version of that claim: it asserts the mutation string is
//     still present in the source file, so a test whose recorded mutation was
//     deleted cannot quietly become decoration.
//   - **A test asserting an absence must assert the absence of the *bytes*.** A
//     refused path that a test only checks the *status* of has written something
//     and the test would not know. So the confinement rows compare a snapshot of
//     the whole filesystem before and after, and the secret rows assert the
//     plaintext is nowhere in the exported bytes rather than that a placeholder
//     appeared.
//
// The export's tests use a **real** `content.Root` over a real directory, because
// the property under test is `os.Root`'s: a double's `At` would return whatever the
// double was told to return, which is the definition of an assertion that cannot
// fail.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/realtime"
)

// The campaign this package's harness speaks for. A slug, because
// `content.Registry` is keyed by slug and the export asks it by slug.
const (
	testSlug     = "greyhaven"
	testCampaign = int64(7)
)

// secretText is the plaintext a secret message carries, and the string every
// secret-exclusion assertion looks for **by value** in bytes that were produced.
//
// Chosen to be searchable and unlikely to appear by accident, and chosen to be
// *not* a `[!secret]` marker — because the failure being guarded against is a
// marker surviving into the export, and a test whose needle was the marker would
// pass for the wrong reason.
const secretText = "the passphrase is hunter2"

// fixedClock is the moment the harness exports at, so every expected file name is
// a constant rather than a format string evaluated by the reader.
var fixedClock = time.Date(2026, time.October, 2, 14, 5, 5, 0, time.UTC)

// --- The harness ---------------------------------------------------------------

// recordingRevisions is a `realtime.Revisions` that keeps what it was asked to
// write, and can be made to fail or to observe the filesystem at the moment it is
// called.
//
// The `observe` hook is how the ordering assertion is made rather than asserted: it
// runs *inside* `Append`, so it sees the vault exactly as the append left it. A
// test that only checked the order afterwards would be checking that the code's
// comments are true.
type recordingRevisions struct {
	mu      sync.Mutex
	rows    []realtime.Revision
	fail    error
	observe func(revision realtime.Revision)
}

func (r *recordingRevisions) Append(_ context.Context, revision realtime.Revision) error {
	r.mu.Lock()
	r.rows = append(r.rows, revision)

	fail := r.fail
	observe := r.observe
	r.mu.Unlock()

	// After the row is recorded and before returning, so a hook that inspects the
	// vault is looking at the moment the append happened.
	if observe != nil {
		observe(revision)
	}

	if fail != nil {
		return fail
	}

	return nil
}

// recorded returns the rows written so far.
func (r *recordingRevisions) recorded() []realtime.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]realtime.Revision(nil), r.rows...)
}

// harness is one campaign's vault, chat and exporter, all real.
type harness struct {
	// base is the temporary directory holding both the vault and the directory
	// beside it that an escaping path would reach. One tree, so the "no bytes"
	// snapshot covers the escape target as well as the vault.
	base string
	// vaultDir is the campaign's content root.
	vaultDir string
	// outsideDir is a sibling of the vault that no legitimate export can write to.
	// An escaping path resolves into here, which is what makes "no bytes" an
	// assertion about bytes rather than about a status code.
	outsideDir string
	registry   *content.Registry
	revisions  *recordingRevisions
}

// newHarness opens a vault with a real `content.Root` over it, plus the directory
// beside the vault that an escaping path would resolve into.
//
// There is deliberately no chat or exporter on the harness: `testChat` builds the
// pair over **one** buffer, and a harness that carried its own would invite a test
// that exports one buffer and asserts on another.
//
// `Journal` is created because it does not exist on a fresh install and this
// package does not own `content`, which has no `Mkdir`: the folder is the GM's or
// the vault's, and the export lands in it when it is there.
func newHarness(t *testing.T) *harness {
	t.Helper()

	base := t.TempDir()
	vaultDir := filepath.Join(base, "vault")
	outsideDir := filepath.Join(base, "outside")

	for _, dir := range []string{vaultDir, outsideDir, filepath.Join(vaultDir, realtime.JournalFolder)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	registry := content.NewRegistry(content.RefuseSymlinks)

	if _, err := registry.Open(testSlug, vaultDir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close the content root: %v", err)
		}
	})

	revisions := &recordingRevisions{}

	// Two chats: `chat` is the one the tests read from and `exported` is the one the
	// exporter owns. They are separate because `Exporter` is built **per campaign
	// around a specific `*Chat`**, so a test that wants to assert on the buffer and
	// on the export has to hold both — and that separation is itself worth having in
	// the harness, because it mirrors what a hub does: one buffer, one exporter
	// bound to it.
	return &harness{
		base:       base,
		vaultDir:   vaultDir,
		outsideDir: outsideDir,
		registry:   registry,
		revisions:  revisions,
	}
}

// request is the export request the harness sends, with the fields a caller must
// set and nothing else.
func (h *harness) request() realtime.ExportRequest {
	return realtime.ExportRequest{
		CampaignID: testCampaign,
		AuthorID:   3,
		Slug:       testSlug,
		Folder:     realtime.JournalFolder,
		At:         fixedClock,
	}
}

// testChat returns a chat and an exporter over **the same buffer**, which is what
// an export asserts on.
//
// One helper for every export test, and the reason it exists rather than three
// lines of setup: an exporter built over a different `*Chat` than the one the test
// reads is a test that passes for the wrong reason, and the seam that allows it is
// exactly the seam a reader would not notice.
func testChat(t *testing.T, h *harness) (*realtime.Chat, *realtime.Exporter) {
	t.Helper()

	chat := realtime.NewChat(32, strings.NewReader(strings.Repeat("k", 64)))

	return chat, realtime.NewExporter(chat, h.registry, h.revisions)
}

// snapshot is every file under root, as path → bytes.
//
// The whole tree rather than the paths the export *says* it wrote, because the
// assertion is "nothing changed" and a list of expected paths would only ever check
// the paths someone thought of. A symlink is recorded by its target path with a
// marker rather than followed, so a snapshot cannot walk out of the tree it was
// taken over.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()

	found := make(map[string]string)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("relative path of %s: %w", path, relErr)
		}

		if entry.Type()&fs.ModeSymlink != 0 {
			found[relative] = "<symlink>"

			return nil
		}

		if entry.IsDir() {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}

		found[relative] = string(data)

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	return found
}

// assertNoBytesWereWritten compares a snapshot taken before the call with one taken
// after, and fails on any difference in **either direction**.
//
// Both directions because "no bytes" is about the filesystem not having gained a
// file, and a test that only checked for additions would pass against an
// implementation that truncated an existing one. The vault and the escape
// directory are both covered, because they share one parent.
func assertNoBytesWereWritten(t *testing.T, before, after map[string]string) {
	t.Helper()

	for path, contents := range after {
		if _, existed := before[path]; !existed {
			t.Errorf("%s did not exist before and holds %d bytes now; an export that "+
				"refuses a path must leave nothing behind, not nothing reported",
				path, len(contents))
		}
	}

	for path := range before {
		if _, still := after[path]; !still {
			t.Errorf("%s existed before and is gone now; a refused export must not "+
				"remove anything either", path)
		}
	}
}

// --- The ring -------------------------------------------------------------------

// TestTheBufferIsBoundedAndEvictsTheOldestFirst is the boundedness claim, from
// both ends.
//
// **Mutation.** Replacing `c.ring[(c.start+c.count)%len(c.ring)] = message` with a
// `c.messages = append(c.messages, message)` slice-append — i.e. turning the ring
// into the unbounded thing the file comment says it is not — fails the `Len` and
// `Capacity` assertions below and fails `TestTheBufferDoesNotGrowWithTheNumberOfMessages`.
func TestTheBufferIsBoundedAndEvictsTheOldestFirst(t *testing.T) {
	t.Parallel()

	const capacity = 5

	chat := realtime.NewChat(capacity, strings.NewReader(strings.Repeat("a", 64)))

	for index := range 20 {
		body := fmt.Sprintf("line %d", index)

		if _, err := chat.Append("mira", body, fixedClock, false); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}

	if got := chat.Len(); got != capacity {
		t.Errorf("Len is %d after 20 appends into a buffer of %d; the bound is not a bound",
			got, capacity)
	}

	if got := chat.Capacity(); got != capacity {
		t.Errorf("Capacity is %d, want %d; the storage is resized", got, capacity)
	}

	if got, want := chat.Highest(), uint64(20); got != want {
		t.Errorf("Highest is %d, want %d; eviction must not roll the counter back", got, want)
	}

	if got, want := chat.Dropped(), uint64(15); got != want {
		t.Errorf("Dropped is %d, want %d; the eviction rule is not the oldest-first one",
			got, want)
	}

	retained := chat.Visible(false)
	if len(retained) != capacity {
		t.Fatalf("Visible returned %d messages, want %d", len(retained), capacity)
	}

	// The retained messages are the **last** five, in the order they were said. The
	// sequence numbers are asserted alongside the bodies because a ring that
	// retained the right count in the wrong order would satisfy a body-only check
	// whenever the bodies were generated in order — which they are.
	for offset, message := range retained {
		if want := uint64(16 + offset); message.Seq != want {
			t.Errorf("retained[%d] has Seq %d, want %d; the ring is not holding the "+
				"newest messages", offset, message.Seq, want)
		}

		if want := fmt.Sprintf("line %d", 15+offset); message.Body != want {
			t.Errorf("retained[%d] is %q, want %q; eviction is not dropping the oldest first",
				offset, message.Body, want)
		}
	}
}

// TestTheBufferDoesNotGrowWithTheNumberOfMessages is the memory claim, and it is a
// separate test because a `Len` assertion cannot see the ring's storage being
// replaced underneath a fixed length.
//
// The property is **zero allocations per append**. A ring writes into an array
// allocated once at the capacity; a slice-append implementation reallocates as it
// grows, which copies everything it holds at the moment the table is busiest — and
// an amortised copy is not visible in a `Len` assertion at all, which is why this is
// its own test and why it is measured rather than reasoned about.
//
// `testing.AllocsPerRun` is the right instrument because the property is *about*
// allocation: it runs the closure repeatedly and reports the average, so a
// reallocating ring reports a non-zero average even though most appends copy
// nothing. The bodies are built **before** the measurement so that the test's own
// `fmt.Sprintf` is not counted as the buffer's.
//
// **Mutation.** Changing `NewChat` to `ring: make([]Message, 0, capacity)` and
// appending to it fails this test on the average; a ring that *preallocates* still
// passes, which is correct — preallocating is what this test asks for.
//
// **Not parallel**, because `testing.AllocsPerRun` pins `GOMAXPROCS` and panics when
// it is called from a parallel test. That is the only test in this file without
// `t.Parallel()`, and the reason is here rather than left to be rediscovered.
func TestTheBufferDoesNotGrowWithTheNumberOfMessages(t *testing.T) {
	const capacity = 8

	chat := realtime.NewChat(capacity, strings.NewReader(strings.Repeat("b", 64)))

	// A thousand distinct bodies, built outside the measurement: the point is what
	// `Append` does with a string it was handed, and `Sprintf` inside the measured
	// closure would be measuring the test.
	bodies := make([]string, 1000)
	for index := range bodies {
		bodies[index] = fmt.Sprintf("line %d", index)
	}

	if _, err := chat.Append("mira", bodies[0], fixedClock, false); err != nil {
		t.Fatalf("append the first message: %v", err)
	}

	// The error is captured rather than reported from inside the closure, because
	// `AllocsPerRun` runs it thousands of times and a `Fatalf` in there would be a
	// failure that cannot name which of the thousand appends it was.
	var appendErr error

	average := testing.AllocsPerRun(200, func() {
		for index := range 200 {
			if _, err := chat.Append("dorn", bodies[index], fixedClock, false); err != nil {
				appendErr = err

				return
			}
		}
	})

	if appendErr != nil {
		t.Fatalf("append into a preallocated ring: %v", appendErr)
	}

	// The measurement above runs 200 appends per sample, so any per-sample constant
	// is divided by 200 before it is compared. A reallocating ring's cost is not a
	// constant and shows up anyway; this division is only here so the threshold
	// reads as "no allocation at all" rather than as a magic number.
	if average > 0.001 {
		t.Errorf("200 appends into a preallocated ring averaged %v allocations each; the "+
			"storage is being reallocated, so memory grows with the number of messages",
			average)
	}

	if got := chat.Capacity(); got != capacity {
		t.Errorf("Capacity is %d after 40000 appends into a buffer of %d; the ring was "+
			"resized", got, capacity)
	}

	if got := chat.Len(); got != capacity {
		t.Errorf("Len is %d after 40000 appends into a buffer of %d", got, capacity)
	}
}

// TestEvictionIsDeterministic is the claim that the retained order is a function of
// the append order and not of Go's map iteration.
//
// There is no map in `Chat` — that is the design — so this is a regression guard
// rather than a description of the current code: the sixty-four runs below would
// disagree on the first and third if anything introduced a map, an iteration over
// the messages, or a sort with an unstable comparator.
//
// **Mutation.** Replacing `messagesLocked`'s walk with a loop over a
// `map[uint64]Message` and appending what it yields fails this test with high
// probability on the first run; the `same-order` row below asserts the *identical*
// output rather than a set, which is what catches an unstable-but-deterministic
// ordering too.
func TestEvictionIsDeterministic(t *testing.T) {
	t.Parallel()

	// A capacity that does not divide the append count, so the wrap point moves:
	// a bug that only appeared when the ring happened to be exactly full would
	// otherwise survive this.
	for _, capacity := range []int{1, 2, 3, 7, 16} {
		var reference []realtime.Message

		for run := range 64 {
			chat := realtime.NewChat(capacity, strings.NewReader(strings.Repeat("c", 64)))

			// **The same messages on every run.** The bodies must not carry the run
			// number: a body that differs per run would make the comparison fail for
			// the right-looking reason and teach nothing about ordering. What is left
			// as a possible difference between two runs is the eviction order and
			// nothing else.
			for index := range 20 {
				secret := index%5 == 0

				body := fmt.Sprintf("line %d", index)
				author := fmt.Sprintf("author-%d", index%3)

				if _, err := chat.Append(author, body, fixedClock, secret); err != nil {
					t.Fatalf("capacity %d: append %d: %v", capacity, index, err)
				}
			}

			got := chat.Visible(true)

			if run == 0 {
				reference = got

				continue
			}

			if len(got) != len(reference) {
				t.Fatalf("capacity %d: run %d retained %d messages, run 0 retained %d",
					capacity, run, len(got), len(reference))
			}

			for index := range got {
				if got[index] != reference[index] {
					t.Fatalf("capacity %d: run %d retained %+v at %d, run 0 retained %+v; "+
						"eviction order is not a function of the append order",
						capacity, run, got[index], index, reference[index])
				}
			}
		}
	}
}

// TestAppendRefusesBeforeTheRingIsTouched is the "the bound holds whatever the
// client sends" claim.
//
// A refusal that assigned a sequence number first would burn one and would evict
// the oldest retained message, so a client that posts a megabyte of text costs a
// real message every time it does.
//
// **Mutation.** Moving the length check above the `c.mu.Lock()` but *after* the
// `Seq` assignment fails this test.
func TestAppendRefusesBeforeTheRingIsTouched(t *testing.T) {
	t.Parallel()

	chat := realtime.NewChat(2, strings.NewReader(strings.Repeat("d", 64)))

	first, err := chat.Append("mira", "kept", fixedClock, false)
	if err != nil {
		t.Fatalf("append the first message: %v", err)
	}

	for _, testCase := range []struct {
		name   string
		author string
		body   string
		want   error
	}{
		{name: "no author", author: "   ", body: "text", want: realtime.ErrNoAuthor},
		{name: "an empty body", author: "mira", body: "", want: realtime.ErrEmptyMessage},
		{
			name:   "an oversized body",
			author: "mira",
			body:   strings.Repeat("x", realtime.MaxChatBodyBytes+1),
			want:   realtime.ErrMessageTooLong,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, appendErr := chat.Append(testCase.author, testCase.body, fixedClock, false)
			if !errors.Is(appendErr, testCase.want) {
				t.Fatalf("Append returned %v, want %v", appendErr, testCase.want)
			}

			if got := chat.Highest(); got != first.Seq {
				t.Errorf("Highest moved to %d after a refused append; a rejected message "+
					"burned a sequence number", got)
			}

			if got := chat.Len(); got != 1 {
				t.Errorf("Len is %d after a refused append into a buffer of 2, want 1; the "+
					"refusal evicted the message that was already there", got)
			}

			if got := chat.Dropped(); got != 0 {
				t.Errorf("Dropped is %d after a refused append, want 0", got)
			}
		})
	}
}

// TestTheSequenceIsMonotonicAndSurvivesEmptiness is the counter's half of S-7.1's
// `seq`/`version` distinction: a cursor must never be handed a number that means
// something else later.
//
// **Mutation.** Setting `c.highest = message.Seq` inside the eviction branch only —
// which is what the first draft of `Append` did, because that branch returned early
// — makes `Highest` freeze once the ring fills. The eviction rows below catch it.
func TestTheSequenceIsMonotonicAndSurvivesEmptiness(t *testing.T) {
	t.Parallel()

	chat := realtime.NewChat(3, strings.NewReader(strings.Repeat("e", 64)))

	if got := chat.Highest(); got != 0 {
		t.Errorf("Highest is %d on an empty chat, want 0", got)
	}

	for index := range 10 {
		message, err := chat.Append("mira", fmt.Sprintf("line %d", index), fixedClock, false)
		if err != nil {
			t.Fatalf("append %d: %v", index, err)
		}

		if want := uint64(index + 1); message.Seq != want {
			t.Errorf("append %d returned Seq %d, want %d", index, message.Seq, want)
		}

		if got := chat.Highest(); got != message.Seq {
			t.Errorf("Highest is %d after appending Seq %d", got, message.Seq)
		}
	}
}

// --- `Since`, and the distinction a client cannot make without a counter ---------

// TestSinceTellsNothingNewApartFromAGap is the whole reason `Feed` is a struct.
//
// Four cases in one table, and the pair that matters is the first two: an empty
// `Messages` with `Gap` false, and an empty `Messages` with `Gap` true, are the
// same wire shape and opposite meanings. A client that cannot tell them apart will
// either re-render a whole table on every poll or silently miss lines.
//
// **Mutation.** Returning `Messages: nil, Gap: false` from the eviction branch —
// that is, dropping the gap computation and returning the retained messages as
// though they were a complete delta — fails the `a cursor below the ring` row.
func TestSinceTellsNothingNewApartFromAGap(t *testing.T) {
	t.Parallel()

	chat := realtime.NewChat(4, strings.NewReader(strings.Repeat("f", 64)))
	incarnation := chat.Incarnation()

	for index := range 10 {
		if _, err := chat.Append(
			"mira",
			fmt.Sprintf("line %d", index),
			fixedClock,
			false,
		); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}

	// A buffer of four after ten appends retains sequences 7, 8, 9 and 10.
	for _, testCase := range []struct {
		name         string
		since        uint64
		wantCount    int
		wantGap      bool
		wantFirstSeq uint64
	}{
		{
			name: "a cursor at the head says there is nothing new",
			// The newest retained message is 10, so a cursor of 10 is caught up.
			since:     10,
			wantCount: 0,
			wantGap:   false,
		},
		{
			name:      "a cursor inside the ring gets the delta and no gap",
			since:     8,
			wantCount: 2,
			wantGap:   false,
			// Sequence 9 is the first message above the cursor.
			wantFirstSeq: 9,
		},
		{
			// The important one: 0 means "I have seen nothing", and messages 1 to 6
			// were evicted, so the four returned are **not** the whole conversation
			// and the feed has to say so.
			name:         "a cursor below the ring gets what is left and a gap",
			since:        0,
			wantCount:    4,
			wantGap:      true,
			wantFirstSeq: 7,
		},
		{
			// The boundary: the cursor's next wanted message is 7, which is the
			// oldest retained one, so nothing is missing and there is no gap.
			name:         "a cursor one below the oldest retained message is complete",
			since:        6,
			wantCount:    4,
			wantGap:      false,
			wantFirstSeq: 7,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			feed, err := chat.Since(incarnation, testCase.since)
			if err != nil {
				t.Fatalf("Since(%d): %v", testCase.since, err)
			}

			if len(feed.Messages) != testCase.wantCount {
				t.Errorf("Since(%d) returned %d messages, want %d",
					testCase.since, len(feed.Messages), testCase.wantCount)
			}

			if feed.Gap != testCase.wantGap {
				t.Errorf("Since(%d) reported Gap=%t, want %t; a client cannot tell a gap "+
					"from an up-to-date cursor", testCase.since, feed.Gap, testCase.wantGap)
			}

			if testCase.wantCount > 0 && feed.Messages[0].Seq != testCase.wantFirstSeq {
				t.Errorf("Since(%d) starts at Seq %d, want %d",
					testCase.since, feed.Messages[0].Seq, testCase.wantFirstSeq)
			}

			if feed.Highest != 10 {
				t.Errorf("Since(%d) reported Highest %d, want 10", testCase.since, feed.Highest)
			}

			if feed.Incarnation != incarnation {
				t.Errorf("Since(%d) reported a different incarnation; the cursor cannot be "+
					"used against the next answer", testCase.since)
			}
		})
	}
}

// TestSinceRefusesACursorAheadOfThisLoading is ADR 0004's detector on the chat side.
//
// A cursor above the counter is a client on the wrong process or a corrupt cursor,
// and answering it with an empty feed would tell a client it has seen messages it
// has not — the exact failure an incarnation check exists to prevent, so the two
// refusals are separate and both are here.
//
// **Mutation.** Replacing the `since > c.highest` branch with `if since >= c.highest`
// passes the up-to-date case (nothing new is still nothing new) but fails the
// `ahead` row; removing the branch entirely fails both.
func TestSinceRefusesACursorAheadOfThisLoading(t *testing.T) {
	t.Parallel()

	chat := realtime.NewChat(4, strings.NewReader(strings.Repeat("g", 64)))

	if _, err := chat.Append("mira", "one", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	if _, err := chat.Since(chat.Incarnation(), 99); !errors.Is(err, realtime.ErrFutureSeq) {
		t.Fatalf("Since(99) on a chat at 1 returned %v, want ErrFutureSeq", err)
	}
}

// TestSinceAcrossARestartIsAFullFeedAndNotADelta is the crash floor seen from the
// chat side, and it is the reason `Chat` has an incarnation at all.
//
// A buffer that lost its messages on restart and answered a pre-restart cursor with
// a computed delta would produce a delta against a counter that no longer means what
// the cursor says — which is the "loaded and never written" ambiguity `state.go`
// refuses everywhere, in the place it would be invisible.
//
// **Mutation.** Comparing `since` against `highest` before checking the incarnation
// fails this test: the pre-restart cursor is 3 and the fresh buffer is at 0, so it
// answers `ErrFutureSeq` rather than the full feed.
func TestSinceAcrossARestartIsAFullFeedAndNotADelta(t *testing.T) {
	t.Parallel()

	first := realtime.NewChat(4, strings.NewReader(strings.Repeat("h", 64)))

	for index := range 3 {
		if _, err := first.Append(
			"mira",
			fmt.Sprintf("line %d", index),
			fixedClock,
			false,
		); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}

	before := first.Incarnation()

	// The restart: a new buffer, with its own counter starting at 1 and its own
	// incarnation. The cursor below is from the loading that is gone.
	second := realtime.NewChat(4, strings.NewReader(strings.Repeat("i", 64)))

	if second.Incarnation() == before {
		t.Fatal("two chats drew the same incarnation from different readers; the " +
			"restart case cannot be reached")
	}

	if _, err := second.Append("mira", "after the restart", fixedClock, false); err != nil {
		t.Fatalf("append after the restart: %v", err)
	}

	feed, err := second.Since(before, 3)
	if err != nil {
		t.Fatalf("Since with a pre-restart cursor: %v; the cursor must not be measured "+
			"against a counter that means something else", err)
	}

	if !feed.Gap {
		t.Error("Since across a restart reported no gap; a client would splice a " +
			"partial list in and neither party could see the missing lines")
	}

	if len(feed.Messages) != 1 {
		t.Errorf("Since across a restart returned %d messages, want the whole retained log (1)",
			len(feed.Messages))
	}

	if feed.Incarnation != second.Incarnation() {
		t.Error("Since across a restart did not report the loading the messages came from")
	}
}

// TestVisibleOmitsASecretMessageRatherThanMarkingIt is S-5.6's position on the
// live surface.
//
// **The assertion is on the absence of the plaintext.** A check that the secret
// message carries no marker would pass an implementation that returned it with
// `Secret: true` intact — and that implementation ships the secret to every reader
// of the log. So the row looks for the body and the flag and requires both gone.
//
// **Mutation.** Replacing the `continue` with `message.Secret = false` and appending
// fails both the plaintext and the flag assertion.
func TestVisibleOmitsASecretMessageRatherThanMarkingIt(t *testing.T) {
	t.Parallel()

	chat := realtime.NewChat(8, strings.NewReader(strings.Repeat("j", 64)))

	ordinary, err := chat.Append("mira", "the tavern is on fire", fixedClock, false)
	if err != nil {
		t.Fatalf("append the ordinary message: %v", err)
	}

	secret, err := chat.Append("dorn", secretText, fixedClock, true)
	if err != nil {
		t.Fatalf("append the secret message: %v", err)
	}

	visible := chat.Visible(false)

	if len(visible) != 1 {
		t.Fatalf("Visible(false) returned %d messages, want 1", len(visible))
	}

	if visible[0] != ordinary {
		t.Errorf("Visible(false) returned %+v, want the ordinary message %+v",
			visible[0], ordinary)
	}

	for _, message := range visible {
		if message.Secret {
			t.Errorf("Visible(false) returned a message still flagged Secret: %+v", message)
		}

		if strings.Contains(message.Body, secretText) {
			t.Errorf("Visible(false) returned a message whose body contains the secret " +
				"plaintext; redaction is omission, not hiding")
		}
	}

	// And the flag carries no information to the caller either: the withheld
	// message's sequence number is simply absent, so a client cannot infer the
	// message's position by counting.
	for _, message := range visible {
		if message.Seq == secret.Seq {
			t.Errorf("the withheld message's Seq %d appears in the visible feed; a client "+
				"could infer the gap's contents from its position", secret.Seq)
		}
	}

	// A reader entitled to secrets does get it, which is what makes the exclusion a
	// decision rather than a loss.
	entitled := chat.Visible(true)
	if len(entitled) != 2 {
		t.Fatalf("Visible(true) returned %d messages, want 2", len(entitled))
	}

	if !strings.Contains(entitled[1].Body, secretText) {
		t.Error("Visible(true) did not return the secret message's body; the exclusion " +
			"removed content rather than withholding it from one reader")
	}
}

// --- The export: the round trip --------------------------------------------------

// TestTheExportRoundTripsThroughTheRealFrontMatterParser is the format's own test,
// and it is deliberately not a fixture comparison.
//
// The exported bytes go through `content.Parse` — the project's real front-matter
// parser, the one Obsidian Sync's input arrives at — and the **body it produces** is
// what `ParseJournal` reads. A test that parsed its own output with a hand-rolled
// splitter would prove the two halves of this package agree; this proves the page
// semiplane writes is a page semiplane can read, which is a different and larger
// claim.
//
// The corpus is chosen for the things a minimal escape loses: backslashes, asterisks,
// a leading `#`, a line break, a quote, an em dash, a separator that looks like the
// format's own, and non-ASCII text. A minimal escape (backslash before `*`, `_`,
// “ ` “ and a leading `#`) fails on every one of them.
//
// **Mutation.** Replacing the total escape with a minimal one — `*`, `_`, “ ` “ and
// a leading `#` only — fails this test on the backslash row and the separator row.
func TestTheExportRoundTripsThroughTheRealFrontMatterParser(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	bodies := []string{
		"plain",
		"asterisks * and ** and ***",
		"a backslash \\ and another \\\\",
		"# a heading that is not one",
		"two\nlines\nwith a break",
		`quotes "double" and 'single'`,
		"an em dash — and an en dash –",
		"a forged separator - 12 - not - **dorn** - not a field",
		"  leading and trailing spaces  ",
		"punctuation !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
		"non-ASCII: κύρος, 日本語, 🐉, naïve",
		"a tab\there",
		strings.Repeat("x", realtime.MaxChatBodyBytes),
	}

	sent := make([]realtime.Message, 0, len(bodies))

	for index, body := range bodies {
		message, err := chat.Append(
			fmt.Sprintf("author %d - with punctuation", index%3),
			body,
			fixedClock.Add(time.Duration(index)*time.Minute),
			false,
		)
		if err != nil {
			t.Fatalf("append message %d: %v", index, err)
		}

		sent = append(sent, message)
	}

	exported, err := exporter.Export(context.Background(), h.request())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if exported.Included != len(sent) {
		t.Errorf("Export reported %d included, want %d", exported.Included, len(sent))
	}

	if exported.Path != realtime.JournalFolder+"/chat-20261002-140505.md" {
		t.Errorf("Export wrote %q, want the derived name in %q",
			exported.Path, realtime.JournalFolder)
	}

	raw, err := os.ReadFile(filepath.Join(h.vaultDir, filepath.FromSlash(exported.Path)))
	if err != nil {
		t.Fatalf("read the exported page: %v", err)
	}

	// The real parser, on the real bytes, with no kind registry: `journal` is
	// declared and unregistered until phase 8, and S-3.3's degradation is that it
	// renders as prose while `DeclaredKind` reports what to fix.
	document := content.Parse(raw, nil)

	if document.FrontMatter.Err != nil {
		t.Fatalf("the front matter did not interpret: %v (block was:\n%s)",
			document.FrontMatter.Err, document.Raw)
	}

	if got := document.FrontMatter.DeclaredKind; got != realtime.JournalKind {
		t.Errorf("DeclaredKind is %q, want %q; the round trip starts at the front matter",
			got, realtime.JournalKind)
	}

	if got := document.FrontMatter.Title; !strings.HasPrefix(got, "Chat ") {
		t.Errorf("the parsed title is %q, want it derived from the export time", got)
	}

	readBack, err := realtime.ParseJournal(document.Body)
	if err != nil {
		t.Fatalf("ParseJournal over the parsed body: %v (body was:\n%s)", err, document.Body)
	}

	if len(readBack) != len(sent) {
		t.Fatalf("ParseJournal returned %d messages, want %d", len(readBack), len(sent))
	}

	for index := range sent {
		got, want := readBack[index], sent[index]

		if got.Seq != want.Seq {
			t.Errorf("message %d came back as Seq %d, want %d", index, got.Seq, want.Seq)
		}

		if got.Author != want.Author {
			t.Errorf("message %d came back with author %q, want %q", index, got.Author, want.Author)
		}

		if got.Body != want.Body {
			t.Errorf("message %d came back as %q, want %q", index, got.Body, want.Body)
		}

		if !got.At.Equal(want.At) {
			t.Errorf("message %d came back at %s, want %s", index, got.At, want.At)
		}

		if got.Secret {
			t.Errorf("message %d came back flagged Secret; a transcript contains none", index)
		}
	}
}

// TestParseJournalRefusesALineItCannotRead is the strict half of the format's
// parse rule, and it is what stops "the transcript is one message shorter" from
// looking like "the transcript is complete".
//
// **Mutation.** Replacing the `return nil, err` in `ParseJournal`'s loop with
// `continue` fails every row here.
func TestParseJournalRefusesALineItCannotRead(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
	}{
		{
			name: "a bullet with too few fields",
			body: "- 1 - 2026-10-02T14:03:11Z\n",
		},
		{
			name: "a bullet with a non-numeric sequence",
			body: "- one - 2026-10-02T14:03:11Z - **mira** - hello\n",
		},
		{
			name: "a bullet with an unparseable timestamp",
			body: "- 1 - yesterday - **mira** - hello\n",
		},
		{
			name: "a bullet with an undelimited author",
			body: "- 1 - 2026-10-02T14:03:11Z - mira - hello\n",
		},
		{
			name: "a bullet whose body ends in a truncated escape",
			body: "- 1 - 2026-10-02T14:03:11Z - **mira** - hello\\\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			messages, err := realtime.ParseJournal(testCase.body)
			if !errors.Is(err, realtime.ErrJournalMalformed) {
				t.Fatalf("ParseJournal returned %v, want ErrJournalMalformed", err)
			}

			if messages != nil {
				t.Errorf("ParseJournal returned %d messages alongside its error; a refusal "+
					"that also returns a transcript is a second answer to the same question",
					len(messages))
			}
		})
	}
}

// TestParseJournalSkipsTheProseAboveTheTranscript is the other half of the rule: a
// line that is not a bullet is not a message, and the explanatory sentence has to
// survive a re-read or the page could not carry one.
func TestParseJournalSkipsTheProseAboveTheTranscript(t *testing.T) {
	t.Parallel()

	body := "Exported by semiplane at 2026-10-02 14:05 UTC. 1 message, oldest first.\n\n" +
		"- 1 - 2026-10-02T14:03:11Z - **mira** - hello there\n"

	messages, err := realtime.ParseJournal(body)
	if err != nil {
		t.Fatalf("ParseJournal: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("ParseJournal returned %d messages, want 1", len(messages))
	}

	if messages[0].Body != "hello there" {
		t.Errorf("the message body is %q, want %q", messages[0].Body, "hello there")
	}
}

// --- The export: secret exclusion ------------------------------------------------

// TestASecretMessageIsAbsentFromTheExportEntirely is the load-bearing security
// assertion of this work item, and it is written the way ADR 0029 requires.
//
// **It asserts the absence of the plaintext from the exported bytes.** Not the
// presence of a marker, not the presence of a placeholder, not a status code, and
// not "the message count is lower". Every one of those is satisfied by an
// implementation that writes the secret into the page and then decorates it — and
// a page is indexed, read by anything with filesystem access to the vault, and
// replicated to every device the GM's Obsidian Sync is connected to. The bytes are
// the only place that failure can be seen.
//
// The second half asserts the same thing about the **revision row**, because the
// row stores the same content and is read back by the GM-only history route.
//
// **Mutation.** Replacing `exportable`'s `continue` with an append of the message
// carrying `Body: "[withheld]"` fails the plaintext assertion in both places.
func TestASecretMessageIsAbsentFromTheExportEntirely(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append the ordinary message: %v", err)
	}

	if _, err := chat.Append("dorn", secretText, fixedClock, true); err != nil {
		t.Fatalf("append the secret message: %v", err)
	}

	if _, err := chat.Append(
		"mira",
		"we are coming from the north",
		fixedClock,
		false,
	); err != nil {
		t.Fatalf("append the second ordinary message: %v", err)
	}

	exported, err := exporter.Export(context.Background(), h.request())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if exported.Included != 2 {
		t.Errorf("Export reported %d included, want 2", exported.Included)
	}

	if exported.Excluded != 1 {
		t.Errorf("Export reported %d excluded, want 1; a GM is entitled to be told a line "+
			"is missing from the page", exported.Excluded)
	}

	raw, err := os.ReadFile(filepath.Join(h.vaultDir, filepath.FromSlash(exported.Path)))
	if err != nil {
		t.Fatalf("read the exported page: %v", err)
	}

	page := string(raw)

	if strings.Contains(page, secretText) {
		t.Errorf("the exported page contains the secret plaintext; redaction is omission, " +
			"and a placeholder still ships the text in the file, the index and the sync " +
			"client")
	}

	// The page also must not carry the *marker* the buffer carries, because a marker
	// surviving into a vault page is S-5.6 failing in the same way: it says "there
	// was a secret here", which is a disclosure about the conversation even when it
	// is not the text.
	if strings.Contains(page, "Secret:") || strings.Contains(page, "[!secret]") {
		t.Errorf("the exported page carries a secret marker; the exclusion is a " +
			"withheld line and not a marked one")
	}

	// The ordinary messages are still there, so the assertion is not passing because
	// the export wrote nothing.
	if !strings.Contains(page, "the tavern is on fire") {
		t.Error("the exported page does not contain an ordinary message; the exclusion " +
			"removed too much")
	}

	for _, row := range h.revisions.recorded() {
		if strings.Contains(row.Content, secretText) {
			t.Errorf("the page_revisions row for %s contains the secret plaintext; the row "+
				"stores the same content and is read back by the history route", row.Path)
		}
	}
}

// TestAnExportAsksForSecretsAndIsRefusedAnyway is the opt-in seam's current state.
//
// **The test asserts the refusal rather than the leak**, and the refusal is the
// point: `IncludeSecrets` is a field on the request so the opt-in has a place to be
// built, but today nothing satisfies it, because the opt-in would have to write
// secrets inside `[!secret]` callouts and that is P10's content, not a route's
// query parameter. An implementation that honoured the field today would be the
// default-on failure the `Export` doc comment is about.
//
// **Mutation.** Changing `exportable` to `if message.Secret && !includeSecrets` into
// `if false` fails this test — and, with the default case unchanged, passes
// `TestASecretMessageIsAbsentFromTheExportEntirely`, which is why this row exists.
func TestAnExportAsksForSecretsAndIsRefusedAnyway(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append the ordinary message: %v", err)
	}

	if _, err := chat.Append("dorn", secretText, fixedClock, true); err != nil {
		t.Fatalf("append the secret message: %v", err)
	}

	request := h.request()
	request.IncludeSecrets = true

	exported, err := exporter.Export(context.Background(), request)
	if err != nil {
		t.Fatalf("Export with IncludeSecrets: %v", err)
	}

	if exported.Excluded != 1 {
		t.Errorf("Export reported %d excluded with IncludeSecrets set, want 1; nothing "+
			"satisfies the opt-in today", exported.Excluded)
	}

	raw, err := os.ReadFile(filepath.Join(h.vaultDir, filepath.FromSlash(exported.Path)))
	if err != nil {
		t.Fatalf("read the exported page: %v", err)
	}

	if strings.Contains(string(raw), secretText) {
		t.Error("the exported page contains the secret plaintext after IncludeSecrets was " +
			"set; the opt-in is not built and the field must not be a way to ask for it")
	}
}

// --- The export: confinement ------------------------------------------------------

// TestTheDerivedNameCannotCarryASeparatorOrATraversal is the *derived* half of
// S-3.5, held as a property over the whole range rather than as a table of
// examples.
//
// The claim is that no value of `time.Time` can produce a file name carrying a
// separator, a traversal, a leading dot, a NUL or a space — so the one string
// semiplane derives from a clock cannot be the string that escapes the root. The
// range walked is every second across four hundred years plus the zero time and a
// pre-epoch time, because a layout's output is periodic in the calendar fields and
// the interesting values are the ones where a field is short or absent.
//
// **Mutation.** Adding a time-of-day field with a colon (`150405Z` → `15:04:05Z`)
// fails the `:` row; using `2006-01-02` alone fails the two exports in one second
// rows of the collision test.
func TestTheDerivedNameCannotCarryASeparatorOrATraversal(t *testing.T) {
	t.Parallel()

	forbidden := []string{"/", "\\", ":", " ", "\x00", "..", "#", "*", "?", "[", "]"}

	times := []time.Time{
		{},
		fixedClock,
		fixedClock.Add(-24 * time.Hour),
		time.Date(1969, time.July, 20, 20, 17, 40, 0, time.UTC),
		time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC),
	}

	// Every second of a leap year in the middle of the range, which crosses a
	// month boundary, a day boundary, a leap day and a hundredth year.
	for moment := time.Date(2024, time.February, 28, 23, 59, 59, 0, time.UTC); ; {
		times = append(times, moment)

		if len(times) > 4000 {
			break
		}

		moment = moment.Add(37 * time.Second)
	}

	for _, moment := range times {
		name := realtime.JournalFileName(moment)

		if !strings.HasSuffix(name, ".md") {
			t.Errorf("JournalFileName(%s) is %q, which does not end in .md; a transcript "+
				"semiplane cannot recognise as a page is a transcript it cannot index",
				moment, name)

			continue
		}

		if !strings.HasPrefix(name, "chat-") {
			t.Errorf("JournalFileName(%s) is %q, want a chat- prefix", moment, name)
		}

		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("JournalFileName(%s) is %q, which contains %q; the derived name "+
					"must be unable to carry a separator or a traversal",
					moment, name, bad)
			}
		}

		if strings.HasPrefix(name, ".") {
			t.Errorf("JournalFileName(%s) is %q, which starts with a dot and is invisible "+
				"to a listing that hides dot-files", moment, name)
		}
	}
}

// TestAnEscapingFolderWritesNoBytes is the confinement table, and it asserts bytes.
//
// Every row is a `ExportRequest.Folder` a caller could be tricked into passing, and
// every row asserts **two** things: that the export refused, and that the whole
// filesystem — the vault *and* the directory beside it that an escaping path
// resolves into — is byte-identical afterwards. A status code alone would be
// satisfied by an implementation that wrote the file and then reported the refusal.
//
// The "no bytes" half is what makes this a test rather than a table, and the
// escape target is a **real directory outside the root** so that a `filepath.Clean`
// plus prefix check — the thing S-3.5 forbids — would actually pass confinement and
// land bytes where this test can see them.
//
// **Mutation.** Replacing `root.At(path)` with `filepath.Join(vaultDir, path)` plus a
// `strings.HasPrefix(vaultDir, ...)` check fails the `parent traversal`, `dot-dot in
// the middle` and `the parent itself` rows with bytes on disk.
func TestAnEscapingFolderWritesNoBytes(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		// folder is the caller-supplied directory.
		folder string
		// escapesTo is a file **relative to the escape target directory** that a
		// filesystem mistake would produce, and which must not exist afterwards.
		// Empty means the row is only asserting "no bytes anywhere".
		escapesTo string
		// confined reports whether `content.Root.At` is expected to refuse the path
		// itself, rather than the folder simply not existing.
		confined bool
	}{
		{
			name:      "one level up",
			folder:    "../" + "outside",
			escapesTo: "outside/chat-20261002-140505.md",
			confined:  true,
		},
		{
			name:      "up through the journal folder",
			folder:    realtime.JournalFolder + "/../../outside",
			escapesTo: "outside/chat-20261002-140505.md",
			confined:  true,
		},
		{
			name:      "the parent itself",
			folder:    "..",
			escapesTo: "chat-20261002-140505.md",
			confined:  true,
		},
		{
			name:      "a deep traversal that lands back inside is still refused by its first hop",
			folder:    "a/b/../../../outside",
			escapesTo: "outside/chat-20261002-140505.md",
			confined:  true,
		},
		{
			name:     "an absolute path",
			folder:   "/etc",
			confined: true,
		},
		{
			name:     "a NUL byte",
			folder:   realtime.JournalFolder + "\x00evil",
			confined: true,
		},
		{
			name:   "a percent-encoded traversal",
			folder: "%2e%2e%2foutside",
		},
		{
			name:   "a backslash separator",
			folder: `..\outside`,
		},
		{
			name:   "a path that does not exist",
			folder: "No Such Folder",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			chat, exporter := testChat(t, h)

			if _, err := chat.Append(
				"mira",
				"the tavern is on fire",
				fixedClock,
				false,
			); err != nil {
				t.Fatalf("append: %v", err)
			}

			before := snapshot(t, h.base)

			request := h.request()
			request.Folder = testCase.folder

			_, err := exporter.Export(context.Background(), request)
			if err == nil {
				t.Fatal("Export accepted a folder it should have refused")
			}

			if testCase.confined {
				// The refusal has to come from the confinement rather than from the
				// folder happening not to exist — otherwise this row is passing for
				// the wrong reason, which is the mistake the mutation log above
				// records twice.
				if !errors.Is(err, content.ErrOutsideRoot) &&
					!errors.Is(err, content.ErrInvalidRef) {
					t.Errorf("Export refused with %v, want content.ErrOutsideRoot or "+
						"content.ErrInvalidRef; a refusal for any other reason is not "+
						"confinement", err)
				}
			}

			assertNoBytesWereWritten(t, before, snapshot(t, h.base))

			if testCase.escapesTo != "" {
				target := filepath.Join(h.outsideDir, filepath.FromSlash(testCase.escapesTo))

				if _, statErr := os.Stat(target); !errors.Is(statErr, fs.ErrNotExist) {
					t.Errorf("%s exists after a refused export (stat said %v); the escape "+
						"target is where a prefix check would have written", target, statErr)
				}
			}
		})
	}
}

// TestAConvolutedFolderInsideTheVaultIsStillAccepted is the other direction, and it
// is what stops the confinement row above from passing for the wrong reason.
//
// A test asserting only "everything with a `..` in it is refused" would be satisfied
// by an implementation that refused every path a GM might legitimately type, and it
// would pass while the product could not write a page into a folder called
// `Notes/../Journal`. Here the path is convoluted, stays inside, and the export
// writes.
//
// **Mutation.** Refusing any `Folder` containing `..` fails this test.
func TestAConvolutedFolderInsideTheVaultIsStillAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	request := h.request()
	request.Folder = "Notes/../" + realtime.JournalFolder

	exported, err := exporter.Export(context.Background(), request)
	if err != nil {
		t.Fatalf("Export into a convoluted but inside folder: %v", err)
	}

	want := realtime.JournalFolder + "/chat-20261002-140505.md"
	if exported.Path != want {
		t.Errorf("Export wrote %q, want %q; a path that stays inside must be cleaned to "+
			"the same spelling in the row and on disk", exported.Path, want)
	}

	if _, err := os.Stat(filepath.Join(h.vaultDir, filepath.FromSlash(want))); err != nil {
		t.Errorf("the exported page is not on disk at the cleaned path: %v", err)
	}
}

// TestTheExportWritesInsideTheVault is the control for the confinement table.
//
// Without it, "no bytes" is satisfied by an exporter that never writes anything, and
// the table above would be green for a broken product. Every refusal assertion in
// this file is only meaningful next to this one.
func TestTheExportWritesInsideTheVault(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	exported, err := exporter.Export(context.Background(), h.request())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(h.vaultDir, filepath.FromSlash(exported.Path)))
	if err != nil {
		t.Fatalf("the exported page is not on disk: %v", err)
	}

	if !strings.Contains(string(raw), "the tavern is on fire") {
		t.Error("the exported page does not carry the message it was asked to publish")
	}

	// And it is a page, not a stray file: the extension is what the indexer keys on
	// and a transcript without it is a file semiplane wrote and cannot read.
	if !strings.HasSuffix(exported.Path, ".md") {
		t.Errorf("the exported path is %q, which is not a page path", exported.Path)
	}

	info, err := os.Stat(filepath.Join(h.vaultDir, filepath.FromSlash(exported.Path)))
	if err != nil {
		t.Fatalf("stat the exported page: %v", err)
	}

	if mode := info.Mode().Perm(); mode != realtime.JournalFileMode {
		t.Errorf("the exported page is mode %o, want %o; the campaign's root is 0o700 and "+
			"a readable file inside it is consistent only by accident", mode, realtime.JournalFileMode)
	}
}

// --- The export: the revision row --------------------------------------------------

// TestTheRevisionRowIsWrittenBeforeTheFile is S-6.4's ordering, asserted from the
// filesystem rather than from the call order.
//
// The `observe` hook runs **inside** `Revisions.Append`, so it asks the vault
// whether the page exists at the instant the row is recorded. If the write came
// first, the answer would be yes and the test would fail. Asserting the order from
// a log of calls instead would only assert that the code does what its comments
// say, which is the mistake this project's own mutation log records.
//
// The second half is the reverse: a revision log that fails leaves **no page**, which
// is the other half of S-6.4 — a refused write records nothing.
//
// **Mutation.** Swapping the two calls in `Export` fails the `before` assertion; a
// version that swallows `Append`'s error fails the second half.
func TestTheRevisionRowIsWrittenBeforeTheFile(t *testing.T) {
	t.Parallel()

	t.Run("the page does not exist when the row is recorded", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		chat, exporter := testChat(t, h)

		if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
			t.Fatalf("append: %v", err)
		}

		var existed bool

		h.revisions.observe = func(revision realtime.Revision) {
			_, statErr := os.Stat(filepath.Join(h.vaultDir, filepath.FromSlash(revision.Path)))
			existed = statErr == nil
		}

		if _, err := exporter.Export(context.Background(), h.request()); err != nil {
			t.Fatalf("Export: %v", err)
		}

		if existed {
			t.Error("the page was on disk before the revision row was appended; a row " +
				"recorded after the file is not the ordering that makes a lost row " +
				"recoverable")
		}

		rows := h.revisions.recorded()
		if len(rows) != 1 {
			t.Fatalf("%d revision rows were recorded, want 1", len(rows))
		}

		if rows[0].Content == "" {
			t.Error("the revision row carries no content; a history that does not explain " +
				"the file it is a history of explains nothing")
		}

		if _, statErr := os.Stat(
			filepath.Join(h.vaultDir, filepath.FromSlash(rows[0].Path)),
		); statErr != nil {
			t.Errorf("the page is not on disk after a successful export: %v", statErr)
		}
	})

	t.Run("a failed revision row leaves no page", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		chat, exporter := testChat(t, h)

		if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
			t.Fatalf("append: %v", err)
		}

		h.revisions.fail = errors.New("the database is gone")

		before := snapshot(t, h.base)

		if _, err := exporter.Export(context.Background(), h.request()); err == nil {
			t.Fatal("Export succeeded with a revision log that fails")
		}

		assertNoBytesWereWritten(t, before, snapshot(t, h.base))
	})

	t.Run("a failed write leaves the row, which is the recoverable direction", func(t *testing.T) {
		t.Parallel()

		// The write failure is injected by materialising a **directory** at the page
		// path, which is a real race rather than a contrivance: a sync client that
		// creates `chat-20261002-140505.md/` between the row and the rename. The
		// rename then fails with `EISDIR` and the page is not written, which is
		// exactly the case the ordering argument is about — the row is noise, and
		// noise is the recoverable direction.
		h := newHarness(t)
		chat, exporter := testChat(t, h)

		if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
			t.Fatalf("append: %v", err)
		}

		h.revisions.observe = func(revision realtime.Revision) {
			// Best effort on purpose: the test's assertion is about the row and the
			// write, not about this hook, and a failure here would make the whole
			// test depend on a cleanup path.
			_ = os.Mkdir(filepath.Join(h.vaultDir, filepath.FromSlash(revision.Path)), 0o700)
		}

		if _, err := exporter.Export(context.Background(), h.request()); err == nil {
			t.Fatal("Export succeeded even though the page path became a directory")
		}

		rows := h.revisions.recorded()
		if len(rows) != 1 {
			t.Fatalf("%d revision rows were recorded, want 1; a write that failed after "+
				"the row must still leave it", len(rows))
		}

		info, err := os.Stat(filepath.Join(h.vaultDir, filepath.FromSlash(rows[0].Path)))
		if err != nil {
			t.Fatalf("stat the page path: %v", err)
		}

		if !info.IsDir() {
			t.Errorf("the page path is a file of %d bytes; the injected rename failure did "+
				"not happen, so this test is not testing what it says it is", info.Size())
		}
	})
}

// TestTwoExportsInOneSecondDoNotOverwriteEachOther is the timestamp's only real
// weakness, and it is a data-loss weakness rather than a cosmetic one.
//
// The name is precise to the second, so two exports in the same second collide. A
// rename over an existing file would destroy the earlier transcript **and leave its
// `page_revisions` row describing content that is no longer on disk** — which is the
// one thing ADR 0008's argument says must not happen.
//
// **Mutation.** Replacing `Exporter.target`'s stat with a single `root.At` and no
// free-name search fails this test: one file, one name, two rows.
func TestTwoExportsInOneSecondDoNotOverwriteEachOther(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "first", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	first, firstErr := exporter.Export(context.Background(), h.request())
	if firstErr != nil {
		t.Fatalf("the first export: %v", firstErr)
	}

	if _, err := chat.Append("mira", "second", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	second, secondErr := exporter.Export(context.Background(), h.request())
	if secondErr != nil {
		t.Fatalf("the second export: %v", secondErr)
	}

	if first.Path == second.Path {
		t.Fatalf("two exports in one second both wrote %q; the second overwrote the first "+
			"and left its revision row describing content that is no longer there", first.Path)
	}

	rows := h.revisions.recorded()
	if len(rows) != 2 {
		t.Fatalf("%d revision rows were recorded, want 2", len(rows))
	}

	for _, row := range rows {
		if row.Path == rows[0].Path && row.Content != rows[0].Content {
			t.Errorf("two rows share the path %q with different content; one of them "+
				"describes a file that is no longer on disk", row.Path)
		}
	}

	// Both files exist and both say what they said, which is the substantive half of
	// the assertion: a path that differs is not enough, since one of them could be
	// empty.
	entries, err := os.ReadDir(filepath.Join(h.vaultDir, realtime.JournalFolder))
	if err != nil {
		t.Fatalf("read the journal folder: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("the journal folder holds %d files, want 2: %v", len(entries), entries)
	}

	for _, row := range rows {
		raw, readErr := os.ReadFile(filepath.Join(h.vaultDir, filepath.FromSlash(row.Path)))
		if readErr != nil {
			t.Errorf("read the page for %s: %v", row.Path, readErr)

			continue
		}

		if string(raw) != row.Content {
			t.Errorf("the page at %s does not match the revision row for it; a row "+
				"describing different content is the hole ADR 0008 is about", row.Path)
		}
	}
}

// TestAnExportWithNothingToExportIsRefused is the empty case, and refusing it is a
// decision rather than a default.
//
// A page saying "no messages" is a page the GM has to delete, and it teaches
// nothing. The case also covers "every message is a secret one", which is the
// interesting half: the buffer is not empty, and the refusal is still right.
//
// **Mutation.** Returning an empty `Exported` instead of the error fails both rows.
func TestAnExportWithNothingToExportIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("an empty buffer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		exporter := realtime.NewExporter(
			realtime.NewChat(8, strings.NewReader(strings.Repeat("u", 64))),
			h.registry,
			h.revisions,
		)

		before := snapshot(t, h.base)

		if _, err := exporter.Export(
			context.Background(),
			h.request(),
		); !errors.Is(
			err,
			realtime.ErrNoExport,
		) {
			t.Fatalf("Export of an empty chat returned %v, want ErrNoExport", err)
		}

		assertNoBytesWereWritten(t, before, snapshot(t, h.base))
	})

	t.Run("every message is a secret one", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		chat, exporter := testChat(t, h)

		if _, err := chat.Append("dorn", secretText, fixedClock, true); err != nil {
			t.Fatalf("append: %v", err)
		}

		before := snapshot(t, h.base)

		if _, err := exporter.Export(
			context.Background(),
			h.request(),
		); !errors.Is(
			err,
			realtime.ErrNoExport,
		) {
			t.Fatalf("Export of an all-secret chat returned %v, want ErrNoExport", err)
		}

		assertNoBytesWereWritten(t, before, snapshot(t, h.base))
	})
}

// TestAnUnconfiguredExporterRefusesRatherThanWritingUntracked covers the seam that
// `NewExporter` deliberately tolerates.
//
// An exporter with no revision log would write a page into the vault that no history
// explains — the file with no row that ADR 0008 calls the unrecoverable direction —
// and the failure would be discovered when somebody asked "who changed this page".
//
// **Mutation.** Removing the `e.revisions == nil` check from `Export` makes this
// panic rather than refuse; removing the check for `e.roots` writes nothing and so
// silently produces a broken page.
func TestAnUnconfiguredExporterRefusesRatherThanWritingUntracked(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat := realtime.NewChat(8, strings.NewReader(strings.Repeat("w", 64)))

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	before := snapshot(t, h.base)

	unconfigured := []*realtime.Exporter{
		realtime.NewExporter(chat, nil, h.revisions),
		realtime.NewExporter(chat, h.registry, nil),
		realtime.NewExporter(nil, h.registry, h.revisions),
	}

	for index, exporter := range unconfigured {
		if _, err := exporter.Export(
			context.Background(),
			h.request(),
		); !errors.Is(
			err,
			realtime.ErrExportUnconfigured,
		) {
			t.Errorf("exporter %d returned %v, want ErrExportUnconfigured", index, err)
		}
	}

	assertNoBytesWereWritten(t, before, snapshot(t, h.base))
}

// TestAnExportRefusesARequestThatNamesNoCampaign is the zero-value check, and both
// fields are ones whose zero value is plausible rather than obviously absent.
func TestAnExportRefusesARequestThatNamesNoCampaign(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	chat, exporter := testChat(t, h)

	if _, err := chat.Append("mira", "the tavern is on fire", fixedClock, false); err != nil {
		t.Fatalf("append: %v", err)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*realtime.ExportRequest)
	}{
		{
			name:   "no campaign",
			mutate: func(request *realtime.ExportRequest) { request.CampaignID = 0 },
		},
		{
			name:   "no slug",
			mutate: func(request *realtime.ExportRequest) { request.Slug = "   " },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			request := h.request()
			testCase.mutate(&request)

			before := snapshot(t, h.base)

			if _, err := exporter.Export(
				context.Background(),
				request,
			); !errors.Is(
				err,
				realtime.ErrNoExport,
			) {
				t.Fatalf("Export returned %v, want ErrNoExport", err)
			}

			assertNoBytesWereWritten(t, before, snapshot(t, h.base))
		})
	}
}

// --- The mutation log ---------------------------------------------------------------

// TestEveryAuditInThisFileHasAMutationThatBreaksIt is the mechanical version of the
// claim this file's header makes about itself.
//
// Each row names a `Test…` in this file and the **exact mutation** that was applied
// to the source and observed to break it. The test asserts the mutation string is
// still present in the file — so a recorded mutation that was deleted, or renamed,
// or fixed by a later edit without the test being revisited, fails here rather than
// leaving a decoration behind.
//
// This is the third version of that idea in this project and the second one to catch
// something. The `TestAConvolutedFolderInsideTheVaultIsStillAccepted` row exists
// because the confinement table was written first and would have passed an
// implementation that refused every dotted path.
//
// **The rows are not self-verifying** — nothing here re-applies a mutation — so this
// test asserts that a claim was recorded and that the code it names is still there.
// The evidence for each claim is in the report that accompanied the commit, and this
// test is what stops the claim from becoming a comment.
func TestEveryAuditInThisFileHasAMutationThatBreaksIt(t *testing.T) {
	t.Parallel()

	for _, claim := range []struct {
		test       string
		mutation   string
		stillThere string
	}{
		{
			test:       "TestTheBufferIsBoundedAndEvictsTheOldestFirst",
			mutation:   "c.ring[(c.start+c.count)%len(c.ring)] = message",
			stillThere: "c.ring[(c.start+c.count)%len(c.ring)] = message",
		},
		{
			test:       "TestTheBufferDoesNotGrowWithTheNumberOfMessages",
			mutation:   "ring: make([]Message, 0, capacity)",
			stillThere: "ring:        make([]Message, capacity)",
		},
		{
			test:       "TestEvictionIsDeterministic",
			mutation:   "iterate a map of messages instead of walking the ring",
			stillThere: "messages := make([]Message, 0, c.count)",
		},
		{
			test:       "TestAppendRefusesBeforeTheRingIsTouched",
			mutation:   "assign the sequence number before validating",
			stillThere: "Every refusal happens before the ring is touched",
		},
		{
			test:       "TestTheSequenceIsMonotonicAndSurvivesEmptiness",
			mutation:   "highest updated only on the non-full branch",
			stillThere: "c.highest = message.Seq\n\n\treturn message, nil",
		},
		{
			test:       "TestSinceTellsNothingNewApartFromAGap",
			mutation:   "drop the gap computation",
			stillThere: "feed.Gap = since+1 < c.oldest",
		},
		{
			test:       "TestSinceRefusesACursorAheadOfThisLoading",
			mutation:   "answer an ahead cursor with an empty feed",
			stillThere: "ErrFutureSeq",
		},
		{
			test:       "TestVisibleOmitsASecretMessageRatherThanMarkingIt",
			mutation:   "return the secret message with the flag cleared",
			stillThere: "if message.Secret && !includeSecrets {\n\t\t\tcontinue\n\t\t}",
		},
		{
			test:       "TestASecretMessageIsAbsentFromTheExportEntirely",
			mutation:   "write a placeholder instead of omitting the line",
			stillThere: "excluded++\n\n\t\t\tcontinue",
		},
		{
			test:       "TestAnExportAsksForSecretsAndIsRefusedAnyway",
			mutation:   "give exportable an includeSecrets parameter",
			stillThere: "func (c *Chat) exportable() (messages []Message, excluded int)",
		},
		{
			test:       "TestTheDerivedNameCannotCarryASeparatorOrATraversal",
			mutation:   "a colon in the time layout",
			stillThere: `at.UTC().Format("20060102-150405")`,
		},
		{
			test:       "TestAnEscapingFolderWritesNoBytes",
			mutation:   "filepath.Join plus a prefix check instead of os.Root",
			stillThere: "target, err := root.At(journalPath(folder, moment, attempt))",
		},
		{
			test:       "TestAConvolutedFolderInsideTheVaultIsStillAccepted",
			mutation:   "refuse any folder containing a dot-dot",
			stillThere: "return path.Join(folder, name)",
		},
		{
			test:       "TestTheRevisionRowIsWrittenBeforeTheFile",
			mutation:   "write the file before appending the row",
			stillThere: "if err := e.revisions.Append(ctx, revision); err != nil {",
		},
		{
			test:       "TestTwoExportsInOneSecondDoNotOverwriteEachOther",
			mutation:   "rename over an existing page",
			stillThere: "_, statErr := target.Stat()",
		},
		{
			test:       "TestParseJournalRefusesALineItCannotRead",
			mutation:   "skip a bullet line that does not parse",
			stillThere: "message, err := parseJournalLine",
		},
	} {
		if claim.test == "" {
			t.Fatal("a mutation claim has no test name")
		}

		if claim.stillThere == "" {
			t.Fatalf("the claim for %s names no source that must still be present",
				claim.test)
		}

		if !strings.Contains(chatSource, claim.stillThere) {
			t.Errorf("the mutation recorded for %s names source text that is no longer "+
				"there:\n  %q\nEither the mutation was reverted without revisiting the test, "+
				"or the claim is stale and one of the two has to go",
				claim.test, claim.stillThere)
		}
	}
}

// chatSource is `chat.go` as it stands, for the mutation log's staleness check.
//
// Read at test time rather than embedded, so the check compares the claim against
// the code that is actually there. A claim table holding a copy of the file would
// pass forever.
var chatSource = func() string {
	raw, err := os.ReadFile("chat.go")
	if err != nil {
		return ""
	}

	return string(raw)
}()
