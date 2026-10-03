package content_test

// The `[!secret]` scanner, over the byte offsets a reveal will splice.
//
// # Why these tests are about offsets
//
// A reveal is a **one-byte edit** (§5.6). That is the whole reason the syntax is a
// single character rather than a word: the diff is one byte and the collision
// window with Obsidian's own line handling is as small as it can be. A scanner
// that reported a callout's *line number* instead of the marker's byte offset
// would make the reveal depend on re-deriving an offset from a possibly-different
// read, and a reveal applied to the wrong byte is a secret disclosed to a player.
//
// So the load-bearing assertion in this file is `TestSetMarkerChangesExactlyOneByte`,
// and it is not a claim about tidiness — it is the property the design exists for.

import (
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

func TestScanFindsACalloutAndItsState(t *testing.T) {
	t.Parallel()

	source := "# Chapter\n\n> [!secret]-\n> The traitor is Captain Aldric.\n\nAfter.\n"

	found := content.ScanSecrets(source)
	if len(found) != 1 {
		t.Fatalf("ScanSecrets() found %d callout(s), want 1:\n%s", len(found), source)
	}

	secret := found[0]

	if secret.State != content.SecretCollapsed {
		t.Errorf("state = %q, want %q. §5.6: - is a secret, collapsed and GM-only",
			secret.State, content.SecretCollapsed)
	}

	if secret.Ordinal != 0 {
		t.Errorf("ordinal = %d, want 0", secret.Ordinal)
	}

	if secret.Body != "The traitor is Captain Aldric." {
		t.Errorf("body = %q, want the quoted line with its > removed", secret.Body)
	}
}

// TestTheMarkerIsTheState is §5.6's central claim in one test: the same document
// with the other marker is a different secret, and the scanner is what says so.
func TestTheMarkerIsTheState(t *testing.T) {
	t.Parallel()

	collapsed := "> [!secret]-\n> Aldric.\n"
	revealed := "> [!secret]+\n> Aldric.\n"

	if got := content.ScanSecrets(collapsed)[0].State; got.IsRevealed() {
		t.Error("a `-` callout reports itself revealed; §5.6 says - is GM-only")
	}

	if got := content.ScanSecrets(revealed)[0].State; !got.IsRevealed() {
		t.Error("a `+` callout does not report itself revealed; §5.6 says + is public")
	}
}

// TestSetMarkerChangesExactlyOneByte is the property the syntax exists for.
func TestSetMarkerChangesExactlyOneByte(t *testing.T) {
	t.Parallel()

	source := "# Chapter\r\n\r\n> [!secret]- Aldric  ^traitor\r\n> He replaced the fire.\r\n\r\nAfter.\r\n"

	updated, err := content.SetMarker(source, 0, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker() error = %v, want nil", err)
	}

	if len(updated) != len(source) {
		t.Errorf("the rewritten source is %d bytes, was %d. A reveal that changed the "+
			"length changed more than one byte, and §5.6's whole reason for a "+
			"single-character marker is that it does not", len(updated), len(source))
	}

	differing := 0
	for index := range source {
		if source[index] != updated[index] {
			differing++
		}
	}

	if differing != 1 {
		t.Errorf("%d bytes differ, want exactly 1:\n was %q\n now %q",
			differing, source, updated)
	}

	// And the byte that changed is the marker, not a CRLF the writer normalised
	// on the way past.
	if !strings.Contains(updated, "> [!secret]+ Aldric  ^traitor\r\n") {
		t.Error("the rewrite did not land on the marker byte; the title, the block id " +
			"or the line ending was disturbed")
	}

	// The CRLFs are still CRLFs.
	if strings.Count(updated, "\r\n") != strings.Count(source, "\r\n") {
		t.Error("the rewrite normalised a line ending. The caller's atomic write owns " +
			"the whole file and must not be handed a file whose bytes moved for a " +
			"reason it did not ask for")
	}
}

// TestTheRoundTripIsItsOwnInverse is the property a redactor and a reconciler both
// depend on: revealing then unrevealing returns the original bytes, or the diff
// against a GM's vault is permanent noise.
func TestTheRoundTripIsItsOwnInverse(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> Aldric.\n\n> [!secret]+ Revealed\n> The rest.\n"

	revealed, err := content.SetMarker(source, 0, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker() error = %v, want nil", err)
	}

	back, err := content.SetMarker(revealed, 0, content.SecretCollapsed)
	if err != nil {
		t.Fatalf("SetMarker() back error = %v, want nil", err)
	}

	if back != source {
		t.Errorf("reveal then unreveal did not return the original:\n was %q\n now %q",
			source, back)
	}
}

func TestOrdinalsCountTheCalloutsOnThePage(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> One.\n\nprose\n\n> [!secret]-\n> Two.\n\n" +
		"> [!secret]-\n> Three.\n"

	found := content.ScanSecrets(source)
	if len(found) != 3 {
		t.Fatalf("found %d, want 3", len(found))
	}

	for index, secret := range found {
		if secret.Ordinal != index {
			t.Errorf("callout %d reports ordinal %d", index, secret.Ordinal)
		}
	}

	// §5.6.3's derived anchor hashes the ordinal, so a wrong one is a wrong
	// anchor and a ledger row no later edit can re-find.
	for index, want := range []string{"One.", "Two.", "Three."} {
		if found[index].Body != want {
			t.Errorf("callout %d body = %q, want %q", index, found[index].Body, want)
		}
	}
}

func TestTheBlockIDIsTheTrailingOne(t *testing.T) {
	t.Parallel()

	source := "> [!secret]+ The traitor is Aldric  ^traitor\n> Body.\n"

	secret := content.ScanSecrets(source)[0]

	if secret.BlockID != "traitor" {
		t.Errorf("block id = %q, want %q. §5.6.3's resolution order puts an Obsidian "+
			"block id first, ahead of the derived hash", secret.BlockID, "traitor")
	}

	if secret.Title != "The traitor is Aldric" {
		t.Errorf("title = %q; the ^traitor is the anchor and not part of the title",
			secret.Title)
	}
}

// TestATrailingCaretIsOnlyAnAnchorWhenWhatFollowsIsOne is the boundary, and my
// first version of it asserted the wrong thing.
//
// I wrote "The two plans: A ^ B" and expected the caret to be prose. It is not:
// Obsidian's own rule is that a block id is a trailing `^` followed by a run of
// letters, digits, `-` and `_`, and `B` is exactly that — so the scanner reads it
// as an anchor and so would Obsidian. Asserting otherwise would have been a test
// for a rule the vault format does not have.
//
// The case that *is* a boundary is the character rule, and it is what this holds
// now: what follows the caret has to be an id, so a trailing sentence, a URL
// fragment or a space-padded caret stays in the title. That matters because a
// misread anchor changes the ledger key, and a wrong key orphans a reveal — which
// §5.6.3 says must never happen silently.
func TestATrailingCaretIsOnlyAnAnchorWhenWhatFollowsIsOne(t *testing.T) {
	t.Parallel()

	// The accepted set is asserted, not only the rejected one. A character rule
	// tested only by what it turns away is a rule whose *allowance* nothing
	// holds: dropping `case r == '-' || r == '_'` from `isBlockID` left every
	// test in this file green, because no fixture used an id containing either.
	// An id that silently stopped accepting a hyphen is a vault whose anchors
	// stop resolving, and §5.6.3's rule is that a resolve failure must never
	// drop a reveal silently.
	for _, id := range []string{
		"traitor",
		"the-traitor",
		"traitor_aldric",
		"traitor-2",
		"TRAITOR",
		"a1",
	} {
		t.Run("accepts "+id, func(t *testing.T) {
			t.Parallel()

			secret := content.ScanSecrets("> [!secret]+ Aldric  ^" + id + "\n> Body.\n")[0]
			if secret.BlockID != id {
				t.Errorf("block id = %q, want %q", secret.BlockID, id)
			}
		})
	}

	// **Trailing whitespace is deliberately not a case.** `^traitor ` and
	// `^traitor` are the same line to Obsidian, which trims trailing space before
	// it looks for a block id, and so does this. Asserting a difference here
	// would be asserting a distinction the vault format does not make -- and it
	// would mean an id read from one read of the file differed from the same id
	// read from another, which is how a reveal gets orphaned.
	for name, line := range map[string]string{
		"punctuation after": "> [!secret]+ Aldric  ^traitor.\n",
		"nothing after":     "> [!secret]+ Aldric  ^\n",
		"a url fragment":    "> [!secret]+ See ^https://example.invalid/x\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := content.ScanSecrets(line + "> Body.\n")
			if len(found) != 1 {
				t.Fatalf("found %d callouts, want 1", len(found))
			}

			if found[0].BlockID != "" {
				t.Errorf("block id = %q, want empty: what follows the caret is not "+
					"letters, digits, - or _, so the caret belongs to the title",
					found[0].BlockID)
			}
		})
	}
}

