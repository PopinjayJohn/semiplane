package search_test

// UI §7.5 and §10.8's search row, asserted against the bytes this route serves.
//
// Four groups, and the grouping is the argument:
//
//   - **The surface.** One `<h1>` carrying the count and the query; §4.7's two
//     search states in the right circumstances; every control carrying `.target`;
//     no `tabindex` above -1; and no `world` or `session` anywhere in the parsed
//     document.
//   - **The absence.** No live region on any of the four documents. Both §7.5 rules
//     this route is most likely to break by habit, and both are asserted as
//     absences because the failure they guard against is a *correct-looking* 200
//     carrying the wrong thing.
//   - **The headers.** An `ETag` that moves when the rows do, `Vary: Cookie`, and a
//     salt that keeps a GM's validator from being a player's.
//   - **The hostile query.** What reaches the page when `q=` is a quote, a script
//     tag, a template expression, an FTS operator, or ten kilobytes of itself.
//
// The visibility assertions are in `visibility_test.go` against a **real store**,
// because the property they hold is not observable through a fake: a fake has no
// tier, so it cannot express "this reader may not read this page". What is asserted
// here with the fake is the half a fake *can* see — what the route asked.
//
// Every assertion below was mutation-checked: the line it holds was changed and the
// test re-run, and each test's comment records the mutation that broke it.

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/store"
)

// searchURL is this route's URL for a query.
func searchURL(query string) string {
	if query == "" {
		return "/c/" + testSlug + "/search"
	}

	return "/c/" + testSlug + "/search?q=" + url.QueryEscape(query)
}

// shellScriptCount is how many `<script>` elements the shell renders into `<head>`.
//
// §3.7's one blocking resolver plus the sheet re-parenting that has to wait for
// `document.body`, and `shell.Resolver` is what renders both. The count is a
// constant rather than a wildcard because "no script" would be satisfied by a shell
// that lost its theme resolver, and "some script" is satisfied by anything.
const shellScriptCount = 2

// The route's own bounds, restated for the same reason the handler restates them.
//
// They are assertions about a *number*, not a reimplementation of the rule: the
// handler's comments hold the reasoning, and a test that asked the handler for its
// own constant would assert nothing.
const (
	// maxQueryDisplay is the byte ceiling on an echoed query, ellipsis included.
	maxQueryDisplay = 200
	// ellipsis is what a truncated echo ends in, so the test can require that the
	// bound says it truncated.
	ellipsis = "…"
	// resultCap is how many rows the route asks the store for.
	resultCap = 100
)

// --- The surface ---------------------------------------------------------------

// TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery is UI §7.2's first
// structural rule and §7.5's count requirement together.
//
// Exactly one, not at least one: two `<h1>`s tell a screen reader the page has two
// titles. The composition that breaks it is the one §4.7 makes possible — the
// *state* components render their own heading, so a route that also rendered a
// heading above them would have two — which is why the states are mounted with
// `ui.HeadingPage` and the route writes no `<h1>` of its own on those paths.
//
// The count and the query are asserted *in* the `<h1>` rather than somewhere on the
// page, because §7.5 puts them there and "the count is on the page" is satisfied by
// a count in the footer. §10.8's row for search says exactly this: "count in the
// `<h1>`".
//
// Mutation: rendering the route's own `<h1>` and mounting the states at
// `ui.HeadingInPage` — the swap a later change is most likely to make, and the one
// that reads as a tidy-up — fails the three state subtests, each of which then finds
// two `<h1>`s.
func TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery(t *testing.T) {
	t.Parallel()

	t.Run("results carry the count and the query", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "results",
			newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor()))

		assertExactlyOneH1(t, rendered)

		heading := textOf(rendered.find("h1")[0])

		want := "3 results for goblin"
		if heading != want {
			t.Errorf("the <h1> is %q, want %q; UI §7.5 puts the count and the query in "+
				"the page's only heading, and §10.8 audits for it there", heading, want)
		}
	})

	t.Run("one result is singular", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "one result",
			newHarness(t).withRows(testRows()[:1]...).get(searchURL("goblin"), gmRequestor()))

		assertExactlyOneH1(t, rendered)

		heading := textOf(rendered.find("h1")[0])
		if heading != "1 result for goblin" {
			t.Errorf("the <h1> is %q, want %q. A search page whose first impression is "+
				"a plural is the first thing a reader notices", heading, "1 result for goblin")
		}
	})

	t.Run("the idle state owns its own heading", func(t *testing.T) {
		t.Parallel()

		assertExactlyOneH1(t, document(t, "idle",
			newHarness(t).withRows(testRows()...).get(searchURL(""), gmRequestor())))
	})

	t.Run("the no-results state owns its own heading", func(t *testing.T) {
		t.Parallel()

		assertExactlyOneH1(t, document(t, "no results",
			newHarness(t).get(searchURL("wyvern"), gmRequestor())))
	})

	t.Run("the failure state owns its own heading", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "failure",
			newHarness(t).failing(errIndexUnavailable).get(searchURL("goblin"), gmRequestor()))

		assertExactlyOneH1(t, rendered)

		if !rendered.hasTestID("search-failure-heading") {
			t.Errorf("%s: the failure document has no route heading; a reader who cannot "+
				"search must still be told where they are", rendered.where)
		}
	})
}

// assertExactlyOneH1 is the shared assertion, so the five subtests above cannot
// drift into five spellings of it.
//
// `Fatalf` and not `Errorf`, because every caller indexes `find("h1")[0]` afterwards:
// a reported failure followed by an index-out-of-range panic reports the panic and
// loses the finding, which is the worse failure message of the two.
func assertExactlyOneH1(t *testing.T, rendered doc) {
	t.Helper()

	headings := rendered.find("h1")
	if len(headings) == 1 {
		return
	}

	t.Fatalf("%s: the document has %d <h1> elements, want exactly 1 (UI §7.2): %s",
		rendered.where, len(headings), strings.Join(headingTexts(rendered), " | "))
}

// headingTexts returns every heading's tag and text, for a failure message.
func headingTexts(rendered doc) []string {
	var found []string

	rendered.elements(func(node *html.Node) {
		if len(node.Data) == 2 && node.Data[0] == 'h' {
			found = append(found, node.Data+": "+textOf(node))
		}
	})

	return found
}

