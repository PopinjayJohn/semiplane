package conformance_test

import (
	"errors"
	"fmt"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
)

// deployment is the ruleset seam: what this process would persist, and whether a
// campaign written under something else may resume.
//
// A struct whose fields are the four ways to get §10.8's row wrong, because the audit
// cannot be shown to catch something by inspecting it — it can only be shown to catch
// something by being run against a deployment that is wrong in one specific way. This
// one is wrong in four, and the table in `conformance_test.go` takes each in turn.
//
// The encoding is deliberately not `realtime`'s. It has to be *readable as plausible*,
// because the whole point of the drift fixture is that the suite asks the deployment's
// own encoder for a different version rather than inventing one; a hand-written prefix
// that `realtime.ParseFingerprint` would reject would make the audit a test of this
// file's string handling.
type deployment struct {
	// houseRulesInFingerprint hashes the enabled house rules into the fingerprint,
	// which is exactly what ADR 0018 forbids.
	houseRulesInFingerprint bool

	// oneSentinel answers a genuine mismatch and an unparseable column with the same
	// error, so the two failures cannot be told apart.
	oneSentinel bool

	// permits answers everything, which is §10.8's "silently misresolve".
	permits bool

	// terse answers with a refusal that names neither version.
	terse bool

	// hidesExpected leaves `Verdict.Expected` empty, which is the other way the same
	// sentence gets truncated.
	hidesExpected bool
}

// The two refusals a deployment can answer with, kept distinct unless the fixture is
// built to conflate them.
var (
	errFixtureDrift      = errors.New("fixture: the state was written under another ruleset")
	errFixtureUnreadable = errors.New("fixture: that ruleset_version is not comparable")

	// errFixtureConflated is the *one* refusal a deployment with a single refusal value
	// returns for both failures. One `var errRefused = errors.New("cannot resume")`
	// handed back for a genuine mismatch and for an unparseable column is a real bug
	// shape, and it is the one the audit is built to catch: a handler branches with
	// `errors.Is` and cannot tell the two apart.
	errFixtureConflated = errors.New("fixture: cannot resume")
)

// errFor picks the refusal for one kind of failure, conflating when asked to.
//
// The two distinct sentinels exist so the audit's "these must be tellable apart" check
// is testing a real distinction rather than two errors that happen to have different
// text: `errors.Is` in either direction, and their roots, are genuinely different.
func errFor(kind string, conflated bool) error {
	switch {
	case conflated:
		return errFixtureConflated
	case kind == "unreadable":
		return errFixtureUnreadable
	default:
		return errFixtureDrift
	}
}

// fingerprintPrefix is this fixture's format marker, standing in for `realtime`'s
// `sp1:`. A fixture that reused the real one would be asserting that the suite knows
// the real encoding, which is the thing it must not know.
const fingerprintPrefix = "fixture:"

// Fingerprint is what a campaign opened now would be written under.
func (d deployment) Fingerprint(system rules.System, house []conformance.HouseRule) string {
	encoded := fingerprintPrefix +
		"system=" + system.ID().String() +
		";ruleset=" + system.RulesetVersion()

	if d.houseRulesInFingerprint {
		// Names the enabled modules, so toggling one moves it — which is precisely the
		// failure a GM enabling a rule at the table would hit.
		enabled := make([]string, 0, len(house))

		for _, rule := range house {
			if rule.Enabled {
				enabled = append(enabled, rule.ModuleID)
			}
		}

		encoded += ";house=" + strings.Join(enabled, ",")
	}

	return encoded
}

// Check answers whether a campaign written under persisted may resume.
func (d deployment) Check(
	system rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	expected := d.Fingerprint(system, house)
	verdict := conformance.Verdict{Persisted: persisted, Expected: expected}

	if d.hidesExpected {
		verdict.Expected = ""
	}

	if persisted == expected {
		return verdict
	}

	if d.permits {
		return verdict
	}

	if !strings.HasPrefix(persisted, fingerprintPrefix) {
		verdict.Err = fmt.Errorf(
			"%w: %q does not begin with %q",
			errFor("unreadable", d.oneSentinel),
			persisted,
			fingerprintPrefix,
		)

		return verdict
	}

	if d.terse {
		verdict.Err = fmt.Errorf("%w: incompatible", errFor("drift", d.oneSentinel))

		return verdict
	}

	if d.oneSentinel {
		// The refusal a handler cannot route: one value, two very different fates.
		verdict.Err = errFixtureConflated

		return verdict
	}

	verdict.Err = fmt.Errorf(
		"%w: the state was written under %q and this build resolves %q", errFixtureDrift,
		persisted, expected,
	)

	return verdict
}

var _ conformance.Resume = deployment{}
