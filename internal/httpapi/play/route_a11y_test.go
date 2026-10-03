package play_test

// The table document's §10.2 structural audit, its §10.6 target-size audit and
// UI §1.2's vocabulary rule — plus the three controls AGENTS.md requires of any
// audit added to this repository.
//
// # Why the rules return faults instead of reporting them
//
// `internal/httpapi/plugins` and `internal/httpapi/wiki` each hold an `auditFailer`
// interface so their rules can be tested by a recorder that counts rather than a
// `*testing.T` that fails. This file takes the same idea one step: **a rule is a
// function from a parsed document to a list of faults**, and every entry point
// above it — the gate test, the negative control, the neutral control — is a
// different question about that list.
//
// That is what makes the three controls cheap enough to be exhaustive:
//
//   - the faults must be **non-empty** for a document built to violate the rule;
//   - the faults must be **empty** for a document that violates nothing;
//   - the faults must be **empty** for every document this route really serves.
//
// A rule that reported "something" unconditionally passes the first and fails the
// second; a rule that reports nothing passes neither. All three run over the same
// function value, so a rule cannot be quietly swapped between the controls.
//
// # What is audited, and from where
//
// The real bytes: `GET /c/{slug}/play` served by a real mounted route, for a GM,
// for a player, on a private campaign, and for a campaign whose gameplay system
// this build cannot resolve. Auditing a fixture instead of the response would be
// auditing the fixture — the failure AGENTS.md records twice, where a rule passed
// on its own markup and failed on the product's.
//
// # The Makefile
//
// `A11Y_ROUTE_PKGS` does not name this package yet, so `make a11y` does not run
// these; `make test` does, because they are ordinary tests in an ordinary package.
// The top-level names below are written to match `A11Y_TESTS`' substrings —
// `Structural`, `Target`, `SkipLink`, `Vocabulary`, `EveryRoute` — so adding
// `./internal/httpapi/play` to that list is a Makefile edit and nothing else. The
// Makefile is the integrator's file, so that edit is reported rather than made.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// --- The documents under audit --------------------------------------------------

// renderedDocument is one response the rules run over.
type renderedDocument struct {
	// where names it in a failure message.
	where string
	// status is carried because an audit that only ever sees a 200 is an audit of
	// the easy path, and the message should say which document it read.
	status int
	// body is the response bytes, whole.
	body string
}

// servedDocuments is every shape of document this route can serve to a member.
//
// Four, and each is a different document rather than the same one fetched twice:
// the player's navigation omits Admin (§4.3's "absent, not disabled"), the
// private campaign resolves a different Campaigns section, and a campaign with no
// resolvable system renders §4.7's empty state in the die sheet instead of a
// notation. A rule about focus stops can pass on one and fail on another, which
// is the whole reason there is more than one.
func servedDocuments(t *testing.T) []renderedDocument {
	t.Helper()

	member := newHarness(t, &stubResolver{})

	gm := member.get(gmSlug, gmUserID)
	player := member.get(gmSlug, playerUserID)
	private := member.get(privateSlug, gmUserID)

	unresolved := newMounted(t, mount{
		handler: func(handler *play.Handler) {
			// No system and no live state: S-10.6's first row reached from the UI
			// side, and the token list's designed empty state at the same time.
			handler.Systems = nil
			handler.Snapshot = nil
		},
	})
	noSystem := unresolved.get(gmSlug, gmUserID)

	return []renderedDocument{
		{where: "the GM's document", status: gm.status, body: gm.body},
		{where: "a player's document", status: player.status, body: player.body},
		{
			where:  "the GM's document on a private campaign",
			status: private.status,
			body:   private.body,
		},
		{
			where:  "a document whose campaign has no resolvable system",
			status: noSystem.status,
			body:   noSystem.body,
		},
	}
}

// docAudit is one parsed document and where it came from.
type docAudit struct {
	where string
	root  *html.Node
}

// parseDocument parses a response, aborting when it is not an HTML document.
//
// Aborting rather than recording: §10.2's rules are about a document, and a
// response that is not one means the route answered something else entirely —
// which the URL-scheme test in `document_test.go` is the right place to notice,
// but which would otherwise make every rule below "pass" on a 404 body.
func parseDocument(t *testing.T, doc renderedDocument) *docAudit {
	t.Helper()

	if !strings.Contains(strings.ToLower(doc.body), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d); there is nothing "+
			"to audit. Begins: %q", doc.where, doc.status, truncate(doc.body))
	}

	root, err := html.Parse(strings.NewReader(doc.body))
	if err != nil {
		t.Fatalf("%s: parse the served document: %v", doc.where, err)
	}

	return &docAudit{where: doc.where, root: root}
}

// parseFixture parses a minimal document written to trip one rule.
func parseFixture(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}

	return root
}

