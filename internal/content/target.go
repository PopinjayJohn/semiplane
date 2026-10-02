// The `.target` pass: the one thing the render pipeline **adds** to sanitised
// output.
//
// # Why this is here and not in the template
//
// UI §7.3 makes §10.6's target size "enforced by construction", and §10.6 says the
// construction is a shared `.target` utility plus a grep of rendered markup for the
// interactive elements missing the class. `internal/web/static/css/shell.css` is the
// utility — a rule that sets `--target-min` on both axes and nothing else — and
// §10.6's grep is `TestEveryRouteCarriesTheTargetClassOnEveryFocusStop` in
// `internal/httpapi/wiki`, which is what found this gap: five focus stops inside
// `div[data-testid=page-body]` carried no class.
//
// None of them can be fixed in a `.templ` file. Three of the five come out of
// goldmark's own renderers (a markdown link, a bare autolink, and both ends of a
// footnote), and one out of `internal/content/ext`, and they all land in the page
// body — which `components.WikiArticle` inserts as a single `templ.Raw`. A wrapper
// can put a class on the *container*; it cannot reach an element the renderer
// emitted inside it. So the class has to be applied to the bytes, and the bytes are
// only ever whole at one point in the pipeline: after `Render` has sanitised them.
//
// # Why it is an *add* and not a subtraction
//
// Every other decision in this package's sanitisation removes something, and the
// whole of `policy.go` reads as "what is not on the list does not reach a reader's
// browser". This one is the exception, and the exception is safe for four reasons
// that are each a property rather than an intention:
//
//   - **The value is a fixed constant semiplane owns.** `targetClass` is a literal in
//     this file. Nothing semiplane reads reaches it, so no value can be shaped, and
//     the only token that can appear in a `class` attribute because of this pass is
//     this one.
//   - **The element set is fixed**, and written out rather than computed, so the
//     pass cannot reach an element the policy did not already allow and a reader
//     can see the whole set without reading a policy.
//   - **The value is a class name, not a behaviour.** `.target` resolves to two
//     minimum sizes in semiplane's own stylesheet. A class of an author's choosing
//     matches no rule, which is `policy.go`'s argument for allowing `class` at all,
//     and it is why the reverse — allowing `target` on the `class` allowlist — is
//     not the fix: that would let a *vault* mint the class, and the class is
//     semiplane's, applied by semiplane. ADR 0037 records the decision.
//   - **It runs after the sanitiser**, so the sanitiser is still the last thing that
//     decided what markup exists, and this pass cannot be undone by it. Run before,
//     the sanitiser would strip `class="target"` again (it is not on the allowlist)
//     and the pass would be a no-op.
//
// # Why it operates on the parsed tree
//
// The pass parses the sanitised fragment with `golang.org/x/net/html`, mutates the
// tree, and re-renders it. That is the expensive way to add one attribute, and it is
// chosen for one property a regex does not have: **the pass cannot resurrect
// anything the sanitiser removed.** It works from a tree built out of bytes that
// already contain the answer, so a `<script>` is not "a script element with its
// content stripped" that a later regex might match — it is absent, and there is no
// code path here that puts one back. A string pass would have to promise the same
// thing, and a promise in a comment is not a boundary.
//
// The reparse is safe for the same reason it is necessary. HTML parsing has error
// recovery, and the cases where recovery *changes* structure — foreign content in
// `<svg>`/`<math>`, raw text in `<script>`/`<style>`/`<noscript>`/`<textarea>`,
// `<template>` contents, misnested table cells — all live inside elements
// `policy.go` removes outright. With none of them present, the tokenizer sees only
// the phrasing and flow content the policy allows, and the tree it builds is the
// tree the sanitiser saw. ADR 0037 states the argument in full.
//
// # Idempotence
//
// The page body is re-rendered on every cache miss, and the cache holds the result
// (S-5.2), so this function sees its own output's *siblings*, never its own output —
// but "the render is deterministic" is a claim about this package being careful, and
// a class list that accumulated `target target target` would be an accumulating bug
// in the one place where accumulating is visible in the bytes a reader gets. So the
// token is checked before it is written, and a page that already carries it is left
// exactly as it was — which also means a document that arrives with the class already
// present comes back unchanged rather than re-serialised.
//
// The two halves are tested from both sides: `target_test.go` runs the pass twice
// over its own output, and `TestRenderIsIdempotent` renders the same document twice
// through the whole pipeline.

