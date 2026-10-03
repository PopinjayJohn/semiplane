package conformance_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
)

// TestTheSuiteRefusesAConfigurationItCannotAudit is the reason `New` refuses anything
// at all.
//
// Each row removes one seam from a complete configuration and requires the refusal to
// name that seam — through `errors.Is` on its own sentinel **and** through the
// umbrella, because an author who wants to ask "did I wire it?" should not have to
// enumerate the four ways they did not.
//
// The empty-GM-only row is the one that matters most and the one a suite would
// naturally wave through: a list of reserved operations with nothing in it makes the
// role audit vacuously true, and vacuously true is how a gate gets trusted.
func TestTheSuiteRefusesAConfigurationItCannotAudit(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		break_  func(*conformance.Config)
		wantErr error
	}{
		{
			name:    "no system",
			break_:  func(cfg *conformance.Config) { cfg.System = nil },
			wantErr: conformance.ErrNoSystem,
		},
		{
			name:    "no tabletop",
			break_:  func(cfg *conformance.Config) { cfg.Scenario.Objects = nil },
			wantErr: conformance.ErrNoScenario,
		},
		{
			name: "a tabletop whose objects carry no content",
			break_: func(cfg *conformance.Config) {
				cfg.Scenario.Objects = []rules.Object{{ID: "p1", Kind: rules.KindToken}}
			},
			wantErr: conformance.ErrNoScenario,
		},
		{
			name: "an intent the wire would never have produced",
			break_: func(cfg *conformance.Config) {
				cfg.Scenario.Intent = rules.Intent{}
			},
			wantErr: conformance.ErrNoScenario,
		},
		{
			name: "a rule context with a role this build has no name for",
			break_: func(cfg *conformance.Config) {
				cfg.Scenario.Call.Role = domain.Role("editor")
			},
			wantErr: conformance.ErrNoScenario,
		},
		{
			name:    "no GM-only operation, which would make the role audit vacuous",
			break_:  func(cfg *conformance.Config) { cfg.GMOnly = nil },
			wantErr: conformance.ErrNoGMOnlyOps,
		},
		{
			name: "a GM-only operation the wire could not carry",
			break_: func(cfg *conformance.Config) {
				cfg.GMOnly = []rules.Op{"Set HP"}
			},
			wantErr: conformance.ErrIncompleteSuite,
		},
		{
			name: "a scenario whose own operation is also declared GM-only",
			break_: func(cfg *conformance.Config) {
				cfg.GMOnly = []rules.Op{cfg.Scenario.Intent.Op}
			},
			wantErr: conformance.ErrGMOnlyScenario,
		},
		{
			name:    "no classifier",
			break_:  func(cfg *conformance.Config) { cfg.Classify = nil },
			wantErr: conformance.ErrNoClassify,
		},
		{
			name:    "no ruleset decision",
			break_:  func(cfg *conformance.Config) { cfg.Resume = nil },
			wantErr: conformance.ErrNoResume,
		},
		{
			name: "a system that declares the kind the unknown-kind audit plants",
			break_: func(cfg *conformance.Config) {
				declaring := wellFormed()
				declaring.kinds = append(declaring.kinds, conformance.SurplusKind)
				cfg.System = declaring
			},
			wantErr: conformance.ErrIncompleteSuite,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config(wellFormed())
			testCase.break_(&cfg)

			_, err := conformance.New(cfg)
			if err == nil {
				t.Fatalf("the suite accepted %s, and it will find nothing", testCase.name)
			}

			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("want %v, got %v", testCase.wantErr, err)
			}

			if !errors.Is(err, conformance.ErrIncompleteSuite) {
				t.Error("the refusal does not satisfy the umbrella an author asks with errors.Is")
			}
		})
	}
}

// TestTheSuiteRefusesRatherThanPassesAnEmptyCertification is the same refusal observed
// through the entry point an author actually calls.
//
// `Check` has to refuse rather than certify, because a `nil` from `Check` is read as
// "certified" and there is no other place for a reader to look.
func TestTheSuiteRefusesRatherThanPassesAnEmptyCertification(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.GMOnly = nil

	if err := conformance.Check(t.Context(), cfg); !errors.Is(err, conformance.ErrNoGMOnlyOps) {
		t.Fatalf("an uncertifiable configuration certified: %v", err)
	}
}

