package secrets

// The request body: which secret, and which way.
//
// # The shape, and why it is JSON at all
//
// The endpoint is a `PUT` with a `Content-Type: application/json` body of at most
// three fields, and the alternative — naming the secret in the URL, as
// `/c/{slug}/secrets/{path...}#{anchor}` or a trailing path segment — was rejected
// for a stated reason. The **URL is the precondition's transport**: `If-Match` names
// a validator over the page, and a URL that varies per secret would make the
// precondition depend on which secret the client means, so two tabs revealing two
// different secrets on one page would have to hold different validators for the same
// page. The path names the *page* and nothing else, which is the same shape the
// editor's `PUT /c/{slug}/edit/{path...}` has and the reason its `If-Match` is a
// statement about the file rather than about the edit.
//
// # `DisallowUnknownFields`, and why an endpoint that flips one byte should reject
// a typo loudly
//
// A client that sends `{"anchro": "traitor"}` under `DisallowUnknownFields` gets a
// 400 naming the problem. Without it the same request reaches `parseReveal` with no
// anchor and no ordinal and is refused as "named no secret" — a 400 too, so the
// status is not the difference, but the *message* is, and a message that says "you
// sent no secret" for a request that sent a misspelled one sends the GM looking at
// the field name rather than at the spelling.
//
// The other end of the argument is that this body will not grow. Its three fields
// are an identity, a state and a choice of identity, and a future field would be a
// new rule rather than a new spelling of this one. A strict decoder is the honest
// posture for a small, closed vocabulary; if that ever stops being true the decoder
// is the one line to change.
//
// # The size cap is small, and deliberately not `content.MaxDocumentBytes`
//
// A reveal request is an anchor (≤ a few hundred characters, since a block id is
// `[-_A-Za-z0-9]` and a derived anchor is twelve hex characters) and two small
// fields. Reading it under the 4 MB document cap would mean a request that could
// put four megabytes into a connection to flip one byte, which is a denial of
// service aimed at a GM's connection rather than at their disk — and unlike the
// editor's cap, this one guards a socket rather than a filesystem, so nothing about
// it is a page-size concern.
//
// `http.MaxBytesReader` rather than `io.LimitReader` because the former makes the
// reader **fail** past the limit, and a decoder that hits EOF at the limit rather
// than an error would accept a truncated body whose truncation happened to leave a
// complete JSON value — which is a reveal request whose intent was longer than what
// was read.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxRevealBody is the most this endpoint will read from a request body.
//
// 4 KiB against a body whose real content is under 300 bytes. The headroom is for a
// generous block id, a future field, and whitespace, and the cap is what makes a
// client that has been tampered with — or that is simply broken — cost a bounded
// amount of the server's time.
const maxRevealBody = 4 << 10

// revealBody is the wire shape.
//
// Every field a pointer, and that is the whole of the "which way" decision. A
// `revealed` that defaulted to `false` would make `{"anchor":"traitor"}` — a request
// that said nothing about the state — a **hide**, which is the one direction a
// mistyped request must not default into: hiding a secret the GM meant to show is a
// silent change to their page, and showing one they meant to hide is a disclosure.
// So an absent `revealed` is a 400, and the client must say which it means.
//
// The `anchor` and `ordinal` pointers are for the same reason, one step weaker: an
// absent anchor falls through to the ordinal, which is §5.6.3's resolution order, and
// an `anchor` of `""` is treated as absent rather than as "a secret whose name is the
// empty string" — `store.ErrInvalidSecretAnchor` calls that a bug in the caller, and
// this endpoint answers it by using the positional selector the client also sent
// rather than by refusing.
type revealBody struct {
	// Anchor is §5.6.3's resolved name: an Obsidian block id, or twelve hex
	// characters. Preferred over `Ordinal` when both are present.
	Anchor *string `json:"anchor"`
	// Ordinal is the callout's position among the page's secrets, counting from zero
	// in document order. The fallback for a client that has no anchor.
	Ordinal *int `json:"ordinal"`
	// Revealed is which way the marker goes. Required.
	Revealed *bool `json:"revealed"`
}

// errNoSelector is a body that named no secret at all.
//
// Not `store.ErrInvalidSecretAnchor` and not a 404: the request was well-formed and
// named nothing, which is a 400 about the *request*, and the route's own sentinel so
// that the classification does not have to be re-derived by a caller.
var errNoSelector = errors.New("secrets: the request named no secret")

// errNoState is a body that named a secret but not which way it wants it.
var errNoState = errors.New("secrets: the request did not say whether to reveal")

// errNestedReveal is a reveal aimed at a callout that contains another one.
//
// The message is the reason, not the secret and not the callout: a GM has to be able
// to act on it, and "this callout contains another secret and so cannot be revealed
// on its own" is actable while "there is no rendering of a revealed outer callout
// that withholds the nested body" is a paragraph. See the package header for why the
// refusal exists at all.
var errNestedReveal = errors.New(
	"secrets: this callout contains another secret, so revealing it would reveal nothing",
)