package content

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// targetClass is the class UI §7.3 names and the stylesheet defines.
//
// A literal rather than a variable from `internal/web`, and the direction of the
// dependency is the point: `internal/content` imports nothing from the project, so a
// shared constant would mean the web package importing this one, which is the wrong
// way round for a name both of them have to agree on. Two literals that agree, one
// asserted by `TestRenderGolden` and the other by
// `internal/web`'s stylesheet test, beats an import cycle.
const targetClass = "target"

// classAttribute is the attribute the pass writes into. Named because this file
// reads it twice and a linter counts three literals — and a named attribute is
// easier to grep for than a bare `"class"` when somebody is auditing what the pass
// touches.
const classAttribute = "class"

// targetStopElements is every element the pass writes `targetClass` onto.
//
// UI §10.6's own list, and it is a list rather than a rule because a page body is
// closed: `policy.go`'s `pageElements` forbids `button`, `select` and `textarea`, and
// strips `tabindex`, so today only `a`, `input` and `summary` can appear here. They
// are all listed anyway, so a later phase that widens the policy does not also have to
// remember this map — the failure of forgetting is a live focus stop under the
// minimum, which is exactly the bug ADR 0037 exists to close.
//
// **The `a` entry carries no `href` condition, and that is deliberate.** §10.6's grep
// tests `a[href]`, because by the time a reader has the document the route has
// written the addresses in and every surviving `<a>` has one. Inside the render
// pipeline that is not yet true: `content.Renderer` emits
// `<a class="wikilink" data-ext="wikilink" data-ref-index="0">` with **no** `href`
// at all, and `internal/httpapi/wiki`'s `attachAddresses` fills it in afterwards
// (ADR 0017 — an address is not permission-neutral, so it cannot be baked into
// rendered output). An `href`-gated pass would therefore skip every wikilink, which is
// three of the five elements the audit reported.
//
// Being a **superset** of what the gate can see is the safe direction for that
// mismatch. The extra elements are the ones that will not be focusable — a `span` an
// address refusal turns a wikilink into, an `img` an asset embed becomes, an `<a>`
// whose `href` the URL policy removed — and `min-inline-size` on an inline element
// computes to nothing, so a `target` class on one of those is inert. A set *narrower*
// than the gate's is the unsafe direction, because a live focus stop is then left
// below 44px.
var targetStopElements = map[string]bool{
	"a":        true,
	"button":   true,
	"input":    true,
	"select":   true,
	"summary":  true,
	"textarea": true,
}

// applyTargetClass gives every focusable element in one rendered page body the
// `.target` class, and returns the body.
//
// Takes a **fragment**, not a document, and is called with `Rendered.HTML` and
// nothing else. The confinement to the page body is therefore structural rather than
// promised: the function is unexported, its only caller is `Renderer.Render`, and
// `Rendered.HTML` is by construction the body the shell inserts as a `templ.Raw`.
// The shell's own chrome is templ, already carries the class by construction
// (`shell.css`'s `.target` plus the templates), and never passes through here — which
// is also why the pass cannot double the shell's class, and why a route that composes
// the shell with a different body gets a different `<h1>` and nothing else.
//
// The fragment is parsed in a `body` context, which is the context it will end up in,
// and naming it is what makes that a choice rather than a default: `ParseFragment`
// reads `context.DataAtom`, and a node without one falls through to its `div`
// branch. `body` and `div` were measured to parse identically for every input a
// sanitised page body can contain — the fragment contexts `x/net/html` actually
// treats differently are `table`, `colgroup`, `select`, `html`, `head` and
// `template`, and none of those is where a page body goes. So this is documentation,
// not a fix, and it is worth being explicit that it is documentation: a comment
// claiming `div` would drop a `<li>` would be a claim a reader would have to go and
// disprove, and they would be right to.
//
// **Returns the input unchanged when there is nothing to do.** Not an optimisation:
// it means a page with no interactive elements is served byte-for-byte as the
// sanitiser wrote it, so this file cannot be the reason a golden file's bytes moved
// on a page that had no focus stops in it. And the early return is also what makes
// idempotence free — a second run finds every token already present and re-renders
// nothing.
func applyTargetClass(page string) (string, error) {
	// A page with no markup in it cannot contain a focus stop, and this is the
	// common case for a vault of short notes.
	if !strings.Contains(page, "<") {
		return page, nil
	}

	nodes, err := html.ParseFragment(strings.NewReader(page), bodyContext())
	if err != nil {
		return "", fmt.Errorf("parse rendered page: %w", err)
	}

	// Only re-render if something was written. See the doc comment: this is what
	// makes the pass a no-op on a page it has no business touching.
	rewritten := false

	for _, node := range nodes {
		markTargetStops(node, &rewritten)
	}

	if !rewritten {
		return page, nil
	}

	var out bytes.Buffer

	for _, node := range nodes {
		if err := html.Render(&out, node); err != nil {
			return "", fmt.Errorf("render page with the target class: %w", err)
		}
	}

	return out.String(), nil
}

