package conformance

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// The wire's rejection vocabulary, copied.
//
// **A copy, deliberately, and this is the one place the suite states something it
// cannot enforce.** `realtime.RejectReason` is a closed set of eight words for a
// reason about the wire rather than about this package: a free string is a field a
// plugin's error can be poured into, and a plugin error quotes the thing it choked
// on. §10.7 is not a rules concern, and `domain` imports nothing from the project —
// `realtime` owns a database, which is the concrete reason a package about pure rule
// resolution cannot reach it.
//
// So the words are restated here, and the audit's claim is deliberately weaker than
// a mirror would be: **every refusal a system produces has to map into a word this
// package knows**, not that the word is one `realtime` has. A plugin whose errors
// map to `not_permitted`, `invalid_args` or `no_such_placement` passes even after a
// ninth word is added upstream, because the adapter the author already wrote does
// the mapping. What fails is an error that becomes its own sentence — "you may not
// unwind another player's loom" — which no client can be told and which quotes
// whatever the system choked on: S-12.3 on the wire, and `protocol.go` says it in
// one line of comment.
//
// Two sets drifting apart is therefore not a failure this suite can see, and that
// asymmetry is the design rather than an omission. A suite asserting set equality
// would fail every plugin's certification for a reason no plugin could act on.
const (
	// ReasonNotYourTurn is a turn-order refusal.
	ReasonNotYourTurn = "not_your_turn"

	// ReasonNotPermitted is §7.2's GM-only rule and S-8's access matrix, and the one
	// word the role audit requires by name.
	ReasonNotPermitted = "not_permitted"

	// ReasonUnknownOp is an operation this campaign's systems do not resolve.
	ReasonUnknownOp = "unknown_op"

	// ReasonInvalidArgs is an operation whose parameters do not parse.
	ReasonInvalidArgs = "invalid_args"

	// ReasonStaleVersion is a client acting on state it has not resynced.
	ReasonStaleVersion = "stale_version"

	// ReasonOutOfOrder is an intent against a placement another change already
	// superseded.
	ReasonOutOfOrder = "out_of_order"

	// ReasonNoSuchPlacement is an operation naming a game object that does not exist.
	ReasonNoSuchPlacement = "no_such_placement"

	// ReasonServerError is the one reason that says nothing about what failed, and so
	// what a contained panic must become.
	ReasonServerError = "server_error"
)

// wireReasons is the closed set, in `realtime`'s order.
var wireReasons = [...]string{
	ReasonNotYourTurn,
	ReasonNotPermitted,
	ReasonUnknownOp,
	ReasonInvalidArgs,
	ReasonStaleVersion,
	ReasonOutOfOrder,
	ReasonNoSuchPlacement,
	ReasonServerError,
}

// WireReasons returns the words a refusal may be told in.
//
// **A fresh slice per call**, for the reason `rules.SemiplaneKinds` gives. An
// exported slice is a mutable global, and the caller most likely to sort one in
// place is a caller building a help string.
func WireReasons() []string { return slices.Clone(wireReasons[:]) }

// Role is §7.2 as an audit: **the actor's role decides, and a player does not get a
// GM-only outcome.**
//
// The property is not "a player is refused". It is that the same resolution, over
// the same state, intent and seed, produces `not_permitted` for a player and
// something else for a GM — because a system that refuses everyone has not enforced
// a role, it has disabled itself, and a system that permits everyone has not
// enforced one at all. Both directions are checked, on every op the author declared, and a
// system that gets one right and another wrong is failed on the one it got wrong.
//
// Three claims, and two of them are why `Classify` is a required seam rather than a
// detail:
//
//   - **The refusal is `not_permitted`.** Asserted by name, and `realtime.Core`
//     orders it ahead of `unknown_op` deliberately: `not_permitted` answers "is this
//     allowed" *about the player*, and `unknown_op` answers "is anything allowed"
//     *about the server*. Ordered the other way, the word on the wire is a role oracle
//     anybody on the campaign can read — the table learns that its GM has operations,
//     and learns from its own refusal what it is not allowed to ask for.
//   - **`not_permitted` is not the answer to anything else.** The scenario's own
//     operation is resolved as a player and must not be refused that way, which is
//     the other way to get §7.2 wrong and the one a suite written only about
//     GM-only ops would miss.
//   - **Every refusal maps into the closed set.** A refusal that becomes its own
//     sentence is one a browser cannot be told.
//
// The GM run is checked for *not* answering `not_permitted` rather than for
// succeeding. The suite cannot build a valid intent for an op whose arguments only
// this system understands, so demanding success would be demanding a second fixture
// per GM-only op; demanding that the role is what decides is the claim itself, and it is
// exactly as falsifiable.
func (s *Suite) Role(ctx context.Context) []Finding {
	var found []Finding

	for _, operation := range s.cfg.GMOnly {
		state, err := s.state()
		if err != nil {
			return append(found, Finding{Audit: AuditRole, Summary: err.Error()})
		}

		intent, err := s.intentFor(operation)
		if err != nil {
			return append(found, Finding{Audit: AuditRole, Summary: err.Error()})
		}

		asPlayer, refusal := Contain(s.cfg.System).Resolve(ctx, s.asPlayer(), state, intent)
		if refusal == nil {
			found = append(found, Finding{
				Audit: AuditRole,
				Summary: fmt.Sprintf(
					"%q is declared GM-only and was resolved for a player, returning %s; §7.2 reserves "+
						"it to the GM",
					operation,
					render(asPlayer),
				),
			})

			continue
		}

		if len(asPlayer) > 0 {
			found = append(found, Finding{
				Audit: AuditRole,
				Summary: fmt.Sprintf(
					"%q was refused to a player and still returned %s alongside the refusal; a refused "+
						"resolution changes nothing, and a list half-built on the way to being discarded "+
						"is a list whose contents depend on when it was abandoned",
					operation,
					render(asPlayer),
				),
			})
		}

		if word := s.cfg.Classify(refusal); word != ReasonNotPermitted {
			found = append(found, Finding{
				Audit: AuditRole,
				Summary: fmt.Sprintf(
					"%q is declared GM-only and a player's refusal maps to %q rather than %q; %q answers "+
						"whether anything is allowed rather than whether this is, and on the wire that "+
						"distinction is a role oracle",
					operation,
					word,
					ReasonNotPermitted,
					ReasonUnknownOp,
				),
			})
		}

		found = append(found, s.wireObjections(operation, refusal)...)

		_, gmRefusal := Contain(s.cfg.System).Resolve(ctx, s.asGM(), state, intent)

		if gmRefusal != nil && s.cfg.Classify(gmRefusal) == ReasonNotPermitted {
			found = append(found, Finding{
				Audit: AuditRole,
				Summary: fmt.Sprintf(
					"%q is declared GM-only and was refused to the GM as well (%v); the actor's role is "+
						"what decides this operation, so refusing the GM too is not enforcement, it is a "+
						"system nobody at the table can use",
					operation,
					gmRefusal,
				),
			})
		}
	}

	found = append(found, s.playerScenarioObjections(ctx)...)

	return found
}

