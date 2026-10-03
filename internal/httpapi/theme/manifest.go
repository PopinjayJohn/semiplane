package theme

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/content"
)

// The manifest: what a campaign writes to say how it looks.
//
// # The file
//
// One file, `theme.yaml`, at the campaign's content root, written by the same
// person who writes the vault in Obsidian. It is not a page, it is not front
// matter, and it is not served: it is read by the server on every request for
// `/c/{slug}/theme.css` and turned into a stylesheet the server generates. §4.12.2
// is explicit that "a campaign cannot ship arbitrary CSS" and this is the shape of
// that claim — the campaign ships *data*, and the only CSS that reaches a browser
// is CSS this package wrote.
//
// It is read through `content.Root` like every other path in a campaign (S-3.5),
// and the read is bounded before the bytes are in memory. That ordering is the
// same one `content.interpretFrontMatter` uses and for the same reason: a limit
// applied after the read bounds the parser but not the buffer, and a file a sync
// client can grow without limit is a lever on both at once.
//
// # The shape
//
//	tokens:
//	  --brand-accent: "#0e7c7b"
//	  --brand-accent-ink: "#ffffff"
//
// A map keyed by CSS custom property name, under one key, and no other key. Three
// decisions in that sentence, each load-bearing:
//
//   - **Keyed by name, not by role.** `accent: "#0e7c7b"` would be shorter and
//     would read better, and it would also be a *second* vocabulary: the names in
//     this file would be the generator's and the names in the stylesheet would be
//     CSS's, and a rename of one would silently orphan the other. Writing the
//     token name in the file means the file, the vocabulary in ownership.go and
//     the declaration in the stylesheet are one list of spellings, and the
//     vocabulary is what decides which of them a campaign may use.
//   - **Under `tokens:`, with nothing else.** `KnownFields(true)` on the decoder
//     turns an unrecognised top-level key into a refusal rather than a shrug, and
//     that is worth a nesting level: a GM who writes `brand:` instead of `tokens:`
//     gets told so, rather than a manifest that validates as empty and a campaign
//     whose brand silently never applies. "Validates as empty" is the failure this
//     is the answer to.
//   - **No version field.** A manifest without one cannot be migrated, and a
//     migration for a file with two keys is a file a person edits — which they can
//     do in Obsidian, where nothing is atomic and nothing is reviewed. If the
//     shape ever changes, the honest version is a new name (`theme-v2.yaml`) and a
//     generator that reads it, because a GM with an old vault must keep a working
//     theme. See the work item's report.
//
// # What a manifest may not do
//
// Whole-manifest refusal, never per-key. §4.12.3's failure direction is the core
// theme standing, and applying the two names of a pair while refusing one of them
// produces a *third* thing: an accent with the default ink, or an ink with the
// default accent, neither of which was validated as a pair and both of which the
// GM did not write. A partial application is a theme nobody asked for and no
// contrast floor covers, so the refusal is total and names the offending token.

// ManifestFileName is where a campaign's theme manifest lives: one name, at the
// root of the content tree.
//
// A leading dot was rejected and the reasoning is worth keeping: the indexer skips
// dot-files, so a hidden manifest would be invisible to the index, to a directory
// listing in the wiki, and to the GM trying to find the file they are told the
// error came from. `theme.yaml` is a file an author can see and edit where they
// already work.
const ManifestFileName = "theme.yaml"

// MaxManifestBytes bounds the manifest at 16 KiB, and the number is small on
// purpose.
//
// `content.MaxDocumentBytes` is 4 MiB and `frontMatterMaxBytes` is 256 KiB, both
// sized for prose. A theme manifest is two colour literals, so 16 KiB is four
// orders of magnitude past what the schema can express — and the file's *grammar*
// is what bounds its content, not its size. The cap is not there to catch a large
// manifest; it is there so that "read the manifest" is a bounded operation even
// when the file is not a manifest at all, which is the case a hostile vault
// presents: a sync client writes four gigabytes into `theme.yaml`, and without
// this the request reads all four before discovering there is nothing to parse.
const MaxManifestBytes = 16 << 10

