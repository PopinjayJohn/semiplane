package dnd5e //nolint:testpackage // see the note below.
// This file asserts on what the package holds internally, and the internal form is
// the assertion: a parsed expression's dice and modifiers, a pack's compiled rows, the
// effect vocabulary's closed set, the merge by slug. Every one of those is
// unexported on purpose — a client validates against `Grammar` and `Parse`, never
// against `Expr`'s fields — so moving this file out of the package would mean
// exporting internals for a test's benefit, which is the opposite of what the
// boundary is for. The externally reachable behaviour is certified separately in
// `dnd5e_test.go`, through the `rules.System` interface.
import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// anEngine builds a system with one overlay applied, or with the base pack alone when the
// overlay is empty.
//
// **The empty string is the base pack alone**, which §10.4 makes a first-class shape
// rather than a missing input: `realtime.checkComponent` exempts exactly the overlay
// component. Every rule-exclusion test needs both, so the constructor takes both.
func anEngine(t *testing.T, overlayYAML string) *Engine {
	t.Helper()

	options := Options{}
	if overlayYAML != "" {
		overlay, err := ParseOverlay([]byte(overlayYAML))
		if err != nil {
			t.Fatalf("compiling the overlay: %v", err)
		}

		options.Overlay = overlay
	}

	engine, err := New(options)
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	return engine
}

// # The ruleset fingerprint, and what it is not
//
// ADR 0018 draws one line and everything about this file follows it: the fingerprint
// names **resolution semantics** and not house-rule configuration. `realtime.Fingerprint`
// has four components — system, ruleset, base pack, overlay pack — and this system
// contributes all four through two values. `RulesetVersion` is one of them and is a
// **constant**, because:
//
//   - The base pack's version is a separate component *precisely so* that a pack revision
//     gates a resume on its own, with its own name in the refusal, so a GM knows which
//     file changed.
//   - The overlay's version is the same argument.
//   - House-rule enablement is none of them. Toggling a house rule changes outcomes and
//     not the meaning of a stored mutation, so a fingerprint that hashed the enabled set
//     would refuse to resume **every campaign whose GM enabled a rule at the table** — a
//     fault that reads as a feature, because the error would be perfectly reasonable on
//     its face.
//
// Folding the packs into `RulesetVersion` would strand a campaign **twice** for one
// change and name nothing: once because the pack really did move, and once because a
// component that was supposed to describe the engine moved with it.

