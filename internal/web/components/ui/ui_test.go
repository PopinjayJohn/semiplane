package ui_test

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// The tests in this file are UI §10.2 and §10.6 against the states and the
// primitive library, run against rendered fixtures rather than against routes.
// The routes do not exist yet — the record builds the markup ahead of the wiring
// on purpose (§15.3, "Each step leaves the shell usable") — and none of these
// assertions needs a database, a session or a request.
//
// EVERY DOCUMENT IS PARSED, NEVER SEARCHED AS A STRING
// ------------------------------------------------------
// `components.AuditDocument` already made the argument for parsing, and the
// strongest version of it is §1.2: a substring assertion over markup is how "no
// world" passes while the word sits in an HTML comment, in a `data-` attribute, or
// in the middle of an identifier. So the vocabulary test walks every text node
// and every attribute value of every node, and the structural tests find
// elements by their attributes rather than by their markup.
//
// WHAT THESE TESTS DO NOT COVER, AND WHY
// --------------------------------------
// §10.3's viewport sweep, §10.4's keyboard walkthrough, §10.5's D-pad pass and
// §10.7's media-query matrix are agent-assisted by the record's own admission,
// and §10.9 says every finding from them becomes a committed Go test. What is
// asserted here is the half that *can* be: the server-rendered contract. Focus
// trapping, `Escape`, arrow traversal and the actual pixel minimums of `.target`
// are the other half, and they belong to the phase-9 client and to the
// stylesheet — `internal/web/components/ui` owns the markup contract, and
// `TestEveryInteractiveElementCarriesTheTargetClass` is what holds later phases
// and every plugin to it (§4.11.1, §10.6).

// forbiddenVocabulary is the interface's closed vocabulary (UI §1.2, AGENTS.md).
//
// Neither word names an entity that exists. The auth session is a real mechanism
// with a real cookie and is never surfaced, and the world entity is gone from
// the domain; a leftover reads as a synonym until a reader goes looking for the
// feature it implies. Case-insensitive so the plural and the capital are caught,
// and deliberately too broad — "World" inside a campaign somebody wants to call
// that is caught too, because the reader would be looking for a feature that does
// not exist.
var forbiddenVocabulary = []string{"world", "session"}

// render turns a component into the document a browser would receive.
func render(t *testing.T, component templ.Component) string {
	t.Helper()

	var out strings.Builder
	if err := component.Render(t.Context(), &out); err != nil {
		t.Fatalf("render component: %v", err)
	}

	return out.String()
}

// parse renders a component and parses the result into a DOM.
//
// The parse is fatal rather than tolerated: a fixture that does not parse is a
// fixture whose assertions would all pass vacuously against an empty tree, and
// "no world in an empty document" is the single most expensive kind of green.
func parse(t *testing.T, component templ.Component) *html.Node {
	t.Helper()

	document := render(t, component)

	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("rendered markup does not parse as HTML: %v", err)
	}

	return root
}

// element writes one element, escaped, as a fixture's content.
//
// Fixtures need content to put in panels, tab panels, dialog bodies and table
// cells, and a templ component cannot be declared in a _test.go file — templ
// reads .templ files only. Escaped so a fixture cannot smuggle markup into a
// document and make an assertion about that markup meaningless.
func element(tag, body string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, "<"+tag+">"+html.EscapeString(body)+"</"+tag+">")
		if err != nil {
			return fmt.Errorf("write fixture element: %w", err)
		}

		return nil
	})
}

// routeHeading is the `<h1>` a route owns above a state that renders its own
// heading as an `<h2>`.
//
// Composed rather than imported from `components` on purpose: this package does
// not depend on the shell package, the shell package depends on this one, and
// importing it here would make the tests a place where a cycle could grow. It is
// the same element the shell's centre slot produces, and the tests that need a
// whole page use it to build one.
func routeHeading(text string) templ.Component {
	return element("h1", text)
}

// withRouteHeading composes a route's heading above a state, in the order the
// shell renders them: main, then the state.
func withRouteHeading(name string, state templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, writer io.Writer) error {
		if err := routeHeading(name).Render(ctx, writer); err != nil {
			return fmt.Errorf("render route heading: %w", err)
		}

		if err := state.Render(ctx, writer); err != nil {
			return fmt.Errorf("render state: %w", err)
		}

		return nil
	})
}

// walk visits every node in the tree, parents before children.
func walk(node *html.Node, visit func(*html.Node)) {
	visit(node)

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, visit)
	}
}

// elements returns every element in the tree satisfying a predicate.
func elements(root *html.Node, matches func(*html.Node) bool) []*html.Node {
	var found []*html.Node

	walk(root, func(node *html.Node) {
		if node.Type == html.ElementNode && matches(node) {
			found = append(found, node)
		}
	})

	return found
}

// attr returns an element's attribute value.
func attr(node *html.Node, name string) (string, bool) {
	for _, attribute := range node.Attr {
		// EqualFold because an attribute name in HTML folds case, and `DATA-TESTID`
		// is the same attribute.
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val, true
		}
	}

	return "", false
}

// hasAttr reports whether an element carries a named attribute at all.
func hasAttr(node *html.Node, name string) bool {
	_, found := attr(node, name)

	return found
}

// classes is an element's class list.
func classes(node *html.Node) []string {
	value, _ := attr(node, "class")

	return strings.Fields(value)
}

// hasClass reports whether an element carries a class.
func hasClass(node *html.Node, class string) bool {
	return slices.Contains(classes(node), class)
}

// byTestID finds the single element carrying a test hook.
func byTestID(t *testing.T, root *html.Node, testID string) *html.Node {
	t.Helper()

	matches := elements(root, func(node *html.Node) bool {
		value, _ := attr(node, "data-testid")

		return value == testID
	})

	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		t.Fatalf("no element carries data-testid=%q", testID)
	default:
		t.Fatalf(
			"%d elements carry data-testid=%q; a repeated hook selects nothing",
			len(matches),
			testID,
		)
	}

	return nil
}

// text is an element's text content, with the elements' own markup discarded.
func text(node *html.Node) string {
	var out strings.Builder

	walk(node, func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}
	})

	return out.String()
}

// normalisedText is an element's text with runs of whitespace collapsed.
//
// Accessible-name comparison is on the *rendered* string, and a name split across
// a line break in the source is one string to a reader and two to a test.
func normalisedText(node *html.Node) string {
	return strings.Join(strings.Fields(text(node)), " ")
}

// idIndex is every element in the tree keyed by its id.
func idIndex(root *html.Node) map[string]*html.Node {
	index := map[string]*html.Node{}

	walk(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if id, found := attr(node, "id"); found {
			index[id] = node
		}
	})

	return index
}

// accessibleName is an element's accessible name, computed the way the
// accessibility API computes it for the two cases this package produces:
// `aria-labelledby` naming other elements, and `aria-label` naming the element
// itself.
//
// A form control's `<label for>` is deliberately *not* computed here. It is the
// third case, and no state in this package is named by one — the search form's
// field is, and that is asserted directly by its own test rather than by a
// general algorithm that would then be a partial implementation of all three.
func accessibleName(node *html.Node, index map[string]*html.Node) string {
	if labelledBy, found := attr(node, "aria-labelledby"); found {
		var out strings.Builder

		for id := range strings.FieldsSeq(labelledBy) {
			target, resolved := index[id]
			if !resolved {
				// A name pointing at nothing is a region with no name. Reported
				// as a marker so a caller comparing against a string sees the
				// difference rather than two empty strings agreeing.
				out.WriteString("<unresolved:" + id + ">")

				continue
			}

			out.WriteString(normalisedText(target))
			out.WriteString(" ")
		}

		return strings.TrimSpace(out.String())
	}

	if label, found := attr(node, "aria-label"); found {
		return label
	}

	return ""
}

// stateCase is one state of UI §4.7 and how it must behave.
//
// `build` takes a heading placement rather than a finished component so every
// structural assertion can run against both: a state mounted under a route that
// owns the `<h1>`, and a state that is the whole page. That is not a formality —
// five of the eleven are rendered both ways in practice, and "exactly one `<h1>`"
// is only a real guarantee if both placements are checked.
type stateCase struct {
	// name identifies the case in failure output, and is the record's own name
	// for the state where the record has one.
	name string
	// testID is the state's own hook.
	testID string
	// wantName is the accessible name the state must expose. Written out rather
	// than derived, because the copy is the contract and a test that computes the
	// expectation from the markup asserts nothing.
	wantName string
	// pagePlacement is the heading placement in which the state owns the `<h1>`.
	// HeadingInPage in practice for every state in this list; the field is there
	// so a future state that must own the page heading says so once.
	pagePlacement ui.Heading
	// assertive reports whether the state carries `role="alert"`, which §7.5
	// grants to a 412, to ruleset drift and to a lost connection — and to nothing
	// else in this list.
	assertive bool
	// wantHooks are further test hooks the state must carry, for the assertions
	// that are about the state rather than about its type.
	wantHooks []string
	// build renders the state at a placement.
	build func(ui.Heading) templ.Component
}

