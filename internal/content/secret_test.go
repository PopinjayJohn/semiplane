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