// bodyContext is the parse context a page body is parsed in.
//
// `atom.Body` rather than a bare `Data: "body"`: `ParseFragment` checks that
// `DataAtom` agrees with `Data` and switches on the atom, so the atom is the choice.
// It is not a security control — see the caller's comment for what was measured.
func bodyContext() *html.Node {
	return &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
}

// markTargetStops walks a subtree and writes the class onto every focusable element
// that does not already carry it, reporting through rewritten whether it wrote
// anything.
//
// The walk recurses into every node rather than descending the element list in
// `targetStopElements`, because the elements that need this are the ones *inside*
// prose — a link inside a `sup`, a backref inside an `li` inside an `ol` — and a walk
// that stopped at the first recognised element would miss all of them.
func markTargetStops(node *html.Node, rewritten *bool) {
	if node.Type == html.ElementNode && isTargetStop(node) && !hasTargetClass(node) {
		writeTargetClass(node)

		*rewritten = true
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		markTargetStops(child, rewritten)
	}
}

// isTargetStop reports whether the pass owes this element the class.
//
// The `tabindex` branch is what §10.6's list has and `targetStopElements` cannot
// enumerate: any element at all becomes a focus stop when it carries one, and the
// sanitiser strips `tabindex` today, so this is a branch nothing reaches. It is here
// so that a policy which allowed `tabindex` gets the class applied by the same pass
// rather than by whoever notices the audit go red.
func isTargetStop(node *html.Node) bool {
	if targetStopElements[node.Data] {
		return true
	}

	return hasAttributeNamed(node, "tabindex")
}

// hasTargetClass reports whether the element's class list already carries the token.
//
// Token-by-token and never a substring: `target` is a substring of `target-large` and
// of `data-target`, and a check that used `strings.Contains` would call an element
// already carrying `target-large` done with it — which is a focus stop at 20px
// reporting that it satisfies §7.3.
func hasTargetClass(node *html.Node) bool {
	for _, attribute := range node.Attr {
		if attribute.Namespace != "" || attribute.Key != classAttribute {
			continue
		}

		for token := range strings.FieldsSeq(attribute.Val) {
			if token == targetClass {
				return true
			}
		}
	}

	return false
}

// writeTargetClass adds the token to an element's class list.
//
// Appended to an existing list rather than replacing it, because the classes already
// there are the renderer's identity — `wikilink`, `footnote-ref`, `language-rust` —
// and `internal/httpapi/wiki`'s address attachment matches on `class` staying intact
// alongside `data-ext`. A missing attribute becomes a new one at the end of the list,
// which is where a renderer puts it too.
func writeTargetClass(node *html.Node) {
	for index, attribute := range node.Attr {
		if attribute.Namespace != "" || attribute.Key != classAttribute {
			continue
		}

		node.Attr[index].Val = attribute.Val + " " + targetClass

		return
	}

	node.Attr = append(node.Attr, html.Attribute{Key: classAttribute, Val: targetClass})
}

// hasAttributeNamed reports whether an attribute is present, whatever its value.
//
// Present and empty counts. §10.2's `tabindex` rule makes that distinction load
// bearing elsewhere in this repository, and it is the same distinction here: `a
// tabindex=""` is still an element the pass must give the class to, and an
// implementation that read the value would skip it.
func hasAttributeNamed(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Namespace != "" || attribute.Key != name {
			continue
		}

		return true
	}

	return false
}
