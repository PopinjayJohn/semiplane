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

import "strings"

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
// They do not, and the reason is two lines of `scanSecrets`: after a callout it
// sets `offset += consumed`, where `consumed` has advanced past the header line
// and every body line, and that is the same value it recorded as `BodyEnd`. So
// the scan resumes at the previous callout's `BodyEnd`, the next callout's
// `HeaderStart` is therefore at or after it, and the spans arrive **sorted and
// non-overlapping**. `spliceOut` may then walk them in order and copy each gap
// once, with no offset arithmetic of its own to get wrong — which is also why
// there is no defensive clamping in it. A clamp would guard a property the
// scanner already holds, and it would be code whose necessity is unreadable to
// anyone who has not read the scanner's loop.
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
// # One gap, measured rather than assumed
//
// A callout nested inside a callout — `> > [!secret]-` inside an outer `+` — is
// absorbed by the outer callout's body and is **not** reported by `ScanSecrets`,
// because the body loop consumes every line that is still quoted. So the inner
// `-` is not cut, and this is what a player receives for
// `> [!secret]+ Outer` / `> > [!secret]- Inner`:
//
// ```html
// <div class="secret secret--revealed" data-secret="revealed"><p>Outer.</p>
// <div class="secret secret--collapsed" data-secret="collapsed"><p>Inner.</p></div>
// </div>
// ```
//
// That is `secret.go`'s decision to own rather than this file's to paper over:
// `secret.go` states that a nested callout "is found by its own header and
// reported separately", so the intent is unambiguous and the code does not yet do
// it. Fixing it means teaching the scanner to look inside a callout it decided
// to keep, and a redactor that re-implemented enough Markdown to find it would
// be the second grammar this file's own comment above exists to avoid — and the
// blunt version of that hack, matching the marker as a substring, would delete
// every `[!secret]` a page merely *mentions*.
// `TestANestedCalloutIsTheOuterCalloutsToDecide` pins the measured behaviour so
// the fix is a visible diff, and it carries the defect into the report rather than
// leaving it in a comment.
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

// cutSpans returns the ranges to remove, in document order, skipping the
// revealed ones.
//
// A slice of spans rather than an index into the source for the caller to loop
// over, because the loop that uses it is the only place offsets appear and it
// should be the shortest one possible.
func cutSpans(source string) []secretSpan {
	found := scanSecrets(source)

	spans := make([]secretSpan, 0, len(found))

	for _, secret := range found {
		if secret.State.IsRevealed() {
			continue
		}

		spans = append(spans, secretSpan{from: secret.HeaderStart, to: secret.BodyEnd})
	}

	return spans
}

// spliceOut removes every span from source and returns what is left.
//
// **One pass, copying the gaps.** The alternative — cutting one callout, writing
// the result back, and cutting the next — re-scans a string whose offsets no
// longer describe it, so the second cut lands on the wrong bytes; and splicing
// from the end backwards instead of collecting first is the same arithmetic with
// an extra loop, which is where an off-by-one hides. Building the result in one
// forward pass over sorted, non-overlapping spans has no offset arithmetic at
// all: the only variable is where the last copy ended.
//
// `Grow` is an upper bound rather than a prediction. The result is never longer
// than the source, and the common case — a page with one collapsed callout and
// two hundred words of prose — is close enough to it that one guess beats a
// doubling chain.
func spliceOut(source string, spans []secretSpan) string {
	var out strings.Builder

	out.Grow(len(source))

	// `copied` is how far into the source the last copy ended, so the next copy
	// starts there. It is the only offset this function computes.
	copied := 0

	for _, span := range spans {
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