// truncate shortens a body for a failure message.
func truncate(body string) string {
	const limit = 120

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// --- The DOM helpers the rules share -------------------------------------------

// elements visits every element in document order.
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
// §10.6's seven: `a[href]`, `button`, `input`, `select`, `textarea`, `summary`
// and anything carrying a `tabindex`. `<input type="hidden">` is excluded — a
// hidden field is not a focus stop, and demanding a target class on one would be
// a rule about an element no keyboard can reach.
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
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttribute(node, "href")
	case "button", "select", "textarea", "summary":
		return true
	case "input":
		return !strings.EqualFold(attr(node, "type"), "hidden")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// skipLinks returns the skip links, in document order.
func (a *docAudit) skipLinks() []*html.Node {
	links := make([]*html.Node, 0)

	a.elements(func(node *html.Node) {
		if node.Data == "a" && hasClass(node, ui.SkipLinkClass) {
			links = append(links, node)
		}
	})

	return links
}

// landmark is one region of the document as landmark navigation sees it.
type landmark struct {
	role string
	name string
	node *html.Node
}

// landmarks returns every landmark region, in document order.
//
// A region is an element whose *effective* role is a landmark one: the explicit
// `role` where there is one, the implicit role of the tag where there is not.
// An explicit role replaces the implicit one rather than adding to it, which is
// what makes `role="main"` on a `<div>` a main landmark and `role="article"` on a
// `<main>` not one.
func (a *docAudit) landmarks() []landmark {
	found := make([]landmark, 0)

	a.elements(func(node *html.Node) {
		role := landmarkRole(node)
		if !isLandmarkRole(role) {
			return
		}

		found = append(found, landmark{
			role: role,
			name: accessibleName(node, a.root),
			node: node,
		})
	})

	return found
}

// landmarkRole is an element's effective role, landmark or not.
func landmarkRole(node *html.Node) string {
	if role := attr(node, "role"); role != "" {
		return role
	}

	switch node.Data {
	case "header":
		return "banner"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "aside":
		return "complementary"
	case "footer":
		return "contentinfo"
	default:
		return ""
	}
}

// landmarkRoles are the roles landmark navigation can jump to, and the ones
// UI §7.2's list is built from.
var landmarkRoles = []string{
	"banner", "navigation", "main", "complementary", "contentinfo", "region", "search",
}

// isLandmarkRole reports whether a role is a landmark.
func isLandmarkRole(role string) bool {
	return slices.Contains(landmarkRoles, role)
}

// accessibleName resolves an element's name the way assistive technology does,
// in the order that matters here: `aria-labelledby`, then `aria-label`.
//
// Name computation is deliberately partial — no `alt`, no `title`, no wrapping
// text — because every landmark this document carries is named by one of those
// two attributes, and a fuller implementation would be a second answer to a
// question the browser already answers. What the partial version must not do is
// report an unnamed landmark as named, which is why a reference that resolves to
// nothing falls through to `aria-label` and then to empty.
func accessibleName(node *html.Node, root *html.Node) string {
	if ids := attr(node, "aria-labelledby"); ids != "" {
		if name := referencedText(root, ids); name != "" {
			return name
		}
	}

	return strings.TrimSpace(attr(node, "aria-label"))
}

// referencedText is the text of the elements some `aria-labelledby` names.
func referencedText(root *html.Node, ids string) string {
	parts := make([]string, 0, len(strings.Fields(ids)))

	for _, id := range strings.Fields(ids) {
		target := byID(root, id)
		if target == nil {
			continue
		}

		if text := elementText(target); text != "" {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, " ")
}

// elementText is an element's text content, recursively.
func elementText(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)

			return
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return strings.TrimSpace(out.String())
}

// byID finds an element by id.
func byID(root *html.Node, id string) *html.Node {
	var found *html.Node

	(&docAudit{root: root}).elements(func(node *html.Node) {
		if found == nil && attr(node, "id") == id {
			found = node
		}
	})

	return found
}

// byTestID finds an element by its `data-testid` hook.
//
// `KeysTestID` and its fellows are **hooks, not ids**: unique in the document,
// but carried in `data-testid`. Four fixtures below originally looked for one as
// an id, found nothing, and reported "changed nothing" — a neutral control that
// silently tests nothing, which is the exact failure this file's controls exist
// to make loud. One helper, so the next fixture cannot pick the wrong lookup by
// habit.
func byTestID(root *html.Node, testID string) *html.Node {
	var found *html.Node

	(&docAudit{root: root}).elements(func(node *html.Node) {
		if found == nil && attr(node, "data-testid") == testID {
			found = node
		}
	})

	return found
}

// find returns the first element matching a predicate, or nil.
func (a *docAudit) find(match func(*html.Node) bool) *html.Node {
	var found *html.Node

	a.elements(func(node *html.Node) {
		if found == nil && match(node) {
			found = node
		}
	})

	return found
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

// hasAttribute reports whether an element carries an attribute at all — present
// and empty counts, because `tabindex=""` is present and unusable.
func hasAttribute(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// hasClass reports whether an element carries a class token.
func hasClass(node *html.Node, token string) bool {
	return slices.Contains(strings.Fields(attr(node, "class")), token)
}

// setAttr adds or replaces an attribute, for the control fixtures.
func setAttr(node *html.Node, key, value string) {
	for index := range node.Attr {
		if node.Attr[index].Key == key {
			node.Attr[index].Val = value

			return
		}
	}

	node.Attr = append(node.Attr, html.Attribute{Key: key, Val: value})
}

// removeAttr drops an attribute, for the control fixtures.
func removeAttr(node *html.Node, key string) {
	node.Attr = slices.DeleteFunc(node.Attr, func(attribute html.Attribute) bool {
		return attribute.Key == key
	})
}

// element builds an element with one text child, for the control fixtures.
//
// Built rather than parsed, because `html.ParseFragment` needs a context node and
// every fixture here inserts into a document it already has: a parser inside a
// mutation helper is a second place a fixture can silently fail to be what it
// claims, and `nil` returned nowhere means a mutation that applied nothing is
// caught by the fixture finding no target.
func element(tag, id, text string) *html.Node {
	node := &html.Node{Type: html.ElementNode, Data: tag}
	if id != "" {
		node.Attr = append(node.Attr, html.Attribute{Key: "id", Val: id})
	}

	if text != "" {
		node.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	}

	return node
}

// nodePath names an element for a failure message.
func nodePath(node *html.Node) string {
	parts := make([]string, 0, 4)

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		name := current.Data

		if id := attr(current, "id"); id != "" {
			name += "#" + id
		} else if class := strings.Fields(attr(current, "class")); len(class) > 0 {
			name += "." + class[0]
		}

		parts = append(parts, name)
	}

	slices.Reverse(parts)

	return strings.Join(parts, "/")
}

// --- The rules ------------------------------------------------------------------

// routeRule is one claim about a document and the function that holds it.
type routeRule struct {
	// name is the subtest's name, and it reads as the claim.
	name string
	// audit returns every fault it can find, empty when the document satisfies it.
	audit func(*docAudit) []string
}

// structuralRules is UI §10.2's list, in the order the record states it.
//
// Nine, and no more than §10.2 asks for: the four it spells out (landmarks,
// heading order, exactly one `h1`, no positive tabindex) plus the five it implies
// and this repository's other audits already hold (skip links, `aria-hidden` on a
// focus stop, reference integrity, unique ids, no `role="application"`). Each is
// a separate subtest so a failure names the rule rather than "the a11y test".
func structuralRules() []routeRule {
	return []routeRule{
		{name: "exactly one h1", audit: exactlyOneH1},
		{name: "heading levels never skip", audit: headingLevelsNeverSkip},
		{name: "landmarks are present and distinguishing", audit: landmarksArePresentAndDistinguishing},
		{name: "skip links come first and resolve", audit: skipLinksComeFirstAndResolve},
		{name: "no positive tabindex", audit: noPositiveTabindex},
		{name: "no aria-hidden on a focus stop", audit: noAriaHiddenOnAFocusStop},
		{name: "every ARIA reference resolves", audit: everyReferenceResolves},
		{name: "identifiers are unique", audit: identifiersAreUnique},
		{name: "no role application", audit: noRoleApplication},
	}
}

// exactlyOneH1 is §10.2's "exactly one `<h1>` per page" and §4.4's "one `<h1>`,
// `/play` included".
func exactlyOneH1(a *docAudit) []string {
	count := 0

	a.elements(func(node *html.Node) {
		if node.Data == "h1" {
			count++
		}
	})

	if count != 1 {
		return []string{fmt.Sprintf(
			"%s: the document has %d <h1> elements, want exactly 1; two of them give a "+
				"screen reader two page titles and no way to choose",
			a.where, count,
		)}
	}

	return nil
}

// headingLevelsNeverSkip is §10.2's "heading levels never skip" over the
// document's real outline.
//
// The first heading must be an `h1` as well as there being exactly one, because
// a document whose outline starts at `h2` skips a level without any consecutive
// pair doing so — and that is the shape a template reaches for when the `<h1>`
// lives in a slot the route forgot to fill.
func headingLevelsNeverSkip(a *docAudit) []string {
	faults := make([]string, 0, 2)

	previous := 0

	a.elements(func(node *html.Node) {
		level, isHeading := headingLevel(node)
		if !isHeading {
			return
		}

		switch {
		case previous == 0 && level != 1:
			faults = append(faults, fmt.Sprintf(
				"%s: the outline starts at h%d at %s; the first heading is an h1 or the "+
					"levels skip (UI §10.2)", a.where, level, nodePath(node),
			))
		case previous > 0 && level > previous+1:
			faults = append(faults, fmt.Sprintf(
				"%s: %s jumps from h%d to h%d; heading levels never skip (UI §10.2)",
				a.where, nodePath(node), previous, level,
			))
		}

		previous = level
	})

	return faults
}

// headingLevel is an element's heading level, and whether it is a heading.
func headingLevel(node *html.Node) (int, bool) {
	if len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0, false
	}

	level, err := strconv.Atoi(node.Data[1:])
	if err != nil || level < 1 || level > 6 {
		return 0, false
	}

	return level, true
}

// landmarksArePresentAndDistinguishing is §10.2's landmark rule and §7.2's
// "distinguishing labels" half.
//
// Four claims, separately asserted because any one can hold while the others
// fail: the landmarks §4.1–§4.5 put on a campaign document are all present; no
// two share a role *and* a name; each named one carries one of the labels the
// record fixes; and no landmark is nested where the record does not place one.
//
// The label sets are sets rather than literals for the reason `plugins`' audit
// gives: a rule that named only "Campaign" would fail this document's second
// navigation the moment the action bar landed, and a rule that named only
// "Primary" would demand a compact bar this document deliberately does not have
// (`chrome.FooterView.Play` replaces it, UI §4.2).
func landmarksArePresentAndDistinguishing(a *docAudit) []string {
	faults := make([]string, 0, 4)

	found := a.landmarks()

	present := map[string]int{}

	for _, region := range found {
		present[region.role]++
	}

	// `main` unconditionally; the other three are this route's fixed chrome.
	if present["main"] != 1 {
		faults = append(faults, fmt.Sprintf(
			"%s: the document has %d main landmarks, want 1 (UI §10.2, §4.4)",
			a.where, present["main"],
		))
	}

	if present["banner"] != 1 {
		faults = append(faults, fmt.Sprintf(
			"%s: the document has %d banner landmarks, want 1 (UI §4.1)",
			a.where, present["banner"],
		))
	}

	if present["contentinfo"] != 1 {
		faults = append(faults, fmt.Sprintf(
			"%s: the document has %d contentinfo landmarks, want 1 (UI §4.2)",
			a.where, present["contentinfo"],
		))
	}

	if present["complementary"] != 1 {
		faults = append(faults, fmt.Sprintf(
			"%s: the document has %d complementary landmarks, want 1 (UI §4.5)",
			a.where, present["complementary"],
		))
	}

	if present["navigation"] < 1 {
		faults = append(faults, fmt.Sprintf(
			"%s: the document has no navigation landmark (UI §4.3)", a.where,
		))
	}

	// Distinctness, keyed on role *and* name: two landmarks of different roles
	// with one name are distinguishable by role, and landmark navigation says
	// both out loud.
	seen := map[string]string{}

	for _, region := range found {
		if region.name == "" {
			if needsName(region.role) {
				faults = append(faults, fmt.Sprintf(
					"%s: the %s landmark at %s has no accessible name; §7.2 requires "+
						"distinguishing labels or landmark navigation is useless",
					a.where, region.role, nodePath(region.node),
				))
			}

			continue
		}

		key := region.role + "=" + region.name

		if first, duplicate := seen[key]; duplicate {
			faults = append(faults, fmt.Sprintf(
				"%s: two %s landmarks are both labelled %q (%s and %s)",
				a.where, region.role, region.name, first, nodePath(region.node),
			))
		}

		seen[key] = nodePath(region.node)
	}

	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigationLabels[region.name] {
				faults = append(faults, fmt.Sprintf(
					"%s: a navigation landmark is labelled %q; UI §7.2 and §4.9 name them "+
						"%q (the campaign navigation) and %q (the table's action bar)",
					a.where, region.name, "Campaign", "Table actions",
				))
			}
		case "complementary":
			if region.name != "Utilities" {
				faults = append(faults, fmt.Sprintf(
					"%s: the complementary landmark is labelled %q, want %q (UI §4.5)",
					a.where, region.name, "Utilities",
				))
			}
		case "region":
			// Named by the rule above; nothing further to check.
		case "banner", "contentinfo", "main":
		default:
			faults = append(faults, fmt.Sprintf(
				"%s: %s is a %s landmark, which is not one UI §7.2's document places; a "+
					"landmark the record does not describe is a region a reader can jump to "+
					"and nobody knows what it is for",
				a.where, nodePath(region.node), region.role,
			))
		}

		if region.role == "contentinfo" && hasAncestor(region.node, "main") {
			faults = append(faults, fmt.Sprintf(
				"%s: a contentinfo landmark is inside <main> at %s; it is the page's "+
					"footer, not the article's (UI §7.2)",
				a.where, nodePath(region.node),
			))
		}
	}

	return faults
}

