// Package campaigns owns the two operations that make a campaign a tenant:
// registering one, and deciding what a request may do with it.
//
// The access decision is here and not in each handler because the S-8 matrix is
// a table, and a table enforced at the call site is a table with one copy per
// call site. `domain.ResolveAccess` holds the rule; this package holds the
// lookup it needs and the gates every campaign-scoped route is mounted behind,
// so a new route inherits the matrix by being mounted rather than by remembering
// it.
package campaigns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/store"
)

// Store is the persistence this package needs: campaign lookup, membership
// lookup, and the writes registration performs.
//
// A narrow interface so a test can exercise the access gates without a
// database. `store.Store` satisfies it structurally; a phase that adds a query
// widens this interface rather than reimplementing it.
type Store interface {
	CampaignBySlug(ctx context.Context, slug string) (domain.Campaign, error)
	Membership(ctx context.Context, campaignID, userID int64) (domain.Membership, error)
	CreateCampaign(ctx context.Context, campaign domain.Campaign) (domain.Campaign, error)
	CreateMembership(ctx context.Context, membership domain.Membership) (domain.Membership, error)
	DeleteCampaign(ctx context.Context, id int64) error
}

type campaignKey struct{}

// Access is one request's resolved relationship to one campaign: the campaign,
// the membership if there was one, and the tier that follows from the two.
//
// Carried whole rather than as a bare tier so a handler that needs the campaign
// does not re-read it — and therefore cannot read it twice and get a row that
// has since changed underneath the first read.
type Access struct {
	Campaign   domain.Campaign
	Membership *domain.Membership
	Tier       domain.Tier
}

// AccessFrom returns the access resolved for this request.
//
// A request that never passed through Resolve gets TierNone, so a route mounted
// without it is a 404 rather than a privileged read.
func AccessFrom(ctx context.Context) Access {
	access, ok := ctx.Value(campaignKey{}).(Access)
	if !ok {
		return Access{Tier: domain.TierNone}
	}

	return access
}

// Resolve attaches the request's access to the campaign its path names.
//
// Runs before the guards in a campaign-scoped chain, and does nothing when the
// request has no `{slug}` — so it composes with a route set where only some
// routes are campaign-scoped. A slug naming nothing leaves TierNone on the
// context, and a following guard answers 404, which is the same answer a
// campaign that exists but is invisible produces. That equality is the point:
// it is what stops a private campaign's existence from being observable.
//
// A storage failure is a 500 and not a 404. Reporting it as absent tells an
// operator their database is healthy when it is serving errors, and that is
// discovered during an incident rather than before one.
func Resolve(backing Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			slug := r.PathValue("slug")
			if slug == "" {
				next.ServeHTTP(w, r)

				return
			}

			campaign, err := backing.CampaignBySlug(ctx, slug)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					next.ServeHTTP(w, r)

					return
				}

				slog.ErrorContext(ctx, "campaign.resolve_failed",
					slog.String("slug", slug),
					slog.String("error", err.Error()),
				)
				writeError(w, http.StatusInternalServerError, "this page could not be loaded")

				return
			}

			member := membershipFor(ctx, backing, campaign, Requestor(ctx))

			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, campaignKey{}, Access{
				Campaign:   campaign,
				Membership: member,
				Tier:       domain.ResolveAccess(campaign, member, Requestor(ctx)),
			})))
		})
	}
}

// Requestor returns the identity for this request.
//
// It reads the context rather than taking a parameter, so a handler cannot
// accidentally pass one request's identity into another's access decision.
func Requestor(ctx context.Context) domain.Requestor {
	return identity.Requestor(ctx)
}

// membershipFor reads the requestor's membership of campaign, or nil.
//
// Only asked for an authenticated requestor: a non-member of a public campaign
// is read-only, and ResolveAccess discards a membership for an unauthenticated
// requestor regardless. A query whose result is thrown away is a query a later
// reader will assume was load-bearing.
//
// A read failure yields nil rather than an error, and the member is
// downgraded to read-only. That is failing toward *less* access, which is the
// safe direction, and it costs nothing: a private campaign is a 404 either way,
// and a public one stays readable. The error is logged because a database that
// cannot answer a membership query is a fault somebody must see.
func membershipFor(
	ctx context.Context,
	backing Store,
	campaign domain.Campaign,
	requestor domain.Requestor,
) *domain.Membership {
	if !requestor.Authenticated || requestor.UserID <= 0 {
		return nil
	}

	membership, err := backing.Membership(ctx, campaign.ID, requestor.UserID)
	switch {
	case err == nil:
		return &membership
	case errors.Is(err, store.ErrNotFound):
		// The ordinary case: most requests to a public campaign are by
		// non-members. Not an error worth logging.
		return nil
	default:
		slog.ErrorContext(ctx, "membership.resolve_failed",
			slog.Int64("campaign_id", campaign.ID),
			slog.Int64("user_id", requestor.UserID),
			slog.String("error", err.Error()),
		)

		return nil
	}
}

