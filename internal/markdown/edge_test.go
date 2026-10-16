package markdown

// Adversarial edge cases for the content pipeline. All tests assert
// observed stock behavior (goldmark v2.1.6, Unsafe off); where a case
// documents a known implementation bug instead of desired behavior it is
// marked with a BUG comment giving input/got/want, and the assertion
// pins the current output so the suite stays green without a fix.

import (
	"strings"
	"testing"
)

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\nfull output:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, rejects ...string) {
	t.Helper()
	for _, r := range rejects {
		if strings.Contains(got, r) {
			t.Errorf("output must not contain %q\nfull output:\n%s", r, got)
		}
	}
}

func TestEdgeWikilinkAnchorOnlyLiteral(t *testing.T) {
	// Page part is empty, so the parser leaves the span literal.
	got := convert(t, RoleGM, "a [[#sec]] b\n")
	mustContain(t, got, "[[#sec]]")
	mustNotContain(t, got, "wikilink", "<a ")
}

func TestEdgeWikilinkMultilineLiteral(t *testing.T) {
	// The wikilink parser only matches single-line [[...]] spans.
	got := convert(t, RoleGM, "a [[foo\nbar]] b\n")
	mustContain(t, got, "[[foo")
	mustNotContain(t, got, `data-testid="wikilink"`)
}

func TestEdgeWikilinkTrailingBrackets(t *testing.T) {
	// First ]] closes the link; the remainder is literal prose.
	got := convert(t, RoleGM, "[[a]]b]]\n")
	mustContain(t, got, `href="/notes/a">a</a>b]]`)
}

func TestEdgeGMUnclosedSwallowsLaterBlocks(t *testing.T) {
	// Fail-closed: an unclosed opener swallows headings and paragraphs
	// to EOF. The player keeps only the pre-opener prose.
	src := "public\n\n:::gm\nsecret\n\n# heading after\n\npara after\n"
	mustNotContain(t, convert(t, RolePlayer, src),
		"secret", "heading after", "para after", "gm-block")
	mustContain(t, convert(t, RolePlayer, src), "public")
	gm := convert(t, RoleGM, src)
	mustContain(t, gm, "secret", "heading after", "para after", `data-testid="gm-block"`)
}

func TestEdgeGMInsideBlockquote(t *testing.T) {
	// The opener parses inside a quote; stripping still removes the
	// secret subtree for players while keeping the quote shell.
	src := "> :::gm\n> secret\n"
	gm := convert(t, RoleGM, src)
	mustContain(t, gm, "<blockquote>", `data-testid="gm-block"`, "secret")
	player := convert(t, RolePlayer, src)
	mustContain(t, player, "<blockquote>")
	mustNotContain(t, player, "secret", "gm-block")
}

func TestEdgeGMInsideFencedCode(t *testing.T) {
	// Fence content is opaque: openers/closers inside stay literal code
	// and remain visible to both roles (no stripping applies).
	for _, src := range []string{
		"```\n:::gm\nsecret\n:::\n```\n",
		"```text\n:::gm titled\nsecret\n:::\n```\n",
	} {
		for _, role := range []Role{RoleGM, RolePlayer} {
			got := convert(t, role, src)
			mustContain(t, got, "<pre><code", ":::gm")
			mustNotContain(t, got, "gm-block")
		}
	}
	if got := convert(t, RolePlayer, "```\n:::gm\nsecret\n:::\n```\n"); !strings.Contains(got, "secret") {
		t.Errorf("fenced code must stay visible to players:\n%s", got)
	}
}

func TestEdgeGMNestedOpeners(t *testing.T) {
	// An opener line inside a block body opens a nested GMBlock; the
	// first ::: closes the inner block. Both sections are stripped for
	// players, so the inner secret never leaks.
	src := ":::gm outer\n:::gm inner\ninner secret\n:::\n"
	gm := convert(t, RoleGM, src)
	if n := strings.Count(gm, `data-testid="gm-block"`); n != 2 {
		t.Errorf("want 2 nested gm-block sections, got %d:\n%s", n, gm)
	}
	mustContain(t, gm, "inner secret")
	player := convert(t, RolePlayer, src)
	mustNotContain(t, player, "inner secret", "gm-block")
	// NOTE: content after the first closer ("outer secret" + trailing
	// ::: in longer inputs) is outside any block and renders visibly to
	// both roles; nesting is malformed input, not a hiding mechanism.
}

func TestEdgeCalloutAtEOFNoBody(t *testing.T) {
	// Bare marker with no trailing newline and no body: title falls back
	// to the capitalized type, body is empty, nothing is dropped.
	for _, src := range []string{"> [!note]", "> [!note]\n"} {
		got := convert(t, RoleGM, src)
		mustContain(t, got,
			`<div class="callout callout-note" data-testid="callout" data-callout="note">`,
			`<p class="callout-title">Note</p>`)
	}
}

func TestEdgeCalloutThenNonQuoteLine(t *testing.T) {
	// Lazy paragraph continuation (blockquote semantics): a non-quote
	// line directly after the body is absorbed into the callout.
	got := convert(t, RoleGM, "> [!note] T\n> body\nplain line\n")
	mustContain(t, got, `data-testid="callout"`, "body", "plain line")
}

func TestEdgeNestedQuoteCallout(t *testing.T) {
	// One quote level is consumed by blockquote; the inner marker still
	// opens a callout nested inside it.
	got := convert(t, RoleGM, ">> [!note] T\n>> body\n")
	mustContain(t, got, "<blockquote>", `data-testid="callout"`, "body")
}