// TestSecretsInCodeAreText holds the claim the package comment makes about the
// other extensions, applied to this one: "documented in the design record" is not
// a test.
func TestSecretsInCodeAreText(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"fenced": "```markdown\n> [!secret]-\n> Aldric.\n```\n",
		"tilde":  "~~~\n> [!secret]-\n> Aldric.\n~~~\n",
		"inline": "A `> [!secret]-` in a code span.\n",
		// An opening ``` and a closing ~~~ is an *unterminated* fence, not two
		// fences. Treating it as closed is how a callout in the second half of a
		// document becomes a secret.
		"mismatched": "```\n> [!secret]-\n> Aldric.\n~~~\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if found := content.ScanSecrets(source); len(found) != 0 {
				t.Errorf("found %d callout(s) inside %s code; a fenced block's "+
					"contents are text and stay text", len(found), name)
			}
		})
	}
}

// TestAMalformedMarkerIsNotACallout covers the forms a typo produces, and the
// claim is that none of them is an error and none of them is a secret.
func TestAMalformedMarkerIsNotACallout(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"no marker":        "> [!secret]\n> Aldric.\n",
		"empty after":      "> [!secret]\n",
		"wrong state":      "> [!secret]*\n> Aldric.\n",
		"not at the start": "> The text says > [!secret]- here.\n",
		"not a quote":      "[!secret]-\nAldric.\n",
		"indented code":    "\t> [!secret]-\n\t> Aldric.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if found := content.ScanSecrets(source); len(found) != 0 {
				t.Errorf("found %d callout(s) for %q, want 0", len(found), name)
			}
		})
	}
}

