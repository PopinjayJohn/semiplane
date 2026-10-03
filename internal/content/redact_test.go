package content_test

// The redactor: what a player is not shown, and how it is not shown.
//
// # Why nearly every assertion here is about *absence of bytes*
//
// §5.6.1 rules out four ways of hiding that all keep the text in the response —
// `display:none`, a `hidden` attribute, an HTML comment, a class — and adds that
// the callout goes entirely rather than leaving a stub. Every one of those ships
// a **200** with a page that looks right in a browser and is recoverable with
// curl. So the assertions are not "the callout is not visible"; they are "this
// string of the author's text is not present in any byte, node or attribute of
// what the pipeline produced".
//
// And the other half, which is the half a redaction test usually forgets: the
// prose **around** the callout must still be there. A redactor that cut too much
// satisfies every absence assertion in this file, and the bug it produces —
// players losing the sentence either side of a secret — is one nobody notices
// because nothing looks broken.
//
// # Why the DOM is parsed rather than the markup scanned
//
// The defect class this repository keeps meeting is an assertion that reads a word
// near a construct: `strings.Contains(html, "secret")` passes for
// `<span class="secret">` and for prose that mentions the word, and fails for a
// secret that goldmark escaped as `Ald&#114;ic`. So the structural assertions walk
// `golang.org/x/net/html`, and the one raw-bytes assertion is the blunt one on
// purpose — "this text is in the response" has no false negatives to worry about.
//
// The walks return counts rather than failing, so `TestTheAbsenceChecksAreNotVacuous`
// can drive them with documents the route cannot produce. That is not tidiness: the
// mutation run showed the response-level checks missing three real defects —
// splicing an HTML comment, a `display:none` div and a `hidden` span in the
// callout's place — because those mutations edit the *markdown* and goldmark drops
// raw HTML, so the mechanism never reached the response at all. The byte-level
// table below caught all three. These detectors are the standing statement of what
// "omission" is, and a standing statement nobody has seen fail is decoration.

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
)

// theSecret is the text the player must never receive, and the fixture the
// assertions are written against.
//
// **A single word, on purpose.** A multi-word phrase would let a mutation that
// escapes or splits one word still pass a substring check on the rest, and a word
// that appears nowhere else in the fixture is the only way "it is absent" is
// unambiguous.
const theSecret = "Aldric"

// playerFixture is the page every absence assertion runs against: prose, a
// collapsed callout with a title and an Obsidian block id, more prose, and a
// second collapsed callout so that a "cut only the first" mutation cannot pass by
// accident.
//
// The block id is here because a title *with* an id is the shape that has the
// most surface to leak from — a header left behind renders `^traitor` as visible
// prose — and the titleless shape is covered by the byte-equality table below.
const playerFixture = "# The vault\n" +
	"\n" +
	"The vault door is iron, and it has never once been opened with a key.\n" +
	"\n" +
	"> [!secret]- The first secret  ^first\n" +
	"> The traitor is Captain Aldric.\n" +
	"\n" +
	"It sticks. The grease on the hinge is from a decade ago.\n" +
	"\n" +
	"> [!secret]-\n" +
	"> A second secret, and a second captain.\n" +
	"\n" +
	"The floor is lava. Nobody has explained the floor.\n"

// TestTheSecretTextIsAbsentFromEveryByteThePlayerReceives is S-5.6's rule, at the
// only level this package can see it.
//
// Four layers, because each one catches a mutation the others do not:
//
//   - **The raw bytes.** The blunt check, and the one that cannot be fooled by
//     escaping, by an attribute, or by a comment. It fails for the right reason
//     on every hiding mechanism §5.6.1 names.
//   - **Every text node and every comment.** The DOM layer, so a secret that
//     arrives split across nodes — escaped, or broken by a soft line break — is
//     still caught. `html.Parse` unescapes text nodes, so `Ald&#114;ic` reads as
//     `Aldric` here while the raw check above would not have seen it.
//   - **Every attribute value.** `data-secret`, a `title`, a `aria-label`: a
//     secret pasted into an attribute is in the response just as much as one in a
//     text node, and no amount of `display:none` changes that.
//   - **The hiding mechanisms themselves.** `display:none`, a `hidden` attribute,
//     a `secret` class. Asserted as *absent* rather than as "not visible",
//     because every one of them is invisible and every one of them is a leak.
func TestTheSecretTextIsAbsentFromEveryByteThePlayerReceives(t *testing.T) {
	t.Parallel()

	document := renderForViewer(t, playerFixture, false)

	assertTextAbsentFromEveryByte(t, document, theSecret)
	assertTextAbsentFromEveryByte(t, document, "The traitor is")
	assertTextAbsentFromEveryByte(t, document, "[!secret]")
	assertTextAbsentFromEveryByte(t, document, "^first")

	if found := hidingMechanisms(document); len(found) > 0 {
		t.Errorf("the response hides rather than omits: %s\n%s",
			strings.Join(found, "; "), document)
	}
}

