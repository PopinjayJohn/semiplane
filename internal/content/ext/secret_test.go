package ext_test

// The `[!secret]` callout, rendered.
//
// # Why the tests are about what is *absent*
//
// The security property this feature exists for is §5.6.1's: the secret body is
// absent from a non-GM's response. That is enforced upstream, on the source, by the
// redactor — nothing in this file can enforce it, and these tests do not pretend
// to. What they *can* hold is the property that makes the upstream enforcement
// sufficient: **by the time this package sees a document, it is already correct**.
//
// So the load-bearing assertions are the negative ones. `TestTheMarkerIsNeverInTheOutput`
// is the one that matters, and it exists because the first version of this
// transform printed the marker: the header and the body are one CommonMark
// paragraph, the marker is cut out of it, and the cut was attempted on the
// paragraph rather than on a byte range — which removed the marker *and the body*,
// leaving an empty div. A test asserting the callout's element and its `class`
// passed throughout that.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
)

// render runs source through the pipeline the composition root installs.
func render(t *testing.T, source string) string {
	t.Helper()

	markdown := goldmark.New(goldmark.WithExtensions(ext.New(ext.Builtins()...)))

	var out bytes.Buffer
	if err := markdown.Convert([]byte(source), &out); err != nil {
		t.Fatalf("Convert() error = %v, want nil", err)
	}

	return out.String()
}

// renderBare runs source through goldmark with **no** semiplane extension at all.
//
// The comparison against this is what makes the block quote shadowing in
// `secret.go` safe: this package registers a renderer for `ast.KindBlockquote`, so
// every block quote on every page passes through it, and the only thing that makes
// that acceptable is that a page with no callout comes out identical to a page
// rendered without this package.
func renderBare(t *testing.T, source string) string {
	t.Helper()

	var out bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &out); err != nil {
		t.Fatalf("Convert() error = %v, want nil", err)
	}

	return out.String()
}

func TestACollapsedCalloutRendersAsCollapsed(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]-\n> The traitor is Captain Aldric.\n")

	want := `<div class="secret secret--collapsed" data-ext="secret" ` +
		`data-secret="collapsed"><p>The traitor is Captain Aldric.</p>`

	if !strings.Contains(got, want) {
		t.Errorf("rendered:\n%s\nwant it to contain:\n%s", got, want)
	}
}

func TestARevealedCalloutRendersAsRevealed(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]+\n> He replaced the eastern signal fire.\n")

	want := `<div class="secret secret--revealed" data-ext="secret" ` +
		`data-secret="revealed">`

	if !strings.Contains(got, want) {
		t.Errorf("rendered:\n%s\nwant it to contain:\n%s", got, want)
	}
}

// TestTheMarkerIsNeverInTheOutput is the test this file is for.
//
// §5.6's marker is a byte of *source syntax*, and a renderer that leaves it in the
// output has told the reader something the callout's element already says — and
// worse, has told a player it too, if the redaction path ever regressed and a
// collapsed callout reached a non-GM's page with its marker intact.
func TestTheMarkerIsNeverInTheOutput(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"collapsed, no title": "> [!secret]-\n> Body.\n",
		"revealed, no title":  "> [!secret]+\n> Body.\n",
		"with a title":        "> [!secret]- Aldric\n> Body.\n",
		"with a block id":     "> [!secret]- Aldric  ^traitor\n> Body.\n",
		"nested in prose":     "Before.\n\n> [!secret]-\n> Body.\n\nAfter.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := render(t, source); strings.Contains(got, "[!secret]") {
				t.Errorf("rendered:\n%s\nthe marker reached the output. It is a byte "+
					"of source syntax and the element already says what it is", got)
			}
		})
	}
}

