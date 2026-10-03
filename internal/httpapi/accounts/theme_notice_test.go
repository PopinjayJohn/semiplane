package accounts_test

// UI §4.12.3's third bullet: a brand pair that fails its contrast floor must
// reach the GM as something they can act on, and the campaign overview is where
// they land.
//
// # Why this is its own file
//
// The theme package shipped `Handler.Notice` with no caller — a correct
// accessor, correctly tested, and no surface. That is a gap rather than a defect
// in what exists, and it is the kind of gap a route test suite does not notice,
// because every other assertion about the campaign list still passes. These
// tests are the caller.
//
// # Both directions, because one direction is the leak
//
// The claim has two halves and either can hold while the other fails:
//
//   - a **GM** sees the notice, naming the refused token and the rule; and
//   - a **player** does not, on the same page, for the same campaign.
//
// The second is the one that matters. §4.12.3 says "a GM notice", and a player
// shown a campaign's rejected brand is a question this product should not raise
// — not because either string is secret (both are fixed sentences this codebase
// writes) but because the gate is the requirement and the content is incidental.
// So the assertions run over the same store with the two roles swapped, which is
// what makes a gate that ignores the role fail loudly instead of quietly.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/web/components"
)

// stubThemes is a `ThemeNotices` that answers for one campaign and no other.
//
// Deliberately **not** role-aware. The gate under test is this route's, so a
// stub that filtered by role would make the test pass for the wrong reason — the
// same mistake a guard that is wired to nothing makes. It answers identically to
// everybody, which is the only way a test can show the *route* is what filters.
type stubThemes struct {
	notices map[int64]components.CampaignNotice
	asked   []int64
}

func (stub *stubThemes) ThemeNotice(
	campaignID int64,
) (components.CampaignNotice, bool) {
	stub.asked = append(stub.asked, campaignID)

	notice, ok := stub.notices[campaignID]

	return notice, ok
}

// theRefusal is a notice shaped like the ones `theme.Handler.Notice` returns:
// a token that is this codebase's own spelling and a fixed sentence.
var theRefusal = components.CampaignNotice{
	Token:  "--brand-accent",
	Reason: "the accent and its ink are 2.9:1 apart, and the floor is 4.5:1",
}

// TestTheCampaignListShowsTheThemeRefusalToTheGM is §4.12.3's third bullet, and
// the assertion is on the rendered document rather than on a view model — a
// notice that reaches the card and not the page is not a notice.
func TestTheCampaignListShowsTheThemeRefusalToTheGM(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaigns[1] = domain.Campaign{ID: 1, Slug: "greyhaven", Name: "Greyhaven"}
	st.members[1] = domain.RoleGM

	stub := &stubThemes{notices: map[int64]components.CampaignNotice{1: theRefusal}}

	body, code := themeNoticeBody(t, st, stub, 1)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	for _, want := range []string{
		`data-testid="campaign-theme-notice"`,
		"--brand-accent",
		"the accent and its ink are 2.9:1 apart, and the floor is 4.5:1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the GM's campaign list does not contain %q; §4.12.3 says the "+
				"campaign overview shows a GM notice naming the rejected pair", want)
		}
	}
}

