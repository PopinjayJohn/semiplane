package assets_test

// UI §10.2 and §10.6 for `GET /c/{slug}/assets/{path...}`, gate-blocking.
//
// # Why this file exists and why it is here rather than in `internal/httpapi`
//
// `make a11y` listed this package and it contributed **no** test matching the
// gate's pattern. `go test -run` answers `[no tests to run]` and exits 0, so the
// package held forty-nine tests, several of them about the failure document, and
// the gate was green because it had asked it nothing. The `A11Y_ROUTE_PKGS` guard
// exists so that cannot happen again; this file is what it is asking for.
//
// `internal/httpapi`'s own §10.2 audit (`shell_render_test.go`) cannot be reached
// from here: its helpers — `newAudit`, `assertVocabulary`, the landmark walker,
// `countingT` — are unexported in package `httpapi_test`, and a **test** package
// is not importable. So the helpers below are a second copy rather than a shared
// one. That is a real duplication and it is the right thing to name rather than
// hide: the eventual fix is one exported audit package imported by
// `httpapi_test`, `wiki_test` and `assets_test`, and until that exists this file
// is deliberately a *narrow* copy — it holds the rules §10.2 and §10.6 state for
// this route and nothing else, so the cost of the duplication stays small and the
// file a reviewer diffs against `shell_render_test.go` is short.
//
// # The subjects, and the one response that is not a subject
//
// §10.2 says "for every route". This route's success response is a battle map's
// bytes: `image/png`, not a document, and §10.2's rules are about a document's
// structure. So the list below is every state the route composes an **HTML
// document** for — the 404, the 403, the 400 and the 500 — plus an explicit
// assertion that the 200 is not one, because a route that served an asset as
// `text/html` would otherwise be unexamined by an audit that skipped every
// non-document response.
//
// The access gate's own refusal is the fourth thing, and it is JSON: ADR 0024
// makes `{"error":"not found"}` the answer for a campaign nobody may read, so that
// answer cannot become an existence oracle. It is not a document and §10.2 has no
// claim about it, which `TestEveryRouteRefusesAnUnknownCampaignWithANonDocument`
// states rather than leaves to inference.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/assets"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components"
)

// retiredEntities is UI §1.2's closed vocabulary. Neither entity exists, a
// leftover is a bug, and a grep for them is a test — which is this.
//
// Case-insensitive and a substring, because the failure being guarded against is a
// noun phrase inside a sentence ("your session has expired", "the world map"), and
// neither is spelled with a capital letter in isolation.
var retiredEntities = []string{"world", "session"}

// renderedDocument is one audited response.
type renderedDocument struct {
	// where names it in a failure message. The route's own states are named by
	// their status and the fact that produced them, because "the 404" is not a
	// document anybody can picture.
	where string
	// status is the response status. Carried because §10.2's subject is the
	// document and the status is how a reader knows which one they are looking at,
	// and because an audit that only ever sees a 200 is an audit of the easy path.
	status int
	// body is the response bytes.
	body []byte
}

// auditedDocuments returns every HTML document this route can put in front of a
// reader who passed the access gate.
//
// Six states, and the spread is the point: they are composed by three different
// `classify` branches and rendered by two different writers, so a rule checked on
// one of them is checked on one implementation.
//
//   - **404** for a file that is not there, for a name the index also refuses
//     (`.hidden.png`), and for an extension the closed table has no opinion on
//     (`archive.bin`) — three different causes, one answer.
//   - **403** for a path that left the campaign, and for a kind of file the route
//     declines to serve. Conflating the two with the 404s is the security bug this
//     route's `failure.go` argues about; auditing only one of them would not see
//     it.
//   - **400** for a path that names nothing, and for one carrying a NUL.
//   - **500** for a campaign whose content root is gone — a degraded instance, and
//     the state a reader sees when the operator's fault is the cause.
func auditedDocuments(t *testing.T) []renderedDocument {
	t.Helper()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())
	fixed.write("images/keep.png", mapBody())
	fixed.write("page.html", []byte("<html></html>"))
	fixed.write(".hidden.png", []byte("contents"))
	fixed.write("archive.bin", []byte{0x00})

	documents := make([]renderedDocument, 0, 8)
	documents = append(documents,
		fromRecorder("404 — a file that is not there",
			fixed.get("/c/greyhaven/assets/absent.png", gmRequestor())),
		fromRecorder("404 — a name the index also refuses",
			fixed.get("/c/greyhaven/assets/.hidden.png", gmRequestor())),
		fromRecorder("404 — an extension the closed table has no opinion on",
			fixed.get("/c/greyhaven/assets/archive.bin", gmRequestor())),
		fromRecorder("403 — a path that left the campaign",
			fixed.get("/c/greyhaven/assets/notes/%2e%2e%2f%2e%2e%2fetc%2fpasswd", gmRequestor())),
		fromRecorder("403 — a kind of file the route declines to serve",
			fixed.get("/c/greyhaven/assets/page.html", gmRequestor())),
		fromRecorder("400 — a path that names nothing",
			fixed.get("/c/greyhaven/assets/", gmRequestor())),
		fromRecorder("400 — a path carrying a NUL",
			fixed.get("/c/greyhaven/assets/map%00.png", gmRequestor())),
	)

	return append(documents, degradedCampaignDocument(t))
}

// degradedCampaignDocument is the 500: a campaign row whose content root is not
// there.
//
// S-4.5 calls that **degraded** rather than gone — the server keeps running and
// the operator has to hear about it — and this route answers it as a fault rather
// than as a 404 precisely so it is logged above debug. The reader sees the same
// designed state as every other failure, which is exactly why it belongs in this
// list: a composition that reached the 500 by a different branch could render it
// differently, and nothing else here would see it.
//
// Assembled here rather than through the harness because the harness opens a root
// for every slug it knows, and the whole subject here is a slug whose root is
// missing. The chain is `serveMountedAt`'s, in the same order.
func degradedCampaignDocument(t *testing.T) renderedDocument {
	t.Helper()

	registry := content.NewRegistry(content.RefuseSymlinks)

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content registry: %v", err)
		}
	})

	campaignMux := http.NewServeMux()
	assets.Mount(campaignMux, &assets.Handler{
		Roots:       registry,
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		SignOutHref: "/logout",
	})

	backing := campaignStore{
		campaigns: map[string]domain.Campaign{
			testSlug: {
				ID:         testCampID,
				Slug:       testSlug,
				Name:       "Greyhaven",
				Visibility: domain.VisibilityPublic,
			},
		},
		members: map[int64]domain.Role{gmUser: domain.RoleGM},
	}

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(backing)(middleware.Chain(campaignMux, campaigns.RequireRead)),
	)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/c/greyhaven/assets/map.png", http.NoBody,
	)
	req = req.WithContext(identity.WithRequestor(req.Context(), gmRequestor()))

	recorder := httptest.NewRecorder()
	middleware.RequestID(outer).ServeHTTP(recorder, req)

	return fromRecorder("500 — a campaign whose content root is gone", recorder)
}

// fromRecorder captures a response as an audited document.
func fromRecorder(where string, recorder *httptest.ResponseRecorder) renderedDocument {
	return renderedDocument{where: where, status: recorder.Code, body: recorder.Body.Bytes()}
}

// abbreviateBody truncates a body for a failure message, because a whole rendered
// document in a test failure is unreadable.
func abbreviateBody(body string) string {
	const limit = 200

	if len(body) > limit {
		return body[:limit] + "..."
	}

	return body
}

// --- The audit ---------------------------------------------------------------