// Guard requires that the request holds at least one of the given capabilities.
//
// The single place a campaign-scoped route states what it needs, and the order
// its failures are answered in is deliberate:
//
//   - No access at all → 404. A private campaign to a non-member is reported as
//     absent rather than forbidden, because a 403 would confirm it exists. This
//     is the same answer, with the same body, that an unmatched route gives —
//     see notFoundHandler for why that matters.
//   - Some access but not the one asked for → 401 when anonymous, 403 otherwise.
//     S-14.4 permits either for an anonymous request; 401 is chosen because it
//     is the answer that tells a reader that signing in will help. Reaching a
//     capability gate at all means the campaign is not itself a secret here —
//     it is public, or the reader is a member of it.
//
// The predicates are functions rather than tier constants because the tiers are
// not a total order for this purpose. `CanRead`, `CanPlay` and `CanEdit` are the
// three capabilities the S-8 table names, and each is its own membership test
// inside domain. Naming one here and re-implementing the other two is how a
// second copy of the matrix appears.
func Guard(allowed ...func(domain.Tier) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			access := AccessFrom(r.Context())

			if access.Tier == domain.TierNone {
				slog.DebugContext(r.Context(), "http.route_miss",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
				writeError(w, http.StatusNotFound, "not found")

				return
			}

			for _, permits := range allowed {
				if permits(access.Tier) {
					next.ServeHTTP(w, r)

					return
				}
			}

			if !Requestor(r.Context()).Authenticated {
				// A challenge rather than a refusal. `Cookie` is a registered
				// scheme (RFC 6765) precisely for a session cookie, and the
				// realm names this application.
				w.Header().Set("WWW-Authenticate", `Cookie realm="semiplane"`)
				writeError(w, http.StatusUnauthorized, "sign in to continue")

				return
			}

			writeError(w, http.StatusForbidden, "this action needs a different role")
		})
	}
}

// The three gates routes mount.
//
// Named rather than spelled out at each call site because a mount list is a
// reviewable claim about a route: `RequireEdit` in a chain says what the route
// needs, where `Guard(domain.Tier.CanEdit)` makes the reader look up what that
// means. Values rather than functions because a gate carries no state.
var (
	// RequireRead admits anyone who may see the campaign's wiki and assets.
	RequireRead = Guard(domain.Tier.CanRead)
	// RequirePlay admits members only. No amount of public visibility makes an
	// anonymous visitor a player (S-8.1).
	RequirePlay = Guard(domain.Tier.CanPlay)
	// RequireEdit admits the campaign GM and nobody else. A player PUT is a 403
	// (S-14.4).
	RequireEdit = Guard(domain.Tier.CanEdit)
)

// RegisterRequest is what a caller supplies to register a campaign.
type RegisterRequest struct {
	Slug string
	Name string
	// SystemID and RulesetVersion are stated at registration, never defaulted.
	// A campaign that silently inherited a gameplay plugin which may not be
	// registered would refuse to start its game and serve an empty wiki, which
	// reads as a broken install rather than as a configuration choice somebody
	// made (S-14.8).
	SystemID       string
	RulesetVersion string
	Visibility     domain.Visibility
	// OwnerID is the user seeded as this campaign's GM. Registration without an
	// owner produces a campaign nobody can run or edit.
	OwnerID int64
}

// Registrar registers campaigns and creates their content roots.
type Registrar struct {
	backing Store
	// contentRootBase is the directory every campaign's root is created
	// beneath. Absolute, and required to be by config at startup.
	contentRootBase string
	// retain receives the `os.Root` for each newly registered campaign.
	//
	// A callback rather than a map held here because the handle is
	// process-scoped state that the content package owns for reading from P3. A
	// registrar holding a second map of them is a second source of truth about
	// which campaigns have a root. This phase creates each root to prove it is
	// creatable and to write an absolute path into the row; the phase that reads
	// files through it keeps it.
	retain func(slug string, root *os.Root) error
}

// NewRegistrar returns a Registrar creating content roots beneath base.
//
// retain may be nil, which is what the server passes today. When it is set, it
// takes ownership of each root; when the call fails the root is closed.
func NewRegistrar(
	backing Store,
	base string,
	retain func(slug string, root *os.Root) error,
) *Registrar {
	return &Registrar{backing: backing, contentRootBase: base, retain: retain}
}

