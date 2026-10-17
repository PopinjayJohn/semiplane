// Package acl implements vault path-prefix access control and token-scoped
// HTTP middleware (tech-stack §3.6: "gm/player roles + path-based ACL
// middleware" — characters/<user>/* ownership maps directly to the vault
// layout, no separate permission store).
//
// Default policy (baked into Can, no acls rows involved):
//
//	gm      read + write everything
//	player  read everything except campaign/* (tech-stack §10 vault layout:
//	        campaign/ is GM-only notes, session logs, hidden lore);
//	        write only characters/<their own ID>/*
//	other   denied (unknown role / anonymous: fail closed)
//
// The acls table can only ADD grants; there are no deny rows — the default
// policy is the deny source. A row (path_prefix, role, user_id) grants that
// user every mode under path_prefix, provided the users table role for that
// user_id equals the row's role. Identity.Role is preferred for the default
// policy decision; the users table is the validation source for override
// grants (the caller passes Identity, the DB confirms — a forged Identity
// cannot mint itself an override). Example: grant a player write access to
// rules/ with INSERT INTO acls('rules/','player','playerX').
//
// Cache decision (documented): acls rows are loaded per Can call and cached
// for a 30-second TTL. Rows change rarely and only via GM/admin action; 30s
// bounds staleness without a per-request query on the single-connection
// SQLite pool. Invalidate() forces a reload (admin UIs, tests). The users
// table lookup used to validate an override candidate is NOT cached — it
// runs per Can call that has an override candidate, because correctness
// there matters more than the one extra point query.
package acl

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// Mode is the access mode checked against a path.
type Mode string

const (
	ModeRead  Mode = "read"
	ModeWrite Mode = "write"
)

// overrideTTL bounds how long a Can call may act on cached acls rows.
const overrideTTL = 30 * time.Second

// Roles. Kept as plain strings (they cross the DB and pluginapi.Identity);
// unknown roles fail closed in Can.
const (
	RoleGM     = "gm"
	RolePlayer = "player"
)

// ACL answers access decisions for vault paths against the default policy
// plus the acls override table. It wraps the (already migrated) index DB.
type ACL struct {
	DB *sql.DB

	mu       sync.Mutex
	rows     []grantRow
	rowsAt   time.Time
	haveRows bool
}

// grantRow mirrors one row of the acls table.
type grantRow struct {
	Prefix string
	Role   string
	UserID string
}

// New builds an ACL over an opened index DB (schema applied via migrate).
func New(db *sql.DB) *ACL {
	return &ACL{DB: db}
}

// Invalidate drops the cached acls snapshot so the next Can reloads from the
// DB (bypassing the 30s TTL). For admin UIs and tests.
func (a *ACL) Invalidate() {
	a.mu.Lock()
	a.haveRows = false
	a.mu.Unlock()
}

