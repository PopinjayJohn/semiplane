// Package webplugins is semiplane's UI registration tier: the place a UI plugin
// declares what it adds to the interface, and the place those declarations are
// checked against what the gameplay tier is allowed to define.
//
// # The whole of the tier, in one sentence
//
// §10.2 states the structural difference between the tiers: **a gameplay plugin
// defines the operation vocabulary and resolves it into mutations; a UI plugin may
// only emit operations that some gameplay system already resolves.** Everything in
// this package is that sentence made executable, and — more to the point — made
// *unnecessary* for a plugin to cooperate with:
//
//   - **A UI plugin cannot write `campaign_state`.** There is no method on anything
//     this package hands a plugin that mutates state, and the read side it *is*
//     handed (`Reader`) is a snapshot filtered by the reader's campaign visibility.
//     S-10.3's "it dispatches the same intents a human player would, so it passes
//     identical authorisation and validation" is what makes the absence cheap: the
//     path is the player's, not a weaker parallel one.
//
//   - **A UI plugin cannot define an operation.** `Plugin` has no field in which to
//     declare one, so the refusal below is about what a plugin *says* it emits —
//     §10.6's worked example, the dice roller emitting `{"op":"roll"}` — and every
//     one of those declarations is checked against the registered gameplay systems
//     before the plugin is admitted. `plugin.ErrNotAnOp` names the rule.
//
//   - **Read access is filtered by the viewer's campaign visibility, exactly as the
//     wiki is.** Not by a check the tier performs: by the type. A `Reader` answers
//     render queries against one campaign and one reader, and the filtering is done
//     before the value reaches here — the same construction ADR 0024 argues for,
//     with authorisation mounted as a gate rather than tested in a handler.
//
// # What this package is not
//
// It mounts no route and renders no document. `internal/httpapi/router.go` is the
// composition root's, and §10.6's "page types — a new kind plus its route handler
// and templ component" is half a declaration and half a mount: this package holds
// the declaration (`PageType`) and the router decides where it is mounted. That is
// a real limitation of the current shape rather than a design choice, and it is
// stated on `PageType` — a page type is not reachable until somebody mounts it.
//
// The render hooks are the same shape for the same reason. `internal/content` owns
// the Markdown pipeline and this package does not import it, so `RenderHook`
// declares *what* participates in a block and the pipeline's owner wires it. A
// render hook registered here is inert until then, which is the honest description
// of a phase-8 build with no page types in it.
//
// # Where registration happens
//
// Explicitly, in the composition root, exactly as the gameplay tier's does (S-10.1,
// ADR 0011). There is no `init()` here and no package-level registry: `New` is the
// only way to build one and the caller holds what it built. The registry needs the
// gameplay registry to answer S-10.3's question, so `New` takes it — which also
// means a UI plugin cannot be registered before the systems it must emit ops for,
// and that ordering is visible in `cmd/server` rather than in an import graph.
package webplugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
)

// ErrMalformedUIPlugin is the umbrella every refusal in this package satisfies, so a
// composition root can ask one question — "is this something I may register?" — with
// `errors.Is`.
//
// It is a *separate* umbrella from `plugin.ErrMalformedPlugin` rather than a shared
// one, for the reason the tiers are separate: a boot that registered four good
// systems and one malformed UI plugin has to be able to say which half it got, and
// the two halves are wired by different people.
var ErrMalformedUIPlugin = errors.New("webplugins: something this registry cannot accept")

