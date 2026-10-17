package acl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/migrate"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
	_ "modernc.org/sqlite"
)

var testDBSeq atomic.Int64

// openTestDB opens a private in-memory SQLite DB with the FINAL schema
// applied. It replicates internal/index.Open's DSN (the fixed name
// "file:memdb" would SHARE one store across every test in the process, so a
// per-test unique name is used instead of importing index.Open directly).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:memacl%04d?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate", testDBSeq.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migrate.Apply(db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func seedUser(t *testing.T, db *sql.DB, id, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO users(id, role, username, phc, created_at) VALUES(?, ?, ?, ?, ?)`,
		id, role, id+"@test", "phc$placeholder", time.Now().Unix()); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

var (
	gm      = pluginapi.Identity{ID: "gm1", Role: RoleGM}
	player1 = pluginapi.Identity{ID: "player1", Role: RolePlayer}
	player2 = pluginapi.Identity{ID: "player2", Role: RolePlayer}
)

func TestCanDefaultPolicy(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, gm.ID, RoleGM)
	seedUser(t, db, player1.ID, RolePlayer)
	seedUser(t, db, player2.ID, RolePlayer)
	a := New(db)
	ctx := context.Background()

	// Segment-boundary ownership: a sibling id (player1fake) must never
	// count as player1's directory.
	for _, tc := range []struct {
		name  string
		ident pluginapi.Identity
		path  string
		mode  Mode
		want  bool
	}{
		{"gm read campaign", gm, "campaign/notes.md", ModeRead, true},
		{"gm write campaign", gm, "campaign/notes.md", ModeWrite, true},
		{"gm write rules", gm, "rules/foo.md", ModeWrite, true},
		{"gm write arbitrary", gm, "characters/whoever/x.md", ModeWrite, true},

		{"player write own character", player1, "characters/player1/hero.md", ModeWrite, true},
		{"player write own character dir", player1, "characters/player1", ModeWrite, true},
		{"player read own character", player1, "characters/player1/hero.md", ModeRead, true},
		{"player write another players character", player1, "characters/player2/hero.md", ModeWrite, false},
		// Segment boundary: "player1fake" is NOT "player1"'s directory.
		{"player write sibling-id character", player1, "characters/player1fake/hero.md", ModeWrite, false},
		{"player read sibling-id character (public reads)", player1, "characters/player1fake/hero.md", ModeRead, true},
		{"player read another players character (public reads)", player1, "characters/player2/hero.md", ModeRead, true},

		// campaign/ is GM-only (tech-stack §10): no read, no write.
		{"player read campaign", player1, "campaign/session-1.md", ModeRead, false},
		{"player write campaign", player1, "campaign/session-1.md", ModeWrite, false},
		{"player read campaign subdir", player1, "campaign/lore/deep.md", ModeRead, false},

		{"player read rules (public)", player1, "rules/classes/barbarian.md", ModeRead, true},
		{"player write rules (no grant)", player1, "rules/classes/barbarian.md", ModeWrite, false},
		{"player read assets (public)", player1, "assets/map.png", ModeRead, true},

		{"unknown role denied", pluginapi.Identity{ID: "x", Role: "admin"}, "rules/foo.md", ModeRead, false},
		{"campaign role denied", pluginapi.Identity{ID: "x", Role: "campaign"}, "rules/foo.md", ModeRead, false},
		{"empty id denied", pluginapi.Identity{ID: "", Role: RoleGM}, "rules/foo.md", ModeRead, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.Can(ctx, tc.ident, tc.path, tc.mode)
			if err != nil {
				t.Fatalf("Can(%q, %s): unexpected error: %v", tc.path, tc.mode, err)
			}
			if got != tc.want {
				t.Errorf("Can(%q, %s) = %v, want %v", tc.path, tc.mode, got, tc.want)
			}
		})
	}
}

func TestCanRejectsBadPaths(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, gm.ID, RoleGM)
	a := New(db)
	ctx := context.Background()
	for _, p := range []string{
		"",                 // empty
		"../../etc/passwd", // traversal
		"..",               // traversal root
		"/abs.md",          // absolute
		"a\\b.md",          // backslash
		"x/../../y",        // mid-path traversal
		"campaign/../lore", // canonicalizes clean — see below, allowed
	} {
		t.Run("path="+p, func(t *testing.T) {
			got, err := a.Can(ctx, gm, p, ModeRead)
			switch p {
			case "campaign/../lore": // path.Clean collapses to "lore" — valid
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !got {
					t.Error("Can = false, want true for canonicalized path")
				}
			default:
				if err == nil {
					t.Fatalf("expected error, got (false, nil)")
				}
				if !errors.Is(err, pluginapi.ErrForbidden) {
					t.Errorf("error not ErrForbidden-wrapped: %v", err)
				}
				if got {
					t.Error("Can = true alongside error")
				}
			}
		})
	}
}

func TestCanUnknownMode(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, gm.ID, RoleGM)
	a := New(db)
	_, err := a.Can(context.Background(), gm, "rules/foo.md", Mode("exec"))
	if err == nil || !errors.Is(err, pluginapi.ErrForbidden) {
		t.Errorf("unknown mode: want ErrForbidden-wrapped error, got %v", err)
	}
}