// TestACalloutWithNoBodyIsStillACallout: §5.6 does not require a body, and an
// empty callout is a GM's note to self that has not been written yet.
func TestACalloutWithNoBodyIsStillACallout(t *testing.T) {
	t.Parallel()

	found := content.ScanSecrets("> [!secret]-\n\nAfter.\n")
	if len(found) != 1 {
		t.Fatalf("found %d, want 1", len(found))
	}

	if found[0].Body != "" {
		t.Errorf("body = %q, want empty", found[0].Body)
	}

	// And it must not have swallowed the paragraph after it.
	if found[0].BodyEnd > len("> [!secret]-\n") {
		t.Errorf("body end is %d, past the header; the empty callout consumed the "+
			"paragraph that followed it", found[0].BodyEnd)
	}
}

// TestABlankLineInsideTheQuoteContinuesTheCallout is the case `isQuote` exists
// for. Obsidian writes a paragraph break inside a callout as a bare `>`, and a
// check on the *content* of the unquoted line would end the callout at its first
// paragraph — which is the common shape.
func TestABlankLineInsideTheQuoteContinuesTheCallout(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> First paragraph.\n>\n> Second paragraph.\n\nAfter.\n"

	found := content.ScanSecrets(source)
	if len(found) != 1 {
		t.Fatalf("found %d, want 1", len(found))
	}

	body := found[0].Body
	if !strings.Contains(body, "First paragraph.") || !strings.Contains(body, "Second paragraph.") {
		t.Errorf("body = %q, want both paragraphs. A quoted blank line continues a "+
			"callout; only a line with no > at all ends it", body)
	}
}

