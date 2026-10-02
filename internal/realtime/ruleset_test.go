// The tests for ruleset-version gating: the fingerprint, the refusal, and the
// audited discard (D17, D18).
//
// # The claim under test is a negative
//
// ADR 0018's decision is what the fingerprint **excludes**. An exclusion is the
// hardest kind of claim to hold, because the test that passes when the exclusion is
// broken is the test that passes when the fingerprint is a constant: a fingerprint of
// "" never changes, so "a house-rule toggle does not change it" is true.
//
// So every exclusion assertion here is paired with a non-vacuity assertion, and each
// of the three properties the brief names is a separate test rather than a table
// row:
//
//   - `TestTheFingerprintExcludesHouseRules` toggles, adds, removes and **reorders**
//     house rules and requires the encoded bytes to be identical — and requires the
//     fingerprint to be non-empty, to carry all four of its inputs, and to be
//     distinguishable from `Fingerprint{}`. Without the last one the test would pass
//     against a function returning the zero value.
//   - `TestTheFingerprintChangesWithAPackVersion` requires each of the four inputs
//     to move the fingerprint. This is the mutation check on the exclusion test: if
//     any of them did not, the exclusion test would be satisfied by a fingerprint
//     that never changes.
//   - `TestTheFingerprintIsNeverEmpty` requires a descriptor of nothing to be
//     refused, so a fingerprint that *is* the empty string is not constructible at
//     all.
//
// # The database is real
//
// The discard's subject is a `campaign_state` row and its record is an `audit_log`
// row, and S-7.8 is a statement about both. A spy would assert that a function was
// called, which is a different statement about a different thing and passes just as
// happily against a column list that does not match the table. So the fixture is a
// migrated database, the same reason `state_test.go` gives, and the assertions read
// the tables.
//
// The one place a double is used is the injected clock, and only because the audit
// row's `created_at` is part of what is asserted and an assertion about "a moment
// ago" is an assertion about a tolerance.

package realtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// The campaign the fixture resolves to. A base pack revision is the most realistic
// drift — a data pack ships more often than a system does — so the pack is the
// variable the mutation in ADR 0018's alternatives-considered is written against.
const (
	fixtureSystem  = "5e-2024"
	fixtureRuleset = "5e-2024.1"
	fixtureBase    = "core-1.0.0"
	fixtureOverlay = "2024-1.0.0"
)

// theGM is the actor a successful discard records. A real account id, because
// `audit_log.actor_id` is NOT NULL and the row's existence is the assertion.
var theGM = realtime.Actor{UserID: 7, Username: "mara"}

// TestTheFingerprintExcludesHouseRules is ADR 0018's decision, asserted.
//
// The four mutations are the four things a GM can do to a house rule, and the last
// one is the one a reader is most likely to think should matter: **reordering**.
// ADR 0018 requires conflicts to resolve first-match-wins by `position`, so a
// fingerprint that read the order would strand a campaign over a re-order that
// changes nothing except which of two modules answers a conflict the campaign is not
// currently hitting.
func TestTheFingerprintExcludesHouseRules(t *testing.T) {
	t.Parallel()

	baseline := descriptor()
	withoutRules, err := realtime.FingerprintOf(baseline)
	if err != nil {
		t.Fatalf("FingerprintOf() error = %v, want nil", err)
	}

	testCases := []struct {
		name  string
		rules []realtime.HouseRule
	}{
		{
			name:  "no house rules at all",
			rules: nil,
		},
		{
			name:  "one module enabled",
			rules: []realtime.HouseRule{{ModuleID: "critical_hits", Enabled: true}},
		},
		{
			name:  "the same module disabled",
			rules: []realtime.HouseRule{{ModuleID: "critical_hits", Enabled: false}},
		},
		{
			name: "three modules in a different declared order",
			rules: []realtime.HouseRule{
				{ModuleID: "flanking_optional", Position: 30, Enabled: true},
				{ModuleID: "crit_on_any_20", Position: 20, Enabled: true},
				{ModuleID: "no_falling_damage", Position: 10, Enabled: true},
			},
		},
		{
			name: "one module, position 9000",
			rules: []realtime.HouseRule{
				{ModuleID: "no_falling_damage", Position: 9000, Enabled: true},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mutated := baseline
			mutated.HouseRules = tc.rules

			got, err := realtime.FingerprintOf(mutated)
			if err != nil {
				t.Fatalf("FingerprintOf() error = %v, want nil", err)
			}

			if got != withoutRules {
				t.Errorf(
					"FingerprintOf() = %v, want %v; house-rule configuration is deliberately "+
						"excluded from the fingerprint (ADR 0018) so a GM enabling a house rule "+
						"is not refused on their own campaign",
					got, withoutRules,
				)
			}

			// The non-vacuity half, and the reason this test is not satisfied by a
			// fingerprint function that returns a constant. Without it, an
			// implementation returning `Fingerprint{}` passes every case above.
			if withoutRules.Empty() {
				t.Fatal("the baseline fingerprint is empty, so the exclusion assertions above " +
					"prove nothing: an empty fingerprint never changes either")
			}

			if got.String() != withoutRules.String() {
				t.Errorf("encoded form = %q, want %q; the persisted form is what the column holds",
					got.String(), withoutRules.String(),
				)
			}
		})
	}
}

// TestTheFingerprintChangesWithAPackVersion is the mutation check on the exclusion
// test.
//
// Each input is changed alone, and each must move the fingerprint. If any of them did
// not, the fingerprint would be partly vacuous and the exclusion test would be
// satisfied by a value that carries no information.
func TestTheFingerprintChangesWithAPackVersion(t *testing.T) {
	t.Parallel()

	baseline, err := realtime.FingerprintOf(descriptor())
	if err != nil {
		t.Fatalf("FingerprintOf() error = %v, want nil", err)
	}

	testCases := []struct {
		name   string
		mutate func(*realtime.Descriptor)
	}{
		{
			name:   "base pack version",
			mutate: func(d *realtime.Descriptor) { d.BasePack = "core-1.1.0" },
		},
		{
			name:   "overlay pack version",
			mutate: func(d *realtime.Descriptor) { d.OverlayPack = "2024-2.0.0" },
		},
		{
			name:   "the system's own ruleset version",
			mutate: func(d *realtime.Descriptor) { d.Ruleset = "5e-2024.2" },
		},
		{
			name:   "the system id",
			mutate: func(d *realtime.Descriptor) { d.System = "5e-2014" },
		},
		{
			name: "an overlay appearing where there was none",
			mutate: func(d *realtime.Descriptor) {
				d.BasePack = fixtureBase
				d.OverlayPack = ""
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mutated := descriptor()
			tc.mutate(&mutated)

			got, err := realtime.FingerprintOf(mutated)
			if err != nil {
				t.Fatalf("FingerprintOf() error = %v, want nil", err)
			}

			if got.Equal(baseline) {
				t.Errorf("FingerprintOf() = %v, want it to differ from %v; %s is an input to the "+
					"fingerprint, and a fingerprint that ignored it would let a campaign resume "+
					"under semantics its persisted mutations were not resolved with",
					got, baseline, tc.name,
				)
			}
		})
	}
}

