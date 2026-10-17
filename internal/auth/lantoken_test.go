package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

func TestLanCodeCreateShapeAndHappyPath(t *testing.T) {
	db := openAuthDB(t)
	svc := NewLanCodeService(db)
	ctx := context.Background()

	code, err := svc.Create(ctx, "u1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code %q is %d chars, want 6", code, len(code))
	}
	if _, err := strconv.Atoi(code); err != nil {
		t.Fatalf("code %q not numeric: %v", code, err)
	}
	// Round-trip the %06d framing: n must re-render to the exact code.
	n, _ := strconv.Atoi(code)
	if got := fmt.Sprintf("%06d", n); got != code {
		t.Fatalf("code %q is not canonical %%06d form (rebuilt as %q)", code, got)
	}

	userID, err := svc.Consume(ctx, code, "192.0.2.7")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if userID != "u1" {
		t.Fatalf("Consume = %q, want u1", userID)
	}
}

func TestLanCodeSingleUse(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewLanCodeService(db)
	ctx := context.Background()

	code, err := svc.Create(ctx, "u1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Consume(ctx, code, "ip-a"); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if _, err := svc.Consume(ctx, code, "ip-b"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("second Consume = %v, want errors.Is(ErrUnauthorized) — reuse must fail", err)
	}
	// The row still exists but remains spent (hash column only).
	var hash, used string
	if err := db.QueryRow(`SELECT hash, used FROM lan_codes`).Scan(&hash, &used); err != nil {
		t.Fatalf("read lan_codes: %v", err)
	}
	if hash != HashToken(code) {
		t.Fatal("plaintext code persisted in lan_codes.hash")
	}
	if used != "1" {
		t.Fatalf("used = %q, want 1 after first consume", used)
	}
}

func TestLanCodeWrongCodeUnauthorized(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewLanCodeService(db)
	ctx := context.Background()

	code, err := svc.Create(ctx, "u1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Pick a wrong code distinct from the issued one.
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	userID, err := svc.Consume(ctx, wrong, "ip-1")
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Consume wrong code = (%q, %v), want errors.Is(ErrUnauthorized)", userID, err)
	}
	// The real code still works after wrong guesses (within rate window).
	if _, err := svc.Consume(ctx, code, "ip-1"); err != nil {
		t.Fatalf("real code consumed by wrong attempts: %v", err)
	}
}

func TestLanCodeExpiry(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewLanCodeService(db)
	ctx := context.Background()

	code, err := svc.Create(ctx, "u1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Age the row past its TTL (unix-second columns: expiry already in
	// the past = expired; no sleeping required).
	if _, err := db.Exec(
		`UPDATE lan_codes SET expires_at = ? WHERE hash = ?`,
		timeNow().Unix()-1, HashToken(code)); err != nil {
		t.Fatal(err)
	}
	userID, err := svc.Consume(ctx, code, "ip-1")
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Consume expired = (%q, %v), want errors.Is(ErrUnauthorized)", userID, err)
	}
	// Expired rows do not consume the real code's budget forever: check
	//the row is now used/expired, i.e. still unusable.
	if _, err := svc.Consume(ctx, code, "ip-1"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Consume expired after expiry = %v, want ErrUnauthorized", err)
	}
}

func TestLanCodeRateLimit(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	// Inject a tight limiter (2 attempts / window) for a deterministic trip.
	svc := &LanCodeService{DB: db, limiter: newIPLimiter(2, time.Minute)}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := svc.Consume(ctx, "999999", "10.0.0.1"); !errors.Is(err, pluginapi.ErrUnauthorized) {
			t.Fatalf("attempt %d = %v, want ErrUnauthorized (below limit)", i+1, err)
		}
	}
	if _, err := svc.Consume(ctx, "999999", "10.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("attempt 3 = %v, want errors.Is(ErrRateLimited)", err)
	}
	// Other IPs have their own budget.
	if _, err := svc.Consume(ctx, "999999", "10.0.0.2"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("fresh IP threw rate limit: %v", err)
	}
	// A valid code is still burnable within budget on a fresh IP.
	okCode, err := svc.Create(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := svc.Consume(ctx, okCode, "10.0.0.3")
	if err != nil || userID != "u1" {
		t.Fatalf("Consume valid within budget = (%q, %v)", userID, err)
	}
	// Sliding window: old hits expire.
	free := newIPLimiter(1, time.Nanosecond)
	if !free.allow("k") {
		t.Fatal("fresh key rejected")
	}
	time.Sleep(time.Millisecond)
	if !free.allow("k") {
		t.Fatal("window pretend-expired hits not pruned")
	}
}

func TestLanCodeCleanupExpired(t *testing.T) {
	db := openAuthDB(t)
	svc := NewLanCodeService(db)
	code, err := svc.Create(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE lan_codes SET expires_at = 0 WHERE hash = ?`, HashToken(code)); err != nil {
		t.Fatal(err)
	}
	if err := svc.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_codes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("lan_codes after cleanup = %d rows, want 0", n)
	}
}
