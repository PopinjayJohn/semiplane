// Package ext holds the markdown extensions semiplane adds on top of
// CommonMark + GFM: `[[wikilink]]`, `![[embed]]`, `{{statblock}}` and
// `{{dice}}`.
//
// They are a package of their own, and separate from the renderer in
// `internal/content`, for a reason that is about the *next* extension rather
// than this one. Phase 10 adds `[!secret]`, and a gameplay plugin in phase 8
// adds whatever its own content uses. Each of those has to be able to say "this
// is a trigger byte, this is the node it produces, this is the HTML it writes"
// and be installed by naming it — with no edit to the renderer, no case added to
// a dispatch, and no entry in the sanitiser policy beyond the element and
// attribute it is allowed to emit. So the mechanism here is a `Definition`
// value: the syntax is a table of these, and installing them is
// `ext.New(defs...)`. Adding an extension is adding a definition.
//
// Three properties of the mechanism are load-bearing, and all three are about
// permission-neutrality rather than convenience:
//
//   - An extension's HTML is written by this package and nowhere else, and every
//     byte it writes is a fixed template with escaped interpolation. Nothing an
//     author writes reaches the output as markup: not the link target, not the
//     alias, not a `{{…}}` argument. S-4.6 makes raw HTML in a vault
//     attacker-reachable, so the grammar is the untrusted part and the template
//     is the trusted part, and only the template is ever markup.
//   - Nothing here resolves anything. `[[Page]]` is rendered as a link with no
//     `href` and the reference handed through, because resolution is
//     Obsidian-ordered, campaign-scoped and viewer-scoped (S-5.5) and belongs to
//     `internal/content/links.go`. An extension that resolved its own target
//     would be a second, differently-ordered resolver, and the one that would
//     be wrong in a way that leaks: whether a target exists is an answer about
//     existence, and the "no access is 404, never 403" rule AGENTS.md states
//     for responses applies to the HTML for the same reason.
//   - An unrecognised `{{name}}` is text. The parser declines it and goldmark
//     writes the author's braces as the braces they wrote, so a page mentioning
//     a directive no installed plugin provides still renders. The alternative —
//     dropping the span — is a page destroyed by a missing plugin.
//
// The extensions are inline parsers, not block parsers, so `[[…]]` works
// mid-sentence as Obsidian writes it and `{{…}}` works inside a table cell. A
// code span and a fenced code block are parsed before any inline parser sees
// their contents, so a `[[wikilink]]` written in backticks is text and stays
// text — the property `TestExtensionsAreInertInCode` holds, because "documented
// in the design record" is not a test.
package ext

