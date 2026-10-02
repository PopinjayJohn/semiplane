// Package dnd5e's tests, in two halves.
//
// This file is the **external** half — `package dnd5e_test` — and everything in it
// goes through the exported surface and the `rules.System` interface. It certifies the
// system against `internal/domain/rules/conformance` and then proves the system is
// actually a 5e engine rather than a thing that satisfies every audit by refusing
// everything, which is the objection the reference system's own tests answer and the
// reason this file has a second half at all.
//
// The **internal** half lives in `notation_test.go`, `pack_test.go`, `resolve_test.go`,
// `overlay_test.go`, `ruleset_test.go` and `imports_test.go`, where the assertions need
// the parsed form of an expression, the compiled form of a pack, or this package's own
// imports. `internal/domain/systems/notfive` splits the same way for the same reason:
// a test that can only reach through the interface cannot check anything the interface
// deliberately does not promise.
package dnd5e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
)

// # The conformance seams
//
// Four values, and the whole of what this package has to hand the suite beyond the
// `rules.System` it exports. They are the shape an author outside this repository
// copies, and none of them reaches into anything semiplane owns except the contract
// and the role vocabulary.

// classify is the composition root's error-to-wire-word map.
//
// **A switch on this system's own sentinels and nothing else**, which is what makes a
// wire word possible at all: the composition root is the only place holding both a
// `rules.System` and a `realtime.RejectReason`, and the alternative — string matching,
// or letting the system's own sentence through — is how whatever a plugin choked on
// reaches a browser.
//
// The default is `server_error` and not a word chosen per case, because a refusal
// nobody recognises is a refusal this repository has no safe reading for. It is also
// the word §10.7 means by "the one reason that says nothing about what failed", which
// is what a contained panic is.
func classify(refusal error) string {
	switch {
	case errors.Is(refusal, dnd5e.ErrGMOnly):
		return conformance.ReasonNotPermitted
	case errors.Is(refusal, dnd5e.ErrNotYourCreature):
		// **The same word as `ErrGMOnly`, and deliberately.** §7.2's two halves are "the
		// GM's to decide" and "yours to act for", and both of them answer the same
		// question on the wire — *may this actor make this change* — with the same
		// answer. Keeping them apart in the system's own vocabulary is what lets the log
		// line say which rule refused; collapsing them before the wire is what stops the
		// table learning that the two are different failures.
		return conformance.ReasonNotPermitted
	case errors.Is(refusal, dnd5e.ErrUnknownOp):
		return conformance.ReasonUnknownOp
	case errors.Is(refusal, dnd5e.ErrNoSuchCreature):
		return conformance.ReasonNoSuchPlacement
	case errors.Is(refusal, dnd5e.ErrMalformedCreature):
		// `no_such_placement` rather than `server_error`: a token whose body this
		// build cannot read is a placement the client cannot render, and a client told
		// `server_error` has nothing to do about it.
		return conformance.ReasonNoSuchPlacement
	case errors.Is(refusal, dnd5e.ErrBadArguments):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, dnd5e.ErrRoll):
		// `invalid_args`, and kept a distinct sentinel from `ErrBadArguments` only so
		// that the *log line* can say which of the two a client got wrong — both are the
		// same word on the wire, which is correct, because a client that fixed neither
		// has one thing to fix: what it sent.
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, dnd5e.ErrNoSuchAttack):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, dnd5e.ErrNoSuchCondition):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, dnd5e.ErrNoSuchAbility):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, dnd5e.ErrNoSuchMastery):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, conformance.ErrContained):
		return conformance.ReasonServerError
	default:
		return conformance.ReasonServerError
	}
}

// deployment is the ruleset seam: what this process would persist, and whether a
// campaign written under something else may resume.
//
// **The encoding mirrors `internal/realtime`'s**, because the suite's whole reason for
// asking the deployment rather than inventing a version is that the fixture must be
// *readable as plausible*: `sp1:` and the four `key=value` components in
// `componentOrder`'s order are what `realtime.FingerprintOf` writes, so a fingerprint
// this produces is one the product would have produced. The suite itself cannot know
// that — it only knows the string this type returns — and a hand-written prefix
// `realtime.ParseFingerprint` would reject would make the version audit a test of this
// file's string handling rather than of the system's semantics.
//
// A real deployment does not call any of this: it calls `realtime.FingerprintOf` over a
// `realtime.Descriptor` and `Gate.Check`, which is why the `Resume` seam exists at all
// (`conformance`'s `versioned.go` says so in as many words).
type deployment struct{}

// fingerprintPrefix is the encoding's format marker, `realtime.fingerprintFormat`.
const fingerprintPrefix = "sp1:"

// componentKeys is `realtime.componentKeys`, positionally paired with the order below.
var componentKeys = []string{"system", "ruleset", "base", "overlay"}

