package wiki_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The DOM helpers every §10.2 and §10.6 assertion in this package runs against.
//
// They exist rather than the assertions inlining them because three of the four
// ways a §10.2 rule passes vacuously are ways a *helper* passes vacuously: a walk
// that skips a node type, a lookup that cannot tell an absent attribute from an
// empty one, and a matcher that scans forward from a selector when the mutation
// under test wraps the selector outside. Each helper below states which of those
// it avoids and why, because a helper that quietly does one of them is worse than
// no helper — every assertion built on it inherits the blind spot and looks
// thorough while checking nothing.

// parseDocument parses a rendered response body, failing the test if it will not.
//
// `html.Parse` is a real parser and not a matcher, which is the whole point: it
// recovers from the `<` and `>` a page body contains as text, resolves an
// `aria-label` on an element a template spelled across an attribute boundary, and
// keeps an HTML comment as a node a rule can inspect. A substring assertion over
// the same bytes finds all three and cannot tell them from the thing it was
// looking for.
func parseDocument(t *testing.T, document string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("parse rendered document: %v", err)
	}

	return root
}

// eachElement calls visit for every element, in document order.
//
// Every *element*, and not every node: a text node has no attributes and no tag,
// and a rule that visited one would have to decide what to check, and a rule that
// decided "nothing" would silently skip the text it was written to police. The
// vocabulary rule below is the exception and it walks nodes itself.
//
// Document order rather than depth-first-by-branch, because two of the rules in
// this file are about the order a keyboard meets things: the skip links are the
// first focusable elements (UI §7.2), and a landmark's nesting changes its role.
func eachElement(node *html.Node, visit func(*html.Node)) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			visit(child)
		}

		eachElement(child, visit)
	}
}

// findElements returns every element with the given tag name, in document order.
func findElements(root *html.Node, tag string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if node.Data == tag {
			found = append(found, node)
		}
	})

	return found
}

// attribute returns an element's attribute value, or empty when it has none.
//
// Explicitly *empty* rather than absent, and the distinction is not pedantry: an
// `aria-label=""` names a landmark nothing, which §10.2 counts as unlabelled,
// and a helper that returned "" for both would let the two cases through one
// assertion. Where a rule needs the difference it asks `hasAttribute`, and
// `TestTheAttributeHelperTellsAbsentFromEmpty` is what holds the pair to it.
func attribute(node *html.Node, name string) string {
	if node == nil {
		return ""
	}

	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether an element carries the attribute at all.
func hasAttribute(node *html.Node, name string) bool {
	if node == nil {
		return false
	}

	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// elementByID returns the element carrying this id, or nil.
func elementByID(root *html.Node, id string) *html.Node {
	var found *html.Node

	eachElement(root, func(node *html.Node) {
		if found == nil && attribute(node, "id") == id {
			found = node
		}
	})

	return found
}

// elementByTestID returns the element carrying this data-testid, failing the test
// when there is not exactly one.
//
// A test hook rather than a CSS class, per the repo's convention: the class is a
// rendering decision the stylesheet owns and a class-name assertion breaks when
// somebody renames a class for readability, whereas a test hook is a contract.
// Failing on *more* than one is deliberate — a hook that appears twice is a hook
// nothing can select, and an assertion that took the first would report on one of
// two elements and pass.
func elementByTestID(t *testing.T, root *html.Node, id string) *html.Node {
	t.Helper()

	found := elementsWithTestID(root, id)
	if len(found) != 1 {
		t.Fatalf("the document has %d elements with data-testid=%q, want exactly 1",
			len(found), id)
	}

	return found[0]
}

// elementsWithTestID returns every element carrying this data-testid.
func elementsWithTestID(root *html.Node, id string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if attribute(node, "data-testid") == id {
			found = append(found, node)
		}
	})

	return found
}

// elementsWithRole returns every element whose explicit `role` is this one.
//
// Role and not "any attribute": every §10.2 landmark rule in this package is a
// question about the role an element claims, so a helper with an attribute name
// as a parameter would be a helper whose second argument nothing ever varied.
func elementsWithRole(root *html.Node, role string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if attribute(node, "role") == role {
			found = append(found, node)
		}
	})

	return found
}

