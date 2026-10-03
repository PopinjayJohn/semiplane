package rules

import "fmt"

// Renderer says which of §10.6's two cases a view is.
//
// Two values and not an open string, because the two cases are **mutually exclusive
// authorities**: one hands HTML to semiplane's layout, the other hands a value to a
// plugin's templ component, and a view that could be both would have two renderers
// and no way to choose. An unrecognised value is refused by `View.Check` rather than
// defaulted, because the default answer to "how should this render?" for a view that
// does not say is *not* — a system whose view list omits the answer has a bug that
// should be a startup failure, not a page that renders with the wrong shape.
type Renderer string

const (
	// RendererPlugin is a view backed by a templ component the plugin ships, which
	// the plugin registers with the UI tier under this view's name.
	//
	// The component is **not** named by a string here and this package does not
	// import templ. That is the whole reason server-side rendering is viable: the
	// domain half of the contract names a view, and the half that owns HTML lives in
	// `internal/web/plugins` where a component is a Go value rather than a template
	// name looked up in a registry at render time. A plugin that wants a rich sheet
	// ships Go; a plugin that wants nothing ships data (see RendererGeneric).
	RendererPlugin Renderer = "plugin"

	// RendererGeneric is a view served by one of semiplane's built-in renderers,
	// selected by the view's `Shape`.
	//
	// The point of this case is that a simple plugin ships data and zero UI code.
	// §10.6.1 calls the shapes a stat block, key/value, list and tag cloud; they are
	// semiplane's components, so a system that needs none of them pays nothing and a
	// system that needs one of them does not have to write a templ file to have a
	// readable page.
	RendererGeneric Renderer = "generic"
)

// Shape names which built-in renderer serves a generic view.
//
// A closed set, and it is closed **here** rather than in the UI tier for the same
// reason `View` is a value in this package at all: the renderer is chosen by a
// system's declaration, so a system has to be able to name it. An unknown shape is
// refused rather than falling back to key/value, because a stat block rendered as
// key/value is a document nobody can read and nothing says it went wrong.
type Shape string

const (
	// ShapeStatBlock is a named block of fields with headings: a creature, an item.
	ShapeStatBlock Shape = "stat_block"

	// ShapeKeyValue is a flat list of labelled values: a summary, a DC breakdown.
	ShapeKeyValue Shape = "key_value"

	// ShapeList is an ordered collection of entries, each of which the renderer may
	// summarise: a spell list, an inventory.
	ShapeList Shape = "list"

	// ShapeTagCloud is a set of labels with weights: conditions, tags, affiliations.
	ShapeTagCloud Shape = "tag_cloud"
)

// View is one thing a system wants rendered, and how.
//
// Declared by a system (`System.Views`) and honoured by the UI tier. It is **not** a
// route, a template path or a permission: the hub decides whether the reader may see
// the thing (S-8, §10.6's "read access to state is filtered by the viewer's campaign
// visibility, exactly as the wiki is"), and a view says nothing about that. A view is
// a claim about *shape*, from a system, to the renderer.
//
// The two cases of §10.6 are one type with a discriminator rather than two types,
// because the query side cannot know which it has: `Query.View` is a name, and the
// name is what picks the renderer. Splitting them would put a type switch in the
// hub's request path whose only job is to learn something `Renderer` says.
type View struct {
	// Name identifies the view within its system, e.g. `character-sheet`. It is what
	// a `Query.View` names and what a plugin registers its component under, and it
	// is held to the same rule as a `Kind` by `Check`.
	Name string

	// Title is the name shown to a person: what a GM sees in the list of what this
	// system can render. Never the empty string, because a view with no label is a
	// blank entry in a menu.
	Title string

	// Renderer is which of the two cases this is.
	Renderer Renderer

	// Shape is which built-in renderer serves it, and it is **required for
	// RendererGeneric and forbidden for RendererPlugin**.
	//
	// Both directions are the rule, not tidiness. A generic view with no shape names
	// no renderer; a plugin view with a shape carries a second answer to "who
	// renders this", and the one that lost would be whichever the hub consulted
	// second. §14's "every declared view renders for an empty, a typical and a maximal
	// output" is enforced against a specific renderer, and a view that names two has
	// no such thing as a maximal output.
	Shape Shape
}

// Check reports whether the view names something the UI tier could render.
//
// Returns an error rather than a predicate because the refusals are three distinct
// sentences a plugin author needs — no name, an unrenderable name, or a renderer and
// a shape that disagree — and flattening them to one `false` would make the most
// common mistake (a shape on a plugin view) the hardest to diagnose.
func (v View) Check() error {
	if v.Name == "" {
		return fmt.Errorf("%w: a view has no name, so no query can ask for it", ErrInvalidView)
	}

	if !Kind(v.Name).Valid() {
		// The *kind* rule rather than the op rule, and the difference is real. An op
		// is a token on the wire, where `protocol.go` fixes the character set and this
		// package mirrors it exactly. A view name is a lookup key that reaches a route
		// and a component registry and never goes on the wire, so it takes the rule
		// this package already argues for in `Kind.Valid` — lowercase, digits,
		// hyphens and underscores, no doubled or edge separator — rather than being
		// spelled `character_sheet` because a wire constraint does not apply to it.
		return fmt.Errorf("%w: %q is not a usable view name", ErrInvalidView, v.Name)
	}

	if v.Title == "" {
		return fmt.Errorf("%w: view %q has no title", ErrInvalidView, v.Name)
	}

	switch v.Renderer {
	case RendererPlugin:
		if v.Shape != "" {
			return fmt.Errorf(
				"%w: view %q names a built-in shape but ships its own component; "+
					"one of the two decides, and the other must be empty",
				ErrInvalidView, v.Name,
			)
		}

		return nil
	case RendererGeneric:
		if !v.Shape.valid() {
			return fmt.Errorf(
				"%w: view %q needs one of the built-in shapes and names %q",
				ErrInvalidView, v.Name, v.Shape,
			)
		}

		return nil
	default:
		// An unknown renderer is refused rather than defaulted. See the type comment:
		// the failure mode of defaulting is a page that renders wrongly and says
		// nothing.
		return fmt.Errorf("%w: view %q names renderer %q", ErrInvalidView, v.Name, v.Renderer)
	}
}

// valid reports whether s is one of the four built-in shapes.
//
// A switch and not a slice lookup, for the same reason `IsSemiplaneKind` is one.
func (s Shape) valid() bool {
	switch s {
	case ShapeStatBlock, ShapeKeyValue, ShapeList, ShapeTagCloud:
		return true
	default:
		return false
	}
}

// String renders the renderer and shape as one word, for a log line or a status
// page.
//
// An unknown renderer or shape prints as itself rather than as an empty string,
// because "this view is rendered by “" is not a line anyone can act on and the value
// came from a compiled-in plugin.
func (v View) String() string {
	if v.Shape == "" {
		return v.Renderer.String()
	}

	return v.Renderer.String() + "/" + string(v.Shape)
}

// String returns the stored text, verbatim, as with every other named thing here.
func (r Renderer) String() string {
	return string(r)
}