// TestAnAuditThatCannotSeeItsOwnSubjectIsRefused is the same principle one level in:
// the GM-only declaration and the scenario's operation may not be the same thing,
// because the role audit would then require one resolution to be both refused and
// allowed, and an audit that requires two contradictory things fails on any system at
// all.
func TestAnAuditThatCannotSeeItsOwnSubjectIsRefused(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.GMOnly = []rules.Op{"set_hp", cfg.Scenario.Intent.Op}

	if _, err := conformance.New(cfg); !errors.Is(err, conformance.ErrGMOnlyScenario) {
		t.Fatalf("a contradictory role declaration was accepted: %v", err)
	}
}

// TestTheDeterminismAuditObjectsToAScenarioItCannotResolve is the first tooth on its
// own, and the finding it produces is about the *scenario* rather than about the
// system.
//
// A system that refuses every intent has not been shown to be deterministic; it has
// been shown to be unreachable, and an audit that reported success would be reporting
// that. The scenario here names an object that is not on the table, which is a mistake
// in the configuration rather than in the system — and the finding has to say so,
// because a plugin author reading "the resolver is nondeterministic" about a resolver
// that answered nothing would go looking in the wrong place.
func TestTheDeterminismAuditObjectsToAScenarioItCannotResolve(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Scenario.Intent = rules.Intent{Op: "move_token", Target: "absent", Args: nil}

	suite := mustSuite(t, cfg)

	got := suite.Determinism(t.Context())
	if !mentions(got, "refused") {
		t.Fatalf("a scenario the resolver refuses was not objected to:\n%s", joinSummaries(got))
	}

	if !mentions(got, "nothing to compare") {
		t.Errorf("the finding does not say there was nothing to compare:\n%s", joinSummaries(got))
	}
}

// emptyResolution is a system that answers every intent with no mutations and no
// error.
//
// The fixture for the determinism audit's second tooth. It is a legal `rules.System`
// — `rules.go` says returning no mutations with no error is a legitimate answer — and
// it is precisely the system the audit must refuse to certify.
type emptyResolution struct{ rules.System }

// Apply changes nothing and says nothing.
func (emptyResolution) Apply(
	_ context.Context,
	_ rules.Context,
	_ rules.State,
	_ rules.Intent,
) ([]rules.Mutation, error) {
	return nil, nil
}

func TestTheDeterminismAuditObjectsToAnEmptyResolution(t *testing.T) {
	t.Parallel()

	cfg := config(emptyResolution{System: wellFormed()})

	suite := mustSuite(t, cfg)

	got := suite.Determinism(t.Context())
	if len(got) == 0 {
		t.Fatal(
			"a resolution that changes nothing was certified as deterministic; an answer that " +
				"cannot vary is reproducible whether or not the resolver is",
		)
	}

	if !mentions(got, "no mutation") {
		t.Errorf("the finding does not say the resolution changed nothing:\n%s", joinSummaries(got))
	}
}

// TestTheDeterminismAuditCatchesAResolverCarryingItsOwnRandomSource checks the first
// tooth from the other side: a resolver that is reproducible for exactly two calls and
// wrong for every call after.
//
// The number is what makes it interesting, and `Runs` is the number S-14.6 names. A
// determinism audit that ran the scenario twice would pass this system.
func TestTheDeterminismAuditCatchesAResolverCarryingItsOwnRandomSource(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	system.shared = rand.New(rand.NewPCG(7, 11))

	suite := mustSuite(t, config(system))

	got := suite.Determinism(t.Context())
	if len(got) == 0 {
		t.Fatalf("a resolver carrying its own random source passed %d runs", conformance.Runs)
	}

	if !mentions(got, "differently from run 0") {
		t.Errorf("the finding does not name the divergence:\n%s", joinSummaries(got))
	}
}

// TestTheRoleAuditRefusesAResolverThatAnswersNotPermittedToEverything is the role
// audit's second claim on its own.
//
// §7.2's rule is not "refuse players", it is "reserve *these* operations", and a suite
// that only ever checked the GM-only list would not notice a system that refused
// everything. The fixture refuses every operation as `not_permitted`, which is a system
// that enforces no role at all because it refuses play.
func TestTheRoleAuditRefusesAResolverThatAnswersNotPermittedToEverything(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	system.refuseEverything = true

	suite := mustSuite(t, config(system))

	got := suite.Role(t.Context())
	if !mentions(got, "not declared GM-only") {
		t.Fatalf("a resolver answering not_permitted to every operation passed the role audit:\n%s",
			joinSummaries(got))
	}
}

