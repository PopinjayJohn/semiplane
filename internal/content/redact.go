// The redaction seam: the one place content the viewer may not see leaves a
// page, and the position that removal runs at is the security property.
//
// S-5.7 states it in one sentence — redaction happens before sanitisation and
// before the value reaches any template, so no intermediate buffer holds an
// unredacted copy for a non-GM — and this file exists because the sentence is
// otherwise easy to satisfy in the wrong order. Everything after it in the
// pipeline is cheap to reorder and expensive to get right:
//
//   - Redact after the render and the unredacted body has been through three
//     buffers this package does not control — goldmark's, bluemonday's, and the
//     cache's — each of which is a thing a bug, a log line, a core dump or a
//     future phase can read from.
//   - Redact inside the template and the value has crossed a sanitiser whose
//     whole job is to be the last thing that touches author-supplied HTML, so
//     the two controls would have to be reasoned about together.
//   - Redact before the render, on the source, and the non-GM pipeline never
//     constructs the string. There is nothing to leak because there is nothing
//     there.
//
// So the interface is expressed on the *source* text, `Redact(body string, …)`,
// and a caller cannot reach it late in the pipeline without changing the type of
// the value it holds. The wiki route's `source` and `redacted` types make that
// ordering structural rather than conventional; this file is the contract that
// makes it expressible.
//
// # What omission means here, concretely
//
// §5.6.1 rules out every form of hiding that still ships the text: `display:none`,
// a `hidden` attribute, an HTML comment, a class. Each of those puts the secret
// in the response body, and a response body is something a player can read with
// curl. So the implementation below **splices the callout's bytes out of the
// source string** and returns the remainder. The secret text is never copied into
// the result, never escaped into it, and never held in a variable that later
// reaches a buffer. There is no placeholder either: §5.6.1's reason is that a
// stub still discloses the *existence and position* of a secret, which is
// information in a game about who is hiding what.
//
// `TestTheSecretTextIsAbsentFromEveryByteThePlayerReceives` is the assertion, and
// it is deliberately blunt about the bytes — the secret text must appear nowhere
// in the response, in a text node, in an attribute, or in a comment — because the
// alternatives are the ones that pass a narrower check.

package content

import (
	"slices"
	"strings"
)

// Redactor removes content the viewer may not see, operating on the *source*
// text before the render.
//
// The position is the security property, not an implementation detail. A
// redactor that ran after the render would mean the unredacted body existed in
// the renderer's buffers, the sanitiser's input, and possibly a cache entry —
// three places a bug, a log line or a core dump would leak it from. Running on
// the source means the non-GM pipeline never constructs the string at all.
//
// An interface rather than a function value so that a caller cannot build the
// pipeline with a redactor chosen at the point of use, and so a test can
// substitute one that removes a known marker — which is the only way to assert
// S-14.1's property, which is that the text appears *nowhere* rather than that
// one function returned something falsy.
type Redactor interface {
	// Redact returns body with viewer-invisible content removed, when
	// includeSecrets is false. With includeSecrets true it returns body
	// unchanged.
	//
	// The whole body, not a rendered fragment and not a front-matter value: a
	// redactor that could only see the prose could not remove anything an author
	// wrote in a field, and a redactor that could only see the rendered HTML
	// would be running one step too late.
	//
	// includeSecrets is the caller's own decision about the viewer, and it is
	// never an input from the request. See the wiki route for where it comes
	// from; the short form is that a query parameter, a cookie or a header would
	// each be a way for a reader to ask for text they may not have.
	Redact(body string, includeSecrets bool) (string, error)
}

