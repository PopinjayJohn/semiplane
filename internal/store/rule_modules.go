package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// The house-rule module statements.
//
// The column list is a constant for the reason every file here has one: the
// SELECT and the scan have to agree, and a mismatch surfaces at runtime as
// "expected N destination arguments in Scan, got M" rather than as a compile
// error.
//
// The ORDER BY is the whole of §10.5's conflict policy, and `determinism.Order`
// states the same order in Go — the two are held together by
// `TestRuleModulesForCampaignMatchesTheOrderTheDomainStates`, because
// first-match-wins over an order that is not total has no referent. `module_id`
// leads the primary key, so the tie-break cannot tie.
const (
	ruleModuleColumns = "campaign_id, module_id, enabled, config, position"

	insertRuleModule = `INSERT INTO campaign_rule_modules (campaign_id, module_id, enabled, config, position)
		VALUES (?, ?, ?, ?, ?)`

	// `ORDER BY position, module_id`, and not `ORDER BY position` alone: two
	// modules may legitimately share a position — nothing in the schema or in the
	// GM's form makes them unique — and a conflict between two of them has to
	// resolve to one of them. Without the second key the winner is whichever row
	// SQLite returned first, which is the last-write-wins behaviour §10.5 names and
	// forbids, arriving by a different route.
	selectRuleModulesForCampaign = "SELECT " + ruleModuleColumns +
		" FROM campaign_rule_modules WHERE campaign_id = ? ORDER BY position, module_id"

	deleteRuleModulesForCampaign = "DELETE FROM campaign_rule_modules WHERE campaign_id = ?"
)

// RuleModulesForCampaign reads a campaign's house-rule module set, in the order the
// modules apply.
//
// **An empty set is an empty slice, not ErrNotFound.** A campaign with no house
// rules is the ordinary case — it is what `core` and a fresh registration both
// have — and a caller asking "which modules does this campaign enable" gets the
// same shape for "none" as for "some", which is what makes the loop below safe to
// write without a length check. ErrNotFound here would be a caller having to
// distinguish two meanings from one error, and `campaign_state` has the same
// property for the same reason.
//
// The order is `determinism.Order`'s, computed by the database rather than in Go:
// the set is a handful of rows and the ORDER BY is total, so the answer does not
// depend on which plan the planner picks. Ordering in Go as well would be a second
// answer to the same question, and the test that compares the two is what keeps it
// one.
func (s *Store) RuleModulesForCampaign(
	ctx context.Context,
	campaignID int64,
) ([]determinism.Module, error) {
	const what = "list a campaign's rule modules"

	rows, err := s.db.QueryContext(ctx, selectRuleModulesForCampaign, campaignID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	// closeRows rather than a bare defer, for the reason campaigns.go gives: errcheck
	// runs with check-blank on, and an unchecked close on a single-connection pool is
	// a cursor left open, which blocks the next query instead of erroring.
	defer closeRows(rows, what)

	modules := make([]determinism.Module, 0, 8)

	for rows.Next() {
		module, err := scanRuleModule(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		modules = append(modules, module)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return modules, nil
}

// ReplaceRuleModules writes a campaign's whole house-rule module set.
//
// Delete-then-insert in **one transaction**, and the unit is the whole set for a
// reason rather than for convenience: applying a half-written module set is not a
// state this project can describe. A campaign either has the effective ruleset its
// modules describe or it has the previous one; a campaign with three of four
// modules replaced has neither, and the next boot would build an effective ruleset
// nobody configured. Every caller therefore reads the set, changes it, and writes
// it back — which is also why there is no single-row upsert here, and why adding
// one later would be an additive change rather than a correction.
//
// **Every row is validated before the delete**, on the caller's slice, so a refused
// write is refused before the transaction opens. The outcome is the same either way
// — the writer wraps the error with `ErrWriteFailed` and the transaction rolls back
// — so this is about which mistake a caller with two of them is told about, and
// `TestAMistypedModuleIdIsReportedInPreferenceToAnUnknownCampaign` is what holds
// that. It is also about the code being obvious: a delete that is not preceded by
// the checks that decide whether to write anything is a delete a reader has to
// reason about.
//
// A campaign that does not exist is ErrForeignKey rather than ErrNotFound, for the
// reason memberships.go gives: the caller's request named something absent, which
// is a 400 where ErrNotFound would be a 404.
func (s *Store) ReplaceRuleModules(
	ctx context.Context,
	campaignID int64,
	modules []determinism.Module,
) error {
	// `determinism.Order` rather than trusting the caller's order, because the
	// caller may have assembled the set from a map, from a form, or from a JSON
	// object — and the application order is a property of the rows, not of how they
	// arrived. Ordering here as well as in the SELECT is not a second answer: it is
	// the same function, and it is what makes the *insert* order the declaration
	// order for any reader that ever looks at the table by rowid.
	ordered, err := validatedRuleModules(campaignID, modules)
	if err != nil {
		return err
	}

	what := fmt.Sprintf("replace rule modules of campaign %d", campaignID)

	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, deleteRuleModulesForCampaign, campaignID); err != nil {
			return translateWrite(err, what)
		}

		for _, module := range ordered {
			_, err := tx.ExecContext(ctx, insertRuleModule,
				campaignID,
				module.ModuleID.String(),
				module.Enabled,
				string(module.Config),
				module.Position,
			)
			if err != nil {
				return translateWrite(err, what)
			}
		}

		return nil
	})
}

