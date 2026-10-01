package content_test

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// knownKinds is a domain.PageKindRegistry over a fixed set, standing in for the
// plugin registry that phase 8 builds.
//
// A fake rather than the real registry because the property under test is the
// *shape* of the answer — a kind this build knows is honoured, one it does not is
// prose — and a test that needed the real registry would be testing phase 8's
// code as much as this phase's. What matters is that the lookup arrives as a
// parameter at all: the production registry implements the same one method, and a
// hardcoded list anywhere in this package would make these cases pass while the
// real behaviour was wrong.
type knownKinds map[string]bool

func (k knownKinds) HasPageKind(name string) bool {
	return k[name]
}

// registry is the fake used where the specific kinds do not matter: semiplane's
// own `token`, and nothing else.
func registry() knownKinds {
	return knownKinds{"token": true}
}

// maxBombGrowth bounds what parsing an alias bomb may allocate.
//
// Generous on purpose. The point is not the number but that there is one: without
// the parser's alias-expansion limit a nine-level bomb allocates gigabytes, so a
// tighter bound would only make this test fragile while a looser one would still
// be far below the failure it exists to catch. 16 MiB is roughly eighty times what
// a refused bomb actually costs and roughly a thousandth of what an expanded one
// does.
const maxBombGrowth = 16 << 20

// TestParseRefusesAnAliasBomb is the S-4.7 assertion, and it is an assertion
// rather than a belief: yaml.v3 documents an alias-expansion limit, and a limit
// nobody has checked is a limit that a future version may drop.
//
// The bomb is under a kilobyte, so the document size cap cannot be what stops it —
// that is deliberate, and it is what makes this the test for the *other* half of
// S-4.7's "alias-expansion limits, or cap document size". Without the parser's
// limit this test does not fail its assertion; it exhausts memory.
func TestParseRefusesAnAliasBomb(t *testing.T) {
	for _, levels := range []int{3, 9} {
		t.Run(strconv.Itoa(levels)+" levels", func(t *testing.T) {
			source := []byte("---\n" + aliasBomb(levels) + "---\n")

			if len(source) > content.MaxDocumentBytes {
				t.Fatalf("the bomb is %d bytes, so the size cap would be what refuses it, "+
					"and this test would not be testing the alias limit", len(source))
			}

			var before, after runtime.MemStats

			runtime.GC()
			runtime.ReadMemStats(&before)

			document := content.Parse(source, registry())

			runtime.ReadMemStats(&after)

			if document.FrontMatter.Err == nil {
				t.Fatal("Parse() accepted an alias bomb; the parser's expansion limit is " +
					"either absent or not enforced")
			}

			if !errors.Is(document.FrontMatter.Err, content.ErrMalformedFrontMatter) {
				t.Errorf("FrontMatter.Err = %v, want ErrMalformedFrontMatter",
					document.FrontMatter.Err)
			}

			// Inert, not partly honoured: the bomb's own `title` key is the bait,
			// and a block that was refused must not hand back even that.
			if document.FrontMatter.Kind != domain.KindProse {
				t.Errorf("FrontMatter.Kind = %q, want %q",
					document.FrontMatter.Kind, domain.KindProse)
			}

			if document.FrontMatter.Title != "" {
				t.Errorf("FrontMatter.Title = %q, want empty for a refused block",
					document.FrontMatter.Title)
			}

			if document.FrontMatter.Fields != nil {
				t.Error("FrontMatter.Fields is populated for a refused block; the parser's " +
					"partly-filled map reached the caller")
			}

			if grown := after.TotalAlloc - before.TotalAlloc; grown > maxBombGrowth {
				t.Errorf("parsing allocated %d bytes for a %d-byte document, want at most %d",
					grown, len(source), int64(maxBombGrowth))
			}
		})
	}
}

