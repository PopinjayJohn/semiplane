package dnd5e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// ErrNoEngine is a nil engine handed to one of this package's functions.
//
// It exists because `Engine` is a **pointer** — it carries a compiled pack and a
// resolved rule set, both of which a `Pack` cannot be, since a `Pack` holds rule
// *interfaces*. So a nil `*Engine` is representable where a zero `Engine` was not, and
// the failure it would produce is a nil dereference inside a resolution: at the table,
// mid-session, with the hub's recover catching it and reporting `server_error` for
// something a developer did.
var ErrNoEngine = errors.New("dnd5e: there is no 5e system to resolve with")

// Options configures one 5e system.
type Options struct {
	// Overlay is the edition overlay applied over the embedded base pack, or the zero
	// `Overlay` for the base pack alone.
	//
	// **Optional, and a standalone pack is a first-class shape** rather than a
	// missing input: `realtime.checkComponent` exempts exactly the overlay component,
	// and §10.4 says so in as many words. P2b's `overlays` package is what constructs
	// one and what the composition root passes here.
	Overlay Overlay
}

// Engine is the 5e system: shared mechanics, resolved against one compiled pack.
//
// **Immutable after `New`, and safe for concurrent use.** Every field is read-only
// once built and the pack holds maps that are only ever read, so two resolutions for
// one campaign at once — which S-7.2's per-placement versioning makes possible — share
// this value without a lock. That is not an optimisation: `rules.System`'s own doc
// comment says a system is safe for concurrent use, and a mutex here would be the
// false alternative, implying there was shared mutable state worth protecting.
type Engine struct {
	// pack is the compiled effective pack, and the whole of what a resolution reads.
	pack *Pack

	// rules is the pack's resolved hooks, **held directly** rather than reached through
	// `pack.rules`.
	//
	// That is a second answer to the same question and it is deliberate for one
	// reason: an interface value reached through two pointer hops is one more thing
	// that can be nil, and `compileHooks` has already proved it is not. Holding it
	// makes the resolver's read `e.rules.Critical` — one hop, and visibly a field.
	rules ruleSet

	// versions is what this system contributes to `realtime.Fingerprint`.
	versions Versions

	// overlayName is the overlay's display name, empty for the base pack alone, and
	// carried so a status page can name what a campaign resolved to. A separate field
	// from `versions` because it is display copy and must never reach a fingerprint.
	overlayName string
}

// New returns the 5e system over the embedded base pack, with an overlay applied when
// one is given.
//
// **A constructor that can fail, and that is the shape `rules.System` assumes.** Its
// doc comment says a plugin that ships a broken data pack validates the pack in its
// own constructor, because the interface has no `Check() error` and a tenth method
// would make every future method a breaking change (ADR 0041). So every refusal in
// `compile` — an unknown hook, a formula naming a variable that is not in scope, a
// proficiency table with a gap — happens **here**, at the composition root, where the
// operator is watching the log for something else, rather than at a resolution during
// a session.
//
// `Options{}` is a valid and complete argument: the base pack alone is a first-class
// system, not a degraded one.
func New(options Options) (*Engine, error) {
	base, err := readPackFile(basePackYAML)
	if err != nil {
		return nil, fmt.Errorf("dnd5e: the embedded base pack: %w", err)
	}

	// The base pack's own version, captured **before** the merge and never taken from
	// the merged file. An overlay declares a version and `mergeFiles` lets the overlay's
	// win, so a merged file's version is the *overlay's* — and reading it as the base's
	// would put the same string in two of the four fingerprint components and make a
	// base-pack revision invisible to the gate that exists to notice one. This is the
	// single most consequential line in this function and it is one line.
	baseVersion := base.Version

	merged := base
	overlayName := ""

	if !options.Overlay.Zero() {
		if merged, err = options.Overlay.apply(base); err != nil {
			return nil, fmt.Errorf(
				"dnd5e: applying the %q overlay: %w",
				options.Overlay.Name(),
				err,
			)
		}

		overlayName = options.Overlay.Name()
	}

	versions := Versions{
		System:      SystemID.String(),
		Ruleset:     EngineVersion,
		BasePack:    baseVersion,
		OverlayPack: options.Overlay.Version(),
	}

	pack, err := compile(merged, versions)
	if err != nil {
		return nil, fmt.Errorf("dnd5e: the base pack%s: %w", overlaySuffix(overlayName), err)
	}

	return &Engine{
		pack:        pack,
		rules:       pack.rules,
		versions:    versions,
		overlayName: overlayName,
	}, nil
}