// auditFailer is the subset of *testing.T the rules use.
//
// An interface rather than `*testing.T` because the rules have to be *tested* — a
// rule that cannot be shown to reject a violation it claims to catch is a gate
// wired to nothing — and a test of a `*testing.T`-typed function is a test that
// fails the suite. `silentFailer` below satisfies this and records instead of
// reporting.
//
// Deliberately without `Fatalf`: the parser's own constructor aborts on a
// response that is not HTML, and that is correct behaviour for it to have.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// docAudit is one parsed document and where it came from.
type docAudit struct {
	t      auditFailer
	where  string
	status int
	root   *html.Node
}

// parseDocument parses an audited response, aborting when it is not HTML.
//
// `bytes.NewReader` rather than the string, because the response is bytes and
// converting them only to convert them back is a place a non-UTF-8 body could be
// mangled silently.
func parseDocument(t *testing.T, doc renderedDocument) *docAudit {
	t.Helper()

	if !strings.Contains(strings.ToLower(string(doc.body)), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d); §10.2's "+
			"rules are about a document and there is nothing to audit. Begins: %q",
			doc.where, doc.status, abbreviateBody(string(doc.body)))
	}

	root, err := html.Parse(bytes.NewReader(doc.body))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", doc.where, err)
	}

	return &docAudit{t: t, where: doc.where, status: doc.status, root: root}
}

// parseFixture parses a minimal document written to trip one rule.
//
// Separate from `parseDocument` because a fixture is not a response: it has no
// route, no status and no headers, and the rule under test should be the only
// thing wrong with it.
func parseFixture(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return root
}

// elements calls visit for every element, in **document order**.
//
// Document order rather than depth-first-by-branch: §10.2's skip-link rule is a
// statement about the order a keyboard meets things, and a walk that finished one
// element's whole subtree before visiting its next sibling would report a different
// order than a reader gets.
func (a *docAudit) elements(visit func(*html.Node)) {
	a.t.Helper()

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)

			if child.Type == html.ElementNode {
				visit(child)
			}
		}
	}

	walk(a.root)
}

// focusStops returns every element a keyboard can reach, in document order.
//
// The set is §10.6's own, spelled out rather than reached through a CSS selector:
// a selector is evaluated against this project's opinion of what is interactive,
// and that opinion is the thing under test. `a[href]` and not every `<a>` — an
// anchor with no `href` is not focusable and so is not a target — and read from
// the attribute rather than the selector so a mutation that removes the `href`
// shows up here instead of quietly deleting the element from the audit.
func (a *docAudit) focusStops() []*html.Node {
	a.t.Helper()

	var found []*html.Node

	a.elements(func(node *html.Node) {
		if isFocusStop(node) {
			found = append(found, node)
		}
	})

	return found
}

// isFocusStop reports whether §10.6 walks this element.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "button", "input", "select", "textarea", "summary":
		return true
	case "a":
		return hasAttribute(node, "href")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// attr returns an attribute's value, or "" when it is absent.
//
// The two are distinguished by `hasAttribute`, because `aria-label=""` names a
// landmark nothing and §10.2 counts that as unlabelled — a helper returning "" for
// both would let both through one assertion.
func attr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}

	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasAttribute reports whether an attribute is present, whatever its value.
func hasAttribute(node *html.Node, name string) bool {
	if node == nil {
		return false
	}

	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// classTokens splits an element's class list.
//
// Split on whitespace and compared by token, never `strings.Contains`: `target`
// is a substring of `data-target` and of `target-large`, and §7.3's contract is
// about the class.
func classTokens(node *html.Node) []string {
	return strings.Fields(attr(node, "class"))
}

// hasClass reports whether an element's class list contains a token.
func hasClass(node *html.Node, token string) bool {
	return slices.Contains(classTokens(node), token)
}

// findAll returns every element with a tag name, in document order.
func findAll(node *html.Node, tag string) []*html.Node {
	var found []*html.Node

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == tag {
				found = append(found, child)
			}

			walk(child)
		}
	}

	walk(node)

	return found
}

// byID returns the element carrying an id, or nil.
func byID(root *html.Node, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode && attr(child, "id") == id {
				found = child

				return
			}

			walk(child)
		}
	}

	walk(root)

	return found
}

// nodePath names an element for a failure message: its nearest four ancestors,
// innermost first, each labelled by id, test hook or first class.
//
// Innermost first and truncated, because a document's full ancestry is a
// paragraph long and a message nobody reads is a message nobody acts on.
func nodePath(node *html.Node) string {
	parts := []string{}

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		label := current.Data

		switch {
		case attr(current, "id") != "":
			label += "#" + attr(current, "id")
		case attr(current, "data-testid") != "":
			label += "[" + attr(current, "data-testid") + "]"
		default:
			if named := classTokens(current); len(named) > 0 {
				label += "." + named[0]
			}
		}

		parts = append([]string{label}, parts...)
	}

	if len(parts) > 4 {
		parts = parts[len(parts)-4:]
	}

	return strings.Join(parts, " > ")
}

// roleOf returns an element's explicit role, or the implicit one its tag carries.
//
// The implicit half is not a convenience: §7.2's contract is written as
// `header[banner]` and `footer[contentinfo]`, and a document relying on the
// implicit role is correct. The audit has to agree with the specification rather
// than with the subset somebody wrote an attribute for.
func roleOf(node *html.Node) string {
	if explicit := attr(node, "role"); explicit != "" {
		return explicit
	}

	switch node.Data {
	case "header":
		return "banner"
	case "footer":
		return "contentinfo"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "aside":
		return "complementary"
	case "form":
		// A form is a landmark only with an explicit role. An unlabelled form is
		// not one, and calling it `form` here would invent a landmark the
		// specification does not have.
		return ""
	default:
		return ""
	}
}

// textOf returns an element's descendant **text nodes**.
//
// Text nodes only: an `alt` or a `title` is text a reader hears but it is not in
// the text content, and the vocabulary rule below reaches attributes through its
// own attribute scan — a helper that returned attributes too would make "no label
// mentions X" and "no attribute mentions X" the same assertion.
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

// landmark is one landmark region and how it is named.
type landmark struct {
	role string
	// name is the accessible name, following `aria-labelledby` when that is what
	// it uses.
	name string
	// namedBy records the mechanism, so a failure can say "labelled by nothing"
	// rather than "labelled by the empty string".
	namedBy string
	node    *html.Node
}

// landmarks returns the document's landmark regions in document order.
func (a *docAudit) landmarks() []landmark {
	a.t.Helper()

	var found []landmark

	a.elements(func(node *html.Node) {
		role := roleOf(node)

		switch role {
		case "banner", "navigation", "main", "complementary", "contentinfo", "search":
			found = append(found, landmark{
				role:    role,
				name:    accessibleName(a, node),
				namedBy: namingMechanism(node),
				node:    node,
			})
		default:
		}
	})

	return found
}

// namingMechanism records which attribute named a landmark.
func namingMechanism(node *html.Node) string {
	if attr(node, "aria-label") != "" {
		return "aria-label"
	}

	if hasAttribute(node, "aria-labelledby") {
		return "aria-labelledby"
	}

	return "nothing"
}

// accessibleName returns a landmark's name, following `aria-labelledby` when used.
func accessibleName(a *docAudit, node *html.Node) string {
	if label := attr(node, "aria-label"); label != "" {
		return label
	}

	var parts []string

	for id := range strings.FieldsSeq(attr(node, "aria-labelledby")) {
		if target := byID(a.root, id); target != nil {
			parts = append(parts, strings.TrimSpace(textOf(target)))
		}
	}

	return strings.Join(parts, " ")
}

// skipLink is one skip link and what it points at.
type skipLink struct {
	text string
	href string
	node *html.Node
}

