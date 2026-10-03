package houserules

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// ErrInadmissibleModule is a module whose declared scope is not data-level only.
//
// **This package's own umbrella, and it wraps `determinism.ErrInadmissibleModule`
// rather than restating it.** A caller asking "may this deployment enable it?"
// should get one answer from `errors.Is(err, determinism.ErrInadmissibleModule)`
// whether the refusal came from `Scope.Admissible` — which is where the answer
// comes from — or from the two checks below, which are about a module's body
// rather than about its authority. A second umbrella of this package's own would
// make a caller ask two questions to learn one thing, and the second question is
// the one somebody forgets.
var ErrInadmissibleModule = fmt.Errorf(
	"%w: a house-rule module that does not stay inside what it declared",
	determinism.ErrInadmissibleModule,
)

var (
	// ErrUndeclaredCapability is a change whose kind the module never declared.
	//
	// The scope is what bounds a module's body, and this is that bound: a module
	// declaring `CapToggle` and returning a `constant` has decided at run time to
	// do something its own registration said it would not, and a campaign load is
	// the only moment anybody is in a position to notice.
	ErrUndeclaredCapability = fmt.Errorf(
		"%w: it returned a change of a kind its declared scope does not name",
		ErrInadmissibleModule,
	)

	// ErrMalformedChange is a change this package cannot read as a setting: no key,
	// or a value the kind does not carry.
	//
	// **A refusal and not a coercion**, for the reason `dnd5e`'s `firstInt` gives:
	// "the value the kind does not carry" is the case where a module means
	// something this package cannot express, and guessing which of the two it meant
	// is how a house rule silently does not apply.
	ErrMalformedChange = fmt.Errorf("%w: a change is not a setting", ErrInadmissibleModule)

	// ErrUnknownModule is a stored module row naming a module this build has not
	// registered.
	//
	// **The whole application is refused rather than the row skipped.** S-10.6's
	// shape — a campaign whose configured plugin resolves to nothing still serves
	// its wiki and refuses to start its game — and the reason for refusing rather
	// than skipping is that a skipped row is a campaign playing by different rules
	// than its GM configured, with nothing in the log saying so. That is the
	// failure this repository cares most about: silent and coherent.
	ErrUnknownModule = errors.New(
		"houserules: the campaign's stored module set names a module this build does not have",
	)

	// ErrIncompleteDefinition is a `Definition` or a `Registry` that cannot be
	// applied at all: nothing was given, a module has no body, or there is no
	// registry to resolve modules against.
	//
	// **Its own sentinel rather than `ErrDuplicateModule` or a generic nil check**,
	// because "the registry was handed a nil" and "two modules answered to one id"
	// are different wiring faults with different fixes, and a message that cannot
	// tell them apart sends the reader to the wrong one.
	ErrIncompleteDefinition = errors.New("houserules: a house-rule module definition is not usable")

	// ErrDuplicateModule is a registration of a module id that is already
	// registered, and so a campaign's configuration would depend on which module a
	// registry happened to be given second.
	ErrDuplicateModule = errors.New("houserules: a house-rule module id is registered twice")
)

// payload is which value a capability's change carries, and the whole of
// `Change`'s shape.
//
// A separate type rather than a boolean or an `int`, because the three states are
// not ordered — "carries a boolean" is not "more" or "less" than "carries
// nothing" — and a reader of `Change.validate` should not have to know that.
type payload int

const (
	// payloadBoolean is a toggle's value.
	payloadBoolean payload = iota

	// payloadNumber is a constant's value.
	payloadNumber

	// payloadAbsent is a change that is a verb rather than an assignment:
	// `CapDisable` names a condition and disables it, and there is no value that
	// would make it mean anything else.
	//
	// **The reason a disable carries no value.** "Disable the condition called
	// `prone`, and give it this modifier instead" is not a disable with a value,
	// it is two changes from two modules, and letting one module express it would
	// mean a `disable` could shadow a `toggle` — which is a reordering of
	// resolution reached through the data, and exactly what S-10.5 refuses.
	payloadAbsent
)