import (
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// nodeKind is the AST kind every semiplane extension node reports.
//
// One kind for all of them, registered once as a package variable, so a node
// needs no type of its own per extension and the renderer dispatches on the
// node's own `kind` through a map the same definitions built. goldmark's
// `ast.NewNodeKind` appends to a package-global name table, so it must run
// exactly once and before any conversion; a package-level variable is
// initialised once, before any goroutine can reach this package's API, and that
// is the only place the guarantee comes from. It is not an `init()`:
// registration that happens where a caller can see it.
var nodeKind = gast.NewNodeKind("SemiplaneExtension")

// priority is where these inline parsers sit in goldmark's dispatch order.
//
// Lower numbers run first, and the constraint that fixes this value is
// goldmark's **link parser at 200**, which triggers on `[` and on `!` and
// *always* returns a node for them: it opens a link label and waits to be closed,
// which is how `[text](url)` and `![alt](src)` are assembled from two separate
// inline parses. A parser registered behind it on the same trigger byte is never
// called, because goldmark stops at the first parser that returns something.
//
// So a wikilink must be tried *before* the link parser, and this sits just below
// it. Above 0, the task-checkbox parser, so `- [ ] done` is still a checkbox and
// not a wikilink whose first bracket happens to be followed by a space. It does
// not need to be below the code-span parser at 100, because that one triggers on
// a backtick and so never competes for a `[` — which is also why a `[[wikilink]]`
// written in backticks is already safe: the code span is consumed whole before any
// inline parser sees its contents.
//
// Behind the link parser, `[text](url)` would still be a markdown link and this
// would be dead code, which is a bug that looks like a feature: the wikilink
// syntax would simply never match and every reference in a campaign would render
// as the author's own brackets. `TestExtensionsFire` is what catches it.
const priority = 150

// triggers is every byte a Definition may dispatch on, in a fixed order.
//
// The order is fixed because the extensions are grouped into one parser per
// trigger byte and a map's iteration order is not: a goldmark instance assembled
// in a random order is one whose behaviour depends on Go's map seed, and nothing
// about a render pipeline should depend on that (S-5.2 keys a cache on a content
// hash, so "the output is the same either way" has to be true rather than
// likely).
var triggers = []byte{'!', '[', '{'}

// Kind names one semiplane inline extension.
//
// A string rather than an int, because it crosses into the HTML as a `class` and
// into the sanitiser policy as a literal, and a number would make those two agree
// only through a lookup somebody has to remember to update.
type Kind string

const (
	// KindWikilink is `[[Page]]`, an Obsidian link to another page.
	KindWikilink Kind = "wikilink"

	// KindEmbed is `![[Page]]`, an embed of another page or of an asset.
	KindEmbed Kind = "embed"

	// KindStatblock is `{{statblock:Name}}`, a game object rendered as a stat
	// block by whichever rules plugin claims its kind.
	KindStatblock Kind = "statblock"

	// KindDice is `{{dice:1d20+5}}`, a dice expression the live layer rolls.
	KindDice Kind = "dice"
)

// String returns the kind as the text that appears in the rendered HTML.
func (k Kind) String() string {
	return string(k)
}

// Node is the inline node every semiplane extension produces.
//
// One node type for all of them, with the extension named in `kind`, because the
// four extensions differ in the bytes they write and in almost nothing else: the
// grammar, the escaping, the index and the position are the same work, and four
// copies of it would be four places for a fix to be missed in.
//
// The node carries the author's bytes and nothing derived from them. The split of
// a `[[…]]` body into a target, an anchor and an alias is
// `content.ParseReference`'s job, in `internal/content/links.go`, and it is
// deliberately not duplicated here — see `scanReference`. A `{{name:arg}}`
// argument is carried verbatim for the same reason: what `1d20+5` means belongs
// to the gameplay plugin's grammar, and the protocol never assumes d20.
type Node struct {
	gast.BaseInline

	// kind is the extension that produced the node. Read with Extension; the
	// field is unexported because `Kind` is the node-kind method `ast.Node`
	// requires, and a field and a method cannot share a name.
	kind Kind

	// inner is the text the author wrote between the delimiters, verbatim: the
	// body of a `[[…]]` or `![[…]]`, and the argument of a `{{name:arg}}`.
	inner string

	// offset is the byte offset of the extension's opening delimiter in the
	// source, so a caller can turn it into a line number for a broken-link
	// report. Set by the parser, read by the collector in `internal/content`.
	offset int

	// index is the node's ordinal in document order across every extension, and
	// is what the rendered element carries as `data-ref-index`. Assigned by the
	// collector before rendering, for the reason in SetIndex.
	index int
}

// Kind implements ast.Node.Kind.
func (n *Node) Kind() gast.NodeKind {
	return nodeKind
}

// Dump implements ast.Node.Dump.
func (n *Node) Dump(source []byte, level int) {
	gast.DumpHelper(n, source, level, map[string]string{
		"Extension": string(n.kind),
		"Inner":     n.inner,
	}, nil)
}

// Extension returns which extension produced the node.
func (n *Node) Extension() Kind {
	return n.kind
}

// Inner returns the text between the extension's delimiters, verbatim.
//
// Never normalised, never escaped and never looked up. It is the author's bytes,
// because a broken-link report has to name what was written rather than what was
// understood, and because the caller's parser is the authority on what it means.
func (n *Node) Inner() string {
	return n.inner
}

// Offset returns the byte offset of the extension in the source, which is where
// its opening delimiter starts.
func (n *Node) Offset() int {
	return n.offset
}

// Index returns the node's document-order ordinal, or -1 before the collector has
// assigned it.
func (n *Node) Index() int {
	return n.index
}

// SetIndex assigns the node's document-order ordinal.
//
// Exported for the collector in `internal/content`, which walks the parsed tree
// before rendering and is the only thing that may assign it. Assigning it here
// rather than in the renderer is what keeps the rendered `data-ref-index` equal to
// the position in the caller's reference list: the collector numbers the nodes in
// the same walk that fills that list, so the two cannot disagree.
func (n *Node) SetIndex(index int) {
	n.index = index
}

// Definition is one extension: the punctuation that starts it, the keyword that
// selects it when it is a `{{name}}` form, and the function that writes its HTML.
//
// The whole unit phase 10 and phase 8 add. A `Definition` is the entire extent of
// an extension, which is the point: nothing else in the pipeline enumerates them.
type Definition struct {
	// Kind names the extension. It becomes the rendered element's `class` and the
	// value of its `data-ext`, and the two must agree — the class is a styling
	// hook phase 5 may rename, `data-ext` is the identity a live layer keys on,
	// and the sanitiser policy allows both values from one list.
	Kind Kind

	// Trigger is the punctuation byte goldmark dispatches on: `'['` for
	// `[[wikilink]]`, `'!'` for `![[embed]]`, `'{'` for both `{{…}}` forms.
	Trigger byte

	// Name is the keyword of a `{{name}}` extension, and empty otherwise. It is
	// matched exactly, which is what makes an unknown `{{…}}` inert.
	Name string

	// Write renders the node's element.
	//
	// Exported, because the package's claim is that an extension is *one value*,
	// and a claim a caller outside the package cannot act on is a comment. Phase
	// 10's `[!secret]` and a phase-8 plugin's own directive both construct a
	// `Definition` from `internal/httpapi` or `internal/plugin`, not from here.
	//
	// It is given no source and no renderer options because it has no business
	// with either: it writes a fixed template whose only variable parts are
	// HTML-escaped, so there is nowhere for the document to get in. A writer that
	// wanted to interpolate raw author text would have to escape it itself, and
	// `escape` is deliberately unexported too — see the note on `Write`'s contract
	// in `slot`.
	Write func(w util.BufWriter, node *Node) error
}

// New returns the goldmark extension that installs every definition.
//
// Order does not affect the result: goldmark sorts parsers by trigger and
// priority, and the two extensions sharing a trigger — `statblock` and `dice` on
// `{` — are told apart by keyword. So `New(Builtins()...)` and
// `New(append(Builtins(), Secret())...)` are the same mechanism with one more
// entry, which is what phase 10 and phase 8 do.
func New(defs ...Definition) goldmark.Extender {
	return extender{defs: defs}
}

// Builtins returns the extensions semiplane ships, in a fixed order.
//
// A function returning a fresh slice rather than a package-level var, so a caller
// that appends its own definition to the result cannot write into a slice every
// campaign shares. This is the list the composition root names; nothing registers
// itself on import.
func Builtins() []Definition {
	return []Definition{
		Wikilink(),
		Embed(),
		Statblock(),
		Dice(),
	}
}

// extender is the goldmark.Extender `New` returns.
//
// A named type rather than a struct literal or a closure because goldmark's
// `Extender` interface is a single method, and a value with no name is harder to
// read in a stack trace than one with a bad extension in it.
type extender struct {
	defs []Definition
}

// Extend implements goldmark.Extender.Extend.
//
// Two things are installed: one inline parser per trigger byte that has a
// definition, and one node renderer carrying every definition's write function.
// The renderer is shared rather than one-per-extension because they all write one
// node kind, and the map is what makes the set open — a fifth extension needs no
// edit to the dispatch.
func (e extender) Extend(markdown goldmark.Markdown) {
	byTrigger := make(map[byte][]Definition, len(triggers))
	writers := make(map[Kind]func(util.BufWriter, *Node) error, len(e.defs))

	for _, def := range e.defs {
		byTrigger[def.Trigger] = append(byTrigger[def.Trigger], def)
		writers[def.Kind] = def.Write
	}

	parsers := make([]util.PrioritizedValue, 0, len(triggers))

	for _, trigger := range triggers {
		defs := byTrigger[trigger]
		if len(defs) == 0 {
			continue
		}

		parsers = append(parsers, atPriority(newParser(trigger, defs)))
	}

	markdown.Parser().AddOptions(parser.WithInlineParsers(parsers...))
	markdown.Renderer().AddOptions(renderer.WithNodeRenderers(
		atPriority(&nodeRenderer{writers: writers}),
	))
}

// atPriority wraps a parser or a renderer in goldmark's prioritised value, at this
// package's priority.
//
// Two details here are about the linter rather than about goldmark, and both are
// stated so a reader does not "fix" them back.
//
// The name is spelled without a `z` and the body's one call is suppressed, because
// goldmark spells its helper with a `z` while this repository's `misspell` is
// configured for UK English. A function of our own with the same spelling would
// be a finding at every call site and at its own declaration, so the wrapper is
// named differently and the single call inside it carries the suppression. That
// also satisfies `nolintlint`, which wants a suppression to be both specific and
// actually used — one here, rather than three.
//
// The parameter is `any` rather than a type union of the two interfaces, because Go
// does not permit a union of interface types that have methods — only of basic types
// or of interfaces with no methods. That is a language rule rather than a
// preference, so the two call sites are left to be checked at run time instead: a
// value that is neither an InlineParser nor a NodeRenderer is refused inside
// goldmark's `addInlineParser`, which is the same failure it would have had here.
func atPriority(value any) util.PrioritizedValue {
	//nolint:misspell // goldmark's own spelling; see this function's doc comment.
	return util.Prioritized(value, priority)
}

// nodeRenderer writes every semiplane extension node.
type nodeRenderer struct {
	writers map[Kind]func(util.BufWriter, *Node) error
}

// RegisterFuncs implements renderer.NodeRenderer.RegisterFuncs.
func (r *nodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(nodeKind, r.render)
}

// render implements the single renderer function.
//
// `WalkSkipChildren` is what makes these elements leaves: none of them has
// children, and a node with children would be walked and written a second time. A
// node whose kind has no writer — which needs a `Definition` built with a nil
// `write`, and `New` is the only way to build one — is skipped rather than
// written as an empty element, so a malformed definition costs an element and not
// a panic on the read path.
func (r *nodeRenderer) render(
	writer util.BufWriter, _ []byte, node gast.Node, entering bool,
) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkSkipChildren, nil
	}

	ext, ok := node.(*Node)
	if !ok {
		return gast.WalkSkipChildren, nil
	}

	write, ok := r.writers[ext.kind]
	if !ok {
		return gast.WalkSkipChildren, nil
	}

	if err := write(writer, ext); err != nil {
		return gast.WalkStop, err
	}

	return gast.WalkSkipChildren, nil
}

// newParser returns the inline parser for one trigger byte.
//
// The two families are different scanners over different grammars, and the split
// is by trigger rather than by extension because that is what the dispatcher
// knows: it hands over a byte, and the byte is what it dispatched on.
func newParser(trigger byte, defs []Definition) parser.InlineParser {
	if trigger == '{' {
		return newBracesParser(defs)
	}

	return newRefParser(trigger, defs)
}

// byName returns the keyword-indexed definitions of a `{{name}}` trigger.
func byName(defs []Definition) map[string]Definition {
	indexed := make(map[string]Definition, len(defs))
	for _, def := range defs {
		if def.Name != "" {
			indexed[def.Name] = def
		}
	}

	return indexed
}