// OmitSecrets is the redactor P10 installs: it cuts every **collapsed**
// `[!secret]` callout out of the source, and only for a viewer who may not see
// one.
//
// # Only the collapsed state, and why the plan's wording is not the answer
//
// The delivery plan's row for this work item reads "the callout is removed
// entirely for non-GM viewers", and read alone that would cut `+` as well as
// `-`. It would also make the reveal endpoint (S6) a way to *lose* information:
// revealing rewrites the marker to `+`, and a reader who could not see a `+`
// callout could not see a revealed secret from any page, which is the entire
// point of revealing one.
//
// The two authoritative sources both say `-` specifically. S-5.6: "A
// `[!secret]-` body is **absent** from every non-GM response." §5.6: "`-` is a
// secret, collapsed and GM-only. `+` is revealed and public." So `-` is cut and
// `+` is left exactly as it was, and
// `TestARevealedCalloutIsPublicAndSurvivesForAPlayer` holds that with a mutation
// behind it — cutting both states fails it, so the choice cannot be made by
// accident later either.
//
// # Cut by byte span, once
//
// The spans come from `scanSecrets`, which has already decided what a callout is
// and where it ends — including that a `[!secret]` inside a fenced code block is
// text and not a secret. Re-parsing here would be a second grammar, and the two
// grammars would agree until a vault found the difference. So this file reads
// offsets and cuts bytes, and the one thing it must get right is that **the cuts
// do not move each other**.
//
// They do not, and the reason is two lines of `scanRange`: after a callout it sets
// `offset += consumed`, where `consumed` has advanced past the header line and
// every body line, and that is the same value it recorded as `BodyEnd`. So the walk
// resumes at the previous callout's `BodyEnd` and the next callout's `HeaderStart`
// is at or after it — which makes the spans **sorted**.
//
// Sorted is not the same as disjoint, and this file used to assume they were. They
// stopped being the same thing when `scanSecrets` learned to report a callout nested
// inside another: a nested span is contained in its parent's by construction, so
// "sorted and non-overlapping" became "sorted, and sometimes nested". The claim in
// this paragraph was true for as long as it was harmless and false the moment it
// mattered, and the two halves of the fix are the two halves of the sentence: the
// span list is filtered by `outermost`, and `spliceOut` no longer assumes.
//
// # Front matter is scanned too, and that is deliberate
//
// `ScanSecrets` walks lines and knows nothing about a `---` block, so a line that
// would be a callout in a body is a callout in front matter too, and it is cut for
// a player. Measured, on the shape that actually occurs — a YAML block scalar
// holding an example:
//
// ```yaml
// notes: |
//
//	> [!secret]-
//	> The traitor is Captain Aldric.
//
// ```
//
// The cut leaves `notes: |` with no block, which is still valid YAML, and the
// player sees a page a GM sees less of. That is the right direction for every
// reason: front matter is attacker-reachable (S-4.7), this redactor cannot tell a
// callout in a block scalar from a callout in a paragraph, and §5.6.2 requires every
// failure path in this subsystem to resolve toward hiding. The file on disk is not
// touched — redaction is per-response — so nothing is lost, only shown to fewer
// people.
//
// What is *not* cut is a marker inside a YAML quoted string, because
// `note: "> [!secret]- …"` does not start with `>` and so is not a callout by the
// grammar. That is correct rather than a hole: front matter never reaches the
// renderer (S-3.x's split) and `body_plain` strips it before the FTS index
// (ADR 0031), so such a line is never in a response to begin with.
// `TestACalloutShapedLineInFrontMatterIsCutToo` holds both halves.
//
// # The error is always nil, and the invariant that keeps it that way
//
// A cut is two integers into a string the redactor was just handed, and
// `scanSecrets` builds them from that same string, so there is nothing here that
// can fail: no I/O, no parse that can be rejected, no allocation whose failure is
// reportable. Returning an error the interface does not need is not sloppiness
// here, it is the reason the seam stays a seam — a caller has one failure path to
// handle, and it is the *renderer's*, not a redactor's.
//
// The corollary is the one thing worth writing down: **when this code does gain an
// error path, the error must not carry page content** (S-12.3). A Markdown or
// YAML parser quotes the line it choked on, and on a wiki page that line is
// routinely a callout body, so "log the offending text" is exactly the mistake.
// `TestAnErrorFromThisRedactorCarriesNoPageContent` is the standing assertion, and
// it is not vacuous: it was written by adding a `fmt.Errorf` carrying the source
// to the `includeSecrets` branch and watching it fail.
//
// # Nesting, and what it cost to get here right
//
// A callout nested inside a callout — `> > [!secret]-` inside an outer `+` — is
// **reported by `ScanSecrets` and cut here**. It was not, and a player received the
// inner body inside a `secret--collapsed` element. Two separate fixes, and it is
// worth keeping them apart because only the first one stops the disclosure:
//
//   - **The scanner reports it.** That alone is sufficient: the inner span then
//     exists, so `cutSpans` has something to remove, and a revealed outer no longer
//     means "nothing is cut from this callout".
//   - **The scanner forces the outer to collapsed.** That is about *bookkeeping*,
//     not about bytes. A nesting mistake must not be able to record itself as a
//     public reveal in the ledger, and §5.6.2 requires every failure path in this
//     subsystem to resolve toward hiding.
//
// Because the outer is forced collapsed, the outer span is cut and the inner one —
// contained in it — is dropped by `outermost`, so the bytes to remove are the outer
// callout's and the nesting is a single cut. `TestANestedCalloutIsRemovedFromA
// PlayerPage` asserts that at the response level, and
// `TestARevealedCalloutIsPublicAndSurvivesForAPlayer` is the control that a
// revealed callout whose body merely *mentions* the keyword is still public.
func OmitSecrets() Redactor {
	return omitSecrets{}
}

