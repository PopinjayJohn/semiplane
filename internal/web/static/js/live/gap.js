// gap.js — the live chrome's arithmetic kernel.
//
// # Why this file exists at all
//
// There is no Node in this repository's toolchain — deliberately, per the note on
// `make a11y` — so a JavaScript test is not available, and a Go test asserting that
// this source *mentions* `scrollHeight` is not a test of anything. So the rule that
// carries arithmetic is one `return` of one expression over four numbers, and
// `gap_arith_test.go` parses this file and evaluates it over every combination that
// matters.
//
// # The rule: when does an appended line scroll into view?
//
// Both logs are patched with `mode: append`, so a line arrives *below* the reader's
// position rather than where they were looking. The naive response — scroll to the
// bottom on every append — destroys the one thing a reader scrolling a chat log is
// doing, which is reading history. The correct response is conditional on whether
// they were *already* at the bottom:
//
//     follow the tail when  scrollTop + clientHeight  >=  scrollHeight - tolerance
//
// and the expression below is the signed distance by which they are short of that
// threshold, so a caller asks one question — "is this positive?" — and this file
// owns the arithmetic.
//
// # Why the tolerance, and why it is four pixels
//
// `scrollTop` is an integer, `clientHeight` includes a fractional scrollbar on some
// platforms, and a container whose content is shorter than itself reports a
// `scrollTop` of zero with a `scrollHeight` *below* `clientHeight`. An exact
// comparison therefore misses the common case of a reader who has seen everything:
// the last line is flush with the bottom, the rounding leaves a pixel of overhang,
// and the reader is left scrolled one line above the new one forever after.
//
// Four pixels is the smallest gap that absorbs that rounding on every platform this
// product runs on, and it is small enough that a reader who has deliberately
// scrolled *up* by three pixels still counts as following — which is the correct
// answer, because they have not scrolled up.
//
// # What this file must never grow
//
// A conditional, a loop, a string, a call, a member access, an assignment or an
// unbound name. The evaluator `gap_arith_test.go` is a restricted grammar and
// anything outside it is a **parse error**, so the build goes red rather than the
// gate quietly evaluating less of the file. `TestTheArithmeticGateRejectsWhatIt
// CannotCheck` holds that from the other side, and
// `TestTheArithmeticGateEvaluatesWhatItClaimsTo` holds that the evaluator is not
// simply refusing everything.
//
// There is **no evaluator-invented vocabulary**: the four parameter names below are
// the only identifiers the expression may mention. An evaluator that bound
// `Math.min` or `document` would be a gate on a document rather than on the
// shipped bytes, which is the mistake `step_arith_test.go` records and the reason it
// refuses to bind them here.

/**
 * The signed distance by which a scroll region is short of following its tail.
 *
 * Positive means "the reader is at the bottom, scroll on append"; negative means
 * "the reader has scrolled away, leave them where they are".
 *
 * @param {number} viewTop   how far the region is scrolled, `scrollTop`
 * @param {number} viewHeight the region's own height, `clientHeight`
 * @param {number} contentHeight the height of everything in it, `scrollHeight`
 * @param {number} tolerance how close counts as "at the bottom"
 * @returns {number} the distance short of the follow threshold
 */
function spFollowGap(viewTop, viewHeight, contentHeight, tolerance) {
  return viewTop + viewHeight - (contentHeight - tolerance);
}