// The two refusals, kept apart for the reason the version audit insists on: a column
// this build cannot parse is a migration problem, and a genuine mismatch is a GM's
// decision, and one sentinel for both would send the GM to discard a game over a
// string.
var (
	errDrift      = errors.New("realtime: persisted state was written under a different ruleset")
	errUnreadable = errors.New("realtime: persisted ruleset_version is not comparable")
)

// Fingerprint is what a campaign opened now would be written under.
//
// **`house` is read for nothing**, and that is ADR 0018 as an assertion rather than a
// comment. The suite toggles a house rule and requires the two fingerprints to be
// byte-identical, so a future edit that starts reading `house` fails a test rather than
// stranding a GM's campaign.
func (deployment) Fingerprint(system rules.System, _ []conformance.HouseRule) string {
	values := []string{system.ID().String(), system.RulesetVersion()}

	// The two pack components come from this system's optional second interface, and
	// **the fallback is empty rather than a refusal**: `realtime.checkComponent`
	// exempts exactly the overlay component, and a system that does not volunteer its
	// pack versions contributes a fingerprint a deployment can still compare. A
	// deployment wanting them would require the interface, which is a composition-root
	// decision rather than this fixture's.
	if versioned, ok := system.(interface{ Versions() dnd5e.Versions }); ok {
		versions := versioned.Versions()
		values = append(values, versions.BasePack, versions.OverlayPack)
	} else {
		values = append(values, "", "")
	}

	parts := make([]string, 0, len(componentKeys))

	for idx, key := range componentKeys {
		parts = append(parts, key+"="+values[idx])
	}

	return fingerprintPrefix + strings.Join(parts, ";")
}

// Check answers whether a campaign written under persisted may resume.
//
// **Two refusals, and the parse is what tells them apart.** A value that does not
// decode into four keyed components is `errUnreadable`; a value that decodes and
// differs is `errDrift`. Both messages name the version they were asked about and the
// one this build would write, because the version audit checks that they do and a
// refusal that names neither has moved the diagnosis onto the reader.
func (d deployment) Check(
	system rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	expected := d.Fingerprint(system, house)
	verdict := conformance.Verdict{Persisted: persisted, Expected: expected}

	if persisted == expected {
		return verdict
	}

	parts, ok := parseFingerprint(persisted)
	if !ok {
		verdict.Err = fmt.Errorf(
			"%w: %q is not a fingerprint this build wrote; it was written by some other build",
			errUnreadable, persisted,
		)

		return verdict
	}

	verdict.Err = fmt.Errorf(
		"%w: the state was written under %q and this build resolves %q",
		errDrift, persisted, expected,
	)

	_ = parts

	return verdict
}

// parseFingerprint decodes the encoding above, and is what tells a drift from an
// unreadable column.
//
// **Key order is checked, not merely key count.** `realtime`'s own parser refuses a
// fingerprint whose components are in an unexpected order, for the reason its
// `componentKeys` comment gives: the two lists are positionally paired and a row in
// any other order is a row this build did not write. A fixture that accepted any order
// would let a campaign whose column was shuffled by something else be reported as a
// gameplay incompatibility.
func parseFingerprint(encoded string) (map[string]string, bool) {
	body, found := strings.CutPrefix(encoded, fingerprintPrefix)
	if !found {
		return nil, false
	}

	decoded := make(map[string]string, len(componentKeys))

	for idx, component := range strings.Split(body, ";") {
		key, value, split := strings.Cut(component, "=")
		if !split || key != componentKeys[idx] {
			return nil, false
		}

		decoded[key] = value
	}

	if len(decoded) != len(componentKeys) {
		return nil, false
	}

	return decoded, true
}

var _ conformance.Resume = deployment{}

// # The scenario
//
// One attack, because the determinism audit needs a resolution that **changes
// something** — an intent that resolves to nothing is reproducible whether or not the
// resolver is deterministic, and the suite refuses to certify one.
//
// The attacker's token is **placed for `Call.Actor`**, which is what makes the scenario
// playable as a player: the role audit resolves this very scenario as a player and
// requires it not to be refused as `not_permitted`, and a token belonging to somebody
// else would be refused for ownership — correctly, but for a reason that has nothing to
// do with the audit. A conformance scenario has to be a resolution a real player would
// make.
const (
	attackerID rules.ObjectID = "orc_1"
	defenderID rules.ObjectID = "kobold_1"
	attackName                = "greataxe"
)

const attackerBody = `{
  "name": "Vurg",
  "actor": 7,
  "level": 5,
  "ability": {
    "strength": 18, "dexterity": 12, "constitution": 16,
    "intelligence": 8, "wisdom": 10, "charisma": 6
  },
  "hp": 44, "max_hp": 49, "ac": 0, "ac_bonus": 2, "speed": 30,
  "conditions": [],
  "attacks": [
    {
      "name": "greataxe",
      "ability": "strength",
      "damage": "1d12+4",
      "attack_bonus": 0,
      "mastery": ["great_weapon", "two_handed"]
    }
  ],
  "masteries": ["finesse", "great_weapon", "two_handed"]
}`

