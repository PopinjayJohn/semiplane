package components

// Package components holds the server-rendered markup for semiplane's routes:
// the shell document, the auth-facing pages, and the states those pages can be
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
//
// # The two view-model layers, and why there is a conversion between them
//
// The shell's four landmarks live in `components/chrome` and the primitive
// library in `components/ui`, because a campaign shell composes the chrome and a
// package cannot import its own caller. So the chrome declares the models *its*
// components take — `HeaderView`, `FooterView`, `NavView` — and this package
// declares the models the *routes* build.
//
// That gives two layers of every concept that appears in both: an
// `AccountView` a route fills and a `chrome.AccountView` a landmark reads, an
// `InstanceView` and a `chrome.HeaderView`, a `DegradedView` and
// `chrome.DegradedView`. The functions at the foot of this file convert between
// them.
//
// **The conversion is deliberate and the alternative was tried.** Making
// `components`' types *aliases* of the chrome's —
// `type AccountView = chrome.AccountView` — looks like the tidier answer and
// removes the conversions entirely. It does not work, for a reason specific to
// Go: an alias is the same type, so the method set is the method set. The chrome
// spells its fallbacks `instanceLabel()`, this package spells its
// `displayName()`, and an alias would leave exactly one of the two names
// available at each call site — three sites today, and the number grows with
// every route. The unexported method would simply stop existing where it was
// called. A conversion keeps both names meaning what they mean to their own
// package, and `TestTheViewModelLayersAgreeWhereTheyOverlap` holds the two from
// drifting apart in the fields they share.
import (
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/ui"
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
//
// Duplicated in `components/chrome`, and the duplication is bounded by a test
// rather than by a shared constant: `chrome`'s copy is unexported, so exporting
// it would make one package the owner of the other's vocabulary for the sake of
// eleven characters. `TestTheProductNameIsTheSameInBothPackages` compares the
// two, so a rename that reaches only one of them fails rather than producing a
// header that says one thing and a document title that says another.
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
	// Campaign is the campaign this route is inside, or the zero value.
	//
	// UI §8.3's rank 1 is the current location, and inside a campaign the
	// campaign's name is it. The zero value is the pre-campaign case, which is
	// what makes the header's decision a zero-value check rather than a branch:
	// `chrome.HeaderView.Campaign` carries this straight through, and an empty
	// slug renders the instance's brand link instead.
	//
	// The type is the chrome's own `CampaignRef` rather than a fourth copy.
	// That package cannot import this one — a package cannot import its caller,
	// which is the direction the whole split rests on — so a local struct here
	// would need a conversion, and the conversion would have to reach for
	// `label()` and `href()`, which are unexported there *on purpose*: both are
	// rendering decisions, and a route that wants different ones has the fields
	// to put its own values in.
	Campaign chrome.CampaignRef
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
// An alias, and the only alias in this package, because the wording and the
// heading placement moved to `components/ui` with the state that renders them
// while every route that builds this shape kept its two lines. A *conversion*
// would have meant editing all four call sites for no benefit at all: there is
// one field to copy and the name already means one thing.
//
// It is an alias rather than a second struct because two structs would be two
// answers to "what does a failed load look like", and the second one would not be
// rendered by `ui.LoadError` — so a caller that got it wrong would compile, ship
// and show nothing. `TestLoadFailureIsTheStatePackageType` pins the alias so the
// two cannot drift into separate types again.
//
// The doc comment moved to `ui.LoadFailure` rather than being kept here, because
// a comment above an alias describes the *original* and duplicating it is how two
// descriptions of one thing start disagreeing.
type LoadFailure = ui.LoadFailure

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
	// ThemeNotice is the standing theme refusal for this campaign, or nil.
	//
	// UI §4.12.3's third bullet: a brand pair that fails its contrast floor must
	// reach the GM as something they can act on, and the campaign overview is
	// where they land. A `theme.brand_invalid` line in a log is not that — an
	// operator reads logs, and the person who can fix the manifest is the GM.
	//
	// **GM-only, and the gate is here rather than in the route.** The notice
	// names a token and the rule it broke; neither is secret, but §4.12.3 says
	// "a GM notice" and a player seeing a campaign's rejected brand is a
	// question this product should not raise. So the field is filled by the
	// caller **only for a GM**, and `roleIsGM` is what decides — the same
	// one-line gate the role label already needs, and putting it here means a
	// route that forgets cannot leak it by omission.
	ThemeNotice *CampaignNotice
}