// hidingMechanisms returns a description of every way §5.6.1 rules out that the
// response uses: a `hidden` attribute, an inline `style`, a class naming the
// secret, or a `data-secret` attribute.
//
// Every one of them renders as "not there" to a player and is a `curl` away for
// anyone who wants the text, which is the whole reason the plan enumerates them
// instead of saying "hide it". A `Redactor` that produced any of them has failed
// even though every byte it meant to remove is still where it was.
//
// **It returns rather than failing**, so that `TestTheAbsenceChecksAreNotVacuous`
// can drive it with documents the wiki route cannot produce — which is the only
// way to know these four arms still work, since goldmark drops raw HTML and
// `policy.go` allows neither `style` nor `hidden`, so on this pipeline none of
// them can fire. An assertion that cannot fail is not an assertion; this one has a
// test that proves it can.
func hidingMechanisms(document string) []string {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return []string{"the response is not parseable HTML: " + err.Error()}
	}

	var found []string

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			for _, attribute := range node.Attr {
				switch {
				case attribute.Key == "hidden":
					found = append(found, "a `hidden` attribute on <"+node.Data+">")
				case attribute.Key == "style":
					found = append(found, "an inline `style` on <"+node.Data+">")
				case attribute.Key == "data-secret":
					found = append(
						found,
						"a `data-secret` attribute, so the callout is still in the document",
					)
				case attribute.Key == "class" && strings.Contains(attribute.Val, "secret"):
					found = append(found, "a `secret` class on <"+node.Data+">")
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// TestTheAbsenceChecksAreNotVacuous is the meta-test, and it exists because the
// mutation run showed the response-level checks missing a real defect.
//
// The redactor was mutated to splice `<!-- redacted -->`, `<div
// style="display:none">` and `<span hidden>` in the callout's place, and **none of
// the three turned the response-level assertions red**. The reason is not that the
// assertions are weak: it is that those mutations edit the *markdown*, and goldmark
// drops raw HTML before the sanitiser ever sees it, so the mechanism never reached
// the response. The byte-level table caught all three.
//
// That is a real limitation of a check made at the end of the pipeline, and it has
// two halves to state honestly. The raw-bytes check has no blind spot and is the
// one that matters. The DOM walk catches the one thing raw bytes cannot — a secret
// written with character references, which reads `Ald&#114;ic` in the bytes and
// `Aldric` to a player. And the mechanism check, above, cannot fire on this
// pipeline at all for the same reason goldmark dropped the three mutations.
//
// So the detectors are driven here directly, with documents the route cannot
// produce, and each is required to object. A green assertion nobody has ever seen
// fail is a green light wired to nothing, and this is the wiring.
func TestTheAbsenceChecksAreNotVacuous(t *testing.T) {
	t.Parallel()

	for name, document := range map[string]string{
		"a text node":          "<p>The traitor is Captain " + theSecret + ".</p>",
		"character references": "<p>The traitor is Captain Ald&#114;ic.</p>",
		"an attribute value":   "<p title=\"The traitor is Captain " + theSecret + "\">Prose.</p>",
		"a comment":            "<p>Prose.</p><!-- The traitor is Captain " + theSecret + " -->",
		"a data attribute":     "<p data-note=\"Captain " + theSecret + "\">Prose.</p>",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if absentFromEveryByte(document, theSecret) == 0 {
				t.Errorf("absentFromEveryByte found nothing in a document carrying the "+
					"secret %s:\n%s", name, document)
			}
		})
	}

	for name, document := range map[string]string{
		"clean": "<h1>The vault</h1>\n<p>The door is iron.</p>\n" +
			"<a href=\"/c/greyhaven/wiki/Vault\" class=\"link\">The vault</a>\n",
		"a link that happens to be called secret elsewhere": "<p><a href=\"/s\">A secret link</a></p>",
	} {
		t.Run("no mechanism: "+name, func(t *testing.T) {
			t.Parallel()

			if absentFromEveryByte(document, theSecret) != 0 {
				t.Errorf("absentFromEveryByte objected to a clean document:\n%s", document)
			}

			if found := hidingMechanisms(document); len(found) > 0 {
				t.Errorf("hidingMechanisms objected to a clean document: %v\n%s", found, document)
			}
		})
	}

	for name, document := range map[string]string{
		"display:none": `<div style="display:none">The traitor is Captain ` + theSecret + `.</div>`,
		"hidden":       "<span hidden>The traitor is Captain " + theSecret + ".</span>",
		"a class":      `<div class="secret secret--collapsed">The traitor is Captain ` + theSecret + `.</div>`,
		"data-secret":  `<div data-secret="collapsed">The traitor is Captain ` + theSecret + `.</div>`,
		"a comment":    "<div>Prose.</div><!-- The traitor is Captain " + theSecret + " -->",
	} {
		t.Run("mechanism found: "+name, func(t *testing.T) {
			t.Parallel()

			// The comment case is the one that cannot be detected by an attribute
			// walk, and it is here because `<!-- … -->` is one of the four ways
			// §5.6.1 names. It is reported by the absence check, not the mechanism
			// check: a comment is not a mechanism, it is a place the text can hide.
			if absentFromEveryByte(document, theSecret) == 0 {
				t.Errorf("absentFromEveryByte found nothing for %s:\n%s", name, document)
			}

			if name == "a comment" {
				if found := hidingMechanisms(document); len(found) != 0 {
					t.Errorf("a comment is not a hiding mechanism, but it was reported as "+
						"one: %v", found)
				}

				return
			}

			if found := hidingMechanisms(document); len(found) == 0 {
				t.Errorf("hidingMechanisms found nothing for %s:\n%s", name, document)
			}
		})
	}
}

// TestTheProseOnBothSidesOfTheCalloutSurvives is the half of redaction that a
// leak test cannot see.
//
// Two paragraphs, and they must be **two paragraphs**: a redactor that cut the
// callout and one of the blank lines around it would join them into a single
// `<p>`, which still contains both sentences and still passes an assertion that
// looks for the sentences. So the assertions are on the parsed structure — the
// heading, then a paragraph, then a paragraph — not on the text.
func TestTheProseOnBothSidesOfTheCalloutSurvives(t *testing.T) {
	t.Parallel()

	document := renderForViewer(t, playerFixture, false)

	blocks := blockTexts(t, document)

	want := []string{
		"h1: The vault",
		"p: The vault door is iron, and it has never once been opened with a key.",
		"p: It sticks. The grease on the hinge is from a decade ago.",
		"p: The floor is lava. Nobody has explained the floor.",
	}

	if len(blocks) != len(want) {
		t.Fatalf("the page has %d block(s), want %d — redaction removed prose as well "+
			"as the secret:\n%s\nblocks:\n%s",
			len(blocks), len(want), document, strings.Join(blocks, "\n---\n"))
	}

	for index, expected := range want {
		if blocks[index] != expected {
			t.Errorf("block %d = %q, want %q", index, blocks[index], expected)
		}
	}
}