// skipLinks returns the document's skip links, in document order.
//
// By class rather than by `href`: §7.2 fixes the targets and the class is the
// hook the stylesheet uses to move the link off-screen until it is focused, so a
// "skip" link without the class is not off-screen and is therefore in the wrong
// place. Reading it from the class is the same claim made from the other end.
func (a *docAudit) skipLinks() []skipLink {
	a.t.Helper()

	var found []skipLink

	a.elements(func(node *html.Node) {
		if node.Data != "a" || !hasClass(node, "skip-link") {
			return
		}

		found = append(found, skipLink{
			text: strings.TrimSpace(textOf(node)),
			href: attr(node, "href"),
			node: node,
		})
	})

	return found
}

// containsFocusStop reports whether a subtree holds anything a keyboard reaches.
func containsFocusStop(node *html.Node) bool {
	found := false

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil && !found; child = child.NextSibling {
			if isFocusStop(child) {
				found = true

				return
			}

			walk(child)
		}
	}

	walk(node)

	return found
}

// hasAncestor reports whether any ancestor has the given tag name.
func hasAncestor(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// --- §10.2: the rules ---------------------------------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking, for
// this route's every document.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y
// test failed". The order is §10.2's own list, in its order, plus the two the
// list implies and does not spell out — heading levels and reference integrity —
// because a landmark can be present and correctly labelled while a document's
// outline and its ARIA wiring are both broken, and neither shows up in a
// "landmarks are fine" result.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			t.Run("ExactlyOneH1", func(t *testing.T) {
				assertExactlyOneH1(t, audit)
			})
			t.Run("HeadingLevelsNeverSkip", func(t *testing.T) {
				assertHeadingLevelsNeverSkip(t, audit)
			})
			t.Run("LandmarksArePresentAndDistinguishing", func(t *testing.T) {
				assertLandmarksArePresentAndDistinguishing(t, audit)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, audit)
			})
			t.Run("NoInlineOutlineSuppression", func(t *testing.T) {
				assertNoInlineOutlineSuppression(t, audit)
			})
			t.Run("NoAriaHiddenOnAFocusStop", func(t *testing.T) {
				assertNoAriaHiddenOnAFocusStop(t, audit)
			})
			t.Run("SkipLinksComeFirstAndResolve", func(t *testing.T) {
				assertSkipLinksComeFirstAndResolve(t, audit)
			})
			t.Run("EveryTestIDIsPresent", func(t *testing.T) {
				assertEveryTestIDIsPresent(t, audit)
			})
			t.Run("NoRetiredEntityIsNamed", func(t *testing.T) {
				assertNoRetiredEntityIsNamed(t, audit)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertEveryReferenceResolves(t, audit)
			})
		})
	}
}

// assertExactlyOneH1 is §10.2's first rule, and §7.2's.
//
// Exactly one, not at least one. Two `<h1>`s tell a screen reader the page has two
// titles; none tell it the page has none while looking complete to a sighted
// reader.
func assertExactlyOneH1(t auditFailer, audit *docAudit) {
	t.Helper()

	headings := findAll(audit.root, "h1")
	if len(headings) == 1 {
		return
	}

	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, nodePath(heading))
	}

	t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (UI §10.2, "+
		"§7.2); found: %s", audit.where, len(headings), strings.Join(names, ", "))
}

// assertHeadingLevelsNeverSkip is §7.2's second structural rule.
//
// Separate from the `<h1>` count because it can fail where the count passes: a
// document whose headings start at `<h3>` has exactly one `<h1>`-shaped hole and
// no valid outline at all.
func assertHeadingLevelsNeverSkip(t auditFailer, audit *docAudit) {
	t.Helper()

	previous := 0

	audit.elements(func(node *html.Node) {
		level, ok := headingLevel(node)
		if !ok {
			return
		}

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level is "+
				"announced as a missing section (UI §7.2)",
				audit.where, previous, level, nodePath(node))
		}

		previous = level
	})
}

// headingLevel returns an element's heading level, or false when it is not one.
func headingLevel(node *html.Node) (int, bool) {
	if node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0, false
	}

	level, err := strconv.Atoi(node.Data[1:])
	if err != nil || level < 1 || level > 6 {
		return 0, false
	}

	return level, true
}

// assertLandmarksArePresentAndDistinguishing is §10.2's landmark rule and §7.2's,
// in four parts.
//
// Presence, because a missing landmark is a region a landmark-navigation reader
// cannot jump to. Distinguishing labels, because two landmarks of the same role
// with the same name make landmark navigation useless. Exactness, for the three
// the record writes out. And nesting, because a `contentinfo` inside a `main` is
// the article's and not the page's, and a screen reader's landmark list will say
// otherwise.
func assertLandmarksArePresentAndDistinguishing(t auditFailer, audit *docAudit) {
	t.Helper()

	found := audit.landmarks()

	present := map[string]int{}
	for _, region := range found {
		present[region.role]++
	}

	// `main` is the only landmark required unconditionally. §4.6 removes the
	// navigation before a campaign exists, and §3.1's tiers move the rail out of
	// the flow rather than out of the document — the rail stays in the
	// accessibility tree at every tier so a script-less reader does not lose it,
	// which `shell.css` states at the rule that moves it off-screen.
	if present["main"] == 0 {
		t.Errorf("%s: the document has no main landmark (UI §10.2, §7.2)", audit.where)
	}

	if present["main"] > 1 {
		t.Errorf("%s: the document has %d main landmarks, want 1 (UI §7.2); two of "+
			"them give a screen reader two articles and no way to choose",
			audit.where, present["main"])
	}

	for _, region := range found {
		switch region.role {
		case "navigation", "complementary", "search":
			if region.name == "" {
				t.Errorf("%s: the %s landmark at %s is labelled by %s; §10.2 requires "+
					"distinguishing labels, and an unnamed landmark cannot be told from "+
					"another of the same role (UI §7.2)",
					audit.where, region.role, nodePath(region.node), region.namedBy)
			}
		case "banner", "contentinfo":
		default:
		}
	}

	// The three the design record writes out, exactly. Both navigation names are
	// checked as a set rather than positionally, because which of them comes first
	// is a tier decision the stylesheet makes and the markup does not.
	allowedNavigation := map[string]bool{"Campaign": true, "Primary": true}

	// searchLandmarkNames are the two `role="search"` labels §7.2's own zones fix:
	// the header's persistent form, and the search route's form for the campaign
	// being searched.
	//
	// A set rather than one name, because a document may carry either or both and
	// §7.2's requirement is that they are *distinct* — a rule about the pair, not
	// about either member. Asserting a single literal tests the pair by naming one
	// of them, and would have failed the search route's own landmark the moment
	// that route landed.
	searchLandmarkNames := []string{"Search pages", "Search this campaign"}

	allowedSearch := make(map[string]bool, len(searchLandmarkNames))
	for _, name := range searchLandmarkNames {
		allowedSearch[name] = true
	}

	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigation[region.name] {
				t.Errorf("%s: a navigation landmark is labelled %q; UI §7.2 names them "+
					"%q (the campaign nav) and %q (the compact bar)",
					audit.where, region.name, "Campaign", "Primary")
			}
		case "complementary":
			if region.name != "Utilities" {
				t.Errorf("%s: the complementary landmark is labelled %q, want %q (UI §7.2)",
					audit.where, region.name, "Utilities")
			}
		case "search":
			if !allowedSearch[region.name] {
				t.Errorf("%s: a search landmark is labelled %q; UI §7.2 requires the "+
					"header's form and the search route's own form to be named "+
					"differently, and those are the two names the record fixes: %v",
					audit.where, region.name, searchLandmarkNames)
			}
		case "banner", "contentinfo", "main":
		default:
		}
	}

	// Distinctness across the whole document.
	seen := map[string]string{}

	for _, region := range found {
		if region.name == "" {
			continue
		}

		key := region.role + "=" + region.name
		if first, duplicate := seen[key]; duplicate {
			t.Errorf("%s: two %s landmarks are both labelled %q (%s and %s); §10.2 "+
				"requires distinguishing labels or landmark navigation is useless",
				audit.where, region.role, region.name, first, nodePath(region.node))
		}

		seen[key] = nodePath(region.node)
	}

	for _, region := range found {
		if region.role == "contentinfo" && hasAncestor(region.node, "main") {
			t.Errorf("%s: a contentinfo landmark is inside <main> at %s; it is the "+
				"page's footer, not the article's (UI §7.2)",
				audit.where, nodePath(region.node))
		}
	}
}

