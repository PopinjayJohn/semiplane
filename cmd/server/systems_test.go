// The composition root's plugin wiring, and the two §10.8 rows nothing else can assert.
//
// # Why these are here rather than in the packages they concern
//
// Each is a claim about **the wiring**, and no package's own test can make one:
//
//   - `TestACampaignWithAnUnknownSystemRefusesItsGameAndStillServesItsWiki` needs a
//     campaign row, a content root, a page, the index, the hub and the router — all
//     assembled by `instance.serve`, which is the composition root's second half.
//     `plugin.Registry`'s own tests hold that `Resolve` refuses an unknown id with the
//     id in the message; nothing holds that the *wiki still serves*, because the wiki
//     route never asks the registry and a fixture that proved otherwise would have to
//     be built to fail.
//   - `TestAPageWhoseKindNoPluginRegistersDegradesToProseWithItsContentIntact` is
//     S-14.7, and the registry is what makes the case exist. Before phase 8 the kind
//     registry was empty by construction, so every kind was prose and the requirement
//     was vacuous. Now `dnd5e` registers kinds — `ancestry`, `feat`, `spell` — so the
//     test can plant a page whose kind **is** registered, assert it is not prose, and
//     then plant one whose kind is not, and assert the degradation. One of those two
//     is the requirement; the other is what keeps it from being vacuous.
//
// # Both are mutations of the tree's own fixture
//
// Each names what it breaks if broken. The unknown-system test has three claims — the
// wiki is 200, the game refuses, and the refusal **names the id** — and any one of them
// passing while the other two fail is the interesting state, so they are asserted
// separately rather than as one "the wiki works" check.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
)

// unknownSystemID is a system this build does not register.
//
// **A string that is not `dnd5e`, and not merely a typo.** The shape §10.8's row is about
// is a campaign whose plugin was *removed or renamed* while the campaign survived — so
// the id must be a perfectly well-formed `rules.ID` that resolves to nothing. A
// malformed id would be refused by `rules.ParseID` and would test a different branch
// entirely, which is why `plugin.Registry.Resolve`'s `id.Valid()` check exists and why
// this fixture does not rely on it.
const unknownSystemID = "5e-2024"

