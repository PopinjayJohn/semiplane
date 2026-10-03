package content

// `outermost` and `spliceOut`, directly.
//
// # Why this file is `package content`
//
// `outermost` and `spliceOut` are unexported, and both carry properties that cannot
// be observed from outside. `outermost`'s is *which* spans come back; `spliceOut`'s
// is that it survives input its own caller is supposed never to build. Testing either
// through `Redact` would assert on output that has already been filtered, so a bug in
// the filter would be invisible — the thing a filter exists to prevent.
//
// This is the same trade `export_test.go` makes and for the same reason, and the
// name is chosen so the `testpackage` linter's own `internal_test.go` exemption
// covers it rather than being argued past with a `//nolint`. The tests that describe
// the package's *contract* stay in `package content_test`; this one describes how two
// functions are built. Go runs both in one binary, so it costs one compile.
//
// It is also the only file here that tests a *property of a property-holding
// function*: `TestOutmostAgreesWithBruteForce` runs the filter against an O(n²)
// containment check over generated span lists, which is the only thing that would
// notice the single-comparison argument in `outermost`'s comment stopping being
// true. It has already caught that argument being wrong twice.

import (
	"slices"
	"strings"
	"testing"
)

// TestTheOutermostSpansAreTheOnesThatGetCut is the property `cutSpans` rests on:
// a nested callout contributes no cut of its own, because its bytes are already
// inside its parent's.
//
// **Both directions, plus the identity case.** A filter that dropped everything
// would pass "a contained span is dropped"; one that dropped nothing would pass "the
// outer span is kept". So each fixture states both what survives and what does not,
// and one of them is a pair of spans that merely touch — which is *not* containment
// and must survive, because a callout that begins exactly where the previous one
// ended is two callouts and not one.
func TestTheOutermostSpansAreTheOnesThatGetCut(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		spans []secretSpan
		want  []secretSpan
	}{
		"nothing at all": {
			spans: nil,
			want:  []secretSpan{},
		},
		"one span survives itself": {
			spans: []secretSpan{{from: 4, to: 20}},
			want:  []secretSpan{{from: 4, to: 20}},
		},
		"two siblings both survive": {
			spans: []secretSpan{{from: 0, to: 20}, {from: 30, to: 50}},
			want:  []secretSpan{{from: 0, to: 20}, {from: 30, to: 50}},
		},
		"a nested span is dropped": {
			spans: []secretSpan{{from: 0, to: 100}, {from: 10, to: 40}},
			want:  []secretSpan{{from: 0, to: 100}},
		},
		"a span identical to the outer is dropped": {
			spans: []secretSpan{{from: 5, to: 50}, {from: 5, to: 50}},
			want:  []secretSpan{{from: 5, to: 50}},
		},
		"a span sharing the outer's start is dropped": {
			spans: []secretSpan{{from: 5, to: 50}, {from: 5, to: 20}},
			want:  []secretSpan{{from: 5, to: 50}},
		},
		"a span sharing the outer's end is dropped": {
			spans: []secretSpan{{from: 5, to: 50}, {from: 30, to: 50}},
			want:  []secretSpan{{from: 5, to: 50}},
		},
		"two levels of nesting": {
			spans: []secretSpan{{from: 0, to: 100}, {from: 10, to: 80}, {from: 20, to: 30}},
			want:  []secretSpan{{from: 0, to: 100}},
		},
		"a nested pair inside an outer": {
			spans: []secretSpan{
				{from: 0, to: 20},
				{from: 30, to: 200},
				{from: 40, to: 90},
				{from: 100, to: 150},
			},
			want: []secretSpan{{from: 0, to: 20}, {from: 30, to: 200}},
		},
		"two spans sharing a start keep the wider one": {
			// **The case the single-pass version got wrong.** `[2,5)` is contained in
			// `[2,14)`, and a filter that only looks backwards has already kept the
			// first by the time the second turns up. `scanSecrets` cannot produce two
			// callouts at the same byte — they are different lines — so this is a shape
			// the page can never present, and it is here because the filter
			// implements the *definition* rather than the scanner's current habit.
			spans: []secretSpan{{from: 2, to: 5}, {from: 2, to: 14}},
			want:  []secretSpan{{from: 2, to: 14}},
		},
		"unsorted input is sorted before filtering": {
			spans: []secretSpan{{from: 30, to: 40}, {from: 0, to: 100}, {from: 10, to: 20}},
			want:  []secretSpan{{from: 0, to: 100}},
		},
		"a span that merely touches the previous one survives": {
			// Adjacent, not contained: the second begins exactly where the first
			// ended, which is two callouts with no prose between them.
			spans: []secretSpan{{from: 0, to: 20}, {from: 20, to: 40}},
			want:  []secretSpan{{from: 0, to: 20}, {from: 20, to: 40}},
		},
		"a span that starts inside and ends outside survives": {
			// Overlapping without being contained. `scanRange` cannot produce one,
			// and the filter does not have to cope — but it must not silently drop
			// it either, because dropping it would leave bytes in the response.
			spans: []secretSpan{{from: 0, to: 20}, {from: 10, to: 40}},
			want:  []secretSpan{{from: 0, to: 20}, {from: 10, to: 40}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := outermost(fixture.spans)

			if len(got) != len(fixture.want) {
				t.Fatalf("outermost(%v) = %v, want %v", fixture.spans, got, fixture.want)
			}

			for index := range got {
				if got[index] != fixture.want[index] {
					t.Errorf("span %d = %v, want %v (all: got %v, want %v)",
						index, got[index], fixture.want[index], got, fixture.want)
				}
			}
		})
	}
}

