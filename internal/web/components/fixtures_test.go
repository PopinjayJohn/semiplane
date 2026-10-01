package components_test

import (
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The fixtures below are built from domain types on purpose. A hand-written
// view model would let the tests pass while NewCampaignCard drifted from the
// values the store actually holds, and the mapping from a stored role to the
// word on screen is exactly the part of this package with no type system
// holding it together.

// instanceFixture is a healthy, named instance.
func instanceFixture() components.InstanceView {
	return components.InstanceView{Name: "Greyhaven", Version: "0.2.0"}
}

// degradedFixture is one unhealthy subsystem, named the way §13.2's counters
// name it.
func degradedFixture() []components.DegradedView {
	return []components.DegradedView{
		{Name: "Content watcher", Detail: "the campaign's directory is no longer being watched"},
	}
}

// LoginViewOptions switches one fixture branch at a time. A boolean per branch
// is enough at four branches and keeps every fixture at a single construction
// site, which is where a reader looks to see what a page is being rendered
// with.
type LoginViewOptions struct {
	failed   bool
	signedIn bool
	unnamed  bool
	returnTo string
}

// loginView builds the /login fixture.
func loginView(options LoginViewOptions) components.LoginView {
	instance := instanceFixture()
	if options.unnamed {
		instance.Name = ""
	}

	view := components.LoginView{
		Shell:    components.ShellView{Instance: instance},
		Username: "mira",
		ReturnTo: options.returnTo,
	}

	if options.signedIn {
		view.Shell.Account = components.AccountView{Username: "mira"}
		view.Shell.SignOutHref = "/logout"
	}

	if options.failed {
		view.Failure = components.LoginFailureCredentials
	}

	return view
}

// CampaignListViewOptions switches one fixture branch at a time.
type CampaignListViewOptions struct {
	firstRun bool
	failed   bool
	degraded bool
	unnamed  bool
}

// campaignListView builds the / fixture.
func campaignListView(options CampaignListViewOptions) components.CampaignListView {
	instance := instanceFixture()
	if options.unnamed {
		instance.Name = ""
	}

	if options.degraded {
		instance.Degraded = degradedFixture()
	}

	view := components.CampaignListView{
		Shell: components.ShellView{
			Instance:    instance,
			Account:     components.AccountView{Username: "mira"},
			SignOutHref: "/logout",
			StatusHref:  "/status",
		},
		CreateHref: "/admin/campaigns/new",
		DocsHref:   "https://popinjayjohn.github.io/semiplane/",
		Campaigns:  campaignCards(),
	}

	switch {
	case options.failed:
		// The failure branch keeps the cards out of the model on purpose:
		// the centre renders the error instead, and a card that survived the
		// error would be a card nobody knows how fresh it is.
		view.Campaigns = nil
		view.Failure = &components.LoadFailure{Reference: "req-4f2a"}
	case options.firstRun:
		view.Campaigns = nil
	}

	return view
}

// campaignCards are two campaigns in the two visibilities, so the rendered
// list exercises both labels and both roles.
func campaignCards() []components.CampaignCard {
	return []components.CampaignCard{
		components.NewCampaignCard(
			domain.Campaign{
				Name:       "Greyhaven",
				Slug:       "greyhaven",
				Visibility: domain.VisibilityPublic,
			},
			domain.RoleGM,
		),
		components.NewCampaignCard(
			domain.Campaign{
				Name:       "Saltmarsh",
				Slug:       "saltmarsh",
				Visibility: domain.VisibilityPrivate,
			},
			domain.RolePlayer,
		),
	}
}