// TestTheBlankLineACalloutLeftBehindChangesNothing is the fidelity half, and it
// exists because a version of this file got it wrong.
//
// A cut ends one past the newline of the callout's last quoted line, so the blank
// line that *followed* the callout survives and the redacted source carries an
// extra newline. The obvious worry is that a blank line too many merges two
// blocks — most visibly by turning two lists into one loose list. It does not,
// and the reason is that the blank line which merges them is the one **before**
// the callout, which is outside the span: a blank line between two list items
// already makes the list loose whether or not a third newline follows.
//
// The claim is worth a test rather than a sentence, because the failure it guards
// against was real once: this file consumed the trailing blank lines on the
// strength of exactly this reasoning, and the mechanism was right while the cause
// was wrong. So the assertion is **byte equality of the rendered HTML** for one
// blank line and for four, around each of the constructs the change would plausibly
// have broken — and if a future goldmark or a future redaction breaks that
// equivalence, this is what says so.
func TestTheBlankLineACalloutLeftBehindChangesNothing(t *testing.T) {
	t.Parallel()

	for name, pair := range map[string]struct{ one, many string }{
		"two lists": {
			one:  "- one\n\n- two\n",
			many: "- one\n\n\n\n- two\n",
		},
		"a heading": {
			one:  "Text.\n\n# Head\n",
			many: "Text.\n\n\n\n# Head\n",
		},
		"a block quote": {
			one:  "Para.\n\n> quoted\n",
			many: "Para.\n\n\n\n> quoted\n",
		},
		"an indented code line": {
			one:  "- one\n\n    code\n",
			many: "- one\n\n\n\n    code\n",
		},
		"a fenced block": {
			one:  "Text.\n\n```\ncode\n```\n",
			many: "Text.\n\n\n\n```\ncode\n```\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			one := renderForViewer(t, pair.one, false)
			many := renderForViewer(t, pair.many, false)

			if one != many {
				t.Errorf("one blank line and four render differently, so the newline "+
					"redaction leaves behind is not harmless:\none:\n%s\nfour:\n%s", one, many)
			}
		})
	}
}

// TestTheResultIsTheSourceWithExactlyTheCalloutBytesRemoved is the byte-level
// version of the same claim, and it is here because "the secret is absent" and
// "nothing else changed" are different assertions with different mutations.
//
// **The extra blank line in the expected values is deliberate**, and it is the
// shape of the rule rather than a fudge: the cut ends one past the newline of the
// callout's last quoted line, and the blank line that separated the callout from
// the prose after it is not the callout's byte. `TestTheBlankLineACalloutLeftBehindChangesNothing`
// is what says that leaving it is harmless.
//
// The fixtures are chosen for the shapes that have somewhere to go wrong: CRLF
// line endings, a callout with no body, a callout with a blank quoted line in
// its body, a callout at end of file with no trailing newline, two callouts with
// no prose between them, a callout indented inside a list item, and a page that
// is nothing but a secret. Every expected value below was written by hand from
// the fixture, not produced by the implementation — which is the only way this
// table can fail.
func TestTheResultIsTheSourceWithExactlyTheCalloutBytesRemoved(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct{ source, want string }{
		"one callout between two paragraphs": {
			source: "Before.\n\n> [!secret]-\n> The traitor is Captain Aldric.\n\nAfter.\n",
			want:   "Before.\n\n\nAfter.\n",
		},
		"the header goes too": {
			// The whole point of the cut being `HeaderStart..BodyEnd` rather than
			// the body alone: `> [!secret]-` left behind renders as a visible line
			// of prose, which is the existence disclosure §5.6.1 rules out.
			source: "Before.\n\n> [!secret]- Aldric  ^traitor\n> The traitor is Captain Aldric.\n\nAfter.\n",
			want:   "Before.\n\n\nAfter.\n",
		},
		"two callouts, no prose between": {
			// The offset-shift case. A redactor that cut one callout, rebuilt the
			// string and cut again on the new offsets takes the second callout's
			// bytes out of the middle of the first one's body and leaves a
			// half-callout standing.
			source: "Before.\n\n> [!secret]-\n> One.\n> [!secret]-\n> Two.\n\nAfter.\n",
			want:   "Before.\n\n\nAfter.\n",
		},
		"a revealed callout between two collapsed ones": {
			source: "A.\n\n> [!secret]-\n> One.\n\n> [!secret]+\n> Public.\n\n> [!secret]-\n> Two.\n\nB.\n",
			want:   "A.\n\n\n> [!secret]+\n> Public.\n\n\nB.\n",
		},
		"a callout with a blank quoted line in its body": {
			source: "Before.\n\n> [!secret]-\n> One.\n>\n> Two.\n\nAfter.\n",
			want:   "Before.\n\n\nAfter.\n",
		},
		"a callout with no body": {
			source: "Before.\n\n> [!secret]-\n\nAfter.\n",
			want:   "Before.\n\n\nAfter.\n",
		},
		"a callout indented inside a list item": {
			// `HeaderStart` is the line's first byte, so the item's indentation
			// goes with the callout. Cutting from the `>` instead leaves two
			// spaces and a line of prose inside the item.
			source: "- one\n\n  > [!secret]-\n  > The traitor is Captain Aldric.\n\n- two\n",
			want:   "- one\n\n\n- two\n",
		},
		"a callout at end of file with no trailing newline": {
			source: "Before.\n\n> [!secret]-\n> The traitor is Captain Aldric.",
			want:   "Before.\n\n",
		},
		"a callout at end of file with trailing blank lines": {
			source: "Before.\n\n> [!secret]-\n> The traitor is Captain Aldric.\n\n\n",
			want:   "Before.\n\n\n\n",
		},
		"CRLF line endings": {
			source: "Before.\r\n\r\n> [!secret]-\r\n> The traitor is Captain Aldric.\r\n\r\nAfter.\r\n",
			want:   "Before.\r\n\r\n\r\nAfter.\r\n",
		},
		"no callout at all": {
			source: "Just prose.\n",
			want:   "Just prose.\n",
		},
		"a marker that is not a callout": {
			// Not quoted, so not a callout: §5.6.1's rule is about the callout,
			// and a sentence mentioning the keyword is prose that must survive.
			source: "The keeper said [!secret] and left.\n",
			want:   "The keeper said [!secret] and left.\n",
		},
		"a page that is nothing but a secret": {
			source: "> [!secret]-\n> The traitor is Captain Aldric.\n",
			want:   "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := content.OmitSecrets().Redact(fixture.source, false)
			if err != nil {
				t.Fatalf("Redact() error = %v, want nil", err)
			}

			if got != fixture.want {
				t.Errorf("Redact() =\n%q\nwant\n%q\nsource:\n%q", got, fixture.want, fixture.source)
			}
		})
	}
}

