package content_test

// Tests for `target.go` — the `.target` pass (ADR 0037).
//
// The gate this file answers to is `TestEveryRouteCarriesTheTargetClassOnEveryFocusStop`
// in `internal/httpapi/wiki`, and that gate is what found the defect: five focus
// stops inside `div[data-testid=page-body]` carried no `.target` class, three of them
// out of goldmark's own renderers and one out of `internal/content/ext`. The gate is
// the regression test and it belongs at the route, because a route is where a reader
// meets the markup and because §10.6 is about rendered routes.
//
// This file is here for the five properties a route cannot see:
//
//  1. **It covers the fixed element set**, not whatever a render happened to produce.
//     §10.6 lists seven ways to be a focus stop; a page body can only contain three
//     of them under today's policy, so the other four are asserted directly against
//     the pass. A set that tracks the policy is a set somebody has to remember.
//  2. **Idempotence**, both as byte equality and as one token per element — the
//     second is the half a byte comparison cannot see on its own.
//  3. **It runs after the sanitiser**, on a document engineered to fail in both
//     directions: a `<script>` in the source, and an author-written
//     `class="target"` in the source.
//  4. **It cannot resurrect** what the sanitiser removed, because it works on a
//     parsed tree rather than on markup as a string.
//  5. **It is confined to the page body** and writes nothing but a class token.
//
// Every assertion here is mutation-checked: the line holding it was removed, the
// test was confirmed to fail, and the line was restored. The evidence is in the PR
// description. Three of phase 5's first-draft gate tests could not fail, and this
// branch exists because a gate was green while auditing nothing — so a test here
// that cannot fail is a defect rather than a style question.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
)

// TestTheTargetPassGivesEveryFocusStopTheClass is property one: the pass's element
// set is §10.6's list.
//
// `content.ApplyTargetClass` rather than `Render`, because goldmark never lets a
// `<button>`, a `<select>`, a `<textarea>` or a `summary` reach the sanitiser — and
// four of those five are §10.6's interactive elements. The pass's contract is that
// the set is **fixed**, so testing it needs a fragment the pipeline cannot produce.
// `TestThePassRunsOnTheWholeOfSectionTenPointSixsList` is the same subject from the
// pipeline's side and holds the three that can reach it.
//
// The `notes` field is what a reader would need to know when this fails, so a
// failure names which element type went unhandled rather than printing a fragment.
func TestTheTargetPassGivesEveryFocusStopTheClass(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		html string
		want int
	}{
		{
			// Six, not five: §10.6's list is `a[href]`, `button`, `input`,
			// `select`, `textarea`, `summary` **and** anything with a `tabindex`,
			// and the `tabindex` branch is its own row below.
			name: "the five element types §10.6 names",
			html: `<a href="/a">a</a><button>b</button>` +
				`<input disabled="" type="checkbox">` +
				`<select><option>o</option></select><textarea>t</textarea>` +
				`<summary>s</summary>`,
			want: 6,
		},
		{
			// The wikilink shape, and the reason the pass is **not** `href`-gated:
			// `Renderer` emits an anchor with no `href` at all and
			// `internal/httpapi/wiki`'s `attachAddresses` writes one afterwards
			// (ADR 0017 — an address is not permission-neutral, so it cannot be baked
			// into rendered output). An `href`-gated pass would skip every wikilink
			// in every campaign, which is most of what the audit reported.
			name: "a wikilink, which has no href yet",
			html: `<p>See <a class="wikilink" data-ext="wikilink" ` +
				`data-ref-index="0">The Lighthouse</a>.</p>`,
			want: 1,
		},
		{
			// goldmark's footnote extension, both ends. The ref sits inside a `sup`
			// and the backref inside an `li` inside an `ol`, so a walk that stopped
			// descending at the first recognised element would reach neither.
			name: "a footnote ref and its backref",
			html: `<sup id="fnref:1"><a href="#fn:1" class="footnote-ref" ` +
				`role="doc-noteref">1</a></sup>` +
				`<ol><li id="fn:1"><p>note. ` +
				`<a href="#fnref:1" class="footnote-backref" role="doc-backlink">↩</a>` +
				`</p></li></ol>`,
			want: 2,
		},
		{
			// §10.6's `tabindex` branch, which the element map cannot enumerate
			// because it is not an element name. The policy strips `tabindex` today,
			// so this is a branch nothing reaches — and it is here so that a policy
			// which allowed it gets the class from this pass rather than from
			// whoever notices the route audit go red.
			name: "an element made focusable by tabindex, at either sign",
			html: `<div tabindex="0">r</div><span tabindex="-1">s</span>`,
			want: 2,
		},
		{
			// An empty value still counts as present. §10.2's `tabindex` rule makes
			// that distinction load bearing elsewhere in this repository and it is
			// the same distinction here.
			name: "an empty tabindex is still a tabindex",
			html: `<div tabindex="">r</div>`,
			want: 1,
		},
		{
			name: "an existing class is appended to, not replaced",
			html: `<a class="wikilink" href="/a">a</a>`,
			want: 1,
		},
		{
			name: "a multi-token class keeps its other tokens",
			html: `<a class="wikilink footnote-ref" href="/a">a</a>`,
			want: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed, err := content.ApplyTargetClass(testCase.html)
			if err != nil {
				t.Fatalf("apply the target class: %v", err)
			}

			page := targetParse(t, fixed)

			got := page.focusStops()
			if len(got) != testCase.want {
				t.Fatalf("the pass found %d focus stops, want %d; a count that does "+
					"not match means one of §10.6's element types is unhandled, and "+
					"the failure would otherwise be a single element with no class "+
					"and no explanation.\n%s", len(got), testCase.want, fixed)
			}

			for _, element := range got {
				if !element.hasClass("target") {
					t.Errorf("%s is a focus stop and does not carry the target "+
						"class (UI §7.3, §10.6):\n%s", element, fixed)
				}
			}
		})
	}
}

