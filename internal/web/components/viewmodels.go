package components

// Package components holds the server-rendered markup for semiplane's routes:
// the shell landmarks, the auth-facing pages, and the states those pages can be
// in. It lives under internal/web beside the embedded assets rather than in
// that package so the two never need to import each other.
//
// The overview is here, below the package clause, and not above it. templ
// writes `// templ: version: ...` into every file it generates, and a comment
// directly above a package clause is that package's doc comment — so a
// hand-written one here makes two, and godoclint fails the build with "package
// has more than one godoc". The generated file is excluded from *reporting* by
// its own "Code generated" header; it is not excluded from the count. Moving
// this comment up one line reintroduces the failure, so it stays here.
import (
	"github.com/semiplane/semiplane/internal/domain"
)

// The view models in this file are the whole contract between the interface
// and the routes that render it. They are constructed by the caller from
// `internal/domain` types and are deliberately small: this package owns the
// labels (UI §1.2), the domain owns the values, and neither one gets to build
// the other's structures. A model that arrived here carrying a password hash,
// a content root or a token would be a model that reached a template by a
// route nobody reviewed.

// productName names the software in the header and in a document title when
// the operator has not configured an instance name.
const productName = "semiplane"

// titleSeparator joins the parts of a document title. UI §7.2 fixes the form as
// "Page — Section — Campaign", so the separator is the spec's own rather than
// a typographic choice.
const titleSeparator = " — "

// ShellView is everything the chrome around a page slot needs, for a route
// that has no campaign yet.
//
// There is no field for the left nav, and that omission is the design rather
// than an oversight: the nav is entirely campaign-scoped and UI §4.6 removes it
// before a campaign exists. A field that could carry one would be a field this
// work item would have to populate for a route it does not own.
type ShellView struct {
	// Title is the document title. The page component composes it; a caller
	// does not set it. Two routes assembling a title by hand is how the
	// "Page — Section — Campaign" format (UI §7.2) drifts apart.
	Title string
	// Instance identifies the running instance and reports what is unhealthy.
	Instance InstanceView
	// Account is the signed-in identity, or the empty one.
	Account AccountView
	// SignOutHref is where the account zone's form posts. Empty renders no
	// form at all: a button that posts nowhere is a focus stop that does
	// nothing, which is worse than no button.
	SignOutHref string
	// StatusHref is the instance status link in the footer. UI §4.2 wants one
	// on every route and UI §4.4 gives the route only inside a campaign, so
	// until that route exists the field is empty and the footer omits the link
	// rather than pointing at a page that answers 404.
	StatusHref string
}

// InstanceView identifies the running instance and reports the subsystems that
// are not healthy.
//
// It is the whole of the right rail on a pre-campaign route (UI §4.4): the
// rail earns its place there precisely because there is no campaign to describe,
// so the instance is what it describes.
type InstanceView struct {
	// Name is the instance's configured name. Empty falls back to the product
	// name rather than rendering an empty heading — every operator reaches an
	// unconfigured instance before any name is set, and a blank slot in the
	// header is the first thing a new install looks like.
	Name string
	// Version is the build identifier, shown in the rail and the footer.
	Version string
	// Degraded names the subsystems that are not working. Empty is healthy, and
	// healthy renders nothing: a rail that says "everything is fine" on every
	// page is noise that trains a GM to ignore the rail.
	Degraded []DegradedView
}

// displayName is the name to show for the instance, falling back to the
// product's name.
func (inst InstanceView) displayName() string {
	if inst.Name == "" {
		return productName
	}

	return inst.Name
}

// DegradedView is one unhealthy subsystem.
//
// Named rather than counted because every one of them reads to an operator as a
// sentence — a content watcher that stopped, a cache serving a stale page, a
// reconciliation loop that gave up (UI §4.2, §9). A counter is a number
// nobody can act on.
type DegradedView struct {
	// Name is the subsystem, as the status view names it.
	Name string
	// Detail is one clause on what it means, in the operator's terms.
	Detail string
}

// AccountView is the signed-in identity.
type AccountView struct {
	// Username is the account's own name. Empty means nobody is signed in,
	// which is the normal state of /login and renders no account zone at all.
	Username string
}

// LoginView is the /login centre slot (UI §4.4).
type LoginView struct {
	// Shell is the chrome. Its Instance field also fills the right rail, so a
	// caller sets it once.
	Shell ShellView
	// Username is echoed back into the form after a failed attempt. The
	// password is not, because there is nothing safe to echo it into.
	Username string
	// Failure says why the last attempt did not succeed.
	Failure LoginFailure
	// ReturnTo is the path to resume at once signed in. Rendered as a hidden
	// field and never interpreted here: validating an open redirect is the
	// handler's job, because a template that half-checks one is a template
	// that eventually stops checking.
	ReturnTo string
}

// LoginFailure is why a sign-in attempt did not succeed.
//
// A closed enum rather than a caller-supplied sentence so the wording lives in
// one place, and so it stays inside the vocabulary contract the render test
// polices. What a reader may be told about *why* — whether the account exists,
// how the credential was rejected — is a property of the failure, not
// something a sentence can carry without a caller remembering to qualify it.
// Add a case here when a failure genuinely has new user-visible meaning;
// do not add a field for prose.
type LoginFailure int

const (
	// LoginFailureNone is the zero value: no failed attempt to report.
	LoginFailureNone LoginFailure = iota
	// LoginFailureCredentials covers a rejected username-or-password pair. One
	// case and one sentence for both, because distinguishing them tells an
	// attacker which half they guessed right.
	LoginFailureCredentials
)

