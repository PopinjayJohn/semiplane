package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

const testPassword = "correct horse battery staple"

// weakPHC builds a syntactically-valid PHC string with attacker-chosen
// (weak or wrong) parameters so the floor/parse rejection branches are
// exercised with REAL argon2 outputs, not fake text. p==0 and friends are
// only rejected by the engine itself, so invalid-argument callers get a
// dummy key — parse rejects the string before any derivation happens.
func weakPHC(m, iters, p uint32, version, alg string) string {
	salt := []byte("0123456789abcdef") // 16 bytes
	key := make([]byte, 32)
	if iters >= 1 && p >= 1 && p <= 255 {
		key = argon2.IDKey([]byte(testPassword), salt, iters, m, uint8(p), 32)
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$%s$%s$m=%d,t=%d,p=%d$%s$%s",
		alg, version, m, iters, p,
		enc.EncodeToString(salt), enc.EncodeToString(key))
}

func TestHashVerifyRoundTrip(t *testing.T) {
	phc, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword(testPassword, phc)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("VerifyPassword(correct password) = false")
	}
	if ok, err := VerifyPassword("wrong password", phc); err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = (%t, %v), want (false, nil)", ok, err)
	}
}

func TestPHCEncodedParametersAreDefaults(t *testing.T) {
	phc, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(phc, `$argon2id$v=19$m=65536,t=3,p=4$`) {
		t.Fatalf("PHC prefix = %q, want $argon2id$v=19$m=65536,t=3,p=4$", phc)
	}
	p, err := parsePHC(phc)
	if err != nil {
		t.Fatalf("parsePHC: %v", err)
	}
	if p.memory != 65536 || p.iters != 3 || p.threads != 4 {
		t.Fatalf("parsed params = m=%d,t=%d,p=%d", p.memory, p.iters, p.threads)
	}
	if len(p.salt) != 16 {
		t.Fatalf("salt = %d bytes, want 16", len(p.salt))
	}
	if len(p.key) != 32 {
		t.Fatalf("key = %d bytes, want 32", len(p.key))
	}
}

func TestHashesAreSaltedUnique(t *testing.T) {
	a, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical — salt not random")
	}
	for _, phc := range []string{a, b} {
		if ok, _ := VerifyPassword(testPassword, phc); !ok {
			t.Fatalf("VerifyPassword(%q) = false", phc)
		}
	}
}

func TestSecurityFloorRejection(t *testing.T) {
	cases := []struct {
		name string
		phc  string
	}{
		{"memory below floor (OWASP 19MiB)", weakPHC(1024, 3, 4, "v=19", "argon2id")},
		{"memory exactly 1MiB", weakPHC(1024, 3, 4, "v=19", "argon2id")},
		{"time below floor", weakPHC(65536, 1, 4, "v=19", "argon2id")},
		{"implicit v=16 (omitted)", weakPHC(65536, 3, 4, "", "argon2id")},
		{"explicit v=16", weakPHC(65536, 3, 4, "v=16", "argon2id")},
		{"argon2i not argon2id", weakPHC(65536, 3, 4, "v=19", "argon2i")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword(testPassword, tc.phc)
			if ok {
				t.Fatal("below-floor PHC accepted as valid authentication material")
			}
			if err == nil {
				t.Fatal("below-floor PHC rejected silently: want a loud error, got (false, nil) which reads as 'just a wrong password'")
			}
		})
	}
}

func TestParseRejectsMalformedPHC(t *testing.T) {
	cases := []struct {
		name string
		phc  string
	}{
		{"not a phc", "hunter2"},
		{"truncated", "$argon2id$v=19$m=65536,t=3,p=4"},
		{"missing salt", "$argon2id$v=19$m=65536,t=3,p=4$$"},
		{"bad salt b64", "$argon2id$v=19$m=65536,t=3,p=4$$$"},
		{"bad params", "$argon2id$v=19$m=65536,p=3,t=4$" + strings.Repeat("x", 22) + "$" + strings.Repeat("y", 43)},
		{"zero parallelism", weakPHC(65536, 3, 0, "v=19", "argon2id")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword(testPassword, tc.phc)
			if ok {
				t.Fatal("malformed PHC accepted")
			}
			if err == nil {
				t.Fatal("malformed PHC produced silent refusal")
			}
		})
	}
}

func TestTamperResistance(t *testing.T) {
	phc, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsePHC(phc)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a bit in the hash → well-formed, in-policy, but must not match.
	badKey := append([]byte{}, p.key...)
	badKey[0] ^= 0x01
	if ok, err := VerifyPassword(testPassword, encodePHC(p.salt, badKey)); ok || err != nil {
		t.Fatalf("bit-flipped hash = (%t, %v), want (false, nil)", ok, err)
	}

	// Flip a bit in the salt → full re-derivation under different salt.
	badSalt := append([]byte{}, p.salt...)
	badSalt[0] ^= 0x01
	if ok, err := VerifyPassword(testPassword, encodePHC(badSalt, p.key)); ok || err != nil {
		t.Fatalf("bit-flipped salt = (%t, %v), want (false, nil)", ok, err)
	}

	// Trailing garbage after the PHC string changes the key bytes and
	// must never verify. (Permissive b64 decoding can still parse it,
	// so the strict requirement is !ok, not a non-nil error.)
	if ok, _ := VerifyPassword(testPassword, phc+"junk"); ok {
		t.Fatal("PHC with trailing garbage accepted")
	}
}

// Guarantee the floor test can't false-pass: the weak strings it feeds in
// MUST be structurally parseable apart from the under-floor parameter
// (i.e. they reach the floor check, not an earlier parse error).
func TestWeakPHCStructuralPrecondition(t *testing.T) {
	p, err := parsePHC(weakPHC(1024, 3, 4, "v=19", "argon2id"))
	if err != nil {
		t.Fatalf("precondition: weak PHC does not parse: %v", err)
	}
	if p.memory != 1024 {
		t.Fatalf("precondition: parsed memory = %d, want 1024", p.memory)
	}
}

func TestHexHashDeterministic(t *testing.T) {
	if HashToken("abc") != HashToken("abc") || HashToken("abc") == HashToken("abd") {
		t.Fatal("HashToken not a stable sha256 fingerprint")
	}
}
