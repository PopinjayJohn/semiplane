package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// TestAViewNamesItselfATitleAndOneRenderer is the §10.6 contract: each declared view
// is *either* backed by a component the plugin ships *or* served by a built-in
// renderer, and a view that could be both would have two renderers and no way to
// choose.
func TestAViewNamesItselfATitleAndOneRenderer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		view    rules.View
		wantErr error
	}{
		{
			name: "a built-in stat block",
			view: rules.View{
				Name:     "creature",
				Title:    "Creature",
				Renderer: rules.RendererGeneric,
				Shape:    rules.ShapeStatBlock,
			},
		},
		{
			name: "a built-in key/value summary",
			view: rules.View{
				Name:     "dc-summary",
				Title:    "DC summary",
				Renderer: rules.RendererGeneric,
				Shape:    rules.ShapeKeyValue,
			},
		},
		{
			name: "a built-in list",
			view: rules.View{
				Name:     "spell-list",
				Title:    "Spell list",
				Renderer: rules.RendererGeneric,
				Shape:    rules.ShapeList,
			},
		},
		{
			name: "a built-in tag cloud",
			view: rules.View{
				Name:     "conditions",
				Title:    "Conditions",
				Renderer: rules.RendererGeneric,
				Shape:    rules.ShapeTagCloud,
			},
		},
		{
			name: "a component the plugin ships",
			view: rules.View{
				Name:     "character-sheet",
				Title:    "Character sheet",
				Renderer: rules.RendererPlugin,
			},
		},
		{
			name: "a hyphenated name",
			view: rules.View{
				Name:     "spell-list",
				Title:    "Spell list",
				Renderer: rules.RendererGeneric,
				Shape:    rules.ShapeList,
			},
		},
		{
			name:    "no name",
			view:    rules.View{Title: "Creature", Renderer: rules.RendererPlugin},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a name with a space",
			view: rules.View{
				Name:     "character sheet",
				Title:    "Sheet",
				Renderer: rules.RendererPlugin,
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "a name with an uppercase character",
			view:    rules.View{Name: "Sheet", Title: "Sheet", Renderer: rules.RendererPlugin},
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "no title",
			view:    rules.View{Name: "sheet", Renderer: rules.RendererPlugin},
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "no renderer",
			view:    rules.View{Name: "sheet", Title: "Sheet"},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a renderer that is neither of the two",
			view: rules.View{
				Name:     "sheet",
				Title:    "Sheet",
				Renderer: "datastar",
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a plugin component that also names a built-in shape",
			view: rules.View{
				Name:     "sheet",
				Title:    "Sheet",
				Renderer: rules.RendererPlugin,
				Shape:    rules.ShapeStatBlock,
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a built-in view with no shape",
			view: rules.View{
				Name:     "sheet",
				Title:    "Sheet",
				Renderer: rules.RendererGeneric,
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a built-in view naming a shape that does not exist",
			view: rules.View{
				Name:     "sheet",
				Title:    "Sheet",
				Renderer: rules.RendererGeneric,
				Shape:    "dossier",
			},
			wantErr: rules.ErrInvalidView,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := testCase.view.Check()

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("want %v, got %v", testCase.wantErr, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("a valid view was refused: %v", err)
			}
		})
	}
}

// TestAViewIsSilentAboutWhatItDoesNotDeclare, so a status page and a log line can
// print a view without inventing a shape for one that has none.
func TestAViewIsSilentAboutWhatItDoesNotDeclare(t *testing.T) {
	t.Parallel()

	generic := rules.View{
		Name:     "creature",
		Title:    "Creature",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeStatBlock,
	}

	if generic.String() != "generic/stat_block" {
		t.Errorf("a generic view printed %q", generic)
	}

	plugin := rules.View{
		Name:     "character-sheet",
		Title:    "Character sheet",
		Renderer: rules.RendererPlugin,
	}

	if plugin.String() != "plugin" {
		t.Errorf("a plugin view printed %q", plugin)
	}

	// An undeclared value prints as itself rather than as nothing, because "this view
	// is rendered by ``" is not a line anyone can act on.
	stranger := rules.View{Name: "sheet", Title: "Sheet", Renderer: "datastar"}

	if stranger.String() != "datastar" {
		t.Errorf("an undeclared renderer printed %q", stranger)
	}

	if rules.RendererGeneric.String() != "generic" {
		t.Error("Renderer.String did not return the stored text")
	}
}

// TestEveryViewASystemDeclaresIsChecked is the registration-time half of §10.6: a
// system's whole list is walked, so a system that is fine except for its third view
// is refused rather than half-registered.
func TestEveryViewASystemDeclaresIsChecked(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	system.views = []rules.View{
		{
			Name:     "creature",
			Title:    "Creature",
			Renderer: rules.RendererGeneric,
			Shape:    rules.ShapeStatBlock,
		},
		{
			Name:     "spell-list",
			Title:    "Spell list",
			Renderer: rules.RendererGeneric,
			Shape:    rules.ShapeList,
		},
		{Name: "third", Title: "Third", Renderer: rules.RendererGeneric, Shape: "dossier"},
	}

	err := rules.Validate(system)
	if !errors.Is(err, rules.ErrInvalidView) {
		t.Fatalf("a bad third view was not caught: %v", err)
	}

	// The refusal names the view, which is the actionable half.
	if want := `view "third"`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name the view: %s", err)
	}
}
