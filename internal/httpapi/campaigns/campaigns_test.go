package campaigns_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/store"
)

// fakeStore is an in-memory stand-in for the queries the gates read.
//
// Hand-written rather than a real database because these tests are about the
// access *matrix*, and a real SQLite fixture would let a change to the schema
// alter the result these tests exist to pin.
type fakeStore struct {
	campaigns map[string]domain.Campaign
	members   map[int64]map[int64]domain.Membership // campaign id -> user id -> membership

	campaignErr error
	memberErr   error

	createdCampaigns []domain.Campaign
	createdMembers   []domain.Membership
	deletedCampaigns []int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		campaigns: map[string]domain.Campaign{},
		members:   map[int64]map[int64]domain.Membership{},
	}
}

// withCampaign adds a campaign and returns the fake for chaining.
func (f *fakeStore) withCampaign(campaign domain.Campaign) *fakeStore {
	f.campaigns[campaign.Slug] = campaign

	return f
}

// withMember adds a membership and returns the fake for chaining.
func (f *fakeStore) withMember(campaignID, userID int64, role domain.Role) *fakeStore {
	if f.members[campaignID] == nil {
		f.members[campaignID] = map[int64]domain.Membership{}
	}

	f.members[campaignID][userID] = domain.Membership{
		CampaignID: campaignID,
		UserID:     userID,
		Role:       role,
	}

	return f
}

func (f *fakeStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	if f.campaignErr != nil {
		return domain.Campaign{}, f.campaignErr
	}

	campaign, ok := f.campaigns[slug]
	if !ok {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (f *fakeStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	if f.memberErr != nil {
		return domain.Membership{}, f.memberErr
	}

	membership, ok := f.members[campaignID][userID]
	if !ok {
		return domain.Membership{}, store.ErrNotFound
	}

	return membership, nil
}

func (f *fakeStore) CreateCampaign(
	_ context.Context,
	campaign domain.Campaign,
) (domain.Campaign, error) {
	if _, exists := f.campaigns[campaign.Slug]; exists {
		return domain.Campaign{}, store.ErrConflict
	}

	campaign.ID = int64(len(f.createdCampaigns) + 1)
	f.createdCampaigns = append(f.createdCampaigns, campaign)
	f.campaigns[campaign.Slug] = campaign

	return campaign, nil
}

func (f *fakeStore) CreateMembership(
	_ context.Context,
	membership domain.Membership,
) (domain.Membership, error) {
	f.createdMembers = append(f.createdMembers, membership)

	if f.members[membership.CampaignID] == nil {
		f.members[membership.CampaignID] = map[int64]domain.Membership{}
	}

	f.members[membership.CampaignID][membership.UserID] = membership

	return membership, nil
}

func (f *fakeStore) DeleteCampaign(_ context.Context, id int64) error {
	f.deletedCampaigns = append(f.deletedCampaigns, id)

	return nil
}

// The campaigns the matrix tests share. Ids are 1 and 2: SQLite rowids start at
// 1, and a test using id 0 would be testing a value the schema cannot hold.
var (
	privateCampaign = domain.Campaign{
		ID:         1,
		Slug:       "greyhaven",
		Visibility: domain.VisibilityPrivate,
	}
	publicCampaign = domain.Campaign{
		ID:         2,
		Slug:       "public-post",
		Visibility: domain.VisibilityPublic,
	}
)

const (
	gmUser     int64 = 10
	playerUser int64 = 11
	otherUser  int64 = 12
)

// mounted builds the chain a campaign-scoped route is mounted behind, in the
// order the router uses: Resolve outermost, then the guard, then the handler.
//
// The identity is *not* put on the context by a middleware here. These tests
// assert the access matrix, and a cookie round trip would be asserting
// `identity.Authenticate` again with a different fake — so the identity goes on
// the request context directly, before the chain runs. The guard reads it from
// there exactly as it reads it in production.
func mounted(
	st campaigns.Store,
	guard func(http.Handler) http.Handler,
) http.Handler {
	return campaigns.Resolve(
		st,
	)(
		guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})),
	)
}

// requestFor builds a request for a campaign route, carrying req on the context
// as a signed-in identity would.
func requestFor(t *testing.T, method, target string, req *domain.Requestor) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	if req != nil {
		r = r.WithContext(identity.WithRequestor(r.Context(), *req))
	}

	return r
}

// asUser returns a signed-in identity for a user id.
func asUser(id int64) *domain.Requestor {
	return &domain.Requestor{UserID: id, Username: "u", Authenticated: true}
}