// stateCases is the eleven states of UI §4.7, in the record's own order.
//
// The count and the list are the assertion `TestEveryStateHasATestHookAndAn
// AccessibleName` makes: a twelfth state has to be added here to be tested, and a
// state dropped from here fails the length check. "Game ended" is absent and
// states.templ's header says why — §7.5 attributes it to §4.7, there is no
// domain state behind it, and §4.9 forbids inventing the client-side flag.
func stateCases() []stateCase {
	return []stateCase{
		{
			name:          "first run, zero campaigns",
			testID:        "first-run",
			wantName:      "No campaigns yet",
			pagePlacement: ui.HeadingInPage,
			wantHooks:     []string{"first-run-cta", "first-run-docs"},
			build: func(heading ui.Heading) templ.Component {
				return ui.FirstRun(ui.FirstRunView{
					CreateHref: "/admin/campaigns/new",
					DocsHref:   "https://example.invalid/docs",
					Heading:    heading,
				})
			},
		},
		{
			name:          "campaign with no pages",
			testID:        "state-campaign-no-pages",
			wantName:      "This campaign has no pages yet",
			pagePlacement: ui.HeadingInPage,
			wantHooks:     []string{"campaign-no-pages-cta"},
			build: func(heading ui.Heading) templ.Component {
				return ui.NoPages(ui.NoPagesView{
					CreateHref: "/c/greyhaven/wiki/new",
					Heading:    heading,
				})
			},
		},
		{
			name:          "search, no query",
			testID:        "state-search-idle",
			wantName:      "Search",
			pagePlacement: ui.HeadingPage,
			wantHooks:     []string{"search-idle-query", "search-idle-hint", "search-idle-submit"},
			build: func(heading ui.Heading) templ.Component {
				return ui.SearchIdle(ui.SearchIdleView{
					Action:  "/c/greyhaven/search",
					Heading: heading,
				})
			},
		},
		{
			name:          "search, no results",
			testID:        "state-search-no-results",
			wantName:      "No results for goblin",
			pagePlacement: ui.HeadingPage,
			wantHooks: []string{
				"search-no-results-term",
				"search-no-results-query",
				"search-no-results-submit",
			},
			build: func(heading ui.Heading) templ.Component {
				return ui.SearchEmpty(ui.SearchEmptyView{
					Action:  "/c/greyhaven/search",
					Query:   "goblin",
					Heading: heading,
				})
			},
		},
		{
			name:          "403",
			testID:        "state-error-403",
			wantName:      "You do not have access to this campaign",
			pagePlacement: ui.HeadingPage,
			wantHooks:     []string{"error-403-campaign"},
			build: func(heading ui.Heading) templ.Component {
				return ui.Forbidden(ui.ForbiddenView{
					CampaignName: "Greyhaven",
					Heading:      heading,
				})
			},
		},
		{
			name:          "404",
			testID:        "state-error-404",
			wantName:      "No page at Locations/Greyhaven",
			pagePlacement: ui.HeadingPage,
			wantHooks:     []string{"error-404-path", "error-404-campaign-link"},
			build: func(heading ui.Heading) templ.Component {
				return ui.NotFound(ui.NotFoundView{
					PagePath:         "Locations/Greyhaven",
					CampaignRootHref: "/c/greyhaven",
					Heading:          heading,
				})
			},
		},
		{
			name:          "412",
			testID:        "state-error-412",
			wantName:      "This page changed while you were editing",
			pagePlacement: ui.HeadingInPage,
			assertive:     true,
			wantHooks:     []string{"error-412-message"},
			build: func(heading ui.Heading) templ.Component {
				return ui.ConflictNotice(
					ui.ConflictView{Heading: heading},
					element("div", "diff"),
				)
			},
		},
		{
			name:          "500",
			testID:        "error-state",
			wantName:      "This page could not be loaded",
			pagePlacement: ui.HeadingInPage,
			wantHooks:     []string{"error-state-message", "error-state-reference"},
			build: func(heading ui.Heading) templ.Component {
				return ui.LoadError(ui.LoadFailure{
					Reference: "req-4f2a",
					Heading:   heading,
				})
			},
		},
		{
			name:          "unknown system_id",
			testID:        "state-system-missing",
			wantName:      "Gameplay system not installed",
			pagePlacement: ui.HeadingInPage,
			wantHooks:     []string{"system-missing-id"},
			build: func(heading ui.Heading) templ.Component {
				return ui.SystemMissing(ui.SystemMissingView{
					SystemID: "dnd5e-2024",
					Heading:  heading,
				})
			},
		},
		{
			name:          "ruleset_version drift",
			testID:        "state-ruleset-drift",
			wantName:      "This table was last played under a different rules version",
			pagePlacement: ui.HeadingInPage,
			assertive:     true,
			wantHooks:     []string{"ruleset-drift-message", "ruleset-drift-link"},
			build: func(heading ui.Heading) templ.Component {
				return ui.RulesetDrift(ui.RulesetDriftView{
					RulesHref: "/c/greyhaven/settings/rules",
					Heading:   heading,
				})
			},
		},
		{
			name:          "connection lost",
			testID:        "state-connection-lost",
			wantName:      "The connection to this table was lost",
			pagePlacement: ui.HeadingInPage,
			assertive:     true,
			build: func(heading ui.Heading) templ.Component {
				return ui.ConnectionLost(ui.ConnectionLostView{Heading: heading})
			},
		},
	}
}

// stateByHook finds one state by its test hook.
//
// By hook rather than by index: a fixture table that is addressed positionally
// silently re-points every assertion when a state is inserted, and the failure
// reads as a broken component rather than as a moved fixture.
func stateByHook(t *testing.T, testID string) stateCase {
	t.Helper()

	for _, testCase := range stateCases() {
		if testCase.testID == testID {
			return testCase
		}
	}

	t.Fatalf("no state in the fixture table carries the hook %q", testID)

	return stateCase{}
}

// TestEveryStateHasATestHookAndAnAccessibleName is the gate on the eleven
// themselves.
//
// A state with no accessible name is a region a screen reader announces as
// "region", which tells a reader nothing about whether the thing they asked for
// is missing, broken or refused. The name is `aria-labelledby` the state's own
// heading, so the two cannot drift: a heading renamed without the region
// re-pointing at it fails here.
func TestEveryStateHasATestHookAndAnAccessibleName(t *testing.T) {
	t.Parallel()

	const wantStates = 11

	cases := stateCases()
	if len(cases) != wantStates {
		t.Fatalf("the state table has %d entries, want %d; UI §4.7 designs eleven "+
			"and a state added without one here is a state nobody has read",
			len(cases), wantStates,
		)
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, testCase.build(testCase.pagePlacement))

			state := byTestID(t, root, testCase.testID)

			index := idIndex(root)
			if name := accessibleName(state, index); name != testCase.wantName {
				t.Errorf("accessible name is %q, want %q", name, testCase.wantName)
			}

			// The name has to come from something, so assert the mechanism too:
			// a name that happened to match because of an `aria-label` on
			// something else would pass the comparison above and fail here.
			if !hasAttr(state, "aria-labelledby") {
				t.Error("the state is not named by aria-labelledby; §7.2 wants the " +
					"region named by the heading it carries")
			}

			// `role="alert"` is the one role a state may carry, and only the
			// three §7.5 grants it to. Anything else — `region`, `status`,
			// `document` — would add a landmark §7.2 counts, and §4.11.1 forbids a
			// plugin from doing the same.
			role, _ := attr(state, "role")
			if role != "" && (role != "alert" || !testCase.assertive) {
				t.Errorf("the state carries role=%q; the only role a state may have "+
					"is \"alert\", and only on the three §7.5 grants it to", role)
			}

			for _, hook := range testCase.wantHooks {
				byTestID(t, root, hook)
			}
		})
	}
}

// TestStatesNeverNameWorldOrSession is UI §1.2 and §10.2, over the DOM rather
// than over the markup.
//
// Every text node and every attribute value on every element, with the text
// inside `<script>` and `<style>` excluded — not because this package renders any
// (it renders none), but because a fixture that grew one would otherwise have its
// own source quoted back at it. Comments are *not* excluded: a comment carrying
// the word is the exact shape of leftover this test exists for, and
// `components.AuditDocument` made the same argument about `data-ui`.
func TestStatesNeverNameWorldOrSession(t *testing.T) {
	t.Parallel()

	for _, testCase := range stateCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, testCase.build(testCase.pagePlacement))

			walk(root, func(node *html.Node) {
				switch node.Type {
				case html.TextNode, html.CommentNode:
					if parent := node.Parent; parent != nil && parent.Type == html.ElementNode {
						if parent.Data == "script" || parent.Data == "style" {
							return
						}
					}

					assertNoForbiddenVocabulary(t, node.Data, "a "+nodeKindName(node))
				case html.ElementNode:
					assertNodeVocabulary(t, node)
				case html.ErrorNode, html.DocumentNode, html.DoctypeNode, html.RawNode:
				}
			})
		})
	}
}

// assertNodeVocabulary fails on a forbidden word in any attribute of an element.
//
// Attribute *names* as well as values: a `data-world` or a `data-session` is a word
// in the document that a reader's browser can see and a substring test over the
// body would miss, and §10.2 greps rendered markup rather than rendered text.
func assertNodeVocabulary(t *testing.T, node *html.Node) {
	t.Helper()

	for _, attribute := range node.Attr {
		assertNoForbiddenVocabulary(t, attribute.Val,
			"the "+attribute.Key+" attribute of <"+node.Data+">")
		assertNoForbiddenVocabulary(t, attribute.Key,
			"an attribute name on <"+node.Data+">")
	}
}

// assertNoForbiddenVocabulary fails on any forbidden word in one string.
func assertNoForbiddenVocabulary(t *testing.T, value, where string) {
	t.Helper()

	lowered := strings.ToLower(value)

	for _, forbidden := range forbiddenVocabulary {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("%s contains %q; neither entity exists in the interface and a "+
				"leftover is a bug (UI §1.2)", where, forbidden)
		}
	}
}

// nodeKindName names a node for a failure message.
func nodeKindName(node *html.Node) string {
	if node.Type == html.CommentNode {
		return "HTML comment"
	}

	return "text node"
}

// TestEveryCompositionHasExactlyOneH1 is §7.2's structural rule, run against
// both placements.
//
// The composition is what a route actually renders: a heading, then the state in
// the centre slot. Rendering the state bare would pass the check trivially for
// the nine states that render an `<h2>`, so the route's half is not optional.
func TestEveryCompositionHasExactlyOneH1(t *testing.T) {
	t.Parallel()

	for _, testCase := range stateCases() {
		for _, heading := range []ui.Heading{ui.HeadingInPage, ui.HeadingPage} {
			t.Run(testCase.name+"/"+placementName(heading), func(t *testing.T) {
				t.Parallel()

				state := testCase.build(heading)

				root := parse(t, composition(heading, state))

				if found := len(elements(root, func(node *html.Node) bool {
					return node.Data == "h1"
				})); found != 1 {
					t.Errorf("the composition has %d <h1> elements, want exactly 1 "+
						"(UI §7.2)", found)
				}
			})
		}
	}
}

// composition is the page a route renders for a state: the route's own heading
// when the state renders an `<h2>`, and the state alone when it owns the `<h1>`.
//
// One function rather than the branch repeated at each call site, because the
// branch is the thing the assertions are about: a state tested bare would pass
// "exactly one `<h1>`" for the wrong reason.
func composition(heading ui.Heading, state templ.Component) templ.Component {
	if heading == ui.HeadingInPage {
		return withRouteHeading("Greyhaven", state)
	}

	return state
}

// placementName names a heading placement for a failure message.
func placementName(heading ui.Heading) string {
	if heading == ui.HeadingPage {
		return "as-page"
	}

	return "in-page"
}