// TestParseKeepsOrdinaryAliases is the other half of the same claim. A limit is
// only worth having if it refuses expansion rather than aliases: front matter that
// reuses an anchor is ordinary YAML, and a parser that refused it would make the
// limit indistinguishable from having no YAML support.
func TestParseKeepsOrdinaryAliases(t *testing.T) {
	source := []byte("---\n" +
		"defaults: &defaults\n" +
		"  size: medium\n" +
		"  disposition: hostile\n" +
		"goblin:\n" +
		"  <<: *defaults\n" +
		"  name: Grinnax\n" +
		"title: Bestiary\n" +
		"---\n" +
		"A goblin.\n")

	document := content.Parse(source, registry())

	if document.FrontMatter.Err != nil {
		t.Fatalf("FrontMatter.Err = %v, want nil for a legal merge key",
			document.FrontMatter.Err)
	}

	if document.FrontMatter.Title != "Bestiary" {
		t.Errorf("FrontMatter.Title = %q, want %q", document.FrontMatter.Title, "Bestiary")
	}

	if _, ok := document.FrontMatter.Fields["defaults"]; !ok {
		t.Error("Fields is missing the anchor's key; an ordinary alias was dropped")
	}
}

// TestParseRefusesAnOversizedDocument covers the size cap, which is what bounds
// the work. Both halves matter and they are different halves: ReadDocument is
// what stops the bytes being buffered, and Parse is what stops a caller that read
// the file by some other route — C1's Target.ReadFile has no cap of its own —
// from handing an unbounded document to a parser.
func TestParseRefusesAnOversizedDocument(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		oversized := strings.Repeat("a", content.MaxDocumentBytes+1)

		_, err := content.ReadDocument(strings.NewReader(oversized))
		if !errors.Is(err, content.ErrDocumentTooLarge) {
			t.Errorf("ReadDocument() error = %v, want ErrDocumentTooLarge", err)
		}
	})

	t.Run("parse", func(t *testing.T) {
		source := []byte("---\nkind: token\n---\n" +
			strings.Repeat("prose. ", content.MaxDocumentBytes/7+1))

		document := content.Parse(source, registry())

		if !errors.Is(document.FrontMatter.Err, content.ErrDocumentTooLarge) {
			t.Errorf("FrontMatter.Err = %v, want ErrDocumentTooLarge", document.FrontMatter.Err)
		}

		// Refused before the block was even located, so nothing was interpreted --
		// not even the `kind` on the first line, which is well within every limit.
		if document.FrontMatter.Kind != domain.KindProse {
			t.Errorf("FrontMatter.Kind = %q, want %q; the cap was applied after parsing",
				document.FrontMatter.Kind, domain.KindProse)
		}

		if document.Present {
			t.Error("Present is true for a document that was refused before it was split")
		}
	})

	t.Run("at the limit", func(t *testing.T) {
		// The boundary is inclusive: a document of exactly the cap is accepted.
		// An off-by-one here would either refuse a real page or admit one byte more
		// than the policy says, and neither shows up anywhere else.
		document := content.Parse([]byte(strings.Repeat("a", content.MaxDocumentBytes)), registry())
		if document.FrontMatter.Err != nil {
			t.Errorf("FrontMatter.Err = %v, want nil for a document exactly at the limit",
				document.FrontMatter.Err)
		}
	})

	t.Run("front matter alone", func(t *testing.T) {
		// A block over its own cap is refused while the document is still inside
		// the document cap, which is what makes "the parser is never handed more
		// than frontMatterMaxBytes" a statement about the input.
		source := []byte("---\ntitle: " +
			strings.Repeat("x", 256<<10) +
			"\n---\nbody\n")

		document := content.Parse(source, registry())

		if !errors.Is(document.FrontMatter.Err, content.ErrDocumentTooLarge) {
			t.Errorf("FrontMatter.Err = %v, want ErrDocumentTooLarge", document.FrontMatter.Err)
		}

		if document.Body != "body\n" {
			t.Errorf("Body = %q, want the prose after the block; the split happened "+
				"before the cap was checked", document.Body)
		}
	})
}

// TestReadDocumentReadsWhatItCan is the positive half of ReadDocument, so the
// test above cannot pass on a function that always refuses.
func TestReadDocumentReadsWhatItCan(t *testing.T) {
	want := "---\ntitle: The Long Road\n---\nThe road is long.\n"

	data, err := content.ReadDocument(strings.NewReader(want))
	if err != nil {
		t.Fatalf("ReadDocument() error = %v, want nil", err)
	}

	if string(data) != want {
		t.Errorf("ReadDocument() = %q, want %q", data, want)
	}
}