// TestThePassRunsOnTheWholeOfSectionTenPointSixsList is property one from the
// pipeline's side: the three forms §10.6 can reach in a real page body, rendered
// from markdown rather than handed to the pass directly.
//
// It is a separate test from the table above because that one cannot tell whether
// the pass runs at all — it feeds the pass a fragment and checks the result, which is
// a statement about `applyTargetClass` and not about `Render`. This one goes through
// `Renderer.Render` and so fails if the pass is never called, which is the mutation
// that matters most and the one a route audit catches only once the whole suite is
// red.
//
// The three are named because they are the three the audit reported: a markdown
// link, an autolink, and both ends of a footnote, plus the wikilink the extensions
// emit. Five focus stops, and every one of them came out of a renderer this
// repository does not write.
func TestThePassRunsOnTheWholeOfSectionTenPointSixsList(t *testing.T) {
	t.Parallel()

	out := renderBody(t,
		"## The road north\n\n"+
			"See [[Greyhaven]] and [the vault](/c/greyhaven/wiki/Vault) and "+
			"<https://example.com/>.\n\n"+
			"A footnote[^salt], and a bare https://example.org/ one.\n\n"+
			"[^salt]: Salt, and the road it came by.\n")

	page := targetParse(t, out.HTML)

	focusStops := page.focusStops()
	if len(focusStops) != 6 {
		t.Fatalf("the page has %d focus stops, want 6 (a wikilink, a markdown link, "+
			"an autolink, a linkified bare URL, a footnote ref and its backref):\n%s",
			len(focusStops), out.HTML)
	}

	for _, current := range focusStops {
		if !current.hasClass("target") {
			t.Errorf("%s is a focus stop and does not carry the target class:\n%s",
				current, out.HTML)
		}

		if count := current.countClass("target"); count != 1 {
			t.Errorf("%s carries the target class %d times, want 1:\n%s",
				current, count, out.HTML)
		}
	}

	// The three renderer-specific forms named, so a future renderer change that
	// stopped emitting one of them says so here rather than as a count mismatch
	// three assertions later.
	for _, want := range []struct {
		name   string
		reason string
	}{
		{"wikilink", "internal/content/ext's anchor"},
		{"footnote-ref", "goldmark's footnote extension"},
		{"footnote-backref", "goldmark's footnote extension"},
	} {
		if len(page.elementsWithClass(want.name)) == 0 {
			t.Errorf("no element carries the %q class, so %s did not render and "+
				"this test is not covering it:\n%s", want.name, want.reason, out.HTML)
		}
	}
}

// TestTheTargetPassIsIdempotent is property two, from both ends.
//
// **Byte equality** is the weak half and the token count is the strong one: a pass
// that appended `target` again would be caught by either, but a pass that rewrote
// the bytes differently each time while leaving one token would pass a count and
// fail a reader diffing two renders of the same page. Both are asserted, on three
// starting points rather than one, because idempotence is a claim about the
// function's whole domain:
//
//   - a page the pass had nothing to write into;
//   - a page the pass wrote into;
//   - a page that already carried the class **from somewhere else**, which is what
//     a check written as "does this element have a `class` attribute" would get
//     wrong.
//
// The third starting point is also the closest thing in this file to the
// author-written-class case, and it is why the token check is per token rather than
// per attribute: `class="target-large"` is a class list the token must be added to,
// and a check that read the attribute's presence would call it done.
func TestTheTargetPassIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		html string
	}{
		{"nothing to do", `<p>Iron and rust, and the road north.</p>`},
		{
			"what the pass wrote",
			`<p>See <a class="wikilink" data-ext="wikilink" data-ref-index="0">A</a> ` +
				`and <a href="/b">B</a>.</p>`,
		},
		{
			"what something else already carried the class onto",
			`<a class="target" href="/a">A</a>`,
		},
		{
			"a class list that already holds the token among others",
			`<a class="target-large" href="/a">A</a>`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			once, err := content.ApplyTargetClass(testCase.html)
			if err != nil {
				t.Fatalf("first pass: %v", err)
			}

			twice, err := content.ApplyTargetClass(once)
			if err != nil {
				t.Fatalf("second pass over the first pass's output: %v", err)
			}

			if once != twice {
				t.Errorf("the pass is not idempotent.\nonce:\n%s\ntwice:\n%s", once, twice)
			}

			for _, element := range targetParse(t, twice).focusStops() {
				if count := element.countClass("target"); count != 1 {
					t.Errorf("%s carries the target class %d times, want 1:\n%s",
						element, count, twice)
				}
			}
		})
	}
}