// CampaignNotice is one thing wrong with a campaign that the GM can fix.
//
// Two fields and no third, and the shape is the theme package's decision
// reaching this one unchanged: the token that was refused (or `fonts:`, or
// empty for a whole-document refusal) and a fixed sentence naming the rule. Both
// are safe to print, which is what keeps a notice on a page from becoming the
// log leak S-12.3 forbids — a notice built from manifest bytes would be exactly
// the `[!secret]`-in-a-log-line failure wearing a different hat.
type CampaignNotice struct {
	// Token is the custom property name, `fonts:`, or empty.
	Token string
	// Reason is the fixed sentence. Never a manifest byte.
	Reason string
}

// Label is the notice's own short heading: "Theme".
//
// Not "Brand" and not "Warning". §1.2's label table gives the interface one word
// for the campaign's appearance, and the thing being reported is a *refusal of a
// theme file*, which the GM calls a theme.
func (notice CampaignNotice) Label() string { return "Theme" }

// Sentence is the notice as a reader reads it: the token, then the reason.
//
// The token leads because it is the actionable half — the GM edits
// `theme.yaml` and needs to know which line — and it is rendered as code by the
// template rather than interpolated into the sentence, so a token that somehow
// carried markup is escaped rather than interpreted.
func (notice CampaignNotice) Sentence() string {
	if notice.Token == "" {
		return notice.Reason
	}

	return notice.Token + ": " + notice.Reason
}

// roleIsGM reports whether this card's reader is the campaign's GM.
//
// Exported because the route fills `ThemeNotice` and the template guards on it,
// and a predicate that is true in one place and re-derived in the other is the
// shape of bug where a notice reaches a player. Both call this.
func (card CampaignCard) roleIsGM() bool { return card.Role == roleLabel(domain.RoleGM) }

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

// documentTitleForCampaign composes the document title for a route inside a
// campaign.
//
// The full form of UI §7.2 — "Page — Section — Campaign" — where the section is
// the campaign's name. This is a second function rather than a third argument on
// `documentTitle` because the two forms differ in *length*: a pre-campaign route
// drops the middle part rather than substituting something, and a single function
// with a conditional section would be one function that builds two different
// titles depending on a blank string.
func documentTitleForCampaign(page string, instance InstanceView, campaign chrome.CampaignRef) string {
	name := campaign.Name
	if name == "" {
		// `chrome.CampaignRef.label()` is unexported on purpose, so the fallback
		// is written here rather than reached for. It is the same fallback, and
		// duplicating it is cheaper than exporting a method whose only caller
		// would be the package that deliberately does not own it — the two must
		// agree, and `TestCampaignFallsBackToItsSlugInBothPlaces` is what holds
		// them to it. A campaign registered without a name still has a slug, and
		// a document title reading " — — " tells a reader nothing.
		name = campaign.Slug
	}

	if name == "" {
		// Neither field set. A caller that built a campaign reference with
		// nothing in it still gets §7.2's *form*, with the section omitted
		// rather than rendered as a stray separator pair.
		return page + titleSeparator + instance.displayName()
	}

	return page + titleSeparator + name + titleSeparator + instance.displayName()
}

// --- The conversions between the two view-model layers ----------------------
//
// Four, and no more. Each is total — every field either converts or is zero on
// purpose — because a partial conversion here is the one place in the shell
// where a campaign's chrome could silently lose a thing a reader needs, and the
// symptom would be a missing link rather than an error.