// hasAncestor reports whether any ancestor has this tag name.
//
// Walked rather than assumed from document order, because §4.5's rule is about
// nesting: a `nav` inside the rail is a navigation *inside* a complementary
// landmark, which is a different document from one where they are siblings.
func hasAncestor(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// textOf returns an element's descendant text nodes, concatenated.
//
// Text nodes only, and *not* attribute values: an `alt` or a `title` is text a
// reader hears, but it is reached by the attribute scan in the vocabulary rule
// below rather than smuggled into this one — a helper that returned attributes
// too would make "no label mentions X" and "no attribute mentions X" the same
// assertion, and the second is strictly the stronger one.
func textOf(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

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

// classes returns an element's class list, split on whitespace.
//
// A class attribute is a space-separated *set*, so the split is what makes
// "carries `.target`" a question with an answer: `strings.Contains(class, "target")`
// would also match `target-size`, and the class is load-bearing in exactly the
// places §10.6 cares about.
func classes(node *html.Node) []string {
	return strings.Fields(attribute(node, "class"))
}

// errListing is the failure a membership listing produces when the database
// cannot answer it.
var errListing = errors.New("test: campaign listing unavailable")

// --- The audits themselves ---------------------------------------------------

// audit is one rendered document and the findings one rule produced on it.
//
// A type rather than a set of free functions because the rules below all need the
// same two things — the parsed tree, and a name for where a violation is — and
// because the counting is what makes them testable: an audit accumulates findings
// rather than failing as it walks, so a test can hand one a document built to
// violate exactly its rule and then ask whether it said anything.
//
// That asking is the point. Three of phase 5's first-draft gate tests could not
// fail, and an audit that reports nothing is the same failure with a green light
// on top: it is a gate wired to nothing, and nobody finds out until the thing it
// watches is broken.
type audit struct {
	// where names the document in a failure message.
	where string
	// root is the parsed document.
	root *html.Node
	// findings accumulates every violation, so a test can assert on the count as
	// well as read the messages.
	findings []string
}

// report prints every finding against the document's name.
//
// A method rather than each audit calling `t.Errorf` itself, for the reason
// `TestEveryAuditFailsOnTheThingItWatches` needs a count: an audit that reports as
// it walks cannot be counted afterwards. It is also the only way a *self-test* of
// an audit can exist — a test that made `*testing.T` fail would fail the suite,
// which is why the audits take a value with a slice and not a `t`.
func (a *audit) report(t *testing.T) {
	t.Helper()

	for _, finding := range a.findings {
		t.Errorf("%s: %s", a.where, finding)
	}
}

// nodePath names an element for a failure message: its nearest three ancestors,
// innermost first, each labelled by id, test hook or first class.
//
// Innermost first and truncated, because a document's full ancestry is a paragraph
// long and a failure message nobody reads is a failure nobody fixes.
func nodePath(node *html.Node) string {
	parts := []string{}

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		label := current.Data

		switch {
		case attribute(current, "id") != "":
			label += "#" + attribute(current, "id")
		case attribute(current, "data-testid") != "":
			label += "[" + attribute(current, "data-testid") + "]"
		default:
			if named := classes(current); len(named) > 0 {
				label += "." + named[0]
			}
		}

		parts = append([]string{label}, parts...)
	}

	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}

	return strings.Join(parts, " > ")
}

// --- §10.6: every interactive element carries `.target` -----------------------

// interactiveElements returns every element §10.6's audit walks.
//
// The set is §10.6's own and it is spelled out rather than derived from a CSS
// selector, because a selector is evaluated by `cascadia` against this project's
// opinion of what is interactive, and that opinion is the thing under test. A
// selector that missed an element type would produce an audit that could not fail
// on the type it missed — which is how a gate comes to pass while the thing it
// watches is broken.
func interactiveElements(root *html.Node) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if isInteractive(node) {
			found = append(found, node)
		}
	})

	return found
}