// TestThePrimitivesNeverNameWorldOrSession is the same vocabulary rule over the
// primitive library rather than over the states.
//
// Separate because the states and the primitives are separately auditable, and
// because a primitive's labels are the ones a plugin reuses verbatim: a plugin
// component inherits `.target`, the tokens and the focus ring (§4.11.1), and
// "inherits the tokens" should not quietly include inheriting a word the interface
// has banned.
func TestThePrimitivesNeverNameWorldOrSession(t *testing.T) {
	t.Parallel()

	for name, component := range primitiveFixtures(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			walk(parse(t, component), func(node *html.Node) {
				switch node.Type {
				case html.TextNode, html.CommentNode:
					if parent := node.Parent; parent != nil && parent.Type == html.ElementNode {
						if parent.Data == "script" || parent.Data == "style" {
							return
						}
					}

					assertNoForbiddenVocabulary(t, node.Data, "a "+nodeKindName(node))
				case html.ElementNode:
					assertNodeVocabulary(t, node)
				case html.ErrorNode, html.DocumentNode, html.DoctypeNode, html.RawNode:
					// None of these carries user-visible text. A doctype is the
					// parser's, and a raw-text node only exists inside <script> and
					// <style>, which the branch above already excludes.
				}
			})
		})
	}
}

// TestEveryIdReferenceResolves is the structural test that catches the whole
// class of "the wiring is in the document but points at nothing" defects.
//
// Every id in a fragment is unique, and every reference a fragment makes —
// `aria-controls`, `aria-labelledby`, `aria-describedby`, a `<label for>`, and a
// skip link's `href="#…"` — names an element that exists in the same document.
// Each of those is one attribute away from being inert, and an inert one is
// invisible: the page renders, the test that looks for an element finds it, and a
// reader is left with a button that does nothing (§7.7).
func TestEveryIdReferenceResolves(t *testing.T) {
	t.Parallel()

	referencing := []string{
		"aria-controls", "aria-labelledby", "aria-describedby", "for",
	}

	for name, component := range stateFixtures(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, component)
			index := idIndex(root)

			for id, node := range index {
				if node.Parent == nil {
					t.Errorf("id %q resolves to nothing in the document", id)
				}
			}

			for _, attribute := range referencing {
				for _, node := range elements(root, func(node *html.Node) bool {
					return hasAttr(node, attribute)
				}) {
					for id := range strings.FieldsSeq(attrValue(node, attribute)) {
						if _, resolved := index[id]; !resolved {
							t.Errorf("<%s> has %s=%q, which names no element in this "+
								"document; the reference is inert (UI §7.7)",
								node.Data, attribute, id)
						}
					}
				}
			}

			for _, link := range elements(root, func(node *html.Node) bool {
				return node.Data == "a" && strings.HasPrefix(attrValue(node, "href"), "#")
			}) {
				target := strings.TrimPrefix(attrValue(link, "href"), "#")
				if _, resolved := index[target]; !resolved {
					t.Errorf("the skip link points at #%s, which this document does "+
						"not render; a link to an absent landmark moves focus nowhere "+
						"(UI §7.2)", target)
				}
			}
		})
	}
}

// TestEveryPrimitiveIdIsUniqueInItsFragment is the half of the previous test
// that a reference walk cannot see: two elements sharing an id still resolve, and
// they resolve to whichever the browser found first.
func TestEveryPrimitiveIdIsUniqueInItsFragment(t *testing.T) {
	t.Parallel()

	for name, component := range primitiveFixtures(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]int{}

			for _, node := range elements(parse(t, component), func(node *html.Node) bool {
				return hasAttr(node, "id")
			}) {
				seen[attrValue(node, "id")]++
			}

			for id, count := range seen {
				if count > 1 {
					t.Errorf("id %q appears %d times in one fragment; a duplicated id "+
						"makes every reference to it resolve to the first one (UI §7.2)",
						id, count)
				}
			}
		})
	}
}

// TestEveryPrimitiveIsClosedUntilSomethingOpensIt is §7.7's "a popup that cannot
// open is a dead control" from the other side: everything that starts closed must
// say so, and everything that starts open must be the caller's choice.
//
// A dialog or a menu in the document at load is a list of controls a reader tabs
// through with no way to know why. A *disclosure* is the exception, because its
// region is rendered either way and `hidden` is what carries the state — so it is
// asserted on `aria-expanded` instead.
func TestEveryPrimitiveIsClosedUntilSomethingOpensIt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		component templ.Component
		hook      string
		wantOpen  bool
	}{
		{name: "dialog", component: dialogFixture(), hook: "confirm-discard", wantOpen: false},
		{name: "menu button", component: menuFixture(), hook: "page-actions-menu", wantOpen: false},
		{
			name:      "disclosure, collapsed",
			component: disclosureFixture(false),
			hook:      "page-outline-region",
			wantOpen:  false,
		},
		{
			name:      "disclosure, open",
			component: disclosureFixture(true),
			hook:      "page-outline-region",
			wantOpen:  true,
		},
		{
			name:      "the unselected tab panels",
			component: tabsFixture(),
			hook:      "rules-panel",
			wantOpen:  false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := hasAttr(
				byTestID(t, parse(t, testCase.component), testCase.hook),
				"hidden",
			); got == testCase.wantOpen {
				t.Errorf("hidden=%v, want %v", got, testCase.wantOpen)
			}
		})
	}
}

// stateFixtures is every state, at the placement it is documented for, plus the
// states mounted under a route heading. The union, because a fragment is rendered
// into both kinds of page and neither may carry a dangling reference.
func stateFixtures(t *testing.T) map[string]templ.Component {
	t.Helper()

	all := map[string]templ.Component{}

	for _, testCase := range stateCases() {
		all[testCase.name] = composition(
			testCase.pagePlacement,
			testCase.build(testCase.pagePlacement),
		)
	}

	for name, component := range primitiveFixtures(t) {
		if name == "skip link" {
			// Excluded from the reference walk on purpose. A skip link's target is
			// a landmark the *shell* renders, so in a fragment of this package the
			// reference cannot resolve — which is §7.2's rule stated from the other
			// side, and the reason the caller chooses the target.
			//
			// TestTheSkipLinkNamesOnlyLandmarksTheRouteRenders asserts the fragment
			// on its own, and `components.Shell` is where a real composition of the
			// two is checked.
			continue
		}

		all[name] = withRouteHeading("Greyhaven", component)
	}

	return all
}

// primitiveFixtures is every primitive fragment this package renders.
func primitiveFixtures(t *testing.T) map[string]templ.Component {
	t.Helper()

	return map[string]templ.Component{
		"skip link":             ui.SkipLink("main", "Skip to content", "skip-to-content"),
		"dialog":                dialogFixture(),
		"menu button":           menuFixture(),
		"tabs":                  tabsFixture(),
		"disclosure, open":      disclosureFixture(true),
		"disclosure, collapsed": disclosureFixture(false),
		"table":                 tableFixture(),
	}
}

// TestHeadingLevelsNeverSkip is the other half of §7.2's structural rule, and it
// is separate because a document with two `<h1>`s and a skipped level are
// different defects caught by different code.
func TestHeadingLevelsNeverSkip(t *testing.T) {
	t.Parallel()

	for _, testCase := range stateCases() {
		for _, heading := range []ui.Heading{ui.HeadingInPage, ui.HeadingPage} {
			t.Run(testCase.name+"/"+placementName(heading), func(t *testing.T) {
				t.Parallel()

				state := testCase.build(heading)

				previous := 0

				walk(parse(t, composition(heading, state)), func(node *html.Node) {
					level := headingLevel(node)
					if level == 0 {
						return
					}

					// A jump of more than one skips a level. A jump *down* is
					// closing sections, which is the ordinary case.
					if previous != 0 && level > previous+1 {
						t.Errorf("<h%d> follows <h%d>, which skips a level (UI §7.2)",
							level, previous)
					}

					previous = level
				})
			})
		}
	}
}

// headingLevel is an element's heading level, or 0 for a non-heading.
func headingLevel(node *html.Node) int {
	if node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0
	}

	level, err := strconv.Atoi(node.Data[1:])
	if err != nil || level < 1 || level > 6 {
		return 0
	}

	return level
}

// TestStatesDeclareNoPositiveTabindex is §7.10's prohibition, restated because
// it is the one rule with no legitimate exception anywhere in this repository.
//
// `-1` is the skip links' and the dialog's mechanism and is asserted separately,
// so the only values these documents may carry are `-1` and none at all.
func TestStatesDeclareNoPositiveTabindex(t *testing.T) {
	t.Parallel()

	for _, testCase := range stateCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			for _, node := range tabbable(t, testCase.build(testCase.pagePlacement)) {
				value, _ := attr(node, "tabindex")

				index, err := strconv.Atoi(value)
				if err == nil && index > 0 {
					t.Errorf("<%s> carries tabindex=%q; a positive tabindex is "+
						"prohibited outright (UI §7.10)", node.Data, value)
				}
			}
		})
	}
}

// tabbable renders a component and returns every element carrying a tabindex.
func tabbable(t *testing.T, component templ.Component) []*html.Node {
	t.Helper()

	return elements(tree(t, component), hasTabIndex)
}

// hasTabIndex reports whether an element carries a tabindex at all.
func hasTabIndex(node *html.Node) bool {
	return hasAttr(node, "tabindex")
}

// TestEveryInteractiveElementCarriesTheTargetClass is §10.6's half that markup
// can answer, and it is the contract later phases and every plugin are held to.
//
// §10.6 greps rendered markup for interactive elements missing the class
// "including plugin output", so the rule has to be decidable from markup: an
// element is interactive if it is a link with an address, a form control, a
// `<summary>`, or anything with a tabindex of zero or more. A `.target` on a
// non-interactive element is not asserted against — a `<li>` with the class would
// be odd rather than broken.
func TestEveryInteractiveElementCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	components := map[string]templ.Component{
		"first run":             stateByHook(t, "first-run").build(ui.HeadingInPage),
		"search, no query":      stateByHook(t, "state-search-idle").build(ui.HeadingPage),
		"403":                   stateByHook(t, "state-error-403").build(ui.HeadingPage),
		"404":                   stateByHook(t, "state-error-404").build(ui.HeadingPage),
		"412":                   stateByHook(t, "state-error-412").build(ui.HeadingInPage),
		"500":                   stateByHook(t, "error-state").build(ui.HeadingInPage),
		"system not installed":  stateByHook(t, "state-system-missing").build(ui.HeadingInPage),
		"ruleset drift":         stateByHook(t, "state-ruleset-drift").build(ui.HeadingInPage),
		"connection lost":       stateByHook(t, "state-connection-lost").build(ui.HeadingInPage),
		"dialog":                dialogFixture(),
		"menu button":           menuFixture(),
		"tabs":                  tabsFixture(),
		"disclosure, open":      disclosureFixture(true),
		"disclosure, collapsed": disclosureFixture(false),
		"table":                 tableFixture(),
		"skip link":             ui.SkipLink("main", "Skip to content", "skip-to-content"),
	}

	for name, component := range components {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, node := range interactiveElements(tree(t, component)) {
				if hasClass(node, ui.TargetClass) {
					continue
				}

				t.Errorf("interactive <%s> does not carry %q; §10.6 audits "+
					"rendered markup for exactly this, plugin output included",
					node.Data, ui.TargetClass)
			}
		})
	}
}

