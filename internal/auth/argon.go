// Package auth's password hashing: Argon2id PHC strings per tech-stack §3.6.
// (Package doc lives in auth.go.)
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Default Argon2id parameters (tech-stack §3.6, implementation-plan Phase
// 1/C): OWASP-recommended for new applications. m=64 MiB, t=3, p=4,
// 16-byte salt, 32-byte key.
const (
	argonMemoryKiB  uint32 = 65536 // 64 MiB
	argonIterations uint32 = 3
	argonThreads    uint8  = 4
	argonSaltLen           = 16
	argonKeyLen            = 32
)

// SECURITY FLOOR for VerifyPassword. A stored PHC string declaring weaker
// parameters than this is refused: it can only come from corrupted data or
// tampering, never from HashPassword. Tuned to OWASP's Password Storage
// Cheat Sheet: m >= 19 MiB, t >= 2, p >= 1, v == 19, alg == argon2id.
const (
	floorMemoryKiB  = 19456 // 19 MiB
	floorIterations = 2
)

// phcB64 is PHC string encoding: unpadded standard base64 (alphabet ./A-Za-z0-9).
var phcB64 = base64.RawStdEncoding

// HashPassword derives an Argon2id key and returns it encoded as a PHC
// string: "$argon2id$v=19$m=65536,t=3,p=4$<b64salt>$<b64hash>" with
// unpadded standard base64 salt and hash. The salt comes from
// crypto/rand. Store the whole string (users.phc); VerifyPassword
// consumes exactly that.
func HashPassword(password string) (phc string, err error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read argon2id salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	return encodePHC(salt, key), nil
}

// encodePHC renders a salt+key pair as a version-19 argon2id PHC string.
func encodePHC(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		phcB64.EncodeToString(salt), phcB64.EncodeToString(key))
}

// phcParams is one parsed PHC string.
type phcParams struct {
	memory  uint32
	iters   uint32
	threads uint8
	salt    []byte
	key     []byte
}

// parsePHC breaks a "$argon2id$v=19$m=...,t=...,p=...$salt$key" string
// into its parameters, strictly:
//   - alg must be argon2id (argon2i/argon2d are refused at parse time);
//   - v=19 (argon2.Version) must be declared explicitly;
//   - m/t/p must be present, positive, and in-range.
//
// Any deviation is an error, never a "best effort rehash": the string is
// either one we issued (byte-exact format) or it is suspect.
func parsePHC(phc string) (phcParams, error) {
	var p phcParams
	// PHC fields: ["", alg, "v=19", "m=..,t=..,p=..", salt, key]
	parts := splitRune(phc, '$')
	if len(parts) != 6 || parts[0] != "" {
		return p, fmt.Errorf("auth: malformed argon2 PHC string: want 5 $-separated fields after leading '$', got %d", len(parts))
	}
	if parts[1] != "argon2id" {
		return p, fmt.Errorf("auth: PHC algorithm %q not supported (only argon2id)", parts[1])
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, fmt.Errorf("auth: PHC version %q not supported (want v=%d)", parts[2], argon2.Version)
	}
	var m, t, pv int64
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &pv); err != nil {
		return p, fmt.Errorf("auth: malformed PHC parameters %q: %w", parts[3], err)
	}
	if t <= 0 || pv <= 0 || pv > 255 || m <= 0 || m > 1<<31 {
		return p, fmt.Errorf("auth: PHC parameters out of range %q", parts[3])
	}
	p.memory, p.iters, p.threads = uint32(m), uint32(t), uint8(pv)
	var err error
	if p.salt, err = phcB64.DecodeString(parts[4]); err != nil {
		return p, fmt.Errorf("auth: malformed PHC salt: %w", err)
	}
	if p.key, err = phcB64.DecodeString(parts[5]); err != nil {
		return p, fmt.Errorf("auth: malformed PHC hash: %w", err)
	}
	if len(p.salt) == 0 || len(p.key) == 0 {
		return p, fmt.Errorf("auth: PHC salt and hash must both be non-empty")
	}
	return p, nil
}

// splitRune splits s on the rune r, always returning n+1 pieces for n
// separators (empty pieces preserved; "PHC strings begin with $").
func splitRune(s string, r rune) []string {
	var out []string
	start := 0
	for i, c := range []byte(s) {
		if rune(c) == r {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// VerifyPassword checks password against a stored PHC string.
//
// SECURITY FLOOR: strings declaring v != 19, alg != argon2id, m < 19456
// (19 MiB), or t < 2 are REFUSED (non-nil error) — the contract is that
// hashes we issued via HashPassword always pass and weaker legacy strings
// are never silently accepted as authentication material.
//
// Returns (true, nil) on a match — constant-time compare — and
// (false, nil) for a well-formed, in-policy string that simply doesn't
// match. A non-nil error means malformed or below-floor input.
func VerifyPassword(password, phc string) (bool, error) {
	p, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	// Floor enforcement after structural parse so the error message can
	// quote the offending parameters.
	if p.iters < floorIterations {
		return false, fmt.Errorf("auth: PHC time cost %d below security floor %d", p.iters, floorIterations)
	}
	if p.memory < floorMemoryKiB {
		return false, fmt.Errorf("auth: PHC memory cost %d KiB below security floor %d KiB", p.memory, floorMemoryKiB)
	}
	computed := argon2.IDKey([]byte(password), p.salt, p.iters, p.memory, p.threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(computed, p.key) == 1, nil
}
