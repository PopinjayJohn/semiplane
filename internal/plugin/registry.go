// Package plugin is semiplane's gameplay registry: an explicit, ordered set of
// the `rules.System` values a build ships, and the adapter that turns them into
// the one authority the hub applies client intents through.
//
// # What is registered, and how
//
// S-10.1 and ADR 0011: plugins are compiled-in Go modules registered **in the
// composition root**, never in an `init()`. There is no package-level registry
// here and no way to reach one — `New` is the only way to build a `Registry`, and
// a caller holds the value it built. `init()` hides the order in which rules packs
// compose, which becomes unreviewable the moment packs are versioned, and it
// registers a system into every test binary that imports the package, so two
// tests cannot disagree about what is registered.
//
// The listing is in **registration order** and a fresh slice per call, because
// registration order is the one order a reader can see in `cmd/server` and the
// one §10.5's first-match-wins rule for house rules resolves against. A map
// iteration would make the order a property of the process, which is ADR 0037's
// fixed point in a new place.
//
// # What registration refuses, and who refused it first
//
// `Register` calls `rules.Validate` and does not reimplement any of it. A system
// whose id is not a usable identifier is refused by `rules.ParseID`'s rule, a
// system declaring one of the five semiplane-owned kinds is refused by
// `rules.Validate`'s ownership table, and a system whose views cannot be rendered
// is refused by `rules.View.Check`. **A second answer to any of those questions
// would be a second rule**, and §10.2.1's refusal is cheap only because there is
// exactly one of it — so this package adds refusals only where nothing upstream
// has an answer: a duplicate id, a missing codec, an unknown id, a kind claimed
// by two systems at once (which is *not* refused, and `kinds.go` says why).
//
// # What an unknown id is, and what it is not
//
// S-10.6 and §10.8: a campaign whose `system_id` resolves to nothing **still
// serves its wiki** and refuses only its game, naming the id it wanted. So an
// unknown id is a typed `*UnknownSystemError` carrying the id, and
// `Registry.Resolve` is the one function that produces it — the composition root
// asks it at boot and the adapter asks it per intent. It is never a nil system
// and never a fallback: a campaign whose plugin was removed or renamed must be
// told which id is missing, and guessing at a replacement would resolve a game
// under rules nobody chose.
//
// # Why the adapter is here rather than in `internal/realtime`
//
// `realtime.Resolver` is declared **in `internal/realtime`** so that package does
// not depend on this one — the dependency runs `plugin → realtime → domain`, and
// `AuditImports`-style test in `imports_test.go` proves the forbidden direction
// is absent rather than promising it. The cost of that decision is that the two
// vocabularies must be translated, and the translation is a table somebody wrote
// (`adapter.go` states it in full). The other cost is this package's existence:
// `internal/realtime` cannot know what a `rules.System` is, which is correct, and
// which is why the seam that turns one into the other is a type rather than a
// function somewhere in a handler.
//
// # Kinds are registry-backed, and an unknown kind is prose
//
// §10.7 makes `kind` registry-backed: semiplane's five register as built-ins and
// a gameplay plugin's `ContentKinds()` join them. What is **not** true is that a
// kind is validated against the registry — S-3.3 and §10.8's last row make an
// unrecognised kind inert, because a page whose plugin was removed degrades to
// prose with its content intact. `KnownKind` exists for the *presentations* that
// want to be specific (a template class, a route), and every caller that asks it
// must have a prose answer, which is the whole contract.
//
// # What this package deliberately does not do
//
// It does not load a pack, read a file, open a socket, mint a campaign's state,
// or mount a route. `Descriptor`/`Fingerprint` composition, the boot pass that
// reports campaigns a new fingerprint strands, and the WebSocket route are the
// composition root's, and ADR 0004's single-instance rule means the registry is a
// value the root builds once and hands to one hub. It also adds no observability
// event name: `plugin.missing` and `plugin.version_mismatch` already exist in
// §13.2's list (ADR 0032), and ADR 0032's test counts the list, so a new name
// here would fail the build rather than ship quietly.
package plugin

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/realtime"
)

// ErrMalformedPlugin is the umbrella every refusal in this package satisfies, so
// a composition root can ask one question — "is this something I may register?" —
// with `errors.Is` and never name a sentinel.
//
// The same shape as `rules.ErrMalformedSystem`, and deliberately a *separate*
// umbrella rather than an alias: a refusal about the registry (a duplicate id, a
// missing codec) and a refusal about a system's own contract are different
// questions with different owners, and merging them would let a caller that
// registered four good systems and one malformed one discover which it got.
var ErrMalformedPlugin = errors.New("plugin: something this registry cannot accept")

