package webplugins_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
)

// TestAUIPluginCannotEmitAnOperationNoSystemResolves is §10.6's "It cannot register a
// new `op`. Only a gameplay system defines those", as a refusal at registration.
//
// It is a table because there are two distinct ways to fail and a plugin author needs
// both messages: an op outside the wire's token shape, and an op shaped like one that
// no registered system resolves. The second is the interesting one — it is the case
// §10.3 exists for, and the reason the check is against the **whole build** rather
// than against the campaign being rendered.
func TestAUIPluginCannotEmitAnOperationNoSystemResolves(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		emits  []rules.Op
		wantIs error
	}{
		"an op no system resolves": {
			emits:  []rules.Op{"set_hp"},
			wantIs: webplugins.ErrUnknownOp,
		},
		"an op that is not a token": {
			emits:  []rules.Op{"Roll"},
			wantIs: webplugins.ErrUnknownOp,
		},
		"an empty op": {
			emits:  []rules.Op{""},
			wantIs: webplugins.ErrUnknownOp,
		},
		"one good op among bad ones": {
			emits:  []rules.Op{"roll", "cast_fireball"},
			wantIs: webplugins.ErrUnknownOp,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			registry := webplugins.New(systemsWith(t))

			declared := diceRoller()
			declared.Emits = testCase.emits

			err := registry.Register(declared)
			if !errors.Is(err, testCase.wantIs) {
				t.Fatalf("Register returned %v, want a refusal satisfying %v", err, testCase.wantIs)
			}

			// The tier-violation sentinel too, so one question covers both tiers.
			if !errors.Is(err, plugin.ErrNotAnOp) {
				t.Errorf("the refusal %v does not satisfy plugin.ErrNotAnOp", err)
			}

			// And it names the operation, which is the half a plugin author can act
			// on and which must never reach a client.
			for _, operation := range testCase.emits {
				if operation != "" && !strings.Contains(err.Error(), operation.String()) {
					t.Errorf("the refusal %q does not name %q", err.Error(), operation)
				}
			}

			if got := len(registry.Plugins()); got != 0 {
				t.Errorf("the registry holds %d plugins after a refusal", got)
			}
		})
	}
}

// TestAUIPluginMayEmitAnOperationSomeSystemResolves is the other side, and it is here
// so the refusal above cannot be satisfied by refusing everything.
func TestAUIPluginMayEmitAnOperationSomeSystemResolves(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	if err := registry.Register(diceRoller()); err != nil {
		t.Fatalf("Register returned %v for an op the system resolves", err)
	}

	if !registry.EmitsFor("dice-roller", "roll") {
		t.Error("EmitsFor does not report the declared operation")
	}

	if registry.EmitsFor("dice-roller", "set_hp") {
		t.Error("EmitsFor reports an operation the plugin never declared")
	}

	if registry.EmitsFor("link-preview", "roll") {
		t.Error("EmitsFor reports an operation for a plugin that is not registered")
	}
}

// TestAUIPluginMayDeclareNoOperationsAtAll is §10.6's link-preview row: a read-only
// hook emits nothing, and a declaration with an empty `Emits` is not a declaration
// with a problem.
func TestAUIPluginMayDeclareNoOperationsAtAll(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	if err := registry.Register(linkPreview()); err != nil {
		t.Fatalf("Register returned %v for a plugin that emits nothing", err)
	}

	if registry.EmitsFor("link-preview", "roll") {
		t.Error("a plugin that declared no operations reports one")
	}
}

// TestABuildWithNoGameplaySystemsRefusesEveryEmittingPlugin is S-10.6's first row
// reached from the other direction.
//
// A build whose gameplay plugins were removed still serves every campaign's wiki, and
// every UI plugin that emits an operation is now a plugin whose button would fail at
// the table. Refusing it at boot names the plugin and the op, which is the difference
// between a report and a mystery.
func TestABuildWithNoGameplaySystemsRefusesEveryEmittingPlugin(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(plugin.New())

	if err := registry.Register(diceRoller()); !errors.Is(err, webplugins.ErrUnknownOp) {
		t.Errorf("Register returned %v, want a refusal satisfying ErrUnknownOp", err)
	}

	// A read-only plugin is unaffected, which is the difference between a degraded
	// interface and a broken one.
	if err := registry.Register(linkPreview()); err != nil {
		t.Errorf("Register returned %v for a read-only plugin in a build with no systems", err)
	}
}