// TestACampaignWithAnUnknownSystemRefusesItsGameAndStillServesItsWiki is S-14.8 and
// §10.8's first row, and it is three assertions that can fail independently.
//
//  1. **The wiki route answers 200 with the page's content.** §10.8 says "Wiki still
//     serves", and this is the half a GM notices: a campaign whose game cannot start is
//     still a wiki with pages in it. A build that refused the whole campaign would
//     satisfy "the game refuses" and break the product's reason for existing.
//
//  2. **The game refuses.** Dispatching an intent for that campaign through the hub the
//     binary builds produces a refusal, not an application.
//
//  3. **The refusal names the id.** §10.8's requirement is "says which ID it wants", and
//     "the game cannot start" does not tell a GM what to do about it. The id is checked
//     through `errors.As` on the typed `*plugin.UnknownSystemError` **and** in the
//     error's text, because the two are separately reachable: a caller reading the type
//     gets the id as a field, and an operator reading a log line gets it in the
//     message. Only checking the message would pass with the field removed.
func TestACampaignWithAnUnknownSystemRefusesItsGameAndStillServesItsWiki(t *testing.T) {
	inst := newInstance(t)

	// A campaign registered with an id this build does not resolve, and a page in its
	// vault — the markdown is always recoverable because it is a file on disk, which is
	// what makes claim 1 possible at all.
	stranger := inst.registerCampaignWithSystem("strangehold", unknownSystemID)
	inst.writePage(stranger, "Goblin.md",
		"---\ntitle: Goblin\n---\n\nA goblin is here.\n")

	registered := append([]domain.Campaign{}, inst.registered...)
	handler := inst.serve(registered)

	t.Run("TheWikiStillServes", func(t *testing.T) {
		recorder := askOne(t, handler, "/c/strangehold/wiki/Goblin")

		if recorder.Code != http.StatusOK {
			t.Fatalf("the wiki answered %d for a campaign whose system_id is unregistered; "+
				"§10.8 requires the game to refuse and the wiki to keep serving, and an "+
				"instance that took the whole campaign down over a missing plugin has "+
				"failed toward not serving", recorder.Code)
		}

		body := bodyOf(t, recorder)

		if !strings.Contains(body, "A goblin is here.") {
			t.Errorf("the page's body is missing from a 200 response; the wiki served "+
				"something that is not the page:\n%s", truncate(body))
		}

		if strings.Contains(body, unknownSystemID) && strings.Contains(body, "not registered") {
			t.Errorf("the wiki page names the missing system %q, which turns a GM's "+
				"content into a diagnostic; the refusal belongs on the game and not in a "+
				"page a reader of the campaign came for", unknownSystemID)
		}
	})

	t.Run("TheGameRefusesAndNamesTheID", func(t *testing.T) {
		_, err := inst.dispatch(t, stranger.ID, "roll")

		if err == nil {
			t.Fatal("an intent resolved for a campaign whose system_id nothing registered; " +
				"§10.8 requires the game to refuse rather than resolve under no rules")
		}

		// The typed half: the id is a field, so a caller does not have to parse a
		// message to learn which system to install.
		var unknown *plugin.UnknownSystemError
		if !errors.As(err, &unknown) {
			t.Fatalf("the refusal is %T, want something wrapping *plugin.UnknownSystemError; "+
				"a refusal a GM cannot act on is 'the game cannot start' with the "+
				"actionable half moved onto the reader", err)
		}

		if string(unknown.ID) != unknownSystemID {
			t.Errorf("the refusal names system %q, want %q; the campaign's own column "+
				"holds %q and the id it wants is the only thing a GM can act on",
				unknown.ID, unknownSystemID, unknownSystemID)
		}

		// The error's **text** deliberately does not name the id, and asserting that it
		// did would be asserting the opposite of S-12.3: `plugin.RejectionError.Error`
		// returns the reason and nothing else, because that string reaches a browser
		// and a log line, and the reason is chosen to be content-free. The id reaches
		// a caller through the typed field, deliberately read.
		//
		// What the *boundary* classifies as is the outer refusal — `plugin.rejected:`
		// plus the reason — and that is correct rather than a gap: `errors.AsType`
		// takes the outermost `Classed`, and the operator-facing half of this
		// requirement is the boot pass below, which logs `plugin.missing` with the
		// campaign and the id beside it. A per-intent class is the one a browser sees,
		// and a browser is told the reason and nothing more.

		// And nothing was applied. A refusal that stamped a version would be worse than
		// no answer: a client would reconcile against a change nobody made.
		if live := inst.plane.registry.Live(); live != 0 {
			t.Errorf("the refused dispatch left %d live state(s); a refusal must not "+
				"open a campaign's state, or a GM who cannot play has a table they "+
				"cannot inspect", live)
		}
	})
}