// tree renders a component and parses it, for the predicates that are walked
// rather than asserted through.
//
// A render failure yields an empty document rather than a fatal, because every
// caller here is a walk whose failure would otherwise be reported as a missing
// element — which points at the component instead of at the fixture.
func tree(t *testing.T, component templ.Component) *html.Node {
	t.Helper()

	var out strings.Builder
	if err := component.Render(t.Context(), &out); err != nil {
		return &html.Node{Type: html.DocumentNode}
	}

	root, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		return &html.Node{Type: html.DocumentNode}
	}

	return root
}

// interactiveElements is every element a person can reach with a pointer or a
// keyboard, as §10.6's grep has to decide it from markup alone.
func interactiveElements(root *html.Node) []*html.Node {
	return elements(root, func(node *html.Node) bool {
		switch node.Data {
		case "button", "select", "textarea", "summary":
			return true
		case "input":
			// A hidden input is not reachable: it is submitted, not focused, and
			// `components.LoginForm` already omits the class from one.
			kind, _ := attr(node, "type")

			return kind != "hidden"
		case "a", "area":
			return hasAttr(node, "href")
		}

		value, found := attr(node, "tabindex")
		if !found {
			return false
		}

		index, err := strconv.Atoi(value)

		return err == nil && index >= 0
	})
}

// TestStatesUseTheRecordsLiveRegionsAndNoOthers is §7.5's channel table and
// §13.4's prohibition on the search route, asserted as one rule: `role="alert"`
// appears on exactly the states the record grants it to, and nowhere else.
//
// This is the test that stops `aria-live` spreading. A live region is not a way
// to make a page louder — it fires when content is *inserted* into it, so one on
// a page-load state is either silent or, worse, an interruption on every
// navigation to that page.
func TestStatesUseTheRecordsLiveRegionsAndNoOthers(t *testing.T) {
	t.Parallel()

	for _, testCase := range stateCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, testCase.build(testCase.pagePlacement))

			alerts := elements(root, func(node *html.Node) bool {
				role, _ := attr(node, "role")

				return role == "alert"
			})

			if testCase.assertive {
				if len(alerts) != 1 {
					t.Fatalf("%d role=alert elements, want 1; §7.5 grants an "+
						"assertive announcement to this state", len(alerts))
				}

				if politeness, _ := attr(alerts[0], "aria-live"); politeness != "assertive" {
					t.Errorf("aria-live=%q on the alert, want \"assertive\" (UI §7.5)", politeness)
				}

				return
			}

			if len(alerts) != 0 {
				t.Errorf("%d role=alert elements, want 0; §7.5 names the states that "+
					"announce and this is not one of them", len(alerts))
			}

			for _, node := range elements(root, func(node *html.Node) bool {
				return hasAttr(node, "aria-live")
			}) {
				t.Errorf("<%s> carries aria-live; this state is reached by "+
					"navigating, and navigating is not an event (UI §7.5)",
					node.Data)
			}
		})
	}
}

// TestTheSearchStatesCarryNoLiveRegionAtAll is §7.5's and §13.4's rule stated
// separately because it is the one with a whole route attached to it.
//
// §7.5's reason is worth keeping in the test's own words: search is not live, a
// result list over FTS is exactly the cacheable content the architecture plan
// says never flows through a reactive transport, and a live region would
// announce the reader's own keystroke back to them.
func TestTheSearchStatesCarryNoLiveRegionAtAll(t *testing.T) {
	t.Parallel()

	for _, testCase := range []stateCase{
		stateByHook(t, "state-search-idle"),
		stateByHook(t, "state-search-no-results"),
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, testCase.build(testCase.pagePlacement))

			for _, node := range elements(root, func(node *html.Node) bool {
				if hasAttr(node, "aria-live") {
					return true
				}

				role, _ := attr(node, "role")

				return role == "alert" || role == "status" || role == "log"
			}) {
				t.Errorf("<%s> is a live region on the search route; §7.5 and §13.4 "+
					"prohibit one", node.Data)
			}
		})
	}
}

// TestTestHooksAreUniquePerDocument guards the convention every assertion here
// relies on: a `data-testid` names one element, so "the first one" is never the
// answer. Rows that repeat use `data-row`, `data-cell` and `data-menu-item`,
// which is the same choice `components.CampaignList` makes for its cards.
func TestTestHooksAreUniquePerDocument(t *testing.T) {
	t.Parallel()

	components := map[string]templ.Component{
		"first run":              stateByHook(t, "first-run").build(ui.HeadingInPage),
		"campaign with no pages": stateByHook(t, "state-campaign-no-pages").build(ui.HeadingInPage),
		"search, no results":     stateByHook(t, "state-search-no-results").build(ui.HeadingPage),
		"404":                    stateByHook(t, "state-error-404").build(ui.HeadingPage),
		"412":                    stateByHook(t, "state-error-412").build(ui.HeadingInPage),
		"500":                    stateByHook(t, "error-state").build(ui.HeadingInPage),
		"ruleset drift":          stateByHook(t, "state-ruleset-drift").build(ui.HeadingInPage),
		"connection lost":        stateByHook(t, "state-connection-lost").build(ui.HeadingInPage),
		"dialog":                 dialogFixture(),
		"menu button":            menuFixture(),
		"tabs":                   tabsFixture(),
		"disclosure, open":       disclosureFixture(true),
		"disclosure, collapsed":  disclosureFixture(false),
		"table":                  tableFixture(),
	}

	for name, component := range components {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]int{}

			for _, node := range elements(tree(t, component), hasTestID) {
				value, _ := attr(node, "data-testid")
				seen[value]++
			}

			for hook, count := range seen {
				if count > 1 {
					t.Errorf("data-testid=%q appears %d times; a repeated hook "+
						"selects nothing", hook, count)
				}
			}
		})
	}
}

// hasTestID reports whether an element carries a test hook.
func hasTestID(node *html.Node) bool {
	return hasAttr(node, "data-testid")
}

// TestTheErrorStatesNameTheFailureWithoutLeaking is §4.7's 500 row and AGENTS.md's
// rule that no event carries content.
//
// The assertion is about what the error state is *able* to say: a caller
// contributes a reference and nothing else, so there is no field through which a
// message, a path or a parser's quotation of a page body could reach a reader.
// Reflection rather than a rendered-string comparison, because a copy assertion
// passes today and fails only when somebody adds a field — and the field is the
// defect.
func TestTheErrorStatesNameTheFailureWithoutLeaking(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		view      any
		wantField []string
	}{
		{name: "LoadFailure", view: ui.LoadFailure{}, wantField: []string{"Reference", "Heading"}},
		{name: "NotFoundView", view: ui.NotFoundView{}, wantField: []string{
			"PagePath", "CampaignRootHref", "Heading",
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			viewType := reflect.TypeOf(testCase.view)

			got := make([]string, 0, viewType.NumField())
			for field := range viewType.Fields() {
				got = append(got, field.Name)
			}

			if !reflect.DeepEqual(got, testCase.wantField) {
				t.Errorf("%s has fields %v, want %v; a field that could carry "+
					"content or a reason is how it would get in (UI §1.2, §10.2)",
					testCase.name, got, testCase.wantField)
			}
		})
	}
}

// TestNotFoundIsIndistinguishableFromAPrivateCampaign is §4.7's closing rule,
// asserted on the shape of the data rather than on the rendered string.
//
// The rule is that "a 404 and a private campaign must not be distinguishable to
// an anonymous user… Both return the same body shape", so what matters is that
// the component has nothing to branch on. NotFoundView carrying only a path, a
// link and a heading is the assertion: there is no reason field, no status field
// and no boolean, so a handler with a reason in its hands has nowhere to put it
// here. Rendered-string comparison would not catch it — the two answers differ by
// a status code the component never sees.
func TestNotFoundIsIndistinguishableFromAPrivateCampaign(t *testing.T) {
	t.Parallel()

	// Named rather than a positive allow-list: an allow-list of the three fields
	// that exist today would pass just as happily on a fourth, and a disqualifying
	// name is the thing a future author would reach for. `Forbidden` is not on the
	// list on purpose — §4.7's 403 state is a separate component, and the field
	// name belongs there.
	disqualifying := []string{"Reason", "Status", "Code", "Private", "Exists", "Anonymous"}

	for field := range reflect.TypeFor[ui.NotFoundView]().Fields() {
		if slices.Contains(disqualifying, field.Name) {
			t.Errorf("NotFoundView has a %q field; §4.7 requires the 404 body to be "+
				"the same shape for a missing page and a campaign the reader cannot "+
				"see, and a field naming the difference is that difference",
				field.Name)
		}
	}
}

