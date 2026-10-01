package domain_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// memberLabel renders a membership for a failure message without dragging a
// timestamp into it.
func memberLabel(member *domain.Membership) string {
	if member == nil {
		return "no membership"
	}

	return "membership(role=" + member.Role.String() + ")"
}

func TestResolveAccessMatchesTheSection8Matrix(t *testing.T) {
	t.Parallel()

	privateCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate}
	publicCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPublic}

	// A context carrying is_admin with no session is not an administrator. It
	// is the shape a builder that skipped authentication produces, and it is in
	// the table because the matrix has to answer for it too.
	anonymous := domain.Requestor{}
	member := domain.Requestor{UserID: 7, Username: "pat", Authenticated: true}
	unauthenticatedAdmin := domain.Requestor{IsAdmin: true}
	admin := domain.Requestor{UserID: 7, Username: "root", IsAdmin: true, Authenticated: true}

	playerRow := &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RolePlayer}
	gmRow := &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RoleGM}

	cases := []struct {
		name        string
		requestor   domain.Requestor
		membership  *domain.Membership
		wantPrivate domain.Tier
		wantPublic  domain.Tier
	}{
		{
			// Anonymous row: `visibility='public'` only, read-only wiki and
			// public assets. On a private campaign there is nothing to grant.
			name:        "anonymous",
			requestor:   anonymous,
			wantPrivate: domain.TierNone,
			wantPublic:  domain.TierReadOnly,
		},
		{
			// S-8.1: games always require membership, and `public` grants wiki
			// read only — so signing in buys an authenticated non-member
			// exactly what an anonymous visitor already had.
			name:        "authenticated non-member",
			requestor:   member,
			wantPrivate: domain.TierNone,
			wantPublic:  domain.TierReadOnly,
		},
		{
			name:        "unauthenticated with is_admin",
			requestor:   unauthenticatedAdmin,
			wantPrivate: domain.TierNone,
			wantPublic:  domain.TierReadOnly,
			membership:  gmRow,
		},
		{
			// S-2.7: is_admin is instance-level and exists for campaign
			// registration and user management. It is not a campaign role.
			name:        "instance admin who is not a member",
			requestor:   admin,
			wantPrivate: domain.TierNone,
			wantPublic:  domain.TierReadOnly,
		},
		{
			// A membership is honoured on either visibility: `private` restricts
			// who may read, not which members exist.
			name:        "player member",
			requestor:   member,
			membership:  playerRow,
			wantPrivate: domain.TierPlayer,
			wantPublic:  domain.TierPlayer,
		},
		{
			name:        "gm member",
			requestor:   member,
			membership:  gmRow,
			wantPrivate: domain.TierGM,
			wantPublic:  domain.TierGM,
		},
		{
			// The flag neither grants nor withholds: the row does.
			name:        "instance admin who is a gm member",
			requestor:   admin,
			membership:  gmRow,
			wantPrivate: domain.TierGM,
			wantPublic:  domain.TierGM,
		},
		{
			// The case that makes the previous one meaningful. An administrator
			// who is only a member of this campaign is a player here, and the
			// instance-wide privilege does not leak into the content.
			name:        "instance admin who is a player member",
			requestor:   admin,
			membership:  playerRow,
			wantPrivate: domain.TierPlayer,
			wantPublic:  domain.TierPlayer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			visibilities := []struct {
				name     string
				campaign domain.Campaign
				want     domain.Tier
			}{
				{name: "private", campaign: privateCampaign, want: tc.wantPrivate},
				{name: "public", campaign: publicCampaign, want: tc.wantPublic},
			}

			for _, vis := range visibilities {
				t.Run(vis.name, func(t *testing.T) {
					t.Parallel()

					got := domain.ResolveAccess(vis.campaign, tc.membership, tc.requestor)
					if got != vis.want {
						t.Errorf(
							"ResolveAccess(%s campaign, %s, %+v) = %v, want %v",
							vis.name, memberLabel(tc.membership), tc.requestor, got, vis.want,
						)
					}
				})
			}
		})
	}
}