// TestTheCampaignListNeverShowsTheThemeRefusalToAPlayer is the other half, and
// it is the half a gate that ignores the role would fail.
//
// **The leak is guarded twice, and the mutations say so.** The route fills the
// field only for a GM, and the template renders it only for a GM — neither
// trusting the other. That redundancy was measured, not assumed, and the
// measurement is worth stating because the first result was surprising:
//
// | Mutation | Result |
// | --- | --- |
// | route fills the notice for every role | **passes** — the template's guard holds |
// | template drops the role guard | **passes** — the route's guard holds |
// | both gates removed | **fails**, naming all three strings |
// | route never fills the field | **fails** the GM half |
//
// So neither single-gate mutation fails this file, and a reader who ran only
// those would conclude the test was vacuous. It is not: each mutation was caught
// by the *other* gate, which is what defence in depth means here, and removing
// both is what a real regression looks like — one edit in two files, or one
// refactor that decides the template can trust the route.
//
// The same store, the same campaign, the same stub — only `members[1]` differs —
// so a notice appearing here cannot be explained by the fixture.
func TestTheCampaignListNeverShowsTheThemeRefusalToAPlayer(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaigns[1] = domain.Campaign{ID: 1, Slug: "greyhaven", Name: "Greyhaven"}
	st.members[1] = domain.RolePlayer

	stub := &stubThemes{notices: map[int64]components.CampaignNotice{1: theRefusal}}

	body, code := themeNoticeBody(t, st, stub, 1)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	// The refusal's *content* is checked as well as its container, because a
	// template that dropped the wrapper but kept the sentence would satisfy a
	// test that only looked for the testid — and a bare sentence in front of a
	// player is the leak with the markup removed.
	for _, forbidden := range []string{
		`data-testid="campaign-theme-notice"`,
		"--brand-accent",
		"the accent and its ink are 2.9:1 apart, and the floor is 4.5:1",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("a player's campaign list contains %q. §4.12.3 says a **GM** "+
				"notice: the role gate is the requirement and neither string being "+
				"secret does not make the gate optional", forbidden)
		}
	}

	// And the card is still there, so the assertion above is about the notice and
	// not about the list having failed to render.
	if !strings.Contains(body, `data-slug="greyhaven"`) {
		t.Error("the player's campaign list lost its campaign row; the notice " +
			"assertions above would pass on an empty page")
	}
}

// TestTheCampaignListAsksTheThemeLookupOnlyForCampaignsItShows is the reason the
// lookup is not free.
//
// It is one call per campaign on the page, so a lookup that scanned every
// campaign's manifest to answer "does this one have a refusal" would be a
// filesystem walk per card — and a reader with twenty campaigns would pay
// twenty walks to render a list. Asserting the *set* of ids asked for, rather
// than the count, is what catches a lookup that was quietly widened to "every
// campaign", which is the shape that would leak: a refusal for a campaign the
// reader cannot even see.
func TestTheCampaignListAsksTheThemeLookupOnlyForCampaignsItShows(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaigns[1] = domain.Campaign{ID: 1, Slug: "greyhaven", Name: "Greyhaven"}
	st.campaigns[2] = domain.Campaign{ID: 2, Slug: "ravensmoot", Name: "Ravensmoot"}
	st.members[1] = domain.RoleGM
	st.members[2] = domain.RoleGM

	// A refusal for campaign 3, which this reader is not a member of.
	stub := &stubThemes{notices: map[int64]components.CampaignNotice{3: theRefusal}}

	_, code := themeNoticeBody(t, st, stub, 1)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if len(stub.asked) != 2 {
		t.Fatalf("the theme lookup was asked %d times, want 2 — once per campaign "+
			"on the page. A lookup that enumerated every campaign would walk a "+
			"manifest for each of them and is the shape that leaks a refusal for a "+
			"campaign this reader cannot see", len(stub.asked))
	}

	for _, id := range stub.asked {
		if id != 1 && id != 2 {
			t.Errorf("the theme lookup was asked about campaign %d, which is not on "+
				"this reader's page", id)
		}
	}
}

// themeNoticeBody serves the campaign list with a `ThemeNotices` wired, and
// returns the body and the status.
//
// Its own request path rather than the package's `request`, because that one
// builds its router from `newRouter(st)` and there is no seam to add a field to
// -- which is the honest shape of the problem: this route grew a dependency, and
// the helper that hid the router's construction is now the thing in the way.
// Everything else about the request matches `request`'s: the same identity
// middleware in front of the same mux, the same fixed clock.
//
// A copy of those four lines is also where a fixture starts to drift between
// tests, and a drifted fixture is how "both directions" quietly becomes "one
// direction, twice".
func themeNoticeBody(
	t *testing.T,
	st *fakeStore,
	themes accounts.ThemeNotices,
	userID int64,
) (body string, code int) {
	t.Helper()

	router := &accounts.Router{
		Store: st,
		Now:   func() time.Time { return time.Unix(1_700_000_000, 0) },
		Theme: themes,
	}

	mux := http.NewServeMux()
	router.Mount(mux)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.AddCookie(signedInCookie(t, st, userID))

	recorder := httptest.NewRecorder()
	identity.Authenticate(st)(mux).ServeHTTP(recorder, req)

	return recorder.Body.String(), recorder.Code
}