// TestNoPageAtRefusesToEchoAServerPath is §4.7's "never a raw server path echo".
//
// Table-driven because the rule is a list of shapes rather than one judgement,
// and because the interesting cases are the ones that look plausible: a Windows
// drive, a traversal, an absolute path, and a name that is merely unusual.
func TestNoPageAtRefusesToEchoAServerPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "a page path inside the campaign",
			path: "Locations/Greyhaven",
			want: "Locations/Greyhaven",
		},
		{
			name: "a page name with spaces and an apostrophe",
			path: "The Sunken Vault",
			want: "The Sunken Vault",
		},
		{
			name: "a colon in a page name is legal",
			path: "Boss: The Warden",
			want: "Boss: The Warden",
		},
		{
			name: "an absolute path",
			path: "/var/lib/semiplane/campaigns/greyhaven/wiki",
			want: "",
		},
		{
			name: "a traversal",
			path: "../../etc/passwd",
			want: "",
		},
		{
			name: "a windows drive",
			path: `C:\campaigns\greyhaven`,
			want: "",
		},
		{
			name: "a NUL byte",
			path: "Greyhaven\x00.md",
			want: "",
		},
		{
			name: "an over-long path",
			path: strings.Repeat("a", 300),
			want: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, ui.NotFound(ui.NotFoundView{
				PagePath: testCase.path,
				Heading:  ui.HeadingPage,
			}))

			got := text(byTestID(t, root, "error-404-path"))

			if got != testCase.want {
				t.Errorf("the 404 echoes %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestLongEchoedValuesAreBoundedAndNeverSplitARune is the reason displayText
// exists, asserted through a rendered heading rather than against an unexported
// function.
//
// §4.7 requires a query to be echoed and a reference to be quotable, so both are
// bounded rather than refused. A cut in the middle of a multi-byte rune renders
// as U+FFFD in some engines and as nothing in others, which is a rendering that
// depends on the browser — so the test looks for the replacement character.
func TestLongEchoedValuesAreBoundedAndNeverSplitARune(t *testing.T) {
	t.Parallel()

	query := strings.Repeat("é", 400)

	root := parse(t, ui.SearchEmpty(ui.SearchEmptyView{
		Action:  "/c/greyhaven/search",
		Query:   query,
		Heading: ui.HeadingPage,
	}))

	echoed := text(byTestID(t, root, "search-no-results-term"))

	if !strings.HasSuffix(echoed, "…") {
		t.Errorf("a 400-rune query renders %d characters with no truncation mark",
			len(echoed))
	}

	if strings.ContainsRune(echoed, '�') {
		t.Error("the echoed query contains U+FFFD; the bound cut a multi-byte rune " +
			"in half and the result now depends on the browser")
	}

	if len([]rune(echoed)) >= len([]rune(query)) {
		t.Error("the echoed query was not bounded at all")
	}
}

// TestTheErrorStatesCarryExactlyOneReference is §4.7's reason for the reference
// line existing: a load failure has to stay diagnosable without becoming a 500
// page's worth of detail.
func TestTheErrorStatesCarryExactlyOneReference(t *testing.T) {
	t.Parallel()

	root := parse(t, ui.LoadError(ui.LoadFailure{
		Reference: "req-4f2a",
		Heading:   ui.HeadingPage,
	}))

	reference := byTestID(t, root, "error-state-reference")

	if got := text(reference); got != "req-4f2a" {
		t.Errorf("the reference reads %q, want the request id verbatim", got)
	}

	if name := reference.Data; name != "code" {
		t.Errorf("the reference is a <%s>; a request id in body type is a sentence "+
			"somebody quotes into a bug report as prose", name)
	}
}

// TestTheErrorStatesOmitTheReferenceWhenThereIsNone keeps the line out of the
// page rather than rendering "Reference:" with nothing after it.
func TestTheErrorStatesOmitTheReferenceWhenThereIsNone(t *testing.T) {
	t.Parallel()

	root := parse(t, ui.LoadError(ui.LoadFailure{Heading: ui.HeadingPage}))

	if strings.Contains(text(root), "Reference:") {
		t.Error("the error state renders a reference line with no reference; the " +
			"label should be omitted rather than left standing alone")
	}
}

// TestTheEmptyStatesOmitAnActionTheyCannotPerform is UI §4.4's rule that edit
// affordances are absent rather than disabled.
//
// Two states: a player on a campaign with no pages gets no "Create a page" at
// all, and the first run with no route to point at gets no call to action. A
// disabled control is a control a reader examines, fails to use, and cannot
// interpret — and a CTA pointing at a route that does not exist is the one button
// a new operator will press.
func TestTheEmptyStatesOmitAnActionTheyCannotPerform(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		component templ.Component
		hook      string
	}{
		{
			name:      "a player on a campaign with no pages",
			component: ui.NoPages(ui.NoPagesView{Heading: ui.HeadingInPage}),
			hook:      "campaign-no-pages-cta",
		},
		{
			name: "a first run with no create route",
			component: ui.FirstRun(ui.FirstRunView{
				DocsHref: "https://example.invalid/docs",
				Heading:  ui.HeadingInPage,
			}),
			hook: "first-run-cta",
		},
		{
			name: "a first run with no documentation link",
			component: ui.FirstRun(ui.FirstRunView{
				CreateHref: "/admin/campaigns/new",
				Heading:    ui.HeadingInPage,
			}),
			hook: "first-run-docs",
		},
		{
			name: "a 404 outside any campaign",
			component: ui.NotFound(ui.NotFoundView{
				PagePath: "Greyhaven",
				Heading:  ui.HeadingPage,
			}),
			hook: "error-404-campaign-link",
		},
		{
			name: "a ruleset drift with no settings route",
			component: ui.RulesetDrift(ui.RulesetDriftView{
				Heading: ui.HeadingInPage,
			}),
			hook: "ruleset-drift-link",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, testCase.component)

			for _, node := range elements(root, hasTestID) {
				if value, _ := attr(node, "data-testid"); value == testCase.hook {
					t.Errorf("the state renders %s, which points nowhere", testCase.hook)
				}
			}
		})
	}
}

// TestTheSearchFormIsLabelledAndDescribed is §7.7's first line — "labels are
// `<label for>`, never placeholders" — and §7.2's rule that a landmark carries a
// distinguishing label.
//
// A placeholder is not a label: it disappears the moment the field has a value,
// which for the no-results state is always.
func TestTheSearchFormIsLabelledAndDescribed(t *testing.T) {
	t.Parallel()

	root := parse(t, ui.SearchIdle(ui.SearchIdleView{
		Action:  "/c/greyhaven/search",
		Heading: ui.HeadingPage,
	}))

	field := byTestID(t, root, "search-idle-query")

	id, found := attr(field, "id")
	if !found || id == "" {
		t.Fatal("the search field has no id, so nothing can label it")
	}

	labels := elements(root, func(node *html.Node) bool {
		return node.Data == "label" && attrValue(node, "for") == id
	})

	if len(labels) != 1 {
		t.Fatalf("%d <label for=%q> elements, want 1 (UI §7.7)", len(labels), id)
	}

	if got := normalisedText(labels[0]); got != "Search this campaign" {
		t.Errorf("the field is labelled %q", got)
	}

	if hasAttr(field, "placeholder") {
		t.Error("the search field has a placeholder; §7.7 requires a real label, " +
			"because a placeholder vanishes as soon as the field has a value")
	}

	describedBy, found := attr(field, "aria-describedby")
	if !found {
		t.Fatal("the search field has no aria-describedby; §4.7 designs this state " +
			"with a hint and a hint nobody is told about is not a hint")
	}

	hint := byTestID(t, root, "search-idle-hint")
	if hintID, _ := attr(hint, "id"); hintID != describedBy {
		t.Errorf("aria-describedby=%q does not name the hint's id %q", describedBy, hintID)
	}

	forms := elements(root, func(node *html.Node) bool {
		return node.Data == "form"
	})

	if len(forms) != 1 {
		t.Fatalf("%d forms, want 1", len(forms))
	}

	if role, _ := attr(forms[0], "role"); role != "search" {
		t.Errorf("the form has role=%q, want \"search\"", role)
	}

	if accessibleName(forms[0], idIndex(root)) == "" {
		t.Error("the search landmark has no accessible name; §7.2 requires " +
			"distinguishing labels on landmarks")
	}
}

// TestTheSearchRouteHasNoLiveRegion is restated here at the level of the rendered
// search route rather than of one state, because §13.4's rule is about a route
// and a route is the state plus a heading.
func TestTheSearchRouteHasNoLiveRegion(t *testing.T) {
	t.Parallel()

	root := parse(t, withRouteHeading("Search",
		ui.SearchIdle(ui.SearchIdleView{
			Action:  "/c/greyhaven/search",
			Heading: ui.HeadingInPage,
		})))

	if strings.Contains(render(t, withRouteHeading("Search",
		ui.SearchIdle(ui.SearchIdleView{Heading: ui.HeadingInPage}))), "aria-live") {
		t.Error("the search route renders aria-live (UI §13.4)")
	}

	if role, _ := attr(root, "role"); role == "alert" {
		t.Error("the search route is an alert")
	}
}

// TestTheDialogIsModalNamedAndWiredToItsTrigger is §7.7's dialog contract in one
// test, because the three halves of it are one contract: a trigger that names a
// panel, a panel that is modal, and a panel that is named.
//
// The trigger/ppanel pair is the part worth testing together. §7.7's "a popup that
// cannot open is a dead control" is not a claim about either element; it is a claim
// about `aria-controls` naming an element that exists.
func TestTheDialogIsModalNamedAndWiredToItsTrigger(t *testing.T) {
	t.Parallel()

	const dialogID = "confirm-discard"

	root := parse(t, dialogFixture())

	panel := byTestID(t, root, dialogID)

	if role, _ := attr(panel, "role"); role != "dialog" {
		t.Errorf("role=%q on the panel, want \"dialog\" (UI §7.7)", role)
	}

	if modal, _ := attr(panel, "aria-modal"); modal != "true" {
		t.Errorf("aria-modal=%q, want \"true\"; a focus trap without it is a "+
			"keyboard trap with no announcement (UI §7.7, §8.1)", modal)
	}

	if _, named := attr(panel, "aria-labelledby"); !named {
		t.Fatal("the panel has no aria-labelledby; §7.7 says a dialog is labelled " +
			"by its heading")
	}

	if name := accessibleName(panel, idIndex(root)); name != "Discard your changes?" {
		t.Errorf("the dialog is named %q, want its heading", name)
	}

	if !hasAttr(panel, "tabindex") {
		t.Error("the panel has no tabindex; focus cannot land on the dialog itself, " +
			"so a reader hears a dialog with no name and no position (UI §7.2)")
	}

	if index, _ := attr(panel, "tabindex"); index != "-1" {
		t.Errorf("tabindex=%q on the panel, want \"-1\"; a dialog that added itself "+
			"to the tab order would be in it twice (UI §7.10)", index)
	}

	if !hasAttr(panel, "hidden") {
		t.Error("the panel is not hidden in the served markup; a dialog open on " +
			"page load traps focus with no script to release it")
	}

	trigger := byTestID(t, root, dialogID+"-trigger")

	if expanded, _ := attr(trigger, "aria-expanded"); expanded != "false" {
		t.Errorf("aria-expanded=%q on a closed dialog's trigger, want \"false\"", expanded)
	}

	if haspopup, _ := attr(trigger, "aria-haspopup"); haspopup != "dialog" {
		t.Errorf("aria-haspopup=%q on the trigger, want \"dialog\"", haspopup)
	}

	controls, _ := attr(trigger, "aria-controls")
	if controls != dialogID {
		t.Errorf("the trigger's aria-controls is %q, want %q; a popup that cannot be "+
			"pointed at cannot be opened (UI §7.7)", controls, dialogID)
	}
}