// isInteractive reports whether an element is one §10.6 walks.
//
// `a[href]` rather than every `<a>`: an anchor with no `href` is not a link and
// not focusable, so it is not a target. The check reads the attribute rather than
// the selector `a[href]` so that the distinction is a *fact about the node* the
// assertion can be shown, and so a mutation that removes the `href` from a link
// is caught here instead of quietly leaving the element out of the audit — which
// is the single most likely way this rule comes to pass vacuously.
func isInteractive(node *html.Node) bool {
	switch node.Data {
	case "button", "input", "select", "textarea", "summary":
		return true
	case "a":
		return hasAttribute(node, "href")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// auditTargets requires every interactive element to carry `.target` (UI §10.6).
//
// The class is on the element and the minimum size is in the stylesheet, which is
// why this is a structural rule and not a measurement: the markup says what the
// element is and the sheet says what that means, and `internal/web`'s §7.3 gate
// asserts the sheet's side from the built artefact.
func auditTargets(a *audit) {
	for _, node := range interactiveElements(a.root) {
		if !slices.Contains(classes(node), "target") {
			a.findings = append(a.findings,
				"<"+node.Data+"> at "+nodePath(node)+" has no .target class (UI §10.6)")
		}
	}
}

// --- §10.2: no positive tabindex --------------------------------------------

// auditTabindex requires that no element carries a `tabindex` above zero.
//
// The parse matters more than it looks. `tabindex="2"` in the markup becomes
// `tabindex="2"` in the tree, so a comparison of the raw attribute against the
// string "0" would also reject a negative value written as `-1` only by accident
// of its spelling, and a comparison against `""` would accept `"0"` because that
// parses to zero and zero is falsy in a language that will let you write it. So the
// value is parsed with `strconv` and compared as a number, and a `tabindex` that
// is not a number at all is a finding: an unparseable one is a value no browser
// agrees on, and three browsers disagreeing about the tab order is the failure.
func auditTabindex(a *audit) {
	eachElement(a.root, func(node *html.Node) {
		if !hasAttribute(node, "tabindex") {
			return
		}

		raw := attribute(node, "tabindex")

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			a.findings = append(a.findings,
				"<"+node.Data+"> at "+nodePath(node)+" has tabindex=\""+raw+
					"\", which is not a number; three browsers would disagree about "+
					"the tab order (UI §7.2)")

			return
		}

		if value > 0 {
			a.findings = append(a.findings,
				"<"+node.Data+"> at "+nodePath(node)+" has tabindex=\""+raw+
					"\", want at most 0; a positive value replaces the document order "+
					"with an author's ordering (UI §7.2)")
		}
	})
}

// --- UI §1.2: neither retired entity is named -------------------------------

// retiredEntities is the closed vocabulary. Neither exists; a leftover is a bug,
// and a grep for them is a test — which is what this is.
//
// Matched case-insensitively and as a substring, because the failure being guarded
// against is a noun phrase inside a sentence ("your session has expired", "the
// world map") and neither is spelled with a capital letter in isolation.
var retiredEntities = []string{"world", "session"}

// auditVocabulary requires that no rendered string names a retired entity.
//
// Four carriers, and each is one a substring assertion over the response body
// cannot distinguish from another — which is why the assertion is over the tree:
//
//   - **text nodes**, which is the copy a reader hears;
//   - **comments**, which a reader never sees and which is therefore the one a
//     rendered-document reader would skip;
//   - **attribute values**, where an `aria-label` or a `title` is text a screen
//     reader announces;
//   - **attribute names**, which nothing renders and which a substring scan over
//     the markup *would* find while a rendered-document reader would not. The word
//     in a name is the nastiest of the four because the two obvious implementations
//     disagree about it, and the one that only reads values is the one that ships.
func auditVocabulary(a *audit) {
	forbidden := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, entity := range retiredEntities {
			if strings.Contains(lowered, entity) {
				a.findings = append(a.findings,
					where+" contains \""+entity+"\"; neither entity exists in the "+
						"interface (UI §1.2)")
			}
		}
	}

	var walk func(node *html.Node, path string)

	walk = func(node *html.Node, path string) {
		switch node.Type {
		case html.TextNode:
			forbidden("a text node at "+path, node.Data)
		case html.CommentNode:
			forbidden("a comment at "+path, node.Data)
		case html.ElementNode:
			// The attributes are walked by key *and* by value, which is the pair a
			// one-sided audit gets wrong. `data-world` is a name nothing renders;
			// `aria-label="game sessions"` is a value a reader hears.
			for _, attr := range node.Attr {
				forbidden("the attribute name "+attr.Key+" at "+path, attr.Key)
				forbidden("the "+attr.Key+" attribute at "+path, attr.Val)
			}

			path = path + "/" + node.Data
		case html.DoctypeNode:
			// A doctype is a rendered string — it is what a browser's view-source
			// shows first — and it is not a `<!DOCTYPE html>`, so the check is worth
			// running rather than assuming.
			forbidden("the doctype at "+path, node.Data)
		default:
			// `html.DocumentNode` carries no text of its own beyond its children's,
			// and `html.ErrorNode` and `html.RawNode` never appear in a parsed
			// document: `html.Parse` recovers from them. The default is here because
			// the linter is right that a switch over an enum with no default is a
			// switch whose next case is somebody's problem.
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, path)
		}
	}

	walk(a.root, "")
}