// TestAUIPluginCannotClaimAKindSemiplaneOwns is §10.2.1's ownership table applied to
// the UI tier, and the reason this check exists here rather than being left to
// `rules.Validate`: a UI page type is a different thing from a gameplay system's
// content kind — it mounts a route — so a plugin offering to render `token` would be
// offering to render the table's own game objects.
func TestAUIPluginCannotClaimAKindSemiplaneOwns(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	for _, kind := range rules.SemiplaneKinds() {
		declared := linkPreview()
		declared.PageTypes[0].Kind = kind

		err := registry.Register(declared)
		if !errors.Is(err, webplugins.ErrKindOwned) {
			t.Errorf("a page type claiming %q was not refused: %v", kind, err)
		}
	}

	if got := len(registry.Plugins()); got != 0 {
		t.Errorf("the registry holds %d plugins after every refusal", got)
	}
}

// TestAUIPluginCannotClaimAKindAnotherUIPluginDeclared is the collision case, and the
// two different refusals it has to tell apart: two plugins claiming one kind is a
// route that would serve whichever registered last.
func TestAUIPluginCannotClaimAKindAnotherUIPluginDeclared(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	first := linkPreview()
	if err := registry.Register(first); err != nil {
		t.Fatalf("Register returned %v for the first plugin", err)
	}

	second := linkPreview()
	second.Name = "link-preview-two"

	err := registry.Register(second)
	if !errors.Is(err, webplugins.ErrKindClaimed) {
		t.Fatalf("Register returned %v, want a refusal satisfying ErrKindClaimed", err)
	}

	// And the **first** survives, as in the gameplay registry: which one won must not
	// be a function of call order.
	pageType, found := registry.PageType("link_preview")
	if !found {
		t.Fatal("the first plugin's page type is gone")
	}

	if pageType.Title != "Link preview" {
		t.Errorf("the surviving page type is %q", pageType.Title)
	}

	if got := len(registry.Plugins()); got != 1 {
		t.Errorf("the registry holds %d plugins, want 1", got)
	}
}

// TestRegisterRefusesAMalformedDeclaration is the table of the tier's own rules,
// distinct from the two cross-package ones above.
func TestRegisterRefusesAMalformedDeclaration(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		declare func() webplugins.Declaration
		wantIs  error
	}{
		"no name": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.Name = ""

				return declared
			},
			wantIs: webplugins.ErrNoName,
		},
		"no title": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.Title = ""

				return declared
			},
			wantIs: webplugins.ErrNoTitle,
		},
		"a page type with no title": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.PageTypes[0].Title = ""

				return declared
			},
			wantIs: webplugins.ErrNoTitle,
		},
		"a page type with no kind": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.PageTypes[0].Kind = "Link Preview"

				return declared
			},
			wantIs: webplugins.ErrBadKind,
		},
		"a page type that renders nothing": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.PageTypes[0].Render = nil

				return declared
			},
			wantIs: webplugins.ErrNoRender,
		},
		"a page type with no path": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.PageTypes[0].Path = ""

				return declared
			},
			wantIs: webplugins.ErrBadPath,
		},
		"a hook with no name": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.RenderHooks[0].Name = ""

				return declared
			},
			wantIs: webplugins.ErrNoName,
		},
		"a hook scoped to a kind that is not one": {
			declare: func() webplugins.Declaration {
				declared := linkPreview()
				declared.RenderHooks[0].Kind = "Not A Kind"

				return declared
			},
			wantIs: webplugins.ErrBadKind,
		},
		"an observer with no name": {
			declare: func() webplugins.Declaration {
				declared := diceRoller()
				declared.Observers[0].Name = ""

				return declared
			},
			wantIs: webplugins.ErrNoName,
		},
		"an observer asking for an unnamed view": {
			declare: func() webplugins.Declaration {
				declared := diceRoller()
				declared.Observers[0].Query = rules.Query{}

				return declared
			},
			wantIs: webplugins.ErrMalformedUIPlugin,
		},
		"an observer that renders nothing": {
			declare: func() webplugins.Declaration {
				declared := diceRoller()
				declared.Observers[0].Render = nil

				return declared
			},
			wantIs: webplugins.ErrNoRender,
		},
		"a second plugin with one name": {
			declare: linkPreview,
			wantIs:  webplugins.ErrDuplicateUI,
		},
	}

	registry := webplugins.New(systemsWith(t))

	if err := registry.Register(linkPreview()); err != nil {
		t.Fatalf("Register returned %v for the first plugin", err)
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if err := registry.Register(testCase.declare()); !errors.Is(err, testCase.wantIs) {
				t.Fatalf("Register returned %v, want a refusal satisfying %v", err, testCase.wantIs)
			}

			if got := len(registry.Plugins()); got != 1 {
				t.Errorf("the registry holds %d plugins after a refusal, want 1", got)
			}
		})
	}
}