// The refusals about a registration.
//
// Each names what broke it and none carries anything from a vault: an id, a
// codec, a kind. A plugin's identifiers are compiled in, so they are operator
// input rather than attacker input — but a refusal message is still a log line
// and is written as if it were.
var (
	// ErrNoCodec is a registration with no codec, or a nil one.
	//
	// Its own sentinel because it is the one refusal here that is *not* about a
	// system's self-description. `rules.System` returns mutations whose payload is
	// opaque (S-12.3: semiplane transports args and never interprets them), so
	// something has to know how a system's own encoding relates to a hub
	// placement — and only the system can, because only the system wrote the
	// encoding. `Codec` is that something, and a system registered without one
	// could resolve an intent into a mutation nobody could apply.
	ErrNoCodec = fmt.Errorf(
		"%w: a system is registered with a codec that translates its payloads, "+
			"because semiplane does not interpret them",
		ErrMalformedPlugin,
	)

	// ErrDuplicateSystem is a second registration of one id.
	//
	// Refused rather than replaced, for the reason the whole registry is ordered:
	// which one won would be a function of the order the composition root happened
	// to write, and a GM at the table cannot tell 5e-2024 from whichever build
	// claimed the id second.
	ErrDuplicateSystem = fmt.Errorf("%w: a system is registered more than once", ErrMalformedPlugin)

	// ErrUnappliable is a system that resolved an intent into mutations the hub
	// cannot apply: one malformed, two naming the same placement, or a payload its
	// own codec cannot decode.
	//
	// Its own sentinel because **it is the system's fault, not the client's**, and
	// the two must not share a reason on the wire. A client handed `invalid_args`
	// for something a plugin returned will retry it; the adapter maps this to
	// `server_error`, which says nothing about what failed, while the error itself
	// names the system and the mutation for the log.
	ErrUnappliable = fmt.Errorf(
		"%w: a system resolved mutations the hub cannot apply",
		ErrMalformedPlugin,
	)

	// ErrNotAnOp is a UI plugin declaring an operation, where a UI plugin has no
	// authority to declare one. See the comment on `Operations` for why the
	// refusal exists at all when the type already has no field for it.
	ErrNotAnOp = fmt.Errorf(
		"%w: only a gameplay system defines an operation",
		ErrMalformedPlugin,
	)
)

// ErrUnknownSystem is the S-10.6 refusal: this build resolves no system with that
// id.
//
// Satisfied by `*UnknownSystemError`, which adds the id. A sentinel alone would
// make §10.8's requirement — "says which ID it wanted" — unsatisfiable by the
// caller that has to print it, because the caller would have to re-derive the id
// it already passed in.
var ErrUnknownSystem = errors.New("plugin: no gameplay system is registered with that id")

// UnknownSystemError names the id that resolved to nothing.
//
// Typed rather than only a formatted sentinel, for the reason
// `rules.OwnershipError` is: the actionable half is the id, and "no such system"
// is not something a GM can act on. It is also what makes the boot report
// readable — one line per campaign naming what is missing, which is the whole of
// §10.8's first row.
type UnknownSystemError struct {
	// ID is the identifier nothing registered. It came from a column or a
	// command line, never from a client frame, so quoting it is safe.
	ID rules.ID
}

// Error returns the id, and nothing else.
func (e *UnknownSystemError) Error() string {
	return "plugin: no gameplay system is registered with id " + string(e.ID)
}

// Unwrap returns ErrUnknownSystem, so `errors.Is` answers the one question most
// callers have and `errors.As` reaches the detail.
func (e *UnknownSystemError) Unwrap() error { return ErrUnknownSystem }

// Class returns the content-free class a log line should carry, because
// `observability.errorClass` falls back to `%T` and would report
// `*plugin.UnknownSystemError` — an identifier no alert can match.
func (e *UnknownSystemError) Class() string { return "plugin.missing" }

// Operations is the half of a gameplay system that names its operation
// vocabulary.
//
// It is a **second interface, not a tenth method**, and ADR 0041 is why: that
// record argues against adding methods to `rules.System` at all, because every
// method makes every future method a breaking change for every plugin. The
// vocabulary is also the one thing the interface genuinely does not need to
// resolve an intent — `Apply` decides that per intent, in the system's own words.
//
// So what is this for? S-10.3: a UI plugin may only emit operations **some
// gameplay system already resolves**, and "some registered system resolves `roll`"
// is a question about the build rather than about the intent. Answering it needs
// the system's answer, and a system that implements this interface has exactly
// one place to give it.
//
// **A system that does not implement it resolves no operation the UI tier may
// emit.** That is the safe direction, and it is deliberate: the alternative —
// asking nobody and assuming yes — is how a UI plugin ships a button that emits
// an op the game refuses, and the refusal arrives at the table with the plugin's
// name on it.
type Operations interface {
	// Resolves reports whether this system resolves op.
	//
	// A predicate over the system's **own** vocabulary, which is the same
	// separation `realtime.validOpShape` draws: the codec validates an op's
	// shape and leaves membership to the system, because membership is the
	// system's answer and a list anywhere else is a list every new system has to
	// edit.
	Resolves(op rules.Op) bool
}

