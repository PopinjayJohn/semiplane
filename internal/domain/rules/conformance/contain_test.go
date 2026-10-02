package conformance_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
)

// TestTheBoundaryContainsAResolverThatPanics is §10.8's panic row as a unit test, on
// its own rather than through an audit.
//
// It belongs here because the boundary is production code — the composition root's
// adapter calls it — and a production function whose only tests are the ones that
// happen to run during someone else's certification is a production function whose
// tests live in another package's test file. The audit exercises the same path on
// every run, with a resolver the suite broke itself; this exercises it with one whose
// panic value is an `error` rather than a string, because that is the other shape a
// plugin's panic takes and the one `PanicError.Error` prints differently.
func TestTheBoundaryContainsAResolverThatPanics(t *testing.T) {
	t.Parallel()

	var state rules.State

	mutations, err := conformance.Contain(panicsWith{}).Resolve(
		t.Context(),
		rules.Context{Campaign: 1, Actor: 2, Role: domain.RoleGM},
		state,
		rules.Intent{Op: "roll"},
	)

	if err == nil {
		t.Fatal("a resolver that panicked returned no error, so the panic crossed the boundary")
	}

	if len(mutations) > 0 {
		t.Errorf("a resolver that panicked returned %d mutation(s)", len(mutations))
	}

	if !errors.Is(err, conformance.ErrContained) {
		t.Fatalf("want %v, got %v", conformance.ErrContained, err)
	}

	var contained *conformance.PanicError
	if !errors.As(err, &contained) {
		t.Fatalf(
			"the refusal is not a *PanicError, so an operator reading the log gets no value: %v",
			err,
		)
	}

	if contained.Value == nil {
		t.Error("the panic's value was dropped, so the log line says only that something panicked")
	}

	if contained.Class() == "" {
		t.Error("the refusal has no class, so a log line carrying it cannot be matched by an alert")
	}

	if !containsText(contained.Error(), "did not parse") {
		t.Errorf(
			"the message does not carry the panic's value, which is the whole reason the boundary "+
				"keeps it: %s",
			contained.Error(),
		)
	}
}

// TestTheBoundaryDiscardsTheMutationsOfAFailedResolution is the half of S-10.2 that is
// not free, and the reason `Resolve` zeroes its results on an error.
//
// A resolver that has resolved half an operation by the time it fails — which is
// exactly when a bad data pack fails, after the first three mutations are in the slice
// — would otherwise hand the hub a partial list. The hub's only choices would then be
// applying a truncated resolution or discarding mutations the campaign's log may
// already claim happened.
func TestTheBoundaryDiscardsTheMutationsOfAFailedResolution(t *testing.T) {
	t.Parallel()

	partial, err := rules.NewMutation("p1", "set_hp", []byte("0"))
	if err != nil {
		t.Fatalf("building a mutation: %v", err)
	}

	mutations, err := conformance.Contain(halfBuilt{residues: []rules.Mutation{partial}}).Resolve(
		t.Context(),
		rules.Context{Campaign: 1, Actor: 2, Role: domain.RoleGM},
		rules.State{},
		rules.Intent{Op: "set_hp", Target: "p1"},
	)
	if err == nil {
		t.Fatal("a half-resolved operation reported success")
	}

	if len(mutations) > 0 {
		t.Errorf(
			"a refused resolution returned %d mutation(s): a list half-built on the way to being "+
				"discarded is a list whose contents depend on when it was abandoned",
			len(mutations),
		)
	}

	if !errors.Is(err, errStubBadPayload) {
		t.Errorf(
			"the boundary replaced the system's own refusal with %v; the operator reading a log "+
				"needs to know whether the plugin or the product gave up",
			err,
		)
	}
}

// TestTheBoundaryRefusesANilResolver covers the seam `Contain` tolerates at
// construction.
//
// Failing closed rather than reporting success, which is `realtime.NewGate`'s argument:
// a boundary wired to nothing that resolves nothing is worse than no boundary, because
// it is trusted.
func TestTheBoundaryRefusesANilResolver(t *testing.T) {
	t.Parallel()

	mutations, err := conformance.Contain(nil).Resolve(
		t.Context(),
		rules.Context{Role: domain.RoleGM},
		rules.State{},
		rules.Intent{Op: "roll"},
	)

	if !errors.Is(err, conformance.ErrContained) {
		t.Fatalf("want %v, got %v", conformance.ErrContained, err)
	}

	if len(mutations) > 0 {
		t.Errorf("a resolver that does not exist returned %d mutation(s)", len(mutations))
	}
}