// assertNoPositiveTabindex is §10.2's tabindex rule and §7.4's "no positive
// tabindex, anywhere — gate failure".
//
// Zero is excluded too. §7.4 says Tab follows natural document order, and a
// `tabindex="0"` moves one element to the front of the focus order while the
// markup still reads in the original order — so the two disagree and neither a
// reader nor a test can tell which one the page means. Only `-1` is usable: it
// makes an element programmatically focusable for a skip link without putting it
// in the tab sequence.
//
// The value is parsed as a number rather than compared as a string, and an
// unparseable value is itself a finding: three browsers disagreeing about the tab
// order is the failure, and `+3` sorted before `0` in a string comparison.
func assertNoPositiveTabindex(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		raw := attr(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer; three "+
				"browsers would disagree about the tab order (UI §10.2, §7.2)",
				audit.where, nodePath(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted. 0 moves the "+
				"element to the front of the tab order while the markup still reads "+
				"in document order (UI §10.2, §7.4)",
				audit.where, nodePath(node), value)
		}
	})
}

// assertNoInlineOutlineSuppression is §10.2's "no `outline: none` without a
// replacement", over the half of it a document can carry.
//
// **The other half is the stylesheet's, and it is not this file's.** A rule in a
// `.css` file that removes a focus indicator is invisible to a DOM, which is why
// `internal/web`'s `TestNoRuleRemovesAFocusIndicatorWithoutAReplacement` reads the
// *built* stylesheet and asserts both that `outline: none` appears nowhere and
// that `:focus-visible` is present. Run that half from here and it would be a
// second copy of the same rule with a different fixture.
//
// What a route can violate is the **inline** half: a `style` attribute the route
// or a template interpolated, which overrides the sheet for one element and is
// invisible to every stylesheet-level gate. That is the case this asserts, and it
// is worth asserting because an inline style is exactly what a templ expression
// produces by accident.
//
// Read as a *declaration* in the attribute rather than as a substring, so that
// `outline-offset` and `outline-colour` — which suppress nothing — are not findings,
// and `outline : 0` is.
func assertNoInlineOutlineSuppression(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		style := attr(node, "style")
		if style == "" {
			return
		}

		for declaration := range strings.SplitSeq(style, ";") {
			property, value, split := strings.Cut(declaration, ":")
			if !split {
				continue
			}

			property = strings.ToLower(strings.TrimSpace(property))
			if property != "outline" {
				continue
			}

			switch strings.ToLower(strings.TrimSpace(value)) {
			case "none", "0":
				t.Errorf("%s: %s carries an inline `outline: %s`; §10.2 and §7.10 "+
					"prohibit removing a focus indicator without a visible replacement, "+
					"and this stylesheet's replacement is the two-tone ring rather than "+
					"a suppression. Nothing on this element puts one back",
					audit.where, nodePath(node), strings.TrimSpace(value))
			default:
			}
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §10.2's `aria-hidden` rule and §7.10's,
// asserted on the *combination* rather than the attribute.
//
// Not on the attribute: the legitimate case in this shell is an icon inside a
// focusable link — an `<svg aria-hidden="true">` within an `<a href>`. The
// attribute alone is therefore not the finding; the attribute on something a
// keyboard can reach is.
//
// Two cases, and the second is the one that catches real bugs: a focusable element
// *inside* an `aria-hidden` subtree is just as unreachable, and the offending
// attribute is on an ancestor several levels up.
func assertNoAriaHiddenOnAFocusStop(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if attr(node, "aria-hidden") != "true" {
			return
		}

		if isFocusStop(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader "+
				"cannot see is one that reader cannot reach (UI §10.2, §7.10)",
				audit.where, nodePath(node))
		}

		if containsFocusStop(node) {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements, so "+
				"everything inside it is unreachable to a screen reader (UI §7.10)",
				audit.where, nodePath(node))
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Type == html.ElementNode &&
				attr(ancestor, "aria-hidden") == "true" && containsFocusStop(ancestor) {
				t.Errorf("%s: %s is inside the aria-hidden subtree at %s, which "+
					"contains focusable elements; everything focusable inside it is "+
					"unreachable to a screen reader (UI §7.10)",
					audit.where, nodePath(node), nodePath(ancestor))

				return
			}
		}
	})
}

// assertSkipLinksComeFirstAndResolve is §10.2's "skip links first in tab order"
// and §7.2's own list, in three parts.
//
// Position — the links must be the first focus stops in the document. A skip link
// after the banner is not a skip link, it is a link named "skip".
//
// Resolution — each `href="#id"` must resolve, and to a focusable landmark. A skip
// link to a landmark that is not in the document moves focus nowhere, which costs a
// reader a keypress and teaches them the skip links are unreliable.
//
// Order — §7.2's sequence, with the campaign-navigation link only where the
// navigation exists. §4.6 removes the nav before a campaign does and §7.2 puts the
// link "only where the nav exists", so the link and the landmark are one condition
// expressed in two places. That is the exact thing a test is for, and both
// directions are asserted because "a link with no landmark" and "a landmark with no
// link" are both failures and only one of them is visible in a rendering.
func assertSkipLinksComeFirstAndResolve(t auditFailer, audit *docAudit) {
	t.Helper()

	links := audit.skipLinks()
	if len(links) == 0 {
		t.Errorf("%s: the document has no skip link; §7.2 makes them the first "+
			"focusable elements and this shell always has at least one", audit.where)

		return
	}

	focusStops := audit.focusStops()

	for index, link := range links {
		if index >= len(focusStops) || focusStops[index] != link.node {
			t.Errorf("%s: skip link %d (%q) is not focus stop %d; §7.2 puts the skip "+
				"links first in tab order, before the banner",
				audit.where, index+1, link.text, index+1)

			continue
		}

		fragment, isFragment := strings.CutPrefix(link.href, "#")
		if !isFragment || fragment == "" {
			t.Errorf("%s: skip link %q has href %q; §7.2's links are fragments",
				audit.where, link.text, link.href)

			continue
		}

		target := byID(audit.root, fragment)
		if target == nil {
			t.Errorf("%s: skip link %q points at #%s, which is not in the document; "+
				"§4.6 removes some landmarks per route, so the link has to go with them",
				audit.where, link.text, fragment)

			continue
		}

		if !hasAttribute(target, "tabindex") {
			t.Errorf("%s: skip link %q lands on %s, which has no tabindex; without "+
				"one the target is not focusable and focus lands at the top of a "+
				"scroll container instead of on an announced element (UI §7.2)",
				audit.where, link.text, nodePath(target))
		}

		if roleOf(target) == "" {
			t.Errorf("%s: skip link %q lands on %s, which is not a landmark; a skip "+
				"link's target is the region it names (UI §7.2)",
				audit.where, link.text, nodePath(target))
		}
	}

	contentAt, navigationAt := -1, -1

	for index, link := range links {
		switch {
		case strings.Contains(link.text, "content"):
			contentAt = index
		case strings.Contains(link.text, "navigation"):
			navigationAt = index
		case strings.Contains(link.text, "utilities"):
		default:
		}
	}

	if contentAt != 0 {
		t.Errorf("%s: the first skip link is %q; §7.2's order is content first, then "+
			"campaign navigation where the nav exists, then utilities",
			audit.where, linkTextAt(links, 0))
	}

	if navigationAt >= 0 && navigationAt < contentAt {
		t.Errorf("%s: the campaign-navigation skip link precedes the content link; "+
			"§7.2's order is content first", audit.where)
	}

	hasNavLandmark := byID(audit.root, "nav") != nil
	hasNavLink := navigationAt >= 0

	if hasNavLandmark && !hasNavLink {
		t.Errorf("%s: the document has a navigation landmark but no skip link to it; "+
			"§7.2 lists %q -> #nav wherever the nav exists",
			audit.where, "Skip to campaign navigation")
	}

	if hasNavLink && !hasNavLandmark {
		t.Errorf("%s: the document has a skip link to #nav but no navigation landmark; "+
			"§4.6 removes the nav before a campaign exists and a skip link to an "+
			"absent landmark moves focus nowhere", audit.where)
	}
}

