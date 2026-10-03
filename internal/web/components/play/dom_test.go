package play_test

// The DOM helpers every audit in this package uses.
//
// **One implementation, not one per file.** `controls_test.go` runs the audit
// bodies in `play_test.go` over documents built to violate one rule each, and a
// helper copied into it would be a second definition of what a focus stop is. The
// repository has already paid for this once: a per-route copy of the target-size
// audit is how a package ends up auditing a slightly different set of elements
// than the stylesheet's gate does.

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// auditer is the reporting surface an audit body needs, and the reason the audit
// bodies in `play_test.go` take an interface rather than a `*testing.T`.
//
// `controls_test.go` supplies a recorder that implements it, so the controls run
// **the same functions** rather than copies. A copy for the controls is a second
// answer to the same question, and it is the copy that would keep passing after the
// real audit stopped looking.
type auditer interface {
	// Helper marks the calling function as a helper, so a failure is attributed to
	// the caller rather than to the line inside the audit.
	Helper()
	// Errorf reports a finding and continues.
	Errorf(format string, args ...any)
	// Fatalf reports a finding and ends the audit.
	Fatalf(format string, args ...any)
	// Fatal reports a finding and ends the audit.
	Fatal(args ...any)
}

// atomButton and atomSpan are the element atoms the fixtures build nodes with.
//
// `html.Node.DataAtom` rather than `Data`: the audits compare against the atom
// because `html.Parse` fills it and a hand-built node that sets only `Data` would
// otherwise compare unequal to a parsed one.
//
// **`var` rather than `const`**, because `atom.Lookup` is a function and a
// hand-built fixture has to set the atom explicitly.
var (
	atomButton = atom.Lookup([]byte("button"))
	atomSpan   = atom.Lookup([]byte("span"))
)

// walk visits every node in the subtree, in document order.
//
// The visitor returns whether to descend into the node. That is what makes a
// **subtree** test possible — the vocabulary carve-out is "the placement's name and
// nothing inside it, and nothing outside it", and a walker that cannot skip a
// subtree can only skip a node, which would check the name's own text while still
// descending into whatever is inside it.
func walk(root *html.Node, visit func(node *html.Node) bool) {
	if !visit(root) {
		return
	}

	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walk(child, visit)
	}
}

// walkAll visits every node in the subtree, in document order, with no skipping.
//
// The common case, and a separate function so the ordinary call sites do not each
// carry a `return true`.
func walkAll(root *html.Node, visit func(node *html.Node)) {
	walk(root, func(node *html.Node) bool {
		visit(node)

		return true
	})
}

// attribute returns one attribute's value.
func attribute(node *html.Node, name string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val, true
		}
	}

	return "", false
}

// attributeOr returns one attribute's value, or "".
func attributeOr(node *html.Node, name string) string {
	value, _ := attribute(node, name)

	return value
}

// hasClass reports whether the element's class attribute names the class.
func hasClass(node *html.Node, class string) bool {
	for name := range strings.FieldsSeq(attributeOr(node, "class")) {
		if name == class {
			return true
		}
	}

	return false
}

// elementWithID returns the element carrying the id, or nil.
func elementWithID(root *html.Node, id string) *html.Node {
	var found *html.Node

	walkAll(root, func(node *html.Node) {
		if found != nil || node.Type != html.ElementNode {
			return
		}

		if value, present := attribute(node, "id"); present && value == id {
			found = node
		}
	})

	return found
}

// focusableElements are the elements §10.6's target-size walk visits.
//
// The same set AGENTS.md names for the stylesheet-side gate, and kept as one list
// for the reason that list is spelled out: two sets would mean two documents'
// worth of focus stops being measured.
var focusableElements = []string{
	"a", "button", "input", "select", "textarea", "summary", "audio", "video",
}

// focusStops returns the focusable elements inside a node, the node included.
//
// The node itself counts when it is focusable, because the audits are applied to
// elements rather than to containers: "every focus stop carries `.target`" is a
// statement about an element, and testing it against a container's *descendants*
// would skip the container.
func focusStops(root *html.Node) []*html.Node {
	var stops []*html.Node

	walkAll(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if focusable(node) {
			stops = append(stops, node)
		}
	})

	return stops
}

// focusable reports whether an element is in the tab order by virtue of what it
// is, plus anything carrying a `tabindex`.
//
// The `tabindex` clause is §10.6's own set and the reason the walk cannot be a
// lookup on tag name: `tabindex="-1"` removes an element from the tab order and
// `tabindex="0"` puts it back, so the class requirement has to follow them.
func focusable(node *html.Node) bool {
	if _, present := attribute(node, "tabindex"); present {
		return true
	}

	for _, name := range focusableElements {
		if node.Data == name {
			// An `<a>` without an `href` is not in the tab order, and neither is
			// a form control with `disabled`. Both are the cases where the tag
			// alone would over-report.
			if name == "a" && !hasAttribute(node, "href") {
				return false
			}

			if hasAttribute(node, "disabled") {
				return false
			}

			return true
		}
	}

	return false
}

// hasAttribute reports whether an attribute is present at all.
func hasAttribute(node *html.Node, name string) bool {
	_, present := attribute(node, name)

	return present
}

// elementsWith returns every element carrying the given attribute with the given
// value, in document order.
func elementsWith(t *testing.T, root *html.Node, name string) []*html.Node {
	t.Helper()

	var found []*html.Node

	walkAll(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if _, present := attribute(node, name); present {
			found = append(found, node)
		}
	})

	return found
}

// elementsWithValue returns every element whose named attribute has the named
// value.
func elementsWithValue(t *testing.T, root *html.Node, name, value string) []*html.Node {
	t.Helper()

	var found []*html.Node

	walkAll(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if got, present := attribute(node, name); present && got == value {
			found = append(found, node)
		}
	})

	return found
}

// textOf returns an element's text, which is what its accessible name is when it
// carries no `aria-label`.
//
// Nested elements are flattened, because a row's four facts are four spans and
// the name is the concatenation of all of them.
func textOf(node *html.Node) string {
	var out strings.Builder

	walkAll(node, func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}
	})

	return strings.Join(strings.Fields(out.String()), " ")
}
