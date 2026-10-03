package houserules

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// Apply resolves a campaign's stored module set into its effective house-rule layer.
//
// **The campaign-load call.** A composition root reads the set with
// `store.RuleModulesForCampaign`, which has already ordered it, and calls this; the
// `Effective` it returns is what the gameplay system folds into the chain's last
// arrow and what a `realtime.Descriptor`'s `HouseRules` is populated from — a field
// ADR 0018 requires to be **absent from the fingerprint**, and
// `TestTogglingEveryModuleLeavesTheRulesetVersionUnchanged` is what holds that
// against this package's own output rather than against a struct somebody filled in
// by hand.
//
// Five refusals, and none of them is "a module I did not like":
//
//   - a nil registry, which is a composition root that forgot to wire one;
//   - a stored row naming a module this build has not registered (`ErrUnknownModule`),
//     because a skipped row is a campaign playing by rules its GM did not choose;
//   - a module whose `Apply` failed, which is a configuration the campaign load
//     cannot honour, so honouring the rest of the set would be a partial answer;
//   - a change of a kind the module never declared (`ErrUndeclaredCapability`);
//   - a change that is not a readable setting (`ErrMalformedChange`).
//
// **The last two are refusals rather than coercions**, and neither is a second
// admissibility check: they are about what a module *returned*, and `determinism`'s
// `Admissible` is about what a module *claimed* at registration. A module that
// claims correctly and returns otherwise is a bug in compiled-in code, and a
// campaign load is the cheapest moment to find out.
//
// **A nil logger is accepted and writes nothing.** An application is not a
// diagnostic: a campaign whose GM has never enabled two modules that conflict loads
// identically whether or not anybody is watching, and making the log mandatory would
// make a package that has no other reason to know about `slog` have one.
//
// **A refusal logs nothing.** The conflicts found before it are not written, because a
// load that failed has one event and it is the refusal — the caller reports that, and a
// line about a setting that is moot until the refusal is fixed is a second thing to
// read. A refusal also returns an **empty** `Effective` rather than the half that
// resolved, and that is the direction every one of the five takes: a campaign load
// either produces the ruleset its modules describe or it produces nothing.
func (r *Registry) Apply(
	ctx context.Context,
	modules []determinism.Module,
	logger *slog.Logger,
) (Effective, error) {
	if r == nil {
		return Effective{}, fmt.Errorf(
			"%w: no registry to resolve modules against",
			ErrIncompleteDefinition,
		)
	}

	// `determinism.Order`, once, at the top. **The store orders these rows in SQL
	// and this is the same rule in Go** (`ORDER BY position, module_id`), and the
	// reason the two are not a second answer is that neither is derived from
	// anything else — P1d holds them together and this call is what puts the Go half
	// to work on data whose order is a column read rather than a slice the caller
	// built. Sorting here instead would be a third order, and a third order is a
	// tie broken by whichever way the code was written.
	ordered := determinism.Order(modules)

	resolved := &builder{seen: make(map[settingKey]int, len(ordered))}

	for _, module := range ordered {
		// A disabled module is off without being deleted, so it contributes nothing
		// and is not a shadowed module either: it declared no view on the setting,
		// and counting it as one would put a module id in a conflict line for a
		// decision it had no part in.
		if !module.Enabled {
			continue
		}

		definition, known := r.Lookup(module.ModuleID)
		if !known {
			return Effective{}, fmt.Errorf(
				"%w: campaign %d has %q enabled, and this build has no such module; "+
					"the wiki still serves, and the game cannot start until the row is removed "+
					"or the module is registered",
				ErrUnknownModule, module.CampaignID, module.ModuleID,
			)
		}

		changes, err := definition.Apply(module.Config)
		if err != nil {
			return Effective{}, fmt.Errorf(
				"houserules: module %q of campaign %d: %w",
				module.ModuleID, module.CampaignID, err,
			)
		}

		if err := definition.permit(changes); err != nil {
			return Effective{}, fmt.Errorf(
				"houserules: module %q of campaign %d: %w",
				module.ModuleID, module.CampaignID, err,
			)
		}

		resolved.absorb(module, changes)
	}

	logConflicts(ctx, logger, resolved.conflicts)

	return Effective{applied: resolved.applied, conflicts: resolved.conflicts}, nil
}

// Origin is which module contributed a change, and where it sat in the application
// order.
//
// **Both halves, and the second is not decoration.** §10.5 resolves a conflict in
// favour of the module that comes first, so a reader asking "why did *that* module
// decide this setting" needs the id to look the module up and the position to see
// it was genuinely earlier — and a conflict between two modules at the same
// position was decided by `module_id`, which is in the log line too because that is
// the case where "earlier" is not visible from the positions alone.
type Origin struct {
	// ID is the contributing module's identifier.
	ID rules.ID

	// Position is where it sat in `determinism.Order`.
	Position int
}