// TestAGMReceivesTheSourceUnchanged: the flag is not a hint.
//
// `includeSecrets` true returns the **same string**, not a string that happens to
// render the same. Byte equality is the assertion because the alternative — a
// redactor that re-serialises the source and gets a newline or an indentation
// subtly different — produces a GM page whose GM and the player's share no byte,
// and the difference between two renderings of one file is invisible until a
// diff of a save is read.
func TestAGMReceivesTheSourceUnchanged(t *testing.T) {
	t.Parallel()

	got, err := content.OmitSecrets().Redact(playerFixture, true)
	if err != nil {
		t.Fatalf("Redact(true) error = %v, want nil", err)
	}

	if got != playerFixture {
		t.Errorf("Redact(true) changed the source:\ngot:  %q\nwant: %q", got, playerFixture)
	}

	document := renderForViewer(t, playerFixture, true)

	if !strings.Contains(document, theSecret) {
		t.Errorf("the GM's page does not contain the secret at all:\n%s", document)
	}

	if !strings.Contains(document, `data-secret="collapsed"`) {
		t.Errorf("the GM's page does not render the callout as a callout:\n%s", document)
	}
}

// TestARevealedCalloutIsPublicAndSurvivesForAPlayer is the decision the delivery
// plan's wording does not settle on its own.
//
// The plan says "the callout is removed entirely for non-GM viewers", which read
// alone would cut `[!secret]+` as well — and would make S6's reveal endpoint a
// way to lose information, because a reader who cannot see a `+` callout cannot
// see a revealed secret from any page. S-5.6 says "a `[!secret]-` body", and §5.6
// says `+` is "revealed and public". So only the collapsed state is cut.
//
// **Both directions in one fixture**, because each alone is satisfiable by a
// constant: asserting only that the `+` survives passes for a redactor that cuts
// nothing, and asserting only that the `-` goes passes for one that cuts both.
func TestARevealedCalloutIsPublicAndSurvivesForAPlayer(t *testing.T) {
	t.Parallel()

	const source = "A.\n\n> [!secret]-\n> Hidden.\n\n> [!secret]+\n> Public.\n\nB.\n"

	document := renderForViewer(t, source, false)

	assertTextAbsentFromEveryByte(t, document, "Hidden.")

	if !strings.Contains(document, "Public.") {
		t.Errorf("a revealed callout was removed for a player; §5.6: `+` is revealed "+
			"and public:\n%s", document)
	}

	if !strings.Contains(document, `data-secret="revealed"`) {
		t.Errorf("the revealed callout did not render as a callout:\n%s", document)
	}
}

