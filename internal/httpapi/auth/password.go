// Package auth holds the credential primitives: password hashing, session
// token minting, and the two cookies a browser carries.
//
// Nothing here reads configuration or touches the database. A caller passes in
// what it already read, which keeps the package testable with no config and no
// schema, and it means the two values that must never be observed anywhere
// else — a password and a raw session token — each have exactly one exit, and
// both exits are in this file.
//
// The threat model is split across two records: ADR 0021 covers how a password
// is stored, ADR 0022 covers why `auth_sessions` keys on a hash of the token
// rather than the token itself.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// pbkdf2Iterations is the work factor for new password hashes, and it is
	// OWASP's PBKDF2-HMAC-SHA256 recommendation.
	//
	// It looks absurd. It is roughly half a second of CPU per hash, which is
	// paid once at `semiplane admin create` and once per login — and it is the
	// multiplier an offline attacker has to work through to turn a stolen
	// database file into passwords. The next reader will see a slow test suite
	// and be tempted to drop this to 10,000 so their table-driven cases finish
	// quickly. Do not. The tests that do not need the real work factor take a
	// lower count through an unexported parameter instead; see hashPassword.
	pbkdf2Iterations = 600_000

	// pbkdf2MaxIterations is the ceiling parsePasswordHash accepts. It exists
	// so a corrupted or hostile row cannot turn every login into a several-
	// second stall: an honest hash never exceeds pbkdf2Iterations, so anything
	// above this is not one of ours.
	pbkdf2MaxIterations = 10_000_000

	// saltLength and derivedKeyLength are RFC 8018's recommendation for
	// PBKDF2-HMAC-SHA256: a 16-byte salt, and a key at least as long as the
	// hash's output.
	//
	// The salt is per-hash and from crypto/rand, which is what makes two users
	// with the same password produce different stored values and makes a
	// precomputed dictionary useless.
	saltLength       = 16
	derivedKeyLength = 32

	// minPasswordLength is 1. An empty password is rejected, because a user
	// account with no password is an account anyone can claim: the guard is
	// here rather than in the CLI so every caller inherits it.
	minPasswordLength = 1

	// maxPasswordLength bounds a single password at 1 KiB.
	//
	// The honest reason is request size, not KDF cost. HMAC folds the password
	// into a fixed-width padded block, so a multi-megabyte password costs
	// roughly one extra compression pass over those megabytes — microseconds
	// next to the iteration count. What it does cost is that the whole string
	// is held in memory on every login attempt, so an unbounded body turns the
	// login route into a memory-pressure lever. 1 KiB is far past any
	// passphrase a human will type and far below anything legitimate.
	maxPasswordLength = 1024
)

const (
	// pbkdf2Algorithm is the algorithm token in the encoded hash, and the
	// only one this package writes.
	//
	// The encoded form is self-describing on purpose:
	//
	//	pbkdf2-sha256$<iterations>$<base64 salt>$<base64 derived key>
	//
	// Every parameter travels with the hash, so raising the work factor
	// affects new hashes without invalidating existing ones, and a future
	// algorithm gets a new token rather than a rehash nobody can trigger.
	// Verification always reads the parameters back out of the string — see
	// VerifyPassword.
	pbkdf2Algorithm = "pbkdf2-sha256"

	// passwordHashFields is the field count of the encoded form above.
	passwordHashFields = 4

	// passwordHashSeparator delimits the encoded form's fields. It is not in
	// the standard base64 alphabet, so no encoded component can contain it and
	// the split is unambiguous.
	passwordHashSeparator = "$"
)

var (
	// ErrInvalidPassword means the password did not match its stored hash.
	//
	// The handler at the login route maps this to a single generic failure, so
	// a caller must not turn it into copy that distinguishes "wrong password"
	// from "no such user".
	ErrInvalidPassword = errors.New("auth: password does not match its hash")

	// ErrMalformedPasswordHash means the stored hash could not be parsed.
	//
	// Distinct from ErrInvalidPassword because they demand different responses:
	// a wrong password is an ordinary outcome of a login form, whereas a
	// malformed hash means the row is corrupt or was written by something other
	// than this package. Folding the two together would turn a database
	// problem into a silent stream of failed logins.
	ErrMalformedPasswordHash = errors.New("auth: stored password hash is malformed")
)

// passwordHash is a parsed encoded hash: the parameters the key was derived
// with, plus the derived key itself.
//
// It is a struct rather than three return values because the three travel
// together from the parser to exactly one comparison, and a tuple that gets
// reordered once is a comparison against the wrong buffer.
type passwordHash struct {
	iterations int
	salt       []byte
	derived    []byte
}

// HashPassword derives a new hash for the password and returns it in the
// encoded, self-describing form to store in `users.password_hash`.
//
// An empty or over-long password is refused. Every call uses a fresh
// crypto/rand salt, so two calls with the same password return different
// strings, and neither reveals the password.
func HashPassword(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}

	return hashPassword(password, pbkdf2Iterations)
}