const defenderBody = `{
  "name": "Kobold",
  "actor": 0,
  "level": 3,
  "ability": {
    "strength": 7, "dexterity": 15, "constitution": 9,
    "intelligence": 8, "wisdom": 7, "charisma": 8
  },
  "hp": 12, "max_hp": 12, "ac": 0, "ac_bonus": 0, "speed": 30,
  "conditions": [],
  "attacks": [],
  "masteries": []
}`

// suiteFor builds a complete configuration: one scenario, the two GM-only operations,
// this package's refusals, and the ruleset seam above.
func suiteFor(t *testing.T) conformance.Config {
	t.Helper()

	engine := anEngine(t)

	intent, err := rules.NewIntent(
		dnd5e.OpAttack, attackerID,
		[]byte(`{"attack":"`+attackName+`","defender":"`+defenderID.String()+`"}`),
	)
	if err != nil {
		t.Fatalf("building the scenario's intent: %v", err)
	}

	return conformance.Config{
		System: engine,
		Scenario: conformance.Scenario{
			Objects: []rules.Object{
				{ID: attackerID, Kind: rules.KindToken, Data: []byte(attackerBody)},
				{ID: defenderID, Kind: rules.KindToken, Data: []byte(defenderBody)},
			},
			Intent: intent,
			Call:   aCall(t, domain.RoleGM, 3),
		},
		GMOnly:   dnd5e.GMOnlyOps(),
		Classify: classify,
		Resume:   deployment{},
	}
}

// TestTheFiveeSystemConforms is §14's conformance suite, all five audits, in the one
// call a system author's test needs.
//
// A single test rather than five, and the reason is `conformance.Audits`'s: the audits
// are a set and the result is a set, and a plugin author who has broken three things
// should learn all three from one run. `TestTheSuiteStillHasTheFiveAuditsItDocuments`
// holds the count where it belongs.
func TestTheFiveeSystemConforms(t *testing.T) {
	t.Parallel()

	if err := conformance.Check(t.Context(), suiteFor(t)); err != nil {
		t.Fatal(err)
	}
}

// # The system's own behaviour
//
// The conformance suite proves this engine obeys the contract. These prove it is *an
// engine*, which is what makes the certification worth anything: a system that passed
// every audit by refusing everything would satisfy all five and demonstrate nothing.