// allowedNavigationLabels are the two navigation names this document can carry.
var allowedNavigationLabels = map[string]bool{"Campaign": true, "Table actions": true}

// needsName reports whether a landmark role must carry an accessible name.
//
// `main`, `banner` and `contentinfo` need not: a document has exactly one of
// each, so landmark navigation distinguishes them by role alone, and the record
// gives them no label to carry.
func needsName(role string) bool {
	switch role {
	case "navigation", "complementary", "region", "search":
		return true
	default:
		return false
	}
}

// hasAncestor reports whether an element sits under another.
func hasAncestor(node *html.Node, tag string) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Data == tag {
			return true
		}
	}

	return false
}

// skipLinksComeFirstAndResolve is §7.2 in both directions.
//
// The four links this route names — content, campaign navigation, utilities, the
// token list — must be the first four focus stops in that order, each must be a
// fragment that resolves to a focusable landmark, and the pairing runs **both
// ways**: a landmark with no link is as much a failure as a link with no
// landmark, because only one of them shows up in a rendering.
//
// The two halves are one function on purpose. They are one condition in two
// places — AGENTS.md's own words — and a condition split across two rules is a
// condition that can be half-satisfied while both rules are green.
func skipLinksComeFirstAndResolve(a *docAudit) []string {
	faults := make([]string, 0, 4)

	links := a.skipLinks()
	if len(links) == 0 {
		return []string{fmt.Sprintf(
			"%s: the document has no skip link; §7.2 makes them the first focusable "+
				"elements and this document always has at least one", a.where,
		)}
	}

	stops := a.focusStops()

	for index, link := range links {
		if index >= len(stops) || stops[index] != link {
			faults = append(faults, fmt.Sprintf(
				"%s: skip link %d (%q) is not focus stop %d; §7.2 puts the skip links "+
					"first in tab order", a.where, index+1, elementText(link), index+1,
			))
		}

		fragment, isFragment := strings.CutPrefix(attr(link, "href"), "#")
		if !isFragment || fragment == "" {
			faults = append(faults, fmt.Sprintf(
				"%s: skip link %q has href %q; §7.2's links are fragments",
				a.where, elementText(link), attr(link, "href"),
			))

			continue
		}

		target := byID(a.root, fragment)
		if target == nil {
			faults = append(faults, fmt.Sprintf(
				"%s: skip link %q points at #%s, which is not in the document; a link "+
					"that moves focus nowhere is worse than no link (§7.2)",
				a.where, elementText(link), fragment,
			))

			continue
		}

		if !hasAttribute(target, "tabindex") {
			faults = append(faults, fmt.Sprintf(
				"%s: skip link %q lands on %s, which has no tabindex; focus would land "+
					"at the top of a scroll container instead of on an announced element",
				a.where, elementText(link), nodePath(target),
			))
		}

		if !isLandmarkRole(landmarkRole(target)) {
			faults = append(faults, fmt.Sprintf(
				"%s: skip link %q lands on %s, which is not a landmark; a skip link's "+
					"target is the region it names (UI §7.2)",
				a.where, elementText(link), nodePath(target),
			))
		}
	}

	// The order §7.2 fixes, and /play's own fourth link comes last.
	positions := map[string]int{}

	for index, link := range links {
		positions[attr(link, "data-testid")] = index
	}

	if at := positions["skip-to-content"]; at != 0 {
		faults = append(faults, fmt.Sprintf(
			"%s: the first skip link is %q; §7.2's order is content first",
			a.where, firstLinkText(links),
		))
	}

	if content, nav := positions["skip-to-content"], positions["skip-to-nav"]; nav >= 0 &&
		content >= 0 && nav < content {
		faults = append(faults, fmt.Sprintf(
			"%s: the campaign-navigation skip link precedes the content link; §7.2's "+
				"order is content first", a.where,
		))
	}

	// Both directions, for each landmark this route names.
	for _, target := range []struct {
		id       string
		testID   string
		whatName string
	}{
		{id: "main", testID: "skip-to-content", whatName: "content"},
		{id: "nav", testID: "skip-to-nav", whatName: "campaign navigation"},
		{id: "rail", testID: "skip-to-utilities", whatName: "utilities"},
		{
			id:       webplayPanelRegionID,
			testID:   "skip-to-token-list",
			whatName: "token list",
		},
	} {
		element := byID(a.root, target.id)
		linkAt := positions[target.testID]

		if element != nil && linkAt < 0 {
			faults = append(faults, fmt.Sprintf(
				"%s: the document has a %s landmark (#%s) but no skip link to it; §7.2 "+
					"pairs each one with the other",
				a.where, target.whatName, target.id,
			))
		}

		if element == nil && linkAt >= 0 {
			faults = append(faults, fmt.Sprintf(
				"%s: a skip link points at #%s, which is not in the document; §4.6 and "+
					"§7.2 keep the landmark and its link together",
				a.where, target.id,
			))
		}
	}

	return faults
}

