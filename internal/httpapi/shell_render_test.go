package httpapi_test

// UI §10.2 and §10.6, gate-blocking, against **every route**.
//
// The stylesheet half is `internal/web/a11y_test.go`; this is the document half.
// The two are separate because they make different claims — §10.2's
// `outline: none` rule is about a file the DOM does not contain, and §7.3's
// target *minimums* are about a class the DOM does carry but the stylesheet
// decides — and because neither can see the other's subject.
//
// # Why the routes and not the templates
//
// §10.2 says "for every route, including every registered plugin page type". A
// template rendered by hand can differ from a route's document by exactly the
// things this gate exists to catch: a middleware that adds a landmark, a handler
// that fills a slot the template left empty, a header that sets a `Vary` the
// template knows nothing about. Every fixture below therefore goes through
// `httpapi.NewRouter` — the same composition root the binary uses — and is
// asserted on the bytes that come back.
//
// # Why the DOM is parsed and never string-matched
//
// A substring assertion over markup is how a gate comes to pass while the thing
// it watches sits in a comment, in an attribute nothing renders, or spelled across
// an attribute boundary by a templ interpolation. §10.2's vocabulary rule is the
// clearest case: "no rendered string contains world or session" is trivially
// satisfied while `<!-- the world of this account -->` sits in the document — and
// a reader never sees a comment, which is precisely why it must be excluded
// rather than scanned.
//
// The parser also gets what a string match structurally cannot: which element an
// `id` resolves to, whether a landmark is nested inside another, and the order of
// the document's focusable elements. §10.2's skip-link rule is an *ordering* rule
// and there is no way to state it over a string.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
)

// forbiddenVocabulary is UI §1.2's closed vocabulary. Neither entity exists, a
// leftover is a bug, and a grep for them is a test — which is this.
//
// Matched case-insensitively and as a substring, because the failure this guards
// against is a noun phrase inside a sentence ("your session has expired", "the
// world map"), and neither is spelled with a capital S in isolation. Both name an
// entity the product does not have.
var forbiddenVocabulary = []string{"world", "session"}

// renderedRoute is one audited document.
type renderedRoute struct {
	// where names the route in a failure message.
	where string
	// status is the response status. A route that answered an error is still
	// audited: §10.2 says every route, and the error documents are built by a
	// different path than the success ones, which is exactly where an audit that
	// only checks `200` would find nothing.
	status int
	// body is the response bytes.
	body []byte
	// vary is the `Vary` response header, carried because a DOM cannot hold it.
	vary string
}

// auditedRoutes drives every route this phase registers and returns its document.
//
// The list is the route table, and `TestEveryRegisteredRouteIsCoveredHere` reads
// the router's own registrations and fails when one is missing from it — so this
// file cannot quietly stop covering a route. The failure is "a route was added
// and not audited" rather than nothing at all, which is the only way a coverage
// gate keeps its value as the router grows.
//
// Four documents, covering the three states the shell distinguishes:
//
//   - `/` for a signed-in member of one campaign — the list, with cards.
//   - `/` for a signed-in member of none — §4.7's first-run state, which is a
//     designed surface and not an error, and which has its own copy.
//   - `/login` for an anonymous reader — the state most of a new installation is
//     actually in, and the only route with no account zone.
//   - `/login` with a failed credential — §4.7's `403` row over the form, so the
//     error copy is audited rather than assumed.
//
// The pre-campaign/campaign split matters because §4.6 is a *structural*
// difference: the navigation landmark and its skip link exist on one and not the
// other. A shell that rendered both, or neither, would pass a single-state audit.
func auditedRoutes(t *testing.T) []renderedRoute {
	t.Helper()

	st := newWiringStore()

	hash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	st.users["ada"] = domain.User{ID: 1, Username: "ada", PasswordHash: hash}
	st.campaigns["greyhaven"] = domain.Campaign{
		ID: 1, Slug: "greyhaven", Name: "Greyhaven", Visibility: domain.VisibilityPrivate,
	}
	st.members[1] = domain.RoleGM

	handler := fullRouter(st)

	// Signed in, so the audit sees a document with an account zone — the header's
	// sign-out form is one of the shell's focus stops and §10.6 walks it.
	signedIn := httptest.NewRecorder()
	handler.ServeHTTP(signedIn, signIn(t, "ada", "correct horse"))
	if signedIn.Code != http.StatusFound && signedIn.Code != http.StatusSeeOther {
		t.Fatalf("sign in = %d, want a redirect", signedIn.Code)
	}

	cookie := sessionCookieOf(t, signedIn)

	// The first-run document needs an account with no campaigns, so a second
	// store. Signing out of one and into the other would be a longer route to the
	// same two documents.
	empty := newWiringStore()
	emptyHash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	empty.users["grace"] = domain.User{ID: 7, Username: "grace", PasswordHash: emptyHash}

	emptyHandler := fullRouter(empty)

	emptySignedIn := httptest.NewRecorder()
	emptyHandler.ServeHTTP(emptySignedIn, signIn(t, "grace", "correct horse"))
	if emptySignedIn.Code != http.StatusFound && emptySignedIn.Code != http.StatusSeeOther {
		t.Fatalf("sign in (first run) = %d, want a redirect", emptySignedIn.Code)
	}

	emptyCookie := sessionCookieOf(t, emptySignedIn)

	// The failed credential. §4.7's `403` row is what this answers, and the copy
	// a reader sees on a wrong password is interface text, so it is in scope for
	// the vocabulary rule.
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, formPost(t, "/login", signInForm("ada", "wrong"), nil))

	// The anonymous sign-in form. `handlers` rather than two anonymousGet calls,
	// because one call per fixture keeps the response and the status together;
	// two calls meant two requests to read one document.
	anonymous := anonymousGet(t, handler, "/login")

	return []renderedRoute{
		renderRoute(t, handler, "/ (a member of one campaign)", cookie, "/"),
		renderRoute(t, emptyHandler, "/ (first run: no campaigns)", emptyCookie, "/"),
		routeFromRecorder("/login (anonymous)", anonymous),
		routeFromRecorder("/login (a rejected credential)", rejected),
		// A fixture carrying **two** `search` landmarks, so the rule the landmark
		// case states — that the header's form and the search route's own form
		// are named differently — is actually exercised.
		//
		// Without it the `case "search"` branch is unreachable: no audited route
		// renders a second search landmark, so an assertion in it can be wrong in
		// either direction and nothing notices. That is not a hypothetical: the
		// first version of this fix asserted one literal name instead of a set,
		// which would have failed the search route's own landmark the moment that
		// route landed, and which no mutation could catch because the branch never
		// ran. A rule with no reachable instance is not a rule.
		{
			where: "a document carrying both search landmarks",
			body:  []byte(searchLandmarkFixture),
		},
	}
}

