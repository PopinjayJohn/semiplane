package components_test

import (
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web/components"
)

// The wiki page's own tests. The structural and vocabulary gates in
// `shell_render_test.go` were written before this route existed and assert the
// *pre-campaign* shell, which this page is not — it is the first component to
// render a campaign route — so the two properties that still apply are restated
// here against this component rather than added to a fixture list whose fixture
// shape (`CampaignListView`) does not fit.
//
// What is restated, and why:
//
//   - The vocabulary. UI §1.2 is gate-blocking and "a grep for them is a test", and
//     this is a new component with new copy — a heading, and the error state under
//     it. A leftover in a heading is exactly the kind of thing nobody reads twice.
//   - Exactly one `<h1>`, on both the body and the failure branch. A failure state
//     with no `<h1>` is the branch a reviewer skips.
//   - The body is inserted unescaped, and only from the render pipeline. Asserted
//     positively, because `templ.Raw` is the one place in this package where
//     author-controlled bytes reach a browser without escaping, and a test that
//     only checked "no script tag" would pass against a template that dropped the
//     body entirely.

// wikiFixtures are the two branches the route can render.
func wikiFixtures() []struct {
	name      string
	view      components.WikiPageView
	testIDs   []string
	wantTitle string
} {
	shell := components.ShellView{
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		Account:     components.AccountView{Username: "mira"},
		SignOutHref: "/logout",
	}

	return []struct {
		name      string
		view      components.WikiPageView
		testIDs   []string
		wantTitle string
	}{
		{
			name: "page",
			view: components.WikiPageView{
				Shell:   shell,
				Heading: "The Saltmarsh Vault",
				Body:    "<p>The door is <strong>iron</strong>.</p>",
			},
			testIDs: []string{
				"shell", "shell-header", "shell-main", "shell-rail", "shell-footer",
				"rail-instance", "header-account", "header-sign-out",
				"page-title", "page-body",
			},
			wantTitle: "The Saltmarsh Vault",
		},
		{
			name: "page/could-not-load",
			view: components.WikiPageView{
				Shell:   shell,
				Heading: "The Saltmarsh Vault",
				Failure: &components.LoadFailure{Reference: "gen-4f2a"},
			},
			testIDs: []string{
				"shell", "shell-main", "page-title",
				"error-state", "error-state-message", "error-state-reference",
			},
			wantTitle: "The Saltmarsh Vault",
		},
		{
			name: "page/empty-body",
			view: components.WikiPageView{
				Shell:   shell,
				Heading: "An Empty Page",
				Body:    "",
			},
			testIDs:   []string{"page-title", "page-body"},
			wantTitle: "An Empty Page",
		},
	}
}

func TestWikiPageRendersItsTestHooks(t *testing.T) {
	t.Parallel()

	for _, fixture := range wikiFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, components.WikiPage(fixture.view))

			for _, testID := range fixture.testIDs {
				if !strings.Contains(document, `data-testid="`+testID+`"`) {
					t.Errorf("the page is missing data-testid=%q", testID)
				}
			}
		})
	}
}

// TestWikiPageHasExactlyOneH1 on every branch, including the failure one: a reader
// who cannot load a page is told where they are by the same heading a reader who
// can is.
func TestWikiPageHasExactlyOneH1(t *testing.T) {
	t.Parallel()

	for _, fixture := range wikiFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, components.WikiPage(fixture.view))

			if found := strings.Count(document, "<h1 "); found != 1 {
				t.Errorf("the page has %d <h1> elements, want exactly 1 (UI §7.2)", found)
			}

			if !strings.Contains(document, ">"+fixture.wantTitle+"</h1>") {
				t.Errorf("the heading does not read %q", fixture.wantTitle)
			}
		})
	}
}

// TestWikiPageNeverNamesWorldOrSession is UI §1.2 over this component, on every
// branch. Substring- and case-insensitive like the pre-campaign gate, so the
// plural and a capitalised "World" inside a page name are both caught.
func TestWikiPageNeverNamesWorldOrSession(t *testing.T) {
	t.Parallel()

	for _, fixture := range wikiFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := strings.ToLower(render(t, components.WikiPage(fixture.view)))

			for _, forbidden := range forbiddenVocabulary {
				if strings.Contains(document, forbidden) {
					t.Errorf("the page contains %q; neither entity exists in the interface "+
						"and a leftover is a bug (UI §1.2)", forbidden)
				}
			}
		})
	}
}

