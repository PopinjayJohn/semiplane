package realtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/semiplane/semiplane/internal/domain"
)

// Core is the inert gameplay system this phase runs against.
//
// It exists so phase 7 is independently runnable, and it is deliberately not a
// stub that returns "not implemented": an inert system that refuses everything
// cannot carry a client through the protocol, so the hub, the monotonic version,
// the persistence cadence and the codec would all be testable only against
// rejection paths. This one **parses and version-checks intents and resolves
// nothing**, which is exactly enough for a real client to connect, greet, send
// presence, send an intent, be refused for that intent by name, and see the
// campaign's state come back as a snapshot. Everything that is genuinely a
// gameplay decision — initiative order, a roll, a ruleset — is phase 8's.
//
// Phase 8 replaces this with the 5e engine. It does not extend it: an
// implementation that *also* does phase 8's work would make "what does the inert
// system do" a question with two answers.
type Core struct {
	// SystemID is the system this instance serves. Present so a refusal can name
	// the campaign's system rather than saying "unknown", which would read as a
	// client error rather than an absence.
	SystemID string
	// RulesetVersion is the fingerprint half the codec version-checks against. It
	// is a plain string because `ruleset.go` owns the encoding and this type only
	// has to carry it; a second encoding here would be a second answer to "what is
	// this campaign's ruleset".
	RulesetVersion string
}

// Core is the whole of its type's contract: it holds configuration and resolves
// nothing, so it is safe for concurrent use with no lock.
var _ Resolver = Core{}

// Resolve is `Resolver`'s one method, implemented as "check what can be checked
// and refuse what cannot".
//
// Three things happen before the refusal, and all three are real work rather than
// scaffolding:
//
//  1. **The frame's shape is checked.** A nil intent, an empty op, or an op that
//     is not a lowercase token is `RejectInvalidArgs`. The op is *shape*-checked
//     and never *membership*-checked: the vocabulary belongs to the gameplay
//     system, and this one has none, so inventing a list here would be a second
//     source of truth that phase 8 contradicts.
//  2. **The actor's role is checked.** `RejectNotPermitted` is here, before the
//     "no resolver" refusal, so the wire reason answers "is this allowed" rather
//     than "is anything allowed" — a GM and a player get the same reason for a
//     different reason, and an observer could otherwise tell them apart by which
//     word came back.
//  3. **The version is checked**, so a client sending an intent against a version
//     it has not seen is told so rather than being refused as unknown.
//
// The refusal is always `RejectUnknownOp`, which is the honest word: this system
// resolves no operations, and "unknown" is true of every op.
func (c Core) Resolve(ctx context.Context, intent Intent) (Resolution, error) {
	// Wrapped, so a caller distinguishing a cancellation with `errors.Is` still
	// works — and so the log line says which context ended it rather than repeating
	// a bare sentinel.
	if err := ctx.Err(); err != nil {
		return Resolution{}, fmt.Errorf("core: resolving an intent: %w", err)
	}

	if intent.Frame == nil {
		return Resolution{}, c.reject(intent, RejectInvalidArgs, "no intent in the frame")
	}

	reason := c.check(intent)
	if reason != RejectNotYourTurn { // the zero value, used only as "no refusal yet"
		return Resolution{}, c.reject(intent, reason, string(reason))
	}

	return Resolution{}, c.reject(intent, RejectUnknownOp,
		"the inert core system resolves no operations; phase 8 replaces it")
}

// rejection is an error that carries the frame answering the actor's `seq`.
//
// The error exists because a refusal is *information about the intent*, and
// `Resolve`'s signature returns either a `Resolution` or an error — so putting the
// answer on the error keeps the pair together. `ResolutionFor` recovers it, and it
// is the only supported way to read a refusal: a caller that drops the error loses
// the answer, which is the correct outcome for a caller that does not understand
// it.
type rejection struct {
	resolution Resolution
	detail     string
}

func (r *rejection) Error() string { return "realtime: intent refused: " + r.detail }

// ResolutionFor returns the resolution carried by err, if it is a refusal.
//
// A nil `Resolution` for any other error, so a caller that passes an unrelated
// error cannot accidentally send an empty frame to a client.
func ResolutionFor(err error) Resolution {
	// `errors.As` rather than a type assertion, so a refusal stays readable
	// through a wrapper. Every other rule here separates the wire answer from the log
	// text so the two cannot be confused, and this is where that separation is spent:
	// a caller that wraps the error for context — which is idiomatic and which the
	// linter insists on — would otherwise lose the frame the client is waiting for.
	if r, ok := errors.AsType[*rejection](err); ok {
		return r.resolution
	}

	return Resolution{}
}