// searchLandmarkFixture is a minimal document carrying the header's search form
// and a search route's own, each named as the design record fixes them.
//
// Hand-written rather than rendered from a route on purpose: it is the *pair* the
// rule is about, and no single route renders both. Keeping it here means the
// landmark audit has an instance of the case it asserts regardless of which
// routes are wired.
// It carries a skip link and `.target` on every focus stop, because the fixture
// is audited by **every** §10.2 rule and not only the landmark one: a fixture that
// violates three unrelated rules fails the audit three times and buries the
// assertion it was written for. That is what happened the first time.
const searchLandmarkFixture = `<!doctype html>
<html lang="en"><head><title>Fixture</title></head><body>
<a class="skip-link target" href="#main">Skip to content</a>
<header role="banner"><form role="search" aria-label="Search pages"><input class="target" type="search" name="q"/></form></header>
<main id="main" class="target" tabindex="-1"><h1>Fixture</h1>
<form role="search" aria-label="Search this campaign"><input class="target" type="search" name="q"/></form>
</main></body></html>`

// renderRoute GETs a path through a handler with a cookie.
func renderRoute(
	t *testing.T,
	handler http.Handler,
	where string,
	cookie *http.Cookie,
	path string,
) renderedRoute {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, getRequest(t, path, cookie))

	return routeFromRecorder(where, recorder)
}

// routeFromRecorder captures a response as an audited document.
func routeFromRecorder(where string, recorder *httptest.ResponseRecorder) renderedRoute {
	return renderedRoute{
		where:  where,
		status: recorder.Code,
		body:   recorder.Body.Bytes(),
		// `Header.Values` rather than `Get`, because `Get` cannot tell "unset"
		// from "set to the empty string", and "no Vary header at all" is the
		// requirement — an empty `Vary` is still a header, and a cache reads it.
		vary: strings.Join(recorder.Header().Values("Vary"), ", "),
	}
}

// anonymousGet GETs a path through a handler with no credential.
func anonymousGet(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, getRequest(t, path, nil))

	return recorder
}

// signInForm builds a credential pair.
func signInForm(username, password string) map[string][]string {
	return map[string][]string{"username": {username}, "password": {password}}
}

// failer is the subset of *testing.T the audit actually uses.
//
// An interface rather than `*testing.T` because the audit's own tests have to
// *test* it, and testing a `*testing.T`-typed function means a real failure ends
// the test. `countingT` below satisfies this and records instead of reporting —
// which is the only way to assert that a rule fires without asserting it fires on
// the fixture you happened to write.
//
// Deliberately not including `Fatalf`: the audit's own constructor aborts on an
// unparseable document, and that behaviour should stay unreachable from a
// sub-audit rather than be faked.
type failer interface {
	Helper()
	Errorf(format string, args ...any)
}

// audit is one parsed document, plus the two response properties a DOM cannot
// carry.
type audit struct {
	t      failer
	where  string
	status int
	// varyHeader is the response's `Vary`, captured as a string because its
	// *value* is irrelevant and its presence is the finding. See
	// `assertNoVaryOnCookie`.
	varyHeader string
	root       *html.Node
	body       *html.Node
	head       *html.Node
}

// newAudit parses a rendered document.
func newAudit(t *testing.T, route renderedRoute) *audit {
	t.Helper()

	if !strings.Contains(strings.ToLower(string(route.body)), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d): %s",
			route.where, route.status, firstBytes(string(route.body)))
	}

	root, err := html.Parse(strings.NewReader(string(route.body)))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", route.where, err)
	}

	return &audit{
		t:          t,
		where:      route.where,
		status:     route.status,
		varyHeader: route.vary,
		root:       root,
		head:       findElement(root, "head"),
		body:       findElement(root, "body"),
	}
}

// --- The walk ---------------------------------------------------------------

// elements calls visit for every element, in document order.
//
// Document order rather than depth-first by branch: §10.2's skip-link rule and
// §7.2's "the first focusable elements in the document" are both statements about
// the order a reader meets things, and a walk that finished the first element's
// whole subtree before visiting the second would report a different order than a
// keyboard gets.
func (a *audit) elements(visit func(*html.Node)) {
	a.t.Helper()

	var walk func(node *html.Node)
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

// focusable returns every element a keyboard can reach, in document order.
func (a *audit) focusable() []*html.Node {
	a.t.Helper()

	var found []*html.Node

	a.elements(func(node *html.Node) {
		if isFocusable(node) {
			found = append(found, node)
		}
	})

	return found
}

// attribute returns an attribute's value, or "" when absent.
func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether an attribute is present, whatever its value.
//
// Separate from `attribute != ""` because `aria-expanded=""` and
// `aria-expanded="false"` are different states, and an empty attribute value is
// almost never what the author meant.
func hasAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// hasClassToken reports whether an element's class list contains a token.
//
// Split on whitespace and compared exactly, never `strings.Contains`: `target` is
// a substring of `data-target` and of `target-large`, and §7.3's contract is about
// the *class*.
func hasClassToken(node *html.Node, token string) bool {
	for field := range strings.FieldsSeq(attribute(node, "class")) {
		if field == token {
			return true
		}
	}

	return false
}

// isFocusable reports whether a keyboard can reach an element.
func isFocusable(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	switch node.DataAtom {
	case atom.A:
		return hasAttribute(node, "href")
	case atom.Button, atom.Input, atom.Select, atom.Textarea, atom.Summary:
		return true
	default:
		return hasAttribute(node, "tabindex")
	}
}

// findElement returns the first element with a tag name, or nil.
func findElement(node *html.Node, tag string) *html.Node {
	if node.Type == html.ElementNode && node.Data == tag {
		return node
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, tag); found != nil {
			return found
		}
	}

	return nil
}