// TestTheTwoSearchStatesRenderInTheRightCircumstances is §4.7's two search rows,
// each with the `data-testid` its state carries, asserted in both directions.
//
// Both directions, because a component that always emits a state emits both of them,
// and the positive assertion alone would be satisfied by that. The idle state is the
// no-query case and the no-results state is the query-that-matched-nothing case;
// each test asserts the other is absent.
func TestTheTwoSearchStatesRenderInTheRightCircumstances(t *testing.T) {
	t.Parallel()

	const (
		idleHook  = "state-search-idle"
		emptyHook = "state-search-no-results"
	)

	t.Run("no query is the idle state", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "idle",
			newHarness(t).withRows(testRows()...).get(searchURL(""), gmRequestor()))

		if !rendered.hasTestID(idleHook) {
			t.Errorf("%s: no %q section; §4.7 designs a state for arriving at the "+
				"search route with no query", rendered.where, idleHook)
		}

		if rendered.hasTestID(emptyHook) {
			t.Errorf("%s: the no-results state rendered for a query that was never "+
				"submitted; it is the state for a query that matched nothing (§4.7)",
				rendered.where)
		}
	})

	t.Run("a whitespace-only query is the idle state", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "blank query",
			newHarness(t).withRows(testRows()...).get(searchURL("   "), gmRequestor()))

		if !rendered.hasTestID(idleHook) {
			t.Errorf("%s: a query of only spaces produced neither the idle state nor a "+
				"400; §4.7's idle state is what a reader who asked to search and typed "+
				"nothing gets", rendered.where)
		}
	})

	t.Run("a query with no matches is the no-results state", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "no results",
			newHarness(t).get(searchURL("wyvern"), gmRequestor()))

		if !rendered.hasTestID(emptyHook) {
			t.Errorf("%s: no %q section; §4.7 designs a state for a query that matched "+
				"nothing", rendered.where, emptyHook)
		}

		if rendered.hasTestID(idleHook) {
			t.Errorf("%s: the idle state rendered for a submitted query; a reader who "+
				"searched and got nothing must be told so (§4.7)", rendered.where)
		}
	})

	t.Run("a result list is neither state", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "results",
			newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor()))

		if rendered.hasTestID(idleHook) || rendered.hasTestID(emptyHook) {
			t.Errorf("%s: a search that matched %d rows rendered an empty state; §4.7's "+
				"states are for the searches that matched nothing", rendered.where,
				len(testRows()))
		}
	})

	t.Run("the no-results state echoes the query back", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "no results",
			newHarness(t).get(searchURL("wyvern"), gmRequestor()))

		if got := textOf(rendered.testID(t, "search-no-results-term")); got != "wyvern" {
			t.Errorf("the echoed term is %q, want %q; §4.7 requires the query echoed "+
				"back so the reader can tell what was searched for", got, "wyvern")
		}
	})

	t.Run("the idle state's field is focused", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "idle",
			newHarness(t).withRows(testRows()...).get(searchURL(""), gmRequestor()))

		field := rendered.testID(t, "search-idle-query")
		if !hasAttribute(field, "autofocus") {
			t.Errorf("%s: the idle state's field carries no autofocus; §4.7 designs "+
				"this state as \"search field, focused, with a hint\", and a reader who "+
				"came here to search should not have to click first", rendered.where)
		}
	})

	t.Run("the idle state carries a hint", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "idle",
			newHarness(t).withRows(testRows()...).get(searchURL(""), gmRequestor()))

		if text := textOf(rendered.testID(t, "search-idle-hint")); text == "" {
			t.Errorf("%s: §4.7's idle state is \"search field, focused, with a hint\", "+
				"and the hint is empty", rendered.where)
		}
	})
}

// TestTheResultListLinksToPagesInThisCampaign is the result row's address.
//
// A result that links nowhere is a dead end wearing a link's clothes, and this is
// what catches `WikiHref` being handed a path with its extension still attached.
//
// The expected hrefs carry `%20`: `content.WikiHref` escapes each segment
// separately, and a literal space in an `href` attribute works in a browser and is
// wrong in a log, a bookmark and a copied URL.
func TestTheResultListLinksToPagesInThisCampaign(t *testing.T) {
	t.Parallel()

	rendered := document(t, "results",
		newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor()))

	list := rendered.testID(t, "search-result-list")
	if list == nil {
		t.Fatalf("%s: no result list; a search that matched %d rows rendered none",
			rendered.where, len(testRows()))
	}

	links := make([]string, 0, 3)
	for _, link := range findElements(list, "a") {
		links = append(links, attribute(link, "href"))
	}

	want := []string{
		"/c/greyhaven/wiki/Adventures/Goblin%20Cave",
		"/c/greyhaven/wiki/Goblin%20Warren",
		"/c/greyhaven/wiki/Notes/Goblin%20muster%20roll",
	}

	if strings.Join(links, ",") != strings.Join(want, ",") {
		t.Errorf("the result links are %v, want %v; a result must link to the page it "+
			"names, inside this campaign", links, want)
	}

	for _, link := range links {
		if strings.HasSuffix(link, ".md") {
			t.Errorf("a result link ends in %q; a URL carries no extension (S-9), and "+
				"the wiki route would resolve the path twice", link)
		}
	}
}

// TestAResultTitleFallsBackToItsBaseName is a page with no front-matter `title:`.
//
// `domain.Page.Title` is documented as empty rather than defaulted, so every consumer
// has to decide what the page calls itself — and a row whose title is blank renders
// a blank link, which is a dead end that looks like a link.
func TestAResultTitleFallsBackToItsBaseName(t *testing.T) {
	t.Parallel()

	rows := testRows()
	rendered := document(t, "results",
		newHarness(t).withRows(rows...).get(searchURL("goblin"), gmRequestor()))

	list := rendered.testID(t, "search-result-list")
	links := findElements(list, "a")

	last := textOf(links[len(links)-1])
	if last != "Goblin muster roll" {
		t.Errorf("the third result is named %q, want %q; a page with no front-matter "+
			"title: still has a name, and the page route names it the same way",
			last, "Goblin muster roll")
	}

	// `Notes/Goblin muster roll.md` → `Goblin muster roll`: the directories are
	// dropped and the extension with them, because the fallback is what the *page*
	// calls itself and `wiki.pageName` names it the same way.
	if strings.Contains(last, "/") || strings.Contains(last, ".md") {
		t.Errorf("the fallback name is %q for the path %q; the fallback is the path's "+
			"base name without its directories and without the extension a URL does "+
			"not carry", last, rows[2].Path)
	}
}

// TestTheResultListShowsEveryRowItCounted is the count's honesty.
//
// §7.5's count and the list are the same answer stated twice, and they can disagree:
// a row the route cannot link is dropped by `resultViews`, and a heading still
// counting it is a heading claiming a link the page does not have.
func TestTheResultListShowsEveryRowItCounted(t *testing.T) {
	t.Parallel()

	rendered := document(t, "results",
		newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor()))

	items := findElements(rendered.testID(t, "search-result-list"), "li")
	heading := textOf(rendered.testID(t, "search-result-count"))

	if !strings.HasPrefix(heading, strconv.Itoa(len(items))+" result") {
		t.Errorf("the <h1> says %q but the list holds %d rows; §7.5's count and the "+
			"rows must be the same answer", heading, len(items))
	}
}

// TestACappedResultListSaysItIsCapped is §7.5's count against a truncated list.
//
// The store caps a search at `maxSearchLimit`, so a query matching more pages than
// that renders a list that is not the whole answer. A heading saying "100 results"
// with 100 rows on the page is not false — it is the count of what is shown — but a
// reader who has no way to tell the list was cut is being told the search finished.
func TestACappedResultListSaysItIsCapped(t *testing.T) {
	t.Parallel()

	capped := make([]domain.SearchHit, 0, 100)
	for index := range 100 {
		capped = append(capped, hit(int64(index+1), "Notes/"+strconv.Itoa(index)+".md",
			"Note "+strconv.Itoa(index), "a body about goblins"))
	}

	t.Run("at the cap the page says so", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "capped",
			newHarness(t).withRows(capped...).get(searchURL("goblin"), gmRequestor()))

		if !rendered.hasTestID("search-result-cap") {
			t.Errorf("%s: the list holds %d rows — everything the store will return — "+
				"and the page does not say so; a reader would take the count for the "+
				"whole answer", rendered.where, len(capped))
		}
	})

	t.Run("below the cap it does not", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "under the cap",
			newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor()))

		if rendered.hasTestID("search-result-cap") {
			t.Errorf("%s: the page says it is showing the first %d results when it is "+
				"showing all %d; a caveat under every long search trains a reader to "+
				"ignore it", rendered.where, 100, len(testRows()))
		}
	})
}

// TestEveryControlCarriesTheTargetClass is §7.3's "enforced by construction" made
// checkable, and §10.6's audit of it.
//
// A *walk* rather than a count, because "N elements carry the class" is satisfied by
// an N that happens to match, and adding a control without the class then makes the
// test pass by coincidence.
func TestEveryControlCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, query)

			controls := rendered.focusable()
			if len(controls) == 0 {
				t.Fatalf("%s: the document holds no focusable element, so the .target "+
					"audit below cannot fail", rendered.where)
			}

			for _, node := range controls {
				if hasClassToken(node, "target") {
					continue
				}

				t.Errorf("%s: %s is focusable and carries no .target; §7.3 enforces "+
					"--target-min by construction and §10.6 audits rendered markup for it",
					rendered.where, describe(node))
			}
		})
	}
}