// TestRenderIsIdempotent is the same property through the whole pipeline rather than
// through the pass, and it is a separate test because the two fail for different
// reasons.
//
// A pass that is idempotent can still be composed into a pipeline that is not, if
// anything between two renders changes. `Renderer.Render` holds a goldmark instance
// and a `bluemonday.Policy`, both shared across every render it serves, so "the
// pipeline is a function of its input" is a claim about two objects a test cannot see
// the inside of — and S-5.2 keys the render cache on a content hash, so a pipeline
// whose output depended on how many times it had been called would serve different
// bytes for the same hash and the ETag with them.
//
// Same document twice, and nothing more. Feeding a render *its own output* was tried
// and does not work, which is worth recording rather than deleting quietly: goldmark
// reads its input as markdown, and a sanitised HTML block is an HTML **block** to
// CommonMark, so every `<h2>` and `<p>` in the output becomes
// `<!-- raw HTML omitted -->`. The output is not markdown and was never meant to be.
// The pass's own idempotence — the same function over the same bytes — is
// `TestTheTargetPassIsIdempotent` above, and it is the right question.
func TestRenderIsIdempotent(t *testing.T) {
	t.Parallel()

	renderer := newTestRenderer(t)
	const source = "## The road north\n\nSee [[Greyhaven]] and [the vault](/x) and " +
		"<https://example.com/> and a note[^s].\n\n[^s]: Salt.\n"

	first, err := renderer.Render(content.Document{Body: source})
	if err != nil {
		t.Fatalf("first render: %v", err)
	}

	second, err := renderer.Render(content.Document{Body: source})
	if err != nil {
		t.Fatalf("second render of the same document: %v", err)
	}

	if first.HTML != second.HTML {
		t.Errorf("two renders of the same document differ, so the pipeline is not a "+
			"function of its input.\nfirst:\n%s\nsecond:\n%s", first.HTML, second.HTML)
	}

	// And the structural facts, which are compared on a second render for the same
	// reason: they are collected from the AST by a walk, and a walk that reached
	// the same answers is a claim worth having in the same assertion as the bytes
	// it travels with.
	if len(first.References) != len(second.References) ||
		len(first.Anchors) != len(second.Anchors) {
		t.Errorf("two renders of the same document collected different structure: "+
			"%d/%d references and %d/%d anchors",
			len(first.References), len(second.References),
			len(first.Anchors), len(second.Anchors))
	}
}

// TestTheTargetClassIsNotOnTheClassAllowlist is property three's negative half, and
// it is the assertion that keeps the mechanism honest.
//
// ADR 0037's decision is that `target` is **semiplane's** class, applied by
// semiplane, and is therefore deliberately **absent** from the `class` allowlist in
// `policy.go`. Adding it there would look like it fixed something and it would not:
// an author writing `class="target"` achieves nothing either way, because goldmark
// drops raw HTML before the sanitiser sees it, so the class could only ever have come
// from the pipeline. It would also be a second source of truth for one class, which
// is what `policy.go`'s comment on `extensionClasses` calls a maintenance obligation
// — and a list nobody has to keep is the whole of why `target.go` exists rather than
// a fifth entry in that regexp.
//
// **Asserted through `Sanitise` rather than through `Render`**, because no output
// assertion can make this claim. Every `target` in a rendered page is semiplane's
// own — goldmark strips author classes before the sanitiser runs — so a page full of
// correctly-classed links looks identical under a policy that allows `target` and one
// that does not. The policy is the only place the claim lives and the policy is what
// this asks about.
//
// The positive half is here too: the classes the pipeline *does* allow survive the
// same sanitiser, so the row is not passing because `class` is dropped altogether.
func TestTheTargetClassIsNotOnTheClassAllowlist(t *testing.T) {
	t.Parallel()

	t.Run("the pipeline's own classes survive the policy", func(t *testing.T) {
		t.Parallel()

		got := content.Sanitise(
			`<a class="wikilink" href="/a" data-ext="wikilink" data-ref-index="0">A</a>` +
				`<a class="footnote-ref" href="#fn:1" role="doc-noteref">1</a>` +
				`<code class="language-go">x</code>`,
		)

		for _, want := range []string{"wikilink", "footnote-ref", "language-go"} {
			if !strings.Contains(got, want) {
				t.Errorf("the %q class did not survive the policy, so the "+
					"row below would pass on a policy that dropped every class:\n%s",
					want, got)
			}
		}
	})

	t.Run("target does not", func(t *testing.T) {
		t.Parallel()

		// On every element §10.6's list reaches, and on the extension's own `a`,
		// because the pass writes onto all of them and an allowlist that allowed
		// the class on one and not another would be a second source of truth
		// wearing a single value's clothing.
		for _, testCase := range []struct {
			element string
			markup  string
		}{
			{"a", `<a class="target" href="/a">A</a>`},
			{"a", `<a class="wikilink target" href="/a">A</a>`},
			{"span", `<span class="target">A</span>`},
			{"div", `<div class="target">A</div>`},
			{"code", `<code class="target">x</code>`},
			{"input", `<input class="target" type="checkbox" disabled="">`},
		} {
			got := content.Sanitise(testCase.markup)

			if strings.Contains(got, "target") {
				t.Errorf("<%s> kept class=\"target\"; the class is semiplane's and "+
					"is applied by target.go after the sanitiser, so the policy "+
					"must not allow it (ADR 0037):\n%s", testCase.element, got)
			}
		}
	})

	t.Run("and the pipeline applies it anyway", func(t *testing.T) {
		t.Parallel()

		// The direction the pass exists for: what the policy refuses, the pass
		// supplies. Without this row the row above would be satisfied by a pass
		// that does nothing at all.
		out := renderBody(t, "A footnote[^1] and a [[Page]] and [text](/x).\n\n[^1]: note.\n")

		if !strings.Contains(out.HTML, `class="wikilink target"`) {
			t.Errorf("the wikilink does not carry both classes, so the pass did "+
				"not run after the sanitiser:\n%s", out.HTML)
		}
	})
}

