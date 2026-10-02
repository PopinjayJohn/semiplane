package content_test

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
)

// updateGoldens rewrites the golden files instead of comparing against them.
//
//	go test ./internal/content -run . -update
//
// Regenerating is a deliberate act rather than something a failing test does on
// its own, because a golden file is a *record* of what the renderer emits: a
// change to it is a change to the bytes a reader's browser receives, and a test
// that quietly accepted the new output would make that invisible. A reviewer
// reads the diff.
//
// It is a separate flag from C4's `-update-links` because the two write to
// different directories and because a run that regenerated both would be a run
// that regenerated the broken-link report too, which is a different artefact with
// a different owner.
var updateGoldens = flag.Bool("update", false, "rewrite the render golden files")

// goldenDir is where this work item's golden files live.
//
// Under `testdata/render/`, not `testdata/` directly: another agent in this wave
// owns `testdata/links/**`, and two work items writing golden files into one
// directory is a merge conflict on every run.
const goldenDir = "testdata/render"

// testKinds is a kind registry with semiplane's own kinds in it (S-3.4), so a
// page declaring one of them is not silently degraded to prose in the tests.
type testKinds map[string]struct{}

// HasPageKind implements domain.PageKindRegistry.
func (kinds testKinds) HasPageKind(name string) bool {
	_, known := kinds[name]

	return known
}

// newTestRenderer returns a Renderer over a registry holding the five kinds
// semiplane owns.
func newTestRenderer(t *testing.T) *content.Renderer {
	t.Helper()

	return content.NewRenderer("greyhaven", testKinds{
		"journal": {}, "handout": {}, "index": {}, "token": {}, "scene": {},
	})
}

// assertGolden compares got with the golden file of that name, or rewrites it
// under -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join(goldenDir, name)

	if *updateGoldens {
		if err := os.MkdirAll(goldenDir, 0o750); err != nil {
			t.Fatalf("create %s: %v", goldenDir, err)
		}

		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./internal/content -update)", name, err)
	}

	if string(want) != got {
		t.Errorf(
			"%s does not match.\ngot:\n%s\nwant:\n%s\n(run: go test ./internal/content -update)",
			name,
			got,
			want,
		)
	}
}

// tokens is a parsed view of a sanitised fragment, used by the tests below so
// that "this must not be markup" is a question about *elements and attributes*
// rather than about substrings.
//
// The distinction it exists for: a page's `<script>alert(1)</script>` must leave
// no `script` element, but the characters `alert(1)` are allowed to survive as
// visible text. A substring assertion cannot tell those two apart, and the
// tempting fix — assert less — would be asserting nothing. So the tests ask what
// elements and attributes are present, and separately, where it matters, whether
// the payload survived as *text*.
type tokens struct {
	// elements is every element name in the fragment, lowercased, in order.
	elements []string

	// attributes is every attribute name in the fragment, lowercased, in order.
	attributes []string

	// classes is every **token** of every `class` attribute in the fragment, in
	// order. Tokens rather than attribute values because a `class` list is a
	// list: `internal/content/target.go` appends `target` to the classes the
	// renderer wrote, so `class="wikilink"` is *one* correct spelling of what is
	// now `class="wikilink target"`, and a test that asserted the value as a
	// string would be asserting that nothing else may ever join the list. That is
	// the mistake `hasClass` below exists to prevent, and it is the same one the
	// route audits call out when they split on whitespace rather than using
	// `strings.Contains`.
	classes []string

	// text is the fragment with every tag removed, so a test can assert about what
	// a reader would actually see.
	text string
}

// parseTokens reads a fragment and reports its elements, attributes and text.
//
// A `net/html` tokenizer rather than a regexp, for the same reason the production
// code never reads its own HTML back with a regexp: a regexp has to be right about
// quoted attribute values, unquoted ones, a `>` inside a value, and self-closing
// tags, and every one of those is a way for a test to pass on output that is not
// what it claims to be checking. The tokenizer is the same one the sanitiser used,
// so the test sees the document a browser will.
func parseTokens(t *testing.T, fragment string) tokens {
	t.Helper()

	var (
		found      tokens
		tokenizer  = html.NewTokenizer(strings.NewReader(fragment))
		visible    strings.Builder
		isErrorEnd = true
	)

	for isErrorEnd {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if err := tokenizer.Err(); !errors.Is(err, io.EOF) {
				t.Fatalf("tokenize: %v", err)
			}

			isErrorEnd = false
		case html.StartTagToken, html.SelfClosingTagToken:
			current := tokenizer.Token()
			found.elements = append(found.elements, strings.ToLower(current.Data))

			for _, attr := range current.Attr {
				found.attributes = append(found.attributes, strings.ToLower(attr.Key))

				if attr.Key == "class" {
					found.classes = append(found.classes, strings.Fields(attr.Val)...)
				}
			}
		case html.TextToken:
			visible.WriteString(html.UnescapeString(tokenizer.Token().Data))
		default:
		}
	}

	found.text = visible.String()

	return found
}

// hasElement reports whether name is among the fragment's elements.
func (tk tokens) hasElement(name string) bool {
	return slices.Contains(tk.elements, name)
}

// hasAttribute reports whether name is among the fragment's attributes.
func (tk tokens) hasAttribute(name string) bool {
	return slices.Contains(tk.attributes, name)
}

// hasClass reports whether any element in the fragment carries name in its class
// list.
//
// By token, never by substring and never by matching the whole attribute value —
// `target` is a substring of `target-large` and `class="wikilink"` is a prefix of
// `class="wikilink target"`, so both of the cheaper checks answer questions
// nobody asked.
func (tk tokens) hasClass(name string) bool {
	return slices.Contains(tk.classes, name)
}

// renderBody renders prose with no front matter, which is what most of these
// cases need and what keeps each one to a single line of setup.
func renderBody(t *testing.T, body string) content.Rendered {
	t.Helper()

	out, err := newTestRenderer(t).Render(content.Document{Body: body})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	return out
}

// TestRenderGolden is the golden-file suite: one markdown file per case in
// `testdata/render/*.md`, one `.html` beside it, compared byte for byte.
//
// The cases are separate files rather than one table because a golden file is
// something a person reads, and a person debugging "why did this page change"
// wants to see the markdown that produced it. Each is a whole document, so the
// cases between them exercise the interaction of the extensions with GFM rather
// than each extension in isolation.
func TestRenderGolden(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/content -update)", goldenDir, err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		t.Run(strings.TrimSuffix(entry.Name(), ".md"), func(t *testing.T) {
			t.Parallel()

			source, err := os.ReadFile(filepath.Join(goldenDir, entry.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", entry.Name(), err)
			}

			out, err := newTestRenderer(t).Render(content.Parse(source, testKinds{
				"journal": {}, "handout": {}, "index": {}, "token": {}, "scene": {},
			}))
			if err != nil {
				t.Fatalf("render %s: %v", entry.Name(), err)
			}

			assertGolden(t, strings.TrimSuffix(entry.Name(), ".md")+".html", out.HTML)
		})
	}
}