// TestAPageWhoseKindNoPluginRegistersDegradesToProseWithItsContentIntact is S-14.7 and
// §10.8's last row: an unknown kind is **inert, not fatal**, and the markdown is
// always recoverable because it is a file on disk.
//
// # Both directions, and the second is why the first means anything
//
// The 5e pack declares kinds — `ancestry`, `feat`, `spell` — and the registry knows
// them because `plugin.Register` joins a system's `ContentKinds()`. So this asserts:
//
//   - a page whose `kind` **is** registered resolves to that kind, not to prose; and
//   - a page whose `kind` is **not** registered degrades to prose, with its body and its
//     title intact and a 200.
//
// Either alone is satisfiable by a constant: a resolver that always said `prose` would
// pass the second, and one that refused an unknown kind would pass the first. And the
// first is what keeps the second honest — a build whose registry knew nothing would make
// every page prose and the degradation untestable.
//
// # Why the degradation is a page rather than an error
//
// S-3.3's answer is that the markdown is on disk. A plugin is removed, a page keeps its
// `kind: ancestry` front matter, and the page must still render — with the front matter
// as prose rather than as a game object. The alternative is a 500 for every page whose
// plugin went away, which loses content that was never in danger.
func TestAPageWhoseKindNoPluginRegistersDegradesToProseWithItsContentIntact(t *testing.T) {
	inst := newInstance(t)

	// One of the kinds the registered 5e pack declares. **Read from the pack** rather
	// than spelled as a literal, because a hardcoded slug is a golden value that changes
	// whenever the pack does — and a fixture that names a kind the registry no longer
	// knows would be exercising the degradation path while claiming to exercise the
	// recognition one.
	declared := declaredPluginKind(t, inst.plugins)
	if declared == "" {
		t.Skip("this build registers no gameplay kinds, so an unknown kind is the only " +
			"case reachable and the recognition half of this test cannot be asserted")
	}

	inst.writePage(inst.campaign, "Ancestry.md",
		"---\ntitle: Ancestry\nkind: "+declared+"\n---\n\nElves.\n")
	inst.writePage(inst.campaign, "Lost.md",
		"---\ntitle: Lost Notes\nkind: homebrew-thing-no-plugin-declares\n---\n\n"+
			"The body that must survive.\n")

	handler := inst.serve(inst.registered)

	t.Run("AKindTheRegistryKnowsIsHonoured", func(t *testing.T) {
		recorder := askOne(t, handler, "/c/"+fixtureSlug+"/wiki/Ancestry")

		if recorder.Code != http.StatusOK {
			t.Fatalf("a page of a registered kind answered %d, want 200", recorder.Code)
		}

		want := domain.PageKind(declared)
		if kind := inst.pageKind(t, "Ancestry.md"); kind != want {
			t.Errorf("the indexed page's kind is %q, want %q; the registry knows this "+
				"kind, so degrading it to prose would be the wiki quietly disagreeing "+
				"with the plugin registry that produced it", kind, want)
		}
	})

	t.Run("AKindNoPluginRegistersDegradesToProse", func(t *testing.T) {
		recorder := askOne(t, handler, "/c/"+fixtureSlug+"/wiki/Lost")

		// **200 and not an error**, which is the whole of the requirement. A page whose
		// plugin was removed is still a page; refusing to render it loses markdown that
		// was never in danger.
		if recorder.Code != http.StatusOK {
			t.Fatalf("a page whose kind no plugin registers answered %d, want 200; "+
				"§10.8's last row makes an unknown kind inert rather than fatal, and "+
				"the markdown is always recoverable because it is a file on disk",
				recorder.Code)
		}

		body := bodyOf(t, recorder)

		if !strings.Contains(body, "The body that must survive.") {
			t.Errorf("the degraded page lost its body; the requirement is prose with the "+
				"content intact, not prose that replaced the content:\n%s", truncate(body))
		}

		// The heading is the page's **base name**, not its front-matter title, and that
		// is `pageName`'s deliberate refusal (ADR 0017: one page has one name
		// everywhere it is referred to, and a title read out of the document must not
		// reach a cache key). So the assertion is that the page is served *as itself* —
		// heading and nav entry both present — rather than that a title reached it.
		if !strings.Contains(body, `>Lost<`) {
			t.Errorf("the degraded page lost its heading; a page whose kind is unknown "+
				"still renders under its own name, and a blank heading is a page "+
				"nothing can be linked to:\n%s", truncate(body))
		}

		// The index agrees, because the index is what the nav tree and the search
		// results read. A page rendering as prose while the index says it is a game
		// object would be two answers, and the nav tree would link to a page that
		// renders differently from the list entry.
		if kind := inst.pageKind(t, "Lost.md"); kind != domain.KindProse {
			t.Errorf("the indexed page's kind is %q, want %q; an unrecognised kind must "+
				"resolve to prose in the index too, or the nav tree offers a game object "+
				"the page will not render", kind, domain.KindProse)
		}
	})
}

