package theme_test

// UI §10.2 and §10.6 for every response this route serves, gate-blocking.
//
// # Why this route's audit is the negative one
//
// §10.2 says "for every document a route renders", and **this route renders no
// document.** `GET /c/{slug}/theme.css` answers `text/css`, its failure branch
// answers `text/plain`, and the access gate in front of it answers ADR 0024's
// JSON. A stylesheet is fetched by a browser as a subresource; there is no
// landmark, no heading and no focus stop for a reader to reach, because the
// bytes are never a document.
//
// That claim is still asserted rather than inferred, for the reason
// `wiki`'s file gives about its 304 and its gate refusal: a subject list that
// quietly omits a state is a subject list that can drift. So every response is
// parsed and the *absence* is the rule — four of them, each named below.
//
// # What happens when the claim stops being true
//
// The moment this route answers `text/html`, `assertNotHTMLContentType` fails
// and the three structural rules fail with it, because a document will have
// grown a heading or a landmark that the "there is none" rule reports. That is
// the intended failure: a route that starts rendering documents owes its readers
// the *full* §10.2 audit — skip links, test hooks, reference integrity — and the
// cheapest way to make sure that debt is paid is to make the narrow audit red
// rather than to leave it silently satisfied by a document it cannot judge.
//
// # The duplication, named rather than hidden
//
// `auditFailer`, `docAudit` and the element walk are a second copy of the ones
// in `wiki`, `assets` and `plugins`, because a test package is not importable.
// The copies here are deliberately narrow: only the rules this route can
// violate, so the file a reviewer diffs against the others stays short.
//
// # Each rule was shown failing, and what it took
//
// A rule that cannot fail is a green light wired to nothing, so every one of the
// four was mutated in the route itself rather than in a fixture, and each was
// restored afterwards:
//
//   - `stylesheetContentType` `text/css` → `text/html` fails
//     `ContentTypeIsNotHTML` on both 200 rows and the pass-on-real-responses
//     test with it;
//   - `failureBody` → `<h1>Unavailable</h1><main><a href=/x>x</a></main>` fails
//     `NoHeading`, `NoLandmark`, `NoFocusStop` **and** §10.6's `TargetClass`, all
//     four on the 500 row — the three structural rules because `html.Parse`
//     builds the elements out of a plain-text body, which is why the parse runs
//     on every content type rather than only on HTML;
//   - the same body with `class=target` on the anchor fails `NoFocusStop` alone.
//     That is the whole reason `NoFocusStop` and `TargetClass` are separate
//     rules: with the class present, §10.6 is satisfied and only the "this is
//     not a document" rule is left to object;
//   - `failureBody` → `"the world is unavailable"` fails `TestEveryRouteSatisfiesTheVocabularyContract`
//     on the 500 row, which is the vocabulary rule seeing the word in bytes no
//     parser was asked about.

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// retiredWords is UI §1.2's closed vocabulary. Neither entity exists, a leftover
// is a bug, and a grep for them is a test — which is this.
//
// Case-insensitive and a substring, because the failure being guarded against is
// a noun phrase inside a sentence ("your session has expired", "the world map"),
// and neither is spelled with a capital letter in isolation.
var retiredWords = []string{"world", "session"}

// --- The responses -------------------------------------------------------------

// servedResponse is one answer this route gives, with the header that says what
// kind of thing it is.
//
// The header travels with the bytes because the *first* rule below reads it: the
// claim is not "the body happens to contain no markup" but "this response is not
// an HTML document", and a `text/html` body that happened to be empty would
// satisfy the second and violate the first.
type servedResponse struct {
	where       string
	status      int
	contentType string
	body        []byte
}

// auditedResponses returns every answer this route can put in front of a reader.
//
// Four, and each is a different *kind* rather than a different payload:
//
//   - the **core sheet**, which is what a campaign with no manifest gets;
//   - the **generated sheet**, the branded one — same status, same content type,
//     and the branch that writes `@font-face` and `url()` rather than nothing;
//   - the **500**, which is this route's own failure and the only branch that
//     answers `text/plain`;
//   - the **gate's refusal**, which never reaches the handler at all.
//
// One harness serves the first three, and that is the same argument `plugins`
// makes: a second harness would be a second registry and the point is that the
// *same* route produced all of them. The gate row makes the campaign private
// afterwards rather than building a second one, so the refusal is the same
// campaign's refusal.
func auditedResponses(t *testing.T) []servedResponse {
	t.Helper()

	served := newHarness(t)

	core := capture("200 — the core sheet, a campaign with no manifest",
		served.get(themePath, gmRequestor()))

	served.write(manifest(fixtureAccent, fixtureInk))

	branded := capture("200 — the generated sheet, a branded campaign",
		served.get(themePath, gmRequestor()))

	served.private()

	refused := capture("404 — the access gate's refusal",
		served.get(themePath, anonymousRequestor()))

	// The only branch that answers `text/plain`. `emptyRoots` is the degraded
	// instance, and it reaches the handler through the same chain the others do.
	fault := capture("500 — a content root this instance does not have",
		serveWithoutRoot(t, &theme.Handler{Roots: emptyRoots{}, Logger: served.logs.logger()}))

	return []servedResponse{core, branded, refused, fault}
}

