package houserules_test

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
)

// TestAModuleThatWouldBreakReplayIsRefusedAtRegistration is S-10.5's second sentence,
// and it is S-10.5's only enforcement that this package owns.
//
// "A module that reorders resolution or introduces nondeterminism is rejected."
// The lint rule and `determinism.Audit` catch *code* reaching for a clock or a map;
// neither can see a compiled-in module's claim about what it may do. So the claim is
// checked, at the only moment a build can still refuse it — registration, where the
// plugin author sees it — and the check is `determinism.Scope.Admissible`, which
// ADR 0044 records as that enforcement point. This package writes no second one, so
// the test that matters is that this refusal happens *at all*: removing the call
// leaves a registry that accepts a module reordering resolution, and
// `TestNoRegisterableScopeCanCarryTheTwoNamedRefusals` below is what catches it.
func TestAModuleThatWouldBreakReplayIsRefusedAtRegistration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		scope determinism.Scope
		want  error
		why   string
	}{
		{
			name:  "reorder, declared alongside a permitted capability",
			scope: determinism.Scope{determinism.CapToggle, determinism.CapReorder},
			want:  determinism.ErrReordersResolution,
			why: "S-10.5 names this failure, and it is the one that breaks replay rather " +
				"than merely surprising it",
		},
		{
			name:  "reorder, on its own",
			scope: determinism.Scope{determinism.CapReorder},
			want:  determinism.ErrReordersResolution,
			why:   "a scope is refused for what it contains, not for how much of it is permitted",
		},
		{
			name: "reorder, declared last",
			scope: determinism.Scope{
				determinism.CapConstant,
				determinism.CapDisable,
				determinism.CapReorder,
			},
			want: determinism.ErrReordersResolution,
			why: "Admissible checks in declaration order, so the permitted capabilities before it " +
				"must not make the module registrable",
		},
		{
			name:  "unseeded randomness",
			scope: determinism.Scope{determinism.CapConstant, determinism.CapUnseededRandomness},
			want:  determinism.ErrUnseededRandomness,
			why: "a number nobody can re-derive is not an audit trail, which is the same failure " +
				"S-10.4's call rules exist to prevent reached through the module boundary",
		},
		{
			name:  "a capability nobody has classified",
			scope: determinism.Scope{"scale-the-damage"},
			want:  determinism.ErrUnknownCapability,
			why: "a capability nobody has classified must not be treated as harmless, because the " +
				"next build may classify it as a refusal and this build would have enabled the module",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := houserules.NewRegistry()

			err := registry.Register(&houserules.Definition{
				ID:    "breaks-replay",
				Scope: testCase.scope,
				Apply: noChanges,
			})

			if err == nil {
				t.Fatalf("Register(scope=%v) = nil, want %v: %s",
					testCase.scope, testCase.want, testCase.why)
			}

			if !errors.Is(err, testCase.want) {
				t.Errorf("Register(scope=%v) = %v, want it to satisfy errors.Is(_, %v)",
					testCase.scope, err, testCase.want)
			}

			// The umbrella as well, because that is the question a registrar asks and
			// asking for the specific sentinel is the caller's problem, not this
			// package's. Two packages' sentinels and one `errors.Is` is the shape
			// `determinism.ErrInadmissibleModule` documents for itself.
			if !errors.Is(err, determinism.ErrInadmissibleModule) {
				t.Errorf("Register(scope=%v) = %v, want it to satisfy errors.Is(_, "+
					"determinism.ErrInadmissibleModule)", testCase.scope, err)
			}

			// Nothing may be left behind by a refusal: a module that failed to
			// register and is nevertheless resolvable would make the check above a
			// message rather than a boundary.
			if _, known := registry.Lookup("breaks-replay"); known {
				t.Error("a refused module is in the registry anyway, so the refusal is a message " +
					"and not a boundary")
			}
		})
	}
}

