package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/store"
)

// TestParseFlagsAcceptsBothSpellings: `--name value` and `--name=value` are the
// two forms an operator will type, and a parser that accepts only one of them is
// a parser somebody works around by quoting.
func TestParseFlagsAcceptsBothSpellings(t *testing.T) {
	t.Parallel()

	parsed, err := parseFlags([]string{"--username", "ada", "--slug=greyhaven"}, "admin")
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if got := parsed.String("username", ""); got != "ada" {
		t.Errorf("username = %q, want %q", got, "ada")
	}

	if got := parsed.String("slug", ""); got != "greyhaven" {
		t.Errorf("slug = %q, want %q", got, "greyhaven")
	}
}

// TestParseFlagsAcceptsSingleHyphen: an operator types `-username` and
// `-username=ada` as readily as the double-hyphen form, and Go's own flag
// package accepts both.
func TestParseFlagsAcceptsSingleHyphen(t *testing.T) {
	t.Parallel()

	parsed, err := parseFlags([]string{"-username=ada"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if got := parsed.String("username", ""); got != "ada" {
		t.Errorf("username = %q, want %q", got, "ada")
	}
}

// TestParseFlagsTreatsBooleansAsValueless: `--public` takes no value, so the
// token after it must not be swallowed. An operator writing
// `--public --ruleset v1` would otherwise lose the ruleset.
func TestParseFlagsTreatsBooleansAsValueless(t *testing.T) {
	t.Parallel()

	parsed, err := parseFlags([]string{"--public", "--ruleset", "v1"}, "public")
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if !parsed.Bool("public") {
		t.Error("public = false, want true")
	}

	if got := parsed.String("ruleset", ""); got != "v1" {
		t.Errorf("ruleset = %q, want %q", got, "v1")
	}
}

// TestParseFlagsStopsAtDoubleHyphen: a value beginning with a hyphen is
// reachable by ending flag parsing, which is why `--` is handled.
func TestParseFlagsStopsAtDoubleHyphen(t *testing.T) {
	t.Parallel()

	parsed, err := parseFlags([]string{"--username", "ada", "--", "--not-a-flag"}, "admin")
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if got := parsed.String("username", ""); got != "ada" {
		t.Errorf("username = %q, want %q", got, "ada")
	}

	if len(parsed.args) != 1 || parsed.args[0] != "--not-a-flag" {
		t.Errorf("args = %v, want [--not-a-flag]", parsed.args)
	}
}

// TestParseFlagsRefusesAValuelessValueFlag: `--username` with nothing after it is
// a typo, and silently treating it as an empty username would produce a confusing
// error several layers down.
func TestParseFlagsRefusesAValuelessValueFlag(t *testing.T) {
	t.Parallel()

	if _, err := parseFlags([]string{"--username"}); err == nil {
		t.Fatal("parseFlags(--username) = nil, want a usage error")
	}
}

// TestParseFlagsStringFallsBack: an omitted flag takes the caller's default, and
// the default is where "stated rather than defaulted" is visible — the
// registrar decides, not the parser.
func TestParseFlagsStringFallsBack(t *testing.T) {
	t.Parallel()

	parsed, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}

	if got := parsed.String("system", "fallback"); got != "fallback" {
		t.Errorf("system = %q, want %q", got, "fallback")
	}

	if parsed.Bool("public") {
		t.Error("public = true with no arguments, want false")
	}
}

// TestAdminCreateRefusesAnEmptyUsername: a usage error, and exit code 2, rather
// than a row holding an empty name.
func TestAdminCreateRefusesAnEmptyUsername(t *testing.T) {
	t.Parallel()

	previous := captureArgs(t)
	defer previous()

	os.Args = []string{"semiplane", "admin", "create"}

	err := run(os.Args[1:])
	if err == nil {
		t.Fatal("admin create with no --username = nil, want a usage error")
	}

	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error = %q, want it to name the missing flag", err.Error())
	}
}

// TestAdminCreateRefusesAnUnknownSubcommand: `semiplane admin nonsense` is a
// usage error, not a server that starts.
func TestAdminCreateRefusesAnUnknownSubcommand(t *testing.T) {
	t.Parallel()

	if err := run([]string{"admin", "nonsense"}); err == nil {
		t.Fatal("admin nonsense = nil, want a usage error")
	}

	if err := run([]string{"admin", "campaign", "nonsense"}); err == nil {
		t.Fatal("admin campaign nonsense = nil, want a usage error")
	}

	if err := run([]string{"admin"}); err == nil {
		t.Fatal("admin with no subcommand = nil, want a usage error")
	}
}

