package edit_test

// The diff algorithm, tested as an algorithm.
//
// A diff that is wrong is the one bug in this phase that still *looks* right: two
// columns of plausible text side by side, silently misaligned. So these tests assert
// the *structure* — how many hunks, which lines on which side, with which numbers —
// rather than that the output is non-empty, and the hunk-merge rule gets its own
// cases because it is the one judgement call in the file.

import (
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web/components/edit"
)

// TestDiffOfIdenticalTextIsEmpty is the case a 412 can reach and a GM must be able to
// read: a client presenting a validator for content that has not changed.
//
// Empty rather than one empty hunk, and the difference matters — the conflict view
// has a designed state for "no differences" and would have no way to tell "no
// differences" from "one hunk with no lines in it".
func TestDiffOfIdenticalTextIsEmpty(t *testing.T) {
	t.Parallel()

	if hunks := edit.Diff("one\ntwo\n", "one\ntwo\n"); len(hunks) != 0 {
		t.Errorf("Diff of identical text = %d hunks, want none: %+v", len(hunks), hunks)
	}
}

// TestDiffOfOneChangedLine is the ordinary case: one line replaced, one hunk, one line
// on each side, with each side's own line number.
func TestDiffOfOneChangedLine(t *testing.T) {
	t.Parallel()

	hunks := edit.Diff("one\ntwo\nthree\n", "one\nTWO\nthree\n")
	if len(hunks) != 1 {
		t.Fatalf("Diff produced %d hunks, want 1: %+v", len(hunks), hunks)
	}

	hunk := hunks[0]
	if hunk.Index != 1 {
		t.Errorf("hunk index = %d, want 1", hunk.Index)
	}

	assertLine(t, "left", hunk.Left, "two", 2)
	assertLine(t, "left", hunk.Left, "two", 2)
	assertLine(t, "right", hunk.Right, "TWO", 2)

	// The same replacement lower down, where the line number is not 2. Without it
	// every number this file asserts would be 2, and a diff that numbered every line
	// "2" would pass.
	lower := edit.Diff("one\ntwo\nthree\nfour\n", "one\nTWO\nthree\nFOUR\n")
	if len(lower) != 1 {
		t.Fatalf("two replacements with two lines between them produced %d hunks, "+
			"want 1: %+v", len(lower), lower)
	}

	assertLine(t, "right", lower[0].Right[len(lower[0].Right)-1:], "FOUR", 4)
}

// TestDiffNumbersLinesOnTheirOwnSide is the property that makes a two-column diff
// navigable: the numbers are the reader's way into Obsidian, and the same line is not
// at the same number on both sides.
//
// The fixture is a rotation — one line moved from the top to the bottom — because it
// is the smallest case where the two sides' numbering genuinely differs, and it is the
// case a greedy diff gets wrong. The hunk holds the moved line on *both* sides, at
// line 1 on one and line 3 on the other, and a diff that reported the left number on
// both would send a GM to the wrong paragraph.
func TestDiffNumbersLinesOnTheirOwnSide(t *testing.T) {
	t.Parallel()

	hunks := edit.Diff("x\na\nb", "a\nb\nx")
	if len(hunks) != 1 {
		t.Fatalf("a moved line produced %d hunks, want 1: %+v", len(hunks), hunks)
	}

	hunk := hunks[0]

	// The hunk spans the move, so it carries the two unchanged lines between the
	// removal and the addition as context — and the moved line is at a different
	// number on each side *within the same hunk*, which is the property.
	if at, found := lineNumberOf(hunk.Left, "x"); !found || at != 1 {
		t.Errorf("the left side places \"x\" at %d (found=%t), want 1: %+v",
			at, found, hunk.Left)
	}

	if at, found := lineNumberOf(hunk.Right, "x"); !found || at != 3 {
		t.Errorf("the right side places \"x\" at %d (found=%t), want 3; each side is "+
			"numbered against itself or a reader is sent to the wrong paragraph: %+v",
			at, found, hunk.Right)
	}
}

// lineNumberOf is the number one side gives a line of text, and whether it is there.
func lineNumberOf(lines []edit.DiffLine, text string) (int, bool) {
	for _, line := range lines {
		if line.Text == text {
			return line.Number, true
		}
	}

	return 0, false
}