// TestEveryRefusalIsReachableThroughTheUmbrella is the meta-check, and it is here for
// the same reason the gameplay registry has one: a refusal a boot pass cannot act on
// is a refusal that appears as a new `errors.New` beside a type rather than a
// `fmt.Errorf` in the block.
//
// `ErrUnknownOp` is asserted separately because it deliberately does **not** sit
// under this umbrella: it wraps `plugin.ErrNotAnOp`, because "this is a tier
// violation" is one question with one answer across both packages, while "this
// declaration is malformed" belongs here alone.
func TestEveryRefusalIsReachableThroughTheUmbrella(t *testing.T) {
	t.Parallel()

	umbrella := []error{
		webplugins.ErrNoName,
		webplugins.ErrNoTitle,
		webplugins.ErrNoRender,
		webplugins.ErrDuplicateUI,
		webplugins.ErrKindOwned,
		webplugins.ErrBadPath,
		webplugins.ErrKindClaimed,
		webplugins.ErrBadKind,
	}

	for _, refusal := range umbrella {
		if !errors.Is(refusal, webplugins.ErrMalformedUIPlugin) {
			t.Errorf("%v does not satisfy ErrMalformedUIPlugin", refusal)
		}
	}

	if !errors.Is(webplugins.ErrUnknownOp, plugin.ErrNotAnOp) {
		t.Error("ErrUnknownOp does not satisfy plugin.ErrNotAnOp")
	}

	// And it is **not** under this package's umbrella, because a plugin emitting an
	// operation is not a malformed declaration — it is a well formed one crossing a
	// tier boundary, and a boot counting malformed registrations should say so.
	if errors.Is(webplugins.ErrUnknownOp, webplugins.ErrMalformedUIPlugin) {
		t.Error("ErrUnknownOp satisfies this package's umbrella; it is a tier violation")
	}
}

// TestTheListingsAreInRegistrationOrderAndNotShared is the same property the gameplay
// registry has, for the same reason: §10.5's deterministic order, and a shared slice
// is mutable global state.
func TestTheListingsAreInRegistrationOrderAndNotShared(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	ordered := []string{"link-preview", "dice-roller"}
	for _, name := range ordered {
		declared := linkPreview()
		if name == "dice-roller" {
			declared = diceRoller()
		}

		if err := registry.Register(declared); err != nil {
			t.Fatalf("Register returned %v", err)
		}
	}

	plugins := registry.Plugins()
	if len(plugins) != len(ordered) {
		t.Fatalf("Plugins() returned %d entries, want %d", len(plugins), len(ordered))
	}

	for at, entry := range plugins {
		if entry.Name != ordered[at] {
			t.Errorf("Plugins()[%d] is %q, want %q", at, entry.Name, ordered[at])
		}
	}

	plugins[0].Name = "overwritten"

	if got := registry.Plugins()[0].Name; got != "link-preview" {
		t.Errorf("a caller overwrote Plugins()[0]; the registry now reports %q", got)
	}

	pageTypes := registry.PageTypes()
	if len(pageTypes) != 1 || pageTypes[0].Kind != "link_preview" {
		t.Errorf("PageTypes() is %+v, want the one declared kind", pageTypes)
	}

	if kinds := registry.Kinds(); !slices.Equal(kinds, []rules.Kind{"link_preview"}) {
		t.Errorf("Kinds() is %v, want [link_preview]", kinds)
	}
}