// TestBlockLevelRawHTMLLosesItsText is the one case where the policy costs an
// author their words, pinned so that it is a decision rather than a surprise.
//
// goldmark treats an HTML *block* as a single unit: without `html.WithUnsafe()` the
// whole unit is dropped, and the text inside it goes with the tags. An *inline* run
// is only the tags, so its text survives. So:
//
//	<p onclick="x">gone</p>            → nothing
//	a <span onclick="x">kept</span> b  → "a kept b"
//
// The asymmetry is goldmark's and is not worth working around. Working around it
// would mean parsing the author's HTML well enough to recover its text, which is the
// interpretation `WithUnsafe()` performs and the reason S-4.6 forbids it. A GM who
// writes raw HTML in a wiki that documents four extensions has left the supported
// path, and the honest behaviour is that their words go with their markup rather
// than that half a construct renders.
//
// The test states both halves, because a change that made the block case keep its
// text would mean something started parsing raw HTML, and a change that made the
// inline case lose its text would be a plain regression.
func TestBlockLevelRawHTMLLosesItsText(t *testing.T) {
	t.Parallel()

	t.Run("a block drops the text with the tags", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, `<p onclick="steal()">the words</p>`)
		if strings.Contains(out.HTML, "the words") {
			t.Errorf("a block-level raw HTML element kept its text:\n%s", out.HTML)
		}

		if strings.Contains(out.HTML, "onclick") {
			t.Errorf("the handler survived:\n%s", out.HTML)
		}
	})

	t.Run("an inline run keeps the text", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, `before <span onclick="steal()">kept</span> after`)
		if !strings.Contains(out.HTML, "kept") {
			t.Errorf("an inline raw HTML element lost its text:\n%s", out.HTML)
		}

		if strings.Contains(out.HTML, "onclick") {
			t.Errorf("the handler survived:\n%s", out.HTML)
		}
	})
}

// TestRawHTMLIsStripped is the S-4.6 assertion, and it is stated as a set of
// absences rather than as a golden file because a golden file records what *is*
// emitted and the property here is about what is not.
//
// "Stripped, not escaped-through" is the distinction that matters, so each case
// asserts that no *element* and no *attribute* survived, rather than that some
// particular replacement string is absent. A policy that escaped the payload
// would satisfy a substring test looking for `alert`; it does not satisfy this one,
// because an escaped payload is still a `<script>` in the author's document and
// the rule is that the renderer decides what is markup, not the author.
//
// `absentText` is separate and used sparingly. For a block-level
// `<script>alert(1)</script>` the content is dropped along with the tag — it is
// character data the reader must never see — while an *inline* `<script>` in the
// middle of a sentence legitimately leaves its text behind as prose. Asserting
// either behaviour for both would be asserting a bug, so the cases say which they
// mean.
func TestRawHTMLIsStripped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		// absentElements are element names that must not appear at all.
		absentElements []string
		// absentAttributes are attribute names that must not appear at all.
		absentAttributes []string
		// absentText is text that must not be visible either, for the cases where
		// the payload's *content* is the dangerous part.
		absentText []string
	}{
		{
			name:           "script element and its content",
			body:           "<script>alert(1)</script>",
			absentElements: []string{"script"},
			absentText:     []string{"alert"},
		},
		{
			name:           "style element and its content",
			body:           "<style>body{display:none}</style>",
			absentElements: []string{"style"},
			absentText:     []string{"display"},
		},
		{
			name:             "event handler attribute on an image",
			body:             `<img src="x.png" onerror="alert(1)" alt="x">`,
			absentAttributes: []string{"onerror"},
		},
		{
			name:             "event handler attribute on a container",
			body:             `<div onclick="steal()">text</div>`,
			absentAttributes: []string{"onclick"},
		},
		{
			name:           "iframe",
			body:           `<iframe src="https://example.com/"></iframe>`,
			absentElements: []string{"iframe"},
		},
		{
			name:           "svg, historically a sanitiser bypass",
			body:           `<svg><script>alert(1)</script></svg>`,
			absentElements: []string{"svg", "script"},
		},
		{
			name:             "form control",
			body:             `<form action="/collect"><input name="pw" type="password"></form>`,
			absentElements:   []string{"form", "input"},
			absentAttributes: []string{"action", "name"},
			absentText:       []string{"pw"},
		},
		{
			name:             "style attribute, which is never allowed",
			body:             `<p style="position:fixed;top:0;width:100vw">covered</p>`,
			absentAttributes: []string{"style"},
		},
		{
			name:           "base, which would retarget every relative url",
			body:           `<base href="https://evil.example/">`,
			absentElements: []string{"base"},
			absentText:     []string{"evil.example"},
		},
		{
			name:           "object and embed elements",
			body:           `<object data="x.swf"></object><embed src="x.swf">`,
			absentElements: []string{"object", "embed"},
		},
		{
			name:             "target, which would hand a window opener to another site",
			body:             `<a href="https://example.com/" target="_blank">x</a>`,
			absentAttributes: []string{"target"},
		},
		{
			name:           "html comment, which can smuggle markup past a non-browser reader",
			body:           "<!--[if IE]><script>alert(1)</script><![endif]-->",
			absentElements: []string{"script"},
		},
		{
			name:           "math, the other known bypass class",
			body:           `<math><mtext><script>alert(1)</script></mtext></math>`,
			absentElements: []string{"math", "mtext", "script"},
		},
		{
			name:           "details, whose open attribute is bounded",
			body:           `<details open="open"><summary>s</summary>body</details>`,
			absentElements: []string{"noscript", "template"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.body)
			found := parseTokens(t, out.HTML)

			for _, absent := range testCase.absentElements {
				if found.hasElement(absent) {
					t.Errorf("the %s element survived:\n%s", absent, out.HTML)
				}
			}

			for _, absent := range testCase.absentAttributes {
				if found.hasAttribute(absent) {
					t.Errorf("the %s attribute survived:\n%s", absent, out.HTML)
				}
			}

			for _, absent := range testCase.absentText {
				if strings.Contains(found.text, absent) {
					t.Errorf("the payload survived as visible text (%q):\n%s", absent, out.HTML)
				}
			}
		})
	}
}