// webplayPanelRegionID is `components/play.PanelRegionID`, the token list's
// element id and the target of this route's fourth skip link.
//
// Spelled rather than imported because the constant lives in a package whose
// *test* files also declare helpers of the same name, and a route audit that
// imported it would be a route audit coupled to a component's internals. The
// value is load-bearing in three places (the section's `id`, the skip link's
// `href`, this rule) and `TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition`
// compares them by rendering rather than by constant.
const webplayPanelRegionID = "play-token-list"

// firstLinkText names the first skip link for a failure message.
func firstLinkText(links []*html.Node) string {
	if len(links) == 0 {
		return "(none)"
	}

	return elementText(links[0])
}

// noPositiveTabindex is §7.4's "No positive `tabindex`, anywhere — gate failure"
// and §10.2's "no `tabindex` > 0", over the served document.
//
// # Three readings of one rule, and this file's
//
// `wiki` and `plugins` each hold a copy of this rule and both read it as **only
// `-1` is permitted**, zero included, arguing that `tabindex="0"` moves an element
// to the front of the focus order. Neither route has ever served a tablist, so
// neither has ever met the case where that reading bites: `ui.Tabs` — the shared
// tabs primitive, and the reason §7.4's Arrows row names tabs as a composite
// widget at all — puts `tabindex="0"` on the selected tab and `-1` on the rest,
// because the widget's defining property is that Tab passes through it as **one**
// stop while the arrow keys move within it. With no `0` anywhere the tablist is
// unreachable by keyboard, and with `0` on every tab it is four stops.
//
// So this rule is the siblings' `-1`-only standard **plus one exception**, scoped
// to an element inside `[role="tablist"]` and only for the value `0`:
//
//   - a positive `tabindex` is a fault everywhere, tablist included — §7.4 says
//     "anywhere" and the roving pattern never needs a positive value;
//   - `0` outside a tablist is a fault, exactly as `wiki` and `plugins` hold;
//   - `0` inside a tablist is the roving item, and `TestTheRail...` in this
//     package's component tests holds the widget's half of the same claim.
//
// The stated rationale of the stricter reading does not reach the exception in
// any case: HTML's sequential focus order sorts positive values first and then
// walks everything else — `tabindex="0"` included — in document order, so a
// `tabindex="0"` **button** sits exactly where it would sit without the
// attribute. What the exception protects is the widget, not the ordering.
//
// The divergence from the other two audits is reported rather than reconciled
// here: neither of their files is this work item's to edit. Until they serve a
// tablist the disagreement has no observable consequence, and when one does, the
// question to settle is which reading the record states — and §10.2 states
// "no `tabindex` > 0".
func noPositiveTabindex(a *docAudit) []string {
	faults := make([]string, 0, 2)

	a.elements(func(node *html.Node) {
		if !hasAttribute(node, "tabindex") {
			return
		}

		raw := attr(node, "tabindex")

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			faults = append(faults, fmt.Sprintf(
				"%s: %s carries tabindex=%q, which is not a number; browsers disagree "+
					"about an unparseable tabindex and the tab order becomes a guess",
				a.where, nodePath(node), raw,
			))

			return
		}

		if value > 0 {
			faults = append(faults, fmt.Sprintf(
				"%s: %s carries tabindex=%d; §7.4 forbids a positive tabindex anywhere "+
					"and §10.2 says no tabindex > 0 — Tab follows document order",
				a.where, nodePath(node), value,
			))

			return
		}

		if value == 0 && a.insideTablist(node) {
			return
		}

		if value >= 0 {
			faults = append(faults, fmt.Sprintf(
				"%s: %s carries tabindex=%d; §10.2 and this repository's route audits "+
					"permit only -1 outside a composite widget's roving item (§7.4)",
				a.where, nodePath(node), value,
			))
		}
	})

	return faults
}

// insideTablist reports whether an element sits inside `[role="tablist"]`.
//
// Ancestor walk rather than a parent check, because `ui.Tabs` renders the tablist
// as a `<div>` with the buttons as children today and a wrapper between them
// tomorrow would silently turn the exception off — and an exception that turns
// itself off fails the gate test, not the widget.
func (a *docAudit) insideTablist(node *html.Node) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if strings.EqualFold(attr(current, "role"), "tablist") {
			return true
		}
	}

	return false
}