// TestSetMarkerRefusesAnOrdinalWithNoCallout is the safety property behind the
// re-derivation in `SetMarker`: a stale ordinal must be an error rather than a
// write to whatever byte happens to be there.
func TestSetMarkerRefusesAnOrdinalWithNoCallout(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> Aldric.\n"

	for _, ordinal := range []int{-1, 1, 99} {
		if _, err := content.SetMarker(source, ordinal, content.SecretRevealed); err == nil {
			t.Errorf("SetMarker(ordinal=%d) error = nil, want a refusal. Rewriting a "+
				"byte because a callout moved is a disclosure", ordinal)
		}
	}
}

func TestSetMarkerRefusesAMarkerThatIsNotAMarker(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> Aldric.\n"

	if _, err := content.SetMarker(source, 0, content.SecretState('*')); err == nil {
		t.Error("SetMarker(state='*') error = nil, want a refusal. The two markers " +
			"are - and +, and accepting a third would write a byte the parser then " +
			"reads as prose")
	}
}

// TestDerivedAnchorIsStableAndSeparated: §5.6.3's fallback is best-effort, so its
// two real properties are that it is stable for one secret and that it does not
// collide across a field boundary.
func TestDerivedAnchorIsStableAndSeparated(t *testing.T) {
	t.Parallel()

	source := "> [!secret]-\n> Aldric.\n"

	secret := content.ScanSecrets(source)[0]

	first := content.DerivedAnchor(1, "lore/traitor", secret)
	again := content.DerivedAnchor(1, "lore/traitor", secret)

	if first != again {
		t.Errorf("the derived anchor is not stable: %q then %q. §5.6.3's fallback "+
			"has to survive a re-read, or every reconcile pass orphans the row",
			first, again)
	}

	if len(first) != 12 {
		t.Errorf("anchor %q is %d characters, want 12 (sha256 hex truncated)",
			first, len(first))
	}

	// The separator's whole job: without it, ("a","b",1) and ("a/b","",1) hash
	// the same concatenation.
	other := content.ScanSecrets("> [!secret]-\n> Aldric.\n")[0]

	if content.DerivedAnchor(1, "lore", other) == content.DerivedAnchor(1, "lore/", other) {
		t.Error("two different (campaign, path, ordinal) triples produced the same " +
			"anchor; the fields are concatenated without a separator")
	}

	// And the campaign is part of it: a secret's anchor differs per campaign even
	// when the page is byte-identical.
	if content.DerivedAnchor(1, "lore", other) == content.DerivedAnchor(2, "lore", other) {
		t.Error("two campaigns produced the same anchor for the same page")
	}
}

// TestTheBlockIDWinsOverTheDerivedAnchor is §5.6.3's resolution order stated as a
// test, because the order is the whole claim: an explicit id is stable across any
// edit and a derived one is not.
func TestTheBlockIDWinsOverTheDerivedAnchor(t *testing.T) {
	t.Parallel()

	first := content.ScanSecrets("> [!secret]- Aldric  ^traitor\n> The fire.\n")[0]
	// Edit the body. The derived anchor moves (its first line changed); the block
	// id does not.
	second := content.ScanSecrets("> [!secret]- Aldric  ^traitor\n> The western fire.\n")[0]

	if first.BlockID != second.BlockID {
		t.Errorf("the block id changed across a body edit: %q then %q. An orphaned "+
			"row is a reveal the next sync reversion will not re-apply",
			first.BlockID, second.BlockID)
	}

	if content.DerivedAnchor(1, "p", first) == content.DerivedAnchor(1, "p", second) {
		t.Error("the derived anchor survived a first-line edit; §5.6.3 says it is " +
			"stable only below the first line, and a test that says otherwise " +
			"would make the drift path unreachable")
	}
}