// overlaySuffix renders " with the 2024 overlay" for a refusal, and the empty string
// when there is none.
//
// A helper because the sentence has two halves and every call site needs both, and a
// refusal that says "the base pack: it names a system this engine does not implement"
// when an overlay was applied sends the reader to the wrong file.
func overlaySuffix(name string) string {
	if name == "" {
		return ""
	}

	return " with the " + name + " overlay"
}

// Pack returns the compiled pack, for a status page and for a test.
//
// **Read-only and the only way out**, and that is what makes an overlay's effect
// inspectable: a GM asking "what does this campaign actually resolve under" needs the
// rows, and the answer is the pack rather than a summary of it.
func (e *Engine) Pack() *Pack {
	if e == nil {
		return nil
	}

	return e.pack
}

// Versions returns the four fingerprint components this system contributes.
//
// Exported because `realtime.Descriptor` is `realtime`'s struct and this package cannot
// import it — the dependency runs inward and `internal/domain` imports nothing from the
// project. So the composition root builds a `Descriptor` from this one value.
func (e *Engine) Versions() Versions {
	if e == nil {
		return Versions{}
	}

	return e.versions
}

// The interface is the whole of what this package needs from the contract, and this
// assertion is the cheapest possible statement of that.
var _ rules.System = (*Engine)(nil)

// ID returns the permanent system identifier.
//
// **One id for both editions**, and that is a decision with a reason: `SystemID` is
// stored in `campaigns.system_id` and is one component of the fingerprint, and the
// editions differ by their **pack versions** rather than by their identity. Two ids
// would mean two entries in `campaigns`, two registrations in the registry, and two
// kinds tables — for systems whose only difference is a data pack this package
// already knows how to compose. ADR 0018's rule is that the fingerprint names
// semantics; making the id name the edition as well would name the same input twice,
// and the status page would then be able to report "the edition differs" for a
// component that is not there.
func (e *Engine) ID() rules.ID { return SystemID }

// Title returns the name shown to a person.
func (e *Engine) Title() string { return SystemTitle }

// RulesetVersion returns this system's half of the ruleset fingerprint.
//
// **A constant, and the exclusions are the point.** It is the version of this
// engine's *resolution semantics*, and the following are deliberately **not** in it:
//
//   - **The base pack's version.** That is `Versions.BasePack`, and it is a separate
//     fingerprint component precisely so that a pack revision gates a resume on its
//     own — with its own name in the refusal, so a GM knows which file changed.
//   - **The overlay's version.** `Versions.OverlayPack`, same reason.
//   - **House-rule enablement.** There is nothing to enable in this slice, and the
//     exclusion is held by `internal/realtime`'s `TestTheFingerprintExcludesHouseRules`
//     and by the conformance suite's `Resume` audit rather than by this comment.
//
// Folding the packs in here would strand a campaign **twice** for one change and name
// nothing: once because the pack really did move, and once because a component that
// was supposed to describe the engine moved with it. `TestThePackVersionIsNotPartOfTheRulesetVersion`
// is what holds that.
func (e *Engine) RulesetVersion() string { return EngineVersion }

// Grammar returns this system's expression notation, as data a client validates
// against.
//
// Built fresh per call from the pack, because the pack is what it is built from: a
// method returning a package-level grammar would be a second place the die sizes were
// written down, and the overlay that changed them would change one and not the other.
func (e *Engine) Grammar() rules.Grammar {
	pack := e.requirePack()
	if pack == nil {
		// An empty grammar rather than a panic: `rules.checkGrammar` refuses it, so a
		// nil engine registered at the composition root fails there with a message about
		// the system instead of taking the process down inside `ContentKinds`'s caller.
		return rules.Grammar{}
	}

	return Grammar(pack)
}