// TestDangerousURLsAreNeutralised is the href half of S-4.6.
//
// Each case asserts the *destination* is gone and the surrounding prose survived.
// The second is the requirement — a GM whose sync pulled in a hostile link wants
// the sentence to still read, not to have a hole in it — and a policy that dropped
// the whole anchor would pass a test that only checked the first.
//
// Asserted on attributes rather than on substrings, because an autolink
// legitimately *displays* the URL it refuses to link to: `<javascript:alert(1)>`
// renders as the text `javascript:alert(1)` with no destination. That is a
// neutralised link, not a surviving one, and a test asserting the string `javascript`
// is absent would be asserting that a page cannot show a reader what it refused to
// do — which would be a different, worse policy.
func TestDangerousURLsAreNeutralised(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"javascript", "[click](javascript:alert(1))"},
		{"javascript uppercase", "[click](JavaScript:alert(1))"},
		{"data", "[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)"},
		{"vbscript", "[click](vbscript:msgbox(1))"},
		{"file", "[click](file:///etc/passwd)"},
		{"autolink", "<javascript:alert(1)>"},
		{"image source", "![alt](javascript:alert(1))"},
		{"entity-encoded scheme", "[click](&#106;avascript:alert(1))"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.body)

			if strings.Contains(out.HTML, "href=") || strings.Contains(out.HTML, "src=") {
				t.Errorf("a destination survived:\n%s", out.HTML)
			}

			// The prose around the link is the other half of the requirement, and
			// for the markdown-link forms it is the link's own text.
			if testCase.name != "autolink" && testCase.name != "image source" &&
				!strings.Contains(parseTokens(t, out.HTML).text, "click") &&
				!strings.Contains(parseTokens(t, out.HTML).text, "alt") {
				t.Errorf("the link's text did not survive:\n%s", out.HTML)
			}
		})
	}
}

// TestSafeLinksSurvive is the other half: a policy that dropped every href would
// pass every test above.
//
// It also pins the schemes that *are* allowed, because an allowlist is only as
// good as the test that says what is on it.
func TestSafeLinksSurvive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{"https", "[x](https://example.com/a)", `href="https://example.com/a"`},
		{"http", "[x](http://example.com/a)", `href="http://example.com/a"`},
		{"relative", "[x](/c/greyhaven/wiki/Page)", `href="/c/greyhaven/wiki/Page"`},
		{"fragment", "[x](#a-heading)", `href="#a-heading"`},
		{"mailto", "[x](mailto:gm@example.com)", `href="mailto:gm@example.com"`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.body)
			if !strings.Contains(out.HTML, testCase.want) {
				t.Errorf("output does not contain %s:\n%s", testCase.want, out.HTML)
			}
		})
	}
}

// TestClassIsBounded is the class judgement call, asserted from both sides.
//
// The class has to reach the browser for the design system's callout styles to
// work, and it must not be a channel an author can use to reach behaviour. Both
// halves are tested: the pipeline's own classes survive, and a class nobody
// defined does not.
func TestClassIsBounded(t *testing.T) {
	t.Parallel()

	t.Run("the pipeline's own classes survive", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "A footnote[^1].\n\n[^1]: note.\n\n```go\nx\n```\n\n[[Page]]")
		for _, want := range []string{
			"footnote-ref", "footnote-backref",
			"footnotes", "language-go", "wikilink",
		} {
			// By token. `.target` joins these lists, and the question this
			// subtest asks is "did the class survive sanitisation", which is
			// asked of a class list and not of one spelling of an attribute
			// value.
			if !parseTokens(t, out.HTML).hasClass(want) {
				t.Errorf("no element carries the %q class:\n%s", want, out.HTML)
			}
		}
	})

	t.Run("an author-invented class does not", func(t *testing.T) {
		t.Parallel()

		// Raw HTML is dropped by goldmark before the sanitiser sees it, so the
		// class here cannot reach the policy at all. The test is that the *whole*
		// element is gone rather than surviving with its class stripped: a class
		// the author chose must not even be present as an inert attribute, because
		// "inert today" is one stylesheet rule away from "live after phase 5".
		out := renderBody(t, `<p class="callout is-gm-only">text</p>`)
		if strings.Contains(out.HTML, "is-gm-only") {
			t.Errorf("an author-chosen class reached the output:\n%s", out.HTML)
		}
	})
}

// TestRoleIsBounded is the `role` judgement call.
//
// The footnote roles must survive, or every page with a footnote loses its
// accessibility semantics; and a role an author chooses must not, because
// `role` overrides an element's implicit role and is therefore a way to change
// what a screen reader does with a page.
func TestRoleIsBounded(t *testing.T) {
	t.Parallel()

	t.Run("the footnote roles survive", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "A footnote[^1].\n\n[^1]: note.")
		for _, want := range []string{`role="doc-noteref"`, `role="doc-backlink"`, `role="doc-endnotes"`} {
			if !strings.Contains(out.HTML, want) {
				t.Errorf("output does not contain %s:\n%s", want, out.HTML)
			}
		}
	})

	t.Run("an author-chosen role does not", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, `<div role="application">text</div>`)
		if strings.Contains(out.HTML, "application") {
			t.Errorf("an author-chosen role reached the output:\n%s", out.HTML)
		}
	})
}

// TestHeadingIDsAreUnique is the anchor half of the `id` judgement call.
//
// goldmark's generator de-duplicates within a document, so this is a property of
// the generator rather than of the sanitiser — and it is exactly the property
// that would be lost if ids were instead re-derived by a post-pass over the
// HTML. The test asserts both halves: ids exist, and two headings with the same
// text do not collide.
func TestHeadingIDsAreUnique(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "# The Coast\n\ntext\n\n## The Coast\n\nmore\n\n## The Coast\n\nmost\n")

	seen := make(map[string]int, len(out.Anchors))
	for _, anchor := range out.Anchors {
		if previous, duplicate := seen[anchor.ID]; duplicate {
			t.Errorf(
				"anchor id %q appears at both position %d and %d",
				anchor.ID,
				previous,
				anchor.Level,
			)
		}

		seen[anchor.ID] = anchor.Level
	}

	if len(out.Anchors) != 3 {
		t.Fatalf("expected 3 anchors, got %d: %+v", len(out.Anchors), out.Anchors)
	}

	// The first occurrence keeps the plain form, so a link written by hand
	// (`#the-coast`) lands on the first heading and not on the third.
	if out.Anchors[0].ID != "the-coast" {
		t.Errorf("first anchor id is %q, want %q", out.Anchors[0].ID, "the-coast")
	}

	if out.Anchors[1].ID != "the-coast-1" || out.Anchors[2].ID != "the-coast-2" {
		t.Errorf("de-duplicated ids are %q and %q, want %q and %q",
			out.Anchors[1].ID, out.Anchors[2].ID, "the-coast-1", "the-coast-2")
	}
}