// TestTheFingerprintIsNeverEmpty closes the vacuous pass structurally.
//
// An empty fingerprint is indistinguishable from a campaign written under no
// particular ruleset, which migration 0005 says is a *meaningful* value. So one value
// would mean two things, and a GM would be told to discard a game over a column that
// was merely unset.
func TestTheFingerprintIsNeverEmpty(t *testing.T) {
	t.Parallel()

	fields := []struct {
		name   string
		mutate func(*realtime.Descriptor)
	}{
		{"no system", func(d *realtime.Descriptor) { d.System = "" }},
		{"no ruleset version", func(d *realtime.Descriptor) { d.Ruleset = "" }},
		{"no base pack", func(d *realtime.Descriptor) { d.BasePack = "" }},
	}

	for _, tc := range fields {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mutated := descriptor()
			tc.mutate(&mutated)

			got, err := realtime.FingerprintOf(mutated)
			if err == nil {
				t.Fatalf(
					"FingerprintOf() = %v, want an error: %s is not a legal fingerprint and a "+
						"fingerprint of nothing cannot be told from a campaign with no ruleset",
					got,
					tc.name,
				)
			}

			if !got.Empty() {
				t.Errorf(
					"FingerprintOf() returned %v alongside its error, want the zero value",
					got,
				)
			}
		})
	}

	t.Run("nothing at all", func(t *testing.T) {
		t.Parallel()

		if _, err := realtime.FingerprintOf(realtime.Descriptor{}); err == nil {
			t.Error("FingerprintOf(Descriptor{}) = nil error, want one")
		}
	})
}

// TestTheFingerprintRoundTrips holds the encoding's whole reason for existing.
//
// The column is one TEXT value, and ADR 0018 requires a status page to *name* the
// persisted version, so the encoded form has to be readable as well as deterministic.
func TestTheFingerprintRoundTrips(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		descriptor realtime.Descriptor
	}{
		{"with an overlay", descriptor()},
		{
			name: "a standalone pack, with no overlay",
			descriptor: realtime.Descriptor{
				System:   "pf2e",
				Ruleset:  "pf2e-1.2.3",
				BasePack: "pathfinder-core-4.0.0",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, err := realtime.FingerprintOf(tc.descriptor)
			if err != nil {
				t.Fatalf("FingerprintOf() error = %v, want nil", err)
			}

			encoded := want.String()

			got, err := realtime.ParseFingerprint(encoded)
			if err != nil {
				t.Fatalf("ParseFingerprint(%q) error = %v, want nil", encoded, err)
			}

			if !got.Equal(want) {
				t.Errorf("ParseFingerprint(%q) = %v, want %v", encoded, got, want)
			}

			if got.String() != encoded {
				t.Errorf(
					"re-encoding = %q, want %q; the encoding is not deterministic",
					got.String(),
					encoded,
				)
			}
		})
	}
}

// TestParseFingerprintRefusesWhatItCannotCompare holds the fail-closed side.
//
// A column can hold anything. A value this build cannot read is refused rather than
// guessed at, because guessing is the "silently misresolve" outcome §10.8 exists to
// prevent — and it is *not* reported as drift, because telling a GM to discard a game
// over an unparseable column is the software inflicting the loss.
func TestParseFingerprintRefusesWhatItCannotCompare(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"a bare hash from a build that does not exist", "sha256:deadbeef"},
		{"truncated", "sp1:system=5e;ruleset=5e-2024.1"},
		{
			"components out of order",
			"sp1:ruleset=5e-2024.1;system=5e-2024;base=core-1.0.0;overlay=",
		},
		{"an unknown component", "sp1:system=5e;housrule=x;base=core-1.0.0;overlay="},
		{"a component with no value", "sp1:system=5e;ruleset;base=core-1.0.0;overlay="},
		{
			"a component that is empty where it may not be",
			"sp1:system=;ruleset=5e-2024.1;base=c;overlay=",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := realtime.ParseFingerprint(tc.encoded)
			if err == nil {
				t.Fatalf("ParseFingerprint(%q) = %v, want an error", tc.encoded, got)
			}

			if !errors.Is(err, realtime.ErrRulesetUnreadable) {
				t.Errorf("ParseFingerprint(%q) error = %v, want it to wrap ErrRulesetUnreadable",
					tc.encoded, err)
			}
		})
	}
}

// TestAMatchingFingerprintResumes is the positive case, and it goes all the way to a
// live state with the placements in it.
//
// A test that stopped at "Check returned nil" would pass against a gate that never
// opened anything, and the claim being made is that a campaign whose fingerprint
// matches resumes — so the assertion is the tabletop.
func TestAMatchingFingerprintResumes(t *testing.T) {
	fixture := newFixture(t, descriptor(), func(int64) string {
		return fingerprintOf(t, descriptor()).String()
	})
	fixture.seedState(t, 3, 2)

	registry := fixture.registry(t)

	state, err := fixture.gate().Resume(t.Context(), registry, fixture.campaignID)
	if err != nil {
		t.Fatalf("Resume() error = %v, want nil: a matching fingerprint must resume", err)
	}

	if state == nil {
		t.Fatal("Resume() = nil state, want the campaign's live state")
	}

	if got := len(state.Snapshot().Placements); got != 2 {
		t.Errorf("resumed with %d placements, want the 2 that were persisted", got)
	}

	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})
}

