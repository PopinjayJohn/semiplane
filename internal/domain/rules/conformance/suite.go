package conformance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// Runs is how many times `Suite.Determinism` resolves the scenario before it
// compares anything.
//
// **One hundred, and not a knob.** S-14.6 names the number, and the argument for
// the number is that two runs would agree by luck: a resolver that reaches for
// something ambient produces the same answer for the first two calls and a
// different one for the third, so a suite that compared two runs would certify a
// system whose next roll nobody can reproduce. A `Config` field here would be a
// field an author sets to two to make a flaky test pass, and the failure it hides
// is the one the audit exists for.
const Runs = 100

// Config is everything the suite is given, and every field of it is a fact the
// suite cannot derive from a `rules.System`.
//
// The four seams and the reasoning for each are in the package comment. What
// makes this a struct rather than seven arguments is the argument
// `realtime.NewGate` makes: they are one decision — *what does this deployment
// say about this system* — and a caller who handed them over in the wrong order
// would get a working suite and a wrong one.
type Config struct {
	// System is the gameplay system under certification. Required; a nil is
	// refused by `New`.
	//
	// It is the interface and nothing else, which is the property S-14.9 is
	// about: a system may implement it from a package that has never heard of
	// semiplane and be certified, because nothing in `Config` asks for anything
	// else.
	System rules.System

	// Scenario is one typical resolution, and every audit that resolves an intent
	// resolves this one. Required, and refused when empty.
	//
	// "Typical" is load-bearing. A suite handed a resolution that does nothing
	// would certify nothing: S-14.6 is about mutations, and an intent that changes
	// nothing is reproducible whether or not the system were deterministic. The
	// determinism audit therefore also requires the scenario to produce at least
	// one mutation, and refuses to certify a system whose chosen example changes
	// nothing rather than reporting that it is deterministic.
	Scenario Scenario

	// GMOnly are the operations §7.2 reserves to this campaign's GM. Required and
	// required non-empty.
	//
	// §7.2 is a rule about *a system's* operations, and this package cannot know
	// them: a closed list here would be a list no system could join, which is the
	// mistake `rules.Op` exists to avoid. An empty list is refused rather than
	// vacuously passed, because "every op I declared GM-only was refused to a
	// player" over an empty list is true of a system that lets players do
	// everything.
	GMOnly []rules.Op

	// Classify maps one of this system's refusals to the wire word that carries it,
	// and is required.
	//
	// The mapping is the composition root's, not the system's and not this
	// package's: the root is the only place that holds both a `rules.System` and a
	// `realtime.RejectReason`. The suite asks for it because §7.2's rule is not
	// "refuse a player" — a refusal a player cannot act on is a refusal, and a
	// refusal worded `unknown_op` tells a whole campaign that its GM's operations
	// do not exist. `Suite.Role` is the audit.
	//
	// It is never called with a nil error.
	Classify func(err error) string

	// Resume answers the questions §10.8's table asks about a `ruleset_version`,
	// and is required. `Suite.Version` is the audit.
	Resume Resume
}

// Scenario is one typical resolution: the tabletop, the operation, and the rule
// context.
//
// Every field is the author's because every field is a fact about a system this
// package knows nothing about. A `State` is built fresh from `Objects` before
// every resolution rather than held, for the reason `rules`' own determinism test
// gives: a shared snapshot would let a resolver that scribbled on one hide behind
// the previous resolution's copy, and the scribbling has its own audit.
type Scenario struct {
	// Objects is the tabletop. Required non-empty, and required to carry at least
	// one object with non-empty `Data`, so that "the content survived" is a
	// comparison of bytes rather than of two empty objects.
	Objects []rules.Object

	// Intent is the operation to resolve. Required, and required valid: the suite
	// does not test the wire, which has its own tests, and an intent it had to
	// repair would be an intent no hub would ever send.
	//
	// Its operation must **not** be one of `Config.GMOnly`. The role audit resolves this
	// scenario as a player and requires the answer not to be `not_permitted`, so a
	// scenario that *is* a GM-only operation would make the audit contradict
	// itself — and `New` refuses that rather than letting the author find it out
	// from a finding that says the system is wrong.
	Intent rules.Intent

	// Call is the rule context: which campaign, which actor, what role, and the
	// seed. Required, and its `Role` must be valid, because a role no build has a
	// name for is one a system would branch on as though it were a player.
	//
	// `Suite.Role` replaces only `Role` when it runs; campaign, actor and seed are
	// the author's throughout.
	Call rules.Context
}