// linkTextAt names the nth skip link for a failure message.
func linkTextAt(links []skipLink, index int) string {
	if index >= len(links) {
		return "(none)"
	}

	return links[index].text
}

// assertEveryTestIDIsPresent is §10.2's "every `data-testid` present", in two
// halves.
//
// The well-formedness half: a hook must have a non-empty value and appear once. A
// hook that appears twice selects nothing, and an *absent* hook is not an empty one
// — counting the empty string would report every unhooked element as a duplicate
// of every other, which is how this first reported fifteen duplicates on a correct
// shell.
//
// The coverage half: the hooks **this route's own view model promises**, by state.
// `components.WikiPageView.Heading` is always set, so `page-title` is always
// present; `Body` and `Failure` are alternatives, so a document has exactly one of
// `page-body` and `error-state`. Asserting the pair is what makes the well-formed
// half mean something — a route that stopped rendering its own body would otherwise
// satisfy "every hook present" by rendering none.
func assertEveryTestIDIsPresent(t auditFailer, audit *docAudit) {
	t.Helper()

	counts := map[string]int{}

	audit.elements(func(node *html.Node) {
		if !hasAttribute(node, "data-testid") {
			return
		}

		counts[attr(node, "data-testid")]++
	})

	duplicates := make([]string, 0)

	for id, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, id+" x"+strconv.Itoa(count))
		}
	}

	sort.Strings(duplicates)

	if len(duplicates) > 0 {
		t.Errorf("%s: these data-testid hooks appear more than once: %s; a hook that "+
			"appears twice selects nothing (UI §10.2)", audit.where, strings.Join(duplicates, ", "))
	}

	hooks := map[string]int{}
	for id := range counts {
		hooks[id]++

		if strings.TrimSpace(id) == "" {
			t.Errorf("%s: an element carries an empty data-testid; a hook with no "+
				"value selects nothing and cannot be asserted on (UI §10.2)",
				audit.where)
		}
	}

	// The hooks this route's view model promises.
	for _, required := range []string{"page-title", "shell-main", "shell-rail"} {
		if hooks[required] == 0 {
			t.Errorf("%s: the document carries no %q hook; components.WikiPageView "+
				"promises it on every state of this route (UI §10.2)",
				audit.where, required)
		}
	}

	// Exactly one of the body and the failure state. Both would render two
	// different things at once; neither would render the page.
	body, failure := hooks["page-body"], hooks["error-state"]
	if body+failure != 1 {
		t.Errorf("%s: the document carries %d page-body hook(s) and %d error-state "+
			"hook(s), want exactly 1 between them; components.WikiPageView renders "+
			"one or the other (UI §10.2)", audit.where, body, failure)
	}
}

// assertNoRetiredEntityIsNamed is UI §1.2 and §10.2's last clause: no rendered
// string contains "world" or "session".
//
// The scan covers **every text node, every comment, every attribute value and
// every attribute name**, and each inclusion has a reason.
//
// Comments: a word in a comment is invisible to a reader, which is exactly why an
// audit reading only text nodes would pass on a document whose markup still names a
// retired entity — and the comment is the one place a renderer is tempted to leave
// one, because nobody sees it.
//
// Attribute values: an `aria-label` or a `title` is announced to a reader, so a
// retired entity named there is named to the person the rule protects. This route's
// refusals carry a `title` in general and it is the most likely carrier of all.
//
// Attribute names: a `data-world` is invisible to a reader and obvious to a
// developer grepping for the feature it implies, which makes it exactly as retired
// as a label.
func assertNoRetiredEntityIsNamed(t auditFailer, audit *docAudit) {
	t.Helper()

	report := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range retiredEntities {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the "+
					"words appear nowhere in the interface", audit.where, where, retired)
			}
		}
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attribute := range node.Attr {
				report("the attribute "+attribute.Key, attribute.Val)
				report("an attribute name", attribute.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(audit.root)
}

// assertEveryReferenceResolves is the ARIA integrity rule §10.2's list implies and
// does not spell out: a document whose `aria-controls` points at nothing is a
// control claiming to control something invisible.
//
// All five reference attributes, plus `<label for>` and `href="#…"`. The id
// uniqueness half is not incidental: two elements sharing an id make every
// reference to it ambiguous, and a screen reader resolves the ambiguity by picking
// the first — so a control can end up controlling the wrong element with nothing in
// the markup to say so.
func assertEveryReferenceResolves(t auditFailer, audit *docAudit) {
	t.Helper()

	ids := map[string]int{}

	audit.elements(func(node *html.Node) {
		if id := attr(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times; every reference to it is ambiguous and "+
				"a screen reader resolves the ambiguity by picking the first (UI §7.2)",
				audit.where, id, count)
		}
	}

	references := []string{
		"aria-controls",
		"aria-labelledby",
		"aria-describedby",
		"aria-owns",
		"aria-activedescendant",
	}

	audit.elements(func(node *html.Node) {
		for _, name := range references {
			value := attr(node, name)
			if value == "" {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if byID(audit.root, id) == nil {
					t.Errorf("%s: %s carries %s=%q, which resolves to no element; a "+
						"control that claims to control something invisible is a control "+
						"that lies (UI §7.2)", audit.where, nodePath(node), name, id)
				}
			}
		}

		// §7.7: a real `<label for>`, never a placeholder. A `for` pointing at
		// nothing is a field with no accessible name.
		if node.Data == "label" {
			if target := attr(node, "for"); target != "" && byID(audit.root, target) == nil {
				t.Errorf("%s: %s has for=%q, which resolves to no element; the field "+
					"has no accessible name (UI §7.7)", audit.where, nodePath(node), target)
			}
		}

		if node.Data == "a" {
			if fragment, isFragment := strings.CutPrefix(attr(node, "href"), "#"); isFragment &&
				fragment != "" && byID(audit.root, fragment) == nil {
				t.Errorf("%s: %s has href=\"#%s\", which resolves to no element",
					audit.where, nodePath(node), fragment)
			}
		}
	})
}

// --- §10.6 -------------------------------------------------------------------

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6 and §7.3's "every
// interactive element … uses it", over every document this route composes.
//
// Its own top-level test rather than a ninth subtest of
// `TestEveryRouteSatisfiesTheStructuralContract`, because §10.6 is a different
// section of the record with its own floor and its own follow-up work, and a rule
// that fails is easier to route to a person when the failure line names the
// section it came from.
//
// The class is on the element and the minimum is in the stylesheet, which is why
// this is a structural rule and not a measurement: the markup says what the element
// is and `internal/web`'s `TestTheTargetMinimumsMeetTheSpecifiedFloors` asserts the
// sheet's side from the **built** stylesheet. Between them, §10.6's two halves are
// both held.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			assertEveryFocusStopCarriesTarget(t, audit)
		})
	}
}