// TestMismatchRefusesResume is §10.8's row, and it is the test that would matter most
// if it were weak.
//
// Four things are asserted, and each is a claim the message has to earn: the resume is
// refused, the refusal is `ErrRulesetDrift`, **no state was opened** — a refusal that
// opened a state and then complained would have already overwritten the evidence —
// and the message names the persisted version, the expected version, the input that
// differs, and the consequence. Four separate checks, because a message that named
// only the first three would read as finished.
func TestMismatchRefusesResume(t *testing.T) {
	persisted := driftedDescriptor()

	registered := descriptor()
	registered.BasePack = "core-1.1.0"

	fixture := newFixture(t, registered, func(int64) string {
		return fingerprintOf(t, persisted).String()
	})
	fixture.seedState(t, 9, 3)

	registry := fixture.registry(t)
	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	_, err := fixture.gate().Resume(t.Context(), registry, fixture.campaignID)
	if err == nil {
		t.Fatal("Resume() = nil error, want a refusal: the base pack moved from 1.0.0 to 1.1.0")
	}

	if !errors.Is(err, realtime.ErrRulesetDrift) {
		t.Errorf("Resume() error = %v, want it to wrap ErrRulesetDrift", err)
	}

	// The order is the gate. A state opened before the check has been made has
	// already been written under the new fingerprint, so there is nothing left to
	// report and nothing left to recover.
	if live := registry.Live(); live != 0 {
		t.Errorf(
			"registry.Live() = %d, want 0: a refused resume must not have opened a state",
			live,
		)
	}

	var drift *realtime.DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Resume() error = %v (%T), want a *realtime.DriftError", err, err)
	}

	if drift.Component != realtime.ComponentBasePack {
		t.Errorf("drift.Component = %q, want %q; a refusal that does not say which input moved "+
			"leaves a GM guessing between three things they could fix differently",
			drift.Component, realtime.ComponentBasePack)
	}

	message := drift.Error()

	// The three facts an action needs, asserted separately. "Incompatible" satisfies
	// none of them, and the version of this message that names only the persisted
	// version still reads like a finished sentence.
	assertMentions(t, message, "persisted", fingerprintOf(t, persisted).String())
	assertMentions(t, message, "expected", fingerprintOf(t, registered).String())
	assertMentions(t, message, "consequence", "will not resume")

	// And the class, because the log line and the page must say the same word.
	if got := observability.ErrorClass(drift); got != "ruleset_drift" {
		t.Errorf("ErrorClass(drift) = %q, want %q", got, "ruleset_drift")
	}
}

// TestTheRefusalSaysWhichInputMoved is the `Diff` contract, and the two cases where a
// single-answer implementation is wrong.
//
// `ComponentSeveral` exists because after an upgrade a system and its base pack move
// together, and naming only the first would send a GM to fix one input and discover
// the other waiting.
func TestTheRefusalSaysWhichInputMoved(t *testing.T) {
	t.Parallel()

	baseline, err := realtime.FingerprintOf(descriptor())
	if err != nil {
		t.Fatalf("FingerprintOf() error = %v, want nil", err)
	}

	testCases := []struct {
		name   string
		mutate func(*realtime.Descriptor)
		want   realtime.Component
	}{
		{"identical", func(*realtime.Descriptor) {}, realtime.ComponentNone},
		{
			"base pack only",
			func(d *realtime.Descriptor) { d.BasePack = "x" },
			realtime.ComponentBasePack,
		},
		{
			"overlay only",
			func(d *realtime.Descriptor) { d.OverlayPack = "x" },
			realtime.ComponentOverlayPack,
		},
		{"system only", func(d *realtime.Descriptor) { d.System = "x" }, realtime.ComponentSystem},
		{
			"ruleset only",
			func(d *realtime.Descriptor) { d.Ruleset = "x" },
			realtime.ComponentRuleset,
		},
		{
			name: "a system and its base pack together",
			mutate: func(d *realtime.Descriptor) {
				d.System = "x"
				d.BasePack = "y"
			},
			want: realtime.ComponentSeveral,
		},
		{
			name: "everything",
			mutate: func(d *realtime.Descriptor) {
				d.System, d.Ruleset, d.BasePack, d.OverlayPack = "a", "b", "c", "d"
			},
			want: realtime.ComponentSeveral,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mutated := descriptor()
			tc.mutate(&mutated)

			got, err := realtime.FingerprintOf(mutated)
			if err != nil {
				t.Fatalf("FingerprintOf() error = %v, want nil", err)
			}

			if component := baseline.Diff(got); component != tc.want {
				t.Errorf("Diff() = %q, want %q", component, tc.want)
			}
		})
	}
}

// TestTheGateFailsClosed holds the three cases where there is no comparison to be had.
//
// All three answer "resumable" if a gate treats an absent answer as an absent
// difference, and a gate that cannot fail is worse than no gate because it is
// trusted.
func TestTheGateFailsClosed(t *testing.T) {
	t.Parallel()

	registered := fingerprintOf(t, descriptor())

	t.Run("no reader is configured", func(t *testing.T) {
		t.Parallel()

		gate := realtime.NewGate(registered, nil)

		err := gate.Check(t.Context(), 1)
		if err == nil {
			t.Fatal("Check() = nil, want a refusal: no reader means no comparison was made")
		}

		if !errors.Is(err, realtime.ErrRulesetUnreadable) {
			t.Errorf("Check() error = %v, want it to wrap ErrRulesetUnreadable", err)
		}
	})

	t.Run("the reader fails", func(t *testing.T) {
		t.Parallel()

		gate := realtime.NewGate(registered, func(context.Context, int64) (string, error) {
			return "", errors.New("the database is closed")
		})

		if err := gate.Check(t.Context(), 1); err == nil {
			t.Error(
				"Check() = nil, want a refusal: an unreadable column is not an absent fingerprint",
			)
		}
	})

	t.Run("the column holds something this build cannot read", func(t *testing.T) {
		t.Parallel()

		gate := realtime.NewGate(registered, func(context.Context, int64) (string, error) {
			return "written by something else", nil
		})

		err := gate.Check(t.Context(), 1)
		if err == nil {
			t.Fatal("Check() = nil, want a refusal")
		}

		if errors.Is(err, realtime.ErrRulesetDrift) {
			t.Error("Check() reported drift for an unparseable column; it is not drift, and " +
				"telling a GM to discard a game over it is the software inflicting the loss")
		}

		if !errors.Is(err, realtime.ErrRulesetUnreadable) {
			t.Errorf("Check() error = %v, want it to wrap ErrRulesetUnreadable", err)
		}
	})
}