// TestTheTargetPassRunsAfterSanitisation is property three's positive half, on a
// document engineered to fail in both directions.
//
// The source contains a block-level `<script>`, an inline `<span class="target">`, a
// markdown link, a wikilink, an autolink and a footnote. Then:
//
//   - the `<script>` and its payload are gone — the pass parsed a tree the sanitiser
//     had already emptied, and there is no code path in it that puts one back;
//   - the author's `<span class="target">` is gone as an **element**, not surviving
//     stripped — goldmark drops a raw HTML *block* whole and strips an inline one,
//     which is the asymmetry `policy.go` documents. Either way the author's class
//     never reaches the pass;
//   - every focusable element carries exactly one `target`.
//
// **No front matter**, because `renderBody` takes a `Document` rather than a file:
// the block would be a rule and the `title:` line would be a heading, which is
// harmless but makes the fixture read as if it were about front matter.
//
// The author's-class assertion is deliberately about the **element** and not about
// the token. "The author's `class="target"` did not survive" is *also* satisfied by
// a pass that refuses to write onto an element already carrying the class, and those
// are different requirements; the second is
// `TestTheTargetPassIsIdempotent`. The claim that the *policy* forbids the class is
// `TestTheTargetClassIsNotOnTheClassAllowlist`, and it has to be asked of the policy
// because no output assertion can distinguish "forbidden and never used" from
// "allowed and never used".
func TestTheTargetPassRunsAfterSanitisation(t *testing.T) {
	t.Parallel()

	source := "See [[Greyhaven]] and [the vault](/c/greyhaven/wiki/Vault) and " +
		"<https://example.com/>.\n\n" +
		"A footnote[^salt].\n\n" +
		"<script>alert(1)</script>\n\n" +
		"A span with the class <span class=\"target\">kept as text</span>.\n\n" +
		"[^salt]: Salt, and the road it came by.\n"

	out := renderBody(t, source)

	for _, forbidden := range []string{"alert(1)", "<script", "</script>", "<span"} {
		if strings.Contains(out.HTML, forbidden) {
			t.Errorf("the output contains %q; the sanitiser removed it and the pass "+
				"put it back:\n%s", forbidden, out.HTML)
		}
	}

	// The inline raw element's **text** survives — that is goldmark's behaviour,
	// documented in policy.go, and not what this test is about. What must not
	// survive is the element. Asserting only the payload above would pass on a page
	// where the author's span had been re-created, which is the mutation that makes
	// this test worth having.
	if !strings.Contains(out.HTML, "kept as text") {
		t.Errorf("the inline raw element's text is gone, so this fixture is not "+
			"reaching the branch it was written for:\n%s", out.HTML)
	}

	page := targetParse(t, out.HTML)

	focusStops := page.focusStops()
	if len(focusStops) == 0 {
		t.Fatalf("the page has no focus stops, so every assertion below passes "+
			"vacuously:\n%s", out.HTML)
	}

	for _, current := range focusStops {
		if !current.hasClass("target") {
			t.Errorf("%s is a focus stop without the target class:\n%s", current, out.HTML)
		}

		if count := current.countClass("target"); count != 1 {
			t.Errorf("%s carries the target class %d times, want 1:\n%s",
				current, count, out.HTML)
		}
	}
}

