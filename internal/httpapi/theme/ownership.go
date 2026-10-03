package theme

import (
	"slices"
	"strings"
)

// §4.12.1's "overridable and not" table, as data.
//
// This file is the whole of UI §4.12.1's enforcement, and it is deliberately the
// *only* place in the repository that knows which side owns a token name. That
// matters because the record states the rule in a table inside a markdown
// document, and a rule that lives only in a document is a rule that rots the
// moment the stylesheet gains a token: nothing fails, nothing says so, and the
// gap shows up as a campaign theme that quietly does less than its author asked
// for — or, in the other direction, as a stylesheet a campaign can set a name in
// that the accessibility contract depends on.
//
// So the distinction is a table, the table is consulted on every manifest, and
// three claims about it are asserted against the product's own stylesheets
// rather than against a comment:
//
//   - every `--brand-*` name the stylesheets declare is in the campaign
//     vocabulary (`TestEveryBrandTokenTheProductDeclaresIsInTheCampaignVocabulary`),
//     so a brand token added to tokens.css and forgotten here fails the build
//     instead of being silently unreachable;
//   - every protected family protects at least one name the stylesheets declare
//     (`TestEveryProtectedFamilyProtectsSomethingTheProductDeclares`), so a
//     family cannot be theatre;
//   - and no campaign token is matched by a protected family
//     (`TestNoCampaignTokenIsAlsoProtected`), because a name that is both would
//     make "who owns this" a question with two answers.
//
// # Why the default is "the product's"
//
// The vocabulary is a **closed allowlist**: a name a campaign may set is named in
// `campaignVocabulary`, and everything else belongs to the product. The
// alternative — an allowlist of protected names and an open set of everything
// else — puts the accessibility contract on the *deny* path, where a token added
// to tokens.css tomorrow is campaign-settable until somebody remembers to add it
// to a list. §4.12.2's wording is the argument: the generator must never emit a
// protected name, and a generator that can only emit names from an allowlist has
// that property by construction rather than by review.
//
// The two directions are therefore not symmetric in cost, and that is the point.
// Refusing a name the product has not heard of is a nuisance for exactly one
// author, once, with a message that names the token. Allowing a name that should
// not have been allowed is an unreadable UI for every reader of that campaign,
// with no gate able to see it across every campaign.

// The three names §4.12.1's left column holds, and the two font slots beside
// them.
//
// Constants rather than string literals at the use sites because they appear in
// the vocabulary, in the pair rule and in the refusal messages, and a spelling
// that differs by one character between those is a manifest that validates
// against a different name than the one the error reports.
//
// `brandImage` is the third of the table's left column and the only one whose
// value is not a colour, which is why it has no partner and why the generator
// treats it as a path. It joins the vocabulary because `theme.css` declares it
// — `TestEveryCampaignTokenIsDeclaredByTheProduct` is what noticed the absence
// and is the reason a token nothing declared is not offered to a GM.
const (
	brandAccent = "--brand-accent"
	brandInk    = "--brand-accent-ink"
	brandImage  = "--brand-header-image"
)

// The two font slots §4.12.1's left column names: "`--font-prose` /
// `--font-ui` via `@font-face` (system stack always retained as fallback)".
//
// **These are not vocabulary entries, and the reason is the sentence in
// parentheses.** A campaign sets a font by naming a file in the manifest's
// `fonts:` section; the server writes the `@font-face` and composes the
// declaration itself, so that the product's system stack is *always* the tail of
// the family list. A campaign naming `--font-ui` under `tokens:` is refused
// with `reasonFontSection` rather than accepted, because accepting it would mean
// either honouring a stack with no fallback (a font that fails to load is then a
// broken UI, which §4.12.3's whole direction forbids) or honouring it and
// silently appending the stack behind the GM's back.
//
// `--font-mono` is a third case and is **protected**: §5.2 marks it not
// overridable because code and dice expressions must stay monospaced, so
// `--font-mono` is in the right-hand column even though the record's table does
// not print it there.
const (
	fontProse = "--font-prose"
	fontUI    = "--font-ui"
	fontMono  = "--font-mono"
)

// owner is which side of §4.12.1's table a token name falls on.
//
// Two states and not three, because there are only two answers: a campaign may
// set this name, or it may not. §4.12.1's right-hand column is not a third kind of
// owner — it is the *same* answer as everything else, with a different
// explanation attached, and `protectedPrefixes` is what carries that explanation.
type owner uint8

const (
	// ownerProduct is the product's token: the campaign may not set it, and no
	// manifest naming it is applied.
	ownerProduct owner = iota

	// ownerCampaign is a token a campaign theme may set: the generator emits it
	// and the manifest's value reaches the stylesheet.
	ownerCampaign
)

// String names the owner for a log line and a refusal message.
//
// A method rather than a bare constant in the messages because the two spellings
// are the two halves of what a GM reads when their theme is refused, and a
// refusal that says "owner=1" tells them nothing.
func (o owner) String() string {
	if o == ownerCampaign {
		return "campaign"
	}

	return "product"
}