// ContentKinds returns the kinds this pack declares, in declaration order.
//
// **From the pack, and that is the whole of §10.4's fourth reason a pack exists.** A
// system that wrote this list in Go would have two answers to "what kinds does 5e
// recognise" — one an overlay could change and one it could not — and the second
// would be the one a registry holds. `TestTheKindsComeFromThePack` holds the coupling
// by editing the pack and requiring the declaration to move with it.
func (e *Engine) ContentKinds() []rules.Kind {
	pack := e.requirePack()
	if pack == nil {
		// **Nil, not a panic**, and `requirePack`'s comment says why: it returns nil so
		// that "there is no pack" is a value the callers can test rather than a
		// dereference. `Pack.Kinds` on a nil receiver would read a nil slice's header,
		// which is fine — but `Pack()` hands the caller a `*Pack` and the first thing
		// anybody does with it is call a method, so every method that goes through it has
		// to carry the check. `rules.Validate` refuses an empty declaration with
		// `ErrNotAGrammar`-shaped advice and the registrar says so.
		return nil
	}

	return pack.Kinds()
}

// Views returns a copy of the declared views.
//
// All three are served by semiplane's built-in renderers, which is §10.6.1's "a simple
// plugin ships data and zero UI code" and the reason a system nobody has heard of can
// be added with no client work at all. This package ships **no templ component and no
// JavaScript**.
func (e *Engine) Views() []rules.View { return slices.Clone(views) }

// Resolves reports whether this system resolves op.
//
// The second, optional interface `internal/plugin` asks through — ADR 0042's second
// registry — because `rules.System` has no method for it by ADR 0041's argument, and
// S-10.3's rule ("a UI plugin may only emit operations some gameplay system already
// resolves") is a question about the build rather than about an intent.
//
// A system that did not implement it would contribute no operations and every UI
// plugin's emit would be refused, which is the safe direction; the alternative is
// asking nobody and assuming yes.
func (e *Engine) Resolves(op rules.Op) bool { return knownOp(op) }

// PackVersion returns the base pack's version, the second of the four components.
//
// A convenience over `Versions().BasePack` and not a second source: it reads the same
// field, and a status page that wanted it does not have to know the struct.
func (e *Engine) PackVersion() string { return e.Versions().BasePack }

// OverlayVersion returns the overlay's version, or the empty string for the base pack
// alone.
func (e *Engine) OverlayVersion() string { return e.Versions().OverlayPack }

// knownOp reports whether this system resolves op.
//
// A `switch` and not a package-level set, for the reason `rules.IsSemiplaneKind` is
// one: the vocabulary is this package's declaration, written out where adding a
// seventh operation is a compile-visible edit rather than an entry somebody appends to
// a slice beside a loop.
func knownOp(op rules.Op) bool {
	switch op {
	case OpRoll, OpAttack, OpHeal, OpApplyCondition, OpClearCondition, OpApplyStatus, OpRemoveToken:
		return true
	default:
		return false
	}
}

// Parse turns notation text into this system's own expression tree.
//
// **Server-side authority, called with the text the client sent** and never with text
// the client parsed — `rules.System.Parse` says so, and S-7.3 is the requirement: a
// client may *ask* for a roll, it never says what the roll was.
//
// The text is whitespace-normalised before parsing and nothing else, and the parsed
// tree records the source **as written** so an audit can show a player what they typed
// rather than what was normalised. §16.3's roll log needs the former; the resolution
// needs the latter.
func (e *Engine) Parse(text string) (rules.Expr, error) {
	pack := e.requirePack()
	if pack == nil {
		return rules.Expr{}, ErrNoEngine
	}

	spec, err := parseExpr(pack, text)
	if err != nil {
		return rules.Expr{}, err
	}

	return rules.NewExpr(SystemID, pack.notation, text, spec), nil
}

