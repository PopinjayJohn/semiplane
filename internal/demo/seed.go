package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// contentRootMode is the mode every campaign's content root carries.
//
// **0700, and the seed enforces it on the vault directory rather than letting the
// registrar's `MkdirAll` decide.** The registrar creates a campaign's root with
// this mode, but a `MkdirAll` over a directory that already exists changes nothing
// — and the demo's directories were created by `tar`, not by semiplane, so nothing
// has set their mode. A content root readable by every account on the host is a
// campaign readable by every account on the host, bypassing §S-8 entirely, and the
// demo vault is the artefact people download and extract into whatever directory
// their host hands them. Tightening the mode is a change to the vault's *metadata*;
// the rule is about who may reach the files inside it, and this is the seed making
// the artefact match the tenancy invariant rather than rewriting anything an author
// wrote.
const contentRootMode fs.FileMode = 0o700

// Options is everything a seed needs that the manifest does not say.
//
// A struct rather than four constructor arguments because they are one decision —
// how this process reaches the database, resolves rulesets, draws a password and
// names its own version — and a caller handed them in the wrong order gets an
// instance with no campaigns or a fingerprint of nobody's.
type Options struct {
	// Store is the open handle. Required; `Seed` refuses without one rather than
	// opening a second, because `store.Open` claims the process's single-instance
	// slot and a seed that opened its own would be a second writer queue.
	Store *store.Store

	// Root is the extracted artefact's directory: the parent of
	// `ManifestFileName` and of every campaign's vault directory. Required and
	// absolute, and both for the same reason — it becomes part of every
	// `campaigns.content_root`, and the store refuses a relative one because the
	// schema's absolute paths are what make an `os.Root` mean the same thing after
	// a restart from a different working directory.
	Root string

	// Fingerprints is what this build resolves each registered system under. See
	// that type's header for why it is passed in rather than computed here.
	//
	// A nil map is **not** a build with no gameplay system — that is an empty map,
	// and the difference matters: a nil map would silently mean "no system
	// resolves", which is the `forgotten-realm` answer applied to every campaign by
	// a wiring mistake.
	Fingerprints Fingerprints

	// HouseRules resolves each campaign's module set, and is the real house-rule
	// path (`houserules.Registry.Apply`). Required: a nil registry is
	// `houserules.ErrIncompleteDefinition` rather than an empty `Effective`, which
	// is precisely the "the module was never checked" state ADR 0048 exists to make
	// unreachable, and this is the one place the demo could reintroduce it.
	HouseRules *houserules.Registry

	// Version is this binary's own release, compared against the manifest's
	// `product`. **Empty is meaningful and not an error** — see `Check`.
	Version string

	// Password is the credential every seeded account gets. **Required by `Seed`,
	// unused by `Reset`** — see `Options.checkCredential` for why the check is not in
	// the shared validator.
	//
	// The command draws it when `--password` was not given and passes it in, rather
	// than letting the seed draw one. That is the whole reason a generated password
	// can be reported on a seed that failed half way: the caller holds it from before
	// the first row is written, so no error path inside this package can lose it.
	Password Secret
}

// Result is what a seed did, and what the operator is told about it.
//
// # It is returned alongside the error
//
// **Deliberately, and it is the one unusual contract in this package.** A seed can
// fail half way — the preflight removes most of the reasons, but not all of them — and
// the operator is then left with an instance holding accounts whose password they have
// never seen. A `Result` that came back empty on the error path would lose the one
// thing the operator cannot recover: `Campaigns` and `Accounts` are appended as each
// row is committed, and `Password` is set before the first write. A refusal raised
// before any write — the version skew, the second seed, the preflight — returns the
// zero `Result`, which says exactly that.
type Result struct {
	// Campaigns are the slugs registered, appended as each is committed.
	Campaigns []string

	// Accounts are the usernames created, appended as each is committed.
	Accounts []string

	// Password is the credential every account above was created with, echoed back so
	// the caller can print it. It is a `Secret` rather than a string, so "must not be
	// logged" is enforced by the type: see `Secret`.
	Password Secret

	// Unresolved names the campaigns whose `system` this build does not register, and
	// which are therefore seeded with an empty `ruleset_version`.
	//
	// Reported rather than refused, because §10.8 says that is a real state:
	// `forgotten-realm` exists in the shipped demo precisely to demonstrate it, and a
	// seed that refused to create the demo's own demonstration of the degraded path
	// would be a demo missing one of its three campaigns.
	Unresolved []string

	// Warning is the skew warning a versionless binary produced, or empty. A string
	// rather than a logger call because this package has no logger: the only thing
	// that prints for an operator is the command, and a warning the command might drop
	// is not a warning.
	Warning string
}

