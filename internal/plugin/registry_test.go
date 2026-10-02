package plugin_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
)

// TestRegisterRefusesWhatRulesValidateRefuses is the claim that the registry
// **calls** `rules.Validate` rather than reimplementing any of it.
//
// Each row asserts the *exact* upstream sentinel. That is the whole test: a
// registry that checked the same conditions and returned its own errors would
// compile, pass every other test in this package, and be a second answer to "is
// this id usable" — the failure mode ADR 0027 and 0029 exist to warn about. Pinning
// `rules.ErrKindOwned` rather than "some error" is what makes the second
// implementation fail here.
//
// The order matters too: `ErrNoCodec` is asked **first**, before anything in the
// system is touched, because a system registered with nothing that can apply what
// it returns is a registration that cannot work whatever else is true of it.
func TestRegisterRefusesWhatRulesValidateRefuses(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		entry plugin.Entry
		want  error
	}{
		"no codec at all": {
			entry: plugin.Entry{System: validSystem("5e-2024")},
			want:  plugin.ErrNoCodec,
		},
		"no system": {
			entry: plugin.Entry{Codec: plugin.PlacementCodec{}},
			want:  rules.ErrNoSystem,
		},
		"an unusable id": {
			entry: plugin.Entry{
				System: systemWith(func(s *stubSystem) { s.id = "5e 2024" }),
				Codec:  plugin.PlacementCodec{},
			},
			want: rules.ErrInvalidID,
		},
		"no title": {
			entry: plugin.Entry{
				System: systemWith(func(s *stubSystem) { s.title = "" }),
				Codec:  plugin.PlacementCodec{},
			},
			want: rules.ErrNoTitle,
		},
		"no ruleset version": {
			entry: plugin.Entry{
				System: systemWith(func(s *stubSystem) { s.version = "" }),
				Codec:  plugin.PlacementCodec{},
			},
			want: rules.ErrNoRulesetVersion,
		},
		"a kind semiplane owns": {
			entry: plugin.Entry{
				System: systemWith(
					func(s *stubSystem) { s.kinds = []rules.Kind{"spell", "token"} },
				),
				Codec: plugin.PlacementCodec{},
			},
			want: rules.ErrKindOwned,
		},
		"a view nobody could render": {
			entry: plugin.Entry{System: systemWith(func(s *stubSystem) {
				s.views = []rules.View{{Name: "sheet", Title: "Sheet"}}
			}), Codec: plugin.PlacementCodec{}},
			want: rules.ErrInvalidView,
		},
		"a grammar that is not one": {
			entry: plugin.Entry{
				System: systemWith(func(s *stubSystem) { s.grammar = rules.Grammar{} }),
				Codec:  plugin.PlacementCodec{},
			},
			want: rules.ErrNotAGrammar,
		},
		"a term whose pattern does not compile": {
			entry: plugin.Entry{System: systemWith(func(s *stubSystem) {
				s.grammar = rules.Grammar{
					Notation: "d20",
					Terms:    []rules.Term{{Name: "roll", Pattern: "([0-9]+"}},
				}
			}), Codec: plugin.PlacementCodec{}},
			want: rules.ErrInvalidPattern,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			registry := plugin.New()

			err := registry.Register(testCase.entry)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Register returned %v, want a refusal satisfying %v", err, testCase.want)
			}

			// Nothing registered: a refusal that left a system in the registry
			// would be a worse bug than the refusal, and the adapter would resolve
			// through it.
			if !registry.Empty() {
				t.Errorf("the registry holds %d systems after a refusal", len(registry.Systems()))
			}
		})
	}
}