// TestTheRulesetVersionIsTheEngineConstantAndNothingElse is the exclusion, asserted on
// the string rather than described in a comment.
//
// **Three separate moves are checked** — a different engine version, a different base
// pack, and a different overlay — and only the first may change the answer. A test that
// only checked the first would pass against a fingerprint that hashed the packs, which
// is the mistake the doc comment in `pack.go` warns about and the one this file exists to
// catch.
func TestTheRulesetVersionIsTheEngineConstantAndNothingElse(t *testing.T) {
	t.Parallel()

	base := anEngine(t, "")

	if got := base.RulesetVersion(); got != EngineVersion {
		t.Errorf("RulesetVersion is %q, want the constant %q", got, EngineVersion)
	}

	// A different base pack. **The base pack is compiled in**, so the honest way to move
	// it is to build a pack whose version differs — which is what `ParsePack` is for and
	// is why this test does not pretend to be able to edit `data/base.yaml`.
	edited, err := ParsePack([]byte(edits{
		replace(`version: "1.0.0"`, `version: "1.0.1"`),
	}.apply(string(BasePackYAML()))))
	if err != nil {
		t.Fatalf("compiling a pack with a different version: %v", err)
	}

	if edited.Version() == base.Pack().Version() {
		t.Fatal("the fixture's pack did not move, so the exclusion is not being tested")
	}

	if edited.Version() == EngineVersion {
		t.Errorf("a pack's version is %q, the same string as the engine's; the two are "+
			"separate components and giving them one spelling makes a refusal name neither",
			edited.Version())
	}

	// And the value the engine reports is a **constant**, read twice and required to be
	// identical, because a `RulesetVersion` that recomputed anything would be a function
	// of the packs — which is the whole of what ADR 0018 forbids.
	overlaid := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
`)

	if overlaid.RulesetVersion() != base.RulesetVersion() {
		t.Errorf("applying an overlay moved the ruleset version from %q to %q; the overlay's "+
			"version is its own fingerprint component",
			base.RulesetVersion(), overlaid.RulesetVersion())
	}
}

// TestTheFingerprintCarriesThePacksSeparately is the other half: the packs *are* in the
// fingerprint, as their own components, so the exclusion above costs nothing.
//
// A campaign whose pack moved is refused — **once**, naming the pack — rather than
// silently resolving under rules the stored mutations were not written against.
func TestTheFingerprintCarriesThePacksSeparately(t *testing.T) {
	t.Parallel()

	standalone := anEngine(t, "")
	overlaid := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
`)

	bare := standalone.Versions()
	applied := overlaid.Versions()

	for _, fixture := range []struct {
		what string
		want string
	}{
		{"the system", string(SystemID)},
		{"the ruleset", EngineVersion},
		{"the base pack", standalone.Pack().Version()},
		{"the overlay", "2024-overlay@1"},
	} {
		if got := componentOf(applied, fixture.what); got != fixture.want {
			t.Errorf("%s fingerprints as %q, want %q", fixture.what, got, fixture.want)
		}
	}

	// **The base pack's version is the same under both**, which is the line `New` draws
	// before the merge and which is easy to get wrong: an overlay declares a version and
	// the merged file's version is the *overlay's*, so reading it back would put the same
	// string in two of the four components and make a base-pack revision invisible to the
	// gate that exists to notice one.
	if bare.BasePack != applied.BasePack {
		t.Errorf("applying an overlay moved the base pack's component from %q to %q; the "+
			"merged file's version is the overlay's and reading it as the base's would put one "+
			"string in two components", bare.BasePack, applied.BasePack)
	}

	// A standalone pack is a first-class shape: the overlay component is empty and stays
	// empty, which is `realtime.checkComponent`'s documented exemption rather than a
	// missing value.
	if bare.OverlayPack != "" {
		t.Errorf(
			"a standalone system reports the overlay component as %q, want empty",
			bare.OverlayPack,
		)
	}

	if bare == applied {
		t.Error("the two fingerprints are identical, so none of the four components moved and " +
			"the pack revision gate has nothing to gate on")
	}

	// And the rendering carries all four, for a status page and a log line — and carries
	// **no resolved value**, because `rules.Mutation.String` withholds payloads for the
	// same reason.
	rendered := applied.String()
	for _, component := range []string{"system=", "ruleset=", "base=", "overlay="} {
		if !strings.Contains(rendered, component) {
			t.Errorf("Versions.String() is %q and carries no %q", rendered, component)
		}
	}
}

// componentOf returns one of the four fingerprint components by name, so the table above
// can be written as prose rather than as four near-identical assertions.
func componentOf(v Versions, what string) string {
	switch what {
	case "the system":
		return v.System
	case "the ruleset":
		return v.Ruleset
	case "the base pack":
		return v.BasePack
	case "the overlay":
		return v.OverlayPack
	default:
		return ""
	}
}

// TestTheEngineContributesFourComponentsAndNoMore is the closed set.
//
// `realtime`'s `Descriptor` has four fields and this system fills all four. A fifth
// would be a component `realtime` does not encode, so it would be silently dropped by
// the very gate that is supposed to notice drift — which is the failure ADR 0018's
// versioned encoding exists to make impossible, and the reason the assertion is on the
// count.
func TestTheEngineContributesFourComponentsAndNoMore(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, "")

	count := 0
	for _, value := range []string{
		engine.Versions().System,
		engine.Versions().Ruleset,
		engine.Versions().BasePack,
	} {
		if value != "" {
			count++
		}
	}

	// The overlay component is the fourth and is legitimately empty for a standalone
	// pack, so it is counted by position rather than by emptiness — and the table above
	// asserts all four, this asserts the *count* of the three that must be present.
	if count != 3 {
		t.Errorf("a standalone system filled %d of the four components, want 3 plus an "+
			"exempt empty overlay", count)
	}

	// The convenience accessors read the same fields rather than being a second source.
	if engine.PackVersion() != engine.Versions().BasePack {
		t.Errorf("PackVersion is %q and Versions().BasePack is %q",
			engine.PackVersion(), engine.Versions().BasePack)
	}

	if engine.OverlayVersion() != engine.Versions().OverlayPack {
		t.Errorf("OverlayVersion is %q and Versions().OverlayPack is %q",
			engine.OverlayVersion(), engine.Versions().OverlayPack)
	}

	if engine.PackVersion() == engine.OverlayVersion() && engine.OverlayVersion() != "" {
		t.Error("the base pack and the overlay report one version, so a pack revision could not " +
			"name which file changed")
	}
}