// Seed writes the manifest's instance, or refuses.
//
// # The order, and why the preflight is not a transaction
//
//  1. **Preflight, entirely read-only.** Every vault directory is there, no slug and
//     no username collides with what the instance already has, every campaign's
//     module rows are ones the store will accept, and every declared tabletop
//     round-trips through `realtime.DecodeDocument`.
//  2. **Accounts.** One hash for all of them, and `IsAdmin: false` on every row.
//  3. **Campaigns, in manifest order.** Registration through
//     `campaigns.Registrar`, the remaining memberships, the house-rule modules
//     **resolved** through `houserules.Registry.Apply`, the module rows through
//     `store.ReplaceRuleModules`, and the seeded `campaign_state` row.
//
// The preflight is not a transaction and the writes are not one either, and the
// honest reason is a Go language rule rather than a design choice: the registrar is
// the only door into tenancy and it commits its row, its owner's membership and its
// filesystem root through separate `store.Write` calls, so there is no transaction
// that spans a seed. What makes that survivable is that **step 1 refuses everything
// that could make a later step fail for a reason the manifest owns** — a missing
// vault, a collision, a module id the store rejects, an unencodable tabletop — so
// the write sequence that follows is one that cannot fail for a reason a preflight
// could have caught.
//
// The one check deliberately left out of step 1 is `houserules.Apply`, and
// `resolveHouseRules` states why: it needs the campaign id for its own message, and
// a preflight call could only report `campaign 0`. A seed that fails there leaves a
// campaign with its memberships and **no** house-rule configuration, which is the
// safe direction — an empty module set is a working campaign, whereas a row naming
// a module this build does not have is a campaign whose game will not start. Either
// way the failure is *visible*: the second-seed refusal then names exactly what
// exists, and `demo reset` removes exactly it.
func Seed(ctx context.Context, manifest Manifest, opts Options) (Result, error) {
	if err := opts.check(); err != nil {
		return Result{}, err
	}

	if err := opts.checkCredential(); err != nil {
		return Result{}, err
	}

	// Before anything else, and before any read that could succeed: the version
	// check is the one refusal that must cost nothing. A skew means the manifest's
	// keys may not mean what this build thinks, so every check below it would be
	// reading a document this build does not have.
	warning, err := Check(manifest, opts.Version)
	if err != nil {
		return Result{}, err
	}

	built, err := opts.preflight(ctx, manifest)
	if err != nil {
		return Result{}, err
	}

	// Built before the first write and appended to as each row commits, for the reason
	// `Result`'s header gives: an error path must not lose the password or the list of
	// what already exists. `Campaigns` starts empty rather than pre-filled from the plan
	// because `writeCampaign` appends each slug as the registrar commits it — pre-filling
	// would list a campaign three times over on the way to the operator.
	result := Result{
		Accounts:   []string{},
		Password:   opts.Password,
		Unresolved: built.unresolved,
		Warning:    warning,
	}

	hash, err := auth.HashPassword(opts.Password.Reveal())
	if err != nil {
		return result, fmt.Errorf("%w: hash the demo password: %w", errDemo, err)
	}

	if err := opts.writeAccounts(ctx, manifest, hash, &result); err != nil {
		return result, err
	}

	registrar := campaigns.NewRegistrar(opts.Store, opts.Root, nil)

	for index := range manifest.Campaigns {
		campaign := manifest.Campaigns[index]

		if err := opts.writeCampaign(ctx, registrar, campaign, built, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// check refuses options that cannot produce a working seed.
func (o Options) check() error {
	if o.Store == nil {
		return fmt.Errorf("%w: no store was given, so there is nowhere to seed", errDemo)
	}

	if o.HouseRules == nil {
		// Not defaulted to an empty registry, and that is the whole point: a nil
		// registry answers every module set with an empty `Effective` and no error,
		// which is exactly the "nobody checked the modules" state
		// `houserules.ErrIncompleteDefinition` exists to refuse.
		return fmt.Errorf(
			"%w: no house-rule registry was given: %w",
			errDemo,
			houserules.ErrIncompleteDefinition,
		)
	}

	if o.Root == "" {
		return fmt.Errorf("%w: no demo root was given", errDemo)
	}

	if !filepath.IsAbs(o.Root) {
		// The same refusal `config.contentRootBaseFromEnv` makes, for the same
		// sentence: a relative base resolves against whatever directory the process
		// was started in, and the value it produces is stored absolute.
		return fmt.Errorf(
			"%w: the demo root %q is not absolute; every campaign's content root is "+
				"derived from it and stored absolute, so a relative root would name a "+
				"different vault after a restart from a different directory",
			errDemo, o.Root,
		)
	}

	return nil
}

// checkCredential refuses a seed with no password to create accounts with.
//
// **In `Seed` and not in `Options.check`,** because `Reset` shares those options and
// has no credential: it creates no account. A check in the shared validator would
// either make reset need a password it will never use or force it to pass a
// placeholder, and both are a way for "this option is not for this command" to become
// something nobody notices.
//
// Not defaulted to a generated credential either, for the reason `Options.Password`
// gives: the command draws the password and hands it in, so that a seed failing half
// way can still report it. A seed that drew its own would have to put the value in a
// `Result` or lose it.
func (o Options) checkCredential() error {
	if o.Password.Empty() {
		return fmt.Errorf(
			"%w: no demo password was given, and this package does not draw one; the "+
				"command draws it and passes it in so a half-seeded instance can still "+
				"report the credential its accounts were created with",
			errDemo,
		)
	}

	return nil
}

// built is everything the preflight decided, held so the write phase asks nothing
// twice and every failure is found before the first write.
type built struct {
	// modules holds each campaign's house-rule rows and its encoded tabletop, keyed
	// by slug. Both are precomputed because both can fail — a module id the store
	// will not accept, a tabletop the state column will not load — and a failure
	// after the first campaign is registered is a failure with a partly seeded
	// instance behind it.
	modules map[string][]determinism.Module
	state   map[string][]byte

	// slugs is the manifest's campaign order, and unresolved names the ones whose
	// system this build does not register.
	slugs      []string
	unresolved []string
}

// preflight reads everything the write phase needs and refuses everything that
// could make it fail.
func (o Options) preflight(ctx context.Context, manifest Manifest) (built, error) {
	plan := built{
		modules: make(map[string][]determinism.Module, len(manifest.Campaigns)),
		state:   make(map[string][]byte, len(manifest.Campaigns)),
		slugs:   make([]string, 0, len(manifest.Campaigns)),
	}

	taken, err := o.existing(ctx, manifest)
	if err != nil {
		return built{}, err
	}

	if len(taken) > 0 {
		return built{}, fmt.Errorf(
			"%w: the artefact declares product %q and this binary is %q, and this "+
				"instance already has %s; `semiplane demo reset --root %s` removes "+
				"exactly what this artefact created and leaves the vault untouched",
			ErrAlreadySeeded, manifest.Product, o.namedVersion(),
			strings.Join(taken, ", "), o.Root,
		)
	}

	for index := range manifest.Campaigns {
		campaign := manifest.Campaigns[index]

		if err := o.checkVault(campaign); err != nil {
			return built{}, err
		}

		// Validated here for what the *store* will refuse — a module id that is not a
		// usable identifier, a configuration that is not a JSON object — because both
		// are properties of the manifest and neither needs a campaign id to decide.
		// Whether this build *resolves* the set is a separate question, asked in
		// `resolveHouseRules` where the id exists.
		modules, err := campaign.Rows(0)
		if err != nil {
			return built{}, err
		}

		if campaign.State != nil {
			encoded, err := campaign.State.Document()
			if err != nil {
				return built{}, fmt.Errorf("%w: campaign %q: %w", errDemo, campaign.Slug, err)
			}

			plan.state[campaign.Slug] = encoded
		}

		plan.modules[campaign.Slug] = modules
		plan.slugs = append(plan.slugs, campaign.Slug)

		if _, resolved := o.Fingerprints.Resolved(campaign.System); !resolved {
			plan.unresolved = append(plan.unresolved, campaign.Slug)
		}
	}

	return plan, nil
}

// checkVault refuses a campaign whose content root is not a directory, and
// tightens it when it is.
//
// **The refusal that keeps a "populated" demo from being an empty one.** The
// registrar creates the directory it is given, so a manifest naming a vault the
// artefact does not contain would be seeded into a brand-new empty directory: every
// page answers 404, the wiki renders, the campaign appears in the list, and the
// demo shows nothing. That is the "silently absent behaviour" this repository keeps
// building defences against, one layer up from a 404 asset.
func (o Options) checkVault(campaign Campaign) error {
	path := filepath.Join(o.Root, campaign.Slug)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf(
			"%w: campaign %q names the vault %q, which is not in the artefact (%s); "+
				"extract the whole artefact rather than one directory of it",
			ErrIncompleteManifest, campaign.Slug, campaign.Vault, path,
		)
	}

	if !info.IsDir() {
		return fmt.Errorf(
			"%w: campaign %q names the vault %q and %s is a file, not a directory",
			ErrIncompleteManifest, campaign.Slug, campaign.Vault, path,
		)
	}

	// Tightened here rather than after registration, so the registrar opens its
	// `os.Root` over a directory that already carries the mode. See
	// contentRootMode.
	if err := os.Chmod(path, contentRootMode); err != nil {
		return fmt.Errorf("%w: restrict the content root for %q: %w", errDemo, campaign.Slug, err)
	}

	return nil
}

// existing returns what the manifest declares that this instance already has, as
// one sentence fragment.
//
// Reads through the store's own methods rather than through SQL, for the reason
// `cmd/server/systems.go` gives about `systemOf`: those statements live in
// `internal/store` and a second copy has to agree with the column list, which is a
// mistake waiting for the next migration.
func (o Options) existing(ctx context.Context, manifest Manifest) ([]string, error) {
	var taken []string

	for index := range manifest.Campaigns {
		slug := manifest.Campaigns[index].Slug

		_, err := o.Store.CampaignBySlug(ctx, slug)
		if err == nil {
			taken = append(taken, "the campaign "+slug)

			continue
		}

		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: look up the campaign %q: %w", errDemo, slug, err)
		}
	}

	for index := range manifest.Accounts {
		username := manifest.Accounts[index].Username

		_, err := o.Store.UserByUsername(ctx, username)
		if err == nil {
			taken = append(taken, "the account "+username)

			continue
		}

		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: look up the account %q: %w", errDemo, username, err)
		}
	}

	return taken, nil
}