// capture records a response as an audited answer.
func capture(where string, recorder *httptest.ResponseRecorder) servedResponse {
	return servedResponse{
		where:       where,
		status:      recorder.Code,
		contentType: recorder.Header().Get("Content-Type"),
		body:        recorder.Body.Bytes(),
	}
}

// --- The audit -----------------------------------------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2 for this route, and
// the rule is the absence the file's header describes.
//
// Each rule in its own subtest so a failure names the rule rather than "the a11y
// test failed", and each rule is written so that it *can* fail: every one of them
// is run against a fixture built to violate it by
// `TestEveryRouteAuditRejectsTheViolationItClaimsTo`, and against this route's
// own four responses by `TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe`.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, response := range auditedResponses(t) {
		t.Run(response.where, func(t *testing.T) {
			t.Parallel()

			audit := parseResponse(t, response)

			t.Run("ContentTypeIsNotHTML", func(t *testing.T) {
				assertNotHTMLContentType(t, audit)
			})
			t.Run("NoHeading", func(t *testing.T) {
				assertNoHeading(t, audit)
			})
			t.Run("NoLandmark", func(t *testing.T) {
				assertNoLandmark(t, audit)
			})
			t.Run("NoFocusStop", func(t *testing.T) {
				assertNoFocusStop(t, audit)
			})
		})
	}
}

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6 for the same
// four responses.
//
// **The assertion is trivially true today and is not the point.** §10.6 walks
// rendered markup for interactive elements missing `.target`, and this route
// serves no interactive element — so the rule is silent on every response, and
// `assertNoFocusStop` above is what says why. What this test adds is the
// *combination*: a focus stop that appears must carry the class **and** must trip
// the structural rule. Either one alone would be satisfiable by a route that
// grew a control nobody looked at, and §10.6 names plugin output precisely
// because that is how a control arrives.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, response := range auditedResponses(t) {
		t.Run(response.where, func(t *testing.T) {
			t.Parallel()

			assertEveryFocusStopCarriesTarget(t, parseResponse(t, response))
		})
	}
}

// TestEveryRouteSatisfiesTheVocabularyContract is UI §1.2 and §10.2's last
// clause, over the same four responses.
//
// # Why this rule reads the bytes and not the parsed tree
//
// AGENTS.md's rule is "parse the DOM; never substring-match the markup", and
// the reason it gives is that a word hidden in an HTML comment, an `aria-label`
// or a `data-` attribute is invisible to a scan of visible text. A scan of the
// **raw bytes** sees all three, so it is not the weaker check the rule warns
// about — it is the stronger one, and there is no markup for it to be fooled by.
//
// The parse is the wrong instrument here for a second and independent reason:
// `html.Parse` on `text/css` builds a document the browser never has, moving the
// stylesheet into a `<head>` the response does not carry and re-encoding what it
// finds. Auditing that reconstruction would be auditing something no reader is
// ever served.
//
// The parse is still available — `assertNoHeading` and its two siblings parse
// every response — so a route that starts serving `text/html` fails the
// structural rule *and* gets this rule running over a real document.
//
// One false-positive source, stated rather than hidden: a **campaign's own**
// strings (a font family, an image path) reach the generated sheet, and a family
// named after a retired entity would be reported here. That is content, not
// interface copy — and a test that objects to it is the right failure, because
// the alternative is a rule that has to know which part of a response a campaign
// is allowed to have written.
func TestEveryRouteSatisfiesTheVocabularyContract(t *testing.T) {
	t.Parallel()

	for _, response := range auditedResponses(t) {
		t.Run(response.where, func(t *testing.T) {
			t.Parallel()

			assertNoRetiredEntityIsNamed(t, parseResponse(t, response))
		})
	}
}