// TestParseResolvesTheKind is S-3.3 and S-3.4: the kind is registry-backed, a page
// with no kind is prose, and an unknown kind degrades to prose *with its front
// matter intact*.
func TestParseResolvesTheKind(t *testing.T) {
	cases := []struct {
		name string
		// source is a whole document, delimiters included.
		source      string
		registry    domain.PageKindRegistry
		wantKind    domain.PageKind
		wantDeclare string
		wantTitle   string
		wantBody    string
	}{
		{
			name:        "no kind is prose",
			source:      "---\ntitle: The Long Road\n---\nThe road is long.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "",
			wantTitle:   "The Long Road",
			wantBody:    "The road is long.\n",
		},
		{
			name:        "registered kind is honoured",
			source:      "---\nkind: token\ntitle: Grinnax\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.PageKind("token"),
			wantDeclare: "token",
			wantTitle:   "Grinnax",
			wantBody:    "A goblin.\n",
		},
		{
			// The shape of the answer is the same whatever the kind's origin, and
			// that is the claim: a plugin's kind is honoured because the registry
			// said so, not because this repository knows its name.
			name:        "a plugin kind is honoured the same way",
			source:      "---\nkind: shadow-hexblade\n---\nA blade.\n",
			registry:    knownKinds{"shadow-hexblade": true},
			wantKind:    domain.PageKind("shadow-hexblade"),
			wantDeclare: "shadow-hexblade",
			wantTitle:   "",
			wantBody:    "A blade.\n",
		},
		{
			name:        "unknown kind degrades to prose",
			source:      "---\nkind: tokne\ntitle: Grinnax\nsize: small\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "tokne",
			wantTitle:   "Grinnax",
			wantBody:    "A goblin.\n",
		},
		{
			// The degradation is *only* the kind. Every other field is still
			// there, which is what lets a GM be told what they mistyped instead of
			// being handed a page that quietly lost its token.
			name:        "unknown kind leaves the rest of the front matter",
			source:      "---\nkind: tokne\nsize: small\ncover: portrait.png\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "tokne",
			wantTitle:   "",
			wantBody:    "A goblin.\n",
		},
		{
			// A `kind` that is not a string is a malformed field, not a malformed
			// block: the page's title and body must survive it, because refusing
			// the whole block would let one bad key cost an author their page.
			//
			// DeclaredKind is the marker rather than the empty string, and the
			// difference is the whole point of the field. An empty DeclaredKind is
			// what a page that never declared a kind looks like, so a GM reading a
			// validation report could not tell "you wrote a list" from "you wrote
			// nothing" — and the first is a mistake worth telling them about. The
			// marker's brackets also mean it can never be read as a kind name a
			// registry recognised, which would be the misreading that makes a GM
			// believe the page is a game object of some unknown type.
			name:        "a kind that is not a string is marked, not fatal",
			source:      "---\nkind: [token, scene]\ntitle: Grinnax\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "[not a string]",
			wantTitle:   "Grinnax",
			wantBody:    "A goblin.\n",
		},
		{
			// Same for a scalar: a mistyped type is still something to report, and
			// reproducing `42` would put author-controlled text into a field that
			// reaches a diagnostic message.
			name:        "a numeric kind is marked too",
			source:      "---\nkind: 42\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "[not a string]",
			wantTitle:   "",
			wantBody:    "A goblin.\n",
		},
		{
			// No folding and no trimming, for the reason domain.ParseRole gives: a
			// kind reaches a URL segment and a token-list filter, and a padded
			// value would have to be escaped in every one of them.
			name:        "a padded kind is not a kind",
			source:      "---\nkind: \"  token  \"\n---\nA goblin.\n",
			registry:    registry(),
			wantKind:    domain.KindProse,
			wantDeclare: "  token  ",
			wantTitle:   "",
			wantBody:    "A goblin.\n",
		},
		{
			// An empty registry is the "nothing installed" case and degrades rather
			// than panicking — phase 8 arrives late, and semiplane serves a wiki in
			// the meantime (S-14.8).
			name:        "an empty registry degrades everything",
			source:      "---\nkind: token\n---\nA goblin.\n",
			registry:    knownKinds{},
			wantKind:    domain.KindProse,
			wantDeclare: "token",
			wantTitle:   "",
			wantBody:    "A goblin.\n",
		},
		{
			name:        "a nil registry degrades everything",
			source:      "---\nkind: token\n---\nA goblin.\n",
			registry:    nil,
			wantKind:    domain.KindProse,
			wantDeclare: "token",
			wantTitle:   "",
			wantBody:    "A goblin.\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := content.Parse([]byte(testCase.source), testCase.registry)

			if document.FrontMatter.Err != nil {
				t.Fatalf("FrontMatter.Err = %v, want nil", document.FrontMatter.Err)
			}

			if document.FrontMatter.Kind != testCase.wantKind {
				t.Errorf("FrontMatter.Kind = %q, want %q",
					document.FrontMatter.Kind, testCase.wantKind)
			}

			if document.FrontMatter.DeclaredKind != testCase.wantDeclare {
				t.Errorf("FrontMatter.DeclaredKind = %q, want %q",
					document.FrontMatter.DeclaredKind, testCase.wantDeclare)
			}

			if document.FrontMatter.Title != testCase.wantTitle {
				t.Errorf("FrontMatter.Title = %q, want %q",
					document.FrontMatter.Title, testCase.wantTitle)
			}

			// The prose is always there, whatever the block said. This is the whole
			// of S-3.3's inertness: a page that is not a game object is still a
			// page.
			if document.Body != testCase.wantBody {
				t.Errorf("Body = %q, want %q", document.Body, testCase.wantBody)
			}
		})
	}
}