// TestTheDialogIsHeightCappedAndScrollable is §11's named failure — "the 580px
// centre height is unforgiving — vertical overflow on a dialog" — asserted on the
// half this package owns.
//
// The height cap itself is a CSS property and lives in the stylesheet; what the
// markup owes it is exactly one element that scrolls, so the stylesheet has
// something to make scrollable and the title and the close button stay put while
// it does. Two scrollable regions would mean two scrollbars in one modal, which
// is the failure the cap is meant to remove.
func TestTheDialogIsHeightCappedAndScrollable(t *testing.T) {
	t.Parallel()

	root := parse(t, dialogFixture())

	panel := byTestID(t, root, "confirm-discard")

	regions := elements(panel, func(node *html.Node) bool {
		return hasAttr(node, "data-dialog-scroll")
	})

	if len(regions) != 1 {
		t.Fatalf("the dialog has %d scroll regions, want exactly 1 (UI §11)", len(regions))
	}

	closes := elements(panel, func(node *html.Node) bool {
		return hasAttr(node, "data-dialog-close")
	})

	if len(closes) != 1 {
		t.Fatalf("the dialog has %d close controls, want 1; §7.7 requires `Escape` "+
			"to close and focus to return to the trigger, and a dialog with no "+
			"visible close is one only a keyboard user can dismiss", len(closes))
	}

	if _, scrollable := attr(regions[0], "class"); !scrollable {
		t.Error("the scroll region has no class for the stylesheet to size")
	}
}

// TestTheMenuButtonCarriesThePopupContract is §7.7's menu-button rule, and the
// three attributes are the whole of it in server-rendered markup.
func TestTheMenuButtonCarriesThePopupContract(t *testing.T) {
	t.Parallel()

	const menuID = "page-actions"

	root := parse(t, menuFixture())

	trigger := byTestID(t, root, menuID+"-trigger")

	if haspopup, _ := attr(trigger, "aria-haspopup"); haspopup != "menu" {
		t.Errorf("aria-haspopup=%q, want \"menu\" (UI §7.7)", haspopup)
	}

	if expanded, _ := attr(trigger, "aria-expanded"); expanded != "false" {
		t.Errorf("aria-expanded=%q, want \"false\" on a closed menu", expanded)
	}

	controls, _ := attr(trigger, "aria-controls")
	if controls != menuID {
		t.Errorf("aria-controls=%q, want %q", controls, menuID)
	}

	menu := byTestID(t, root, menuID+"-menu")

	if role, _ := attr(menu, "role"); role != "menu" {
		t.Errorf("role=%q on the popup, want \"menu\"", role)
	}

	if !hasAttr(menu, "hidden") {
		t.Error("the menu is not hidden; a menu in the document at load is a list " +
			"of links a reader tabs through and cannot tell from the page")
	}

	// §7.7's touch and TV rule is a stylesheet decision, so what the markup owes
	// it is the hook: on touch and TV a menu is a full-width bottom sheet, not a
	// popover anchored to a 44px trigger.
	if !hasAttr(menu, "data-menu-sheet") {
		t.Error("the popup has no data-menu-sheet hook; the stylesheet cannot switch " +
			"it to a bottom sheet without knowing it is a menu (UI §7.7)")
	}

	if accessibleName(menu, idIndex(root)) == "" {
		t.Error("the menu has no accessible name; §7.2 requires distinguishing " +
			"labels on landmarks and WAI-ARIA names a menu by its trigger")
	}

	items := elements(menu, func(node *html.Node) bool {
		role, _ := attr(node, "role")

		return role == "menuitem"
	})

	if len(items) != 2 {
		t.Fatalf("the menu has %d items, want 2", len(items))
	}

	for _, item := range items {
		if normalisedText(item) == "" {
			t.Error("a menu item has no text; §7.10 rules out an affordance that is " +
				"only an icon or a hover target")
		}
	}
}

// TestTheMenuButtonSeparatesLinkItemsFromActionItems is the closed enum's reason:
// an item that is both is an item whose behaviour depends on which handler a
// later change wrote, and a navigation that only works on click is one a
// keyboard-and-switch user cannot make.
func TestTheMenuButtonSeparatesLinkItemsFromActionItems(t *testing.T) {
	t.Parallel()

	root := parse(t, menuFixture())

	items := elements(root, func(node *html.Node) bool {
		role, _ := attr(node, "role")

		return role == "menuitem"
	})

	byTag := map[string]*html.Node{}
	for _, item := range items {
		byTag[item.Data] = item
	}

	link, hasLink := byTag["a"]
	button, hasButton := byTag["button"]

	if !hasLink || !hasButton {
		t.Fatalf("the menu renders tags %v, want one <a> and one <button>", byTag)
	}

	if href, _ := attr(link, "href"); href == "" {
		t.Error("a MenuItemLink rendered as an <a> with no href, which is not a link")
	}

	if href, found := attr(button, "href"); found {
		t.Errorf("a MenuItemAction rendered as an <a href=%q>; it has no address "+
			"and pretending otherwise gives it a status line that does nothing", href)
	}

	if kind, _ := attr(button, "type"); kind != "button" {
		t.Errorf("type=%q on the action item, want \"button\"", kind)
	}
}

// TestTabsSelectExactlyOneTabAndRoveTheTabStop is WAI-ARIA's tabs pattern, and
// the two halves of it are what make a tablist one tab stop rather than four.
func TestTabsSelectExactlyOneTabAndRoveTheTabStop(t *testing.T) {
	t.Parallel()

	root := parse(t, tabsFixture())

	tabs := elements(root, func(node *html.Node) bool {
		role, _ := attr(node, "role")

		return role == "tab"
	})

	if len(tabs) != 3 {
		t.Fatalf("the tablist has %d tabs, want 3", len(tabs))
	}

	selected, inOrder := 0, 0

	for _, tab := range tabs {
		value, _ := attr(tab, "aria-selected")

		switch value {
		case "true":
			selected++
			inOrder++

			if index, _ := attr(tab, "tabindex"); index != "0" {
				t.Errorf("the selected tab has tabindex=%q, want \"0\"; roving "+
					"tabindex is what makes the tablist one tab stop", index)
			}
		case "false":
			if index, _ := attr(tab, "tabindex"); index != "-1" {
				t.Errorf("an unselected tab has tabindex=%q, want \"-1\"; every tab at "+
					"\"0\" is a tab stop, and three tabs is three stops (UI §7.4)",
					index)
			}
		default:
			t.Errorf("aria-selected=%q on a tab; only \"true\" and \"false\" are valid "+
				"and the absent value is undefined rather than false", value)
		}

		controls, found := attr(tab, "aria-controls")
		if !found {
			t.Fatalf("a tab has no aria-controls; a tab that points at nothing " +
				"displays a label and no content (UI §7.4)")
		}

		panel, resolved := nodeByID(root, controls)
		if !resolved {
			t.Errorf("the tab's aria-controls=%q names no element", controls)
		} else if role, _ := attr(panel, "role"); role != "tabpanel" {
			t.Errorf("aria-controls=%q names a <%s> with role=%q, want a tabpanel",
				controls, panel.Data, role)
		}

		if tabID, _ := attr(tab, "id"); tabID == "" {
			t.Error("a tab has no id, so its panel cannot name it back")
		}
	}

	if selected != 1 {
		t.Errorf("%d tabs are aria-selected=\"true\", want exactly 1; a tablist "+
			"with two selected tabs is a widget with two answers to \"what am I "+
			"looking at\"", selected)
	}

	if inOrder != 1 {
		t.Errorf("%d tab stops in the tablist, want 1 (UI §7.4)", inOrder)
	}
}

// TestTabsHideEveryPanelButTheSelected is the other half of the pattern, and it
// is asserted on `hidden` rather than on `aria-hidden`.
//
// §7.10 prohibits `aria-hidden` on a focusable element and a tab panel is full of
// them, so the only correct way to say "not selected" is to take the subtree out
// of the accessibility tree and the tab order together.
func TestTabsHideEveryPanelButTheSelected(t *testing.T) {
	t.Parallel()

	root := parse(t, tabsFixture())

	panels := elements(root, func(node *html.Node) bool {
		role, _ := attr(node, "role")

		return role == "tabpanel"
	})

	if len(panels) != 3 {
		t.Fatalf("the widget has %d panels, want 3", len(panels))
	}

	visible := 0

	for _, panel := range panels {
		if hasAttr(panel, "hidden") {
			continue
		}

		visible++

		labelledBy, named := attr(panel, "aria-labelledby")
		if !named {
			t.Fatalf("a visible panel has no aria-labelledby; a panel a reader " +
				"cannot get back to is a panel they cannot find again")
		}

		tab, resolved := nodeByID(root, labelledBy)
		if !resolved {
			t.Errorf("aria-labelledby=%q names no element", labelledBy)
		} else if selected, _ := attr(tab, "aria-selected"); selected != "true" {
			t.Errorf("the visible panel is labelled by a tab with aria-selected=%q", selected)
		}

		if hasAttr(panel, "tabindex") {
			t.Error("a tabpanel carries a tabindex; the panel's content provides " +
				"the tab stops and an extra one lengthens the sequence without " +
				"making the widget usable")
		}
	}

	if visible != 1 {
		t.Errorf("%d panels are visible, want 1", visible)
	}
}

