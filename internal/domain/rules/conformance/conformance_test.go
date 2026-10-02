package conformance_test

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
)

// runners is every audit, addressed by name.
//
// **A map rather than a switch**, so that `TestEveryAuditRejectsTheViolationItClaimsTo`
// cannot silently stop exercising one: a `switch` with a `default` that fails would
// catch an audit added to `conformance.Audits` but not one whose runner was forgotten,
// and the reverse — a runner for an audit that no longer exists — is caught by
// `TestEveryAuditHasARunner`.
var runners = map[conformance.Audit]func(*testing.T, *conformance.Suite) []conformance.Finding{
	conformance.AuditDeterminism: func(t *testing.T, suite *conformance.Suite) []conformance.Finding {
		return suite.Determinism(t.Context())
	},
	conformance.AuditUnknownKind: func(t *testing.T, suite *conformance.Suite) []conformance.Finding {
		return suite.UnknownKind(t.Context())
	},
	conformance.AuditVersion: func(t *testing.T, suite *conformance.Suite) []conformance.Finding {
		return suite.Version()
	},
	conformance.AuditRole: func(t *testing.T, suite *conformance.Suite) []conformance.Finding {
		return suite.Role(t.Context())
	},
	conformance.AuditContainment: func(t *testing.T, suite *conformance.Suite) []conformance.Finding {
		return suite.Containment(t.Context())
	},
}

// violation is one audit, the system that breaks exactly that audit, and the seam that
// has to be broken as well when the break is not in the system.
//
// The `resume` field exists because the version audit is about a *deployment*, not
// about a system: a system cannot answer §10.8's row, and a table whose version row
// only broke `stub` would be a table asserting that the audit can fail on something it
// cannot see.
type violation struct {
	// audit is the audit the break is aimed at.
	audit conformance.Audit

	// name says what is broken, in the failure's own vocabulary.
	name string

	// breakSystem breaks the system under certification.
	breakSystem func(system *stub)

	// breakResume breaks the ruleset seam, or leaves it alone.
	breakResume func(*deployment)

	// breakClassify breaks the error-to-wire-word map, or leaves it alone.
	breakClassify func(*classifyFixture)
}

// classifyFixture is `classify` with the one failure an adapter can have that matters
// here: a refusal that becomes its own sentence.
type classifyFixture struct {
	inner func(error) string

	// verbatim answers every refusal with the system's own text, which is what an
	// adapter that did not translate would do.
	verbatim bool

	// oracle answers a role refusal as `unknown_op`, which is the wrong word for the
	// right reason: `realtime.Core` orders these two deliberately, because `unknown_op`
	// answers "is anything allowed" about the server rather than "is this allowed" about
	// the player, and on the wire that is a role oracle the whole table can read.
	oracle bool
}

func (c classifyFixture) classify(refusal error) string {
	switch {
	case c.verbatim:
		return refusal.Error()
	case c.oracle && errors.Is(refusal, errStubGMOnly):
		return conformance.ReasonUnknownOp
	default:
		return c.inner(refusal)
	}
}