// TestTheTargetPassCannotResurrectSanitisedMarkup is the claim that made the pass
// parse rather than match strings — asserted on its own rather than as a remark on
// another test, because a remark is not a gate.
//
// A regexp substitution works on bytes, so "add `class="target"` before the first
// `>`" and "`target` is not already in this element's class" are two regular
// languages, and neither can see that the bytes they are reading are the output of a
// sanitiser. A parsed tree sees the document a browser will, and there is nothing in
// it that can manufacture an element the sanitiser removed — because the tree it was
// built from does not contain one.
//
// The rows are the elements `policy.go` removes for **documented** reasons, and each
// is asserted absent rather than inert. Two of the rows are the ones where the
// sanitiser and the *first* layer disagree about what happens to the payload, and
// they are worth reading twice:
//
//   - **Inline raw HTML** (`<a onclick>` inside a paragraph): goldmark keeps the
//     text and the policy strips the tag, so `a link` survives and `onclick` does
//     not. Asserted in both directions — a pass that dropped the element would lose
//     what a reader reads, which is its own failure mode.
//   - **A refused scheme**: `javascript:` is not on `AllowURLSchemes`, so bluemonday
//     removes the `href` and leaves the sentence. The link becomes inert and the
//     prose still reads, which is the documented intent.
//
// `<svg><script>` is asserted on its **elements** and not on its payload. goldmark's
// HTML-block handling ends the block at the `<script>`, so `alert(1)` arrives as
// visible text — policy.go says a block-level raw element loses its text, and this
// case is the one where goldmark's answer differs; what is asserted here is that the
// sanitiser's decision about *elements* was not undone, which is the property the
// pass is claimed to hold.
func TestTheTargetPassCannotResurrectSanitisedMarkup(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		source  string
		absent  []string
		present []string
	}{
		{
			// `AllowUnsafe(false)` — the default, never called with `true` — is what
			// makes bluemonday drop a `<script>`'s *content* with the tag. The
			// payload is already gone before the pass runs, so a pass that
			// re-serialised text nodes could not bring it back.
			name:    "a script and its payload",
			source:  "Before.\n\n<script>alert(1)</script>\n\nAfter.\n",
			absent:  []string{"script", "alert(1)"},
			present: []string{"Before.", "After."},
		},
		{
			// The inline half of policy.go's asymmetry: the text survives, the tag
			// does not. Asserted in both directions, because "the handler is gone"
			// is also satisfied by a pass that deleted the whole element — and that
			// would be a pass costing a reader a word.
			name: "an inline event handler",
			source: "Before.\n\nA paragraph with <a href=\"/x\" onclick=\"alert(1)\">" +
				"a link</a> in it.\n\nAfter.\n",
			absent:  []string{"onclick"},
			present: []string{"a link", "After."},
		},
		{
			// The documented mXSS vector, and the reason `svg` and `math` are both
			// forbidden as *elements*: a script inside foreign content reparses
			// differently. Nothing survives sanitisation for the pass to reparse.
			name:    "foreign content",
			source:  "Before.\n\n<svg><script>alert(1)</script></svg>\n\nAfter.\n",
			absent:  []string{"svg", "<script"},
			present: []string{"Before.", "After."},
		},
		{
			name:    "a noscript wrapper",
			source:  "Before.\n\n<noscript><img src=x onerror=alert(1)></noscript>\n\nAfter.\n",
			absent:  []string{"noscript", "onerror"},
			present: []string{"Before.", "After."},
		},
		{
			name:    "a style block",
			source:  "Before.\n\n<style>p{display:none}</style>\n\nAfter.\n",
			absent:  []string{"<style", "display"},
			present: []string{"Before.", "After."},
		},
		{
			name:    "a form control",
			source:  "Before.\n\n<form action=\"//evil\"><input name=\"q\"></form>\n\nAfter.\n",
			absent:  []string{"<form", "name=\"q\""},
			present: []string{"Before.", "After."},
		},
		{
			// The one that has to keep its *text*: a link whose scheme the policy
			// refuses keeps the sentence, because a GM whose sync pulled in a hostile
			// link wants the prose to still read. If the pass lost it, the pass would
			// be changing what a reader sees, which property five forbids.
			name:    "a refused link keeps its text",
			source:  "Before.\n\n[click me](javascript:alert(1))\n\nAfter.\n",
			absent:  []string{"javascript:", "href="},
			present: []string{"click me", "Before.", "After."},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out := renderBody(t, testCase.source)

			for _, forbidden := range testCase.absent {
				if strings.Contains(out.HTML, forbidden) {
					t.Errorf("the output contains %q; the sanitiser removed it and "+
						"the pass brought it back:\n%s", forbidden, out.HTML)
				}
			}

			for _, wanted := range testCase.present {
				if !strings.Contains(out.HTML, wanted) {
					t.Errorf("the output does not contain %q, so the pass changed "+
						"what a reader reads:\n%s", wanted, out.HTML)
				}
			}
		})
	}
}

// TestTheTargetPassWritesNothingButAClassToken is property five's strong half: the
// pass's *entire* effect on the document is one token per focus stop.
//
// Compared element by element rather than as strings, and the reason is that a
// string comparison cannot see the mutation that matters. The obvious wrong
// implementations of "add the class" all **add** something — an extra attribute, a
// wrapper element, a `title` — and a test that counted `target` tokens would report
// every one of them as a pass. So: same number of elements, same tag names in the
// same order, same attributes with the same values, same text, and the same class
// tokens plus exactly one.
//
// One spelling difference is expected and excluded rather than papered over: the
// pass re-renders through `html.Render`, which writes a void element as `<hr/>`
// where the sanitiser writes `<hr>`. That is HTML5's canonical serialisation and
// every browser reads it identically, and the comparison above is spelling-agnostic
// precisely so it can be said out loud instead of hidden behind a normalisation
// helper. `TestTheTargetPassLeavesAPageWithNothingToDoAlone` is the other side of
// the same fact: the re-render only happens at all when the pass wrote something.
func TestTheTargetPassWritesNothingButAClassToken(t *testing.T) {
	t.Parallel()

	// Everything a page body can hold: a heading with a generated id, both link
	// forms, an autolink, a wikilink and an embed from the extensions, a footnote
	// on both ends, an image, a fenced code block with a language, a blockquote,
	// a rule and a task-list checkbox.
	const fragment = "<h2 id=\"the-road\">The road north</h2>\n" +
		"<p>See <a class=\"wikilink\" data-ext=\"wikilink\" data-ref-index=\"0\">A</a>, " +
		"<a href=\"/b\">B</a> and <a href=\"https://example.com/\">https://example.com/</a>, " +
		"and a note<sup id=\"fnref:1\"><a href=\"#fn:1\" class=\"footnote-ref\" " +
		"role=\"doc-noteref\">1</a></sup>.</p>\n" +
		"<blockquote><p>a quote</p></blockquote>\n" +
		"<ul><li><input checked=\"\" disabled=\"\" type=\"checkbox\"> done</li></ul>\n" +
		"<pre><code class=\"language-go\">x := 1\n</code></pre>\n" +
		"<p><img alt=\"the valley\" src=\"map.png\"></p>\n" +
		"<hr>\n" +
		"<div class=\"footnotes\" role=\"doc-endnotes\"><ol><li id=\"fn:1\"><p>Salt. " +
		"<a href=\"#fnref:1\" class=\"footnote-backref\" role=\"doc-backlink\">↩</a>" +
		"</p></li></ol></div>\n"

	after, err := content.ApplyTargetClass(fragment)
	if err != nil {
		t.Fatalf("apply the target class: %v", err)
	}

	before := targetParse(t, fragment)
	got := targetParse(t, after)

	if len(before.elements()) != len(got.elements()) {
		t.Fatalf("the pass changed the element count: %d before, %d after.\nbefore:\n%s\n"+
			"after:\n%s", len(before.elements()), len(got.elements()), fragment, after)
	}

	for index, original := range before.elements() {
		current := got.elements()[index]

		if current.name != original.name {
			t.Fatalf("element %d is <%s> after the pass and <%s> before it; the "+
				"pass may only add a class token.\nbefore:\n%s\nafter:\n%s",
				index, current.name, original.name, fragment, after)
		}

		if current.text != original.text {
			t.Errorf("<%s> reads %q after the pass and %q before it; the pass must "+
				"not touch what a reader sees.\nbefore:\n%s\nafter:\n%s",
				current.name, current.text, original.text, fragment, after)
		}

		for key, value := range original.attributes {
			if key == "class" {
				continue
			}

			if current.attributes[key] != value {
				t.Errorf("<%s> %s is %q after the pass and %q before it; the pass "+
					"may only add a class token.\nbefore:\n%s\nafter:\n%s",
					current.name, key, current.attributes[key], value, fragment, after)
			}
		}

		wantClasses := append([]string{}, original.classes...)
		if current.isFocusStop() {
			wantClasses = append(wantClasses, "target")
		}

		if strings.Join(current.classes, " ") != strings.Join(wantClasses, " ") {
			t.Errorf("<%s> carries classes %q after the pass, want %q; the pass adds "+
				"one token to a focus stop and changes nothing else.\nbefore:\n%s\nafter:\n%s",
				current.name, strings.Join(current.classes, " "),
				strings.Join(wantClasses, " "), fragment, after)
		}
	}

	// And that the comparison was not between two identical documents because the
	// pass did nothing: at least one element must have gained the token.
	if len(got.elementsWithClass("target")) == 0 {
		t.Errorf("the pass added no target class, so the comparison above "+
			"compared a document with itself:\n%s", after)
	}
}