// Audit names one check the suite performs.
//
// A named string rather than a `func` field, for two reasons. A `Finding` has to
// say *which* audit objected, and a function has no name to print. And a suite
// whose result is "audit 3 of 5" is a suite whose result can be counted: this
// package's own test asserts `Audits` has exactly five entries in the order §14
// lists them, so a sixth audit cannot be added without a test noticing that the
// documented set and the running one have parted.
type Audit string

// The five audits §14 names for plugin conformance.
const (
	// AuditDeterminism is S-14.6: identical (state, intent, seed) yields identical
	// mutations.
	AuditDeterminism Audit = "determinism"

	// AuditUnknownKind is S-14.7: a kind nothing registers is inert rather than
	// fatal.
	AuditUnknownKind Audit = "unknown-kind tolerance"

	// AuditVersion is §10.8's ruleset row: a differing `ruleset_version` is
	// refused rather than silently misresolved, and ADR 0018's exclusion holds.
	AuditVersion Audit = "ruleset version refusal"

	// AuditRole is §7.2: the actor's role decides, and a player does not get a
	// GM-only outcome.
	AuditRole Audit = "GM-only enforcement"

	// AuditContainment is S-10.2 and §10.8: a panic inside `Apply` is recovered
	// and leaves nothing mutated.
	AuditContainment Audit = "Apply panic containment"
)

// auditOrder is the order the audits run and are documented in.
//
// Fixed, and for the reason `realtime`'s `componentOrder` is fixed: the order is a
// property of this file rather than of a collection, and a report that listed its
// findings in a different order on a different day would be a report two readers
// could not compare.
var auditOrder = [...]Audit{
	AuditDeterminism,
	AuditUnknownKind,
	AuditVersion,
	AuditRole,
	AuditContainment,
}

// Audits returns the five audits, in the order they run.
//
// **A fresh slice per call**, for the reason `rules.SemiplaneKinds` gives: a
// shared slice is mutable global state, and the caller most likely to append to
// one is a caller iterating the findings and building a summary.
func Audits() []Audit { return slices.Clone(auditOrder[:]) }

// Finding is one audit's objection to one system.
//
// Two fields, and neither of them is a `rules.Mutation`. A finding quotes what
// the audit can prove from the interface — an op name, an object id, a refusal's
// own text — and never a payload, because a payload is where a resolved roll
// lives and S-12.3 forbids one reaching anywhere it would be recorded. See the
// package comment.
type Finding struct {
	// Audit is which check objected.
	Audit Audit

	// Summary is one sentence saying what was observed, addressed to the plugin
	// author rather than to a log aggregator.
	Summary string
}

// String renders a finding for a test failure.
func (f Finding) String() string { return string(f.Audit) + ": " + f.Summary }

// Result is what a run found.
//
// A slice and a predicate rather than an `error`, because an audit's objection is
// not a failure of the run: `Err` is what turns findings into one error, and a
// caller that wants to assert on a single audit's findings has them separately
// without re-running the other four.
type Result struct {
	// Findings is every objection, in audit order and then in the order that audit
	// found them.
	Findings []Finding
}

// OK reports whether the run found nothing to object to.
func (r Result) OK() bool { return len(r.Findings) == 0 }

// Err returns the findings as one error, or nil when there are none.
//
// One error per finding rather than one joined string, so a caller can `errors.Is`
// a finding it cared about and so the failure names every objection instead of
// the first.
func (r Result) Err() error {
	if r.OK() {
		return nil
	}

	failures := make([]error, 0, len(r.Findings))

	for _, finding := range r.Findings {
		failures = append(failures, errors.New(finding.String()))
	}

	return errors.Join(failures...)
}

// ErrIncompleteSuite is the umbrella every refusal by `New` satisfies, so a
// caller wiring a suite asks one question — "did I give it everything?" — with
// `errors.Is` and never names a sentinel.
//
// A suite that is missing a seam is not a suite that finds fewer things. It is a
// suite that finds nothing, which is why every one of these is a refusal rather
// than a warning: a gate wired to nothing is worse than no gate, because it is
// trusted.
var ErrIncompleteSuite = errors.New("conformance: the suite is not configured to be able to fail")

