// The plugin wiring: the gameplay systems this build ships, the UI tier over them, and
// the house-rule registry — every registration in this file, and nowhere else.
//
// # Why the registrations live here and not in an `init()`
//
// S-10.1 and ADR 0011: plugins are compiled-in modules registered **in the composition
// root**, in the order a reader can see. `init()` hides that order, and it registers
// into every test binary that imports the package, so two tests in one run cannot
// disagree about what is registered. Both `plugin.Registry` and `houserules.Registry`
// are empty on `New`, which is what makes this file the whole of the answer: there is
// no second door, so a reader asking "what does this binary resolve under?" reads this
// file and gets one list.
//
// # The order, and why each step is where it is
//
//	5e edition → gameplay registry → UI tier → house-rule registry → resolver → hub
//
//  1. **The edition, then the engine.** `overlays.ByID` parses one edition's embedded
//     file and `dnd5e.New` compiles it over the base pack. Both can fail, and both fail
//     **here**, at startup, where an operator is watching for something else — because
//     `rules.System`'s contract says a plugin validates its own data pack in its own
//     constructor, having no `Check() error` to do it with.
//  2. **The gameplay registry**, over the engine. `plugin.Register` calls
//     `rules.Validate` and refuses a duplicate id, a missing codec, and a kind the
//     system may not declare; a refusal here is a defect in *this file* rather than
//     anything an operator did.
//  3. **The UI tier**, over that registry. `webplugins.New` takes the gameplay registry
//     and holds it, because S-10.3's question — "may a UI plugin emit this op?" — is
//     answered by asking the registered systems. Registering a UI plugin first would be
//     a plugin declaring operations against a build that has no systems, which is the
//     refusal that keeps §10.3 from being a claim.
//  4. **The house-rule registry**, last and independent. It is not over the gameplay
//     registry because a module does not compose packs (ADR 0048): it declares keyed
//     settings and `houserules.Registry.Apply` folds them per campaign, at campaign-load
//     time, not at boot.
//
// # One edition, and why that is a decision rather than an omission
//
// `dnd5e.SystemID` is **one id for both editions** (`engine.go` says so, with the
// reason: the editions differ by their *pack versions*, which are their own fingerprint
// component). Two editions registered in one process would therefore be two entries
// claiming one id, and `plugin.Register` refuses the second as `ErrDuplicateSystem` —
// correctly, because a GM at the table cannot tell 5e-2014 from whichever build claimed
// the id second.
//
// So this build registers the edition `defaultEdition` names, and a deployment that
// wants the other one changes **one line**. That is the whole shape of ADR 0011's
// argument: the choice is a line a reviewer can see, and it is here rather than in a
// build tag or an environment variable. `TestTheCompositionRootRegistersTheEditionItNames`
// holds the two in step, because an edition renamed in `overlays` and not here would be
// a boot failure at runtime and nothing else.
//
// # What a campaign naming an unregistered system gets
//
// S-10.6, §10.8, S-14.8: **the wiki still serves.** `campaigns.system_id` is a column
// and a campaign is registered with whatever id its GM's command line stated, so a
// campaign whose plugin was removed or renamed is a real state and not a fault in the
// row. `plugin.UnknownSystemError` names the id so the refusal is actionable, and
// `TestACampaignWithAnUnknownSystemRefusesItsGameAndStillServesItsWiki` asserts both
// halves against the router the binary builds.
//
// Nothing here decides that: it is `plugin.Registry.Resolve` refusing and the wiki
// route never asking. What this file owns is that the registry exists and is empty-able,
// so the refusal is a lookup that misses rather than a boot failure.

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	pluginroutes "github.com/semiplane/semiplane/internal/httpapi/plugins"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// defaultEdition is the edition of 5e this build resolves under.
//
// **One line, and the only place the choice is made.** `overlays.ByID` refuses an id it
// does not ship, so renaming this without renaming the constant is a boot failure
// rather than a silent fallback to some other edition — and that is the direction the
// failure should go, because a deployment resolving a campaign under 2014 while its
// operator asked for 2024 is the worst outcome available and every other one is louder.
//
// 2024 rather than 2014 because it is the current edition of the game and it is the one
// whose critical rule §10.4 names as the difference the data/engine split exists to
// express.
const defaultEdition = overlays.Dnd5e2024

