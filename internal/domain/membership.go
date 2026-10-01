package domain

import "time"

// Membership is one row of `campaign_members`: one user's role in one campaign.
//
// The pair (CampaignID, UserID) is the primary key, and Role is the only thing
// in the whole schema that grants a campaign privilege. It is held as a Role
// rather than a string so an unrecognised value cannot reach ResolveAccess
// without passing through ParseRole first, and so a role that is not a Role
// cannot be built by accident: a zero Membership names user 0, campaign 0 and
// no role, and ResolveAccess refuses all three.
type Membership struct {
	CampaignID int64
	UserID     int64
	Role       Role
	CreatedAt  time.Time
}