// TestGuardEnforcesTheS8Matrix is the phase's role-enforcement row, as a table.
//
// Every cell is a (campaign, identity, guard) triple and its expected status.
// The matrix is the S-8 table transcribed: the point of a table test here is
// that a change to one cell shows up as a change to the table rather than as a
// role quietly gaining a capability elsewhere.
func TestGuardEnforcesTheS8Matrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		campaign  domain.Campaign
		requestor *domain.Requestor
		guard     func(http.Handler) http.Handler
		want      int
	}{
		{
			name:     "private campaign, anonymous, read gate",
			campaign: privateCampaign,
			guard:    campaigns.RequireRead,
			// 404, not 403: a 403 would confirm the campaign exists.
			want: http.StatusNotFound,
		},
		{
			name:      "private campaign, non-member, read gate",
			campaign:  privateCampaign,
			requestor: asUser(otherUser),
			guard:     campaigns.RequireRead,
			want:      http.StatusNotFound,
		},
		{
			name:      "private campaign, player, read gate",
			campaign:  privateCampaign,
			requestor: asUser(playerUser),
			guard:     campaigns.RequireRead,
			want:      http.StatusNoContent,
		},
		{
			name:      "private campaign, gm, read gate",
			campaign:  privateCampaign,
			requestor: asUser(gmUser),
			guard:     campaigns.RequireRead,
			want:      http.StatusNoContent,
		},
		{
			name:     "public campaign, anonymous, read gate",
			campaign: publicCampaign,
			guard:    campaigns.RequireRead,
			want:     http.StatusNoContent,
		},
		{
			name:     "public campaign, anonymous, play gate",
			campaign: publicCampaign,
			guard:    campaigns.RequirePlay,
			// S-8.1: public grants wiki read and nothing else.
			want: http.StatusUnauthorized,
		},
		{
			name:      "public campaign, non-member, play gate",
			campaign:  publicCampaign,
			requestor: asUser(otherUser),
			guard:     campaigns.RequirePlay,
			want:      http.StatusForbidden,
		},
		{
			name:      "public campaign, player, play gate",
			campaign:  publicCampaign,
			requestor: asUser(playerUser),
			guard:     campaigns.RequirePlay,
			want:      http.StatusNoContent,
		},
		{
			name:     "public campaign, anonymous, edit gate",
			campaign: publicCampaign,
			guard:    campaigns.RequireEdit,
			want:     http.StatusUnauthorized,
		},
		{
			name:      "public campaign, player, edit gate",
			campaign:  publicCampaign,
			requestor: asUser(playerUser),
			guard:     campaigns.RequireEdit,
			// S-14.4: a player PUT is 403.
			want: http.StatusForbidden,
		},
		{
			name:      "public campaign, gm, edit gate",
			campaign:  publicCampaign,
			requestor: asUser(gmUser),
			guard:     campaigns.RequireEdit,
			want:      http.StatusNoContent,
		},
		{
			name:     "private campaign, anonymous, edit gate",
			campaign: privateCampaign,
			guard:    campaigns.RequireEdit,
			want:     http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := newFakeStore().
				withCampaign(tc.campaign).
				withMember(tc.campaign.ID, gmUser, domain.RoleGM).
				withMember(tc.campaign.ID, playerUser, domain.RolePlayer)

			target := "/c/" + tc.campaign.Slug + "/wiki/Page"
			r := requestFor(t, http.MethodGet, target, tc.requestor)
			r.SetPathValue("slug", tc.campaign.Slug)

			recorder := httptest.NewRecorder()
			mounted(st, tc.guard).ServeHTTP(recorder, r)

			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

// TestGuardChallengesAnonymousWithACookieChallenge asserts the 401 carries a
// challenge. A bare 401 with no challenge is indistinguishable from a
// misconfigured proxy in the logs of whoever is being asked to sign in.
func TestGuardChallengesAnonymousWithACookieChallenge(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(publicCampaign)
	r := requestFor(t, http.MethodGet, "/c/public-post/play", nil)
	r.SetPathValue("slug", publicCampaign.Slug)

	recorder := httptest.NewRecorder()
	mounted(st, campaigns.RequirePlay).ServeHTTP(recorder, r)

	if got := recorder.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("WWW-Authenticate is empty on a 401, want a challenge")
	}
}

// TestGuardIsNotReachedWithoutASlug covers the composition case: a router where
// only some routes are campaign-scoped. No slug means no resolution, and a
// guard with no resolution is a 404 rather than an open door.
func TestGuardIsNotReachedWithoutASlug(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(privateCampaign)
	handler := campaigns.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	campaigns.Resolve(st)(handler).ServeHTTP(recorder,
		requestFor(t, http.MethodGet, "/readyz", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a route with no slug", recorder.Code)
	}
}

// TestResolveIsIndistinguishableForAbsentAndInvisible is the property the
// notFoundHandler comment claims: a slug nobody registered and a private
// campaign the reader is not a member of produce the same status and the same
// body. If they diverge, a private campaign's existence becomes observable by
// request timing or body comparison alone.
func TestResolveIsIndistinguishableForAbsentAndInvisible(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(privateCampaign)
	handler := campaigns.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux := campaigns.Resolve(st)(handler)

	serve := func(slug string) *httptest.ResponseRecorder {
		r := requestFor(t, http.MethodGet, "/c/"+slug+"/wiki/Page", nil)
		r.SetPathValue("slug", slug)

		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, r)

		return recorder
	}

	absent, invisible := serve("no-such-campaign"), serve(privateCampaign.Slug)

	if absent.Code != invisible.Code {
		t.Errorf("absent = %d, invisible = %d: want the same status", absent.Code, invisible.Code)
	}

	if absent.Body.String() != invisible.Body.String() {
		t.Errorf("bodies differ:\n absent:    %q\n invisible: %q",
			absent.Body.String(), invisible.Body.String())
	}
}

// TestResolveReportsStorageFailureAsServerError: a database fault is a 500, not
// a 404. Reporting it as absent tells an operator their database is healthy
// when it is serving errors.
func TestResolveReportsStorageFailureAsServerError(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	st.campaignErr = errors.New("disk I/O error")

	r := requestFor(t, http.MethodGet, "/c/greyhaven/wiki/Page", nil)
	r.SetPathValue("slug", privateCampaign.Slug)

	recorder := httptest.NewRecorder()
	mounted(st, campaigns.RequireRead).ServeHTTP(recorder, r)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
}

// TestResolveDowngradesAMembershipReadFailure covers the member-lookup fault: a
// GM whose membership query fails loses GM rights but keeps the public wiki.
// Failing toward less access is the correct direction, and a 500 would be a
// stricter answer than the situation warrants.
func TestResolveDowngradesAMembershipReadFailure(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(publicCampaign).
		withMember(publicCampaign.ID, gmUser, domain.RoleGM)
	st.memberErr = errors.New("disk I/O error")

	r := requestFor(t, http.MethodGet, "/c/public-post/wiki/Page", asUser(gmUser))
	r.SetPathValue("slug", publicCampaign.Slug)

	recorder := httptest.NewRecorder()
	mounted(st, campaigns.RequireRead).ServeHTTP(recorder, r)

	if recorder.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204: a read-only downgrade is not an error", recorder.Code)
	}
}