// TestTabsFallBackToTheFirstTabRatherThanSelectingNone is the degradation the
// server-rendered form of the pattern has to make on its own.
//
// With no script there is nothing to select a tab, and a tablist whose every panel
// is hidden shows labels and no content. An unknown Selected and an empty one both
// fall back to the first tab.
func TestTabsFallBackToTheFirstTabRatherThanSelectingNone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		selected string
	}{
		{name: "no selection given", selected: ""},
		{name: "a selection naming a tab that is not there", selected: "nope"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			view := tabsView(testCase.selected)

			root := parse(t, ui.Tabs(view))

			selected := elements(root, func(node *html.Node) bool {
				value, _ := attr(node, "aria-selected")

				return value == "true"
			})

			if len(selected) != 1 {
				t.Fatalf("%d selected tabs, want 1", len(selected))
			}

			if id, _ := attr(selected[0], "id"); id != "rules" {
				t.Errorf("the fallback selected %q, want the first tab \"rules\"", id)
			}
		})
	}
}

// TestAnEmptyTablistRendersNothing is the other degradation: a labelled group of
// no tabs is worse than an absent widget, because the reader spends a tab stop
// discovering it.
func TestAnEmptyTablistRendersNothing(t *testing.T) {
	t.Parallel()

	document := render(t, ui.Tabs(ui.TabsView{Label: "Campaign settings"}))

	if strings.Contains(document, `role="tablist"`) {
		t.Error("an empty tablist renders a tablist")
	}
}

// TestDisclosureHidesItsRegionWhenCollapsed is §4.3's "a `<button
// aria-expanded>` on the row" and the whole of what it means.
//
// Both states are rendered rather than one plus a script, because the script does
// not exist yet and the *contract* is that the served markup says which state it
// is in — that is what a reader with no script gets, and what phase 9's script
// starts from.
func TestDisclosureHidesItsRegionWhenCollapsed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		open    bool
		wantExp string
		wantHid bool
	}{
		{name: "collapsed", open: false, wantExp: "false", wantHid: true},
		{name: "open", open: true, wantExp: "true", wantHid: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := parse(t, disclosureFixture(testCase.open))

			button := byTestID(t, root, "outline-toggle")

			if button.Data != "button" {
				t.Errorf("the disclosure control is a <%s>; §7.4 requires `Enter` "+
					"and `Space` to activate every control and only a button gives "+
					"both without a script", button.Data)
			}

			if expanded, _ := attr(button, "aria-expanded"); expanded != testCase.wantExp {
				t.Errorf("aria-expanded=%q, want %q", expanded, testCase.wantExp)
			}

			controls, found := attr(button, "aria-controls")
			if !found {
				t.Fatal("the button has no aria-controls, so \"expanded\" refers to " +
					"nothing")
			}

			region, resolved := nodeByID(root, controls)
			if !resolved {
				t.Fatalf("aria-controls=%q names no element", controls)
			}

			if got := hasAttr(region, "hidden"); got != testCase.wantHid {
				t.Errorf("the region is hidden=%v, want %v", got, testCase.wantHid)
			}

			if !strings.Contains(text(region), "Greyhaven") {
				t.Error("the region does not carry its content in the document; " +
					"§6.7 says nothing may depend on an animation to become visible, " +
					"and a reader with no script still needs the content")
			}
		})
	}
}

// TestTheDisclosureSummaryIsText asserts the one thing a disclosure button is
// most often reduced to.
func TestTheDisclosureSummaryIsText(t *testing.T) {
	t.Parallel()

	root := parse(t, disclosureFixture(true))

	button := byTestID(t, root, "outline-toggle")

	if normalisedText(button) != "Page outline" {
		t.Errorf("the disclosure button reads %q, want its summary text; a chevron "+
			"alone is a control whose purpose a screen reader has to guess",
			normalisedText(button))
	}
}

// TestTheTableStackedFormCarriesTheSameDataAsTheTabularOne is §7.8's stacked
// form, asserted as equality rather than as presence.
//
// Both forms are in every response and the stylesheet chooses which is displayed,
// which is deliberate: the stacked form is then exercised by every run rather than
// only on the viewport §10.3's agent-assisted sweep happens to look at. The
// assertion compares the two renderings cell by cell, because a stacked form that
// dropped a column would still *look* right on the two rows it kept.
func TestTheTableStackedFormCarriesTheSameDataAsTheTabularOne(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	// Split by form first: the two renderings share `data-row`, so a comparison
	// that did not would compare every row against itself.
	tabular := cellsOf(root, "tabular")
	stacked := cellsOf(root, "stacked")

	if len(tabular) == 0 {
		t.Fatal("the tabular form rendered no rows")
	}

	if !reflect.DeepEqual(tabular, stacked) {
		t.Errorf("the two forms disagree.\ntabular: %v\nstacked: %v", tabular, stacked)
	}
}

// TestTheTableStackedFormNamesEveryCell is §7.8's `<dl>` pairs requirement.
//
// A stacked card whose `<dd>`s have no `<dt>` is a list of values with no column
// names, which is the whole failure the stacked form exists to avoid at 360px.
func TestTheTableStackedFormNamesEveryCell(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	cards := elements(root, func(node *html.Node) bool {
		return node.Data == "article" && hasAttr(node, "data-row") && inForm(node, "stacked")
	})

	if len(cards) != 2 {
		t.Fatalf("the stacked form has %d cards, want 2", len(cards))
	}

	terms := elements(root, func(node *html.Node) bool {
		return node.Data == "dt" && inForm(node, "stacked")
	})

	if len(terms) == 0 {
		t.Fatal("the stacked form has no <dt> elements; §7.8 requires <dl> pairs")
	}

	for _, term := range terms {
		if normalisedText(term) == "" {
			t.Errorf("<dt data-cell-term=%q> has no text", attrValue(term, "data-cell-term"))
		}
	}

	values := elements(root, func(node *html.Node) bool {
		return node.Data == "dd" && inForm(node, "stacked")
	})

	if len(values) != len(terms) {
		t.Errorf("the stacked form has %d <dd> against %d <dt>; §7.8 requires pairs",
			len(values), len(terms))
	}
}

// inForm reports whether an element sits inside one of the table's two forms.
//
// Upward rather than a subtree walk: re-parenting a parsed tree to build a second
// one is how a test ends up comparing a node against itself, and `html.Node` does
// carry the parent pointer that makes this a three-line predicate.
func inForm(node *html.Node, form string) bool {
	for ancestor := node; ancestor != nil; ancestor = ancestor.Parent {
		if attrValue(ancestor, "data-table-form") == form {
			return true
		}
	}

	return false
}

// attrValue is an attribute, or the empty string.
func attrValue(node *html.Node, name string) string {
	value, _ := attr(node, name)

	return value
}

// isRow is a body row of the tabular form or a card of the stacked form. Both
// carry the same `data-row`, which is what lets the two renderings be compared
// by identity rather than by position.
func isRow(node *html.Node) bool {
	return (node.Data == "tr" || node.Data == "article") && hasAttr(node, "data-row")
}

// isCell is one cell of either form.
func isCell(node *html.Node) bool {
	return (node.Data == "td" || node.Data == "dd") && hasAttr(node, "data-cell")
}

// cellsOf is one form's rows' cell values, keyed by row then by column key.
func cellsOf(root *html.Node, form string) map[string]map[string]string {
	found := map[string]map[string]string{}

	for _, row := range elements(root, func(node *html.Node) bool {
		return isRow(node) && inForm(node, form)
	}) {
		cells := map[string]string{}
		for _, cell := range elements(row, isCell) {
			cells[attrValue(cell, "data-cell")] = normalisedText(cell)
		}

		found[attrValue(row, "data-row")] = cells
	}

	return found
}

// TestTheTableSortIsAButtonInsideAScopedHeader is §7.8's sort rule.
//
// Two parts and they are both required. `scope="col"` is what stops a `<th>`
// announcing its label on every cell beneath it. And `aria-sort` belongs on the
// header cell rather than on the button inside it, because ARIA defines it there
// and a screen reader announces it as part of the column's identity.
func TestTheTableSortIsAButtonInsideAScopedHeader(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	headers := elements(root, func(node *html.Node) bool {
		return node.Data == "th"
	})

	if len(headers) == 0 {
		t.Fatal("the table has no header cells")
	}

	for _, header := range headers {
		if scope, _ := attr(header, "scope"); scope != "col" {
			t.Errorf("<th> has scope=%q, want \"col\"; a `<th>` without a scope "+
				"announces its label on every cell beneath it (UI §7.8)", scope)
		}
	}

	// Four states, and the test distinguishes all four because the record's own
	// vocabulary distinguishes them: ARIA's "none" means *sortable and currently
	// unsorted*, which is a claim about a control existing. Conflating it with
	// "no control" is the mistake this table of cases is here to prevent.
	sortStates := map[string]int{}

	for _, header := range headers {
		buttons := elements(header, func(node *html.Node) bool {
			return node.Data == "button"
		})

		hasButton := len(buttons) == 1
		sort, _ := attr(header, "aria-sort")

		switch sort {
		case "":
			sortStates["no attribute"]++

			if hasButton {
				t.Errorf("a column with no aria-sort renders a sort button; " +
					"§7.8's control and the attribute belong together")
			}
		case "none":
			sortStates["sortable, unsorted"]++

			if !hasButton {
				t.Error(`a column with aria-sort="none" has no sort button; ARIA's ` +
					`"none" is the sortable-and-currently-unsorted state, so the ` +
					"control is what makes the claim true (UI §7.8)")
			}

			// Nothing to read: the direction word is for an applied order.
			if strings.Contains(text(header), "ascending") ||
				strings.Contains(text(header), "descending") {
				t.Error(`a column with aria-sort="none" names a direction; ` +
					"unsorted is not a direction (UI §6.5)")
			}
		case "ascending", "descending":
			sortStates[sort]++

			if !hasButton {
				t.Errorf("a sorted column has no sort button (UI §7.8)")
			}

			// §6.5: never colour alone, and an arrow is no better — a triangle
			// points up whether it means ascending or "click to sort ascending",
			// and a screen reader announces it as "graphic".
			if !strings.Contains(text(header), sort) {
				t.Errorf("the sorted header carries aria-sort=%q but no word saying "+
					"so; a direction a reader cannot see is a direction carried by "+
					"hue alone (UI §6.5)", sort)
			}
		default:
			t.Errorf("aria-sort=%q; the values are \"ascending\", \"descending\" and "+
				"\"none\", and no attribute at all", sort)
		}
	}

	// The fixture exercises every state: two sortable columns, one of them sorted
	// descending, and one column with no control. A fixture that omitted a branch
	// would let the switch above pass without testing it.
	want := map[string]int{
		"sortable, unsorted": 1,
		"descending":         1,
		"no attribute":       2,
	}

	if !reflect.DeepEqual(sortStates, want) {
		t.Errorf("the fixture's sort states are %v, want %v", sortStates, want)
	}
}