// TestResolveAccessDoesNotGrantCampaignAccessToAnInstanceAdmin is the test for
// the mistake this type exists to prevent. Folding is_admin into a campaign
// tier would hand every private campaign on the instance to every
// administrator, and it would do so quietly, to a server an operator believes
// is private.
func TestResolveAccessDoesNotGrantCampaignAccessToAnInstanceAdmin(t *testing.T) {
	t.Parallel()

	privateCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate}
	publicCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPublic}

	admin := domain.Requestor{UserID: 7, Username: "root", IsAdmin: true, Authenticated: true}

	cases := []struct {
		name       string
		campaign   domain.Campaign
		membership *domain.Membership
		want       domain.Tier
	}{
		{
			name:     "private campaign, no membership",
			campaign: privateCampaign,
			want:     domain.TierNone,
		},
		{
			name:     "public campaign, no membership",
			campaign: publicCampaign,
			want:     domain.TierReadOnly,
		},
		{
			name:       "private campaign, player membership",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RolePlayer},
			want:       domain.TierPlayer,
		},
		{
			name:       "public campaign, player membership",
			campaign:   publicCampaign,
			membership: &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RolePlayer},
			want:       domain.TierPlayer,
		},
		{
			name:       "private campaign, membership naming another campaign",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 2, UserID: 7, Role: domain.RoleGM},
			want:       domain.TierNone,
		},
		{
			name:       "public campaign, membership naming another campaign",
			campaign:   publicCampaign,
			membership: &domain.Membership{CampaignID: 2, UserID: 7, Role: domain.RoleGM},
			want:       domain.TierReadOnly,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := domain.ResolveAccess(tc.campaign, tc.membership, admin)
			if got != tc.want {
				t.Errorf(
					"ResolveAccess(%+v, %s, admin) = %v, want %v",
					tc.campaign,
					memberLabel(tc.membership),
					got,
					tc.want,
				)
			}
		})
	}

	// The capability is real, so a reader of this file can see that what is
	// refused above is the campaign tier and not the administration itself.
	if !admin.CanManageInstance() {
		t.Error("admin.CanManageInstance() = false, want true for an authenticated instance admin")
	}
}

// TestResolveAccessFailsClosedForUnauthenticatedRequestors covers a context
// whose authentication did not populate, and the membership that a careless
// caller would pass it anyway. The invariant is a cap, not a list: no
// combination of arguments may resolve an unauthenticated requestor above
// TierReadOnly, and the ordering of the constants is the capability ordering
// the comparison relies on.
func TestResolveAccessFailsClosedForUnauthenticatedRequestors(t *testing.T) {
	t.Parallel()

	privateCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate}
	publicCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPublic}
	// A visibility this build does not understand. It is not `public`, so it
	// must not be read as `public`.
	unknownCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.Visibility("")}

	gmRow := &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RoleGM}
	playerRow := &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RolePlayer}

	cases := []struct {
		name       string
		campaign   domain.Campaign
		membership *domain.Membership
		requestor  domain.Requestor
		want       domain.Tier
	}{
		{
			name:       "gm membership, public campaign",
			campaign:   publicCampaign,
			membership: gmRow,
			requestor:  domain.Requestor{},
			want:       domain.TierReadOnly,
		},
		{
			name:       "gm membership, private campaign",
			campaign:   privateCampaign,
			membership: gmRow,
			requestor:  domain.Requestor{},
			want:       domain.TierNone,
		},
		{
			name:       "player membership, unknown visibility",
			campaign:   unknownCampaign,
			membership: playerRow,
			requestor:  domain.Requestor{},
			want:       domain.TierNone,
		},
		{
			// A user id without Authenticated is a half-built context: the id
			// arrived and the proof did not. The id is not the proof.
			name:       "gm membership, user id but no session",
			campaign:   publicCampaign,
			membership: gmRow,
			requestor:  domain.Requestor{UserID: 7, Username: "pat"},
			want:       domain.TierReadOnly,
		},
		{
			name:       "gm membership, is_admin but no session",
			campaign:   publicCampaign,
			membership: gmRow,
			requestor:  domain.Requestor{IsAdmin: true},
			want:       domain.TierReadOnly,
		},
		{
			name:      "no membership",
			campaign:  publicCampaign,
			requestor: domain.Requestor{},
			want:      domain.TierReadOnly,
		},
		{
			// Matched ids, and still capped: a membership that agrees with the
			// requestor is not what makes a requestor authenticated.
			name:       "gm membership naming user 0",
			campaign:   publicCampaign,
			membership: &domain.Membership{CampaignID: 1, UserID: 7, Role: domain.RoleGM},
			requestor:  domain.Requestor{UserID: 7, Authenticated: false, IsAdmin: true},
			want:       domain.TierReadOnly,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := domain.ResolveAccess(tc.campaign, tc.membership, tc.requestor)
			if got != tc.want {
				t.Errorf(
					"ResolveAccess(%+v, %s, %+v) = %v, want %v",
					tc.campaign,
					memberLabel(tc.membership),
					tc.requestor,
					got,
					tc.want,
				)
			}

			if !tc.requestor.Authenticated && got > domain.TierReadOnly {
				t.Errorf(
					"ResolveAccess(%+v, %s, %+v) = %v; an unauthenticated requestor must not resolve above %v",
					tc.campaign,
					memberLabel(tc.membership),
					tc.requestor,
					got,
					domain.TierReadOnly,
				)
			}
		})
	}
}

