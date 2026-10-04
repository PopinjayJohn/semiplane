package demo_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	democmd "github.com/semiplane/semiplane/cmd/server/demo"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/demo"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/domain/systems/notfive"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// The versions the fixture and the fake binary agree on, as constants so a test that
// changes one does not have to remember the other.
const (
	harnessArtefactVersion = "0.1.0"
	harnessOtherVersion    = "9.9.9"
	harnessPassword        = "the-password-the-harness-chose"
)

// harness is one demo artefact, one database and the registries the composition root would
// have registered.
//
// **It builds the registries the way `cmd/server/systems.go` does**, rather than inventing
// a fixture: the real 5e edition compiled from its embedded packs, registered in a real
// `plugin.Registry`, and its fingerprint computed through `realtime.FingerprintOf` from
// `dnd5e.Engine.Versions()`. That is what makes
// `TestTheRulesetVersionIsResolvedAndNeverHardcoded` an assertion about the product — the
// expected value comes from the engine, so a fingerprint hardcoded anywhere else cannot
// match it.
//
// # It holds no database handle
//
// Every store use goes through `withStore`, which opens, runs and closes. That is not a
// tidiness preference: `store.Open` claims the process's single-instance slot (ADR 0004) and
// refuses a second handle, so a harness that held one open would make **every** test that
// drives the command fail — the command opens the store itself, which is the whole of its
// contract with `admin`. A fixture holding a handle is a fixture that cannot test the
// command at all.
type harness struct {
	t *testing.T

	// root is the extracted artefact: the manifest and one directory per campaign.
	root string

	// dsn is where this harness's database lives. One directory per harness, so two
	// harnesses in one test are two instances — which is what
	// `TestTheHouseRuleLayerIsResolvedAndIsNotTheFingerprint` needs.
	dsn string

	// artefactVersion is what the fixture's manifest declares and version is what the
	// binary claims. Both are mutable because "reset proceeds through a release skew" is a
	// behaviour, and it needs the two to disagree without rebuilding the artefact.
	artefactVersion string
	version         string

	// fingerprint is what this build resolves `dnd5e` under, and module is the house-rule
	// module the fixture's manifest declares, when it declares one.
	fingerprint string
	module      string

	systems    *plugin.Registry
	houseRules *houserules.Registry

	// knownSecret reports whether `seed` should pass `--password`. False means the command
	// draws one, which is the case the D14 assertions are about.
	knownSecret bool
}

// harnessOption is a construction-time choice, because every one of them affects the artefact
// or the registries and so cannot be changed once the artefact is written.
type harnessOption func(*harness)

// withHouseRules registers a compiled-in house-rule module, so the seed has a set it can
// actually resolve. **This build ships none**, which is why the shipped manifest declares
// none and why the house-rule assertions have to register one to have anything to resolve.
func withHouseRules() harnessOption {
	return func(h *harness) {
		h.houseRules = houserules.NewRegistry()

		definition := &houserules.Definition{
			ID:    rules.ID("mild-crits"),
			Scope: determinism.Scope{determinism.CapToggle},
			Apply: func(json.RawMessage) ([]houserules.Change, error) {
				enabled := true

				return []houserules.Change{{
					Kind:   determinism.CapToggle,
					Key:    "critical_on_max",
					Toggle: &enabled,
				}}, nil
			},
		}

		if err := h.houseRules.Register(definition); err != nil {
			h.t.Fatalf("register the test house rule: %v", err)
		}
	}
}

// withModuleInManifest declares a house-rule module in the fixture's manifest **and**
// registers it, so the seed can resolve what the manifest asks for.
func withModuleInManifest(id string) harnessOption {
	return func(h *harness) {
		h.module = id

		withHouseRules()(h)
	}
}

// newHarness writes the artefact and builds the registries.
//
// **No `t.Parallel` in any test that builds one**, and the reason is `t.Setenv`: the DSN is
// set through the environment because `demo`'s `withStore` reads `config.Load`, so a test
// driving the command cannot set it any other way. `t.Setenv` forbids `t.Parallel` on its
// own, and the tests that never build a harness are free to run in parallel.
func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()

	h := &harness{
		t:               t,
		root:            t.TempDir(),
		dsn:             "file:" + filepath.Join(t.TempDir(), "demo.db"),
		artefactVersion: harnessArtefactVersion,
		version:         harnessArtefactVersion,
		houseRules:      houserules.NewRegistry(),
		knownSecret:     true,
	}

	for _, option := range options {
		option(h)
	}

	h.writeArtefact()

	registry := plugin.New()

	engine := harnessEngine(t)

	if err := registry.Register(
		plugin.Entry{System: engine, Codec: plugin.PlacementCodec{}},
	); err != nil {
		t.Fatalf("register the 5e edition: %v", err)
	}

	h.systems = registry
	h.fingerprint = harnessFingerprint(t, engine)

	t.Setenv("SEMIPLANE_DATABASE_URL", h.dsn)

	return h
}