// playerScenarioObjections resolves the scenario's own operation as a player and
// objects to the answer.
//
// `New` guarantees the scenario's operation is not a declared GM-only one, so this is the
// shape that catches a system answering `not_permitted` for everything. A refusal
// here is not itself wrong — a player may legitimately fail a resolution — so only
// the *word* is checked.
func (s *Suite) playerScenarioObjections(ctx context.Context) []Finding {
	state, err := s.state()
	if err != nil {
		return []Finding{{Audit: AuditRole, Summary: err.Error()}}
	}

	_, refusal := Contain(s.cfg.System).Resolve(
		ctx, s.asPlayer(), state, s.cfg.Scenario.Intent,
	)
	if refusal == nil {
		return nil
	}

	operation := s.cfg.Scenario.Intent.Op

	var found []Finding

	if word := s.cfg.Classify(refusal); word == ReasonNotPermitted {
		found = append(found, Finding{
			Audit: AuditRole,
			Summary: fmt.Sprintf(
				"%q is not declared GM-only and was refused to a player as %q; a system that answers "+
					"%q to everything enforces no role at all, it refuses play",
				operation,
				ReasonNotPermitted,
				ReasonNotPermitted,
			),
		})
	}

	return append(found, s.wireObjections(operation, refusal)...)
}

// wireObjections returns what is wrong with a refusal's wording, if anything: a word
// outside the closed set.
//
// One place for the claim, because it is the same claim at every call site and a
// second copy would be a second thing to keep right.
func (s *Suite) wireObjections(operation rules.Op, refusal error) []Finding {
	if slices.Contains(wireReasons[:], s.cfg.Classify(refusal)) {
		return nil
	}

	// The word is quoted because it is what `Classify` returned: a deployment's own
	// constant, not anything from a vault and not a payload.
	return []Finding{{
		Audit: AuditRole,
		Summary: fmt.Sprintf(
			"the refusal for %q maps to %q, which is not one of the wire's reasons (%s); a refusal a "+
				"client cannot be told is not a refusal, and one that carries the system's own sentence "+
				"is how whatever the system choked on reaches a browser",
			operation,
			s.cfg.Classify(refusal),
			strings.Join(quotedWireReasons(), ", "),
		),
	}}
}

// quotedWireReasons renders the closed set for a finding.
func quotedWireReasons() []string {
	quoted := make([]string, 0, len(wireReasons))

	for _, reason := range wireReasons {
		quoted = append(quoted, fmt.Sprintf("%q", reason))
	}

	return quoted
}

// intentFor builds an intent for one op, addressed at the first object the author
// declared.
//
// **No arguments**, and that is a decision rather than an omission: this system's
// parameters are this system's encoding, and the suite would have to know the
// encoding to invent any. An intent with no arguments is one a system may well
// refuse with `invalid_args` — which is exactly why the GM run is checked for *not*
// saying `not_permitted` rather than for succeeding.
func (s *Suite) intentFor(operation rules.Op) (rules.Intent, error) {
	target := s.firstObject().ID

	if target == "" {
		return rules.Intent{}, fmt.Errorf(
			"conformance: the scenario declares an object with no id, so the role audit has nowhere "+
				"to address %q",
			operation,
		)
	}

	intent, err := rules.NewIntent(operation, target, nil)
	if err != nil {
		return rules.Intent{}, fmt.Errorf(
			"conformance: building an intent for %q: %w",
			operation,
			err,
		)
	}

	return intent, nil
}