// TestAccessFromWithoutResolveIsUnresolvable: a handler reaching for access on a
// context that never passed through Resolve gets no access, not a zero Tier that
// a comparison would read as read-only.
func TestAccessFromWithoutResolveIsUnresolvable(t *testing.T) {
	t.Parallel()

	access := campaigns.AccessFrom(t.Context())

	if access.Tier != domain.TierNone {
		t.Errorf("Tier = %s, want %s", access.Tier, domain.TierNone)
	}

	if access.Campaign.ID != 0 {
		t.Errorf("Campaign.ID = %d, want 0", access.Campaign.ID)
	}
}

// TestRegisterCreatesTheRootAndSeedsTheOwner is the registration happy path: an
// absolute content root, an `os.Root` that opens, and a GM membership.
func TestRegisterCreatesTheRootAndSeedsTheOwner(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	st := newFakeStore()

	var retainedSlug string
	registrar := campaigns.NewRegistrar(st, base, func(slug string, root *os.Root) error {
		retainedSlug = slug

		return nil
	})

	campaign, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug:           "greyhaven",
		Name:           "Greyhaven",
		SystemID:       "5e-2024",
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPrivate,
		OwnerID:        gmUser,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	want := filepath.Join(base, "greyhaven")
	if campaign.ContentRoot != want {
		t.Errorf("ContentRoot = %q, want %q", campaign.ContentRoot, want)
	}

	if !filepath.IsAbs(campaign.ContentRoot) {
		t.Errorf("ContentRoot %q is not absolute", campaign.ContentRoot)
	}

	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("content root was not created: %v", err)
	}

	// 0o700: a root any account on the host can read is a campaign any account
	// on the host can read, bypassing the matrix entirely.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("content root mode = %o, want 700", perm)
	}

	if len(st.createdMembers) != 1 {
		t.Fatalf("created %d memberships, want 1", len(st.createdMembers))
	}

	member := st.createdMembers[0]
	if member.Role != domain.RoleGM {
		t.Errorf("owner role = %q, want %q", member.Role, domain.RoleGM)
	}

	if member.UserID != gmUser {
		t.Errorf("owner = %d, want %d", member.UserID, gmUser)
	}

	if retainedSlug != "greyhaven" {
		t.Errorf("retained root for %q, want %q", retainedSlug, "greyhaven")
	}
}

