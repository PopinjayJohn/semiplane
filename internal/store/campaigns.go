package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/semiplane/semiplane/internal/domain"
)

// The campaign statements. Named constants for the reason users.go gives: the
// SELECT list and the scan list in CampaignBySlug have to agree.
const (
	campaignColumns = "id, slug, name, content_root, visibility, system_id, ruleset_version, created_at"

	insertCampaign = `INSERT INTO campaigns (slug, name, content_root, visibility, system_id,
		ruleset_version, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`

	selectCampaignBySlug = "SELECT " + campaignColumns + " FROM campaigns WHERE slug = ?"

	selectCampaignByID = "SELECT " + campaignColumns + " FROM campaigns WHERE id = ?"

	// A viewer's campaign list is built from their memberships, so the join is
	// the query rather than a second round trip: campaign_members_user_id_idx
	// finds the rows and the slug index orders them.
	//
	// Nothing is filtered afterwards. A public campaign the user is not a member
	// of is reachable by its slug, and it is not a membership -- listing it here
	// would say the user belongs to a campaign they hold no role in.
	selectCampaignsForUser = "SELECT " + prefixedCampaignColumns +
		" FROM campaigns AS c JOIN campaign_members AS m ON m.campaign_id = c.id" +
		" WHERE m.user_id = ? ORDER BY c.slug"

	// The instance-wide listing, for startup and the watcher. See Campaigns.
	selectCampaigns = "SELECT " + campaignColumns + " FROM campaigns ORDER BY slug"

	deleteCampaign = "DELETE FROM campaigns WHERE id = ?"
)

// prefixedCampaignColumns is campaignColumns with every column qualified by the
// alias the join uses.
//
// Built as a constant rather than written out twice: the two lists differ only
// by the prefix, and a qualified column list is exactly the kind of string that
// gets edited in one place and not the other -- where the mismatch surfaces as a
// "expected N destination arguments in Scan, got M" at runtime.
const prefixedCampaignColumns = "c.id, c.slug, c.name, c.content_root, c.visibility, c.system_id," +
	" c.ruleset_version, c.created_at"

// CreateCampaign inserts a campaign and returns it as stored, with its id
// assigned here.
//
// Two values are checked before the row is written, because both are properties
// nothing downstream re-checks:
//
//   - the slug, through domain.ValidateSlug. The database enforces that a slug is
//     unique; only the domain can say what one may contain, and the slug reaches
//     a URL, a cache key and the ETag's partition.
//   - the content root, for being absolute. It becomes the os.Root confinement
//     boundary for everything in the campaign, and a relative path would resolve
//     against whatever directory the process was started in.
//
// Neither is a second implementation of a rule: one calls the domain's, and the
// other tests a path property no rule owns yet.
func (s *Store) CreateCampaign(
	ctx context.Context,
	campaign domain.Campaign,
) (domain.Campaign, error) {
	if err := domain.ValidateSlug(campaign.Slug); err != nil {
		return domain.Campaign{}, fmt.Errorf("%w: create campaign", err)
	}

	if !filepath.IsAbs(campaign.ContentRoot) {
		return domain.Campaign{}, fmt.Errorf(
			"%w: create campaign %s", ErrInvalidContentRoot, campaign.Slug,
		)
	}

	if campaign.CreatedAt.IsZero() {
		campaign.CreatedAt = nowFunc()
	}

	campaign.CreatedAt = storedTime(campaign.CreatedAt)

	what := "create campaign " + campaign.Slug

	var id int64

	// Translated inside the closure, where the driver error is still unwrapped and
	// the operation is still named; the writer wraps whatever comes back with
	// ErrWriteFailed, which preserves the chain.
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, insertCampaign,
			campaign.Slug,
			campaign.Name,
			campaign.ContentRoot,
			campaign.Visibility.String(),
			campaign.SystemID,
			campaign.RulesetVersion,
			unixSeconds(campaign.CreatedAt),
		)
		if err != nil {
			return translateWrite(err, what)
		}

		id, err = result.LastInsertId()

		return translateWrite(err, what)
	})
	if err != nil {
		return domain.Campaign{}, err
	}

	campaign.ID = id

	return campaign, nil
}

// CampaignBySlug reads one campaign by its exact slug -- the tenancy key in every
// URL.
//
// The visibility is returned rather than applied, and that is the point. This is
// the read the whole S-8 matrix hangs off, and who may see the result is decided
// in one place, domain.ResolveAccess. A visibility filter written here would be a
// second implementation of that matrix, and the second one is always the one a
// later edit changes.
func (s *Store) CampaignBySlug(ctx context.Context, slug string) (domain.Campaign, error) {
	return scanCampaign(
		s.db.QueryRowContext(ctx, selectCampaignBySlug, slug),
		"read campaign "+slug,
	)
}