// writeAccounts creates every account the manifest declares.
//
// **One hash for all of them, and `IsAdmin: false` on every row.** The single hash
// is the expensive half of `admin create` and every account gets the same password,
// so one derivation is the truth about all of them; a per-account salt is still
// applied inside `HashPassword`, so two rows do not carry the same stored value.
//
// `IsAdmin: false` is written literally and **there is no parameter that could make
// it otherwise**: `Account` has no such key, so the manifest cannot ask for one.
// That is D14 made structural rather than promised — a demo credential that could
// register campaigns and manage users would reach every other campaign on the
// instance.
func (o Options) writeAccounts(
	ctx context.Context,
	manifest Manifest,
	hash string,
	result *Result,
) error {
	for index := range manifest.Accounts {
		account := &manifest.Accounts[index]

		if _, err := o.Store.CreateUser(ctx, domain.User{
			Username:     account.Username,
			PasswordHash: hash,
			IsAdmin:      false,
		}); err != nil {
			return fmt.Errorf("%w: create the account %q: %w", errDemo, account.Username, err)
		}

		// Appended per account rather than pre-filled from the manifest, because the
		// preflight guarantees none of them exist and nothing guarantees the third one
		// commits: a failure here must report the accounts that are really there.
		result.Accounts = append(result.Accounts, account.Username)
	}

	return nil
}