// noAriaHiddenOnAFocusStop is §10.2's "no `aria-hidden` on a focus stop", in
// both directions: the element itself and anything containing it.
//
// The ancestor half is the one that matters in practice. A hidden container with
// a button inside it is a focus stop a keyboard can reach and a screen reader
// cannot announce — the reader tabs into something their technology says does not
// exist, which is the worst of both.
func noAriaHiddenOnAFocusStop(a *docAudit) []string {
	faults := make([]string, 0, 2)

	for _, stop := range a.focusStops() {
		if hidden := hiddenBy(stop); hidden != "" {
			faults = append(faults, fmt.Sprintf(
				"%s: %s is a focus stop inside %s; §10.2 forbids aria-hidden on anything "+
					"a keyboard can reach", a.where, nodePath(stop), hidden,
			))
		}
	}

	return faults
}

// hiddenBy is the ancestor carrying `aria-hidden="true"`, or "".
func hiddenBy(node *html.Node) string {
	for current := node; current != nil; current = current.Parent {
		if strings.EqualFold(attr(current, "aria-hidden"), "true") {
			return nodePath(current)
		}
	}

	return ""
}

// everyReferenceResolves is §10.2's reference-integrity rule: every id an
// attribute points at exists.
//
// Six attributes, because ARIA has six of them and a template that renames an id
// without renaming the reference is the failure: the reference then resolves to
// nothing, and an `aria-controls` that names no element is a disclosure claiming
// to control something that is not there.
func everyReferenceResolves(a *docAudit) []string {
	faults := make([]string, 0, 2)

	const references = "aria-labelledby aria-controls aria-describedby aria-owns " +
		"aria-activedescendant for form"

	a.elements(func(node *html.Node) {
		for _, attribute := range strings.Fields(references) {
			for _, id := range strings.Fields(attr(node, attribute)) {
				if byID(a.root, id) == nil {
					faults = append(faults, fmt.Sprintf(
						"%s: %s has %s=%q, which resolves to nothing (UI §10.2)",
						a.where, nodePath(node), attribute, id,
					))
				}
			}
		}
	})

	return faults
}

// identifiersAreUnique is §10.2's "ids are unique", and the other half of
// reference integrity: a duplicated id means `aria-controls` has two answers and
// `getElementById` picks whichever the parser saw first.
func identifiersAreUnique(a *docAudit) []string {
	faults := make([]string, 0, 2)

	seen := map[string]string{}

	a.elements(func(node *html.Node) {
		id := attr(node, "id")
		if id == "" {
			return
		}

		if first, duplicate := seen[id]; duplicate {
			faults = append(faults, fmt.Sprintf(
				"%s: id=%q is on both %s and %s; an id with two elements gives every "+
					"reference two answers", a.where, id, first, nodePath(node),
			))
		}

		seen[id] = nodePath(node)
	})

	return faults
}

// noRoleApplication is §10.2's prohibition, and it is one line of markup with a
// whole interaction model behind it: `role="application"` tells assistive
// technology to stop interpreting the region and pass keys through, so the
// document's landmarks, headings and live regions all stop working inside it.
func noRoleApplication(a *docAudit) []string {
	faults := make([]string, 0, 1)

	a.elements(func(node *html.Node) {
		if strings.EqualFold(attr(node, "role"), "application") {
			faults = append(faults, fmt.Sprintf(
				"%s: %s has role=application; §10.2 forbids it — it switches assistive "+
					"technology off for everything inside it", a.where, nodePath(node),
			))
		}
	})

	return faults
}

// targetClassOnEveryFocusStop is §10.6's audit: every element a keyboard can
// reach carries `.target`, which is what gives it one of the two minimum sizes
// `--target-min` declares.
//
// The class is enforced by construction for anything `ui` renders — the
// primitive writes it — so what this rule catches is the element a *route*
// writes by hand. This route writes two: the four skip links (through `ui`) and
// the `<h1>`'s neighbourhood through `chrome`, so the interesting failure is a
// future `<button>` added straight to `document.templ`.
func targetClassOnEveryFocusStop(a *docAudit) []string {
	faults := make([]string, 0, 2)

	for _, stop := range a.focusStops() {
		if !hasClass(stop, ui.TargetClass) {
			faults = append(faults, fmt.Sprintf(
				"%s: %s is a focus stop without %q; §10.6 requires the class on every "+
					"one, and the stylesheet decides what it means",
				a.where, nodePath(stop), ui.TargetClass,
			))
		}
	}

	return faults
}

// retiredVocabulary is UI §1.2's two words, and the reason they are checked as
// substrings of *every* text run rather than of the visible copy: "your session
// has expired" is a sentence a UI writes in good faith, and the word in an
// `aria-label` or an HTML comment is as much a part of the interface as the word
// in a paragraph — a screen reader reads the first, and a reviewer reads the
// second.
var retiredVocabulary = []string{"world", "session"}

// vocabularyFaults returns every use of a retired word, wherever it appears.
//
// Text nodes, attribute values **and comment nodes**, in that order of
// thoroughness: the DOM's text nodes are what a reader hears, the attributes are
// what a reader hears in a name, and the comments are what a reviewer reads —
// AGENTS.md requires all three, and `TestTheVocabularyAuditFindsTheWordWhereverItIs`
// feeds it six ways of smuggling one in.
func vocabularyFaults(a *docAudit) []string {
	faults := make([]string, 0, 2)

	mention := func(where, word string) {
		faults = append(faults, fmt.Sprintf(
			"%s: %q appears at %s; UI §1.2 retires it from the interface entirely",
			a.where, word, where,
		))
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			for _, word := range retiredVocabulary {
				if strings.Contains(strings.ToLower(node.Data), word) {
					mention("a text node", word)
				}
			}
		}

		if node.Type == html.CommentNode {
			for _, word := range retiredVocabulary {
				if strings.Contains(strings.ToLower(node.Data), word) {
					mention("an HTML comment", word)
				}
			}
		}

		if node.Type == html.ElementNode {
			for _, attribute := range node.Attr {
				for _, word := range retiredVocabulary {
					if strings.Contains(strings.ToLower(attribute.Val), word) {
						mention(fmt.Sprintf("%s=%q on %s", attribute.Key, attribute.Val,
							nodePath(node)), word)
					}
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(a.root)

	return faults
}

// --- The gate tests -------------------------------------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, one test with a
// subtest per rule so a failure names the rule rather than "the a11y test failed".
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, doc := range servedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			audit := parseDocument(t, doc)

			for _, rule := range structuralRules() {
				t.Run(rule.name, func(t *testing.T) {
					t.Parallel()

					for _, fault := range rule.audit(audit) {
						t.Error(fault)
					}
				})
			}
		})
	}
}

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6, over the
// document the route actually serves.
//
// The stylesheet is where the class becomes a size, and `make a11y` reads the
// built file for that half; this is the markup half, and neither answers for the
// other — a class with no rule is a class that does nothing, and a rule with no
// class is a rule that finds nothing.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, doc := range servedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			for _, fault := range targetClassOnEveryFocusStop(parseDocument(t, doc)) {
				t.Error(fault)
			}
		})
	}
}

// TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition is AGENTS.md's pairing
// claim in both directions, over this route's four skip links.
//
// It runs the same function the structural contract runs, and that is the point:
// a second implementation would be a second answer to "does this document pair
// its skip links with their landmarks", and the two would drift the first time
// one was fixed. The top-level test exists because `A11Y_TESTS` matches top-level
// names only — `go test -list` never sees a subtest.
func TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition(t *testing.T) {
	t.Parallel()

	for _, doc := range servedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			for _, fault := range skipLinksComeFirstAndResolve(parseDocument(t, doc)) {
				t.Error(fault)
			}
		})
	}
}

// TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe is the positive
// control: every rule, over every document this route serves, must be silent.
//
// Without it a rule could be green on its own fixture and red on the product —
// the exact failure AGENTS.md records, where an audit "passed" while the thing
// it was written for failed it. Every rule in the list is included by iterating
// `structuralRules()` rather than by naming them, so a rule added to the list
// above cannot be left out here.
func TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	type namedRule struct {
		name  string
		audit func(*docAudit) []string
	}

	checks := make([]namedRule, 0, len(structuralRules())+2)
	for _, rule := range structuralRules() {
		checks = append(checks, namedRule{name: rule.name, audit: rule.audit})
	}

	checks = append(checks,
		namedRule{name: "target class", audit: targetClassOnEveryFocusStop},
		namedRule{name: "vocabulary", audit: vocabularyFaults},
	)

	for _, doc := range servedDocuments(t) {
		for _, check := range checks {
			t.Run(doc.where+" — "+check.name, func(t *testing.T) {
				t.Parallel()

				faults := check.audit(parseDocument(t, doc))
				if len(faults) != 0 {
					t.Errorf("the %q audit reported %d finding(s) about a document this "+
						"route really serves: %v. An audit whose product it rejects is worse "+
						"than no audit: it is green on its fixtures and red on the thing it "+
						"was written for", check.name, len(faults), faults)
				}
			})
		}
	}
}

// --- The controls ----------------------------------------------------------------

// violation is one rule and a mutation that breaks it.
type violation struct {
	// name is the subtest's name and `why` is what the audit is expected to catch.
	name string
	why  string
	// rule is the rule under test, matched against `structuralRules()` by name so
	// a fixture cannot quietly test a rule that is not in the gate's list.
	rule string
	// mutate applies the violation to a parsed copy of a served document.
	mutate func(*html.Node) bool
	// mentions is a substring at least one fault must carry: an audit that fired
	// about something else has not been shown to catch this.
	mentions string
}

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control.
//
// Each rule is run over the route's **own document** with exactly one thing
// broken in it, rather than over a minimal fixture — a minimal fixture that
// violated two rules at once cannot tell you which one the audit caught, and a
// fixture written by hand tends to agree with the rule it was written for. Every
// mutation here is applied to real served bytes, so the only difference between
// this document and a passing one is the single change under test.
//
// The completeness assertion at the end is the part that matters for the future:
// a rule added to `structuralRules()` with no fixture here is a rule nobody has
// shown can fail, and this loop is where that becomes a red test rather than a
// silent gap.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	served := servedDocuments(t)
	baseline := parseDocument(t, served[0])

	for _, testCase := range violations() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &docAudit{where: testCase.name, root: cloneDocument(t, baseline.root)}

			if !testCase.mutate(audit.root) {
				t.Fatalf("the fixture for %q changed nothing: the mutation did not apply, "+
					"so the rule was never given the violation it claims to catch", testCase.name)
			}

			// `auditFor`, not `ruleNamed`: the table below also holds the two rules
			// gated by their own top-level test ("target class", "vocabulary"), and a
			// lookup that only knew the structural list would fail the fixture rather
			// than run it.
			target := auditFor(t, testCase.rule)

			faults := target.audit(audit)
			if len(faults) == 0 {
				t.Errorf("the %q audit reported nothing for a document that violates it. "+
					"%s. An audit that reports nothing is the same failure as no audit, with "+
					"a green light on top", testCase.rule, testCase.why)
			}

			mentioned := false

			for _, fault := range faults {
				if strings.Contains(strings.ToLower(fault), strings.ToLower(testCase.mentions)) {
					mentioned = true
				}
			}

			if !mentioned {
				t.Errorf("the %q audit fired %d time(s) but none of its messages mentions "+
					"%q, which is the thing this rule is about: %v",
					testCase.rule, len(faults), testCase.mentions, faults)
			}
		})
	}

	t.Run("every rule has a fixture that trips it", func(t *testing.T) {
		t.Parallel()

		covered := make(map[string]bool, len(structuralRules())+1)

		for _, testCase := range violations() {
			covered[testCase.rule] = true
		}

		for _, rule := range structuralRules() {
			if !covered[rule.name] {
				t.Errorf("the rule %q has no fixture in violations(); a rule nobody can "+
					"show failing is a rule nobody can trust passing — write the fixture or "+
					"remove the rule", rule.name)
			}
		}
	})
}

// ruleNamed looks a rule up by the name it is gated under.
func ruleNamed(t *testing.T, name string) routeRule {
	t.Helper()

	for _, rule := range structuralRules() {
		if rule.name == name {
			return rule
		}
	}

	t.Fatalf("no rule named %q; a fixture testing a rule that is not in structuralRules() "+
		"is a fixture for a gate nobody runs", name)

	return routeRule{}
}

