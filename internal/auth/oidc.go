// OIDC Authorization Code + S256 PKCE login (tech-stack §3.6 remote
// identity) against the server-side oidc_states table. (Package doc
// lives in auth.go.)
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

const (
	// oidcStateTTL bounds the CSRF state (and with it the PKCE verifier):
	// the user clicking through the IdP rarely takes more than minutes.
	oidcStateTTL = 15 * time.Minute
	// oauthTimeout bounds every outbound token/ID-token round trip.
	oauthTimeout = 30 * time.Second
)

// OIDCService drives the Authorization Code + PKCE flow.
//
// PKCE storage note: the FINAL migrate schema's oidc_states table has no
// verifier column (id, state, nonce, created_at, expires_at). The row's
// `id` TEXT PRIMARY KEY therefore doubles as the PKCE verifier: it is a
// 43+ character oauth2.GenerateVerifier() value, never leaves the server
// (only its S256 challenge goes to the IdP's authorization endpoint as
// the challenge), and is recoverable at callback time with the
// state-selected row. This keeps the verifier out of cookies AND out of
// the URL — strictly more server-side than the client-stored pattern.
type OIDCService struct {
	DB *sql.DB
}

// NewOIDCService returns an OIDC flow driver over an opened
// (schema-applied) DB.
func NewOIDCService(db *sql.DB) *OIDCService {
	return &OIDCService{DB: db}
}

// Begin starts a login: generates state + nonce + PKCE verifier, persists
// them in oidc_states (TTL 15 minutes) keyed so the verifier is the row
// id, and returns the CSRF state plus the authorization redirect target
// (AuthCodeURL carrying state, S256 code_challenge, and nonce). cfg must
// already carry the right RedirectURL and provider Endpoint (from
// (*oidc.Provider).Endpoint()). redirectURL/callbackPath of an earlier
// draft of this brief live in cfg.RedirectURL — no separate parameter.
//
// Signature deviation from the task brief: provider *oidc.Provider was
// dropped from Begin — it is only consulted at HandleCallback (ID-token
// verification), and its absence here keeps Begin testable without a
// bootstrapped provider.
func (o *OIDCService) Begin(ctx context.Context, cfg *oauth2.Config) (state, authCodeURL string, err error) {
	verifier := oauth2.GenerateVerifier()
	state, err = randomToken()
	if err != nil {
		return "", "", fmt.Errorf("auth: generate oidc state: %w", err)
	}
	nonce, err := randomToken()
	if err != nil {
		return "", "", fmt.Errorf("auth: generate oidc nonce: %w", err)
	}
	now := timeNow()
	err = inImmediateTx(ctx, o.DB, func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx,
			`INSERT INTO oidc_states(id, state, nonce, created_at, expires_at) VALUES(?, ?, ?, ?, ?)`,
			verifier, state, nonce, now.Unix(), now.Add(oidcStateTTL).Unix())
		return err
	})
	if err != nil {
		return "", "", fmt.Errorf("auth: persist oidc state: %w", err)
	}
	url := cfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce))
	return state, url, nil
}

// HandleCallback completes a login: atomically consumes the state row
// (single-use — replay of the same state refuses even against a fresh
// row, because the SELECT+DELETE happens inside one BEGIN IMMEDIATE),
// exchanges the code using the row's stored verifier, verifies the ID
// token with go-oidc, then performs the MANUAL nonce check:
//
//	go-oidc (v3.15) captures each token's `nonce` CLAIM but does NOT
//	match it against anything. Replay/injection protection requires the
//	caller to compare IDToken.Nonce against the nonce bound to the state
//	row — done here, and the only place nonce comparison must ever
//	happen. A mismatching nonce wraps pluginapi.ErrUnauthorized.
//
// On success the mapped local user is upserted (id = "oidc:"+sub) with
// default role "player" and EMPTY phc — OIDC users have no local
// password; Login's Argon2id path rejects an empty phc at parse, which is
// exactly the intended failure. The returned Identity carries the
// (possibly pre-existing, e.g. GM-upgraded) role from users.
func (o *OIDCService) HandleCallback(ctx context.Context, provider *oidc.Provider, cfg *oauth2.Config, state, code string) (pluginapi.Identity, error) {
	nonce, verifier, err := o.consumeState(ctx, state)
	if err != nil {
		return pluginapi.Identity{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()

	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: oidc code exchange: %w", err)
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return pluginapi.Identity{}, fmt.Errorf("auth: oidc token response: no id_token field")
	}
	idt, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: oidc id_token verify: %w", err)
	}
	if idt.Nonce != nonce {
		return pluginapi.Identity{}, fmt.Errorf("auth: oidc nonce mismatch: %w", pluginapi.ErrUnauthorized)
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: oidc id_token claims: %w", err)
	}
	username := claims.PreferredUsername
	if username == "" {
		// Spec fallback: sub is always present and unique per issuer.
		username = idt.Subject
	}
	return o.upsertOIDCUser(ctx, idt.Subject, username)
}

