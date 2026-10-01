package domain

// PageKind is what a page is: prose, or a registered kind of game object.
//
// Prose is a first-class value rather than the empty string, and that is the
// whole of S-3.3. A `kind` that is absent and a `kind` that names something this
// build does not understand produce the *same* answer — KindProse — so a caller
// cannot tell them apart by accident, and cannot read "no kind" as "some kind
// whose name happens to be empty". It also makes the degradation total: an
// unknown kind leaves no PageKind behind that a switch is missing a case for, so
// removing a plugin cannot stop a page from rendering, and a page with a
// misspelled `kind` still renders its prose and its front matter is still
// available to whoever wrote it.
//
// The type is a string and not a closed enum. semiplane owns `journal`,
// `handout`, `index`, `token` and `scene` (S-3.4) and a gameplay plugin declares
// rules-content kinds of its own, so any list written into this package would be
// a list a plugin could not join, and the wrong answer for every kind it adds:
// the page would degrade to prose in a build where it is perfectly well formed.
// What counts as known is a property of the *running* process — the plugin
// registry — and is therefore asked of the registry rather than asserted here.
type PageKind string

// KindProse is an ordinary markdown page: no kind declared, or a kind this build
// does not recognise.
//
// Every page is renderable as prose, which is what makes the degradation safe
// rather than lossy. It is also the value stored in `pages.kind`, so the index
// says "prose" rather than leaving the column blank and leaving a reader to guess
// whether a blank kind means "no kind" or "a kind nobody wrote down".
const KindProse PageKind = "prose"

// PageKindRegistry is the lookup `ResolvePageKind` asks, and the one question the
// content pipeline needs answered about kinds.
//
// One method, and the smallest interface that carries the answer. The set of kinds
// is a property of a registry that phase 8 builds; declaring the question here
// rather than taking a `map[string]PageKind` keeps the caller from having to build
// a map per parse, keeps the answer in one place when a plugin is added, and — the
// reason it is an interface at all — keeps the check testable without a plugin
// registry. `domain` must not import the registry that does not exist yet, and a
// function parameter typed `func(string) bool` would hide the question behind a
// signature a reader has to decode.
type PageKindRegistry interface {
	// HasPageKind reports whether name is a kind the running build has
	// registered. The name arrives exactly as the page declared it: no trimming,
	// no case folding, because this lookup answers "is this the identifier the
	// registry registered", not "is this recognisable as one of its kinds".
	HasPageKind(name string) bool
}

// ResolvePageKind maps the `kind` a page declares onto the kind to honour.
//
// It is total and it has no error: absent, empty, and unregistered all resolve
// to KindProse, and a caller therefore cannot fail to handle the interesting
// case. A rule that could fail would need every caller to decide what to do with
// the failure, and the loader is exactly the caller that must not be tempted to
// treat an unknown kind as a fatal one — S-3.3's inertness is the property.
//
// The match is exact, for the reason ParseRole's and ParseVisibility's are. A
// kind reaches a URL segment, a token-list filter and a template class name, so a
// value carrying surrounding whitespace, or differing in case, would have to be
// escaped or normalised in each of those places, and the two spellings would be
// two pages to a filter and one to an author. Degrading to prose is the honest
// answer: the page renders, and the declared value is still available to the
// caller that wants to tell the author about it.
//
// A nil registry is treated as one that knows nothing, which is the answer that
// degrades rather than the one that panics.
func ResolvePageKind(declared string, registry PageKindRegistry) PageKind {
	if declared == "" || registry == nil {
		return KindProse
	}

	if !registry.HasPageKind(declared) {
		return KindProse
	}

	return PageKind(declared)
}

// String returns the stored text, verbatim.
//
// Identity rather than a validity check, as with Role.String and
// Visibility.String: the value is written to a column and shown to an operator,
// and a caller that needs to *know* the kind is understood must ask the registry
// rather than believe a string.
func (k PageKind) String() string {
	return string(k)
}