// TestRedactionIsAPureFunctionOfTheSource is the determinism half of "there are
// exactly two variants per page, never a per-user cache" (§5.5).
//
// Four properties, all of which a redactor could plausibly grow:
//
//   - **Repeatable.** The same input gives the same bytes every time.
//   - **Concurrent.** Two readers of one page do not interleave. The cache shares
//     one `Redactor` across every request for a campaign.
//   - **Idempotent.** Redacting twice equals redacting once, which also proves the
//     first pass left no residue that looks like a callout — a redactor that
//     replaced the marker with a placeholder would be idempotent in the *wrong*
//     direction, and this catches it because the placeholder is a second marker.
//   - **Irreversible by a later GM pass.** `Redact(Redact(s, false), true)` equals
//     `Redact(s, false)`: once the bytes are gone, no flag brings them back, so a
//     pipeline that redacts twice — once per variant — cannot resurrect them by
//     re-running with the GM's flag.
func TestRedactionIsAPureFunctionOfTheSource(t *testing.T) {
	t.Parallel()

	once, err := content.OmitSecrets().Redact(playerFixture, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	for round := range 32 {
		again, redactErr := content.OmitSecrets().Redact(playerFixture, false)
		if redactErr != nil {
			t.Fatalf("Redact() error = %v, want nil", redactErr)
		}

		if again != once {
			t.Fatalf("round %d differed from round 0; redaction is not deterministic:\n%q\n%q",
				round, once, again)
		}
	}

	twice, err := content.OmitSecrets().Redact(once, false)
	if err != nil {
		t.Fatalf("Redact(redacted) error = %v, want nil", err)
	}

	if twice != once {
		t.Errorf("redacting twice is not the same as redacting once:\n%q\n%q", twice, once)
	}

	asGM, err := content.OmitSecrets().Redact(once, true)
	if err != nil {
		t.Fatalf("Redact(redacted, true) error = %v, want nil", err)
	}

	if asGM != once {
		t.Errorf("a GM flag run over redacted bytes restored something:\n%q\n%q", asGM, once)
	}

	// Concurrent, because one `Redactor` serves every request for a campaign and
	// `make check` runs the suite under `-race`.
	var group sync.WaitGroup

	results := make([]string, 64)

	for index := range results {
		group.Go(func() {
			body, redactErr := content.OmitSecrets().Redact(playerFixture, false)
			if redactErr != nil {
				t.Errorf("Redact() error = %v, want nil", redactErr)
				return
			}

			results[index] = body
		})
	}

	group.Wait()

	for index, got := range results {
		if got != once {
			t.Fatalf("goroutine %d produced different bytes", index)
		}
	}
}

// TestRedactionOfRenderedHTMLCannotRemoveAnything is the position, argued from
// inside this package rather than asserted in a comment.
//
// The pipeline order is `redact → parse → render → sanitise`, and the order is
// what S-5.7 protects. This test is the demonstration of **why**: hand the
// redactor the finished HTML — which is what a redactor running one step late
// would be handed — and the secret is still there, because `> [!secret]-` became
// `<div class="secret secret--collapsed">` and the callout is not a line of
// markdown any more.
//
// So a future change that moves the call after the render does not produce a
// failing test here; it produces a passing one and a leak, which is why this test
// exists as the standing proof that the late position cannot work. The
// pipeline-level assertion — that the renderer was handed bytes without the text
// in them — is `TestRedactedTextAppearsNowhereInANonGMResponse` in
// `internal/httpapi/wiki`, which owns the only caller.
func TestRedactionOfRenderedHTMLCannotRemoveAnything(t *testing.T) {
	t.Parallel()

	rendered := renderForViewer(t, playerFixture, true)

	// Sanity: the late position is handed real HTML, not markdown, or the test
	// would be proving something about a string nobody produces.
	if !strings.Contains(rendered, `data-secret="collapsed"`) {
		t.Fatalf("the fixture did not render a callout, so this test would be vacuous:\n%s",
			rendered)
	}

	late, err := content.OmitSecrets().Redact(rendered, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if !strings.Contains(late, theSecret) {
		t.Errorf("redacting the rendered HTML removed the secret, which would mean "+
			"the late position works and this file's whole argument is wrong:\n%s", late)
	}
}

// TestASecretInsideAFenceIsTextAndSurvives: the redactor does not re-parse.
//
// The grammar belongs to `ScanSecrets`, and it has already decided that a
// `[!secret]` inside a fenced code block is text — an author documenting the
// syntax, or a page about the feature itself. A redactor that scanned for the
// marker as a substring would delete a code block from every player's page, which
// is the mirror-image bug of leaking one: over-removal that destroys the author's
// work on the way past.
//
// The positive assertion matters as much as the negative here. "The fence
// survives" is what proves the redactor *consulted* the scanner rather than
// looking for the word.
func TestASecretInsideAFenceIsTextAndSurvives(t *testing.T) {
	t.Parallel()

	const source = "How to write one:\n\n" +
		"```markdown\n" +
		"> [!secret]-\n" +
		"> The traitor is Captain Aldric.\n" +
		"```\n\n" +
		"That is the syntax.\n"

	got, err := content.OmitSecrets().Redact(source, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if got != source {
		t.Errorf("a fenced `[!secret]` was cut; it is a code sample, not a secret:\n%q", got)
	}

	document := renderForViewer(t, source, false)

	if !strings.Contains(document, "[!secret]") {
		t.Errorf("the code sample did not reach the rendered page:\n%s", document)
	}
}

// TestAnErrorFromThisRedactorCarriesNoPageContent is S-12.3, at the one place a
// redaction error would be born.
//
// **The `err != nil` branch does not execute today** — cutting two integers out
// of a string cannot fail, and the implementation returns nil for that reason.
// It is here anyway, and it is here anyway on purpose: the mutation this was
// written for is "add a `fmt.Errorf` carrying the source to a branch", which is
// exactly the mistake S-12.3 forbids and exactly what a parser's own error does
// (`ScanSecrets`' neighbours quote the line they choked on, and on a wiki page
// that line is routinely a callout body). It was verified by mutation — see the
// commit message — because a guard that cannot fail is not a guard, and this one
// exists to be armed by the next person who adds an error path.
func TestAnErrorFromThisRedactorCarriesNoPageContent(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"ordinary":              playerFixture,
		"unterminated fence":    "```\n> [!secret]-\n> The traitor is Captain Aldric.\n",
		"nested":                "> [!secret]+\n> Outer.\n> > [!secret]-\n> > Inner.\n",
		"front matter":          "---\ntitle: T\n---\n> [!secret]-\n> The traitor is Captain Aldric.\n",
		"only a marker":         "> [!secret]-\n",
		"a marker mid-sentence": "The keeper said [!secret] and left.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, includeSecrets := range []bool{true, false} {
				_, err := content.OmitSecrets().Redact(source, includeSecrets)

				if err == nil {
					continue
				}

				// The check is over the *whole* source and not just the secret,
				// because an error that quotes the neighbouring paragraph is just
				// as much a leak as one that quotes the body.
				for _, forbidden := range []string{theSecret, "Aldric", source} {
					if forbidden == "" {
						continue
					}

					if strings.Contains(err.Error(), forbidden) {
						t.Errorf("the error carries page content: %q\nsource:\n%s",
							err.Error(), source)
					}
				}
			}
		})
	}
}

