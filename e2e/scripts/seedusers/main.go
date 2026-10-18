// Command seedusers seeds the e2e scratch vault's index.db with the fixed
// users, LAN codes, and API tokens the Wave-1 E2E specs need. It is run by
// e2e/scripts/run.sh before the semiplane binary starts, so the auth
// surface (login, LAN join, ACL, reveal) has principals to authenticate
// against. No internal/ code changes are required: this program consumes
// the existing auth package exactly as the server does.
//
// It lives in e2e/ (not internal/) so the binary stays unchanged; it is a
// test-harness tool, not product code.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/auth"
)

var (
	dbPath  = flag.String("db", "", "path to the scratch index.db (required)")
	gmPass  = flag.String("gm-pass", "gm-secret", "gm user password")
	p1Pass  = flag.String("p1-pass", "p1-secret", "player1 user password")
	p2Pass  = flag.String("p2-pass", "p2-secret", "player2 user password")
	seedLan = flag.Bool("seed-lan", true, "seed a known LAN join code for player1")
)

func main() {
	flag.Parse()
	if *dbPath == "" {
		log.Fatal("--db is required")
	}
	ctx := context.Background()

	// auth.Open applies the schema (idempotent) and returns the shared DB.
	db, err := auth.Open(*dbPath)
	if err != nil {
		log.Fatalf("open index.db: %v", err)
	}
	defer func() { _ = db.Close() }()

	users := auth.NewUsers(db)
	for _, u := range []struct {
		id, role, pass string
	}{
		{"gm", "gm", *gmPass},
		{"player1", "player", *p1Pass},
		{"player2", "player", *p2Pass},
	} {
		if err := users.Create(u.id, u.role, u.pass); err != nil {
			log.Fatalf("create user %q (%s): %v", u.id, u.role, err)
		}
		fmt.Printf("seeded user %q role=%s\n", u.id, u.role)
	}

	if *seedLan {
		// Seed a deterministic code the specs can rely on. Consume uses the
		// per-IP limiter; these seeds bypass it (direct INSERT), so the
		// specs can join without burning the limiter budget.
		code := "123456"
		now := time.Now().Unix()
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO lan_codes(id, user_id, created_at, expires_at, used, hash) VALUES(?, ?, ?, ?, 0, ?)`,
			"lc-seed-123456", "player1", now, now+600, auth.HashToken(code)); err != nil {
			log.Fatalf("seed lan code: %v", err)
		}
		fmt.Printf("seeded lan code %q for player1\n", code)
	}

	// Seed a bearer token with insufficient scope for the token-scope test.
	// rules:read < characters:write, so presenting it where characters:write
	// is required yields 401 (realm failure), never 403.
	tok := "tok-rules-read-only"
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO tokens(id, user_id, scope, created_at, hash) VALUES(?, ?, ?, ?, ?)`,
		"tok-seed-rules", "player1", "rules:read", now, auth.HashToken(tok)); err != nil {
		log.Fatalf("seed token: %v", err)
	}
	fmt.Printf("seeded bearer token scope=rules:read for player1\n")
}