// The refusals about a UI plugin's declaration.
//
// Each names what broke it, and none carries anything from a vault: an id, a kind, a
// path, an operation name. A plugin's identifiers are compiled in, so they are
// operator input rather than attacker input — but a refusal message is still a log
// line and is written as if it were.
var (
	// ErrNoName is a plugin, page type, hook or observer with no identifier.
	//
	// Shared across the four rather than declared four times, because the answer is
	// one sentence: a registration nobody can name in a status page or a
	// `data-testid` is a registration nothing can be looked up.
	ErrNoName = fmt.Errorf("%w: it has no name", ErrMalformedUIPlugin)

	// ErrNoTitle is a declaration with no label for a person.
	ErrNoTitle = fmt.Errorf("%w: it has no title to show a reader", ErrMalformedUIPlugin)

	// ErrNoRender is a declaration whose render function is nil.
	//
	// Its own sentinel because "it renders nothing" and "it renders an empty
	// document" are different failures, and only the first is worth refusing at
	// startup. A component that renders nothing at all is a nil function called by
	// a template engine, which is a panic in somebody's browser tab.
	ErrNoRender = fmt.Errorf("%w: it declares no way to render itself", ErrMalformedUIPlugin)

	// ErrDuplicateUI is a second declaration of one name within the tier.
	//
	// Refused rather than replaced, as in the gameplay registry: which one won
	// would be a function of registration order, and a page type that silently
	// replaced another is a route serving the wrong document.
	ErrDuplicateUI = fmt.Errorf("%w: a name is declared more than once", ErrMalformedUIPlugin)

	// ErrKindOwned is a page type claiming a kind semiplane owns.
	//
	// §10.2.1's table is the gameplay tier's half of this rule and `rules.Validate`
	// is where a *system* is stopped; this is the UI half, and the reason it exists
	// rather than being left to the same check is that a UI page type is a different
	// thing: it mounts a route, so a plugin claiming `token` would be offering to
	// render the table's own game objects. `plugin.ErrKindOwned` is satisfied too,
	// so a boot pass can ask one question about both tiers.
	ErrKindOwned = fmt.Errorf(
		"%w: a page type may only declare kinds of its own; the game-object kinds are semiplane's",
		ErrMalformedUIPlugin,
	)

	// ErrUnknownOp is a UI plugin declaring an operation no registered gameplay
	// system resolves.
	//
	// S-10.3 and §10.6's "it cannot register a new op". It wraps `plugin.ErrNotAnOp`
	// so the question "is this a tier violation?" has one answer across both
	// packages, while the message names the op — which is the half a plugin author
	// can act on, and which must not go on the wire.
	//
	// **It is deliberately not under `ErrMalformedUIPlugin`**, and `errors.Is` follows
	// one chain, so it cannot be. The distinction is worth a sentinel: a declaration
	// with no title is *malformed*, and a declaration emitting `set_hp` from a UI
	// plugin is well formed and forbidden. A boot pass that counted "malformed
	// registrations" would then include a plugin author trying to define an operation,
	// which is the one failure on the list that is a design mistake rather than a bug.
	ErrUnknownOp = fmt.Errorf("%w: no registered gameplay system resolves it", plugin.ErrNotAnOp)

	// ErrBadPath is a page type whose path is not a route path.
	ErrBadPath = fmt.Errorf("%w: a page type's path must start with a slash", ErrMalformedUIPlugin)

	// ErrKindClaimed is a page type declaring a kind another registered page type
	// already declared.
	ErrKindClaimed = fmt.Errorf(
		"%w: another page type already declares that kind",
		ErrMalformedUIPlugin,
	)
)

