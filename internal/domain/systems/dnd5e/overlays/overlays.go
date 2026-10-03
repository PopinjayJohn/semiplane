// Package overlays carries the two editions of 5e this engine ships — 2014 and 2024 —
// as data overlays over `dnd5e`'s shared base pack.
//
// # What this package is for
//
// §10.4 draws the shape and then the claim that depends on it:
//
//	base pack (shared 5e data)
//	  ├── overlay:  2014  — race naming, crit on 20 only, no mastery properties
//	  └── overlay:  2024  — species, mastery properties, crit on any natural 20
//
// *"The differences show up as reviewable data diffs."* This package is where that claim
// is either true or not, and the command that answers it is
// `diff data/dnd5e-2014.yaml data/dnd5e-2024.yaml`: every edition difference in this
// engine is on the screen, and nothing else is. There is **no Go in this package that a
// rule runs.** It parses two embedded files and hands them to the engine, which is the
// whole of what "the differences are data" means operationally.
//
// # Why a separate package rather than two more files in `dnd5e`
//
// Three reasons, and the first is mechanical:
//
//   - **`go:embed` cannot reach a parent directory.** An overlay file sitting in
//     `dnd5e/data/` could only be embedded by the `dnd5e` package, and putting it there
//     puts *this edition's choices* inside the engine's package — which is precisely the
//     boundary ADR 0045 draws. `dnd5e` embeds the base pack because the base pack is the
//     engine's own vocabulary; an edition is somebody else's choice about that vocabulary.
//   - **The base pack is a first-class subject and an overlay is not.** `dnd5e.New(Options{})`
//     with no overlay is a complete system, and this package must not make it look like a
//     partial one. An overlay is one strategy among several (§10.4's closing sentence), and a
//     package that holds only strategies can be omitted without touching the engine.
//   - **It is copyable.** A deployment wanting a third variant — a house system, a legacy
//     campaign's own rules — copies this directory and nothing else. Forking `dnd5e` to add
//     one would be the fork §10.4 exists to avoid.
//
// # The four differences, and none of them is a Go rule
//
// §10.4 names three edition differences and this engine has four, the fourth being the one
// `dnd5e`'s own `hooks.go` describes: 2024 exempts attacks against an incapable, paralysed
// or unconscious creature from the critical rule. All four are **rows, a switch or a label**:
//
//   - `toggles.crit_damage_die_max` — 2014 crits only on a natural 20 from the attack die;
//     2024 also crits when a damage die shows its maximum.
//   - `toggles.crit_ignored_by_incapacitated` — 2024's exemption; false in 2014.
//   - `attack.mastery.enabled` — 2014 has no weapon mastery properties at all.
//   - The `ancestry` kind's **label** — "Race" in 2014, "Species" in 2024.
//
// Nothing here needs a third hook, and that is the finding rather than the coincidence:
// every one of the four is a *policy* the shared procedure already reads, so the procedure
// never forked. The thing that would have needed a hook is a difference in what the
// engine *asks*, and there is none. `dnd5e`'s
// `TestEveryHookIsNamedInThePackAndThePackNamesNoOther` holds the count at two and this
// package adds nothing to it.
//
// # What this package deliberately does not do
//
// It does not add a build tag, a registry, an `init()`, or a chain. ADR 0018's exclusions
// are the reason: a house-rule module applied over an overlay is P2d's shape and it is
// *data-level*, so it composes with these files without a seam here. What it does is hand
// `dnd5e.Options` a `dnd5e.Overlay`, and the composition root decides which edition a
// campaign runs — explicitly, where every other choice in this repository is made.
package overlays

import (
	_ "embed"
	"errors"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
)

// The two editions' files, `go:embed`ed so a deployed binary cannot be missing the rules
// it claims to run.
//
// **`_ "embed"` and not `embed.FS`,** and the reason is that this package reads each file
// exactly once, at the moment a caller asks for that edition. A filesystem would add a
// lookup and a name to get wrong for no gain, and `dnd5e` embeds its base pack the same way
// for the same reason.
//
// The unexported names are the whole of "two editions", and the file names carry the
// version's identity twice: in the path a reader opens and in the `version:` key the file
// declares. `TestTheEmbeddedFileNamesAndItsDeclaredVersionAgree` is what holds the two in
// step, because a file called `2024` that fingerprints as the 2014 overlay is a campaign
// that resumed under the wrong rules and reported nothing.

//go:embed data/dnd5e-2014.yaml
var edition2014YAML []byte