// TestAnAttackResolvesTwoMutationsOnTwoObjects is the flagship rule end to end, and it
// asserts the **shape** as well as the number.
//
// Two mutations under two different op names on two different targets is
// `rules.Mutation`'s rule made concrete. A resolver that reused the intent's op for
// both would put one fact on the wire twice and a client reconciling the two would be
// reading two records of one event.
func TestAnAttackResolvesTwoMutationsOnTwoObjects(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	// A seed that is fixed *and* whose roll is forced, so the assertion is about the
	// resolution's shape and not about which way a die fell. `struckEveryOne` walks the
	// seeds until one lands the blow.
	seed := aSeedThatLands(t, system)

	mutations, err := system.Apply(
		t.Context(), aCall(t, domain.RoleGM, seed), aTable(t), anAttack(t, attackerID, defenderID),
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(mutations) != 2 {
		t.Fatalf("a landed attack resolved to %d mutations, want 2", len(mutations))
	}

	if mutations[0].Target != attackerID || mutations[0].Op != dnd5e.OpAttack {
		t.Errorf("the first mutation is %s, want attack on %s", mutations[0], attackerID)
	}

	if mutations[1].Target != defenderID || mutations[1].Op != dnd5e.OpSetHitPoints {
		t.Errorf("the second mutation is %s, want set_hit_points on %s", mutations[1], defenderID)
	}

	if mutations[0].Op == mutations[1].Op {
		t.Error("both mutations carry the intent's own op, which is a second record of one fact")
	}
}

// TestAMissedAttackStillRecordsTheRollAndDealsNoDamage is the other branch, and it is
// the one a table notices: a player watching their attack fizzle needs the roll on
// their token.
//
// **Both branches over many seeds rather than one of each**, and that is forced by the
// rules rather than chosen: a natural 20 is an automatic critical hit that bypasses
// armour class, so no defender on this table can be made unmissable — AC 40 still falls
// to a natural 20. Asserting "every seed missed" would therefore be a test about a
// fixture that cannot exist, and asserting on a single seed would fail one run in
// twenty in the direction that reads as a regression. Requiring that both branches were
// *seen* over forty seeds cannot be flaky: the probability of forty consecutive natural
// twenties is one in 20^40.
//
// A **miss is one mutation, on the attacker**, and that is the assertion. Returning
// nothing would be indistinguishable from an intent that changed nothing; returning a
// second mutation on a defender whose hit points did not move would be a record of an
// event that did not happen.
func TestAMissedAttackStillRecordsTheRollAndDealsNoDamage(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	// A golum with an armour class a d20 does not reach: `ac` is a stored override, so
	// the fixture needs no formula and no house rule.
	//nolint:tagliatelle // the fixture speaks the encoding the resolver reads, so it is
	// in the resolver's spelling rather than one of its own.
	//nolint:tagliatelle // the fixture speaks the encoding the resolver reads, so it is
	// in the resolver's spelling rather than one of its own.
	//nolint:tagliatelle // the fixture speaks the encoding the resolver reads, so it is
	// in the resolver's spelling rather than one of its own.
	//nolint:tagliatelle // the fixture speaks the encoding the resolver reads, so it is
	// in the resolver's spelling rather than one of its own.
	golum, err := json.Marshal(struct {
		Name         string         `json:"name"`
		Level        int            `json:"level"`
		Ability      map[string]int `json:"ability"`
		HitPoints    int            `json:"hp"`
		MaxHitPoints int            `json:"max_hp"`
		ArmourClass  int            `json:"ac"`
	}{
		Name:         "Golum",
		Level:        3,
		Ability:      map[string]int{"strength": 10, "dexterity": 10},
		HitPoints:    40,
		MaxHitPoints: 40,
		ArmourClass:  40,
	})
	if err != nil {
		t.Fatalf("encoding the defender: %v", err)
	}

	table, err := rules.NewState(4, []rules.Object{
		{ID: attackerID, Kind: rules.KindToken, Data: []byte(attackerBody)},
		{ID: defenderID, Kind: rules.KindToken, Data: golum},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	misses, hits := 0, 0

	for seed := byte(1); seed < 41; seed++ {
		mutations, err := system.Apply(
			t.Context(), aCall(t, domain.RoleGM, seed), table, anAttack(t, attackerID, defenderID),
		)
		if err != nil {
			t.Fatalf("seed %d: Apply: %v", seed, err)
		}

		switch len(mutations) {
		case 1:
			misses++

			if mutations[0].Target != attackerID || mutations[0].Op != dnd5e.OpAttack {
				t.Errorf("seed %d: the mutation is %s, want attack on %s",
					seed, mutations[0], attackerID)
			}
		case 2:
			hits++

			if mutations[1].Target != defenderID {
				t.Errorf("seed %d: the second mutation is %s, want one on %s",
					seed, mutations[1], defenderID)
			}
		default:
			t.Fatalf(
				"seed %d: an attack resolved to %d mutations, want 1 or 2",
				seed,
				len(mutations),
			)
		}
	}

	if misses == 0 || hits == 0 {
		t.Errorf("forty seeds produced %d misses and %d hits; both branches must be exercised, "+
			"and a test that only ever saw one of them would certify half a rule", misses, hits)
	}
}

// TestTheEngineDrawsOnlyFromTheSeedsItIsGiven is the property the whole labelled-draw
// design exists for, asserted on the system rather than through the audit.
//
// **Both directions**, and the second is the one that matters: many seeds producing one
// answer would mean the draw is not reaching the result at all, which is the state the
// determinism audit *cannot* detect — an audit that runs the same scenario a hundred
// times is perfectly happy certifying a resolver whose answer never varies.
func TestTheEngineDrawsOnlyFromTheSeedsItIsGiven(t *testing.T) {
	t.Parallel()

	system := anEngine(t)
	table := aTable(t)
	intent := anAttack(t, attackerID, defenderID)

	first, err := system.Apply(t.Context(), aCall(t, domain.RoleGM, 3), table, intent)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	again, err := system.Apply(t.Context(), aCall(t, domain.RoleGM, 3), table, intent)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !bytes.Equal(encodeAll(first), encodeAll(again)) {
		t.Error("the same seed and the same state gave two different answers")
	}

	// Sixteen seeds rather than one: an attack rolls a d20 and a d12, so any two seeds
	// agree about both by chance about one time in 240, and sixteen makes a coincidence
	// something a reader does not have to reason about.
	diverged := false

	for seed := byte(4); seed < 20; seed++ {
		elsewhere, err := system.Apply(t.Context(), aCall(t, domain.RoleGM, seed), table, intent)
		if err != nil {
			t.Fatalf("seed %d: Apply: %v", seed, err)
		}

		if !bytes.Equal(encodeAll(first), encodeAll(elsewhere)) {
			diverged = true

			break
		}
	}

	if !diverged {
		t.Error(
			"sixteen different seeds produced one answer, so the draw is not reaching the result",
		)
	}
}

// TestTheGmOnlyOperationsAreRefusedBeforeTheTableIsRead is §7.2's enforcement point,
// asserted against an **empty** state.
//
// An empty state is the strongest form: a resolver that checked the tabletop first
// would report `no_such_placement` here, and the wire would tell a player's table
// something about its own contents. The refusal has to come from the role.
func TestTheGmOnlyOperationsAreRefusedBeforeTheTableIsRead(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	empty, err := rules.NewState(0, nil)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	for _, op := range dnd5e.GMOnlyOps() {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(op, attackerID, nil)
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			mutations, err := system.Apply(
				t.Context(),
				aCall(t, domain.RolePlayer, 3),
				empty,
				intent,
			)
			if !errors.Is(err, dnd5e.ErrGMOnly) {
				t.Fatalf("want %v, got %v", dnd5e.ErrGMOnly, err)
			}

			if len(mutations) > 0 {
				t.Errorf("a refused operation returned %d mutation(s)", len(mutations))
			}
		})
	}
}

// TestTheGmMayAdjudicate is the other direction, and a suite written only about refusal
// would certify a system nobody at the table can use.
//
// **Real arguments per operation**, and that is the difference from the suite's own
// role audit: `conformance` builds its GM-only intents with *no* arguments, because it
// cannot know this system's encoding, and it therefore checks only that the GM is not
// refused as `not_permitted`. A test that copied that would pass against a system whose
// every GM operation was broken past the role check, so this one supplies the arguments
// an adjudicator would actually send and requires success.
func TestTheGmMayAdjudicate(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	for _, op := range dnd5e.GMOnlyOps() {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(op, attackerID, adjudicationArgs(op))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			mutations, err := system.Apply(
				t.Context(),
				aCall(t, domain.RoleGM, 3),
				aTable(t),
				intent,
			)
			if err != nil {
				t.Fatalf("the GM was refused %q: %v", op, err)
			}

			if len(mutations) == 0 {
				t.Errorf("%q resolved to nothing, so the GM cannot perform it at all", op)
			}
		})
	}
}