// TestNoTabindexAboveMinusOne is §7.4's "no positive tabindex, anywhere — gate
// failure", over the four documents.
//
// `0` is excluded too: it moves one element to the front of the focus order while the
// markup still reads in the original order, so the two disagree and only -1 is
// usable.
func TestNoTabindexAboveMinusOne(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, query)

			rendered.elements(func(node *html.Node) {
				raw := attribute(node, "tabindex")
				if raw == "" {
					return
				}

				value, err := strconv.Atoi(strings.TrimSpace(raw))
				if err != nil {
					t.Errorf("%s: %s carries tabindex=%q, which is not an integer",
						rendered.where, describe(node), raw)

					return
				}

				if value > -1 {
					t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted (UI §7.4)",
						rendered.where, describe(node), value)
				}
			})
		})
	}
}

// TestNoRetiredEntityIsNamedAnywhere is UI §1.2 and §10.2's last clause, over every
// text node, every comment, every attribute value and every attribute name.
//
// The four inclusions each have a reason, and they are why this test cannot be a
// `strings.Contains` over the body: a word in a comment is invisible to a reader,
// which is precisely why a reader of text nodes would miss it; an `aria-label` is
// announced, so a retired entity named there is named to the person the rule
// protects; and a `data-` attribute is invisible to a reader and obvious to a
// developer grepping for the feature it implies.
//
// **The rule is scoped by what this route *authors*.** A result title is campaign
// content, and the words are absent from semiplane's own copy. A GM whose page is
// called "The World Map" will see their own title on their own screen and no audit
// changes that — so the fixtures use titles free of the words, and this test polices
// the route's copy rather than the campaign's.
//
// Mutation: adding `aria-live="polite"` to the result list's `<section>` fails this
// test and `TestNoLiveRegionOnAnyDocument` together, which is the point of running
// both over the same four documents.
func TestNoRetiredEntityIsNamedAnywhere(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, query)

			report := func(place, text string) {
				lowered := strings.ToLower(text)

				for _, word := range retiredEntities {
					if strings.Contains(lowered, word) {
						t.Errorf("%s: %s contains %q. Neither entity exists and UI §1.2 "+
							"makes the words appear nowhere in the interface",
							rendered.where, place, word)
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

			walk(rendered.root)
		})
	}
}

// TestAResultCarriesTheTitleVerbatimAndTheSnippetUnredactedOnly is ADR 0036 pinned.
//
// Two halves, and the first is an assertion about the *absence* of a defence — which
// is the only kind of assertion that pins a decision like this. A title is one line of
// front matter with no callout structure, so there is no boundary in it to redact to,
// and a filter would have to guess which words are secret: a guess that fires breaks a
// real title and a guess that misses leaks the secret. So `pages.title` is indexed
// verbatim and a result list carries it as it stands, and a future change that filters
// titles fails here and has to argue with the record rather than with a test nobody
// wrote on purpose.
//
// The second half is the opposite and is the load-bearing one for security: the snippet
// *is* redacted, because it is FTS5's excerpt of `body_plain` and that column excludes
// `[!secret]` callout content in **every** reveal state (S-5.11, UI §4.10.5). So the
// surface that carries unredacted text is the title and the surface that does not is the
// excerpt, and both facts are asserted rather than assumed.
func TestAResultCarriesTheTitleVerbatimAndTheSnippetUnredactedOnly(t *testing.T) {
	t.Parallel()

	rows := testRows()
	rendered := document(t, "results",
		newHarness(t).withRows(rows...).get(searchURL("goblin"), gmRequestor()))

	list := rendered.testID(t, "search-result-list")
	links := findElements(list, "a")

	if got := textOf(links[0]); got != rows[0].Title {
		t.Errorf("the first result is named %q, want the indexed title %q verbatim. "+
			"ADR 0036 keeps titles unredacted because a title has no callout boundary "+
			"to redact to, and filtering one would break real titles while missing real "+
			"leaks — so if this is failing because a filter was added, that is an ADR, "+
			"not a fix", got, rows[0].Title)
	}

	body := pageText(rendered)
	if !strings.Contains(body, rows[1].Snippet) {
		t.Errorf("the response does not contain the store's snippet %q; the excerpt is "+
			"how a reader sees what matched, and dropping it would make every result "+
			"look identical", excerpt(rows[1].Snippet))
	}
}

// --- The absence ---------------------------------------------------------------

// TestNoLiveRegionOnAnyDocument is S-13.4 and §7.5, stated as an absence.
//
// **Asserted over the whole parsed document rather than over the centre slot**,
// because the way this rule gets broken is by habit: every other dynamic surface in
// this application is a live region, and the two §4.7 search states were written
// beside four that are. A check scoped to the centre would pass while the chrome
// gained a `role="status"`, which is the wrong half to leave unasserted.
//
// Three attributes, all of them: `role="status"` and `role="alert"` are the two
// roles §13.3 names, and `aria-live` is the mechanism both spellings use. A region
// written `aria-live="polite"` with no role is the commonest way one arrives by
// accident, so checking only the roles would miss it — and `aria-live="off"` is
// read as a *value*, because that spelling states the rule correctly rather than
// breaking it.
//
// Mutation: putting `role="status"` on the result list's `<section>` fails this
// test. It is the one line §7.5's task asks a test to be able to fail on.
func TestNoLiveRegionOnAnyDocument(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, query)

			rendered.elements(func(node *html.Node) {
				role := attribute(node, "role")
				if role == "status" || role == "alert" || role == "log" {
					t.Errorf("%s: %s carries role=%q; S-13.4 and UI §7.5 require no "+
						"live region at all on the search route, because a search "+
						"result page is a fresh navigation and not an update",
						rendered.where, describe(node), role)
				}

				if live, present := ariaLive(node); present && live != "off" {
					t.Errorf("%s: %s carries aria-live=%q; §7.5 gives the search route "+
						"no live region, and a region that is in the document at load "+
						"says nothing anyway while firing on every reload",
						rendered.where, describe(node), live)
				}
			})
		})
	}
}

// ariaLive returns an element's `aria-live` value and whether it carries one at all.
func ariaLive(node *html.Node) (string, bool) {
	if !hasAttribute(node, "aria-live") {
		return "", false
	}

	return attribute(node, "aria-live"), true
}

// --- The headers ---------------------------------------------------------------

// TestEveryDocumentCarriesAnETagAndRevalidates is S-13.4 and §7.5: "assert the
// response carries an `ETag`", and "ETag-cacheable".
//
// All four documents, because a route that emits one on its success path and not on
// its revalidation cannot revalidate — which is the property being claimed, and a
// status assertion on one document would miss it.
func TestEveryDocumentCarriesAnETagAndRevalidates(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			recorder := newHarness(t).withRows(testRows()...).get(searchURL(query), gmRequestor())

			validator := recorder.Header().Get("ETag")
			if validator == "" {
				t.Fatalf("%s: the response carries no ETag; S-13.4 requires one on the "+
					"search route, and §7.5 makes submit-to-navigate cacheable on the "+
					"strength of it", where)
			}

			if !strings.HasPrefix(validator, `W/"`) {
				t.Errorf("%s: the ETag is %q, want a weak validator; a strong validator "+
					"promises byte identity, which a cache may exploit to serve one "+
					"of two byte-identical responses (RFC 9110 §8.8.3)", where, validator)
			}

			revalidation := newHarness(t).withRows(testRows()...).
				getWith(searchURL(query), gmRequestor(), http.Header{
					"If-None-Match": {validator},
				})

			if revalidation.Code != http.StatusNotModified {
				t.Errorf("%s: revalidating with %s = %d, want 304; the validator is "+
					"carried so that a second submission of the same query costs no "+
					"transfer", where, validator, revalidation.Code)
			}

			if revalidation.Body.Len() != 0 {
				t.Errorf("%s: the 304 carried %d bytes of body; RFC 9110 §15.4.5 says a "+
					"304 carries the metadata and no body", where, revalidation.Body.Len())
			}
		})
	}
}

