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

// The two names of the brand pair, §4.12.1's only overridable colours.
//
// Constants rather than string literals at the use sites because they appear in
// the vocabulary, in the pair rule and in the refusal messages, and a spelling
// that differs by one character between those is a manifest that validates
// against a different name than the one the error reports.
const (
	brandAccent = "--brand-accent"
	brandInk    = "--brand-accent-ink"
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
// and the two could disagree. The values are the partner name, which is how §6.2's
// "the pair" is encoded — see `Manifest.brand`.
//
// Two entries, and §4.12.1 names exactly two overridable colours. `--brand-header-image`
// is the third name in that table's left column and it is **absent on purpose**:
// nothing in the product declares it, so a manifest setting it would emit a
// declaration no rule reads — a theme that appears to work and does nothing. It
// joins the vocabulary when tokens.css declares it, and
// `TestEveryCampaignTokenIsDeclaredByTheProduct` is what notices the difference.
var campaignVocabulary = map[string]string{
	brandAccent: brandInk,
	brandInk:    brandAccent,
}

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
// `--font-prose` and `--font-ui` are here because §4.12.1's left column makes
// fonts overridable *through* the generator, and this work item does not generate
// them: the product declares both as a system stack in shell.css and there is no
// `--brand-font-*` token for an override to land in, so a campaign setting
// `--font-ui` is refused rather than accepted and dropped. See the work item's
// report — the hook the record asks for is a shell.css change, and the
// vocabulary should not claim a token whose override has nowhere to go.
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
	"--font-",
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