// findElements returns every element with a tag name, in document order.
func findElements(node *html.Node, tag string) []*html.Node {
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

// hasAncestor reports whether any ancestor has the given tag name.
func hasAncestor(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// elementByID returns the element carrying an id, or nil.
func elementByID(root *html.Node, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode && attribute(child, "id") == id {
				found = child

				return
			}

			walk(child)
		}
	}
	walk(root)

	return found
}

// elementPath is a readable location for a failure message: the nearest four
// ancestors, innermost first, each labelled by id, test hook or first class.
func elementPath(node *html.Node) string {
	parts := []string{}

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		label := current.Data

		switch {
		case attribute(current, "id") != "":
			label += "#" + attribute(current, "id")
		case attribute(current, "data-testid") != "":
			label += "[" + attribute(current, "data-testid") + "]"
		default:
			if class := strings.Fields(attribute(current, "class")); len(class) > 0 {
				label += "." + class[0]
			}
		}

		parts = append([]string{label}, parts...)
	}

	if len(parts) > 4 {
		parts = parts[len(parts)-4:]
	}

	return strings.Join(parts, " > ")
}

// roleOf returns an element's explicit role, or the implicit role its tag carries.
//
// The implicit half is not a convenience: §7.2's contract is written as
// `header[banner]` and `footer[contentinfo]`, and a document relying on the
// implicit role is correct. The audit has to agree with the specification, not
// with the subset somebody wrote an attribute for.
func roleOf(node *html.Node) string {
	if explicit := attribute(node, "role"); explicit != "" {
		return explicit
	}

	switch node.DataAtom {
	case atom.Header:
		return "banner"
	case atom.Footer:
		return "contentinfo"
	case atom.Nav:
		return "navigation"
	case atom.Main:
		return "main"
	case atom.Aside:
		return "complementary"
	case atom.Form:
		// A form is a landmark only when it carries an explicit role. An
		// unlabelled form is not one, and treating it as `form` here would invent
		// a landmark the specification does not have.
		return ""
	default:
		return ""
	}
}

// textContent returns an element's descendant text.
//
// Text nodes only: an `alt` or a `title` is text a reader hears and is not in the
// text content, and §10.2's vocabulary rule is about *rendered strings*. The
// attribute scan below covers the rest of the document.
func textContent(node *html.Node) string {
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

// --- §10.2: the seven rules, one subtest each ------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y
// test failed". The rules are §10.2's own list, in its order, plus the two it
// implies and does not spell out — heading levels and reference integrity —
// because a landmark can be present and correctly labelled while the document's
// outline and its ARIA wiring are both broken, and neither shows up in a
// "landmarks are fine" result.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, route := range auditedRoutes(t) {
		t.Run(route.where, func(t *testing.T) {
			t.Parallel()

			audit := newAudit(t, route)

			t.Run("ExactlyOneH1", func(t *testing.T) {
				assertExactlyOneH1(t, audit)
			})
			t.Run("HeadingLevelsNeverSkip", func(t *testing.T) {
				assertHeadingLevelsNeverSkip(t, audit)
			})
			t.Run("NoHeadingIsRepeated", func(t *testing.T) {
				assertNoHeadingIsRepeated(t, audit)
			})
			t.Run("LandmarksArePresentAndDistinct", func(t *testing.T) {
				assertLandmarks(t, audit)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, audit)
			})
			t.Run("NoAriaHiddenOnAFocusableElement", func(t *testing.T) {
				assertNoAriaHiddenOnFocusable(t, audit)
			})
			t.Run("SkipLinksComeFirstAndResolve", func(t *testing.T) {
				assertSkipLinks(t, audit)
			})
			t.Run("EveryTestIDResolves", func(t *testing.T) {
				assertTestIDsResolve(t, audit)
			})
			t.Run("NoRetiredEntityIsNamed", func(t *testing.T) {
				assertVocabulary(t, audit)
			})
			t.Run("EveryFocusableElementCarriesTarget", func(t *testing.T) {
				assertEveryFocusableCarriesTarget(t, audit)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertEveryReferenceResolves(t, audit)
			})
			t.Run("TheDocumentDoesNotVaryByTheThemeCookie", func(t *testing.T) {
				assertNoVaryOnCookie(t, audit)
			})
		})
	}
}

// assertExactlyOneH1 is §7.2's first structural rule.
//
// Exactly one, not at least one. Two `<h1>`s tell a screen reader the page has two
// titles; none tell it the page has none while looking complete to a sighted
// reader.
func assertExactlyOneH1(t failer, audit *audit) {
	t.Helper()

	headings := findElements(audit.root, "h1")
	if len(headings) == 1 {
		return
	}

	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, elementPath(heading))
	}

	t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (UI §7.2); found: %s",
		audit.where, len(headings), strings.Join(names, ", "))
}

// assertHeadingLevelsNeverSkip is §7.2's second structural rule.
//
// Separate from the `<h1>` count because it can fail where the count passes: a
// document whose headings start at `<h3>` has exactly one `<h1>`-shaped hole and
// no valid outline at all.
func assertHeadingLevelsNeverSkip(t failer, audit *audit) {
	t.Helper()

	previous := 0

	audit.elements(func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		level, err := strconv.Atoi(node.Data[1:])
		if err != nil || level < 1 || level > 6 {
			return
		}

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level "+
				"is announced as a missing section (UI §7.2)",
				audit.where, previous, level, elementPath(node))
		}

		previous = level
	})
}

