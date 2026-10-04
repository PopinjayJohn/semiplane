// P12 H6, the second claim: **a campaign whose persisted state was written under
// another ruleset is refused rather than quietly re-resolved, and the reason is
// distinguishable.**
//
// # What §10.8's row says, and what is left of it here
//
// "A `ruleset_version` that differs from the persisted state means **refuse to resume
// rather than silently misresolve**." ADR 0018 is the record, and its whole claim is
// that the fingerprint names *resolution semantics* — a plugin release, a data pack
// revision — and not the campaign's house-rule configuration, because toggling a house
// rule changes outcomes and not the meaning of a stored mutation.
//
// The refusal itself is `realtime.Gate`'s, and `internal/realtime` is not this work
// item's. **What is reachable from `internal/domain` is the other half: the components
// a fingerprint is made of, and whether a change to any one of them is visible at all.**
// That is the half that can be silently wrong. A fingerprint that compared only the
// system id and the ruleset version would refuse a campaign whose *base pack* moved —
// which is a pack revision changing what `attack` means, exactly the case ADR 0018
// exists for, and it would be missed by a comparison that looked like it was checking
// everything.
//
// So this file asserts, per system:
////
//   - **every component, moved alone, is refused** — four components, four refusals;
//   - **the refusal names which component differs**, because "the ruleset differs"
//     leaves a GM guessing between three things they could each fix differently;
//   - **the refusals are pairwise distinguishable** to a handler routing with
//     `errors.Is` *and* at the root, and distinguishable from a column this build cannot
//     parse at all — telling a GM to discard a game over an unparseable string is the
//     software inflicting the loss ADR 0018 exists to prevent;
//   - **an unchanged fingerprint still resumes**, because a gate that refuses everything
//     passes every other row on this list;
//   - **a house rule does not move the fingerprint**, so enabling one at the table is
//     not a reason a GM is refused on their own campaign; and
//   - **a deployment that resolves instead of refusing is caught by the published
//     suite**, through `conformance.Suite.Version()` rather than through this file's own
//     fixture — which is what makes the first six rows a claim about the product's own
//     certification rather than about a test's.
//
// # The fixture, and the third reason this cannot reach
//
// `rulesetGate` is a `conformance.Resume`: a ruleset decision a deployment owns. **Its
// encoding is this file's own** and deliberately not `realtime`'s — the suite cannot
// know the persisted format (`versioned.go` says so in as many words), and this file
// has no business depending on it either. The components are read from the system's own
// declarations, which is the part that is real.
//
// **§10.8's table has three rows and this file reaches two of them.** An unknown
// `system_id` — S-14.8, the refusal that names the id and serves the wiki anyway — is
// not expressible through `conformance.Resume` at all, because `Fingerprint` takes a
// live `rules.System`: a deployment can be asked what *this* system fingerprints as and
// never what a system it does not have would have fingerprinted as. There is no hook
// here to add and this work item may not add one, so what the test does instead is
// require the **system component's refusal to be distinguishable from the others by
// name** — which is the part of "the plugin is gone" the domain half can carry — and
// the gap is reported rather than papered over.

package rules_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/notfive"
)

// # The fixture

// component is one input of a fingerprint, as a name a person can act on.
//
// **Four values and no more**, and the count is the claim: `realtime.Fingerprint` has
// four fields and a system that contributes fewer cannot be told apart from one that
// has not been upgraded. A fifth would be a field `realtime` does not encode, silently
// dropped by the very gate meant to notice drift.
type component string

const (
	componentSystem  component = "the system"
	componentRuleset component = "the ruleset"
	componentBase    component = "the base pack"
	componentOverlay component = "the overlay pack"
)

// componentOrder is the order they are compared and named in.
//
// **Fixed, and it is a list rather than a map** for the reason `realtime.componentOrder`
// is fixed: the order a refusal names its components in is part of what an operator
// reads, and a map would make it differ between runs.
var componentOrder = []component{
	componentSystem,
	componentRuleset,
	componentBase,
	componentOverlay,
}

// the two refusals, kept apart for the reason the audit insists on: a column this build
// cannot parse is a migration problem, and a genuine mismatch is a GM's decision, and
// one sentinel for both sends a GM to discard a game over a string.
var (
	errFixtureDrift      = errors.New("fixture: the state was written under another ruleset")
	errFixtureUnreadable = errors.New("fixture: that ruleset_version is not comparable")
)

