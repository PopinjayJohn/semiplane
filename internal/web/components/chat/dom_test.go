package chat_test

// The DOM helpers the audits in `chat_test.go` are written against, and the negative
// controls that prove each of them can fail.
//
// They live in their own file because the audits and the **proofs that the audits
// work** are two different kinds of statement. `chat_test.go` says "this surface
// satisfies §10.2"; this file says "and §10.2's rules are not decoration", with a
// fixture per rule that violates exactly that rule.
//
// # The helpers are one implementation, not four copies
//
// Every route package in this project carries its own copy of this vocabulary, and
// that is deliberate: an audit shared between two packages would let one package's
// markup satisfy the other's rule, and a failure would not say which package broke.
// Here there is one package, so there is one copy, and it is the shape the others
// have so that a reader comparing them is comparing rules and not spelling.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// auditFailer is what an audit needs in order to report a finding.
//
// The interface rather than `*testing.T` so `controls_test.go` can run **the same
// audit bodies** over a fixture and assert that they objected: a violation a control
// *expects* cannot be reported through the real `*testing.T`, because that would fail
// the test for the thing it is looking for — the same shape of mistake as a mutation
// that passes for the wrong reason.
//
// `Helper` is in the interface because every audit calls it, and its presence is what
// stops an audit's finding from pointing at the line in this file that made the call.
// On the `talker` adapter it is a no-op, which is the documented behaviour rather than
// an omission.
type auditFailer interface {
	// Helper marks the calling function as a helper for reporting purposes.
	Helper()
	// Errorf records one finding.
	Errorf(format string, args ...any)
}

// walk visits every **element** in document order.
//
// Elements only, and that is a limitation rather than a simplification: the
// vocabulary audit needs text nodes and comments too, and it uses `walkAll`. A single
// walk visiting everything would be one less thing to remember, and the cost is that
// every structural rule would have to re-check `node.Type` — which is how a rule ends
// up matching a comment's text.
func walk(failer auditFailer, node *html.Node, visit func(*html.Node)) {
	failer.Helper()

	walkInOrder(node, func(current *html.Node, _ int) bool {
		if current.Type == html.ElementNode {
			visit(current)
		}

		return true
	})
}

// walkAll visits every node, whatever its type.
//
// For the audits whose subjects are not elements: UI §1.2's words can live in a text
// node or an HTML comment, and neither is an element. An audit written over `walk`
// alone would report nothing for the two cases it exists for — which is precisely
// what the negative control for "the word in an HTML comment" caught during
// development.
func walkAll(failer auditFailer, node *html.Node, visit func(*html.Node)) {
	failer.Helper()

	walkInOrder(node, func(current *html.Node, _ int) bool {
		visit(current)

		return true
	})
}

// walkInOrder visits every node and its position among the document's **focus stops**.
//
// A position that is the node's *own* index among the focus stops, or `len(focusStops)`
// for a node that is not one — and `len(focusStops)` **before** any walk has run, so
// the callback can compare two nodes' positions.
//
// Returned rather than accumulated in a closure, because the skip-link rule needs the
// whole list to answer "is anything focusable before the first skip link" and a
// callback that could see only its own position would have to compare against a
// running total — which is the mistake where a rule reports the banner's links as
// preceding the skip links when the counter was incremented after the visit.
func focusStops(node *html.Node) []*html.Node {
	var stops []*html.Node

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		if current.Type == html.ElementNode && isFocusStop(current) {
			stops = append(stops, current)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)

	return stops
}

// walkInOrder visits every node, with its focus-stop position.
func walkInOrder(node *html.Node, visit func(*html.Node, int) bool) {
	stops := focusStops(node)
	index := make(map[*html.Node]int, len(stops))

	for position, stop := range stops {
		index[stop] = position
	}

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		if !visit(current, index[current]) {
			return
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)
}

// collect returns every node in the subtree, elements included, in document order.
//
// Used where the assertion is about a node *existing* rather than about an
// attribute: a `<script>` that a message body produced is the failure, and a rule
// about one element's attributes would not see it.
func collect(node *html.Node) []*html.Node {
	var nodes []*html.Node

	var descend func(*html.Node)

	descend = func(current *html.Node) {
		nodes = append(nodes, current)

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			descend(child)
		}
	}

	descend(node)

	return nodes
}