// TestNoRegisterableScopeCanCarryTheTwoNamedRefusals is the other half of the
// statement above, and it is stated over the *whole* vocabulary rather than over two
// literals.
//
// `determinism` exports five capabilities: three it classifies as data-level and two
// it refuses by name. This asserts that the split is exactly that — every
// data-level capability registers, and neither refusal can be smuggled in beside one
// — so a build that adds a fourth capability to `determinism.DataLevel` without
// deciding what such a module changes fails here rather than shipping a module the
// application cannot read.
func TestNoRegisterableScopeCanCarryTheTwoNamedRefusals(t *testing.T) {
	t.Parallel()

	level := determinism.DataLevel()
	if len(level) == 0 {
		t.Fatal("determinism.DataLevel() is empty, so a module can declare nothing and the " +
			"admissibility check has nothing to refuse")
	}

	for _, capability := range level {
		if !slices.Contains([]determinism.Capability{
			determinism.CapToggle, determinism.CapConstant, determinism.CapDisable,
		}, capability) {
			t.Errorf("determinism.DataLevel() contains %q, and this package has no change shape "+
				"for it: a build that classifies a capability as data-level must also say what "+
				"such a change carries", capability)
		}
	}

	refusals := []determinism.Capability{
		determinism.CapReorder,
		determinism.CapUnseededRandomness,
	}

	for _, capability := range level {
		for _, refusal := range refusals {
			registry := houserules.NewRegistry()

			err := registry.Register(&houserules.Definition{
				ID:    "sneaky",
				Scope: determinism.Scope{capability, refusal},
				Apply: noChanges,
			})

			if !errors.Is(err, determinism.ErrInadmissibleModule) {
				t.Errorf("Register(scope={%q, %q}) = %v, want a refusal; every registerable "+
					"scope excludes both named failures, which is what makes a module's body "+
					"bound by its own declaration", capability, refusal, err)
			}
		}
	}
}

// TestEveryDataLevelCapabilityHasAChangeShape is the correspondence the comment on
// `Change.Kind` claims, asserted by *running* one.
//
// A capability `determinism` classifies as data-level is a permission, and a
// permission a module cannot exercise is decoration. So each one is registered
// inside a scope of exactly itself, its module returns one change of that kind, and
// the application is required to resolve it into an `Applied` that names the module
// that won.
//
// The failure this test exists to catch is a fourth capability landing in
// `determinism.DataLevel` with no change shape here: `changeOf` has no case for it,
// the change arrives with no payload, `Change.validate` refuses it, and the test
// says so by name rather than the capability sitting silently unusable.
func TestEveryDataLevelCapabilityHasAChangeShape(t *testing.T) {
	t.Parallel()

	for _, capability := range determinism.DataLevel() {
		t.Run(capability.String(), func(t *testing.T) {
			t.Parallel()

			registry := houserules.NewRegistry()
			err := registry.Register(&houserules.Definition{
				ID:    "capable",
				Scope: determinism.Scope{capability},
				Apply: func(json.RawMessage) ([]houserules.Change, error) {
					return []houserules.Change{changeOf(capability, "the-setting")}, nil
				},
			})
			if err != nil {
				t.Fatalf("Register(scope={%q}) = %v, want nil: determinism classifies it as "+
					"data-level, so a scope of exactly it is admissible", capability, err)
			}

			effective, err := registry.Apply(t.Context(),
				[]determinism.Module{enabled(module("capable", 0))}, nil)
			if err != nil {
				t.Fatalf("Apply() = %v, want nil: a change of kind %q carries a value this "+
					"package understands, because determinism classifies it as data-level",
					err, capability)
			}

			applied, found := effective.Lookup(capability, "the-setting")
			if !found {
				t.Fatalf("Lookup(%q, \"the-setting\") reported nothing after a module "+
					"declared exactly that: %s", capability, rendered(effective))
			}

			if applied.Winner.ID != "capable" {
				t.Errorf("the applied setting names %q as its winner, want %q",
					applied.Winner.ID, rules.ID("capable"))
			}
		})
	}
}