// Emitter is the only authority a UI plugin holds over game state.
//
// One method, and it is **not** a state handle: it dispatches an intent and reports
// whether the server accepted it. That is the whole of §10.6's "it dispatches the
// same intents a human player would, so it passes identical authorisation and
// validation" — the plugin is on the same road as a keystroke, so the access gate,
// the codec, the codec's bounds and the system's own role check all apply to it
// exactly as they apply to a player.
//
// ## The actor is the reader, and the reader is named by the server
//
// `who` is a `domain.Requestor` and it is the **only** thing this interface takes
// that could decide anything. It comes from the request the plugin is rendering, not
// from the plugin: a UI plugin's emitted intent is attributed to the account that
// loaded the page, so a plugin cannot escalate by being clever, and there is no
// field in which it could say who it is speaking for.
//
// ## What a UI plugin cannot do with this
//
// Roll. §10.6's dice-roller row is explicit — "it must **not** roll client-side; a
// client-side roll is unverifiable and would break the audit trail" — and the reason
// is structural rather than a matter of trust: the answer arrives in the delta the
// hub broadcasts, stamped with the version the hub assigned, and a number a plugin
// invented has no such stamp. A plugin that wants dice asks for a roll and renders
// the server's answer.
//
// **This interface has no implementation in this package**, and that is a gap worth
// naming rather than hiding. `realtime.Hub` applies a `*ClientIntent` against a
// `*Peer`, and `Peer` is admitted by `Join` — a real connection, with a presence
// entry and a slot in the campaign's peer limit. A UI plugin is not a connection, so
// the seam it needs is a hub method that resolves an intent on behalf of a viewer and
// broadcasts the result; that method belongs to `internal/realtime`, which this
// package does not import. It is the one thing the integrator must add.
type Emitter interface {
	// Emit dispatches one intent as `who`, and reports the reason for a refusal.
	//
	// The refusal is a `plugin.RejectionError` (or an error satisfying the same
	// shape) so a plugin can read a `realtime.RejectReason` **without** being able
	// to send one: nothing in a UI plugin's hands can put a reason on the wire, and
	// that asymmetry is what makes a plugin safe to run in-process.
	Emit(ctx context.Context, who domain.Requestor, requested rules.Intent) error
}

// Reader is the read side a UI plugin is given, and it is deliberately the smallest
// thing that can answer a render query.
//
// **It has no method that mutates and no reference to the hub's state.** There is no
// `SetHP`, no `Mutate`, no `*realtime.CampaignState`: §10.2's table's "Writes
// `campaign_state`: gameplay yes / UI **never**" is enforced by the shape of the
// type rather than by a check, which is the only enforcement that survives a refactor.
//
// `Campaign` is here so a plugin can scope a link or a label, and it is **not** a
// capability: nothing on this interface authorises anything on the strength of it
// (ADR 0024 — authorisation is a gate the route mounts, never a check inside a
// handler). `Role` is the reader's role, for a plugin that wants to render a GM-only
// affordance as such; a plugin that renders one to a player has produced a page the
// hub's authorisation will refuse, which is the correct outcome for a cosmetic
// mistake and a bad one for a security one.
//
// ## Where the visibility filtering happens
//
// Before here. A `Reader` is built for one reader over one campaign, after the
// access gate the wiki's own pages are gated by, so "read access to state is
// filtered by the viewer's campaign visibility, exactly as the wiki is" is a property
// of the construction rather than of this interface. What this type guarantees is
// that a plugin cannot *ask* for a different campaign: `Campaign` is a value it may
// read and there is no method that takes one.
type Reader interface {
	// Campaign is the campaign this reader may see.
	Campaign() int64

	// Role is the reader's role in it.
	Role() domain.Role

	// View answers one of the campaign's declared views, through the gameplay
	// system's own `Derive`.
	//
	// The query names a view the campaign's system declared (`rules.System.Views`),
	// so a view name is never free text from a browser — the same property
	// `rules.Query.View` states. The payload is opaque to semiplane and to this
	// tier; a plugin component takes its own Go type out of it, which is what makes
	// a rich sheet type-safe instead of a map traversal (§10.6.1).
	View(query rules.Query) (rules.Payload, error)
}

// PageType is a new kind plus the component that renders it.
//
// §10.6's first of the three things a UI plugin registers. The `Kind` is the claim —
// "a page of this kind exists and this component renders it" — and it joins the
// gameplay registry's kind table, so a front matter value naming it stops being
// prose and becomes a game object (§10.7).
//
// ## Not a route, and the limit stated rather than hidden
//
// `Path` is where the page type is *meant* to be mounted, and this package does not
// mount it: `internal/httpapi/router.go` is the composition root's, and this package
// does not import it. So a registered page type is unreachable until somebody wires
// it, and the tests here can prove the declaration is well formed without a request.
// That is the honest description of a phase-8 build, and it is why this package
// claims no route and appears in no a11y audit.
//
// It is also why `Path` is validated anyway. A path that does not start with a slash
// cannot be mounted, and finding that out when the router is being written is a
// finding about a plugin rather than about a router.
type PageType struct {
	// Kind is the page kind this type declares. Required, and it may not be one
	// semiplane owns.
	Kind rules.Kind

	// Title is the name shown to a person: what a GM sees in the list of kinds
	// this build understands.
	Title string

	// Path is where the page type is mounted, `/c/{slug}/plugins/{kind}` by
	// convention. Leading slash required; the mount itself is not this package's.
	Path string

	// Render turns a page's payload into a component. Required.
	Render func(payload rules.Payload) templ.Component
}