// violations is the negative control's table: one row per structural rule.
//
// The mutations are stated as Go functions rather than as text substitutions
// because the document is a tree and a text substitution that matched nothing
// would be a fixture that "passed": `mutate` returns whether it applied, and a
// `false` fails the test before the rule ever runs.
func violations() []violation {
	return []violation{
		{
			name:     "two page titles",
			why:      "a second <h1> gives a screen reader two page titles",
			rule:     "exactly one h1",
			mentions: "<h1>",
			mutate: func(root *html.Node) bool {
				main := byID(root, "main")
				if main == nil {
					return false
				}

				main.InsertBefore(element("h1", "", "Another title"), main.FirstChild)

				return true
			},
		},
		{
			name:     "a heading that skips a level",
			why:      "an outline that jumps two levels hides the section in between",
			rule:     "heading levels never skip",
			mentions: "skip",
			mutate: func(root *html.Node) bool {
				heading := byID(root, "token-list-heading")
				if heading == nil {
					return false
				}

				heading.Data = "h4"

				return true
			},
		},
		{
			name:     "no main landmark",
			why:      "§4.4 gives every route exactly one main",
			rule:     "landmarks are present and distinguishing",
			mentions: "main landmark",
			mutate: func(root *html.Node) bool {
				main := byID(root, "main")
				if main == nil {
					return false
				}

				setAttr(main, "role", "article")

				return true
			},
		},
		{
			name:     "two navigation landmarks with one label",
			why:      "landmark navigation cannot tell the two apart",
			rule:     "landmarks are present and distinguishing",
			mentions: "labelled",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				nav := audit.find(func(node *html.Node) bool {
					return landmarkRole(node) == "navigation" &&
						attr(node, "aria-label") == "Campaign"
				})
				if nav == nil {
					return false
				}

				setAttr(nav, "aria-label", "Table actions")

				return true
			},
		},
		{
			name:     "a skip link to an absent landmark",
			why:      "a link that moves focus nowhere is worse than no link",
			rule:     "skip links come first and resolve",
			mentions: "#gone",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				link := audit.find(func(node *html.Node) bool {
					return node.Data == "a" && attr(node, "data-testid") == "skip-to-utilities"
				})
				if link == nil {
					return false
				}

				setAttr(link, "href", "#gone")

				return true
			},
		},
		{
			name:     "a focus stop ahead of the skip links",
			why:      "§7.2 puts the skip links first, or they are one more tab stop",
			rule:     "skip links come first and resolve",
			mentions: "focus stop",
			mutate: func(root *html.Node) bool {
				// Into `<body>`, not into a detached node: inserting into a node with
				// no parent is a no-op, and a no-op mutation makes the fixture report
				// "changed nothing". Body's first child is ahead of every skip link
				// wherever they sit.
				audit := &docAudit{root: root}
				body := audit.find(func(node *html.Node) bool { return node.Data == "body" })
				if body == nil || body.FirstChild == nil {
					return false
				}

				body.InsertBefore(element("button", "", "Pretend this comes first"),
					body.FirstChild)

				return true
			},
		},
		{
			name:     "a positive tabindex",
			why:      "§7.4 forbids a positive tabindex anywhere",
			rule:     "no positive tabindex",
			mentions: "tabindex=3",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				button := audit.find(func(node *html.Node) bool {
					return node.Data == "button" && attr(node, "data-testid") == "play-token"
				})
				if button == nil {
					return false
				}

				setAttr(button, "tabindex", "3")

				return true
			},
		},
		{
			// The exception's upper boundary: "inside a tablist" must not become
			// "anything goes inside a tablist". Without this row, a rule that
			// skipped the tablist entirely would pass every other fixture here.
			name:     "a positive tabindex inside the tablist",
			why:      "§7.4 says positive anywhere, and the roving pattern never needs one",
			rule:     "no positive tabindex",
			mentions: "tabindex=3",
			mutate: func(root *html.Node) bool {
				tablist := byTestID(root, "play-rail-tablist")
				if tablist == nil {
					return false
				}

				// Walked from the tablist itself: a document-order search for
				// "an element with a tabindex" would match `<main>` first and
				// mutate a landmark while claiming to mutate a tab — a fixture
				// that trips the rule for the wrong reason.
				inner := &docAudit{where: "the tablist", root: tablist}
				tab := inner.find(func(node *html.Node) bool {
					return hasAttribute(node, "tabindex")
				})
				if tab == nil {
					return false
				}

				setAttr(tab, "tabindex", "3")

				return true
			},
		},
		{
			// The exception's lower boundary, and the mutation that kills a rule
			// whose tablist test returned `true` for everything: a zero on an
			// element no widget owns is a fault, exactly as `wiki` and `plugins`
			// hold it.
			name:     "a zero tabindex outside any widget",
			why:      "only -1 is permitted outside a composite widget's roving item",
			rule:     "no positive tabindex",
			mentions: "tabindex=0",
			mutate: func(root *html.Node) bool {
				paragraph := byTestID(root, "token-list-keys")
				if paragraph == nil {
					return false
				}

				setAttr(paragraph, "tabindex", "0")

				return true
			},
		},
		{
			name:     "a focus stop hidden from assistive technology",
			why:      "the reader tabs into an element their technology cannot see",
			rule:     "no aria-hidden on a focus stop",
			mentions: "aria-hidden",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				button := audit.find(func(node *html.Node) bool {
					return node.Data == "button" && attr(node, "data-testid") == "play-chat"
				})
				if button == nil {
					return false
				}

				setAttr(button, "aria-hidden", "true")

				return true
			},
		},
		{
			name:     "a reference that resolves to nothing",
			why:      "a disclosure claiming to control an element that is not there",
			rule:     "every ARIA reference resolves",
			mentions: "aria-controls",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				button := audit.find(func(node *html.Node) bool {
					return node.Data == "button" && attr(node, "data-testid") == "play-token"
				})
				if button == nil {
					return false
				}

				setAttr(button, "aria-controls", "nowhere")

				return true
			},
		},
		{
			name:     "a duplicated id",
			why:      "an id with two elements gives every reference two answers",
			rule:     "identifiers are unique",
			mentions: "id=\"main\"",
			mutate: func(root *html.Node) bool {
				heading := byID(root, "page-heading")
				if heading == nil {
					return false
				}

				setAttr(heading, "id", "main")

				return true
			},
		},
		{
			name:     "role application",
			why:      "it switches assistive technology off for everything inside it",
			rule:     "no role application",
			mentions: "role=application",
			mutate: func(root *html.Node) bool {
				main := byID(root, "main")
				if main == nil {
					return false
				}

				setAttr(main, "role", "application")

				return true
			},
		},
		{
			name:     "a focus stop with no target class",
			why:      "§10.6's size comes from the class, and no class means no size",
			rule:     "target class",
			mentions: ui.TargetClass,
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				button := audit.find(func(node *html.Node) bool {
					return node.Data == "button" && attr(node, "data-testid") == "play-roll-trigger"
				})
				if button == nil {
					return false
				}

				removeClass(button, ui.TargetClass)

				return true
			},
		},
	}
}