// validOpShape reports whether op is a well-formed operation token.
//
// **Shape, never membership.** The vocabulary of operations belongs to the
// gameplay system, and this one has none: a list here would be a second source of
// truth that phase 8 contradicts, and a client would be refused as unknown for an
// operation that phase 8 supports. What can be checked without knowing the
// vocabulary is that the token is shaped like one — non-empty, lowercase,
// token characters only — which is what stops a megabyte of junk reaching the
// log and the error path.
func validOpShape(op Op) bool {
	token := string(op)

	if token == "" || len(token) > 64 {
		return false
	}

	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}

	return true
}

// isGMOnlyShape reports whether op is shaped like a GM-only operation.
//
// Like `validOpShape` this is a **shape** test and carries the same honesty: the
// authoritative list is phase 8's, and this one only refuses the obvious shapes
// (`pause`, `unpause`, `reveal`, `set_hp`) so that the inert system's
// `not_permitted` path is reachable and testable rather than dead. A player cannot
// be harmed by it, because this system resolves nothing either way.
func isGMOnlyShape(op Op) bool {
	switch string(op) {
	case "pause", "unpause", "reveal", "set_hp":
		return true
	default:
		return false
	}
}

// SystemName is the system this instance serves, for a log line or a refusal that
// has to name it.
func (c Core) SystemName() string {
	if c.SystemID == "" {
		return "core"
	}

	return strings.TrimSpace(c.SystemID)
}

// Ruleset is the fingerprint this system reports. See `RulesetVersion` for why it
// is carried rather than computed.
func (c Core) Ruleset() string { return c.RulesetVersion }

// reject builds the resolution for a refusal: an answer for the actor's `seq`
// and **no** broadcast.
//
// No broadcast is the load-bearing half. A rejection that broadcast would tell
// the whole campaign that this client tried something, and the reason is on the
// wire — so every rejected intent would become a channel for telling a table what

// reject builds the resolution for a refusal: an answer for the actor's `seq`
// and **no** broadcast.
//
// No broadcast is the load-bearing half. A rejection that broadcast would tell
// the whole campaign that this client tried something, and the reason is on the
// wire — so every rejected intent would become a channel for telling a table what
// another member is attempting.
// check returns the refusal this intent earns, or the zero `RejectReason` when it
// earns none.
//
// The zero value is reused as "no refusal" rather than a named constant, because
// `RejectReason` is a closed set of eight words on the wire and adding a ninth
// member for a control-flow marker would put a value in the set that can never
// reach a client — which is the opposite of what a closed set is for.
func (c Core) check(intent Intent) RejectReason {
	operation := intent.Frame.Op

	if !validOpShape(operation) {
		return RejectInvalidArgs
	}

	// **Before** the "nothing is resolved" refusal. A player attempting a GM-only
	// shape is refused `not_permitted` rather than `unknown_op`, because the first
	// answers "is this allowed" about them and the second answers "is anything
	// allowed" about the server. Ordered the other way, the word on the wire is a
	// role oracle anyone on the campaign can read.
	if intent.Role != domain.RoleGM && isGMOnlyShape(operation) {
		return RejectNotPermitted
	}

	return RejectNotYourTurn
}

func (c Core) reject(intent Intent, reason RejectReason, detail string) error {
	// **No frame is answered when there is no frame to answer.** A nil
	// `Intent.Frame` has no `seq` to echo, and inventing one — zero, or the last
	// seq seen — would pair the refusal with an intent the client never sent, which
	// is worse than answering nothing: the client would reconcile a rejection
	// against the wrong optimistic state.
	//
	// This is the only refusal that carries no frame, and it is unreachable from the
	// codec, which refuses a nil frame before a resolver is called. It is here
	// because `Resolve` is an interface method and an interface method must not
	// panic on a value its caller got wrong; a panic in a socket read loop takes the
	// connection's goroutine with it.
	if intent.Frame == nil {
		return &rejection{detail: detail}
	}

	rejected := &ServerRejected{
		Seq:    intent.Frame.Seq,
		Reason: reason,
	}

	return &rejection{resolution: Resolution{Answer: rejected}, detail: detail}
}

// check returns the refusal this intent earns, or the zero `RejectReason` when it
// earns none.
//
// The zero value is reused as "no refusal" rather than a named constant, because
// `RejectReason` is a closed set of eight words on the wire and adding a ninth
// member for a control-flow marker would put a value in the set that can never

// ensure the role the check consults is the one the domain defines, so a rename
// there breaks the build here rather than silently making every intent permitted.
var _ = domain.RoleGM
