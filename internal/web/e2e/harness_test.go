package e2e_test

// The fixture: a real router over a real handler, a real hub and the real engine.
//
// See `document_test.go` for why this exists and why it stands up a server rather
// than composing components by hand. The short version: three of the four ways
// this page can be wrong are not in any component — a script written at a call
// site an audit does not read, a module embedded but never referenced, and a route
// mounted behind a gate the document asserts nothing about.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

const (
	gmSlug      = "gilded-cage"
	privateSlug = "hollow-choir"

	gmCampaignID      = int64(1)
	privateCampaignID = int64(2)

	gmUserID     = int64(11)
	playerUserID = int64(12)

	instanceName = "Greyhaven"
	userHeader   = "X-Test-User"
)

type storeImpl struct {
	mu          sync.Mutex
	campaigns   map[string]domain.Campaign
	memberships map[int64]map[int64]domain.Membership
}

func newStore() *storeImpl {
	backing := &storeImpl{
		campaigns:   map[string]domain.Campaign{},
		memberships: map[int64]map[int64]domain.Membership{},
	}

	backing.campaigns[gmSlug] = domain.Campaign{
		ID: gmCampaignID, Slug: gmSlug, Name: "The Gilded Cage",
		Visibility: domain.VisibilityPublic,
		SystemID:   string(dnd5e.SystemID),
	}
	backing.campaigns[privateSlug] = domain.Campaign{
		ID: privateCampaignID, Slug: privateSlug, Name: "The Hollow Choir",
		Visibility: domain.VisibilityPrivate,
		SystemID:   string(dnd5e.SystemID),
	}

	backing.memberships[gmCampaignID] = map[int64]domain.Membership{
		gmUserID:     {CampaignID: gmCampaignID, UserID: gmUserID, Role: domain.RoleGM},
		playerUserID: {CampaignID: gmCampaignID, UserID: playerUserID, Role: domain.RolePlayer},
	}
	backing.memberships[privateCampaignID] = map[int64]domain.Membership{
		gmUserID: {CampaignID: privateCampaignID, UserID: gmUserID, Role: domain.RoleGM},
	}

	return backing
}

func (s *storeImpl) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	campaign, found := s.campaigns[slug]
	if !found {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (s *storeImpl) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	membership, found := s.memberships[campaignID][userID]
	if !found {
		return domain.Membership{}, store.ErrNotFound
	}

	return membership, nil
}

func (s *storeImpl) CreateCampaign(context.Context, domain.Campaign) (domain.Campaign, error) {
	return domain.Campaign{}, store.ErrNotFound
}

func (s *storeImpl) CampaignsForUser(
	_ context.Context,
	userID int64,
) ([]domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listed := make([]domain.Campaign, 0, len(s.campaigns))
	// rangeValCopy: `s.campaigns` is a `map[int64]domain.Campaign`, so a range
	// value cannot be addressed, and the result holds whole rows because the
	// caller sorts them. Copying 128 bytes per campaign is the price of that and
	// there is no indexing form available to avoid it.
	//nolint:gocritic // see above: a map range cannot take an address
	for _, campaign := range s.campaigns {
		if _, member := s.memberships[campaign.ID][userID]; member {
			listed = append(listed, campaign)
		}
	}

	slices.SortFunc(listed, func(a, b domain.Campaign) int {
		return strings.Compare(a.Slug, b.Slug)
	})

	return listed, nil
}

func (s *storeImpl) CreateMembership(
	context.Context,
	domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, store.ErrNotFound
}

func (s *storeImpl) DeleteCampaign(context.Context, int64) error {
	return store.ErrNotFound
}

func withRequestor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(userHeader)
		if raw == "" || raw == "0" {
			anonymous := identity.WithRequestor(r.Context(), identity.Anonymous())
			next.ServeHTTP(w, r.WithContext(anonymous))

			return
		}

		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		next.ServeHTTP(w, r.WithContext(identity.WithRequestor(r.Context(), domain.Requestor{
			UserID:        userID,
			Username:      "tester",
			Authenticated: true,
		})))
	})
}

func newRegistry(t *testing.T) *realtime.Registry {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the state database: %v", err)
		}
	})

	const ddl = `CREATE TABLE campaign_state (
		campaign_id INTEGER PRIMARY KEY,
		state       BLOB    NOT NULL,
		version     INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL
	)`

	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create campaign_state: %v", err)
	}

	return realtime.NewRegistry(t.Context(), realtime.Config{
		Write: func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin the state transaction: %w", err)
			}

			if err := fn(ctx, tx); err != nil {
				_ = tx.Rollback()

				return err
			}

			return tx.Commit()
		},
		Read: func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
			row := db.QueryRowContext(ctx,
				`SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = ?`,
				campaignID,
			)

			var (
				blob      []byte
				version   int64
				updatedAt int64
			)

			if err := row.Scan(&blob, &version, &updatedAt); err != nil {
				return realtime.Persisted{}, realtime.ErrNoState
			}

			return realtime.Persisted{
				Blob:      blob,
				Version:   version,
				UpdatedAt: time.Unix(updatedAt, 0).UTC(),
			}, nil
		},
	})
}

// startTabletop stands up the play route behind the campaign gates and returns the
// server and the hub, so a test can fetch a document and a cleanup can close the
// hub.
//
// **The gates are in the fixture on purpose.** `campaigns.Resolve` wraps the route
// here exactly as `mountCampaignRoutes` does, so "the play page" in a test means
// the play page a member receives — and if the gate were left out, every claim in
// `play_test.go` would be about a route nobody can reach, which is how an
// authorisation regression hides behind a green page test.
func startTabletop(t *testing.T) (*httptest.Server, *realtime.Hub) {
	t.Helper()

	backing := newStore()
	registry := newRegistry(t)

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{States: registry})

	handler := &play.Handler{
		Hub:         hub,
		ReadTimeout: 250 * time.Millisecond,
		Instance:    components.InstanceView{Name: instanceName, Version: "test"},
		SignOutHref: "/logout",
		StatusHref:  "/status",
		Systems:     fixtureSystems(t),
		Snapshot:    fixtureSnapshot,
		Campaigns:   backing,
	}

	route := http.NewServeMux()
	play.Mount(route, handler)

	mux := http.NewServeMux()
	mux.Handle("/c/{slug}/", campaigns.Resolve(backing)(route))

	server := httptest.NewServer(withRequestor(mux))

	t.Cleanup(func() {
		server.Close()

		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v", err)
		}
	})

	return server, hub
}

func fixtureSystems(t *testing.T) play.Systems {
	t.Helper()

	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("build the 5e engine: %v", err)
	}

	return func(context.Context, int64) (rules.System, error) {
		return engine, nil
	}
}

func fixtureSnapshot(_ context.Context, campaignID int64) (realtime.Document, bool) {
	if campaignID <= 0 {
		return realtime.Document{}, false
	}

	return realtime.Document{
		Revision: 7,
		Placements: []realtime.Placement{
			{
				ID: "p-shown", X: 1, Y: 2,
				HP: 7, MaxHP: 7, Visible: true, Version: 7,
			},
			{
				ID: "p-hidden", X: 3, Y: 4,
				HP: 1, MaxHP: 4, Visible: false, Version: 7,
			},
		},
	}, true
}