// rulesetGate is a deployment's ruleset decision, with the faults a deployment can have
// switched on so a test can require the audit to catch each one.
//
// **A value with two booleans rather than a registry of broken variants.** Two is what
// this file needs — resolve instead of refuse, and conflate an unreadable column with a
// genuine mismatch — and a third switch would be a variant nobody asserts, which is the
// "audit no fixture reaches" failure in miniature.
type rulesetGate struct {
	// resolvesInsteadOfRefusing is §10.8's forbidden outcome: a campaign written under
	// another ruleset is permitted to resume, and the mutations recorded under the old
	// semantics are re-resolved under the new ones with nothing saying so.
	resolvesInsteadOfRefusing bool

	// conflatesAnUnreadableColumn answers a column it cannot parse and a genuine
	// mismatch with the same error, so a handler cannot tell a GM which of two very
	// different recoveries applies.
	conflatesAnUnreadableColumn bool
}

var _ conformance.Resume = rulesetGate{}

// componentsOf returns what a campaign opened now would be written under, read from
// the system's own declarations.
//
// **The pack components come from an optional interface and the fallback is empty**,
// which is `realtime.checkComponent`'s documented exemption: a system shipping one
// standalone pack contributes an empty overlay rather than nothing at all, and a system
// that volunteers no pack versions contributes a fingerprint a deployment can still
// compare.
func componentsOf(system rules.System) map[component]string {
	components := map[component]string{
		componentSystem:  system.ID().String(),
		componentRuleset: system.RulesetVersion(),
	}

	if versioned, ok := system.(interface{ Versions() dnd5e.Versions }); ok {
		versions := versioned.Versions()
		components[componentBase] = versions.BasePack
		components[componentOverlay] = versions.OverlayPack
	} else {
		components[componentBase] = ""
		components[componentOverlay] = ""
	}

	return components
}

// fingerprint renders a set of components as the column would hold it.
//
// **A fixed order and a fixed separator, and neither escaped.** A component carrying a
// separator is refused rather than escaped, which is `realtime`'s decision for the
// reason its header gives: a plugin whose id contains a semicolon is a plugin whose id
// should be renamed.
func encodeFingerprint(components map[component]string) string {
	parts := make([]string, 0, len(componentOrder))

	for _, one := range componentOrder {
		parts = append(parts, string(one)+"="+components[one])
	}

	return "fixture:" + strings.Join(parts, ";")
}

// Fingerprint is what a campaign opened now would be written under.
func (g rulesetGate) Fingerprint(system rules.System, _ []conformance.HouseRule) string {
	return encodeFingerprint(componentsOf(system))
}

// Check answers whether a campaign written under `persisted` may resume.
func (g rulesetGate) Check(
	system rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	expected := g.Fingerprint(system, house)
	verdict := conformance.Verdict{Persisted: persisted, Expected: expected}

	if persisted == expected {
		return verdict
	}

	// The misresolve, and it is the one §10.8's row names: a refusal replaced by a
	// silent re-resolution. The verdict carries **no error at all**, so a caller
	// checking `verdict.Err != nil` sees a campaign that may resume.
	if g.resolvesInsteadOfRefusing {
		return verdict
	}

	wanted := componentsOf(system)
	got, read := decodeFingerprint(persisted)

	if !read {
		refusal := errFixtureUnreadable
		if g.conflatesAnUnreadableColumn {
			refusal = errFixtureDrift
		}

		verdict.Err = fmt.Errorf("%w: %q is not a fingerprint this build wrote", refusal, persisted)

		return verdict
	}

	verdict.Err = fmt.Errorf(
		"%w: the state was written under %q and this build resolves %q, and %s differs",
		errFixtureDrift, persisted, expected, firstDifference(got, wanted),
	)

	return verdict
}