// TestTheBodySurvivesTheCut is the other half, and it is here because the first
// version of this transform failed it.
//
// §5.6's own example has no blank line between the marker and the body, so
// CommonMark makes them one paragraph: `[` splits the marker across four inline
// nodes, and the body is the rest of the same paragraph. Removing the header
// paragraph removed the body with it, and the page rendered an empty div — a
// secret callout that shows a GM nothing at all, which is the kind of bug that
// looks like the feature working.
func TestTheBodySurvivesTheCut(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]-\n> The traitor is Captain Aldric.\n"+
		"> He replaced the eastern signal fire.\n")

	for _, want := range []string{
		"The traitor is Captain Aldric.",
		"He replaced the eastern signal fire.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered:\n%s\nwant it to contain %q. The header and the body "+
				"are one CommonMark paragraph, so cutting the header must not take "+
				"the body with it", got, want)
		}
	}

	// And the body\'s **own** paragraph break survives, which is the other half:
	// a rule that clipped by position rather than by line would join the body\'s
	// paragraphs into one run of text, and a secret written as two paragraphs would
	// arrive as one.
	multi := render(t, "> [!secret]-\n> First paragraph.\n>\n> Second paragraph.\n")

	for _, want := range []string{"First paragraph.", "Second paragraph."} {
		if !strings.Contains(multi, want) {
			t.Errorf("rendered:\n%s\nwant it to contain %q", multi, want)
		}
	}

	if strings.Contains(multi, "First paragraph.Second paragraph.") {
		t.Errorf("rendered:\n%s\nthe body's paragraph break was eaten", multi)
	}
}

// TestTheBlockIDIsNotRenderedAsText: `^traitor` in the output would be an anchor
// that resolves for nobody and reads to a GM as a typo. The scanner strips it from
// the source; the renderer strips it from the tree.
func TestTheBlockIDIsNotRenderedAsText(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]+ The traitor is Aldric  ^traitor\n> The fire.\n")

	if strings.Contains(got, "^traitor") || strings.Contains(got, "^") {
		t.Errorf("rendered:\n%s\nthe block id reached the output as text", got)
	}

	// **The title, terminated by the line break and nothing else.**
	//
	// Asserting the bare title string is the weaker test and it is the one that let
	// a mutation through: cutting at the caret's offset rather than before the
	// padding leaves a trailing space, which a `Contains` on the words cannot see.
	// The space is invisible in a preview and obvious in a diff against Obsidian's
	// own rendering of the same vault.
	if !strings.Contains(got, "The traitor is Aldric\n") {
		t.Errorf("rendered:\n%s\nwant the title with its block id and the padding "+
			"before it cut, and nothing else left over", got)
	}
}

// TestANonIDTrailingCaretStaysInTheTitle is the character rule, on this side.
//
// Dropping `isBlockID` from the block-id cut leaves every other assertion in this
// file green, because every fixture here uses a caret followed by a real id. What
// it then does is cut at the caret regardless, so `^https://example.invalid/x`
// loses its `^https` and the title reads `See //example.invalid/x` -- the renderer
// quietly editing an author's sentence. The scanner holds the same rule and its own
// test; this is the half that would otherwise be untested.
func TestANonIDTrailingCaretStaysInTheTitle(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct{ line, title string }{
		"a url fragment": {"> [!secret]+ See ^https://example.invalid/x\n", "See ^https://example.invalid/x"},
		"punctuation":    {"> [!secret]+ Aldric ^traitor.\n", "Aldric ^traitor."},
		"bare caret":     {"> [!secret]+ Aldric ^\n", "Aldric ^"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := render(t, fixture.line+"> Body.\n")

			// The title **in full**, caret and all. Asserting the absence of `^`
			// instead would be the weaker and wrong test: `^` is ordinary text in
			// HTML, and a title legitimately contains one.
			if !strings.Contains(got, fixture.title) {
				t.Errorf("rendered:\n%s\nwant the title %q intact. What follows the "+
					"caret is not an id, so the caret is part of the title and "+
					"cutting at it edits the author's sentence", got, fixture.title)
			}
		})
	}
}

// TestTheTitleKeepsItsOwnMarkdown holds that the cut is a byte range and not a
// re-render. If the title were re-inserted as a plain string it would come out
// `<strong>`-free, and a callout titled `**Aldric**` would show its own asterisks.
func TestTheTitleKeepsItsOwnMarkdown(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]- **Aldric** and the fire\n> Body.\n")

	if !strings.Contains(got, "<strong>Aldric</strong>") {
		t.Errorf("rendered:\n%s\nwant the title's emphasis preserved. The cut is a "+
			"byte range over the nodes goldmark already parsed, so the title's "+
			"formatting is the author's and not this package's", got)
	}
}

