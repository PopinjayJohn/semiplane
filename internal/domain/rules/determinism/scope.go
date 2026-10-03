package determinism

import (
	"errors"
	"fmt"
	"slices"
)

// The house-rule half of S-10.5.
//
// S-10.5 reads: "House rules are **data-level only**. A module that reorders
// resolution or introduces nondeterminism is rejected." That is the only half of
// the house-rule feature this package owns, and it is the half with no other
// enforcement: the lint rule and `Audit` catch *code* that reaches for a clock or a
// map, and neither of them can see a compiled-in module's claim about what it may
// do. A module that declares it may reorder resolution is not breaking a rule that
// any linter reads.
//
// # Why the claim is checked rather than read
//
// A compiled-in module's author is this repository. A comment saying "this module
// only toggles flags" is exactly as enforceable as every other comment, and the
// consequence of being wrong is silent: a replay that does not replay, and an audit
// trail that cannot be checked. So a module declares a `Scope`, and `Admissible`
// refuses a scope that is not data-level only. The vocabulary is deliberately tiny
// and lives here rather than in `internal/domain/rules/houserules`, which owns
// *applying* modules and owns the config format this deliberately does not define.
//
// # Why it stands alone
//
// `Admissible` is a pure function over a compiled-in value. It reads no
// configuration, touches no database, and depends on no registry — which is what
// makes it safe for the house-rule work item to call at registration time without
// this package and that package having to know about each other.

var (
	// ErrInadmissibleModule is the umbrella every refusal here satisfies, so a
	// registrar can ask "may I enable this module?" with `errors.Is` and never name
	// a sentinel. Same shape as `rules.ErrMalformedSystem`, and for the same
	// reason: a refusal a caller has to match on text is a refusal whose handling
	// drifts.
	ErrInadmissibleModule = errors.New(
		"determinism: a house-rule module that is not data-level only",
	)

	// ErrReordersResolution is a module that declares authority to change the
	// order rules resolve in. S-10.5 names this failure explicitly, and it is the
	// one that breaks replay rather than merely surprising it: two resolutions of
	// the same intent under the same module list must agree, and a reordering
	// module makes them agree only by accident of the inputs.
	ErrReordersResolution = fmt.Errorf(
		"%w: it may not reorder resolution", ErrInadmissibleModule,
	)

	// ErrUnseededRandomness is a module that declares authority to draw randomness
	// the campaign's recorded seed does not derive. The same failure S-10.4's call
	// rules exist to prevent, reached through the module boundary instead of
	// through a line of code, and for the same reason: a number nobody can
	// re-derive is not an audit trail (§16.3).
	ErrUnseededRandomness = fmt.Errorf(
		"%w: it may not draw randomness the campaign seed does not derive", ErrInadmissibleModule,
	)

	// ErrUnknownCapability is a scope naming a capability this build has no rule
	// for. Refused rather than ignored, and the direction is the whole point: a
	// capability nobody has classified must not be treated as harmless, because the
	// next build to add one may classify it as a refusal and this build would have
	// already enabled the module.
	//
	// Under the umbrella, because an unrecognised capability is not data-level
	// either — it is simply not known to be, and "not known to be safe" is the same
	// answer as "known not to be safe" for a caller deciding whether to enable it.
	ErrUnknownCapability = fmt.Errorf(
		"%w: it declares a capability this build has no rule for", ErrInadmissibleModule,
	)
)

// Capability is one thing a house-rule module declares it may do to an effective
// ruleset.
//
// A string and not an enum for the reason `rules.ID` is a string: the set is a
// property of the running build, and a closed list here would be a list a compiled-in
// module could not join without editing this file.
type Capability string

const (
	// CapToggle flips a declared boolean in a data pack. §10.5's own first
	// example — `flanking_optional`.
	CapToggle Capability = "toggle"

	// CapConstant replaces a numeric constant in a data pack. §10.5's second
	// example — "change a DC formula constant".
	CapConstant Capability = "constant"

	// CapDisable switches a named condition off. §10.5's third example — "disable
	// a condition".
	CapDisable Capability = "disable"

	// CapReorder changes the order rules resolve in. **A refusal, not a switch**:
	// listed here so that a module declaring it is refused by name rather than
	// refused for being unrecognised, because "no such capability" is a much worse
	// message for a plugin author than "this is the thing you may not do".
	CapReorder Capability = "reorder"

	// CapUnseededRandomness draws randomness the campaign's recorded seed does not
	// derive. A refusal, for the reason `CapReorder` is one.
	CapUnseededRandomness Capability = "unseeded-randomness"
)

// dataLevel lists the capabilities a house-rule module may declare.
//
// The whole of "data-level only", and it is a list rather than a predicate so that
// adding a capability is a visible edit here rather than a widening nobody reads.
var dataLevel = []Capability{CapToggle, CapConstant, CapDisable}

// Scope is the set of capabilities a compiled-in house-rule module declares it may
// exercise.
//
// A slice rather than a set because it is a declaration written once in a
// registration and read once by a check; a set would buy a deduplication nothing
// asks for, at the cost of a map range in the package that forbids map ranges.
type Scope []Capability

// Admissible reports whether every capability in s is data-level.
//
// Nil and empty are both admissible: a module that changes nothing is a module that
// cannot break anything, and refusing it would make the zero value an error for no
// reason a caller could act on.
//
// The checks run in declaration order and stop at the first refusal, so the error a
// caller gets names the first thing wrong rather than an unordered set of
// everything wrong.
func (s Scope) Admissible() error {
	for _, capability := range s {
		switch {
		case capability == CapReorder:
			return fmt.Errorf("%w: capability %q", ErrReordersResolution, capability)
		case capability == CapUnseededRandomness:
			return fmt.Errorf("%w: capability %q", ErrUnseededRandomness, capability)
		case slices.Contains(dataLevel, capability):
		default:
			return fmt.Errorf("%w: %q", ErrUnknownCapability, capability)
		}
	}

	return nil
}

// DataLevel returns the capabilities a module may declare, in a stable order.
//
// Exported so that a registrar composing a `Scope` does not have to spell three
// constants, and so that a caller wanting to enumerate the whole admissible set
// has one place to read it from rather than a duplicate list of its own.
func DataLevel() []Capability {
	return dataLevel
}

// String renders a capability as its declared spelling.
//
// Identity and not a validity check, as with `rules.ID.String`: a capability read
// from a registration is printed as it was written, so the operator reading the log
// sees the value that failed rather than a normalised one.
func (c Capability) String() string {
	return string(c)
}