// focusStopElements is §10.6's list.
//
// A `set` rather than a switch so the same list answers both questions the audits
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
// **Every element carrying `tabindex` counts**, whatever it is: `tabindex="-1"` is
// not in the tab order but it *is* a focus stop for `focus()` and §7.2's skip links
// rely on it. A rule that only looked at the element name would miss every landmark
// the shell focuses, which is three of them.
func isFocusStop(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	if focusStopElements[node.Data] {
		return true
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
	return slices.Contains(slices.Collect(strings.FieldsSeq(attributeOr(node, "class"))), class)
}

// attribute returns an attribute's value, and whether the attribute is present at
// all.
//
// Both, because §10.2's rules turn on the difference: a `<label>` with no `for` and a
// `<label for="">` are not the same element, and a field that lost a hint is a
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

// countElements counts the elements with a given tag name in a subtree.
func countElements(node *html.Node, tag string) int {
	count := 0

	walk(countingFailer{}, node, func(current *html.Node) {
		if current.Data == tag {
			count++
		}
	})

	return count
}

// countingFailer is an `auditFailer` that counts findings and reports nothing.
//
// `countElements` is a query rather than an audit — it returns a number and asserts
// nothing — and the alternative is threading a `*testing.T` into every call site for a
// helper that has no failures to report.
type countingFailer struct{}

func (countingFailer) Helper()               {}
func (countingFailer) Errorf(string, ...any) {}

// find returns the first element matching a CSS selector, or nil.
//
// `golang.org/x/net/html` has no selector engine, so a full selector is out; what the
// audits need is attribute equality, and the selectors below are all of that shape.
// A helper that took a real selector would be a second thing to be right about, and
// the ones in use here are deliberately the simple form.
func find(node *html.Node, selector string) *html.Node {
	name, value, ok := parseAttributeSelector(selector)
	if !ok {
		return nil
	}

	var found *html.Node

	walk(countingFailer{}, node, func(current *html.Node) {
		if found != nil {
			return
		}

		if current.Data != name {
			return
		}

		if attributeOr(current, value[0]) == strings.Trim(value[1], `"`) {
			found = current
		}
	})

	return found
}

// mustFind is `find` with a failure rather than a nil, so a renamed hook stops the
// test where it is used instead of returning nil into an assertion that would then
// read as an absence.
func mustFind(t *testing.T, node *html.Node, selector string) *html.Node {
	t.Helper()

	found := find(node, selector)
	if found == nil {
		t.Fatalf("nothing matches %s; the fixture no longer renders what it claims to", selector)
	}

	return found
}

// parseAttributeSelector splits `[name="value"]` into its three parts, and the
// element name before the bracket.
//
// `name` is the first return, `value` is `[attribute, value]`, and `ok` is false for
// anything else — which the callers treat as "no such element", so a typo in a
// selector produces a failed assertion rather than a silent match on everything.
func parseAttributeSelector(selector string) (name string, value []string, ok bool) {
	open := strings.Index(selector, "[")
	if open < 0 || !strings.HasSuffix(selector, "]") {
		return "", nil, false
	}

	name = selector[:open]
	inner := selector[open+1 : len(selector)-1]

	before, after, found := strings.Cut(inner, "=")
	if !found {
		return "", nil, false
	}

	return name, []string{strings.TrimSpace(before), strings.TrimSpace(after)}, true
}

// where is a short human location for a node, so a finding names the element rather
// than a line number in the renderer.
//
// The ancestry rather than the path: a panel's second message is "under section.chat"
// and not "html > body > div.shell > main > section.panel > ol > li:nth-child(2)".
func where(node *html.Node) string {
	var parts []string

	for current := node; current != nil; current = current.Parent {
		if current.Type != html.ElementNode {
			continue
		}

		part := current.Data

		if value, present := attribute(current, "data-testid"); present && value != "" {
			part += "[" + value + "]"
		} else if value, present := attribute(current, "class"); present && value != "" {
			part += "." + strings.Fields(value)[0]
		}

		parts = append([]string{part}, parts...)
	}

	return strings.Join(parts, " > ")
}

// liveRegion is one announced region in a document.
type liveRegion struct {
	// role is the element's `role`, which may be empty.
	role string
	// politeness is the `aria-live` value, or `""` when the element relies on its
	// role's implicit politeness.
	politeness string
	// testID is the element's `data-testid`, so a finding names the element.
	testID string
}

// String renders a region for a failure message.
//
// A struct's default `%v` prints every field on one line with no labels, and a
// finding that reads `{  <4 empty field>}` cannot be acted on.
func (r liveRegion) String() string {
	return fmt.Sprintf("role=%q aria-live=%q testid=%q", r.role, r.politeness, r.testID)
}

// liveRegionRoles are the roles that are live regions whether or not they carry
// `aria-live`.
//
// §13.3's names, and the point of the list is that checking **only** the attribute
// misses every region written the conventional way — `role="status"` with no
// `aria-live` is how a live region arrives by accident most often.
var liveRegionRoles = map[string]bool{
	"alert":   true,
	"log":     true,
	"status":  true,
	"timer":   true,
	"marquee": true,
}

// announcingRegions is `liveRegions` without the ones that have opted out.
//
// **The `off` case is a decision, not a filter's convenience.** `aria-live="off"` is an
// element saying "I am a region and I am not announcing", which is the *answer* to the
// question §7.5 asks rather than a violation of it — it is how a chat surface honours
// "must not replay history into a live region on connect". A gate that counted it would
// make the correct markup unrepresentable, and the way that would be discovered is a
// component removing the attribute and re-breaking the replay rule.
func announcingRegions(document *html.Node) []liveRegion {
	all := liveRegions(document)
	regions := make([]liveRegion, 0, len(all))

	for _, region := range all {
		if region.politeness == "off" {
			continue
		}

		regions = append(regions, region)
	}

	return regions
}

// liveRegions collects every live region in a document.
//
// Both halves of the rule, and they are not the same check: an element carrying
// `aria-live` in any value is a region (including `off`, which is a region that has
// chosen not to announce), and an element with one of `liveRegionRoles` is a region
// whatever its attributes say. §7.5's search rule forbids the region, so an element
// that is *configured* as one counts even when it is currently quiet — which is
// exactly what `internal/httpapi/search`'s own audit says about `aria-live="off"`.
//
// `role="region"` is deliberately **not** in the list: it is a landmark, not an
// announcer, and including it would make every labelled `<section>` on a page a live
// region, which is the search route's own false positive written the other way.
func liveRegions(document *html.Node) []liveRegion {
	var regions []liveRegion

	walk(countingFailer{}, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		role := attributeOr(node, "role")
		politeness, hasAttribute := attribute(node, "aria-live")

		if !liveRegionRoles[role] && !hasAttribute {
			return
		}

		regions = append(regions, liveRegion{
			role:       role,
			politeness: politeness,
			testID:     attributeOr(node, "data-testid"),
		})
	})

	return regions
}