// writeCampaign writes one campaign: its row, its memberships, its module rows and its
// seeded tabletop.
//
// The slug is appended to `result.Campaigns` **as soon as `Register` returns**, so a
// failure later in this function reports a campaign that is really there rather than
// one the operator has to discover for themselves.
func (o Options) writeCampaign(
	ctx context.Context,
	registrar *campaigns.Registrar,
	campaign Campaign,
	plan built,
	result *Result,
) error {
	owner, err := o.ownerOf(ctx, campaign)
	if err != nil {
		return err
	}

	fingerprint, _ := o.Fingerprints.Resolved(campaign.System)

	// `RulesetVersion` is **computed, never declared**. The empty string passes
	// through unchanged for a system this build does not resolve, which migration
	// 0005 defines as a meaningful value and which `realtime.Gate.Inspect` reports
	// as `Unfingerprinted` without logging an error.
	registered, err := registrar.Register(ctx, campaigns.RegisterRequest{
		Slug:           campaign.Slug,
		Name:           campaign.Name,
		SystemID:       campaign.System,
		RulesetVersion: fingerprint,
		Visibility:     campaign.ParsedVisibility(),
		OwnerID:        owner.ID,
	})
	if err != nil {
		return fmt.Errorf("%w: register the campaign %q: %w", errDemo, campaign.Slug, err)
	}

	// Appended immediately, before anything else in this function runs, so a failure
	// in the house-rule resolution reports a campaign that is really there.
	result.Campaigns = append(result.Campaigns, campaign.Slug)

	if err := o.writeMembers(ctx, campaign, registered.ID, owner.ID); err != nil {
		return err
	}

	if err := o.resolveHouseRules(ctx, registered.ID, campaign); err != nil {
		return err
	}

	if err := o.writeModules(ctx, registered.ID, plan.modules[campaign.Slug]); err != nil {
		return err
	}

	return o.writeState(ctx, registered.ID, plan.state[campaign.Slug])
}