//go:embed data/dnd5e-2024.yaml
var edition2024YAML []byte

// EditionID names one edition of 5e, and is what a deployment's own configuration holds.
//
// **A string and not an enum**, for the reason `rules.Op` is one: the set is the *product's*
// vocabulary, written in a campaign's own configuration, and a closed Go type here would
// make adding an edition an edit to this file rather than a new YAML. §10.4 wants editions
// to be data; a type that has to be extended in Go is the beginning of the fork it refuses.
type EditionID string

// The editions this package ships.
const (
	// Dnd5e2014 is D&D 5e 2014.
	Dnd5e2014 EditionID = "2014"

	// Dnd5e2024 is D&D 5e 2024.
	Dnd5e2024 EditionID = "2024"
)

// The refusals, each carrying this package's own identifier and nothing from a file.
//
// `ErrUnknownEdition` does **not** satisfy `dnd5e.ErrPack`, and deliberately: an unknown
// id is a caller naming an edition this build does not ship, which is a configuration
// fault, and folding it under the pack umbrella would have a composition root answering
// "did the packs load?" about a pack that was never read.
var (
	// ErrUnknownEdition is an edition id no embedded file answers.
	ErrUnknownEdition = errors.New("overlays: there is no such edition of 5e")

	// ErrNoEdition is the zero `Edition` asked to build a system. It is a separate
	// refusal from an unknown id because the two have different fixes: one is a typo and
	// the other is a `var` nobody filled in.
	ErrNoEdition = errors.New("overlays: there is no edition to resolve with")
)

// Edition is one edition of 5e: an embedded overlay, parsed, ready to hand the engine.
//
// **Immutable, and safe for concurrent use.** Every field is written by `ByID` and read
// by every other method, and `dnd5e.Overlay` is itself a value with unexported fields, so
// a campaign's edition is a thing two goroutines can hold at once. There is no `init()` and
// no package-level mutable state: which edition a deployment ships is a choice made at the
// composition root, in the place where every other choice is made.
//
// The zero `Edition` is **not** a base pack with no overlay. `dnd5e.Overlay`'s own `Zero`
// predicate says a zero overlay means "the base pack alone", which is a first-class shape
// §10.4 says so in as many words — so a zero `Edition` would quietly resolve under the base
// pack while naming no edition at all, and a campaign would record a fingerprint with an
// empty overlay component and no way to tell that is what happened. `System` refuses one.
type Edition struct {
	// id is the edition this was parsed from, and is what `ID` returns. Kept beside the
	// overlay rather than read out of the YAML again, because the YAML's `version` is the
	// *fingerprint component* and this is the *name* a deployment chose; they are related
	// but they are not the same thing, and conflating them would make renaming an id look
	// like a pack revision.
	id EditionID

	// overlay is the parsed replacements. Unexported because merging is `dnd5e`'s job
	// and this package's whole contract is "apply me".
	overlay dnd5e.Overlay
}

// ByID returns one edition, parsed from its embedded file.
//
// **A constructor that can fail**, and the shape `dnd5e.ParseOverlay` forces: the file is
// compiled in, so a refusal here is a broken checkout rather than bad input, and the two
// have to be told apart anyway — an id nobody ships is a configuration fault, and a file
// that will not parse is a defect. Swallowing the second and returning the base pack
// instead would be the failure this whole design refuses elsewhere: an edition difference
// that is silently not applied, with the fingerprint recording it as applied.
//
// Errors from `dnd5e.ParseOverlay` satisfy `dnd5e.ErrPack`, so the composition root asks
// one question — "did the packs load?" — and gets an answer that covers both files.
func ByID(id EditionID) (Edition, error) {
	source, known := sourceFor(id)
	if !known {
		return Edition{}, fmt.Errorf("%w: %q", ErrUnknownEdition, id)
	}

	overlay, err := dnd5e.ParseOverlay(source)
	if err != nil {
		return Edition{}, fmt.Errorf("overlays: the %s edition: %w", id, err)
	}

	return Edition{id: id, overlay: overlay}, nil
}

// ID returns the edition this is.
func (e Edition) ID() EditionID { return e.id }

// Name returns the edition's display name, for a status page and for the refusal a bad
// pack produces.
//
// **Read from the file's own `title` rather than from a Go constant**, and the reason is
// that this package must not be a second answer: `dnd5e` prints the name in its load
// refusal, a campaign's status page prints it, and three spellings of one edition's name
// is three chances for a log line to disagree with a page about which rules are running.
func (e Edition) Name() string { return e.overlay.Name() }