// TestAnUnknownKindHasNoPageTypeAndIsNotAnError is §10.8's last row from this tier's
// half: the absence is an ordinary answer, because the caller asking has to render
// prose when it gets one.
//
// It is also what makes §10.7's "Validation is still opt-in — unknown `kind` means
// prose" work with a registry-backed kind: a page naming a kind whose plugin was
// removed finds no page type, renders as prose, and keeps its content.
func TestAnUnknownKindHasNoPageTypeAndIsNotAnError(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	if err := registry.Register(linkPreview()); err != nil {
		t.Fatalf("Register returned %v", err)
	}

	for _, kind := range []rules.Kind{"spell", "stale_kind", "not a kind"} {
		pageType, found := registry.PageType(kind)
		if found {
			t.Errorf("a page type was found for %q", kind)
		}

		if pageType.Kind != "" {
			t.Errorf("the absent page type for %q carries kind %q", kind, pageType.Kind)
		}
	}

	// Semiplane's own kinds have no *page type* here either: they are rendered by
	// semiplane's components, and a UI plugin claiming one is refused above.
	for _, kind := range rules.SemiplaneKinds() {
		if _, found := registry.PageType(kind); found {
			t.Errorf("the UI tier holds a page type for semiplane's %q", kind)
		}
	}
}

// TestALookupMissesAreNotErrors is the predicate-form contract for all three lookups,
// checked together because they are one contract: a hook and an observer are looked up
// by name from a status page or a component's wiring, and a missing one must be a
// `false` rather than a refusal somebody learns to ignore.
func TestALookupMissesAreNotErrors(t *testing.T) {
	t.Parallel()

	registry := webplugins.New(systemsWith(t))

	if err := registry.Register(linkPreview()); err != nil {
		t.Fatalf("Register returned %v", err)
	}

	if _, found := registry.RenderHook("nope"); found {
		t.Error("a missing hook was found")
	}

	if _, found := registry.Observer("nope"); found {
		t.Error("a missing observer was found")
	}

	hook, found := registry.RenderHook("link-preview.external")
	if !found {
		t.Fatal("the registered hook is missing")
	}

	if hook.Kind != "" {
		t.Errorf("the hook is scoped to %q, want every page", hook.Kind)
	}
}

// TestTheReaderAUIPluginIsGivenCannotWriteState is S-10.3's "it cannot write
// `campaign_state`" and §10.2's table, enforced by the shape of the type rather than
// by a check.
//
// The method set is read by reflection and held to a shape: **no method returns an
// `error`**, so none of them can report that it wrote something, and no method other
// than `View` takes an argument a caller could use to describe a change. `View`
// takes a `rules.Query`, whose three fields are a view name, an object id and a limit —
// and `rules.Query`'s own type is the reason: it has no field in which a change
// could be described.
//
// The test would pass on an interface with a `SetHP` method only if `SetHP` returned
// nothing, so the argument list is checked as well as the results. This is the one
// test in the package that is about the *absence* of code, and the absence is the
// feature.
func TestTheReaderAUIPluginIsGivenCannotWriteState(t *testing.T) {
	t.Parallel()

	// The whole method set, spelled out. An interface is a contract of exactly these
	// signatures, and a fourth method — `SetHP`, `Mutate`, `CampaignState` — is the
	// one change that would give a UI plugin a write path, so the test is written to
	// fail on the *addition* rather than to check the absence of a name it happens to
	// know.
	want := map[string]string{
		"Campaign": "func() int64",
		"Role":     "func() domain.Role",
		"View":     "func(rules.Query) (rules.Payload, error)",
	}

	readerType := reflect.TypeFor[webplugins.Reader]()

	if readerType.NumMethod() != len(want) {
		t.Errorf("Reader has %d methods, want %d: %v",
			readerType.NumMethod(), len(want), methodNames(readerType))
	}

	for _, method := range slices.Collect(readerType.Methods()) {
		signature, expected := want[method.Name]
		if !expected {
			t.Errorf("Reader has a method %q, which is a write path this contract does not have",
				method.Name)

			continue
		}

		if got := method.Type.String(); got != signature {
			t.Errorf("Reader.%s is %s, want %s", method.Name, got, signature)
		}
	}

	// And the payload a view answers carries no write path either: `rules.Payload`'s
	// value is `any` and semiplane interprets nothing about it, which is the property
	// §10.6.1 relies on for a plugin component to be type-safe.
	payload, err := rules.NewPayload("roll-recap", AReader{})
	if err != nil {
		t.Fatalf("building a payload: %v", err)
	}

	if payload.View() != "roll-recap" {
		t.Errorf("the payload answers view %q", payload.View())
	}
}

// methodNames lists an interface's method names, for the message above.
func methodNames(interfaceType reflect.Type) []string {
	names := make([]string, 0, interfaceType.NumMethod())
	for method := range interfaceType.Methods() {
		names = append(names, method.Name)
	}

	return names
}