// CampaignsForUser lists the campaigns a user is a member of, ordered by slug.
func (s *Store) CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error) {
	const what = "list campaigns for a user"

	// closeRows rather than a bare `defer rows.Close()`: errcheck runs with
	// check-blank on, and an unchecked close is the shape of the failure migrate.go
	// documents -- on a single-connection pool a cursor left open blocks the next
	// query instead of erroring.
	rows, err := s.db.QueryContext(ctx, selectCampaignsForUser, userID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	campaigns := make([]domain.Campaign, 0, 8)

	for rows.Next() {
		campaign, err := scanCampaignFields(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		campaigns = append(campaigns, campaign)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return campaigns, nil
}

// DeleteCampaign removes a campaign and, by cascade, its memberships and its
// pages.
//
// `campaign_state` has no foreign key to it -- migration 0005 explains why, and
// records the rebuild that will add one -- so a deleted campaign leaves its live
// game state behind. That gap belongs to the work item that owns that table, and
// it is why this is the only deletion here that is knowingly incomplete.
//
// The pages cascade does leave something behind, and this rebuilds it. `pages_fts`
// is a virtual table with no foreign key reaching it and no trigger maintaining it
// (migration 0007 explains both), so the index keeps entries for rows the cascade
// removed. They join to nothing and can never be returned -- which is why this is
// housekeeping rather than a leak -- but they occupy the index forever. A rebuild
// is one statement, and it doubles as the check that the index still agrees with
// `pages`: an external-content FTS5 table answers no query of its own, so a
// rebuild that changed anything would mean the writer had drifted.
func (s *Store) DeleteCampaign(ctx context.Context, id int64) error {
	what := fmt.Sprintf("delete campaign %d", id)

	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, deleteCampaign, id); err != nil {
			return translateWrite(err, what)
		}

		if rebuildErr := rebuildPagesFTSTx(ctx, tx, what); rebuildErr != nil {
			return rebuildErr
		}

		return nil
	})
}

// scanCampaign reads one campaign row and maps a failure onto this package's
// vocabulary.
func scanCampaign(row *sql.Row, what string) (domain.Campaign, error) {
	campaign, err := scanCampaignFields(row)
	if err != nil {
		return domain.Campaign{}, translateRead(err, what)
	}

	return campaign, nil
}

// scanCampaignFields reads one campaign row, from a QueryRow or from an open
// cursor.
//
// One function for both because they take different types and return the same
// fields: two copies of an eight-column scan is two places for a column to be
// added to one and forgotten in the other.
func scanCampaignFields(row rowScanner) (domain.Campaign, error) {
	var (
		campaign   domain.Campaign
		visibility string
		createdAt  int64
	)

	if err := row.Scan(
		&campaign.ID,
		&campaign.Slug,
		&campaign.Name,
		&campaign.ContentRoot,
		&visibility,
		&campaign.SystemID,
		&campaign.RulesetVersion,
		&createdAt,
	); err != nil {
		return domain.Campaign{}, fmt.Errorf("scan campaign row: %w", err)
	}

	// Parsed rather than converted, because the column is the read side of the
	// privilege decision and an unrecognised value must arrive as domain's
	// ErrInvalidVisibility -- a loud failure at the boundary -- instead of as a
	// Visibility this build does not understand. domain.ResolveAccess would
	// resolve such a campaign to no access; refusing to hand it back at all is
	// the louder of the two, and the caller decides which it wants.
	parsed, err := domain.ParseVisibility(visibility)
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("campaign %s: %w", campaign.Slug, err)
	}

	campaign.Visibility = parsed
	campaign.CreatedAt = unixTime(createdAt)

	return campaign, nil
}

// rowScanner is what *sql.Row and *sql.Rows both satisfy.
type rowScanner interface {
	Scan(dest ...any) error
}

// Campaigns lists every registered campaign, ordered by slug.
//
// The instance-wide read, and it exists because something has to enumerate
// campaigns rather than resolve one. Two callers need it now or soon, and neither
// can be served by CampaignsForUser:
//
//   - Startup opens each campaign's `os.Root`, and it must open the ones nobody
//     has signed in to. A campaign whose root is never opened serves a 500 on its
//     first page request, which is how "the wiki is broken" happens.
//   - The watcher (P4) registers a watch per campaign, and a watcher that only
//     saw the campaigns a particular user belongs to would index nothing.
//
// Ordered by slug because both callers are building something out of the list —
// a watch set, a map — and a deterministic order is what lets a caller diff two
// reads and know nothing moved. The slug index provides the order, so this is
// the same scan with no sort.
func (s *Store) Campaigns(ctx context.Context) ([]domain.Campaign, error) {
	const what = "list campaigns"

	rows, err := s.db.QueryContext(ctx, selectCampaigns)
	if err != nil {
		return nil, translateRead(err, what)
	}

	// closeRows for the reason PagesForCampaign gives: an unchecked close on a
	// single-connection pool is a cursor left open, which blocks the next query
	// rather than erroring.
	defer closeRows(rows, what)

	campaigns := make([]domain.Campaign, 0, 16)

	for rows.Next() {
		campaign, err := scanCampaignFields(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		campaigns = append(campaigns, campaign)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return campaigns, nil
}

// CampaignByID reads one campaign by its id.
//
// The companion to CampaignBySlug, for the two callers that hold an id and not a
// name. A URL names a campaign by slug (S-9.1); a page row, a membership and the
// `campaign_state` row all carry an id, and resolving one of those to a slug
// needs this rather than a listing filtered in Go — which would read every
// campaign to find one and would make the answer depend on how many there are.
func (s *Store) CampaignByID(ctx context.Context, id int64) (domain.Campaign, error) {
	return scanCampaign(
		s.db.QueryRowContext(ctx, selectCampaignByID, id),
		fmt.Sprintf("read campaign %d", id),
	)
}