// decodeFingerprint reads the encoded form back, and is what tells a drift from a
// column this build cannot compare.
func decodeFingerprint(encoded string) (map[component]string, bool) {
	body, found := strings.CutPrefix(encoded, "fixture:")
	if !found {
		return nil, false
	}

	decoded := make(map[component]string, len(componentOrder))

	parts := strings.Split(body, ";")

	if len(parts) != len(componentOrder) {
		return nil, false
	}

	for idx, part := range parts {
		name, value, assigned := strings.Cut(part, "=")
		if !assigned || name != string(componentOrder[idx]) {
			return nil, false
		}

		decoded[componentOrder[idx]] = value
	}

	return decoded, true
}

// firstDifference names the component that moved, in `componentOrder`.
//
// **The first in a fixed order rather than every one that moved**, because the reader of
// a refusal wants the thing to go and look at: a GM fixes one input, looks again, and
// sees the next. A refusal listing three has told them there are three and nothing about
// which to take first.
func firstDifference(got, wanted map[component]string) component {
	for _, one := range componentOrder {
		if got[one] != wanted[one] {
			return one
		}
	}

	return ""
}

// # The claims

// TestARulesetDriftIsRefusedForEveryShippedSystemAndEveryComponent is the row, over
// the fleet.
//
// **A table over four components per system, and each cell moves exactly one.** That is
// the whole of the claim and it is the half that can be silently wrong: a fingerprint
// reading three of the four components passes every test that changes all four at once,
// and the component it does not read is precisely the one nobody noticed was gone.
//
// **And the negative control is in the same test**, before the refusals: the deployment's
// own fingerprint, checked against itself, must resume. A gate that refuses everything
// satisfies all four rows below and would leave this file's assertions about the
// refusals true for the wrong reason.
func TestARulesetDriftIsRefusedForEveryShippedSystemAndEveryComponent(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			gate := rulesetGate{}
			mine := gate.Fingerprint(one.system, nil)

			if fresh := gate.Check(one.system, nil, mine); fresh.Err != nil {
				t.Fatalf("a campaign written under this deployment's own fingerprint %q was "+
					"refused: %v; a gate that refuses the version it just wrote refuses "+
					"everything, and every row below would then be true for nothing",
					mine, fresh.Err)
			}

			for _, moved := range componentOrder {
				t.Run(string(moved), func(t *testing.T) {
					t.Parallel()

					theirs := componentsOf(one.system)
					theirs[moved] = movedValue(theirs[moved])

					if theirs[moved] == componentsOf(one.system)[moved] {
						t.Fatalf("the fixture could not move %s, so this row asserts nothing",
							moved)
					}

					verdict := gate.Check(one.system, nil, encodeFingerprint(theirs))

					if verdict.Err == nil {
						t.Fatalf(
							"a campaign written under %q may resume under a deployment that "+
								"resolves %q, and only %s differs; §10.8 requires a refusal "+
								"rather than a silent misresolution, because a mutation "+
								"resolved under the older semantics may not replay under the "+
								"newer ones",
							encodeFingerprint(theirs), mine, moved,
						)
					}

					// And the refusal says **which** input moved, in the place the status
					// page and the log line both read it. A refusal naming the ruleset for a
					// pack revision has sent the GM to the wrong file.
					//
					// **Matched against the clause and not against the message**, and that
					// correction is a mutation's doing: a `strings.Contains(err, moved)`
					// assertion passed under a fingerprint that stopped reading the two pack
					// components, because the refusal quotes the persisted fingerprint and
					// that string contains "the base pack" whether or not the refusal blamed
					// it. `componentsNamedIn` looks for the blaming clause.
					if named := componentsNamedIn(verdict.Err.Error()); len(named) != 1 ||
						named[0] != moved {
						t.Errorf("the refusal %q names %v, want exactly [%s]; a GM has four "+
							"inputs and no way to know which one moved",
							verdict.Err, named, moved)
					}

					// **Both versions**, because `objectionTo` in the suite checks the same
					// two and a deployment that fills one and not the other would leave
					// half the page saying nothing.
					for _, end := range []string{encodeFingerprint(theirs), mine} {
						if !strings.Contains(verdict.Err.Error(), end) {
							t.Errorf("the refusal %q does not name the version %q; a GM told "+
								"there is a difference and not which has been given a "+
								"diagnosis to carry", verdict.Err, end)
						}
					}
				})
			}
		})
	}
}