// TestTheRoleAuditRefusesARefusalOutsideTheWireVocabulary is the third claim on its own,
// and it is a finding about the **adapter** rather than about the system.
func TestTheRoleAuditRefusesARefusalOutsideTheWireVocabulary(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Classify = func(refusal error) string { return refusal.Error() }

	suite := mustSuite(t, cfg)

	got := suite.Role(t.Context())
	if len(got) == 0 {
		t.Fatal("an adapter that hands a client the system's own sentence passed the role audit")
	}

	if !mentions(got, "not one of the wire's reasons") {
		t.Errorf("the finding does not name what is wrong:\n%s", joinSummaries(got))
	}
}

// TestTheVersionAuditRefusesADeploymentThatReportsNoFingerprint is the audit's first
// claim, and the fixture is a seam with nothing to say.
//
// Without this the audit's second claim could pass against a deployment that reports
// the same string for every version — `theirs == mine` — and the refusal for that is a
// distinct finding, which is the point of having one.
func TestTheVersionAuditRefusesADeploymentThatReportsNoFingerprint(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Resume = silentDeployment{}

	suite := mustSuite(t, cfg)

	got := suite.Version()
	if len(got) == 0 {
		t.Fatal("a deployment reporting no fingerprint passed the version audit")
	}
}

// TestTheVersionAuditRefusesADriftFixtureThatIsNotAFingerprint is the claim that keeps
// the drift row honest: the suite asks the deployment's own encoder for a *different
// version*, and a deployment whose encoder cannot tell two versions apart cannot be
// audited at all.
func TestTheVersionAuditRefusesADriftFixtureThatIsNotAFingerprint(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Resume = flatDeployment{}

	suite := mustSuite(t, cfg)

	got := suite.Version()
	if len(got) == 0 {
		t.Fatal("a deployment that fingerprints every version identically passed the version audit")
	}

	if !mentions(got, "no drift case") {
		t.Errorf(
			"the finding does not say the drift case could not be built:\n%s",
			joinSummaries(got),
		)
	}
}

// TestTheVersionAuditDistinguishesDriftFromAnUnreadableColumn is §10.8's row against
// ADR 0018's cost, on its own, and the fixture is one sentinel for both.
func TestTheVersionAuditDistinguishesDriftFromAnUnreadableColumn(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Resume = deployment{oneSentinel: true}

	suite := mustSuite(t, cfg)

	got := suite.Version()
	if !mentions(got, "cannot tell from") {
		t.Fatalf("one refusal for both failures was not objected to:\n%s", joinSummaries(got))
	}
}

// TestADriftRefusalMustNameBothVersions checks the audit's third claim against a
// deployment that answers "incompatible", and against one that leaves `Expected` empty.
func TestADriftRefusalMustNameBothVersions(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		resume conformance.Resume
	}{
		{name: "a refusal that says only incompatible", resume: deployment{terse: true}},
		{name: "a verdict that does not report the expected version", resume: deployment{hidesExpected: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			suite := mustSuite(t, config2(wellFormed(), testCase.resume))

			if got := suite.Version(); !mentions(got, "neither") {
				t.Errorf("a refusal that named no version was accepted:\n%s", joinSummaries(got))
			}
		})
	}
}

// TestAHouseRuleMustNotStrandACampaign is ADR 0018 as a single assertion, with the
// fingerprint that hashes the enabled set.
func TestAHouseRuleMustNotStrandACampaign(t *testing.T) {
	t.Parallel()

	suite := mustSuite(t, config2(wellFormed(), deployment{houseRulesInFingerprint: true}))

	got := suite.Version()
	if !mentions(got, "ADR 0018") {
		t.Fatalf("a fingerprint that moves with a house rule was accepted:\n%s", joinSummaries(got))
	}

	if !mentions(got, "strands") && !mentions(got, "refused once a house rule was enabled") {
		t.Errorf("the findings do not say the campaign was refused:\n%s", joinSummaries(got))
	}
}