// check reports whether the page type is one this tier can register.
func (p PageType) check() error {
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: a page type's kind %q is not a usable kind name",
			ErrBadKind, p.Kind)
	}

	if rules.IsSemiplaneKind(p.Kind) {
		return fmt.Errorf("%w: page type %q", ErrKindOwned, p.Kind)
	}

	if p.Title == "" {
		return fmt.Errorf("%w: page type %q", ErrNoTitle, p.Kind)
	}

	if p.Render == nil {
		return fmt.Errorf("%w: page type %q", ErrNoRender, p.Kind)
	}

	if !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("%w: page type %q declares %q", ErrBadPath, p.Kind, p.Path)
	}

	return nil
}

// ErrBadKind is a page type whose kind is not a usable kind name.
//
// Its own sentinel beside `ErrKindOwned` because the two are different failures for
// a plugin author: one is a spelling, and the other is an ownership claim they may
// well have meant. `rules.Kind.Valid`'s rule is asked rather than restated, for the
// reason the gameplay registry calls `rules.Validate` instead of reimplementing it.
var ErrBadKind = fmt.Errorf("%w: a declared kind is not a usable kind name", ErrMalformedUIPlugin)

// RenderHook is participation in the Markdown pipeline for a block.
//
// §10.6's second registration, and the thinnest of the three: a hook is named, scoped
// to a kind, and handed the block's text and the page's own payload. **It is not
// wired here.** `internal/content` owns the pipeline, this package does not import it
// — the dependency runs `web → content` and not the other way — so a registered hook
// is inert until the pipeline's owner asks this registry what it holds.
//
// That is a real gap in the current phase and it is the reason `Hook`'s scope is a
// `rules.Kind` rather than a Markdown node name: the pipeline has no node vocabulary
// semiplane owns yet, so scoping by kind is the half of the contract that can be
// stated without inventing one.
type RenderHook struct {
	// Name identifies the hook within this build. Required, and the name a status
	// page prints.
	Name string

	// Kind is the kind of page this hook participates in. Empty means every page,
	// which is the link-preview shape and is why the field is optional.
	Kind rules.Kind

	// Render turns a block's text into a component. Required.
	Render func(text string, payload rules.Payload) templ.Component
}

// check reports whether the hook is one this tier can register.
func (h RenderHook) check() error {
	if h.Name == "" {
		return ErrNoName
	}

	if h.Kind != "" && !h.Kind.Valid() {
		return fmt.Errorf("%w: hook %q declares %q", ErrBadKind, h.Name, h.Kind)
	}

	if h.Render == nil {
		return fmt.Errorf("%w: hook %q", ErrNoRender, h.Name)
	}

	return nil
}

// Observer is a participant in the live chrome: a Datastar patch driven by the
// campaign's deltas.
//
// §10.6's third registration. It is given a `Reader` and a component to fill in, and
// **no delta**: a plugin that wanted the raw frame stream would be handed the
// protocol, and the protocol is where a client-side roll would come from. What it is
// given is a query it wants answered and a component that answers it, which is the
// same authority the link-preview row of §10.6's table describes ("read-only; the
// rule data itself comes from a gameplay module, never from the UI").
//
// ## Who the observer is for
//
// `Reader` is built per viewer per request, so an observer renders one reader's view
// of the table. That is the visibility filter — a private campaign's observer never
// renders for a non-member, because the observer is not reachable without the access
// gate the wiki is gated by.
type Observer struct {
	// Name identifies the observer within this build. Required.
	Name string

	// Query is what the observer wants answered. It is a `rules.Query`, so the view
	// name is one the campaign's system declared and the object is a game object on
	// this campaign.
	Query rules.Query

	// Render turns the answered payload into the fragment the live chrome patches in.
	// Required.
	Render func(payload rules.Payload) templ.Component
}