// consumeState atomically validates and deletes the state row, returning
// (nonce, pkce-verifier-as-row-id). Unknown state, replay (second use of
// a row), and expired rows ALL wrap pluginapi.ErrUnauthorized — replay
// and expiry being indistinguishable from mismatch to the caller is the
// standard hardening posture.
func (o *OIDCService) consumeState(ctx context.Context, state string) (nonce, verifier string, err error) {
	var exp int64
	txErr := inImmediateTx(ctx, o.DB, func(ctx context.Context, conn *sql.Conn) error {
		var n, v string
		var e2 int64
		e := conn.QueryRowContext(ctx,
			`SELECT nonce, expires_at, id FROM oidc_states WHERE state = ?`, state).
			Scan(&n, &e2, &v)
		if e != nil { // sql.ErrNoRows ⇒ unknown OR already-consumed (replay)
			return e
		}
		res, e := conn.ExecContext(ctx, `DELETE FROM oidc_states WHERE state = ?`, state)
		if e != nil {
			return e
		}
		rowsAffected, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if rowsAffected != 1 {
			return fmt.Errorf("oidc: state deleted %d rows, want 1", rowsAffected)
		}
		nonce, verifier, exp = n, v, e2
		return nil
	})
	if txErr != nil {
		return "", "", fmt.Errorf("auth: oidc state invalid, expired, or already consumed: %w", pluginapi.ErrUnauthorized)
	}
	// Expiry check AFTER the row is gone from the DB (self-cleaning while
	// holding the write lock): valid state requires expires_at strictly
	// in the future (INTEGER unix seconds).
	if timeNow().Unix() >= exp {
		return "", "", fmt.Errorf("auth: oidc state expired: %w", pluginapi.ErrUnauthorized)
	}
	return nonce, verifier, nil
}

// upsertOIDCUser maps (sub, username) to a local users row
// (id = "oidc:"+sub) and refreshes the username on re-login
// (preferred_username changes propagate). Returns the Identity with the
// row's real role.
func (o *OIDCService) upsertOIDCUser(ctx context.Context, sub, username string) (pluginapi.Identity, error) {
	userID := "oidc:" + sub
	_, err := o.DB.ExecContext(ctx, `
		INSERT INTO users(id, role, username, phc, created_at) VALUES(?, 'player', ?, '', ?)
		ON CONFLICT(id) DO UPDATE SET username = excluded.username`,
		userID, username, timeNowUnix())
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: upsert oidc user %q: %w", userID, err)
	}
	var role string
	if err := o.DB.QueryRowContext(ctx,
		`SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		// The INSERT just succeeded; not finding the row is
		// impossible short of concurrent corruption. Surface it loudly.
		return pluginapi.Identity{}, fmt.Errorf("auth: re-read oidc user %q: %w", userID, err)
	}
	return pluginapi.Identity{ID: userID, Role: role}, nil
}

// CleanupExpired removes stale state rows (abandoned login attempts).
func (o *OIDCService) CleanupExpired(ctx context.Context) error {
	if _, err := o.DB.ExecContext(ctx,
		`DELETE FROM oidc_states WHERE expires_at <= ?`, timeNow().Unix()); err != nil {
		return fmt.Errorf("auth: cleanup oidc states: %w", err)
	}
	return nil
}