// chromeAccount converts the signed-in identity into the form the header's
// account zone reads.
//
// The sign-out target moves with it because `components.AccountView` and
// `chrome.AccountView` disagree about where it lives: the route-facing model
// holds it on `ShellView` (it was added there before the chrome existed, and
// four routes set it there), and the chrome model holds it on the account
// because the zone is what posts. Two fields, one place that knows about it.
func chromeAccount(account AccountView, signOutHref string) chrome.AccountView {
	return chrome.AccountView{
		Username:    account.Username,
		SignOutHref: signOutHref,
	}
}

// chromeDegraded converts the rail's unhealthy subsystems into the footer's.
//
// Deliberately converting a slice rather than sharing one: the two are read by
// two landmarks in two routes' worth of markup, and a `[]chrome.DegradedView`
// field on `InstanceView` would make every route that fills the rail depend on
// the chrome package for a model it has no other reason to know about.
func chromeDegraded(degraded []DegradedView) []chrome.DegradedView {
	if len(degraded) == 0 {
		// Nil rather than an empty slice: `len(view.Degraded) > 0` is the
		// condition the footer tests, and both satisfy it, but nil is what the
		// zero value of the field is and a conversion that manufactured an
		// empty allocation would make the two indistinguishable to a reader of
		// the struct.
		return nil
	}

	converted := make([]chrome.DegradedView, 0, len(degraded))
	for _, item := range degraded {
		converted = append(converted, chrome.DegradedView{
			Name:   item.Name,
			Detail: item.Detail,
		})
	}

	return converted
}

// chromeHeader converts a route's shell view into the banner's model.
//
// `Campaign` is copied rather than derived, because the header's decision of
// which link to render is §8.3's rank 1 and it keys on exactly one thing: is
// there a campaign. The zero `CampaignRef` is the pre-campaign case and renders
// the instance's brand link, so a route that leaves the field empty gets the
// pre-campaign banner with no branch to remember.
//
// `Theme` is left at `chrome.ThemeAuto` and `Connection` at
// `chrome.ConnectionNone` because the server is not entitled to assert either:
// §3.7 resolves both client-side before the first paint, and §6.6's conclusion is
// that the document does not vary by them. A route that *did* pass a theme
// through would make the document vary by the cookie, which is the thing §6.6
// rules out. The search zone is empty for the same reason there is no search
// before a campaign exists.
func chromeHeader(shell ShellView) chrome.HeaderView {
	return chrome.HeaderView{
		InstanceName: shell.Instance.Name,
		Account:      chromeAccount(shell.Account, shell.SignOutHref),
		Campaign:     shell.Campaign,
		Theme:        chrome.ThemeAuto,
		Connection:   chrome.ConnectionNone,
	}
}

// chromeFooter converts a route's shell view into the contentinfo landmark's
// model.
//
// `degraded` is a parameter rather than being read off the shell view, and that
// is S3's integration rule 10 recorded as structure: on a pre-campaign route the
// *rail* already renders `components.DegradedNotice`, and the footer's copy
// carries its own `<h2>Not working</h2>`. Passing the same subsystems to both
// would meet the reader with the same heading twice in one document. So the
// pre-campaign shell passes nothing here and the campaign shell passes the list,
// where the rail is the campaign's own panels and renders none of these.
func chromeFooter(shell ShellView, degraded []DegradedView) chrome.FooterView {
	return chrome.FooterView{
		Version:    shell.Instance.Version,
		StatusHref: shell.StatusHref,
		Degraded:   chromeDegraded(degraded),
		// Bar and Play stay zero: the compact bar's four destinations are the
		// routes behind them (P6's, and the shape of a wiki path is the content
		// pipeline's to decide), and the play row is the realtime plane's. A shell
		// that guessed them would be guessing at three URL schemes in one place.
	}
}
