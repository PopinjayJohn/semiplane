package rules

import (
	"fmt"
	"slices"
)

// maxOpLen bounds an op name at 64 bytes.
//
// The wire's own bound, mirrored rather than invented: `realtime` refuses an op
// token over 64 bytes on the way in (`protocol.go`'s `maxToken`), so an op this
// package accepted and the wire refused would be an op no client could ever send —
// a system whose vocabulary the protocol silently truncates. The charset below is
// that file's `validOpToken` rule exactly, for the same reason.
const maxOpLen = 64

// Op is an operation name in a gameplay system's vocabulary: `roll`, `set_hp`,
// `move_token`, and whatever else that system resolves.
//
// A string and not an enum, for the reason `realtime.Op` is one: **membership is the
// system's answer and not this package's.** §10.2 states the structural difference
// between the tiers — a gameplay plugin defines the operation vocabulary — so a list
// of ops here would be a list a plugin could not join, and adding a system would be
// an edit to this file. What *is* checked is **shape**: lower case, digits and
// underscores, bounded. That is a property of the bytes rather than of the system,
// and it holds whoever resolved the intent.
//
// S-10.3 is the other half and lives in the registry rather than here: a UI plugin
// may emit an op only if some registered system resolves it, so "may this be sent"
// is a question about the process's systems and not about the op's spelling.
type Op string

// ErrInvalidOp is returned for an op outside the shape `Valid` enforces.
var ErrInvalidOp = fmt.Errorf("%w: the operation name is not a usable op token", ErrMalformedSystem)

// String returns the stored text, verbatim.
//
// Identity and not a validity check, as with `ID.String`: a refusal that prints the
// op it choked on is what makes a log line actionable.
func (o Op) String() string {
	return string(o)
}

// Valid reports whether op is shaped like an operation the wire can carry.
//
// Shape and never membership, and the distinction is the same one
// `realtime.validOpToken` draws. The charset is identical to that file's rule —
// lower-case letters, digits and underscores, at most 64 bytes — so an op this
// accepts is an op the codec accepts, and an op the codec refuses is refused here
// first with a message that names the operation.
func (o Op) Valid() bool {
	if o == "" || len(o) > maxOpLen {
		return false
	}

	for _, char := range string(o) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
		default:
			return false
		}
	}

	return true
}

// Intent is one validated operation to resolve.
//
// "Validated" is load-bearing and means **the envelope, not the payload**. By the
// time an `Intent` exists the access gate has admitted the connection, the codec
// has decoded the frame with unknown fields refused, the op is one a registered
// system resolves, the target object exists and the version the client claims has
// been checked. None of that is re-done here, and none of it is this type's job.
//
// What is left is the system-typed part: an op name, the object it acts on, and the
// system's own arguments. A system decodes `Args` itself, and the reason is the one
// `protocol.go` gives for its flat `IntentArgs` — the file that names the arguments
// would need an edit from every system, and here that file is one every system has
// read.
//
// **No `seq` field, deliberately.** The `seq` pairs a client frame with its answer,
// and that is the hub's bookkeeping: a system has no frame to answer and no client
// waiting. Carrying it would invite a system to echo it into an op payload, and the
// payload is opaque — so the echo would arrive as a number nobody could check
// against the frame that carried it.
type Intent struct {
	// Op is the operation, in the vocabulary of the system this intent is routed to.
	Op Op

	// Target is the game object the operation acts on, empty for an operation with
	// no object — a campaign-wide pause, a roll that names nothing on the table.
	//
	// **This is the only address the hub honours.** It is semiplane's field because
	// addressing a placement is semiplane's job; the `Args` payload is the system's.
	// A system whose own arguments repeat a placement id is describing a second
	// address, and where the two disagree `Target` wins — because the alternative is
	// the hub decoding a system's private schema to find out where to apply the
	// change, which is the boundary this package exists to hold.
	Target ObjectID

	// Args are the system's parameters, encoded by the system.
	//
	// Semiplane transports them, bounds them and never interprets them: they reach
	// the resolver, and the resolver-shaped payload that comes back in a
	// `Mutation` is equally opaque. S-12.3 is why that matters in both directions —
	// nothing here may reach a log line, because nothing here has been shown to be
	// free of dice results or vault content.
	Args []byte
}

// NewIntent returns an intent, refusing one the hub could not have produced.
//
// Refused: an op outside the wire's token shape, a non-empty target that is not a
// usable object id, and arguments over `MaxOpArgsLen`.
//
// The copies are the point of the constructor. The bytes handed to it are the
// frame's, and a frame's buffer is the transport's — so a resolver that scribbled
// on them would be editing a buffer the codec still holds, and the corruption would
// surface as a rejected frame on an unrelated connection. `NewIntent` takes its
// own bytes, and after it returns nothing shares an array with the caller.
func NewIntent(operation Op, target ObjectID, args []byte) (Intent, error) {
	if !operation.Valid() {
		return Intent{}, fmt.Errorf("%w: %q", ErrInvalidOp, operation)
	}

	if target != "" && !target.Valid() {
		return Intent{}, fmt.Errorf("%w: %q", ErrInvalidObject, target)
	}

	if len(args) > MaxOpArgsLen {
		return Intent{}, fmt.Errorf("%w: %d bytes, maximum is %d",
			ErrOpArgsTooLarge, len(args), MaxOpArgsLen)
	}

	return Intent{Op: operation, Target: target, Args: slices.Clone(args)}, nil
}

// Valid reports whether the intent is shaped like one a hub could have produced.
//
// A predicate and not a check, as with `ID.Valid` and `Kind.Valid`: it answers
// whether the value is well formed, and `NewIntent` is what produces one that is.
// The zero `Intent` is **invalid** — an empty op is no operation — so a caller that
// forgot to build one is told rather than handing a resolver an intent that resolves
// nothing and says nothing about why.
func (in Intent) Valid() bool {
	if !in.Op.Valid() {
		return false
	}

	if in.Target != "" && !in.Target.Valid() {
		return false
	}

	return len(in.Args) <= MaxOpArgsLen
}

// String renders the intent's address and op for a log line.
//
// **Never the arguments.** They are the system's payload, unexamined, and S-12.3
// forbids an event carrying dice results or vault content — a `String` method is
// exactly where such a value would slip in, which is the argument
// `content.Change.String` makes for its own half.
func (in Intent) String() string {
	if in.Target == "" {
		return in.Op.String()
	}

	return in.Op.String() + " " + in.Target.String()
}