// resolveHouseRules resolves a campaign's declared modules through the real
// house-rule path and refuses a set this build cannot apply.
//
// **This is the "resolved through the real path" half of the delivery plan's
// requirement, and it runs here rather than in the preflight for one reason: the
// campaign id.** `houserules.Registry.Apply` puts `module.CampaignID` into its
// refusal message, and there is no id to report before the registrar assigns one —
// a preflight call would print `campaign 0`, which is a number that means nothing
// to the operator reading the message. So the resolution happens where the id is
// real.
//
// Note carefully what the result is and is not used for: **it is not folded into
// `ruleset_version`.** ADR 0018 keeps house rules out of the fingerprint precisely
// so that toggling one cannot strand a campaign, and
// `realtime.FingerprintOf`'s own header says the exclusion exists so that
// "enabling a house rule" can never be a reason a GM is refused on their own
// campaign. Resolving the layer proves the set *loads*; it contributes nothing to
// the fingerprint, and the seed's test holds both halves of that.
//
// **It runs before the rows are written**, so a set this build cannot apply leaves
// a campaign carrying no house-rule configuration at all rather than a
// configuration that would refuse at the GM's first campaign load. An empty module
// set is what `houserules.Apply` resolves and is a working campaign; a row
// naming a module this build does not have is a campaign whose game will not
// start.
func (o Options) resolveHouseRules(
	ctx context.Context,
	campaignID int64,
	campaign Campaign,
) error {
	if len(campaign.RuleModules) == 0 {
		return nil
	}

	modules, err := campaign.Rows(campaignID)
	if err != nil {
		return err
	}

	if _, err := o.HouseRules.Apply(ctx, modules, nil); err != nil {
		return fmt.Errorf(
			"%w: campaign %q: the house-rule modules this artefact declares cannot be "+
				"applied by this build: %w",
			errDemo, campaign.Slug, err,
		)
	}

	return nil
}

// ownerOf returns the account a campaign's GM membership is seeded from.
//
// The registrar creates exactly one membership — the owner's, as GM — so the owner
// has to be one of the manifest's members rather than a separate field, or the two
// would be able to disagree. The manifest's check requires exactly one `gm` per
// campaign, so this cannot pick wrongly.
func (o Options) ownerOf(ctx context.Context, campaign Campaign) (domain.User, error) {
	for index := range campaign.Members {
		member := &campaign.Members[index]
		if member.ParsedRole() != domain.RoleGM {
			continue
		}

		account, err := o.Store.UserByUsername(ctx, member.Username)
		if err != nil {
			return domain.User{}, fmt.Errorf(
				"%w: campaign %q names the account %q as its GM, and it could not be read: %w",
				errDemo, campaign.Slug, member.Username, err,
			)
		}

		return account, nil
	}

	return domain.User{}, fmt.Errorf(
		"%w: campaign %q has no member with the gm role, so it would be registered with "+
			"nobody who can edit it or run its game",
		ErrIncompleteManifest, campaign.Slug,
	)
}

// writeMembers adds the memberships the registrar did not create.
//
// The registrar writes the owner's GM membership and rolls the campaign back if
// that fails, so reaching here means one GM membership exists. Every *other*
// member is added after registration rather than through the registrar, because the
// registrar takes one owner and rewriting it to take a list would be a change to
// the one door into tenancy, which this work item does not own.
func (o Options) writeMembers(
	ctx context.Context,
	campaign Campaign,
	campaignID, ownerID int64,
) error {
	for index := range campaign.Members {
		member := &campaign.Members[index]

		account, err := o.Store.UserByUsername(ctx, member.Username)
		if err != nil {
			return fmt.Errorf(
				"%w: campaign %q names the account %q: %w",
				errDemo, campaign.Slug, member.Username, err,
			)
		}

		if account.ID == ownerID {
			// Already written by the registrar. Checked by id rather than by
			// comparing roles so the skip is about the row that exists rather than
			// about what the manifest said would create it.
			continue
		}

		membership := domain.Membership{
			CampaignID: campaignID,
			UserID:     account.ID,
			Role:       member.ParsedRole(),
		}

		if _, err := o.Store.CreateMembership(ctx, membership); err != nil {
			return fmt.Errorf(
				"%w: campaign %q: add the member %q: %w",
				errDemo, campaign.Slug, member.Username, err,
			)
		}
	}

	return nil
}