// Change is one data-level change a house-rule module declares.
//
// **The payload is chosen by the kind and by nothing else**, and that is what makes
// a change inspectable: the kind says whether there is a value at all, so a reader
// never has to know which fields a given kind populates, and a module cannot
// express a change this package would have to interpret. `validate` refuses the
// three combinations that do not fit rather than reading past them.
type Change struct {
	// Kind is which of the module's declared capabilities this change exercises,
	// and which kind of setting it names: `CapToggle` names a toggle,
	// `CapConstant` a formula constant, `CapDisable` a condition.
	//
	// The three are exactly `determinism.DataLevel()`, and
	// `TestEveryDataLevelCapabilityHasAChangeShape` is what holds the
	// correspondence — a build that classifies a fourth capability as data-level
	// fails that test until it decides what such a change carries, which is the
	// moment the decision is cheap.
	Kind determinism.Capability

	// Key names the setting within that kind: a toggle's name, a constant's name,
	// a condition's slug. A `rules.ID`-shaped string would be wrong — a condition
	// slug is the pack's own vocabulary, not a plugin identifier — and
	// `validate` requires only that it is non-empty.
	Key string

	// Toggle is the value for `CapToggle`, and nil for every other kind.
	Toggle *bool

	// Constant is the value for `CapConstant`, and nil for every other kind.
	//
	// A `float64` rather than an integer because the DC constants §10.5 names are
	// a system's own business and rounding here would be this package deciding
	// what a system means. A gameplay system refuses a value that does not fit its
	// pack, at the seam, where it can say which pack and which key.
	Constant *float64
}

// Name renders a change as the setting it names, for a message and for a log line.
//
// `"toggle flanking_optional"` rather than a slash-joined pair, because the pair is
// not a path: nothing resolves it, and a separator that reads as one sends a
// reader looking for a lookup that does not exist. Identity rather than a
// validity check, as with `rules.ID.String` and `determinism.Capability.String`: a
// value read from a module is printed as the module wrote it.
func (c Change) Name() string { return string(c.Kind) + " " + c.Key }

// setting returns the payload a change declares, and whether the change is one
// this package can read.
//
// **The three data-level capabilities and nothing else**, and `CapReorder` and
// `CapUnseededRandomness` fall to the refusal rather than to a shape: they are
// listed by name in `determinism` for exactly the reason the message matters, and
// a change whose kind is one of them is unreachable anyway, because a scope
// containing one was refused by `Registry.Register`. It is refused here too so that
// a `Definition` assembled in a test and passed in by hand is refused the same way
// — the bound does not depend on how the module got here.
func (c Change) setting() (payload, bool) {
	switch c.Kind {
	case determinism.CapToggle:
		return payloadBoolean, true
	case determinism.CapConstant:
		return payloadNumber, true
	case determinism.CapDisable:
		return payloadAbsent, true
	default:
		return payloadAbsent, false
	}
}

// validate refuses a change this package cannot read as a setting.
//
// **The order is key, then kind, then payload**, so the message names the first
// thing wrong in the order a reader checks them. A capability this package has no
// shape for is refused under `determinism.ErrInadmissibleModule` rather than
// invented here: the classification of capabilities belongs to `determinism`, and a
// refusal this package named itself would be a second place a capability could be
// declared harmless.
func (c Change) validate() error {
	if c.Key == "" {
		return fmt.Errorf("%w: it names no setting", ErrMalformedChange)
	}

	want, readable := c.setting()
	if !readable {
		return fmt.Errorf("%w: a change of kind %q", ErrInadmissibleModule, c.Kind)
	}

	switch want {
	case payloadBoolean:
		if c.Toggle == nil || c.Constant != nil {
			return fmt.Errorf(
				"%w: a %q change carries a boolean and nothing else", ErrMalformedChange, c.Kind,
			)
		}
	case payloadNumber:
		if c.Constant == nil || c.Toggle != nil {
			return fmt.Errorf(
				"%w: a %q change carries a number and nothing else", ErrMalformedChange, c.Kind,
			)
		}
	case payloadAbsent:
		if c.Toggle != nil || c.Constant != nil {
			return fmt.Errorf(
				"%w: a %q change names a setting and disables it; it carries no value",
				ErrMalformedChange, c.Kind,
			)
		}
	}

	return nil
}