// The refusals. All of them are *answers*, not faults: a campaign's theme is
// attacker-reachable input (Obsidian sync writes it, §4.12.2), and §4.12.3's
// requirement is that a rejected theme degrades to the default rather than
// shipping an unreadable UI. Nothing here is a 5xx.
var (
	// ErrNoManifest is a campaign with no manifest, which is not a failure and
	// not an absence either: it is the ordinary state of every campaign that has
	// not been branded, and it produces the core theme.
	ErrNoManifest = errors.New("theme: the campaign has no theme manifest")

	// ErrManifestTooLarge is a manifest over MaxManifestBytes.
	ErrManifestTooLarge = fmt.Errorf("%w over the %d-byte limit",
		content.ErrDocumentTooLarge, MaxManifestBytes)

	// ErrMalformedManifest is a manifest the YAML parser would not read, or one
	// carrying a key this build has no name for.
	ErrMalformedManifest = errors.New("theme: the theme manifest is not valid YAML")

	// ErrUnreadableManifest is a manifest that exists and could not be read —
	// a permissions change, an I/O error. Distinct from the refusals above
	// because it is the one that answers 500: the campaign may well have a
	// perfectly good theme on disk, and telling every reader of it that there is
	// none would be a lie produced by our own fault.
	ErrUnreadableManifest = errors.New("theme: the theme manifest could not be read")
)

// document is the manifest as parsed, before any decision has been made about it.
//
// A one-field struct rather than `map[string]any` because `KnownFields(true)` is
// the mechanism that refuses an unrecognised key, and it only works against a
// struct: the moment the target is a bare map, every key is by definition
// recognised and the strictness is gone. The inner map is `map[string]string`
// because a token's value is a colour literal and nothing else, so a value of any
// other shape is a refusal rather than a coercion — and coercion is what would
// make `--brand-accent: 0e7c7b` (a YAML number-ish scalar) mean something
// different from what the author read.
type document struct {
	// `tokens` is the one key a manifest may carry. It is a tag and not a
	// constant because a struct tag cannot name one, and this sentence is the
	// answer to "what may a manifest contain" for a reader who would rather not
	// read a struct literal: exactly this, and `KnownFields(true)` refuses
	// anything else (`TestAnUnknownKeyIsRefused` is the evidence).
	Tokens map[string]any `yaml:"tokens"`
}

// Parse reads a campaign's manifest and returns the theme it describes.
//
// Total, in the sense that matters here: a manifest it will not apply returns an
// error and **no** stylesheet, and the caller is expected to keep whatever theme
// the campaign had rather than to serve half of this one. `ErrNoManifest` is not
// returned here — an absent file is not a manifest that failed, and the caller
// reads the file, so absence is its own case before `Parse` is reached.
//
// The error is a `*RefusalError` for every decision this package makes about the
// manifest's *content*, including a colour that fails a contrast floor: a caller
// that wants to tell a GM which token it was reads it with `errors.As`. The
// parser's own message is discarded rather than wrapped, following
// `content.interpretFrontMatter`: a YAML error quotes the line it choked on, and
// that line is a campaign file's contents.
func Parse(src []byte) (Manifest, error) {
	if len(src) > MaxManifestBytes {
		return Manifest{}, ErrManifestTooLarge
	}

	var parsed document

	decoder := yaml.NewDecoder(bytes.NewReader(src))
	decoder.KnownFields(true)

	if err := decoder.Decode(&parsed); err != nil {
		// An empty document is not an error. A manifest that is nothing but
		// comments decodes to a zero value, and "a GM wrote a file with the
		// explanation in it and no theme in it yet" is a state to serve, not a
		// fault to report. The core sheet comes back rather than an empty string,
		// because the route serves `Sheet()` unconditionally and an empty body is
		// not a stylesheet.
		if errors.Is(err, io.EOF) {
			return Manifest{sheet: generate(brand{})}, nil
		}

		return Manifest{}, ErrMalformedManifest
	}

	branded, err := parsed.brand()
	if err != nil {
		return Manifest{}, err
	}

	return Manifest{branded: branded.branded, sheet: generate(branded)}, nil
}

