package plugin

import (
	"slices"
	"sync"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// kindRegistry answers "is this kind known to this build?", and §10.7's reason
// for asking is that `kind` is no longer a fixed enum: a page's front matter may
// name `ancestry`, and whether that is a game object or prose is a property of
// the plugins this process registered, not of the schema.
//
// Four fields, and the two indices are for different questions. `known` answers
// "may this build render one" and `order` makes that answer printable in a
// stable order. `owners` answers "who declared it", which is a different question
// with a different answer for two systems declaring `spell` — §10.2.1's explicit
// requirement that a system is neither blocked by nor colliding with another's
// vocabulary.
//
// `builtins` is separate from `owners` because the five semiplane-owned kinds are
// owned by *semiplane*, and no system may declare them (`rules.Validate` refuses
// one that tries). Folding them into `owners` would make the two cases
// indistinguishable at the call site, which is the question a template asks.
type kindRegistry struct {
	mu       sync.RWMutex
	order    []rules.Kind
	known    map[rules.Kind]struct{}
	owners   map[rules.Kind][]rules.ID
	builtins map[rules.Kind]struct{}
}

// newKindRegistry returns a registry holding the given built-in kinds, in order.
//
// The argument rather than a call to `rules.SemiplaneKinds()` inside, so that a
// test can register a registry holding two kinds and ask the questions the real
// one answers. The production caller passes the five; a caller that passed
// something else would get a registry that is honest about its own contents,
// which is the property a test needs.
func newKindRegistry(builtins []rules.Kind) *kindRegistry {
	registry := &kindRegistry{
		known:    make(map[rules.Kind]struct{}, len(builtins)),
		owners:   make(map[rules.Kind][]rules.ID),
		builtins: make(map[rules.Kind]struct{}, len(builtins)),
	}

	for _, kind := range builtins {
		// A duplicate built-in is ignored rather than refused: the list is a
		// compile-time constant of five distinct names, and a refusal here would
		// be a panic in `New` for something a reader of `rules.SemiplaneKinds`
		// can already see is impossible. What *is* refused is a value outside
		// `Kind.Valid`, because that is the case where the registry would be
		// claiming a kind no build could render — see `TestNewRefusesNothingItCannotRender`.
		if !kind.Valid() {
			continue
		}

		if _, seen := registry.known[kind]; seen {
			continue
		}

		registry.order = append(registry.order, kind)
		registry.known[kind] = struct{}{}
		registry.builtins[kind] = struct{}{}
	}

	return registry
}

// declare records that system declared these kinds.
//
// **A kind two systems declare is recorded twice, and that is not a conflict.**
// §10.2.1 asks for it in as many words — a system must be able to register its
// own `ancestry` "without colliding with, or being blocked by, 5e's vocabulary" —
// and both 5e revisions declare `spell`. Refusing the second declaration would
// mean 5e-2014 could not register in a build that has 5e-2024, which is the
// blocking §10.2.1 exists to prevent.
//
// The declaration is ignored rather than refused when it names a built-in, and
// that cannot happen through `Register` (`rules.Validate` refuses a system
// declaring one). The guard is here so that a future caller of this method has
// the safe answer available rather than having to derive it.
func (k *kindRegistry) declare(system rules.ID, kinds []rules.Kind) {
	k.mu.Lock()
	defer k.mu.Unlock()

	for _, kind := range kinds {
		if _, semiplane := k.builtins[kind]; semiplane {
			continue
		}

		if _, seen := k.known[kind]; !seen {
			k.known[kind] = struct{}{}
			k.order = append(k.order, kind)
		}

		// `slices.Contains` on a handful of ids rather than a nested set: a kind
		// is declared by a handful of systems, registration happens once at
		// startup, and a second index would be a second place to keep in step.
		if !slices.Contains(k.owners[kind], system) {
			k.owners[kind] = append(k.owners[kind], system)
		}
	}
}

// carries reports whether this build knows kind.
func (k *kindRegistry) carries(kind rules.Kind) bool {
	k.mu.RLock()
	defer k.mu.RUnlock()

	_, found := k.known[kind]

	return found
}

// list returns every known kind, in registration order with the built-ins first.
func (k *kindRegistry) list() []rules.Kind {
	k.mu.RLock()
	defer k.mu.RUnlock()

	return slices.Clone(k.order)
}

// declaring returns the systems whose `ContentKinds` named kind, in registration
// order.
//
// **Empty for a semiplane-owned kind**, and that is the answer rather than a gap:
// no system declares `token`, because `rules.Validate` refuses one that tries,
// so "which plugin declared `token`" has the answer "semiplane did" and a caller
// asking this is asking a question whose answer is a constant for five of the
// kinds any build has.
func (k *kindRegistry) declaring(kind rules.Kind) []rules.ID {
	k.mu.RLock()
	defer k.mu.RUnlock()

	return slices.Clone(k.owners[kind])
}

// isBuiltIn reports whether kind is one of the five semiplane owns.
func (k *kindRegistry) isBuiltIn(kind rules.Kind) bool {
	k.mu.RLock()
	defer k.mu.RUnlock()

	_, found := k.builtins[kind]

	return found
}

// KnownKind reports whether this build knows kind.
//
// **An unknown kind is not an error and this predicate is why that is safe.**
// S-3.3 makes a page whose kind nothing registers render as prose, and §10.8's
// last row makes it explicit: a page whose plugin was removed degrades to prose
// with its content intact, because the file is still on disk. So this is a
// question a *presentation* asks — a template wanting a token list rather than
// paragraphs, a route wanting to know there is a handler — and every caller that
// asks it must have the prose answer.
//
// It is deliberately **not** a validity check. `rules.Kind.Valid` is that, and it
// is a property of the bytes; this is a property of the process. A kind can be
// perfectly well formed and unknown here, and that is the ordinary state of a
// campaign whose plugin is one release behind the binary serving it.
func (r *Registry) KnownKind(kind rules.Kind) bool {
	return r.kinds().carries(kind)
}

// Kinds returns every kind this build knows, built-ins first and then declared
// kinds in registration order.
//
// **A fresh slice per call**, as with `Systems`, for the same reason: a shared
// one is mutable global state.
func (r *Registry) Kinds() []rules.Kind {
	return r.kinds().list()
}

// DeclaringSystems returns the ids of the systems whose `ContentKinds` named
// kind, in registration order.
//
// The empty answer for a semiplane-owned kind is documented on
// `kindRegistry.declaring`: it is `token` that the client renders, and it is
// semiplane's in every build.
func (r *Registry) DeclaringSystems(kind rules.Kind) []rules.ID {
	return r.kinds().declaring(kind)
}

// SemiplaneKind reports whether kind is one of the five semiplane owns,
// regardless of what any system declared.
//
// This is `rules.IsSemiplaneKind` with the registry in front of it, and the
// indirection is worth nothing except that a caller holding only a `*Registry`
// can ask. A caller that can import `rules` should ask `rules` — the set lives
// there, and this method exists for the UI tier, which holds a registry and must
// not reach past it into the domain vocabulary to decide whether a page type may
// claim a kind.
func (r *Registry) SemiplaneKind(kind rules.Kind) bool {
	return r.kinds().isBuiltIn(kind)
}

// kinds returns the kind registry, read under the registry's lock.
//
// The indirection is for two reasons, one mechanical and one about what the
// method bodies are allowed to say. Mechanically, `Register` writes kinds while
// holding the registry's lock, so a reader that fetched the table without it
// would race. And for the reader: each accessor is a question about kinds, and
// every one of them would otherwise open with two lines of lock bookkeeping that
// say nothing about the answer.
func (r *Registry) kinds() *kindRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.kindTable
}