// TestTheCompositionRootRegistersTheEditionItNames holds the one line that chooses the
// edition against the package that ships them.
//
// **A rename in either place is a boot failure, and this is what makes it one at a test
// rather than at a deployment.** `overlays.ByID` refuses an id it does not ship, so
// `defaultEdition` naming an edition that does not exist stops the boot with a message
// naming it — a good failure, but only if it is reached. Without this assertion a
// renamed edition is discovered by an operator running the binary, and the failure is a
// refusal to start rather than a game under the wrong rules.
//
// The other direction is held by asking the package what it ships: a build registering
// an edition the overlay package does not carry would resolve a campaign under rules no
// embedded file describes.
func TestTheCompositionRootRegistersTheEditionItNames(t *testing.T) {
	t.Parallel()

	edition, err := overlays.ByID(defaultEdition)
	if err != nil {
		t.Fatalf("the composition root names edition %q, which `overlays.ByID` refuses: "+
			"%v. Either the constant was renamed without the package or the package "+
			"stopped shipping it, and both are a build fault rather than anything an "+
			"operator did", defaultEdition, err)
	}

	if !slices.Contains(overlays.IDs(), defaultEdition) {
		t.Errorf("the composition root names edition %q, which is not among the %v this "+
			"build ships", defaultEdition, overlays.IDs())
	}

	// The edition's **overlay** version is what the fingerprint's fourth component
	// names, and asserting it is non-empty here is the cheap half of the pairing the
	// next test makes exact: an edition with no overlay version would leave the
	// fingerprint's overlay component empty, which `validate` exempts as a standalone
	// pack — a real value for a system that ships one, and a silent loss of the edition
	// distinction for a system that ships two.
	if edition.Version() == "" {
		t.Error("the named edition carries no overlay version, so the fingerprint's " +
			"overlay component would be empty and two editions would compare equal")
	}

	// The edition builds, and it is the same edition the registry resolved to.
	registered, err := registerPlugins(discardLogger())
	if err != nil {
		t.Fatalf("register the plugins: %v", err)
	}

	entry, found := registered.gameplay.Lookup(registered.engine.ID())
	if !found {
		t.Fatalf("the registered set holds no system with id %q, so the resolver would "+
			"refuse every campaign including the ones this build is meant to play",
			registered.engine.ID())
	}

	// The engine the registry holds is **the edition's**, and the assertion is on the
	// pair of components rather than on equality between them: `RulesetVersion` is the
	// engine's own semantics and deliberately excludes both packs (ADR 0018), so
	// requiring them to agree would assert the exclusion away.
	if got := entry.System.RulesetVersion(); got == "" {
		t.Error("the registered system reports no ruleset version, so the fingerprint " +
			"has an empty component and every campaign reads as drift")
	}

	if got := entry.System.ContentKinds(); len(got) == 0 {
		t.Error("the registered system declares no content kinds, so every page " +
			"degrades to prose and S-14.7's degradation is indistinguishable from a " +
			"registry that knows nothing")
	}
}

// TestTheFingerprintNamesTheEngineThatIsRegistered is the half of the above that has a
// test failure rather than a boot failure.
//
// **The gate compares against this value**, so a fingerprint naming a different pack
// from the engine that resolves would strand every campaign on the instance with a
// `ComponentBasePack` drift message naming a file that is not the one in use. That is
// the worst drift report this codebase can produce — it is actionable and wrong — so the
// pairing is asserted rather than left to the two functions agreeing by accident.
//
// Four components, each read from the engine. `OverlayPack` is included because 2024 is
// an overlay and a fingerprint naming the base pack alone would let an edition change
// pass the gate unremarked.
func TestTheFingerprintNamesTheEngineThatIsRegistered(t *testing.T) {
	t.Parallel()

	registered, err := registerPlugins(discardLogger())
	if err != nil {
		t.Fatalf("register the plugins: %v", err)
	}

	fingerprint := registered.fingerprint()
	versions := registered.engine.Versions()

	for _, component := range []struct {
		name string
		got  string
		want string
	}{
		{name: "system", got: fingerprint.System, want: versions.System},
		{name: "ruleset", got: fingerprint.Ruleset, want: versions.Ruleset},
		{name: "base pack", got: fingerprint.BasePack, want: versions.BasePack},
		{name: "overlay pack", got: fingerprint.OverlayPack, want: versions.OverlayPack},
	} {
		if component.got != component.want {
			t.Errorf("the fingerprint's %s is %q, want %q; the gate compares every "+
				"campaign's persisted version against this value, so a component naming "+
				"a different pack from the engine that resolves strands the campaign "+
				"with a drift message about a file that is not in use",
				component.name, component.got, component.want)
		}
	}

	// And it round-trips: the gate **parses** the column back rather than comparing
	// strings, so a fingerprint that cannot be parsed compares as drift against every
	// campaign with no component named. `FingerprintOf` refuses a separator and a
	// control character, and this is the assertion that the values the engine produced
	// survive their own encoding.
	parsed, err := realtime.ParseFingerprint(fingerprint.String())
	if err != nil {
		t.Fatalf("the engine's own fingerprint does not parse back: %v. A version this "+
			"build wrote and cannot read is drift against every campaign, with no "+
			"component to name", err)
	}

	if !parsed.Equal(fingerprint) {
		t.Errorf("the fingerprint round-tripped to %q, want %q", parsed, fingerprint)
	}
}