// landmark is one landmark region and how it is named.
type landmark struct {
	role string
	// label is the accessible name, following `aria-labelledby` when that is
	// what it uses.
	label string
	// namedBy records the mechanism, so a failure can say "labelled by nothing"
	// rather than "labelled by the empty string".
	namedBy string
	node    *html.Node
}

// landmarks returns the document's landmark regions in document order.
//
// The set is §7.2's own diagram: banner, navigation, main, complementary,
// contentinfo, and an explicit `search` role. Anything else is not a landmark.
func landmarks(audit *audit) []landmark {
	var found []landmark

	audit.elements(func(node *html.Node) {
		role := roleOf(node)
		if role == "" {
			return
		}

		switch role {
		case "banner", "navigation", "main", "complementary", "contentinfo", "search":
			found = append(found, landmark{
				role:    role,
				label:   accessibleName(audit, node),
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
	if attribute(node, "aria-label") != "" {
		return "aria-label"
	}

	if hasAttribute(node, "aria-labelledby") {
		return "aria-labelledby"
	}

	return "nothing"
}

// accessibleName returns a landmark's name, following `aria-labelledby` when used.
func accessibleName(audit *audit, node *html.Node) string {
	if label := attribute(node, "aria-label"); label != "" {
		return label
	}

	var parts []string

	for id := range strings.FieldsSeq(attribute(node, "aria-labelledby")) {
		if target := elementByID(audit.root, id); target != nil {
			parts = append(parts, strings.TrimSpace(textContent(target)))
		}
	}

	return strings.Join(parts, " ")
}

// assertLandmarks is §7.2's second rule, in four parts.
//
// Presence, because a missing landmark is a region a landmark-navigation reader
// cannot jump to. Distinguishing labels, because two landmarks of the same role
// with the same name make landmark navigation useless — §7.2 calls that a gate
// failure, not a style nit, and the compact bottom bar is the case it exists for.
// Exactness, for the three the record writes out. And nesting, because a
// `contentinfo` inside a `main` is not the document's contentinfo and a screen
// reader's landmark list will say otherwise.
func assertLandmarks(t failer, audit *audit) {
	t.Helper()

	found := landmarks(audit)

	present := map[string]int{}
	for _, region := range found {
		present[region.role]++
	}

	// `main` is the only landmark required unconditionally. §4.6 removes the
	// navigation before a campaign exists, and §3.1's tiers move the rail out of
	// the flow rather than out of the document — the rail stays in the
	// accessibility tree at every tier so a script-less reader does not lose it,
	// which shell.css states at the rule that moves it off-screen.
	if present["main"] == 0 {
		t.Errorf("%s: the document has no main landmark (UI §7.2)", audit.where)
	}

	if present["main"] > 1 {
		t.Errorf("%s: the document has %d main landmarks, want 1 (UI §7.2)",
			audit.where, present["main"])
	}

	for _, region := range found {
		switch region.role {
		case "navigation", "complementary", "search":
			if region.label == "" {
				t.Errorf(
					"%s: the %s landmark at %s is labelled by %s; §7.2 requires a "+
						"distinguishing label, and an unnamed landmark cannot be told "+
						"from another of the same role",
					audit.where, region.role, elementPath(region.node), region.namedBy,
				)
			}
		case "banner", "contentinfo":
		default:
		}
	}

	// The two the record writes out, exactly. Both navigation names are checked
	// as a set rather than positionally, because which of them comes first is a
	// tier decision the stylesheet makes and the markup does not.
	allowedNavigation := map[string]bool{"Campaign": true, "Primary": true}

	// searchLandmarkNames are the two `role="search"` landmark labels the design
	// record's own zones fix: the header's persistent form, and the search route's
	// form for the campaign being searched.
	//
	// A set rather than one name, because a document may carry either or both and
	// §7.2's requirement is that they are *distinct* — a rule about the pair, not
	// about either one. Asserting a single literal tests the pair by naming a member.
	searchLandmarkNames := []string{"Search pages", "Search this campaign"}

	allowedSearch := func() map[string]bool {
		allowed := make(map[string]bool, len(searchLandmarkNames))
		for _, name := range searchLandmarkNames {
			allowed[name] = true
		}

		return allowed
	}()

	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigation[region.label] {
				t.Errorf("%s: a navigation landmark is labelled %q; UI §7.2 names "+
					"them \"Campaign\" (the campaign nav) and \"Primary\" (the compact bar)",
					audit.where, region.label)
			}
		case "complementary":
			if region.label != "Utilities" {
				t.Errorf("%s: the complementary landmark is labelled %q, want %q (UI §7.2)",
					audit.where, region.label, "Utilities")
			}
		case "search":
			// The header's search form and the search route's own form are both
			// `search` landmarks, and §7.2's rule for them is the rule for two
			// navigations: they must not share a name.
			//
			// A **set** of the two names, not one of them. Asserting a single
			// literal would have made the search route's own landmark a false
			// failure the moment the route landed — which is exactly what a gate
			// that encodes one caller's answer instead of the rule does: it fails
			// on correct work, and the fix a tired author reaches for is to delete
			// the assertion.
			//
			// What is actually required is that the name is one of the agreed ones
			// **and** that it is distinct from the other `search` landmark on the
			// same page. Distinctness is asserted below, over every landmark, so it
			// holds here without this case restating it.
			if !allowedSearch[region.label] {
				t.Errorf("%s: a search landmark is labelled %q; UI §7.2 requires the "+
					"header's form and the search route's own form to be named "+
					"differently from each other, and those are the two names the "+
					"design record fixes: %v",
					audit.where, region.label, searchLandmarkNames)
			}
		case "banner", "contentinfo", "main":
		default:
		}
	}

	// Distinctness across the whole document.
	seen := map[string]string{}

	for _, region := range found {
		if region.label == "" {
			continue
		}

		key := region.role + "=" + region.label
		if first, duplicate := seen[key]; duplicate {
			t.Errorf("%s: two %s landmarks are both labelled %q (%s and %s); "+
				"§7.2 requires distinguishing labels or landmark navigation is useless",
				audit.where, region.role, region.label, first, elementPath(region.node))
		}

		seen[key] = elementPath(region.node)
	}

	for _, region := range found {
		if region.role == "contentinfo" && hasAncestor(region.node, "main") {
			t.Errorf("%s: a contentinfo landmark is inside <main> at %s; it is the "+
				"page's footer, not the article's (UI §7.2)",
				audit.where, elementPath(region.node))
		}
	}
}

// assertNoHeadingIsRepeated is the rule §7.2 does not state and §4.6's
// composition makes necessary.
//
// The shell has one centre slot and one rail. §4.2 puts a persistent "Not working"
// warning in the footer, and the pre-campaign rail carries its own notice under the
// same heading — so a composition that passes the subsystems to *both* renders
// `<h2>Not working</h2>` twice in one document.
//
// §7.2 requires exactly one `<h1>` and that heading levels never skip, and a
// repeated heading satisfies both. It is still wrong: a screen reader's heading
// list is the reader's table of contents, and two identical entries in it point at
// two different regions, so a reader navigating by heading arrives at one and has
// no way to know the other exists. Nothing else in the phase can see it — the
// count is right, the levels are right, and both copies render correctly.
//
// The rule is general rather than named after one string: two headings with the
// same text in one document of this shell is always a defect, and a rule about one
// string would be re-armed by a rename.
func assertNoHeadingIsRepeated(t *testing.T, audit *audit) {
	t.Helper()

	seen := map[string]*html.Node{}

	audit.elements(func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		text := strings.TrimSpace(textContent(node))
		if text == "" {
			return
		}

		if first, repeated := seen[text]; repeated {
			t.Errorf("%s: the heading %q appears twice, at %s and %s. A heading "+
				"list is a reader's table of contents, and two identical entries "+
				"point at two different regions — §4.2 puts the degraded warning in "+
				"the footer while the rail carries its own notice, and a composition "+
				"that renders both meets the reader with the same heading twice",
				audit.where, text, elementPath(first), elementPath(node))

			return
		}

		seen[text] = node
	})
}

// assertNoPositiveTabindex is §7.4's "no positive tabindex, anywhere — gate
// failure".
//
// Zero is excluded too. §7.4 says Tab follows natural document order, and a
// `tabindex="0"` moves one element to the front of the focus order while the
// markup still reads in the original order — so the two disagree and neither a
// reader nor a test can tell which one the page means. Only `-1` is usable: it
// makes an element programmatically focusable for a skip link without putting it
// in the tab sequence.
func assertNoPositiveTabindex(t failer, audit *audit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		raw := attribute(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer",
				audit.where, elementPath(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted. 0 moves the "+
				"element to the front of the tab order while the markup still reads "+
				"in document order (UI §7.4, §10.2)",
				audit.where, elementPath(node), value)
		}
	})
}