// Apply resolves one already-validated intent against one read-only snapshot.
//
// Six rules, and each is a way this system could have been wrong:
//
//  1. **Cancellation is observed and ends the call.** A `ctx.Err()` is returned with
//     **no mutations**, because `rules.System.Apply` says a resolution that observes
//     cancellation must never return a partial list — a list half-computed on the way
//     to being discarded is a list whose contents depend on *when* the hub cancelled,
//     which is the one input S-14.6 says resolution may not depend on. Checked first
//     because it is the only check that costs nothing and the only one that can be
//     made before any work.
//  2. **The op is checked before the state**, so an op this system does not resolve is
//     a wiring fault named as one rather than a refusal about the tabletop.
//  3. **The role decides next, still before the state.** §7.2's GM-only operations are
//     refused before anything is read, so a player's attempt reveals nothing at all
//     about the table — not even whether the creature exists. That is the difference
//     between this check and the ownership check below it, and it is why the order is
//     not negotiable.
//  4. **Only the addressed object is read, plus one the arguments name.** `state.Lookup`
//     on the target and on the defender, and no walk. That is what makes an unknown
//     kind **inert rather than fatal** (S-14.7, §10.8): a tabletop carrying a placement
//     whose kind nothing registers resolves exactly as it would not have been, because
//     this system never walks its state. The conformance suite plants such an object
//     and requires the resolution to succeed anyway.
//  5. **Every draw is named** from what the draw *is* — operation, actor, defender,
//     attack — and **never from a counter**. A counter would make the third attack of a
//     campaign depend on the two before it, and S-14.6 is about a resolution being a
//     function of its inputs.
//  6. **Mutations are returned, never applied.** `rules.NewMutation` hands back a
//     value, the state arrived by value, and `rules.State` keeps its fields unexported,
//     so there is no path from here to the hub's document. S-10.2's sentence about a
//     panic leaving `campaign_state` byte-identical is a consequence of that rather
//     than a promise this function makes.
func (e *Engine) Apply(
	ctx context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	if e == nil || e.pack == nil {
		return nil, ErrNoEngine
	}

	// Wrapped, because it crosses a boundary: the caller is `internal/plugin`'s
	// adapter, which recognises this system's sentinels by `errors.Is` and needs to be
	// able to tell "the hub cancelled" from "the resolver refused". `context.Canceled`
	// itself is what the adapter compares against, so the chain has to reach it.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dnd5e: %s was cancelled: %w", intent.Op, err)
	}

	if !knownOp(intent.Op) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownOp, intent.Op)
	}

	// Rule 3, before any read of the table.
	if GMOnly(intent.Op) && call.Role != domain.RoleGM {
		return nil, fmt.Errorf("%w: %q is not a player's to make", ErrGMOnly, intent.Op)
	}

	switch intent.Op {
	case OpRoll:
		return e.resolveRoll(ctx, call, state, intent)
	case OpAttack:
		return e.resolveAttack(ctx, call, state, intent)
	case OpHeal:
		return e.resolveHeal(ctx, call, state, intent)
	case OpApplyCondition, OpClearCondition:
		return e.resolveCondition(ctx, call, state, intent)
	case OpApplyStatus:
		return e.resolveStatus(ctx, call, state, intent)
	case OpRemoveToken:
		return e.resolveRemove(ctx, call, state, intent)
	default:
		// Unreachable through `knownOp`, which every case above is drawn from, and which
		// a test pins against the `rules.Op` constants. Present because a function with
		// an implicit zero return is a function whose failure is invisible.
		return nil, fmt.Errorf("%w: %q", ErrUnknownOp, intent.Op)
	}
}

// resolveRemove takes the target off the table.
//
// **The only resolution that sets `Mutation.Remove`,** and it is a `bool` rather than
// an op because removal is not an ordinary mutation: the hub's removal path stamps a
// version that outlives the placement, so a late client can still reconcile against it.
// Deciding that from an op's name would put that distinction in a string.
func (e *Engine) resolveRemove(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	if _, err := e.actingCreature(call, state, intent); err != nil {
		return nil, err
	}

	mutation, err := rules.NewMutation(intent.Target, OpRemoveToken, nil)
	if err != nil {
		return nil, fmt.Errorf("dnd5e: stating the removal of %q: %w", intent.Target, err)
	}

	mutation.Remove = true

	return []rules.Mutation{mutation}, nil
}