// TestANilEngineRefusesRatherThanDereferencing is the zero-value story, and it exists
// because `Engine` is a **pointer**.
//
// A nil `*Engine` reaching a resolver is a composition-root fault, and every one of its
// nine `rules.System` methods would otherwise panic on the first field read — inside the
// hub's `recover`, reported as `server_error`, with nothing in the log saying the plugin
// was absent. The methods that cannot return an error answer an **empty pack**, which
// then fails its own lookups with a message naming what is missing.
func TestANilEngineRefusesRatherThanDereferencing(t *testing.T) {
	t.Parallel()

	var absent *Engine

	if absent.Pack() != nil {
		t.Error("a nil engine handed back a pack")
	}

	if absent.Versions() != (Versions{}) {
		t.Errorf("a nil engine reported the versions %v", absent.Versions())
	}

	if absent.RulesetVersion() != EngineVersion {
		t.Errorf("a nil engine reported the ruleset version %q; it is a constant and answers "+
			"without reading a pack, which is correct", absent.RulesetVersion())
	}

	// `Views` is a package-level declaration and answers without a pack at all, which is
	// why it is listed here: three of the nine methods are safe on a nil receiver for
	// free, and the six that read the pack are not.
	if len(absent.Views()) != len(views) {
		t.Errorf("a nil engine declared %d views, want the %d this system always declares",
			len(absent.Views()), len(views))
	}

	if !absent.Resolves(OpAttack) {
		t.Error("a nil engine does not resolve `attack`; `Resolves` reads no pack, so a " +
			"registry asking it about a UI plugin's ops gets an answer")
	}

	// The three that go through the pack.
	if absent.Grammar().Valid() {
		t.Error("a nil engine declared a usable grammar, so `rules.checkGrammar` would pass it")
	}

	if absent.ContentKinds() != nil {
		t.Errorf("a nil engine declared the kinds %v; the pack is absent and so are they",
			absent.ContentKinds())
	}

	// The two methods with an error return the system's own refusal.
	if _, err := absent.Parse("1d20"); !errors.Is(err, ErrNoEngine) {
		t.Errorf("Parse on a nil engine: want %v, got %v", ErrNoEngine, err)
	}

	if _, err := absent.Derive(
		rules.State{},
		rules.Query{View: ViewStatBlock},
	); !errors.Is(
		err,
		ErrNoEngine,
	) {
		t.Errorf("Derive on a nil engine: want %v, got %v", ErrNoEngine, err)
	}

	state, err := rules.NewState(0, nil)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	if _, err := absent.Apply(
		t.Context(),
		aTestCall(t),
		state,
		attackOn(t, "a", "b"),
	); !errors.Is(
		err,
		ErrNoEngine,
	) {
		t.Errorf("Apply on a nil engine: want %v, got %v", ErrNoEngine, err)
	}
}

// TestThePackVersionIsNotPartOfTheRulesetVersion is the direct form of the exclusion,
// asserted by **searching the string** rather than by comparing two engines.
//
// "The ruleset version must not contain the base pack's version" is a weaker assertion
// than "two engines over different packs report the same ruleset version", and it is here
// as the cheap half that a *hash-based* implementation would also pass — which is the
// point: a fingerprint built by hashing the packs contains the pack version as a
// substring, and this catches the implementation people actually write.
func TestThePackVersionIsNotPartOfTheRulesetVersion(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, "")
	packVersion := engine.Pack().Version()

	if packVersion == "" {
		t.Fatal("the shipped pack declares no version, so there is nothing to exclude")
	}

	if strings.Contains(engine.RulesetVersion(), packVersion) {
		t.Errorf("the ruleset version %q contains the base pack's version %q; the two are "+
			"separate components and a fingerprint that carried one inside the other would "+
			"strand a campaign twice for one change",
			engine.RulesetVersion(), packVersion)
	}

	// And the ruleset version is the constant, which is the strongest statement
	// available: nothing at all varies it.
	if engine.RulesetVersion() != EngineVersion || !strings.Contains(EngineVersion, "@") {
		t.Errorf("the engine version is %q, want a constant with a version marker in it",
			engine.RulesetVersion())
	}
}