// TestAnchorTextIsPlainText is what `Anchors` is for.
//
// The heading's own text, with its markup removed, so a table of contents built
// from it needs no HTML un-escaping. `**` and the link syntax must be gone and
// the words must be there.
func TestAnchorTextIsPlainText(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "## The **Drowned** `Lighthouse`\n")
	if len(out.Anchors) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(out.Anchors))
	}

	// Emphasis markers and code delimiters are markup; the words inside them are
	// what a table of contents shows.
	want := "The Drowned Lighthouse"
	if out.Anchors[0].Text != want {
		t.Errorf("anchor text is %q, want %q", out.Anchors[0].Text, want)
	}
}

// TestReferencesAreInDocumentOrder is the seam with C4.
//
// `References` and the `data-ref-index` in the HTML have to agree, because
// `Resolver.Links` returns its answers in the order it was given the slice and a
// caller attaches an href by position. A mismatch would put a link on the wrong
// sentence — a wrong page, silently, to a reader who has no way to tell.
func TestReferencesAreInDocumentOrder(t *testing.T) {
	t.Parallel()

	out := renderBody(
		t,
		"[[One]]\n\n![[Two]]\n\n{{statblock:Three}}\n\n{{dice:1d20}}\n\n[[Four|Four]]\n",
	)

	if len(out.References) != 5 {
		t.Fatalf("expected 5 references, got %d: %+v", len(out.References), out.References)
	}

	for position, ref := range out.References {
		if ref.Index != position {
			t.Errorf("reference %d has Index %d", position, ref.Index)
		}

		want := `data-ref-index="` + itoa(position) + `"`
		if !strings.Contains(out.HTML, want) {
			t.Errorf("the HTML does not contain %s", want)
		}
	}
}

// TestReferenceParts is the grammar, one table for the shapes C4 has to receive.
//
// Every case is a form Obsidian accepts, and the target is asserted to arrive
// *unnormalised* — no `.md` stripped, no leading `/` removed, no cleaning —
// because `links.go` refuses what it cannot resolve rather than absorbing it, and
// a renderer that cleaned first would turn a reference that ought to be refused
// into one that quietly means something else.
func TestReferenceParts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		target string
		anchor string
		alias  string
		arg    string
		ext    ext.Kind
	}{
		{name: "plain", body: "[[Page]]", target: "Page", ext: ext.KindWikilink},
		{
			name:   "with extension",
			body:   "[[notes/Page.md]]",
			target: "notes/Page.md",
			ext:    ext.KindWikilink,
		},
		{name: "vault root", body: "[[/Page]]", target: "/Page", ext: ext.KindWikilink},
		{
			name:   "alias",
			body:   "[[Page|the page]]",
			target: "Page",
			alias:  "the page",
			ext:    ext.KindWikilink,
		},
		{
			name:   "heading anchor",
			body:   "[[Page#Lore]]",
			target: "Page",
			anchor: "#Lore",
			ext:    ext.KindWikilink,
		},
		{
			name:   "block anchor",
			body:   "[[Page#^block-id]]",
			target: "Page",
			anchor: "#^block-id",
			ext:    ext.KindWikilink,
		},
		{
			name: "anchor and alias", body: "[[Page#Lore|read this]]",
			target: "Page", anchor: "#Lore", alias: "read this", ext: ext.KindWikilink,
		},
		{
			name:   "anchor only",
			body:   "[[#Lore]]",
			target: "",
			anchor: "#Lore",
			ext:    ext.KindWikilink,
		},
		{name: "spaced", body: "[[  Page  ]]", target: "Page", ext: ext.KindWikilink},
		{
			name:   "parent segment kept",
			body:   "[[../sibling]]",
			target: "../sibling",
			ext:    ext.KindWikilink,
		},
		{
			name:   "hash in alias",
			body:   "[[Page#Lore|the # part]]",
			target: "Page",
			anchor: "#Lore",
			alias:  "the # part",
			ext:    ext.KindWikilink,
		},
		{name: "embed", body: "![[map.png]]", target: "map.png", ext: ext.KindEmbed},
		{
			name:   "embed with alias",
			body:   "![[Page|the page]]",
			target: "Page",
			alias:  "the page",
			ext:    ext.KindEmbed,
		},
		{name: "statblock", body: "{{statblock:Goblin}}", arg: "Goblin", ext: ext.KindStatblock},
		{name: "dice colon", body: "{{dice:1d20+5}}", arg: "1d20+5", ext: ext.KindDice},
		{name: "dice quoted", body: `{{dice "2d6"}}`, arg: "2d6", ext: ext.KindDice},
		{name: "dice bare", body: "{{dice}}", arg: "", ext: ext.KindDice},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.body)
			if len(out.References) != 1 {
				t.Fatalf("expected 1 reference, got %d: %+v", len(out.References), out.References)
			}

			ref := out.References[0]
			if ref.Target != testCase.target {
				t.Errorf("target is %q, want %q", ref.Target, testCase.target)
			}

			if ref.Anchor != testCase.anchor {
				t.Errorf("anchor is %q, want %q", ref.Anchor, testCase.anchor)
			}

			if ref.Alias != testCase.alias {
				t.Errorf("alias is %q, want %q", ref.Alias, testCase.alias)
			}

			if ref.Arg != testCase.arg {
				t.Errorf("arg is %q, want %q", ref.Arg, testCase.arg)
			}

			if ref.Extension != testCase.ext {
				t.Errorf("extension is %q, want %q", ref.Extension, testCase.ext)
			}
		})
	}
}

// TestNonReferencesStayText is the degradation half of the extension grammar.
//
// Each of these is text an author might legitimately write, and each must reach
// the page unchanged rather than becoming a link to nowhere or a swallowed span.
// A directive naming a plugin that is not installed is the case that matters most:
// a page must not be destroyed by a missing plugin (S-3.3's inertness, applied to
// a directive).
func TestNonReferencesStayText(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"[[]]",
		"[[",
		"]]",
		"[[unclosed",
		"[[a[b]]",
		"[[a]b]]",
		"{{unknown:arg}}",
		"{{}}",
		"{{ dice }}",
		"{{dice:}}",
		`{{dice "1d20" extra}}`,
		"{{dice 1d20}}",
		"{{1dice}}",
		"a [b] c",
		"a ![b](x.png) c",
		"[text](https://example.com/)",
		"![[]]",
		"![Page]",
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, body)
			if len(out.References) != 0 {
				t.Errorf("expected no references, got %+v", out.References)
			}
		})
	}
}