// TestRegisterRefusesASecondSystemWithOneID is the ordering property ADR 0011's
// `init()` argument is about, stated as a test: which of two systems claimed an id
// must not be a function of anything a reader cannot see.
func TestRegisterRefusesASecondSystemWithOneID(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, validSystem("5e-2024"))

	second := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.title = "A different build of the same id"
	})

	err := registry.Register(plugin.Entry{System: second, Codec: plugin.PlacementCodec{}})
	if !errors.Is(err, plugin.ErrDuplicateSystem) {
		t.Fatalf("Register returned %v, want a refusal satisfying ErrDuplicateSystem", err)
	}

	// The **first** registration survives. Replacing it would make the winner a
	// function of call order, which is what the refusal exists to prevent.
	entry, found := registry.Lookup("5e-2024")
	if !found {
		t.Fatal("the first registration is gone")
	}

	if entry.System.Title() != "Fixture system" {
		t.Errorf("the surviving system is %q, want the first registration",
			entry.System.Title())
	}

	if got := len(registry.Systems()); got != 1 {
		t.Errorf("the registry holds %d systems, want 1", got)
	}
}

// TestResolveNamesTheIDItWanted is S-10.6 and §10.8's first row, which is the whole
// reason `UnknownSystemError` is a type rather than a formatted sentinel.
//
// §10.8's wording is exact: a campaign whose `system_id` resolves to nothing "says
// which ID it wants". A caller handed a bare sentinel would have to know the id it
// already passed in to produce that sentence, which is a property of the caller and
// not of the refusal.
func TestResolveNamesTheIDItWanted(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	_, err := registry.Resolve("5e-2014")
	if !errors.Is(err, plugin.ErrUnknownSystem) {
		t.Fatalf("Resolve returned %v, want a refusal satisfying ErrUnknownSystem", err)
	}

	var unknown *plugin.UnknownSystemError
	if !errors.As(err, &unknown) {
		t.Fatalf("Resolve returned %T, want a *plugin.UnknownSystemError", err)
	}

	if unknown.ID != "5e-2014" {
		t.Errorf("the refusal names %q, want %q", unknown.ID, "5e-2014")
	}

	// The message is the sentence a boot pass prints, and it is checked rather than
	// assumed: `observability.Classed` and a status page both read it.
	for _, fragment := range []string{"5e-2014", "no gameplay system"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("the refusal %q does not name %q", err.Error(), fragment)
		}
	}

	if got := unknown.Class(); got != "plugin.missing" {
		t.Errorf("Class() is %q, want %q", got, "plugin.missing")
	}

	// The predicate form answers the same question for a boot pass that continues.
	if _, found := registry.Lookup("5e-2014"); found {
		t.Error("Lookup found a system in an empty registry")
	}
}

// TestResolveRefusesAnIDThatIsNotEvenWellFormed is the boundary between the two
// refusals: an id that is not a usable identifier never gets as far as being
// unknown, and the reason it does not is that the two have different owners —
// `rules` owns id validity and this package owns what is registered.
func TestResolveRefusesAnIDThatIsNotEvenWellFormed(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	_, err := registry.Resolve("5e 2024")
	if !errors.Is(err, rules.ErrInvalidID) {
		t.Fatalf("Resolve returned %v, want a refusal satisfying rules.ErrInvalidID", err)
	}

	if errors.Is(err, plugin.ErrUnknownSystem) {
		t.Error("an unusable id was reported as an unregistered one")
	}
}

// TestSystemsListsInRegistrationOrder pins §10.5's deterministic application order at
// the level it is visible from.
//
// A `map` iteration would make the order a property of the process, and the two
// things that depend on it — a `/readyz` listing and a reviewer reading
// `cmd/server` — would both be reading something the source does not say.
func TestSystemsListsInRegistrationOrder(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	ordered := []rules.ID{"5e-2024", "5e-2014", "pathfinder"}
	for _, id := range ordered {
		registerWith(t, registry, validSystem(id))
	}

	ids := registry.IDs()
	if !slices.Equal(ids, ordered) {
		t.Errorf("IDs() is %v, want %v", ids, ordered)
	}

	systems := registry.Systems()
	if len(systems) != len(ordered) {
		t.Fatalf("Systems() returned %d entries, want %d", len(systems), len(ordered))
	}

	for at, entry := range systems {
		if entry.System.ID() != ordered[at] {
			t.Errorf("Systems()[%d] is %q, want %q", at, entry.System.ID(), ordered[at])
		}
	}
}

