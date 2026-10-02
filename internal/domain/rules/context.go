package rules

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/semiplane/semiplane/internal/domain"
)

// ErrInvalidSeed is a seed that is not exactly `SeedLen` bytes, or not the hex of
// exactly that many.
//
// Refused rather than repaired for the reason `ParseRole` refuses rather than folds:
// a seed that is the wrong length is a hub bug or a hand-edited column, and
// silently padding or truncating it produces a campaign whose rolls nobody can
// reproduce from the value they can see.
var ErrInvalidSeed = fmt.Errorf("%w: a seed must be exactly %d bytes", ErrMalformedSystem, SeedLen)

// ErrInvalidRole is a `Context` built with a role this build does not have a name
// for.
//
// Distinct from `domain`'s own parse failure because the question is different.
// `domain.ParseRole` refuses a stored column value; this refuses a role on its way
// into rule code, where the alternative is a system branching on a value that is
// neither GM nor player and quietly treating it as the lesser of the two.
var ErrInvalidRole = fmt.Errorf(
	"%w: the context carries a role this build does not have",
	ErrMalformedSystem,
)

// SeedLen is the length of a `Seed` in bytes.
//
// 32 bytes, which is twice the width of the PCG state `Rand` seeds and four times
// the width of a campaign's identifier. It is not a security parameter — a seed is
// not a secret — and 32 is chosen because it is the size of a SHA-256 input block
// boundary, so the derivation below hashes the seed without padding it, and because
// it is small enough to put in a status page and large enough that a hub which
// minted one per campaign will not collide with a birthday argument in any lifetime
// this project has.
const SeedLen = 32

// Seed is the entropy one resolution draws from.
//
// A fixed-size array rather than a string so that a seed cannot be "the empty
// string" by accident and cannot carry a length a reader has to trust: 32 bytes of
// hex is 64 characters and is the whole of it.
//
// A seed is **not a secret**, and that is the point rather than an accident.
// §16.3's open question says the seeded RNG is supplied through `Context` "which
// makes a per-session seed + roll log auditable at no extra cost" — an audit trail
// nobody can check is not an audit trail, so the seed is printed wherever the
// fingerprint that gates a resume is printed.
//
// There is deliberately no "unseeded" value. The zero `Seed` is a legal seed that
// produces the same numbers on every machine forever, and a system that has no
// randomness available has no ambient source to fall back on (§10.4) rather than a
// sentinel that reads as one.
type Seed [SeedLen]byte

// NewSeed returns the seed built from exactly SeedLen bytes.
//
// Exactly. A shorter slice is refused rather than right-padded, because
// right-padding makes `[]byte{1}` and `[]byte{1, 0}` the same seed — two different
// hub bugs producing one campaign's rolls — and a longer one is refused because
// truncating it silently discards entropy the caller believed was there. Both
// mistakes are wire-invisible: the value that reaches the resolver is what it was
// always going to be.
func NewSeed(entropy []byte) (Seed, error) {
	if len(entropy) != SeedLen {
		return Seed{}, fmt.Errorf("%w: %d bytes, exactly %d are required",
			ErrInvalidSeed, len(entropy), SeedLen)
	}

	var seed Seed
	copy(seed[:], entropy)

	return seed, nil
}

// ParseSeed reads the hex form `String` writes.
//
// Hex rather than base64 because a hex seed is 64 characters of `[0-9a-f]` that a
// human can compare two of by eye, and because a seed is going to appear in a
// status page, a fingerprint and a log line where `+` and `/` would be one more
// thing to quote.
//
// **Lowercase only.** `hex.DecodeString` accepts either case, and for a value that
// exists to be transcribed by hand one spelling is worth having: an audit that reads
// "the seed was `A1B2…`" when the column holds `a1b2…` is a thread nobody follows.
// This is the same refusal `ParseRole` makes for the same reason, and the same one
// `ParseVisibility` makes: a value this project stores has one spelling, and the
// second one is a value somebody typed by mistake.
func ParseSeed(text string) (Seed, error) {
	if len(text) != hex.EncodedLen(SeedLen) {
		return Seed{}, fmt.Errorf("%w: %d hex characters, exactly %d are required",
			ErrInvalidSeed, len(text), hex.EncodedLen(SeedLen))
	}

	raw, err := hex.DecodeString(text)
	if err != nil {
		// The underlying error's text names the character it choked on. That text
		// came from a column or a command line rather than from a client, so quoting
		// it is safe — and it is the half an operator needs, since "not a seed" does
		// not tell them which character to look at.
		return Seed{}, fmt.Errorf("%w: %w", ErrInvalidSeed, err)
	}

	if text != strings.ToLower(text) {
		return Seed{}, fmt.Errorf(
			"%w: %q is not the lowercase hex form String writes",
			ErrInvalidSeed,
			text,
		)
	}

	// Length is already checked, so this cannot fail; the check is kept because a
	// second caller of `NewSeed` would otherwise have to trust that.
	return NewSeed(raw)
}

