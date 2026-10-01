package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain"
)

// The membership statements.
//
// The column list is written without table qualification even in the joined
// selects, because the lists being selected from are the ones this file reads and
// neither is ambiguous -- memberships are the only table these statements touch.
const (
	membershipColumns = "campaign_id, user_id, role, created_at"

	insertMembership = `INSERT INTO campaign_members (campaign_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)`

	selectMembership = "SELECT " + membershipColumns +
		" FROM campaign_members WHERE campaign_id = ? AND user_id = ?"

	selectMembershipsForCampaign = "SELECT " + membershipColumns +
		" FROM campaign_members WHERE campaign_id = ? ORDER BY user_id"

	selectMembershipsForUser = "SELECT " + membershipColumns +
		" FROM campaign_members WHERE user_id = ? ORDER BY campaign_id"

	deleteMembership = "DELETE FROM campaign_members WHERE campaign_id = ? AND user_id = ?"
)

// CreateMembership adds a user to a campaign with a role.
//
// The role is refused unless the domain recognises it. Not a duplicate of
// domain.Role.Valid -- that is the call -- and not a CHECK in the schema, which
// would be a second copy of the vocabulary and could not be altered once shipped.
// Refusing at the write means the refusal is loud; a stored role the domain does
// not understand would still fail closed, but only after somebody had read it.
//
// A user or campaign that does not exist comes back as ErrForeignKey, which is a
// different answer from ErrNotFound and a different response: the caller's
// request named something absent, rather than the request asking for something
// that is not there.
func (s *Store) CreateMembership(
	ctx context.Context,
	membership domain.Membership,
) (domain.Membership, error) {
	if !membership.Role.Valid() {
		return domain.Membership{}, fmt.Errorf("%w: add membership to campaign %d",
			domain.ErrInvalidRole, membership.CampaignID)
	}

	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = nowFunc()
	}

	membership.CreatedAt = storedTime(membership.CreatedAt)

	what := fmt.Sprintf("add user %d to campaign %d", membership.UserID, membership.CampaignID)

	// Translated inside the closure, where the driver error is still unwrapped and
	// the operation is still named; the writer wraps whatever comes back with
	// ErrWriteFailed, which preserves the chain.
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertMembership,
			membership.CampaignID,
			membership.UserID,
			membership.Role.String(),
			unixSeconds(membership.CreatedAt),
		)

		return translateWrite(err, what)
	})
	if err != nil {
		return domain.Membership{}, err
	}

	return membership, nil
}

// Membership reads one user's membership of one campaign, for the access
// decision.
//
// A missing pair is ErrNotFound and a nil membership is not offered in its place:
// domain.ResolveAccess takes a *Membership precisely so that "no membership" is a
// value the caller cannot confuse with a zero one.
func (s *Store) Membership(
	ctx context.Context,
	campaignID int64,
	userID int64,
) (domain.Membership, error) {
	row := s.db.QueryRowContext(ctx, selectMembership, campaignID, userID)

	membership, err := scanMembership(row)
	if err != nil {
		return domain.Membership{}, translateRead(err, fmt.Sprintf(
			"read membership of user %d in campaign %d", userID, campaignID,
		))
	}

	return membership, nil
}

// MembershipsForCampaign lists a campaign's members, by user id.
//
// Ordered by user id rather than by anything a person recognises: the list is
// used to render a member roster and to enumerate roles, and a deterministic
// order is what lets a caller diff two reads and know nothing moved.
func (s *Store) MembershipsForCampaign(
	ctx context.Context,
	campaignID int64,
) ([]domain.Membership, error) {
	const what = "list a campaign's members"

	rows, err := s.db.QueryContext(ctx, selectMembershipsForCampaign, campaignID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	memberships := make([]domain.Membership, 0, 8)

	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		memberships = append(memberships, membership)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return memberships, nil
}

// MembershipsForUser lists one user's memberships, by campaign id.
//
// The by-user direction is the one the request path issues, which is why
// campaign_members_user_id_idx exists: the composite primary key leads with
// campaign_id and so cannot answer this.
func (s *Store) MembershipsForUser(
	ctx context.Context,
	userID int64,
) ([]domain.Membership, error) {
	const what = "list a user's memberships"

	rows, err := s.db.QueryContext(ctx, selectMembershipsForUser, userID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	memberships := make([]domain.Membership, 0, 8)

	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		memberships = append(memberships, membership)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return memberships, nil
}

// DeleteMembership removes a user's role in a campaign -- the operation behind
// both "remove a member" and "revoke this GM".
//
// It is the deliberate counterpart of the RESTRICT on DeleteUser: a membership is
// removed one campaign at a time, by whoever is about to remove a GM, rather than
// swept away as a side effect of deleting an account.
func (s *Store) DeleteMembership(ctx context.Context, campaignID, userID int64) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, deleteMembership, campaignID, userID)

		return translateWrite(err, fmt.Sprintf(
			"remove user %d from campaign %d", userID, campaignID,
		))
	})
}

// scanMembership reads one membership row, from a QueryRow or from an open cursor.
func scanMembership(row rowScanner) (domain.Membership, error) {
	var (
		membership domain.Membership
		role       string
		createdAt  int64
	)

	if err := row.Scan(
		&membership.CampaignID,
		&membership.UserID,
		&role,
		&createdAt,
	); err != nil {
		return domain.Membership{}, fmt.Errorf("scan membership row: %w", err)
	}

	// Parsed, not converted, for the reason campaigns.go gives: `role` is the
	// single place in the schema where a campaign privilege comes from (S-2.6),
	// so a value this build cannot interpret has to arrive as an error rather
	// than as a Role.
	parsed, err := domain.ParseRole(role)
	if err != nil {
		return domain.Membership{}, fmt.Errorf(
			"membership of user %d in campaign %d: %w",
			membership.UserID,
			membership.CampaignID,
			err,
		)
	}

	membership.Role = parsed
	membership.CreatedAt = unixTime(createdAt)

	return membership, nil
}