func TestEdgeStatblockExtraColons(t *testing.T) {
	// Only the first colon splits key from value.
	got := convert(t, RoleGM, "```ad-statblock\nkey: value: extra-colons\n```\n")
	mustContain(t, got, `<div class="statblock-row"><dt>key</dt><dd>value: extra-colons</dd></div>`)
}

func TestEdgeDiceFenceEmpty(t *testing.T) {
	// Empty content is a valid (empty) formula, not an error.
	got := convert(t, RoleGM, "```dice\n```\n")
	mustContain(t, got, `data-testid="dice-roll"`, `data-formula=""`)
}

func TestEdgeFenceInfoCaseInsensitive(t *testing.T) {
	// Info-string match is lowercased before comparison.
	got := convert(t, RoleGM, "```Ad-Statblock\nname: Rat\n```\n")
	mustContain(t, got,
		`<div class="ad-statblock" data-testid="statblock">`,
		`<div class="statblock-row"><dt>name</dt><dd>Rat</dd></div>`)
}

func TestEdgeCRLFDocument(t *testing.T) {
	// CRLF line endings throughout: ordinary blocks, wikilinks, tables,
	// fences and titled markers all parse; frontmatter splits cleanly.
	src := "---\r\ntitle: T\r\n---\r\npara with [[tavern]] link\r\n\r\n" +
		"| A | B |\r\n|---|---|\r\n| 1 | 2 |\r\n\r\n" +
		"```ad-statblock\r\nname: Rat\r\n```\r\n\r\n" +
		":::gm Secret\r\nsecret\r\n:::\r\n\r\n" +
		"> [!note] T\r\n> body\r\n"
	doc, err := ParseFrontmatter([]byte(src))
	if err != nil {
		t.Fatalf("ParseFrontmatter(CRLF): %v", err)
	}
	if doc.Meta["title"] != "T" {
		t.Errorf("CRLF frontmatter meta = %#v", doc.Meta)
	}
	gm := convert(t, RoleGM, string(doc.Body))
	mustContain(t, gm,
		`href="/notes/tavern"`, "<table>",
		`data-testid="statblock"`, `data-testid="gm-block"`, "secret",
		`data-testid="callout"`)
	player := convert(t, RolePlayer, string(doc.Body))
	mustContain(t, player, `href="/notes/tavern"`, "<table>")
	mustNotContain(t, player, "secret", "gm-block")
}

// Fixed (was BUG md-crlf-bare-markers): bare (title-less) :::gm openers
// and bare > [!type] callout markers are recognized under CRLF, like
// their LF forms -- the end-of-marker checks accept CR. Regression test:
// a bare :::gm under CRLF must strip for players, never leak as prose.
func TestEdgeCRLFBareMarkers(t *testing.T) {
	gmPlayer := convert(t, RolePlayer, "para\r\n\r\n:::gm\r\nsecret\r\n:::\r\n")
	mustNotContain(t, gmPlayer, "gm-block", "secret")
	mustContain(t, gmPlayer, "para")
	gmGM := convert(t, RoleGM, "para\r\n\r\n:::gm\r\nsecret\r\n:::\r\n")
	mustContain(t, gmGM, `data-testid="gm-block"`, "secret")
	co := convert(t, RoleGM, "> [!note]\r\n> body\r\n")
	mustContain(t, co, `data-testid="callout"`)
	mustNotContain(t, co, "<blockquote>")
}

func TestEdgeFrontmatterSemiplaneNamespace(t *testing.T) {
	// The semiplane: namespace mapping survives verbatim in Meta.
	doc, err := ParseFrontmatter([]byte("---\nsemiplane:\n  channel: notes\n  order: 3\ntitle: T\n---\nbody\n"))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	ns, ok := doc.Meta["semiplane"].(map[string]any)
	if !ok {
		t.Fatalf("semiplane namespace = %#v, want map[string]any", doc.Meta["semiplane"])
	}
	if ns["channel"] != "notes" || ns["order"] != 3 {
		t.Errorf("semiplane namespace = %#v", ns)
	}
	if string(doc.Body) != "body\n" {
		t.Errorf("body = %q", doc.Body)
	}
}

func TestEdgeHTMLCommentOmitted(t *testing.T) {
	// Unsafe is off: even HTML comments render as the omission marker,
	// never passthrough — block and inline alike.
	for _, src := range []string{
		"<!-- a comment -->\n",
		"para <!-- c --> more\n",
	} {
		got := convert(t, RoleGM, src)
		mustContain(t, got, "<!-- raw HTML omitted -->")
		mustNotContain(t, got, "<!-- a comment -->", "<!-- c -->")
	}
}

func TestEdgeJavascriptURLSanitized(t *testing.T) {
	// Stock goldmark v2.1.6 link rendering drops javascript: URLs: the
	// href renders empty. This pins observed renderer behavior.
	got := convert(t, RoleGM, "a [x](javascript:alert(1)) b\n")
	mustNotContain(t, got, "javascript:")
	mustContain(t, got, `<a href="">x</a>`)
}

func TestEdgeTableAlignment(t *testing.T) {
	got := convert(t, RoleGM, "| L | C | R |\n|:---|:---:|---:|\n| a | b | c |\n")
	mustContain(t, got,
		`<th style="text-align:left">L</th>`,
		`<th style="text-align:center">C</th>`,
		`<th style="text-align:right">R</th>`,
		`<td style="text-align:left">a</td>`,
		`<td style="text-align:center">b</td>`,
		`<td style="text-align:right">c</td>`)
}