// Codec is how one system's opaque payloads relate to the hub's placements.
//
// It exists because of two facts that meet in one place. `rules.Mutation.Args` is
// the *system's* payload, which semiplane transports and never interprets
// (S-12.3), and `realtime.Placement` is semiplane's envelope of position, hit
// points, conditions and visibility. Nothing in either package's vocabulary says
// what the first means for the second, and only the system can: it is the code
// that wrote the encoding in `Object.Data` and therefore the code that can read
// it back out of `Mutation.Args`.
//
// **That is why `Object` and `Apply` are one interface and not two seams.** Split
// them and a system has two places to disagree about its own encoding, which is a
// mutation that decodes into one shape and a state that holds another.
//
// A system whose placements are exactly semiplane's own needs none of this: pass
// `PlacementCodec`, which is the projection with no opinion in it.
type Codec interface {
	// Object renders a placement as the game object a system sees.
	//
	// The `Kind` is the system's answer rather than semiplane's, because the hub
	// has no kind column on a placement and §10.2.1's rule is that the kind is
	// what a client renders from — so a system that places something that is not
	// a token needs to say so, and a system that does not say so gets
	// `PlacementCodec`'s answer.
	Object(placement realtime.Placement) (rules.Object, error)

	// Apply writes what a resolved mutation says onto a placement.
	//
	// Called on a **draft** copy outside the hub's lock and never inside it, so a
	// codec that cannot decode refuses the whole resolution with nothing stamped
	// — see `Entry.apply`. The fields the hub owns are stamped over whatever this
	// leaves: `ID` and `Version` are not the codec's to write (S-7.2), and
	// `Conditions` stays sorted through `AddCondition`.
	Apply(placement *realtime.Placement, mutation rules.Mutation) error
}

// Entry is one registered gameplay system: the plugin, and the codec that
// translates what it returns.
//
// The two halves travel together because a system without its codec is a system
// whose mutations cannot be applied, and finding that out on the first intent at
// a table is the wrong time to find it out.
type Entry struct {
	// System is the plugin. Required.
	System rules.System

	// Codec translates its payloads. Required — see `ErrNoCodec`.
	Codec Codec
}

// Registry is the ordered set of gameplay systems this build ships.
//
// Safe for concurrent use. Registration happens in the composition root and
// lookup happens on every intent, so the two are separated by an `RWMutex` rather
// than by a convention that nobody will remember to keep.
type Registry struct {
	mu      sync.RWMutex
	entries []Entry
	// index is a position in entries, not a copy of it, so a listing and a
	// lookup cannot disagree about which object an id resolved to.
	index map[rules.ID]int
	// kindTable is §10.7's registry-backed `kind`. It has its own lock because
	// the UI tier reads kinds on a page render while a boot is still registering,
	// and it is reached through `kinds()` rather than directly.
	kindTable *kindRegistry
}

// New returns an empty registry with semiplane's five kinds registered as
// built-ins.
//
// **Empty**, and the five kinds are the only thing in it: a build that registers
// no system serves every campaign's wiki and resolves no game anywhere, which is
// the honest state of a build whose plugins were removed. S-10.6 wants that to be
// survivable, and it is survivable precisely because a registry can be empty.
//
// Built-in kinds are registered here rather than in a package variable because
// §10.7 makes the set a property of the running build, and because a variable
// would be reachable mutable state in a package whose whole argument is that
// there is none.
func New() *Registry {
	// The five, in `rules.SemiplaneKinds`'s order, which is wiki kinds then the
	// VTT's. Nothing here can fail: each is `rules.Kind.Valid` and distinct, and
	// `rules`'s own test pins that set — so a registration that reported an error
	// would be reporting `internal/domain`'s bug as this package's, and the
	// composition root has no way to tell the two apart.
	return &Registry{
		index:     make(map[rules.ID]int),
		kindTable: newKindRegistry(rules.SemiplaneKinds()),
	}
}