// adjudicationArgs returns what a GM would actually send for each GM-only operation.
//
// A map keyed by op rather than a slice of fixtures, so that adding a GM-only operation
// without an entry here fails the table lookup rather than silently sending that
// operation's arguments to a different one.
func adjudicationArgs(op rules.Op) []byte {
	switch op {
	case dnd5e.OpApplyStatus:
		return []byte(`{"hp":40,"max_hp":49,"clear_condition":"poisoned"}`)
	default:
		// `remove_token` takes none: it is the one operation whose subject is the
		// placement's presence rather than its body.
		return nil
	}
}

// TestAPlayerMayActOnlyForItsOwnCreature is §7.2's second half, and it is a rule
// **independent** of the GM-only set: it holds for `heal`, which the GM may also
// perform, which is why it is a separate refusal rather than a consequence.
//
// **The actor is varied, not the role.** A fixture that varied the role would be
// re-testing the GM-only table; the question here is whether a player may act for a
// token placed for a *different* account, and that is only answerable by holding the
// role fixed and changing the actor.
func TestAPlayerMayActOnlyForItsOwnCreature(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	intent, err := rules.NewIntent(dnd5e.OpHeal, attackerID, []byte(`{"amount":5}`))
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	mutations, err := system.Apply(
		t.Context(), aCallFor(t, domain.RolePlayer, 99, 3), aTable(t), intent,
	)
	if !errors.Is(err, dnd5e.ErrNotYourCreature) {
		t.Fatalf("want %v, got %v", dnd5e.ErrNotYourCreature, err)
	}

	if len(mutations) > 0 {
		t.Errorf("a refused heal returned %d mutation(s)", len(mutations))
	}

	// The GM is not subject to it, and that is the whole of §7.2: the GM acts for the
	// table rather than for itself.
	if _, err := system.Apply(
		t.Context(), aCallFor(t, domain.RoleGM, 99, 3), aTable(t), intent,
	); err != nil {
		t.Errorf("the GM was refused a heal on somebody else's token: %v", err)
	}
}