// brand validates the manifest's tokens and returns the brand pair, or the zero
// value for "no theme".
//
// Sorted iteration rather than map order, for a reason that is about the log and
// not about the sheet: a refusal names the *first* offending token, and which one
// that is must not depend on Go's map iteration order — a manifest with two bad
// names would report a different one per request, and an operator comparing two
// log lines would conclude they were two different manifests.
func (d document) brand() (brand, error) {
	if len(d.Tokens) == 0 {
		return brand{}, nil
	}

	parsed := map[string]colour{}

	for _, name := range slices.Sorted(maps.Keys(d.Tokens)) {
		value, err := claimToken(name, d.Tokens[name])
		if err != nil {
			return brand{}, err
		}

		parsed[name] = value
	}

	// Both halves or neither, and the check is driven by the vocabulary's own
	// partner column rather than by a hand-written "is the accent there too".
	// That is the point of encoding the partner in `campaignVocabulary`: the
	// completeness rule cannot drift from the vocabulary, because there is only
	// one list and it carries both facts.
	for _, name := range slices.Sorted(maps.Keys(parsed)) {
		if _, half := parsed[campaignVocabulary[name]]; !half {
			return brand{}, refused(name, reasonHalfPair)
		}
	}

	return measure(parsed)
}

// claimToken is one token's decision: is the name one a campaign may set, and is
// the value a colour.
//
// Three refusals and one acceptance, in this order, and the order is the
// argument:
//
//  1. **The name's shape.** Before ownership, because an unrecognisable name has
//     no owner and no rule — and because the name is what reaches a log line.
//  2. **Ownership.** §4.12.1's table, consulted as data. A product token is
//     refused with the reason that matches it: the accessibility contract is a
//     different sentence from "that is ours", and a GM who wrote `--target-min`
//     deserves to be told why that one in particular is closed to them.
//  3. **The value's grammar.** Last, so that a manifest naming a protected token
//     is refused for being protected rather than for a colour it never got as far
//     as parsing.
func claimToken(name string, raw any) (colour, error) {
	if _, wellFormed := tokenName(name); !wellFormed {
		return colour{}, &RefusalError{Reason: reasonMalformedKey}
	}

	switch ownerOf(name) {
	case ownerCampaign:
	case ownerProduct:
		if protected(name) {
			return colour{}, refused(name, reasonProtected)
		}

		return colour{}, refused(name, reasonProductToken)
	}

	// A null value is its own answer rather than a colour failure, and it is the
	// single most likely mistake in this file: YAML reads `#` as the start of a
	// comment, so an unquoted `--brand-accent: #0e7c7b` hands the parser an
	// *empty* value and not a colour. Without this branch the author is told their
	// colour is not a colour, which is true and useless; with it, they are told
	// the two characters they have to change.
	if raw == nil {
		return colour{}, refused(name, reasonUnquoted)
	}

	value, isText := raw.(string)
	if !isText {
		return colour{}, refused(name, reasonNotAColour)
	}

	parsed, err := parseColour(value)
	if err != nil {
		return colour{}, refused(name, reasonNotAColour)
	}

	return parsed, nil
}

// brand is a validated brand pair, or the absence of one.
//
// A map keyed by token name rather than two colour fields, and the reason is that
// `campaignVocabulary` is the list of names a campaign may set — so the generator
// must ask *that* list which declarations to write. A `brand` with an `accent` and
// an `ink` field would put a second list of names in this package, and the two
// would agree until somebody added a third overridable token to ownership.go and
// the generator went on emitting two.
//
// A struct rather than a bare map for the same reason `document` is a struct: the
// "no theme" state must be representable, and a zero map with a nil entry is a
// subtler way of saying that than a flag next to the values.
type brand struct {
	// branded reports that the manifest named tokens and they passed. False for a
	// manifest that named nothing, which is a campaign with no theme rather than a
	// campaign with a black one.
	branded bool

	// tokens is the validated value for each name the manifest set. Every key is
	// in `campaignVocabulary` and every value came out of `parseColour`, so this
	// map is the only thing `generate` ever reads.
	tokens map[string]colour
}