// omitSecrets is the Redactor above. A type rather than a package-level value so
// there is no mutable state for one to share, and so a caller cannot compare
// against it to learn which redactor is installed.
type omitSecrets struct{}

// Redact returns source with every collapsed `[!secret]` callout cut out of it,
// or source unchanged when includeSecrets is true.
//
// The flag is read exactly once and gates everything: with it set the callouts
// are left in place for the renderer's extension to render (`ext/secret.go` never
// decides whether to render one, so this is the only place the decision exists),
// and without it every collapsed callout is spliced out. Nothing else varies —
// no viewer identity, no clock, no counter — so two players asking for the same
// page get the same bytes, and the render cache has exactly two variants to hold
// (§5.5) rather than one per reader.
func (omitSecrets) Redact(source string, includeSecrets bool) (string, error) {
	if includeSecrets {
		return source, nil
	}

	spans := cutSpans(source)
	if len(spans) == 0 {
		return source, nil
	}

	return spliceOut(source, spans), nil
}

// secretSpan is one callout's bytes in the source, from the first byte of its
// header line to one past the last byte of its body.
//
// The **header is included** even though the header itself is not the secret, and
// that is the detail §5.6.1 settles rather than this file: a redaction that cut
// only the body would leave `> [!secret]-` in the source, which renders as a
// visible line of prose saying the page has a secret here — a stub, and the
// existence disclosure §5.6.1 rules out. It would also leave a dangling block
// quote for a renderer to fill with the surrounding text.
//
// `from` is the start of the *line*, leading indentation included, because
// `scanSecrets` reports `HeaderStart` as the line's first byte. That is what a
// callout inside a list item needs: cutting from the `>` instead would leave the
// item's indentation behind as an indented code line.
//
// # The span's end, and the blank line it leaves behind
//
// `to` is `BodyEnd`, the scanner's own value and measured: it sits one past the
// newline of the last quoted line. So a cut takes the header line, the body lines
// and their newlines — **and not the blank line that followed the callout**,
// because that newline is not the callout's byte and guessing at how many
// belonged to it is a guess about the author's spacing.
//
// Leaving it is measurably harmless, which is the only reason the answer is not
// "and also tidy up the whitespace". CommonMark treats one blank line and four
// identically for every block construct this renderer emits, and that was checked
// rather than assumed: `- one\n\n- two\n` and `- one\n\n\n\n- two\n` render to
// byte-identical HTML, as do the same pair around a heading, a block quote, an
// indented code line and a fenced block.
// `TestTheBlankLineACalloutLeftBehindChangesNothing` is the assertion.
//
// There was a version of this file that consumed the following blank lines, on
// the claim that it kept two lists apart. It was wrong, and wrong in the way this
// repository keeps getting things wrong: the claim was plausible, the mechanism was
// real, and the *cause* was not. A blank line between two list items already makes
// the list loose, so the third newline changed the rendered HTML not at all. The
// function is gone rather than kept "in case", because a note must not outlive the
// fact it describes.
type secretSpan struct {
	from, to int
}