// assertNotHTMLContentType is the first and loudest of the four: the response
// says what it is, and it must not say HTML.
//
// A prefix check on the media type rather than an equality on the whole header,
// because `text/css; charset=utf-8` and `text/html; charset=utf-8` are the two
// values this route writes and the parameter is not the claim.
func assertNotHTMLContentType(t auditFailer, audit *docAudit) {
	t.Helper()

	if strings.HasPrefix(strings.ToLower(audit.contentType), "text/html") {
		t.Errorf("%s: Content-Type is %q; this route serves a subresource, and a "+
			"subresource that returns an HTML document is a document the browser "+
			"parses and discards with an error nobody sees (UI §10.2)",
			audit.where, audit.contentType)
	}
}

// assertNoHeading is §10.2's `<h1>` rule in the form this route can violate it:
// there must not be one at all.
//
// Not "exactly one". A document-shaped response is the finding, and a count of
// one would be a rule that passed the very change it exists to catch.
func assertNoHeading(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		if node.Data[1] < '1' || node.Data[1] > '6' {
			return
		}

		t.Errorf("%s: the response carries a <%s> at %s; this route serves a "+
			"stylesheet, and a heading means it has started serving documents and "+
			"owes readers the full §10.2 audit (UI §10.2)",
			audit.where, node.Data, nodePath(node))
	})
}

// assertNoLandmark is §10.2's landmark rule in the same form.
//
// Tags and roles both, because a `<div role="main">` is a landmark and an `<aside>`
// is one without a role attribute — and an audit that read only one of the two
// would pass the other's arrival.
var (
	landmarkTags = map[string]bool{
		"main": true, "nav": true, "header": true, "footer": true, "aside": true,
	}

	landmarkRoles = map[string]bool{
		"banner": true, "navigation": true, "main": true, "contentinfo": true,
		"complementary": true, "search": true, "region": true, "form": true,
	}
)

// assertNoLandmark reports any region a landmark-navigation reader could be sent
// to. `html` and `head` and `body` are excluded because the parser invents them
// for every input it is given; they are the parse, not the response.
func assertNoLandmark(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if landmarkTags[node.Data] {
			t.Errorf("%s: the response carries a <%s> at %s; a region a screen "+
				"reader can jump to means this route is rendering a document (UI "+
				"§10.2)", audit.where, node.Data, nodePath(node))
		}

		if role := attr(node, "role"); landmarkRoles[role] {
			t.Errorf("%s: the response carries role=%q at %s; §10.2's landmark "+
				"rules are about a document, and this route serves none (UI §10.2)",
				audit.where, role, nodePath(node))
		}
	})
}

// focusStopTags are the element types §10.6's list names, minus the ones a
// parser could plausibly invent.
var focusStopTags = map[string]bool{
	"button": true, "input": true, "select": true, "textarea": true,
	"summary": true,
}

// assertNoFocusStop reports anything a keyboard could reach in a response that is
// never a document.
//
// `a[href]` rather than `a`: a bare `<a>` is not focusable, and reporting one
// would make the rule fire on markup no reader could ever tab to — which is the
// difference between "this route grew a control" and "this route's bytes contain
// the letter a".
func assertNoFocusStop(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		switch {
		case node.Data == "a" && attr(node, "href") != "":
		case focusStopTags[node.Data]:
		case attr(node, "tabindex") != "":
		default:
			return
		}

		t.Errorf("%s: the response carries a focus stop at %s; §10.6 enforces "+
			"--target-min by construction on every interactive element, and a "+
			"response that grows one owes readers that class and this route's "+
			"audits a real subject (UI §10.6)", audit.where, nodePath(node))
	})
}

// assertEveryFocusStopCarriesTarget is §10.6's rule, and it is silent on every
// response this route serves — see `assertNoFocusStop` for why.
func assertEveryFocusStopCarriesTarget(t auditFailer, audit *docAudit) {
	t.Helper()

	for _, node := range audit.focusStops() {
		if hasClass(node, "target") {
			continue
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §10.6 "+
			"audits rendered markup for this **including plugin output**, and "+
			"--target-min is asserted from the built stylesheet (UI §10.6)",
			audit.where, nodePath(node))
	}
}

