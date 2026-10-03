// Package realtime holds the protocol's own types, the hub's in-memory authority, and
// the refusal that carries its own frame: `ResolutionFor` and the error behind it.
//
// # Why this file exists, and what phase 8 removed from it
//
// This was `core.go`'s second half. ADR 0039 decided that phase 8 **replaces** the
// inert `Core` resolver rather than extending it, and it did — `internal/plugin`'s
// `Resolver` is what the hub resolves an intent with now, and `plugin.RejectionError`
// is the refusal a system produces. `Core` and its shape tests are deleted, and the
// gate that enforced "phase 8 replaces this type" is this file's absence from them.
//
// **The refusal-carrier was not part of that decision, and deleting it would have
// broken the UI tier for no gain.** `rejection` is not a property of the inert system:
// it is the only way a resolver's answer to *one particular client frame* reaches that
// client, and the client is not always a socket peer. §10.6's dice roller dispatches
// over HTTP with no frame behind it, and `dice.ReadRefusal` reads a refusal in both
// shapes deliberately — a `rejected` frame when the hub resolved for a socket, and a
// `*plugin.RejectionError` when the refusal came from a form post. The first shape
// arrives through `ResolutionFor`, so deleting it would have made the roller answer "no
// refusal" for every refusal a socket produced, which is the silent-half-working
// defect this repository keeps paying for.
//
// So the type moved and the reasoning came with it. What is gone is `Core`: the type
// that resolved nothing, the `SystemName` and `Ruleset` accessors only it had, and the
// two shape tests (`validOpShape`, `isGMOnlyShape`) that existed so the inert system
// could reach `not_permitted` at all. `protocol.go`'s `validOpToken` is the codec's
// own shape check and was always the one on the request path; the two were a second
// answer to the same question and the resolver's copy is now gone with the resolver
// that asked it.
//
// # What this file promises, and to whom
//
// A refusal is *information about one intent*, and `Resolver`'s signature returns
// either a `Resolution` or an error — so the frame that answers the actor's `seq`
// travels **on the error**. A caller that drops the error drops the answer, which is
// the correct outcome for a caller that does not understand it, and the reason
// `ResolutionFor` returns a **zero** `Resolution` for any other error: a caller handed
// an unrelated failure must not be able to send an empty frame to a client it thought
// it was answering.
package realtime

import "errors"

// rejection is an error that carries the frame answering the actor's `seq`.
//
// The fields are unexported and the only constructor is `Refuse`, so the frame on the
// error is always one this package built for a `seq` the caller named — and the only way
// to read it back is `ResolutionFor`, which is the supported reader by construction
// rather than by convention.
type rejection struct {
	resolution Resolution
	detail     string
}

func (r *rejection) Error() string { return "realtime: intent refused: " + r.detail }

// Refuse returns an error carrying the `rejected` frame that answers seq.
//
// **The constructor a resolver uses, and it exists because the inert `Core` used to be
// the only thing that made one.** ADR 0039 deleted `Core`, and with it the only reachable
// path to `*rejection` — which left `ResolutionFor` with a shape no caller could produce
// and §10.6's dice roller reading a refusal from a hub that could never produce one. A
// UI plugin's dispatch (`Hub.Dispatch`, as opposed to `Apply` over a socket) is exactly
// the case this is for: there is no peer to send a frame to, so the frame travels on the
// error and the page renders it.
//
// `detail` is the log-line half and **is never printed to a client**: `Error` returns
// only the detail, and the *reason* is the closed word on the wire. So a detail that
// quotes a client's own payload reaches an operator's log and not a browser — which is
// the split `RejectionError` above states and the reason this is a separate type rather
// than an alias of it.
//
// **A zero seq refuses nothing and carries nothing.** There is no frame to build: a
// client that sent nothing cannot be paired with an answer, and answering with an
// invented `seq` would leave a client reconciling a rejection against the wrong
// optimistic state. So the zero value returns a bare error, which is what a caller with
// no client frame behind it wants — the reason it exists at all.
func Refuse(seq ClientSeq, reason RejectReason, detail string) error {
	if seq == 0 {
		return &rejection{detail: detail}
	}

	return &rejection{
		resolution: Resolution{Answer: &ServerRejected{
			Type:   TypeRejected,
			Seq:    seq,
			Reason: reason,
		}},
		detail: detail,
	}
}

// ResolutionFor returns the resolution carried by err, if it is a refusal.
//
// **The only supported way to read a refusal's frame**, and the reason it is a function
// rather than a type assertion at each call site: `errors.As`, so a refusal stays
// readable through a wrapper. A caller that wraps the error for context — which is
// idiomatic and which the linter insists on (`wrapcheck`) — would otherwise lose the
// frame the client is waiting for, and the failure would look like a hub that answered
// nothing.
//
// A zero `Resolution` for every other error, so a caller cannot accidentally send an
// empty frame to a client. That is the same reason `reject` refuses to answer a nil
// frame: a pair of answers is worse than one, because a client reconciles each.
func ResolutionFor(err error) Resolution {
	// `errors.As` rather than a type assertion, so a refusal stays readable through a
	// wrapper. Every other rule here separates the wire answer from the log text so the
	// two cannot be confused, and this is where that separation is spent.
	if r, ok := errors.AsType[*rejection](err); ok {
		return r.resolution
	}

	return Resolution{}
}