// TestAnOrdinaryBlockQuoteIsByteIdentical is what makes the shadowing safe.
//
// `secret.go` registers a renderer for `ast.KindBlockquote`, which means every
// block quote in every vault passes through code this package wrote. The only
// acceptable price is that a page with no callout in it comes out exactly as
// goldmark alone would produce — not approximately, not on a code path someone
// believes is equivalent, but the same bytes.
func TestAnOrdinaryBlockQuoteIsByteIdentical(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"one quote":     "> just a quote\n> with prose\n",
		"two quotes":    "> First.\n\n> Second.\n",
		"nested quotes": "> outer\n> > inner\n",
		"a list":        "> - one\n> - two\n",
		"prose only":    "# Chapter\n\nSome text.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got, want := render(t, source), renderBare(t, source); got != want {
				t.Errorf("a page with no secret callout rendered differently:\n got %q\nwant %q",
					got, want)
			}
		})
	}
}

// TestAPlainQuoteBesideACalloutIsStillABlockQuote is the half of the shadowing
// question the byte-identity test cannot ask, because its fixture has a callout in
// it. A page that mixes the two is the ordinary case — a GM quoting a rule and then
// hiding the thing that breaks it — so "the callout changed" is not enough; the
// quote on either side of it has to survive as a quote.
func TestAPlainQuoteBesideACalloutIsStillABlockQuote(t *testing.T) {
	t.Parallel()

	got := render(t, "> A rule everyone knows.\n\n> [!secret]-\n> Body.\n\n"+
		"> Another rule.\n")

	if count := strings.Count(got, "<blockquote>"); count != 2 {
		t.Errorf("rendered:\n%s\nfound %d blockquote elements, want 2. The renderer "+
			"is registered for every block quote on the page, so the ones this "+
			"transform did not mark have to come out as goldmark writes them",
			got, count)
	}

	if count := strings.Count(got, `class="secret`); count != 1 {
		t.Errorf("rendered:\n%s\nfound %d callouts, want 1", got, count)
	}

	for _, want := range []string{"A rule everyone knows.", "Another rule."} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered:\n%s\nwant %q to survive", got, want)
		}
	}
}

// TestTheNonCalloutFormsAreProse covers what §5.6's closed grammar refuses, and
// the claim is that refusing is **inert**, not fatal: a typo in a marker renders as
// the prose it is, rather than as a callout carrying a body or as an error.
func TestTheNonCalloutFormsAreProse(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"no marker":        "> [!secret]\n> Aldric.\n",
		"a third marker":   "> [!secret]*\n> Aldric.\n",
		"not at the start": "> The text says > [!secret]- here.\n",
		"not a quote":      "[!secret]-\nAldric.\n",
		"a different type": "> [!note]-\n> Just a note.\n",
		"indented in code": "    > [!secret]-\n    > Aldric.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := render(t, source)

			if strings.Contains(got, `class="secret`) {
				t.Errorf("rendered:\n%s\nthis is not a callout and rendered as one", got)
			}

			if got != renderBare(t, source) {
				t.Errorf("rendered:\n%s\nwant goldmark's own output for a page that "+
					"contains no callout at all", got)
			}
		})
	}
}

// TestACalloutInsideAFenceIsText: goldmark does not build a block quote out of a
// fenced block's contents, so this is really a test that installing this transform
// does not change goldmark's handling of code — and that the scanner's fence rule
// and this transform agree about where a fence is.
func TestACalloutInsideAFenceIsText(t *testing.T) {
	t.Parallel()

	source := "```markdown\n> [!secret]-\n> Aldric.\n```\n"

	got := render(t, source)
	if strings.Contains(got, `class="secret`) {
		t.Errorf("rendered:\n%s\na fenced block's contents are text", got)
	}

	if got != renderBare(t, source) {
		t.Errorf("rendered:\n%s\nwant goldmark's own output", got)
	}
}