// TestASourceWhoseLastLineHasNoNewlineIsScanned is a crash this file was missing,
// and it is here because the panic it describes was live on the request path.
//
// `scanSecrets` computed the end of the final line as `len(source)` rather than
// `len(source) - offset`, so for every line after the first in a file with no
// trailing newline it sliced past the end of the string. `"Before.\nHello"` panicked
// with a slice-bounds error; a single-line file survived, which is why no fixture
// in this file found it.
//
// Four callers reach it, and the first two are on a live path: `SetMarker` (S6's
// reveal write), `Reassociate` (S7's reconciliation) and `cutSpans` (S5's
// redactor, which every non-GM page render now goes through). Whether a vault's
// pages end with a newline is the author's editor's decision, not semiplane's, so
// "well-formed authors do" is not an answer for a wiki that reads whatever is on
// disk — and neither is Obsidian Sync, which rewrites files.
//
// The assertion is that no input panics, over the shapes that take the
// unterminated branch, and that the answers are still right rather than merely
// non-crashing: a callout on the final line with no newline after it is still a
// callout, with the right offsets.
func TestASourceWhoseLastLineHasNoNewlineIsScanned(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		source string
		want   int
	}{
		"a single unterminated line":     {source: "Hello", want: 0},
		"an unterminated line after one": {source: "Before.\nHello", want: 0},
		"the same, CRLF throughout":      {source: "Before.\r\nHello", want: 0},
		"a callout on the final line":    {source: "Before.\n\n> [!secret]-\n> Hello.", want: 1},
		"a callout, final body unterminated": {
			source: "Before.\n\n> [!secret]-\n> One.\n> Two.",
			want:   1,
		},
		"an unterminated fence":          {source: "Before.\n```\ncode", want: 0},
		"unterminated, blank final line": {source: "Before.\n> [!secret]-\n> Hello.\n", want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := content.ScanSecrets(fixture.source)
			if len(found) != fixture.want {
				t.Errorf("ScanSecrets() found %d callout(s), want %d, for:\n%q",
					len(found), fixture.want, fixture.source)
			}
		})
	}

	// And the write path, because a scanner that survives is not the same claim as
	// a one-byte splice that survives: `SetMarker` re-scans rather than reusing the
	// caller's offsets, so it walks the same loop on the same kind of source.
	const source = "Before.\n\n> [!secret]-\n> Hello.\n\nAfter"

	got, err := content.SetMarker(source, 0, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker() error = %v, want nil", err)
	}

	if want := "Before.\n\n> [!secret]+\n> Hello.\n\nAfter"; got != want {
		t.Errorf("SetMarker() = %q, want %q", got, want)
	}
}