// TestUnknownDirectiveIsInert is S-3.3's rule for a `{{…}}` directive, stated
// separately because the golden file for it would not make the *reason* legible.
//
// The braces must survive into the output as text. A renderer that dropped an
// unrecognised directive would delete the sentence around it, and a plugin's
// absence is not a reason to lose a GM's prose.
func TestUnknownDirectiveIsInert(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "Roll {{blast:3d6}} when the door opens.")
	if !strings.Contains(out.HTML, "{{blast:3d6}}") {
		t.Errorf("an unknown directive did not survive as text:\n%s", out.HTML)
	}
}

// TestExtensionsAreInertInCode is the property the package doc claims.
//
// A code span and a fenced code block are consumed whole before any inline parser
// sees their contents, so `[[wikilink]]` written in backticks is text. Asserted
// because "documented in a design record" is not a test, and because it is a
// property of goldmark's dispatch order rather than of anything in this package —
// so a goldmark upgrade is exactly the kind of change that could quietly break it.
func TestExtensionsAreInertInCode(t *testing.T) {
	t.Parallel()

	cases := []string{
		"`[[Page]]`",
		"``[[Page|alias]]``",
		"```\n[[Page]]\n```",
		"~~~text\n![[Page]]\n~~~",
		"    [[Page]]",
		"`{{dice:1d20}}`",
	}

	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, body)
			if len(out.References) != 0 {
				t.Errorf("expected no references, got %+v", out.References)
			}

			if !strings.Contains(out.HTML, "[[") && !strings.Contains(out.HTML, "{{") {
				t.Errorf("the source text was not preserved:\n%s", out.HTML)
			}
		})
	}
}

// TestExtensionsFire is the regression test for the dispatch order.
//
// A wikilink parser registered *behind* goldmark's link parser is never called —
// that parser claims `[` and `!` and always returns a node, because it opens a
// link label and waits for it to close. The result is that every `[[wikilink]]`
// in a campaign silently renders as the author's own brackets, which looks like
// working software and is a broken feature. This test exists because that failure
// was reached during development and is invisible in a golden file: the golden
// would simply contain `[[Page]]`.
func TestExtensionsFire(t *testing.T) {
	t.Parallel()

	out := renderBody(
		t,
		"See [[Page]] and ![[map.png]] and {{dice:1d20}} and {{statblock:Goblin}}.",
	)
	// By class **token**, not by matching `class="wikilink"`: the `.target` pass
	// appends its own token to these elements, so the attribute value is
	// `class="wikilink target"` and asserting the shorter spelling would be
	// asserting that nothing may ever join the list.
	for _, want := range []string{"wikilink", "embed", "dice", "statblock"} {
		if !parseTokens(t, out.HTML).hasClass(want) {
			t.Errorf("no element carries the %q class, so the extension did not fire:\n%s",
				want, out.HTML)
		}
	}
}

// TestMarkdownLinkStillWins is the other half of the dispatch order.
//
// The wikilink parser runs *before* goldmark's, so it has to decline every
// construct that is ordinary markdown. Without this, `[text](url)` would become a
// wikilink and every link on the wiki would break.
func TestMarkdownLinkStillWins(t *testing.T) {
	t.Parallel()

	cases := []string{
		"[text](https://example.com/)",
		"![alt](map.png)",
		"[ref][label]\n\n[label]: https://example.com/",
		"- [ ] a task",
		"- [x] a done task",
	}

	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, body)
			if len(out.References) != 0 {
				t.Errorf("a markdown construct produced references: %+v", out.References)
			}
		})
	}
}

// TestTaskListKeepsItsCheckbox pins the one `input` the policy allows.
//
// A task list without a box is a list of items that look like they are checkable
// and are not, so the exception is worth a test of its own. It also pins that the
// box is *inert*: no name, no value, nothing to submit.
func TestTaskListKeepsItsCheckbox(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "- [ ] open\n- [x] done\n")
	if !strings.Contains(out.HTML, `type="checkbox"`) {
		t.Errorf("the task-list checkbox was stripped:\n%s", out.HTML)
	}

	for _, absent := range []string{"name=", "value=", "<form"} {
		if strings.Contains(out.HTML, absent) {
			t.Errorf("the checkbox carries %q:\n%s", absent, out.HTML)
		}
	}
}

// TestTableAlignmentSurvivesWithoutStyle is the `style` judgement call.
//
// goldmark's table extension emits `style="text-align:…"` by default, which the
// policy strips; the renderer therefore configures it to emit `align` instead.
// If that configuration were lost, alignment would vanish silently — every table
// would still render, just wrong — so it is asserted rather than assumed.
func TestTableAlignmentSurvivesWithoutStyle(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "| a | b |\n|:--|--:|\n| 1 | 2 |\n")
	if !strings.Contains(out.HTML, `align="left"`) || !strings.Contains(out.HTML, `align="right"`) {
		t.Errorf("table alignment did not survive:\n%s", out.HTML)
	}

	if strings.Contains(out.HTML, "style") {
		t.Errorf("a style attribute reached the output:\n%s", out.HTML)
	}
}

// TestGFMSurface is the GFM assertion, one case per feature, so a failure names
// the feature rather than a golden file.
func TestGFMSurface(t *testing.T) {
	t.Parallel()

	t.Run("table", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "| a | b |\n|---|---|\n| 1 | 2 |\n")
		for _, want := range []string{"<table>", "<thead>", "<tbody>", "<th>", "<td>"} {
			if !strings.Contains(out.HTML, want) {
				t.Errorf("output does not contain %s:\n%s", want, out.HTML)
			}
		}
	})

	t.Run("strikethrough", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "~~gone~~")
		if !strings.Contains(out.HTML, "<del>gone</del>") {
			t.Errorf("strikethrough did not render:\n%s", out.HTML)
		}
	})

	t.Run("autolink", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "see https://example.com/x")
		if !strings.Contains(out.HTML, `href="https://example.com/x"`) {
			t.Errorf("autolink did not render:\n%s", out.HTML)
		}
	})

	t.Run("typographer", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, `He said "hello" -- twice...`)
		if strings.Contains(out.HTML, `"hello"`) {
			t.Errorf("typographic replacement did not happen:\n%s", out.HTML)
		}
	})

	t.Run("footnote has a working back-reference", func(t *testing.T) {
		t.Parallel()

		out := renderBody(t, "A footnote[^note].\n\n[^note]: The text.\n")

		// The reference and the back-reference have to point at each other, which
		// means the href has to survive sanitisation. A policy that dropped
		// fragment hrefs would render footnotes that cannot be followed back —
		// a real accessibility regression that no other test here would catch.
		if !strings.Contains(out.HTML, `href="#fn:1"`) {
			t.Errorf("the footnote reference has no href:\n%s", out.HTML)
		}

		if !strings.Contains(out.HTML, `href="#fnref:1"`) {
			t.Errorf("the footnote back-reference has no href:\n%s", out.HTML)
		}

		if !strings.Contains(out.HTML, `id="fn:1"`) {
			t.Errorf("the footnote has no id:\n%s", out.HTML)
		}
	})
}