// TestAChangeTheModuleNeverDeclaredIsRefused is the scope's second job.
//
// `Register` bounds what a module may *do* by refusing an inadmissible scope. This
// bounds what it may *return*: a module declaring `CapToggle` and handing back a
// `constant` has decided at campaign load to do something its own registration said
// it would not, and a campaign load is the only moment anybody is in a position to
// notice.
//
// The four cases include the two capabilities `determinism` refuses. They are
// unreachable through a registered module — no scope can contain them — and are here
// anyway because the bound must not depend on how the definition got here: a
// `Definition` assembled by hand, or a future caller that resolves modules without
// this registry, meets the same refusal.
func TestAChangeTheModuleNeverDeclaredIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		change houserules.Change
		why    string
	}{
		{
			name: "a constant from a module that declared only toggles",
			change: func() houserules.Change {
				return changeOf(determinism.CapConstant, "the-setting")
			}(),
			why: "the scope is the bound on the body, and a declaration nobody enforces is a comment",
		},
		{
			name:   "a capability nobody has classified",
			change: houserules.Change{Kind: "scale-the-damage", Key: "the-setting", Toggle: &yes},
			why: "the refusal must not depend on `determinism` having heard of the capability: " +
				"this package has no shape for it and says so",
		},
		{
			name: "reordering resolution",
			change: houserules.Change{
				Kind: determinism.CapReorder,
				Key:  "the-setting",
			},
			why: "unreachable through a registered module, and refused rather than accepted here so " +
				"that the bound does not depend on how the definition was assembled",
		},
		{
			name: "unseeded randomness",
			change: houserules.Change{
				Kind: determinism.CapUnseededRandomness,
				Key:  "the-setting",
			},
			why: "the other of the two S-10.5 names, and unreachable for the same reason",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := registryOf(
				t,
				scoped("overreaching", determinism.Scope{determinism.CapToggle},
					func(json.RawMessage) ([]houserules.Change, error) {
						return []houserules.Change{testCase.change}, nil
					}),
			)

			effective, err := registry.Apply(
				t.Context(),
				[]determinism.Module{enabled(module("overreaching", 0))},
				nil,
			)

			if err == nil {
				t.Fatalf("Apply() = %s, want a refusal: %s", rendered(effective), testCase.why)
			}

			if !errors.Is(err, houserules.ErrUndeclaredCapability) {
				t.Errorf("Apply() = %v, want it to satisfy errors.Is(_, "+
					"houserules.ErrUndeclaredCapability)", err)
			}

			if !effective.Empty() {
				t.Errorf("Apply() returned %s alongside a refusal; a campaign load that cannot "+
					"honour the whole set must not return half of it", rendered(effective))
			}
		})
	}
}

// TestAMalformedChangeIsRefused covers every way a change can be something other than
// a setting.
//
// Each case is a module that declared the capability honestly and then built the
// change wrongly: no name, a value the kind does not carry, or both. They are
// refusals rather than coercions for the reason `Change.validate` gives — "the value
// the kind does not carry" is a module meaning something this package cannot
// express, and guessing which of the two it meant is how a house rule silently does
// not apply.
func TestAMalformedChangeIsRefused(t *testing.T) {
	t.Parallel()

	twenty := 20.0

	cases := []struct {
		name   string
		change houserules.Change
	}{
		{
			name:   "a toggle with no name",
			change: houserules.Change{Kind: determinism.CapToggle, Toggle: &yes},
		},
		{
			name: "a toggle with no value",
			change: houserules.Change{
				Kind: determinism.CapToggle,
				Key:  "flanking_optional",
			},
		},
		{
			name: "a toggle carrying a constant as well",
			change: houserules.Change{
				Kind:     determinism.CapToggle,
				Key:      "flanking_optional",
				Toggle:   &yes,
				Constant: &twenty,
			},
		},
		{
			name:   "a constant with no value",
			change: houserules.Change{Kind: determinism.CapConstant, Key: "dc_passive"},
		},
		{
			name: "a constant carrying a toggle as well",
			change: houserules.Change{
				Kind:   determinism.CapConstant,
				Key:    "dc_passive",
				Toggle: &yes,
			},
		},
		{
			name: "a disable carrying a value",
			change: houserules.Change{
				Kind:   determinism.CapDisable,
				Key:    "prone",
				Toggle: &yes,
			},
		},
		{
			name:   "a disable with no name",
			change: houserules.Change{Kind: determinism.CapDisable},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := registryOf(t, scoped("malformed", dataLevel(),
				func(json.RawMessage) ([]houserules.Change, error) {
					return []houserules.Change{testCase.change}, nil
				}))

			effective, err := registry.Apply(
				t.Context(),
				[]determinism.Module{enabled(module("malformed", 0))},
				nil,
			)

			if !errors.Is(err, houserules.ErrMalformedChange) {
				t.Fatalf("Apply() = (%s, %v), want an error satisfying errors.Is(_, "+
					"houserules.ErrMalformedChange)", rendered(effective), err)
			}

			if !errors.Is(err, houserules.ErrInadmissibleModule) {
				t.Errorf("Apply() = %v, want it to satisfy errors.Is(_, "+
					"houserules.ErrInadmissibleModule) so a registrar can ask one question", err)
			}
		})
	}
}

