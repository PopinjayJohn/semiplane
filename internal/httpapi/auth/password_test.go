package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/auth"
)

// TestHashPasswordRoundTripsAndIsSelfDescribing exercises the real work factor
// on purpose: the encoded form is what gets stored, so a change to its shape is
// a migration, and this is the test that would notice.
func TestHashPasswordRoundTripsAndIsSelfDescribing(t *testing.T) {
	t.Parallel()

	const password = "a passphrase with spaces"

	encoded, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v, want nil", err)
	}

	if err := auth.VerifyPassword(encoded, password); err != nil {
		t.Errorf("VerifyPassword() error = %v, want nil", err)
	}

	// Four $-separated fields, algorithm token first. A hash that parses only
	// because the package remembers the parameters it used is not
	// self-describing, and raising the work factor would then invalidate every
	// stored hash instead of only new ones.
	fields := strings.Split(encoded, "$")
	if len(fields) != 4 {
		t.Fatalf("encoded hash = %q, want 4 $-separated fields, got %d", encoded, len(fields))
	}

	if fields[0] != "pbkdf2-sha256" {
		t.Errorf("algorithm = %q, want %q", fields[0], "pbkdf2-sha256")
	}

	if !strings.HasPrefix(fields[1], "600000") {
		t.Errorf("iterations = %q, want the 600000 OWASP recommendation", fields[1])
	}

	if encoded == password {
		t.Error("encoded hash contains the password verbatim")
	}
}

// TestVerifyPasswordRejectsWrongPassword is the assertion that verification is
// actually checking something.
func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	const password = "correct horse battery staple"

	encoded, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v, want nil", err)
	}

	wrong := []string{
		password + " ",
		strings.ToUpper(password),
		password[:len(password)-1],
		"",
	}

	for _, candidate := range wrong {
		err := auth.VerifyPassword(encoded, candidate)

		switch {
		case err == nil:
			t.Errorf("VerifyPassword(%q) = nil, want a mismatch", mask(candidate))

		case candidate == "" && errors.Is(err, auth.ErrInvalidPassword):
			// An empty password never reaches the comparison: the length
			// policy refuses it first. That is the same refusal HashPassword
			// makes, and it is why an empty password can never be a login.
			t.Errorf(`VerifyPassword("") = %v, want the length policy`, err)

		case candidate != "" && !errors.Is(err, auth.ErrInvalidPassword):
			t.Errorf(
				"VerifyPassword(%q) error = %v, want %v",
				mask(candidate),
				err,
				auth.ErrInvalidPassword,
			)
		}
	}
}

// TestHashPasswordUsesAFreshSalt is the assertion that the salt is real.
//
// Without it PBKDF2 degenerates into one hash per password across the whole
// table: a single precomputed dictionary would crack every user at once, and
// two users who picked the same passphrase would be visibly identical in the
// database.
func TestHashPasswordUsesAFreshSalt(t *testing.T) {
	t.Parallel()

	const password = "the same password twice"

	first, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v, want nil", err)
	}

	second, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v, want nil", err)
	}

	if first == second {
		t.Fatalf("two hashes of one password are identical: %q", first)
	}

	saltOf := func(encoded string) string {
		fields := strings.Split(encoded, "$")

		return fields[2]
	}

	if saltOf(first) == saltOf(second) {
		t.Errorf("salts are identical: %q", saltOf(first))
	}
}

// TestHashPasswordRejectsUnusablePasswords covers the length policy. Neither
// case derives anything, so this is fast.
func TestHashPasswordRejectsUnusablePasswords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
	}{
		{name: "empty", password: ""},
		{name: "at the limit", password: strings.Repeat("x", 1024+1)},
		{name: "multi-megabyte", password: strings.Repeat("x", 4<<20)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := auth.HashPassword(tc.password); err == nil {
				t.Errorf(
					"HashPassword(len=%d) error = nil, want a length-policy error",
					len(tc.password),
				)
			}
		})
	}
}

