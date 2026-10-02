package conformance

import (
	"context"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// Determinism is S-14.6 as an audit a system is certified against: the scenario
// resolved `Runs` times from one seed, and every one of the answers compared byte
// for byte.
//
// `rules`' own test proves the property at the *type* level — that `Context` has
// nowhere to receive a clock and `State` sorts its objects — and that test is the
// stronger kind of evidence where it applies, because it is about the contract
// rather than about a caller. It cannot be the whole answer, for one reason: the
// three ways §10.3 forbids are still calls a plugin *can* make. `time.Now`,
// `math/rand`'s package-level functions and `crypto/rand` all compile, all work,
// and every one of them makes a resolution irreproducible while leaving the type
// untouched. Those are calls, and only a test that runs the system can see them.
// That is what this is, and the reference system's absence of any 5e vocabulary is
// what keeps it from being an audit of one system's mistakes.
//
// Three teeth, and the third is the one that keeps the other two honest:
//
//  1. The scenario resolves at all. A system that answers every intent with a
//     refusal has not been shown to be deterministic; it has been shown to be
//     unreachable, and an audit that reported success would be reporting that.
//  2. The scenario changes something. A resolution that produces no mutation is
//     reproducible whether or not the system were deterministic — including
//     deterministically, buggily, empty. S-14.6 is about *mutations*, and a suite
//     that certified an empty one would be the exact failure this repository's
//     standing rule is about: a gate wired to nothing.
//  3. Every one of the `Runs` answers is the same answer. Compared as a digest
//     rather than as a rendering, because the payload is where a resolved roll
//     lives and S-12.3 forbids it reaching a log — see `fingerprintMutations`.
//
// A fresh snapshot per run, built from `Scenario.Objects`, so a resolver that
// scribbled on the copy it was handed cannot hide behind the previous run's — and
// so a system that mutated its input is caught here rather than by the
// containment audit, where the finding would be about the wrong thing.
func (s *Suite) Determinism(ctx context.Context) []Finding {
	var found []Finding

	// Run 0 establishes the reference and answers tooth 1. It is inside the loop so
	// there is one call site: an audit with a "first" resolution and a "rest"
	// resolution is an audit whose two halves can be edited apart.
	var reference string

	var referenceMutations []rules.Mutation

	for run := range Runs {
		state, err := s.state()
		if err != nil {
			return append(found, Finding{
				Audit:   AuditDeterminism,
				Summary: err.Error(),
			})
		}

		mutations, err := Contain(s.cfg.System).Resolve(
			ctx, s.cfg.Scenario.Call, state, s.cfg.Scenario.Intent,
		)
		if err != nil {
			// Reported once, not once per run: ninety-nine identical failures say
			// nothing ninety-nine times, and a finding list long enough to scroll past
			// is a finding nobody reads.
			if run == 0 {
				found = append(found, Finding{
					Audit: AuditDeterminism,
					Summary: fmt.Sprintf(
						"resolving %q on %s was refused: %v; there is nothing to compare, and a "+
							"resolver that refuses every intent has not been shown to be deterministic",
						s.cfg.Scenario.Intent, stateFingerprintShort(state), err,
					),
				})
			}

			continue
		}

		if run == 0 {
			referenceMutations = mutations

			if len(mutations) == 0 {
				found = append(found, Finding{
					Audit: AuditDeterminism,
					Summary: fmt.Sprintf(
						"resolving %q produced no mutation, and a resolution that changes nothing is "+
							"reproducible whether or not the resolver is deterministic; choose a "+
							"scenario that changes something",
						s.cfg.Scenario.Intent,
					),
				})

				continue
			}

			reference = fingerprintMutations(mutations)

			continue
		}

		if got := fingerprintMutations(mutations); got != reference {
			found = append(found, Finding{
				Audit: AuditDeterminism,
				Summary: fmt.Sprintf(
					"run %d of %d resolved %q differently from run 0 with the same state, intent and "+
						"seed: run 0 gave %s, run %d gave %s; resolution is a function of (state, intent, "+
						"seed) and of nothing else, so something outside those three reached the answer",
					run,
					Runs,
					s.cfg.Scenario.Intent,
					render(referenceMutations),
					run,
					render(mutations),
				),
			})

			// One difference is enough to certify the failure, and stopping keeps the
			// finding list about the first divergence — which is the one an author
			// reproduces by running twice.
			break
		}
	}

	return found
}

// stateFingerprintShort names a snapshot in one line, for a finding.
//
// `Fingerprint` is the whole state and belongs in the containment audit, where the
// claim is about all of it. Here the claim is "there was a tabletop at all", so the
// revision and the count are the whole of what a reader needs.
func stateFingerprintShort(state rules.State) string {
	return fmt.Sprintf("a state at revision %d with %d objects", state.Revision(), state.Len())
}