// TestAWellFormedChangeIsAccepted is the other half of the table above, and it is
// here rather than implied: an audit that only ever rejects proves nothing about
// what it accepts, and the zero value of `Change` — an empty struct — is refused for
// two independent reasons, which is a refusal a reader could mistake for a strictness
// nobody wanted rather than for two missing fields.
func TestAWellFormedChangeIsAccepted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		change houserules.Change
		want   string
	}{
		{
			name:   "a toggle",
			change: changeOf(determinism.CapToggle, "flanking_optional"),
			want:   "toggle flanking_optional",
		},
		{
			name:   "a constant",
			change: changeOf(determinism.CapConstant, "dc_passive"),
			want:   "constant dc_passive",
		},
		{
			name:   "a disable",
			change: changeOf(determinism.CapDisable, "prone"),
			want:   "disable prone",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := registryOf(t, declaring("well-formed", testCase.change))

			effective, err := registry.Apply(
				t.Context(),
				[]determinism.Module{enabled(module("well-formed", 0))},
				nil,
			)
			if err != nil {
				t.Fatalf("Apply() = %v, want nil for a well-formed change", err)
			}

			if got := rendered(effective); !strings.Contains(got, testCase.want) {
				t.Errorf("Apply() = %s, want it to record the setting %q", got, testCase.want)
			}
		})
	}
}

// TestTheRegistryRefusesADefinitionItCannotApply is the wiring faults, and each is
// its own sentinel because a caller with two of them is told about one.
//
// A nil definition, a definition with no `Apply`, an identifier that could not be
// stored, and two modules answering to one id. The last is the one with teeth: a
// registry holding whichever was registered second would make a campaign's
// configuration depend on registration order, which is a package-level mutable state
// decided by an import graph — the thing `init()` registration exists to avoid.
func TestTheRegistryRefusesADefinitionItCannotApply(t *testing.T) {
	t.Parallel()

	valid := func(id rules.ID) *houserules.Definition {
		return &houserules.Definition{ID: id, Scope: dataLevel(), Apply: noChanges}
	}

	cases := []struct {
		name    string
		first   *houserules.Definition
		second  *houserules.Definition
		want    error
		wantMsg string
	}{
		{
			name:    "no definition at all",
			second:  nil,
			want:    houserules.ErrIncompleteDefinition,
			wantMsg: "a nil definition could not be refused for anything else, so it is reported first",
		},
		{
			name:   "no Apply",
			second: &houserules.Definition{ID: "no-apply", Scope: dataLevel()},
			want:   houserules.ErrIncompleteDefinition,
			wantMsg: "a nil function is a wiring fault, and reading it as a module that changes nothing is " +
				"the kind of silence that costs an afternoon",
		},
		{
			name:   "an identifier that could not be stored",
			second: &houserules.Definition{ID: "Not An ID", Scope: dataLevel(), Apply: noChanges},
			want:   rules.ErrInvalidID,
			wantMsg: "rules.ParseID is the one rule about what an identifier may contain, and a campaign " +
				"row could never name this module",
		},
		{
			name:   "two modules answering to one id",
			first:  valid("twice"),
			second: valid("twice"),
			want:   houserules.ErrDuplicateModule,
			wantMsg: "a campaign's configuration must not depend on which module a registry happened " +
				"to register second",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := houserules.NewRegistry()

			if testCase.first != nil {
				if err := registry.Register(testCase.first); err != nil {
					t.Fatalf("Register(%q) = %v, want nil", testCase.first.ID, err)
				}
			}

			err := registry.Register(testCase.second)

			if !errors.Is(err, testCase.want) {
				t.Fatalf("Register() = %v, want an error satisfying errors.Is(_, %v): %s",
					err, testCase.want, testCase.wantMsg)
			}
		})
	}
}