// TestANestedCalloutIsReportedAndItsParentIsForcedCollapsed is §5.6's nesting
// rule as the scanner now implements it, and it replaces a header comment that
// claimed the opposite.
//
// The old comment said a nested callout "is found by its own header and reported
// separately, because a secret nested in a secret has no state a viewer could act
// on". Both halves were wrong: the body loop swallowed it, and the nesting does have
// state a viewer can act on — whether the nested body is ever rendered is decided
// entirely by the **outer** callout's marker.
//
// Three claims, separately asserted because each can hold while the others fail:
//
//  1. The nested callout is reported, with its own span and ordinal, so a ledger row
//     can name it. (Reported twice: `Ordinal` 0 and 1, and the second's `HeaderStart`
//     strictly after the first's.)
//  2. The two spans **nest**: the child's is contained in the parent's. That is what
//     `redact.go`'s containment filter relies on, and it is a property of the scan's
//     range bounds rather than of this rule.
//  3. The outer comes back `SecretCollapsed` **whatever its own marker byte says**,
//     and `MarkerOffset` still points at that byte — the reported state is forced,
//     the offsets still describe the file.
func TestANestedCalloutIsReportedAndItsParentIsForcedCollapsed(t *testing.T) {
	t.Parallel()

	// The exact fixture from the report: a revealed outer over a collapsed inner.
	const source = "> [!secret]+ Outer, revealed to the party.\n" +
		"> > [!secret]- The traitor is Captain Aldric.\n" +
		"\n" +
		"After.\n"

	found := content.ScanSecrets(source)
	if len(found) != 2 {
		t.Fatalf("ScanSecrets() found %d callout(s), want 2 — the nested one has to be "+
			"reported for a redactor to have anything to cut:\n%q", len(found), source)
	}

	outer, inner := found[0], found[1]

	if outer.Ordinal != 0 || inner.Ordinal != 1 {
		t.Errorf("ordinals = %d, %d; want 0, 1 in document order", outer.Ordinal, inner.Ordinal)
	}

	if outer.HeaderStart >= inner.HeaderStart {
		t.Errorf("the nested callout's HeaderStart (%d) is not after its parent's (%d), "+
			"so the ordinals are not document order", inner.HeaderStart, outer.HeaderStart)
	}

	if inner.HeaderStart < outer.HeaderStart || inner.BodyEnd > outer.BodyEnd {
		t.Errorf("the spans do not nest: parent [%d,%d), child [%d,%d)",
			outer.HeaderStart, outer.BodyEnd, inner.HeaderStart, inner.BodyEnd)
	}

	if outer.State != content.SecretCollapsed {
		t.Errorf("the outer callout is %q, want %q. There is no rendering of a public "+
			"outer callout that withholds a nested body, so a GM who wrote `+` over a "+
			"`-` made a mistake and every failure path in this subsystem resolves toward "+
			"hiding (§5.6.2)", string(rune(outer.State)), string(rune(content.SecretCollapsed)))
	}

	// **Forced state, real byte.** The two must not be confused: the report says
	// "collapsed" and the file still says `+`, and `SetMarker` needs the offset that
	// says which byte that is.
	if got := source[outer.MarkerOffset]; got != '+' {
		t.Errorf("the outer callout's MarkerOffset points at %q, want the `+` the author "+
			"wrote. Forcing the reported state must not move the offset", got)
	}

	if got := source[inner.MarkerOffset]; got != '-' {
		t.Errorf("the nested callout's MarkerOffset points at %q, want `-`. The offset "+
			"skips two levels of quoting, not one", got)
	}

	// **The text after a marker is the callout's *title*, not its body** — and
	// that is the whole of why this fixture matters beyond its nesting. A reader
	// skimming for `Body` would assume the secret text lived there; it does not, it
	// lives on the header line, which is inside `HeaderStart..HeaderEnd` and so
	// inside the cut. Asserted here so that "the redactor cut the title" is a
	// checked fact about where secret text can be, rather than an assumption.
	if inner.Title != "The traitor is Captain Aldric." {
		t.Errorf("the nested callout's title = %q, want the text after its marker",
			inner.Title)
	}

	if outer.Title != "Outer, revealed to the party." {
		t.Errorf("the outer callout's title = %q", outer.Title)
	}

	if inner.Body != "" {
		t.Errorf("the nested callout's body = %q, want empty: its text is on the header "+
			"line, which is a title and not a body", inner.Body)
	}
}

// TestANestedCalloutUnquotesEveryLevelOfItsBody is the other half of the depth
// work, and it is separate because a callout whose text all sits on its header line
// has an **empty body**, so the fixture above cannot see it at all.
//
// One level of `> ` short of correct leaves a `>` at the front of every body line,
// which is cosmetic — the text is still the author's. That is exactly why it needs
// its own test: `DerivedAnchor` hashes the **first line of the body** (§5.6.3), so a
// stray `>` changes the anchor of a nested secret, and §5.6.3's whole promise is
// that the derived form is stable. A wrong anchor is a ledger row that no longer
// resolves, which S7 re-associates by ordinal and logs as `secret_anchor_drift` —
// correct behaviour, paid for by a bug.
func TestANestedCalloutUnquotesEveryLevelOfItsBody(t *testing.T) {
	t.Parallel()

	const source = "> [!secret]+ Outer, with a body.\n" +
		"> > [!secret]- The traitor is Captain Aldric.\n" +
		"> > He replaced the eastern signal fire.\n" +
		"\n" +
		"After.\n"

	found := content.ScanSecrets(source)
	if len(found) != 2 {
		t.Fatalf("ScanSecrets() found %d callout(s), want 2, for:\n%q", len(found), source)
	}

	outer, inner := found[0], found[1]

	// The outer's body keeps the nested quote marker, and that is right rather than
	// a shortfall: one level of `> ` is the outer's own quoting, and the second `>` is
	// the author's — it is a block quote inside a block quote, which is what they
	// wrote. It is the *nested* callout's body where every level must come off.
	if want := "> [!secret]- The traitor is Captain Aldric.\n" +
		"> He replaced the eastern signal fire."; outer.Body != want {
		t.Errorf("the outer callout's body =\n%q\nwant\n%q — one level of `> ` stripped",
			outer.Body, want)
	}

	if want := "He replaced the eastern signal fire."; inner.Body != want {
		t.Errorf("the nested callout's body =\n%q\nwant\n%q — two levels of `> ` "+
			"stripped, and a stray `>` here would change §5.6.3's derived anchor",
			inner.Body, want)
	}
}

