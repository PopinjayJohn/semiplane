package auth

import (
	"errors"
	"strings"
	"testing"
)

// testIterations is the work factor the internal tests use. Low enough that the
// suite is quick, and only ever reached through hashPassword — the default stays
// at pbkdf2Iterations and no test can move it.
const testIterations = 1000

// TestHashPasswordIsTotal is the assertion that an empty password hashes and
// round-trips at the primitive level.
//
// HashPassword refuses it, and that is the correct entry-point behaviour: an
// account with no password is an account anyone can claim. What is asserted
// here is the layer below — the encoding has no special case for an empty input,
// so it cannot panic, cannot emit a shorter field list, and cannot produce a
// string that fails to parse back. The rejection is policy in one function, not
// an invariant smeared across the format.
func TestHashPasswordIsTotal(t *testing.T) {
	t.Parallel()

	encoded, err := hashPassword("", testIterations)
	if err != nil {
		t.Fatalf("hashPassword(\"\") error = %v, want nil", err)
	}

	if fields := strings.Split(encoded, passwordHashSeparator); len(fields) != passwordHashFields {
		t.Fatalf("encoded = %q, want %d fields, got %d", encoded, passwordHashFields, len(fields))
	}

	if _, parseErr := parsePasswordHash(encoded); parseErr != nil {
		t.Errorf("parsePasswordHash() error = %v, want nil", parseErr)
	}

	// The refusal must be the length policy and not a verdict on the password.
	// ErrInvalidPassword would mean the comparison ran and failed, which is a
	// different claim about the world: that an empty string is a wrong guess
	// rather than an input the caller is not allowed to make.
	verifyErr := VerifyPassword(encoded, "")
	if verifyErr == nil {
		t.Error(`VerifyPassword("") error = nil, want the length policy`)
	}

	if errors.Is(verifyErr, ErrInvalidPassword) || errors.Is(verifyErr, ErrMalformedPasswordHash) {
		t.Errorf(`VerifyPassword("") error = %v, want the length policy, not a verdict`, verifyErr)
	}
}

// TestParsePasswordHashRejectsEverythingGuessable walks the parser's rejection
// paths from inside, where the encoded form can be assembled field by field
// instead of by corrupting a real hash.
//
// The point of the parser is that no component is ever defaulted or inferred, so
// every case here has to be an error rather than a hash verified against a
// substitute parameter.
func TestParsePasswordHashRejectsEverythingGuessable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		hash string
	}{
		{name: "empty", hash: ""},
		{name: "algorithm only", hash: pbkdf2Algorithm},
		{name: "trailing separator only", hash: pbkdf2Algorithm + "$"},
		{name: "empty field in the middle", hash: pbkdf2Algorithm + "$$1000"},
		{name: "unknown algorithm", hash: "argon2id$1000$c2FsdA==$a2V5"},
		{name: "algorithm case differs", hash: "PBKDF2-SHA256$1000$c2FsdA==$a2V5"},
		{name: "leading whitespace", hash: " pbkdf2-sha256$1000$c2FsdA==$a2V5"},
		{name: "iterations absent", hash: pbkdf2Algorithm + "$" + "$c2FsdA==$a2V5"},
		{name: "iterations not numeric", hash: pbkdf2Algorithm + "$1e6$c2FsdA==$a2V5"},
		{name: "iterations zero", hash: pbkdf2Algorithm + "$0$c2FsdA==$a2V5"},
		{name: "iterations negative", hash: pbkdf2Algorithm + "$-1$c2FsdA==$a2V5"},
		{name: "iterations above the ceiling", hash: pbkdf2Algorithm + "$10000001$c2FsdA==$a2V5"},
		{name: "salt absent", hash: pbkdf2Algorithm + "$1000$$a2V5"},
		{name: "salt empty", hash: pbkdf2Algorithm + "$1000$$" + strings.Repeat("A", 43) + "="},
		{name: "salt not base64", hash: pbkdf2Algorithm + "$1000$not base64!$a2V5"},
		{
			name: "salt too short",
			hash: pbkdf2Algorithm + "$1000$YWJj$" + strings.Repeat("A", 43) + "=",
		},
		{
			name: "salt too long",
			hash: pbkdf2Algorithm + "$1000$" + strings.Repeat("A", 45) + "$a2V5",
		},
		{name: "derived key absent", hash: pbkdf2Algorithm + "$1000$c2FsdA==$"},
		{name: "derived key not base64", hash: pbkdf2Algorithm + "$1000$c2FsdA==$not base64!"},
		{
			name: "derived key too short",
			hash: pbkdf2Algorithm + "$1000$c2FsdA==$" + strings.Repeat("A", 27) + "=",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := parsePasswordHash(tc.hash); err == nil {
				t.Errorf("parsePasswordHash(%q) error = nil, want a rejection", tc.hash)
			}
		})
	}
}

// TestParsePasswordHashRoundTrips is the assertion that the parser recovers
// exactly what the encoder wrote — including the work factor, which is the
// whole reason the format is self-describing.
func TestParsePasswordHashRoundTrips(t *testing.T) {
	t.Parallel()

	const password = "swordfish"

	for _, iterations := range []int{1, 1000, pbkdf2Iterations} {
		encoded, err := hashPassword(password, iterations)
		if err != nil {
			t.Fatalf("hashPassword() error = %v, want nil", err)
		}

		parsed, err := parsePasswordHash(encoded)
		if err != nil {
			t.Fatalf("parsePasswordHash(%q) error = %v, want nil", encoded, err)
		}

		if parsed.iterations != iterations {
			t.Errorf("iterations = %d, want %d", parsed.iterations, iterations)
		}

		if len(parsed.salt) != saltLength {
			t.Errorf("salt is %d bytes, want %d", len(parsed.salt), saltLength)
		}

		if len(parsed.derived) != derivedKeyLength {
			t.Errorf("derived key is %d bytes, want %d", len(parsed.derived), derivedKeyLength)
		}

		if err := VerifyPassword(encoded, password); err != nil {
			t.Errorf("VerifyPassword() error = %v, want nil", err)
		}
	}
}

// TestParsePasswordHashRejectsTrailingBits pins the reason the parser uses
// base64.Strict.
//
// Without strict decoding, two different encoded strings can decode to the same
// key. A row could then be edited to a form that hashes the same way while no
// longer matching what the application wrote, and every check that compares the
// encoded strings directly — a migration diff, a backup integrity check — would
// disagree with the one that matters.
func TestParsePasswordHashRejectsTrailingBits(t *testing.T) {
	t.Parallel()

	encoded, err := hashPassword("swordfish", testIterations)
	if err != nil {
		t.Fatalf("hashPassword() error = %v, want nil", err)
	}

	fields := strings.Split(encoded, passwordHashSeparator)

	// A 32-byte key encodes to 44 characters ending in one "=" of padding.
	// Flipping the character just before it sets a bit that strict decoding
	// rejects: the same 32 bytes, spelled a second way.
	relaxed := []byte(fields[3])
	relaxed[len(relaxed)-2] ^= 0x01

	fields[3] = string(relaxed)

	if _, err := parsePasswordHash(strings.Join(fields, passwordHashSeparator)); err == nil {
		t.Error("parsePasswordHash() error = nil, want a rejection of non-zero trailing bits")
	}
}