// TestTheValidatorMovesWhenTheRowsDo is what makes the `ETag` mean anything.
//
// A validator over the *query* rather than the *rows* would pass every header
// assertion above and be wrong: a page edited, a title changed, a page added, and
// every reader's cached result list would still be current. S-5.2's argument is that
// validity is a function of content, and for a result list the content is the rows.
//
// Mutation: replacing the fingerprint's pre-image with the query alone fails all
// three subtests.
func TestTheValidatorMovesWhenTheRowsDo(t *testing.T) {
	t.Parallel()

	base := newHarness(t).withRows(testRows()...).
		get(searchURL("goblin"), gmRequestor()).Header().Get("ETag")

	t.Run("a different row set", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t).withRows(testRows()[:2]...).
			get(searchURL("goblin"), gmRequestor()).Header().Get("ETag")

		if got == base {
			t.Errorf("the ETag is %q for three rows and for two. S-5.2 makes validity a "+
				"function of content, and a result list's content is its rows: a "+
				"validator over the query alone lets every cached list outlive the "+
				"edit that changed it", got)
		}
	})

	t.Run("a changed snippet", func(t *testing.T) {
		t.Parallel()

		edited := testRows()
		edited[0] = hit(1, edited[0].Path, edited[0].Title, "a rewritten opening line")

		got := newHarness(t).withRows(edited...).
			get(searchURL("goblin"), gmRequestor()).Header().Get("ETag")

		if got == base {
			t.Errorf("the ETag is %q before and after a page's body changed; the snippet "+
				"is part of what the reader is shown, so it is part of the content the "+
				"validator is taken over", got)
		}
	})

	t.Run("a different query", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t).withRows(testRows()...).
			get(searchURL("wyrm"), gmRequestor()).Header().Get("ETag")

		if got == base {
			t.Errorf("the ETag is %q for two different queries", got)
		}
	})
}

// TestTheValidatorIsSaltedByTier is ADR 0016 and S-14.2.
//
// A GM's response and a player's must never share a validator. Nothing distinguishes
// them *today* — visibility is per campaign rather than per page, and `body_plain`
// excludes callout content for every role — so this asserts the future-proofing
// rather than a difference in today's rows, and that is the point: phase 10 is the
// phase that could make the sets differ, and the validator that could differ already
// does.
//
// The last subtest is the one that stops the salt being a regression dressed as a
// safeguard: a reader revalidating against their *own* validator must still get 304.
func TestTheValidatorIsSaltedByTier(t *testing.T) {
	t.Parallel()

	gm := newHarness(t).withRows(testRows()...).
		get(searchURL("goblin"), gmRequestor()).Header().Get("ETag")
	player := newHarness(t).withRows(testRows()...).
		get(searchURL("goblin"), playerRequestor()).Header().Get("ETag")
	anonymous := newHarness(t).withRows(testRows()...).
		get(searchURL("goblin"), anonymousRequestor()).Header().Get("ETag")

	if gm == player {
		t.Errorf("a GM and a player share the ETag %q; S-14.2 says a GM response and a "+
			"player response never share one, and a cache holding both serves whichever "+
			"it stored first", gm)
	}

	if gm == anonymous {
		t.Errorf("a GM and an anonymous reader share the ETag %q, for the same reason", gm)
	}

	repeat := newHarness(t).withRows(testRows()...).
		getWith(searchURL("goblin"), playerRequestor(), http.Header{
			"If-None-Match": {player},
		})

	if repeat.Code != http.StatusNotModified {
		t.Errorf("a player revalidating with their own ETag = %d, want 304; salting by "+
			"tier must not make a reader's own second submission un-revalidatable",
			repeat.Code)
	}
}

// TestEveryDocumentVariesOnCookie is the corrected ADR 0035.
//
// `Vary: Cookie` is **required** here, not prohibited. The shell carries the reader's
// name and a sign-out form, so two 200 responses to one URL differ, and a shared
// cache that keyed only on the URL would hand one reader another's header. ADR 0035
// records the correction: an earlier claim that no route emits `Vary` was false.
//
// The failure document is included even though it carries `no-store`: `Vary` says
// what a representation depends on, and a body that rendered a reader's name depends
// on the cookie whether or not anything is allowed to keep it.
func TestEveryDocumentVariesOnCookie(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			recorder := renderedRecorder(t, where, query)

			if got := recorder.Header().Get("Vary"); got != "Cookie" {
				t.Errorf("%s: Vary is %q, want %q. The shell carries the reader's name, "+
					"so two responses to one URL differ and a cache has to be told "+
					"(ADR 0035)", where, got, "Cookie")
			}
		})
	}
}

// TestTheDocumentsDifferByReaderIsTheSubstantiveVaryCheck is why the header
// assertion above is not the whole of it.
//
// ADR 0035's lesson: a test asserting a header's *presence* cannot see the variation
// that header is protecting. This measures that the variation is real, by serving the
// same URL as two members and comparing the bytes. If the shell stopped carrying the
// reader's name, the presence assertion would still pass and the `Vary` would be a
// promise nothing keeps.
func TestTheDocumentsDifferByReaderIsTheSubstantiveVaryCheck(t *testing.T) {
	t.Parallel()

	gm := newHarness(t).withRows(testRows()...).get(searchURL("goblin"), gmRequestor())
	player := newHarness(t).withRows(testRows()...).get(searchURL("goblin"), playerRequestor())

	if bytes.Equal(gm.Body.Bytes(), player.Body.Bytes()) {
		t.Errorf("the document is byte-identical for a GM (mira) and a player (tobin). " +
			"The shell carries the reader's name, so this is what makes Vary: Cookie " +
			"load-bearing rather than decorative — and if the shell stops carrying it, " +
			"the Vary assertion still passes while nothing needs it")
	}
}

// TestAFailureIsNotCacheable is S-5.4's shape applied to a search failure.
//
// `no-store`, because there is no variant worth keeping: a pinned error outlives the
// request that produced it, and a GM whose index read failed should not be shown the
// same failure from a cache after the index recovered.
func TestAFailureIsNotCacheable(t *testing.T) {
	t.Parallel()

	recorder := newHarness(t).failing(errIndexUnavailable).get(searchURL("goblin"), gmRequestor())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("a store failure = %d, want 500; an error this route has not been "+
			"taught about must not be answered as an empty result set, which is "+
			"indistinguishable from a vault nobody wrote", recorder.Code)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("a failure carries Cache-Control %q, want no-store; a cached error "+
			"outlives the request that produced it", got)
	}
}

// TestAQueryWithNoUsableTermsIs400 is `store`'s sentinel, answered.
//
// The store refuses a query with nothing in it that could match, and says so with a
// sentinel *whose stated purpose* is that a handler answers 400 from it. The
// alternative — treating it as the idle state — hands a reader whose query was
// refused an empty form and no reason, which reads as "this campaign has nothing",
// and a 400 cannot be mistaken for "no results".
func TestAQueryWithNoUsableTermsIs400(t *testing.T) {
	t.Parallel()

	recorder := newHarness(t).failing(errInvalidQuery).
		get(searchURL("!!!"), gmRequestor())

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("a query with no usable terms = %d, want 400", recorder.Code)
	}
}

// TestTheFailureDoesNotEchoTheQuery is §4.7's closing rule on an error page.
//
// The query is attacker-controlled and the failure response is what a scanner
// triggers, so it is the one place a raw echo is both useless to the reader and
// useful to whoever is probing. §4.7's rule for a 404's *path* is the same rule.
func TestTheFailureDoesNotEchoTheQuery(t *testing.T) {
	t.Parallel()

	const query = "goblin-dragon-secret-term"

	rendered := document(t, "failure",
		newHarness(t).failing(errIndexUnavailable).get(searchURL(query), gmRequestor()))

	if strings.Contains(pageText(rendered), query) {
		t.Errorf("%s: the failure document echoes the query; §4.7 requires an error page "+
			"not to echo the caller's input", rendered.where)
	}
}

// --- The hostile query ---------------------------------------------------------