// violations is the table: one row per audit, and the reason a row exists is that
// without it the audit is unfalsifiable.
var violations = []violation{
	{
		audit:       conformance.AuditDeterminism,
		name:        "a resolver carrying its own random source",
		breakSystem: func(system *stub) { system.shared = rand.New(rand.NewPCG(1, 2)) },
	},
	{
		audit: conformance.AuditUnknownKind,
		name:  "a resolver that treats a stale kind as fatal",
		// §10.8's last row says the opposite: an unknown kind is inert, so a plugin
		// removal must not fail resolutions that have nothing to do with it.
		breakSystem: func(system *stub) { system.fatal = true },
	},
	{
		audit:       conformance.AuditUnknownKind,
		name:        "a resolver that changes an object of a kind it does not declare",
		breakSystem: func(system *stub) { system.touchSurplus = true },
	},
	{
		audit: conformance.AuditVersion,
		name:  "a deployment that resumes a campaign written under another version",
		// §10.8: refuse rather than silently misresolve.
		breakResume: func(d *deployment) { d.permits = true },
	},
	{
		audit:       conformance.AuditVersion,
		name:        "a deployment that answers an unreadable column as drift",
		breakResume: func(d *deployment) { d.oneSentinel = true },
	},
	{
		audit:       conformance.AuditVersion,
		name:        "a refusal that names neither version",
		breakResume: func(d *deployment) { d.terse = true },
	},
	{
		audit:       conformance.AuditVersion,
		name:        "a verdict that does not report the version it would resolve under",
		breakResume: func(d *deployment) { d.hidesExpected = true },
	},
	{
		audit: conformance.AuditVersion,
		name:  "a fingerprint that moves when a house rule is enabled",
		// ADR 0018: the fingerprint names resolution semantics, not configuration.
		breakResume: func(d *deployment) { d.houseRulesInFingerprint = true },
	},
	{
		audit: conformance.AuditRole,
		name:  "a resolver that does not check the actor's role",
		// §7.2: the role decides.
		breakSystem: func(system *stub) { system.ignoreRole = true },
	},
	{
		audit: conformance.AuditRole,
		name:  "a resolver that refuses its GM-only operation to the GM as well",
		// The other direction: a system nobody at the table can use is not enforcing a
		// role either.
		breakSystem: func(system *stub) { system.denyGM = true },
	},
	{
		audit: conformance.AuditRole,
		name:  "a refusal that becomes its own sentence",
		// Not a system fault at all: the adapter is the composition root's, and an
		// adapter that hands a browser the system's own text is how whatever the system
		// choked on reaches a browser.
		breakClassify: func(c *classifyFixture) { c.verbatim = true },
	},
	{
		audit: conformance.AuditRole,
		name:  "a role refusal worded unknown_op",
		// The oracle case: the refusal is real and the system is right, but the word says
		// nothing is allowed rather than that this is not.
		breakClassify: func(c *classifyFixture) { c.oracle = true },
	},
	{
		audit:       conformance.AuditContainment,
		name:        "a resolver that reports failure and still says what changed",
		breakSystem: func(system *stub) { system.failWithMutation = true },
	},
}

// TestEveryAuditRejectsTheViolationItClaimsTo is the meta-test, and it is the reason
// the five audits can be trusted at all.
//
// Two halves per row, and **both are required**:
//
//   - The broken configuration produces at least one finding, *and every finding it
//     produces is that audit's*. Requiring the attribution is what stops a row from
//     passing because of an unrelated fault: a version fixture that happened to make
//     the role audit complain would satisfy a length check while proving nothing about
//     the version audit.
//   - The intact configuration produces **none**.
//
// The second half is the one that is easy to leave out and the one that does the work.
// An audit that objects to a well-behaved system is not strict, it is wrong, and
// without this column every row above would pass just as happily against a suite that
// rejected everything — which is the failure mode this repository has been bitten by
// three times, in `make a11y`.
func TestEveryAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range violations {
		t.Run(string(testCase.audit)+"/"+testCase.name, func(t *testing.T) {
			t.Parallel()

			runner, known := runners[testCase.audit]
			if !known {
				t.Fatalf("no runner is registered for audit %q", testCase.audit)
			}

			intact := suiteFor(t, wellFormed(), nil, nil)

			if got := runner(t, intact); len(got) != 0 {
				t.Fatalf("a well-formed system drew %d finding(s) from this audit, so every "+
					"violation below would pass for the wrong reason:\n%s",
					len(got), joinSummaries(got))
			}

			broken := suiteFor(
				t,
				brokenSystem(testCase),
				testCase.breakResume,
				testCase.breakClassify,
			)

			got := runner(t, broken)
			if len(got) == 0 {
				t.Fatalf("%s: the %q audit reported nothing for a system broken exactly that way, "+
					"so it cannot fail",
					testCase.name, testCase.audit)
			}

			for _, finding := range got {
				if finding.Audit != testCase.audit {
					t.Errorf("%s: the audit under test reported %q, which is a different audit; "+
						"this row would pass on somebody else's finding",
						testCase.name, finding.Audit)
				}
			}
		})
	}
}