// plugins is everything the composition root registered: the gameplay systems, the UI
// tier over them, and the house-rule registry.
//
// **One value, held as one thing**, because `runServer` hands each part to a different
// subsystem and the failure this shape prevents is three functions each building a
// partial set — a UI tier with no gameplay registry under it, or a resolver over a
// registry the roller cannot ask. Returning one struct means a test can hold exactly
// what the binary holds, which is what makes the wiring assertions below assertions
// about the product rather than about a fixture that resembles it.
type plugins struct {
	// gameplay is the ordered set of gameplay systems this build ships, and the
	// resolver's authority. Empty-able: `plugin.New` returns an empty registry and a
	// build with no gameplay system serves every campaign's wiki and resolves no game
	// anywhere, which is S-10.6's survivable case.
	gameplay *plugin.Registry

	// ui is the UI tier, holding the page types and render hooks. It is handed the
	// gameplay registry by `webplugins.New` and never constructed without one.
	ui *webplugins.Registry

	// houseRules is the compiled-in house-rule module registry, and `RuleModulesForCampaign`
	// rows are resolved through it at campaign load.
	//
	// **Not nil, and not optional**: `houserules.NewRegistry` on a nil receiver returns
	// an empty `Effective` and no error, which is exactly the "the module was never
	// checked" state ADR 0048's `Register` refusal exists to make unreachable. A
	// registry that is a value rather than a pointer to one means the composition root
	// cannot forget it.
	houseRules *houserules.Registry

	// engine is the 5e system, held for the two things that need its own answers rather
	// than the registry's: the fingerprint (a `dnd5e.Versions`, and the gate compares
	// against **this** build's resolution semantics) and the campaign page-kind registry
	// (which asks the registry, not the engine, but is seeded by what the engine
	// registered).
	//
	// Nil in a build with no gameplay system, and every reader here handles that: a nil
	// engine has no fingerprint to offer, and the gate's expected value is then
	// *empty*, which every campaign's `Inspect` reports as drift rather than as a match.
	// That is the honest direction — a build that resolves nothing must not report a
	// campaign as resumable.
	engine *dnd5e.Engine
}