// Applied is one setting, and the one value that survived for it.
//
// **A record of the decision rather than a value alone**, because "flanking is
// optional because module `mild-crits` said so at position 0" is the answer a GM
// needs when a house rule surprises them, and a bare `true` cannot tell them which
// of three enabled modules to switch off.
type Applied struct {
	// Change is the setting and the value it took.
	Change

	// Winner is the module whose declaration this is.
	//
	// Named `Winner` rather than `Module` because the word has to mean the same
	// thing here as in `Conflict`, where it is one half of a pair: the module that
	// decided. A field called `Module` in one place and `Shadowed` in the other
	// would make a reader check which is which.
	Winner Origin
}

// Conflict is one setting that two modules declared: the module whose value survived
// and the module whose value did not.
//
// **Pairwise, and one `Conflict` per losing module.** A setting three modules
// declared is two conflicts, not one conflict with two losers, and the reason is
// §10.5's and ADR 0018's "logged with both module IDs": a line that names a winner
// and a set of losers cannot be read as the pair it is, and the second and third
// modules genuinely were in conflict with the winner one at a time.
type Conflict struct {
	// Kind is the setting's kind: `CapToggle`, `CapConstant` or `CapDisable`.
	Kind determinism.Capability

	// Key is the setting's name within that kind.
	Key string

	// Winner is the module whose declaration survived.
	Winner Origin

	// Shadowed is the module whose declaration did not.
	Shadowed Origin
}

// Setting renders the setting this conflict is about, as `Change.Name` does.
func (c Conflict) Setting() string { return string(c.Kind) + " " + c.Key }

// Effective is a campaign's resolved house-rule layer: every setting a module
// declared, and every module that lost a conflict.
//
// **An ordered slice rather than a map, and the ordering is the answer.** Two
// campaigns with the same settings in a different order are not the same
// effective ruleset — §10.5 makes the declaration order the conflict policy — so
// `Applied` preserves it and a caller that rebuilds it in map order has thrown away
// half of what this package computed. `Applied()` and `Conflicts()` return copies
// for the reason `determinism.Order` does: the caller holds a value it may still
// want, and a function that hands out its own slice hands out its own bugs.
//
// **No map accessor**, and that is a decision: `Lookup` is a linear walk over a
// handful of settings, and the map a gameplay system wants — keyed by name, folded
// into a pack once at campaign load — has to be built at a seam that is not rule
// code. Offering one here would put `for k := range effective.byName` inside
// `internal/domain/rules`, which is the construct `determinism.Audit` exists to
// refuse, and the way to offer it without offering it is not to.
type Effective struct {
	applied   []Applied
	conflicts []Conflict
}

// Applied returns every resolved setting, in the order it was first declared.
func (e Effective) Applied() []Applied {
	return slices.Clone(e.applied)
}

// Conflicts returns every conflict, in the order the later declaration reached it.
func (e Effective) Conflicts() []Conflict {
	return slices.Clone(e.conflicts)
}

// Lookup returns the setting a kind and key name, if any module declared it.
//
// A linear walk, deliberately — see the `Effective` comment. A caller folding the
// layer into a pack should walk `Applied()` once and build whatever index it needs
// there, where a map range is not a determinism violation.
func (e Effective) Lookup(kind determinism.Capability, key string) (Applied, bool) {
	for _, applied := range e.applied {
		if applied.Kind == kind && applied.Key == key {
			return applied, true
		}
	}

	return Applied{}, false
}

// Empty reports whether no module declared anything.
//
// A predicate rather than `len(e.applied) == 0` at each call site, for the reason
// `dnd5e.Overlay.Zero` is one: the zero `Effective` is what a registry with no
// modules returns, and a caller asking "did any house rule apply" should not have
// to know that the answer is a slice field.
func (e Effective) Empty() bool { return len(e.applied) == 0 }

// settingKey is a change's identity: which kind of setting, and which one.
//
// **The whole of what "a conflict" means here.** Two modules conflict when they
// name the same pair, and the pair is deliberately not a string: a toggle called
// `prone` and a condition called `prone` are different settings in 5e, and folding
// them into one namespace would let a house rule disable a condition by accident
// naming a toggle that happens to share its slug.
type settingKey struct {
	kind determinism.Capability
	key  string
}