// TestResolveAccessIgnoresAMembershipForAnotherIdentity is the tenant boundary.
// A row loaded for the wrong user or the wrong campaign is an ordinary
// mistake, and ResolveAccess is the last place before it becomes access.
func TestResolveAccessIgnoresAMembershipForAnotherIdentity(t *testing.T) {
	t.Parallel()

	privateCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate}

	cases := []struct {
		name       string
		campaign   domain.Campaign
		membership *domain.Membership
		requestor  domain.Requestor
		want       domain.Tier
	}{
		{
			name:       "membership for another user",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 1, UserID: 8, Role: domain.RoleGM},
			requestor:  domain.Requestor{UserID: 7, Authenticated: true},
			want:       domain.TierNone,
		},
		{
			name:       "membership for another campaign",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 2, UserID: 7, Role: domain.RoleGM},
			requestor:  domain.Requestor{UserID: 7, Authenticated: true},
			want:       domain.TierNone,
		},
		{
			name:       "membership for another user and campaign",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 2, UserID: 8, Role: domain.RoleGM},
			requestor:  domain.Requestor{UserID: 7, Authenticated: true},
			want:       domain.TierNone,
		},
		{
			// An authenticated requestor with no id cannot be matched to any
			// row, because both columns are rowids and the first row is 1.
			name:       "authenticated with no user id",
			campaign:   privateCampaign,
			membership: &domain.Membership{CampaignID: 1, UserID: 0, Role: domain.RoleGM},
			requestor:  domain.Requestor{Authenticated: true},
			want:       domain.TierNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := domain.ResolveAccess(tc.campaign, tc.membership, tc.requestor)
			if got != tc.want {
				t.Errorf("ResolveAccess(campaign %d, %s, %+v) = %v, want %v",
					tc.campaign.ID, memberLabel(tc.membership), tc.requestor, got, tc.want)
			}
		})
	}
}

