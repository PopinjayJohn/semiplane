package webplugins_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
)

// The fixtures below are the two reference plugins of §10.6's worked-consequences
// table, in the shape this tier declares them and **not** implemented: a link
// preview and a graphical dice roller. They are here so the tier is exercised by
// the shapes P2c will build rather than by shapes invented for a test.

// diceRoller is §10.6's dice roller, declared.
//
// `Emits: []rules.Op{"roll"}` is the whole of §10.6's "on send, emits
// `{"op":"roll"}`" turned into something checkable, and it is the row the
// registration-time op check exists for.
func diceRoller() webplugins.Declaration {
	return webplugins.Declaration{
		Name:  "dice-roller",
		Title: "Graphical dice roller",
		Observers: []webplugins.Observer{{
			Name:  "dice-roller.chat",
			Query: rules.Query{View: "roll-recap"},
			Render: func(rules.Payload) templ.Component {
				return nil
			},
		}},
		Emits: []rules.Op{"roll"},
	}
}

// linkPreview is §10.6's link preview, declared: a render hook with no kind, which is
// the shape that makes `RenderHook.Kind` optional.
func linkPreview() webplugins.Declaration {
	return webplugins.Declaration{
		Name:  "link-preview",
		Title: "Link preview",
		RenderHooks: []webplugins.RenderHook{{
			Name: "link-preview.external",
			Render: func(string, rules.Payload) templ.Component {
				return nil
			},
		}},
		PageTypes: []webplugins.PageType{{
			Kind:   "link_preview",
			Title:  "Link preview",
			Path:   "/c/{slug}/plugins/link-preview",
			Render: func(rules.Payload) templ.Component { return nil },
		}},
	}
}

// systemsWith returns a gameplay registry holding one system that resolves `roll`.
//
// `roll` is the only operation, and it is the *only* one because
// `TestAUIPluginCannotEmitAnOperationNoSystemResolves` is about the refusals and a
// system resolving more would make the refusals unreachable.
func systemsWith(t *testing.T) *plugin.Registry {
	t.Helper()

	registry := plugin.New()

	if err := registry.Register(plugin.Entry{
		System: rollingSystem{},
		Codec:  plugin.PlacementCodec{},
	}); err != nil {
		t.Fatalf("registering the fixture system: %v", err)
	}

	return registry
}

// rollingSystem is a gameplay system whose only operation is `roll`, which is the
// minimum a `plugin.Resolves` answer needs.
type rollingSystem struct{}

// The nine methods of rules.System. Each is the smallest legal answer, because this
// fixture is here for `plugin.Operations` and nothing else — the conformance suite is
// P1c's, and a fixture that pretended to be a rules engine would be tested by a
// suite it was never written for.
func (rollingSystem) ID() rules.ID { return "fixture" }

func (rollingSystem) Title() string { return "Fixture system" }

func (rollingSystem) RulesetVersion() string { return "fixture-1" }

func (rollingSystem) Grammar() rules.Grammar {
	return rules.Grammar{
		Notation: "d20",
		Terms:    []rules.Term{{Name: "roll", Pattern: `^[0-9]+d[0-9]+$`}},
	}
}

func (rollingSystem) Parse(string) (rules.Expr, error) {
	return rules.NewExpr("fixture", "d20", "1d20", nil), nil
}

func (rollingSystem) Apply(
	context.Context, rules.Context, rules.State, rules.Intent,
) ([]rules.Mutation, error) {
	return nil, nil
}

func (rollingSystem) Derive(rules.State, rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload("roll-recap", struct{}{})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("the fixture could not build a payload: %w", err)
	}

	return payload, nil
}

func (rollingSystem) Views() []rules.View { return nil }

func (rollingSystem) ContentKinds() []rules.Kind { return []rules.Kind{"spell"} }

// Resolves implements plugin.Operations: the only op this system answers is `roll`.
func (rollingSystem) Resolves(operation rules.Op) bool { return operation == "roll" }

// AReader is a `Reader` built over one reader and one campaign.
//
// It is here to be **proved insufficient**, which is the point: `TestTheReaderAUIPluginIsGivenCannotWriteState` reflects over the interface and requires that no method in it returns an error or takes a non-query argument that could carry a write. A test that only asserted "the interface looks small" would pass on an interface one method away from dangerous.
type AReader struct{}

// Campaign implements webplugins.Reader.
func (AReader) Campaign() int64 { return 42 }

// Role implements webplugins.Reader.
func (AReader) Role() domain.Role { return domain.RolePlayer }

// View implements webplugins.Reader.
func (AReader) View(rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload("roll-recap", struct{}{})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("the fixture reader could not build a payload: %w", err)
	}

	return payload, nil
}