// TestTheListingsAreNotSharedWithTheirCallers is the reason `Systems`, `IDs` and
// `Kinds` all clone.
//
// A shared slice is mutable global state in a package whose entire argument is
// that there is none, and the caller that proves it is real is the one that sorts:
// a status page listing systems in alphabetical order would silently reorder every
// later caller's registration order.
func TestTheListingsAreNotSharedWithTheirCallers(t *testing.T) {
	t.Parallel()

	registry := plugin.New()
	registerWith(t, registry, validSystem("5e-2024"))
	registerWith(t, registry, validSystem("5e-2014"))

	first := registry.Systems()
	first[0] = plugin.Entry{}

	if got := registry.Systems()[0].System.ID(); got != "5e-2024" {
		t.Errorf("a caller overwrote Systems()[0]; the registry now reports %q", got)
	}

	ids := registry.IDs()
	ids[0] = "overwritten"

	if got := registry.IDs()[0]; got != "5e-2024" {
		t.Errorf("a caller overwrote IDs()[0]; the registry now reports %q", got)
	}

	kinds := registry.Kinds()
	kinds[0] = "overwritten"

	if got := registry.Kinds()[0]; got != rules.KindJournal {
		t.Errorf("a caller overwrote Kinds()[0]; the registry now reports %q", got)
	}
}

// TestEveryRefusalIsReachableThroughTheUmbrella is a meta-check on the sentinel set,
// and it is the same argument `rules`' own version makes: a refusal that does not
// satisfy `ErrMalformedPlugin` is a refusal a composition root cannot act on, and
// the way one appears is a new `errors.New` beside a type rather than a
// `fmt.Errorf` in the block.
//
// `ErrUnknownSystem` is in the table and does not satisfy the umbrella **on
// purpose**, so it is asserted separately: an unknown system is not a malformed
// registration, it is a missing plugin, and the boot pass reports the two with
// different events.
func TestEveryRefusalIsReachableThroughTheUmbrella(t *testing.T) {
	t.Parallel()

	umbrella := []error{
		plugin.ErrNoCodec,
		plugin.ErrDuplicateSystem,
		plugin.ErrUnappliable,
		plugin.ErrNotAnOp,
	}

	for _, refusal := range umbrella {
		if !errors.Is(refusal, plugin.ErrMalformedPlugin) {
			t.Errorf("%v does not satisfy ErrMalformedPlugin", refusal)
		}
	}

	if errors.Is(plugin.ErrUnknownSystem, plugin.ErrMalformedPlugin) {
		t.Error(
			"ErrUnknownSystem satisfies the umbrella; it is a missing plugin, not a malformed one",
		)
	}

	if errors.Is(plugin.ErrPanicked, plugin.ErrMalformedPlugin) {
		t.Error("ErrPanicked satisfies the umbrella; it is a runtime crash, not a registration")
	}
}

// TestEmptyReportsABuildThatCanResolveNothing is S-10.6's precondition.
//
// A build whose plugins were removed is a build that serves every campaign's wiki
// and resolves no game anywhere. The state has to be *reportable*, because the
// operator's question is "why does nothing play" and the answer is a list of
// campaigns naming ids nothing registered.
func TestEmptyReportsABuildThatCanResolveNothing(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	if !registry.Empty() {
		t.Error("a new registry is not empty")
	}

	if got := len(registry.Systems()); got != 0 {
		t.Errorf("a new registry holds %d systems, want 0", got)
	}

	registerWith(t, registry, validSystem("5e-2024"))

	if registry.Empty() {
		t.Error("a registry with one system reports itself empty")
	}
}

// systemWith returns a valid system with one field changed.
func systemWith(change func(*stubSystem)) *stubSystem {
	system := validSystem("5e-2024")
	change(system)

	return system
}
