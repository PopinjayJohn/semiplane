package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/semiplane/semiplane/internal/store"
)

// ResetResult is what a reset removed.
type ResetResult struct {
	// Campaigns are the slugs whose rows, memberships, pages, module sets and
	// `campaign_state` rows were deleted, in manifest order.
	Campaigns []string

	// Accounts are the usernames deleted, in manifest order.
	Accounts []string

	// Absent names what the manifest declared that this instance did not have. Not
	// a failure: a reset on an instance that was never seeded removes nothing and
	// says so, and a reset that refused would be a command with no way to ask
	// "is it clean yet".
	Absent []string

	// Warning is the skew this reset proceeded through, or empty.
	//
	// **Reset reports a mismatch and continues where seed refuses.** That asymmetry
	// is the whole design: the skew between an artefact and a binary is one of the
	// reasons an operator reaches for `demo reset`, so a reset that refused on the
	// very condition it exists to recover from would be a command with no recovery
	// path. The manifest is still parsed and still has to be usable — a reset needs
	// the campaign and account names out of it — and every removal below is named
	// explicitly rather than swept, so nothing this build cannot identify is touched.
	Warning string
}

// Reset removes what the manifest created, and touches no file.
//
// # "Leaves the vault untouched" is the load-bearing word
//
// The artefact is the thing an operator re-extracts, and a reset that rewrote it
// would destroy the state it is resetting *to*. So there is no `os` call in this
// file, no content-root open, and no path derived from `Options.Root` except as
// the text of an error message. The vault's directories and its files are the
// user's, and semiplane's rule is that the filesystem is the source of truth and
// the database is a rebuildable index (ADR 0006) — a command that deletes index
// rows *and* files has broken that relationship in the one direction nobody can
// undo.
//
// # The three deletions, in this order
//
//  1. **`campaign_state`, first.** The table has **no foreign key** to `campaigns`:
//     migration 0005 records that the constraint needs a table rebuild its author
//     called out as belonging to the work item that owns that table, and
//     `store.DeleteCampaign` documents its own deletion as knowingly incomplete for
//     exactly this reason. Dropping the campaigns first would leave three rows no
//     campaign can ever read, and — because `campaign_state`'s key is
//     `campaign_id` and `campaigns.id` is `AUTOINCREMENT` — those rows could not
//     bind to a future campaign either. They would simply occupy the table.
//  2. **The campaign.** `store.DeleteCampaign` cascades its memberships, its pages
//     and its house-rule module rows, and rebuilds the FTS index. Nothing else is
//     needed and nothing else is done.
//  3. **The accounts, last.** A membership references the account and
//     `store.DeleteUser` refuses while one exists, so the accounts can only be
//     removed once step 2 has cascaded them away.
//
// # The preflight, and the one account this refuses to delete
//
// Everything is checked before anything is deleted, because a reset that removed two
// campaigns and then refused on the third would leave an instance that is neither
// seeded nor clean.
//
// The refusal is an account that still exists and **holds instance administration**.
// The demo never creates one, so an admin named in the manifest is an account this
// instance had before the demo was seeded — most likely the operator's own — and
// deleting it because a downloaded artefact listed its username would be the worst
// thing this command could do. It is named, and the operator deletes it themselves
// with `semiplane admin`, which is the command that manages users.
//
// An account that exists and still has a membership in a campaign this artefact did
// not create is refused for the same reason, which is also what `store.DeleteUser`
// would do with `ErrForeignKey`; checking it here means the refusal arrives before
// the first deletion rather than after.
func Reset(ctx context.Context, manifest Manifest, opts Options) (ResetResult, error) {
	// Reused rather than restated, and deliberately: every option `Reset` needs is
	// an option `Seed` needs, and a second validation would be a second answer to
	// "is this a usable demo invocation".
	if err := opts.check(); err != nil {
		return ResetResult{}, err
	}

	// Reported, not refused. See ResetResult.Warning. A **schema** mismatch is the
	// one version failure that is still a refusal, because the names below would be
	// read from a document this build does not understand.
	warning, skew := Check(manifest, opts.Version)
	if skew != nil {
		if errors.Is(skew, ErrSchemaMismatch) {
			return ResetResult{}, skew
		}

		warning = fmt.Sprintf(
			"semiplane: this artefact declares product %q and this binary is %q; resetting "+
				"anyway, because a version mismatch is one of the reasons to reset",
			manifest.Product, opts.namedVersion(),
		)
	}

	absent, err := opts.resetPlan(ctx, manifest)
	if err != nil {
		return ResetResult{}, err
	}

	// `Accounts` is **not** seeded from the plan here. It was, and every account came
	// out twice: `resetPlan` already returns the usernames it found, and the deletion
	// loop below appended each one again as it was removed. Nothing was deleted twice —
	// the loop deletes by name, and a second delete of a gone row is simply not
	// `deleted` — but `len(result.Accounts)` was twice the truth and the command
	// printed each name twice.
	//
	// The duplication was invisible to the package's own tests because they assert
	// that a name is **present**, not that it appears **once**. It was found by
	// running `demo reset` and reading its output, which is the argument for the
	// install guide's own rule: a guide whose commands were not run is a guide that
	// reports a defect nobody typed.
	//
	// So the result carries what was actually removed, and nothing else.
	result := ResetResult{Absent: absent, Warning: warning}

	for index := range manifest.Campaigns {
		slug := manifest.Campaigns[index].Slug

		removed, err := opts.deleteCampaign(ctx, slug)
		if err != nil {
			return ResetResult{}, err
		}

		if removed {
			result.Campaigns = append(result.Campaigns, slug)
		}
	}

	for index := range manifest.Accounts {
		username := manifest.Accounts[index].Username

		deleted, err := opts.deleteAccount(ctx, username)
		if err != nil {
			return ResetResult{}, err
		}

		if deleted {
			result.Accounts = append(result.Accounts, username)
		}
	}

	return result, nil
}