// validatedRuleModules normalises and checks a module set, and returns it in the
// order it will be written.
//
// **Ordered here, in Go, before the transaction, with `determinism.Order`** — so
// the caller's slice is not reordered underneath them and the insert order is the
// declaration order. `RuleModulesForCampaign` orders by the same rule in SQL, and
// `TestRuleModulesForCampaignMatchesTheOrderTheDomainStates` is what holds the two
// to one answer.
func validatedRuleModules(
	campaignID int64,
	modules []determinism.Module,
) ([]determinism.Module, error) {
	seen := make(map[rules.ID]struct{}, len(modules))
	ordered := make([]determinism.Module, 0, len(modules))

	for _, module := range modules {
		// The one rule about what a module identifier may contain, called rather
		// than restated: `rules.ParseID` is the implementation, and a second copy
		// here would be a second answer with a migration per future relaxation.
		if _, err := rules.ParseID(module.ModuleID.String()); err != nil {
			return nil, fmt.Errorf("%w: rule module for campaign %d", err, campaignID)
		}

		// A duplicate in the caller's slice. The primary key would refuse it as
		// ErrConflict, with the driver's message and no indication of which of the
		// two rows was meant to win — and a caller assembling a set from a form has
		// no other way to learn it submitted the same module twice.
		if _, duplicate := seen[module.ModuleID]; duplicate {
			return nil, fmt.Errorf("%w: %q in campaign %d",
				ErrDuplicateRuleModule, module.ModuleID, campaignID)
		}

		seen[module.ModuleID] = struct{}{}

		config, err := normaliseRuleModuleConfig(module.Config)
		if err != nil {
			return nil, fmt.Errorf("%w: module %q of campaign %d",
				err, module.ModuleID, campaignID)
		}

		module.CampaignID = campaignID
		module.Config = config

		ordered = append(ordered, module)
	}

	return ordered, nil
}

// normaliseRuleModuleConfig returns config as a JSON object, refusing anything else.
//
// The shape and nothing else, because the shape is what the column's `DEFAULT '{}'`
// asserts and what a house-rule application must be able to read: a row whose
// config is the empty string or a bare number is a row no application can decode
// into settings, and it would fail at campaign load rather than at the write that
// created it. **Which keys a module understands is the module's business**, and is
// deliberately not checked here — that vocabulary belongs to
// `internal/domain/rules/houserules`, and a second implementation of it in the
// store would be two answers to "what may this module be configured with".
//
// Empty becomes `{}` rather than being refused: no configuration is a legitimate
// value, and it is the same value the column defaults to.
func normaliseRuleModuleConfig(config json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(config)
	if len(trimmed) == 0 {
		return json.RawMessage("{}"), nil
	}

	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: a rule module's config must be a JSON object",
			ErrInvalidRuleModuleConfig)
	}

	// Compact, so two modules configured the same way store byte-identical configs
	// and a diff between two databases' rows means something.
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, trimmed); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRuleModuleConfig, err)
	}

	return json.RawMessage(buffer.Bytes()), nil
}

// scanRuleModule reads one module row, from a QueryRow or an open cursor.
func scanRuleModule(row rowScanner) (determinism.Module, error) {
	var (
		module     determinism.Module
		moduleID   string
		enabled    int64
		config     string
		campaignID int64
	)

	if err := row.Scan(
		&campaignID,
		&moduleID,
		&enabled,
		&config,
		&module.Position,
	); err != nil {
		return determinism.Module{}, fmt.Errorf("scan rule module row: %w", err)
	}

	// Parsed, not converted, for the reason campaigns.go gives about `visibility`
	// and memberships.go about `role`: this build cannot hand back a module
	// identifier it does not understand, because the registry lookup that would
	// resolve it is not the store's to perform and a caller that cannot tell a bad
	// id from a good one will look it up and get nothing.
	parsed, err := rules.ParseID(moduleID)
	if err != nil {
		return determinism.Module{}, fmt.Errorf("rule module of campaign %d: %w", campaignID, err)
	}

	module.CampaignID = campaignID
	module.ModuleID = parsed
	module.Enabled = enabled != 0
	module.Config = json.RawMessage(config)

	return module, nil
}

// RuleModuleIDs returns the ids of a module set, in order.
//
// One line for a caller that only wants the names — a status line, a diff between
// two effective rulesets, the `House-rules widget` in §10.6's table, which "is
// read-only; the rule data itself comes from a gameplay module". It preserves the
// order rather than sorting, because the order is the answer this package exists to
// keep total.
func RuleModuleIDs(modules []determinism.Module) []string {
	ids := make([]string, 0, len(modules))
	for _, module := range modules {
		ids = append(ids, module.ModuleID.String())
	}

	return ids
}