// removeClass drops one class token.
func removeClass(node *html.Node, token string) bool {
	kept := make([]string, 0, len(strings.Fields(attr(node, "class"))))

	for _, class := range strings.Fields(attr(node, "class")) {
		if class != token {
			kept = append(kept, class)
		}
	}

	if len(kept) == len(strings.Fields(attr(node, "class"))) {
		return false
	}

	setAttr(node, "class", strings.Join(kept, " "))

	return true
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the neutral control,
// and it is the one that catches an audit firing for the wrong reason.
//
// Each rule runs over the route's own document with a change applied that
// **violates nothing that rule cares about** — an extra heading at the right
// level, an id that is still unique, a reference that is not an id reference —
// and must be silent. Without this, the negative control would pass for a rule
// that was really reporting something else entirely: the fixture would produce a
// finding, the count would be non-zero, and the gate would look covered while
// the rule under test was never exercised.
//
// One document per rule, changed only for that rule. They cannot share one
// document, because a document that violated *something* would make silence the
// wrong expectation rather than a strict one.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	served := servedDocuments(t)
	baseline := parseDocument(t, served[0])

	for _, testCase := range []struct {
		name   string
		rule   string
		mutate func(*html.Node) bool
	}{
		{
			name: "one h1 with a different heading",
			rule: "exactly one h1",
			mutate: func(root *html.Node) bool {
				heading := byID(root, "page-heading")
				if heading == nil {
					return false
				}

				heading.FirstChild.Data = "The Hollow Choir"

				return true
			},
		},
		{
			name: "a heading added at the level below",
			rule: "heading levels never skip",
			mutate: func(root *html.Node) bool {
				heading := byID(root, "token-list-heading")
				if heading == nil {
					return false
				}

				heading.Parent.InsertBefore(element("h3", "", "Detail"), heading.NextSibling)

				return true
			},
		},
		{
			name: "a paragraph removed from the centre",
			rule: "landmarks are present and distinguishing",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				paragraph := audit.find(func(node *html.Node) bool {
					return node.Data == "p" && hasClass(node, "panel-note")
				})
				if paragraph == nil || paragraph.Parent == nil {
					return false
				}

				paragraph.Parent.RemoveChild(paragraph)

				return true
			},
		},
		{
			name: "a later focus stop removed",
			rule: "skip links come first and resolve",
			mutate: func(root *html.Node) bool {
				audit := &docAudit{root: root}

				button := audit.find(func(node *html.Node) bool {
					return node.Data == "button" && attr(node, "data-testid") == "play-chat"
				})
				if button == nil || button.Parent == nil {
					return false
				}

				button.Parent.RemoveChild(button)

				return true
			},
		},
		{
			name: "a minus-one tabindex added",
			rule: "no positive tabindex",
			mutate: func(root *html.Node) bool {
				paragraph := byTestID(root, "token-list-keys")
				if paragraph == nil {
					return false
				}

				setAttr(paragraph, "tabindex", "-1")

				return true
			},
		},
		{
			name: "a paragraph hidden that no keyboard can reach",
			rule: "no aria-hidden on a focus stop",
			mutate: func(root *html.Node) bool {
				paragraph := byTestID(root, "token-list-keys")
				if paragraph == nil {
					return false
				}

				setAttr(paragraph, "aria-hidden", "true")

				return true
			},
		},
		{
			name: "a name that names nothing",
			rule: "every ARIA reference resolves",
			mutate: func(root *html.Node) bool {
				section := byID(root, webplayPanelRegionID)
				if section == nil {
					return false
				}

				removeAttr(section, "aria-labelledby")
				setAttr(section, "aria-label", "Placements")

				return true
			},
		},
		{
			name: "an id added that nothing else carries",
			rule: "identifiers are unique",
			mutate: func(root *html.Node) bool {
				paragraph := byTestID(root, "token-list-keys")
				if paragraph == nil {
					return false
				}

				setAttr(paragraph, "id", "keyboard-hint")

				return true
			},
		},
		{
			name: "a role that is not application",
			rule: "no role application",
			mutate: func(root *html.Node) bool {
				section := byID(root, webplayPanelRegionID)
				if section == nil {
					return false
				}

				setAttr(section, "role", "note")

				return true
			},
		},
		{
			name: "the target class added where nothing needed it",
			rule: "target class",
			mutate: func(root *html.Node) bool {
				paragraph := byTestID(root, "token-list-keys")
				if paragraph == nil {
					return false
				}

				setAttr(paragraph, "class", attr(paragraph, "class")+" "+ui.TargetClass)

				return true
			},
		},
		{
			name: "copy that names neither retired word",
			rule: "vocabulary",
			mutate: func(root *html.Node) bool {
				heading := byID(root, "page-heading")
				if heading == nil {
					return false
				}

				heading.FirstChild.Data = "The Gilded Cage"

				return true
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &docAudit{where: testCase.name, root: cloneDocument(t, baseline.root)}

			if !testCase.mutate(audit.root) {
				t.Fatalf("the fixture for %q changed nothing; silence would be vacuous",
					testCase.name)
			}

			faults := auditFor(t, testCase.rule).audit(audit)
			if len(faults) != 0 {
				t.Errorf("the %q audit reported %d finding(s) about a document that violates "+
					"nothing it cares about: %v. An audit that fires for the wrong reason is "+
					"an audit that will be switched off the first time it is inconvenient",
					testCase.rule, len(faults), faults)
			}
		})
	}
}

// auditFor looks a rule up by name, including the two that are gated by their
// own top-level test rather than by the structural contract.
func auditFor(t *testing.T, name string) routeRule {
	t.Helper()

	if name == "target class" {
		return routeRule{name: name, audit: targetClassOnEveryFocusStop}
	}

	if name == "vocabulary" {
		return routeRule{name: name, audit: vocabularyFaults}
	}

	return ruleNamed(t, name)
}

// cloneDocument re-parses a document so a mutation cannot leak into the next
// fixture.
//
// Re-parsed rather than copied field by field: `html.Node` holds a tree of
// pointers and a deep copy that missed one of them would share it, which is the
// classic way a mutation-based control silently mutates its own baseline. The
// bytes are the source of truth, so the bytes are what gets cloned.
func cloneDocument(t *testing.T, root *html.Node) *html.Node {
	t.Helper()

	var out strings.Builder

	if err := html.Render(&out, root); err != nil {
		t.Fatalf("re-render the document for a fixture: %v", err)
	}

	clone, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("re-parse the document for a fixture: %v", err)
	}

	return clone
}

// TestTheVocabularyAuditFindsTheWordWhereverItIs is UI §1.2's rule asserted
// against its own audit, in the six ways a word can be smuggled past a check
// that reads only the visible copy.
//
// Six, and each is a way a real interface has shipped one: in a paragraph, in a
// name a screen reader announces, in a tooltip, in a `data-` attribute a client
// reads, in an HTML comment a reviewer reads, and in an `id` that ends up in the
// accessibility tree through `aria-labelledby`.
//
// The audit must object to **every** one. An audit that cannot fail is worse
// than no audit — it is a green light wired to nothing — and an audit that fails
// five of six is worse than that, because the sixth is then the way the word
// comes back.
func TestTheVocabularyAuditFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		body  string
		word  string
		where string
	}{
		{
			name:  "in a text node",
			body:  `<html><head><title>t</title></head><body><p>The world map.</p></body></html>`,
			word:  "world",
			where: "text node",
		},
		{
			name: "in an aria-label",
			body: `<html><head><title>t</title></head><body>` +
				`<p aria-label="your session is active">Active.</p></body></html>`,
			word:  "session",
			where: "aria-label",
		},
		{
			name: "in a title attribute",
			body: `<html><head><title>t</title></head><body>` +
				`<p title="worlds beyond counting">Beyond.</p></body></html>`,
			word:  "world",
			where: "title",
		},
		{
			name: "in a data attribute",
			body: `<html><head><title>t</title></head><body>` +
				`<p data-note="session replay is off">Replay.</p></body></html>`,
			word:  "session",
			where: "data- attribute",
		},
		{
			name: "in an HTML comment",
			body: `<html><head><title>t</title></head><body><p>Visible.</p>` +
				`<!-- the world of the campaign --></body></html>`,
			word:  "world",
			where: "HTML comment",
		},
		{
			name: "in an id",
			body: `<html><head><title>t</title></head><body>` +
				`<p id="session-note" aria-labelledby="session-note">Note.</p></body></html>`,
			word:  "session",
			where: "id",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := &docAudit{where: testCase.name, root: parseFixture(t, testCase.body)}

			faults := vocabularyFaults(audit)

			mentioned := false

			for _, fault := range faults {
				if strings.Contains(strings.ToLower(fault), testCase.word) {
					mentioned = true
				}
			}

			if !mentioned {
				t.Errorf("the vocabulary audit did not object to %q appearing in an %s; "+
					"UI §1.2 retires the word from every text run of the interface, and an "+
					"audit that misses one place is an audit the word returns through. "+
					"Faults: %v", testCase.word, testCase.where, faults)
			}
		})
	}
}