// TestAFindingCarriesNoMutationPayload is the S-12.3 half of the package's contract,
// asserted rather than promised.
//
// Two resolutions are compared, one whose payload is a number and one whose payload is
// 200 bytes of distinct filler, and the rendered form is required to show only their
// **lengths**. A rendering that printed the payload would put a resolved roll in a CI
// log, and no amount of documentation stops that happening by accident.
func TestAFindingCarriesNoMutationPayload(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("secret-payload-", 16)

	one, err := rules.NewMutation("p1", "roll", []byte("17"))
	if err != nil {
		t.Fatalf("building a mutation: %v", err)
	}

	two, err := rules.NewMutation("p1", "roll", []byte(secret))
	if err != nil {
		t.Fatalf("building a mutation: %v", err)
	}

	// The rendering is reached through findings, because a finding is the only place
	// it is ever used — and the fixture fails its resolution *and* says what changed,
	// which is the one combination that makes a suite print the mutations it rejected.
	report := func(mutations []rules.Mutation) string {
		cfg := config(wellFormed())
		cfg.System = residue{System: wellFormed(), residues: mutations}

		suite := mustSuite(t, cfg)

		found := suite.Run(t.Context()).Findings
		if len(found) == 0 {
			t.Fatal(
				"a resolver that reported failure and returned mutations produced no finding, " +
					"so nothing was rendered",
			)
		}

		return joinSummaries(found)
	}

	if strings.Contains(report([]rules.Mutation{one, two}), secret) {
		t.Fatal("a finding printed a mutation's payload")
	}

	if strings.Contains(report([]rules.Mutation{one, two}), "17") {
		t.Error("a finding printed a mutation's payload, which is where a resolved roll lives")
	}
}

// residue is a system that returns a fixed list whatever it is asked, so the audit's
// rendering is reachable without arranging for a divergence.
type residue struct {
	rules.System

	residues []rules.Mutation
}

func (r residue) Apply(
	_ context.Context,
	_ rules.Context,
	_ rules.State,
	_ rules.Intent,
) ([]rules.Mutation, error) {
	return r.residues, errors.New("residue: giving up after saying what changed")
}

