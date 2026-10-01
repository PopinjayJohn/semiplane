package shell

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// RootAttribute is one of the two attributes the inline script owns.
//
// Named as a type rather than left as a string so a caller comparing an audit
// result cannot typo one into a silent pass, and so the set is closed: a third
// root attribute would be a decision about the design record, not a string that
// appears in a loop.
type RootAttribute string

// The two attributes. Both live on the document element, both are absent from
// the served document, and both are set by ResolverSource before the first
// paint.
const (
	// AttributeTheme is the colour scheme: `light` or `dark`.
	AttributeTheme RootAttribute = "data-theme"
	// AttributeUI is the resolved tier: `compact`, `compact-short`, `medium`,
	// `large`, `wide`, `tv` or `tv-wide`.
	AttributeUI RootAttribute = "data-ui"
)

// rootAttributes is every attribute the script owns, in the order the script
// sets them.
var rootAttributes = []RootAttribute{AttributeTheme, AttributeUI}

// Audit is what one served document says about the head contract.
//
// A value rather than a list of failures, because the consumer is a structural
// gate that wants to name the tier it is looking at and then assert several
// properties of one document without re-parsing it four times. Every field is an
// observation; Faults is the verdict, and an empty Faults is the pass
// condition.
type Audit struct {
	// HasColorSchemeMeta reports the colour-scheme meta element, which is
	// §3.7's answer for a visitor with no script.
	HasColorSchemeMeta bool
	// HasResolver reports the blocking script, matched on its exact text rather
	// than on the presence of a `<script>` element. A document carrying some
	// other script has not resolved its root attributes.
	HasResolver bool
	// ResolverInHead reports that the resolver is inside `<head>`. One in the
	// body has already missed the first paint, which is the one thing it exists
	// to avoid.
	ResolverInHead bool
	// ResolverBeforeStyles reports that the resolver precedes the stylesheet
	// link in document order.
	ResolverBeforeStyles bool
	// HasSheetScript reports the post-parse re-parent script, matched the same
	// way as the resolver.
	HasSheetScript bool
	// ResolverBytes is the served size of the blocking script, so a caller can
	// report it without reaching for the source and counting bytes itself.
	ResolverBytes int
	// ResolvedAttributes lists the root attributes found anywhere in the
	// document. Empty is the pass condition, and it is the whole reason this
	// package audits rather than renders: a document that shipped either
	// attribute would vary by a value the server cannot see, which is the one
	// thing S-13.5 forbids.
	ResolvedAttributes []RootAttribute
	// Faults is every contract violation found, in the order found.
	Faults []Fault
}

// Fault is one contract violation in a served document.
type Fault struct {
	// Attribute names the thing at fault, or is empty for a fault about the
	// head rather than about one attribute.
	Attribute RootAttribute
	// What says what went wrong, in enough words to fix it.
	What string
}

// String renders a fault for a test failure message.
func (fault Fault) String() string {
	if fault.Attribute == "" {
		return fault.What
	}

	return string(fault.Attribute) + ": " + fault.What
}

// walker carries the state of one traversal. A struct rather than a closure
// over five locals, because the traversal is recursive and the locals would
// otherwise be a parameter list.
type walker struct {
	audit Audit
	// order counts nodes in document order, which is what makes "the resolver
	// precedes the stylesheet" a comparison of two integers instead of a
	// guess about sibling positions.
	order int
	// resolverOrder and stylesheetOrder are the document-order index of each,
	// and -1 until one is found.
	resolverOrder   int
	stylesheetOrder int
}

// AuditDocument parses a served document and reports on the head contract.
//
// Parsed into a DOM, and never searched as a string, for a reason specific to
// this package: the resolver's own source *contains* the text `data-ui` and
// `data-theme`, so a substring test over the response either fails on a correct
// document or has to carve out the script — and a carve-out is exactly where a
// `data-ui` hidden inside an HTML comment goes unnoticed. Walking the tree finds
// an attribute wherever it is, and comments and ordinary text are walked too,
// because "the served document does not carry the attribute" has to mean no
// reader's browser can see it, and a comment is the one place a resolved tier
// can sit in a cached document while looking, to a string comparison, like
// nothing at all.
//
// The text inside `<script>` and `<style>` is excluded from that text scan for
// the reason above. An element's *attributes* are never excluded.
func AuditDocument(doc string) Audit {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return Audit{Faults: []Fault{{
			What: "the document does not parse as HTML: " + err.Error(),
		}}}
	}

	walk := &walker{
		resolverOrder:   -1,
		stylesheetOrder: -1,
	}
	walk.audit.ResolverBytes = len(resolverSource)

	walk.visit(root)
	walk.verdict()

	return walk.audit
}

// visit records everything one node says, then descends.
func (walk *walker) visit(node *html.Node) {
	current := walk.order
	walk.order++

	if node.Type == html.ElementNode {
		walk.element(node, current)
	} else {
		walk.text(node)
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk.visit(child)
	}
}

// element records an element's attributes and, for the four elements the head
// contract is about, its identity and its place in the document.
func (walk *walker) element(node *html.Node, order int) {
	for _, attribute := range node.Attr {
		for _, root := range rootAttributes {
			// EqualFold because an attribute name in HTML is case-insensitive:
			// `DATA-UI` is the attribute, and a document that spells it that way
			// still varies by it.
			if !strings.EqualFold(attribute.Key, string(root)) {
				continue
			}

			walk.fault(root, "is an attribute of <"+node.Data+"> in the served document; "+
				"it must be set by the inline script, not sent (UI §3.7, S-13.5)")
		}
	}

	switch {
	case node.Data == "meta" && hasAttr(node, "name", colorSchemeName):
		walk.audit.HasColorSchemeMeta = true
	case isStylesheetLink(node):
		walk.stylesheetOrder = order
	case node.Data == "script":
		walk.script(node, order)
	}
}