// The refusals, each naming the seam that was not supplied and why the suite
// cannot run without it.
var (
	// ErrNoSystem is no `System`, which `rules.Validate` also refuses — the same
	// input, so the same umbrella.
	ErrNoSystem = fmt.Errorf("%w: there is no system to certify", ErrIncompleteSuite)

	// ErrNoScenario is no tabletop, no operation, or no usable rule context. Without
	// a resolution there is nothing to resolve one hundred times, so every audit
	// that resolves an intent would resolve nothing.
	ErrNoScenario = fmt.Errorf(
		"%w: a scenario needs at least one object with content, a valid intent and a valid role",
		ErrIncompleteSuite,
	)

	// ErrNoGMOnlyOps is no GM-only operation, which would make the role audit a pass
	// over an empty set. The message states the rule it exists to keep, because
	// "the GM-only audit passed" is otherwise a sentence nobody can check.
	ErrNoGMOnlyOps = fmt.Errorf(
		"%w: declare the operations §7.2 reserves to the GM; an audit no fixture reaches is "+
			"not an audit, and an empty declaration certifies nothing",
		ErrIncompleteSuite,
	)

	// ErrNoClassify is no error-to-wire-word map, without which the suite can assert
	// that a refusal exists but not that anything can be told about it.
	ErrNoClassify = fmt.Errorf(
		"%w: no classifier, so a refusal this suite found could not be told to a client",
		ErrIncompleteSuite,
	)

	// ErrNoResume is no ruleset decision, without which §10.8's row and ADR 0018's
	// exclusion are untestable.
	ErrNoResume = fmt.Errorf(
		"%w: no ruleset decision, so a differing ruleset_version is untestable",
		ErrIncompleteSuite,
	)

	// ErrGMOnlyScenario is a scenario whose operation is declared GM-only, which
	// would make the role audit assert two contradictory things about one resolution.
	ErrGMOnlyScenario = fmt.Errorf(
		"%w: the scenario's operation is also declared GM-only, so the role audit would "+
			"require the same resolution to be refused and allowed",
		ErrIncompleteSuite,
	)
)

// Suite is a configured set of audits, ready to be run.
//
// Built by `New` rather than assembled as a struct literal, because half of what
// `New` does is **refuse**: a suite is a claim about what will be checked, and a
// literal would let a caller skip the checks it forgot to declare.
type Suite struct {
	cfg Config
	// house is the house-rule pair the version audit toggles: the same module, the
	// same declared position, `Enabled` differing. Both the fingerprint and the
	// resume decision are taken under each, and the two must agree — which is
	// ADR 0018 as an assertion rather than as a comment.
	house [2]HouseRule
}

// New returns a suite over one configured system, refusing a configuration that
// could not fail.
//
// The refusals are the point. See `ErrIncompleteSuite`: each of them is a missing
// seam, and a missing seam is not a smaller result, it is no result.
func New(cfg Config) (*Suite, error) {
	if cfg.System == nil {
		return nil, ErrNoSystem
	}

	if err := checkScenario(cfg); err != nil {
		return nil, err
	}

	if len(cfg.GMOnly) == 0 {
		return nil, ErrNoGMOnlyOps
	}

	if cfg.Classify == nil {
		return nil, ErrNoClassify
	}

	if cfg.Resume == nil {
		return nil, ErrNoResume
	}

	for _, operation := range cfg.GMOnly {
		if !operation.Valid() {
			return nil, fmt.Errorf(
				"%w: the GM-only operation %q is not a usable op token, so the role audit would "+
					"resolve something the wire could never carry",
				ErrIncompleteSuite,
				operation,
			)
		}

		if operation == cfg.Scenario.Intent.Op {
			return nil, fmt.Errorf("%w: %q", ErrGMOnlyScenario, operation)
		}
	}

	for _, kind := range cfg.System.ContentKinds() {
		if kind == SurplusKind {
			return nil, fmt.Errorf(
				"%w: the system declares %q, which is the kind the unknown-kind audit plants",
				ErrIncompleteSuite, kind,
			)
		}
	}

	suite := &Suite{cfg: cfg}

	// One module in two configurations, not two modules: ADR 0018's exclusion is
	// about the toggle, and a differing module id would be a different question.
	suite.house = [2]HouseRule{
		{ModuleID: "conformance", Position: 0, Enabled: false},
		{ModuleID: "conformance", Position: 0, Enabled: true},
	}

	return suite, nil
}