// campaignVocabulary is the closed set of names a campaign manifest may set, and
// the only set the generator ever emits.
//
// A map rather than a slice because the lookup is the enforcement: a name's
// presence *is* the permission, so a slice would need a membership test beside it
// and the two could disagree. The values are the partner name, which is how §4.12.3's
// "the pair" is encoded — see `document.theme`.
//
// **An empty partner means "no partner"**, and the both-or-neither rule reads
// that as the absence of a rule rather than as a rule about the empty string.
// Two of the three names have partners and one does not: `--brand-accent` and
// `--brand-accent-ink` are a pair whose halves are measured together, and
// `--brand-header-image` is a path with nothing to be measured against. Encoding
// that as an empty string rather than as a second map keeps one lookup.
var campaignVocabulary = map[string]string{
	brandAccent: brandInk,
	brandInk:    brandAccent,
	brandImage:  "",
}

// fontPrefix is the family §4.12.1's left column makes overridable "via
// `@font-face`", and it is **not** protected — which is the interesting entry in
// this table, because the generated sheet does emit these names.
//
// The protected list is a promise about what the *generator* never emits, and a
// generator that emits `--font-prose` would violate it. So `--font-prose` and
// `--font-ui` are not on either side of §4.12.1's table: they are overridable,
// through a channel this package owns, and a manifest that names one directly is
// refused with the reason that points at that channel. That is a third answer
// rather than a second, and `fontOverride` is where it lives.
const fontPrefix = "--font-"

// protectedPrefixes is §4.12.1's right-hand column: the tokens no campaign may
// ever set, because a stylesheet that could set them would break 1.4.11, 1.4.3
// or 2.5.5 with no gate able to catch it across every campaign.
//
// Spelling, from the record and from tokens.css's own table:
//
//	--text, --text-muted, --text-subtle   --type-scale, --space-scale, --target-min
//	--border, --border-subtle, --border-w  --dur-*, --radius-*
//	--focus-ring, --focus-ring-offset     --callout-surface, --callout-border,
//	                                         --callout-secret, --callout-revealed
//
// The record writes some of them as exact names and some as families, and this
// list is **all prefixes**, which over-protects by design: `--target-min-x`
// matches here even though no such token exists. Over-protection is the cheap
// direction for an accessibility contract — it refuses a name nobody has — while
// under-protection is the expensive one.
//
// `--font-mono` is here rather than under `fontPrefix`'s cousins because §5.2
// marks it not overridable: code and dice expressions must stay monospaced. It
// is the one `--font-*` name in the right-hand column, and it is the reason the
// font family is a *rule* rather than "everything starting `--font-` is
// overridable".
//
// `--brand-*` is deliberately **not** a protected prefix. The brand names are the
// campaign's, and protecting them would invert the table; the check that keeps the
// two sets honest is `TestNoCampaignTokenIsAlsoProtected`.
var protectedPrefixes = []string{
	"--text-",
	"--border",
	"--focus-ring",
	"--callout-",
	"--dur-",
	"--radius-",
	fontMono,
	"--type-scale",
	"--space-scale",
	"--target-min",
}

// ownerOf reports which side owns a token name.
//
// Total by construction: a name the vocabulary does not name is the product's.
// That default is the enforcement, and `ownerOf` is the one function a reader
// should look at to see why a name a campaign has never heard of is refused.
func ownerOf(name string) owner {
	if _, ok := campaignVocabulary[name]; ok {
		return ownerCampaign
	}

	return ownerProduct
}

// protected reports whether name is in §4.12.1's right-hand column.
//
// Separate from `ownerOf` because it answers a different question — not "may a
// campaign set this?" (which `ownerOf` answers for every name) but "is this one of
// the names the accessibility contract is written in?", which is what a refusal
// message says and what the stylesheet cross-checks assert.
func protected(name string) bool {
	return slices.ContainsFunc(protectedPrefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// fontOverride reports whether name is in §4.12.1's left column's font row, and
// therefore reachable only through the manifest's `fonts:` section.
//
// **Its own predicate rather than a slice, because the set is a family and the
// family is a prefix.** Three answers rather than two, and the middle one is
// `reasonFontSection`: a campaign that wrote `--font-ui` under `tokens:` meant to
// set a font, the record says that is allowed, and telling them the token is
// semiplane's own would be a true sentence that sends them to the wrong file.
//
// `--font-mono` returns false here and true in `protected`, which is §5.2's
// exception and the reason the two functions are separate: a reader checking
// "is this overridable?" and a reader checking "is this protected?" get opposite
// answers for it, and the order `claimToken` asks them in is the difference
// between the two messages a GM could receive.
func fontOverride(name string) bool {
	return name != fontMono && strings.HasPrefix(name, fontPrefix)
}

// CampaignTokens returns every name a campaign manifest may set, sorted.
//
// Exported for the tests that hold the vocabulary against the product's own
// stylesheets, and for a future campaign-overview surface that has to tell a GM
// which names exist. A sorted clone rather than the map, so a caller cannot reach
// the table and mutate it.
func CampaignTokens() []string {
	names := make([]string, 0, len(campaignVocabulary))
	for name := range campaignVocabulary {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// ProtectedTokens returns §4.12.1's right-hand column, as the prefixes it is
// matched with, in declaration order.
//
// Exported for the same reason as `CampaignTokens`: the claim that each family
// protects something is checkable only from outside the package.
func ProtectedTokens() []string {
	return slices.Clone(protectedPrefixes)
}
