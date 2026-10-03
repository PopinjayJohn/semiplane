package rules

import "fmt"

// Query is one request for derived data: which declared view, about which game
// object, and how much of it.
//
// The field named `View` is the one a `Payload` answers, and `Payload` carries that
// name back for the reason §10.6.1 gives: semiplane interprets nothing about the
// data, so the only thing it can do with a payload is route it to a renderer — and a
// renderer chosen by the caller rather than recorded in the value is one step of
// indirection away from rendering a character sheet with a stat block's renderer and
// getting HTML nobody can debug.
type Query struct {
	// View is the `Name` of one of the system's declared views. The hub takes it
	// from the route the client asked for and the system has declared it, so this is
	// never free text from a browser.
	View string

	// Object is the game object the data is about, empty for a campaign-wide query.
	Object ObjectID

	// Limit caps how many entries a list-shaped payload should carry. Zero means the
	// system's own default rather than "no limit": an unbounded list is not a
	// contract a view can honour, because §14's "renders for an empty, a typical and
	// a maximal `Derive` output" needs a maximal that is still a number.
	Limit int
}

// Check reports whether the query names a view this build could route.
//
// A predicate and not a constructor-with-error, because there is nothing to copy and
// nothing to bound: a query is three scalars, so the failure mode of building it by
// hand is a missing name rather than a mutated buffer, and `errors.Is` on that is not
// worth a sentinel. What a caller does with a false is its own business — refuse the
// route, or ask the system and let it answer.
//
// An **empty `Object` is allowed** and means a campaign-wide query: an empty
// `ObjectID` is not a valid object id, so `Object.Valid()` on its own would refuse
// every query that has no object, which is the query for a whole campaign's summary.
func (q Query) Check() bool {
	if q.View == "" || q.Limit < 0 {
		return false
	}

	return q.Object == "" || q.Object.Valid()
}

// Payload is one answered query: a system's own value, opaque to semiplane, plus
// the view it answers.
//
// `any` for the same reason `Expr.Node` is, and the argument is §10.6.1's own: a
// character sheet, a spell list and a DC summary share almost nothing between 5e,
// Pathfinder and Starfinder, so a payload type defined here would be a 5e payload
// type that every other system wraps. A plugin's own templ component takes its own
// Go type, which is what makes a rich view type-safe instead of a map traversal.
//
// The view is recorded rather than expected because semiplane's only job with a
// payload is handing it to a renderer, and the renderer is chosen by the view.
type Payload struct {
	view string
	data any
}

// NewPayload returns the answer to a query about one view.
//
// Exported because a `System.Derive` is the only thing that can build one, and the
// contract would be useless if its implementations had no way to answer it. A system
// that wants to return nothing returns a payload whose value is nil, which is
// different from returning **no** payload: §14 requires every declared view to
// render for an empty `Derive` output, and an empty list or an empty stat block is
// that, while a missing payload is a view the renderer has nothing to be told by.
//
// The view name is recorded verbatim. Whether it names something the system declared
// is a question for the hub that asked, which has both lists.
func NewPayload(view string, data any) (Payload, error) {
	if view == "" {
		return Payload{}, fmt.Errorf("%w: a payload answers a named view", ErrInvalidView)
	}

	return Payload{view: view, data: data}, nil
}

// View is the declared view this payload answers.
//
// The renderer is chosen by this and by nothing else. A caller that wanted to render
// a payload with a different view's renderer has two payloads' worth of data and one
// view's worth of meaning, and that is not a mistake this type can make harder.
func (p Payload) View() string {
	return p.view
}

// Value is the system's own data, unexamined.
//
// Nil is legal and means "nothing to show". Nothing here validates it, and §14's
// "unknown fields are ignored, not fatal" is the requirement that keeps that true:
// a payload carrying a field a renderer does not know about must still render, which
// is a property of the renderers rather than of this accessor.
func (p Payload) Value() any {
	return p.data
}

// Valid reports whether the payload answers a named view.
//
// The zero `Payload` is **invalid**: it has no view, and a renderer chosen by an
// empty name is a lookup that silently finds nothing and renders an empty page. A
// caller that forgot to check gets `false` rather than a blank document.
func (p Payload) Valid() bool {
	return p.view != ""
}