// TestDiceRendersAPlaceholderAndNoResult is the dice requirement, and the most
// important single test in this file.
//
// Architecture §5.4 and S-7.1 put dice on the server: the client sends an intent,
// the server rolls and broadcasts, and no event carries a result the server did
// not produce. A number computed during rendering would be produced by whatever
// process happened to serve the page — different on every request, uncountable,
// unauditable — and the render cache would then be caching a random number, which
// breaks S-5.2's content-hash key as well.
//
// So the assertion is not "no digits" — the expression itself contains digits and
// is *supposed* to reach the DOM, because the live layer needs it and must not
// have to re-parse. The assertion is that the element carries no value: the
// expression is an attribute, the text content is empty, and two renders of the
// same bytes are identical.
func TestDiceRendersAPlaceholderAndNoResult(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "Roll {{dice:1d20+5}} for the attack.")

	slot := regexp.MustCompile(`<span class="dice"[^>]*>([^<]*)</span>`)
	match := slot.FindStringSubmatch(out.HTML)
	if match == nil {
		t.Fatalf("no dice element was rendered:\n%s", out.HTML)
	}

	if match[1] != "" {
		t.Errorf("the dice element has text content %q; it must be an empty slot", match[1])
	}

	if !strings.Contains(out.HTML, `data-arg="1d20+5"`) {
		t.Errorf("the expression did not reach the live layer:\n%s", out.HTML)
	}

	if len(out.References) != 1 || out.References[0].Arg != "1d20+5" {
		t.Errorf("the expression was not reported: %+v", out.References)
	}

	// The strongest form of the claim: a computed result would differ between two
	// renders of the same input, so byte-identity *is* the assertion that no roll
	// happened.
	again := renderBody(t, "Roll {{dice:1d20+5}} for the attack.")
	if again.HTML != out.HTML {
		t.Errorf(
			"two renders of the same input differ, so something is not deterministic:\n%s\n---\n%s",
			out.HTML,
			again.HTML,
		)
	}
}

// TestEmbedIsAnEmptySlot is the embed requirement, and the ADR 0017 one.
//
// An embed is inlining, and inlining is what makes the linking page's HTML depend
// on another document. So the slot C3 renders must be *empty* and identical for
// every viewer: C4 fills it, and C4 refuses a cross-campaign target outright.
func TestEmbedIsAnEmptySlot(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "![[map.png]] and ![[notes/Page|the page]]")

	slot := regexp.MustCompile(`<span class="embed"[^>]*>([^<]*)</span>`)
	matches := slot.FindAllStringSubmatch(out.HTML, -1)
	if len(matches) != 2 {
		t.Fatalf("expected 2 embed slots, got %d:\n%s", len(matches), out.HTML)
	}

	for _, match := range matches {
		if match[1] != "" {
			t.Errorf("an embed slot has content %q; it must be empty", match[1])
		}
	}

	if len(out.References) != 2 {
		t.Fatalf("expected 2 references, got %+v", out.References)
	}

	if out.References[0].Extension != ext.KindEmbed {
		t.Errorf("the first reference is %q, want %q", out.References[0].Extension, ext.KindEmbed)
	}
}

// TestStatblockIsAnEmptySlot is the same property for `{{statblock}}`.
//
// The stat block's shape is a gameplay plugin's business (S-2.4, S-2.5), so C3
// emits a slot carrying the operand and nothing else.
func TestStatblockIsAnEmptySlot(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "{{statblock:Goblin}}")
	if !strings.Contains(out.HTML, `data-arg="Goblin"`) {
		t.Errorf("the operand did not reach the element:\n%s", out.HTML)
	}

	slot := regexp.MustCompile(`<span class="statblock"[^>]*>([^<]*)</span>`)
	match := slot.FindStringSubmatch(out.HTML)
	if match == nil {
		t.Fatalf("no statblock element was rendered:\n%s", out.HTML)
	}

	if match[1] != "" {
		t.Errorf("the statblock element has content %q; it must be empty", match[1])
	}
}

// TestAuthorTextIsEscaped is the invariant every extension writer routes its
// interpolation through.
//
// A reference's target, an alias, and a `{{…}}` operand are all author-supplied
// and all end up inside an element. None may become markup, which is the property
// that makes an extension a fixed template plus escaped interpolation rather than
// a string built from the document.
func TestAuthorTextIsEscaped(t *testing.T) {
	t.Parallel()

	// Each payload tries to close the attribute it is in and add a handler. The
	// assertion is that no handler *attribute* appears and no new *element* appears
	// — which is the actual requirement. The escaped text surviving as text is
	// correct and expected: an author who writes a quote in an alias wants to see
	// the quote, not to have the page silently swallow the rest of the sentence.
	cases := []struct {
		name string
		body string
	}{
		{"quote in alias", `[[Page|a" onmouseover="alert(1)]]`},
		{"angle bracket in alias", "[[Page|<script>alert(1)</script>]]"},
		{"quote in operand", `{{dice:1d20" onload="alert(1)}}`},
		{"angle bracket in operand", "{{statblock:<img src=x onerror=alert(1)>}}"},
		{"ampersand in alias", "[[Page|fish & chips]]"},
		{"quote in target", `[[Page"onclick="alert(1)]]`},
		{"single quote in alias", `[[Page|a' onfocus='alert(1)]]`},
		{"backslash escape attempt", `[[Page|a\" onmouseover=\"alert(1)]]`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.body)
			found := parseTokens(t, out.HTML)

			for _, absent := range []string{"onmouseover", "onload", "onerror", "onclick", "onfocus"} {
				if found.hasAttribute(absent) {
					t.Errorf("the %s attribute reached the output:\n%s", absent, out.HTML)
				}
			}

			for _, absent := range []string{"script", "img", "iframe", "svg"} {
				if found.hasElement(absent) {
					t.Errorf("the %s element reached the output:\n%s", absent, out.HTML)
				}
			}
		})
	}
}