// writeModules writes a campaign's house-rule module rows.
//
// Through `store.ReplaceRuleModules` and not through an insert of its own, because
// that method is the only door to the table and it validates the whole set before
// the transaction opens — so a module id the store refuses is refused here rather
// than as a rolled-back half-write.
//
// **A campaign with no modules writes nothing at all**, which is the ordinary case:
// this build registers no house-rule modules, and `campaign_rule_modules` holding no
// rows is what `houserules.Registry.Apply` resolves an empty set to.
func (o Options) writeModules(
	ctx context.Context,
	campaignID int64,
	modules []determinism.Module,
) error {
	if len(modules) == 0 {
		return nil
	}

	// `ReplaceRuleModules` overwrites `campaign_id` on every row it is handed, so
	// the preflight's placeholder id does not reach the table — the call is made
	// with the id the registrar just assigned.
	if err := o.Store.ReplaceRuleModules(ctx, campaignID, modules); err != nil {
		return fmt.Errorf(
			"%w: write the house-rule modules of campaign %d: %w",
			errDemo,
			campaignID,
			err,
		)
	}

	return nil
}

// writeState writes one seeded `campaign_state` row, or nothing when the manifest
// declares no tabletop.
//
// **A direct statement rather than a store method**, and that is the reason
// `upsertState` and `deleteState` are named in this package: no store method writes
// this table, and the only other writer is the realtime registry's debounced
// flush — which cannot be reached from here, because opening a live state would
// put a tabletop in memory for a table nobody has joined.
//
// The write goes through `store.Write` and not `Store.DB().ExecContext`: the writer
// queue is the only way to write, and bypassing it is a `SQLITE_BUSY` waiting for a
// second writer, which on a single-instance instance is the seed itself.
func (o Options) writeState(ctx context.Context, campaignID int64, blob []byte) error {
	if len(blob) == 0 {
		return nil
	}

	// The document's revision *is* the `version` column; `realtime` refuses to load
	// a row whose two disagree, so this reads the one out of the bytes it is about to
	// write rather than tracking it twice here.
	document, err := decodeState(blob)
	if err != nil {
		return fmt.Errorf("%w: campaign %d: %w", errDemo, campaignID, err)
	}

	// The `fmt.Errorf` inside the closure is what `wrapcheck` wants and is also the honest
	// shape: the statement is named at the point it is issued, and the writer wraps whatever
	// comes back with `ErrWriteFailed` on top of it.
	writeErr := o.Store.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, execErr := tx.ExecContext(
			ctx,
			upsertState,
			campaignID,
			blob,
			document.Revision,
			time.Now().Unix(),
		)
		if execErr != nil {
			return fmt.Errorf("write the state row of campaign %d: %w", campaignID, execErr)
		}

		return nil
	})
	if writeErr != nil {
		return fmt.Errorf("%w: write the state of campaign %d: %w", errDemo, campaignID, writeErr)
	}

	return nil
}

// namedVersion is this build's version as a message reads it, with the
// versionless case spelled out rather than blank.
func (o Options) namedVersion() string {
	if o.Version == "" {
		return UnversionedBinary
	}

	return o.Version
}

// decodeState reads back a seeded `campaign_state` blob.
//
// **A second read-back rather than a trust in the encoder.** `Table.Document`
// already round-trips through `realtime.DecodeDocument` when the manifest is
// validated, and this reads the bytes that are actually on their way to the column
// so the `version` written beside them is the document's own revision rather than a
// second number this package tracked. `realtime` refuses a row whose two disagree,
// so a disagreement introduced here would be a campaign that cannot be opened.
func decodeState(blob []byte) (realtime.Document, error) {
	document, err := realtime.DecodeDocument(blob)
	if err != nil {
		return realtime.Document{}, fmt.Errorf(
			"%w: the seeded tabletop does not decode: %w",
			errDemo,
			err,
		)
	}

	return document, nil
}