// TestTheNestingRuleDoesNotOverReach is the control, on the scanner's side.
//
// The forcing rule asks "does this callout's body contain another **callout**", and
// the version that over-reaches asks "does it contain the string `[!secret]`". Every
// campaign with a page documenting the syntax would lose that page's secrets from
// players — a regression that looks like the feature working.
//
// Four placings of the keyword that are not callout headers, and one that is. The
// fifth case is what makes the first four mean anything: if the rule cannot tell them
// apart, asserting "stays revealed" four times proves nothing.
func TestTheNestingRuleDoesNotOverReach(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		source string
		want   []content.SecretState
	}{
		"the keyword mid-sentence": {
			source: "> [!secret]+ Revealed.\n> The keeper wrote [!secret] and left.\n",
			want:   []content.SecretState{content.SecretRevealed},
		},
		"the keyword at the start of a quoted line, not a header": {
			source: "> [!secret]+ Revealed.\n> [!secret] is the marker.\n",
			want:   []content.SecretState{content.SecretRevealed},
		},
		"the keyword inside an inline code span": {
			source: "> [!secret]+ Revealed.\n> Write `[!secret]-` and it is a secret.\n",
			want:   []content.SecretState{content.SecretRevealed},
		},
		"a malformed marker": {
			source: "> [!secret]+ Revealed.\n> [!secret]? is not a marker.\n",
			want:   []content.SecretState{content.SecretRevealed},
		},
		"a real nested header, for contrast": {
			source: "> [!secret]+ Revealed.\n> > [!secret]- Inner.\n",
			want: []content.SecretState{
				content.SecretCollapsed,
				content.SecretCollapsed,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := content.ScanSecrets(fixture.source)
			if len(found) != len(fixture.want) {
				t.Fatalf("ScanSecrets() found %d callout(s), want %d, for:\n%q",
					len(found), len(fixture.want), fixture.source)
			}

			for index, want := range fixture.want {
				if found[index].State != want {
					t.Errorf("callout %d is %q, want %q",
						index, string(rune(found[index].State)), string(rune(want)))
				}
			}
		})
	}
}

// TestSetMarkerReachesBothOfTwoNestedSecrets is the confirmation, by test, that
// `SetMarker` is unaffected by nesting — which is worth establishing rather than
// reading, because the alternative (splicing every span and joining) would be an
// entirely reasonable refactor of the same function and would be wrong.
//
// **Four claims, each of which the others do not imply.** The first is the
// interesting one: `SetMarker` re-scans rather than reusing the caller's `Secret`,
// so the ordinal is resolved against a *fresh* walk of the same bytes. If the two
// nested secrets' ordinals were resolved from different walks they would disagree
// here, and neither of them would notice.
//
//   - Revealing the outer changes exactly one byte, and that byte is the outer's own.
//   - Revealing the inner changes exactly one byte, and that byte is the inner's.
//   - Neither disturbs the other: the outer's reveal leaves the inner's marker, and
//     the inner's leaves the outer's.
//   - Both together still leave every other byte alone — the two splices are
//     independent, so doing them one after the other is the same as doing them apart.
func TestSetMarkerReachesBothOfTwoNestedSecrets(t *testing.T) {
	t.Parallel()

	const source = "> [!secret]- Outer.\n> > [!secret]- Inner.\n"

	outerRevealed, err := content.SetMarker(source, 0, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker(0, revealed) error = %v, want nil", err)
	}

	if want := "> [!secret]+ Outer.\n> > [!secret]- Inner.\n"; outerRevealed != want {
		t.Errorf("revealing the outer = %q, want %q", outerRevealed, want)
	}

	innerRevealed, err := content.SetMarker(source, 1, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker(1, revealed) error = %v, want nil", err)
	}

	if want := "> [!secret]- Outer.\n> > [!secret]+ Inner.\n"; innerRevealed != want {
		t.Errorf("revealing the inner = %q, want %q", innerRevealed, want)
	}

	// And both, one after the other, against a fresh scan each time — which is what
	// S6's reveal endpoint actually does when it writes a page twice.
	both, err := content.SetMarker(outerRevealed, 1, content.SecretRevealed)
	if err != nil {
		t.Fatalf("SetMarker(1, revealed) after the outer error = %v, want nil", err)
	}

	if want := "> [!secret]+ Outer.\n> > [!secret]+ Inner.\n"; both != want {
		t.Errorf("revealing both = %q, want %q", both, want)
	}

	// **One byte, measured.** Every version differs from its predecessor by exactly
	// one byte, which is the property S5.6's one-byte-edit design exists for.
	for _, pair := range []struct{ name, before, after string }{
		{name: "the outer", before: source, after: outerRevealed},
		{name: "the inner", before: source, after: innerRevealed},
		{name: "both", before: outerRevealed, after: both},
	} {
		if differing := differingBytes(pair.before, pair.after); differing != 1 {
			t.Errorf("revealing %s changed %d bytes, want exactly 1", pair.name, differing)
		}
	}
}