// TestAnUnfingerprintedCampaignResumes holds the one case the gate deliberately does
// not refuse.
//
// The empty string is a meaningful value (migration 0005: "state written under no
// particular ruleset"), and there is nothing to compare it against — so there is no
// claim that the semantics differ. Refusing would strand every campaign created
// before this column was populated, with no recovery, which is the outcome ADR 0018
// exists to prevent.
func TestAnUnfingerprintedCampaignResumes(t *testing.T) {
	fixture := newFixture(t, descriptor(), func(int64) string { return "" })
	fixture.seedState(t, 4, 1)

	status, err := fixture.gate().Inspect(t.Context(), fixture.campaignID)
	if err != nil {
		t.Fatalf("Inspect() error = %v, want nil", err)
	}

	if !status.Resumable {
		t.Error("Inspect() reported not resumable for an unfingerprinted campaign, want resumable")
	}

	if !status.Unfingerprinted {
		t.Error("Inspect() did not report the campaign as unfingerprinted, so a status page " +
			"cannot tell 'no ruleset recorded' from 'the column was not read'")
	}

	if status.Drift != nil {
		t.Errorf("Inspect() reported drift %v, want none", status.Drift)
	}
}

// TestTheDiscardIsGMOnly holds S-7.8's "GM-only", at the operation rather than only
// at the route.
//
// Two tiers and two actors, because the two refusals are two properties: a *player* is
// entitled to be in the campaign and is still refused, and an *anonymous* requestor is
// refused for a second reason — there is no `audit_log.actor_id` to write, and a row
// that cannot be attributed is not an audit record.
//
// Each case asserts three things, because a refusal that returned early with a tidy
// error while deleting the row anyway would satisfy one of them: the error, the
// surviving state, and the absence of an audit row.
func TestTheDiscardIsGMOnly(t *testing.T) {
	testCases := []struct {
		name   string
		tier   domain.Tier
		actor  realtime.Actor
		wantIs error
	}{
		{
			name:   "a player",
			tier:   domain.TierPlayer,
			actor:  realtime.Actor{UserID: 9, Username: "ilse"},
			wantIs: realtime.ErrDiscardNotGM,
		},
		{
			name:   "an authenticated non-member",
			tier:   domain.TierReadOnly,
			actor:  realtime.Actor{UserID: 9, Username: "ilse"},
			wantIs: realtime.ErrDiscardNotGM,
		},
		{
			name:   "an anonymous requestor",
			tier:   domain.TierNone,
			actor:  realtime.Actor{UserID: 0},
			wantIs: realtime.ErrDiscardNotGM,
		},
		{
			name:   "a GM with no account behind them",
			tier:   domain.TierGM,
			actor:  realtime.Actor{UserID: 0},
			wantIs: realtime.ErrDiscardNoActor,
		},
		{
			name:   "a tier this build does not recognise",
			tier:   domain.Tier(99),
			actor:  theGM,
			wantIs: realtime.ErrDiscardNotGM,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, descriptor(), func(int64) string {
				return fingerprintOf(t, driftedDescriptor()).String()
			})
			fixture.seedState(t, 5, 2)

			discarder := fixture.discarder(t)

			confirmation, _, err := discarder.Prepare(
				t.Context(),
				fixture.gate(),
				fixture.campaignID,
			)
			if err != nil {
				t.Fatalf("Prepare() error = %v, want nil", err)
			}

			err = discarder.Discard(
				t.Context(),
				fixture.campaignID,
				tc.tier,
				tc.actor,
				confirmation,
			)
			if err == nil {
				t.Fatal("Discard() = nil, want a refusal")
			}

			if !errors.Is(err, tc.wantIs) {
				t.Errorf("Discard() error = %v, want it to wrap %v", err, tc.wantIs)
			}

			// The umbrella, because a route applies S-8's "no access is 404, never
			// 403" with one `errors.Is`.
			if !errors.Is(err, realtime.ErrDiscardForbidden) {
				t.Errorf("Discard() error = %v, want it to wrap ErrDiscardForbidden", err)
			}

			fixture.requireStateIntact(t)
			fixture.requireNoAuditRows(t)
		})
	}
}