// check reports whether the observer is one this tier can register.
func (o Observer) check() error {
	if o.Name == "" {
		return ErrNoName
	}

	if !o.Query.Check() {
		return fmt.Errorf("%w: observer %q asks for %q", ErrMalformedUIPlugin, o.Name, o.Query.View)
	}

	if o.Render == nil {
		return fmt.Errorf("%w: observer %q", ErrNoRender, o.Name)
	}

	return nil
}

// Declaration is one UI plugin's whole declaration.
//
// The record `Registry.Register` takes, and it is a struct rather than four calls
// because the three registrations of §10.6 are one decision — this is what this
// plugin adds to the interface — and a caller who registered a page type and then
// failed to register its hook has built half a plugin.
//
// `Emits` is the only field whose authority is checked against another package, and
// it is what makes §10.6's "it cannot register a new op" executable: the tier has no
// other place to learn what a plugin intends to send, and a declared operation that
// no registered gameplay system resolves is refused at startup rather than at the
// table.
type Declaration struct {
	// Name identifies the plugin within this build. Required.
	Name string

	// Title is the name shown to a person, e.g. on a status page.
	Title string

	// PageTypes are the kinds this plugin adds. Optional.
	PageTypes []PageType

	// RenderHooks are the pipeline participations this plugin adds. Optional.
	RenderHooks []RenderHook

	// Observers are the live-chrome participants this plugin adds. Optional.
	Observers []Observer

	// Emits are the operations this plugin's UI dispatches. Optional, and every
	// one is checked against the gameplay registry before the plugin is admitted.
	Emits []rules.Op
}

// check reports whether the plugin is one this tier can register, ignoring the
// operations — those are this registry's business, because the answer is about the
// build and not about the plugin.
//
// Order is the order a plugin author should be told: identity, then the three
// declarations in §10.6's own order, then nothing.
func (d Declaration) check() error {
	if d.Name == "" {
		return ErrNoName
	}

	if d.Title == "" {
		return fmt.Errorf("%w: plugin %q", ErrNoTitle, d.Name)
	}

	for _, pageType := range d.PageTypes {
		if err := pageType.check(); err != nil {
			return fmt.Errorf("webplugins: plugin %q: %w", d.Name, err)
		}
	}

	for _, hook := range d.RenderHooks {
		if err := hook.check(); err != nil {
			return fmt.Errorf("webplugins: plugin %q: %w", d.Name, err)
		}
	}

	for _, observer := range d.Observers {
		if err := observer.check(); err != nil {
			return fmt.Errorf("webplugins: plugin %q: %w", d.Name, err)
		}
	}

	return nil
}

// Entry names one registered UI plugin and its declarations, in registration order.
//
// The output of `Register` and the element of `Plugins`, and it is a distinct type
// from `Declaration` because the two answer different questions: a declaration is
// what a plugin *says*, and an entry is what the build *holds*. Keeping them apart
// means a caller cannot hand a registry a value it has not checked, and that the
// three fields the registry owns — the plugin's position, its copied slices — cannot
// be set by a plugin author.
type Entry struct {
	// Name is the plugin's identifier, as `Declaration.Name` declared it.
	Name string

	// Title is its display name.
	Title string

	// PageTypes, RenderHooks and Observers are the plugin's declarations, as
	// registered. Copies, so a caller cannot mutate the registry through them.
	PageTypes   []PageType
	RenderHooks []RenderHook
	Observers   []Observer

	// Emits are the operations it declared, and every one of them is resolved by
	// some registered gameplay system. Kept because "what does this plugin send?"
	// is a question an operator asks after an incident.
	Emits []rules.Op
}

