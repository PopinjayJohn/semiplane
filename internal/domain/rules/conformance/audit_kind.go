package conformance

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// SurplusKind is the kind the unknown-kind audit plants on the tabletop.
//
// **Named, and refused if the system declares it.** The audit needs a kind that no
// registered system recognises, and the honest way to get one is to ask: it is
// taken from the system's own `ContentKinds` and named here only so the finding can
// quote it. `New` refuses a system that claims it, because a system that *did*
// recognise this object would make the audit certify nothing — and would make the
// fixture in this package's own tests, which is a system that errors on it, the only
// thing the audit could catch.
//
// The spelling is deliberately odd and obviously not a rules-content kind a system
// would want: it is a fixture, and a fixture that looks like a real kind is a
// fixture somebody will ship.
const SurplusKind rules.Kind = "conformance_surplus"

// SurplusObjectID is the object the unknown-kind audit plants.
//
// Separate from the kind because the two answers are independent: a system can
// recognise the kind and not the object, or the reverse, and S-14.7 is about
// neither.
const SurplusObjectID rules.ObjectID = "conformance_surplus_1"

// surplusData is the planted object's content.
//
// **Non-empty, and that is the point of the whole audit.** S-14.7's requirement is
// that content is *intact*, and "intact" over an empty object is unfalsifiable: a
// system that dropped every unknown object it saw would preserve an empty one
// perfectly. These bytes are what a finding about lost content is about.
//
// A constant rather than a `[]byte` var, so there is no package-level mutable value
// to be edited by anything — `rules.NewState` clones what it is handed, so the
// slice could not be corrupted through a state, and a string cannot be corrupted at
// all.
const surplusData = `{"kind":"conformance_surplus","note":"content a plugin may not understand"}`

// UnknownKind is S-14.7 and §10.8's last row as an audit: **a kind nothing
// registers is inert, not fatal**, and whatever the system made of it is still
// there afterwards.
//
// The wiki half of S-14.7 — a page degrades to prose, the Markdown is always
// recoverable because it is a file on disk — is `content`'s to prove and is proved
// there. This is the half a *gameplay system* can get wrong, and it can get it
// wrong in three distinct ways, so the audit has three teeth:
//
//  1. The resolution does not fail. A system that returns a refusal because the
//     tabletop carries an object it does not recognise has made an unrelated intent
//     fail over a stale kind, which is the fatal reading §10.8's row exists to
//     forbid: a plugin removal would then break every other resolution in the
//     campaign, not just the ones about the removed plugin's objects.
//  2. Nothing addresses the unknown object. Inert means inert: a system that
//     mutated, removed, or renamed an object of a kind it does not declare has
//     decided something about content it said it does not understand.
//  3. The unknown object's bytes survive. "Content intact" over an empty object is
//     unfalsifiable, so the planted object carries content and the whole snapshot
//     is compared — which also catches a resolver that rewrote the *known* objects
//     while it was looking at the unknown one.
//
// Tooth 3 is the same claim the containment audit makes over the same snapshot, and
// they are deliberately not the same finding: here the subject is what the system
// did to content it did not recognise, which is a different bug from a system that
// writes to its input at all.
func (s *Suite) UnknownKind(ctx context.Context) []Finding {
	var found []Finding

	surplus := rules.Object{
		ID:   SurplusObjectID,
		Kind: SurplusKind,
		Data: []byte(surplusData),
	}

	before, err := s.state(surplus)
	if err != nil {
		return []Finding{{Audit: AuditUnknownKind, Summary: err.Error()}}
	}

	fingerprintBefore := Fingerprint(before)

	mutations, err := Contain(s.cfg.System).Resolve(
		ctx, s.cfg.Scenario.Call, before, s.cfg.Scenario.Intent,
	)
	if err != nil {
		found = append(found, Finding{
			Audit: AuditUnknownKind,
			Summary: fmt.Sprintf(
				"resolving %q on a tabletop carrying %q — a kind this system declares none of, "+
					"it declares %s — was refused: %v; an unknown kind is inert, so a plugin removal "+
					"must not fail the resolutions that have nothing to do with the removed plugin",
				s.cfg.Scenario.Intent, SurplusKind, declaredKinds(s.cfg.System.ContentKinds()), err,
			),
		})
	}

	for _, mutation := range mutations {
		if mutation.Target != SurplusObjectID {
			continue
		}

		found = append(found, Finding{
			Audit: AuditUnknownKind,
			Summary: fmt.Sprintf(
				"resolving %q addressed %q, whose kind %q this system does not declare, with %q; "+
					"inert means inert, and a resolver that changes an object of a kind it does not "+
					"know is guessing",
				s.cfg.Scenario.Intent, SurplusObjectID, SurplusKind, mutation,
			),
		})
	}

	// Compared after the resolution, over a **freshly built** snapshot rather than the
	// one that was handed in: the hub's copy is the hub's, and asking the suite to
	// inspect the very value it passed down would be asking it to confirm its own
	// argument. What is compared is what the author declared plus the planted object.
	after, err := s.state(surplus)
	if err != nil {
		return append(found, Finding{Audit: AuditUnknownKind, Summary: err.Error()})
	}

	if fingerprintAfter := Fingerprint(after); fingerprintAfter != fingerprintBefore {
		found = append(found, Finding{
			Audit: AuditUnknownKind,
			Summary: fmt.Sprintf(
				"the unknown-kind tabletop is not the tabletop that was resolved; before:\n%s\nafter:\n%s",
				fingerprintBefore,
				fingerprintAfter,
			),
		})
	}

	return found
}

// declaredKinds renders a system's declared content kinds for a finding.
//
// A helper and not `fmt.Sprintf("%v", …)` because `rules` hands out a slice the
// caller could otherwise have mutated, and because an empty declaration should read
// as "none" rather than as `[]`.
func declaredKinds(kinds []rules.Kind) string {
	if len(kinds) == 0 {
		return "none"
	}

	names := make([]string, 0, len(kinds))

	for _, kind := range kinds {
		names = append(names, strconv.Quote(kind.String()))
	}

	return strings.Join(names, ", ")
}