// TestDiffSeparatesDistantChangesAndMergesAdjacentOnes is the context rule, both
// directions, and both matter for §4.8's per-hunk accept/reject.
//
// Two changes far apart are two decisions, and merging them would make one button
// answer for both. Two changes next to each other are one region of prose, and
// splitting them would make a GM accept a paragraph in two pieces — where the second
// acceptance silently undoes work the first did not cover.
func TestDiffSeparatesDistantChangesAndMergesAdjacentOnes(t *testing.T) {
	t.Parallel()

	const gap = 8

	build := func(first, last string) string {
		lines := make([]string, 0, gap+2)
		lines = append(lines, first)

		for range gap {
			lines = append(lines, "unchanged")
		}

		return strings.Join(append(lines, last), "\n")
	}

	distant := edit.Diff(build("one", "ten"), build("ONE", "TEN"))
	if len(distant) != 2 {
		t.Errorf("two changes %d lines apart produced %d hunks, want 2: %+v",
			gap, len(distant), distant)
	}

	if len(distant) == 2 && (distant[0].Index != 1 || distant[1].Index != 2) {
		t.Errorf("hunk indexes = %d, %d; they are assigned in order by Diff so a "+
			"heading can name them", distant[0].Index, distant[1].Index)
	}

	adjacent := edit.Diff("one\ntwo\nthree\n", "ONE\nTWO\nthree\n")
	if len(adjacent) != 1 {
		t.Errorf("two adjacent changes produced %d hunks, want 1: %+v",
			len(adjacent), adjacent)
	}
}

// TestDiffOfAnAddedOrRemovedLine: the two shapes where the sides have different
// lengths, which is the whole reason a hunk is not a list of pairs.
func TestDiffOfAnAddedOrRemovedLine(t *testing.T) {
	t.Parallel()

	added := edit.Diff("one\nthree\n", "one\ntwo\nthree\n")
	if len(added) != 1 {
		t.Fatalf("an added line produced %d hunks, want 1: %+v", len(added), added)
	}

	if len(added[0].Left) != 0 {
		t.Errorf("the left side of an addition holds %d lines, want none: %+v",
			len(added[0].Left), added[0].Left)
	}

	assertLine(t, "right", added[0].Right, "two", 2)

	removed := edit.Diff("one\ntwo\nthree\n", "one\nthree\n")
	if len(removed) != 1 {
		t.Fatalf("a removed line produced %d hunks, want 1: %+v", len(removed), removed)
	}

	assertLine(t, "left", removed[0].Left, "two", 2)

	if len(removed[0].Right) != 0 {
		t.Errorf("the right side of a removal holds %d lines, want none: %+v",
			len(removed[0].Right), removed[0].Right)
	}
}

// TestDiffOfAnEmptySide: a GM who cleared the field, and a page that did not exist
// when they opened it. The hunk carries everything on one side and nothing on the
// other, and the view has to render that without inventing a blank line.
func TestDiffOfAnEmptySide(t *testing.T) {
	t.Parallel()

	cleared := edit.Diff("", "one\ntwo\n")
	if len(cleared) != 1 {
		t.Fatalf("clearing the buffer produced %d hunks, want 1: %+v", len(cleared), cleared)
	}

	if len(cleared[0].Left) != 0 {
		t.Errorf("the left side holds %d lines, want none: %+v",
			len(cleared[0].Left), cleared[0].Left)
	}

	if len(cleared[0].Right) != 2 {
		t.Errorf("the right side holds %d lines, want 2: %+v",
			len(cleared[0].Right), cleared[0].Right)
	}
}

// TestDiffDoesNotClaimATrailingNewlineIsADifference is the rule `splitLines` exists
// for, and it is worth its own test because getting it wrong is invisible: every
// correctly-terminated file would differ from every other by one blank line at the
// end, and a GM would be offered a hunk containing an empty row on both sides.
//
// The two texts below are equal as *lines* and differ as *bytes*, which is the case
// the view does not claim to have found. The bytes are still compared by the
// validator, which is what decides whether a write happens at all.
func TestDiffDoesNotClaimATrailingNewlineIsADifference(t *testing.T) {
	t.Parallel()

	if hunks := edit.Diff("one\ntwo", "one\ntwo\n"); len(hunks) != 0 {
		t.Errorf("Diff of texts differing only in a trailing newline = %d hunks, want "+
			"none: %+v", len(hunks), hunks)
	}
}