// grants returns the current acls rows, refreshing the cached snapshot when
// the TTL has expired. An empty table caches as an empty (but valid)
// snapshot; a query error is returned without caching.
func (a *ACL) grants(ctx context.Context) ([]grantRow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.haveRows && time.Since(a.rowsAt) < overrideTTL {
		return a.rows, nil
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT path_prefix, role, user_id FROM acls`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []grantRow
	for rows.Next() {
		var g grantRow
		if err := rows.Scan(&g.Prefix, &g.Role, &g.UserID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.rows = out
	a.rowsAt = time.Now()
	a.haveRows = true
	return out, nil
}

// usersRole returns the role stored on the users row for userID, or "" when
// the user has no row. Any other error is returned as-is (infrastructure
// failure, not an authorization verdict).
func (a *ACL) usersRole(ctx context.Context, userID string) (string, error) {
	var role string
	err := a.DB.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

// Can reports whether ident may access vaultPath (vault-relative, slash
// separated) in the given mode. A rejected path (empty, traversal, absolute,
// backslash) yields (false, error) wrapping pluginapi.ErrForbidden —
// malformed input is a client problem, never a silent deny. Database errors
// surface as errors without a verdict. Unknown roles and empty user IDs deny
// with (false, nil) — fail closed by policy, not by malfunction.
func (a *ACL) Can(ctx context.Context, ident pluginapi.Identity, vaultPath string, mode Mode) (bool, error) {
	if mode != ModeRead && mode != ModeWrite {
		return false, fmt.Errorf("acl: unknown mode %q: %w", mode, pluginapi.ErrForbidden)
	}
	clean, err := validatePath(vaultPath)
	if err != nil {
		return false, err
	}
	if ident.ID == "" {
		return false, nil
	}
	switch ident.Role {
	case RoleGM:
		return true, nil
	case RolePlayer:
		if defaultPlayerAllows(clean, ident.ID, mode) {
			return true, nil
		}
	default:
		return false, nil
	}
	return a.overrideAllows(ctx, ident, clean)
}

// defaultPlayerAllows is the default policy for a player identity.
func defaultPlayerAllows(clean, userID string, mode Mode) bool {
	parts := strings.Split(clean, "/")
	// campaign/ is GM-only (tech-stack §10): session logs, hidden lore,
	// GM secrets. Players get neither read nor write via default policy.
	if parts[0] == "campaign" {
		return false
	}
	if mode == ModeWrite {
		// Ownership: characters/<their-ID>/* only. Segment-boundary match:
		// "characters/bob" never matches "characters/bobby/".
		return len(parts) >= 2 && parts[0] == "characters" && parts[1] == userID
	}
	// Reads of every other vault area are public to authenticated players.
	return true
}

// overrideAllows applies acls-table grants. A grant applies iff some row
// has row.UserID == ident.ID and matches the path on segment boundaries,
// AND the users table role for that user equals row.Role. Note a grant row
// covers every mode: write implies read under the same prefix (rules/ write
// grants are usable; read there is already public by default policy).
func (a *ACL) overrideAllows(ctx context.Context, ident pluginapi.Identity, clean string) (bool, error) {
	rows, err := a.grants(ctx)
	if err != nil {
		return false, err
	}
	var candidates []grantRow
	for _, g := range rows {
		if g.UserID != ident.ID {
			continue
		}
		if !pathHasPrefix(clean, g.Prefix) {
			continue
		}
		candidates = append(candidates, g)
	}
	if len(candidates) == 0 {
		return false, nil
	}
	role, err := a.usersRole(ctx, ident.ID)
	if err != nil {
		return false, err
	}
	if role == "" {
		return false, nil // unknown user cannot hold a grant
	}
	for _, g := range candidates {
		if g.Role == role {
			return true, nil
		}
	}
	return false, nil
}

// pathHasPrefix reports whether p lies under the prefix on segment
// boundaries: "characters/bob" does not match "characters/bobby/".
// An empty (or bare "/") prefix matches every path — an explicit root grant.
func pathHasPrefix(p, prefix string) bool {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return true
	}
	p = strings.Trim(p, "/")
	if p == prefix {
		return true
	}
	return strings.HasPrefix(p, prefix+"/")
}

// validatePath canonicalizes and bounds-checks a vault-relative slash path.
// It mirrors internal/vault.canonicalPath's guards (no empty, no
// backslashes, no absolute, no traversal) minus the .md suffix rule: ACL
// checks also address directory-like prefixes. Not imported from vault to
// keep this package's dependency surface minimal — if the guards ever
// diverge, prefer matching vault's.
func validatePath(p string) (string, error) {
	const invalid = "invalid path"
	if p == "" {
		return "", fmt.Errorf("acl: empty path: %w", pluginapi.ErrForbidden)
	}
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("acl: %s: %w", invalid, pluginapi.ErrForbidden)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("acl: %s: %w", invalid, pluginapi.ErrForbidden)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("acl: %s: %w", invalid, pluginapi.ErrForbidden)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("acl: %s: %w", invalid, pluginapi.ErrForbidden)
		}
	}
	return clean, nil
}