// TestTheBoundaryPassesASuccessfulResolutionThrough is the other direction: a boundary
// that threw away good answers would satisfy every other test here.
//
// Checked on the bytes rather than on a `reflect.DeepEqual` over a slice, because the
// payload is what matters and a value comparison over `[]rules.Mutation` is exactly the
// comparison `fingerprintMutations` exists to make properly.
func TestTheBoundaryPassesASuccessfulResolutionThrough(t *testing.T) {
	t.Parallel()

	wanted, err := rules.NewMutation("p1", "set_hp", []byte("7"))
	if err != nil {
		t.Fatalf("building a mutation: %v", err)
	}

	got, err := conformance.Contain(single{residues: []rules.Mutation{wanted}}).Resolve(
		t.Context(),
		rules.Context{Campaign: 1, Actor: 2, Role: domain.RoleGM},
		rules.State{},
		rules.Intent{Op: "set_hp", Target: "p1"},
	)
	if err != nil {
		t.Fatalf("a successful resolution was refused: %v", err)
	}

	if len(got) != 1 || string(got[0].Args) != "7" || got[0].Target != "p1" ||
		got[0].Op != "set_hp" {
		t.Errorf("the boundary changed a successful answer: %+v", got)
	}
}

// TestAResultIsAnErrorOnlyWhenItFoundSomething is the small contract an author's test
// depends on: `nil` means certified, and a non-nil one names every objection.
func TestAResultIsAnErrorOnlyWhenItFoundSomething(t *testing.T) {
	t.Parallel()

	quiet := conformance.Result{}
	if !quiet.OK() {
		t.Error("a result with no findings is not OK")
	}

	if err := quiet.Err(); err != nil {
		t.Errorf("a result with no findings returned %v", err)
	}

	noisy := conformance.Result{Findings: []conformance.Finding{
		{Audit: conformance.AuditRole, Summary: "first"},
		{Audit: conformance.AuditVersion, Summary: "second"},
	}}

	if noisy.OK() {
		t.Error("a result with findings is OK")
	}

	err := noisy.Err()
	if err == nil {
		t.Fatal("a result with findings returned no error")
	}

	if !containsText(err.Error(), "first") || !containsText(err.Error(), "second") {
		t.Errorf("the error does not name every finding: %v", err)
	}

	// Every finding carries its audit's name, because "the suite failed" with no
	// indication which audit is what an author reads at half past midnight.
	if !containsText(err.Error(), string(conformance.AuditRole)) {
		t.Errorf("the error does not say which audit objected: %v", err)
	}
}

// panicsWith is a resolver that panics with an `error` rather than a string.
type panicsWith struct{}

func (panicsWith) ID() rules.ID { return "panics-with" }

func (panicsWith) Title() string { return "Panics with an error" }

func (panicsWith) RulesetVersion() string { return "panics-with@1" }

func (panicsWith) Grammar() rules.Grammar {
	return rules.Grammar{
		Notation: "none",
		Terms:    []rules.Term{{Name: "any", Pattern: "^.*$"}},
	}
}

func (panicsWith) Parse(text string) (rules.Expr, error) {
	return rules.NewExpr("panics-with", "none", text, text), nil
}

func (panicsWith) Apply(
	context.Context,
	rules.Context,
	rules.State,
	rules.Intent,
) ([]rules.Mutation, error) {
	panic(errors.New("panics-with: the pack entry did not parse"))
}

func (panicsWith) Derive(_ rules.State, query rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload(query.View, nil)
	if err != nil {
		return rules.Payload{}, fmt.Errorf("panicsWith: deriving %q: %w", query.View, err)
	}

	return payload, nil
}

func (panicsWith) Views() []rules.View {
	return []rules.View{
		{Name: "any", Title: "Any", Renderer: rules.RendererGeneric, Shape: rules.ShapeKeyValue},
	}
}

func (panicsWith) ContentKinds() []rules.Kind { return nil }

// halfBuilt is a resolver that says what changed and then gives up.
type halfBuilt struct {
	rules.System

	residues []rules.Mutation
}

func (h halfBuilt) Apply(
	context.Context,
	rules.Context,
	rules.State,
	rules.Intent,
) ([]rules.Mutation, error) {
	return h.residues, fmt.Errorf("residue: %w", errStubBadPayload)
}

// single is a resolver that answers with exactly what it was given, successfully.
type single struct {
	rules.System

	residues []rules.Mutation
}

func (s single) Apply(
	context.Context,
	rules.Context,
	rules.State,
	rules.Intent,
) ([]rules.Mutation, error) {
	return s.residues, nil
}

// containsText is `strings.Contains`, spelled out so the helper reads as a predicate
// rather than as a search. A fixture that returns true for an empty needle would make
// `mentions(found, "")` pass for every finding, which is a test that cannot fail.
func containsText(haystack, needle string) bool {
	return needle != "" && strings.Contains(haystack, needle)
}