// registerPlugins builds every registry this process resolves under.
//
// **An error here stops the boot, and that is the correct direction.** Every refusal in
// this function is about *this file* or about a compiled-in data pack: a pack that will
// not parse, an edition id this build does not ship, a system that is not a valid
// `rules.System`. None of them is anything an operator did to their instance, and each
// would otherwise be discovered as a campaign that cannot start a game — or, worse, as a
// game that starts under rules nobody chose.
//
// The named returns in each callee (`out gameplaySystem, err error`) are the shape every
// wiring constructor in this package uses, because a caller that forgets to check an
// error here gets a nil engine and a nil registry and a hub that resolves nothing with
// no line in the log saying why.
func registerPlugins(logger *slog.Logger) (out plugins, err error) {
	// 1. The edition and the engine. The only step in this function that reads a data
	// pack, and the only one whose failure is a broken checkout rather than a wiring
	// mistake — which is why its error is wrapped as a pack failure rather than a
	// registration one.
	out.gameplay = plugin.New()

	edition, err := overlays.ByID(defaultEdition)
	if err != nil {
		return plugins{}, fmt.Errorf("read the %s edition of 5e: %w", defaultEdition, err)
	}

	out.engine, err = edition.System()
	if err != nil {
		return plugins{}, fmt.Errorf("build the %s edition of 5e: %w", defaultEdition, err)
	}

	// 2. The registration itself: the system, and the codec that translates what it
	// returns.
	//
	// **`plugin.PlacementCodec` and not a 5e codec**, because 5e's placements *are*
	// semiplane's: a `dnd5e.Engine` encodes its creature into `Mutation.Args` as the
	// same JSON shape `realtime.Placement` marshals, and `PlacementCodec`'s own header
	// says it is the projection for "a system whose game objects are semiplane's own
	// placements — which today is every system". A 5e-specific codec would be a second
	// encoding of one document and a second answer to what a token holds.
	//
	// `err` is named rather than `:=` so a `defer` below could not shadow it, which is
	// the mistake the store's deferred close in `runServer` is commented about.
	if err = out.gameplay.Register(plugin.Entry{
		System: out.engine,
		Codec:  plugin.PlacementCodec{},
	}); err != nil {
		return plugins{}, fmt.Errorf("register the %s edition of 5e: %w", defaultEdition, err)
	}

	// 3. The UI tier, over that registry — and the two reference plugins, in the order
	// they appear in §10.6's table.
	//
	// **`dice` before `linkpreview` is not load-bearing**, and it is worth saying so:
	// neither plugin claims the other's kind, and `webplugins.Register` refuses a
	// duplicate *name* rather than caring about order. The order is the order a reader
	// meets the surfaces.
	out.ui = webplugins.New(out.gameplay)

	// **By pointer, and indexed**, because `webplugins.Declaration` is a multi-field
	// struct: a range over the values copies one per iteration for a loop that only
	// reads them, and gocritic is right to say so. The order is the slice literal's
	// rather than a loop body's, which is where a reader looks for it.
	uiPlugins := []*webplugins.Declaration{
		new(dice.Declaration()),
		new(linkpreview.Declaration()),
	}

	for _, declared := range uiPlugins {
		if err = out.ui.Register(*declared); err != nil {
			return plugins{}, fmt.Errorf("register a UI plugin: %w", err)
		}
	}

	// 4. The house-rule registry.
	//
	// **Empty**, and the emptiness is the answer rather than a gap: this slice ships no
	// house-rule modules, and a registry holding none resolves every campaign through
	// the same empty answer the store returns for an empty set, so no caller needs a
	// length check. A module is registered here when one ships, and this is the only
	// line it would be added beside — which is the whole of "house rules are registered
	// explicitly, never by `init()`".
	out.houseRules = houserules.NewRegistry()

	// One line, once, at the end: what this binary resolves under. An operator reading
	// a boot log should not have to know that the fingerprint exists to find out which
	// edition their instance runs.
	logger.Info("plugins registered",
		slog.String("edition", string(edition.ID())),
		slog.String("edition_name", edition.Name()),
		slog.String("system", out.engine.ID().String()),
		slog.Int("kinds", len(out.gameplay.Kinds())),
		slog.Int("page_types", len(out.ui.PageTypes())),
		slog.Int("house_rule_modules", len(out.houseRules.IDs())),
	)

	return out, nil
}

// fingerprint returns the resolution semantics this process offers, for the gate.
//
// **The engine's own four components and nothing composed by hand**, because
// `realtime.FingerprintOf` is the package that defines the encoding and
// `checkComponent` is the only place a reserved separator is refused. A hand-written
// fingerprint would defer that failure until a campaign tried to resume, which is the
// exact failure the encoding exists to make a boot failure.
//
// **An empty fingerprint when there is no engine**, and the direction matters: a gate
// expecting nothing reports every *fingerprinted* campaign as drift on `ComponentSystem`,
// while a gate expecting a made-up fingerprint would report it as drift on a component
// that is not the problem. "This build resolves nothing" is the answer, and `Status`
// already has a word for the related case — `Unfingerprinted` — which this does not
// conflate with it.
func (p plugins) fingerprint() realtime.Fingerprint {
	if p.engine == nil {
		return realtime.Fingerprint{}
	}

	versions := p.engine.Versions()

	fingerprint, err := realtime.FingerprintOf(realtime.Descriptor{
		System:      versions.System,
		Ruleset:     versions.Ruleset,
		BasePack:    versions.BasePack,
		OverlayPack: versions.OverlayPack,
	})
	if err != nil {
		// Unreachable by construction, and a panic rather than a substituted zero
		// value for the reason `expectedFingerprint` gave before phase 8: this runs
		// once at boot over values a compiled-in pack produced and the engine's own
		// constructor already validated, so a failure is a defect in the package that
		// defines the encoding. The alternative — a `Fingerprint{}` — compares as drift
		// against every campaign on the instance while naming no component, which is the
		// lie this whole file's fingerprint handling exists to avoid.
		panic(fmt.Sprintf("semiplane: the 5e system's fingerprint does not encode: %v", err))
	}

	return fingerprint
}

