// Package ext_test tests the extension grammar and the HTML each extension
// writes, through the public API of the subpackage.
//
// The tests are in `ext_test` rather than in `ext` because everything worth
// asserting here is observable from outside: an extension's contract is the bytes
// it puts in the document and the node it puts in the tree, and neither needs
// access to a private. What that costs is that the scanners cannot be called
// directly, so each case goes through a real `goldmark.Markdown` — which is also
// the more honest test, because it exercises the *dispatch* and not only the
// scan, and dispatch is where a parser that never fires would hide.
package ext_test

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content/ext"
)

// indexPattern matches a `data-ref-index` attribute's value, whatever it is.
var indexPattern = regexp.MustCompile(`data-ref-index="\d+"`)

// convert renders source with a goldmark instance carrying exactly the given
// extensions.
//
// A fresh instance per case, and no package-level one, for the same reason
// `internal/content` has no package-level goldmark: `goldmark.New` builds a
// parser and a renderer, and sharing one across tests would mean a test could see
// another's extension set. That is the same argument `content.Registry` makes
// about a package-global map.
//
// The result has its `data-ref-index` values rewritten to `N`, because the ordinal
// is not this package's to assign. `internal/content`'s collector numbers the nodes
// in one walk *before* rendering, so that the ordinal in the element and the
// position in the `References` slice are the same number by construction; this
// converts straight through goldmark with no collector, so every node still
// carries its zero value. Document order is what this package is responsible for,
// and `TestOffsetsAreAbsolute` and `TestInnerIsVerbatim` assert it through the
// tree. The ordinal itself is asserted where it is assigned, in `internal/content`'s
// `TestReferencesAreInDocumentOrder`.
func convert(t *testing.T, source string, defs ...ext.Definition) string {
	t.Helper()

	var out bytes.Buffer

	if err := goldmark.New(goldmark.WithExtensions(ext.New(defs...))).
		Convert([]byte(source), &out); err != nil {
		t.Fatalf("convert: %v", err)
	}

	return indexPattern.ReplaceAllString(out.String(), `data-ref-index="N"`)
}

// withBuiltins renders source with semiplane's own four extensions.
func withBuiltins(t *testing.T, source string) string {
	t.Helper()

	return convert(t, source, ext.Builtins()...)
}

// TestWikilinkForms is the `[[…]]` grammar, one case per Obsidian form.
//
// Each case asserts the element and the label, because both are grammar
// decisions rather than rendering details: a wikilink's label is the author's own
// text, and ADR 0017 requires it to stay that way.
func TestWikilinkForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "plain",
			source: "[[Page]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with a path",
			source: "[[notes/Page]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with the extension Obsidian allows",
			source: "[[notes/Page.md]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page.md</a>`,
		},
		{
			name:   "from the vault root",
			source: "[[/notes/Page]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with an alias",
			source: "[[Page|the page]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">the page</a>`,
		},
		{
			name:   "with a heading anchor, which is not part of the label",
			source: "[[Page#Lore]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with a block anchor",
			source: "[[Page#^block-id]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with an anchor and an alias",
			source: "[[Page#Lore|read this]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">read this</a>`,
		},
		{
			name:   "into the current page, where the anchor is the label",
			source: "[[#Lore]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Lore</a>`,
		},
		{
			name:   "spaced, as a vault author writes it",
			source: "[[  Page  ]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
		{
			name:   "with a parent segment, kept for resolution to refuse",
			source: "[[../sibling]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">sibling</a>`,
		},
		{
			name:   "adjacent, which is two links",
			source: "[[One]][[Two]]",
			want: `<a class="wikilink" data-ext="wikilink" data-ref-index="N">One</a>` +
				`<a class="wikilink" data-ext="wikilink" data-ref-index="N">Two</a>`,
		},
		{
			name:   "inline, in the middle of a sentence",
			source: "See [[Page]] for detail.",
			want:   `See <a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a> for detail.`,
		},
		{
			name:   "a hash inside an alias belongs to the alias",
			source: "[[Page#Lore|the # part]]",
			want:   `<a class="wikilink" data-ext="wikilink" data-ref-index="N">the # part</a>`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := withBuiltins(t, testCase.source)
			if !strings.Contains(got, testCase.want) {
				t.Errorf("output does not contain\n\t%s\ngot:\n%s", testCase.want, got)
			}
		})
	}
}