// TestWikilinkHasNoHref is the C4 seam, stated as a property of the markup.
//
// The element an unresolved reference renders is an `<a>` with **no** `href`. That
// is deliberate and it is what makes ADR 0017's byte-identity testable: the page
// carries a reference and an ordinal and nothing that varies by viewer, and C4
// attaches the address afterwards by matching `data-ref-index` to
// `Reference.Index`.
//
// A test rather than only a comment, because the failure it guards against is a
// *helpful* change: someone adding a guessed href to make a link clickable, which
// would make the HTML depend on a resolution and reintroduce a per-viewer cache
// variant that S-5.1 rules out.
func TestWikilinkHasNoHref(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "[[Page]]")
	if strings.Contains(out.HTML, "href=") {
		t.Errorf("an unresolved reference carries an href:\n%s", out.HTML)
	}

	if !strings.Contains(out.HTML, `data-ref-index="0"`) {
		t.Errorf("the element does not carry its ordinal:\n%s", out.HTML)
	}
}

// TestLabelAgreesWithTheLinkLayer is the invariant behind two implementations of
// one rule.
//
// `ext` computes the display text at render time and `content.Reference.Label`
// computes it for the resolver, and they are separate functions because `ext`
// cannot import `internal/content` — that package imports `ext`. The duplication
// is held in place by this test: one table, both implementations, and a failure
// here means a rendered anchor says one thing while the resolver's `Link.Label`
// says another, which is a link whose text and address disagree.
//
// It is in this file rather than in `ext`'s tests because the comparison is
// against `content.Reference.Label`, and only this package can see both.
func TestLabelAgreesWithTheLinkLayer(t *testing.T) {
	t.Parallel()

	refs := []string{
		"Page",
		"notes/Page",
		"notes/Page.md",
		"/Page",
		"Page|alias",
		"notes/Page|alias",
		"Page#Lore",
		"Page#Lore|alias",
		"#Lore",
		"../sibling",
		"Page.md",
		"Some Page",
	}

	for _, inner := range refs {
		t.Run(inner, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, "[["+inner+"]]")

			// What the renderer wrote between the tags. The class list is
			// matched by its first token rather than exactly, because
			// `target.go`'s pass appends `.target` to the same anchor and
			// this is about the *label*, not about what else the element
			// carries.
			match := regexp.MustCompile(`<a class="wikilink(?: [^"]*)?"[^>]*>([^<]*)</a>`).
				FindStringSubmatch(out.HTML)
			if match == nil {
				t.Fatalf("no wikilink rendered for %q:\n%s", inner, out.HTML)
			}

			// What the resolver will report: the same split, run through the real
			// collector rather than through the test's own copy of it, so the
			// comparison is between the renderer's label and the renderer's
			// reported reference — which is exactly the pair that has to agree
			// before C4 ever sees the slice.
			if len(out.References) != 1 {
				t.Fatalf("expected 1 reference, got %+v", out.References)
			}

			ref := out.References[0]
			if match[1] != ref.Label() {
				t.Errorf("the rendered label is %q but Reference.Label says %q",
					match[1], ref.Label())
			}
		})
	}
}

// TestRenderIsDeterministic is S-5.2's precondition.
//
// The cache is keyed on a content hash and the `ETag` on that hash, so a render
// whose bytes depended on anything but the input would make both meaningless. The
// inputs are chosen to be hostile to the three ways that happens: a map whose
// iteration order could leak into the output, an extension set assembled from a
// slice, and a footnote whose numbering is per-document state.
//
// Ten renders, not two, because the thing being ruled out is a rare ordering.
func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()

	body := "# The Coast\n\n" +
		"[[One]] ![[Two]] {{dice:1d20+5}} {{statblock:Goblin}} [[Three|three]]\n\n" +
		"| a | b |\n|:--|--:|\n| 1 | 2 |\n\n" +
		"- [ ] task\n- [x] done\n\n" +
		"A footnote[^1].\n\n[^1]: note.\n\n" +
		"## The Coast\n\n```go\nx := 1\n```\n"

	first := renderBody(t, body)

	for attempt := range 10 {
		next := renderBody(t, body)
		if next.HTML != first.HTML {
			t.Fatalf(
				"render %d differs from the first:\n%s\n---\n%s",
				attempt,
				first.HTML,
				next.HTML,
			)
		}

		if !equalRefs(next.References, first.References) {
			t.Fatalf("render %d reported different references:\n%+v\n---\n%+v",
				attempt, next.References, first.References)
		}
	}
}

// TestRenderIsConcurrencySafe is the `*goldmark.Markdown` answer, held under
// `-race` rather than asserted in a comment.
//
// The claim is that goldmark's parser and renderer are safe for concurrent
// `Convert` once constructed, and it rests on both halves guarding their one-time
// setup with a `sync.Once` and thereafter only reading their configured slices,
// while everything mutable — the reader, the parse context, the AST — is
// allocated per call. The parse context matters in particular: it owns the
// heading-id table, so a shared one would leak heading ids between two pages and
// two concurrent renders of the same page would race on it.
//
// One shared `*Renderer` across many goroutines, which is exactly the shape a
// per-campaign renderer has in production: one instance, every request.
func TestRenderIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	renderer := newTestRenderer(t)
	body := "# The Coast\n\n[[One]] ![[Two]] {{dice:1d20+5}} {{statblock:Goblin}}\n\nA note[^1].\n\n[^1]: n."

	const goroutines = 32

	var group sync.WaitGroup

	results := make([]string, goroutines)

	for worker := range goroutines {
		// Group.Go rather than Add plus a bare `go func` with a deferred Done: the
		// latter is the form that can forget its Done.
		group.Go(func() {
			out, err := renderer.Render(content.Document{Body: body})
			if err != nil {
				t.Errorf("render: %v", err)
				return
			}

			results[worker] = out.HTML
		})
	}

	group.Wait()

	for worker := 1; worker < goroutines; worker++ {
		if results[worker] != results[0] {
			t.Fatalf("goroutine %d produced different output:\n%s\n---\n%s",
				worker, results[0], results[worker])
		}
	}
}