// checkScenario refuses a scenario the suite cannot resolve with.
func checkScenario(cfg Config) error {
	if len(cfg.Scenario.Objects) == 0 {
		return ErrNoScenario
	}

	carriesContent := false

	for _, object := range cfg.Scenario.Objects {
		if object.ID.Valid() && object.Kind.Valid() && len(object.Data) > 0 {
			carriesContent = true

			break
		}
	}

	if !carriesContent {
		return fmt.Errorf(
			"%w: no object carries content, so %q would be certified against an empty tabletop "+
				"without ever comparing a byte",
			ErrNoScenario, cfg.System.ID(),
		)
	}

	if !cfg.Scenario.Intent.Valid() {
		return fmt.Errorf("%w: the intent is %q", ErrNoScenario, cfg.Scenario.Intent)
	}

	if !cfg.Scenario.Call.Role.Valid() {
		return fmt.Errorf(
			"%w: the rule context carries role %q, which this build has no name for",
			ErrNoScenario, cfg.Scenario.Call.Role,
		)
	}

	return nil
}

// System returns the system under certification.
func (s *Suite) System() rules.System { return s.cfg.System }

// Run performs every audit and returns everything they objected to.
//
// It does not stop at the first finding. A plugin author who has broken three
// things should learn all three from one run rather than from three, and the cost
// is nil because every audit is cheap and every one of them is handed a value it
// does not keep.
func (s *Suite) Run(ctx context.Context) Result {
	result := Result{}

	for _, findings := range [][]Finding{
		s.Determinism(ctx),
		s.UnknownKind(ctx),
		s.Version(),
		s.Role(ctx),
		s.Containment(ctx),
	} {
		result.Findings = append(result.Findings, findings...)
	}

	return result
}

// Certify is `Run` turned into the single error a certification test asserts on.
func (s *Suite) Certify(ctx context.Context) error { return s.Run(ctx).Err() }

// Check configures a suite and runs it, and is the one call a system author's
// test needs.
//
// The shape is deliberate: there is no way to run a suite without passing through
// `New`, so the refusals cannot be skipped by not asking for a suite at all.
func Check(ctx context.Context, cfg Config) error {
	suite, err := New(cfg)
	if err != nil {
		return err
	}

	return suite.Certify(ctx)
}

// state builds a fresh snapshot of the scenario's tabletop, with the audit's own
// extra objects appended.
//
// Fresh per call rather than held, for the reason in `Scenario`: a resolver that
// scribbled on a shared snapshot would be comparing against its own previous
// output, which turns the containment audit into a tautology.
func (s *Suite) state(extra ...rules.Object) (rules.State, error) {
	objects := make([]rules.Object, 0, len(s.cfg.Scenario.Objects)+len(extra))
	objects = append(objects, s.cfg.Scenario.Objects...)
	objects = append(objects, extra...)

	state, err := rules.NewState(1, objects)
	if err != nil {
		return rules.State{}, fmt.Errorf(
			"conformance: the scenario's tabletop is not a state: %w",
			err,
		)
	}

	return state, nil
}

// asPlayer returns the scenario's rule context with the actor's role replaced.
//
// Only the role, and the function says so by taking nothing else.
func (s *Suite) asPlayer() rules.Context {
	call := s.cfg.Scenario.Call
	call.Role = domain.RolePlayer

	return call
}

// asGM returns the scenario's rule context with the actor's role replaced.
func (s *Suite) asGM() rules.Context {
	call := s.cfg.Scenario.Call
	call.Role = domain.RoleGM

	return call
}

// render lists mutations without their payloads.
//
// The op, the target, and the payload's length — never the payload itself.
// `rules.Mutation.String` already withholds it for the same reason; this adds the
// length, because two runs of the determinism audit that differ *only* in payload
// bytes are the case where "these look identical" would send an author looking in
// the wrong place.
func render(mutations []rules.Mutation) string {
	if len(mutations) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(mutations))

	for _, mutation := range mutations {
		shape := mutation.String() + " args=" + strconv.Itoa(len(mutation.Args)) + "b"

		if mutation.Remove {
			shape += " removed"
		}

		parts = append(parts, shape)
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

// firstObject returns the first object the author declared, which the role audit
// uses as the target for the intents it builds itself.
//
// **Declared order, not id order.** `rules.NewState` sorts by id before the suite
// ever sees the state, so "the first object" would otherwise mean something
// different here than in the finding an author reads about the object the audit
// addressed. `checkScenario` has already guaranteed there is one.
func (s *Suite) firstObject() rules.Object { return s.cfg.Scenario.Objects[0] }
