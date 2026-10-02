package conformance

import (
	"errors"
	"fmt"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// HouseRule is one house-rule module as a campaign has it configured.
//
// The suite declares the shape rather than taking a `realtime.HouseRule`, and the
// reason is the one that keeps `realtime` out of this package: `domain` imports
// nothing from the project, and `realtime` owns the database. What is shared is a
// **shape** — three fields the adapter maps by name — and a shape two packages
// agree on is not the second answer to a question the first one answers, which is
// what a duplicated *decision* would be.
//
// `Enabled` is the field the audit is about. `ModuleID` and `Position` are part of
// the claim rather than decoration: ADR 0018 requires conflicts to resolve
// first-match-wins by `position` with both module ids logged, so a fingerprint that
// read the ordering would strand a campaign over a re-order that changes nothing
// except which module answers a conflict that is not being hit.
type HouseRule struct {
	// ModuleID is the module's stable id.
	ModuleID string

	// Position is the declared order.
	Position int

	// Enabled is whether the campaign has the module switched on.
	Enabled bool
}

// Verdict is one answer about one campaign's persisted state.
//
// A struct rather than an `error` because the audit asks three questions and an
// error can carry only one of the answers: *may it resume* (`Err`), *what does it
// think it was written under* (`Persisted`), and *what would it write now*
// (`Expected`). The second and third are what make a drift refusal actionable, and
// `realtime.DriftError.Error` names all three for the same reason: a refusal that
// says only "incompatible" has moved the diagnosis onto the reader.
type Verdict struct {
	// Persisted is the `ruleset_version` the campaign's state was written under, as
	// the column holds it.
	Persisted string

	// Expected is what this deployment would write for a campaign opened now.
	Expected string

	// Err is nil when the campaign may resume, and the refusal otherwise.
	Err error
}

// Resume is the deployment's answer about a campaign's ruleset: what fingerprint
// it persists, and whether a campaign written under another one may continue.
//
// **A seam and not a helper this package computes**, because the encoding is
// `realtime`'s — `Fingerprint`'s `sp1:` prefix, its fixed component order, its
// reserved separators — and the column is `campaigns.ruleset_version`. Re-deriving
// any of that here would be a second implementation of a persisted format, and the
// two would drift the first time a component was added, with a campaign silently
// refusing to resume because the comparison is a string equality.
//
// Two methods because two questions, and the audit needs both: `Fingerprint` to
// learn what this deployment would persist, so that it has something to resume
// under and something to contrast against, and `Check` to ask about a version that
// is not it.
type Resume interface {
	// Fingerprint is what a campaign opened now would be written under, for this
	// system and these house rules.
	//
	// The house rules are a parameter and not something this package has an opinion
	// about: ADR 0018 requires the **result** to be independent of them, and a
	// fingerprint whose signature did not accept them could not be asked the
	// question.
	Fingerprint(system rules.System, house []HouseRule) string

	// Check answers whether a campaign whose state was written under `persisted` may
	// resume under `system` with these house rules.
	Check(system rules.System, house []HouseRule, persisted string) Verdict
}

// upgradeSuffix is appended to a system's `RulesetVersion` to build the "some other
// build's fingerprint" the version audit refuses.
//
// A suffix rather than a hand-written fingerprint literal, because the suite cannot
// construct one: the encoding is `realtime`'s. What it can do is hand `Fingerprint`
// a `rules.System` whose version is a different string — which is exactly the input
// a plugin upgrade produces, and is a far better drift fixture than a literal the
// suite invented, because the deployment's own encoder agrees that it is a valid
// version of something.
const upgradeSuffix = "+conformance-next"

// Version is §10.8's ruleset row and ADR 0018's exclusion, as audits a system is
// certified against.
//
// §10.8's row says one thing — a `ruleset_version` differing from persisted state
// means **refuse to resume rather than silently misresolve** — and "silently
// misresolve" is the whole failure being prevented, so most of this audit is about
// what the refusal says and how distinguishable it is from the failures beside it.
// Five claims, and each of them can fail:
//
//  1. **A fresh campaign resumes.** The deployment's own fingerprint, checked against
//     itself, is permitted and reported. Otherwise the gate refuses everything, and a
//     suite that only ever asked about drift would never notice.
//  2. **Some other version exists to be refused.** The suite asks the deployment's
//     own encoder for the fingerprint of a system one upgrade newer. Without it there
//     is no drift case, and claiming to have audited one would be the audit that
//     cannot fail.
//  3. **A differing version is refused, and the refusal names both ends.** Not
//     "incompatible": the three facts a GM needs are which version the game is
//     sitting under, which one this build resolves under, and what will not happen. A
//     verdict whose message omits either is one a GM cannot act on, and the two have
//     different fixes.
//  4. **An unreadable version is refused *differently*.** `realtime` already draws
//     this line, and the reason is worth repeating because it is a failure this audit
//     exists to prevent twice over: a column this build cannot parse is not a
//     campaign whose game is incompatible, and a GM told to discard for one is being
//     told to lose a game over a string. `errors.Is` against the drift refusal is how
//     the two are told apart, so a deployment answering both with one sentinel fails
//     here.
//  5. **A house rule does not move the fingerprint** (ADR 0018). The same module,
//     enabled and disabled: the fingerprints must be byte-identical and the same
//     campaign must still resume. A fingerprint that hashed the enabled set would
//     refuse to resume every campaign whose GM enabled a house rule at the table — a
//     fault that reads as a feature, because the error would be perfectly reasonable
//     on its face.
//     its face.
func (s *Suite) Version() []Finding {
	var found []Finding

	disabled, enabled := s.house[0:1], s.house[1:]

	mine := s.cfg.Resume.Fingerprint(s.cfg.System, disabled)

	if mine == "" {
		return []Finding{{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"the deployment reports no fingerprint for system %q, so a campaign written under it "+
					"has nothing to be compared with and a resume could not detect drift",
				s.cfg.System.ID(),
			),
		}}
	}

	fresh := s.cfg.Resume.Check(s.cfg.System, disabled, mine)

	switch {
	case fresh.Err != nil:
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"a campaign written under this deployment's own fingerprint %q was refused: %v; a gate "+
					"that refuses the version it just wrote is a gate that refuses everything",
				mine,
				fresh.Err,
			),
		})
	case fresh.Persisted != mine, fresh.Expected != mine:
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"checking %q against this deployment's own fingerprint reported persisted %q and "+
					"expected %q; a verdict that does not name the version it compared is a verdict "+
					"a GM cannot act on",
				mine, fresh.Persisted, fresh.Expected,
			),
		})
	}

	// Claim 2: the other build's fingerprint, asked of the deployment's own encoder.
	theirs := s.cfg.Resume.Fingerprint(upgradeVersion(s.cfg.System), disabled)

	if theirs == "" || theirs == mine {
		return append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"a system reporting ruleset version %q fingerprints as %q — the same fingerprint as "+
					"this one, or none at all — so there is no drift case to refuse and §10.8's row "+
					"could not be exercised",
				s.cfg.System.RulesetVersion()+upgradeSuffix,
				theirs,
			),
		})
	}

	drift := s.cfg.Resume.Check(s.cfg.System, disabled, theirs)

	if drift.Err == nil {
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"a campaign written under %q was permitted to resume under %q; §10.8 requires a "+
					"refusal rather than a silent misresolution, because a mutation resolved under the "+
					"older semantics may not replay under the newer ones",
				theirs, mine,
			),
		})
	}

	found = append(found, objectionTo("the drift refusal", drift, theirs, mine)...)

	unreadable := s.cfg.Resume.Check(s.cfg.System, disabled, "not a fingerprint this build wrote")

	switch {
	case unreadable.Err == nil:
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: "a ruleset_version this build cannot parse was permitted to resume; refusing " +
				"it is the whole point, and guessing at a version is the misresolve",
		})
	case sameRefusal(drift.Err, unreadable.Err):
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"a ruleset_version this build cannot parse was refused in a way a caller cannot tell from "+
					"a genuine version mismatch (%v against %v); the two are different failures, and telling "+
					"a GM to discard a game over an unreadable column is the software inflicting the loss "+
					"ADR 0018 exists to prevent",
				drift.Err,
				unreadable.Err,
			),
		})
	}

	// Claim 5, as bytes rather than as a comment.
	if moved := s.cfg.Resume.Fingerprint(s.cfg.System, enabled); moved != mine {
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"enabling a house rule moved the fingerprint from %q to %q; ADR 0018 requires the "+
					"fingerprint to name resolution semantics and not house-rule configuration, or a GM "+
					"enabling a rule at the table is refused on their own campaign",
				mine, moved,
			),
		})
	}

	if verdict := s.cfg.Resume.Check(s.cfg.System, enabled, mine); verdict.Err != nil {
		found = append(found, Finding{
			Audit: AuditVersion,
			Summary: fmt.Sprintf(
				"a campaign this deployment wrote itself was refused once a house rule was enabled (%v); "+
					"toggling a house rule changes outcomes and not the meaning of a stored mutation, so "+
					"it must not strand a campaign",
				verdict.Err,
			),
		})
	}

	return found
}