// TestTheTargetPassLeavesAPageWithNothingToDoAlone is property five's other half,
// and the one that makes the confinement claim about the *shell* checkable.
//
// `applyTargetClass` returns its input **unchanged** when it wrote nothing. That is
// not only an optimisation: it means a body with no focus stops in it is served
// byte-for-byte as the sanitiser wrote it, so this file cannot be the reason a page's
// bytes moved on a page that had nothing interactive in it.
//
// The fixture is full of void elements — a rule, a break, an image, a `wbr` —
// because `<hr>` becoming `<hr/>` is the canary. A pass that re-rendered
// unconditionally would change all of them, and byte equality would fail. So this is
// the test that says the pass is conditional.
//
// **Why that makes the shell safe.** The shell's chrome is templ, carries `.target`
// by construction, and never passes through here — `applyTargetClass` is unexported,
// `Renderer.Render` is its only caller, and `Rendered.HTML` is its only argument. The
// observable consequence of that confinement is this test plus the fragment
// assertion in `TestARenderedPageBodyIsAFragmentAndNotADocument`: a pass that could
// reach the shell's markup would necessarily be a pass over a document, and neither
// of these two allows that.
func TestTheTargetPassLeavesAPageWithNothingToDoAlone(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		html string
	}{
		{
			name: "prose with rule, break, image and wbr",
			html: "<p>Iron and rust.</p>\n<hr>\n<p>A break.</p>\n<br>\n" +
				"<p><img alt=\"the valley\" src=\"map.png\"></p>\n" +
				"<p>A<wbr>break.</p>\n<hr>\n<p>The end.</p>\n",
		},
		{
			name: "a shell's worth of markup, none of it in a page body",
			// The chrome is not what the pass is for, and this row says so in
			// bytes: a fragment shaped like a header and a footer comes back
			// exactly as it went in, because the pass is a page-body pass.
			html: "<header class=\"shell-header\" role=\"banner\">" +
				"<a class=\"shell-brand target\" href=\"/\">Greyhaven</a>" +
				"<button type=\"button\" class=\"target\" aria-pressed=\"false\">Theme</button>" +
				"</header>\n<footer class=\"shell-footer\" role=\"contentinfo\">" +
				"<span>Version 0.3.0</span></footer>\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := content.ApplyTargetClass(testCase.html)
			if err != nil {
				t.Fatalf("apply the target class: %v", err)
			}

			if got != testCase.html {
				t.Errorf("a fragment with nothing to do came back different, so the "+
					"pass re-serialised markup it had no business rewriting.\nbefore:\n%s\n"+
					"after:\n%s", testCase.html, got)
			}
		})
	}
}

// TestARenderedPageBodyIsAFragmentAndNotADocument is the confinement assertion that
// can actually fail.
//
// "The pass is confined to the page body" is a claim about the pass's **input**, and
// an input cannot be observed in the output once the pass is idempotent — running it
// over a shell that already carries the class changes nothing a reader can see. So
// the falsifiable half is this: `Rendered.HTML` must stay a fragment.
//
// The plausible wrong implementation is `html.Parse` instead of
// `html.ParseFragment`, which wraps the input in `<html><head><body>` and hands back
// a document. That would break the page entirely rather than merely widen it — the
// shell would have a nested document, and a stylesheet stops applying inside one. So
// this test is a real gate rather than a formality, and it fails loudly.
//
// The three tags are checked as openers rather than as substrings, because
// `html.Parse` produces exactly `<html><head><body>` and a check that matched
// `"<body"` alone would also match a `<body` an author wrote — which is not a thing
// they can write, since goldmark drops raw HTML, which is the point.
func TestARenderedPageBodyIsAFragmentAndNotADocument(t *testing.T) {
	t.Parallel()

	out := renderBody(t,
		"## The road north\n\nSee [[Greyhaven]] and [the vault](/x) and "+
			"<https://example.com/> and a note[^s].\n\n[^s]: Salt.\n")

	// The fragment is not empty, so the assertions below cannot pass vacuously.
	if !strings.Contains(out.HTML, "<p>") {
		t.Fatalf("the rendered body is empty, so the fragment assertions below "+
			"cannot fail:\n%s", out.HTML)
	}

	for _, forbidden := range []string{"<html", "<head", "<body", "<!doctype"} {
		if strings.Contains(strings.ToLower(out.HTML), forbidden) {
			t.Errorf("the rendered page body contains %q; it is a fragment inserted "+
				"into a templ shell, and a nested document is how a stylesheet "+
				"stops applying (S-5.1):\n%s", forbidden, out.HTML)
		}
	}
}