// TestAHostileQueryReachesThePageAsTextAndNeverAsMarkup is the `?q=` surface, which
// is as attacker-reachable as front matter is (S-9 puts a query string in a URL).
//
// Four assertions per case, because "it did not become a `<script>`" is one of four
// things that could go wrong and the others are the ones that actually do:
//
//  1. No script element beyond the shell's two. Not "no script" — §3.7's resolver
//     pair is in every document, and a test that rejected it would fail a correct
//     page.
//  2. No inline event handler anywhere. `onerror` on an `img` is the canonical
//     reflected-XSS payload, and it is not a `<script>`.
//  3. The echo element carries **no element children**: the query is text inside one
//     element, not markup that became nodes. This is the assertion a substring check
//     cannot make — `<script>` in the response is *expected*, because §4.7 requires
//     the echo, and it is escaped.
//  4. The echoed text is what the reader typed, with invalid UTF-8 stripped. The
//     FTS operators are the interesting cases: `store.buildMatchQuery` quotes every
//     term, so `NEAR`, `*` and `^` reach SQLite as text inside quotes and match
//     nothing special — and the echo still contains them, because they are the
//     reader's own words and §4.7 requires the echo.
//
// **Each case runs against both echo sites**, and that is the load-bearing part: the
// §4.7 states render their echo from `components/ui` and a result list renders this
// route's own. Checking only the state would leave this route's own template — the
// one place a `templ.Raw` would be a reflected-XSS hole — unasserted.
func TestAHostileQueryReachesThePageAsTextAndNeverAsMarkup(t *testing.T) {
	t.Parallel()

	for _, testCase := range hostileQueries() {
		t.Run(testCase.name+" (no results)", func(t *testing.T) {
			t.Parallel()

			// No rows: the point of these cases is the echo, and a result list adds a
			// snippet that has nothing to do with the query.
			rendered := document(t, testCase.name,
				newHarness(t).get(searchURL(testCase.query), gmRequestor()))

			assertEchoIsText(t, rendered, "search-no-results-term", testCase.want)
		})

		t.Run(testCase.name+" (with results)", func(t *testing.T) {
			t.Parallel()

			rendered := document(t, testCase.name,
				newHarness(t).withRows(testRows()...).
					get(searchURL(testCase.query), gmRequestor()))

			assertEchoIsText(t, rendered, "search-result-query", testCase.want)
		})
	}
}

// assertEchoIsText is the shared body of the hostile-query audit: no extra script, no
// event handler, the echo is one element's text, and that text is what the reader
// typed.
func assertEchoIsText(t *testing.T, rendered doc, hook, want string) {
	t.Helper()

	if scripts := rendered.find("script"); len(scripts) != shellScriptCount {
		t.Errorf("%s: the document holds %d script elements, want exactly the shell's "+
			"%d (UI §3.7's resolver pair). Anything more is the query's",
			rendered.where, len(scripts), shellScriptCount)
	}

	for _, node := range rendered.eventHandlers() {
		t.Errorf("%s: %s carries an inline event handler attribute; the query is "+
			"attacker-controlled and reached a rendered page", rendered.where,
			describe(node))
	}

	echo := rendered.testID(t, hook)

	if child := firstElementChild(echo); child != nil {
		t.Errorf("%s: the echoed term in [%s] contains the element <%s>. The query "+
			"reached the page as markup, which is the whole failure this assertion "+
			"exists for: templ escapes an interpolation and an unescaped one does not",
			rendered.where, hook, child.Data)
	}

	got := textOf(echo)

	if !utf8.ValidString(got) {
		t.Errorf("%s: [%s] is not valid UTF-8 (%q); a lone invalid byte renders as "+
			"U+FFFD in some engines and nothing in others", rendered.where, hook, got)
	}

	if strings.ContainsRune(got, 0) {
		t.Errorf("%s: [%s] carries a NUL", rendered.where, hook)
	}

	if got != want {
		t.Errorf("%s: [%s] is %q, want %q. The echo is the reader's own words bounded "+
			"to %d bytes; an FTS operator is text inside a quoted term and must "+
			"survive it", rendered.where, hook, excerpt(got), want, maxQueryDisplay)
	}
}

// TestATenKilobyteQueryIsBoundedAndStillSearchable is the DoS half of the hostile
// query, and it asserts both directions of the same bound.
//
// A 10kB query must still be *searched* — truncating before the store would answer a
// different question from the one asked, silently — while the echo must be bounded,
// because §7.5 requires the echo and an unbounded 10kB echo is a denial of service on
// the reader's own screen.
//
// **Both documents, because they are bounded by different things.** The two §4.7
// states re-apply `components/ui`'s own bound to whatever the handler hands them, so
// they would be bounded even if the handler did nothing. The result list is this
// route's own component and nothing re-bounds it there — which is the whole reason
// the handler's `boundQuery` exists, and the reason asserting only the state's echo
// would be an assertion of somebody else's bound.
func TestATenKilobyteQueryIsBoundedAndStillSearchable(t *testing.T) {
	t.Parallel()

	query := strings.Repeat("a", 10*1024)

	t.Run("searched whole, echoed bounded", func(t *testing.T) {
		t.Parallel()

		pages := &recordingPages{}
		harness := newHarness(t).indexedBy(pages)

		rendered := document(t, "10kB query", harness.get(searchURL(query), gmRequestor()))

		if got := pages.last(t).search.Query; got != query {
			t.Errorf("the store was asked for %d bytes, want %d; the bound is a "+
				"*display* bound, and truncating the query would answer a question "+
				"nobody asked", len(got), len(query))
		}

		assertBoundedEcho(t, rendered, "search-no-results-term")
	})

	t.Run("and bounded on a result list too", func(t *testing.T) {
		t.Parallel()

		rendered := document(t, "10kB query, with results",
			newHarness(t).withRows(testRows()...).get(searchURL(query), gmRequestor()))

		assertBoundedEcho(t, rendered, "search-result-query")
	})
}

// assertBoundedEcho is the shared bound assertion, so the two subtests above cannot
// drift into two spellings of it — and so neither can pass by leaning on
// `components/ui`'s bound rather than the handler's.
func assertBoundedEcho(t *testing.T, rendered doc, hook string) {
	t.Helper()

	term := textOf(rendered.testID(t, hook))

	if len(term) > maxQueryDisplay {
		t.Errorf("%s: [%s] is %d bytes, want at most %d (it begins %q); §7.5 requires "+
			"the echo and the query is attacker-controlled, so the bound is what keeps "+
			"a 40kB query off the reader's screen", rendered.where, hook, len(term),
			maxQueryDisplay, excerpt(term))
	}

	if !strings.HasSuffix(term, ellipsis) {
		t.Errorf("%s: [%s] = %q does not end in the ellipsis; a bound that truncated "+
			"without saying so makes a shortened query look like the whole of it",
			rendered.where, hook, excerpt(term))
	}

	if !utf8.ValidString(term) {
		t.Errorf("%s: [%s] is not valid UTF-8; the cut must land on a rune boundary, "+
			"which is what components/ui's own displayText does", rendered.where, hook)
	}
}

// TestTheEchoIsBoundedOnARuneBoundary is the composition property, asserted directly
// on a value made of multi-byte runes.
//
// A bound that cuts mid-rune produces a value that is not valid UTF-8, and such a
// value renders differently in different engines — so "at most 200 bytes" alone
// would pass on a bound that mangles the reader's last character. It is asserted on a
// **result list** for the reason the bound test above gives: the §4.7 states re-bound
// what they are handed, and this is about the handler's own cut.
func TestTheEchoIsBoundedOnARuneBoundary(t *testing.T) {
	t.Parallel()

	// Two-byte runes, so a byte bound in the wrong place splits one.
	query := strings.Repeat("é", 300)

	rendered := document(t, "a rune-heavy query",
		newHarness(t).withRows(testRows()...).get(searchURL(query), gmRequestor()))

	term := textOf(rendered.testID(t, "search-result-query"))

	if len(term) > maxQueryDisplay {
		t.Errorf("the echoed term is %d bytes, want at most %d", len(term), maxQueryDisplay)
	}

	if !utf8.ValidString(term) {
		t.Fatalf("the echoed term is not valid UTF-8 (%q); the cut landed mid-rune", term)
	}

	if strings.ContainsRune(term, utf8.RuneError) {
		t.Errorf("the echoed term carries U+FFFD (%q); a mid-rune cut reaches the page "+
			"as a replacement character", term)
	}
}