// TestParseKeepsTheFrontMatterOfADegradedPage is the "intact" half of the
// degradation, asserted separately because it is the half a later edit drops: it
// looks like tidying up to discard the keys nothing reads.
func TestParseKeepsTheFrontMatterOfADegradedPage(t *testing.T) {
	source := "---\nkind: tokne\ntitle: Grinnax\nsize: small\ncover: portrait.png\n---\nA goblin.\n"

	document := content.Parse([]byte(source), registry())

	if document.FrontMatter.Kind != domain.KindProse {
		t.Fatalf("FrontMatter.Kind = %q, want %q", document.FrontMatter.Kind, domain.KindProse)
	}

	for key, want := range map[string]string{
		"kind":  "tokne",
		"title": "Grinnax",
		"size":  "small",
		"cover": "portrait.png",
	} {
		got, ok := document.FrontMatter.Fields[key]
		if !ok {
			t.Errorf("Fields is missing %q; an unknown kind discarded the front matter", key)

			continue
		}

		if got != want {
			t.Errorf("Fields[%q] = %v, want %q", key, got, want)
		}
	}
}

// TestParseMalformedFrontMatterIsInert is S-3.3's second sentence: a malformed
// front-matter block in a lore page cannot break the loader.
//
// Every case produces a usable document. A loader that returns an error here is a
// loader where one author's typo hides a campaign's lore page from everybody else,
// which is the opposite of what a tolerant format is for.
func TestParseMalformedFrontMatterIsInert(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantBody string
	}{
		{
			name:     "not yaml at all",
			source:   "---\nkind: [unclosed\n---\nThe prose survives.\n",
			wantBody: "The prose survives.\n",
		},
		{
			name:     "a tab where yaml wants spaces",
			source:   "---\ntitle:\n\tGrinnax\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
		{
			// The parser rejects this one, which is the point: a mapping with the
			// same key twice has two answers and no rule for which wins, and
			// Obsidian writes files that people hand-edit.
			name:     "a duplicate key",
			source:   "---\ntitle: One\ntitle: Two\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
		{
			// Front matter that is a sequence rather than a mapping. Some front
			// matter is prose, and prose in a block is still a block whose content
			// did not interpret.
			name:     "a sequence rather than a mapping",
			source:   "---\n- one\n- two\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
		{
			name:     "a scalar rather than a mapping",
			source:   "---\njust a string\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
		{
			name:     "binary rubbish",
			source:   "---\n\x00\x01\x02\xff\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := content.Parse([]byte(testCase.source), registry())

			if !errors.Is(document.FrontMatter.Err, content.ErrMalformedFrontMatter) {
				t.Errorf("FrontMatter.Err = %v, want ErrMalformedFrontMatter",
					document.FrontMatter.Err)
			}

			if !document.Present {
				t.Error("Present is false; the block was found and refused, which is not " +
					"the same as there being no block")
			}

			// The raw block is recoverable, which is the whole point of keeping it:
			// the file that has to be fixed is in the file, but the block that has
			// to be diagnosed is here, and phase 4's broken-page report needs it.
			if document.Raw == "" {
				t.Error("Raw is empty; the text an author has to fix is unreachable")
			}

			if document.FrontMatter.Kind != domain.KindProse {
				t.Errorf(
					"FrontMatter.Kind = %q, want %q",
					document.FrontMatter.Kind,
					domain.KindProse,
				)
			}

			if document.FrontMatter.Fields != nil {
				t.Error("FrontMatter.Fields is populated; a refused block must not be " +
					"half-interpreted")
			}

			if document.Body != testCase.wantBody {
				t.Errorf("Body = %q, want %q", document.Body, testCase.wantBody)
			}
		})
	}
}

// TestParseWithoutFrontMatter covers the documents a campaign is mostly made of.
func TestParseWithoutFrontMatter(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantBody string
	}{
		{
			name:     "plain markdown",
			source:   "# The Long Road\n\nIt goes on.\n",
			wantBody: "# The Long Road\n\nIt goes on.\n",
		},
		{
			// A thematic break in the middle of a page is ordinary markdown and must
			// stay in the prose. Treating any `---` as a delimiter would cut the
			// page in half at its first horizontal rule.
			name:     "a horizontal rule in the body",
			source:   "Above.\n\n---\n\nBelow.\n",
			wantBody: "Above.\n\n---\n\nBelow.\n",
		},
		{
			// Leading whitespace is not front matter. Searching past it would find
			// the rule in the previous case and eat the prose above it.
			name:     "an indented delimiter",
			source:   "Above.\n\n  ---\n\nBelow.\n",
			wantBody: "Above.\n\n  ---\n\nBelow.\n",
		},
		{
			// An opening delimiter with no closing one is not a block, and the only
			// safe reading is prose: a file that opens with a rule is a page whose
			// first line is a rule.
			name:     "an unterminated block",
			source:   "---\ntitle: The Long Road\n\nIt goes on.\n",
			wantBody: "---\ntitle: The Long Road\n\nIt goes on.\n",
		},
		{
			name:     "an empty block",
			source:   "---\n---\nThe prose.\n",
			wantBody: "The prose.\n",
		},
		{
			name:     "a block and nothing else",
			source:   "---\ntitle: The Long Road\n---\n",
			wantBody: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := content.Parse([]byte(testCase.source), registry())

			if document.FrontMatter.Err != nil {
				t.Errorf("FrontMatter.Err = %v, want nil", document.FrontMatter.Err)
			}

			if document.Body != testCase.wantBody {
				t.Errorf("Body = %q, want %q", document.Body, testCase.wantBody)
			}
		})
	}
}