// TestOutermostAgreesWithBruteForce checks the single-pass filter against the
// definition it claims to implement, over a generated corpus.
//
// **A property test rather than a table**, because `outermost`'s whole argument is
// one comparison instead of a loop over all kept spans. A table of hand-written
// cases can only cover the nestings its author imagined, and the argument is only
// wrong for a shape nobody imagined — which is the entire risk of an optimisation
// with a proof attached to it.
//
// The reference implementation is the definition, spelled out: a span is dropped
// when some other span contains it. Everything else is generated, so a disagreement
// is a bug in the fast path or in the proof, and either way it is found here rather
// than on a page.
func TestOutermostAgreesWithBruteForce(t *testing.T) {
	t.Parallel()

	// A deterministic pseudo-random walk, because `crypto/rand` is banned in this
	// package's rule code and a test that wanted one would be the same smell.
	seed := uint32(0x5EED)

	next := func(bound int) int {
		seed = seed*1664525 + 1013904223

		return int(seed>>8) % bound
	}

	for round := range 500 {
		count := 1 + next(7)
		spans := make([]secretSpan, 0, count)

		for range count {
			from := next(60)
			spans = append(spans, secretSpan{from: from, to: from + next(40)})
		}

		// **Unsorted on purpose.** `scanSecrets` hands back spans ordered by `from`,
		// but `outermost` sorts anyway, so the corpus must arrive out of order for
		// that sort to be tested. A pre-sorted corpus would let a regression in it
		// through — which is what the first version of this function relied on being
		// unnecessary, and the equal-`from` case in the table above is what found it.
		slices.Reverse(spans)

		got := outermost(spans)
		want := bruteForceOutmost(spans)

		if len(got) != len(want) {
			t.Fatalf("round %d: outermost(%v) = %v, brute force says %v",
				round, spans, got, want)
		}

		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("round %d: outermost(%v) = %v, brute force says %v",
					round, spans, got, want)
			}
		}
	}
}

// bruteForceOutmost is the definition `outermost` claims to implement, spelled out
// rather than optimised, in the two steps the definition actually has:
//
//  1. **Distinct spans.** A set of ranges does not repeat itself, and two identical
//     spans remove the same bytes.
//  2. **Not contained in another.** A span is dropped when a *different* span covers
//     it.
//
// The second step is why step one has to come first, and the corpus found it: given
// two identical spans, each is contained in the other, so a containment test that
// ignores equality drops **both** — and with them the bytes they were there to
// remove. The single-pass version collapses them to one, which is what a set means.
// The disagreement was in the reference, not in the code under test, which is a
// reason to keep a reference implementation around rather than reason harder about
// the fast one.
func bruteForceOutmost(spans []secretSpan) []secretSpan {
	distinct := make([]secretSpan, 0, len(spans))

	for _, span := range spans {
		if !slices.ContainsFunc(distinct, func(other secretSpan) bool { return other == span }) {
			distinct = append(distinct, span)
		}
	}

	kept := make([]secretSpan, 0, len(distinct))

	for index, span := range distinct {
		contained := false

		for other, candidate := range distinct {
			if other != index && contains(candidate, span) {
				contained = true

				break
			}
		}

		if !contained {
			kept = append(kept, span)
		}
	}

	sortedByFrom(kept)

	return kept
}

