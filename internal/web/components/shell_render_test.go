package components_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components"
)

// These tests are UI §10.2, which is gate-blocking, run against rendered
// fixtures rather than against routes. The routes do not exist yet — I5 builds
// the markup ahead of P5 on purpose (plan P2, I5) so the accessibility
// contract is proven before the pipeline lands. Every assertion here is about
// the interface's contract with a person, and none of it needs a database, a
// session or a request, which is what lets it stay this small.

// render turns a component into the document a browser would receive.
func render(t *testing.T, component templ.Component) string {
	t.Helper()

	var out strings.Builder
	if err := component.Render(t.Context(), &out); err != nil {
		t.Fatalf("render component: %v", err)
	}

	return out.String()
}

// The vocabulary of the interface is closed. "world" and "session" name
// entities that do not exist, and a leftover is invisible in review: it reads
// as a synonym until a reader looks for the feature it implies (UI §1.2,
// AGENTS.md).
//
// The auth session is a real mechanism with a real cookie, and it is never
// surfaced. There is no "sessions" page, no list of them, and no error copy
// naming one: what a reader sees when their credential stops working is this
// login page, which says the credentials did not match and nothing about how
// they are carried. The rule is substring-and-case-insensitive, so the plural
// "sessions" is caught too — and so is "World" inside a name somebody might
// legitimately want to give a campaign, which is the point: the reader would
// be looking for a feature that does not exist.
var forbiddenVocabulary = []string{"world", "session"}

// fixtures are the documents every structural assertion runs against. Each one
// exercises a different branch, because a structural test that only renders the
// happy path proves the happy path.
type pageFixture struct {
	name      string
	component templ.Component
	// testIDs are the hooks this page must carry. Listed per fixture so a
	// state that loses its own hook fails as itself rather than as a page.
	testIDs []string
}

func pageFixtures() []pageFixture {
	return []pageFixture{
		{
			name: "login",
			component: components.LoginPage(loginView(LoginViewOptions{
				signedIn: true,
				returnTo: "/c/greyhaven",
			})),
			testIDs: []string{
				"shell", "skip-to-content", "skip-to-utilities",
				"shell-header", "shell-main", "shell-rail", "shell-footer",
				"header-home", "header-account", "header-sign-out",
				"rail-instance", "rail-instance-name", "rail-instance-version",
				"login-heading", "login-form", "login-username",
				"login-password", "login-submit", "login-return-to",
			},
		},
		{
			name:      "login/anonymous",
			component: components.LoginPage(loginView(LoginViewOptions{})),
			testIDs: []string{
				"shell", "shell-header", "shell-main", "shell-rail",
				"shell-footer", "header-home",
				"login-heading", "login-form", "login-username",
				"login-password", "login-submit",
			},
		},
		{
			name:      "login/failed-credentials",
			component: components.LoginPage(loginView(LoginViewOptions{failed: true})),
			testIDs: []string{
				"login-heading", "login-error", "login-form", "login-submit",
			},
		},
		{
			name:      "campaigns",
			component: components.CampaignListPage(campaignListView(CampaignListViewOptions{})),
			testIDs: []string{
				"shell", "shell-header", "shell-main", "shell-rail", "shell-footer",
				"header-account", "header-sign-out", "footer-version",
				"footer-status-link",
				"campaigns-heading", "campaign-list",
			},
		},
		{
			name: "campaigns/first-run",
			component: components.CampaignListPage(
				campaignListView(CampaignListViewOptions{firstRun: true}),
			),
			testIDs: []string{
				"campaigns-heading", "first-run", "first-run-cta", "first-run-docs",
			},
		},
		{
			name: "campaigns/failed-to-load",
			component: components.CampaignListPage(
				campaignListView(CampaignListViewOptions{failed: true}),
			),
			testIDs: []string{
				"campaigns-heading", "error-state", "error-state-message",
				"error-state-reference",
			},
		},
		{
			name: "campaigns/degraded-instance",
			component: components.CampaignListPage(
				campaignListView(CampaignListViewOptions{degraded: true}),
			),
			testIDs: []string{
				"campaigns-heading", "rail-instance", "degraded-warning",
			},
		},
	}
}

func TestInterfaceNeverNamesWorldOrSession(t *testing.T) {
	t.Parallel()

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := strings.ToLower(render(t, fixture.component))

			for _, forbidden := range forbiddenVocabulary {
				if strings.Contains(document, forbidden) {
					t.Errorf(
						"rendered page contains %q; neither entity exists in the "+
							"interface and a leftover is a bug (UI §1.2)", forbidden,
					)
				}
			}
		})
	}
}

