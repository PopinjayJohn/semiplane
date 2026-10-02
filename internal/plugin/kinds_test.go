package plugin_test

import (
	"context"
	"slices"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
)

// TestNewRegistersSemiplanesFiveKinds is §10.2.1's ownership table, asserted
// positionally rather than as a set.
//
// A set comparison would pass if the order changed, and the order is what a status
// page and a template class print. `rules.SemiplaneKinds`'s order is wiki kinds
// then the VTT's, and this test fails the day the two disagree.
func TestNewRegistersSemiplanesFiveKinds(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	if got := registry.Kinds(); !slices.Equal(got, rules.SemiplaneKinds()) {
		t.Errorf("Kinds() is %v, want %v", got, rules.SemiplaneKinds())
	}

	for _, kind := range rules.SemiplaneKinds() {
		if !registry.KnownKind(kind) {
			t.Errorf("a new registry does not know %q", kind)
		}

		if !registry.SemiplaneKind(kind) {
			t.Errorf("a new registry does not own %q", kind)
		}

		// No system declares a built-in, so this is empty — and it is the answer
		// rather than a gap. See `Registry.DeclaringSystems`.
		if got := registry.DeclaringSystems(kind); len(got) != 0 {
			t.Errorf("%q is declared by %v, want nobody", kind, got)
		}
	}
}

// TestASystemsContentKindsJoinTheRegistry is §10.7's "a gameplay plugin registers
// additional kinds at startup", and the ordering rule with it: built-ins first,
// then declared kinds in registration order.
func TestASystemsContentKindsJoinTheRegistry(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(
		t,
		registry,
		systemWith(func(s *stubSystem) { s.kinds = []rules.Kind{"spell", "class"} }),
	)

	if !registry.KnownKind("spell") {
		t.Error("a registered system's kind is not known")
	}

	want := append(rules.SemiplaneKinds(), "spell", "class")
	if got := registry.Kinds(); !slices.Equal(got, want) {
		t.Errorf("Kinds() is %v, want %v", got, want)
	}

	if got := registry.DeclaringSystems("spell"); !slices.Equal(got, []rules.ID{"5e-2024"}) {
		t.Errorf("DeclaringSystems(%q) is %v, want [5e-2024]", "spell", got)
	}

	if registry.SemiplaneKind("spell") {
		t.Error("a declared kind is reported as semiplane's")
	}
}

// TestTwoSystemsMayDeclareTheSameKind is §10.2.1's explicit requirement — a system
// registers its own `ancestry` "without colliding with, or being blocked by, 5e's
// vocabulary" — and both 5e revisions declaring `spell` is the case that makes it
// real.
//
// It is also the failure this test exists to catch: a kind registry that refuses a
// second declaration would make 5e-2014 unregistrable in any build that has 5e-2024,
// which is the blocking §10.2.1 names.
func TestTwoSystemsMayDeclareTheSameKind(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.kinds = []rules.Kind{"spell", "class"}
	}))
	registerWith(t, registry, systemWith(func(s *stubSystem) {
		s.id = "5e-2014"
		s.kinds = []rules.Kind{"spell", "class"}
	}))

	// Declared twice is **listed twice**, in registration order, and the kind
	// appears once in `Kinds`.
	if got := registry.DeclaringSystems(
		"spell",
	); !slices.Equal(
		got,
		[]rules.ID{"5e-2024", "5e-2014"},
	) {
		t.Errorf(
			"DeclaringSystems(%q) is %v, want both systems in registration order",
			"spell",
			got,
		)
	}

	count := 0

	for _, kind := range registry.Kinds() {
		if kind == "spell" {
			count++
		}
	}

	if count != 1 {
		t.Errorf("Kinds() lists %q %d times, want once", "spell", count)
	}
}

// TestAnUnknownKindIsInertAndNotAnError is S-3.3 and §10.8's last row from the
// registry's half.
//
// The other half — a page with an unknown kind rendering as prose with its content
// intact — belongs to the wiki route and is asserted there. What this package owes
// is that the *question* is answerable without an error, because a presentation that
// asked and got a refusal would have nowhere to go but to fail the page.
func TestAnUnknownKindIsInertAndNotAnError(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, validSystem("5e-2024"))

	for _, kind := range []rules.Kind{"ancestry", "feat", "creature", "starfinder_role", "nonsense"} {
		if registry.KnownKind(kind) {
			t.Errorf("%q is known to a build that never registered it", kind)
		}

		if got := registry.DeclaringSystems(kind); len(got) != 0 {
			t.Errorf("%q is declared by %v, want nobody", kind, got)
		}

		if registry.SemiplaneKind(kind) {
			t.Errorf("%q is reported as semiplane's", kind)
		}
	}

	// An unusable kind name is equally inert: it is not a kind this build would
	// ever render, and a page naming it degrades rather than fails.
	if registry.KnownKind("Spell With Spaces") {
		t.Error("an unusable kind name is reported as known")
	}
}

// TestResolvesTrustsTheSystemThatAnswers is S-10.3's question asked of the build: may
// a UI plugin emit this operation?
func TestResolvesTrustsTheSystemThatAnswers(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, &opSystem{
		stubSystem: systemWith(func(s *stubSystem) { s.id = "5e-2024" }),
		ops:        []rules.Op{"roll", "set_hp"},
	})

	for _, op := range []rules.Op{"roll", "set_hp"} {
		if !registry.Resolves(op) {
			t.Errorf("Resolves(%q) is false for an op the system declares", op)
		}
	}

	for _, op := range []rules.Op{"move_token", "cast_fireball", ""} {
		if registry.Resolves(op) {
			t.Errorf("Resolves(%q) is true for an op no system declares", op)
		}
	}

	// An op outside the wire's token shape is refused before any system is asked.
	// `rules.Op.Valid` mirrors the codec's rule, so an op it accepts is one the
	// codec would carry and one the codec refuses never reaches a system.
	if registry.Resolves("Roll With Spaces") {
		t.Error("Resolves accepted an op that is not a token")
	}
}

// TestResolvesTrustsTheSystemThatDoesNotAnswer is the safe direction, and it is the
// reason `Operations` is optional.
//
// A system that does not implement the interface contributes no operations, so a UI
// plugin that emits one is refused at registration. The alternative — asking nobody
// and assuming yes — is how a plugin ships a button whose op the game refuses, and
// the refusal arrives at the table with the plugin's name on it.
func TestResolvesTrustsTheSystemThatDoesNotAnswer(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			// The system *does* resolve a roll. It just does not say so, and the
			// registry believes the silence rather than reading its Apply.
			return nil, nil
		}
	}))

	if registry.Resolves("roll") {
		t.Error(`Resolves("roll") is true for a system that does not implement Operations`)
	}
}

// TestAnEmptyRegistryAnswersEveryQuestion is the S-10.6 build: plugins removed, wiki
// serving, nothing playable. Every read must answer rather than panic, because the
// operator's first question is "is anything registered".
func TestAnEmptyRegistryAnswersEveryQuestion(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	if registry.Resolves("roll") {
		t.Error("an empty registry resolves an operation")
	}

	if got := registry.Kinds(); len(got) != len(rules.SemiplaneKinds()) {
		t.Errorf("an empty registry reports %d kinds, want the %d built-ins",
			len(got), len(rules.SemiplaneKinds()))
	}

	if _, found := registry.Lookup("5e-2024"); found {
		t.Error("an empty registry found a system")
	}
}