// measure applies §4.12.3's two floors to a validated set of tokens.
//
// The pair floor first, then the page floors, and the order is the order a GM
// should fix things in: the ink is the one they can change freely, and a brand
// that is unreadable *on itself* is a worse problem than one that is hard to see
// against the page.
//
// The floors are named to the pair rather than to the set, and that is a real
// limit: `brandAccent` and `brandInk` are the only two tokens with a contrast
// requirement, because they are the only two whose relationship to something else
// is specified. A third campaign token — `--brand-header-image` is the one §4.12.1
// still owes the product — needs its own rule here when it arrives, and the
// vocabulary's partner column is what will say so.
func measure(parsed map[string]colour) (brand, error) {
	accent, ink := parsed[brandAccent], parsed[brandInk]

	if contrastRatio(ink, accent) < brandPairFloor {
		return brand{}, refused(brandInk, reasonPairFloor)
	}

	for index, page := range pageBackgrounds {
		if contrastRatio(accent, page) < brandPageFloor {
			return brand{}, refused(brandAccent, reasonPageFloorOn(themeNames[index]))
		}
	}

	return brand{branded: true, tokens: parsed}, nil
}

// reasonPageFloorOn is the page-floor reason for one theme, so the two refusals
// differ in exactly one word and a GM can tell which page failed.
func reasonPageFloorOn(theme string) string {
	return reasonPageFloor + " in the " + theme + " theme"
}

// RefusalError is a manifest decision semiplane will not apply.
//
// A named exported type rather than a formatted error because §4.12.3 requires
// the campaign overview to show a GM "a notice naming the rejected pair", and a
// caller cannot do that with a string: it needs the token, and it needs to know
// the difference between "you named the product's token" and "your colour is too
// close to the page to read".
//
// `Token` is empty when the manifest's own *name* was the problem, and `Reason`
// is always a fixed sentence plus, at most, one validated token name. Neither
// field ever carries a byte from the manifest's values.
type RefusalError struct {
	// Token is the custom property name the refusal is about, when the name was
	// a well-formed one. Empty otherwise, and `Reason` says so.
	Token string

	// Reason is the sentence a GM reads. Fixed text, plus the token name or the
	// theme's name — never a value from the file.
	Reason string
}

// Error satisfies the error interface, and is the log line's text.
func (e *RefusalError) Error() string {
	if e.Token == "" {
		return e.Reason
	}

	return e.Token + ": " + e.Reason
}

// refused builds a refusal about a named token.
func refused(token, reason string) *RefusalError {
	return &RefusalError{Token: token, Reason: reason}
}

// The reasons. Named constants rather than literals at the call sites because
// they are part of this package's contract — a caller may match on them — and
// because a message that varies with its call site is a message nobody can grep.
//
// None of them contains a byte from the manifest. That is the constraint all
// eight obey, and it is why the offending value never appears in one — see
// `errNotAColour`.
const (
	reasonProductToken = "this is semiplane's own token and no campaign theme may set it"
	reasonProtected    = "this token is part of the accessibility contract and is never overridable"
	reasonMalformedKey = "a token name must be -- followed by lowercase letters, digits or dashes"
	reasonUnquoted     = "a colour must be quoted: an unquoted # begins a YAML comment"
	reasonNotAColour   = "a colour is written #rrggbb, with no alpha and no other notation"
	reasonHalfPair     = "the brand is two tokens; set both or set neither"
	reasonPairFloor    = "the brand ink is below 4.5:1 on the brand accent"
	reasonPageFloor    = "the brand accent is below 3:1 on the page"
	reasonConfinement  = "the manifest is a link, or lies outside the campaign's own root"
)

// maxTokenNameLen bounds a token name at 64 characters.
//
// A bound because a YAML key is an arbitrary-length byte string and this value
// reaches a log line and a refusal message. CSS custom property names have no
// length limit in the specification, so this is a choice rather than a standard:
// the longest name in the product's stylesheets is `--surface-sunken-border` at 22,
// and a campaign cannot invent a meaningful name an order of magnitude longer
// than that. Everything longer is refused with `reasonMalformedKey`, unquoted.
const maxTokenNameLen = 64

// tokenName validates a manifest key as a CSS custom property name.
//
// The grammar is `--` followed by at least one character from `a-z`, `0-9` and
// `-`. That is a subset of what CSS permits, deliberately: a name that reaches a
// log line, a refusal message and a generated declaration is a name worth
// restricting to characters that cannot terminate a line, quote a string, or open
// a comment. Uppercase and non-ASCII are excluded for the same reason the colour
// grammar is `#rrggbb` and nothing else — the product's own names are all
// lowercase, so the subset loses nothing a campaign could have used.
func tokenName(name string) (string, bool) {
	if !strings.HasPrefix(name, "--") || len(name) > maxTokenNameLen {
		return "", false
	}

	rest := name[2:]
	if rest == "" {
		return "", false
	}

	for _, letter := range rest {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= '0' && letter <= '9',
			letter == '-':
		default:
			return "", false
		}
	}

	return name, true
}