// TestWikiPageInsertsTheRenderedBodyAsMarkup is the positive assertion about
// `templ.Raw`: the sanitised HTML reaches the browser as markup rather than as
// visible source text.
//
// A route that escaped the body would render a page full of `<p>` and would fail
// every other test here for the wrong reason, so the check is that the strong
// element is an element.
func TestWikiPageInsertsTheRenderedBodyAsMarkup(t *testing.T) {
	t.Parallel()

	document := render(t, components.WikiPage(components.WikiPageView{
		Shell:   components.ShellView{Instance: components.InstanceView{Name: "Greyhaven"}},
		Heading: "The Saltmarsh Vault",
		Body:    `<p>The door is <strong>iron</strong>.</p>`,
	}))

	if !strings.Contains(document, "<strong>iron</strong>") {
		t.Errorf("the rendered body did not reach the page as markup:\n%s", document)
	}

	if strings.Contains(document, "&lt;strong&gt;") {
		t.Error("the rendered body was escaped, so a reader would see the markup as text")
	}
}

// TestWikiPageRendersNoBodyOnTheFailureBranch: the two branches are exclusive, and
// a failure that still rendered a body would show a reader a page nobody can vouch
// for.
func TestWikiPageRendersNoBodyOnTheFailureBranch(t *testing.T) {
	t.Parallel()

	document := render(t, components.WikiPage(components.WikiPageView{
		Shell:   components.ShellView{Instance: components.InstanceView{Name: "Greyhaven"}},
		Heading: "The Saltmarsh Vault",
		Body:    "<p>stale prose</p>",
		Failure: &components.LoadFailure{Reference: "gen-4f2a"},
	}))

	if strings.Contains(document, "stale prose") {
		t.Error("the failure branch rendered a body as well as the error state")
	}

	if !strings.Contains(document, `data-testid="error-state"`) {
		t.Error("the failure branch did not render the error state")
	}
}

// TestWikiPageCarriesTheCampaignRouteLandmarks: the four landmarks are the shell's,
// and this route keeps all four (UI §4.6 removes the navigation, not the rail).
// Asserted here rather than in the pre-campaign gate because the rail this route
// renders is `InstanceRail` until P6 gives it a campaign rail, and a reviewer should
// be able to see that in one place.
func TestWikiPageCarriesTheCampaignRouteLandmarks(t *testing.T) {
	t.Parallel()

	document := render(t, components.WikiPage(components.WikiPageView{
		Shell:   components.ShellView{Instance: components.InstanceView{Name: "Greyhaven"}},
		Heading: "The Saltmarsh Vault",
		Body:    "<p>Prose.</p>",
	}))

	for _, landmark := range []string{
		`role="banner"`,
		`role="main"`,
		`role="contentinfo"`,
		`role="complementary"`,
	} {
		if !strings.Contains(document, landmark) {
			t.Errorf("the page is missing %s (UI §4.5)", landmark)
		}
	}

	if strings.Contains(document, `role="navigation"`) {
		t.Error("the page renders a navigation landmark; the campaign nav arrives in P6")
	}
}

// TestWikiPageViewIsUsableWithoutACampaignName: a campaign registered without a
// name still renders a shell, and the route composes the document title from the
// slug. Asserted here on the component rather than in the route, because the
// component's half is that it does not require a name to render at all.
func TestWikiPageViewIsUsableWithoutACampaignName(t *testing.T) {
	t.Parallel()

	var zero components.ShellView

	document := render(t, components.WikiPage(components.WikiPageView{
		Shell:   zero,
		Heading: "A Page",
	}))

	if !strings.Contains(document, "semiplane") {
		t.Errorf("an unconfigured instance did not name itself in the header:\n%s", document)
	}

	if !strings.Contains(document, "A Page") {
		t.Errorf("the page heading is missing:\n%s", document)
	}
}

// TestWikiArticleIsTheCentreSlotAlone, so the rail can be asserted separately: the
// article carries no landmark of its own, because adding one is how a plugin page
// type ends up with two `<main>` elements (UI §7.2).
func TestWikiArticleIsTheCentreSlotAlone(t *testing.T) {
	t.Parallel()

	document := render(t, components.WikiArticle(components.WikiPageView{
		Heading: "A Page",
		Body:    "<p>Prose.</p>",
	}))

	for _, forbidden := range []string{"<main", "<header", "<footer", "<aside", "<html"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("the article renders %s; it is a slot, not a document", forbidden)
		}
	}
}

// TestWikiPageRendersWithNothingSetAtAll: the zero view must still produce a
// document rather than a nil dereference, because a route that reaches a component
// with an incomplete view has to fail visibly in a test rather than as a 500 in
// production.
func TestWikiPageRendersWithNothingSetAtAll(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(render(t, components.WikiPage(components.WikiPageView{}))) == "" {
		t.Error("the page rendered nothing at all")
	}
}