// TestTheRegistryListsItsModulesInOrder is the one method that reads a map.
//
// `Registry.IDs` walks the module map, so "in order" is the whole of its contract:
// a listing that reordered between two runs with nothing having changed is a listing
// a startup message cannot diff and a test cannot compare, and the range it must not
// write is the range `determinism.Audit` refuses — which is why the walk is a sorted
// key list rather than a `range`.
func TestTheRegistryListsItsModulesInOrder(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("zebra"),
		declaring("averaging"),
		declaring("charmed"),
		declaring("flanking-optional"),
	)

	want := []rules.ID{"averaging", "charmed", "flanking-optional", "zebra"}

	for range 2 {
		got := registry.IDs()

		if !slices.Equal(got, want) {
			t.Fatalf("IDs() = %v, want %v: the listing must not depend on a hash seed", got, want)
		}
	}
}

// TestThisPackageImportsNothingOutsideTheRulesTree is the layering rule as a test.
//
// `AGENTS.md`: "`domain` imports nothing from the project." This package reads a
// campaign's stored module set and turns it into settings, and everything that would
// make it more than that — the database that holds the rows, the engine that
// resolves them, the logger that records a conflict — lives one layer out. A
// dependency inward would turn a rule into a subsystem, and the determinism audit
// would then be type-checking a package that transitively reaches a `database/sql`.
//
// Parsed imports rather than a substring search, for the reason
// `determinism.Audit` is: an import can be aliased, grouped and commented in ways a
// search has to guess about.
func TestThisPackageImportsNothingOutsideTheRulesTree(t *testing.T) {
	t.Parallel()

	const prefix = "github.com/semiplane/semiplane/internal/domain/rules"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	checked := 0

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		checked++

		for _, imported := range parsedImports(t, name) {
			// A standard-library path has no dot in its first element.
			if !strings.Contains(strings.SplitN(imported, "/", 2)[0], ".") {
				continue
			}

			if imported == prefix || strings.HasPrefix(imported, prefix+"/") {
				continue
			}

			t.Errorf("%s imports %q; a rule package reads no project code outside "+
				"internal/domain/rules, so that the determinism audit is not type-checking "+
				"something that reaches a database", name, imported)
		}
	}

	if checked == 0 {
		t.Fatal("no non-test Go files were read, so the import rule audited nothing")
	}
}

// parsedImports returns the import paths of one file in this directory.
func parsedImports(t *testing.T, name string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(
		token.NewFileSet(),
		filepath.Join(".", name),
		nil,
		parser.ImportsOnly,
	)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}

	paths := make([]string, 0, len(parsed.Imports))

	for _, imported := range parsed.Imports {
		// `Trim` rather than `TrimPrefix`/`Trim`: an import path is a Go string
		// literal and may be backquoted, so both quote characters have to go and
		// the path is what is left.
		paths = append(paths, strings.Trim(imported.Path.Value, "`\""))
	}

	return paths
}

// yes is the boolean a fixture toggle carries when the test is about which module won
// rather than about what the value was.
var yes = true