// String returns the seed as lowercase hex.
//
// The whole 64 characters, not an abbreviation: a shortened seed in a log line is
// one nobody can reproduce a campaign from, and reproducibility is the single
// property this type exists to have.
func (s Seed) String() string {
	return hex.EncodeToString(s[:])
}

// randDomain versions the derivation from a seed and a label to a deterministic
// source.
//
// Versioned, and for the reason `state.go`'s `documentMagic` and `ruleset.go`'s
// `fingerprintFormat` are: the derivation is a promise about a campaign's history.
// Change the mixing function without changing this constant and every stored
// campaign silently re-rolls — a table that resumes, draws from a different stream,
// and reports numbers nobody asked for, with no error anywhere. The constant is the
// switch that turns that from invisible into refused.
//
// It is a domain separator rather than just a comment, because it is hashed: it is
// what stops a label from colliding with the seed bytes that precede it.
const randDomain = "semiplane/rules/rng/v1"

// Context is everything a resolution is given besides its state and its intent.
//
// **The whole of it is four fields, and that is the design.** There is no clock, no
// database handle, no filesystem root, no `*rand.Rand` and no logger. S-10.4 makes
// rule code deterministic and there is no sandbox to enforce it, so the enforcement
// available to a *type* is subtraction: a function this package hands a system has
// nowhere to receive a wall clock, nowhere to receive a handle, and no ambient
// randomness except the one derived here. `time.Now` remains a call a plugin *can*
// make — a lint rule and the conformance suite are what refuse that — but there is
// no longer a field through which a wall clock arrives, which is the one hole that
// would have made the other rules decorative.
//
// The fields are the four the record names (§10.3) and they are all identity:
// which campaign, which actor, what may that actor do, and what may it draw from.
// A `Context` is a value, built fresh per intent by the hub, so two resolutions
// cannot share one — which is what makes the shared-`*rand.Rand` failure
// unrepresentable rather than merely discouraged.
type Context struct {
	// Campaign is `domain.Campaign.ID`. Present so a system can scope a lookup or a
	// log line, and never a capability: nothing in this package authorises anything
	// on the strength of it (ADR 0024 — authorisation is a gate the route mounts).
	Campaign int64

	// Actor is `domain.User.ID` of the account whose intent is being resolved.
	//
	// It comes from the connection the access gate admitted, not from the frame
	// (`hub.go` is explicit that a client frame has no field in which to name a
	// campaign, and the same is true of an actor), so a system cannot be told which
	// player is rolling by anything the player sent.
	Actor int64

	// Role is that actor's role in this campaign, and it is how §7.2's GM-only
	// operations are enforced *in rules* without a database round trip.
	//
	// Enforcement lives at the route and at the op, not here: `Role` exists so a
	// system's own rules can branch on it, and a system that lets a player do
	// something a GM may do has a bug the conformance suite's GM-only check catches
	// rather than a hole this type opens.
	Role domain.Role

	// Seed is this resolution's entropy. See `Seed`: not a secret, and the thing
	// that makes a roll log auditable (§16.3).
	Seed Seed
}