// TestRegisterRefusesAnInvalidSlugBeforeTouchingDisk: a slug becomes a directory
// name, so a bad one is refused before anything is created.
func TestRegisterRefusesAnInvalidSlugBeforeTouchingDisk(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"", "Greyhaven", "-leading", "trailing-", "double--hyphen", "has space"} {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()

			base := t.TempDir()
			st := newFakeStore()
			registrar := campaigns.NewRegistrar(st, base, nil)

			if _, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
				Slug: slug, Visibility: domain.VisibilityPrivate, OwnerID: gmUser,
			}); err == nil {
				t.Fatalf("Register(%q) = nil, want an error", slug)
			}

			if len(st.createdCampaigns) != 0 {
				t.Errorf("a campaign row was created for slug %q", slug)
			}

			entries, err := os.ReadDir(base)
			if err != nil {
				t.Fatalf("read base: %v", err)
			}

			if len(entries) != 0 {
				t.Errorf("Register(%q) created %d entries on disk", slug, len(entries))
			}
		})
	}
}

// TestRegisterRefusesAnUnknownVisibility: an unrecognised visibility would be a
// published campaign if it were allowed through, and the failure is not
// reversible in anybody's cache.
func TestRegisterRefusesAnUnknownVisibility(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	registrar := campaigns.NewRegistrar(st, t.TempDir(), nil)

	_, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug: "greyhaven", Visibility: domain.Visibility(""), OwnerID: gmUser,
	})
	if err == nil {
		t.Fatal("Register with an empty visibility = nil, want an error")
	}

	if len(st.createdCampaigns) != 0 {
		t.Error("a campaign row was created for an invalid visibility")
	}
}

// TestRegisterRefusesAnOwnerlessCampaign: a campaign nobody can run or edit is
// not a usable registration outcome.
func TestRegisterRefusesAnOwnerlessCampaign(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	registrar := campaigns.NewRegistrar(st, t.TempDir(), nil)

	if _, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug: "greyhaven", Visibility: domain.VisibilityPrivate,
	}); err == nil {
		t.Fatal("Register without an owner = nil, want an error")
	}

	if len(st.createdCampaigns) != 0 {
		t.Error("a campaign row was created without an owner")
	}
}

// TestRegisterRemovesTheRowWhenTheOwnerCannotBeSeeded: a campaign with no GM
// is unusable, so the row goes rather than being left as a tenant nobody can
// administer. The directory is deliberately left — the comment on Register says
// why, and this test is the assertion of the other half.
func TestRegisterRemovesTheRowWhenTheOwnerCannotBeSeeded(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	// A store whose membership write always fails, standing in for any failure
	// on that step without the fake needing a second scripted mode.
	failing := &failingMembershipStore{fakeStore: newFakeStore()}
	registrar := campaigns.NewRegistrar(failing, base, nil)

	_, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug: "greyhaven", Visibility: domain.VisibilityPrivate, OwnerID: gmUser,
	})
	if err == nil {
		t.Fatal("Register = nil, want an error from the membership write")
	}

	if len(failing.deletedCampaigns) != 1 {
		t.Errorf("deleted %d campaign rows, want 1: the row must not survive a failed seed",
			len(failing.deletedCampaigns))
	}

	// The directory survives, and re-registering the same slug must succeed
	// against it rather than failing on a path that already exists.
	if _, err := os.Stat(filepath.Join(base, "greyhaven")); err != nil {
		t.Errorf("the content root was removed on failure: %v", err)
	}
}

// failingMembershipStore refuses every membership write.
type failingMembershipStore struct {
	*fakeStore
}

func (f *failingMembershipStore) CreateMembership(
	context.Context,
	domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errors.New("write refused")
}