// TestResolveAccessLocksOutAnUnrecognisedRole asserts that a role this build
// does not understand is not a player. The member is locked out and the bad
// value stays visible in the database, where an operator can see it.
func TestResolveAccessLocksOutAnUnrecognisedRole(t *testing.T) {
	t.Parallel()

	privateCampaign := domain.Campaign{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate}
	requestor := domain.Requestor{UserID: 7, Authenticated: true}

	roles := []domain.Role{
		"",
		"editor",
		"GM",
		"gm ",
		"Player",
		"owner",
		"admin",
	}

	for _, role := range roles {
		t.Run(role.String(), func(t *testing.T) {
			t.Parallel()

			got := domain.ResolveAccess(privateCampaign, &domain.Membership{
				CampaignID: 1,
				UserID:     7,
				Role:       role,
			}, requestor)
			if got != domain.TierNone {
				t.Errorf(
					"ResolveAccess(private campaign, role %q, member) = %v, want %v",
					role,
					got,
					domain.TierNone,
				)
			}
		})
	}

	// The zero Membership is the case a forgotten field produces, so it is
	// worth naming: no campaign, no user, and no role.
	if got := domain.ResolveAccess(
		privateCampaign,
		&domain.Membership{},
		requestor,
	); got != domain.TierNone {
		t.Errorf(
			"ResolveAccess(private campaign, zero membership, member) = %v, want %v",
			got,
			domain.TierNone,
		)
	}
}

// TestResolveAccessIsTotalAndDeterministic walks the degenerate inputs
// together and asserts two things: every combination answers, and the answer
// does not move. A rule that could fail would put the meaning of an
// unresolvable request back in the caller, and a caller under pressure answers
// "allow".
func TestResolveAccessIsTotalAndDeterministic(t *testing.T) {
	t.Parallel()

	campaigns := []domain.Campaign{
		{ID: 1, Slug: "curse", Visibility: domain.VisibilityPrivate},
		{ID: 1, Slug: "curse", Visibility: domain.VisibilityPublic},
		{ID: 1, Slug: "curse", Visibility: domain.Visibility("")},
		{ID: 1, Slug: "curse", Visibility: domain.Visibility("PUBLIC")},
		{},
	}

	requestors := []domain.Requestor{
		{},
		{UserID: 7, Authenticated: true},
		{UserID: 7, Username: "pat", IsAdmin: true, Authenticated: true},
		{UserID: 7, IsAdmin: true},
		{IsAdmin: true},
		{Authenticated: true},
		{UserID: -1, Authenticated: true},
	}

	memberships := []*domain.Membership{
		nil,
		{CampaignID: 1, UserID: 7, Role: domain.RoleGM},
		{CampaignID: 1, UserID: 7, Role: domain.RolePlayer},
		{CampaignID: 1, UserID: 7},
		{CampaignID: 0, UserID: 0, Role: domain.RoleGM},
		{CampaignID: 2, UserID: 7, Role: domain.RoleGM},
		{CampaignID: 1, UserID: 8, Role: domain.RoleGM},
		{CampaignID: 1, UserID: 7, Role: domain.Role("editor")},
	}

	for _, campaign := range campaigns {
		for _, requestor := range requestors {
			for _, membership := range memberships {
				got := domain.ResolveAccess(campaign, membership, requestor)

				switch got {
				case domain.TierNone, domain.TierReadOnly, domain.TierPlayer, domain.TierGM:
				default:
					t.Errorf("ResolveAccess(%+v, %s, %+v) = %d, which is not a tier",
						campaign, memberLabel(membership), requestor, int64(got))
				}

				// No clock, no randomness, no map iteration: the same question
				// twice has the same answer, which is what makes a cached
				// decision safe to make once.
				again := domain.ResolveAccess(campaign, membership, requestor)
				if again != got {
					t.Errorf("ResolveAccess(%+v, %s, %+v) = %d then %d; the rule must be pure",
						campaign, memberLabel(membership), requestor, int64(got), int64(again))
				}
			}
		}
	}

	// Totality is part of the signature, so assert it as one: a second result
	// would be an error every caller has to decide what to do with.
	if results := reflect.TypeOf(domain.ResolveAccess).NumOut(); results != 1 {
		t.Errorf("ResolveAccess returns %d values, want 1: it must not be able to fail", results)
	}
}

func TestTierCapabilitiesFollowTheAccessMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tier    domain.Tier
		canRead bool
		canPlay bool
		canEdit bool
	}{
		// Anonymous row: read-only wiki and public assets, on a public
		// campaign only — which is what TierReadOnly means, and why it is not a
		// statement that the requestor is anonymous.
		{tier: domain.TierNone, canRead: false, canPlay: false, canEdit: false},
		{tier: domain.TierReadOnly, canRead: true, canPlay: false, canEdit: false},
		// Campaign player: play only, no content write (S-14.4).
		{tier: domain.TierPlayer, canRead: true, canPlay: true, canEdit: false},
		// Campaign GM: content edit, game control, and invite — one gate.
		{tier: domain.TierGM, canRead: true, canPlay: true, canEdit: true},
	}

	for _, tc := range cases {
		t.Run(tc.tier.String(), func(t *testing.T) {
			t.Parallel()

			if got := tc.tier.CanRead(); got != tc.canRead {
				t.Errorf("%v.CanRead() = %t, want %t", tc.tier, got, tc.canRead)
			}

			if got := tc.tier.CanPlay(); got != tc.canPlay {
				t.Errorf("%v.CanPlay() = %t, want %t", tc.tier, got, tc.canPlay)
			}

			if got := tc.tier.CanEdit(); got != tc.canEdit {
				t.Errorf("%v.CanEdit() = %t, want %t", tc.tier, got, tc.canEdit)
			}
		})
	}
}

// TestTierPredicatesFailClosedOnAnUnknownTier is the reason no predicate is a
// comparison. Tier is an int, so a conversion can produce a value outside the
// set, and `t >= TierGM` would make that value the most privileged value there
// is. Every predicate is an equality against a constant instead, so an
// unrecognised tier grants nothing.
func TestTierPredicatesFailClosedOnAnUnknownTier(t *testing.T) {
	t.Parallel()

	unknownTiers := []domain.Tier{
		-1,
		4,
		42,
		math.MaxInt,
		math.MinInt,
	}

	for _, tier := range unknownTiers {
		t.Run(tier.String(), func(t *testing.T) {
			t.Parallel()

			if tier.CanRead() {
				t.Errorf("Tier(%d).CanRead() = true, want false", int64(tier))
			}

			if tier.CanPlay() {
				t.Errorf("Tier(%d).CanPlay() = true, want false", int64(tier))
			}

			if tier.CanEdit() {
				t.Errorf("Tier(%d).CanEdit() = true, want false", int64(tier))
			}
		})
	}
}

func TestTierStringLabelsKnownTiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tier domain.Tier
		want string
	}{
		{tier: domain.TierNone, want: "none"},
		{tier: domain.TierReadOnly, want: "read-only"},
		{tier: domain.TierPlayer, want: "player"},
		{tier: domain.TierGM, want: "gm"},
		// A log line is where an unrecognised tier gets mistaken for a real
		// one, so it renders as something that cannot be read as a privilege.
		{tier: domain.Tier(4), want: "invalid"},
		{tier: domain.Tier(-1), want: "invalid"},
	}

	for _, tc := range cases {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("Tier(%d).String() = %q, want %q", int64(tc.tier), got, tc.want)
		}
	}
}

// TestCanManageInstanceRequiresAnAuthenticatedRequestor pins the other half of
// the S-2.7 separation. The instance capability is real and is asked for
// separately from the campaign tier, so a context that was built rather than
// resolved must not carry it.
func TestCanManageInstanceRequiresAnAuthenticatedRequestor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		requestor domain.Requestor
		want      bool
	}{
		{name: "empty", requestor: domain.Requestor{}, want: false},
		{
			name:      "is_admin without a session",
			requestor: domain.Requestor{UserID: 7, IsAdmin: true},
			want:      false,
		},
		{
			name:      "session without is_admin",
			requestor: domain.Requestor{UserID: 7, Authenticated: true},
			want:      false,
		},
		{
			name:      "authenticated instance admin",
			requestor: domain.Requestor{UserID: 7, IsAdmin: true, Authenticated: true},
			want:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.requestor.CanManageInstance(); got != tc.want {
				t.Errorf(
					"Requestor%+v.CanManageInstance() = %t, want %t",
					tc.requestor,
					got,
					tc.want,
				)
			}
		})
	}
}