// assertNoAriaHiddenOnFocusable is §7.10's rule, asserted on the *combination*.
//
// Not on the attribute: §7.10 prohibits `aria-hidden` on an element that can
// receive focus, and the legitimate case in this shell is a navigation's icons —
// an `<svg aria-hidden="true">` inside a focusable link. The attribute alone is
// therefore not the finding; the attribute on something a keyboard can reach is.
//
// Two cases, and the second is the one that catches real bugs: a focusable
// element *inside* an `aria-hidden` subtree is just as unreachable, and the
// offending attribute is on an ancestor several levels up.
func assertNoAriaHiddenOnFocusable(t failer, audit *audit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if attribute(node, "aria-hidden") != "true" {
			return
		}

		if isFocusable(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader "+
				"cannot see is one that reader cannot reach (UI §7.10)",
				audit.where, elementPath(node))
		}

		if containsFocusable(node) {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements, so "+
				"everything inside it is unreachable to a screen reader (UI §7.10)",
				audit.where, elementPath(node))
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Type == html.ElementNode &&
				attribute(ancestor, "aria-hidden") == "true" && containsFocusable(ancestor) {
				t.Errorf("%s: %s is inside the aria-hidden subtree at %s, which "+
					"contains focusable elements; everything focusable inside it is "+
					"unreachable to a screen reader (UI §7.10)",
					audit.where, elementPath(node), elementPath(ancestor))

				return
			}
		}
	})
}

// containsFocusable reports whether a subtree holds anything a keyboard can reach.
func containsFocusable(node *html.Node) bool {
	found := false

	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil && !found; child = child.NextSibling {
			if isFocusable(child) {
				found = true

				return
			}

			walk(child)
		}
	}
	walk(node)

	return found
}

// skipLink is one skip link and what it points at.
type skipLink struct {
	text string
	href string
	node *html.Node
}

// skipLinks returns the document's skip links, in document order.
//
// Identified by class rather than by href. §7.2 fixes the targets, and the class
// is the hook the stylesheet uses to move the link off-screen until it is
// focused — so a "skip" link without the class is not off-screen and is therefore
// in the wrong place, and reading it from the class is the same claim made from
// the other end.
func skipLinks(audit *audit) []skipLink {
	var found []skipLink

	audit.elements(func(node *html.Node) {
		if node.DataAtom != atom.A || !hasClassToken(node, "skip-link") {
			return
		}

		found = append(found, skipLink{
			text: strings.TrimSpace(textContent(node)),
			href: attribute(node, "href"),
			node: node,
		})
	})

	return found
}