// message is the sentence a failure renders.
//
// Every string in this function is user-visible copy, and the interface has a
// vocabulary rule that a grep test polices (UI §1.2, §10.2). Keeping the copy
// here rather than in the template is what makes that test possible: the
// template has no way to know what a caller is about to interpolate into it.
func (failure LoginFailure) message() string {
	switch failure {
	case LoginFailureNone:
		return ""
	case LoginFailureCredentials:
		return "That username and password did not match. Try again."
	default:
		// A value built by hand rather than through the enum above is a bug,
		// not a new failure. Saying nothing is the only honest answer: a
		// stranger's sentence in the credential form is a phishing surface.
		return ""
	}
}

// CampaignListView is the / centre slot (UI §4.4): the campaigns this account
// is a member of, or the designed first run when there are none.
type CampaignListView struct {
	// Shell is the chrome. Its Instance field also fills the right rail.
	Shell ShellView
	// Campaigns are the reader's campaigns in the order the caller wants them
	// read. Empty is a first run, which is a designed surface (UI §4.7) and not
	// an error.
	Campaigns []CampaignCard
	// CreateHref is where the first-run call to action points. Empty omits the
	// action, leaving the explanation: a CTA pointing at a route that does not
	// exist is the one button a new operator will press.
	CreateHref string
	// DocsHref is the documentation the first run offers. Empty omits the
	// link, for an operator who has not configured one.
	DocsHref string
	// Failure replaces the list with the error state when the list could not
	// be loaded at all. A pointer, so "the list is empty" and "the list could
	// not be fetched" cannot be the same value — telling a reader they have no
	// campaigns when the database was unreachable sends them looking for a
	// create button that cannot fix it.
	Failure *LoadFailure
}

// LoadFailure describes a page that could not be loaded (UI §4.7's `500` row).
//
// A value rather than a sentence, for the same reason as LoginFailure: the
// wording is the interface's, and only the per-request reference is the
// caller's. That reference is what ties a reader's screenshot to a line in the
// access log, which is the only reason this page exists at all.
type LoadFailure struct {
	// Reference is the request id from the RequestID middleware. Rendered only
	// when set.
	Reference string
}

// CampaignCard is one row of the campaign list: the campaign's name, the URL
// that reaches it, and the reader's own relationship to it.
type CampaignCard struct {
	// Name is what the reader calls this campaign.
	Name string
	// Slug is the URL segment. It comes from a domain.Campaign that has been
	// through domain.ValidateSlug, so it cannot carry a scheme, a slash or a
	// quote; the template also runs it through templ's URL sanitiser, and
	// neither check is load-bearing on its own.
	Slug string
	// Role is the reader's role, as a label: "Game Master" or "Player". Empty
	// for a role this build does not recognise, and the row omits the line
	// rather than rendering a stored value back to the reader.
	Role string
	// Visibility is the campaign's own setting: "Public" or "Private". Shown
	// because a reader with several campaigns on one instance has to tell a
	// public one from a private one without opening both.
	Visibility string
}

// NewCampaignCard builds a card from the two domain rows that describe it.
//
// The role and visibility labels are chosen here rather than in the template
// because the interface owns the labels (UI §1.2) and the domain owns the
// values: a role is gm and player in the database, and printing the stored
// text puts "gm" in front of a reader.
func NewCampaignCard(campaign domain.Campaign, role domain.Role) CampaignCard {
	name := campaign.Name
	if name == "" {
		// A campaign with no name still has a URL. A row whose only link reads
		// blank is a dead end where the slug would have been honest.
		name = campaign.Slug
	}

	return CampaignCard{
		Name:       name,
		Slug:       campaign.Slug,
		Role:       roleLabel(role),
		Visibility: visibilityLabel(campaign.Visibility),
	}
}

// roleLabel maps a domain role to the word a reader sees.
//
// Absent rather than blank-and-guessed: an unrecognised role is not a role,
// and the alternative — echoing the stored text — would print whatever a
// hand-edited database happens to hold into a permissions surface.
func roleLabel(role domain.Role) string {
	switch role {
	case domain.RoleGM:
		return "Game Master"
	case domain.RolePlayer:
		return "Player"
	default:
		return ""
	}
}

// visibilityLabel maps a domain visibility to the word a reader sees, and fails
// toward saying nothing: a visibility this build does not understand is not
// public, so rendering it as anything other than omitted risks reading as a
// promise the domain has not made.
func visibilityLabel(visibility domain.Visibility) string {
	switch visibility {
	case domain.VisibilityPublic:
		return "Public"
	case domain.VisibilityPrivate:
		return "Private"
	default:
		return ""
	}
}

// pageChrome fills in the one ShellView field a caller does not own.
//
// Taken and returned by value so a caller's view model is never written
// through: the same ShellView may be reused across requests, and mutating it
// during rendering would make the response depend on request order.
func pageChrome(shell ShellView, page string) ShellView {
	shell.Title = documentTitle(page, shell.Instance)

	return shell
}

// documentTitle composes the document title for a pre-campaign route.
//
// UI §7.2 fixes the form as "Page — Section — Campaign". A pre-campaign route
// has no campaign, so the third part names the instance and the middle one is
// dropped rather than filled with something that names nothing.
func documentTitle(page string, instance InstanceView) string {
	return page + titleSeparator + instance.displayName()
}