// TestTheFingerprintNamesEveryObjectAndItsContent is the containment audit's first
// tooth's instrument.
//
// If the fingerprint did not read the objects, "the state is byte-identical" would be
// unfalsifiable — comparing two renderings of nothing is how an audit passes while
// watching a resolver corrupt every object on the table.
func TestTheFingerprintNamesEveryObjectAndItsContent(t *testing.T) {
	t.Parallel()

	state, err := rules.NewState(4, []rules.Object{
		{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)},
		{ID: "s1", Kind: rules.KindScene, Data: []byte(`{"fog":"dark"}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	fingerprint := conformance.Fingerprint(state)

	for _, want := range []string{"revision 4", "2 objects", "p1", "s1", "token", "scene", "hp", "fog"} {
		if !strings.Contains(fingerprint, want) {
			t.Errorf("the fingerprint does not mention %q:\n%s", want, fingerprint)
		}
	}

	changed, err := rules.NewState(4, []rules.Object{
		{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":9}`)},
		{ID: "s1", Kind: rules.KindScene, Data: []byte(`{"fog":"dark"}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	if conformance.Fingerprint(changed) == fingerprint {
		t.Error(
			"a state with different content produced the same fingerprint, so the containment " +
				"audit cannot see a resolver change anything",
		)
	}
}

// config2 is `config` with the ruleset seam replaced, for the version tables.
func config2(system rules.System, resume conformance.Resume) conformance.Config {
	cfg := config(system)
	cfg.Resume = resume

	return cfg
}

// silentDeployment is a ruleset seam that reports nothing at all.
type silentDeployment struct{}

func (silentDeployment) Fingerprint(rules.System, []conformance.HouseRule) string { return "" }

func (silentDeployment) Check(
	rules.System,
	[]conformance.HouseRule,
	string,
) conformance.Verdict {
	return conformance.Verdict{}
}

// flatDeployment is a ruleset seam that fingerprints every version the same way, which
// is a deployment that cannot detect drift at all.
type flatDeployment struct{}

func (flatDeployment) Fingerprint(rules.System, []conformance.HouseRule) string {
	return fingerprintPrefix + "system=5e-2024;ruleset=5e-2024@1"
}

func (f flatDeployment) Check(
	_ rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	if persisted == f.Fingerprint(nil, house) {
		return conformance.Verdict{Persisted: persisted, Expected: persisted}
	}

	return conformance.Verdict{Persisted: persisted, Expected: persisted}
}

// mentions reports whether any finding's summary contains a phrase.
func mentions(found []conformance.Finding, phrase string) bool {
	for _, finding := range found {
		if strings.Contains(finding.Summary, phrase) {
			return true
		}
	}

	return false
}

// TestTheDeterminismAuditComparesOneHundredRuns holds S-14.6's *number*, and it is a
// separate test because the number is a separate claim from the property.
//
// The fixture agrees for its first two resolutions and diverges on the third — which is
// the shape of the failure the number exists to catch, and the reason a fixture that
// diverged immediately would not do. A resolver carrying its own `*rand.Rand` diverges
// on the second call, so an audit comparing two runs would catch *it*; this one is
// reproducible for exactly as long as an unlucky fixture happened to be, and only a
// third call exposes it.
//
// The constant is asserted alongside, because a test that only exercised the property
// would not notice someone lowering `Runs` to two **and** keeping this fixture — the
// fixture needs the loop to run three times, and the constant says how many times the
// loop is allowed to.
func TestTheDeterminismAuditComparesOneHundredRuns(t *testing.T) {
	t.Parallel()

	if conformance.Runs != 100 {
		t.Errorf("the suite resolves %d times; S-14.6 names one hundred, and two runs would agree "+
			"by luck for any resolver that diverges on its third", conformance.Runs)
	}

	system := wellFormed()
	system.agreeFor = 2

	suite := mustSuite(t, config(system))

	got := suite.Determinism(t.Context())
	if len(got) == 0 {
		t.Fatalf(
			"a resolver that agreed for two resolutions and diverged on the third passed %d runs",
			conformance.Runs,
		)
	}

	if !mentions(got, "run 2 of 100") {
		t.Errorf("the finding does not name the run that diverged:\n%s", joinSummaries(got))
	}
}

// TestTheRoleAuditRefusesARoleOracleWording is §7.2's wire requirement on its own.
//
// The refusal is real and the system is right; the word is what makes it a role oracle
// anybody on the campaign can read. Nothing else in the suite distinguishes
// `not_permitted` from `unknown_op`, so without this row the requirement is a comment.
func TestTheRoleAuditRefusesARoleOracleWording(t *testing.T) {
	t.Parallel()

	cfg := config(wellFormed())
	cfg.Classify = classifyFixture{inner: classify, oracle: true}.classify

	suite := mustSuite(t, cfg)

	got := suite.Role(t.Context())
	if !mentions(got, "role oracle") {
		t.Fatalf("a role refusal worded unknown_op passed the role audit:\n%s", joinSummaries(got))
	}
}

// TestAScribblingResolverChangesNothingItWasHanded is the containment audit's first
// tooth on its own, and it is a **guard on another package**.
//
// `Apply` returns its changes, the state arrives by value, and `rules.State` keeps its
// fields unexported with every accessor handing out a copy — so a resolver that writes to
// every object it reads corrupts its own copy of nothing. That is S-10.2's "a plugin
// therefore cannot write `campaign_state`", and today it holds because of a *type*, which
// is the strongest kind of guarantee available. The cost of a type-level guarantee is
// that it can be undone by an edit to the type, and nothing in this work item's tests
// would notice.
//
// So this test notices: build the state, resolve with a resolver that scribbles, compare
// the fingerprint. Verified by mutation against `internal/domain/rules/state.go` —
// removing the `Data` clone from `cloneObject` turns this red and nothing else. That
// mutation is applied, run and reverted, because `rules` is not this work item's file.
func TestAScribblingResolverChangesNothingItWasHanded(t *testing.T) {
	t.Parallel()

	objects := []rules.Object{
		{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)},
		{ID: "s1", Kind: rules.KindScene, Data: []byte(`{"fog":"dark"}`)},
	}

	state, err := rules.NewState(7, objects)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	cfg := config(scribbler{System: wellFormed()})
	cfg.Scenario.Objects = objects

	suite := mustSuite(t, cfg)

	before := conformance.Fingerprint(state)

	if got := suite.Containment(t.Context()); len(got) > 0 {
		t.Fatalf(
			"a resolver writing to every object it read drew findings, which means the state it "+
				"was handed did change:\n%s",
			joinSummaries(got),
		)
	}

	if after := conformance.Fingerprint(state); after != before {
		t.Fatalf("a scribbling resolver changed the state it was handed:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}

	// The caller's own collection, which is the other half: `NewState` copies, so writing
	// through the snapshot cannot reach the objects the caller still holds.
	if string(objects[0].Data) != `{"hp":10}` || string(objects[1].Data) != `{"fog":"dark"}` {
		t.Errorf("the caller's own objects changed: %q %q", objects[0].Data, objects[1].Data)
	}
}