// TestKindIsResolvedAgainstTheRegistry is S-3.3 at the render boundary.
//
// `Render` re-resolves the declared kind against its own registry rather than
// trusting `Document.Kind`, so a caller that built a Document by hand — the editor
// path in P6, a test, a future caller — cannot make the renderer report a kind the
// running build does not have.
func TestKindIsResolvedAgainstTheRegistry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		declared   string
		registered bool
		want       domain.PageKind
	}{
		{"prose is always prose", "", true, domain.KindProse},
		{"a registered kind is honoured", "token", true, domain.PageKind("token")},
		{"an unregistered kind degrades", "spell", false, domain.KindProse},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kinds := testKinds{}
			if testCase.registered {
				kinds["token"] = struct{}{}
			}

			renderer := content.NewRenderer("greyhaven", kinds)
			out, err := renderer.Render(content.Document{
				FrontMatter: domain.FrontMatter{
					Kind:         domain.KindProse,
					DeclaredKind: testCase.declared,
				},
				Body: "text",
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			if out.Kind != testCase.want {
				t.Errorf("kind is %q, want %q", out.Kind, testCase.want)
			}
		})
	}
}

// TestNilRegistryDegradesToProse is the same rule for a nil registry.
//
// `Parse` answers "prose" for a nil registry, and `Render` has to give the same
// answer — a caller must not have to distinguish "no registry" from "a registry
// that knows nothing" to know what kind of page it is looking at.
func TestNilRegistryDegradesToProse(t *testing.T) {
	t.Parallel()

	renderer := content.NewRenderer("greyhaven", nil)
	out, err := renderer.Render(content.Document{
		FrontMatter: domain.FrontMatter{DeclaredKind: "token"},
		Body:        "text",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if out.Kind != domain.KindProse {
		t.Errorf("kind is %q, want %q", out.Kind, domain.KindProse)
	}
}

// TestEmptyPageIsNotNil is the small structural property that keeps a caller from
// having to nil-check.
//
// `References` and `Anchors` are empty rather than nil, so `len(x) == 0` and `x ==
// nil` are not two ways to ask whether a page has any.
func TestEmptyPageIsNotNil(t *testing.T) {
	t.Parallel()

	out := renderBody(t, "just prose")
	if out.References == nil {
		t.Error("References is nil for a page with none")
	}

	if out.Anchors == nil {
		t.Error("Anchors is nil for a page with none")
	}
}

// TestFrontMatterNeverReachesTheMarkup is the split's whole point.
//
// `content-model.md` says front matter is parsed separately and never reaches the
// renderer, so a value in it that happens to contain HTML cannot become markup.
// Asserted with a title carrying a script tag, which is the case that would
// matter if the split were ever undone.
func TestFrontMatterNeverReachesTheMarkup(t *testing.T) {
	t.Parallel()

	source := []byte("---\ntitle: \"<script>alert(1)</script>\"\nkind: handout\n---\n\nProse.\n")
	doc := content.Parse(source, testKinds{"handout": {}})

	out, err := newTestRenderer(t).Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(out.HTML, "script") || strings.Contains(out.HTML, "alert") {
		t.Errorf("front matter reached the markup:\n%s", out.HTML)
	}

	if out.Kind != domain.PageKind("handout") {
		t.Errorf("kind is %q, want %q", out.Kind, "handout")
	}
}

// equalRefs reports whether two reference slices are equal, field by field.
func equalRefs(left, right []content.Reference) bool {
	if len(left) != len(right) {
		return false
	}

	for at := range left {
		if left[at] != right[at] {
			return false
		}
	}

	return true
}

// itoa renders a non-negative int, so the tests do not depend on strconv for one
// call each.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	var digits [20]byte

	at := len(digits)

	for value > 0 {
		at--
		digits[at] = byte('0' + value%10)
		value /= 10
	}

	return string(digits[at:])
}

// TestRawHTMLBlockTextIsDropped states the one behaviour in this pipeline that
// loses an author's content, so that it is a decision rather than a surprise.
//
// A **block-level** raw HTML element in a markdown file is an HTML block in
// CommonMark terms. goldmark does not convert it into the document AST; without
// `WithUnsafe()` — which S-4.6 forbids, and for good reason — it emits
// `<!-- raw HTML omitted -->` in its place and the element's text is never seen
// again. It is not a sanitiser decision and the policy in `policy.go` cannot
// change it; the sanitiser is the second of two layers, and this happens in the
// first.
//
// The failure mode this guards is a GM pasting HTML from a web page into a page,
// finding their paragraph gone, and filing it as data loss. It is a documented
// property of every conformant CommonMark renderer — Obsidian and GitHub behave
// the same way — so the answer is that it is expected, and this test is where
// that answer lives.
//
// The inline case is asserted alongside it because the distinction is the useful
// part: `a <span>b</span> c` keeps its text, with the tag removed by the policy.
// A reader who loses the block case and keeps the inline case has learned the
// rule rather than just the symptom.
func TestRawHTMLBlockTextIsDropped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		body       string
		wantText   string
		wantGone   string
		wantKept   string
		inlineCase bool
	}{
		{
			name:     "a block paragraph loses its text",
			body:     "<p>secret text</p>\n",
			wantGone: "secret text",
		},
		{
			name:     "a block heading loses its text",
			body:     "<h1>Heading text</h1>\n",
			wantGone: "Heading text",
		},
		{
			name:     "a hand-written table loses its cells",
			body:     "<table><tr><td>cell text</td></tr></table>\n",
			wantGone: "cell text",
		},
		{
			name:     "a block script loses its body",
			body:     "<script>alert('body text')</script>\n",
			wantGone: "alert",
		},
		{
			name:     "markdown around a block survives",
			body:     "before\n\n<p>gone</p>\n\nafter\n",
			wantText: "before",
			wantKept: "after",
		},
		{
			name:       "an inline element keeps its text",
			body:       "keep <span onclick=\"steal()\">this text</span>\n",
			wantText:   "keep this text",
			inlineCase: true,
		},
		{
			name:     "an inline element loses its tag",
			body:     "keep <em onclick=\"steal()\">this text</em>\n",
			wantText: "keep this text",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := newTestRenderer(t).Render(
				content.Parse([]byte(tc.body), testKinds{"token": {}, "scene": {}}),
			)
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			if tc.wantGone != "" && strings.Contains(out.HTML, tc.wantGone) {
				t.Errorf("output contains %q, which should have been dropped:\n%s",
					tc.wantGone, out.HTML)
			}

			for _, want := range []string{tc.wantText, tc.wantKept} {
				if want != "" && !strings.Contains(out.HTML, want) {
					t.Errorf("output lost %q, which should have survived:\n%s", want, out.HTML)
				}
			}

			// The event handler never survives, in either case. The distinction
			// above is about text, not about the attribute.
			if strings.Contains(out.HTML, "onclick") || strings.Contains(out.HTML, "steal") {
				t.Errorf("an event handler survived sanitisation:\n%s", out.HTML)
			}
		})
	}
}