// requirePack returns the pack, refusing a nil engine.
//
// **Not `e.pack` at every call site.** A nil `*Engine` reaching a resolver is a
// composition-root fault, and every one of these nine methods would otherwise panic on
// the first field read — inside the hub's `recover`, reported as `server_error`, with
// nothing in the log saying the plugin was absent.
func (e *Engine) requirePack() *Pack {
	if e == nil {
		// Returned rather than panicked: the caller is a `rules.System` method whose
		// signature has an error on two of the nine and not on the rest, and a panic
		// here is a worse way to say it than an empty pack that then fails its own
		// lookups with a message naming this.
		return nil
	}

	return e.pack
}

// actingCreature reads the creature the intent addressed, and refuses a player
// reaching for somebody else's.
//
// **Two refusals, and the order is what keeps them from being one.** An object that is
// not on the table is `ErrNoSuchCreature`; an object that belongs to another account is
// `ErrNotYourCreature`. They are distinct because they have different fixes — the first
// means the GM has not placed it, the second means the player rolled up the wrong
// token — and a single message would tell a player neither.
//
// The distinctness does leak that a token *exists*, and that is acceptable here for a
// reason that is not "it is fine": a token on the campaign's table is already visible to
// every member through the views `Derive` serves, and S-8 governs what a reader may
// learn, not this resolver. A campaign where the tabletop is private to the GM does not
// exist in this model — `internal/realtime`'s placements are the same for every peer.
//
// The GM is not subject to the ownership check at all, and that is the whole point of
// §7.2: the GM acts *for* the table.
func (e *Engine) actingCreature(
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) (creature, error) {
	object, found := state.Lookup(intent.Target)
	if !found {
		return creature{}, fmt.Errorf("%w: %q", ErrNoSuchCreature, intent.Target)
	}

	read, err := readCreature(object)
	if err != nil {
		return creature{}, err
	}

	if call.Role == domain.RoleGM {
		return read, nil
	}

	if read.Actor != call.Actor {
		return creature{}, fmt.Errorf(
			"%w: %q is placed for another account",
			ErrNotYourCreature,
			intent.Target,
		)
	}

	return read, nil
}

// scope builds the formula scope for one creature.
//
// **Every constant the pack declares, plus `level`, `prof`, and every ability's
// modifier** — which is the whole of the documented vocabulary in `formula.go`.
//
// `maps.Copy` rather than a range: the *order* constants are copied in is irrelevant
// (they are keyed), and a range over a map to build a map is still the construct S-10.4
// forbids by name even where it happens to be harmless. The sanctioned spellings are
// `maps.Copy`, `maps.Insert`, and `slices.Sorted(maps.Keys(…))` — and this file uses
// all three, each where it belongs.
//
// The abilities are walked in the **pack's** declaration order and looked up in the
// creature's score **map**, never the other way round. See `creature.go`'s `Ability`
// comment: a creature's six scores is the smallest possible map and therefore the one a
// contributor reaches for first.
func (e *Engine) scope(subject *creature) (map[string]int, error) {
	scope := make(map[string]int, len(e.pack.constants)+len(e.pack.abilities)+2)

	maps.Copy(scope, e.pack.constants)
	scope[scopeLevel] = subject.Level

	proficiency, err := e.pack.Proficiency(subject.Level)
	if err != nil {
		return nil, err
	}

	scope[scopeProf] = proficiency

	for _, ability := range e.pack.abilities {
		scope[scopePrefixAbility+ability.slug] = Modifier(subject.Ability[ability.slug])
	}

	return scope, nil
}

// dcScope adds the pack's named difficulty classes to a scope.
//
// **Read once per scope, not per lookup**, and the reason is that `dc.<slug>` has to be
// in scope for `validateVariables` to have allowed a formula to name it — a scope built
// without the DC table would compile fine and then refuse at resolution, which is the
// one place a load-time check must not be able to be satisfied and then fail.
func (e *Engine) dcScope(scope map[string]int) {
	for _, named := range e.pack.dcs {
		scope[scopePrefixDC+named.slug] = named.dc
	}
}