// movedValue returns a value that differs from the one it is given.
//
// **Suffixed rather than randomised**, because a fixed value is a golden one a reader
// can check by eye, and a random one would make a failure unreproducible. The empty
// component is the awkward case — the overlay is legitimately empty for a standalone
// pack — and filling it is exactly the move that distinguishes "no overlay" from "an
// overlay this build does not have", which is the point of gating on it at all.
//
// **One argument, and the component is not one of them**: the four components move the
// same way, so a parameter would be a second way of spelling the same function and a
// place for a caller to pass the wrong one.
func movedValue(current string) string {
	if current == "" {
		return "moved"
	}

	return current + "-moved"
}

// TestTheTwoKindsOfRefusalAreDistinguishableAndTheFourComponentsAreNamedApart is "the
// reason is distinguishable", split into the two claims it actually consists of — and
// **a first version of this file asserted the wrong one, which is worth recording**.
//
// # What it first asserted, and what the product actually does
//
// It asserted that the four per-component drift refusals are pairwise distinguishable
// to a handler routing with `errors.Is`. They are not, and they are **not supposed to
// be**: `realtime.DriftError.Is` answers `true` for `ErrRulesetDrift` for every
// component, because they are one failure with four causes. A deployment that wrapped a
// different sentinel around each component would be inventing four failures where the
// product has one, and every caller that branches on `ErrRulesetDrift` would need
// three more cases.
//
// So the claim has two halves and this test asserts both:
//
//  1. **The two *kinds* of failure are distinguishable** — a genuine mismatch against a
//     column this build cannot parse — by `errors.Is` in both directions *and* at their
//     roots. This is the half with consequences: the recovery differs (a GM's decision
//     against a migration), and telling a GM to discard a game over an unparseable
//     string is the software inflicting the loss ADR 0018 exists to prevent.
//  2. **The four *components* are named apart** — each refusal names exactly one
//     component, the four names are pairwise distinct, and the name is one the operator
//     can go and look at. This is the half `DriftError.Component` exists for: a handler
//     reads it as a typed field and a status page prints it, and a GM who is told "the
//     base pack differs" knows which file to open.
//
// # Why the root is compared as well as the sentinel
//
// `errors.Is(a, b)` alone is not enough, because `b` here is the whole message-bearing
// error rather than a sentinel. A deployment that wrapped one sentinel in two different
// sentences would pass a comparison a handler could never make — and `realtime` answers
// these two with a `*DriftError`, which has an `Is` and **no `Unwrap`**, so its root is
// itself. Two deployments answering the two failures with objects that merely share a
// sentinel are one refusal to every caller.
func TestTheTwoKindsOfRefusalAreDistinguishableAndTheFourComponentsAreNamedApart(t *testing.T) {
	t.Parallel()

	one := fiveeFleet(t)
	gate := rulesetGate{}

	unreadable := gate.Check(one.system, nil, "not a fingerprint this build wrote")
	if unreadable.Err == nil {
		t.Fatal("a column this build cannot parse was permitted to resume; refusing it is " +
			"the whole point, and guessing at a version is the misresolve")
	}

	named := make(map[component]string, len(componentOrder))

	for _, moved := range componentOrder {
		theirs := componentsOf(one.system)
		theirs[moved] = movedValue(theirs[moved])

		verdict := gate.Check(one.system, nil, encodeFingerprint(theirs))
		if verdict.Err == nil {
			t.Fatalf("only %s differs and the campaign was permitted to resume", moved)
		}

		// Claim 1, against the unreadable column. Both directions and the roots.
		if indistinguishable(verdict.Err, unreadable.Err) {
			t.Errorf(
				"a refusal naming %s cannot be told from the refusal for a column this "+
					"build cannot parse, by errors.Is in either direction or at their roots; "+
					"one is a GM's decision and the other is a migration, and telling a GM to "+
					"discard a game over an unparseable string is the software inflicting "+
					"the loss ADR 0018 exists to prevent\n  mismatch: %v\n  unreadable: %v",
				moved, verdict.Err, unreadable.Err,
			)
		}

		// Claim 2, the name. Exactly one, or the refusal has told the reader that three
		// things moved and nothing about which to take first.
		found := componentsNamedIn(verdict.Err.Error())
		if len(found) != 1 {
			t.Errorf("the refusal for %s names %v; it must name exactly one component, "+
				"because a refusal listing three has told the reader there are three and "+
				"nothing about which to look at", moved, found)
		}

		for _, blamed := range found {
			if already, twice := named[blamed]; twice {
				t.Errorf("both the %s move and the %s move name %q, so an operator reading "+
					"the two cannot tell which input they are being sent to look at",
					already, moved, blamed)
			}

			named[blamed] = string(moved)
		}
	}

	// Every component must have been named by something, or one of the four is
	// invisible and the count assertion above would pass on three.
	for _, moved := range componentOrder {
		if _, named := named[moved]; !named {
			t.Errorf("no refusal in this table names %s, so a change to it produces a "+
				"refusal a GM cannot act on", moved)
		}
	}
}