// script identifies a script by its exact text and records where it sits.
func (walk *walker) script(node *html.Node, order int) {
	body := textOf(node)

	switch strings.TrimSpace(body) {
	case resolverSource:
		walk.audit.HasResolver = true
		walk.audit.ResolverInHead = inHead(node)
		walk.resolverOrder = order
	case sheetScriptSource:
		walk.audit.HasSheetScript = true
	}
}

// text records a text-bearing node, which is where a resolved attribute can hide
// without being an attribute at all.
func (walk *walker) text(node *html.Node) {
	if node.Type != html.CommentNode && node.Type != html.TextNode {
		return
	}

	// Excluded: the resolver's own source names both attributes, and a document
	// carrying the resolver is a document carrying the strings. Not excluded:
	// anything else, so a comment cannot smuggle one past the audit.
	if parent := node.Parent; parent != nil && parent.Type == html.ElementNode {
		if parent.Data == "script" || parent.Data == "style" {
			return
		}
	}

	lowered := strings.ToLower(node.Data)

	for _, root := range rootAttributes {
		if !strings.Contains(lowered, string(root)) {
			continue
		}

		kind := "text"
		if node.Type == html.CommentNode {
			kind = "an HTML comment"
		}

		walk.fault(root, "is named in "+kind+" of the served document; a comment "+
			"carrying a resolved attribute is how a tier sneaks into a document "+
			"a cache will hand to the next reader (UI §3.7)")
	}
}

// verdict turns the observations into faults.
func (walk *walker) verdict() {
	walk.audit.ResolverBeforeStyles = walk.resolverOrder >= 0 &&
		walk.stylesheetOrder >= 0 &&
		walk.resolverOrder < walk.stylesheetOrder

	if !walk.audit.HasColorSchemeMeta {
		walk.note(
			"the head has no <meta name=\"" + colorSchemeName + "\">; a visitor " +
				"with no script then gets a page pinned against the operating " +
				"system's polarity (UI §3.7)",
		)
	}

	switch {
	case !walk.audit.HasResolver:
		walk.note("the head has no inline resolver; nothing sets " +
			"data-theme or data-ui before the first paint (UI §3.7)")
	case !walk.audit.ResolverInHead:
		walk.note("the resolver is not inside <head>; it has already missed the " +
			"first paint by the time it runs (UI §3.7)")
	case walk.stylesheetOrder < 0:
		walk.note("the head has no stylesheet link, so the ordering of the " +
			"resolver against the first paint cannot be established (UI §3.7)")
	case !walk.audit.ResolverBeforeStyles:
		walk.note("the stylesheet link precedes the resolver; the resolver has to " +
			"run first or the first paint is unthemed (UI §3.7)")
	}

	if !walk.audit.HasSheetScript {
		walk.note("the head has no sheet re-parent script; the below-640px rails " +
			"would have to be re-rendered on a tier change instead of moved " +
			"(UI §3.7)")
	}

	if len(resolverSource) > ResolverMaxBytes {
		walk.note("the resolver is " + strconv.Itoa(len(resolverSource)) +
			" bytes, over the " + strconv.Itoa(ResolverMaxBytes) +
			"-byte budget; it is parsed and executed before anything paints (UI §3.7)")
	}
}

// fault records a resolved attribute found where it must not be.
func (walk *walker) fault(attribute RootAttribute, what string) {
	walk.audit.ResolvedAttributes = append(walk.audit.ResolvedAttributes, attribute)
	walk.audit.Faults = append(walk.audit.Faults, Fault{Attribute: attribute, What: what})
}

// note records a fault about the head rather than about one attribute.
func (walk *walker) note(what string) {
	walk.audit.Faults = append(walk.audit.Faults, Fault{What: what})
}

// textOf returns the concatenated text children of a node.
func textOf(node *html.Node) string {
	var out strings.Builder

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			out.WriteString(child.Data)
		}
	}

	return out.String()
}

// inHead reports whether a node is inside `<head>`.
func inHead(node *html.Node) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == "head" {
			return true
		}
	}

	return false
}

// isStylesheetLink reports whether a node is a stylesheet `<link>`.
//
// The href is required to be present as well as the rel: a `<link>` with no
// href is not a stylesheet, and treating it as one would make the ordering
// check pass on a document that loads nothing.
func isStylesheetLink(node *html.Node) bool {
	return node.Data == "link" &&
		hasAttr(node, "rel", "stylesheet") &&
		hasAttrFold(node, "href")
}

// hasAttr reports whether an element carries a named attribute with a value.
//
// Name and value are both matched case-insensitively: HTML folds attribute
// names, and `rel="STYLESHEET"` is the same relationship.
func hasAttr(node *html.Node, name, value string) bool {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) && strings.EqualFold(attribute.Val, value) {
			return true
		}
	}

	return false
}

// hasAttrFold reports whether an element carries a named attribute at all.
func hasAttrFold(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return true
		}
	}

	return false
}

// Describe renders an audit's faults for a test failure message, one per line.
//
// A method rather than a helper so a caller cannot forget the empty case, and
// so the "no faults" string is written once.
func (audit Audit) Describe() string {
	if len(audit.Faults) == 0 {
		return "no faults"
	}

	out := make([]string, 0, len(audit.Faults))
	for _, fault := range audit.Faults {
		out = append(out, fault.String())
	}

	return fmt.Sprintf("%d fault(s):\n  - %s", len(audit.Faults), strings.Join(out, "\n  - "))
}