// TestTheDiscardIsConfirmationGated holds S-7.8's "confirmation-gated", as four
// separate claims.
//
// The brief's phrasing is "reaching it requires the explicit act, not that a GET does
// it", and that is two assertions plus two more it took me a while to see:
//
//  1. `Prepare` writes nothing. A read path that moved the component closer to a
//     delete would be the "a GET does it" failure.
//  2. A `Discard` with no confirmation is refused. There is no default and no
//     overload, so the zero value is the only value an unconfirmed caller can have.
//  3. A confirmation is not transferable between campaigns.
//  4. A confirmation is not transferable across time: the row moved after it was
//     prepared, so the GM confirmed a game that no longer exists.
func TestTheDiscardIsConfirmationGated(t *testing.T) {
	t.Run("preparing writes nothing", func(t *testing.T) {
		fixture := newFixture(t, descriptor(), func(int64) string {
			return fingerprintOf(t, driftedDescriptor()).String()
		})
		fixture.seedState(t, 6, 2)

		discarder := fixture.discarder(t)

		confirmation, status, err := discarder.Prepare(
			t.Context(),
			fixture.gate(),
			fixture.campaignID,
		)
		if err != nil {
			t.Fatalf("Prepare() error = %v, want nil", err)
		}

		// A confirmation exists, and reading about a campaign got one.
		if confirmation.Revision() != 6 || confirmation.Placements() != 2 {
			t.Errorf("Prepare() bound to revision %d with %d placements, want revision 6 with 2",
				confirmation.Revision(), confirmation.Placements())
		}

		// And nothing moved.
		fixture.requireStateIntact(t)
		fixture.requireNoAuditRows(t)

		// The status comes back with it, so the GM confirms against the versions the
		// alert named rather than a second read.
		if status.Drift == nil {
			t.Error("Prepare() returned a resumable status for a drifted campaign, want the drift")
		}
	})

	t.Run("a discard with no confirmation", func(t *testing.T) {
		fixture := newFixture(t, descriptor(), func(int64) string {
			return fingerprintOf(t, driftedDescriptor()).String()
		})
		fixture.seedState(t, 7, 1)

		discarder := fixture.discarder(t)

		err := discarder.Discard(
			t.Context(), fixture.campaignID, domain.TierGM, theGM, realtime.DiscardConfirmation{},
		)
		if err == nil {
			t.Fatal(
				"Discard() = nil, want a refusal: no confirmation means no evidence a GM read it",
			)
		}

		if !errors.Is(err, realtime.ErrDiscardUnconfirmed) {
			t.Errorf("Discard() error = %v, want it to wrap ErrDiscardUnconfirmed", err)
		}

		fixture.requireStateIntact(t)
		fixture.requireNoAuditRows(t)
	})

	t.Run("a confirmation prepared for another campaign", func(t *testing.T) {
		fixture := newFixture(t, descriptor(), func(int64) string {
			return fingerprintOf(t, driftedDescriptor()).String()
		})
		fixture.seedState(t, 8, 1)
		fixture.seedStateFor(t, 2, 1, 1)

		discarder := fixture.discarder(t)

		confirmation, _, err := discarder.Prepare(t.Context(), fixture.gate(), 2)
		if err != nil {
			t.Fatalf("Prepare() error = %v, want nil", err)
		}

		err = discarder.Discard(t.Context(), 1, domain.TierGM, theGM, confirmation)
		if err == nil {
			t.Fatal("Discard() = nil, want a refusal")
		}

		if !errors.Is(err, realtime.ErrDiscardForeign) {
			t.Errorf("Discard() error = %v, want it to wrap ErrDiscardForeign", err)
		}

		fixture.requireStateIntact(t)
		fixture.requireNoAuditRows(t)
	})

	t.Run("a confirmation for state that has moved on", func(t *testing.T) {
		fixture := newFixture(t, descriptor(), func(int64) string {
			return fingerprintOf(t, driftedDescriptor()).String()
		})
		fixture.seedState(t, 11, 2)

		discarder := fixture.discarder(t)

		confirmation, _, err := discarder.Prepare(t.Context(), fixture.gate(), fixture.campaignID)
		if err != nil {
			t.Fatalf("Prepare() error = %v, want nil", err)
		}

		// The table plays on. This is the ordinary case the check exists for: a GM
		// read the versions, closed the page, and somebody moved a token.
		fixture.seedState(t, 12, 2)

		err = discarder.Discard(t.Context(), fixture.campaignID, domain.TierGM, theGM, confirmation)
		if err == nil {
			t.Fatal("Discard() = nil, want a refusal: the GM confirmed revision 11, not 12")
		}

		if !errors.Is(err, realtime.ErrDiscardStale) {
			t.Errorf("Discard() error = %v, want it to wrap ErrDiscardStale", err)
		}

		// And the sentence names both revisions, so the GM can tell a stale
		// confirmation from a broken one.
		assertMentions(t, err.Error(), "prepared revision", "11")
		assertMentions(t, err.Error(), "current revision", "12")

		// The state at revision 12 is untouched: refusing must not be a partial
		// delete.
		fixture.requireStateRevision(t, 12)
		fixture.requireNoAuditRows(t)
	})
}

// TestTheDiscardWritesAnAuditRowThatSaysWhatItDiscarded is the requirement's whole
// point.
//
// "A destructive, irreversible, GM-only action that leaves no record is the shape of
// the worst possible audit gap." So the row is asserted to exist, and asserted to say
// **what was discarded** — not merely that something happened.
func TestTheDiscardWritesAnAuditRowThatSaysWhatItDiscarded(t *testing.T) {
	persisted := driftedDescriptor()

	registered := descriptor()

	fixture := newFixture(t, registered, func(int64) string {
		return fingerprintOf(t, persisted).String()
	})
	fixture.seedState(t, 21, 4)

	discarder := fixture.discarder(t)

	confirmation, _, err := discarder.Prepare(t.Context(), fixture.gate(), fixture.campaignID)
	if err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}

	if err := discarder.Discard(
		t.Context(), fixture.campaignID, domain.TierGM, theGM, confirmation,
	); err != nil {
		t.Fatalf("Discard() error = %v, want nil", err)
	}

	// The state is gone. A discard that recorded itself and left the game would be
	// the opposite failure, and it is the one a `Delete` in the wrong transaction
	// would produce.
	fixture.requireNoStateRow(t)

	row := fixture.onlyAuditRow(t)

	if got, want := row.action, "state.discard"; got != want {
		t.Errorf("audit_log.action = %q, want %q", got, want)
	}

	if got, want := row.target, "campaign_state/1"; got != want {
		t.Errorf(
			"audit_log.target = %q, want %q; the target names both the table and the row",
			got,
			want,
		)
	}

	if row.actorID != theGM.UserID {
		t.Errorf("audit_log.actor_id = %d, want %d", row.actorID, theGM.UserID)
	}

	if row.createdAt != discardClock {
		t.Errorf("audit_log.created_at = %d, want %d", row.createdAt, discardClock)
	}

	// What was discarded: both versions, the revision, and how much was on the
	// table. Four separate assertions, because the sentence that names the versions
	// and drops the count is a sentence an operator cannot act on.
	assertMentions(
		t,
		row.detail,
		"the version it was written under",
		fingerprintOf(t, persisted).String(),
	)
	assertMentions(
		t,
		row.detail,
		"the version now registered",
		fingerprintOf(t, registered).String(),
	)
	assertMentions(t, row.detail, "the revision it had reached", "revision 21")
	assertMentions(t, row.detail, "how much was on the table", "4 placements")

	// And that the *content* is not in there. A state document is everything on the
	// tabletop, and S-12.3 is enforced everywhere else by `EventAttributes` having no
	// field a document could be passed through; this is the one place in the project
	// that formats something *about* a state.
	for _, placement := range seededPlacements {
		if strings.Contains(row.detail, placement) {
			t.Errorf(
				"audit_log.detail = %q, want it to carry no content: it names the placement %q, "+
					"and a state document is a bag of everything on the tabletop",
				row.detail,
				placement,
			)
		}
	}
}