// cutSpans returns the byte ranges to remove, in document order, skipping the
// revealed ones and keeping only the outermost of what is left.
//
// A slice of spans rather than an index into the source for the caller to loop
// over, because the loop that uses it is the only place offsets appear and it
// should be the shortest one possible.
//
// The outermost filter is `outermost`'s job and not an afterthought: it is what
// keeps this function's answer **sensible**, as opposed to merely safe.
func cutSpans(source string) []secretSpan {
	found := scanSecrets(source)

	spans := make([]secretSpan, 0, len(found))

	for _, secret := range found {
		if secret.State.IsRevealed() {
			continue
		}

		spans = append(spans, secretSpan{from: secret.HeaderStart, to: secret.BodyEnd})
	}

	return outermost(spans)
}

// outermost drops every span contained in another, and returns the rest sorted by
// position.
//
// # Containment, and why a nested secret needs no cut of its own
//
// A nested callout's span lies inside its parent's — `scanRange` bounds the nested
// walk to the parent's body, which is what makes that true — so cutting both would
// either double-cut (harmless) or, cut in the wrong order, cut a range that has
// already moved. It would also make the answer harder to read: `cutSpans` is asked
// for "the bytes to remove", and the bytes to remove for a page holding a nested
// secret are the outer callout's. Nothing else.
//
// So the property is named, not incidental: **`cutSpans` returns only the outermost
// spans**, and `TestTheOutermostSpansAreTheOnesThatGetCut` holds it, cross-checked
// against a brute-force containment test.
//
// # Why the sort, and why `to` is the tie-break
//
// Sorting by `from` alone is not enough, and the version this replaced was wrong in
// a way the table of hand-written cases did not have: given `[2,5)` then `[2,14)`,
// the first is contained in the second and must go, but a single forward pass that
// only looks *backwards* has already kept it by the time it learns otherwise. Two
// callouts cannot share a `from` — they are different lines — so the scanner cannot
// produce that input, and the first version of this function would have been correct
// on every real page and wrong on the definition it claimed to implement.
//
// Which is the argument for implementing the *definition* rather than the
// special case. Ordering by `from` ascending and `to` descending puts every
// container before everything it contains: a span starting earlier is already
// earlier, and one starting at the same byte is the wider one and so sorts first.
// With that order, "is this span contained in another" reduces to "does its `to` fall
// at or before the widest `to` seen so far", because any container is by then
// behind us.
//
// **That is the whole of the single pass**, and it is why the filter is O(n) rather
// than O(n²): `cutSpans` runs on every non-GM page render. The brute-force
// comparison in the test is not ceremony for it — it is the only thing that would
// notice the argument being wrong.
//
// One thing recorded because it looks like a simplification and is not a change:
// replacing `widest` with `kept[len(kept)-1].to` is **provably the same function**.
// Kept spans have strictly increasing `to` (that is what the filter enforces) and
// non-decreasing `from` (that is what the sort gives), so the last kept span carries
// the widest `to` seen. It was measured as a mutation and every test stayed green.
// `widest` is kept because it states the property directly rather than leaning on an
// invariant about `kept`, and because the next reader who does the substitution
// should find the reasoning already here.
func outermost(spans []secretSpan) []secretSpan {
	// `SortStableFunc` rather than a hand-rolled insertion sort: equal spans keep
	// their input order, so the first of two identical ones is the one kept, which
	// is not a distinction that matters but is one not worth having to reason about.
	ordered := make([]secretSpan, len(spans))
	copy(ordered, spans)

	slices.SortStableFunc(ordered, func(a, b secretSpan) int {
		if a.from != b.from {
			return a.from - b.from
		}

		return b.to - a.to
	})

	kept := make([]secretSpan, 0, len(ordered))

	// `widest` is the largest `to` among the spans already passed. Zero rather than
	// minus one because a span's `to` is always greater than its `from` and `from` is
	// never negative, so no real span can be contained in a zero-width one.
	widest := 0

	for _, span := range ordered {
		if span.to <= widest {
			continue
		}

		kept = append(kept, span)
		widest = span.to
	}

	return kept
}