// VerifyPassword reports whether password is the one encodedHash was derived
// from. It returns nil on a match, ErrInvalidPassword on a mismatch, and
// ErrMalformedPasswordHash when the stored hash cannot be parsed.
//
// The encoded string is parsed first and the parameters inside it — the
// iteration count, the salt, the key length — are what the comparison uses. A
// row claiming a different work factor therefore still verifies, which is the
// only way raising pbkdf2Iterations can ever happen without logging everybody
// out.
//
// The derived key is compared with crypto/subtle.ConstantTimeCompare rather
// than bytes.Equal. It has already been parsed by the time the comparison
// runs, and the lengths are equal by construction, so ConstantTimeCompare
// cannot leak a length through an early return; what it protects is the
// per-byte timing of the first difference, which is what an attacker with a
// stolen database measures against a hash they control the input to.
func VerifyPassword(encodedHash, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}

	parsed, err := parsePasswordHash(encodedHash)
	if err != nil {
		return err
	}

	derived, err := pbkdf2.Key(
		sha256.New,
		password,
		parsed.salt,
		parsed.iterations,
		len(parsed.derived),
	)
	if err != nil {
		return fmt.Errorf("auth: derive password key: %w", err)
	}

	if subtle.ConstantTimeCompare(derived, parsed.derived) != 1 {
		return ErrInvalidPassword
	}

	return nil
}

// validatePassword applies the length policy.
//
// Both HashPassword and VerifyPassword run it, so a caller cannot get a cheap
// verification path by skipping straight to the KDF, and so the login route
// rejects an oversized body before spending half a second of CPU on it.
func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return errors.New("auth: password must not be empty")
	}

	if len(password) > maxPasswordLength {
		return fmt.Errorf(
			"auth: password is longer than the %d-byte limit", maxPasswordLength,
		)
	}

	return nil
}

// hashPassword is HashPassword with an explicit work factor.
//
// The parameter exists so tests can exercise the encoding, the parser and the
// round trip without paying 600,000 iterations each time — the tests call this
// with a low count and verify through the exported VerifyPassword, which proves
// the verifier takes the count from the encoded string.
//
// It is a parameter rather than a package-level variable on purpose. A global
// knob is mutable from any file in the package, leaks between tests, and is one
// careless assignment away from shipping a 10,000-iteration default.
func hashPassword(password string, iterations int) (string, error) {
	salt := make([]byte, saltLength)

	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read password salt: %w", err)
	}

	derived, err := pbkdf2.Key(sha256.New, password, salt, iterations, derivedKeyLength)
	if err != nil {
		return "", fmt.Errorf("auth: derive password key: %w", err)
	}

	// base64.StdEncoding, not the URL-safe form: this string lives in a
	// database column and in a JSON payload, neither of which is a URL. Strict
	// decoding on the way back in keeps a hand-edited hash from being accepted
	// with its non-significant trailing bits rewritten.
	fields := []string{
		pbkdf2Algorithm,
		strconv.Itoa(iterations),
		base64.StdEncoding.Strict().EncodeToString(salt),
		base64.StdEncoding.Strict().EncodeToString(derived),
	}

	return strings.Join(fields, passwordHashSeparator), nil
}

// parsePasswordHash reads the encoded form back into its components, or
// explains which component is wrong.
//
// Every component is validated and nothing is guessed or defaulted. A parser
// that fell back to a default iteration count would silently accept a row whose
// work factor was truncated to one; one that skipped the salt length check
// would let a zero-length salt through, which is the single change that turns
// PBKDF2 back into one hash per password for the whole table.
//
// The error messages deliberately carry no field *values*. The encoded string
// is stored data, and it can be corrupt or hostile; interpolating it into an
// error is how untrusted text ends up in a log line.
func parsePasswordHash(encoded string) (passwordHash, error) {
	fields := strings.Split(encoded, passwordHashSeparator)
	if len(fields) != passwordHashFields {
		return passwordHash{}, fmt.Errorf(
			"%w: want %d fields separated by %q, got %d",
			ErrMalformedPasswordHash, passwordHashFields, passwordHashSeparator, len(fields),
		)
	}

	if fields[0] != pbkdf2Algorithm {
		return passwordHash{}, fmt.Errorf("%w: unrecognised algorithm", ErrMalformedPasswordHash)
	}

	iterations, err := strconv.Atoi(fields[1])
	if err != nil {
		return passwordHash{}, fmt.Errorf(
			"%w: iteration count is not an integer", ErrMalformedPasswordHash,
		)
	}

	if iterations <= 0 || iterations > pbkdf2MaxIterations {
		return passwordHash{}, fmt.Errorf(
			"%w: iteration count is outside the accepted range", ErrMalformedPasswordHash,
		)
	}

	salt, err := base64.StdEncoding.Strict().DecodeString(fields[2])
	if err != nil {
		return passwordHash{}, fmt.Errorf("%w: salt is not valid base64", ErrMalformedPasswordHash)
	}

	if len(salt) != saltLength {
		return passwordHash{}, fmt.Errorf(
			"%w: salt is %d bytes, want %d",
			ErrMalformedPasswordHash, len(salt), saltLength,
		)
	}

	derived, err := base64.StdEncoding.Strict().DecodeString(fields[3])
	if err != nil {
		return passwordHash{}, fmt.Errorf(
			"%w: derived key is not valid base64", ErrMalformedPasswordHash,
		)
	}

	if len(derived) != derivedKeyLength {
		return passwordHash{}, fmt.Errorf(
			"%w: derived key is %d bytes, want %d",
			ErrMalformedPasswordHash, len(derived), derivedKeyLength,
		)
	}

	return passwordHash{iterations: iterations, salt: salt, derived: derived}, nil
}