// TestTheDiscardAndItsAuditRowAreOneTransaction is the pairing's reason.
//
// A best-effort audit line is a gap with extra steps. The direction that matters is
// which way the failure goes: a GM told "the discard failed" still has their game.
// So the audit table is dropped out from under the discard and the state must survive.
//
// The mutation this kills: moving the `INSERT` to its own write after the transaction
// commits, which is the version where a failure to record leaves a deleted game and
// no row.
func TestTheDiscardAndItsAuditRowAreOneTransaction(t *testing.T) {
	fixture := newFixture(t, descriptor(), func(int64) string {
		return fingerprintOf(t, driftedDescriptor()).String()
	})
	fixture.seedState(t, 31, 2)

	discarder := fixture.discarder(t)

	confirmation, _, err := discarder.Prepare(t.Context(), fixture.gate(), fixture.campaignID)
	if err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}

	// The audit table goes away. `audit_log`'s own indexes go with it, which is the
	// point: this is "the record could not be written", not "the record was refused".
	fixture.exec(t, "DROP TABLE audit_log")

	err = discarder.Discard(t.Context(), fixture.campaignID, domain.TierGM, theGM, confirmation)
	if err == nil {
		t.Fatal("Discard() = nil, want an error: the row could not be recorded")
	}

	// The game is still there. This is the assertion that matters.
	fixture.requireStateRevision(t, 31)
}

// TestDiscardingStateThatIsNotThereIsRefused holds the "nothing to lose" case.
//
// A campaign nobody has opened has no game, and "there is nothing to lose" is not the
// same finding as "your game is incompatible". It is still refused rather than treated
// as success, because a caller that believes it discarded something and did not is a
// caller whose recovery path has silently stopped working.
func TestDiscardingStateThatIsNotThereIsRefused(t *testing.T) {
	fixture := newFixture(t, descriptor(), func(int64) string { return "" })

	discarder := fixture.discarder(t)

	_, _, err := discarder.Prepare(t.Context(), fixture.gate(), fixture.campaignID)
	if err == nil {
		t.Fatal("Prepare() = nil, want a refusal: there is no state to confirm a discard of")
	}

	if !errors.Is(err, realtime.ErrNoDiscardableState) {
		t.Errorf("Prepare() error = %v, want it to wrap ErrNoDiscardableState", err)
	}
}

// TestTheFingerprintNamesNoContent is S-12.3 at the boundary this file adds.
//
// A fingerprint is a plugin id and three version strings, so there is nothing to leak
// — and the assertion is that the *encoded* form, which is what a status page renders
// and what a log line would carry, names only its four inputs.
func TestTheFingerprintNamesNoContent(t *testing.T) {
	t.Parallel()

	withRules := descriptor()
	withRules.HouseRules = []realtime.HouseRule{
		{ModuleID: "the_passphrase_is_hunter2", Position: 1, Enabled: true},
	}

	fingerprint, err := realtime.FingerprintOf(withRules)
	if err != nil {
		t.Fatalf("FingerprintOf() error = %v, want nil", err)
	}

	encoded := fingerprint.String()

	for _, forbidden := range []string{"house", "hunter2", "passphrase", "position", "enabled"} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("encoded fingerprint = %q, want it to carry nothing from the house rules "+
				"(found %q)", encoded, forbidden)
		}
	}
}

// TestErrorClassDistinguishesProtocolRefusals is the fix R2 routed here.
//
// `errorClass` used to end in `fmt.Sprintf("%T", err)`, so **every** refusal the
// protocol codec could produce arrived as the single useless class
// `*realtime.FrameError`. An alert cannot match that, and the cases call for different
// operator responses. These are real refusals from real decodes, not constructed
// values, because the claim is about what the subsystem actually returns.
func TestErrorClassDistinguishesProtocolRefusals(t *testing.T) {
	t.Parallel()

	// Two real refusals of different kinds: a counter past `maxCounter` on a frame
	// whose type is fine, and a message type outside the three a client may send.
	oversizeCounter := decodeFrame(t, `{"t":"hello","since":9007199254740993}`)
	unknownType := decodeFrame(t, `{"t":"teleport"}`)

	if errors.Is(oversizeCounter, unknownType) {
		t.Fatal("the two frames produced the same error value, so the test cannot distinguish them")
	}

	first := observability.ErrorClass(oversizeCounter)
	second := observability.ErrorClass(unknownType)

	for name, class := range map[string]string{"oversize counter": first, "unknown type": second} {
		if class == "" {
			t.Errorf("ErrorClass(%s) = \"\", want a class", name)
		}

		if strings.Contains(class, "FrameError") {
			t.Errorf(
				"ErrorClass(%s) = %q, want the refusal's own class rather than its wrapper type; "+
					"an alert cannot match %q",
				name,
				class,
				class,
			)
		}
	}

	if first == second {
		t.Errorf(
			"both protocol refusals classified as %q, want two distinct classes: a client that "+
				"sent a counter out of range and a client that sent an unknown message type are "+
				"different events",
			first,
		)
	}

	// Both are the refusal's own vocabulary, which is what makes them alertable.
	for name, class := range map[string]string{"oversize counter": first, "unknown type": second} {
		if !strings.Contains(class, "counter") && !strings.Contains(class, "type") {
			t.Errorf(
				"ErrorClass(%s) = %q, want one of the codec's own class identifiers",
				name,
				class,
			)
		}
	}
}

// TestNoNewEventNameWasAdded is the constraint R6 is under about the registry.
//
// `state.write_ms` is §13.2's and was already in `AllEventNames()`, so a drift is
// reported through the existing `plugin.version_mismatch` and this file adds nothing.
// The count is asserted because a seventh `state.*` name would move a number another
// file's test owns, and that test is the only thing standing between a phase and an
// event name nobody documented.
func TestNoNewEventNameWasAdded(t *testing.T) {
	t.Parallel()

	names := observability.AllEventNames()

	if len(names) != 24 {
		t.Errorf("AllEventNames() has %d names, want 24: ruleset gating reports through "+
			"plugin.version_mismatch, which §13.2 already lists", len(names))
	}

	want := observability.EventName("state.write_ms")

	found := false

	for _, name := range names {
		if name == want {
			found = true
		}
	}

	if !found {
		t.Errorf(
			"AllEventNames() does not contain %q, which R6 relies on rather than adding a name",
			want,
		)
	}
}

