package secrets

// Which of the content layer's answers is answered with which status, and which
// refusals are this route's own.
//
// The classification is a copy of `internal/httpapi/edit/failure.go` and of
// `internal/httpapi/wiki/failure.go`, and it is a copy rather than a shared package
// for a reason worth stating, because it is now the third: a shared
// `httpapi/refusals` package would be the tidier answer, and it becomes the right
// one the moment a third route needs the same three-way distinction. Until then the
// duplication is forty lines in each package that needs them, and the alternative is
// a package whose entire content is a type and a switch — which reviewers read less
// carefully than the switch they are reviewing. If a **fourth** route copies this
// again, extract it then; that is what `edit`'s own note asks for and this is the
// note that counts it.
//
// The three answers are distinct types of fact, and conflating any two of them is a
// security bug rather than a simplification:
//
//   - **ErrNotExist** — inside the campaign's root, not there. A 404, and the only
//     answer here that is a statement about existence.
//   - **ErrOutsideRoot / ErrSymlink** — the path leaves the root, or runs through a
//     link the policy refuses. A refusal, and never a 404: a 404 says "there is
//     nothing at this path", and answering it for a path that left the campaign turns
//     a reveal into a probe for the layout of the host's filesystem.
//   - **ErrInvalidRef** — the path never named a location. A 400, because the input
//     is malformed rather than hostile.
//
// The refusals' text carries no path, and neither does any response here. The path is
// in the access log, which the middleware wrote before this handler ran; a 403 that
// echoed the attempted path would undo `content.Root`'s confinement in one string.

import (
	"errors"
	"net/http"

	"github.com/semiplane/semiplane/internal/content"
)

// refusal is a classified failure: an error and the status it is answered with.
//
// A value travelling with the error rather than a status returned beside it, so that
// the classification cannot be forgotten: a handler that forgets to classify gets the
// default 500, which is the safe direction, and there is one function to change rather
// than one per call site.
type refusal struct {
	status int
	err    error
}

// Error returns the underlying failure's message, which for a refusal is a fixed
// sentence and never the caller's path.
func (r refusal) Error() string {
	return r.err.Error()
}

// Unwrap keeps `errors.Is` reaching the content layer's own sentinel, so a caller —
// or a test — can ask what happened rather than only how it was answered.
func (r refusal) Unwrap() error {
	return r.err
}

// classify turns one of the content layer's answers into a refusal and leaves
// everything else alone.
//
// A refusal is a value so the default is the 500 rather than the 404: an error this
// function has not been taught about is an error whose answer is unknown, and
// "unknown" must not become "there is nothing at this path" — or, on this route,
// "there is nothing at this path" would become a statement about a secret.
//
// **`ErrDocumentTooLarge` is not taught here at all**, and that is the one place
// this route differs from the editor's copy on purpose. For the editor, an oversized
// document is the *request body* being too large, which is RFC 9110's 413. For this
// route the document is a page on disk, and a page on disk being too large is an
// operator's problem that the editor's read path answers 500 — a page the watcher
// indexed is a page the reader could be served. The request body's own cap is
// `maxRevealBody` and is classified in `classifyBody`, so both sizes are answered
// and neither is answered by the other's rule.
//
// # This is the one table of "which error is which status"
//
// **The route's own four sentinels are taught here too**, and that is the point of
// naming them in this file rather than wrapping each one where it is raised. The default
// is 500, and that default is right for an error the route does not recognise — an
// unknown failure must not become "there is nothing at this path". But `errNoSelector`
// and friends are *known*, and a request that named no secret arriving as a 500 would
// send a GM hunting for a database problem when the body they sent was wrong.
//
// So a reader asking why a request whose secret does not exist answers 404 while one
// whose secret cannot be revealed answers 400 finds it in the switch below: the first
// is a statement about existence and therefore shares its bytes with every other 404,
// and the second is a statement about the request.
// `TestEveryRefusalThisRouteRaisesIsClassified` walks the sentinels and requires each to
// answer with something other than the 500 default, so a new sentinel added without an
// entry here is a test failure rather than a 500 in production.
func classify(err error) error {
	switch {
	case errors.Is(err, content.ErrNotExist), errors.Is(err, content.ErrNotDir):
		return refusal{status: http.StatusNotFound, err: errNotFound}
	case errors.Is(err, content.ErrOutsideRoot), errors.Is(err, content.ErrSymlink):
		// The content layer's own message, not `errNotFound`. This is the one place
		// a 403 and a 404 carry different bodies on this route, and it is the same
		// decision `edit` and `wiki` make for the same reason: the status already
		// distinguishes "that path is outside your campaign" from "there is nothing
		// there", and repeating the distinction in the body adds nothing that a
		// reader could use — because the only reader who reaches this line is a GM,
		// who already owns every byte in the content root. The message carries no
		// path (`content.Root` refuses to put one in it), so it is safe either way.
		return refusal{status: http.StatusForbidden, err: err}
	case errors.Is(err, content.ErrInvalidRef):
		return refusal{status: http.StatusBadRequest, err: err}
	case errors.Is(err, errUnknownSecret):
		return refusal{status: http.StatusNotFound, err: errNotFound}
	case errors.Is(err, errNoSelector), errors.Is(err, errNoState),
		errors.Is(err, errNestedReveal):
		return refusal{status: http.StatusBadRequest, err: err}
	default:
		return err
	}
}

// statusFor answers a classified failure, and 500 for anything unclassified.
func statusFor(err error) int {
	if found, ok := errors.AsType[refusal](err); ok {
		return found.status
	}

	return http.StatusInternalServerError
}

// The refusals this route raises itself, as fixed sentences.
//
// Named rather than formatted at the call site, and for the reason
// `internal/httpapi/campaigns`'s `writeError` gives: an error whose text varies with
// the caller's input is a channel. Each is a **constant**, so the response body for
// "you named no secret" is the same bytes for every caller.
//
// **`errNotFound` is the load-bearing one, and it is what makes the 404 an
// existence oracle's opposite. It is spelled out here rather than reusing
// `campaigns`' unexported `"not found"`, because that function is unexported and
// `internal/httpapi/campaigns` is not this package to change — and because a copy a
// test asserts **byte for byte against the gate's own 404** is a checked copy rather
// than a parallel one. `TestTheRefusalsAreTheSameBytesAsNoAccessIs` is the check.
//
// Two refusals share it, and they are the two that must: a page that is not there and
// a secret on it that is not there. A body that said "no such secret" would tell a
// reader that the *page* exists, and a page's existence is exactly what S-8 withholds
// — the difference between "no such page" and "no such secret on it" is the whole
// existence oracle a 404 exists to close.
var (
	// errNotFound is a page that is not there, and a secret on it that is not there.
	errNotFound = errors.New("not found")

	// errMissingValidator is a write with no `If-Match` at all (RFC 6585's 428).
	errMissingValidator = errors.New("this write must carry If-Match")
)
