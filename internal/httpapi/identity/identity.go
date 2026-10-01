// Package identity resolves the identity behind a request and carries it on the
// request context.
//
// It sits between the credential primitives in `internal/httpapi/auth` and the
// access decision in `internal/domain`: this package turns a session cookie into
// a domain.Requestor, and `domain.ResolveAccess` turns a Requestor plus a
// campaign into a tier. Neither half decides anything on its own, and there is
// no third place where the two are combined — that combination is the S-8 matrix,
// and the matrix living in one function is what stops a second one appearing.
//
// The package is separate from `auth` because `auth` is deliberately
// dependency-free: it hashes, mints and compares, touches no database and reads
// no configuration. The moment a session cookie has to become a user row, that
// package would have to import the store, and the credential primitives would
// stop being testable with no schema.
package identity

import (
	"context"
	"net/http"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/store"
)

// SessionReader is the one store query this package needs.
//
// A narrow interface rather than a *store.Store, so a test can supply a fake
// without opening a database. `store.Store` satisfies it structurally.
type SessionReader interface {
	SessionByTokenHash(ctx context.Context, tokenHash string) (store.AuthSession, error)
}

// UserReader is the second query: the session names a user, and the identity is
// that user.
type UserReader interface {
	UserByID(ctx context.Context, id int64) (domain.User, error)
}

// Reader is both reads, which is what a live request needs.
type Reader interface {
	SessionReader
	UserReader
}

type requestorKey struct{}

// Anonymous is the requestor for a request that proved no identity.
//
// A value rather than a pointer so no caller has to nil-check before asking a
// question. It is deliberately not a Requestor with `UserID` set and
// `Authenticated` false: domain.Requestor carries the two fields separately
// precisely so that a half-built identity cannot be read as a real one, and
// ResolveAccess treats an unauthenticated requestor as at most read-only.
func Anonymous() domain.Requestor {
	return domain.Requestor{}
}

// WithRequestor returns a context carrying req as the request's identity.
func WithRequestor(ctx context.Context, req domain.Requestor) context.Context {
	return context.WithValue(ctx, requestorKey{}, req)
}

// Requestor returns the identity resolved for this request, or Anonymous.
//
// The zero value is returned when the context carries none, so a route mounted
// outside the authentication middleware degrades to anonymous instead of
// panicking. That is the safe direction: a request that was not authenticated is
// not one that may be treated as a member.
func Requestor(ctx context.Context) domain.Requestor {
	req, ok := ctx.Value(requestorKey{}).(domain.Requestor)
	if !ok {
		return Anonymous()
	}

	return req
}

// Authenticate resolves the session cookie and puts the identity on the
// context.
//
// It never fails a request and never writes a status. A missing, malformed,
// unknown, expired or revoked cookie is five different facts that all mean the
// same thing to a handler — this request is anonymous — and a middleware that
// could tell them apart would be the account-enumeration oracle that
// store.ErrNotFound is written to refuse. Requiring authentication is a route's
// decision, made where the route's access tier is known.
//
// A store failure is also anonymous, and that is a real trade rather than an
// oversight. A broken database makes every request anonymous, which is
// visible: the symptom is that the login form stops working and the campaign
// list is empty, not that one user's session quietly stops resolving. The
// alternative — propagating the error and 500ing the request — turns a
// transient database fault into a difference in behaviour between a request
// with a cookie and one without, which is the same oracle wearing a hat.
func Authenticate(reader Reader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(
				WithRequestor(r.Context(), resolve(r.Context(), reader, r)),
			))
		})
	}
}

// resolve turns one request into an identity, or into Anonymous.
func resolve(ctx context.Context, reader Reader, r *http.Request) domain.Requestor {
	token, ok := auth.SessionToken(r)
	if !ok {
		return Anonymous()
	}

	// Hashed here, and the raw token is a local that is never logged, never
	// returned and never reaches the store. From this line on the only copy of
	// the credential outside the browser is the hash.
	session, err := reader.SessionByTokenHash(ctx, auth.HashSessionToken(token))
	if err != nil || session.UserID <= 0 {
		return Anonymous()
	}

	// A session whose user has been deleted resolves to anonymous rather than to
	// an error. The row outliving its user is a normal consequence of the
	// RESTRICT on `campaign_members` (migration 0006): an account that was never
	// in a campaign can be removed while its sessions remain. Treating that as a
	// valid identity would mean a deleted account could still act for as long as
	// the browser offered the cookie.
	user, err := reader.UserByID(ctx, session.UserID)
	if err != nil {
		return Anonymous()
	}

	return domain.Requestor{
		UserID:        user.ID,
		Username:      user.Username,
		IsAdmin:       user.IsAdmin,
		Authenticated: true,
	}
}