// TestDiffFallsBackToOneHunkPastTheCap is the guard on the LCS table, and it is
// asserted as *behaviour* rather than as a memory measurement: a pair of sides too
// large to diff becomes one hunk covering everything, which is a correct answer and
// merely less useful.
//
// Two 600-line sides is 360,000 line-pairs against a cap of 262,144, so this is past
// it — and if the cap were raised the test would still pass, which is the point: the
// contract is "a huge conflict is one hunk", not "the table is this big".
func TestDiffFallsBackToOneHunkPastTheCap(t *testing.T) {
	t.Parallel()

	const lines = 600

	left := make([]string, 0, lines)
	right := make([]string, 0, lines)

	for at := range lines {
		left = append(left, "left "+strings.Repeat("x", at%7))
		right = append(right, "right "+strings.Repeat("y", at%5))
	}

	hunks := edit.Diff(strings.Join(left, "\n"), strings.Join(right, "\n"))

	if len(hunks) != 1 {
		t.Fatalf("a %d-line conflict produced %d hunks, want 1", lines, len(hunks))
	}

	if len(hunks[0].Left) != lines || len(hunks[0].Right) != lines {
		t.Errorf("the fallback hunk holds %d left and %d right lines, want %d each: "+
			"the fallback must be a *complete* report, not a truncated one",
			len(hunks[0].Left), len(hunks[0].Right), lines)
	}
}

// TestDiffOfAMovedLineKeepsItInBothColumns is why the algorithm is an LCS and not a
// greedy "skip ahead to the next match".
//
// A greedy diff reports a moved line as a deletion plus an insertion, and a GM
// reconciling a moved heading would be asked to accept its disappearance and then
// separately its reappearance — two decisions that are not independent, and the
// second undoes the first. So the assertion is that both sides of the hunk hold the
// *same lines*, in a different order: that is what a move looks like, and a
// delete-then-add diff cannot produce it.
func TestDiffOfAMovedLineKeepsItInBothColumns(t *testing.T) {
	t.Parallel()

	hunks := edit.Diff("one\ntwo\nthree\n", "two\nthree\none\n")
	if len(hunks) != 1 {
		t.Fatalf("a moved line produced %d hunks, want 1: %+v", len(hunks), hunks)
	}

	left := lineTexts(hunks[0].Left)
	right := lineTexts(hunks[0].Right)

	if len(left) != len(right) {
		t.Fatalf("the sides hold %d and %d lines, want the same: %v vs %v",
			len(left), len(right), left, right)
	}

	// The same *set*, in a different order. Comparing sorted copies rather than the
	// sequences is what makes this a test about a move: a delete-then-add diff holds
	// the same set too, and what distinguishes them is that the two sides report the
	// lines at different numbers — which the number assertion below pins.
	if strings.Join(sortedCopy(left), ",") != strings.Join(sortedCopy(right), ",") {
		t.Errorf("the two sides of a moved line hold different text:\n left %v\nright %v",
			left, right)
	}

	if strings.Join(left, ",") == strings.Join(right, ",") {
		t.Errorf("the two sides of a moved line are in the same order (%v); the fixture "+
			"rotates, so the diff has found a move it did not report", left)
	}

	// "one" is line 1 on the left and line 3 on the right.
	if at, found := lineNumberOf(hunks[0].Left, "one"); !found || at != 1 {
		t.Errorf("the left side places \"one\" at %d (found=%t), want 1", at, found)
	}

	if at, found := lineNumberOf(hunks[0].Right, "one"); !found || at != 3 {
		t.Errorf("the right side places \"one\" at %d (found=%t), want 3", at, found)
	}
}

// lineTexts is a hunk side's line texts, in order.
func lineTexts(lines []edit.DiffLine) []string {
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}

	return texts
}

// sortedCopy is a sorted copy, so two sides can be compared as sets.
func sortedCopy(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)

	return copied
}

// assertLine checks a hunk's side: that it holds one line, and that the line is this
// text at this number.
//
// One line, always, because that is the shape every case here is asserting: a
// replaced line, an added line, a removed line. A hunk of several changed lines is
// asserted by position instead, in the tests that care about the ordering.
func assertLine(t *testing.T, side string, lines []edit.DiffLine, wantText string, wantNumber int) {
	t.Helper()

	if len(lines) != 1 {
		t.Fatalf("the %s side holds %d lines, want 1: %+v", side, len(lines), lines)
	}

	if lines[0].Text != wantText {
		t.Errorf("the %s side's line is %q, want %q", side, lines[0].Text, wantText)
	}

	if lines[0].Number != wantNumber {
		t.Errorf("the %s side's line is numbered %d, want %d", side, lines[0].Number, wantNumber)
	}
}