// assertEveryFocusStopCarriesTarget is §10.6's "grep rendered markup for
// interactive elements missing the class".
//
// A **per-element** walk rather than a count, because a count cannot name the
// element and a failure message that does not name it is a failure a reader has to
// go and re-derive.
//
// No owner is named in the message, because this route composes no author markup:
// every focus stop in these documents is the shell's, so there is one owner and it
// is `internal/web`. The wiki route's copy of this rule names the second owner,
// because that route does put sanitised author HTML into a document.
func assertEveryFocusStopCarriesTarget(t auditFailer, audit *docAudit) {
	t.Helper()

	for _, node := range audit.focusStops() {
		if hasClass(node, "target") {
			continue
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §7.3 "+
			"enforces the --target-min minimum by construction and §10.6 audits for it",
			audit.where, nodePath(node))
	}
}

// --- The two responses that are not documents --------------------------------

// TestEveryRouteRefusesAnUnknownCampaignWithANonDocument is ADR 0024 from the
// audit's side, and the reason the document list above stops where it does.
//
// A slug naming no campaign is refused by `campaigns.RequireRead` before this
// route's handler runs, and the gate's answer is `{"error":"not found"}` as JSON.
// It is deliberately byte-identical to the answer for a private campaign the reader
// is not a member of, so the refusal cannot become an existence oracle.
//
// The consequence for §10.2 is that the body is **not a document**, and every
// §10.2 rule is a claim about a document's structure. Asserting that here rather
// than leaving it to inference is what stops the list of subjects from drifting:
// a future change that made this route answer an unknown campaign with a rendered
// shell would be a *good* change for §10.2 and a **security regression**, and this
// test is what says so.
func TestEveryRouteRefusesAnUnknownCampaignWithANonDocument(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	unknown := fixed.get("/c/no-such-campaign/assets/map.png", gmRequestor())
	privateCampaign := fixed.private().get("/c/greyhaven/assets/map.png", anonymousRequestor())

	for _, testCase := range []struct {
		where    string
		recorder *httptest.ResponseRecorder
	}{
		{"a campaign nobody registered", unknown},
		{"a private campaign the reader is not a member of", privateCampaign},
	} {
		t.Run(testCase.where, func(t *testing.T) {
			t.Parallel()

			if testCase.recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", testCase.recorder.Code)
			}

			if got := testCase.recorder.Header().Get("Content-Type"); got !=
				"application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want the gate's JSON; a rendered shell "+
					"here would still have to be byte-identical to the private "+
					"campaign's, and a difference between two refusals is an existence "+
					"oracle (ADR 0024)", got)
			}

			if strings.Contains(strings.ToLower(testCase.recorder.Body.String()), "<html") {
				t.Errorf("the gate's refusal is an HTML document, so §10.2's rules "+
					"would apply to it and this file's subject list is incomplete:\n%s",
					abbreviateBody(testCase.recorder.Body.String()))
			}
		})
	}
}

// TestEveryRouteServesNoAssetAsADocument is the other half of the subject list,
// and the reason a 200 is not in it.
//
// This route's success response is a file's bytes with a media type from a closed
// table, and §10.2's rules are about a document. So the list of §10.2 subjects
// skips the 200 — and a route that served an asset *as* `text/html` would then be
// examined by no audit at all, which is how "the audit found nothing" and "the
// audit looked at nothing" stay indistinguishable.
//
// So the skip is asserted rather than assumed, from the same closed table the
// handler uses: no extension this route will serve produces `text/html`, and no
// extension it will not serve produces a 200.
func TestEveryRouteServesNoAssetAsADocument(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())
	fixed.write("images/keep.png", mapBody())
	fixed.write("page.html", []byte("<html><body>a document</body></html>"))

	for _, testCase := range []struct {
		path string
	}{
		{"/c/greyhaven/assets/map.png"},
		{"/c/greyhaven/assets/images/keep.png"},
		// The refusals, which are documents. Asserted here as well because the
		// asymmetry is the property: the same URL prefix produces one served as an
		// image and one served as a document, and which is which is decided by the
		// extension table rather than by the prefix.
		{"/c/greyhaven/assets/page.html"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get(testCase.path, gmRequestor())
			contentType := recorder.Header().Get("Content-Type")

			switch {
			case strings.HasPrefix(testCase.path, "/c/greyhaven/assets/page.html"):
				if recorder.Code != http.StatusForbidden {
					t.Errorf("an .html file answered %d, want 403", recorder.Code)
				}

				if !strings.HasPrefix(contentType, "text/html") {
					t.Errorf("Content-Type = %q on a refusal, want the document type; "+
						"the refusal is a rendered shell and has to be served as one",
						contentType)
				}
			case strings.HasPrefix(contentType, "text/html"):
				t.Errorf("an asset was served as %q; §10.2's rules are about a "+
					"document, so an asset served as one would be a document no audit "+
					"looks at (ADR 0028: render output is permission-neutral)",
					contentType)
			default:
				if recorder.Code != http.StatusOK {
					t.Errorf("status = %d, want 200 for a servable extension", recorder.Code)
				}
			}
		})
	}
}

// --- The audit's own tests -----------------------------------------------------

// silentFailer records `Errorf` calls instead of reporting them.
//
// A `testing.T` whose `Errorf` is intercepted, so a rule can be *tested* rather
// than merely run. That is the only way to assert a rule fires, and asserting it
// is the only way to know it is a gate rather than a comment: three of phase 5's
// first-draft gate tests could not fail, and an audit that reports nothing is the
// same failure with a green light on top.
//
// The *messages* are kept as well as the count, because a rule that fires for the
// wrong reason is still a rule that appears to work: a landmark audit that also
// reported duplicate hooks would pass a fixture built to trip the landmark rule,
// and the gate would look covered while the thing it names was not being checked.
type silentFailer struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one document produces a count and a
// list rather than stopping at the first violation.
func (f *silentFailer) Errorf(format string, args ...any) {
	f.failures++
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
	f.Logf(format, args...)
}

// Helper satisfies the rules' `t.Helper()` calls without recording anything.
func (f *silentFailer) Helper() {}