// TestAdminCampaignAddRequiresOwnerAndSlug: both are required, and both failures
// are named rather than defaulting into a campaign nobody can run.
func TestAdminCampaignAddRequiresOwnerAndSlug(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"admin", "campaign", "add"},
		{"admin", "campaign", "add", "--slug", "greyhaven"},
		{"admin", "campaign", "add", "--owner", "ada"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%v) = nil, want a usage error", args)
		}
	}
}

// TestAdminUsageNamesTheFlagsItRequires: the usage text is the only place an
// operator learns that --owner is required, so a flag appearing nowhere in it is
// a flag that will be discovered by failing.
func TestAdminUsageNamesTheFlagsItRequires(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--username", "--password", "--admin", "--slug", "--name", "--owner", "--system", "--public", "--ruleset"} {
		if !strings.Contains(adminUsage, flag) {
			t.Errorf("adminUsage does not document %s", flag)
		}
	}
}

// TestMainUsageNamesTheAdminSubcommands: `semiplane admin` must not report that
// nothing is registered.
func TestMainUsageNamesTheAdminSubcommands(t *testing.T) {
	t.Parallel()

	for _, want := range []string{"admin create", "admin campaign add"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usageText does not mention %q", want)
		}
	}
}

// TestAdminCreateThenRegisterCampaignIsTheWholeFirstRun walks the sequence an
// operator actually performs on a fresh instance, against a real database: create
// the account, register a campaign naming that account as its GM, and read both
// back.
//
// The point is the join. Each command is individually covered by its own
// package's tests; what this asserts is that the pair agree on the things that
// only show up when both have run — that the username the CLI wrote is the name
// `admin campaign add --owner` resolves, and that the membership it seeds is
// readable as a GM by the access layer that will enforce it.
func TestAdminCreateThenRegisterCampaignIsTheWholeFirstRun(t *testing.T) {
	base := t.TempDir()
	database := filepath.Join(base, "semiplane.db")

	t.Setenv("SEMIPLANE_DATABASE_URL", "file:"+database)
	t.Setenv("SEMIPLANE_CONTENT_ROOT_BASE", filepath.Join(base, "vaults"))

	const password = "correct horse battery staple"

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	db, err := store.Open(t.Context(), "file:"+database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	owner, err := db.CreateUser(t.Context(), domain.User{
		Username:     "ada",
		PasswordHash: hash,
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// The registrar the CLI uses, and the one the HTTP route will use.
	registrar := campaigns.NewRegistrar(db, filepath.Join(base, "vaults"), nil)

	campaign, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug:           "greyhaven",
		Name:           "Greyhaven",
		SystemID:       "5e-2024",
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPrivate,
		OwnerID:        owner.ID,
	})
	if err != nil {
		t.Fatalf("register campaign: %v", err)
	}

	// The content root exists, is confined, and is 0700: a root readable by
	// every account on the host is a campaign readable by every account on the
	// host, bypassing the S-8 matrix entirely.
	info, err := os.Stat(campaign.ContentRoot)
	if err != nil {
		t.Fatalf("content root was not created: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("content root mode = %o, want 700", perm)
	}

	if !filepath.IsAbs(campaign.ContentRoot) {
		t.Errorf("content root %q is not absolute; the schema requires it", campaign.ContentRoot)
	}

	// The seeded owner reads back as a GM — the join that makes the whole
	// sequence work, and the row the access layer will consult on every request.
	membership, err := db.Membership(t.Context(), campaign.ID, owner.ID)
	if err != nil {
		t.Fatalf("read owner membership: %v", err)
	}

	if membership.Role != domain.RoleGM {
		t.Errorf("owner role = %q, want %q", membership.Role, domain.RoleGM)
	}

	// And the matrix resolves that row to the GM tier, which is the only reason
	// any of this is useful.
	tier := domain.ResolveAccess(campaign, &membership, domain.Requestor{
		UserID: owner.ID, Authenticated: true,
	})
	if !tier.CanEdit() {
		t.Errorf("owner tier = %s, want one that can edit content", tier)
	}

	if err := db.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}

// captureArgs swaps os.Args for the duration of a test and returns the function
// that puts it back.
func captureArgs(t *testing.T) func() {
	t.Helper()

	saved := os.Args

	return func() { os.Args = saved }
}
