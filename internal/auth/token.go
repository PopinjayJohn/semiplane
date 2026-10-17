// Opaque token primitives shared by sessions, LAN codes, and OIDC states.
// (Package doc lives in auth.go.)
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// tokenRandLen is the number of entropy bytes in every exposed opaque
// token (128 bits minimum; 32 bytes = 256 bits).
const tokenRandLen = 32

// NewToken generates a fresh opaque token: 32 bytes of crypto/rand,
// base64url-encoded unpadded (43 characters), plus its sha256 hex
// fingerprint. PERSIST THE HASH, never the plaintext token — a stolen DB
// dump can't forge sessions without it (tech-stack §3.6). Shared by the
// session, LAN-code, and OIDC flows.
func NewToken() (token string, hash string, err error) {
	b := make([]byte, tokenRandLen)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth: read %d rand bytes: %w", tokenRandLen, err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the canonical opaque-token fingerprint: hex sha256. Must
// stay deterministic — tokens are only ever compared as hashes.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomToken is randomTokenN(nBytes) at the canonical length.
func randomToken() (string, error) {
	return randomTokenN(tokenRandLen)
}

// randomTokenN returns nBytes of crypto/rand base64url-encoded (unpadded).
// Used for state values and row ids that must be random but are not
// themselves compared against caller input by hash.
func randomTokenN(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read %d rand bytes: %w", nBytes, err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// timeNowUnix is the single wall-clock seam for INTEGER unix-second
// columns (schema.sql: all timestamps are INTEGER unix seconds). No
// clock-jailing for tests — they drive expiry through short TTLs or by
// writing expiry rows directly.
var timeNow = time.Now

func timeNowUnix() int64 {
	return timeNow().Unix()
}
