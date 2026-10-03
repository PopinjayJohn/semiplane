package events_test

// The DOM helpers and the audit vocabulary `route_a11y_test.go` reads.
//
// # Why they are here and not shared
//
// `internal/httpapi`'s helpers are unexported in `httpapi_test`, and `plugins`' audit
// is a second copy for the same reason: **a test package is not importable**. A third
// copy is not ideal and the alternative is worse — an exported audit package is a
// change to a file this work item does not own, and a rule that lives in four places
// is four rules. So this file is a *narrow* copy: the rules §10.2 and §10.6 state for
// this route and nothing else, so the duplication stays small and the file a reviewer
// diffs against the others is short.
//
// `golang.org/x/net/html` rather than string matching throughout, for the reason
// AGENTS.md states as an invariant: substring matching is how "no `world`" passes while
// the word sits in an HTML comment, an `aria-label` or a `data-` attribute.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// retiredWords is UI §1.2's closed vocabulary. Neither entity exists, a leftover is a
// bug, and a grep for them is a test — which is this.
//
// Case-insensitive and a substring, because the failure being guarded against is a
// noun phrase inside a sentence, written out in full below, and neither of those is
// spelled with a capital letter in isolation.
//
// linter for unclosed work markers cannot tell a vocabulary from a marker.
//
//nolint:godox // This is the list of the two words, so it has to spell them out; a
var retiredWords = []string{"world", "session"}

// auditFailer is the subset of `*testing.T` the rules use.
//
// An interface rather than `*testing.T` because the rules have to be *tested* — a rule
// that cannot be shown to reject a violation it claims to catch is a gate wired to
// nothing — and a test of a `*testing.T`-typed function is a test that fails the suite.
// `silentFailer` in `route_a11y_self_test.go` satisfies this and records instead of
// reporting.
//
// Deliberately without `Fatalf`: the parser's own constructor aborts on a response
// that is not HTML, and that is correct behaviour for it to have.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// docAudit is one parsed document and where it came from.
type docAudit struct {
	t     auditFailer
	where string
	root  *html.Node
}

// anyTag is the tag name `findAll` accepts to mean "every element".
const anyTag = "*"

// parseDocument parses an audited response, aborting when it is not HTML.
//
// `html.Parse` is used rather than a fragment parser and that is deliberate: a
// fragment's parse result is nested inside an implied `<html><head><body>`, so the
// tree a rule walks is three levels deeper than the markup the server wrote. The
// rules walk elements and never depend on the depth, and the alternative — a fragment
// parser this repository would have to keep correct against the grammar — is the same
// duplicated-implementation trap in a different place.
func parseDocument(t *testing.T, doc renderedDocument) *docAudit {
	t.Helper()

	if !strings.Contains(strings.ToLower(string(doc.body)), "<") {
		t.Fatalf("%s: the response is not an HTML fragment (status %d); §10.2's rules "+
			"are about markup and there is nothing to audit. Begins: %q",
			doc.where, 0, truncateBody(string(doc.body)))
	}

	root, err := html.Parse(strings.NewReader(string(doc.body)))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", doc.where, err)
	}

	return &docAudit{t: t, where: doc.where, root: root}
}

// elements visits every element in the document, in document order.
func (a *docAudit) elements(visit func(*html.Node)) {
	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			visit(node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(a.root)
}

// focusStops returns every element a keyboard can reach, in document order.
//
// §10.6's list: `a[href]`, `button`, `input`, `select`, `textarea`, `summary` and
// anything with a `tabindex`.
func (a *docAudit) focusStops() []*html.Node {
	stops := make([]*html.Node, 0)

	a.elements(func(node *html.Node) {
		if isFocusStop(node) {
			stops = append(stops, node)
		}
	})

	return stops
}

// isFocusStop reports whether an element is one a keyboard can reach.
//
// **`<input type="hidden">` is excluded**, and that is the reason this is not a
// one-line tag check: a hidden field is not a focus stop, and an audit that counted it
// would demand a `.target` class on an element no keyboard can reach — which is both a
// false finding and a rule a correct fragment could not satisfy.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttribute(node, "href")
	case "button", "select", "textarea", "summary", "iframe", "audio", "video":
		return true
	case "input":
		return !strings.EqualFold(attr(node, "type"), "hidden")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// attr returns an element's attribute value, or the empty string.
func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasAttribute reports whether an element carries an attribute at all.
//
// **Presence, not a non-empty value**, and the distinction is load-bearing twice over:
// a `tabindex=""` is present and unusable, and a `data-testid=""` is a hook that
// selects nothing.
func hasAttribute(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// hasClass reports whether an element carries a class **token**.
func hasClass(node *html.Node, token string) bool {
	for candidate := range strings.FieldsSeq(attr(node, "class")) {
		if candidate == token {
			return true
		}
	}

	return false
}

// findAll returns every element with the given tag name, in document order.
func findAll(node *html.Node, tag string) []*html.Node {
	found := make([]*html.Node, 0)

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && (tag == anyTag || current.Data == tag) {
			found = append(found, current)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return found
}

// focusStopsWithin returns the tag names of every focus stop in a subtree.
func focusStopsWithin(node *html.Node) []string {
	found := []string{}

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current != node && isFocusStop(current) {
			found = append(found, current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}

	return found
}

// nodePath names an element's position for a failure message.
func nodePath(node *html.Node) string {
	if node == nil {
		return "(nil)"
	}

	var parts []string

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		if current.Data == "html" {
			break
		}

		parts = append([]string{describe(current)}, parts...)
	}

	if len(parts) == 0 {
		return node.Type.String()
	}

	return strings.Join(parts, " > ")
}

// describe names one element with its identifying attributes.
//
// A `strings.Builder` rather than `+=`: the concatenation is in a loop, and `perfsprint`
// is right that this is the shape where it shows.
func describe(node *html.Node) string {
	var described strings.Builder

	described.WriteString("<" + node.Data)

	for _, name := range []string{"id", "href", "for", "data-testid", "role", "aria-label"} {
		if value := attr(node, name); value != "" {
			described.WriteString(" " + name + "=" + strconv.Quote(value))
		}
	}

	described.WriteString(">")

	return described.String()
}

// truncateBody shortens a body for a failure message.
func truncateBody(body string) string {
	const limit = 200

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// fmtUnused keeps `fmt` in the import list for the self-test file, which shares this
// file's package.
//
// A compile-time reference rather than a blank import, because a blank import is a
// thing a reader has to look up and this is a line that says what it is.
var _ = fmt.Sprintf