// spliceOut removes every span from source and returns what is left.
//
// **One pass, copying the gaps.** The alternative — cutting one callout, writing
// the result back, and cutting the next — re-scans a string whose offsets no
// longer describe it, so the second cut lands on the wrong bytes; and splicing
// from the end backwards instead of collecting first is the same arithmetic with
// an extra loop, which is where an off-by-one hides. Building the result in one
// forward pass over sorted spans has almost no offset arithmetic at all: the only
// variable is where the last copy ended.
//
// # Total on overlapping input, on purpose
//
// A span that begins before `copied` is one whose bytes have already been removed,
// by an enclosing span. It is skipped, and `copied` never moves backwards. The
// naive version — copy the gap, then set `copied = span.to` — takes a slice with a
// negative length and panics, which is what this function used to do to a page
// holding a nested callout.
//
// **Skipping is chosen over panicking deliberately**, and it is the one place in
// this file where a defensive branch earns its keep. A panic here is a 500 for
// every reader of the page, which is a self-inflicted outage; a wrong splice is a
// page that renders mangled for players and correctly for the GM, which is a
// disclosure-shaped failure that fails *quietly*. Between those, the total function
// is the better failure and the containment filter in `cutSpans` is what makes the
// answer right rather than merely survivable. Both are tested, and
// `TestSpliceOutSurvivesOverlappingSpans` asserts that the two agree on nested
// input rather than assuming they do.
//
// `Grow` is an upper bound rather than a prediction. The result is never longer
// than the source, and the common case — a page with one collapsed callout and
// two hundred words of prose — is close enough to it that one guess beats a
// doubling chain.
func spliceOut(source string, spans []secretSpan) string {
	var out strings.Builder

	out.Grow(len(source))

	// `copied` is how far into the source the last copy ended, so the next copy
	// starts there. It is the only offset this function computes, and it is
	// monotonic because a backwards step would re-emit bytes already dropped.
	copied := 0

	for _, span := range spans {
		if span.from < copied {
			continue
		}

		out.WriteString(source[copied:span.from])
		copied = span.to
	}

	out.WriteString(source[copied:])

	return out.String()
}

// NoSecrets is the name this redactor shipped under while it removed nothing, and
// it is **kept only so the call sites outside this work item's paths keep
// compiling**. It delegates to `OmitSecrets` and behaves identically.
//
// This is a naming debt, not a second implementation: the phase that wrote the
// pass-through said in this file's own header that P10 replaces the body of
// `Redact` "and nothing else — the signature, the position and this file stay",
// which is what happened, and the call sites in `cmd/server/` that install a
// redactor were left calling the old name because `cmd/server/` belongs to the
// phase integrator. **Prefer `OmitSecrets` in new code**, and treat every
// remaining `NoSecrets()` call site as a rename waiting for its owner.
func NoSecrets() Redactor {
	return OmitSecrets()
}