// TestTheQueryNeverReachesTheDocumentTitle is where the echo stops.
//
// A document title is read before the page, into the browser tab, the history entry
// and the window title. An echoed query there is a place the reader's own words reach
// that the page's copy has no business putting them in, and the heading already
// carries the query — so the title says "Search — <campaign>" and nothing else.
func TestTheQueryNeverReachesTheDocumentTitle(t *testing.T) {
	t.Parallel()

	const query = "goblin-dragon-secret-term"

	rendered := document(t, "the title",
		newHarness(t).withRows(testRows()...).get(searchURL(query), gmRequestor()))

	titles := rendered.find("title")
	if len(titles) != 1 {
		t.Fatalf("%s: the document has %d <title> elements, want 1", rendered.where,
			len(titles))
	}

	got := textOf(titles[0])

	if strings.Contains(got, query) {
		t.Errorf("the document title is %q; UI §7.5 puts the query in the <h1> and the "+
			"title has no room for 200 attacker-controlled bytes", got)
	}

	if !strings.HasPrefix(got, "Search — ") {
		t.Errorf("the document title is %q, want it to start %q", got, "Search — ")
	}
}

// --- The failure log -----------------------------------------------------------

// TestAFailureDoesNotLogTheQuery is AGENTS.md's rule about carrying caller input into
// a log line, which is a rule about bytes in an aggregator.
//
// `store`'s invalid-query error *quotes the query it refused*, so a handler that logs
// the error's text logs the query. That is the natural thing to do when debugging and
// it is what this test forbids: the line carries the status, the campaign and the
// error, and nothing the reader typed.
//
// A positive control is asserted first — a line *was* captured — so the negative
// assertion cannot be satisfied by a logger that was never installed. That is the
// failure mode an audit that cannot fail has.
func TestAFailureDoesNotLogTheQuery(t *testing.T) {
	t.Parallel()

	records := &logCapture{}

	newHarness(t).failing(errIndexUnavailable).logging(records).
		get(searchURL("goblin"), gmRequestor())

	captured := records.all()
	if len(captured) == 0 {
		t.Fatalf("no log line was captured; the negative assertion below would be " +
			"satisfied by a logger that was never installed")
	}

	for _, line := range captured {
		if strings.Contains(line, "goblin") {
			t.Errorf("a log line carries the query: %q. AGENTS.md's rule is that no "+
				"event carries caller input into an aggregator, and store's "+
				"invalid-query error quotes the query it refused", line)
		}
	}
}

// --- The scope the route passes ------------------------------------------------

// TestTheSearchIsScopedAndJoined is S-8.2 from the route's side: what it asks the
// store.
//
// Three assertions, each one a way the S-8.2 leak happens:
//
//   - The campaign scope is the resolved campaign's id, never 0. `store.PageSearch`
//     documents 0 as "every campaign the requestor may read", so a route that passed
//     the zero value of a missing campaign searches the whole instance.
//   - The requestor is passed as data, and an anonymous requestor stays anonymous.
//     `store.SearchPages` zeroes the user id for an unauthenticated requestor
//     precisely because it cannot ask whether the id was proven; a route that
//     synthesised one would defeat that.
//   - The query reaches the store **whole**, because the tokenisation policy is the
//     store's and it needs the reader's own words. The bound is a display bound.
//
// Mutation: `searchScope` returning the campaign unconditionally — dropping the tier
// and id checks — fails the ungated test below, not this one, which is why that test
// exists as its own assertion rather than as a subtest here.
func TestTheSearchIsScopedAndJoined(t *testing.T) {
	t.Parallel()

	t.Run("the scope is the resolved campaign", func(t *testing.T) {
		t.Parallel()

		pages := &recordingPages{hits: testRows()}
		harness := newHarness(t).indexedBy(pages)
		harness.get(searchURL("goblin"), playerRequestor())

		if got := pages.last(t).search.CampaignID; got != testCampID {
			t.Errorf("the store was asked for campaign %d, want %d; 0 means every "+
				"campaign the requestor may read, and a URL-scoped search must never "+
				"be an instance-scoped one (S-8.2)", got, testCampID)
		}
	})

	t.Run("an anonymous requestor reaches the store unauthenticated", func(t *testing.T) {
		t.Parallel()

		pages := &recordingPages{hits: testRows()}
		harness := newHarness(t).indexedBy(pages)
		harness.get(searchURL("goblin"), anonymousRequestor())

		if call := pages.last(t); call.req.Authenticated {
			t.Errorf("the store received Authenticated=true for an anonymous requestor; " +
				"§2.6 makes authentication a precondition of a membership being " +
				"consulted, and a route that asserted it would bypass that")
		}
	})

	t.Run("the query reaches the store whole", func(t *testing.T) {
		t.Parallel()

		pages := &recordingPages{hits: testRows()}
		harness := newHarness(t).indexedBy(pages)
		harness.get(searchURL("goblin"), playerRequestor())

		if got := pages.last(t).search.Query; got != "goblin" {
			t.Errorf("the store was asked for %q, want %q; the tokenisation policy is "+
				"the store's and it needs the reader's own words", got, "goblin")
		}
	})

	t.Run("the limit is the route's cap", func(t *testing.T) {
		t.Parallel()

		pages := &recordingPages{hits: testRows()}
		harness := newHarness(t).indexedBy(pages)
		harness.get(searchURL("goblin"), playerRequestor())

		if got := pages.last(t).search.Limit; got != resultCap {
			t.Errorf("the store was asked for %d rows, want %d; the route's cap is what "+
				"makes the truncation condition knowable", got, resultCap)
		}
	})
}

// TestTheRouteMountedWithoutItsGateRefusesRatherThanSearching is the leak made
// unexpressible, and it is the assertion `searchScope` exists for.
//
// `store.PageSearch.CampaignID` reads 0 as *every campaign the requestor may read*.
// A route reached without `campaigns.Resolve` has no campaign and no tier on its
// context, so the zero value is the only scope it could build — and passing it
// searches the instance. The gate is the right place for that decision (ADR 0024),
// and this route refuses when the gate's answer is missing rather than proceeding on
// a scope it did not resolve.
func TestTheRouteMountedWithoutItsGateRefusesRatherThanSearching(t *testing.T) {
	t.Parallel()

	pages := &recordingPages{hits: testRows()}
	harness := newHarness(t).indexedBy(pages)

	recorder := harness.ungated(searchURL("goblin"), anonymousRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Errorf("a search with no campaign resolved = %d, want 404. Reaching the "+
			"handler without campaigns.Resolve means the only scope available is 0, "+
			"which store.PageSearch reads as every campaign the requestor may read — "+
			"and a search across the instance is the shape a private title leaks "+
			"through (S-8.2, S-14.3)", recorder.Code)
	}

	if calls := pages.calls_(); len(calls) != 0 {
		t.Errorf("the store was queried %d times with no campaign resolved; the route "+
			"must refuse before it asks, because the question it would ask is the "+
			"unscoped one", len(calls))
	}
}

// TestTheRouteIsNotReachableByAnotherVerb is the method surface.
//
// `GET` alone in the mount pattern, so a `POST` is the mux's 405 rather than a branch
// in the handler, and a `HEAD` is net/http's own.
func TestTheRouteIsNotReachableByAnotherVerb(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	search.Mount(mux, newHarness(t).handler())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequestWithContext(
			t.Context(), method, "/c/"+testSlug+"/search?q=goblin", http.NoBody)

		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)

		if recorder.Code == http.StatusOK {
			t.Errorf("%s /c/{slug}/search = 200; the mount pattern admits GET alone, so "+
				"an unsafe verb is the mux's refusal rather than this route's branch",
				method)
		}
	}
}