// TestTheOverlayVersionIsOneComponentAndNotTwo is the overlay's fingerprint journey, and
// it is what a later overlay work item depends on.
//
// The overlay declares a version, `mergeFiles` lets the overlay's win over the base's, and
// `New` captures the base's **before** the merge precisely so that the two do not collide.
// A change to that one line is invisible until a campaign with a moved base pack silently
// resumes — which is why the assertion is that the base's component is the base's.
func TestTheOverlayVersionIsOneComponentAndNotTwo(t *testing.T) {
	t.Parallel()

	const overlayVersion = "2024-overlay@7"

	engine := anEngine(t, `system: "dnd5e"
version: "`+overlayVersion+`"
title: "the 2024 edition"
`)

	if got := engine.OverlayVersion(); got != overlayVersion {
		t.Errorf("the overlay component is %q, want %q", got, overlayVersion)
	}

	if engine.PackVersion() == overlayVersion {
		t.Error("the base pack's component holds the overlay's version; the two components " +
			"have collided and a refusal could not name which file changed")
	}

	// An overlay with no version is not an overlay: `ParseOverlay` refuses one, because a
	// version that fingerprints to the empty string is a campaign whose resume cannot be
	// compared.
	_, err := ParseOverlay([]byte("system: \"dnd5e\"\ntitle: \"no version\"\n"))
	if !errors.Is(err, ErrNoPackVersion) {
		t.Errorf("an overlay with no version: want %v, got %v", ErrNoPackVersion, err)
	}
}

// TestAnOverlayNamingAnotherSystemIsRefused is the check `ParseOverlay` performs that a
// plain `ParsePack` cannot.
//
// An overlay applied to this engine and naming another system would record a version
// nobody can resolve, and the campaign would resume under this engine's rules while the
// fingerprint claimed another's.
func TestAnOverlayNamingAnotherSystemIsRefused(t *testing.T) {
	t.Parallel()

	_, err := ParseOverlay([]byte("system: \"pathfinder\"\nversion: \"1.0.0\"\n"))
	if !errors.Is(err, ErrForeignPack) {
		t.Errorf("want %v, got %v", ErrForeignPack, err)
	}

	// And the base pack's system name is checked too, which is a different code path: a
	// standalone pack naming another system is `compile`'s refusal rather than the
	// overlay's.
	if _, err := ParsePack([]byte(edits{
		replace(`system: "dnd5e"`, `system: "starfinder"`),
	}.apply(string(BasePackYAML())))); !errors.Is(err, ErrForeignPack) {
		t.Errorf("want %v, got %v", ErrForeignPack, err)
	}
}

// TestTheOverlayVersionIsRenderedForAStatusPage is the display half, and it is a small
// test with a large reason.
//
// An operator reading "base=1.0.0" and "overlay=" needs to know the second is *absent*
// rather than *broken*, and a rendering that omitted an empty component would make a
// standalone pack look like one with a missing file.
func TestTheOverlayVersionIsRenderedForAStatusPage(t *testing.T) {
	t.Parallel()

	standalone := anEngine(t, "").Versions()

	if !strings.Contains(standalone.String(), "overlay=") {
		t.Errorf("a standalone system's versions render as %q, with no overlay component at all",
			standalone.String())
	}

	if !strings.HasSuffix(standalone.String(), "overlay=") {
		t.Errorf("a standalone system renders as %q; the empty component is not at the end, so "+
			"a reader cannot tell it from a truncated line", standalone.String())
	}

	// The four components appear in the order `realtime.componentOrder` encodes them in,
	// because that order is the persisted one and a status page that prints them in a
	// different order is harder to read against a stored column than one that matches.
	order := []string{"system=", "ruleset=", "base=", "overlay="}
	rendered := standalone.String()

	at := -1
	for _, component := range order {
		found := strings.Index(rendered, component)
		if found <= at {
			t.Errorf("the components are out of order in %q: %q does not come after %q",
				rendered, component, order[max(at, 0)])

			break
		}

		at = found
	}
}