// Registry is the ordered set of UI plugins this build ships.
//
// Safe for concurrent use, for the same reason the gameplay registry is: registered
// once at boot, read on every page render and on every live-chrome patch.
type Registry struct {
	mu      sync.RWMutex
	plugins []Entry
	// pages, hooks and observers are indices into the plugin list rather than their
	// own copies, so a lookup and a listing cannot disagree about which plugin
	// declared what.
	pages     map[rules.Kind]int
	hooks     map[string]int
	observers map[string]int
	systems   *plugin.Registry
}

// New returns an empty UI registry over the gameplay registry it must consult.
//
// `systems` is required and cannot be nil, because the one question this tier asks
// that is not about a plugin's own declaration — "may this plugin emit `roll`?" — is
// a question about the build. An empty gameplay registry is legal and answers that
// question "no" for everything, which is a build whose plugins were removed and
// whose UI plugins are consequently refused: S-10.6's first row, reached from the
// other direction.
func New(systems *plugin.Registry) *Registry {
	if systems == nil {
		systems = plugin.New()
	}

	return &Registry{
		pages:     make(map[rules.Kind]int),
		hooks:     make(map[string]int),
		observers: make(map[string]int),
		systems:   systems,
	}
}

// Register adds one UI plugin, in the order the call was written.
//
// The refusals, in the order they are asked: the plugin's own declaration first, so
// a malformed page type is reported as such rather than as an unknown op; then the
// operations, against the gameplay registry; then the collisions, because two page
// types claiming one kind is an ambiguity that would otherwise be a route serving
// whichever registered last.
//
// **A refusal adds nothing.** Every check runs before the first mutation of the
// registry's state, so a plugin refused for its fourth operation has not left its
// first page type behind.
func (r *Registry) Register(declared Declaration) error {
	if err := declared.check(); err != nil {
		return err
	}

	if err := r.checkEmits(declared); err != nil {
		return err
	}

	return r.add(declared)
}

// pluginNames is the names of every registered plugin, in order. A helper rather
// than a loop at the call site because the duplicate check and the listing want the
// same list and one of them must not be a `map`.
func pluginNames(registered []Entry) []string {
	names := make([]string, 0, len(registered))
	for index := range registered {
		names = append(names, registered[index].Name)
	}

	return names
}

// Systems returns the gameplay registry these UI plugins were checked against.
//
// Exposed because the composition root wires both tiers and a boot report that
// listed UI plugins without the systems they emit for would be answering half the
// question.
func (r *Registry) Systems() *plugin.Registry { return r.systems }

// Plugins returns every registered UI plugin, in registration order.
//
// **A fresh slice per call**, for the reason the gameplay registry's is: a shared one
// is mutable global state in a package whose argument is that there is none.
func (r *Registry) Plugins() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Clone(r.plugins)
}

// PageType returns the page type registered for a kind, and whether there was one.
//
// **A missing page type is a normal answer, not a refusal.** §10.8's last row is the
// reason: a page whose kind nothing registers degrades to prose with its content
// intact, so a caller asking this has to have a prose answer, and a missing page type
// is exactly the case where prose is correct.
func (r *Registry) PageType(kind rules.Kind) (PageType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	position, found := r.pages[kind]
	if !found {
		return PageType{}, false
	}

	for _, pageType := range r.plugins[position].PageTypes {
		if pageType.Kind == kind {
			return pageType, true
		}
	}

	return PageType{}, false
}

// RenderHook returns the hook with this name, and whether there was one.
func (r *Registry) RenderHook(name string) (RenderHook, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	position, found := r.hooks[name]
	if !found {
		return RenderHook{}, false
	}

	for _, hook := range r.plugins[position].RenderHooks {
		if hook.Name == name {
			return hook, true
		}
	}

	return RenderHook{}, false
}

// Observer returns the observer with this name, and whether there was one.
func (r *Registry) Observer(name string) (Observer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	position, found := r.observers[name]
	if !found {
		return Observer{}, false
	}

	for _, observer := range r.plugins[position].Observers {
		if observer.Name == name {
			return observer, true
		}
	}

	return Observer{}, false
}

// PageTypes returns every registered page type, in registration order.
func (r *Registry) PageTypes() []PageType {
	registered := r.Plugins()

	pageTypes := make([]PageType, 0, len(registered))
	for index := range registered {
		pageTypes = append(pageTypes, registered[index].PageTypes...)
	}

	return pageTypes
}