// TestEmbedForms is the same grammar with the `!` in front, and a different
// element.
//
// The element is an empty `<span>` and the emptiness is the assertion, not an
// omission: an embed is an inline, and an inline is what would make this page's
// HTML depend on another document (ADR 0017). C4 fills the slot; nothing here may
// pre-empt it.
func TestEmbedForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "an asset",
			source: "![[chart.png]]",
			want:   `<span class="embed" data-ext="embed" data-ref-index="N"></span>`,
		},
		{
			name:   "a page",
			source: "![[notes/Page]]",
			want:   `<span class="embed" data-ext="embed" data-ref-index="N"></span>`,
		},
		{
			name:   "with an alias, which an empty slot has no use for",
			source: "![[Page|the page]]",
			want:   `<span class="embed" data-ext="embed" data-ref-index="N"></span>`,
		},
		{
			name:   "inline",
			source: "![[Page]] and [[Page]]",
			want: `<span class="embed" data-ext="embed" data-ref-index="N"></span>` +
				` and <a class="wikilink" data-ext="wikilink" data-ref-index="N">Page</a>`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := withBuiltins(t, testCase.source)
			if !strings.Contains(got, testCase.want) {
				t.Errorf("output does not contain\n\t%s\ngot:\n%s", testCase.want, got)
			}
		})
	}
}

// TestBraceForms is the `{{name}}` and `{{name:arg}}` grammar.
//
// Both argument spellings the documentation uses, because it uses both: the
// colon form and the quoted form. And the bare form, because a plugin may take no
// argument at all.
func TestBraceForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "no argument",
			source: "{{dice}}",
			want:   `<span class="dice" data-ext="dice" data-ref-index="N"></span>`,
		},
		{
			name:   "colon argument",
			source: "{{dice:1d20+5}}",
			want:   `<span class="dice" data-ext="dice" data-ref-index="N" data-arg="1d20+5"></span>`,
		},
		{
			name:   "quoted argument",
			source: `{{dice "2d6"}}`,
			want:   `<span class="dice" data-ext="dice" data-ref-index="N" data-arg="2d6"></span>`,
		},
		{
			name:   "a quoted argument with spaces in it",
			source: `{{dice "4d6 drop lowest"}}`,
			want:   `<span class="dice" data-ext="dice" data-ref-index="N" data-arg="4d6 drop lowest"></span>`,
		},
		{
			name:   "a statblock operand",
			source: "{{statblock:Goblin}}",
			want:   `<span class="statblock" data-ext="statblock" data-ref-index="N" data-arg="Goblin"></span>`,
		},
		{
			name:   "a statblock with a path",
			source: "{{statblock:creatures/Goblin}}",
			want: `<span class="statblock" data-ext="statblock" data-ref-index="N" ` +
				`data-arg="creatures/Goblin"></span>`,
		},
		{
			name:   "inline, in the middle of a sentence",
			source: "Roll {{dice:1d20}} for it.",
			want:   `Roll <span class="dice" data-ext="dice" data-ref-index="N" data-arg="1d20"></span> for it.`,
		},
		{
			name:   "a keyword with a digit and a dash in it",
			source: "{{dice_2:1d6}}",
			want:   `<span class="dice_2" data-ext="dice_2" data-ref-index="N" data-arg="1d6"></span>`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// The keyword cases beyond the built-ins install a definition of their
			// own, which is the point: adding an extension is naming one.
			defs := ext.Builtins()
			if strings.HasPrefix(testCase.source, "{{dice_2") {
				defs = append(defs, ext.Definition{
					Kind:    ext.Kind("dice_2"),
					Trigger: '{',
					Name:    "dice_2",
					Write:   ext.OperandSlot("span"),
				})
			}

			got := convert(t, testCase.source, defs...)
			if !strings.Contains(got, testCase.want) {
				t.Errorf("output does not contain\n\t%s\ngot:\n%s", testCase.want, got)
			}
		})
	}
}

// TestAddingAnExtensionIsOneValue is the mechanism's own claim.
//
// The package doc says phase 10 and phase 8 add an extension by adding a
// `Definition`, with no edit to the renderer, the dispatch, or the collector. A
// test rather than a promise, because the alternative failure is a fifth extension
// that requires a fifth case somewhere, and the first person to find out would be
// whoever wrote the secret extension.
func TestAddingAnExtensionIsOneValue(t *testing.T) {
	t.Parallel()

	custom := ext.Definition{
		Kind:    ext.Kind("roll"),
		Trigger: '{',
		Name:    "roll",
		Write:   ext.OperandSlot("span"),
	}

	got := convert(t, "Roll {{roll:3d6}} now.", append(ext.Builtins(), custom)...)
	want := `<span class="roll" data-ext="roll" data-ref-index="N" data-arg="3d6"></span>`
	if !strings.Contains(got, want) {
		t.Errorf("output does not contain\n\t%s\ngot:\n%s", want, got)
	}

	// And the built-ins still work alongside it, which is what "installed
	// together" has to mean.
	if with := withBuiltins(t, "[[Page]]"); !strings.Contains(with, `class="wikilink"`) {
		t.Errorf("the built-in extensions stopped working alongside a custom one:\n%s", with)
	}
}