// TestTheHouseRuleRegistryIsNotOptionalAndTheLoggerIs checks §10.5's two halves, and
// they are not symmetric.
//
// **`houserules.Registry.Apply` takes a `*slog.Logger` and refuses to be a nil
// registry**: a nil logger writes nothing, because a campaign whose GM enabled two
// conflicting modules loads identically whether or not anybody is watching, and making
// the log mandatory would give a package with no other reason to know about `slog` one.
// A nil *registry* is refused, because an application through a set nobody admitted has
// not been checked — which is the whole of ADR 0048's enforcement point.
//
// So the composition root must build the registry, and both behaviours are asserted here
// because a composition root that wired `nil` would compile (`Apply` has a nil-receiver
// check) and would resolve every campaign through an empty answer.
func TestTheHouseRuleRegistryIsNotOptionalAndTheLoggerIs(t *testing.T) {
	t.Parallel()

	registered, err := registerPlugins(discardLogger())
	if err != nil {
		t.Fatalf("register the plugins: %v", err)
	}

	if registered.houseRules == nil {
		t.Fatal("the composition root registered no house-rule registry; " +
			"`houserules.Registry.Apply` refuses a nil registry, so a campaign would " +
			"be resolved through a module set nobody admitted")
	}

	// An empty module set applies cleanly and logs nothing — which is the honest state
	// of a build shipping no modules, and the reason an empty registry is a value
	// rather than a fault.
	effective, err := registered.houseRules.Apply(t.Context(), nil, discardLogger())
	if err != nil {
		t.Fatalf("an empty module set did not apply: %v", err)
	}

	if !effective.Empty() {
		t.Errorf("an empty module set resolved to %d applied change(s) and %d conflict(s); "+
			"this build ships no house-rule modules, so the answer is the empty one",
			len(effective.Applied()), len(effective.Conflicts()))
	}

	// And the nil logger is genuinely accepted, because a test with no logger is the
	// common case and a package that insisted on one would make every caller build one.
	if _, err := registered.houseRules.Apply(t.Context(), nil, nil); err != nil {
		t.Errorf("applying an empty module set with no logger = %v, want success; "+
			"§10.5's conflicts are logged and a nil logger writes nothing, which is "+
			"why the parameter is optional and the registry is not", err)
	}
}

// TestThePluginsRouteIsMountedOnTheRouterTheBinaryServes is the mount, asserted through
// the router rather than through `plugins.Mount`.
//
// **The one claim no route package's own tests can make.** `plugins_test` assembles the
// same chain on a mux of its own — its harness says so out loud, and for good reason, so
// that a change on either side shows up as a failing test. The consequence is that every
// one of those tests passes with `plugins.Handler` absent from `httpapi.NewRouter`
// entirely, which is exactly the state the tree was in before this wiring: a package
// with six audits and no route.
//
// So the assertion is a **status code through the product's router**: an anonymous reader
// of a public campaign asking for the roller's path gets 401, not 404. 404 would mean
// the route is not on the router — the campaign subtree is mounted and gated and no
// handler inside it matched — and 101/200 would mean it answered without a gate, which
// is worse: a roller anybody who can reach the port can post a roll to.
func TestThePluginsRouteIsMountedOnTheRouterTheBinaryServes(t *testing.T) {
	inst := newInstance(t)
	handler := inst.serve(inst.registered)

	// The path comes from the registry rather than from a literal, because §10.6's
	// page types declare their own paths and `plugins.Mount` derives its patterns from
	// them. A literal here would be a second answer for "where does the roller live",
	// and the test would pass while the route moved.
	paths := pluginPaths(t, inst.plugins)
	if len(paths) == 0 {
		t.Fatal("the UI registry declares no page types, so there is no route to mount " +
			"and this assertion cannot be made")
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			recorder := askOne(t, handler, strings.ReplaceAll(path, "{slug}", fixtureSlug))

			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("GET %s answered %d for an anonymous reader of a public campaign, "+
					"want %d. 404 means the route is not on the router; %d would mean it "+
					"answered without the play gate, which is a tabletop route anybody "+
					"who can reach the port can post a roll to",
					path, recorder.Code, http.StatusUnauthorized, http.StatusOK)
			}
		})
	}
}

// declaredPluginKind returns one kind a registered gameplay system declared, and fails
// the test if none did.
//
// **A gameplay kind rather than a built-in**, because the five semiplane kinds are
// registered by `plugin.New` in every build including one whose plugins were removed —
// so a fixture using `token` would pass against a registry with no systems in it, which
// is the vacuous case this helper exists to avoid. `rules.Validate` refuses a system
// declaring a built-in, so every kind on this list came from a pack.
func declaredPluginKind(t *testing.T, registered plugins) string {
	t.Helper()

	if registered.gameplay == nil {
		t.Fatal("no gameplay registry; the fixture cannot ask what kinds it knows")
	}

	for _, kind := range registered.gameplay.Kinds() {
		if registered.gameplay.SemiplaneKind(kind) {
			continue
		}

		return kind.String()
	}

	t.Fatalf("no registered system declared a content kind, so every kind in this build " +
		"is one of semiplane's own five and S-14.7's degradation cannot be distinguished " +
		"from a registry that knows nothing")

	return ""
}