// componentsNamedIn returns the components a refusal message names.
//
// **Over the four known names**, and the reason is that this is a fixture's own message
// format: `component` is a closed set of four by construction, and a message that named
// a fifth would be naming something this fixture cannot represent.
func componentsNamedIn(message string) []component {
	var named []component

	for _, one := range componentOrder {
		if strings.Contains(message, "and "+string(one)+" differs") {
			named = append(named, one)
		}
	}

	return named
}

// indistinguishable reports whether two refusals are the same refusal as far as a
// handler can tell, in both directions and at their roots.
func indistinguishable(first, second error) bool {
	if errors.Is(first, second) || errors.Is(second, first) {
		return true
	}

	return deepest(first) != nil && errors.Is(deepest(first), deepest(second))
}

// deepest unwraps to the last error in a chain, or the error itself when it unwraps to
// nothing.
func deepest(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}

		err = unwrapped
	}
}

// TestADeploymentThatResolvesInsteadOfRefusingIsCaughtByThePublishedSuite is the
// mutation, committed rather than performed: the fixture carries the fault and the test
// requires the *product's own* certification to object.
//
// **Two halves, and the second is the one that does the work.** A broken deployment
// producing a finding proves the fixture is broken; a *well-formed* deployment and a
// well-formed system producing **none** proves the finding was the audit's and not the
// fixture's — which is the failure `conformance_test.go` calls "a version fixture that
// happened to make the role audit complain", and the reason that test requires every
// finding to be attributed to the audit under test.
//
// **Every finding must be attributed to `AuditVersion` *and* must be the finding about
// this fault.** The second half was added because the first was not enough, and the
// mutation that found it is the reason this test reads the way it does: with the audit's
// drift row deleted, a broken deployment still drew findings — from the *unreadable*
// row, because a fixture that resolves everything resolves an unparseable column too —
// so "the audit objected" passed while "the audit objected about §10.8's row" was false.
// **A test that counts findings is a test that can be satisfied by the wrong one**, which
// is the failure `conformance_test.go` calls "a version fixture that happened to make the
// role audit complain".
const (
	// driftPermittedFinding is what `audit_version.go` says when a campaign written under
	// another ruleset was allowed to resume.
	driftPermittedFinding = "was permitted to resume under"

	// conflatingFinding is what it says when a genuine mismatch and an unparseable column
	// are one refusal to a caller.
	conflatingFinding = "refused in a way a caller cannot tell from"
)

func TestADeploymentThatResolvesInsteadOfRefusingIsCaughtByThePublishedSuite(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			intact := suiteFor(t, one, rulesetGate{})

			if found := intact.Version(); len(found) != 0 {
				t.Fatalf(
					"a well-formed system and a well-formed deployment drew %d finding(s) from "+
						"the version audit, so the violation below would pass for the wrong "+
						"reason:\n%s",
					len(found), joinSummaries(found),
				)
			}

			broken := suiteFor(t, one, rulesetGate{resolvesInsteadOfRefusing: true})

			found := broken.Version()
			if len(found) == 0 {
				t.Fatal(
					"a deployment that resumed a campaign written under another ruleset " +
						"drew nothing, so the audit cannot fail at §10.8's row",
				)
			}

			aboutThisFault := false

			for _, finding := range found {
				if finding.Audit != conformance.AuditVersion {
					t.Errorf("the version audit under test reported %q, which is a different "+
						"audit; this row would pass on somebody else's finding", finding.Audit)
				}

				if strings.Contains(finding.Summary, driftPermittedFinding) {
					aboutThisFault = true
				}
			}

			if !aboutThisFault {
				t.Errorf(
					"none of the %d finding(s) is about a campaign resuming under another "+
						"ruleset, so this row passed on a finding about something else:\n%s",
					len(found), joinSummaries(found),
				)
			}
		})
	}
}