// NewContext returns a context for one resolution.
//
// A constructor rather than four field assignments because the actor identity and
// the campaign identity are the two fields a caller must get right and the two a
// careless caller would copy from somewhere else, and because this is the one place
// where a `Role` can be refused before it reaches a system that would branch on it.
//
// A role this build does not have a name for is refused rather than passed on. A
// system that branches `Role == RoleGM` against a value that is neither GM nor
// player treats it as a player, and S-8 answers "no access" without saying why —
// which means the safe answer is the one that never reaches the branch.
func NewContext(campaign, actor int64, role domain.Role, seed Seed) (Context, error) {
	if !role.Valid() {
		return Context{}, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	return Context{Campaign: campaign, Actor: actor, Role: role, Seed: seed}, nil
}

// Rand returns a deterministic pseudo-random source for one named draw.
//
// **Named, and that is the entire mechanism.** The returned source is derived from
// `c.Seed` and `label` alone, by a pure function, so:
//
//   - The same `Context` and the same label produce the same numbers, on every
//     machine and in every build of the toolchain. That is S-14.6's property, and it
//     is a consequence of the signature rather than a discipline.
//   - Two different labels produce unrelated streams. A system that draws twice
//     names the two draws (`"attack/1"`, `"attack/2"`), and a replay reproduces
//     both.
//   - The source is **not** the package-level `math/rand/v2` functions. Those read
//     a process-global source seeded from the OS at startup, so a plugin calling
//     `rand.IntN` directly gets numbers that differ per process — a roll nobody can
//     audit and a `campaign_state` that resumes to a different game. P1d's
//     determinism audit refuses those calls; this is what it points them at.
//
// The empty label is legal and means "the one draw in this resolution". It is not
// an error because a resolution that needs exactly one draw should not have to
// invent a name for it, and it is safe because the failure it invites — two draws
// under one name — produces the *same* numbers twice, which is loud in a roll log
// rather than silent. A system that needs two names them.
//
// The returned `*rand.Rand` is **not safe for concurrent use**, which is correct
// rather than a caveat: one source belongs to one resolution, and a system
// resolving two intents at once has two `Context` values and two sources. Nothing
// in this package hands the same source to two goroutines, because nothing in it
// hands a source to anything but the caller who asked.
//
// The type is `*rand.Rand` and not a narrow interface with `Roll` and `Sum` methods
// because those methods would be a dice vocabulary in this package — the exact
// assumption §10.3 forbids. A d20 system wants `1 + IntN(20)`; a 2d6-pool system
// wants six `IntN(6)` results counted against a target. `Rand` hands over the
// substrate and each system composes its own notation from it.
//
// One dependency worth stating: reproducibility rests on `rand.PCG`'s output, which
// the standard library holds fixed with a golden-output regression test rather than
// with an API promise. If that ever changed, the damage is bounded and it is worth
// knowing what it is: the draws a campaign has **already** made are recorded in its
// state and its log and are not re-derived, so nothing misresolves — but its next
// roll would differ from what a reader with the same seed would compute, which is
// precisely the audit §16.3 wants. That is the trade for depending on a fixed-width
// generator rather than writing one here, and it is the trade this package would
// revisit only with a record.
func (c Context) Rand(label string) *rand.Rand {
	// Domain, then the fixed-width seed, then the label — and because the seed is
	// always exactly SeedLen bytes the split between the last two is unambiguous,
	// so no length prefix and no separator is needed to keep `(seed, "ab")` distinct
	// from `(seed, "a") + "b"`. The buffer is one allocation of the exact size.
	buffer := make([]byte, 0, len(randDomain)+SeedLen+len(label))
	buffer = append(buffer, randDomain...)
	buffer = append(buffer, c.Seed[:]...)
	buffer = append(buffer, label...)

	digest := sha256.Sum256(buffer)

	//nolint:gosec // G404 asks for unpredictability; this is the opposite on purpose.
	// A rule resolver must draw from a source that is a *pure function of the
	// campaign's recorded seed* — S-14.6's property, and §16.3's roll log, both of
	// which are unbuildable if the numbers cannot be re-derived. `crypto/rand` here
	// would make every resolution unreproducible, and S-10.4 forbids a plugin
	// reaching it directly for the same reason it is refused here.
	return rand.New(rand.NewPCG(
		binary.LittleEndian.Uint64(digest[0:8]),
		binary.LittleEndian.Uint64(digest[8:16]),
	))
}