// TestNonExtensionsStayText is the degradation half of both grammars.
//
// Everything here is text a person might legitimately write, and each has to
// reach the document unchanged. The two that matter most are an unknown
// `{{keyword}}` — a page must not be destroyed by a plugin that is not installed
// — and an unbalanced `[[`, which would otherwise swallow the rest of a page
// into one link.
func TestNonExtensionsStayText(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"[[]]",
		"[[",
		"]]",
		"[[unclosed",
		"[[a[b]]",
		"[[a]b]]",
		"{{}}",
		"{{}} and text",
		"{{unknown}}",
		"{{unknown:arg}}",
		"{{ dice }}",
		"{{dice:}}",
		"{{1dice}}",
		"{{-dice}}",
		`{{dice "1d20" extra}}`,
		"{{dice 1d20}}",
		"a [b] c",
		"a ![b](x.png) c",
		"[text](https://example.com/)",
		"![[]]",
		"![[a[b]]",
		"a lone } brace",
		"a lone { brace",
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			got := withBuiltins(t, body)
			for _, unwanted := range []string{"wikilink", `class="embed"`, `class="dice"`, `class="statblock"`} {
				if strings.Contains(got, unwanted) {
					t.Errorf("an extension fired on %q:\n%s", body, got)
				}
			}
		})
	}
}

// TestNestedBracesKeepTheOuterText is a case that is neither clean nor obvious, so
// it is pinned rather than left to chance.
//
// `{{{{dice}}}}` is not a nested directive and is not a failure either: the inner
// `{{dice}}` is a well-formed directive and fires, and the outer braces are text
// that happened to surround it. The scan declines the outer pair — a keyword may
// not start with `{` — and goldmark then reaches the inner pair and accepts it.
//
// The alternative would be a scan that consumed the outer braces and produced
// nothing, which would silently delete an author's characters. Rendering the
// directive inside the author's own braces is the answer that loses nothing.
func TestNestedBracesKeepTheOuterText(t *testing.T) {
	t.Parallel()

	got := withBuiltins(t, "{{{{dice}}}}")
	want := `{{<span class="dice" data-ext="dice" data-ref-index="N"></span>}}`
	if !strings.Contains(got, want) {
		t.Errorf("output does not contain\n\t%s\ngot:\n%s", want, got)
	}
}

// TestExtensionsAreInertInCode is the property the package doc claims.
//
// A code span and a fenced code block are consumed whole before any inline parser
// sees their contents. That is a property of goldmark's dispatch rather than of
// this package, which is exactly why it is a test: a goldmark upgrade is the kind
// of change that would break it silently.
func TestExtensionsAreInertInCode(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"`[[Page]]`",
		"``[[Page|alias]]``",
		"```\n[[Page]]\n```",
		"~~~\n![[Page]]\n~~~",
		"    [[Page]]",
		"    {{dice:1d20}}",
		"`{{dice:1d20}}`",
		"````\n```\n[[Page]]\n```\n````",
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			got := withBuiltins(t, body)
			for _, unwanted := range []string{"wikilink", `class="embed"`, `class="dice"`} {
				if strings.Contains(got, unwanted) {
					t.Errorf("an extension fired inside code:\n%s", got)
				}
			}

			// The author's own text is still there, as text.
			if !strings.Contains(got, "Page") && !strings.Contains(got, "dice") {
				t.Errorf("the source text did not survive:\n%s", got)
			}
		})
	}
}