// TestTheTitleCannotInjectMarkup is the security claim about the title, and it is
// worth stating precisely because the title *is* author text reaching the renderer.
//
// It reaches it as a text node over the author's own bytes, so goldmark's text
// renderer escapes it exactly once — the same path any other paragraph takes. The
// alternative this refuses is holding the title in an attribute and writing it out
// of the block renderer, which is a second escaping site to get wrong.
func TestTheTitleCannotInjectMarkup(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]- <img src=x onerror=alert(1)> & \"quoted\"\n> Body.\n")

	for _, forbidden := range []string{"<img", "onerror="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("rendered:\n%s\nthe title contributed %q to the output. It is "+
				"author text and must be escaped exactly once, as any other "+
				"paragraph is", got, forbidden)
		}
	}

	// The title's own markup is **omitted, not escaped**, and that is goldmark's
	// behaviour and not something this package chose: with unsafe HTML off, raw HTML
	// in a document never reaches the output in any form. Asserting `&lt;img` here
	// would be asserting a weaker property than the pipeline actually has.
	if !strings.Contains(got, "raw HTML omitted") {
		t.Errorf("rendered:\n%s\nwant the title's raw HTML dropped rather than "+
			"passed through", got)
	}

	// And the characters that *are* text are escaped rather than dropped, which is
	// the difference between sanitising and deleting.
	for _, want := range []string{"&amp;", "&quot;"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered:\n%s\nwant %q in the output: the title is text and "+
				"must be escaped, not removed", got, want)
		}
	}
}

// TestTheTwoSpellingsAgree is the only thing holding the two copies of the grammar
// in step.
//
// `internal/content`'s source scanner and this transform each carry their own
// `calloutPrefix`, their own `isBlockID` and their own idea of where a marker ends,
// because `internal/content` imports this package and sharing a constant would mean
// an import cycle. That is the weaker guarantee, and the alternative -- a cycle
// between the parser and the thing it parses -- is worse. So they are held together
// by a test, and this is that test: for a document of callouts, the states this
// transform renders and the states the scanner reports must be the same, in the
// same order, the same number of times.
//
// If they ever drift, the symptom is not a test failure first. It is a GM whose page
// shows two callouts as one state and the ledger showing the other.
func TestTheTwoSpellingsAgree(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> First body.\n\n" +
		"> [!secret]+ Second  ^two\n> Second body.\n\n" +
		"> [!secret]- Third\n\n" +
		"prose\n\n" +
		"> [!note]-\n> not a secret\n\n" +
		"> [!secret]*\n> not a secret either\n"

	scanned := content.ScanSecrets(source)
	if len(scanned) != 3 {
		t.Fatalf("ScanSecrets() found %d callouts, want 3", len(scanned))
	}

	got := render(t, source)

	seen := 0

	for _, want := range scanned {
		state := "collapsed"
		if want.State.IsRevealed() {
			state = "revealed"
		}

		if !strings.Contains(got, `data-secret="`+state+`"`) {
			t.Errorf("the scanner reports callout %d as %s, and the renderer "+
				"produced no %s element:\n%s", want.Ordinal, want.State, state, got)
		}

		seen++
	}

	if seen == 0 {
		t.Fatalf("the loop ran zero times; the fixture is not reaching the "+
			"assertion it was written for:\n%s", source)
	}

	// And exactly as many as the scanner found, so a form the scanner accepts and
	// the transform rejects shows up as a count mismatch rather than as a pair of
	// coincidentally-equal states.
	if count := strings.Count(got, `<div class="secret`); count != len(scanned) {
		t.Errorf("the renderer produced %d callouts, the scanner found %d:\n%s",
			count, len(scanned), got)
	}
}

// TestACalloutIsADivNotABlockquote exists because the element choice is a claim:
// §5.6.1's omission means the element carries no information a player can act on, so
// there is nothing to gain from a semantic element, and `div` is what the policy
// allowlist names.
func TestACalloutIsADivNotABlockquote(t *testing.T) {
	t.Parallel()

	got := render(t, "> [!secret]-\n> Body.\n")

	if strings.Contains(got, "<blockquote>") {
		t.Errorf("rendered:\n%s\na callout is a div; a blockquote would give it a "+
			"semantic meaning the feature deliberately does not have", got)
	}
}
