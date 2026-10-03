package secret_test

// The DOM vocabulary every audit in this package is written against, and the
// negative controls that prove each of them can fail.
//
// Four conventions, each learned from a gate that passed for the wrong reason:
//
//   - **Parse the DOM; never substring-match the markup.** "No secret text in the
//     player's document" is a claim about what a screen reader reaches, and
//     `strings.Contains(document, "secret")` cannot see a word in an HTML comment,
//     in an attribute *name*, or in an attribute value nobody renders. Every audit
//     below walks `golang.org/x/net/html`.
//   - **Every audit must be able to fail.** `controls_test.go` runs each audit
//     body over a fixture built to violate exactly that rule and requires a
//     finding that *mentions the rule*. A rule that fires for an unrelated reason
//     looks exactly like one that works.
//   - **Every audit must also be capable of reporting nothing.** A red suite in
//     the first week is how an audit gets switched off rather than fixed.
//   - **A carve-out is exactly as wide as it claims.** There is one here —
//     `CalloutChrome`'s title, which is author text — and
//     `TestTheVocabularyCarveOutIsExactlyTheCalloutTitle` holds it to that width.
//
// The audits take an interface rather than a `*testing.T` so `controls_test.go`
// can run the *same bodies* over a fixture. A copy of an audit for the controls
// is the failure this file exists to prevent: a copy is a second answer to the
// same question, and it drifts.

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// auditFailer is what an audit needs in order to report a finding.
//
// The interface rather than `*testing.T` for the reason in the file header: a
// violation a control *expects* cannot be reported through the real `*testing.T`,
// because that would fail the test for the thing it is looking for — the same
// shape of mistake as a mutation that passes for the wrong reason.
//
// `Helper` is in the interface because every audit calls it, and its presence is
// what stops an audit's finding from pointing at the line in this file that made
// the call. On the `talker` adapter it is a no-op, which is the documented
// behaviour rather than an omission.
type auditFailer interface {
	// Helper marks the calling function as a helper for reporting purposes.
	Helper()
	// Errorf records one finding.
	Errorf(format string, args ...any)
}

// walk visits every **element** in document order.
//
// Elements only, and that is a limitation rather than a simplification: the
// vocabulary audit needs text nodes and comments too, and it uses `walkAll`. One
// walk visiting everything would be one less thing to remember, and the cost is
// that every structural rule would have to re-check `node.Type` — which is how a
// rule ends up matching a comment's text.
func walk(failer auditFailer, node *html.Node, visit func(*html.Node)) {
	failer.Helper()

	var descend func(*html.Node, int)

	descend = func(current *html.Node, depth int) {
		if current.Type == html.ElementNode {
			visit(current)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child, depth+1)
		}
	}

	descend(node, 0)
}

// walkAll visits every node, whatever its type.
//
// For the audits whose subjects are not elements: UI §1.2's retired words can live
// in a text node or an HTML comment, and neither is an element. An audit written
// over `walk` alone would report nothing for the two cases it exists for.
func walkAll(failer auditFailer, node *html.Node, visit func(*html.Node)) {
	failer.Helper()

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		visit(current)

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)
}

// focusStopElements is §10.6's list.
//
// A `map` rather than a switch so the same list answers both questions the audits
// ask — "is this element focusable" and "is this element a target stop" — and a
// list rather than a rule because the set is closed and a page body is closed too.
var focusStopElements = map[string]bool{
	"a":        true,
	"button":   true,
	"input":    true,
	"select":   true,
	"textarea": true,
	"summary":  true,
}

// isFocusStop reports whether an element is one a keyboard can reach.
//
// **Every element carrying `tabindex` counts**, whatever its value: `tabindex="-1"`
// is not in the tab order but it *is* a focus stop for `focus()`, and §7.2's skip
// links rely on it. A rule that only looked at the element name would miss every
// landmark the shell focuses, which is three of them.
func isFocusStop(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	if focusStopElements[node.Data] {
		// `<input type="hidden">` is not reachable, and §10.6's audit in
		// `internal/web` excludes it for the reason `plugins`' audit gives: a
		// hidden field is not a focus stop and failing one would be a rule a
		// correct document could not satisfy.
		return !strings.EqualFold(attributeOr(node, "type"), "hidden")
	}

	return hasAttribute(node, "tabindex")
}

// containsFocusStop reports whether a subtree holds anything a keyboard can reach.
func containsFocusStop(node *html.Node) bool {
	found := false

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		if found {
			return
		}

		if isFocusStop(current) {
			found = true

			return
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)

	return found
}