// withStore opens this harness's database, runs fn, and closes it.
//
// **The only way a test reaches the database**, for the reason `harness` gives: the
// single-instance slot is claimed by `store.Open` and released by `Close`, so a handle that
// outlives its call would make the next one fail — including the one the command opens for
// itself.
func (h *harness) withStore(t *testing.T, fn func(*view)) {
	t.Helper()

	if _, err := config.Load(); err != nil {
		t.Fatalf("config.Load error = %v, want nil", err)
	}

	db, err := store.Open(t.Context(), h.dsn)
	if err != nil {
		t.Fatalf("store.Open(%q) error = %v, want nil", h.dsn, err)
	}

	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the store: %v", err)
		}
	}()

	fn(&view{Store: db, t: t})
}

// harnessEngine compiles the same edition `cmd/server`'s `defaultEdition` names.
//
// The constant is repeated rather than imported, because it is unexported in `package main`
// and importing it is impossible. `TestTheEditionThisHarnessUsesIsTheOneTheCompositionRootNames`
// closes that gap by reading the composition root's source and comparing the two spellings.
func harnessEngine(t *testing.T) *dnd5e.Engine {
	t.Helper()

	edition, err := overlays.ByID(overlays.Dnd5e2024)
	if err != nil {
		t.Fatalf("overlays.ByID error = %v, want nil", err)
	}

	engine, err := edition.System()
	if err != nil {
		t.Fatalf("edition.System error = %v, want nil", err)
	}

	return engine
}

// harnessFingerprint is what this build resolves under, computed the one way the tree can.
func harnessFingerprint(t *testing.T, engine *dnd5e.Engine) string {
	t.Helper()

	versions := engine.Versions()

	fingerprint, err := realtime.FingerprintOf(realtime.Descriptor{
		System:      versions.System,
		Ruleset:     versions.Ruleset,
		BasePack:    versions.BasePack,
		OverlayPack: versions.OverlayPack,
	})
	if err != nil {
		t.Fatalf("FingerprintOf error = %v, want nil", err)
	}

	return fingerprint.String()
}

// TestTheEditionThisHarnessUsesIsTheOneTheCompositionRootNames closes the loop
// `harnessEngine` opens.
//
// A harness resolving 2014 while the binary resolves 2024 would produce a `ruleset_version`
// no binary in this repository agrees with — and every assertion about "this build's
// fingerprint" would be green over the wrong edition.
//
// **It compares the identifier, not the edition's value.** `overlays.EditionID` is a string
// type, so nothing at runtime maps a constant's *name* to its *value*: a source comparison can
// check that both sides name `overlays.Dnd5e2024`, and that the constant's value is `2024`
// (asserted separately below), but it cannot notice the composition root naming a different
// constant that happens to hold the same value. That limit is stated rather than papered over.
func TestTheEditionThisHarnessUsesIsTheOneTheCompositionRootNames(t *testing.T) {
	t.Parallel()

	if got := string(overlays.Dnd5e2024); got != "2024" {
		t.Errorf("overlays.Dnd5e2024 = %q; this file compares the constant's spelling, so its "+
			"value has to stay put for the comparison to mean anything", got)
	}

	source, err := os.ReadFile(filepath.Join("..", "systems.go"))
	if err != nil {
		t.Fatalf("read the composition root's systems.go: %v", err)
	}

	const want = "const defaultEdition = overlays.Dnd5e2024"

	if !strings.Contains(string(source), want) {
		t.Errorf("cmd/server/systems.go no longer reads %q, and this harness resolves under it; "+
			"every fingerprint assertion in this file would be green over an edition the binary "+
			"does not ship", want)
	}
}