// sortedByFrom orders by `from` only, so the reference implementation's own output
// ordering is independent of whatever tie-break `outermost` chose.
func sortedByFrom(spans []secretSpan) {
	slices.SortStableFunc(spans, func(a, b secretSpan) int { return a.from - b.from })
}

// contains reports whether outer covers inner.
//
// **Equal spans count as containment**, which is what makes two identical spans
// collapse to one. There is no byte-level tie to break between them: they remove
// the same bytes, and either of them cut is the same output.
func contains(outer, inner secretSpan) bool {
	return outer.from <= inner.from && inner.to <= outer.to
}

// TestCutSpansCallsTheFilter is the one assertion that the filter is *wired up*.
//
// Without it, deleting `outermost(spans)` from `cutSpans` changes nothing an
// end-to-end test can see: `spliceOut` skips contained spans itself, so the page
// comes out identical. The filter is not redundant — it is what makes `cutSpans`
// return the *outermost* set rather than the whole set, and that is a property of
// the function's answer, not of the response. A mutation removing the call site was
// run against the black-box tests first and went green; this is what catches it.
//
// **Two nesting shapes and a sibling**, because a filter that kept only the first
// span would pass a one-nest fixture.
func TestCutSpansCallsTheFilter(t *testing.T) {
	t.Parallel()

	const nested = "> [!secret]+ Outer.\n> > [!secret]- Inner.\n\nAfter.\n"

	got := cutSpans(nested)

	if len(got) != 1 {
		t.Fatalf("cutSpans returned %d span(s), want 1: the nested callout's span is "+
			"contained in its parent's, so the bytes to remove are the outer's:\n%v",
			len(got), got)
	}

	// The callout ends one past the newline of its last quoted line, which is the
	// first byte of the blank line that follows it.
	if want := (secretSpan{from: 0, to: strings.Index(nested, "\n\n") + 1}); got[0] != want {
		t.Errorf("cutSpans()[0] = %v, want the whole outer callout %v", got[0], want)
	}

	// Two nests on one page: one cut each, and neither nest may swallow the other.
	const two = "> [!secret]+ One.\n> > [!secret]- Inner one.\n\n" +
		"> [!secret]+ Two.\n> > [!secret]- Inner two.\n\nAfter.\n"

	gotTwo := cutSpans(two)
	if len(gotTwo) != 2 {
		t.Fatalf("cutSpans returned %d span(s) for two nests, want 2:\n%v", len(gotTwo), gotTwo)
	}

	first := secretSpan{from: 0, to: strings.Index(two, "\n\n") + 1}
	if gotTwo[0] != first {
		t.Errorf("the first cut is %v, want the first callout's own bytes %v", gotTwo[0], first)
	}

	second := secretSpan{
		from: first.to + len("\n"),
		to:   strings.Index(two, "After.") - len("\n"),
	}
	if gotTwo[1] != second {
		t.Errorf("the second cut is %v, want %v — the second callout's own bytes, "+
			"not the rest of the page", gotTwo[1], second)
	}
}