// TestInterfaceVocabularyCoversEveryExportedState renders every exported state
// component on its own, not only the two routes that mount them.
//
// The other test proves what the pages say; this one exists so that a state
// added later cannot ship without being read by a vocabulary test. A state
// nobody renders is a state nobody has read.
func TestInterfaceVocabularyCoversEveryExportedState(t *testing.T) {
	t.Parallel()

	states := []struct {
		name      string
		component templ.Component
	}{
		{
			name: "Shell/empty-slots",
			component: components.Shell(
				components.ShellView{Instance: components.InstanceView{}},
				templ.NopComponent,
				templ.NopComponent,
			),
		},
		{name: "InstanceRail", component: components.InstanceRail(instanceFixture())},
		{name: "DegradedNotice", component: components.DegradedNotice(degradedFixture())},
		{
			name: "FirstRun",
			component: components.FirstRun(
				campaignListView(CampaignListViewOptions{firstRun: true}),
			),
		},
		{
			name:      "LoadError",
			component: components.LoadError(components.LoadFailure{Reference: "req-2"}),
		},
	}

	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			t.Parallel()

			document := strings.ToLower(render(t, state.component))

			for _, forbidden := range forbiddenVocabulary {
				if strings.Contains(document, forbidden) {
					t.Errorf("rendered state contains %q (UI §1.2)", forbidden)
				}
			}
		})
	}
}

func TestPageHasExactlyOneH1(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`(?i)<h1[\s>]`)

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			// Counted by tag, not by class: a screen reader and a validator
			// both count h1 elements, so the assertion has to as well.
			if found := len(pattern.FindAllString(render(t, fixture.component), -1)); found != 1 {
				t.Errorf("page has %d <h1> elements, want exactly 1 (UI §7.2)", found)
			}
		})
	}
}

func TestPageCarriesTheFourPreCampaignLandmarks(t *testing.T) {
	t.Parallel()

	// A pre-campaign route keeps the header, centre, rail and footer and drops
	// the nav (UI §4.6). Each landmark is asserted as an attribute, including
	// the two that HTML implies implicitly, because the structural test is a
	// grep and a grep cannot see an implication.
	required := []struct {
		pattern string
		what    string
	}{
		{`role="banner"`, `role="banner"`},
		{`role="main"`, `role="main"`},
		{`role="contentinfo"`, `role="contentinfo"`},
		{`<aside[^>]*role="complementary"`, `role="complementary"`},
		{`<aside[^>]*aria-label="Utilities"`, `aria-label="Utilities"`},
	}

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, fixture.component)

			for _, landmark := range required {
				if !regexp.MustCompile(landmark.pattern).MatchString(document) {
					t.Errorf("page is missing %s (UI §4.5, §4.6)", landmark.what)
				}
			}
		})
	}
}

// TestPageHasNoCampaignNavigation asserts the nav's absence rather than its
// emptiness. The nav is entirely campaign-scoped, and a shell that could
// render an empty one would make "is this the pre-campaign shell?" a question
// with two right answers.
func TestPageHasNoCampaignNavigation(t *testing.T) {
	t.Parallel()

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, fixture.component)

			if strings.Contains(document, `role="navigation"`) {
				t.Error("page renders a navigation landmark; the left nav is absent " +
					"before a campaign exists (UI §4.6)")
			}

			if strings.Contains(document, `href="#nav"`) {
				t.Error("page renders a skip link to a navigation landmark that is not there")
			}
		})
	}
}

func TestSkipLinksAreTheFirstFocusableElements(t *testing.T) {
	t.Parallel()

	// UI §7.2: skip links first in the document, in a fixed order, and the
	// campaign-navigation one only where the nav exists. Both halves matter —
	// an absent link to a landmark that is not there moves focus nowhere.
	anchor := regexp.MustCompile(`<a\b[^>]*>`)
	skipTarget := regexp.MustCompile(`href="#([^"]+)"`)

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, fixture.component)

			anchors := anchor.FindAllString(document, -1)
			if len(anchors) < 2 {
				t.Fatalf("page has %d links, want at least the two skip links", len(anchors))
			}

			want := []string{"main", "rail"}
			for idx, target := range want {
				match := skipTarget.FindStringSubmatch(anchors[idx])
				if match == nil {
					t.Fatalf("focusable element %d is not a skip link: %s", idx+1, anchors[idx])
				}

				if match[1] != target {
					t.Errorf(
						"skip link %d points at #%s, want #%s (UI §7.2)",
						idx+1, match[1], target,
					)
				}
			}
		})
	}
}

func TestPageHasNoPositiveTabindex(t *testing.T) {
	t.Parallel()

	// UI §7.10 prohibits a positive tabindex outright. -1 is the skip links'
	// mechanism and is asserted separately, so the only value allowed here is
	// nothing at all.
	pattern := regexp.MustCompile(`tabindex="([^"]*)"`)

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			for _, match := range pattern.FindAllStringSubmatch(render(t, fixture.component), -1) {
				if match[1] != "-1" {
					t.Errorf(`tabindex=%q on a page; only -1 is allowed (UI §7.10)`, match[1])
				}
			}
		})
	}
}