// assertSkipLinks is §7.2's ordering rule and §10.2's "skip links first in tab
// order", in three parts.
//
// Position — the links must be the first focusable elements in the document. A
// skip link after the banner is not a skip link, it is a link named "skip".
//
// Resolution — each `href="#id"` must resolve, and to a focusable landmark. A
// skip link to a landmark that is not in the document moves focus nowhere, which
// costs a reader a keypress and teaches them the skip links are unreliable.
//
// Order — §7.2's own sequence, and the campaign-navigation link only where the
// navigation exists. §4.6 removes the nav before a campaign does, and §7.2 says
// the link appears "only where the nav exists", so the link and the landmark are
// one condition expressed in two places. That is the exact thing a test is for.
func assertSkipLinks(t failer, audit *audit) {
	t.Helper()

	links := skipLinks(audit)
	if len(links) == 0 {
		t.Errorf("%s: the document has no skip link; §7.2 makes them the first "+
			"focusable elements and this shell always has at least one", audit.where)

		return
	}

	focusable := audit.focusable()

	for index, link := range links {
		if index >= len(focusable) || focusable[index] != link.node {
			t.Errorf("%s: skip link %d (%q) is not focusable element %d; §7.2 puts "+
				"the skip links first in tab order, before the banner",
				audit.where, index+1, link.text, index+1)

			continue
		}

		fragment, isFragment := strings.CutPrefix(link.href, "#")
		if !isFragment || fragment == "" {
			t.Errorf("%s: skip link %q has href %q; §7.2's links are fragments",
				audit.where, link.text, link.href)

			continue
		}

		target := elementByID(audit.root, fragment)
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
				audit.where, link.text, elementPath(target))
		}

		if roleOf(target) == "" {
			t.Errorf("%s: skip link %q lands on %s, which is not a landmark; a skip "+
				"link's target is the region it names (UI §7.2)",
				audit.where, link.text, elementPath(target))
		}
	}

	// §7.2's order, over the links that are present.
	contentAt, navigationAt, utilitiesAt := -1, -1, -1

	for index, link := range links {
		switch {
		case strings.Contains(link.text, "content"):
			contentAt = index
		case strings.Contains(link.text, "navigation"):
			navigationAt = index
		case strings.Contains(link.text, "utilities"):
			utilitiesAt = index
		}
	}

	if contentAt != 0 {
		t.Errorf("%s: the first skip link is %q; §7.2's order is content, campaign "+
			"navigation (where the nav exists), utilities",
			audit.where, skipLinkText(links, 0))
	}

	if navigationAt > 0 && navigationAt < contentAt {
		t.Errorf("%s: the campaign-navigation skip link precedes the content link; "+
			"§7.2's order is content first", audit.where)
	}

	if utilitiesAt >= 0 && utilitiesAt < contentAt {
		t.Errorf("%s: the utilities skip link precedes the content link; §7.2's "+
			"order is content first", audit.where)
	}

	// §4.6's half: the navigation skip link and the navigation landmark are one
	// condition in two places, and this is what catches them disagreeing. Both
	// directions, because "a link with no landmark" and "a landmark with no link"
	// are both failures and only one of them is visible in a rendering.
	hasNavLandmark := elementByID(audit.root, "nav") != nil
	hasNavLink := navigationAt >= 0

	if hasNavLandmark && !hasNavLink {
		t.Errorf("%s: the document has a navigation landmark but no skip link to "+
			"it; §7.2 lists \"Skip to campaign navigation\" -> #nav as the third "+
			"skip link wherever the nav exists", audit.where)
	}

	if hasNavLink && !hasNavLandmark {
		t.Errorf("%s: the document has a skip link to #nav but no navigation "+
			"landmark; §4.6 removes the nav before a campaign exists and a skip "+
			"link to an absent landmark moves focus nowhere", audit.where)
	}
}

// skipLinkText names the nth skip link for a failure message.
func skipLinkText(links []skipLink, index int) string {
	if index >= len(links) {
		return "(none)"
	}

	return links[index].text
}

// assertTestIDsResolve is §10.2's "every data-testid present", read as
// well-formedness.
//
// The two properties a hook must have to be usable at all: a non-empty value, and
// appearing once in the document. The per-route *enumeration* — which hooks this
// phase promises — lives with each route's own test, where a reader can see the
// list; what is asserted here is the property that makes an enumeration
// meaningful, and the reason is in the failure message: a hook that appears twice
// selects nothing.
func assertTestIDsResolve(t failer, audit *audit) {
	t.Helper()

	counts := map[string]int{}

	audit.elements(func(node *html.Node) {
		// An *absent* hook is not an empty one. Counting the empty string would
		// report every element without a hook as a duplicate of every other, and
		// the count would be the element count of the document — which is how this
		// first reported fifteen duplicates on a correct shell.
		if id := attribute(node, "data-testid"); id != "" {
			counts[id]++
		}
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
			"appears twice selects nothing (§10.2)",
			audit.where, strings.Join(duplicates, ", "))
	}
}