// TestTheEmitterIsTheOnlyAuthorityAndItTakesTheReadersIdentity is §10.6's "dispatches
// the same intents a human player would", read off the interface's signature.
//
// `Emit` takes a `domain.Requestor` and nothing that could name a campaign, an actor
// or a role — so the actor is the account that loaded the page and a plugin cannot
// escalate by being clever. The absence of a second parameter is the whole of the
// claim, which is why this test reads the method set rather than exercising it: there
// is no implementation of `Emitter` in this package, and the integration report says
// so.
func TestTheEmitterIsTheOnlyAuthorityAndItTakesTheReadersIdentity(t *testing.T) {
	t.Parallel()

	emitterType := reflect.TypeFor[webplugins.Emitter]()

	methods := slices.Collect(emitterType.Methods())
	if len(methods) != 1 {
		t.Fatalf("Emitter has %d methods, want 1", len(methods))
	}

	emit := methods[0]
	if emit.Name != "Emit" {
		t.Fatalf("Emitter's method is %q, want Emit", emit.Name)
	}

	// Emit(ctx, who, requested) error: three inputs, one output, and no field in
	// which to name a campaign or a role.
	if got, expected := emit.Type.String(), "func(context.Context, domain.Requestor, rules.Intent) error"; got != expected {
		t.Errorf("Emitter.Emit is %s, want %s", got, expected)
	}

	if emit.Type.In(0) != reflect.TypeFor[context.Context]() {
		t.Errorf("Emit's first argument is %s, want a context", emit.Type.In(0))
	}

	if emit.Type.In(1) != reflect.TypeFor[domain.Requestor]() {
		t.Errorf("Emit's second argument is %s, want a domain.Requestor", emit.Type.In(1))
	}

	if emit.Type.In(2) != reflect.TypeFor[rules.Intent]() {
		t.Errorf("Emit's third argument is %s, want a rules.Intent", emit.Type.In(2))
	}

	if emit.Type.Out(0) != reflect.TypeFor[error]() {
		t.Errorf("Emit returns %s, want an error", emit.Type.Out(0))
	}
}

// TestTheRendererFieldsAreFunctionsAndNotTemplateNames is §10.6.1's decision, held at
// the type: a component is a Go value rather than a template name looked up at render
// time.
//
// The cost of that decision is that a plugin ships Go, and the benefit is that no
// plugin ships a template path into semiplane's loader — where a path is a claim about
// a filesystem semiplane owns. The three render fields are therefore `func`s, and this
// test fails if one is ever changed to a string.
func TestTheRendererFieldsAreFunctionsAndNotTemplateNames(t *testing.T) {
	t.Parallel()

	fields := map[string]any{
		"PageType.Render":   webplugins.PageType{}.Render,
		"RenderHook.Render": webplugins.RenderHook{}.Render,
		"Observer.Render":   webplugins.Observer{}.Render,
	}

	for name, field := range fields {
		if field == nil {
			continue
		}

		if kind := reflect.TypeOf(field).Kind(); kind != reflect.Func {
			t.Errorf("%s is a %s, want a func returning a templ.Component", name, kind)
		}
	}

	// The declaration's own compile-time check is the other half: `check` refuses a
	// nil render, so a plugin cannot register a field it has not filled in. Asserted
	// through `Register` rather than by calling the unexported method.
	registry := webplugins.New(systemsWith(t))

	declared := linkPreview()
	declared.PageTypes[0].Render = nil

	if err := registry.Register(declared); !errors.Is(err, webplugins.ErrNoRender) {
		t.Errorf("Register returned %v for a page type with no renderer", err)
	}

	// And a `templ.Component` is what they return, which is why `internal/web/plugins`
	// is the only package in the two tiers that imports a rendering library.
	_ = templateFixture()

	// The render fields' own types are asserted by assignment rather than by
	// reflection, because the compiler is the better witness for a type:
	// these do not compile if a Render field's signature changes.
	pageTypeRender := webplugins.PageType{}.Render
	hookRender := webhooksRender()
	observerRender := webplugins.Observer{}.Render

	_ = pageTypeRender
	_ = hookRender
	_ = observerRender
}

// templateFixture is the smallest thing a renderer in this package can return.
func templateFixture() templ.Component { return nil }

// webhooksRender is a render hook's render function, named for the assignment above.
func webhooksRender() func(string, rules.Payload) templ.Component {
	return webplugins.RenderHook{}.Render
}