// TestACalloutShapedLineInFrontMatterIsCutToo holds the claim `redact.go` makes
// about front matter, in both directions, because "front matter is scanned too" is
// the kind of sentence that is either obviously right or quietly wrong.
//
// The line that is a callout in a body is a callout in front matter — the scanner
// walks lines and knows nothing about a `---` block — so it is cut, and the cut
// leaves YAML that still parses. The marker inside a **quoted** string is not a
// callout by the grammar, because the line does not begin with `>`, and it is
// correctly left alone: front matter never reaches the renderer and `body_plain`
// strips it before the index, so it cannot reach a response either way.
//
// The second half matters more than it looks. A redactor that reached for the
// marker *as a string* would strip the quoted-string case too, and would then be
// editing YAML it does not understand — which is the same mistake as re-parsing
// the markdown, and for the same reason.
func TestACalloutShapedLineInFrontMatterIsCutToo(t *testing.T) {
	t.Parallel()

	const blockScalar = "---\ntitle: T\nnotes: |\n  > [!secret]-\n  > " + theSecret +
		"\n---\nProse.\n"

	gotBlock, err := content.OmitSecrets().Redact(blockScalar, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if strings.Contains(gotBlock, theSecret) {
		t.Errorf("a callout-shaped line inside front matter survived:\n%q", gotBlock)
	}

	if want := "---\ntitle: T\nnotes: |\n---\nProse.\n"; gotBlock != want {
		t.Errorf("Redact() = %q, want %q", gotBlock, want)
	}

	// And it must still be a document: a cut that broke the front matter block
	// would leave `Parse` with nothing, which is a 500 rather than a leak.
	document := content.Parse([]byte(gotBlock), nil)
	if document.FrontMatter.Err != nil {
		t.Errorf("the cut left unparseable front matter: %v\n%q",
			document.FrontMatter.Err, gotBlock)
	}

	const quoted = "---\ntitle: T\nnote: \"> [!secret]- " + theSecret + "\"\n---\nProse.\n"

	gotQuoted, err := content.OmitSecrets().Redact(quoted, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if gotQuoted != quoted {
		t.Errorf("a marker inside a YAML quoted string was cut; it is not a callout, "+
			"and front matter never reaches the renderer or the index:\ngot:  %q\nwant: %q",
			gotQuoted, quoted)
	}

	// Positive: the same quoted string *inside a fenced block in the body* is also
	// text, and the two rules agree.
	const fenced = "Prose.\n\n```\n" + quoted + "```\n"

	gotFenced, err := content.OmitSecrets().Redact(fenced, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if gotFenced != fenced {
		t.Errorf("front matter inside a fence was cut:\ngot:  %q\nwant: %q", gotFenced, fenced)
	}
}

// TestANestedCalloutIsTheOuterCalloutsToDecide pins a measured defect in
// `secret.go`, because the measured behaviour and the documented behaviour are
// different and the difference is a disclosure.
//
// `secret.go` states that "a callout inside another callout is found by its own
// header and reported separately". It is not: the scanner's body loop consumes
// every line that is still quoted, so `> > [!secret]-` is swallowed by the outer
// callout and never appears in the result. So when the outer callout is `+` —
// public, therefore kept — the inner `-` is not cut, and it reaches a player as
// a `secret--collapsed` callout with its text inside it.
//
// This is **not fixed here**, and the reason is a boundary rather than a
// difficulty: teaching the redactor to look inside a callout it decided to keep
// means re-implementing enough Markdown to find the nested header, which is the
// second grammar this file exists to avoid — and the blunt version of that hack,
// matching the marker as a substring, would delete every `[!secret]` a page merely
// *mentions*, which is a different and worse failure. The fix belongs to
// `secret.go`, whose header already says what the answer is.
//
// The assertion is the **rendered** consequence and not the redactor's return
// value, because the redactor's return value is not the disclosure: the point is
// that the inner callout comes back as a `secret--collapsed` element with its body
// in it, and only the render shows that.
//
// What this file can do is make the current behaviour a **visible diff** rather
// than a silent one: it fails today only when the behaviour changes, and the
// change it is waiting for is the right one.
func TestANestedCalloutIsTheOuterCalloutsToDecide(t *testing.T) {
	t.Parallel()

	const source = "A.\n\n> [!secret]+\n> Outer.\n> > [!secret]-\n> > Inner.\n\nB.\n"

	got, err := content.OmitSecrets().Redact(source, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if got != source {
		t.Errorf("a nested callout inside a *revealed* one was cut. When this starts "+
			"failing the other way, `secret.go` has learned to report nested callouts "+
			"and the comment above should be replaced with what it now does:\ngot:\n%q",
			got)
	}

	// The measured disclosure, pinned so the report's claim is a test and not an
	// assertion: a player receives the inner body inside a collapsed callout.
	rendered := renderForViewer(t, source, false)

	if !strings.Contains(rendered, "Inner.") ||
		!strings.Contains(rendered, `data-secret="collapsed"`) {
		t.Errorf("the nested collapsed callout no longer renders as one, so this "+
			"fixture no longer demonstrates the defect it was written for:\n%s", rendered)
	}

	// The same shape inside a *collapsed* outer callout is removed whole, which is
	// the direction every failure path in this subsystem has to resolve toward
	// (§5.6.2): the outer cut takes the inner with it, so no text survives.
	const collapsed = "A.\n\n> [!secret]-\n> Outer.\n> > [!secret]-\n> > Inner.\n\nB.\n"

	gotCollapsed, err := content.OmitSecrets().Redact(collapsed, false)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	if gotCollapsed != "A.\n\n\nB.\n" {
		t.Errorf("a nested callout inside a collapsed one left text behind:\n%q", gotCollapsed)
	}
}

// TestRedactorSeesTheWholeSource is the position, expressed as a test.
//
// A redactor is handed the file's bytes — front matter included — rather than a
// parsed body, because front matter is attacker-reachable (S-4.7) and a redactor
// that could only see the prose would have to be trusted not to care about the
// fields. The stub below records what it was given, and the assertion is that the
// recording contains the block a parse would have split off.
func TestRedactorSeesTheWholeSource(t *testing.T) {
	t.Parallel()

	const source = "---\ntitle: The vault\nsecret_note: behind the door\n---\nThe vault door is iron.\n"

	seen := recordingRedactor{}
	if _, err := seen.Redact(source, false); err != nil {
		t.Fatalf("Redact: %v", err)
	}

	if !strings.Contains(seen.body, "secret_note: behind the door") {
		t.Errorf("the redactor was not shown the front matter:\n%s", seen.body)
	}

	if !strings.Contains(seen.body, "The vault door is iron.") {
		t.Errorf("the redactor was not shown the prose:\n%s", seen.body)
	}

	if seen.includeSecrets {
		t.Error("the redactor was told secrets were wanted, for a viewer that may not see them")
	}
}

// TestARedactorErrorReachesItsCaller: an error is a fault rather than an answer,
// and swallowing one would mean serving a page whose redaction state nobody knows.
func TestARedactorErrorReachesItsCaller(t *testing.T) {
	t.Parallel()

	wanted := errors.New("redactor failed")

	if _, err := (failingRedactor{wanted}).Redact("body", false); !errors.Is(err, wanted) {
		t.Errorf("Redact returned %v, want the redactor's own error", err)
	}
}

// TestNoSecretsIsTheNameThisRedactorShippedUnder holds the alias.
//
// `NoSecrets` is a **naming debt**, kept only so `cmd/server/` and the route
// tests keep compiling — those paths belong to the phase integrator, and the
// phase that wrote the pass-through said in `redact.go` that replacing it touches
// one function body. It is a test rather than a comment because the failure it
// guards against is the one the name invites: someone reading `NoSecrets()`,
// believing it removes nothing, and using it in a test that then asserts a secret
// is absent — which passes for the wrong reason, or fails for a reason that has
// nothing to do with redaction.
//
// **Delete this test with the alias**, in the same commit that renames the
// call sites.
func TestNoSecretsIsTheNameThisRedactorShippedUnder(t *testing.T) {
	t.Parallel()

	player, err := content.NoSecrets().Redact(playerFixture, false)
	if err != nil {
		t.Fatalf("NoSecrets().Redact() error = %v, want nil", err)
	}

	if strings.Contains(player, theSecret) {
		t.Errorf("content.NoSecrets() still removes nothing, and the phase that wrote "+
			"it says P10 replaces it. The alias has diverged from OmitSecrets:\n%s", player)
	}
}

// TestARedactorDoesNotPanicOnAPageWithoutATrailingNewline is here, in this file,
// rather than only next to the scanner.
//
// Every non-GM page render goes through `Redact`, so this file is where a scanner
// defect becomes a request that fails rather than a unit test that fails — and the
// defect it guards against was live: `scanSecrets` computed the end of an
// unterminated final line as `len(source)` instead of `len(source) - offset`, and
// `Redact` panicked with a slice-bounds error on `"Before.\nHello"`. The fix and
// its own regression test are in `secret.go`; this is the statement that the
// redaction path cannot crash, which is a different claim from the scanner's
// being correct.
//
// The last line of a vault page is the author's editor's decision, not
// semiplane's, and a sync client rewrites files — so "well-formed vaults end with a
// newline" is not an assumption this path may hold.
func TestARedactorDoesNotPanicOnAPageWithoutATrailingNewline(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		source   string
		wantGone bool
	}{
		"one unterminated line":      {source: "The vault door is iron.", wantGone: true},
		"several, last unterminated": {source: "The vault door is iron.\nIt sticks.", wantGone: true},
		"CRLF, last unterminated":    {source: "The vault door is iron.\r\nIt sticks.", wantGone: true},
		"a callout on the last line": {
			source:   "Before.\n\n> [!secret]-\n> The traitor is " + theSecret + ".",
			wantGone: true,
		},
		"a callout, then prose": {
			source:   "Before.\n\n> [!secret]-\n> " + theSecret + ".\n\nAfter",
			wantGone: true,
		},
		"an unterminated fence": {
			// The one case where the secret-looking text **must** stay: an
			// unterminated fence runs to the end of the file, so the callout
			// inside it is text. Asserting it were cut would be the wrong
			// assertion, and it is here to say so.
			source:   "Before.\n```\n> [!secret]-\n> " + theSecret,
			wantGone: false,
		},
		"front matter then prose": {
			source:   "---\ntitle: T\n---\nProse with no final newline",
			wantGone: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, includeSecrets := range []bool{true, false} {
				got, err := content.OmitSecrets().Redact(fixture.source, includeSecrets)
				if err != nil {
					t.Errorf("Redact(includeSecrets=%t) error = %v, want nil", includeSecrets, err)
				}

				// Only the player's variant is asserted here. Whether a GM sees a
				// callout's body at all is `TestAGMReceivesTheSourceUnchanged`'s
				// claim, and several of these fixtures carry no callout for it to be
				// about.
				if includeSecrets {
					continue
				}

				if carries := strings.Contains(got, theSecret); carries == fixture.wantGone {
					t.Errorf("Redact(false) = %q; the secret should be %s",
						got, present(!fixture.wantGone))
				}
			}
		})
	}
}

// present is "present" or "absent", for a failure message that has to say which of
// the two was wrong.
func present(yes bool) string {
	if yes {
		return "present"
	}

	return "absent"
}

// renderForViewer redacts source for one viewer and runs the result through the
// whole pipeline: parse, render, sanitise, `.target`.
//
// **The order is the assertion.** `redact` runs first and its output is what
// `content.Parse` is given, so every assertion above is about what a player
// receives after the sanitiser and the target pass have both run — which is the
// last byte of the response and therefore the only place "absent from every byte"
// means what it says.
func renderForViewer(t *testing.T, source string, includeSecrets bool) string {
	t.Helper()

	redacted, err := content.OmitSecrets().Redact(source, includeSecrets)
	if err != nil {
		t.Fatalf("Redact() error = %v, want nil", err)
	}

	result, err := content.NewRenderer("traitor", nil).Render(content.Parse([]byte(redacted), nil))
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}

	return result.HTML
}

// absentFromEveryByte returns how many places in the response carry `forbidden`
// — counted over the raw bytes, every text node, every comment node, and every
// attribute value.
//
// The raw check has no blind spot and is the one that matters; the DOM walk catches
// the single thing raw bytes cannot, which is a secret written with character
// references. `html.Parse` resolves them, so `Ald&#114;ic` reads as `Aldric` here
// and is invisible to `strings.Contains` — and it is not invisible to the player.
//
// It returns a count rather than failing so that
// `TestTheAbsenceChecksAreNotVacuous` can drive it; the assertions below are the
// wrappers.
func absentFromEveryByte(document, forbidden string) int {
	violations := 0

	if strings.Contains(document, forbidden) {
		violations++
	}

	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return violations + 1
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode, html.CommentNode:
			if strings.Contains(node.Data, forbidden) {
				violations++
			}
		case html.ElementNode:
			for _, attribute := range node.Attr {
				if strings.Contains(attribute.Val, forbidden) {
					violations++
				}
			}
		default:
			// Document, doctype, error and raw nodes carry no author text this
			// check is about; the attribute loop above is the one that matters for
			// an element.
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return violations
}

// assertTextAbsentFromEveryByte fails when forbidden appears in the response at
// all — in the raw bytes, in a text node, in a comment, or in an attribute value.
func assertTextAbsentFromEveryByte(t *testing.T, document, forbidden string) {
	t.Helper()

	if absentFromEveryByte(document, forbidden) == 0 {
		return
	}

	if strings.Contains(document, forbidden) {
		t.Errorf("the response contains %q as raw bytes:\n%s", forbidden, document)
	}

	t.Errorf("the response carries %q somewhere the raw bytes do not show — a text "+
		"node, a comment or an attribute value. Redaction must be omission, so there "+
		"is nowhere left for it to be:\n%s", forbidden, document)
}

// blockTexts returns the page's blocks in document order, each as
// `element: text`, with whitespace collapsed.
//
// **The innermost block, not the outermost.** A list item is recorded as `li: one`
// when it holds its words directly and as `p: one` when it holds a paragraph —
// which is the difference between a tight list and a loose one, the same words in
// two documents. A walker that recorded the outermost block would report both as
// `li:` and would pass for a redactor that had changed how the prose renders,
// which is the mistake this file's header is about.
//
// The blocks are collected rather than the text nodes for the same reason the
// element name is included: a test that checks two sentences are "in there"
// passes for a redactor that removed the break between them.
func blockTexts(t *testing.T, document string) []string {
	t.Helper()

	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("the response is not parseable HTML (%v):\n%s", err, document)
	}

	var blocks []string

	// walk returns how many blocks it recorded, so a parent knows whether one of
	// its own descendants already claimed this subtree.
	var walk func(*html.Node) int

	walk = func(node *html.Node) int {
		found := 0

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			found += walk(child)
		}

		if node.Type != html.ElementNode || !isBlock(node.Data) || found > 0 {
			return found
		}

		blocks = append(blocks, node.Data+": "+textOf(node))

		return 1
	}

	walk(root)

	return blocks
}