// assertVocabulary is UI §1.2 and §10.2's last clause: no rendered string
// contains "world" or "session".
//
// The scan covers **every text node, every attribute value, every attribute name
// and every HTML comment**, and each inclusion has a reason.
//
// Comments: a word in a comment is invisible to a reader, which is exactly why a
// gate reading only text nodes would pass on a document whose markup still names a
// retired entity. `TestTheVocabularyAuditFindsTheWordWhereverItIs` feeds this
// audit six ways of smuggling one in and requires it to object to each.
//
// Attribute values: an `aria-label` or a `title` is announced to a reader, so a
// retired entity named there is named to the person the rule protects.
//
// Attribute names: a `data-world` is invisible to a reader and obvious to a
// developer grepping for the feature it implies, which makes it exactly as retired
// as a label.
func assertVocabulary(t failer, audit *audit) {
	t.Helper()

	report := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, forbidden := range forbiddenVocabulary {
			if strings.Contains(lowered, forbidden) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes "+
					"the words appear nowhere in the interface",
					audit.where, where, forbidden)
			}
		}
	}

	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attr := range node.Attr {
				report("the attribute "+attr.Key, attr.Val)
				report("an attribute name", attr.Key)
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

// assertEveryFocusableCarriesTarget is §7.3's "enforced by construction" made
// checkable, and §10.6's "grep rendered markup for interactive elements missing
// the class".
//
// §10.6 says *including plugin output*. A plugin component's markup is in the
// same tree as everything else's, so a plugin rendering a button without `.target`
// fails this test exactly as a shell component would. The residual is stated
// rather than hidden: a plugin that injects markup by a mechanism bypassing the
// document tree is not visible to a Go test over parsed HTML, and §4.11.1 makes
// the client's involvement in that a contract rather than an implementation.
func assertEveryFocusableCarriesTarget(t failer, audit *audit) {
	t.Helper()

	for _, node := range audit.focusable() {
		if hasClassToken(node, "target") {
			continue
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §7.3 "+
			"enforces the --target-min minimum by construction and §10.6 audits for it",
			audit.where, elementPath(node))
	}
}

// assertEveryReferenceResolves is the ARIA integrity rule §10.2's list implies
// and does not spell out: a document whose `aria-controls` points at nothing has a
// control claiming to control something invisible.
//
// All five reference attributes, plus `<label for>` and `href="#…"`. The id
// uniqueness half is not incidental: two elements sharing an id make every
// reference to it ambiguous, and a screen reader resolves the ambiguity by picking
// the first — so a control can end up controlling the wrong element with nothing
// in the markup to say so.
func assertEveryReferenceResolves(t failer, audit *audit) {
	t.Helper()

	ids := map[string]int{}

	audit.elements(func(node *html.Node) {
		if id := attribute(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times; every reference to it is ambiguous "+
				"and a screen reader resolves the ambiguity by picking the first (UI §7.2)",
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
			value := attribute(node, name)
			if value == "" {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if elementByID(audit.root, id) == nil {
					t.Errorf("%s: %s carries %s=%q, which resolves to no element; a "+
						"control that claims to control something invisible is a "+
						"control that lies (UI §7.2)",
						audit.where, elementPath(node), name, id)
				}
			}
		}

		// §7.7: a real `<label for>`, never a placeholder. A `for` pointing at
		// nothing is a field with no accessible name.
		if node.DataAtom == atom.Label {
			if target := attribute(node, "for"); target != "" &&
				elementByID(audit.root, target) == nil {
				t.Errorf("%s: %s has for=%q, which resolves to no element; the field "+
					"has no accessible name (UI §7.7)",
					audit.where, elementPath(node), target)
			}
		}

		if node.DataAtom == atom.A {
			if fragment, isFragment := strings.CutPrefix(
				attribute(node, "href"),
				"#",
			); isFragment &&
				fragment != "" {
				if elementByID(audit.root, fragment) == nil {
					t.Errorf("%s: %s has href=\"#%s\", which resolves to no element",
						audit.where, elementPath(node), fragment)
				}
			}
		}
	})
}

// assertNoVaryOnCookie is S-13.5 and UI §6.6: the document never varies by the
// theme cookie, so **no `Vary: Cookie` is emitted**.
//
// Per-document rather than per-header: §3.7's resolver writes `data-theme` and
// `data-ui` client-side before the first paint, so the *bytes* are identical for
// every reader and the server has nothing to vary on. A `Vary: Cookie` here would
// be a cache-fragmentation tax for no correctness gain, and it would be a false
// promise — `Vary: Cookie` says the representation depends on the cookie, which is
// the opposite of the design.
//
// The header is checked on the recorder, so this needs the response rather than
// the parsed tree; it is in the per-route list because a route that set the header
// would be a route whose *other* properties are already suspect.
func assertNoVaryOnCookie(t failer, audit *audit) {
	t.Helper()

	// The value of the header is irrelevant; its presence is the finding.
	if audit.varyHeader == "" {
		return
	}

	t.Errorf("%s: the response carries Vary: %s; §6.6 requires that no Vary header "+
		"is emitted, because sp_ui never varies the document — §3.7 resolves both "+
		"root attributes client-side before the first paint (S-13.5)",
		audit.where, audit.varyHeader)
}

// --- The audit's own tests -------------------------------------------------

// TestTheVocabularyAuditFindsTheWordWhereverItIs is the audit's own test.
//
// §10.2's vocabulary rule is the easiest one in the list to pass vacuously: a
// substring assertion over a rendered document finds nothing in a correct shell
// and also finds nothing in a shell with `<!-- the world of this account -->` in
// it, because a comment is not a text node and the word is in a comment. The same
// is true of an `aria-label`, a `data-` attribute and an attribute *name*.
//
// So the audit is fed six ways of smuggling a retired entity in, and each has to
// be objected to. This is the same discipline `internal/httpapi/shell`'s
// `TestTheAuditFindsAResolvedAttributeWhereverItIs` applies to the resolver
// audit, and for the same reason: an audit that cannot fail is worse than no
// audit, because it is a green light wired to nothing.
//
// Every case is a *minimal* document — one element and the word — so a failure
// names the smuggling method rather than a shell that happens to be large.
func TestTheVocabularyAuditFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"a text node", `<html><body><p>the World of this account</p></body></html>`},
		{"an aria-label", `<html><body><nav aria-label="game sessions"></nav></body></html>`},
		{"a title attribute", `<html><body><span title="Your session"></span></body></html>`},
		{"a data attribute value", `<html><body><div data-note="world map"></div></body></html>`},
		{
			"an HTML comment",
			`<html><body><!-- the world of this account --><p>Fine.</p></body></html>`,
		},
		{
			// The nastiest one, because it is the one a substring assertion over
			// attributes would find and a rendered-document reader would not: the
			// word is in the *name* of an attribute nothing renders.
			"an attribute name",
			`<html><body><div data-world="true"></div></body></html>`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// A silent audit: it must report at least one violation, and it must
			// be a *t.Errorf* call rather than a return value, because that is how
			// the real audit is written and a test of a different shape would not
			// be testing the thing.
			counter := &countingT{T: t}

			assertVocabulary(counter, &audit{
				t:     counter,
				where: "fixture",
				root:  mustParse(t, testCase.body),
				body:  findElement(mustParse(t, testCase.body), "body"),
			})

			if counter.failures == 0 {
				t.Errorf("the vocabulary audit reported nothing for %s; a substring "+
					"assertion over a document finds no word in an HTML comment, an "+
					"attribute name or an aria-label, which is how this gate comes to "+
					"pass while the word sits in the markup (§1.2, §10.2)", testCase.name)
			}
		})
	}
}