// TestParseEmptyBlockIsNotTheSameAsNoBlock covers the distinction Document.Present
// exists for. An author who wrote an empty block and an author who wrote none are
// different situations, and a validator that reports "this page declares nothing"
// should be able to tell them apart.
func TestParseEmptyBlockIsNotTheSameAsNoBlock(t *testing.T) {
	empty := content.Parse([]byte("---\n---\nProse.\n"), registry())
	if !empty.Present {
		t.Error("Present is false for an empty block")
	}

	if empty.FrontMatter.Err != nil {
		t.Errorf(
			"FrontMatter.Err = %v, want nil; an empty block is valid YAML",
			empty.FrontMatter.Err,
		)
	}

	if empty.FrontMatter.Fields == nil {
		t.Error("Fields is nil for an empty block; it cannot be distinguished from no block")
	}

	if len(empty.FrontMatter.Fields) != 0 {
		t.Errorf("Fields = %v, want empty", empty.FrontMatter.Fields)
	}

	none := content.Parse([]byte("Prose.\n"), registry())
	if none.Present {
		t.Error("Present is true for a document with no block")
	}

	if none.FrontMatter.Fields != nil {
		t.Error("Fields is non-nil for a document with no block")
	}
}

// TestParseToleratesUnknownKeys is the other half of S-3.3's tolerance, and it is
// the half that is easy to regress: a strict decoder would refuse a page because
// an Obsidian plugin wrote `cssclasses` into it, which is a page lost to a feature
// of somebody else's editor.
func TestParseToleratesUnknownKeys(t *testing.T) {
	source := "---\n" +
		"title: The Long Road\n" +
		"cssclasses: wide lore\n" +
		"aliases:\n" +
		"  - The Road\n" +
		"  - Long Road\n" +
		"tags: [travel, road]\n" +
		"updated: 1750000000\n" +
		"plugin-key:\n" +
		"  nested:\n" +
		"    anything: [1, 2, 3]\n" +
		"---\n" +
		"It goes on.\n"

	document := content.Parse([]byte(source), registry())

	if document.FrontMatter.Err != nil {
		t.Fatalf("FrontMatter.Err = %v, want nil; an unknown key refused the block",
			document.FrontMatter.Err)
	}

	for _, key := range []string{"cssclasses", "aliases", "tags", "updated", "plugin-key"} {
		if _, ok := document.FrontMatter.Fields[key]; !ok {
			t.Errorf("Fields is missing %q; unknown keys must be kept, not dropped", key)
		}
	}
}