// TestSetMarkerFindsTheMarkerWithoutASpaceAfterTheQuote is the off-by-one the
// depth-aware `MarkerOffset` fixed, and it is worth its own test because the
// constant it replaced was wrong on a *legal* callout.
//
// `>[!secret]-` is a callout — CommonMark's rule is `>` followed by an **optional**
// space, and `unquoteOne` has always accepted it. But `MarkerOffset` was
// `lineStart + 1 + 1 + len(calloutPrefix)`, which assumes two bytes of quoting. On
// `>[!secret]-` that lands on the `\n`, so `SetMarker` rewrote the line ending: the
// reveal did not take, and the GM's page rendered collapsed while the file said the
// secret was public. A reveal applied to the wrong byte is worse than a reveal that
// did not happen, because the ledger would say it happened.
func TestSetMarkerFindsTheMarkerWithoutASpaceAfterTheQuote(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct{ source, want string }{
		"no space after the quote": {
			source: ">[!secret]-\n> Body.\n",
			want:   ">[!secret]+\n> Body.\n",
		},
		"one space after the quote": {
			source: "> [!secret]-\n> Body.\n",
			want:   "> [!secret]+\n> Body.\n",
		},
		"a tab after the quote": {
			// CommonMark's optional space may be a tab, and Obsidian reads this as a
			// callout — so semiplane has to as well, or a secret written this way
			// renders as a plain block quote for everybody. The `unquoteOne` this
			// replaces had a comment claiming it ate tabs and code that did not.
			source: ">\t[!secret]-\n> Body.\n",
			want:   ">\t[!secret]+\n> Body.\n",
		},
		"indented inside a list item": {
			source: "- one\n\n  > [!secret]-\n  > Body.\n",
			want:   "- one\n\n  > [!secret]+\n  > Body.\n",
		},
		"two levels of quoting": {
			source: "> [!secret]-\n> > [!secret]-\n> > Body.\n",
			want:   "> [!secret]+\n> > [!secret]-\n> > Body.\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := content.SetMarker(fixture.source, 0, content.SecretRevealed)
			if err != nil {
				t.Fatalf("SetMarker() error = %v, want nil", err)
			}

			if got != fixture.want {
				t.Errorf("SetMarker() = %q, want %q", got, fixture.want)
			}
		})
	}
}

// differingBytes counts the positions at which two strings differ. It is the
// measurement behind "a reveal is one byte", asserted on nesting shapes where a
// splice that touched two bytes would look correct in a string comparison only if
// you already knew the answer.
func differingBytes(before, after string) int {
	differing := 0

	for index := range max(len(before), len(after)) {
		if character(before, index) != character(after, index) {
			differing++
		}
	}

	return differing
}

// character is `value`'s byte at `index`, or 0 past its end.
func character(value string, index int) byte {
	if index >= len(value) {
		return 0
	}

	return value[index]
}