// errUnknownSecret is a page that has no callout by the name or position the request
// gave.
//
// A 404 and **the same 404** as a page that is not there — see `writeNotFound`. It
// is deliberately not `content.UnknownSecretOrdinalError`, whose text carries the
// ordinal and the count: those are facts about the page's interior, and an error
// whose text varies with what the caller guessed is a channel. The count in
// particular would turn a 404 into a page-content oracle — "the page has 3 secrets"
// is a fact about a `[!secret]` a reader may not have been shown.
var errUnknownSecret = errors.New("secrets: no such secret on this page")

// parseReveal reads the request body into the selector the write path uses.
//
// Every failure is a 400 and every message is fixed, because this is the one place
// where the *caller's input* is described back to it. `json.SyntaxError` and
// `json.UnmarshalTypeError` carry an offset and a field name and neither is quoted
// here, because the field names are this file's and the values are the caller's — a
// message quoting a value would echo whatever the client put there, and on an
// endpoint about secrets the safe posture is to say nothing the client did not
// already know.
func parseReveal(r *http.Request) (selector, error) {
	body, err := decodeRevealBody(r)
	if err != nil {
		return selector{}, err
	}

	if body.Revealed == nil {
		return selector{}, classify(errNoState)
	}

	wanted := selector{Revealed: *body.Revealed}

	if body.Anchor != nil {
		wanted.Anchor = *body.Anchor
	}

	if wanted.Anchor != "" {
		return wanted, nil
	}

	if body.Ordinal == nil {
		return selector{}, classify(errNoSelector)
	}

	wanted.Ordinal = *body.Ordinal

	return wanted, nil
}

// decodeRevealBody reads the body under the cap and decodes it strictly.
func decodeRevealBody(r *http.Request) (revealBody, error) {
	var decoded revealBody

	capped := http.MaxBytesReader(nil, r.Body, maxRevealBody)

	reader := json.NewDecoder(capped)
	reader.DisallowUnknownFields()

	if err := reader.Decode(&decoded); err != nil {
		return revealBody{}, classifyBody(err)
	}

	// **One value, or it is refused.** `json.Decoder` reads a *stream*: without this
	// second check, `{"anchor":"a","revealed":true}{"anchor":"b","revealed":false}`
	// decodes the first object and silently ignores the second, so a request that
	// expressed two contradictory intents would be answered as though it had
	// expressed one. The second decode must find EOF, and it is the decoder's own
	// error rather than a raw `io.ReadAll` so that trailing whitespace is tolerated
	// and trailing content is not.
	if err := reader.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		if err == nil {
			return revealBody{}, classifyBody(errors.New("more than one JSON value"))
		}

		return revealBody{}, classifyBody(err)
	}

	return decoded, nil
}

// classifyBody maps a decode failure onto a refusal.
//
// A 400 for every one of them, and there is no case where a decode failure is
// anything else: a body this endpoint cannot read is a statement about the request,
// and there is no version of "unreadable" that means the server should answer 500.
// `http.MaxBytesError` is called out separately because it is the one failure that
// names a *size*, and a client that needs to know it was too large rather than
// malformed is a client that can fix it.
func classifyBody(err error) error {
	// `http.MaxBytesError`'s own `Limit`, so the message names the limit that was actually
	// exceeded rather than this file's constant. They are the same number today; the point
	// is that a message about a size should quote the size the server applied, and if the
	// cap ever moves the two cannot disagree silently.
	tooLarge, isTooLarge := errors.AsType[*http.MaxBytesError](err)
	if isTooLarge {
		return refusal{
			status: http.StatusRequestEntityTooLarge,
			err: fmt.Errorf("%w: the reveal body is over the %d-byte limit",
				errBodyTooLarge, tooLarge.Limit),
		}
	}

	// `*json.SyntaxError` and `*json.UnmarshalTypeError` both carry a fragment of the
	// caller's body in their message — the latter's `Field` is a field name this
	// endpoint chose, but `*json.UnmarshalTypeError`'s `Value` is the caller's own
	// text quoted back. Neither is wrapped here, deliberately: the reason this
	// function exists at all is to *drop* them, and the standing rule is that no error
	// leaving this package carries page content (S-12.3) — a client that POSTs its
	// secret text into the wrong field must not find it quoted in a response.
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return refusal{status: http.StatusBadRequest, err: errTruncatedBody}
	}

	return refusal{status: http.StatusBadRequest, err: errUnreadableBody}
}

// errBodyTooLarge is a reveal body over `maxRevealBody`.
var errBodyTooLarge = errors.New("secrets: reveal body too large")

// errTruncatedBody is a body that ended in the middle of a JSON value.
var errTruncatedBody = errors.New("secrets: the request body ended early")

// errUnreadableBody is a body that is not the JSON this endpoint reads.
//
// One fixed sentence for every shape of it — unparseable, wrong type, unknown field,
// more than one value — and the reason is the one above: the decoder's own messages
// quote the caller's bytes, and an endpoint about secrets has no business repeating
// them. A client that needs to know which of those it hit gets the field name from
// the specification rather than from an error string that echoes its payload.
var errUnreadableBody = errors.New("secrets: the request body is not a reveal")
