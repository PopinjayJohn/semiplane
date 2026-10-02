package edit

// Which of the content layer's answers is answered with which status.
//
// The classification is a copy of `internal/httpapi/wiki/failure.go` and it is a
// copy rather than a shared package for a reason worth stating: a shared
// `httpapi/refusals` package would be the tidier answer, and it becomes the right
// one the moment a third route needs the same three-way distinction. Until then
// the duplication is forty lines in the package that needs them, and the
// alternative is a package whose entire content is a type and a switch — which
// reviewers read less carefully than the switch they are reviewing.
//
// The three answers are distinct types of fact, and conflating any two of them is
// a security bug rather than a simplification:
//
//   - **ErrNotExist** — inside the campaign's root, not there. A 404, and the only
//     answer here that is a statement about existence.
//   - **ErrOutsideRoot / ErrSymlink** — the path leaves the root, or runs through
//     a link the policy refuses. A refusal, and never a 404: a 404 says "there is
//     nothing at this path", and answering it for a path that left the campaign
//     turns a save into a probe for the layout of the host's filesystem.
//   - **ErrInvalidRef** — the path never named a location. A 400, because the
//     input is malformed rather than hostile.
//
// The refusals' text carries no path, and neither does any response here. The
// path is in the access log, which the middleware wrote before this handler ran;
// a 403 that echoed the attempted path would undo `content.Root`'s confinement in
// one string.

import (
	"errors"
	"net/http"

	"github.com/semiplane/semiplane/internal/content"
)

// refusal is a classified failure: an error and the status it is answered with.
//
// A value travelling with the error rather than a status returned beside it, so
// that the classification cannot be forgotten: a handler that forgets to classify
// gets the default 500, which is the safe direction, and there is one function to
// change rather than one per call site.
type refusal struct {
	status int
	err    error
}

// Error returns the underlying failure's message, which for a refusal is a fixed
// sentence and never the caller's path.
func (r refusal) Error() string {
	return r.err.Error()
}

// Unwrap keeps `errors.Is` reaching the content layer's own sentinel, so a caller
// — or a test — can ask what happened rather than only how it was answered.
func (r refusal) Unwrap() error {
	return r.err
}

// classify turns one of the content layer's answers into a refusal and leaves
// everything else alone.
//
// A refusal is a value so the default is the 500 rather than the 404: an error
// this function has not been taught about is an error whose answer is unknown,
// and "unknown" must not become "there is nothing at this path".
//
// `ErrDocumentTooLarge` is the one place the two routes differ, and deliberately:
// the wiki route answers it 500 because a file on disk being too large is an
// operator's problem, while here it is the *request body* being too large, which
// is RFC 9110's 413 and a fact about the request the reader can act on.
func classify(err error) error {
	switch {
	case errors.Is(err, content.ErrNotExist), errors.Is(err, content.ErrNotDir):
		return refusal{status: http.StatusNotFound, err: err}
	case errors.Is(err, content.ErrOutsideRoot), errors.Is(err, content.ErrSymlink):
		return refusal{status: http.StatusForbidden, err: err}
	case errors.Is(err, content.ErrInvalidRef):
		return refusal{status: http.StatusBadRequest, err: err}
	case errors.Is(err, content.ErrDocumentTooLarge):
		return refusal{status: http.StatusRequestEntityTooLarge, err: err}
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