// Kinds returns every kind this build's UI tier knows, in registration order.
//
// Semiplane's five are **not** in it: they are the gameplay registry's, they are
// registered before any UI plugin is, and a kind is known to the build if *either*
// tier knows it. A caller asking "may this front matter value be a game object?"
// must ask both, and the gameplay registry's `KnownKind` is the one that answers it.
func (r *Registry) Kinds() []rules.Kind {
	pageTypes := r.PageTypes()

	kinds := make([]rules.Kind, 0, len(pageTypes))
	for _, pageType := range pageTypes {
		kinds = append(kinds, pageType.Kind)
	}

	return kinds
}

// EmitsFor reports whether the plugin with this name declared that it emits the
// operation.
//
// Asked by the composition root when wiring a plugin's components, and by a boot
// report: which operations does this build's interface emit, and who emits them. A
// plugin with no such declaration is a plugin that dispatches nothing, which §10.6's
// house-rules row is an example of.
func (r *Registry) EmitsFor(name string, operation rules.Op) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for index := range r.plugins {
		if r.plugins[index].Name == name {
			return slices.Contains(r.plugins[index].Emits, operation)
		}
	}

	return false
}

// checkEmits refuses the first declared operation no registered gameplay system
// resolves.
//
// The loop is over the *declared* operations and the question is asked of the whole
// build, because §10.3's rule is about the build rather than about this plugin's
// campaign: an op is emittable if **some** system resolves it. Whether the campaign
// being rendered resolves it is a per-request question, and a plugin that emits `roll`
// on a Pathfinder campaign gets `unknown_op` from the adapter like anyone else —
// which is the correct answer, and arrives at the table with the plugin's name on it,
// which is why the registration-time check is against any system rather than against
// the first one that answers.
func (r *Registry) checkEmits(declared Declaration) error {
	for _, operation := range declared.Emits {
		if !operation.Valid() {
			return fmt.Errorf("%w: plugin %q declares %q", ErrUnknownOp, declared.Name, operation)
		}

		if !r.systems.Resolves(operation) {
			return fmt.Errorf("%w: plugin %q declares %q", ErrUnknownOp, declared.Name, operation)
		}
	}

	return nil
}

// add writes one checked plugin into the registry.
func (r *Registry) add(declared Declaration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if slices.Contains(pluginNames(r.plugins), declared.Name) {
		return fmt.Errorf("%w: plugin %q", ErrDuplicateUI, declared.Name)
	}

	for _, pageType := range declared.PageTypes {
		if _, taken := r.pages[pageType.Kind]; taken {
			return fmt.Errorf(
				"%w: plugin %q declares %q",
				ErrKindClaimed,
				declared.Name,
				pageType.Kind,
			)
		}
	}

	for _, hook := range declared.RenderHooks {
		if _, taken := r.hooks[hook.Name]; taken {
			return fmt.Errorf(
				"%w: plugin %q declares hook %q",
				ErrDuplicateUI,
				declared.Name,
				hook.Name,
			)
		}
	}

	for _, observer := range declared.Observers {
		if _, taken := r.observers[observer.Name]; taken {
			return fmt.Errorf(
				"%w: plugin %q declares observer %q", ErrDuplicateUI, declared.Name, observer.Name,
			)
		}
	}

	position := len(r.plugins)
	r.plugins = append(r.plugins, Entry{
		Name:        declared.Name,
		Title:       declared.Title,
		PageTypes:   slices.Clone(declared.PageTypes),
		RenderHooks: slices.Clone(declared.RenderHooks),
		Observers:   slices.Clone(declared.Observers),
		Emits:       slices.Clone(declared.Emits),
	})

	for _, pageType := range declared.PageTypes {
		r.pages[pageType.Kind] = position
	}

	for _, hook := range declared.RenderHooks {
		r.hooks[hook.Name] = position
	}

	for _, observer := range declared.Observers {
		r.observers[observer.Name] = position
	}

	return nil
}