// writeArtefact lays down the manifest and one directory per campaign.
//
// The campaign directories are created with mode 0755 **on purpose**: a `tar` extraction
// produces that, so the seed's tightening of a content root has something real to do, and a
// fixture shipping 0700 would let a regression in that tightening pass unnoticed.
func (h *harness) writeArtefact() {
	h.t.Helper()

	source := shippedManifestSource(h.artefactVersion)

	if h.module != "" {
		const anchor = "    members:\n      - username: demo-gm\n        role: gm\n" +
			"      - username: demo-player\n        role: player\n"

		replacement := anchor + "    rule_modules:\n      - id: " + h.module + "\n        position: 0\n"

		if !strings.Contains(source, anchor) {
			h.t.Fatalf("the fixture manifest no longer contains the anchor this harness edits; " +
				"the manifest and the harness have drifted")
		}

		source = strings.Replace(source, anchor, replacement, 1)
	}

	writeFile(h.t, filepath.Join(h.root, demo.ManifestFileName), source)

	for _, slug := range []string{"greyhaven", "public-post", "forgotten-realm"} {
		directory := filepath.Join(h.root, slug)

		if err := os.MkdirAll(directory, 0o755); err != nil {
			h.t.Fatalf("create the vault for %q: %v", slug, err)
		}

		if err := os.Chmod(directory, 0o755); err != nil {
			h.t.Fatalf("set the vault mode for %q: %v", slug, err)
		}

		writeFile(h.t, filepath.Join(directory, "index.md"),
			"---\nkind: index\ntitle: "+slug+"\n---\n\n# "+slug+"\n\nThe demo vault's index.\n")
	}
}

// deps are the three values `cmd/server`'s composition root hands to the command.
func (h *harness) deps() democmd.Dependencies {
	return democmd.Dependencies{
		Systems:     h.systems,
		Fingerprint: h.fingerprint,
		HouseRules:  h.houseRules,
	}
}

// options are this harness's seed options.
//
// Every value is explicit rather than defaulted, because a default here would be a second
// answer to "what does the seed get" and the tests that vary one of them would be varying a
// fixture rather than the product.
func (h *harness) options(t *testing.T) (demo.Options, error) {
	t.Helper()

	fingerprints, err := demo.BuildFingerprints(h.systems, h.fingerprint)
	if err != nil {
		return demo.Options{}, fmt.Errorf("BuildFingerprints: %w", err)
	}

	return demo.Options{
		Root:         h.root,
		Fingerprints: fingerprints,
		HouseRules:   h.houseRules,
		Version:      h.version,
		Password:     demo.NewSecret(harnessPassword),
	}, nil
}

// seed runs the seed directly against a store this harness opens, and fails the test unless
// it succeeds.
//
// **Direct rather than through the command**, because the command opens the store itself and
// a test wanting to assert on the resulting rows cannot hold a handle at the same time.
func (h *harness) seed(t *testing.T) demo.Result {
	t.Helper()

	result, err := h.seedExpectingFailure(t)
	if err != nil {
		t.Fatalf("demo.Seed error = %v, want nil", err)
	}

	return result
}

// seedExpectingFailure runs the seed and hands back whatever it says — the `Result` a partial
// seed carries, and the error.
func (h *harness) seedExpectingFailure(t *testing.T) (demo.Result, error) {
	t.Helper()

	options, err := h.options(t)
	if err != nil {
		t.Fatalf("BuildFingerprints error = %v, want nil", err)
	}

	manifest, err := demo.Load(h.root)
	if err != nil {
		t.Fatalf("Load(%s) error = %v, want nil", h.root, err)
	}

	var result demo.Result

	h.withStore(t, func(db *view) {
		options.Store = db.Store

		result, err = demo.Seed(t.Context(), manifest, options)
	})
	if err != nil {
		return result, fmt.Errorf("demo.Seed: %w", err)
	}

	return result, nil
}

// run dispatches the command and returns what it printed, with the error rather than a
// failure. With `knownSecret` set, a `seed` carries `--password`; without it, the command
// draws one — the case the D14 assertions are about.
func (h *harness) run(t *testing.T, subcommand string) (stdout, stderr string, err error) {
	t.Helper()

	args := []string{subcommand, "--root", h.root}
	if subcommand == "seed" && h.knownSecret {
		args = append(args, "--password", harnessPassword)
	}

	var out, errs strings.Builder

	err = democmd.Run(t.Context(), args, h.deps(),
		democmd.Env{Stdout: &out, Stderr: &errs, Version: h.version})

	return out.String(), errs.String(), err
}