// readManifest reads a campaign's manifest through its confined root.
//
// Three answers, and the middle one is why this function exists rather than a
// `defer` at each call site:
//
//   - `ErrNoManifest` — there is no `theme.yaml`. Not a failure: the campaign is
//     unbranded, and the caller serves the core theme and forgets any theme it
//     had. Withdrawal is not an error, and treating a deleted manifest as one
//     would keep serving a brand a GM had deliberately removed.
//   - a `*RefusalError` — the file is there and will not be applied. The caller's
//     problem, not this one's: keep the last good theme and log it.
//   - anything else — a read that failed. The caller answers 500.
//
// The size cap is applied to the *read*, not after it: `Target.ReadFile` would
// have the whole file in memory before anything could look at it, and the file is
// attacker-reachable.
func readManifest(root *content.Root) ([]byte, error) {
	target, err := root.At(ManifestFileName)
	if err != nil {
		return nil, classifyManifestRead(err)
	}

	file, err := target.Open()
	if err != nil {
		return nil, classifyManifestRead(err)
	}

	defer func() {
		// The close error is deliberately dropped. The bytes are read by this
		// point, a close on a read-only handle fails only if the descriptor is
		// already gone, and the alternative — threading a close error into a
		// return that has three answers of its own — buys a log line nobody
		// acts on. `assets` logs its close failure because it hands the handle
		// to `ServeContent`, which reads after the defer.
		_ = file.Close()
	}()

	// One `stat` before the read, because a *directory* called `theme.yaml` opens
	// successfully on Unix and then fails the read with a platform-specific error —
	// which would make this campaign's theme a 500 for as long as the directory
	// existed. `content.Target.ReadFile` performs the same check for the same
	// reason; it is not available here because the bounded read needs the handle.
	//
	// The file was already resolved through `Root.At`, so this stat cannot escape
	// the root and answers only "is this a regular file".
	if info, statErr := file.Stat(); statErr == nil && !info.Mode().IsRegular() {
		return nil, ErrNoManifest
	}

	// `Parse` is what refuses an over-long document, and there is deliberately no
	// second check here. Two checks on one number is one check and a place for
	// them to disagree — and the mutation that removes this one is provably
	// unobservable, because the other one still holds. `io.LimitReader` is the
	// part that is this function's own: it bounds how many bytes are *read*, which
	// no later check can do, since by then the bytes are already in memory.
	//
	// One byte past the cap, so the read is bounded even for a file that is
	// enormous.
	src, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadableManifest, err)
	}

	return src, nil
}

// classifyManifestRead maps a root's refusal to one of this package's answers.
//
// Three, and the middle one is the one worth reading twice:
//
//   - `ErrNoManifest` for a missing file and for a path that is not a file. From
//     this package's side those are the same fact: there is no manifest to apply.
//   - a `*RefusalError` for a link. `content.RefuseSymlinks` is S-4.4, and
//     `content.Root`'s own note on it is the right frame — "a vault with a link in
//     it is a configuration to look at, not an attack". So a symlinked
//     `theme.yaml` is a refused manifest, answered with the campaign's last good
//     theme and one error line. A 500 would be wrong in both directions: it tells
//     every reader of the campaign that the instance is broken, over a file the
//     campaign's owner can delete.
//   - anything else keeps its identity for the caller to classify as a fault.
//
// The sentinels are wrapped rather than replaced, so `errors.Is` still finds
// `content.ErrOutsideRoot` for a caller that wants to say so in a log.
func classifyManifestRead(err error) error {
	switch {
	case errors.Is(err, content.ErrNotExist), errors.Is(err, content.ErrNotDir):
		return ErrNoManifest

	case errors.Is(err, content.ErrSymlink), errors.Is(err, content.ErrOutsideRoot):
		return &RefusalError{Reason: reasonConfinement}
	}

	return fmt.Errorf("%w: %w", ErrUnreadableManifest, err)
}