// TestTheRenderedBodyIsTheSanitisedBodyPlusOneTokenPerFocusStop is property five over
// the **real corpus** rather than one hand-written fragment.
//
// The table in `TestTheTargetPassWritesNothingButAClassToken` says the pass adds a
// class token and changes nothing else, but it says it about a fragment written for
// the purpose — so a change that dropped a `<colgroup>`, lost a `colspan` or
// reflowed a `<details>` would pass it, because the fragment does not contain one.
// This one renders every golden document **twice**: once through `Render`, and once
// through `RenderSanitised`, which is the same pipeline with the pass removed. The
// two documents are then compared element by element.
//
// That is the shape of the argument the ADR makes about the reparse being safe — that
// HTML parsing's error recovery changes structure only inside elements the policy
// removes — stated over the markup goldmark, `ext` and bluemonday actually produce
// rather than over markup a person imagined. A `<table><colgroup>` or a
// `<details open="">` is a case where the parser *could* differ and does not, and
// this is the test that says it does not.
//
// The one difference the corpus does contain is void-element spelling: `html.Render`
// writes `<hr/>` where bluemonday writes `<hr>`, and the six golden files record it.
// So the comparison is over parsed attributes and text, not over bytes, and the
// spelling difference is named in
// `TestTheTargetPassWritesNothingButAClassToken`'s comment rather than normalised away
// by a helper nobody would think to read.
func TestTheRenderedBodyIsTheSanitisedBodyPlusOneTokenPerFocusStop(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/content -update)", goldenDir, err)
	}

	renderer := newTestRenderer(t)
	kinds := testKinds{
		"journal": {}, "handout": {}, "index": {}, "token": {}, "scene": {},
	}

	seen := 0

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		seen++

		t.Run(strings.TrimSuffix(entry.Name(), ".md"), func(t *testing.T) {
			t.Parallel()

			source, readErr := os.ReadFile(filepath.Join(goldenDir, entry.Name()))
			if readErr != nil {
				t.Fatalf("read %s: %v", entry.Name(), readErr)
			}

			document := content.Parse(source, kinds)

			withPass, passErr := renderer.Render(document)
			if passErr != nil {
				t.Fatalf("render %s: %v", entry.Name(), passErr)
			}

			withoutPass, noPassErr := content.RenderSanitised(
				content.NewRenderer("greyhaven", kinds), document,
			)
			if noPassErr != nil {
				t.Fatalf("render %s without the pass: %v", entry.Name(), noPassErr)
			}

			compareAroundThePass(t, entry.Name(), withoutPass, withPass.HTML)
		})
	}

	if seen == 0 {
		t.Fatalf("%s holds no markdown, so this test compared nothing", goldenDir)
	}
}

// compareAroundThePass asserts that two documents differ by exactly one `target` token
// per focusable element and by nothing else.
func compareAroundThePass(t *testing.T, where, before, after string) {
	t.Helper()

	originals := targetParse(t, before)
	got := targetParse(t, after)

	if len(originals.elements()) != len(got.elements()) {
		t.Fatalf("%s: the pass changed the element count: %d before, %d after.\n"+
			"before:\n%s\nafter:\n%s",
			where, len(originals.elements()), len(got.elements()), before, after)
	}

	for index, original := range originals.elements() {
		current := got.elements()[index]

		if current.name != original.name {
			t.Fatalf("%s: element %d is <%s> after the pass and <%s> before it.\n"+
				"before:\n%s\nafter:\n%s",
				where, index, current.name, original.name, before, after)
		}

		if current.text != original.text {
			t.Errorf("%s: <%s> reads %q after the pass and %q before it.\nbefore:\n%s\n"+
				"after:\n%s", where, current.name, current.text, original.text, before, after)
		}

		for key, value := range original.attributes {
			if key == "class" {
				continue
			}

			if current.attributes[key] != value {
				t.Errorf("%s: <%s> %s is %q after the pass and %q before it.\n"+
					"before:\n%s\nafter:\n%s",
					where, current.name, key, current.attributes[key], value, before, after)
			}
		}

		want := append([]string{}, original.classes...)
		if current.isFocusStop() {
			want = append(want, "target")
		}

		if strings.Join(current.classes, " ") != strings.Join(want, " ") {
			t.Errorf("%s: <%s> carries classes %q after the pass, want %q.\nbefore:\n%s\n"+
				"after:\n%s", where, current.name, strings.Join(current.classes, " "),
				strings.Join(want, " "), before, after)
		}
	}
}

// --- The fixture helpers -----------------------------------------------------

// element is one element of a parsed fragment: its tag name, its attributes as a
// map, its descendant text, and its class list **as tokens**.
//
// A wrapper rather than the `*html.Node` itself because every assertion here wants
// the class tokens, and a parsed class attribute is a string with spaces in it —
// which is the whole mistake this file exists to avoid repeating.
type element struct {
	name       string
	attributes map[string]string
	text       string
	classes    []string
}