// runOrFail dispatches the command and fails the test if it refused.
func (h *harness) runOrFail(t *testing.T, subcommand string) (stdout, stderr string) {
	t.Helper()

	stdout, stderr, err := h.run(t, subcommand)
	if err != nil {
		t.Fatalf("demo %s error = %v (stdout: %s, stderr: %s)", subcommand, err, stdout, stderr)
	}

	return stdout, stderr
}

// view is one open database handle with the reads the assertions need.
//
// **A wrapper rather than the harness**, because `store.Open` claims the process's
// single-instance slot (ADR 0004) and releases it on `Close`: a handle has to live inside one
// `withStore` call and nowhere else, and a type that only exists for the duration of that
// call cannot outlive it.
type view struct {
	*store.Store
	t *testing.T
}

// campaign returns one seeded campaign.
func (v *view) campaign(slug string) domain.Campaign {
	v.t.Helper()

	campaign, err := v.CampaignBySlug(v.t.Context(), slug)
	if err != nil {
		v.t.Fatalf("CampaignBySlug(%q) error = %v, want nil", slug, err)
	}

	return campaign
}

// campaigns lists every campaign on the instance.
func (v *view) campaigns() []domain.Campaign {
	v.t.Helper()

	campaigns, err := v.Campaigns(v.t.Context())
	if err != nil {
		v.t.Fatalf("Campaigns error = %v, want nil", err)
	}

	return campaigns
}

// account returns one seeded account.
func (v *view) account(username string) domain.User {
	v.t.Helper()

	account, err := v.UserByUsername(v.t.Context(), username)
	if err != nil {
		v.t.Fatalf("UserByUsername(%q) error = %v, want nil", username, err)
	}

	return account
}

// membership returns one account's membership of one campaign.
func (v *view) membership(slug, username string) domain.Membership {
	v.t.Helper()

	campaign, err := v.CampaignBySlug(v.t.Context(), slug)
	if err != nil {
		v.t.Fatalf("CampaignBySlug(%q) error = %v, want nil", slug, err)
	}

	membership, err := v.Membership(v.t.Context(), campaign.ID, v.account(username).ID)
	if err != nil {
		v.t.Fatalf("the membership of %q in %q: %v", username, slug, err)
	}

	return membership
}

// ruleModules returns one campaign's house-rule rows.
func (v *view) ruleModules(slug string) []determinism.Module {
	v.t.Helper()

	campaign, err := v.CampaignBySlug(v.t.Context(), slug)
	if err != nil {
		v.t.Fatalf("CampaignBySlug(%q) error = %v, want nil", slug, err)
	}

	rows, err := v.RuleModulesForCampaign(v.t.Context(), campaign.ID)
	if err != nil {
		v.t.Fatalf("RuleModulesForCampaign(%q) error = %v, want nil", slug, err)
	}

	return rows
}

// countRows counts a table's rows, for the assertions a store method cannot make:
// `campaign_state` has no reader outside `internal/realtime`, and a reset's whole job is that
// its rows are gone.
//
// The table name is a literal at each call site in this file, never a value derived from
// input — which is what keeps this a constant query rather than a string built from a name a
// caller chose.
func (v *view) countRows(table string) int {
	v.t.Helper()

	var count int

	row := v.DB().QueryRowContext(v.t.Context(), "SELECT COUNT(*) FROM "+table)
	if err := row.Scan(&count); err != nil {
		v.t.Fatalf("count %s: %v", table, err)
	}

	return count
}

// readState returns one campaign's `campaign_state` blob and version column.
func (v *view) readState(slug string) (blob []byte, version int64) {
	v.t.Helper()

	row := v.DB().QueryRowContext(v.t.Context(),
		"SELECT state, version FROM campaign_state WHERE campaign_id = ?", v.campaign(slug).ID)

	if err := row.Scan(&blob, &version); err != nil {
		v.t.Fatalf("read campaign_state for %q: %v", slug, err)
	}

	return blob, version
}

// promoteToAdmin flips an account's `is_admin`, as an operator who ran
// `semiplane admin create --admin` by hand would have left an account the artefact happens
// to name.
func (v *view) promoteToAdmin(username string) {
	v.t.Helper()

	if _, err := v.DB().ExecContext(v.t.Context(),
		"UPDATE users SET is_admin = 1 WHERE username = ?", username); err != nil {
		v.t.Fatalf("promote %q: %v", username, err)
	}
}