// sameRefusal reports whether two refusals are the same refusal as far as a caller can
// tell.
//
// **Interchangeability, in both directions, and including the root.** A handler routes
// a refusal with `errors.Is` and with nothing finer, so two refusals a handler cannot
// separate are two refusals a GM cannot be told apart — whatever their messages say.
// `errors.Is(a, b)` alone is not enough, because `b` here is the whole message-bearing
// error rather than a sentinel, and a deployment that wraps one sentinel in two
// different sentences would pass on a comparison a handler could never make.
//
// So the roots are compared too: the deepest error each wraps. `realtime` answers these
// two with a `*DriftError` — which has an `Is` and no `Unwrap`, so its root is itself —
// and with something wrapping `ErrRulesetUnreadable`, and those are different objects.
// A deployment answering both with `ErrRulesetDrift` has the same root twice, and a GM
// is told to discard their game over a string.
//
// The property claimed is deliberately the one a caller can rely on, not "the messages
// differ": a refusal pair with different sentences and one shared sentinel is
// indistinguishable to every `errors.Is` a handler will write.
func sameRefusal(first, second error) bool {
	if first == nil || second == nil {
		return false
	}

	if errors.Is(first, second) || errors.Is(second, first) {
		return true
	}

	root := deepest(first)

	return root != nil && errors.Is(root, deepest(second))
}