// systemOf reports which gameplay system a campaign plays under.
//
// The seam between the column and the registry, and it is a **function type for the
// same reason `plugin.SystemOf` is**: the answer lives in `campaigns.system_id`,
// `internal/store` is not this file's to read a column out of directly without naming
// the statement twice, and the composition root is the only place that can join them.
//
// `store.CampaignByID` rather than a bare `SELECT system_id`. **Deliberately, and it is
// the one place this file could have written its own statement**: the row is eight
// columns and this reads one, so a hand-written `SELECT` would be marginally cheaper —
// and it would be a second statement that has to agree with the store's column list,
// which is the mistake `selectState` and `selectRulesetVersion` each name in their own
// comments. A column added to `campaigns` would need three edits rather than one, and
// the third would be invisible.
//
// The error is wrapped with the campaign id, because a bare `store.ErrNotFound` tells a
// caller reading a log line *which* campaign's system is unreadable only by guesswork,
// and §10.8's refusal is worthless without the campaign.
func (p plugins) systemOf(backing *store.Store) plugin.SystemOf {
	return func(ctx context.Context, campaignID int64) (rules.ID, error) {
		campaign, err := backing.CampaignByID(ctx, campaignID)
		if err != nil {
			return "", fmt.Errorf(
				"read the system of campaign %d: %w", campaignID, err,
			)
		}

		return rules.ID(campaign.SystemID), nil
	}
}

// systemFor reports the gameplay system a campaign plays under, for the UI tier.
//
// **A different question from `systemOf`, and the difference is what happens on a
// miss.** `plugin.SystemOf` feeds the *resolver*, and a campaign whose id nothing
// registered must refuse its game — §10.8's requirement. This feeds a widget that
// renders the campaign's own notation, and S-10.6's first row reached from the UI side
// says the widget renders **without** notation rather than not at all: the roller
// still posts, the system still refuses the roll it cannot resolve, and the reader is
// told nothing about which systems the build ships.
//
// So this returns the error and lets `plugins.Systems`'s caller decide, rather than
// substituting a system: substituting one would be resolving a campaign's rolls under
// rules its GM did not choose, which is precisely what the registry refuses to do by
// default. `plugins.Handler.Systems`'s own doc comment says a nil or an error is not
// fatal and the widget says so.
func (p plugins) systemFor(backing *store.Store) pluginroutes.Systems {
	return func(ctx context.Context, campaignID int64) (rules.System, error) {
		id, err := p.systemOf(backing)(ctx, campaignID)
		if err != nil {
			return nil, err
		}

		entry, err := p.gameplay.Resolve(id)
		if err != nil {
			return nil, fmt.Errorf("the system for campaign %d: %w", campaignID, err)
		}

		return entry.System, nil
	}
}

// pageKinds returns the kind registry the content pipeline and the editor ask.
//
// **The gameplay registry, not a hand-written list**, and the reason is that a
// hand-written list is the two-vocabularies defect `pageKinds`'s original comment named:
// semiplane's five built-in kinds are registered by `plugin.New` (so a build whose
// plugins were removed still knows them) and a gameplay plugin's `ContentKinds()` join
// them (so `ancestry` is a game object in a build shipping 5e and prose in one that is
// not). A second list beside the registry would agree with it until an overlay disagreed
// with it, and a page that renders as a token in development and as prose in production
// is the kind of defect only a user ever finds.
//
// `HasPageKind` and not `KnownKind`: the domain's interface takes the **string a page
// declared**, verbatim, with no trimming and no case folding — "is this the identifier
// the registry registered", not "is this recognisable as one of its kinds". The
// conversion is `rules.Kind(name)` and nothing else, because a kind that is not well
// formed cannot be in the registry and the answer is prose either way.
func (p plugins) pageKinds() domain.PageKindRegistry {
	return kindRegistry{known: p.gameplay}
}