// TestEscaping is the invariant every writer routes its interpolation through.
//
// A reference's target, an alias, and a `{{…}}` argument are all author-supplied
// and all end up inside an element. The assertion is about the *markup structure*:
// the author's bytes are present, and every character that could end an attribute
// value or open a tag has been escaped. A substring search for `onmouseover` would
// be the wrong question — the text `onmouseover` legitimately survives, as the
// escaped characters a GM typed — and the right question is whether it is inside a
// tag, which `tagAttributes` answers.
//
// The complementary half is that the payload cannot have *escaped* its context: the
// number of tags in the output must be what the template alone accounts for, so a
// payload that closed a tag and opened its own would show up as an extra one.
func TestEscaping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		// tags is the exact set of tag names the output may contain. Anything an
		// author wrote that became a tag would be a name not in here.
		tags []string
		// escaped is text that must appear in its escaped form, proving the bytes
		// were carried rather than dropped.
		escaped []string
	}{
		{
			name:    "a quote in an alias",
			body:    `[[Page|a" onmouseover="alert(1)]]`,
			tags:    []string{"p", "a"},
			escaped: []string{"&quot; onmouseover=&quot;alert(1)"},
		},
		{
			name:    "angle brackets in an alias",
			body:    "[[Page|<script>alert(1)</script>]]",
			tags:    []string{"p", "a"},
			escaped: []string{"&lt;script&gt;"},
		},
		{
			name:    "a quote in an operand",
			body:    `{{dice:1d20" onload="alert(1)}}`,
			tags:    []string{"p", "span"},
			escaped: []string{"1d20&quot; onload=&quot;alert(1)"},
		},
		{
			name:    "angle brackets in an operand",
			body:    "{{statblock:<img src=x onerror=alert(1)>}}",
			tags:    []string{"p", "span"},
			escaped: []string{"&lt;img src=x onerror=alert(1)&gt;"},
		},
		{
			// A single quote is *not* escaped, and that is correct rather than an
			// oversight: every attribute this package writes is delimited by a
			// double quote, and `'` cannot end a double-quoted value or open a tag.
			// Escaping it would put `&#39;` in a reader's page for nothing, and the
			// browser would render the same character either way.
			name:    "a single quote in an alias, which needs no escaping",
			body:    `[[Page|a' onfocus='alert(1)]]`,
			tags:    []string{"p", "a"},
			escaped: []string{"a' onfocus='alert(1)"},
		},
		{
			name:    "a quote in a target",
			body:    `[[Page"onclick="alert(1)]]`,
			tags:    []string{"p", "a"},
			escaped: []string{"Page&quot;onclick=&quot;alert(1)"},
		},
		{
			name:    "an ampersand in an alias",
			body:    "[[Page|fish & chips]]",
			tags:    []string{"p", "a"},
			escaped: []string{"fish &amp; chips"},
		},
		{
			name:    "bare angle brackets in an alias",
			body:    "[[Page|<>]]",
			tags:    []string{"p", "a"},
			escaped: []string{"&lt;&gt;"},
		},
		{
			name:    "angle brackets in a dice expression",
			body:    "{{dice:a<b>c}}",
			tags:    []string{"p", "span"},
			escaped: []string{"a&lt;b&gt;c"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := withBuiltins(t, testCase.body)

			for _, tag := range tagNames(got) {
				if !slices.Contains(testCase.tags, tag) {
					t.Errorf("the <%s> tag is not one this template writes:\n%s", tag, got)
				}
			}

			for _, want := range testCase.escaped {
				if !strings.Contains(got, want) {
					t.Errorf("output does not contain the escaped form %s:\n%s", want, got)
				}
			}
		})
	}
}

// tagNames returns every tag name in a fragment, in order, openers only.
//
// A tokenizer rather than a regexp, for the reason `internal/content`'s own
// `parseTokens` is: a regexp has to be right about quoted values, and every place
// it is not is a place this test would pass on output it is supposed to reject.
func tagNames(fragment string) []string {
	var (
		names []string
		scan  = html.NewTokenizer(strings.NewReader(fragment))
		done  bool
	)

	for !done {
		switch scan.Next() {
		case html.ErrorToken:
			done = true
		case html.StartTagToken, html.SelfClosingTagToken:
			names = append(names, strings.ToLower(scan.Token().Data))
		default:
		}
	}

	return names
}

// TestInnerIsVerbatim is the seam with `content`: the node carries the author's
// bytes and nothing derived from them.
//
// It is what lets `internal/content` split the reference with its own grammar —
// the one that has the campaign's rules in it — rather than this package guessing
// at it a second time. A test because the temptation to pre-parse here is strong:
// every consumer wants a target, and the target is one `strings.Cut` away.
func TestInnerIsVerbatim(t *testing.T) {
	t.Parallel()

	cases := []string{
		"Page",
		"notes/Page.md",
		"/Page",
		"Page#Lore",
		"Page#Lore|alias",
		"#Lore",
		"  spaced  ",
		"../sibling",
	}

	for _, inner := range cases {
		t.Run(inner, func(t *testing.T) {
			t.Parallel()

			collected := collectInners(t, "[["+inner+"]]")
			if len(collected) != 1 {
				t.Fatalf("expected 1 node, got %d", len(collected))
			}

			if collected[0].inner != inner {
				t.Errorf("inner is %q, want %q", collected[0].inner, inner)
			}

			if collected[0].kind != ext.KindWikilink {
				t.Errorf("kind is %q, want %q", collected[0].kind, ext.KindWikilink)
			}
		})
	}
}