// pluginPaths returns the mux patterns the UI registry's page types declare, with the
// slug placeholder intact.
//
// **Read from the registry, not written here.** §10.6's "naming a route is a matter of
// registering it" is only true if the test's expectation also comes from the registry;
// a path written in a test is a second answer, and it is the one a plugin author reads
// when the route they registered does not appear.
func pluginPaths(t *testing.T, registered plugins) []string {
	t.Helper()

	if registered.ui == nil {
		t.Fatal("no UI registry; there are no page types to mount")
	}

	paths := make([]string, 0, len(registered.ui.PageTypes()))
	for _, pageType := range registered.ui.PageTypes() {
		paths = append(paths, pageType.Path)
	}

	return paths
}

// registerCampaignWithSystem registers one campaign under an explicit system id.
//
// **A parameter rather than a literal**, because the whole test is about a *specific*
// unregistered id and the fixture that registers every campaign at one hardcoded value
// could not express it. Everything else is `registerCampaign`'s behaviour — the registrar
// creates the content root at mode 0700 and writes the absolute `content_root` column —
// so the campaign under test differs from the healthy one in exactly one column, which
// is what makes "the wiki still serves" a statement about the system id.
func (i *instance) registerCampaignWithSystem(slug, systemID string) domain.Campaign {
	i.t.Helper()

	registrar := campaigns.NewRegistrar(i.store, filepath.Join(i.base, "vaults"), nil)

	campaign, err := registrar.Register(i.t.Context(), campaigns.RegisterRequest{
		Slug:           slug,
		Name:           slug,
		SystemID:       systemID,
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPublic,
		OwnerID:        i.owner.ID,
	})
	if err != nil {
		i.t.Fatalf("register campaign %s under system %q: %v", slug, systemID, err)
	}

	i.registered = append(i.registered, campaign)

	return campaign
}

// dispatch sends one intent for a campaign through the hub the composition root built,
// and returns what came back.
//
// **Through `Hub.Dispatch` rather than `Apply`**, for the reason the dice roller's route
// gives: `Dispatch` is the shape a caller with no socket peer behind it uses, so its
// refusal arrives as an *error* carrying the reason. `Apply` answers the peer directly
// and returns `nil`, which would make "the game refused" unassertable from a test.
//
// The actor is the campaign's GM and the frame names a placement that does not exist, so
// the refusal this is reaching for is the **system's** — which is the point: a campaign
// whose system id resolves to nothing is refused before anything on its table is read.
func (i *instance) dispatch(
	t *testing.T,
	campaignID int64,
	op string,
) (realtime.Resolution, error) {
	t.Helper()

	actor, err := realtime.NewActor(campaignID, realtime.UserID(i.owner.ID), domain.RoleGM)
	if err != nil {
		t.Fatalf("building an actor for campaign %d: %v", campaignID, err)
	}

	resolution, err := i.plane.hub.Dispatch(t.Context(), actor, &realtime.ClientIntent{
		Type: realtime.TypeIntent,
		Seq:  1,
		Op:   realtime.Op(op),
		Args: realtime.IntentArgs{Placement: "token_1", Expr: "1d20"},
	})
	if err != nil {
		// **Not wrapped, and that is the one place a `wrapcheck` suppression is right.**
		// The whole subject of the test above is what this error says — §10.8's
		// requirement is that it names the id — so wrapping it here would put a
		// campaign id and a fixture name in front of the text being asserted about and
		// the assertion would be reading the wrapper. Every other error in this file is
		// wrapped; this one is the subject.
		//nolint:wrapcheck // the error's own text is the assertion's subject; see above
		return resolution, err
	}

	return resolution, nil
}

// pageKind reads one page's indexed kind, which is what the nav tree and the search
// results render.
//
// **From the `pages` table rather than from the rendered document**, and the two are
// deliberately different claims: the document proves the *page* degraded, and the row
// proves the *index* agrees. A page that renders as prose while the index says it is a
// game object is two answers, and the nav tree would offer a link to something the page
// will not render.
func (i *instance) pageKind(t *testing.T, path string) domain.PageKind {
	t.Helper()

	page, err := i.store.PageByPath(t.Context(), i.campaign.ID, path)
	if err != nil {
		t.Fatalf("read the indexed page %s: %v", path, err)
	}

	return page.Kind
}