// TestSpliceOutSurvivesOverlappingSpans is the total-ness, held directly.
//
// The input here is **not** what `cutSpans` produces — it is what `cutSpans` would
// produce with `outermost` removed, and it is the input that used to panic with
// `slice bounds out of range [:10] with length 40`. `defer recover` is the
// assertion, because a panic is the failure and there is nothing else to inspect:
//
// A function that cannot fail is not a gate — a panic is the gate, and it fires on
// the first request rather than in this file.
func TestSpliceOutSurvivesOverlappingSpans(t *testing.T) {
	t.Parallel()

	const source = "0123456789ABCDEFGHIJ"

	// `source` is two labelled halves so the expected values can be read off the
	// index arithmetic rather than counted by eye: `0123456789` occupies 0..9 and
	// `ABCDEFGHIJ` occupies 10..19.
	for name, fixture := range map[string]struct {
		spans []secretSpan
		want  string
	}{
		"an inner span is ignored": {
			// Removing [0,10) already removed [4,6), so the answer is the same as if
			// the second span had never been handed over.
			spans: []secretSpan{{from: 0, to: 10}, {from: 4, to: 6}},
			want:  "ABCDEFGHIJ",
		},
		"an identical span is ignored": {
			// Removing [3,8) leaves "012" then "89ABCDEFGHIJ".
			spans: []secretSpan{{from: 3, to: 8}, {from: 3, to: 8}},
			want:  "01289ABCDEFGHIJ",
		},
		"a span that would move backwards is ignored": {
			spans: []secretSpan{{from: 0, to: 12}, {from: 2, to: 5}},
			want:  "CDEFGHIJ",
		},
		"two nested spans inside one outer": {
			spans: []secretSpan{{from: 0, to: 20}, {from: 2, to: 6}, {from: 8, to: 12}},
			want:  "",
		},
		"a filtered list and an unfiltered one agree": {
			// The claim the two functions make together, asserted rather than
			// assumed: dropping the contained spans up front and skipping them in
			// `spliceOut` are the same answer. [0,10) and [2,4) go, leaving
			// "0123456789" + "AB" and then [12,14) removes "CD".
			spans: []secretSpan{{from: 0, to: 10}, {from: 2, to: 4}, {from: 12, to: 14}},
			want:  "ABEFGHIJ",
		},
		"two outers with a nested span between them": {
			// [0,4) removes "0123", [5,15) removes "56789ABCDE", and the
			// contained [6,8) is already gone. What is left is index 4 and 15..19.
			spans: []secretSpan{{from: 0, to: 4}, {from: 5, to: 15}, {from: 6, to: 8}},
			want:  "4FGHIJ",
		},
		"no spans copies everything": {
			spans: nil,
			want:  source,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("spliceOut panicked on %v: %v", fixture.spans, recovered)
				}
			}()

			got := spliceOut(source, fixture.spans)

			if got != fixture.want {
				t.Errorf("spliceOut(%v) = %q, want %q", fixture.spans, got, fixture.want)
			}

			// And the two paths agree, which is what makes the skip in `spliceOut` a
			// second line of defence rather than a second answer.
			if filtered := spliceOut(source, outermost(fixture.spans)); filtered != got {
				t.Errorf("spliceOut disagrees with itself: filtered %q, unfiltered %q",
					filtered, got)
			}
		})
	}
}

// TestSpliceOutNeverReEmitsRemovedBytes is the property that makes the skip
// correct rather than merely non-panicking: `copied` is monotonic, so a span that
// was already cut cannot drag the writer back into removed bytes.
//
// **Asserted by removing a span and checking the removed text is absent**, over
// every nesting shape the scanner can produce from a real page, because the
// interesting failures are the ones where the writer moves backwards and re-emits a
// fragment — which a length comparison would not notice and a reader would.
func TestSpliceOutNeverReEmitsRemovedBytes(t *testing.T) {
	t.Parallel()

	// The three nesting shapes: a nested `-` in a `+`, a nested `+` in a `-`, and a
	// nested callout in a nested callout.
	for name, source := range map[string]string{
		"one level": "> [!secret]+ Outer.\n> > [!secret]- SECRET.\n",
		"two levels": "> [!secret]+ Outer.\n" +
			"> > [!secret]- Middle.\n" +
			"> > > [!secret]- SECRET.\n",
		"a sibling after the nest": "> [!secret]+ Outer.\n" +
			"> > [!secret]- SECRET.\n" +
			"\n" +
			"> [!secret]- Also SECRET.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spans := cutSpans(source)

			// Every secret in the source has to fall inside a cut, which is the
			// assertion that says the *outermost* span covers it — the earlier
			// version of this test checked the wrong direction and passed on a
			// `cutSpans` that returned the span *containing* the secret, which is
			// the whole point.
			for offset := 0; offset < len(source); {
				line := source[offset:]
				if stop := strings.IndexByte(line, '\n'); stop >= 0 {
					line = line[:stop+1]
				}

				if !strings.Contains(line, "SECRET") {
					offset += len(line)

					continue
				}

				covered := false

				for _, span := range spans {
					if span.from <= offset && offset < span.to {
						covered = true

						break
					}
				}

				if !covered {
					t.Errorf("no cut covers the secret on the line at offset %d; cuts %v "+
						"of %q", offset, spans, source)
				}

				offset += len(line)
			}

			got := spliceOut(source, spans)

			if strings.Contains(got, "SECRET") {
				t.Errorf("the secret survived:\n%s\ncuts: %v", got, spans)
			}

			if strings.Contains(got, "[!secret]") {
				t.Errorf("a marker survived a cut that should have been outermost:\n%s", got)
			}
		})
	}
}