// TestEveryAuditHasARunnerAndEveryRunnerHasAnAudit keeps the two tables agreeing.
//
// Two directions because they fail differently: an audit with no runner is an audit
// the meta-test never exercises, and a runner with no audit is a function nobody calls.
func TestEveryAuditHasARunnerAndEveryRunnerHasAnAudit(t *testing.T) {
	t.Parallel()

	for _, audit := range conformance.Audits() {
		if _, known := runners[audit]; !known {
			t.Errorf("audit %q has no runner, so TestEveryAuditRejectsTheViolationItClaimsTo "+
				"cannot exercise it", audit)
		}
	}

	for audit := range runners {
		if !slices.Contains(conformance.Audits(), audit) {
			t.Errorf("a runner exists for %q, which is not one of the audits", audit)
		}
	}
}

// TestTheAuditsAreTheFiveTheRecordNames is the count, so a sixth audit cannot be added
// without this noticing that the documented set and the running one have parted.
func TestTheAuditsAreTheFiveTheRecordNames(t *testing.T) {
	t.Parallel()

	want := []conformance.Audit{
		"determinism",
		"unknown-kind tolerance",
		"ruleset version refusal",
		"GM-only enforcement",
		"Apply panic containment",
	}

	if got := conformance.Audits(); !slices.Equal(got, want) {
		t.Errorf("the audits are\n %v\nwant\n %v", got, want)
	}
}

// TestTheWireReasonsAreTheEightsTheProtocolCarries mirrors `realtime`'s closed set,
// and is the one test here that *does* assert equality against a copy — because this
// package does not own that set and cannot import the one that does, a drift would
// otherwise be invisible. See the argument in `audit_role.go` for why the audit itself
// asserts membership rather than equality.
func TestTheWireReasonsAreTheEightsTheProtocolCarries(t *testing.T) {
	t.Parallel()

	want := []string{
		"not_your_turn", "not_permitted", "unknown_op", "invalid_args",
		"stale_version", "out_of_order", "no_such_placement", "server_error",
	}

	if got := conformance.WireReasons(); !slices.Equal(got, want) {
		t.Errorf("the wire reasons are\n %v\nwant\n %v", got, want)
	}
}

// TestTheSuiteFindsNothingForAWellFormedSystem is `Run` end to end: a system that obeys
// every rule certifies.
//
// Table-driven over both seams that could hide a fault — a system with none of the
// faults, and a deployment with none of its own — because a suite that passes only with
// one particular fixture pair is a suite nobody has run twice.
func TestTheSuiteFindsNothingForAWellFormedSystem(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		system *stub
		resume conformance.Resume
	}{
		{name: "the stub as written", system: wellFormed(), resume: deployment{}},
		{
			name: "a second well-formed system with a different vocabulary",
			system: func() *stub {
				other := wellFormed()
				other.id = "pathfinder"
				other.title = "Pathfinder"
				other.ruleset = "pathfinder@3"
				other.grammar.Notation = "2d6-pool"
				other.kinds = []rules.Kind{"spell", "feat", "ancestry"}

				return other
			}(),
			resume: deployment{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config(testCase.system)
			cfg.Resume = testCase.resume

			if err := conformance.Check(t.Context(), cfg); err != nil {
				t.Errorf("a well-formed system was not certified: %v", err)
			}
		})
	}
}

// brokenSystem applies a violation's system half, or returns the intact one.
func brokenSystem(testCase violation) *stub {
	system := wellFormed()

	if testCase.breakSystem != nil {
		testCase.breakSystem(system)
	}

	return system
}

// suiteFor configures a suite, applying whichever halves of a violation the row
// supplies.
func suiteFor(
	t *testing.T,
	system *stub,
	breakResume func(*deployment),
	breakClassify func(*classifyFixture),
) *conformance.Suite {
	t.Helper()

	deploy := deployment{}
	if breakResume != nil {
		breakResume(&deploy)
	}

	words := classifyFixture{inner: classify}
	if breakClassify != nil {
		breakClassify(&words)
	}

	cfg := config(system)
	cfg.Resume = deploy
	cfg.Classify = words.classify

	return mustSuite(t, cfg)
}

// joinSummaries renders findings for a failure message.
func joinSummaries(found []conformance.Finding) string {
	var out strings.Builder

	for _, finding := range found {
		out.WriteString("\n  ")
		out.WriteString(finding.String())
	}

	return out.String()
}