// evaluate runs a formula that binds no ability.
//
// **Refuses a `for:` formula**, naming it, because a per-ability formula evaluated with
// nothing bound would read `ability` as absent and fail with a message about scope —
// which sends a reader looking at the pack's vocabulary rather than at the call site
// that forgot to say which ability.
func (e *Engine) evaluate(name string, scope map[string]int) (int, error) {
	formula, declared := e.pack.Formula(name)
	if !declared {
		return 0, fmt.Errorf("dnd5e: this pack declares no formula named %q", name)
	}

	if formula.For != ForNone {
		return 0, fmt.Errorf(
			"dnd5e: formula %q is evaluated once per ability, so it needs one; %q binds none",
			name, name,
		)
	}

	return formula.Evaluate(scope)
}

// evaluateFor runs a formula bound to one ability.
//
// **Two guards, both refusals rather than defaults.** The formula must exist and must
// be a `for:` one; and the ability must be one the pack declares. A formula missing
// from a pack is a load failure in principle — see `requiredFormulas` — so reaching
// this at resolution means an overlay replaced the table wholesale, which is exactly
// when a resolution-time refusal is worth having.
func (e *Engine) evaluateFor(name, ability string, scope map[string]int) (int, error) {
	formula, declared := e.pack.Formula(name)
	if !declared {
		return 0, fmt.Errorf("dnd5e: this pack declares no formula named %q", name)
	}

	if formula.For != ForAbility {
		return 0, fmt.Errorf(
			"dnd5e: formula %q binds no ability, so it cannot be evaluated for %q", name, ability,
		)
	}

	if _, known := e.pack.AbilityAt(ability); !known {
		return 0, fmt.Errorf("%w: %q", ErrNoSuchAbility, ability)
	}

	// The binding, written into the scope rather than carried alongside it. That is
	// what lets `Evaluate` be one function for every formula: a formula's variable
	// vocabulary is fixed, and `for` is the only thing that varies, so `for` is the only
	// thing that writes to the scope.
	scope[scopeAbility] = scope[scopePrefixAbility+ability]

	return formula.Evaluate(scope)
}

// armourClass returns a creature's armour class from the pack's formula, or its stored
// override.
//
// **The override wins, and it is checked first** because a plate is not a modifier: a
// creature wearing full plate has an armour class that does not mention Dexterity at
// all, and a formula that added the modifier anyway would be a formula nobody wearing
// plate agrees with. The stored value is the plate; the formula is the unarmoured
// case.
//
// `ArbourBonus` — a shield — is added to **either**, because it is additive by
// definition and reading it out of the formula would mean a pack change moved a
// shield.
func (e *Engine) armourClass(subject *creature, scope map[string]int) (int, error) {
	if subject.ArmourClass > 0 {
		return subject.ArmourClass + subject.ArmourBonus, nil
	}

	computed, err := e.evaluate("armour_class", scope)
	if err != nil {
		return 0, err
	}

	return computed + subject.ArmourBonus, nil
}

// decodeArgs reads an intent's arguments into a typed value.
//
// **Strict**, and `json.Decoder.DisallowUnknownFields` rather than `json.Unmarshal`,
// because a mistyped argument name is otherwise silently ignored: a client sending
// `{"exprs": "1d20"}` would get a mutation recording a roll of nothing rather than
// an error, and the player would watch a number appear that means nothing. The
// alternative failure — a client sending an argument a newer build added — is not this
// system's problem: §14's "unknown fields are ignored, not fatal" is about *rendered
// output*, and an op's arguments are a request, where an unknown field means the client
// and the server disagree about what was asked.
func decodeArgs[V any](intent rules.Intent, into *V) error {
	if len(intent.Args) == 0 {
		return fmt.Errorf(
			"%w: %q takes arguments and the intent carried none",
			ErrBadArguments,
			intent.Op,
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(intent.Args))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		// The decoder's text quotes the offending bytes. Those bytes are a client's own
		// request rather than anything from a vault, and naming the field is the
		// actionable half — so it is wrapped rather than dropped, unlike a YAML pack's
		// message (`readPackFile`'s comment says why that one differs).
		return fmt.Errorf("%w: %q: %w", ErrBadArguments, intent.Op, err)
	}

	return nil
}