// Version returns the edition's version, which is the third of the four
// `realtime.Fingerprint` components.
//
// The overlay's, and not a value composed from the id: ADR 0018 wants the fingerprint to
// name resolution semantics, and a pack revision has to be a thing a GM can be told about
// by name. `dnd5e-2024-overlay@2` is that name; "2024" is not, because a GM who changed
// nothing and a GM who changed a row both wrote `2024`.
func (e Edition) Version() string { return e.overlay.Version() }

// Overlay returns the overlay itself, for a caller that has to carry it as a value.
//
// **Not a way to merge by hand.** `dnd5e` unexports `apply` precisely so that merging is
// compiled once and held, and handing the overlay out does not change that: what a caller
// gets is an opaque value it can put in `dnd5e.Options`. What it is for is the composition
// root's own chain — base pack → overlay → enabled house-rule modules — which is a value
// it must be able to hold, pass and record, and P2d's `Scope` is a property of the *pack*
// rather than of a system.
func (e Edition) Overlay() dnd5e.Overlay { return e.overlay }

// Options returns the engine options for this edition: the base pack with this overlay
// applied.
//
// A method rather than a caller spelling `dnd5e.Options{Overlay: …}`, so that the
// composition root does not have to know that `dnd5e.Options` happens to have one field
// shaped exactly like this and will keep having one. A deployment adding a second knob to
// `Options` edits this one method.
func (e Edition) Options() dnd5e.Options { return dnd5e.Options{Overlay: e.overlay} }

// System builds the 5e system this edition resolves under.
//
// **Refuses the zero `Edition`** for the reason the type's own comment gives: an empty
// overlay is a valid *pack* — §10.4 makes the base pack alone a first-class system — and an
// invalid *edition*. A caller that meant the base pack asks for it, by not asking for an
// edition at all; a caller whose `var` was never filled in should be told.
func (e Edition) System() (*dnd5e.Engine, error) {
	if e.id == "" {
		return nil, fmt.Errorf(
			"%w: the zero Edition names no edition, and building the base pack alone is "+
				"`dnd5e.New(dnd5e.Options{})`",
			ErrNoEdition,
		)
	}

	engine, err := dnd5e.New(e.Options())
	if err != nil {
		return nil, fmt.Errorf("overlays: the %s edition: %w", e.id, err)
	}

	return engine, nil
}

// Source returns one edition's file, byte for byte.
//
// **A clone, never the embedded slice.** `go:embed`'s value is package state, and a caller
// that appended to it would corrupt every edition that had not been parsed yet — and a
// rules pack corrupted in memory is a campaign whose fingerprint says one thing and whose
// resolver does another. The engine's `BasePackYAML` clones for the same reason.
//
// It exists for two readers, and both are outside this package: a test asserting that the
// two files differ in exactly the ways §10.4 names, which cannot see the merged pack's
// provenance; and an operator reading the rules a deployed binary is running, who has the
// binary and no source checkout. `go tool nm` would find the variable; this makes it
// printable.
func Source(id EditionID) ([]byte, error) {
	source, known := sourceFor(id)
	if !known {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEdition, id)
	}

	return append([]byte(nil), source...), nil
}

// IDs returns every edition this package ships, in declaration order.
//
// **A fresh slice per call**, for the reason `rules.SemiplaneKinds` gives: a shared slice
// handed to a caller that appended to it is package state changing under a registry, and
// the caller most likely to append is one building a menu of what a campaign may run.
//
// Declaration order and not sorted, and the two happen to coincide — `2014` sorts before
// `2024` — but the order is the *author's*, which is the property every ordered walk in
// this repository uses.
func IDs() []EditionID {
	return []EditionID{Dnd5e2014, Dnd5e2024}
}

// sourceFor returns one edition's embedded bytes.
//
// **A `switch` and not a map**, for the reason `dnd5e.knownOp` is a `switch`: the
// vocabulary is this package's declaration, written out where adding an edition is an edit
// a reviewer sees rather than an entry somebody appends to a table beside a loop. It also
// keeps the set out of package-level mutable state, which a `map[EditionID][]byte` would
// be — and `sourceFor` is the only place the two files are named, so there is one answer
// to "which file is this edition".
func sourceFor(id EditionID) ([]byte, bool) {
	switch id {
	case Dnd5e2014:
		return edition2014YAML, true
	case Dnd5e2024:
		return edition2024YAML, true
	default:
		return nil, false
	}
}