// kindRegistry is the gameplay registry seen as `domain.PageKindRegistry`.
//
// **A struct over the registry rather than a method on `plugins`**, because
// `content.NewRenderer` and `wiki.Handler` take the interface and holding a pointer to
// the composition root's value would be a package reaching back into `main`. One field,
// one method, and the nil case handled: a nil `known` knows nothing, which is the
// answer that degrades to prose rather than the one that panics — `ResolvePageKind`'s
// own comment makes that the required direction.
type kindRegistry struct {
	known *plugin.Registry
}

// HasPageKind reports whether name is a kind this build registered.
func (k kindRegistry) HasPageKind(name string) bool {
	if k.known == nil {
		return false
	}

	return k.known.KnownKind(rules.Kind(name))
}

// ensure the adapter satisfies the domain's interface at compile time rather than by a
// test that fails only when a page is parsed.
var _ domain.PageKindRegistry = kindRegistry{}

// reportMissingSystems is the boot pass over every campaign's `system_id`.
//
// # Why a boot pass, and not only the per-intent refusal
//
// §10.8's row says a campaign naming an unregistered system "refuses to start a game
// and says which ID it wants". **The refusal is per-intent** — `plugin.Registry.Resolve`
// refuses on every dispatch, which is what S-14.8's test asserts — and the *saying* has
// two audiences with two different needs:
//
//   - a **GM at the table** sees `server_error` on the wire, which is correct and says
//     nothing about the rules: `plugin.RejectionError`'s text is the reason alone,
//     because that string reaches a browser and a log line (S-12.3);
//   - an **operator reading a log** needs to know which campaign, which id, and that
//     nothing is broken about the instance. Nothing produces that line without this
//     pass, because the refusal is only ever seen by a client.
//
// So the refusal names the id as a **typed field** an alert-free operator can read, and
// this pass is what puts it in a log with the campaign beside it.
//
// # The line, and its exact shape
//
// **`plugin.missing`, an §13.2 event name (ADR 0032), and the campaign's slug.** The slug
// is operator-supplied from their own registration, so naming it discloses nothing; the
// id is a column value and the same. **Neither the refusal's text nor any error text is
// logged** — `errorClass` for the typed error is `plugin.missing`, which is
// content-free by ADR 0032's own definition, and the id goes in a `slog.String` of its
// own rather than being interpolated into a message.
//
// # Why an error and not a warning
//
// Because a campaign that cannot start its game is a GM who will discover it
// mid-session. The wiki still serves, so the instance is healthy and nothing is on fire
// — and `resumeCampaignStates` treats an unreadable ruleset the same way, for the same
// reason: one campaign is one campaign.
//
// # Why it runs before the server listens
//
// Same argument as the fingerprint pass beside it: drift discovered at boot is a line an
// operator reads with a coffee in hand, and the same condition discovered by a failed
// join is a GM waiting.
//
// **No `context.Context`**, and the absence is the honest shape rather than an omission:
// every value this pass reads is a column already loaded by the caller's campaign list,
// so there is nothing to query and nothing to cancel. A parameter here would be a
// convention copied from the passes beside it, and a reader would reasonably assume the
// pass does I/O.
func reportMissingSystems(
	systems *plugin.Registry,
	campaigns []domain.Campaign,
	logger *slog.Logger,
) {
	if systems.Empty() {
		// One line, once, and it is an error rather than a per-campaign one: a build
		// whose plugins were removed refuses **every** campaign, and the answer to
		// "why" is a single fact about this binary rather than a fact about each row.
		logger.Error("plugin.missing",
			slog.String("detail", "this build registers no gameplay system, so no "+
				"campaign can start a game; every campaign's wiki still serves"),
		)

		return
	}

	// Indexed: domain.Campaign is 128 bytes and only the id and slug are read here.
	for i := range campaigns {
		campaign := &campaigns[i]

		if _, known := systems.Lookup(rules.ID(campaign.SystemID)); known {
			continue
		}

		// The id in its own attribute, and **not** in the message. A message
		// interpolating it would be a second place the id is spelled, and the class
		// above is what an alert matches — the attribute is for a human reading the
		// line.
		logger.Error("plugin.missing",
			slog.String("campaign", campaign.Slug),
			slog.String("system", campaign.SystemID),
			slog.String("detail", "no gameplay system is registered with this id, so "+
				"this campaign cannot start a game; its pages still serve"),
		)
	}
}