// TestTheEngineIgnoresAPlacementWhoseKindItDoesNotDeclare is S-14.7 asserted on the
// system rather than through the audit, and the **empty** state is the strongest form:
// a resolver that validated kinds would find nothing here either, so the fixture
// cannot pass for a validator.
func TestTheEngineIgnoresAPlacementWhoseKindItDoesNotDeclare(t *testing.T) {
	t.Parallel()

	state, err := rules.NewState(3, []rules.Object{
		{ID: attackerID, Kind: rules.KindToken, Data: []byte(attackerBody)},
		{ID: defenderID, Kind: rules.KindToken, Data: []byte(defenderBody)},
		{
			ID:   conformance.SurplusObjectID,
			Kind: conformance.SurplusKind,
			Data: []byte(`{"note":"content a plugin may not understand"}`),
		},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	mutations, err := anEngine(t).Apply(
		t.Context(), aCall(t, domain.RoleGM, 3), state, anAttack(t, attackerID, defenderID),
	)
	if err != nil {
		t.Fatalf("a tabletop carrying a foreign kind was refused: %v", err)
	}

	for _, mutation := range mutations {
		if mutation.Target == conformance.SurplusObjectID {
			t.Errorf("the resolution addressed the foreign object: %s", mutation)
		}
	}
}

// TestArgumentsAreReadStrictly is the reason `decodeArgs` uses
// `DisallowUnknownFields`, asserted from both directions.
//
// A mistyped argument name would otherwise be **silently absent**, and the resolution
// would report a number that means nothing — the player watches a figure appear and has
// no way to know it was computed without what they sent. The second case is the cost of
// strictness stated honestly: a client built against a newer server is told so.
func TestArgumentsAreReadStrictly(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	for _, args := range []string{
		`{"exprs":"1d20"}`,         // a misspelled field name
		`{"expr":"1d20","dc":"5"}`, // a field of the wrong type
		`not json at all`,          //
	} {
		t.Run(strconv.Quote(args), func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(dnd5e.OpRoll, attackerID, []byte(args))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			if _, err := system.Apply(
				t.Context(), aCall(t, domain.RolePlayer, 3), aTable(t), intent,
			); !errors.Is(err, dnd5e.ErrBadArguments) {
				t.Errorf("want %v, got %v", dnd5e.ErrBadArguments, err)
			}
		})
	}

	// An intent with **no** arguments at all is refused too, rather than reaching the
	// resolver as an empty expression: "you sent nothing" and "you sent something I
	// could not read" are the same wire word and different log lines.
	intent, err := rules.NewIntent(dnd5e.OpRoll, attackerID, nil)
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	if _, err := system.Apply(
		t.Context(), aCall(t, domain.RolePlayer, 3), aTable(t), intent,
	); !errors.Is(err, dnd5e.ErrBadArguments) {
		t.Errorf("an argument-less %q: want %v, got %v", dnd5e.OpRoll, dnd5e.ErrBadArguments, err)
	}
}

// TestAnApplyStatusThatStatedNothingIsRefused is the one operation whose arguments are
// all optional, and the case where "all optional" would otherwise mean "silently
// nothing happened".
//
// `rules.System.Apply` says returning no mutations with no error is a legitimate answer,
// so an `apply_status` whose every field was mistyped would succeed and change nothing
// — which is the one answer a client cannot tell from success.
func TestAnApplyStatusThatStatedNothingIsRefused(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	intent, err := rules.NewIntent(dnd5e.OpApplyStatus, attackerID, []byte(`{}`))
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	if _, err := system.Apply(
		t.Context(), aCall(t, domain.RoleGM, 3), aTable(t), intent,
	); !errors.Is(err, dnd5e.ErrBadArguments) {
		t.Errorf("want %v, got %v", dnd5e.ErrBadArguments, err)
	}
}

// TestOnlyTheRemovalSetsTheRemoveFlag is the one flag in `rules.Mutation` that is not an
// op, and getting it from the op's name would put the hub's removal-version semantics
// into a string.
func TestOnlyTheRemovalSetsTheRemoveFlag(t *testing.T) {
	t.Parallel()

	system := anEngine(t)

	intent, err := rules.NewIntent(dnd5e.OpRemoveToken, attackerID, nil)
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	mutations, err := system.Apply(t.Context(), aCall(t, domain.RoleGM, 3), aTable(t), intent)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(mutations) != 1 || !mutations[0].Remove {
		t.Fatalf("the removal resolved to %s, want one mutation marked removed", render(mutations))
	}

	// And the converse, because the flag has to be *only* the removal's: a heal on a
	// token nobody removed must not carry it.
	heal, err := rules.NewIntent(dnd5e.OpHeal, attackerID, []byte(`{"amount":1}`))
	if err != nil {
		t.Fatalf("building the heal: %v", err)
	}

	mutations, err = system.Apply(t.Context(), aCall(t, domain.RoleGM, 3), aTable(t), heal)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, mutation := range mutations {
		if mutation.Remove {
			t.Errorf("a heal resolved to a removal: %s", mutation)
		}
	}
}

// TestTheEngineAnswersItsDeclaredViewsAndRefusesTheRest is §10.6 from the system's
// side: a view it declares renders, and a view it does not is refused rather than
// answered from nothing.
func TestTheEngineAnswersItsDeclaredViewsAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	system := anEngine(t)
	state := aTable(t)

	for _, view := range system.Views() {
		t.Run(view.Name, func(t *testing.T) {
			t.Parallel()

			query := rules.Query{View: view.Name, Object: attackerID}

			payload, err := system.Derive(state, query)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}

			if payload.View() != view.Name {
				t.Errorf("the payload answers %q, not %q; the renderer is chosen by the view",
					payload.View(), view.Name)
			}
		})
	}

	for _, name := range []string{"character-sheet", "", "stat block"} {
		if _, err := system.Derive(state, rules.Query{View: name, Object: attackerID}); err == nil {
			t.Errorf(
				"%q was answered rather than refused; a card rendered from nothing is a blank "+
					"document that says nothing went wrong",
				name,
			)
		}
	}
}