// isBlock reports whether an element is one whose text this file counts as prose.
//
// The list goldmark emits for the constructs `Render` produces, and deliberately
// nothing else: a `<code>` inside a paragraph is part of that paragraph's text and
// counting it separately would double it.
func isBlock(name string) bool {
	switch name {
	case "p", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "li", "td", "th":
		return true
	case "blockquote", "ul", "ol", "table", "tr":
		return false
	default:
		return false
	}
}

// textOf is an element's text content with whitespace collapsed, so a value
// compared against a hand-written sentence is not defeated by the newline goldmark
// puts inside a `<p>`.
func textOf(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
			out.WriteByte(' ')
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return strings.Join(strings.Fields(out.String()), " ")
}

// recordingRedactor keeps whatever it was handed.
type recordingRedactor struct {
	body           string
	includeSecrets bool
}

func (r *recordingRedactor) Redact(body string, includeSecrets bool) (string, error) {
	r.body = body
	r.includeSecrets = includeSecrets

	return body, nil
}

// failingRedactor fails every call.
type failingRedactor struct{ err error }

func (r failingRedactor) Redact(_ string, _ bool) (string, error) {
	return "", r.err
}

// Both stubs satisfy the interface this package installs, which is the assertion
// that a future redactor can be a function-shaped thing or a struct without
// either being a change to the pipeline.
var (
	_ content.Redactor = (*recordingRedactor)(nil)
	_ content.Redactor = failingRedactor{}
)