// deepest unwraps to the last error in a chain, or the error itself when it unwraps to
// nothing.
//
// The loop and not a recursion because `errors.Unwrap` is a chain and a chain of
// `fmt.Errorf` sentinels is short but not bounded by anything this package controls.
func deepest(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}

		err = unwrapped
	}
}

// objectionTo returns what is wrong with one refusal, if anything: that it did not
// report the version it was asked about, or did not name either end in its message.
//
// Two checks rather than one because they are two ways the same sentence gets
// truncated, and because the *fields* are what a handler renders while the
// *message* is what a log line carries — `realtime` keeps both for the same reason,
// and a verdict that filled one and not the other would leave half the page saying
// nothing.
func objectionTo(what string, verdict Verdict, persisted, expected string) []Finding {
	if verdict.Err == nil {
		return nil
	}

	message := verdict.Err.Error()

	var missing []string

	if verdict.Persisted != persisted {
		missing = append(missing, fmt.Sprintf("the version it was asked about (%q)", persisted))
	} else if !strings.Contains(message, persisted) {
		missing = append(missing, "the version the state was written under")
	}

	if verdict.Expected != expected {
		missing = append(
			missing,
			fmt.Sprintf("the version this build would resolve under (%q)", expected),
		)
	} else if !strings.Contains(message, expected) {
		missing = append(missing, "the version this build would resolve under")
	}

	if len(missing) == 0 {
		return nil
	}

	return []Finding{{
		Audit: AuditVersion,
		Summary: fmt.Sprintf(
			"%s names neither %s: %q; a GM told there is a difference and not which has been given a "+
				"diagnosis to carry, and each end has a different fix",
			what,
			strings.Join(missing, " nor "),
			message,
		),
	}}
}