// ---------------------------------------------------------------------------
// The fixture.
//
// A migrated database, for the reason `state_test.go` gives: S-7.8 is a statement
// about two rows, and a spy is a statement about a function call.
// ---------------------------------------------------------------------------

// discardClock is the fixed `created_at` the fixture's audit rows carry, so the
// assertion is a number rather than a tolerance.
const discardClock = 1_760_000_000

// seededPlacements are the ids seeded into every fixture state, and the strings the
// audit row's detail is required **not** to contain.
var seededPlacements = []string{"kobold-1", "kobold-2", "kobold-3", "kobold-4"}

// auditRow is the subset of an `audit_log` row this file asserts on.
type auditRow struct {
	campaignID int64
	actorID    int64
	action     string
	target     string
	detail     string
	createdAt  int64
}

// fixture is one campaign in one migrated database, with the three seams the
// components under test need.
type fixture struct {
	db         *store.Store
	campaignID int64
	registered realtime.Fingerprint
	persisted  func(campaignID int64) string
}

// newFixture opens a database, inserts a second campaign, and returns the fixture.
//
// Both versions are arguments rather than one of them being a default, and that is
// deliberate: an earlier version of this fixture defaulted the *registered*
// fingerprint to `descriptor()` and the mismatch test read as resumable, because the
// test's own "registered" descriptor was never the one the gate held. Two
// fingerprints, stated at every call site, cannot drift apart that way.
func newFixture(
	t *testing.T,
	registered realtime.Descriptor,
	persisted func(campaignID int64) string,
) *fixture {
	t.Helper()

	db, _, _ := openDatabase(t)

	// A second campaign, for the cross-campaign confirmation case. Inserted by hand
	// alongside the one `openDatabase` makes, for its reason: the other tables an
	// insert touches are preconditions here rather than subjects.
	fixture := &fixture{
		db:         db,
		campaignID: 1,
		registered: fingerprintOf(t, registered),
		persisted:  persisted,
	}

	fixture.exec(t, `INSERT INTO campaigns
		(slug, name, content_root, visibility, system_id, ruleset_version, created_at)
		VALUES ('gilded-cage-ii', 'The Gilded Cage II', ?, 'private', ?, ?, ?)`,
		t.TempDir(), fixtureSystem, persisted(2), 1_700_000_000)

	return fixture
}

// gate returns the campaign's gate, over the registered fingerprint.
func (f *fixture) gate() *realtime.Gate {
	return realtime.NewGate(
		f.registered,
		func(ctx context.Context, campaignID int64) (string, error) {
			return f.version(ctx, campaignID)
		},
	)
}

// discarder returns a discarder over the real database, with a fixed clock.
func (f *fixture) discarder(t *testing.T) *realtime.Discarder {
	t.Helper()

	return realtime.NewDiscarder(realtime.DiscardConfig{
		Write: f.writer(),
		Rows: func(ctx context.Context, campaignID int64) ([]byte, int64, error) {
			return f.stateColumns(ctx, campaignID)
		},
		Now: func() time.Time { return time.Unix(discardClock, 0).UTC() },
	})
}

// registry returns a live registry over the database, for the resume cases.
func (f *fixture) registry(t *testing.T) *realtime.Registry {
	t.Helper()

	return realtime.NewRegistry(t.Context(), realtime.Config{
		Write: f.writer(),
		Read:  f.reader(),
		// A long debounce, so a test that opens a state cannot have its scheduler
		// rewrite the row underneath the next assertion. The gate's own decisions
		// are what is under test, not the cadence.
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})
}

func (f *fixture) writer() realtime.Writer {
	return func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
		return f.db.Write(ctx, fn)
	}
}

func (f *fixture) reader() realtime.Reader {
	return func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
		blob, version, err := f.stateColumns(ctx, campaignID)
		if err != nil {
			return realtime.Persisted{}, err
		}

		return realtime.Persisted{Blob: blob, Version: version}, nil
	}
}

// stateColumns reads the one `campaign_state` row, as the composition root's two
// closures do.
func (f *fixture) stateColumns(ctx context.Context, campaignID int64) ([]byte, int64, error) {
	var (
		blob    []byte
		version int64
	)

	err := f.db.DB().QueryRowContext(ctx,
		`SELECT state, version FROM campaign_state WHERE campaign_id = ?`, campaignID,
	).Scan(&blob, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, realtime.ErrNoState
	}

	if err != nil {
		return nil, 0, fmt.Errorf("read campaign %d state: %w", campaignID, err)
	}

	return blob, version, nil
}

// version reads `campaigns.ruleset_version`, which is what the composition root's
// `VersionReader` closure is.
func (f *fixture) version(ctx context.Context, campaignID int64) (string, error) {
	var version string

	err := f.db.DB().QueryRowContext(ctx,
		`SELECT ruleset_version FROM campaigns WHERE id = ?`, campaignID,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("campaign %d does not exist", campaignID)
	}

	if err != nil {
		return "", fmt.Errorf("read campaign %d ruleset_version: %w", campaignID, err)
	}

	return version, nil
}

// seedState writes a `campaign_state` row at the given revision with that many
// placements, and sets the campaign's `ruleset_version` from the fixture's function.
//
// Both in one place because they are one fact: the row is what will be resumed and
// the column is what it is claimed to have been written under, and a fixture that set
// one and not the other would test a comparison against a value the campaign does not
// actually carry.
func (f *fixture) seedState(t *testing.T, revision uint64, placements int) {
	t.Helper()

	f.seedStateFor(t, f.campaignID, revision, placements)
}

