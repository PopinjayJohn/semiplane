// 6-digit LAN join codes (tech-stack §3.6: zero-friction at the table) —
// hashed at rest, 10-minute TTL, strictly single-use, per-IP rate-limited.
// (Package doc lives in auth.go.)
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// LAN code policy (implementation-plan Phase 1/C: hash, 10-min TTL,
// single-use, rate-limit).
const (
	lanCodeTTL        = 10 * time.Minute
	lanCodeMaxPerIP   = 10
	lanCodeRateWindow = time.Minute
)

// ErrRateLimited is a sentinel (errors.Is-compatible, like the pluginapi
// set) for a rejected consume attempt: too many tries per IP within the
// sliding window. Handlers map it to HTTP 429 — NOT 401 — so honest
// clients with a mistyped code know to wait instead of re-requesting.
var ErrRateLimited = errors.New("auth: lan code rate limit exceeded")

// LanCodeService issues and consumes single-use 6-digit join codes stored
// sha256-hashed in the lan_codes table.
type LanCodeService struct {
	DB      *sql.DB
	limiter *ipLimiter
}

// NewLanCodeService returns a LAN-code service with the default limiter
// (10 attempts per minute per IP).
func NewLanCodeService(db *sql.DB) *LanCodeService {
	return &LanCodeService{DB: db, limiter: newIPLimiter(lanCodeMaxPerIP, lanCodeRateWindow)}
}

// Create mints a fresh code for userID: uniformly random 0..999999 drawn
// with crypto/rand.Int (NOT a modulo of raw bytes — single-byte mod bias
// makes leading digits measurably more likely), stored as
// fmt.Sprintf("%06d") and persisted only as its HashToken. The plaintext
// code exists in memory and, transitively, on the GM's screen.
func (l *LanCodeService) Create(ctx context.Context, userID string) (string, error) {
	draw, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("auth: read lan code rand: %w", err)
	}
	code := fmt.Sprintf("%06d", draw)
	now := timeNow()
	id, err := randomTokenN(16)
	if err != nil {
		return "", fmt.Errorf("auth: new lan code id: %w", err)
	}
	if _, err := l.DB.ExecContext(ctx,
		`INSERT INTO lan_codes(id, user_id, created_at, expires_at, used, hash) VALUES(?, ?, ?, ?, 0, ?)`,
		id, userID, now.Unix(), now.Add(lanCodeTTL).Unix(), HashToken(code)); err != nil {
		return "", fmt.Errorf("auth: persist lan code: %w", err)
	}
	return code, nil
}

// Consume validates and burns a code presented by a device at ipKey.
// Exactly one row transition 0→1 wins (single-use): the UPDATE checks
// `used = 0 AND expires_at > now` inside a BEGIN IMMEDIATE and requires
// rows-affected == 1. Wrong / expired / already-used all wrap
// pluginapi.ErrUnauthorized (indistinguishable, as with sessions); over
// the rate limit wraps ErrRateLimited.
//
// ipKey is caller-chosen — normally the client IP, but it may be any
// grouping key (e.g. empty for "global").
func (l *LanCodeService) Consume(ctx context.Context, code, ipKey string) (string, error) {
	if !l.limiter.allow(ipKey) {
		return "", fmt.Errorf("auth: consume attempts for %q over limit: %w", ipKey, ErrRateLimited)
	}
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("auth: empty lan code: %w", pluginapi.ErrUnauthorized)
	}
	want := HashToken(code)
	var userID string
	err := inImmediateTx(ctx, l.DB, func(ctx context.Context, conn *sql.Conn) error {
		if err := conn.QueryRowContext(ctx,
			`SELECT user_id FROM lan_codes WHERE hash = ?`, want).
			Scan(&userID); err != nil {
			return err // sql.ErrNoRows ⇒ unknown code below
		}
		res, err := conn.ExecContext(ctx,
			`UPDATE lan_codes SET used = 1
			  WHERE hash = ? AND used = 0 AND expires_at > ?`, want, timeNow().Unix())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			// 0 rows: used already, or expired. Id == 1 impossible in a
			// BEGIN IMMEDIATE on the single-threaded pool.
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("auth: lan code invalid, expired, or already used: %w", pluginapi.ErrUnauthorized)
	}
	return userID, nil
}

// CleanupExpired deletes expired (and, belt-and-braces, used) rows.
func (l *LanCodeService) CleanupExpired(ctx context.Context) error {
	if _, err := l.DB.ExecContext(ctx,
		`DELETE FROM lan_codes WHERE expires_at <= ? OR used = 1`, timeNow().Unix()); err != nil {
		return fmt.Errorf("auth: cleanup lan codes: %w", err)
	}
	return nil
}

// ipLimiter is an in-memory sliding-window limiter keyed by caller-chosen
// string (caller passes the IP). Deliberately process-local: SQLite is
// the shared state of this deployment, but consume-rate limiting is a
// per-process DoS guard and restarts resetting it is acceptable for a
// single-binary tabletop server.
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, hits: make(map[string][]time.Time)}
}

// allow records one attempt for key (pruning out-of-window hits first)
// and reports whether it is within the budget. The limiter runs BEFORE
// the hash comparison in Consume, so failure responses are uniform.
func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := timeNow()
	cutoff := now.Add(-l.window)
	old := l.hits[key]
	keep := old[:0]
	for _, t := range old {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}