// TestParseAcceptsEditorArtefacts is the CRLF and BOM case. Both produce a file
// whose front matter silently stopped working, which is the worst failure mode a
// tolerant format has: nothing errors, the page renders as prose, and the author
// has no idea why their `kind` is being ignored.
func TestParseAcceptsEditorArtefacts(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{name: "crlf", source: "---\r\nkind: token\r\ntitle: Grinnax\r\n---\r\nA goblin.\r\n"},
		{name: "bom", source: "\xef\xbb\xbf---\nkind: token\ntitle: Grinnax\n---\nA goblin.\n"},
		{
			name:   "bom and crlf",
			source: "\xef\xbb\xbf---\r\nkind: token\r\ntitle: Grinnax\r\n---\r\nA goblin.\r\n",
		},
		{
			// Trailing whitespace on a delimiter is what several editors leave
			// behind, and it must not make the block disappear.
			name:   "delimiters with trailing whitespace",
			source: "--- \nkind: token\ntitle: Grinnax\n---\t\nA goblin.\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := content.Parse([]byte(testCase.source), registry())

			if !document.Present {
				t.Fatal("Present is false; the block was not found")
			}

			if document.FrontMatter.Err != nil {
				t.Fatalf("FrontMatter.Err = %v, want nil", document.FrontMatter.Err)
			}

			if document.FrontMatter.Kind != domain.PageKind("token") {
				t.Errorf("FrontMatter.Kind = %q, want %q; the block was not interpreted",
					document.FrontMatter.Kind, domain.PageKind("token"))
			}

			if document.FrontMatter.Title != "Grinnax" {
				t.Errorf("FrontMatter.Title = %q, want %q", document.FrontMatter.Title, "Grinnax")
			}
		})
	}
}

// TestParseLeavesTheProseByteForByte is the property every other case above leans
// on: the body is the author's bytes, not a normalised copy of them. A renderer
// that receives rewritten line endings is a renderer whose ETag covers text the
// file does not contain.
func TestParseLeavesTheProseByteForByte(t *testing.T) {
	source := "---\nkind: token\n---\r\nA goblin.\r\n\r\n  Indented.\r\n"

	document := content.Parse([]byte(source), registry())

	want := "A goblin.\r\n\r\n  Indented.\r\n"
	if document.Body != want {
		t.Errorf("Body = %q, want %q", document.Body, want)
	}
}

// aliasBomb builds a YAML "billion laughs" document: each level anchors a sequence
// of nine aliases of the level below, so a parser that expands them faithfully
// materialises 9^levels values from a few hundred bytes.
//
// The fan-out is nine and the first level is literal, which is the standard shape
// and the one yaml.v3's ratio check is written against.
func aliasBomb(levels int) string {
	var out strings.Builder

	out.WriteString(`seed: &seed ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]` + "\n")

	previous := "seed"

	for level := range levels {
		name := "level" + strconv.Itoa(level)

		out.WriteString(name + ": &" + name + " [*" + previous)

		for range 8 {
			out.WriteString(",*" + previous)
		}

		out.WriteString("]\n")
		previous = name
	}

	// A key the author would plausibly have written, so the test can assert that a
	// refused block yields nothing rather than nothing-at-all by accident.
	out.WriteString("title: The Long Road\n")

	return out.String()
}