// askOne asks a handler for one path and returns what it answered.
//
// **`httptest.NewServer`-free**, because every handler under test is reachable through
// the router value itself and a real listener would add a dial and a port for no gain.
// The request comes from `get` in `pipeline_test.go`, which builds it on `t.Context()`
// rather than a fresh context — the identity and access middlewares read the request's
// context, and a new one would cut them off from the test's lifetime.
func askOne(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get(t, path))

	return recorder
}

// bodyOf returns a response's body as text, failing the test if the body cannot be read.
//
// The read error is not swallowed into an empty string: a failure to read a body this
// process just wrote means the recorder was used twice or the assertion is reading the
// wrong response, and an empty body would turn every `strings.Contains` below into a
// failure with no cause.
func bodyOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	body, err := io.ReadAll(recorder.Body)
	if err != nil {
		t.Fatalf("read the response body: %v", err)
	}

	return string(body)
}

// truncate shortens a rendered fragment for a failure message.
//
// **Bounded and lossless-at-the-boundary**, because a failure message that prints a whole
// document is a failure nobody reads to the end. Two hundred lines is enough to show a
// missing body and short enough to scroll.
func truncate(body string) string {
	const limit = 200

	lines := strings.Split(body, "\n")
	if len(lines) <= limit {
		return body
	}

	return strings.Join(lines[:limit], "\n") + "\n…"
}

// TestTheBootPassNamesTheCampaignAndTheSystemAnOperatorHasToFix is the other half of
// §10.8's row, and it is a separate test because it is a different audience.
//
// **The per-intent refusal is seen by a browser, not by an operator.** It arrives as
// `server_error` — correct, and deliberately saying nothing, because `plugin.RejectionError`'s
// text is the reason alone (S-12.3). So the "says which ID it wants" requirement needs a
// log line, and nothing produces one without the boot pass: without it a campaign whose
// plugin was removed is indistinguishable from any other `server_error` on the instance.
//
// Four assertions, each of which fails for a different wrong wiring:
//
//   - the event name is `plugin.missing`, one of §13.2's own (ADR 0032) and **not** a
//     new one — a new name would move the count `observability.AllEventNames` asserts
//     and would split one alert into two queries;
//   - the campaign's **slug** is on the line, so an operator knows which campaign;
//   - the system **id** is on the line, so they know what to register;
//   - the line is **one per offending campaign**, and no line appears for a campaign
//     whose system resolves — a pass that logged every campaign would be unreadable,
//     which is how a boot report stops being read.
func TestTheBootPassNamesTheCampaignAndTheSystemAnOperatorHasToFix(t *testing.T) {
	t.Parallel()

	registered, err := registerPlugins(discardLogger())
	if err != nil {
		t.Fatalf("register the plugins: %v", err)
	}

	lines := &logCapture{}
	reportMissingSystems(registered.gameplay, []domain.Campaign{
		// Resolvable: `dnd5e` is what the composition root registers.
		{Slug: "greyhaven", SystemID: registered.engine.ID().String()},
		// Not resolvable, twice, so "one line per offending campaign" is assertable
		// rather than assumed from a single row.
		{Slug: "strangehold", SystemID: unknownSystemID},
		{Slug: "blackgate", SystemID: "pathfinder"},
	}, lines.logger())

	missing := lines.records("plugin.missing")
	if len(missing) != 2 {
		t.Fatalf("the boot pass logged %d plugin.missing line(s), want 2 (one each for "+
			"strangehold and blackgate); a pass that logs every campaign is unreadable, "+
			"and one that logs none leaves §10.8's 'says which ID it wants' unsatisfied: %v",
			len(missing), lines.all())
	}

	for _, want := range []struct{ slug, system string }{
		{slug: "strangehold", system: unknownSystemID},
		{slug: "blackgate", system: "pathfinder"},
	} {
		if !missing.anyLine(func(line logLine) bool {
			return line.attribute("campaign") == want.slug &&
				line.attribute("system") == want.system
		}) {
			t.Errorf("no plugin.missing line names campaign %q with system %q; an "+
				"operator reading the log needs both, and one without the other sends "+
				"them looking at the wrong campaign: %v", want.slug, want.system, lines.all())
		}
	}

	// And the healthy campaign is **absent**, which is the half that makes the other
	// half mean something: a log line on every boot is a log line nobody reads.
	if slices.ContainsFunc(missing, func(line logLine) bool {
		return line.attribute("campaign") == "greyhaven"
	}) {
		t.Errorf("the boot pass reported greyhaven, whose system %q this build resolves; "+
			"a boot report that names healthy campaigns is noise: %v",
			registered.engine.ID(), lines.all())
	}
}