// TestAStatBlockAboutSomethingThatIsNotATokenIsRefused is the kind check, and it is a
// real one: `readCreature` decodes any object's `Data`, so a page-derived placement
// would render as a creature at 0 of 0 rather than be refused.
func TestAStatBlockAboutSomethingThatIsNotATokenIsRefused(t *testing.T) {
	t.Parallel()

	state, err := rules.NewState(2, []rules.Object{
		{ID: attackerID, Kind: rules.KindToken, Data: []byte(attackerBody)},
		{ID: "map_1", Kind: rules.KindScene, Data: []byte(`{"name":"the cellar"}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	_, err = anEngine(t).Derive(state, rules.Query{View: dnd5e.ViewStatBlock, Object: "map_1"})
	if !errors.Is(err, dnd5e.ErrNoSuchCreature) {
		t.Errorf("want %v, got %v", dnd5e.ErrNoSuchCreature, err)
	}
}

// TestAConditionAPackDoesNotDeclareIsDroppedRatherThanFatal is S-3.3's inertness in
// the direction a pack removal produces.
//
// A campaign whose pack removed a condition keeps tokens carrying its slug, and
// refusing to *read* one would make **every** resolution against that creature fail —
// which is §10.8's forbidden failure, a plugin removal breaking resolutions that have
// nothing to do with it. The refusal has to happen on write.
func TestAConditionAPackDoesNotDeclareIsDroppedRatherThanFatal(t *testing.T) {
	t.Parallel()

	// A kobold carrying `poisoned` and `beserked`. `beserked` is not a 5e condition and
	// is not in this pack; it stands in for a condition a *different* pack declares.
	stale, err := json.Marshal(map[string]any{
		"name": "Kobold", "level": 3,
		"ability": map[string]int{"dexterity": 15},
		"hp":      12, "max_hp": 12, "ac": 12,
		"conditions": []string{"beserked", "poisoned"},
	})
	if err != nil {
		t.Fatalf("encoding the token: %v", err)
	}

	table, err := rules.NewState(1, []rules.Object{
		{ID: defenderID, Kind: rules.KindToken, Data: stale},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	intent, err := rules.NewIntent(
		dnd5e.OpApplyCondition,
		defenderID,
		[]byte(`{"condition":"prone"}`),
	)
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	mutations, err := anEngine(t).Apply(
		t.Context(), aCall(t, domain.RoleGM, 3), table, intent,
	)
	if err != nil {
		t.Fatalf("a token carrying a slug this pack does not declare was refused: %v", err)
	}

	if len(mutations) != 1 {
		t.Fatalf("resolved to %d mutations, want 1", len(mutations))
	}

	// `beserked` is gone and the two that remain are in the **pack's** order, which is
	// `poisoned` before `prone` — the reverse of the order they were acquired in, and
	// the reason `pruneConditions` exists.
	if bytes.Contains(mutations[0].Args, []byte("beserked")) {
		t.Errorf("the mutation carried a slug this pack does not declare: %s", mutations[0].Args)
	}

	body := mutations[0].Args

	poisonedAt := bytes.Index(body, []byte(`"poisoned"`))
	proneAt := bytes.Index(body, []byte(`"prone"`))

	if poisonedAt < 0 || proneAt < 0 || poisonedAt > proneAt {
		t.Errorf("the conditions are not in the pack's declaration order: %s", mutations[0].Args)
	}
}

// TestAConditionThisPackDoesNotDeclareCannotBeApplied is the other half of the same
// decision: dropping is for what a token *carries*, and **applying** one is a claim
// about the pack's vocabulary, which is a refusal.
//
// The distinction is ADR 0012's third house-rule example: a GM who switches `poisoned`
// off has made it not a condition, and applying it would be the rule silently not
// applying.
func TestAConditionThisPackDoesNotDeclareCannotBeApplied(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"beserked", "cowering"} {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(
				dnd5e.OpApplyCondition,
				defenderID,
				[]byte(`{"condition":"`+slug+`"}`),
			)
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			if _, err := anEngine(t).Apply(
				t.Context(), aCall(t, domain.RoleGM, 3), aTable(t), intent,
			); !errors.Is(err, dnd5e.ErrNoSuchCondition) {
				t.Errorf("want %v, got %v", dnd5e.ErrNoSuchCondition, err)
			}
		})
	}
}

// TestAResolverThatIsCancelledReturnsNothing is `rules.System.Apply`'s own rule, and the
// reason it is worth a test: a list half-computed on the way to being discarded is a
// list whose contents depend on **when** the hub cancelled, which is the one input
// S-14.6 says resolution may not depend on.
func TestAResolverThatIsCancelledReturnsNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	mutations, err := anEngine(
		t,
	).Apply(ctx, aCall(t, domain.RoleGM, 3), aTable(t), anAttack(t, attackerID, defenderID))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want %v, got %v", context.Canceled, err)
	}

	if len(mutations) > 0 {
		t.Errorf("a cancelled resolution returned %d mutation(s)", len(mutations))
	}
}

// TestTheSuiteStillHasTheFiveAuditsItDocuments holds the count where it belongs rather
// than assuming it, because a `Check` that certified three audits would print `ok`.
//
// The suite documents five and runs five, and this asserts that the documentation and
// the running set have not parted. It costs one comparison and it is the assertion that
// makes "the conformance suite passes" a sentence with a number in it.
func TestTheSuiteStillHasTheFiveAuditsItDocuments(t *testing.T) {
	t.Parallel()

	audits := conformance.Audits()
	if len(audits) != 5 {
		t.Fatalf("the suite runs %d audits and documents five", len(audits))
	}
}

// # Fixtures

func anEngine(t *testing.T) *dnd5e.Engine {
	t.Helper()

	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	return engine
}

func aTable(t *testing.T) rules.State {
	t.Helper()

	state, err := rules.NewState(9, []rules.Object{
		{ID: attackerID, Kind: rules.KindToken, Data: []byte(attackerBody)},
		{ID: defenderID, Kind: rules.KindToken, Data: []byte(defenderBody)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	return state
}

// The actor the scenario's token is placed for, and the one every call defaults to.
const scenarioActor = 7

func aCall(t *testing.T, role domain.Role, seedByte byte) rules.Context {
	t.Helper()

	return aCallFor(t, role, scenarioActor, seedByte)
}

// aCallFor is `aCall` with the actor stated, which the ownership rule needs and the
// seed alone cannot express.
//
// **A separate constructor rather than a struct field to set afterwards**, because the
// actor identity and the seed are the two fields a careless caller copies from the wrong
// place — one is *whose* token this is and the other is *which game* is being played —
// and `rules.NewContext`'s comment makes the same argument about the two fields a
// `Context` carries.
func aCallFor(t *testing.T, role domain.Role, actor int64, seedByte byte) rules.Context {
	t.Helper()

	call, err := rules.NewContext(42, actor, role, rules.Seed{seedByte, seedByte, seedByte})
	if err != nil {
		t.Fatalf("building a rule context for role %q: %v", role, err)
	}

	return call
}

// two tokens it was about.
//
//nolint:unparam // both ids are named so a reader of a failing assertion sees which
func anAttack(t *testing.T, attacker, defender rules.ObjectID) rules.Intent {
	t.Helper()

	args := []byte(`{"attack":"` + attackName + `","defender":"` + defender.String() + `"}`)

	intent, err := rules.NewIntent(dnd5e.OpAttack, attacker, args)
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	return intent
}

// aSeedThatLands finds a seed whose attack connects, so a test can assert on a landed
// attack's shape without asserting on which way a die fell.
//
// **A search rather than a hardcoded byte**, and the reason is worth stating: a
// hardcoded seed is a golden value that changes the moment the pack's numbers change,
// and a test that fails because a proficiency bonus moved is a test about the pack
// wearing a resolver's clothes. Sixteen seeds is more than the hit probability clears.
func aSeedThatLands(t *testing.T, system *dnd5e.Engine) byte {
	t.Helper()

	table := aTable(t)
	intent := anAttack(t, attackerID, defenderID)

	for seed := byte(1); seed < 40; seed++ {
		mutations, err := system.Apply(t.Context(), aCall(t, domain.RoleGM, seed), table, intent)
		if err != nil {
			t.Fatalf("seed %d: Apply: %v", seed, err)
		}

		if len(mutations) == 2 {
			return seed
		}
	}

	t.Fatal("no seed in 1..39 landed an attack; the fixture cannot reach a landed attack")

	return 0
}

// encodeAll renders a mutation list for comparison, payloads and all.
//
// **The payloads, deliberately**, and it is the opposite of what `rules.Mutation.String`
// does: that one withholds a payload because a payload is where a resolved roll lives
// and S-12.3 forbids one reaching a log. This is a *test*, nothing is logged, and the
// determinism claim is about the bytes a mutation carries — comparing op names alone
// would pass a resolver whose dice changed on every run, which is the precise failure
// the determinism audit exists to prevent.
func encodeAll(mutations []rules.Mutation) []byte {
	var out bytes.Buffer

	for _, mutation := range mutations {
		out.WriteString(mutation.String())
		out.WriteByte(' ')
		out.Write(mutation.Args)
		out.WriteByte('\n')
	}

	return out.Bytes()
}

// render lists mutations without their payloads, for a failure message.
func render(mutations []rules.Mutation) string {
	if len(mutations) == 0 {
		return "nothing"
	}

	parts := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		parts = append(parts, mutation.String())
	}

	return "[" + strings.Join(parts, ", ") + "]"
}