// TestADeploymentThatCannotTellDriftFromAnUnreadableColumnIsCaught is the second fault,
// and it is the one whose consequence is a lost game rather than a misresolved roll.
func TestADeploymentThatCannotTellDriftFromAnUnreadableColumnIsCaught(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			broken := suiteFor(t, one, rulesetGate{conflatesAnUnreadableColumn: true})

			found := broken.Version()
			if len(found) == 0 {
				t.Fatal(
					"a deployment answering a genuine mismatch and an unparseable column " +
						"with one refusal drew nothing, so the audit cannot fail at the half " +
						"of §10.8's row that keeps a GM from being told to discard a game " +
						"over a string",
				)
			}

			aboutThisFault := false

			for _, finding := range found {
				if finding.Audit != conformance.AuditVersion {
					t.Errorf("the version audit under test reported %q, which is a different "+
						"audit; this row would pass on somebody else's finding", finding.Audit)
				}

				if strings.Contains(finding.Summary, conflatingFinding) {
					aboutThisFault = true
				}
			}

			if !aboutThisFault {
				t.Errorf(
					"none of the %d finding(s) is about an unreadable column answered as a "+
						"genuine mismatch, so this row passed on a finding about something "+
						"else:\n%s",
					len(found), joinSummaries(found),
				)
			}
		})
	}
}

// TestAHouseRuleDoesNotStrandACampaign is ADR 0018's exclusion, asserted through this
// file's fixture rather than only through the suite's own row.
//
// **Both directions, and the second is the one that reads like a bug.** A fingerprint
// that hashed the enabled set would refuse to resume every campaign whose GM enabled a
// rule at the table — a fault that reads as a feature, because the refusal would be
// perfectly reasonable on its face. So the fingerprints must be byte-identical, *and*
// the campaign must still resume under the enabled configuration.
func TestAHouseRuleDoesNotStrandACampaign(t *testing.T) {
	t.Parallel()

	gate := rulesetGate{}

	disabled := []conformance.HouseRule{{ModuleID: "strict", Position: 0, Enabled: false}}
	enabled := []conformance.HouseRule{{ModuleID: "strict", Position: 0, Enabled: true}}

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			before := gate.Fingerprint(one.system, disabled)
			after := gate.Fingerprint(one.system, enabled)

			if before != after {
				t.Fatalf(
					"enabling a house rule moved the fingerprint from %q to %q; ADR 0018 "+
						"requires the fingerprint to name resolution semantics and not "+
						"house-rule configuration, or a GM enabling a rule at the table is "+
						"refused on their own campaign", before, after,
				)
			}

			if verdict := gate.Check(one.system, enabled, before); verdict.Err != nil {
				t.Errorf("a campaign this deployment wrote itself was refused once a house "+
					"rule was enabled: %v; toggling a house rule changes outcomes and not the "+
					"meaning of a stored mutation, so it must not strand a campaign",
					verdict.Err)
			}
		})
	}
}

// TestASystemWithNoRulesetVersionCannotBeRegistered closes the loop: the whole of this
// file's machinery is worthless against a system whose ruleset component is a constant,
// so the contract refuses one at registration rather than letting a build register a
// system whose fingerprint cannot move.
//
// **Three claims.** The contract refuses a system reporting no version, with its own
// sentinel; every system this build ships reports one; and **two systems this build
// ships that resolve the same tabletop cannot be told apart by the ruleset component
// alone** — which is why the pack components exist and why `TestARulesetDriftIsRefusedForEveryShippedSystemAndEveryComponent`
// moves each of them in turn.
func TestASystemWithNoRulesetVersionCannotBeRegistered(t *testing.T) {
	t.Parallel()

	unversioned := unversionedSystem{System: orderByMapResolver{}}

	err := rules.Validate(unversioned)

	if !errors.Is(err, rules.ErrNoRulesetVersion) {
		t.Fatalf("a system reporting no ruleset version was accepted: %v; a campaign whose "+
			"expected version is empty cannot detect drift, so the gate that exists to "+
			"notice it would be silent", err)
	}

	if !errors.Is(err, rules.ErrMalformedSystem) {
		t.Error("the refusal does not satisfy the umbrella a registry asks with errors.Is")
	}

	reported := make(map[string]string, 4)

	for _, one := range shippedFleets(t) {
		version := one.system.RulesetVersion()

		if version == "" {
			t.Errorf("%s reports no ruleset version, so a campaign of its own cannot "+
				"detect drift", one.name)
		}

		// Recorded rather than compared: two systems may legitimately share a version
		// string (`dnd5e` and `dnd5e` with an overlay do, because the overlay is its own
		// component), and the claim is not that they differ.
		reported[one.name] = version
	}

	if len(reported) < 2 {
		t.Fatalf("only %d system(s) are in the fleet, so this file cannot claim anything "+
			"about two of them", len(reported))
	}
}