// TestTheSearchRouteHasAtMostOneSearchLandmarkAndItIsNamed is §7.2's landmark rule
// for the one landmark this route adds.
//
// The form is `role="search"`, which makes it a landmark, and §7.2 requires a
// *distinguishing* label — two search landmarks with the same name make landmark
// navigation useless. `components/ui`'s own form is labelled "Search this campaign"
// and the header's is "Search pages", so the two already differ, and this is what
// holds them apart as the chrome grows.
//
// **At most one, and none on the failure document.** Each of the route's three
// success documents carries exactly one form — the refine form on a result list, a
// state's own form on the two empty states — and a document with both would have two
// identically-labelled search landmarks, which is what the natural "add the refine
// field everywhere" change produces. §4.7's `500` row is the request id and nothing
// else, so the failure document carries no form: a reader who cannot search has no
// use for a search field, and one would be a control that submits a query whose
// answer has already failed.
func TestTheSearchRouteHasAtMostOneSearchLandmarkAndItIsNamed(t *testing.T) {
	t.Parallel()

	const headerSearchLabel = "Search pages"

	cases := map[string]struct {
		query string
		want  int
	}{
		"results":    {query: "goblin", want: 1},
		"idle":       {query: "", want: 1},
		"no results": {query: "wyvern", want: 1},
		"a failure":  {query: "goblin", want: 0},
	}

	for where, expected := range cases {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, expected.query)

			var landmarks []*html.Node

			rendered.elements(func(node *html.Node) {
				if attribute(node, "role") == "search" {
					landmarks = append(landmarks, node)
				}
			})

			if len(landmarks) != expected.want {
				names := make([]string, 0, len(landmarks))
				for _, node := range landmarks {
					names = append(names, attribute(node, "aria-label"))
				}

				t.Fatalf("%s: the document holds %d search landmarks (%v), want %d; "+
					"§7.2's rule for two landmarks of one role is that they must not "+
					"share a name, which a duplicated form cannot satisfy",
					rendered.where, len(landmarks), names, expected.want)
			}

			if len(landmarks) == 0 {
				return
			}

			label := attribute(landmarks[0], "aria-label")
			if label == "" {
				t.Errorf("%s: the search landmark is labelled by nothing; §7.2 requires "+
					"a distinguishing label, and an unnamed landmark cannot be told from "+
					"another of the same role", rendered.where)
			}

			if label == headerSearchLabel {
				t.Errorf("%s: the centre's search landmark is labelled %q, which is the "+
					"banner's; §7.2 names the header's so it cannot collide with this "+
					"route's own form", rendered.where, label)
			}
		})
	}
}

// TestEveryTestHookAppearsExactlyOnce is §10.2's "every data-testid present" read as
// well-formedness.
//
// Asserted over the whole document rather than against an enumeration, and it is the
// assertion that caught the rows carrying `search-result-path` on every `<li>` — three
// rows, three of the same hook, and a hook that appears three times selects nothing.
// The rows are located by `data-path` and by nothing else.
func TestEveryTestHookAppearsExactlyOnce(t *testing.T) {
	t.Parallel()

	for where, query := range auditedQueries() {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			rendered := renderedDocument(t, where, query)

			counts := map[string]int{}

			rendered.elements(func(node *html.Node) {
				// An *absent* hook is not an empty one: counting the empty string would
				// report every element without a hook as a duplicate of every other,
				// and the count would be the element count of the document.
				if hook := attribute(node, "data-testid"); hook != "" {
					counts[hook]++
				}
			})

			duplicates := make([]string, 0, len(counts))

			for hook, count := range counts {
				if count > 1 {
					duplicates = append(duplicates, hook+" x"+strconv.Itoa(count))
				}
			}

			if len(duplicates) > 0 {
				sortStrings(duplicates)

				t.Errorf("%s: these hooks appear more than once: %s; a hook that "+
					"appears twice selects nothing (UI §10.2)",
					rendered.where, strings.Join(duplicates, ", "))
			}

			// The rows' locator, asserted on whichever documents have rows: a row
			// without `data-path` is unreachable to every assertion above that counts
			// them.
			for _, row := range resultRows(t, rendered) {
				if attribute(row, "data-path") == "" {
					t.Errorf("%s: a result row carries no data-path; the rows are located "+
						"by the campaign-relative path and a hook on every row would "+
						"appear once per row (§10.2)", rendered.where)
				}
			}
		})
	}
}

// resultRows returns a document's result rows, or none when it has no result list.
func resultRows(t *testing.T, rendered doc) []*html.Node {
	t.Helper()

	if !rendered.hasTestID("search-result-list") {
		return nil
	}

	return findElements(rendered.testID(t, "search-result-list"), "li")
}

// --- The mutation log ----------------------------------------------------------

// Nineteen mutations were applied and each was caught, in the order below. The record
// is in the test file rather than in a scratch document because a mutation log nobody
// reads is a comment about diligence, and this one is read by whoever changes the
// route next and wants to know which assertions are load-bearing.
//
//	 1. `role="status" aria-live="polite"` on the result list's <section>
//	     → TestNoLiveRegionOnAnyDocument.
//	 2. The result list's own <h1> deleted
//	     → TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery (0 headings).
//	 3. Both §4.7 states mounted at ui.HeadingInPage
//	     → the same test, on the idle and no-results subtests.
//	 4. `searchScope` returning the campaign unconditionally
//	     → TestTheRouteMountedWithoutItsGateRefusesRatherThanSearching.
//	 5. `CampaignID: 0` passed to the store
//	     → TestTheSearchIsScopedAndJoined and, end to end against a real store,
//	       TestASearchNeverReturnsAnotherCampaignsRows.
//	 6. The fingerprint's pre-image reduced to the query
//	     → TestTheValidatorMovesWhenTheRowsDo.
//	 7. `IncludeSecrets: false` (the tier salt dropped)
//	     → TestTheValidatorIsSaltedByTier.
//	 8. `Vary: Cookie` removed
//	     → TestEveryDocumentVariesOnCookie.
//	 9. `boundQuery` returning the value unbounded
//	     → TestATenKilobyteQueryIsBoundedAndStillSearchable and
//	       TestTheEchoIsBoundedOnARuneBoundary. (The first attempt removed only the
//	       early return and passed: the §4.7 states re-bound what they are handed,
//	       so the result-list assertion is the one that holds the handler's cut.)
//	10. `.target` dropped from the refine field
//	     → TestEveryControlCarriesTheTargetClass.
//	11. The result list's echo rendered with `templ.Raw`
//	     → TestAHostileQueryReachesThePageAsTextAndNeverAsMarkup, on the result-list
//	       echo only. (The first attempt checked the state's echo and passed, for the
//	       reason above: `components/ui` renders that one.)
//	12. The query added to the failure log line
//	     → TestAFailureDoesNotLogTheQuery.
//	13. `tabindex="0"` on the refine field
//	     → TestNoTabindexAboveMinusOne.
//	14. The query appended to the document title
//	     → TestTheQueryNeverReachesTheDocumentTitle.
//	15. `strings.TrimSpace` dropped from the idle-state decision
//	     → TestTheTwoSearchStatesRenderInTheRightCircumstances.
//	16. `store.ErrInvalidSearchQuery` no longer classified as a 400
//	     → TestAQueryWithNoUsableTermsIs400.
//	17. `countLabel`'s singular branch removed
//	     → TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery.
//	18. Truncation assumed for any list over two rows
//	     → TestACappedResultListSaysItIsCapped.
//	19. The title transformed on its way to the page (`strings.ToUpper`)
//	     → TestAResultCarriesTheTitleVerbatimAndTheSnippetUnredactedOnly. ADR 0036's
//	       decision is an *absence* of filtering, so only a test that asserts the
//	       absence can hold it.
//
// Three of the nineteen passed on the first attempt, and all three were failures of
// the *mutation*, not of the assertion: a bound that had been half-removed, an escape
// applied to a component whose text another package already bounds, and the fixture
// serving result rows for the no-results document so that a structural audit walked
// the same markup twice. All three are recorded above because they are the shape of
// the mistake this discipline exists to catch — an assertion that passes because the
// thing it watches is not the thing on the page. The third one is the worst of the
// three and it was found by the audit the phase-5 gate contributed, not by a bug.