func TestPageHidesNothingFromAssistiveTechnology(t *testing.T) {
	t.Parallel()

	// §7.10 prohibits aria-hidden on a focusable element, which is a rule about
	// a combination this package has no reason to express. Asserting that these
	// two pages declare no aria-hidden at all is the stronger form and states
	// the intent: nothing here is hidden from a reader, because nothing here
	// is presentational-only. A later element that genuinely is gets the
	// weaker assertion with its reason.
	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			if strings.Contains(render(t, fixture.component), "aria-hidden") {
				t.Error("page declares aria-hidden; these pages hide nothing (UI §7.10)")
			}
		})
	}
}

func TestPageCarriesItsTestHooks(t *testing.T) {
	t.Parallel()

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, fixture.component)

			for _, testID := range fixture.testIDs {
				if !strings.Contains(document, `data-testid="`+testID+`"`) {
					t.Errorf("page is missing data-testid=%q", testID)
				}
			}
		})
	}
}

// TestTestHooksAreUnique guards the convention the fixtures rely on: a
// data-testid appears at most once per document, so "the first one" is not an
// answer. Rows that repeat — campaign cards — use data-slug instead.
func TestTestHooksAreUnique(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`data-testid="([^"]+)"`)

	for _, fixture := range pageFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]int{}
			for _, match := range pattern.FindAllStringSubmatch(render(t, fixture.component), -1) {
				seen[match[1]]++
			}

			for testID, count := range seen {
				if count > 1 {
					t.Errorf(
						"data-testid=%q appears %d times; a repeated hook selects nothing",
						testID,
						count,
					)
				}
			}
		})
	}
}

// TestLoginPageRendersNoAccountZoneWhenNobodyIsSignedIn: the account zone is
// the only header control that would be a dead end here, and a sign-out button
// on the page a reader reaches precisely because they are not signed in is a
// control that cannot help.
func TestLoginPageRendersNoAccountZoneWhenNobodyIsSignedIn(t *testing.T) {
	t.Parallel()

	document := render(t, components.LoginPage(loginView(LoginViewOptions{})))

	for _, absent := range []string{`data-testid="header-account"`, `data-testid="header-sign-out"`} {
		if strings.Contains(document, absent) {
			t.Errorf("anonymous login page renders %s", absent)
		}
	}
}

func TestCampaignCardLabelsComeFromTheDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		campaign   domain.Campaign
		role       domain.Role
		wantName   string
		wantRole   string
		wantVisibl string
	}{
		{
			name: "gm in a public campaign",
			campaign: domain.Campaign{
				Name:       "Greyhaven",
				Slug:       "greyhaven",
				Visibility: domain.VisibilityPublic,
			},
			role:       domain.RoleGM,
			wantName:   "Greyhaven",
			wantRole:   "Game Master",
			wantVisibl: "Public",
		},
		{
			name: "player in a private campaign",
			campaign: domain.Campaign{
				Name:       "Saltmarsh",
				Slug:       "saltmarsh",
				Visibility: domain.VisibilityPrivate,
			},
			role:       domain.RolePlayer,
			wantName:   "Saltmarsh",
			wantRole:   "Player",
			wantVisibl: "Private",
		},
		{
			name:     "unnamed campaign falls back to its slug",
			campaign: domain.Campaign{Slug: "ashfall", Visibility: domain.VisibilityPrivate},
			role:     domain.RoleGM,
			wantName: "ashfall", wantRole: "Game Master", wantVisibl: "Private",
		},
		{
			// Both labels fail toward saying nothing: an unrecognised role is
			// not a role, and an unrecognised visibility is not public. The
			// card omits the line rather than rendering a stored value back.
			name:     "unknown role and visibility label nothing",
			campaign: domain.Campaign{Name: "Odd", Slug: "odd", Visibility: "open"},
			role:     "editor",
			wantName: "Odd", wantRole: "", wantVisibl: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			card := components.NewCampaignCard(testCase.campaign, testCase.role)

			if card.Name != testCase.wantName {
				t.Errorf("Name = %q, want %q", card.Name, testCase.wantName)
			}

			if card.Role != testCase.wantRole {
				t.Errorf("Role = %q, want %q", card.Role, testCase.wantRole)
			}

			if card.Visibility != testCase.wantVisibl {
				t.Errorf("Visibility = %q, want %q", card.Visibility, testCase.wantVisibl)
			}
		})
	}
}

// TestUnconfiguredInstanceStillNamesItself: an install reaches a browser before
// an operator has named it, and a document title with a blank slot is the first
// thing a new instance looks like.
func TestUnconfiguredInstanceStillNamesItself(t *testing.T) {
	t.Parallel()

	view := campaignListView(CampaignListViewOptions{unnamed: true})
	document := render(t, components.CampaignListPage(view))

	if !strings.Contains(document, "<title>Campaigns — semiplane</title>") {
		t.Errorf("document title does not fall back to the product name: %s", document)
	}
}