// hasClass reports whether an element carries a class token.
//
// Split on whitespace and compared whole, because `strings.Contains(class, "target")`
// is true for `data-target` and for `notarget` — and a target-size gate that passes
// on `class="notarget"` is a gate wired to nothing.
func hasClass(node *html.Node, class string) bool {
	for token := range strings.FieldsSeq(attributeOr(node, "class")) {
		if token == class {
			return true
		}
	}

	return false
}

// attribute returns an attribute's value, and whether the attribute is present at
// all.
//
// Both, because §10.2's rules turn on the difference: a `<label>` with no `for` and
// a `<label for="">` are not the same element, and a field that lost a hint is a
// finding either way for different reasons.
func attribute(node *html.Node, name string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val, true
		}
	}

	return "", false
}

// hasAttribute reports whether an attribute is present, whatever its value.
func hasAttribute(node *html.Node, name string) bool {
	_, present := attribute(node, name)

	return present
}

// attributeOr is `attribute` with the "not present" case folded into the empty
// string, for the rules where absent and empty mean the same thing.
func attributeOr(node *html.Node, name string) string {
	value, _ := attribute(node, name)

	return value
}

// anyAttributeNamed reports whether an element carries an attribute whose name
// matches, anywhere.
//
// **The *name*, not the value.** This is the shape a substring scan over markup
// cannot answer: `data-secret`, `data-secret-state` and `data-secret-anchor` are
// three disclosures of one kind, and a scan for the literal `data-secret` finds
// all three only by accident of spelling. A callout's presence is a *family* of
// attributes, so the audit matches the family.
func anyAttributeNamed(node *html.Node, prefix string) []string {
	var found []string

	for _, attr := range node.Attr {
		if strings.HasPrefix(attr.Key, prefix) {
			found = append(found, attr.Key)
		}
	}

	return found
}

// headingLevel returns an element's heading level, and whether it is a heading.
func headingLevel(node *html.Node) (int, bool) {
	if node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0, false
	}

	if node.Data[1] < '1' || node.Data[1] > '6' {
		return 0, false
	}

	return int(node.Data[1] - '0'), true
}

// nodeText concatenates every text node in a subtree, whitespace-collapsed.
//
// **Text nodes only, and no attribute values** — so this is the *content* a reader
// hears, which is what "the prose reads as though the sentence was never there" is
// a claim about. `accessibleName` is the other half and is a separate function,
// because a word in an `aria-label` is announced just as loudly as one in a `<p>`
// and neither is reachable by reading the other.
func nodeText(node *html.Node) string {
	var text strings.Builder

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			text.WriteString(" ")
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)

	return strings.Join(strings.Fields(text.String()), " ")
}

// accessibleName is the name an assistive technology computes for an element,
// computed here well enough to find a disclosure.
//
// **Every source a name can come from**, which is the point: `aria-label`,
// `aria-labelledby` (resolved), `alt`, `title`, and the element's own text.
// A scan reading only the text nodes misses all four, and a scan reading only
// `aria-label` misses the fifth — and the fifth is where a callout's title would
// land if a template rendered one.
func accessibleName(node *html.Node) string {
	if node.Type != html.ElementNode {
		return ""
	}

	if label, ok := attribute(node, "aria-label"); ok && strings.TrimSpace(label) != "" {
		return strings.TrimSpace(label)
	}

	if alt := strings.TrimSpace(attributeOr(node, "alt")); alt != "" {
		return alt
	}

	if title := strings.TrimSpace(attributeOr(node, "title")); title != "" {
		return title
	}

	return nodeText(node)
}

// describe names one node in a finding, so a failure names an element rather than
// a line number in the test.
//
// The tag, the class, and the test hook. **Never the element's text**: a finding
// is the thing that gets logged and copy-pasted into an issue, and the text of a
// node in a secrets component is exactly the kind of thing that must not travel.
func describe(node *html.Node) string {
	if node == nil || node.Type != html.ElementNode {
		return "a non-element node"
	}

	if hook := attributeOr(node, "data-testid"); hook != "" {
		return "<" + node.Data + ">[" + hook + "]"
	}

	if class := attributeOr(node, "class"); class != "" {
		return "<" + node.Data + ">." + class
	}

	return "<" + node.Data + ">"
}

// failNode reports one finding about a node.
func failNode(failer auditFailer, node *html.Node, format string, args ...any) {
	failer.Helper()
	failer.Errorf("%s: %s", describe(node), fmt.Sprintf(format, args...))
}