// --- Fixtures ------------------------------------------------------------------

// retiredEntities is UI §1.2's closed vocabulary. Neither entity exists, and a
// leftover is a bug — which is why a grep for them is a test rather than a
// convention.
var retiredEntities = []string{"world", "session"}

// errIndexUnavailable is a store failure that is not a classified refusal, so it
// exercises the 500 path.
var errIndexUnavailable = errors.New("search_pages: the index is unavailable")

// errInvalidQuery is `store`'s sentinel wrapped the way the store wraps it — it quotes
// the query, which is what makes the logging assertion above meaningful.
var errInvalidQuery = fmt.Errorf("%w: %q", store.ErrInvalidSearchQuery, "goblin")

// testRows are the indexed rows the fixtures search.
//
// Two carry the private marker and one the public one, so an absence assertion has a
// string to look for and a presence assertion can be asserted first to keep the
// absence non-vacuous.
func testRows() []domain.SearchHit {
	return []domain.SearchHit{
		hit(1, "Adventures/Goblin Cave.md", "Goblin Cave", "Three goblins "+privateMarker),
		hit(2, "Goblin Warren.md", "Goblin Warren", "A warren beyond the "+publicMarker),
		hit(3, "Notes/Goblin muster roll.md", "", "Muster roll, "+privateMarker),
	}
}

// auditedQueries is the four documents every structural audit runs over.
//
// The failure document is in the list because it is a document: it is built by a
// different path than the success ones, which is exactly where an audit that only
// checks the success path finds nothing. Its status is 500 and the audits say
// nothing about status.
func auditedQueries() map[string]string {
	return map[string]string{
		"results":    "goblin",
		"idle":       "",
		"no results": "wyvern",
		"a failure":  "goblin",
	}
}

// hostileQueries is the `?q=` surface, each with the echo the route must produce.
//
// `want` is stated per case rather than computed, so a test that passed for the wrong
// reason — an echo that lost the reader's words, or gained markup — is visible. The
// FTS-operator cases expect the operator to *survive*: it is the reader's own word
// and it reaches SQLite inside a quoted term.
func hostileQueries() []struct {
	name  string
	query string
	want  string
} {
	return []struct {
		name  string
		query string
		want  string
	}{
		{"a double quote", `"`, `"`},
		{"a closing quote and an operator", `" OR "x`, `" OR "x`},
		{"a script tag", "<script>alert(1)</script>", "<script>alert(1)</script>"},
		{"an img onerror", `<img src=x onerror=alert(1)>`, `<img src=x onerror=alert(1)>`},
		{"a templ expression", "{{ .Query }}", "{{ .Query }}"},
		{"a curly brace and a dollar", "{{7*7}}${x}", "{{7*7}}${x}"},
		{"an FTS NEAR operator", "goblin NEAR troll", "goblin NEAR troll"},
		{"a prefix star", "goblin*", "goblin*"},
		{"a caret boost", "^goblin", "^goblin"},
		{"a column filter", "title:goblin", "title:goblin"},
		{"a quote pair that could escape FTS", `""goblin`, `""goblin`},
		// `ToValidUTF8` drops the invalid bytes rather than replacing them, so the
		// echo is the three letters and nothing else. This is the one case where the
		// echo is deliberately *not* the reader's bytes, and saying so is what keeps
		// the assertion honest: a bound that echoed U+FFFD would render as nothing in
		// one engine and as a replacement glyph in another.
		{"invalid UTF-8 bytes", "\xff\xfebad", "bad"},
		// Control characters. A newline is dropped like any other C0 control, a NUL
		// is *valid* UTF-8 so `ToValidUTF8` keeps it and only the control rule
		// removes it, and DEL is the last byte in the same range. Each expectation
		// shows the two words joined rather than the byte surviving — the reason
		// `boundQuery` removes them is that a downstream reader may truncate a
		// rendered value at exactly these bytes, so the page would then show a
		// string the server never had.
		{"a newline between two words", "goblin\ntroll", "goblintroll"},
		{"a NUL between two words", "wyrm\x00thug", "wyrmthug"},
		{"a DEL between two words", "kobold\x7fogre", "koboldogre"},
	}
}

// renderedRecorder serves one of the four documents and returns the response, for the
// header assertions that need the recorder rather than the DOM.
//
// **The fake does not filter by query** — it answers with whatever rows it holds — so
// the rows are chosen per document rather than per query: a "results" fixture needs
// rows and a "no results" fixture needs none. Serving `testRows()` for a query the
// fixture claims matched nothing would render the *result list* twice and leave the
// §4.7 no-results state unaudited, which is how a state gets written and never
// checked.
func renderedRecorder(t *testing.T, where, query string) *httptest.ResponseRecorder {
	t.Helper()

	harness := newHarness(t)

	switch where {
	case "a failure":
		harness = harness.failing(errIndexUnavailable)
	case "results":
		harness = harness.withRows(testRows()...)
	case "idle", "no results":
		// No rows. The idle state never reaches the store; the no-results state needs
		// an empty answer.
	default:
		t.Fatalf("%s: no fixture for this document; a new document added to "+
			"auditedQueries needs one here or it is audited against the wrong markup",
			where)
	}

	return harness.get(searchURL(query), gmRequestor())
}

// renderedDocument serves one of the four documents and parses it, so the structural
// audits above do not each spell out the harness.
//
// The failure document is served with a failing store, and its status is *not*
// asserted: the audits are about markup, and the status has its own test.
func renderedDocument(t *testing.T, where, query string) doc {
	t.Helper()

	recorder := renderedRecorder(t, where, query)

	return document(t, where, recorder)
}

// describe names a node for a failure message.
func describe(node *html.Node) string {
	if id := attribute(node, "data-testid"); id != "" {
		return node.Data + "[" + id + "]"
	}

	if id := attribute(node, "id"); id != "" {
		return node.Data + "#" + id
	}

	if class := strings.Fields(attribute(node, "class")); len(class) > 0 {
		return node.Data + "." + class[0]
	}

	return node.Data
}

// findElements returns every descendant of node with the given tag name.
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

// firstElementChild returns an element descendant of node, or nil.
//
// Used by the "the echo is text, not markup" assertion: the presence of *any*
// element inside the echoed term means the query was parsed as markup, which is the
// one thing a substring check cannot see.
func firstElementChild(node *html.Node) *html.Node {
	var found *html.Node

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode {
				found = child

				return
			}

			walk(child)
		}
	}

	walk(node)

	return found
}

// pageText returns every text node in the document concatenated, for a marker
// assertion.
//
// **Only for markers that are page content**, never for a structural rule: §7.5 and
// §10.8's rules are asked of the DOM above because a substring assertion over markup
// cannot tell a word in a text node from a word in a comment. This is for a marker
// whose whole job is to be greppable in content.
func pageText(rendered doc) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(rendered.root)

	return out.String()
}

// focusable returns every element a keyboard can reach, in document order.
func (d doc) focusable() []*html.Node {
	var found []*html.Node

	d.elements(func(node *html.Node) {
		if isFocusable(node) {
			found = append(found, node)
		}
	})

	return found
}

// isFocusable reports whether a keyboard can reach an element.
//
// §10.6's own element set, plus `tabindex` — a positive `tabindex` is a focus stop
// the document order does not show, which is why `TestNoTabindexAboveMinusOne` is a
// separate rule rather than a consequence of this one.
func isFocusable(node *html.Node) bool {
	if hasAttribute(node, "disabled") {
		return false
	}

	switch node.Data {
	case "a", "area":
		return hasAttribute(node, "href")
	case "button", "input", "select", "textarea", "summary":
		return true
	default:
		return hasAttribute(node, "tabindex")
	}
}

// eventHandlers returns every element carrying an inline event-handler attribute.
func (d doc) eventHandlers() []*html.Node {
	var found []*html.Node

	d.elements(func(node *html.Node) {
		for _, attr := range node.Attr {
			if strings.HasPrefix(attr.Key, "on") {
				found = append(found, node)

				return
			}
		}
	})

	return found
}
