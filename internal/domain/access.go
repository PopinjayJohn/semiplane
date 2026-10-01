// Package domain holds semiplane's pure types and rules: the identity and
// tenancy entities, the enums the schema stores as text, and the one function
// that decides what a request may do to a campaign.
//
// It imports nothing from the project and performs no I/O. That is what lets
// `httpapi` and `store` agree on an access decision without either of them
// owning it, and it is why nothing here reads a clock, iterates a map, or draws
// a random number: a rule whose answer depends on ambient state cannot be
// asserted, and every consumer ends up re-deriving it differently.
//
// The S-8 access matrix lives in ResolveAccess and nowhere else. A second
// implementation is how a campaign ends up readable by a player.
package domain

// Requestor is the identity behind a request, as resolved from the session
// cookie.
//
// The zero value is an anonymous visitor, not "user 0", and authentication is
// carried by Authenticated rather than inferred from UserID. The two disagree
// in exactly the interesting case: middleware that failed to populate the
// identity still yields a fully non-zero struct, and a rule that reads UserID
// would read it as a real account. Authenticated is the only field that says
// "this request proved who it is".
type Requestor struct {
	UserID        int64
	Username      string
	IsAdmin       bool
	Authenticated bool
}

// CanManageInstance reports whether the requestor may register campaigns and
// manage users.
//
// Authenticated is required as well as IsAdmin, because a Requestor carrying
// IsAdmin without it is a context that was built rather than resolved, and an
// instance-wide capability is the one place where a half-built context must
// not be believed.
func (req Requestor) CanManageInstance() bool {
	return req.Authenticated && req.IsAdmin
}

// Tier is the access level resolved for one requestor against one campaign.
//
// Instance administration is deliberately not a Tier. `is_admin` is
// instance-level (S-2.7) and exists for campaign registration and user
// management; a value above TierGM would mean the two were comparable, and the
// cheapest way to make them comparable is for a handler to trust it — which
// hands every private campaign to every administrator. Instance capability is
// asked for separately, with Requestor.CanManageInstance.
//
// The constants are ordered by capability so a comparison reads sensibly, but
// no predicate below is written as one. Tier is an int, so any conversion can
// produce a value outside this set, and `t >= TierGM` would make that value
// the most privileged value in the set. Every predicate is an explicit
// membership test, so a tier this build does not recognise grants nothing.
type Tier int

const (
	// TierNone is no access at all. Not an error: it is what a 404 is built
	// from, and it is the answer for a private campaign to a non-member.
	TierNone Tier = iota
	// TierReadOnly is the wiki and public assets, nothing else. It says nothing
	// about who the requestor is — an anonymous visitor to a public campaign
	// and an authenticated non-member to the same campaign both land here
	// (S-8.1).
	TierReadOnly
	// TierPlayer is a member with role=player: they play, they never write
	// content. A player PUT is a 403 (S-14.4).
	TierPlayer
	// TierGM is a member with role=gm. Content edit, game control and invite
	// are the same gate: the S-8 table gives them one row, and splitting them
	// is a change to that matrix, not a refactor. Anyone who needs a narrower
	// GM is describing a new matrix and should say so explicitly rather than
	// discover it in a helper.
	TierGM
)

// String returns a stable label for a tier, for logs and error text.
//
// A tier outside the set renders as "invalid" rather than as a privilege. The
// value is an int, so this is the one place an unrecognised tier can be
// mistaken for a real one, and a log line is exactly where that mistake gets
// made.
func (t Tier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierReadOnly:
		return "read-only"
	case TierPlayer:
		return "player"
	case TierGM:
		return "gm"
	default:
		return "invalid"
	}
}

// CanRead reports whether the requestor may read the campaign's wiki pages and
// public assets.
//
// Membership is not required. The anonymous row of the S-8 table grants a
// read-only wiki, and an authenticated non-member of a public campaign gets the
// same thing: `public` grants wiki read and nothing else.
func (t Tier) CanRead() bool {
	switch t {
	case TierReadOnly, TierPlayer, TierGM:
		return true
	default:
		return false
	}
}

// CanPlay reports whether the requestor may reach the Table.
//
// This is the S-8.1 rule in one predicate: games always require membership, so
// no amount of campaign visibility makes a non-member a player.
func (t Tier) CanPlay() bool {
	switch t {
	case TierPlayer, TierGM:
		return true
	default:
		return false
	}
}

// CanEdit reports whether the requestor may write content, reveal secrets, or
// invite a member.
func (t Tier) CanEdit() bool {
	return t == TierGM
}

// ResolveAccess resolves the access tier for one requestor against one
// campaign. member is the requestor's membership in that campaign, or nil when
// there is none.
//
// It is pure and total: no clock, no randomness, no map iteration, and no
// error return. Every combination of arguments yields a tier, including the
// combinations that cannot occur in practice — an unknown visibility, a
// membership belonging to somebody else, a requestor the middleware never
// authenticated. A rule that can fail needs a caller to decide what to do with
// the failure, and a caller under pressure decides "allow"; a rule that cannot
// fail keeps the decision here.
//
// Three properties hold for every input, and each is a test:
//
//   - An unauthenticated requestor never resolves above TierReadOnly, and no
//     membership is consulted for one. A request context whose authentication
//     failed to populate therefore fails closed rather than open.
//   - is_admin raises nothing. S-2.7 and the S-8 table are explicit that a
//     campaign GM and an instance administrator are different things.
//   - A membership is honoured only for the identity it names. A row loaded for
//     another user, or for another campaign, is treated as no membership at
//     all — the alternative is expressing a lookup bug as a privilege.
func ResolveAccess(campaign Campaign, member *Membership, req Requestor) Tier {
	if honoursMembership(campaign, member, req) {
		switch member.Role {
		case RoleGM:
			return TierGM
		case RolePlayer:
			return TierPlayer
		default:
			// A role this build does not understand is not a player. The
			// member is locked out and the bad value stays visible in the
			// database, where an operator can see it, rather than being
			// guessed at here.
			return TierNone
		}
	}

	// Not a member. An authenticated non-member reads a public campaign and
	// nothing else, and an anonymous visitor is the same case: S-8.1 says games
	// always require membership and `public` grants wiki read only.
	if campaign.Visibility == VisibilityPublic {
		return TierReadOnly
	}

	// A visibility this build does not recognise is treated as not public, so
	// an unknown value fails toward a 404 rather than toward a published
	// campaign.
	return TierNone
}

// honoursMembership reports whether member is a membership that req may act on
// within campaign.
//
// The identity cross-checks are not a restatement of the caller's lookup, and
// they are the only thing standing between a wrong row and a privilege. A
// membership fetched by user alone is scoped to no campaign; one carried over
// from the previous request in a reused struct belongs to a different viewer.
// Both are ordinary mistakes, and ResolveAccess is the last place before they
// become access.
func honoursMembership(campaign Campaign, member *Membership, req Requestor) bool {
	if member == nil {
		return false
	}

	// Authentication is checked before the identity and before the role is ever
	// compared, which is what makes the fail-closed invariant above structural
	// rather than a matter of ordering that a later edit could disturb.
	if !req.Authenticated || req.UserID <= 0 {
		return false
	}

	// An id of 0 names no row: both are SQLite rowids, so the schema's first
	// user and first campaign are 1.
	return member.UserID == req.UserID && member.CampaignID == campaign.ID
}
