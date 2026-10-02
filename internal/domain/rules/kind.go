package rules

import (
	"fmt"
	"slices"
)

// maxKindLen bounds a kind at 64 bytes, the same bound as an `ID` and for the same
// reason: the two appear side by side in front matter and in a status page, and a
// reader should not have to learn two limits.
const maxKindLen = 64

// Kind is what a page is, when it is a game object rather than prose: `token`,
// `scene`, `spell`, `ancestry`.
//
// A string and not an enum, for the reason `domain.PageKind` is one: a gameplay
// plugin declares kinds of its own, so any closed list written here would be a
// list a plugin could not join — and the wrong answer for every kind it added,
// because the page would degrade to prose in a build where it is perfectly well
// formed. What counts as known is a property of the running process, asked of the
// registry.
//
// What *is* closed here is the other direction: the five kinds below. They are
// semiplane's, in every build, forever, and a plugin that redeclares one is refused
// at registration rather than merged into the registry.
type Kind string

// The kinds semiplane owns.
//
// Three are wiki kinds — content, not rules — and two are the VTT's, and the
// second pair is the load-bearing one. §10.2.1's argument is that the PixiJS map
// layer renders placements, fog and initiative order without knowing any rules, and
// that is true **because** `token` and `scene` are semiplane's: a system
// parameterises them, it does not define them. That is what makes a system sharing
// nothing with 5e — Pathfinder, Starfinder — need no client work at all, and
// therefore what makes the refusal in `Validate` worth having.
const (
	// KindJournal is a campaign journal: a log written in the vault.
	KindJournal Kind = "journal"

	// KindHandout is material handed to players.
	KindHandout Kind = "handout"

	// KindIndex is an index page.
	KindIndex Kind = "index"

	// KindToken is one instance of a game object placed on the table. Parameterised
	// by the system, defined by semiplane.
	KindToken Kind = "token"

	// KindScene is a map: placements, fog, and the layer that draws them.
	KindScene Kind = "scene"
)

// ErrInvalidKind is returned for a kind outside the shape `Valid` enforces.
var ErrInvalidKind = fmt.Errorf("%w: a declared kind is not a usable kind name", ErrMalformedSystem)

// ErrDuplicateKind is a kind declared twice by one system.
//
// Two registrations of one name is an ambiguity in the registry, and which one won
// would be a function of registration order — which, with no `init()` anywhere in
// this project, is the order the composition root happened to write.
var ErrDuplicateKind = fmt.Errorf("%w: a kind is declared more than once", ErrMalformedSystem)

// String returns the stored text, verbatim.
//
// Identity and not a validity check, as with `ID.String` and `domain.Role.String`:
// the value is printed as it was declared so an operator can see the misspelling,
// and a caller that needs to *know* the kind is one a build understands must ask
// the registry.
func (k Kind) String() string {
	return string(k)
}

// Valid reports whether k is shaped like a kind a build could register.
//
// Lowercase ASCII letters, digits, hyphens and underscores; no leading, trailing or
// doubled separator; at most 64 bytes.
//
// The restriction is not tidiness. A kind is **attacker-reachable**: it is a
// front-matter value in a vault that Obsidian Sync may share, and it reaches a
// URL segment, a token-list filter and a template class name. Every character
// allowed here is safe in all three, and `domain.ResolvePageKind` matches it
// exactly rather than folding case — so an uppercase or padded kind is not a kind
// this build would ever honour, and refusing it at registration keeps the failure
// at the plugin rather than at a page that silently rendered as prose.
func (k Kind) Valid() bool {
	if k == "" || len(k) > maxKindLen {
		return false
	}

	first, last := k[0], k[len(k)-1]

	switch {
	case first == '-' || first == '_', last == '-' || last == '_':
		return false
	}

	previousSeparator := false

	for idx := range len(k) {
		char := k[idx]

		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			previousSeparator = false
		case char == '-', char == '_':
			// Both doubled and mixed, so `spell--list` and `spell-_list` are one
			// answer rather than two spellings of one kind name.
			if previousSeparator {
				return false
			}

			previousSeparator = true
		default:
			return false
		}
	}

	return true
}

// IsSemiplaneKind reports whether kind is one semiplane owns, and is therefore a
// kind no `System` may declare.
//
// A function and not a map, for the same reason `realtime.validOpToken` is a
// switch: the set is five values, it is read once per declaration at startup, and
// a map would be package-level mutable state for no gain. A `switch` also means
// adding a sixth kind is a compile-visible edit to a function rather than an entry
// someone appends to a slice.
//
// `prose` is deliberately **not** in the set. It is `domain.KindProse`, the answer
// for a page that declared no kind and for a page whose kind nothing registers, and
// registering it would make the fallback depend on a plugin being present — which
// is the opposite of S-3.3's inertness. The `TestSemiplaneKindsAreExactlyTheFiveTheOwnershipTableNames`
// pins the set, so adding a sixth kind to this function without a record fails the
// build.
func IsSemiplaneKind(kind Kind) bool {
	switch kind {
	case KindJournal, KindHandout, KindIndex, KindToken, KindScene:
		return true
	default:
		return false
	}
}

// SemiplaneKinds returns the kinds semiplane owns, in a stable order.
//
// **A fresh slice per call**, not a package-level value. A shared slice is mutable
// global state: a registry that sorted it in place, or appended a system's kind to
// it, would corrupt the answer for every later caller — and the second half is
// exactly the mistake this function exists to prevent. The order is fixed (wiki
// kinds, then VTT kinds) so a registry, a status page and a conformance test all
// print the same five in the same order.
//
// The slice is **cloned per call**, so two callers hold two arrays. Clamping the
// capacity would not do: it stops an append reaching past the slice and leaves the
// elements themselves shared, so a caller that overwrote one would still be writing
// into the package's state. A fresh array costs one five-element allocation at
// registration and on a status page, which is not a cost worth optimising into a
// shared-mutable-state bug.
func SemiplaneKinds() []Kind {
	return slices.Clone(variants[:])
}

// variants is the order semiplane owns kinds in: wiki first, then the VTT's.
//
// An array rather than a slice because a slice header could be reassigned by a future
// edit, and because `variants[:]` of an array is the whole of it by construction.
var variants = [...]Kind{KindJournal, KindHandout, KindIndex, KindToken, KindScene}
