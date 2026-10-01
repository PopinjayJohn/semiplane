package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/campaignroots"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/web/components"
)

// TestADegradedCampaignReachesTheRail is the test for a bug in which two halves
// each reported success.
//
// `campaignroots.Open` returns the campaigns whose content root could not be
// opened, and `runServer` logs them. Nothing carried that list into the
// interface: the wiki route was handed a zero `components.InstanceView{}`, whose
// empty `Degraded` slice means healthy *by construction*, so the rail rendered
// "everything is fine" on a page whose campaign could not load. The pipeline also
// marked the campaign degraded and emitted `watch.degraded` — and the operator
// saw a healthy instance.
//
// This asserts the seam rather than the policy: given the boot-time degradation
// the composition root already computes, the view the interface renders names
// it.
func TestADegradedCampaignReachesTheRail(t *testing.T) {
	t.Parallel()

	view := instanceView(
		testConfig(),
		campaignroots.Degraded{{
			Slug: "greyhaven",
			Path: "/var/lib/semiplane/vaults/greyhaven",
			Err:  errors.New("no such file or directory"),
		}},
	)

	if len(view.Degraded) != 1 {
		t.Fatalf("a campaign with an unreadable content root produced %d degraded "+
			"entries, want 1: the boot already knows, and the rail is where the "+
			"operator finds out", len(view.Degraded))
	}

	got := view.Degraded[0]

	// The slug names the campaign. It is operator-supplied, from their own
	// registration, so it discloses nothing an operator does not already know —
	// and a notice that cannot be acted on is not a notice.
	if got.Name == "" || got.Detail == "" {
		t.Errorf("the degraded entry is %+v; a subsystem with no name is a counter "+
			"nobody can act on", got)
	}

	// The filesystem's own error text stays out. `CampaignRootError.Err` can
	// carry a host path, and the rail is rendered for every reader of the
	// campaign list including an anonymous one on a public instance.
	for _, leak := range []string{"/var/lib", "no such file"} {
		if strings.Contains(strings.ToLower(got.Name), leak) ||
			strings.Contains(strings.ToLower(got.Detail), leak) {
			t.Errorf("the degraded entry %+v carries %q; the rail renders for every "+
				"reader and must not carry a host path or raw error text", got, leak)
		}
	}
}

// TestTheInstanceIsHealthyOnlyWhenItIs is the negative half, and the reason the
// first test is worth anything.
//
// An audit tuned until it always fires is switched off within a phase. Healthy
// has to render nothing, or the rail says "everything is fine" on every page and
// trains a GM to ignore it.
func TestTheInstanceIsHealthyOnlyWhenItIs(t *testing.T) {
	t.Parallel()

	view := instanceView(testConfig(), nil)

	if len(view.Degraded) != 0 {
		t.Errorf("a healthy instance reported %d degraded subsystems (%+v); an "+
			"always-populated rail is noise that trains a reader to ignore it",
			len(view.Degraded), view.Degraded)
	}
}

// TestTheInstanceNameFallsBackToTheProduct pins the other half of the view: an
// operator reaches an unconfigured instance before any name is set, and a blank
// heading in the header is the first thing a new install looks like.
//
// The fallback is `displayName()`'s, which is unexported in `components`; so this
// asserts the *value* the composition root passes is empty in that case, and
// that a configured name is carried through untouched. What the component does
// with it is its own test's business.
func TestTheInstanceNameFallsBackToTheProduct(t *testing.T) {
	t.Parallel()

	if got := instanceView(config.Config{}, nil).Name; got != "" {
		t.Errorf("an unconfigured instance carries the name %q; an empty name is "+
			"what lets the component fall back to the product name", got)
	}

	cfg := testConfig()
	cfg.InstanceName = "The Greyhaven"

	if got := instanceView(cfg, nil).Name; got != "The Greyhaven" {
		t.Errorf("the configured name is %q, want %q", got, "The Greyhaven")
	}
}

// ensure the view type is the one the interface renders, at compile time.
var _ = components.InstanceView{}