// Register creates a campaign's content root, its row, and its owner's GM
// membership.
//
// The order is the substance of this function, and it is filesystem-first:
//
//  1. The slug is validated. Checked here as well as in the store because a slug
//     is about to become a directory name, and a directory named from an
//     unvalidated slug is a directory an attacker chose.
//  2. The directory is created and an `os.Root` opened on it. The root is the
//     confinement boundary for everything in the campaign (AGENTS.md §12), so
//     opening it *before* the row exists means a campaign that cannot be
//     confined is never registered — the alternative is a row whose
//     `content_root` names a path nothing can open.
//  3. The campaign row, then the owner's membership.
func (r *Registrar) Register(ctx context.Context, req RegisterRequest) (domain.Campaign, error) {
	if err := domain.ValidateSlug(req.Slug); err != nil {
		// Wrapped rather than returned bare so the log line names the operation
		// that refused. The sentinel is preserved, so a caller can still
		// errors.Is for ErrInvalidSlug and answer 400.
		return domain.Campaign{}, fmt.Errorf("register campaign: %w", err)
	}

	if !req.Visibility.Valid() {
		return domain.Campaign{}, fmt.Errorf(
			"%w: register campaign %s", domain.ErrInvalidVisibility, req.Slug,
		)
	}

	if req.OwnerID <= 0 {
		return domain.Campaign{}, errors.New("campaigns: register campaign without an owner")
	}

	contentRoot := filepath.Join(r.contentRootBase, req.Slug)

	// 0o700 rather than 0o755. The root holds a campaign's pages, and a
	// directory readable by every account on the host is a campaign readable by
	// every account on the host — a read path that bypasses the S-8 matrix
	// entirely, and the first one reached by an attacker who gets shell access.
	if err := os.MkdirAll(contentRoot, 0o700); err != nil {
		return domain.Campaign{}, fmt.Errorf("create content root for %s: %w", req.Slug, err)
	}

	root, err := os.OpenRoot(contentRoot)
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("open content root for %s: %w", req.Slug, err)
	}

	// Handled on every return path, so the one exit that transfers ownership —
	// the successful retain below — has to say so.
	handedOver := false

	defer func() {
		if handedOver {
			return
		}

		if closeErr := root.Close(); closeErr != nil {
			slog.WarnContext(ctx, "content root close failed",
				slog.String("slug", req.Slug),
				slog.String("error", closeErr.Error()),
			)
		}
	}()

	campaign, err := r.backing.CreateCampaign(ctx, domain.Campaign{
		Slug:           req.Slug,
		Name:           req.Name,
		ContentRoot:    contentRoot,
		Visibility:     req.Visibility,
		SystemID:       req.SystemID,
		RulesetVersion: req.RulesetVersion,
	})
	if err != nil {
		// Wrapped, and the sentinel chain is intact: a slug already taken
		// arrives as store.ErrConflict, which is what the caller answers 409
		// from.
		return domain.Campaign{}, fmt.Errorf("register campaign %s: %w", req.Slug, err)
	}

	if _, err := r.backing.CreateMembership(ctx, domain.Membership{
		CampaignID: campaign.ID,
		UserID:     req.OwnerID,
		Role:       domain.RoleGM,
	}); err != nil {
		// The campaign now exists and is unusable: no GM, so nothing in it can
		// be edited and its game cannot be run. The row is removed to restore
		// the invariant that a campaign has an owner. The directory is left in
		// place — see below.
		if delErr := r.backing.DeleteCampaign(ctx, campaign.ID); delErr != nil {
			slog.ErrorContext(ctx, "campaign.rollback_failed",
				slog.String("slug", req.Slug),
				slog.Int64("campaign_id", campaign.ID),
				slog.String("error", delErr.Error()),
			)
		}

		return domain.Campaign{}, fmt.Errorf("seed owner membership for %s: %w", req.Slug, err)
	}

	// A directory is deliberately not removed when a later step fails. A failed
	// cleanup that deleted a path tree is how a bug becomes data loss, and
	// nothing writes inside the root during registration, so the tree is empty
	// and harmless. Re-registering the same slug finds the directory present and
	// reuses it — the same state as a vault an operator created by hand and
	// pointed a campaign at.
	if r.retain != nil {
		if err := r.retain(req.Slug, root); err != nil {
			return domain.Campaign{}, fmt.Errorf("retain content root for %s: %w", req.Slug, err)
		}

		handedOver = true
	}

	return campaign, nil
}

// writeError writes the JSON error body every route in this package answers with.
//
// The shape matches httpapi.writeJSON's error payload, duplicated rather than
// shared because the two live in different packages. A handler that rendered a
// different error shape for an authorisation failure would be a small way for a
// client to tell a 403 from a 404 by parsing the body instead of the status.
//
// `no-store` is load-bearing and was missing for this package's whole life.
//
// Every response this writes is **reader-dependent**: the same URL answers 404
// for an anonymous requestor and 200 for a member, because S-8 answers "no
// access" with a status that does not say why. Nothing in the body distinguishes
// them — deliberately, so the status cannot become an existence oracle — which is
// exactly what makes the response unsafe to store. A shared cache in front of a
// self-hosted instance is the ordinary deployment (a reverse proxy, a CDN, a
// corporate proxy), and one that stored an anonymous 404 for
// `/c/greyhaven/wiki/index` would serve it to the GM who is entitled to that
// page, for as long as the entry lived.
//
// `private, no-store` rather than either alone: `no-store` is the directive that
// stops the write, and `private` states the reason to a cache that honours only
// one of the two. This is the same pair ADR 0016 requires for a GM's unredacted
// wiki response, for the same reason — a body whose content depends on who is
// asking must never be stored by something that does not know who is asking.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")

	w.WriteHeader(status)

	if _, err := fmt.Fprintf(w, "{\"error\":%q}\n", message); err != nil {
		slog.Error("write response", slog.String("error", err.Error()))
	}
}