// Definition is one compiled-in house-rule module: what it is called, what it
// declares it may do, and how it turns its configuration into changes.
//
// **The module itself, as opposed to `determinism.Module`, which is one row.** The
// two are different things at different stages — a row says which module a campaign
// switched on, and this says what switching it on does — and merging them would put
// the set of available modules in the database, which is a property of the running
// build and not of anybody's data. `campaigns.system_id` is a string for the same
// reason.
type Definition struct {
	// ID names the module, and must be a `rules.ID`.
	//
	// Validated through `rules.ParseID` rather than `ID.Valid()` because the parser
	// is the one rule about what a persisted identifier may contain and a second
	// caller of it would be a second answer to "may this be stored" — which is the
	// store's question, asked here so that a wiring fault is reported at
	// registration rather than at a GM's campaign load.
	ID rules.ID

	// Scope is what the module declares it may exercise, and the whole of its
	// authority. `Register` refuses a scope that is not data-level only.
	//
	// **Also the bound on the module's body.** A change whose kind is not here is
	// refused at application, so a module cannot declare one capability and then
	// do another. A declaration that is not enforced is a comment.
	Scope determinism.Scope

	// Apply turns the stored configuration into the changes this module declares.
	//
	// `config` is `determinism.Module.Config` verbatim: opaque JSON, unvalidated
	// beyond being an object, because the keys it may hold are this module's
	// vocabulary and P1d's refusal to validate them is load-bearing. An empty or
	// absent config is `{}` — the store normalises it — and a module that
	// declares no configuration reads an empty object.
	//
	// Required even for a module that changes nothing, and refused when nil rather
	// than treated as a module that changes nothing: a nil function is a wiring
	// fault, and reading it as the empty module is the kind of silence that costs
	// an afternoon.
	Apply func(config json.RawMessage) ([]Change, error)
}

// Registry holds this build's compiled-in house-rule modules.
//
// **The only door a definition enters, and that is what makes `Register`'s
// admissibility check enforceable.** A module applied by a set the registry did not
// admit has not been checked, and `Registry.Apply` resolves exclusively through
// `Lookup`, so there is no second path by which an unchecked module could reach a
// campaign.
//
// Explicit registration rather than `init()`, for the reason every registry in
// this repository registers explicitly (S-10.1, ADR 0011): `init()` hides the order
// modules compose in and registers into every test binary that imports the
// package, so two tests cannot disagree about what exists.
type Registry struct {
	modules map[rules.ID]*Definition
}

// NewRegistry returns an empty registry.
//
// Empty rather than pre-seeded, so that a campaign with no house rules — which is
// what `core` and a fresh registration both have — resolves through the same empty
// answer the store returns for an empty set, and no caller needs a length check.
func NewRegistry() *Registry {
	return &Registry{modules: make(map[rules.ID]*Definition)}
}

// Register admits one compiled-in module, or refuses it.
//
// **The refusals, in the order they are checked**, and the order is the argument: a
// definition that is nil could not be refused *for* anything, so it is first; then the
// identifier, because a module this registry cannot be asked about by name is a
// module no campaign row can ever enable; then admissibility, which is S-10.5's own
// sentence; then the function, which is the last thing that can be missing because
// everything above is about what the module *claims* and this is about whether it
// exists. The duplicate check comes last, and is the only one of the five that is not
// a property of the definition alone — it is the only one that depends on what is
// already registered, so a definition that is going to be refused on its own merits is
// refused on them first.
//
// A nil registry is refused rather than panicking, and the reason is the same one it
// is refused in `Apply`: the composition root that forgot to call `NewRegistry` should
// hear about it once, at registration, and not at a campaign load.
func (r *Registry) Register(definition *Definition) error {
	if r == nil {
		return fmt.Errorf("%w: no registry to register into", ErrIncompleteDefinition)
	}

	if definition == nil {
		return fmt.Errorf("%w: none was given", ErrIncompleteDefinition)
	}

	if _, err := rules.ParseID(definition.ID.String()); err != nil {
		return fmt.Errorf("houserules: registering a module: %w", err)
	}

	// The one admissibility check, and it is `determinism`'s. See the package
	// comment: there is deliberately no second gate here, and this call is what a
	// module that tries to reorder resolution meets.
	if err := definition.Scope.Admissible(); err != nil {
		return fmt.Errorf("houserules: module %q: %w", definition.ID, err)
	}

	if definition.Apply == nil {
		return fmt.Errorf("%w: module %q has no Apply", ErrIncompleteDefinition, definition.ID)
	}

	if _, registered := r.modules[definition.ID]; registered {
		return fmt.Errorf("%w: %q", ErrDuplicateModule, definition.ID)
	}

	r.modules[definition.ID] = definition

	return nil
}

// Lookup returns the module an id names.
//
// The read `Registry.Apply` resolves every module through, and the reason there is
// no other path is in the `Registry` comment.
func (r *Registry) Lookup(id rules.ID) (*Definition, bool) {
	definition, known := r.modules[id]

	return definition, known
}

// IDs returns every registered module id, in order.
//
// Sorted rather than in map order, because a registry listing printed in map order
// is a listing that reorders between two runs with nothing having changed — the
// reason `observability.Registry.SortedNames` sorts, and the reason the walk here
// is `slices.Sorted` over a map iterator rather than a `range`. It is how a startup
// message or a test fixture names what this build has without the answer depending
// on a hash seed.
func (r *Registry) IDs() []rules.ID {
	return slices.Sorted(maps.Keys(r.modules))
}
