package identity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/store"
)

// fakeReader is a scripted stand-in for the two store reads identity needs.
//
// Scripted on the hash rather than on the token, because the hash is the only
// value the production code is allowed to hand it — a fake keyed on the token
// would let a test pass while the code still forwarded the credential.
type fakeReader struct {
	session store.AuthSession
	user    domain.User
	err     error
}

func (f *fakeReader) SessionByTokenHash(_ context.Context, _ string) (store.AuthSession, error) {
	return f.session, f.err
}

func (f *fakeReader) UserByID(_ context.Context, _ int64) (domain.User, error) {
	return f.user, f.err
}

// validReader is a reader holding one unexpired session for user 7.
func validReader() *fakeReader {
	return &fakeReader{
		session: store.AuthSession{
			TokenHash: "hash",
			UserID:    7,
			ExpiresAt: time.Now().Add(time.Hour),
		},
		user: domain.User{ID: 7, Username: "ada", IsAdmin: true},
	}
}

// requestWithCookie returns a request carrying a valid session cookie.
func requestWithCookie(t *testing.T) *http.Request {
	t.Helper()

	token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})

	return r
}

// serve runs one request through Authenticate and reports the requestor the
// handler saw.
func serve(t *testing.T, reader identity.Reader, r *http.Request) domain.Requestor {
	t.Helper()

	var seen domain.Requestor

	handler := identity.Authenticate(
		reader,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			seen = identity.Requestor(req.Context())
		}),
	)
	handler.ServeHTTP(httptest.NewRecorder(), r)

	return seen
}

// TestAuthenticateResolvesSession is the ordinary case: a valid cookie becomes
// the identity the session names, including is_admin.
func TestAuthenticateResolvesSession(t *testing.T) {
	t.Parallel()

	seen := serve(t, validReader(), requestWithCookie(t))

	if !seen.Authenticated {
		t.Fatal("requestor is not authenticated, want authenticated")
	}

	if seen.UserID != 7 {
		t.Errorf("UserID = %d, want 7", seen.UserID)
	}

	if seen.Username != "ada" {
		t.Errorf("Username = %q, want %q", seen.Username, "ada")
	}

	if !seen.IsAdmin {
		t.Error("IsAdmin = false, want true: is_admin is carried onto the identity")
	}
}

// TestAuthenticateAnonymousWithoutCookie is the pre-login case, and the reason
// the zero Requestor is safe to hand out: it is unauthenticated, so
// ResolveAccess can never read it as a member.
func TestAuthenticateAnonymousWithoutCookie(t *testing.T) {
	t.Parallel()

	seen := serve(t, validReader(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if seen.Authenticated {
		t.Fatal("requestor is authenticated with no cookie, want anonymous")
	}

	if seen.UserID != 0 {
		t.Errorf("UserID = %d, want 0", seen.UserID)
	}
}

// TestAuthenticateAnonymousWhenSessionLookupFails is the enumeration defence.
//
// An unknown token and a database failure are the same answer, and they are the
// same answer as no cookie at all. If a test could tell those three apart it
// would be describing a handler that can, which is the oracle S-14.4 and
// store.ErrNotFound both exist to prevent.
func TestAuthenticateAnonymousWhenSessionLookupFails(t *testing.T) {
	t.Parallel()

	reader := validReader()
	reader.err = errors.New("no such row")

	seen := serve(t, reader, requestWithCookie(t))

	if seen.Authenticated {
		t.Fatal("requestor is authenticated after a failed session lookup, want anonymous")
	}
}

// TestAuthenticateAnonymousWhenUserLookupFails covers the row outliving its
// user: deleting an account that held no membership leaves its sessions behind,
// and those sessions must not keep acting.
func TestAuthenticateAnonymousWhenUserLookupFails(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		session: store.AuthSession{
			TokenHash: "hash",
			UserID:    7,
			ExpiresAt: time.Now().Add(time.Hour),
		},
		err: errors.New("no such row"),
	}

	seen := serve(t, reader, requestWithCookie(t))

	if seen.Authenticated {
		t.Fatal("requestor is authenticated for a deleted user, want anonymous")
	}
}

// TestAuthenticateAnonymousForSessionNamingNoUser is the malformed-row case: a
// session whose user_id is 0 names nobody, and naming nobody is anonymous rather
// than user 0.
func TestAuthenticateAnonymousForSessionNamingNoUser(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		session: store.AuthSession{
			TokenHash: "hash",
			UserID:    0,
			ExpiresAt: time.Now().Add(time.Hour),
		},
		user: domain.User{ID: 0},
	}

	seen := serve(t, reader, requestWithCookie(t))

	if seen.Authenticated {
		t.Fatal("requestor is authenticated for a session naming no user, want anonymous")
	}
}

// TestRequestorWithoutContextIsAnonymous covers a route mounted outside the
// chain. It degrades to anonymous rather than panicking, and a request that was
// never authenticated is not one that may be treated as a member.
func TestRequestorWithoutContextIsAnonymous(t *testing.T) {
	t.Parallel()

	seen := identity.Requestor(t.Context())

	if seen.Authenticated {
		t.Fatal("Requestor on a bare context is authenticated, want anonymous")
	}
}