// unversionedSystem is a system that reports no ruleset version, which is the one
// fingerprint input a build can get wrong silently.
//
// **A decorator, for the reason `conformance`'s `upgraded` is one**: every method but
// the version is promoted, so the refusal is about the version and nothing else.
type unversionedSystem struct {
	rules.System
}

func (unversionedSystem) RulesetVersion() string { return "" }

func (unversionedSystem) ID() rules.ID { return "unversioned" }

func (unversionedSystem) Title() string { return "A system with no ruleset version" }

// # The suite's own plumbing, which this file reuses rather than rebuilds

// suiteFor configures `conformance` over one fleet and one ruleset decision.
//
// **A real configuration rather than a hand-assembled `Suite`**, because the point of
// these tests is that the *published* certification objects: `conformance.New` is what
// refuses a configuration that could not fail, and a suite built around it is the one
// a plugin author runs.
//
// **The scenario is one corpus scenario**, so the scenario's operation is not the
// GM-only one — `New` refuses that, correctly, because the role audit would then
// require the same resolution to be refused and allowed. This file only runs
// `Suite.Version()`, but a suite that could not be built is not a suite.
func suiteFor(t *testing.T, one fleet, gate conformance.Resume) *conformance.Suite {
	t.Helper()

	if len(one.scenarios) == 0 {
		t.Fatal("the fleet carries no scenario, so §10.8's row cannot be asked of it")
	}

	chosen := one.scenarios[0]

	suite, err := conformance.New(conformance.Config{
		System: one.system,
		Scenario: conformance.Scenario{
			Objects: chosen.objects,
			Intent:  chosen.intent,
			Call: rules.Context{
				Campaign: chosen.campaign,
				Actor:    chosen.actor,
				Role:     chosen.role,
				Seed:     chosen.seed,
			},
		},
		GMOnly:   one.gmOnly,
		Classify: classifyForRulesetTesting,
		Resume:   gate,
	})
	if err != nil {
		t.Fatalf("configuring the suite for %s: %v", one.name, err)
	}

	return suite
}

// classifyForRulesetTesting is the error-to-wire-word map, and it is deliberately the
// laziest one that exists.
//
// **This file runs `Suite.Version()`, which never calls it.** It is here because
// `conformance.New` requires it, and because a map that claims to be careful when the
// test does not exercise it is a map that would pass a reader's attention without
// earning it. `server_error` is §10.7's "the one reason that says nothing about what
// failed", which is the honest answer for a fixture's refusals.
func classifyForRulesetTesting(error) string { return conformance.ReasonServerError }

// joinSummaries renders findings for one failure message.
func joinSummaries(found []conformance.Finding) string {
	lines := make([]string, 0, len(found))

	for _, finding := range found {
		lines = append(lines, finding.String())
	}

	return strings.Join(lines, "\n")
}

// // The compile-time assertion that the role vocabulary is reachable from here, which is
// what lets a corpus scenario be handed to the suite as a `rules.Context`.
//
// **`dnd5e` and `notfive` are named only for their GM-only operation lists**, and the
// fleet carries those because it is built from their own constructors
// (`overlays.ByID` and `notfive.New`) rather than from a table somebody edits beside
// them. A system that reserved a third GM-only operation would therefore be certified
// for the third rather than for the two this file happens to know about.
var (
	_ = dnd5e.GMOnlyOps
	_ = notfive.LoomGMOnlyOps
	_ = domain.RoleGM
)