func TestOverrideGrants(t *testing.T) {
	ctx := context.Background()
	t.Run("grant player write on rules/", func(t *testing.T) {
		db := openTestDB(t)
		seedUser(t, db, player1.ID, RolePlayer)
		seedUser(t, db, player2.ID, RolePlayer)
		a := New(db)

		if ok, _ := a.Can(ctx, player1, "rules/foo.md", ModeWrite); ok {
			t.Fatal("pre-grant write unexpectedly allowed")
		}
		if _, err := db.Exec(`INSERT INTO acls(path_prefix, role, user_id) VALUES(?, ?, ?)`,
			"rules/", RolePlayer, player1.ID); err != nil {
			t.Fatalf("insert acls row: %v", err)
		}
		a.Invalidate() // bypass the 30s TTL for this re-check
		for _, mode := range []Mode{ModeWrite, ModeRead} {
			if ok, err := a.Can(ctx, player1, "rules/hooks/sneak-attack.md", mode); err != nil || !ok {
				t.Errorf("post-grant Can(rules/hooks/sneak-attack.md, %s) = %v, err %v; want true", mode, ok, err)
			}
		}
		// Grant is per-user: player2 gains nothing.
		if ok, _ := a.Can(ctx, player2, "rules/foo.md", ModeWrite); ok {
			t.Error("player2 wrongly granted by player1's row")
		}
		// campaign/ is GM-only and overrides only ADD grants — but an override
		// on campaign/ for a validated player WOULD grant (rows are absolute,
		// additive). Documented behavior: keep the deny default untested there;
		// assert a non-covered campaign path stays denied for player1.
		if ok, _ := a.Can(ctx, player1, "campaign/x.md", ModeRead); ok {
			t.Error("non-granted campaign read unexpectedly allowed")
		}
	})

	t.Run("role column must match users.role for that user", func(t *testing.T) {
		db := openTestDB(t)
		seedUser(t, db, player1.ID, RolePlayer)
		a := New(db)
		// Row claims player1 is "gm": users table says "player" → no grant.
		if _, err := db.Exec(`INSERT INTO acls(path_prefix, role, user_id) VALUES(?, ?, ?)`,
			"rules/", RoleGM, player1.ID); err != nil {
			t.Fatalf("insert acls row: %v", err)
		}
		a.Invalidate()
		if ok, _ := a.Can(ctx, player1, "rules/foo.md", ModeWrite); ok {
			t.Error("override granted despite users-table role mismatch")
		}
	})

	t.Run("unknown user cannot hold grants", func(t *testing.T) {
		db := openTestDB(t)
		seedUser(t, db, player1.ID, RolePlayer)
		a := New(db)
		if _, err := db.Exec(`INSERT INTO acls(path_prefix, role, user_id) VALUES(?, ?, ?)`,
			"rules/", RolePlayer, "ghost"); err != nil {
			t.Fatalf("insert acls row: %v", err)
		}
		a.Invalidate()
		if ok, _ := a.Can(ctx, pluginapi.Identity{ID: "ghost", Role: RolePlayer}, "rules/foo.md", ModeWrite); ok {
			t.Error("ghost user granted without a users row")
		}
	})

	t.Run("grant prefix is segment-boundary matched", func(t *testing.T) {
		db := openTestDB(t)
		seedUser(t, db, player1.ID, RolePlayer)
		a := New(db)
		if _, err := db.Exec(`INSERT INTO acls(path_prefix, role, user_id) VALUES(?, ?, ?)`,
			"rules", RolePlayer, player1.ID); err != nil {
			t.Fatalf("insert acls row: %v", err)
		}
		a.Invalidate()
		if ok, _ := a.Can(ctx, player1, "rulesX/foo.md", ModeWrite); ok {
			t.Error("prefix rules leaked into rulesX/")
		}
		if ok, _ := a.Can(ctx, player1, "rules/foo.md", ModeWrite); !ok {
			t.Error("prefix rules did not cover rules/foo.md")
		}
	})
}

func TestGrantsCacheTTLAndInvalidate(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, player1.ID, RolePlayer)
	a := New(db)
	ctx := context.Background()
	// Warm the cache BEFORE the row exists → snapshot without the row.
	if ok, err := a.Can(ctx, player1, "rules/foo.md", ModeWrite); err != nil || ok {
		t.Fatalf("warm call before any row already granted: %v (err %v)", ok, err)
	}
	a.rowsAt = time.Now().Add(1 * time.Hour) // freeze the snapshot inside TTL
	if _, err := db.Exec(`INSERT INTO acls(path_prefix, role, user_id) VALUES(?, ?, ?)`,
		"rules/", RolePlayer, player1.ID); err != nil {
		t.Fatalf("insert acls row: %v", err)
	}
	if ok, err := a.Can(ctx, player1, "rules/foo.md", ModeWrite); err != nil || ok {
		t.Fatalf("stale snapshot leaked grant: %v (err %v)", ok, err)
	}
	a.rowsAt = time.Now().Add(-31 * time.Second) // force TTL expiry on next call
	if ok, err := a.Can(ctx, player1, "rules/foo.md", ModeWrite); err != nil || !ok {
		t.Fatalf("expired TTL did not reload grant: %v (err %v)", ok, err)
	}
	// And Invalidate() reloads immediately even inside the TTL window.
	a.Invalidate()
	if ok, _ := a.Can(ctx, player1, "rules/foo.md", ModeWrite); !ok {
		t.Error("Invalidate() did not refresh grants")
	}
}