// TestOffsetsAreAbsolute is what makes a broken-link report able to name a line.
//
// The node's offset is into the whole document, not into the line it was found on,
// so a caller can count newlines up to it. An offset relative to a line — which is
// what goldmark's reader naturally has — would be a second answer to "where is
// this", and the collector would have to guess which one it was given.
func TestOffsetsAreAbsolute(t *testing.T) {
	t.Parallel()

	source := "first line\n\nsecond [[Page]] line\n\nthird line\n"
	collected := collectInners(t, source)

	if len(collected) != 1 {
		t.Fatalf("expected 1 node, got %d", len(collected))
	}

	// Byte 19 is where the second `[[` opens: ten bytes of "first line\n\n", seven
	// of "second ", and then the bracket. Spelled out rather than computed from
	// `strings.Index`, because the point of the test is that the offset is an
	// absolute position into the *document* and a computed one would agree with a
	// relative implementation by accident.
	if got, want := collected[0].offset, 19; got != want {
		t.Errorf("offset is %d, want %d (the position of the second `[[`)", got, want)
	}
}

// TestKindsAreDistinct is the small structural property the collector's type
// switch on relies on.
//
// Two extensions sharing a kind would make `Reference.Extension` unable to say
// which produced a reference, and a broken-link report would group an embed with a
// wikilink because they had the same name.
func TestKindsAreDistinct(t *testing.T) {
	t.Parallel()

	seen := make(map[ext.Kind]bool, len(ext.Builtins()))

	for _, def := range ext.Builtins() {
		if seen[def.Kind] {
			t.Errorf("kind %q appears twice in Builtins()", def.Kind)
		}

		seen[def.Kind] = true
	}

	for _, want := range []ext.Kind{ext.KindWikilink, ext.KindEmbed, ext.KindStatblock, ext.KindDice} {
		if !seen[want] {
			t.Errorf("Builtins() does not include %q", want)
		}
	}
}

// TestNoExtensionsIsNotAnError is the empty case.
//
// A caller that builds a goldmark instance with no semiplane extensions — a test
// of goldmark itself, a future caller that wants plain CommonMark — must get a
// working parser. `ext.New()` with no definitions is the way to say that, and it
// has to not panic on the empty map.
//
// The assertion is on the *elements*, because the author's own `{{…}}` text
// legitimately survives: with nothing installed there is no keyword to match, so
// the braces are prose. A substring search for `dice` would fail on exactly the
// right behaviour.
func TestNoExtensionsIsNotAnError(t *testing.T) {
	t.Parallel()

	got := convert(t, "See [[Page]] and {{dice:1d20}}.")
	for _, tag := range tagNames(got) {
		if !slices.Contains([]string{"p"}, tag) {
			t.Errorf("the <%s> tag is not one plain CommonMark writes:\n%s", tag, got)
		}
	}

	if !strings.Contains(got, "{{dice:1d20}}") {
		t.Errorf("the author's braces did not survive as text:\n%s", got)
	}
}

// collected is one extension node, observed through the tree rather than through
// the node's own accessors, so a test can read the unexported fields this package
// keeps for itself.
type collected struct {
	kind   ext.Kind
	inner  string
	offset int
}

// collectInners walks a converted document and returns every extension node in
// document order.
//
// The walk rather than a hook, because goldmark has no "node visited" callback a
// caller can install, and adding one to the public API for a test would be a
// production change made for a test.
func collectInners(t *testing.T, source string) []collected {
	t.Helper()

	reader := text.NewReader([]byte(source))
	tree := goldmark.New(goldmark.WithExtensions(ext.New(ext.Builtins()...))).
		Parser().
		Parse(reader)

	var found []collected

	err := gast.Walk(tree, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}

		if typed, isExtension := node.(*ext.Node); isExtension {
			found = append(found, collected{
				kind:   typed.Extension(),
				inner:  typed.Inner(),
				offset: typed.Offset(),
			})
		}

		return gast.WalkContinue, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	return found
}