// builder accumulates one application's answer.
//
// **Not a method on `Effective` with a map field**, because `Effective` is returned
// by value to a caller and a map inside it is a reference two copies share, which is
// a way to have two values that are not equal and answer the same. The index is what
// a builder needs and an effective ruleset does not.
type builder struct {
	// seen maps a setting to where in `applied` it was first recorded, so the
	// first-match-wins check is a lookup rather than a walk. Never ranged.
	seen map[settingKey]int

	// applied is every setting recorded, in first-declaration order.
	applied []Applied

	// conflicts is every losing declaration, in the order it reached one.
	conflicts []Conflict
}

// absorb records a module's changes, and returns nothing because a conflict is not
// a separate outcome: the module's declaration was recorded either way, and whether
// it won is a fact about which of the two came first.
//
// **First-match-wins is the absence of an assignment.** A setting already in `seen`
// keeps the value it has — this function reads `applied[recorded]` for the winner and
// never writes to it — and the new declaration becomes a `Conflict`. Last-write-wins
// is the same function with one assignment added, which is why the distinction is a
// test on the *earlier* module's value surviving rather than a comment.
func (b *builder) absorb(module determinism.Module, changes []Change) {
	origin := Origin{ID: module.ModuleID, Position: module.Position}

	for _, change := range changes {
		identity := settingKey{kind: change.Kind, key: change.Key}

		recorded, exists := b.seen[identity]
		if !exists {
			b.seen[identity] = len(b.applied)
			b.applied = append(b.applied, Applied{Change: change, Winner: origin})

			continue
		}

		b.conflicts = append(b.conflicts, Conflict{
			Kind:     change.Kind,
			Key:      change.Key,
			Winner:   b.applied[recorded].Winner,
			Shadowed: origin,
		})
	}
}

// permit refuses a change whose kind the definition never declared, and a change
// this package cannot read.
//
// **Scope first, then shape**, and the order decides what a plugin author is told.
// A module that returned a change of an undeclared kind has done the thing its
// registration said it would not, and "it declared no `reorder` capability" names
// that; "that is not a shape a change has" would name a missing case in this
// package, which is not what went wrong. For the two capabilities `determinism`
// refuses by name the first message is also the right one, and it is reachable only
// because a `Definition` was assembled by hand — a registered module's scope cannot
// contain either.
func (d *Definition) permit(changes []Change) error {
	for _, change := range changes {
		if !slices.Contains(d.Scope, change.Kind) {
			return fmt.Errorf("%w: it declares %v, and returned a %q change",
				ErrUndeclaredCapability, d.Scope, change.Kind)
		}

		if err := change.validate(); err != nil {
			return fmt.Errorf("%w: module %q: %w", ErrInadmissibleModule, d.ID, err)
		}
	}

	return nil
}

// logConflicts writes one line per conflict, naming both module ids.
//
// **Warn rather than error, because a conflict is a resolution rather than a
// failure.** §10.5 asks for it to be logged and not for the campaign to be refused:
// first-match-wins is a *defined* outcome, and a GM who enabled two modules that
// both touch `flanking_optional` has configured something the ruleset answers, just
// not the answer they may have expected. An error here would make a legal campaign
// unstartable over a log line, which is the failure mode ADR 0018 exists to prevent
// in the opposite direction.
//
// **What may be on the line.** The message is a fixed constant and the attributes
// are drawn from a closed set: two module ids, two positions, the setting's kind and
// key. Nothing reads a module's `Config` — a JSON document a GM's own browser
// supplied — and nothing reads a change's value. S-12.3 is enforced by
// `observability.EventAttributes` for every other event in this repository; the
// equivalent here is that this function builds its attributes out of `Conflict`'s
// fields and nothing else, and
// `TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration` feeds it a
// configuration carrying a secret-shaped string and requires it not to appear.
//
// **One line per conflict, and a separate line per losing module**, so the pair is
// what a reader sees. `Conflict` is pairwise for the reason its own comment gives,
// and a single line naming a winner and a joined list of losers would put the two
// facts in the wrong fields.
func logConflicts(ctx context.Context, logger *slog.Logger, conflicts []Conflict) {
	if logger == nil {
		return
	}

	for _, conflict := range conflicts {
		logger.WarnContext(ctx, "houserules.conflict",
			slog.String("setting", conflict.Setting()),
			slog.String("winner", conflict.Winner.ID.String()),
			slog.Int("winner_position", conflict.Winner.Position),
			slog.String("shadowed", conflict.Shadowed.ID.String()),
			slog.Int("shadowed_position", conflict.Shadowed.Position),
		)
	}
}
