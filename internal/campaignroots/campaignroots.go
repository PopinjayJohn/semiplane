// Package campaignroots is the composition root's wiring for campaign content
// roots: it opens one `os.Root` per campaign at startup and holds the set.
//
// It is a package rather than a few functions in `main` because the behaviour is
// worth having a name and a test, and because two things need it. The wiki route
// resolves a slug to a confined root on every request, and the watcher (P4)
// registers a watch per campaign. Both start from the same enumeration, and a
// second enumeration that disagreed with this one would be a campaign that one
// subsystem serves and the other cannot see.
//
// The registry itself lives in `internal/content`; this package is the policy
// around it — what to do when a root cannot be opened, and what that means for
// the campaigns that can.
package campaignroots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// CampaignRootError names a campaign whose content root could not be
// opened.
//
// Distinct from "the root is not there" and from a storage failure, because
// they have three different answers. A missing root marks the campaign
// **degraded** and the server still starts (S-4.5) — a campaign whose vault is
// on a disk that is not mounted must not take the instance down with it, and its
// pages answer a load error naming the request id rather than a 404 that would
// read as "this campaign does not exist".
type CampaignRootError struct {
	// Slug is the campaign. Safe to surface: it came from the operator's own
	// registration, not from a request.
	Slug string
	// Path is the configured content root, also operator-supplied.
	Path string
	// Err is what the filesystem said.
	Err error
}

func (e *CampaignRootError) Error() string {
	return fmt.Sprintf("campaign %s: content root %s is unusable: %v", e.Slug, e.Path, e.Err)
}

func (e *CampaignRootError) Unwrap() error { return e.Err }

// IsCampaignRootMissing reports whether err names a campaign with no usable
// content root, unwrapping through a chain.
//
// A helper rather than an errors.As at each call site, because the interesting
// case is a *joined* error — `errors.Join` of one campaign's failure and another's
// — and `errors.As` handles a chain but a caller writing it at four sites will
// get one of the four subtly wrong.
func IsCampaignRootMissing(err error) bool {
	var missing *CampaignRootError

	return errors.As(err, &missing)
}

// Degraded is one campaign that could not be opened, reported rather than
// returned.
//
// A slice rather than a single error because the right response to a boot with
// three unmounted vaults is to serve the other nine campaigns and say which
// three are unhappy, not to abort. Aborting would mean one unmounted disk makes
// the whole instance unavailable, which is the failure S-4.5 exists to prevent.
type Degraded []CampaignRootError

// Slugs returns the slugs of the campaigns that could not be opened, sorted by
// the order they appear (which is slug order, from the store's query).
func (d Degraded) Slugs() []string {
	if len(d) == 0 {
		return nil
	}

	slugs := make([]string, 0, len(d))
	for _, entry := range d {
		slugs = append(slugs, entry.Slug)
	}

	return slugs
}

// Open builds a registry holding one confined root per registered campaign.
//
// The failures are returned *and* the successful roots are kept, because the
// caller needs both: the registry serves the campaigns that opened, and the error
// names the ones that did not. Returning only the error would leave the caller
// with a nil registry and no instance; returning only the registry would hide a
// campaign nobody can read.
//
// A campaign whose row exists but whose directory is gone is `degraded`, not
// fatal. That is S-4.5 and it is the common case in practice: a vault on a disk
// that is not mounted, or a campaign created by a test whose temp directory has
// been cleaned up.
func Open(
	ctx context.Context,
	db *store.Store,
	logger *slog.Logger,
) (*content.Registry, Degraded, error) {
	registry := content.NewRegistry(content.RefuseSymlinks)

	campaigns, err := db.Campaigns(ctx)
	if err != nil {
		// A failure to *enumerate* is different from a failure to open one.
		// Nothing is known to be wrong with any campaign, so there is nothing to
		// degrade and the server has no content at all — which is a boot
		// failure, not a degraded instance.
		return nil, nil, fmt.Errorf("list campaigns: %w", err)
	}

	var degraded Degraded

	// Indexed: domain.Campaign is 128 bytes and this is a boot-time loop, but
	// gocritic is right that the copy buys nothing.
	for i := range campaigns {
		campaign := campaigns[i]

		if err := open(registry, campaign); err != nil {
			degraded = append(degraded, CampaignRootError{
				Slug: campaign.Slug,
				Path: campaign.ContentRoot,
				Err:  err,
			})

			continue
		}

		if logger != nil {
			logger.DebugContext(ctx, "campaign root opened",
				slog.String("slug", campaign.Slug),
			)
		}
	}

	return registry, degraded, nil
}

// open adds one campaign's root to the registry.
func open(registry *content.Registry, campaign domain.Campaign) error {
	root, err := registry.Open(campaign.Slug, campaign.ContentRoot)
	if err != nil {
		// Wrapped with the campaign, because the caller's log line has to say
		// which one and this is the only layer that knows.
		return fmt.Errorf("open root for campaign %s: %w", campaign.Slug, err)
	}

	if root == nil {
		return errors.New("registry returned no root and no error")
	}

	return nil
}

// Retain returns the `retain` callback a `campaigns.Registrar` should be given,
// so a campaign registered by this process joins the registry immediately.
//
// Without it, `Registrar.Register` opens an `os.Root`, finds no consumer, and
// closes it — which is correct for the CLI and wrong for a server that is about
// to serve the campaign it just registered. The callback is what makes the
// registration's root the one the read path uses.
//
// The callback deliberately does *not* close a root it refuses. The registrar
// has its own `handedOver` guard and closes on the failure path; a callback that
// closed as well would double-close, and a double close on an `*os.Root` is an
// error a reader would see as a mysterious 500 on the campaign that was just
// created.
func Retain(
	registry *content.Registry,
	logger *slog.Logger,
) func(slug string, root *os.Root) error {
	return func(slug string, root *os.Root) error {
		if err := registry.Adopt(slug, root); err != nil {
			return fmt.Errorf("adopt content root for %s: %w", slug, err)
		}

		if logger != nil {
			logger.Info("campaign registered", slog.String("slug", slug))
		}

		return nil
	}
}