// TestVerifyPasswordRejectsMalformedHashes is the assertion that a stored hash is
// parsed rather than interpreted.
//
// Each case is a hash that a plausible mistake produces: a truncated column, a
// hand-pasted value, a half-migrated row. Every one of them must be an error
// and not a comparison against a zero-length or default buffer, which would be a
// verification that succeeds against anything.
func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	t.Parallel()

	// A known-good hash at a low work factor, so the cases below are the only
	// thing under test and the suite stays fast.
	valid := hashWith(t, 1000, "swordfish")

	fields := strings.Split(valid, "$")
	if len(fields) != 4 {
		t.Fatalf("known-good hash = %q, want 4 fields", valid)
	}

	cases := []struct {
		name string
		hash string
	}{
		{name: "empty", hash: ""},
		{name: "no separators", hash: "pbkdf2-sha256"},
		{name: "truncated after algorithm", hash: "pbkdf2-sha256$"},
		{name: "one field short", hash: strings.Join(fields[:3], "$")},
		{name: "one field long", hash: valid + "$trailing"},
		{name: "unknown algorithm", hash: "argon2id$" + strings.Join(fields[1:], "$")},
		{name: "bcrypt hash", hash: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		{
			name: "iterations not a number",
			hash: "pbkdf2-sha256$many$" + fields[2] + "$" + fields[3],
		},
		{name: "iterations empty", hash: "pbkdf2-sha256$$" + fields[2] + "$" + fields[3]},
		{name: "iterations zero", hash: "pbkdf2-sha256$0$" + fields[2] + "$" + fields[3]},
		{name: "iterations negative", hash: "pbkdf2-sha256$-1$" + fields[2] + "$" + fields[3]},
		{
			name: "iterations absurd",
			hash: "pbkdf2-sha256$99999999999$" + fields[2] + "$" + fields[3],
		},
		{name: "salt not base64", hash: "pbkdf2-sha256$1000$not base64!$" + fields[3]},
		{name: "salt empty", hash: "pbkdf2-sha256$1000$$" + fields[3]},
		{name: "salt short", hash: "pbkdf2-sha256$1000$" + fields[2][:4] + "$" + fields[3]},
		{name: "derived key not base64", hash: "pbkdf2-sha256$1000$" + fields[2] + "$%%%%"},
		{name: "derived key empty", hash: "pbkdf2-sha256$1000$" + fields[2] + "$"},
		{
			name: "derived key short",
			hash: "pbkdf2-sha256$1000$" + fields[2] + "$" + fields[3][:8],
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := auth.VerifyPassword(tc.hash, "swordfish")
			if err == nil {
				t.Fatalf("VerifyPassword(%q) = nil, want a rejection", tc.hash)
			}

			if !errors.Is(err, auth.ErrMalformedPasswordHash) {
				t.Errorf("VerifyPassword() error = %v, want %v", err, auth.ErrMalformedPasswordHash)
			}
		})
	}
}

// TestVerifyPasswordTakesTheWorkFactorFromTheHash is why the encoded form is
// self-describing.
//
// A hash written at a lower work factor than the current default still verifies,
// which is the only way the default can ever be raised. A verifier that used the
// package constant instead would reject every hash created before the raise, and
// the raise would be a mass logout.
func TestVerifyPasswordTakesTheWorkFactorFromTheHash(t *testing.T) {
	t.Parallel()

	encoded := hashWith(t, 1000, "swordfish")

	if err := auth.VerifyPassword(encoded, "swordfish"); err != nil {
		t.Errorf("VerifyPassword() error = %v, want nil", err)
	}

	if err := auth.VerifyPassword(encoded, "swordfsh"); !errors.Is(err, auth.ErrInvalidPassword) {
		t.Errorf("VerifyPassword(wrong) error = %v, want %v", err, auth.ErrInvalidPassword)
	}
}

// TestHashPasswordErrorsAreClassified separates "you typed the wrong password"
// from "this row is damaged". Folding them together would turn a database
// problem into a silent stream of failed logins with nothing to grep for.
func TestHashPasswordErrorsAreClassified(t *testing.T) {
	t.Parallel()

	if _, err := auth.HashPassword(""); err == nil {
		t.Error("HashPassword(\"\") error = nil, want a length-policy error")
	}

	encoded := hashWith(t, 1000, "swordfish")

	if err := auth.VerifyPassword(encoded, "wrong"); !errors.Is(err, auth.ErrInvalidPassword) {
		t.Errorf("VerifyPassword(wrong) error = %v, want %v", err, auth.ErrInvalidPassword)
	}

	if err := auth.VerifyPassword(
		"garbage",
		"swordfish",
	); !errors.Is(
		err,
		auth.ErrMalformedPasswordHash,
	) {
		t.Errorf("VerifyPassword(garbage) error = %v, want %v", err, auth.ErrMalformedPasswordHash)
	}
}

// hashWith builds an encoded hash at a low work factor.
//
// The exported default is 600,000 iterations, which is right for a login and
// wrong for a table-driven suite that assembles a hash per case. Routing through
// auth.HashPasswordWithIterations keeps the work factor a per-call argument, so
// no test can lower it for the next one and nothing outside a test build can
// reach it at all.
func hashWith(t *testing.T, iterations int, password string) string {
	t.Helper()

	encoded, err := auth.HashPasswordWithIterations(password, iterations)
	if err != nil {
		t.Fatalf("HashPasswordWithIterations(%d) error = %v, want nil", iterations, err)
	}

	return encoded
}

// mask keeps a candidate out of the failure message. Not a secret in a test, but
// neither is it something that should be copied into a pattern for a future
// test to copy back out.
func mask(candidate string) string {
	if candidate == "" {
		return `""`
	}

	return candidate[:1] + "…"
}
