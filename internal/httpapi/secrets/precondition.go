package secrets

// The precondition: what a reveal must present, what it presents, and what happens
// when the two disagree.
//
// S-6.2 is the requirement and it is short: *writes carry `If-Match`; a mismatch
// returns 412 with the current body and its hash, and records nothing.* §5.6.4 adds
// that a reveal goes through "the same authorization and `If-Match` precondition as
// any other write", which is what makes this a copy rather than a new rule.
//
// Three statuses come out of it and each is a different kind of statement:
//
//   - **428** — the request named no validator. RFC 6585's answer, and the reason
//     it is not a 200 is in `writePreconditionRequired`: without the header the
//     request is an unconditional overwrite wearing a reveal's clothes.
//   - **412** — the request named a validator and it does not describe what is on
//     disk now. The page is *there* and is *not* what the GM was looking at.
//   - **204** — the request named the validator and it matches.
//
// The one that is easy to get wrong, and the one this file exists to prevent, is the
// third turning into the second silently. There is no code path here that falls
// through to a write when the comparison fails or is skipped: `reveal` in
// `secrets.go` is not reached, and a validator that cannot be parsed counts as a
// mismatch rather than as an absent check.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
)

// The header names, named because `net/http` has no constants for them.
const (
	ifMatchHeader = "If-Match"
	etagHeader    = "ETag"
)

// weakPrefix marks a validator as weak (RFC 9110 §8.8.3), and S-5.3's spelling puts
// it on every validator this server mints.
const weakPrefix = "W/"

// anyValidator is `If-Match: *`, which RFC 9110 §13.1.1 defines as "the resource
// exists" — the one precondition that asks no question about *which* version.
const anyValidator = "*"

// validator is the `ETag` for one page's bytes to one campaign's GM.
//
// Derived from `content.CacheKey.ETag` rather than computed here, and that is
// load-bearing rather than tidy: the wiki route and the editor advertise the same
// function, and a GM who read a page and then revealed a secret on it must be able
// to take the validator from **any** of the three and have it match the others. Two
// derivations of "what is this page's validator" is how a GM's unredacted validator
// ends up being the one a player is compared against (ADR 0016).
// `TestTheRevealAndTheWikiRouteAgreeOnTheValidator` is the guard, and it is the
// reason this duplication is tolerable: a hash function cannot be asserted equal to
// itself, so the property asserted is that **three routes agree**.
//
// `IncludeSecrets` is true and always true, because the route is GM-only (S-6.5) and
// a reveal is performed by the GM. It is passed explicitly rather than derived from
// a request, for the reason the wiki route derives it from the access tier and from
// nothing else: a value a request could influence is a value a request can lie about.
func validator(campaignID int64, pagePath, digest string) string {
	return content.CacheKey{
		CampaignID:     campaignID,
		Path:           pagePath,
		ContentHash:    digest,
		IncludeSecrets: true,
	}.ETag()
}

// contentHash is the hex SHA-256 of a page's bytes.
//
// The same digest `content.Indexer` records in `pages.content_hash` and the same one
// the validator above salts. `content` keeps its own copy unexported, so this is a
// second *derivation* of one fact — the same duplication `edit/precondition.go`
// carries, with the same answer: the guard against the two drifting apart is not
// this function but the cross-route equality test, because the thing that matters
// is that the routes agree, not that the hash function is the hash function.
func contentHash(raw []byte) string {
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// matches reports whether an `If-Match` header names this validator.
//
// A list, because a client may send several — after a merge, or from a tab holding
// an old copy — and it must match any of them. `*` matches, and it is correct rather
// than a loophole: on a `PUT` it is RFC 9110's "if the resource exists" precondition,
// and the resource's existence is already established (this route read the file
// before getting here, and a page that is not there is a 404). It is a *weaker*
// precondition than a digest, and a client that sends it has chosen that; what S-6.3
// forbids is a *server-side* fallback to last-write-wins, and there is none.
//
// ## The comparison is weak, against RFC 9110 §13.1.1
//
// `If-Match` is specified to use the *strong* comparison function, and this does
// not. The reason is that the validator this server mints is weak — S-5.3 spells it
// `W/"…"`, and there is no strong form of it anywhere in the project — and strong
// comparison requires both sides to be strong. A strict implementation would find
// `W/"abc"` never matches `W/"abc"`, every reveal would be a 412, and the route would
// be a working precondition that refuses every request. The project's answer is to
// compare the opaque values with the `W/` prefix stripped from both sides, which is
// what the editor's `If-Match` and the wiki route's `If-None-Match` revalidation
// already do, so a client that learned to drop the prefix from one route is not
// surprised by another.
//
// A header that cannot be parsed as a list of entity-tags does not match. That is
// not a 400: a 400 would say "your request is malformed", and the useful truth is
// "the version you named is not the version on disk", which is what a client can act
// on by re-reading the page. It is also the safe direction — an unparseable header
// can never authorise a write, so no malformation is a way through the check
// (S-6.3).
func matches(header, want string) bool {
	header = strings.TrimSpace(header)
	if header == "" || want == "" {
		return false
	}

	for candidate := range strings.SplitSeq(header, ",") {
		switch strings.TrimSpace(candidate) {
		case anyValidator:
			return true
		case "":
			// An empty element is skipped rather than treated as a match and
			// rather than treated as a failure. RFC 9110 §13.1.1 defines the
			// condition as "any of the listed validators match", so a list with a
			// stray comma is a list with a valid element in it — and refusing the
			// whole header would turn a typo into a 412 for a client whose validator
			// was right.
			continue
		default:
			if opaque(strings.TrimSpace(candidate)) == opaque(want) {
				return true
			}
		}
	}

	return false
}

// opaque is a validator's quoted value, with the weak marker removed.
//
// The quotes are kept: a validator's value is the quoted string, and stripping the
// quotes as well would make `"` and an unquoted value compare equal. A candidate with
// no quotes at all — `If-Match: garbage` — therefore never matches a properly-formed
// validator, which is the intended outcome for a malformed header.
func opaque(validator string) string {
	return strings.TrimPrefix(validator, weakPrefix)
}
