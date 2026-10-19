// Package identity provides the canonical context key for request identities.
// This breaks the import cycle between internal/api and internal/acl.
package identity

import (
	"context"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// ctxKeyIdentity is the unexported context key for the authenticated identity.
type ctxKeyIdentity struct{}

// WithIdentity stores ident in ctx under the canonical key.
func WithIdentity(ctx context.Context, ident pluginapi.Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity{}, ident)
}

// IdentityFrom returns the Identity stored by WithIdentity and whether one was present.
func IdentityFrom(ctx context.Context) (pluginapi.Identity, bool) {
	ident, ok := ctx.Value(ctxKeyIdentity{}).(pluginapi.Identity)
	return ident, ok
}