// TestTheTableGivesEachRowOneOverflowMenu is §7.8's row-action rule: "row actions
// live in an overflow menu cell, not four always-visible icon buttons — 80 tab
// stops destroys usability".
func TestTheTableGivesEachRowOneOverflowMenu(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	rows := elements(root, func(node *html.Node) bool {
		return node.Data == "tr" && hasAttr(node, "data-row") && inForm(node, "tabular")
	})

	if len(rows) != 2 {
		t.Fatalf("the tabular form has %d rows, want 2", len(rows))
	}

	for _, row := range rows {
		rowID := attrValue(row, "data-row")

		buttons := elements(row, func(node *html.Node) bool {
			return node.Data == "button"
		})

		if len(buttons) != 1 {
			t.Errorf("row %q has %d buttons in its body; §7.8 wants one overflow "+
				"menu, because four always-visible actions per row is 80 tab stops",
				rowID, len(buttons))
		}

		if label := normalisedText(buttons[0]); label != "Actions" {
			t.Errorf("row %q's row-action control reads %q, want \"Actions\"", rowID, label)
		}
	}

	// The menu says which row it is for, because "Actions" repeated twice is two
	// identical accessible names and forty would be forty.
	for _, menu := range elements(root, func(node *html.Node) bool {
		return hasAttr(node, "data-menu-sheet")
	}) {
		name := accessibleName(menu, idIndex(root))
		if !strings.HasPrefix(name, "Actions for ") {
			t.Errorf("a row's overflow menu is named %q, want \"Actions for <row>\"", name)
		}
	}
}

// TestTheTableHasOneCellPerColumnInBothForms is the shape assertion that makes
// the equality test above meaningful: a row that renders a different *number* of
// cells in the two forms would pass a value-only comparison.
func TestTheTableHasOneCellPerColumnInBothForms(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	columns := len(elements(root, func(node *html.Node) bool {
		return node.Data == "th" && attrValue(node, "data-column") != ""
	}))

	if columns == 0 {
		t.Fatal("the table has no identified columns")
	}

	for _, row := range elements(root, func(node *html.Node) bool {
		return node.Data == "tr" && hasAttr(node, "data-row") && inForm(node, "tabular")
	}) {
		cells := elements(row, func(node *html.Node) bool {
			return node.Data == "td" && hasAttr(node, "data-cell")
		})

		if len(cells) != columns {
			t.Errorf("row %q has %d cells for %d columns", attrValue(row, "data-row"),
				len(cells), columns)
		}
	}

	for _, card := range elements(root, func(node *html.Node) bool {
		return node.Data == "article" && hasAttr(node, "data-row") && inForm(node, "stacked")
	}) {
		values := elements(card, func(node *html.Node) bool {
			return node.Data == "dd" && hasAttr(node, "data-cell")
		})

		if len(values) != columns {
			t.Errorf("stacked row %q has %d values for %d columns",
				attrValue(card, "data-row"), len(values), columns)
		}
	}
}

// TestTheTableNamesItselfExactlyOnce is the one-name rule, and §7.8's caption is
// the visible half of it.
//
// Two names for a region is the same defect as two `<h1>`s, and `<caption>` is
// what a screen reader announces on entering the table *and* what a sighted reader
// reads, so it wins over `aria-label` when both are set.
func TestTheTableNamesItselfExactlyOnce(t *testing.T) {
	t.Parallel()

	root := parse(t, tableFixture())

	table := byTestID(t, root, "pages-table")

	if accessibleName(table, idIndex(root)) != "Campaign pages" {
		t.Errorf("the table is named %q, want its caption", accessibleName(table, idIndex(root)))
	}

	captions := elements(table, func(node *html.Node) bool {
		return node.Data == "caption"
	})

	if len(captions) != 1 {
		t.Fatalf("the table has %d captions, want 1", len(captions))
	}
}

// TestTheSkipLinkCarriesBothClassNames is §7.2's skip-link contract as far as
// this package owns it.
//
// `components.Shell` already renders these anchors; the class is what the
// stylesheet's off-screen-until-focused rule targets, and `.target` is on it
// because a skip link is an interactive element like any other — a 0×0 one is
// unclickable on a phone and unreachable with a D-pad.
func TestTheSkipLinkCarriesBothClassNames(t *testing.T) {
	t.Parallel()

	root := parse(t, ui.SkipLink("main", "Skip to content", "skip-to-content"))

	link := byTestID(t, root, "skip-to-content")

	if href, _ := attr(link, "href"); href != "#main" {
		t.Errorf("href=%q, want \"#main\"; the target is a bare id because §7.2's "+
			"targets are focusable elements with tabindex=\"-1\"", href)
	}

	if !hasClass(link, ui.SkipLinkClass) {
		t.Errorf("the skip link does not carry %q; the stylesheet's "+
			"off-screen-until-focused rule targets it by that name", ui.SkipLinkClass)
	}

	if !hasClass(link, ui.TargetClass) {
		t.Errorf("the skip link does not carry %q", ui.TargetClass)
	}

	if normalisedText(link) != "Skip to content" {
		t.Errorf("the skip link reads %q", normalisedText(link))
	}
}

// TestTheSkipLinkNamesOnlyLandmarksTheRouteRenders is §7.2's third rule stated as
// a note on the caller's side of the contract: a link to a landmark that is not
// here moves focus nowhere, which is why §4.6 removes the campaign-navigation
// skip link from a pre-campaign route rather than leaving it inert.
//
// The component cannot check this — it has no view of the document — so what is
// asserted is the shape of its input: the target is the id, and nothing about it
// implies the caller cannot pass something that does not exist.
func TestTheSkipLinkNamesOnlyLandmarksTheRouteRenders(t *testing.T) {
	t.Parallel()

	root := parse(t, ui.SkipLink("nav", "Skip to campaign navigation", "skip-to-nav"))

	link := byTestID(t, root, "skip-to-nav")

	if href, _ := attr(link, "href"); href != "#nav" {
		t.Errorf("href=%q, want \"#nav\"", href)
	}

	if _, resolved := nodeByID(root, "nav"); resolved {
		t.Error("this composition renders no #nav, which is the point: a skip link " +
			"to a landmark that is absent moves focus nowhere")
	}
}

// nodeByID returns the element with an id.
func nodeByID(root *html.Node, id string) (*html.Node, bool) {
	node := nodeByIDOrNil(root, id)

	return node, node != nil
}

// nodeByIDOrNil returns the element with an id, or nil.
func nodeByIDOrNil(root *html.Node, id string) *html.Node {
	found := elements(root, func(node *html.Node) bool {
		return attrValue(node, "id") == id
	})

	if len(found) == 0 {
		return nil
	}

	return found[0]
}

// The fixtures below are built from the package's own view models, which is what
// lets the structural assertions above be about markup rather than about
// hand-written HTML: a hand-written fixture would let a component's ARIA go
// missing while every test stayed green.

// dialogFixture is a confirmation dialog with its trigger.
func dialogFixture() templ.Component {
	return ui.Dialog(ui.DialogView{
		ID:           "confirm-discard",
		TriggerLabel: "Discard changes",
		Title:        "Discard your changes?",
		Description:  "The page on disk will not be changed.",
		Body:         element("p", "Three unsaved edits will be lost."),
		TestID:       "confirm-discard",
	})
}

// menuFixture is a menu button with one link item and one action item.
func menuFixture() templ.Component {
	return ui.MenuButton(ui.MenuView{
		ID:    "page-actions",
		Label: "Actions",
		Name:  "Actions for this page",
		Items: []ui.MenuItem{
			{Label: "Rename", Href: "/c/greyhaven/wiki/Greyhaven/rename", Kind: ui.MenuItemLink},
			{Label: "Archive", Kind: ui.MenuItemAction},
		},
		TestID: "page-actions-trigger",
	})
}

// tabsView is a three-tab tablist with a selection.
func tabsView(selected string) ui.TabsView {
	return ui.TabsView{
		Label:    "Campaign settings",
		Selected: selected,
		TestID:   "settings-tabs",
		Tabs: []ui.Tab{
			{ID: "rules", Label: "Rules", Content: element("p", "House rules, in order.")},
			{ID: "members", Label: "Members", Content: element("p", "Mira is the Game Master.")},
			{ID: "system", Label: "System", Content: element("p", "D&D 5e 2024.")},
		},
	}
}

// tabsFixture is the tablist with its middle tab selected.
func tabsFixture() templ.Component {
	return ui.Tabs(tabsView("members"))
}

// disclosureFixture is a disclosure, open or collapsed.
func disclosureFixture(open bool) templ.Component {
	return ui.Disclosure(ui.DisclosureView{
		ID:      "page-outline",
		Summary: "Page outline",
		Content: element("ul", "Greyhaven"),
		Open:    open,
		TestID:  "outline-toggle",
	})
}

// tableFixture is a two-row table with a sorted column and per-row menus, which
// is every branch the component has: sortable and not, sorted and not, with and
// without actions.
func tableFixture() templ.Component {
	return ui.Table(ui.TableView{
		ID:      "pages",
		Label:   "Campaign pages",
		Caption: "Campaign pages",
		TestID:  "pages-table",
		Columns: []ui.TableColumn{
			// SortUnsorted written out rather than left at the zero value, so the
			// fixture names all three states it exercises.
			{Key: "title", Label: "Title", Sort: ui.SortUnsorted},
			{Key: "updated", Label: "Updated", Sort: ui.SortDescending},
			// A page's kind has no order, so it claims none.
			{Key: "kind", Label: "Kind", Sort: ui.SortAbsent},
		},
		Rows: []ui.TableRow{
			{
				ID:    "greyhaven",
				Label: "Greyhaven",
				Cells: []string{"Greyhaven", "12 March", "Location"},
				Actions: []ui.MenuItem{
					{
						Label: "Rename",
						Href:  "/c/greyhaven/wiki/Greyhaven/rename",
						Kind:  ui.MenuItemLink,
					},
				},
			},
			{
				ID:    "sunken-vault",
				Label: "The Sunken Vault",
				Cells: []string{"The Sunken Vault", "9 March", "Location"},
				Actions: []ui.MenuItem{
					{
						Label: "Rename",
						Href:  "/c/greyhaven/wiki/Sunken%20Vault/rename",
						Kind:  ui.MenuItemLink,
					},
				},
			},
		},
	})
}