// --- §10.2: no heading is repeated ------------------------------------------

// auditHeadings requires that no two headings in a document read the same.
//
// §7.2's outline rule has three halves and this is the fourth: exactly one `<h1>`
// (asserted where the page's own title is asserted, since it is that heading which
// must be unique), levels that never skip, and **no repeated heading**. The third
// is the one a reader notices: a screen reader's heading list is a table of
// contents, and two rows reading "Not working" is a document whose outline lies
// about its own structure.
//
// The comparison folds case and collapses whitespace, because two headings that
// differ only in a line break between the words are the same heading to a reader
// and a comparison that treated them as distinct would pass on the document this
// rule was written about.
func auditHeadings(a *audit) {
	seen := map[string]string{}

	eachElement(a.root, func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		if _, err := strconv.Atoi(node.Data[1:]); err != nil {
			return
		}

		text := normaliseHeading(textOf(node))
		if text == "" {
			return
		}

		if first, repeated := seen[text]; repeated {
			a.findings = append(a.findings,
				"<"+node.Data+"> reading \""+text+"\" at "+nodePath(node)+
					" repeats the heading at "+first+" (UI §7.2)")

			return
		}

		seen[text] = nodePath(node)
	})
}

// normaliseHeading folds a heading's text for comparison.
func normaliseHeading(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// TestTheAuditHelpersTellAbsentFromEmpty is the self-test for `attribute` and
// `hasAttribute`, and it exists because the failure it guards is silent.
//
// An audit that read `aria-label` through a helper returning "" for both an absent
// attribute and an empty one would pass on `aria-label=""` — a landmark named
// nothing, which §10.2 counts as unlabelled — because "" is what an absent
// attribute also returns. Nothing about the resulting failure would look wrong.
func TestTheAuditHelpersTellAbsentFromEmpty(t *testing.T) {
	t.Parallel()

	const empty = `<html><body><nav aria-label="" id="named-nothing"></nav>` +
		`<nav role="navigation" id="unnamed"></nav></body></html>`

	root := parseDocument(t, empty)

	named := elementByID(root, "named-nothing")
	unnamed := elementByID(root, "unnamed")

	if !hasAttribute(named, "aria-label") {
		t.Error(`hasAttribute says a present aria-label="" is absent`)
	}

	if attribute(named, "aria-label") != "" {
		t.Errorf(`attribute returned %q for aria-label="", want ""`,
			attribute(named, "aria-label"))
	}

	if hasAttribute(unnamed, "aria-label") {
		t.Error("hasAttribute says an absent aria-label is present")
	}

	// And the pair together: the first names nothing and the second is unnamed, so
	// a rule keyed on "the value is empty" cannot tell them apart, and a rule keyed
	// on the pair can. This is the only assertion here that would fail if either
	// helper ever collapsed.
	if hasAttribute(named, "aria-label") == hasAttribute(unnamed, "aria-label") {
		t.Error("the two landmarks are indistinguishable to hasAttribute, so " +
			"an audit could not tell a landmark named nothing from an unnamed one")
	}
}

// TestEveryAuditFailsOnTheThingItWatches is the discipline AGENTS.md requires of
// every gate test: each audit is run over a document built to violate exactly its
// rule, and each is required to produce at least one finding.
//
// Three of phase 5's first-draft gate tests could not fail, and the reasons were
// mechanical — one scanned forward from a selector when the mutation wraps the
// selector *outside*, one counted an absent attribute as an empty one, and one
// walked elements only and so never saw a comment. So the fixtures here are
// minimal and each carries its violation in a different carrier from the one the
// sibling fixture uses, so that an audit which happens to catch one is provably
// not the reason the other passes.
func TestEveryAuditFailsOnTheThingItWatches(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		body  string
		audit func(*audit)
	}{
		{
			name:  "an anchor with no target class",
			body:  `<html><body><a href="/somewhere">Go</a></body></html>`,
			audit: auditTargets,
		},
		{
			// A `<button>`, not an anchor: the element type is the variable, so an
			// audit that only walked `a[href]` would pass this one.
			name:  "a button with no target class",
			body:  `<html><body><button type="button">Go</button></body></html>`,
			audit: auditTargets,
		},
		{
			name:  "a positive tabindex",
			body:  `<html><body><main tabindex="3">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			name:  "a tabindex that is not a number",
			body:  `<html><body><main tabindex="first">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			// Spelled with a sign and padded, which is what a value interpolated
			// from a variable looks like. `strconv.Atoi` reads it as 3; a string
			// comparison against `"0"` does not, because `"+3"` sorts before `"0"`.
			// The row exists so that the numeric parse is load-bearing rather than a
			// stylistic preference — the failure it prevents is a browser disagreeing
			// with this gate about the tab order.
			name:  "a tabindex with a sign and padding",
			body:  `<html><body><main tabindex=" +3 ">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			name:  "the word in a text node",
			body:  `<html><body><p>the World of this account</p></body></html>`,
			audit: auditVocabulary,
		},
		{
			// The case a rendered-document reader skips: a reader never sees a
			// comment, which is precisely why it must be *excluded* rather than
			// scanned.
			name:  "the word in an HTML comment",
			body:  `<html><body><!-- the world of this account --></body></html>`,
			audit: auditVocabulary,
		},
		{
			name:  "the word in an aria-label",
			body:  `<html><body><nav aria-label="game sessions"></nav></body></html>`,
			audit: auditVocabulary,
		},
		{
			name:  "the word in an attribute name",
			body:  `<html><body><div data-world="true"></div></body></html>`,
			audit: auditVocabulary,
		},
		{
			name:  "two headings reading the same thing",
			body:  `<html><body><h2>Not working</h2><h2>not   working</h2></body></html>`,
			audit: auditHeadings,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &audit{where: "fixture", root: parseDocument(t, testCase.body)}
			testCase.audit(audit)

			if len(audit.findings) == 0 {
				t.Errorf("the audit reported nothing for %q; a gate that cannot "+
					"fail is a green light wired to nothing", testCase.name)
			}
		})
	}
}

// TestEveryAuditSaysNothingAboutWhatItMustNotTouch is the negative control for the
// table above, and it is a separate test rather than a flag on that table because
// the two are about opposite failure modes, and a row that said the wrong thing
// would then look like a row written wrong.
//
// Each fixture is the *nearest* document to a violation that is not one: a `<p>`
// rather than an `<a>`, an `<a>` with no `href`, a negative `tabindex`. An audit
// that walked every element for `.target`, or every anchor including the ones that
// are not links, or every `tabindex` for being non-zero, would report these — and
// would be enforcing a rule the record does not have. §10.6 is about *interactive*
// elements, and an anchor with no `href` is not focusable and so is not one.
func TestEveryAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
		// wantFindings is false for every row but one, and the field is here so
		// that the exception is visible in the table rather than encoded in a
		// predicate that matches on the row's name — which is the shape a
		// "fix the test" edit takes.
		wantFindings bool
		audit        func(*audit)
	}{
		{
			name:  "a paragraph is not interactive",
			body:  `<html><body><p>Prose.</p></body></html>`,
			audit: auditTargets,
		},
		{
			name:  "an anchor with no href is not a link",
			body:  `<html><body><a id="anchor">Not a link</a></body></html>`,
			audit: auditTargets,
		},
		{
			// The other direction: a class on a non-interactive element is not a
			// violation either, and §4.11.1 makes carrying `.target` a *plugin's*
			// obligation rather than something the shell refuses to share.
			name:  "a target class on a non-interactive element",
			body:  `<html><body><p class="target">Prose.</p></body></html>`,
			audit: auditTargets,
		},
		{
			// The value every skip-link target in the product carries. A rule that
			// rejected it would fail every document the shell renders.
			name:  "a negative tabindex is what §7.2 asks for",
			body:  `<html><body><main tabindex="-1">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			name:  "a zero tabindex is the document order",
			body:  `<html><body><main tabindex="0">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			// The padded counterpart of the row above: `Atoi(strings.TrimSpace(" -1"))`
			// is -1, so a value a template interpolated with spaces around it is
			// still the negative one §7.2 asks for and is not a finding.
			name:  "a padded negative tabindex",
			body:  `<html><body><main tabindex=" -1 ">Content</main></body></html>`,
			audit: auditTabindex,
		},
		{
			// UI §1.2's rule is a case-insensitive substring, so "Worldly" and
			// "worlds" are findings. The row is here to say that knowingly: a reader
			// who later adds a word boundary is changing the rule, not fixing an
			// audit.
			name:         "the word inside a longer word is still a finding",
			body:         `<html><body><p>Worldly, worlds, sessions.</p></body></html>`,
			wantFindings: true,
			audit:        auditVocabulary,
		},
		{
			name:  "two different headings are not a repeat",
			body:  `<html><body><h2>Instance</h2><h2>Not working</h2></body></html>`,
			audit: auditHeadings,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &audit{where: "fixture", root: parseDocument(t, testCase.body)}
			testCase.audit(audit)

			switch {
			case len(audit.findings) == 0 && testCase.wantFindings:
				t.Errorf("the audit reported nothing for %q, want a finding",
					testCase.name)
			case len(audit.findings) != 0 && !testCase.wantFindings:
				t.Errorf("the audit reported %d findings for %q, want none: %s",
					len(audit.findings), testCase.name,
					strings.Join(audit.findings, "; "))
			}
		})
	}
}

// TestEveryAuditPassesOnACorrectDocument is the other half, and it is the half
// that keeps the fixtures above honest.
//
// An audit that fails on everything is not a gate either: it is a gate that has
// been wired to the wrong end, and the way it gets discovered is a failing suite
// in the first week. So each audit is also required to report *nothing* on a
// document that satisfies its rule, and the document is one this route could
// actually serve.
func TestEveryAuditPassesOnACorrectDocument(t *testing.T) {
	t.Parallel()

	// The landmarks carry `.target` because they carry `tabindex="-1"`, which
	// puts them in §10.6's set — the three landmarks and the two skip links are
	// the only interactive elements the shell adds around a page, and the page
	// body itself contributes none, because sanitised prose cannot carry an
	// element the policy allows with an interactive role.
	correct := `<html lang="en"><body>
		<a class="skip-link target" href="#main">Skip to content</a>
		<nav class="shell-nav target" role="navigation" aria-label="Campaign" tabindex="-1">
			<a class="nav-link target" href="/c/greyhaven/wiki/Vault">Vault</a>
		</nav>
		<main id="main" class="shell-main target" role="main" tabindex="-1">
			<h1 id="page-heading">Vault</h1>
			<div class="page-body"><p>Iron and rust.</p></div>
		</main>
		<aside class="shell-rail target" role="complementary" aria-label="Utilities" tabindex="-1">
			<section><h2>Instance</h2></section>
		</aside>
	</body></html>`

	for _, testCase := range []struct {
		name  string
		audit func(*audit)
	}{
		{name: "targets", audit: auditTargets},
		{name: "tabindex", audit: auditTabindex},
		{name: "vocabulary", audit: auditVocabulary},
		{name: "headings", audit: auditHeadings},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &audit{where: "fixture", root: parseDocument(t, correct)}
			testCase.audit(audit)

			if len(audit.findings) != 0 {
				t.Errorf("the audit reported %d findings on a document that "+
					"satisfies the rule: %s", len(audit.findings),
					strings.Join(audit.findings, "; "))
			}
		})
	}
}