// hasClass reports whether the element's class list carries token.
func (e element) hasClass(token string) bool {
	return e.countClass(token) > 0
}

// countClass reports how many times token appears in the element's class list.
//
// A count rather than a boolean, because `class="target target"` is exactly the
// failure idempotence is about and `hasClass` cannot see it.
func (e element) countClass(token string) int {
	total := 0

	for _, candidate := range e.classes {
		if candidate == token {
			total++
		}
	}

	return total
}

// String names the element for a failure message, with its class list — so a failure
// says *which* focus stop went unclassed.
func (e element) String() string {
	if len(e.classes) == 0 {
		return "<" + e.name + ">"
	}

	return "<" + e.name + ` class="` + strings.Join(e.classes, " ") + `">`
}

// fragment is a parsed fragment as a list of elements in document order.
//
// `html`, `head` and `body` are **not** in the list, and that is what makes
// `targetParse` read a *fragment* rather than a document: `html.Parse` synthesises
// those three around whatever it is given, so a page body — which is a fragment, by
// `Rendered.HTML`'s own contract — arrives wrapped in them. Leaving them in would
// put three elements in front of every comparison for no gain.
//
// That is also the assertion `TestARenderedPageBodyIsAFragmentAndNotADocument`
// exists for, done the other way round: here they are filtered because the input is
// known to be a fragment, and there the filter is the *claim*.
type fragment struct {
	found []element
}

// elements returns every element the fragment itself contained, in document order.
func (f fragment) elements() []element {
	return f.found
}

// focusStops returns the elements the **pass** treats as focus stops, in document
// order.
//
// This is `target.go`'s rule, not §10.6's audit rule, and the difference is the whole
// of the wikilink. §10.6 walks `a[href]`, because by the time a reader has the
// document `internal/httpapi/wiki`'s `attachAddresses` has written every address in.
// Inside the render pipeline no extension anchor has an `href` yet —
// `Renderer` emits `<a class="wikilink" data-ext="wikilink" data-ref-index="0">`
// with no destination at all, because an address is not permission-neutral and ADR 0017
// keeps it out of rendered output. A test using the audit's rule here would not be
// able to *see* a wikilink, so it could not assert that the pass reached one.
//
// Being the pass's own rule makes this a weak check on the element set — a wrong set
// here would be wrong in the same way as in the pass. The two independent
// hold-it-down tests are `TestThePassRunsOnTheWholeOfSectionTenPointSixsList`, which
// counts the five real forms through `Render`, and
// `TestTheTargetPassWritesNothingButAClassToken`, which states the expected class
// list per element without consulting any set at all.
func (f fragment) focusStops() []element {
	var found []element

	for _, current := range f.found {
		if current.isFocusStop() {
			found = append(found, current)
		}
	}

	return found
}

// isFocusStop reports whether the pass would write the class onto this element.
//
// `a` with no `href` condition, for the reason `fragment.focusStops` gives.
func (e element) isFocusStop() bool {
	switch e.name {
	case "a", "button", "input", "select", "summary", "textarea":
		return true
	default:
		_, present := e.attributes["tabindex"]

		return present
	}
}

// elementsWithClass returns every element carrying token in its class list.
func (f fragment) elementsWithClass(token string) []element {
	var found []element

	for _, current := range f.found {
		if current.hasClass(token) {
			found = append(found, current)
		}
	}

	return found
}

// syntheticWrappers are the three elements `html.Parse` puts around any fragment, and
// which are therefore not part of one.
var syntheticWrappers = map[string]bool{"html": true, "head": true, "body": true}

// targetParse reads a rendered fragment through `golang.org/x/net/html`.
//
// A parser and not a regexp, for the reason `render_test.go`'s `parseTokens` gives: a
// regexp has to be right about quoted attribute values, unquoted ones and a `>` inside
// a value, and every one of those is a way for a test to pass on markup that is not
// what it claims to check. It is the same parser the pass uses, which is deliberate —
// a test that disagreed with the pass about the document would be a test disagreeing
// with a browser.
//
// Elements are visited in **document order**, and the recursion descends into a
// child before moving to its sibling, which is the order a reader's tab key meets
// them and so the order a count in a failure message should be reported in.
func targetParse(t *testing.T, markup string) fragment {
	t.Helper()

	root, err := html.Parse(strings.NewReader("<body>" + markup))
	if err != nil {
		t.Fatalf("parse rendered fragment: %v", err)
	}

	var (
		walk  func(current *html.Node)
		found fragment
	)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)

			if child.Type != html.ElementNode || syntheticWrappers[child.Data] {
				continue
			}

			found.found = append(found.found, newElement(child))
		}
	}

	walk(root)

	return found
}

// newElement describes one parsed element.
func newElement(node *html.Node) element {
	found := element{
		name:       node.Data,
		attributes: map[string]string{},
	}

	for _, attribute := range node.Attr {
		found.attributes[attribute.Key] = attribute.Val

		if attribute.Key == "class" {
			found.classes = strings.Fields(attribute.Val)
		}
	}

	found.text = descendantText(node)

	return found
}

// descendantText returns an element's descendant **text nodes**, so a test compares
// what a reader reads rather than how the markup is spelled.
//
// Text nodes only: an `alt` or a `title` is text a reader hears but it is not in the
// text content, and a helper returning attributes too would make "the pass did not
// change the prose" and "the pass did not change the attributes" one assertion.
func descendantText(node *html.Node) string {
	var out strings.Builder

	var walk func(current *html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return out.String()
}