// assertNoRetiredEntityIsNamed is UI §1.2 over the response's own bytes.
//
// Every byte, lower-cased, tested for each retired word as a substring. See the
// vocabulary test's comment for why this is the bytes and not the parsed tree.
func assertNoRetiredEntityIsNamed(t auditFailer, audit *docAudit) {
	t.Helper()

	lowered := strings.ToLower(string(audit.raw))

	for _, retired := range retiredWords {
		if strings.Contains(lowered, retired) {
			t.Errorf("%s: the response contains %q; neither entity exists, and §1.2 "+
				"makes the words appear nowhere in the interface. First bytes: %q",
				audit.where, retired, truncate(string(audit.raw)))
		}
	}
}

// --- Parsing and the walk --------------------------------------------------------

// auditFailer is the subset of *testing.T the rules use.
//
// An interface rather than `*testing.T` because the rules have to be *tested* —
// a rule that cannot be shown to reject a violation it claims to catch is a gate
// wired to nothing — and a test of a `*testing.T`-typed function is a test that
// fails the suite. `silentFailer` in the self-test file satisfies this and
// records instead of reporting.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// docAudit is one response, parsed, and where it came from.
//
// `raw` is kept beside the tree because the vocabulary rule reads it: for a
// response that is not a document, the bytes are the only complete
// representation of what the reader is handed.
type docAudit struct {
	t           auditFailer
	where       string
	status      int
	contentType string
	raw         []byte
	root        *html.Node
}

// parseResponse parses an audited response.
//
// No "is this HTML?" guard, and the difference from `wiki`'s constructor is the
// whole point of this file: that one `Fatalf`s when a subject turns out not to
// be a document, because a document is what its route serves. Here a response
// that is not a document is the *expected* case, so the parse is always run and
// the rules below are the ones that say what the tree must not contain.
func parseResponse(t *testing.T, response servedResponse) *docAudit {
	t.Helper()

	root, err := html.Parse(strings.NewReader(string(response.body)))
	if err != nil {
		t.Fatalf("%s: parse response: %v", response.where, err)
	}

	return &docAudit{
		t:           t,
		where:       response.where,
		status:      response.status,
		contentType: response.contentType,
		raw:         response.body,
		root:        root,
	}
}

// parseFixture parses a minimal body written to trip one rule.
//
// Separate from `parseResponse` because a fixture is not a response: it has no
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

// elements calls visit for every element, in document order.
//
// Document order rather than depth-first-by-branch: the failure a reader meets
// is the one they meet first, and a message ordering that disagrees with the
// document makes a list of findings harder to act on rather than easier.
func (a *docAudit) elements(visit func(*html.Node)) {
	a.t.Helper()

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

// focusStops is every element a keyboard can reach, in document order.
//
// The same three cases `assertNoFocusStop` reports, read from one place so the
// rule that forbids a focus stop and the rule that demands `.target` on it can
// never disagree about what a focus stop is.
func (a *docAudit) focusStops() []*html.Node {
	a.t.Helper()

	stops := make([]*html.Node, 0)

	a.elements(func(node *html.Node) {
		switch {
		case node.Data == "a" && attr(node, "href") != "":
		case focusStopTags[node.Data]:
		case attr(node, "tabindex") != "":
		default:
			return
		}

		stops = append(stops, node)
	})

	return stops
}

// attr is one attribute's value, or "".
func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasClass reports whether the element's class attribute carries a token.
//
// Token, not substring: a styling rename producing `untargeted` must not satisfy
// §10.6, and `strings.Contains` does not know the difference between a class and
// a fragment of one.
func hasClass(node *html.Node, class string) bool {
	for token := range strings.FieldsSeq(attr(node, "class")) {
		if token == class {
			return true
		}
	}

	return false
}

// nodePath is a short description of where a node is, for a failure message.
//
// Element-only: `html` and `body` are the parser's own scaffolding, so naming
// them in a path would make every finding look like it is at the document root.
func nodePath(node *html.Node) string {
	parts := make([]string, 0, 4)

	for current := node; current != nil; current = current.Parent {
		if current.Type != html.ElementNode {
			continue
		}

		label := current.Data
		if id := attr(current, "id"); id != "" {
			label += "#" + id
		} else if testID := attr(current, "data-testid"); testID != "" {
			label += "[" + testID + "]"
		}

		parts = append(parts, label)
	}

	reversed := slices.Clone(parts)
	slices.Reverse(reversed)

	return strings.Join(reversed, " > ")
}

// truncate shortens a body for a failure message.
//
// The whole response is never printed: a generated sheet is hundreds of lines,
// and the line a reader needs is the one the assertion already named.
func truncate(body string) string {
	const limit = 240

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}