// Register adds one gameplay system, in the order the call was written.
//
// It is the whole of S-10.1's registration, and it refuses in the order a plugin
// author should be told: the codec, then everything `rules.Validate` checks,
// then the duplicate. The codec first because it is the one refusal here that
// nothing upstream checks, and a system with no codec cannot resolve anything
// useful — telling an author their grammar is wrong when the real problem is that
// nobody can apply what it returns is how a plugin ships broken.
func (r *Registry) Register(entry Entry) error {
	if entry.Codec == nil {
		return fmt.Errorf("%w: system %q", ErrNoCodec, systemID(entry))
	}

	if err := rules.Validate(entry.System); err != nil {
		return fmt.Errorf("plugin: register system %q: %w", systemID(entry), err)
	}

	// A **typed** nil is not caught by the `== nil` above and is not caught here
	// either, deliberately: `rules.Validate` has already called a method on it by
	// this line, so a plugin registered as a nil pointer panicked at the
	// composition root — which owns the wiring and is the right place to find out.
	// `rules.Validate`'s own comment makes the same argument, and the alternative
	// (a reflection check in a package about game rules) would be machinery for a
	// mistake a developer makes once and never twice.

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.index[entry.System.ID()]; taken {
		return fmt.Errorf("%w: id %q", ErrDuplicateSystem, entry.System.ID())
	}

	// ContentKinds is a slice the plugin owns, so it is copied before the kinds
	// are registered rather than aliased into the registry.
	r.kindTable.declare(entry.System.ID(), slices.Clone(entry.System.ContentKinds()))
	r.index[entry.System.ID()] = len(r.entries)
	r.entries = append(r.entries, entry)

	return nil
}

// Resolve returns the entry for id, or the S-10.6 refusal naming it.
//
// The one function in this package that produces `*UnknownSystemError`, so a boot
// pass and a per-intent lookup report a missing plugin identically.
func (r *Registry) Resolve(id rules.ID) (Entry, error) {
	if !id.Valid() {
		return Entry{}, fmt.Errorf("%w: %q", rules.ErrInvalidID, id)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	position, found := r.index[id]
	if !found {
		return Entry{}, &UnknownSystemError{ID: id}
	}

	return r.entries[position], nil
}

// Lookup returns the entry for id, and whether there was one.
//
// The predicate form, for a caller with a fallback that is *not* an error — the
// boot pass that reports "this campaign's plugin is missing" while continuing
// through the rest is one, and `Resolve` would give it an error it has to
// classify to get back to the same place.
func (r *Registry) Lookup(id rules.ID) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	position, found := r.index[id]

	if !found {
		return Entry{}, false
	}

	return r.entries[position], true
}

// Systems returns every registered system, in registration order.
//
// **A fresh slice per call.** A shared one would be mutable global state in a
// package whose argument is that there is none: a caller that sorted it, or
// appended to it, would corrupt the answer for every later caller — and a
// `/readyz` listing is exactly the caller that sorts.
func (r *Registry) Systems() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Clone(r.entries)
}

// IDs returns every registered id, in registration order.
//
// The listing's own use, so it exists rather than being left to a caller to
// project: the order is the property, and a caller building it from `Systems()`
// with a `map` in between would lose it.
func (r *Registry) IDs() []rules.ID {
	entries := r.Systems()

	ids := make([]rules.ID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.System.ID())
	}

	return ids
}

// Resolves reports whether some registered system resolves op.
//
// S-10.3's question, asked of the build: "may a UI plugin emit this?" It is
// answered from every system's own `Operations`, and a system that does not
// implement it contributes nothing — see `Operations` for why that direction is
// the safe one.
//
// Registration order decides ties, and it does not matter: the answer is a
// `bool` about whether *some* system resolves the op, and which one it was is
// not a question this function can answer usefully.
func (r *Registry) Resolves(operation rules.Op) bool {
	if !operation.Valid() {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, entry := range r.entries {
		operations, ok := entry.System.(Operations)
		if !ok {
			continue
		}

		if operations.Resolves(operation) {
			return true
		}
	}

	return false
}

// Empty reports whether no gameplay system is registered.
//
// A predicate rather than `len(Systems()) == 0` because the two differ in intent:
// this one is asked "can this build start a game at all", which is what a boot
// report wants to say before it lists anything.
func (r *Registry) Empty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.entries) == 0
}

// systemID names a registration for a refusal message, whether or not it has a
// system.
//
// A nil system is refused by `rules.Validate` with `ErrNoSystem`, and a refusal
// about a registration that named no system must say so rather than printing an
// empty id next to the word "system".
func systemID(entry Entry) string {
	if entry.System == nil {
		return ""
	}

	return entry.System.ID().String()
}