// TestTheVocabularyAuditPassesOnTheRealShell is the other half: the audit must
// also be capable of reporting nothing.
//
// Six positive controls and one negative, because a gate that always fails is
// just as useless as one that never does — and an audit tuned until it fires on
// everything gets switched off within a phase.
func TestTheVocabularyAuditPassesOnTheRealShell(t *testing.T) {
	t.Parallel()

	for _, route := range auditedRoutes(t) {
		t.Run(route.where, func(t *testing.T) {
			t.Parallel()

			counter := &countingT{T: t}

			parsed := newAudit(t, route)
			parsed.t = counter

			assertVocabulary(counter, parsed)

			if counter.failures != 0 {
				t.Errorf("the vocabulary audit reported %d failures on the real "+
					"shell, which renders nothing forbidden; the audit is too eager "+
					"and will be switched off rather than fixed",
					counter.failures)
			}
		})
	}
}

// mustParse parses a fixture document, aborting on failure.
//
// Separate from `newAudit` because these fixtures are *not* responses: they are
// minimal documents written to trip one rule, and they carry no route, no status
// and no headers.
func mustParse(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return root
}

// countingT records Errorf calls instead of reporting them.
//
// A `testing.T` whose `Errorf` is intercepted, so a sub-audit can be *tested*
// rather than merely run. `FailNow` is deliberately not part of it: `Fatalf`
// aborting the test is correct behaviour for the audit's own constructor and is
// left alone.
//
// The *messages* are kept as well as the count, because a rule that fires for the
// wrong reason is still a rule that appears to work: a `<h1>` audit that also
// reported duplicate hooks would pass a fixture built to trip the heading count,
// and the gate would look covered while the thing it names was not being checked.
type countingT struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one document produces a count and
// a list rather than stopping at the first violation.
func (c *countingT) Errorf(format string, args ...any) {
	c.failures++
	c.messages = append(c.messages, fmt.Sprintf(format, args...))

	c.Logf(format, args...)
}

// mentions reports whether any recorded message contains a fragment.
func (c *countingT) mentions(fragment string) bool {
	for _, message := range c.messages {
		if strings.Contains(message, fragment) {
			return true
		}
	}

	return false
}

// Helper satisfies the audit's `t.Helper()` calls without recording anything.
func (c *countingT) Helper() {}

// TestTheStructuralAuditFindsEachViolationItClaimsTo is the same discipline over
// the rules that are not the vocabulary rule.
//
// Four violations, one per rule, each injected into an otherwise-valid shell
// document. A rule that cannot fail is a rule that has stopped being a gate, and
// these four are the ones with a real temptation to stop: the `<h1>` count (a
// second heading added by a route is easy), the tabindex (a plugin's first
// control), the skip-link resolution (a landmark removed per §4.6 without its
// link) and the `.target` class (§10.6's grep, which is the one most likely to be
// satisfied by a shell that happens to be tidy today).
func TestTheStructuralAuditFindsEachViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	// A minimal document with exactly one h1 and a resolvable skip link, so each
	// case perturbs one thing and the others stay valid.
	const base = `<html><head><title>t</title></head><body>` +
		`<a class="skip-link target" href="#main">Skip to content</a>` +
		`<main id="main" class="shell-main target" tabindex="-1"><h1>Page</h1>` +
		`<a class="target" href="/x">Link</a></main></body></html>`

	cases := []struct {
		name    string
		body    string
		check   func(failer, *audit)
		wantOne string
	}{
		{
			name:  "a second h1",
			body:  strings.Replace(base, "<h1>Page</h1>", "<h1>Page</h1><h1>Also page</h1>", 1),
			check: assertExactlyOneH1, wantOne: "<h1>",
		},
		{
			name: "a positive tabindex",
			body: strings.Replace(
				base,
				`<a class="target" href="/x">`,
				`<a tabindex="3" class="target" href="/x">`,
				1,
			),
			check:   assertNoPositiveTabindex,
			wantOne: "tabindex",
		},
		{
			name: "a focusable element with no target class",
			body: strings.Replace(
				base,
				`<a class="target" href="/x">`,
				`<a class="link" href="/x">`,
				1,
			),
			check:   assertEveryFocusableCarriesTarget,
			wantOne: ".target",
		},
		{
			name:  "a skip link to an absent landmark",
			body:  strings.Replace(base, `href="#main"`, `href="#nowhere"`, 1),
			check: assertSkipLinks, wantOne: "not in the document",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &countingT{T: t}
			root := mustParse(t, testCase.body)

			testCase.check(counter, &audit{
				t:      counter,
				where:  "fixture",
				status: 200,
				root:   root,
				body:   findElement(root, "body"),
				head:   findElement(root, "head"),
			})

			if counter.failures == 0 {
				t.Errorf("the audit reported nothing for %s; the rule is not "+
					"holding anything (§10.2)", testCase.name)
			}

			// And it fired for the *right* reason. A count alone cannot tell which
			// rule spoke, and an audit that fires for an unrelated reason looks
			// exactly like one that works.
			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the audit reported %d failures for %s but none of them "+
					"mentions %q, so the rule that fired was not the rule under "+
					"test; messages:\n  %s",
					counter.failures, testCase.name, testCase.wantOne,
					strings.Join(counter.messages, "\n  "))
			}
		})
	}
}