// joinCampaignOutsideTheDemo adds a campaign the artefact does not name and puts an account
// in it, so a reset has an account it must refuse to delete.
func (v *view) joinCampaignOutsideTheDemo(slug, username string) {
	v.t.Helper()

	campaign, err := v.CreateCampaign(v.t.Context(), domain.Campaign{
		Slug:        slug,
		Name:        slug,
		ContentRoot: filepath.Join(v.t.TempDir(), slug),
		Visibility:  domain.VisibilityPrivate,
		SystemID:    "dnd5e",
	})
	if err != nil {
		v.t.Fatalf("create the unrelated campaign %q: %v", slug, err)
	}

	if _, err := v.CreateMembership(v.t.Context(), domain.Membership{
		CampaignID: campaign.ID,
		UserID:     v.account(username).ID,
		Role:       domain.RolePlayer,
	}); err != nil {
		v.t.Fatalf("add the unrelated membership: %v", err)
	}
}

// printedPassword extracts the drawn credential from the command's output.
//
// **By position after the marker the command prints**, and it fails the test rather than
// returning an empty string: a password assertion that silently skipped because the extraction
// found nothing is a green light wired to nothing, which is the defect class this repository
// keeps meeting.
func printedPassword(t *testing.T, stdout string) string {
	t.Helper()

	const marker = "printed once and stored nowhere in plain text:"

	// `Cut` returns (before, after, found) and it is the *second* value this wants —
	// binding the first and reading it as the tail is a bug this helper shipped with,
	// and it produced a "password" of "Demo account password," which failed verification
	// rather than passing, so the test caught it rather than hiding it.
	_, after, found := strings.Cut(stdout, marker)
	if !found {
		t.Fatalf("the output does not carry the password marker:\n%s", stdout)
	}

	// The first non-empty line after the marker, rather than a fixed offset: the command
	// prints the credential, a blank line, and then its summary, and "the second line"
	// would be the empty one and would make this assertion silently vacuous.
	for line := range strings.SplitSeq(after, "\n") {
		if credential := strings.TrimSpace(line); credential != "" {
			return credential
		}
	}

	t.Fatalf("the password marker is not followed by a credential:\n%s", stdout)

	return ""
}

// snapshotTree renders a whole directory tree as a sorted list of `path|mode|sha256` lines.
//
// **Content hash and mode, both.** "The files are still there" would be satisfied by a reset
// that rewrote a page or loosened a directory's mode, and both are changes to the vault — the
// one thing a reset is required not to touch.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()

	var lines []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", root, err)
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relativise %s: %w", path, err)
		}

		digest := ""
		if !entry.IsDir() {
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", path, readErr)
			}

			sum := sha256.Sum256(contents)
			digest = hex.EncodeToString(sum[:])
		}

		lines = append(lines, fmt.Sprintf("%s|%o|%s", relative, info.Mode().Perm(), digest))

		return nil
	})
	if err != nil {
		t.Fatalf("walk the artefact: %v", err)
	}

	// Sorted explicitly rather than relying on the walk's order, so the snapshot says what it
	// depends on.
	slices.Sort(lines)

	return strings.Join(lines, "\n")
}

// recordingHandler captures every log line's message and attributes, for the S-12.3 assertion
// that the demo credential never reaches a log.
type recordingHandler struct {
	mu      sync.Mutex
	written bytes.Buffer
}

func (r *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (r *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.written.WriteString(record.Message)
	r.written.WriteByte('\n')

	record.Attrs(func(attribute slog.Attr) bool {
		r.written.WriteString(attribute.Value.String())
		r.written.WriteByte('\n')

		return true
	})

	return nil
}

func (r *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *recordingHandler) WithGroup(string) slog.Handler { return r }

func (r *recordingHandler) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.written.String()
}

// The harness satisfies the handler contract at compile time rather than by a test that fails
// only when the first log line arrives.
var _ slog.Handler = (*recordingHandler)(nil)

// notfiveSystem is the second gameplay system `BuildFingerprints` must refuse to answer for.
// The real package rather than a stub, because S-14.9's counterexample exists and a
// hand-written stub would be a `rules.System` this repository has no other reason to maintain.
func notfiveSystem() rules.System { return notfive.New() }

// slicesSorted reports whether a list is in ascending order.
//
// Named rather than an inline `slices.IsSorted` at the call site, because the assertion is
// about the *document's* promise — its placements are sorted by id and its condition set is
// held sorted — and the promise is the thing being tested.
func slicesSorted(values []string) bool { return slices.IsSorted(values) }

// writeFile writes one file, creating its parents.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