// TestTheBootPassSaysSoOnceWhenThisBuildResolvesNothing is the empty-registry branch,
// and it is the case a deployment reaches by removing a plugin from the build.
//
// **One line, and it is about the binary rather than about any campaign.** A build with
// no gameplay system refuses every campaign, so a per-campaign line would be one
// identical message repeated per row — and the actionable fact is the same for all of
// them. The assertion checks both halves: exactly one line, and no campaign attributes
// on it.
func TestTheBootPassSaysSoOnceWhenThisBuildResolvesNothing(t *testing.T) {
	t.Parallel()

	lines := &logCapture{}
	reportMissingSystems(plugin.New(), []domain.Campaign{
		{Slug: "greyhaven", SystemID: "dnd5e"},
		{Slug: "strangehold", SystemID: "dnd5e"},
	}, lines.logger())

	missing := lines.records("plugin.missing")
	if len(missing) != 1 {
		t.Fatalf("a build with no gameplay systems logged %d plugin.missing line(s), want "+
			"exactly 1; the condition is a property of this binary rather than of any "+
			"campaign, so the actionable fact is the same for all of them: %v",
			len(missing), lines.all())
	}

	if missing[0].attribute("campaign") != "" {
		t.Errorf("the single line names campaign %q, so a build with no systems appears to "+
			"be blaming one campaign for a property of the whole instance: %v",
			missing[0].attribute("campaign"), lines.all())
	}
}

// logLine is one captured log record, as the message and the attributes it carried.
type logLine struct {
	message    string
	attributes map[string]string
}

// attribute returns one attribute's value, or the empty string.
func (l logLine) attribute(name string) string { return l.attributes[name] }

// logLines is a captured set of lines.
type logLines []logLine

// anyLine reports whether any line satisfies the predicate.
func (l logLines) anyLine(about func(logLine) bool) bool {
	return slices.ContainsFunc(l, about)
}

// logCapture collects the lines a `slog` handler is given, and hands out a logger.
//
// **A `slog.Handler` and not a channel or a goroutine**, because the pass under test is
// synchronous and a reader that had to be drained would make every assertion in these
// two tests conditional on scheduling. The handler's `Enabled` accepts every level so a
// pass that logged at debug rather than error would still be caught — a report nobody
// can see is a report nobody reads.
type logCapture struct {
	lines logLines
}

// logger returns a logger writing into this capture.
func (c *logCapture) logger() *slog.Logger {
	return slog.New(&captureHandler{capture: c})
}

// records returns the captured lines whose message is exactly msg.
func (c *logCapture) records(msg string) logLines {
	return c.lines.byMessage(msg)
}

// all returns every captured line, for a failure message.
func (c *logCapture) all() logLines { return c.lines }

// byMessage returns the lines carrying exactly that message.
func (l logLines) byMessage(msg string) logLines {
	var found logLines

	for _, line := range l {
		if line.message == msg {
			found = append(found, line)
		}
	}

	return found
}

// captureHandler is a `slog.Handler` that appends to a `logCapture`.
//
// **`Enabled` accepts everything**, deliberately: the two tests above assert on lines the
// passes write at `Error`, and a handler that filtered by level would hide a pass that
// had been demoted to `Info` — which is a real regression in a boot report, because an
// operator watching errors would then see nothing.
type captureHandler struct {
	capture *logCapture
	attrs   []slog.Attr
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(
	_ context.Context,
	record slog.Record,
) error {
	attributes := make(map[string]string, record.NumAttrs()+len(h.attrs))

	for _, attr := range h.attrs {
		attributes[attr.Key] = attr.Value.String()
	}

	record.Attrs(func(attr slog.Attr) bool {
		attributes[attr.Key] = attr.Value.String()

		return true
	})

	h.capture.lines = append(h.capture.lines, logLine{
		message:    record.Message,
		attributes: attributes,
	})

	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Copied rather than appended to the receiver's slice: `slog` reuses a handler
	// across `With` calls, so appending in place would make one call's attributes leak
	// into the next — a capture that reported attributes no log line carried.
	return &captureHandler{capture: h.capture, attrs: append(slices.Clone(h.attrs), attrs...)}
}

func (h *captureHandler) WithGroup(string) slog.Handler { return h }