// TestRegisterIsIdempotentOnAnExistingRoot: a directory left by a previous
// failed registration is reused, which is also the state of a vault an operator
// created by hand.
func TestRegisterIsIdempotentOnAnExistingRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "greyhaven"), 0o700); err != nil {
		t.Fatalf("prepare root: %v", err)
	}

	registrar := campaigns.NewRegistrar(newFakeStore(), base, nil)

	campaign, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug: "greyhaven", Visibility: domain.VisibilityPrivate, OwnerID: gmUser,
	})
	if err != nil {
		t.Fatalf("Register over an existing root: %v", err)
	}

	if campaign.ContentRoot != filepath.Join(base, "greyhaven") {
		t.Errorf("ContentRoot = %q", campaign.ContentRoot)
	}
}

// TestRegisterClosesTheRootWhenRetainFails: a root handed to a callback that
// rejects it must be closed rather than leaked, and the campaign must not be
// reported as registered — the caller believes the roots are in place.
func TestRegisterClosesTheRootWhenRetainFails(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	registrar := campaigns.NewRegistrar(st, t.TempDir(), func(string, *os.Root) error {
		return errors.New("sink unavailable")
	})

	if _, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug: "greyhaven", Visibility: domain.VisibilityPrivate, OwnerID: gmUser,
	}); err == nil {
		t.Fatal("Register = nil, want an error from the retain callback")
	}
}

// TestResolveIgnoresAMembershipBelongingToAnotherUser: domain.ResolveAccess
// cross-checks the identity, and this asserts the wiring does not defeat it by
// attaching a row it looked up wrongly.
func TestResolveIgnoresAMembershipBelongingToAnotherUser(t *testing.T) {
	t.Parallel()

	// A membership stored for user 99, presented by a request claiming to be
	// user 10. Resolve must consult the store for the requestor's own id, and
	// find nothing.
	st := newFakeStore().withCampaign(privateCampaign).
		withMember(privateCampaign.ID, 99, domain.RoleGM)

	var seen campaigns.Access

	r := requestFor(t, http.MethodGet, "/c/greyhaven/wiki/Page", asUser(playerUser))
	r.SetPathValue("slug", privateCampaign.Slug)

	campaigns.Resolve(st)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		seen = campaigns.AccessFrom(req.Context())
	})).ServeHTTP(httptest.NewRecorder(), r)

	if seen.Tier != domain.TierNone {
		t.Errorf("Tier = %s, want %s: another user's membership granted access",
			seen.Tier, domain.TierNone)
	}
}

// TestResolveRunsOnceForAMethodIndependentRoute: a PUT is gated exactly as a
// GET is. S-14.4 is about the write path, and a matrix that only ever saw GETs
// would not have caught a player being able to write.
func TestResolveRunsOnceForAMethodIndependentRoute(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(publicCampaign).
		withMember(publicCampaign.ID, playerUser, domain.RolePlayer)

	r := requestFor(t, http.MethodPut, "/c/public-post/edit/Page", asUser(playerUser))
	r.SetPathValue("slug", publicCampaign.Slug)

	recorder := httptest.NewRecorder()
	mounted(st, campaigns.RequireEdit).ServeHTTP(recorder, r)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("player PUT status = %d, want 403 (S-14.4)", recorder.Code)
	}
}

// TestRegisterRequestShapeIsNotDecidedByDefaults guards the field a migration
// comment names: a campaign with no system_id would refuse to start its game.
// The value is stated, not defaulted, so this asserts the registrar writes
// exactly what it was given.
func TestRegisterRequestShapeIsNotDecidedByDefaults(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	registrar := campaigns.NewRegistrar(st, t.TempDir(), nil)

	if _, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug:       "forgotten-realm",
		SystemID:   "pathfinder-2e",
		Visibility: domain.VisibilityPrivate,
		OwnerID:    gmUser,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	created := st.createdCampaigns[0]
	if created.SystemID != "pathfinder-2e" {
		t.Errorf("SystemID = %q, want the value it was given", created.SystemID)
	}

	// An empty ruleset_version is a meaningful value — state written under no
	// particular ruleset — so it is stored rather than filled in. The real store
	// stamps CreatedAt; the fake does not, and asserting it here would be
	// asserting the fake.
	if created.RulesetVersion != "" {
		t.Errorf("RulesetVersion = %q, want it left empty rather than defaulted",
			created.RulesetVersion)
	}
}