// resetPlan reads everything a reset would remove and refuses everything that
// would make it unsafe, before a single row is deleted.
func (o Options) resetPlan(
	ctx context.Context,
	manifest Manifest,
) (absent []string, err error) {
	absent = []string{}

	named, err := o.namedCampaigns(ctx, manifest)
	if err != nil {
		return nil, err
	}

	for index := range manifest.Accounts {
		username := manifest.Accounts[index].Username

		account, err := o.Store.UserByUsername(ctx, username)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				absent = append(absent, username)

				continue
			}

			return nil, fmt.Errorf("%w: look up the account %q: %w", errDemo, username, err)
		}

		if account.IsAdmin {
			// The refusal this file is named for. See the type's comment.
			return nil, fmt.Errorf(
				"%w: the account %q exists and holds instance administration, so this "+
					"artefact did not create it and this command will not delete it; remove "+
					"it with `semiplane admin` if that is what you want",
				errDemo, username,
			)
		}

		// Checked rather than left to `DeleteUser`'s `ErrForeignKey`, because a refusal after
		// the campaigns are gone is a refusal that arrived too late to have been useful.
		elsewhere, err := o.membershipsOutside(ctx, account.ID, named)
		if err != nil {
			return nil, err
		}

		if len(elsewhere) > 0 {
			return nil, fmt.Errorf(
				"%w: the account %q is still a member of %s, which this artefact does not seed, "+
					"so this command did not create it and will not delete it",
				errDemo, username, strings.Join(elsewhere, ", "),
			)
		}

		// Nothing is appended: the caller deletes what it finds and reports from
		// what it deleted. This list used to be returned and then reported a second
		// time, which is the double-print the install walkthrough found by running it.
	}

	return absent, nil
}

// namedCampaigns returns the slugs the manifest declares that this instance actually has.
//
// **Missing campaigns are skipped rather than an error**, because a reset on a partly seeded
// instance has to remove what is there: the alternative is refusing the whole command because
// one of the three rows is already gone, which is the state a reset exists to reach.
func (o Options) namedCampaigns(
	ctx context.Context,
	manifest Manifest,
) (map[string]struct{}, error) {
	named := make(map[string]struct{}, len(manifest.Campaigns))

	for index := range manifest.Campaigns {
		slug := manifest.Campaigns[index].Slug

		if _, err := o.Store.CampaignBySlug(ctx, slug); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}

			return nil, fmt.Errorf("%w: look up the campaign %q: %w", errDemo, slug, err)
		}

		named[slug] = struct{}{}
	}

	return named, nil
}

// membershipsOutside returns the slugs of campaigns an account belongs to that this artefact
// does not name.
func (o Options) membershipsOutside(
	ctx context.Context,
	userID int64,
	named map[string]struct{},
) ([]string, error) {
	memberships, err := o.Store.MembershipsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: list the memberships of the account: %w", errDemo, err)
	}

	var elsewhere []string

	for _, membership := range memberships {
		campaign, err := o.Store.CampaignByID(ctx, membership.CampaignID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// A membership pointing at no campaign is a row nothing can read, and it is
				// not this command's to clean up.
				continue
			}

			return nil, fmt.Errorf("%w: read the campaign of a membership: %w", errDemo, err)
		}

		if _, ours := named[campaign.Slug]; ours {
			continue
		}

		elsewhere = append(elsewhere, campaign.Slug)
	}

	return elsewhere, nil
}

// deleteCampaign removes one campaign and its `campaign_state` row, and reports
// whether there was anything to remove.
//
// **The state row first, and in its own transaction**, for the reason the type's
// comment gives: `campaign_state` has no foreign key to `campaigns`, so deleting
// the campaign first would leave a row that no campaign can reach and that
// `AUTOINCREMENT` guarantees will never be reused.
func (o Options) deleteCampaign(ctx context.Context, slug string) (bool, error) {
	campaign, err := o.Store.CampaignBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}

		return false, fmt.Errorf("%w: look up the campaign %q: %w", errDemo, slug, err)
	}

	// The `fmt.Errorf` inside the closure is what makes `wrapcheck` satisfied and is
	// also the honest shape: the statement is named at the point it is issued, and the
	// writer wraps whatever comes back with `ErrWriteFailed` on top of it.
	err = o.Store.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, deleteState, campaign.ID); execErr != nil {
			return fmt.Errorf("delete the state row of campaign %d: %w", campaign.ID, execErr)
		}

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("%w: remove the state of the campaign %q: %w", errDemo, slug, err)
	}

	// Cascades memberships, pages and house-rule module rows, and rebuilds the FTS
	// index. See `store.DeleteCampaign`.
	if err := o.Store.DeleteCampaign(ctx, campaign.ID); err != nil {
		return false, fmt.Errorf("%w: delete the campaign %q: %w", errDemo, slug, err)
	}

	return true, nil
}

// deleteAccount removes one account, and reports whether there was anything to
// remove.
func (o Options) deleteAccount(ctx context.Context, username string) (bool, error) {
	account, err := o.Store.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}

		return false, fmt.Errorf("%w: look up the account %q: %w", errDemo, username, err)
	}

	if err := o.Store.DeleteUser(ctx, account.ID); err != nil {
		return false, fmt.Errorf("%w: delete the account %q: %w", errDemo, username, err)
	}

	return true, nil
}