// mentions reports whether any recorded message contains a fragment.
func (f *silentFailer) mentions(fragment string) bool {
	for _, message := range f.messages {
		if strings.Contains(message, fragment) {
			return true
		}
	}

	return false
}

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control: each
// §10.2 rule is run over a minimal document built to violate exactly that rule,
// and each must produce at least one finding **that mentions the thing it is
// about**.
//
// The minimal fixtures are the point. A document written to trip the `<h1>` count
// must violate nothing else, or the count of failures says nothing — an audit that
// fires on the whole fixture passes the "did it report anything" check while the
// rule under test never spoke. And the message check is what makes a rule's
// identity testable rather than its existence.
//
// Every fixture here violates one rule and is otherwise a document that satisfies
// the rest, so a rule that fires for an unrelated reason fails the
// `wantOne` check below rather than passing quietly.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		body    string
		check   func(auditFailer, *docAudit)
		wantOne string
	}{
		{
			name:    "a second h1",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><h1>Also page</h1></main></body></html>`,
			check:   assertExactlyOneH1,
			wantOne: "<h1>",
		},
		{
			name:    "no h1 at all",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1"><p>Page.</p></main></body></html>`,
			check:   assertExactlyOneH1,
			wantOne: "<h1>",
		},
		{
			name:    "a heading that skips a level",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><h3>Sub</h3></main></body></html>`,
			check:   assertHeadingLevelsNeverSkip,
			wantOne: "jumps from h1 to h3",
		},
		{
			name:    "an unnamed navigation landmark",
			body:    `<html><head><title>t</title></head><body><nav id="nav" tabindex="-1"><a href="/a">A</a></nav><main id="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check:   assertLandmarksArePresentAndDistinguishing,
			wantOne: "labelled by nothing",
		},
		{
			name:    "a complementary landmark mislabelled",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1></main><aside id="rail" aria-label="Extras" tabindex="-1"></aside></body></html>`,
			check:   assertLandmarksArePresentAndDistinguishing,
			wantOne: "complementary landmark is labelled",
		},
		{
			name:    "no main landmark",
			body:    `<html><head><title>t</title></head><body><h1>Page</h1></body></html>`,
			check:   assertLandmarksArePresentAndDistinguishing,
			wantOne: "no main landmark",
		},
		{
			name:    "a contentinfo inside main",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><footer role="contentinfo"></footer></main></body></html>`,
			check:   assertLandmarksArePresentAndDistinguishing,
			wantOne: "inside <main>",
		},
		{
			name:    "a positive tabindex",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="3"><h1>Page</h1></main></body></html>`,
			check:   assertNoPositiveTabindex,
			wantOne: "tabindex",
		},
		{
			// Padded and signed, which is what a value interpolated from a variable
			// looks like. `strconv.Atoi(strings.TrimSpace(" +3 "))` reads it as 3; a
			// string comparison against "0" does not, because "+3" sorts before "0".
			// The row exists so the numeric parse is load-bearing rather than a
			// stylistic preference — the failure it prevents is a browser disagreeing
			// with this gate about the tab order.
			name:    "a tabindex with a sign and padding",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex=" +3 " class="target"><h1>Page</h1></main></body></html>`,
			check:   assertNoPositiveTabindex,
			wantOne: "tabindex",
		},
		{
			// A zero tabindex is also a finding, and it is the one a string
			// comparison against "0" gets wrong in the other direction: `0` is
			// falsy in a language that will let you write it.
			name:    "a zero tabindex",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="0" class="target"><h1>Page</h1></main></body></html>`,
			check:   assertNoPositiveTabindex,
			wantOne: "only -1 is permitted",
		},
		{
			name:    "an inline outline suppression",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 style="outline: none">Page</h1></main></body></html>`,
			check:   assertNoInlineOutlineSuppression,
			wantOne: "outline: none",
		},
		{
			name:    "a focusable element that is aria-hidden",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a href="/a" aria-hidden="true">A</a></main></body></html>`,
			check:   assertNoAriaHiddenOnAFocusStop,
			wantOne: "focusable and aria-hidden",
		},
		{
			name:    "a focusable element inside an aria-hidden subtree",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div aria-hidden="true"><a href="/a">A</a></div></main></body></html>`,
			check:   assertNoAriaHiddenOnAFocusStop,
			wantOne: "contains focusable elements",
		},
		{
			name:    "a skip link after the banner",
			body:    `<html><head><title>t</title></head><body><header role="banner"><a href="/a" class="target">A</a></header><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check:   assertSkipLinksComeFirstAndResolve,
			wantOne: "skip links first in tab order",
		},
		{
			name:    "a skip link to an absent landmark",
			body:    `<html><head><title>t</title></head><body><a class="skip-link target" href="#nowhere">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check:   assertSkipLinksComeFirstAndResolve,
			wantOne: "not in the document",
		},
		{
			// §4.6's pairing, and the direction a rendering cannot show: a
			// navigation landmark with no skip link to it.
			name:    "a navigation landmark with no skip link",
			body:    `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><nav id="nav" role="navigation" aria-label="Campaign" tabindex="-1"></nav><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check:   assertSkipLinksComeFirstAndResolve,
			wantOne: "no skip link to",
		},
		{
			name:    "a repeated data-testid",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Page</h1><p data-testid="page-title">x</p></main></body></html>`,
			check:   assertEveryTestIDIsPresent,
			wantOne: "more than once",
		},
		{
			name:    "a promise this route makes and did not keep",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1"><p>Page.</p></main></body></html>`,
			check:   assertEveryTestIDIsPresent,
			wantOne: `no "page-title" hook`,
		},
		{
			name:    "both a body and a failure state",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Page</h1><div data-testid="page-body"></div><section data-testid="error-state"></section></main></body></html>`,
			check:   assertEveryTestIDIsPresent,
			wantOne: "want exactly 1 between them",
		},
		{
			name:    "the word in a text node",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p>the World of this account</p></main></body></html>`,
			check:   assertNoRetiredEntityIsNamed,
			wantOne: "a text node",
		},
		{
			// The case a rendered-document reader skips: a reader never sees a
			// comment, which is precisely why it must be excluded rather than
			// scanned.
			name:    "the word in an HTML comment",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><!-- the world of this account --></main></body></html>`,
			check:   assertNoRetiredEntityIsNamed,
			wantOne: "an HTML comment",
		},
		{
			name:    "the word in an aria-label",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><nav aria-label="game sessions"></nav></main></body></html>`,
			check:   assertNoRetiredEntityIsNamed,
			wantOne: "the attribute aria-label",
		},
		{
			name:    "the word in a title attribute",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><span title="Your session">x</span></main></body></html>`,
			check:   assertNoRetiredEntityIsNamed,
			wantOne: "the attribute title",
		},
		{
			// The nastiest one, because it is the one a scan over attribute
			// *values* would miss and a scan over the raw markup would find: the
			// word is in the *name* of an attribute nothing renders.
			name:    "the word in an attribute name",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div data-world="true"></div></main></body></html>`,
			check:   assertNoRetiredEntityIsNamed,
			wantOne: "an attribute name",
		},
		{
			name:    "a focus stop with no target class",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a class="link" href="/a">A</a></main></body></html>`,
			check:   assertEveryFocusStopCarriesTarget,
			wantOne: ".target",
		},
		{
			// A `<button>` rather than an anchor, so the element type is the variable
			// and an audit that only walked `a[href]` would pass this row.
			name:    "a button with no target class",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><button type="button">Go</button></main></body></html>`,
			check:   assertEveryFocusStopCarriesTarget,
			wantOne: ".target",
		},
		{
			// The tabindex branch, which is the one a `a[href]`-shaped audit never
			// reaches.
			name:    "an element with a tabindex and no target class",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div class="region" tabindex="0">R</div></main></body></html>`,
			check:   assertEveryFocusStopCarriesTarget,
			wantOne: ".target",
		},
		{
			name:    "a reference that resolves to nothing",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><button aria-controls="nowhere">Go</button></main></body></html>`,
			check:   assertEveryReferenceResolves,
			wantOne: "resolves to no element",
		},
		{
			name:    "a label for nothing",
			body:    `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><label for="nowhere">Q</label></main></body></html>`,
			check:   assertEveryReferenceResolves,
			wantOne: "no accessible name",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}
			root := parseFixture(t, testCase.body)

			testCase.check(
				counter,
				&docAudit{t: counter, where: "fixture", status: 200, root: root},
			)

			if counter.failures == 0 {
				t.Fatalf("the rule reported nothing for %s; a rule that cannot fail is "+
					"a gate wired to nothing (UI §10.2)", testCase.name)
			}

			// And it fired for the *right* reason. A count alone cannot tell which
			// rule spoke, and an audit that fires for an unrelated reason looks
			// exactly like one that works.
			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the rule reported %d failures for %s but none of them "+
					"mentions %q, so the rule that fired was not the rule under test; "+
					"messages:\n  %s",
					counter.failures, testCase.name, testCase.wantOne,
					strings.Join(counter.messages, "\n  "))
			}
		})
	}
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the negative control's
// other direction, and a separate test rather than a flag on the table above
// because the two are about opposite failure modes — a row that said the wrong
// thing would otherwise look like a row written wrong.
//
// Each fixture is the *nearest* document to a violation that is not one. An audit
// that walked every element for `.target`, or rejected every `tabindex` for being
// non-zero, or scanned the raw bytes for the retired words, would report these — and
// would be enforcing rules the record does not have. §10.6 is about *interactive*
// elements and an anchor with no `href` is not one; `-1` is what §7.2 asks for;
// and "Worldly" is caught while `World` inside `Worlds`… is the same finding by
// design, so the vocabulary rows state that knowingly.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
		// wantFindings is false for every row but one, and the field is here so the
		// exception is visible in the table rather than encoded in a predicate
		// keyed on the row's name — which is the shape a "fix the test" edit takes.
		wantFindings bool
		check        func(auditFailer, *docAudit)
	}{
		{
			name:  "a paragraph is not interactive",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Page</h1><div data-testid="page-body"><p>Prose.</p></div></main></body></html>`,
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			name:  "an anchor with no href is not a link",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Page</h1><div data-testid="page-body"><a id="anchor">Not a link</a></div></main></body></html>`,
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			// The other direction: a class on a non-interactive element is not a
			// violation either, and §4.11.1 makes carrying `.target` a *plugin's*
			// obligation rather than something the shell refuses to share.
			name:  "a target class on a non-interactive element",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Page</h1><div data-testid="page-body"><p class="target">Prose.</p></div></main></body></html>`,
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			name:  "a negative tabindex is what §7.2 asks for",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: assertNoPositiveTabindex,
		},
		{
			// The padded counterpart of the row above: `Atoi(strings.TrimSpace(" -1 "))`
			// is -1, so a value a template interpolated with spaces around it is
			// still the negative one §7.2 asks for and is not a finding.
			name:  "a padded negative tabindex",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex=" -1 " class="target"><h1>Page</h1></main></body></html>`,
			check: assertNoPositiveTabindex,
		},
		{
			name:  "an inline outline that suppresses nothing",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 style="outline-offset: 2px; outline-colour: red">Page</h1></main></body></html>`,
			check: assertNoInlineOutlineSuppression,
		},
		{
			// The legitimate `aria-hidden`: an icon inside a focusable link. §7.10
			// prohibits the attribute on something a keyboard can reach, not on a
			// decorative child of one.
			name:  "aria-hidden on a decorative icon inside a link",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a class="target" href="/a">A <svg aria-hidden="true"></svg></a></main></body></html>`,
			check: assertNoAriaHiddenOnAFocusStop,
		},
		{
			name:  "a resolved in-page reference",
			body:  `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><a class="target" href="#note">A note</a><p id="note">Note.</p></main></body></html>`,
			check: assertEveryReferenceResolves,
		},
		{
			name:  "an id used once",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><label for="q">Query</label><input id="q" type="search" class="target"/></main></body></html>`,
			check: assertEveryReferenceResolves,
		},
		{
			// A word that merely *contains* nothing retired. The row is here to say
			// that knowingly: §1.2's rule is a case-insensitive substring, so
			// "Worldly" and "worlds" are findings, and a reader who later adds a word
			// boundary is changing the rule rather than fixing an audit.
			name:         "the retired word inside a longer word is still a finding",
			body:         `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p>Worldly, worlds, sessions.</p></main></body></html>`,
			wantFindings: true,
			check:        assertNoRetiredEntityIsNamed,
		},
		{
			name:  "an element with no hook at all",
			body:  `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target" data-testid="shell-main"><h1 data-testid="page-title">Page</h1><div data-testid="page-body"><p>Prose.</p><span>More.</span></div></main><aside id="rail" role="complementary" aria-label="Utilities" tabindex="-1" class="target" data-testid="shell-rail"></aside></body></html>`,
			check: assertEveryTestIDIsPresent,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}
			root := parseFixture(t, testCase.body)

			testCase.check(
				counter,
				&docAudit{t: counter, where: "fixture", status: 200, root: root},
			)

			switch {
			case counter.failures == 0 && testCase.wantFindings:
				t.Errorf("the rule reported nothing for %s, want a finding", testCase.name)
			case counter.failures != 0 && !testCase.wantFindings:
				t.Errorf("the rule reported %d findings for %s, want none: %s",
					counter.failures, testCase.name, strings.Join(counter.messages, "; "))
			}
		})
	}
}

// TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe is the third control:
// each rule must also be capable of reporting **nothing**, on a document that
// satisfies it.
//
// An audit that fails on everything is not a gate either — it is a gate wired to
// the wrong end, and the way that is discovered is a red suite in the first week,
// after which the audit gets switched off rather than fixed. The document is the
// smallest one this route's states actually render: one skip link, the centre with
// its heading and its load-error state, and the rail.
func TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	const correct = `<!doctype html>
<html lang="en"><head><title>Asset — Greyhaven</title></head><body>
<a class="skip-link target" href="#main" data-testid="skip-to-content">Skip to content</a>
<a class="skip-link target" href="#rail" data-testid="skip-to-utilities">Skip to utilities</a>
<div class="shell shell--pre-campaign" data-testid="shell">
<header class="shell-header" role="banner" data-testid="shell-header">
<a class="shell-brand target" href="/" data-testid="header-home">Greyhaven</a>
<button type="button" class="target header-theme" aria-pressed="false" data-testid="header-theme">Theme: auto</button>
<div class="shell-account" data-testid="header-account"><span class="shell-account-name">mira</span>
<form method="post" action="/logout"><button type="submit" class="target" data-testid="header-sign-out">Sign out</button></form>
</div></header>
<main id="main" class="shell-main target" role="main" tabindex="-1" data-testid="shell-main">
<h1 id="page-heading" data-testid="page-title">Asset</h1>
<section class="empty" aria-labelledby="error-state-heading" data-testid="error-state">
<h2 id="error-state-heading">This page could not be loaded</h2>
<p data-testid="error-state-message">semiplane could not load this page. Nothing has been changed.</p>
<p class="empty-note">Reference: <code data-testid="error-state-reference">req-1</code></p>
</section></main>
<aside id="rail" class="shell-rail target" role="complementary" aria-label="Utilities" tabindex="-1" data-shell-rail data-testid="shell-rail">
<div class="rail-panels" data-testid="rail-panels">
<section class="panel" aria-labelledby="instance-rail-heading" data-testid="rail-instance">
<h2 id="instance-rail-heading">Instance</h2>
<p class="panel-line" data-testid="rail-instance-name">Greyhaven</p>
</section></div></aside>
<footer class="shell-footer" role="contentinfo" data-testid="shell-footer">
<span class="shell-footer-item" data-testid="footer-version">Version 0.3.0</span>
</footer></div></body></html>`

	for _, testCase := range []struct {
		name  string
		check func(auditFailer, *docAudit)
	}{
		{name: "one h1", check: assertExactlyOneH1},
		{name: "heading levels", check: assertHeadingLevelsNeverSkip},
		{name: "landmarks", check: assertLandmarksArePresentAndDistinguishing},
		{name: "tabindex", check: assertNoPositiveTabindex},
		{name: "inline outline", check: assertNoInlineOutlineSuppression},
		{name: "aria-hidden", check: assertNoAriaHiddenOnAFocusStop},
		{name: "skip links", check: assertSkipLinksComeFirstAndResolve},
		{name: "test hooks", check: assertEveryTestIDIsPresent},
		{name: "vocabulary", check: assertNoRetiredEntityIsNamed},
		{name: "references", check: assertEveryReferenceResolves},
		{name: "target class", check: assertEveryFocusStopCarriesTarget},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}
			root := parseFixture(t, correct)

			testCase.check(
				counter,
				&docAudit{t: counter, where: "fixture", status: 404, root: root},
			)

			if counter.failures != 0 {
				t.Errorf("the rule reported %d findings on a document this route "+
					"could actually serve, so it is too eager and will be switched off "+
					"rather than fixed: %s",
					counter.failures, strings.Join(counter.messages, "; "))
			}
		})
	}
}