// seedStateFor writes a row for an arbitrary campaign.
func (f *fixture) seedStateFor(t *testing.T, campaignID int64, revision uint64, placements int) {
	t.Helper()

	document := realtime.Document{Revision: revision, Paused: true}

	for at := range placements {
		// Distinct, recognisable ids: the audit row must not carry them, and a test
		// that asserts only "the detail is not empty" would not notice if it did.
		document.Placements = append(document.Placements, realtime.Placement{
			ID:      realtime.PlacementID("kobold-" + strconv.Itoa(at+1)),
			HP:      7,
			MaxHP:   7,
			Version: revision,
		})
	}

	blob, err := realtime.EncodeDocument(document)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v, want nil", err)
	}

	f.exec(t, `INSERT INTO campaigns (slug, name, content_root, visibility, system_id,
		ruleset_version, created_at)
		VALUES ('gilded-cage', 'The Gilded Cage', ?, 'private', ?, ?, 1700000000)
		ON CONFLICT(slug) DO UPDATE SET ruleset_version = excluded.ruleset_version`,
		"/tmp/gilded-cage", fixtureSystem, f.persisted(campaignID))

	f.exec(t, `INSERT INTO campaign_state (campaign_id, state, version, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(campaign_id) DO UPDATE SET
			state = excluded.state, version = excluded.version, updated_at = excluded.updated_at`,
		campaignID, blob, int64(revision), discardClock)
}

// exec runs one statement against the fixture's database.
func (f *fixture) exec(t *testing.T, statement string, args ...any) {
	t.Helper()

	if _, err := f.db.DB().ExecContext(t.Context(), statement, args...); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

// requireStateIntact asserts that the campaign's row is still at the revision and
// placement count a discard would have removed.
func (f *fixture) requireStateIntact(t *testing.T) {
	t.Helper()

	f.requireStateRevision(t, f.seededRevision(t))
}

func (f *fixture) requireStateRevision(t *testing.T, want int64) {
	t.Helper()

	_, version, err := f.stateColumns(t.Context(), f.campaignID)
	if err != nil {
		t.Fatalf(
			"the campaign's state row is gone: %v; a refused discard must not delete anything",
			err,
		)
	}

	if version != want {
		t.Errorf("campaign_state.version = %d, want %d", version, want)
	}
}

// seededRevision reads the revision the fixture's row is at, so an assertion about
// "still intact" does not restate a number that has to be kept in step by hand.
func (f *fixture) seededRevision(t *testing.T) int64 {
	t.Helper()

	_, version, err := f.stateColumns(t.Context(), f.campaignID)
	if err != nil {
		t.Fatalf("read the seeded state: %v", err)
	}

	return version
}

// requireNoStateRow asserts the campaign has no `campaign_state` row at all.
func (f *fixture) requireNoStateRow(t *testing.T) {
	t.Helper()

	_, _, err := f.stateColumns(t.Context(), f.campaignID)
	if !errors.Is(err, realtime.ErrNoState) {
		t.Errorf("the campaign still has a state row (err = %v), want it discarded", err)
	}
}

// requireNoAuditRows asserts nothing was recorded, which is how "a refusal did not
// half-succeed" is checked.
func (f *fixture) requireNoAuditRows(t *testing.T) {
	t.Helper()

	if got := f.countAuditRows(t); got != 0 {
		t.Errorf("audit_log has %d rows, want 0: a refused discard must leave no record", got)
	}
}

// onlyAuditRow returns the single audit row, and fails if there is not exactly one.
func (f *fixture) onlyAuditRow(t *testing.T) auditRow {
	t.Helper()

	if got := f.countAuditRows(t); got != 1 {
		t.Fatalf("audit_log has %d rows, want exactly 1: a discard that leaves no record is the "+
			"failure this exists to prevent", got)
	}

	var row auditRow

	err := f.db.DB().QueryRowContext(t.Context(),
		`SELECT campaign_id, actor_id, action, target, detail, created_at
		 FROM audit_log WHERE campaign_id = ? ORDER BY id DESC`, f.campaignID,
	).Scan(&row.campaignID, &row.actorID, &row.action, &row.target, &row.detail, &row.createdAt)
	if err != nil {
		t.Fatalf("read the audit row: %v", err)
	}

	return row
}

func (f *fixture) countAuditRows(t *testing.T) int {
	t.Helper()

	var count int

	if err := f.db.DB().
		QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log`).
		Scan(&count); err != nil {
		t.Fatalf("count the audit rows: %v", err)
	}

	return count
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

// descriptor is the registered ruleset: system, its version, and two packs.
func descriptor() realtime.Descriptor {
	return realtime.Descriptor{
		System:      fixtureSystem,
		Ruleset:     fixtureRuleset,
		BasePack:    fixtureBase,
		OverlayPack: fixtureOverlay,
	}
}

// driftedDescriptor is the campaign's *persisted* side in every drift test: the
// registered ruleset with an older base pack.
//
// A single fixed value rather than a parameter, because the version is the
// precondition of a dozen tests and a parameter would be one more thing each of them
// has to get right. A data pack is the realistic drift anyway — packs ship far more
// often than systems do — so this is the case ADR 0018's
// alternatives-considered writes against.
func driftedDescriptor() realtime.Descriptor {
	persisted := descriptor()
	persisted.BasePack = "core-0.9.0"

	return persisted
}

// fingerprintOf is `FingerprintOf` that fails the test rather than returning an
// error, because every use of it is with a constant descriptor.
func fingerprintOf(t *testing.T, descriptor realtime.Descriptor) realtime.Fingerprint {
	t.Helper()

	fingerprint, err := realtime.FingerprintOf(descriptor)
	if err != nil {
		t.Fatalf("FingerprintOf(%+v) error = %v, want nil", descriptor, err)
	}

	return fingerprint
}

// assertMentions requires that message carries the labelled value, and says which
// fact is missing when it does not.
//
// The three labelled arguments are the three claims a refusal has to earn. A test
// that asserted only "the message is non-empty" would be satisfied by "incompatible",
// which is the failure this exists to catch.
func assertMentions(t *testing.T, message, label, value string) {
	t.Helper()

	if !strings.Contains(message, value) {
		t.Errorf("message = %q, want it to name the %s (%q); a refusal that does not say which is "+
			"which, or what follows, is not actionable", message, label, value)
	}
}

// decodeFrame asks the protocol codec for a refusal and returns the error, so the
// class assertions are about what the subsystem actually returns rather than about a
// constructed value.
func decodeFrame(t *testing.T, frame string) error {
	t.Helper()

	_, err := realtime.Decode([]byte(frame))
	if err == nil {
		t.Fatalf("Decode(%q) = nil error, want a refusal", frame)
	}

	// Wrapped, and that is not ceremony: it puts the frame under test behind the same
	// `%w` every real call site adds, so the class assertions hold through a wrapper
	// rather than only against a bare refusal.
	return fmt.Errorf("decode %q: %w", frame, err)
}
