package demo_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	democmd "github.com/semiplane/semiplane/cmd/server/demo"
	"github.com/semiplane/semiplane/internal/demo"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// # Where every test in this file lives, and why two of them touch nothing but the parser
//
// The schema, the version check and the password generator are pure functions of bytes, so
// their tests are `t.Parallel` and hold no database. Everything else builds a harness, which
// sets an environment variable and therefore cannot run in parallel. Nothing here holds a
// store handle across a call: `store.Open` claims the process's single-instance slot
// (ADR 0004), so every database assertion happens inside one `withStore` block.

// TestTheDemoManifestAtTheRepositoryRootParses holds the shipped artefact honest.
//
// **The real file, read from the repository, not a fixture.** A schema that only ever sees a
// test's own manifest is a schema the shipped one has never met, and the failure that produces
// is a release artefact `demo seed` refuses — discovered by the first person to download it.
func TestTheDemoManifestAtTheRepositoryRootParses(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "demo-vault")

	manifest, err := demo.Load(root)
	if err != nil {
		t.Fatalf("Load(%s) error = %v, want nil", root, err)
	}

	// The three campaigns of plan D10, asserted as a set rather than as a count, so renaming
	// one turns this red instead of quietly leaving two.
	want := map[string]bool{"greyhaven": true, "public-post": true, "forgotten-realm": true}

	if len(manifest.Campaigns) != len(want) {
		t.Fatalf("the shipped manifest declares %d campaigns, want %d",
			len(manifest.Campaigns), len(want))
	}

	for _, campaign := range manifest.Campaigns {
		if !want[campaign.Slug] {
			t.Errorf(
				"the shipped manifest declares campaign %q, which plan D10 does not",
				campaign.Slug,
			)
		}

		delete(want, campaign.Slug)

		// Every campaign declares what it demonstrates, because `make demo-check` asserts
		// coverage per declared intent and an empty list is an intent of "nothing", which
		// the gate would then hold the campaign to.
		if len(campaign.Demonstrates) == 0 {
			t.Errorf("campaign %q declares no `demonstrates`, and demo-check reads that field",
				campaign.Slug)
		}

		// And the vault directory name the registrar derives is the slug, which the seed
		// refuses to take on trust.
		if campaign.Slug != campaign.Vault {
			t.Errorf("campaign %q declares vault %q; a campaign's content root is derived as "+
				"<demo root>/<slug>", campaign.Slug, campaign.Vault)
		}
	}

	for slug := range want {
		t.Errorf("the shipped manifest does not declare campaign %q", slug)
	}
}

// TestTheManifestRefusesAKeyThisBuildDoesNotKnow covers the closed schema.
//
// `KnownFields(true)` on the decoder is the mechanism and this is the evidence: an
// unrecognised key is a refusal rather than a shrug. The failure it prevents is a manifest that
// validates as almost-empty — a demo with no accounts, seeded into an instance nobody can sign
// in to, reporting success.
func TestTheManifestRefusesAKeyThisBuildDoesNotKnow(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"a misspelled top-level key": tinyManifest() + "\naccountz: []\n",
		"a misspelled campaign key":  manifestWith("campaign", "    demostrates: [y]\n"),
		"an unknown account field":   "schema: 1\nproduct: \"1\"\naccounts:\n  - username: gm\n    is_admin: true\n",
		"a ruleset_version key": manifestWith(
			"campaign",
			"    ruleset_version: sp1:hand-written\n",
		),
		"a fog field on a placement": manifestWith(
			"state",
			placement("p1", 1, 1)+"          fog: 3\n",
		),
		"an initiative order": manifestWith("campaign", "    initiative: [a, b]\n"),
		"a version on a placement": manifestWith(
			"state",
			placement("p1", 1, 1)+"          version: 7\n",
		),
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := demo.Parse([]byte(source)); err == nil {
				t.Fatalf("Parse accepted a manifest with %s; want a refusal", name)
			}
		})
	}
}

// TestTheManifestRefusesAnInstanceAdminIsTheStructuralFormOfD14.
//
// Plan D14 makes the demo GM "campaign-GM only, never an instance admin", and the strongest
// form of that is a schema in which the claim cannot be written down. This asserts the schema
// refuses `is_admin` before any row exists; the companion assertion that the seeded rows carry
// `is_admin = 0` is `TestTheSeededAccountsAreNeverInstanceAdministrators`.
func TestTheManifestRefusesAnInstanceAdmin(t *testing.T) {
	t.Parallel()

	source := "schema: 1\nproduct: \"1\"\naccounts:\n  - username: gm\n    is_admin: true\n" +
		manifestWith("campaign", "")

	if _, err := demo.Parse([]byte(source)); err == nil {
		t.Fatal("Parse accepted an account with is_admin: true; the schema must refuse it")
	}
}

// TestAVersionSkewIsRefusedNamingBothVersions is the DoD's first half.
//
// **Both numbers are asserted in the message, not just the sentinel.** A refusal that says
// "version mismatch" sends the operator to the release page to work out which two they are
// holding, and neither number appears anywhere else in the product — the rail's version line is
// the binary's, not the artefact's.
//
// The mutation this exists for: making `Check` return `nil` on a mismatch turns this red, because
// the seed then proceeds and the message assertions never run.
func TestAVersionSkewIsRefusedNamingBothVersions(t *testing.T) {
	t.Parallel()

	manifest := parse(t, shippedManifestSource(harnessArtefactVersion))

	_, err := demo.Check(manifest, harnessOtherVersion)
	if err == nil {
		t.Fatalf("Check accepted an artefact for %s against a binary that is %s; want a refusal",
			harnessArtefactVersion, harnessOtherVersion)
	}

	if !errors.Is(err, demo.ErrVersionSkew) {
		t.Fatalf("Check error = %v, want errors.Is(err, demo.ErrVersionSkew)", err)
	}

	for _, version := range []string{harnessArtefactVersion, harnessOtherVersion} {
		if !strings.Contains(err.Error(), version) {
			t.Errorf("the refusal message does not name %q: %s", version, err.Error())
		}
	}
}

// TestASeedingABinaryFromAnotherReleaseIsRefused is the same rule end to end, through the
// command — a refusal that only `demo.Check` produced would not be one an operator meets.
func TestASeedingABinaryFromAnotherReleaseIsRefused(t *testing.T) {
	h := newHarness(t)

	h.version = harnessOtherVersion

	_, _, err := h.run(t, "seed")
	if err == nil {
		t.Fatal("a seed from a binary of another release was accepted; want a refusal")
	}

	if !errors.Is(err, demo.ErrVersionSkew) {
		t.Fatalf("the seed error = %v, want errors.Is(err, demo.ErrVersionSkew)", err)
	}

	for _, version := range []string{h.artefactVersion, harnessOtherVersion} {
		if !strings.Contains(err.Error(), version) {
			t.Errorf("the refusal does not name %q: %s", version, err.Error())
		}
	}

	// Nothing was written, which is the property that makes the refusal safe.
	h.withStore(t, func(db *view) {
		if got := db.countRows("campaigns"); got != 0 {
			t.Errorf("a refused seed wrote %d campaign rows, want 0", got)
		}

		if got := db.countRows("users"); got != 0 {
			t.Errorf("a refused seed wrote %d user rows, want 0", got)
		}
	})
}

// TestASchemaMismatchIsRefusedSeparatelyFromAReleaseMismatch holds the two apart.
//
// `Reset` treats them differently on purpose — a release mismatch is a reason to reset, a
// schema mismatch means the names in the document may not be the ones the seed created — and
// that distinction is only executable if the two errors are distinguishable. Asserted in both
// directions.
func TestASchemaMismatchIsRefusedSeparatelyFromAReleaseMismatch(t *testing.T) {
	t.Parallel()

	source := strings.Replace(shippedManifestSource(harnessArtefactVersion),
		"schema: 1", "schema: 2", 1)

	// Refused by the parser, which is the right place: `Manifest.check` compares the schema
	// before it reads anything else.
	if _, err := demo.Parse([]byte(source)); err == nil {
		t.Fatal("Parse accepted schema: 2; want a refusal")
	} else if !errors.Is(err, demo.ErrIncompleteManifest) {
		t.Errorf("Parse(schema 2) error = %v, want errors.Is(err, demo.ErrIncompleteManifest)", err)
	}

	// And refused again by `Check`, for a caller that assembled the manifest rather than
	// parsing one — which is what distinguishes the two sentinels.
	_, err := demo.Check(
		demo.Manifest{Schema: 2, Product: harnessArtefactVersion},
		harnessArtefactVersion,
	)
	if !errors.Is(err, demo.ErrSchemaMismatch) {
		t.Fatalf("Check(schema 2) error = %v, want errors.Is(err, demo.ErrSchemaMismatch)", err)
	}

	if !errors.Is(err, demo.ErrVersionSkew) {
		t.Errorf("a schema mismatch must also satisfy ErrVersionSkew; got %v", err)
	}

	// The other direction: a release mismatch is not a schema mismatch, which is what lets
	// `Reset` continue through one and refuse the other.
	if _, err := demo.Check(parse(t, shippedManifestSource(harnessArtefactVersion)),
		harnessOtherVersion); errors.Is(err, demo.ErrSchemaMismatch) {
		t.Error("a release mismatch was reported as a schema mismatch; Reset would refuse it")
	}
}

// TestAVersionlessBinarySaysSoRatherThanPretending holds the documented exception.
//
// `cmd/server`'s `productVersion` is an empty string until a release workflow stamps one, so the
// skew comparison has nothing to compare against. The seed then **warns and proceeds** rather
// than refusing, because refusing would make the demo unseedable in every pre-release build —
// which is exactly when phase 11's own DoD ("a fresh instance reaches a populated map in one
// command") has to be demonstrated. The warning names the artefact, so the check is visibly
// absent rather than silently skipped.
//
// The mutation: making this a refusal turns the second assertion red.
func TestAVersionlessBinarySaysSoRatherThanPretending(t *testing.T) {
	t.Parallel()

	warning, err := demo.Check(parse(t, shippedManifestSource(harnessArtefactVersion)), "")
	if err != nil {
		t.Fatalf(
			"Check against a versionless binary error = %v, want a warning and no refusal",
			err,
		)
	}

	if warning == "" {
		t.Fatal("Check against a versionless binary returned no warning; the comparison that did " +
			"not happen has to be visible")
	}

	if !strings.Contains(warning, harnessArtefactVersion) {
		t.Errorf("the warning does not name the artefact's version: %s", warning)
	}

	// And it says the binary's own version is the missing half, because a warning that
	// named only one side leaves the reader guessing which.
	if !strings.Contains(warning, demo.UnversionedBinary) {
		t.Errorf("the warning does not say the binary carries no version: %s", warning)
	}
}

// TestTheSeededAccountsAreNeverInstanceAdministrators is D14's row assertion.
//
// Read out of the database rather than out of the manifest, because the manifest cannot express
// the claim and the row is where it would matter. `is_admin` is compared through
// `domain.User.IsAdmin`, which is the field every authorisation decision reads.
//
// The mutation: writing `IsAdmin: true` turns this red, and so does a manifest key that could
// reach it — which is why `TestTheManifestRefusesAnInstanceAdmin` exists beside it.
func TestTheSeededAccountsAreNeverInstanceAdministrators(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		for _, username := range []string{"demo-gm", "demo-player"} {
			if db.account(username).IsAdmin {
				t.Errorf("the seeded account %q holds instance administration", username)
			}
		}

		// And the role it does hold, because "campaign-GM only" is the positive half of the
		// claim and an account with no role at all would satisfy the negative half.
		if role := db.membership("greyhaven", "demo-gm").Role; role != domain.RoleGM {
			t.Errorf("demo-gm's role in greyhaven = %q, want %q", role, domain.RoleGM)
		}

		if role := db.membership("greyhaven", "demo-player").Role; role != domain.RolePlayer {
			t.Errorf("demo-player's role in greyhaven = %q, want %q", role, domain.RolePlayer)
		}
	})
}

// TestTheRulesetVersionIsResolvedAndNeverHardcoded is the plan's named landmine.
//
// **Asserted against the fingerprint the real engine produces**, computed through
// `realtime.FingerprintOf` from `dnd5e.Engine.Versions()` — the same call `cmd/server/systems.go`
// makes, which is the point: the seed is handed that value and must write it, so a literal typed
// into `internal/demo` could not match it.
//
// The mutation: replacing the computed value with a constant in `seed.go` turns this red, and so
// does writing the manifest's `system` id into the column, because the two differ for every id.
func TestTheRulesetVersionIsResolvedAndNeverHardcoded(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		for _, slug := range []string{"greyhaven", "public-post"} {
			campaign := db.campaign(slug)

			if campaign.RulesetVersion != h.fingerprint {
				t.Errorf(
					"campaign %q: ruleset_version = %q, want this build's resolved fingerprint %q",
					slug,
					campaign.RulesetVersion,
					h.fingerprint,
				)
			}

			// Parsed back through the gate's own parser, so the stored value is one the resume
			// path can compare rather than a string that merely looks like one.
			parsed, err := realtime.ParseFingerprint(campaign.RulesetVersion)
			if err != nil {
				t.Errorf("campaign %q: ParseFingerprint(%q) error = %v, want nil",
					slug, campaign.RulesetVersion, err)

				continue
			}

			if parsed.System != "dnd5e" {
				t.Errorf("campaign %q: the fingerprint names the system %q, want %q",
					slug, parsed.System, "dnd5e")
			}
		}
	})
}

// TestACampaignNamingAnUnregisteredSystemGetsTheEmptyRulesetVersion is `forgotten-realm`.
//
// §10.8's degraded path: the wiki serves and the game refuses by name. The seed's part in that is
// writing **the empty string** rather than a fingerprint naming a system this build does not have
// — a fiction in the column the resume gate compares. The empty value is the one migration 0005
// defines and `Gate.Inspect` reports as `Unfingerprinted`.
func TestACampaignNamingAnUnregisteredSystemGetsTheEmptyRulesetVersion(t *testing.T) {
	h := newHarness(t)

	result := h.seed(t)

	h.withStore(t, func(db *view) {
		campaign := db.campaign("forgotten-realm")

		if campaign.RulesetVersion != "" {
			t.Errorf("forgotten-realm: ruleset_version = %q, want the empty string because no "+
				"build resolves pathfinder-2e", campaign.RulesetVersion)
		}

		if campaign.SystemID != "pathfinder-2e" {
			t.Errorf("forgotten-realm: system_id = %q, want %q — the id is what the refusal names",
				campaign.SystemID, "pathfinder-2e")
		}
	})

	// And the seed said so, rather than leaving the operator to find out from a failed join.
	if len(result.Unresolved) != 1 || result.Unresolved[0] != "forgotten-realm" {
		t.Errorf("the seed reported unresolved = %v, want [forgotten-realm]", result.Unresolved)
	}
}

// TestTheHouseRuleLayerIsResolvedAndIsNotTheFingerprint holds both halves of the delivery
// plan's phrase.
//
//  1. The modules the manifest declares are written **through the store's own door** and
//     **applied by the real resolver** — a set naming a module this build does not register is
//     refused at the seed rather than left for a GM's first campaign load.
//  2. The resolved layer contributes **nothing** to `ruleset_version`, because ADR 0018 keeps
//     house rules out of the fingerprint precisely so that toggling one cannot strand a campaign.
//     Asserted by seeding the same campaign twice, once with the module and once without, and
//     requiring identical fingerprints.
//
// The second assertion is the one a mutation breaks: folding the module set into the fingerprint
// — which §10.5's chain reads as though it belongs there — turns it red.
func TestTheHouseRuleLayerIsResolvedAndIsNotTheFingerprint(t *testing.T) {
	withModule := newHarness(t, withModuleInManifest("mild-crits"))
	without := newHarness(t, withHouseRules())

	withModule.seed(t)
	without.seed(t)

	var withRows, withoutRows []determinism.Module

	withModule.withStore(t, func(db *view) { withRows = db.ruleModules("greyhaven") })
	without.withStore(t, func(db *view) { withoutRows = db.ruleModules("greyhaven") })

	if len(withRows) != 1 {
		t.Fatalf("the seeded campaign has %d house-rule rows, want 1", len(withRows))
	}

	if withRows[0].ModuleID.String() != "mild-crits" || !withRows[0].Enabled {
		t.Errorf(
			"the seeded house-rule row is %+v, want the enabled module mild-crits",
			withRows[0],
		)
	}

	// Resolved rather than merely stored: the effective layer is what the module produced,
	// which is the whole of what `houserules.Registry.Apply` computes.
	effective, err := withModule.houseRules.Apply(t.Context(), withRows, nil)
	if err != nil {
		t.Fatalf("Apply the seeded module set: %v", err)
	}

	if effective.Empty() {
		t.Error(
			"the seeded module set resolves to nothing; the module was stored but never applied",
		)
	}

	// No modules declared, so no rows — the ordinary case for this build.
	if len(withoutRows) != 0 {
		t.Errorf("a manifest with no modules wrote %d rows, want none", len(withoutRows))
	}

	var withVersion, withoutVersion string

	withModule.withStore(
		t,
		func(db *view) { withVersion = db.campaign("greyhaven").RulesetVersion },
	)
	without.withStore(
		t,
		func(db *view) { withoutVersion = db.campaign("greyhaven").RulesetVersion },
	)

	if withVersion != withoutVersion {
		t.Errorf("enabling a house rule moved ruleset_version from %q to %q; ADR 0018 requires "+
			"toggling a house rule never to strand a campaign", withoutVersion, withVersion)
	}

	if withoutVersion != withModule.fingerprint {
		t.Errorf("ruleset_version = %q, want this build's resolved fingerprint %q",
			withoutVersion, withModule.fingerprint)
	}
}

// TestAHouseRuleModuleThisBuildDoesNotRegisterIsRefused covers the other half: the module the
// resolver refuses.
//
// The refusal is `houserules.ErrUnknownModule`, and the row is **not** written — the direction
// that matters, because a row naming a module this build does not have is a campaign whose game
// refuses to start mid-session.
func TestAHouseRuleModuleThisBuildDoesNotRegisterIsRefused(t *testing.T) {
	h := newHarness(t, withModuleInManifest("no-such-module"))

	_, err := h.seedExpectingFailure(t)
	if !errors.Is(err, houserules.ErrUnknownModule) {
		t.Fatalf("the seed error = %v, want errors.Is(err, houserules.ErrUnknownModule)", err)
	}

	if !strings.Contains(err.Error(), "no-such-module") {
		t.Errorf("the refusal does not name the module: %s", err.Error())
	}

	h.withStore(t, func(db *view) {
		if got := len(db.ruleModules("greyhaven")); got != 0 {
			t.Errorf("a refused module set still wrote %d rows, want none", got)
		}
	})
}

// TestTheGeneratedPasswordReachesTheOperatorExactlyOnce is D14's "printed once".
//
// **The value is read back out of the printed output and then verified against the stored
// hash**, so the test is not circular: it proves the thing on the terminal is the credential the
// account was created with, and then that it appears exactly once. A `Contains` alone would be
// satisfied by a command that printed it in the summary *and* again in a trailing block.
//
// The mutation: removing the password from the printed output turns the verification red, and
// printing it twice turns the count red.
func TestTheGeneratedPasswordReachesTheOperatorExactlyOnce(t *testing.T) {
	h := newHarness(t)

	h.knownSecret = false

	stdout, stderr, err := h.run(t, "seed")
	if err != nil {
		t.Fatalf("demo seed error = %v (stderr: %s)", err, stderr)
	}

	password := printedPassword(t, stdout)

	h.withStore(t, func(db *view) {
		// The printed value is the credential.
		if err := auth.VerifyPassword(db.account("demo-gm").PasswordHash, password); err != nil {
			t.Errorf("the printed value does not verify against the seeded account: %v", err)
		}
	})

	// Printed exactly once.
	if got := strings.Count(stdout, password); got != 1 {
		t.Errorf("the generated password appears %d times in the output, want exactly 1\n%s",
			got, stdout)
	}

	// And never on the error stream, which is where an operator pastes from.
	if strings.Contains(stderr, password) {
		t.Errorf("the generated password reached stderr:\n%s", stderr)
	}
}

// TestASuppliedPasswordIsNotEchoedBack is the other half of the override.
//
// A password the operator supplied has just been through their shell history; echoing it back
// would put it in a terminal scrollback a second time and in any `script` capture of the session.
// So the *generated* case prints and the *supplied* case does not.
func TestASuppliedPasswordIsNotEchoedBack(t *testing.T) {
	h := newHarness(t)

	const supplied = "a-passphrase-the-operator-chose"

	var stdout, stderr strings.Builder

	err := democmd.Run(t.Context(),
		[]string{"seed", "--root", h.root, "--password", supplied},
		h.deps(),
		democmd.Env{Stdout: &stdout, Stderr: &stderr, Version: h.version},
	)
	if err != nil {
		t.Fatalf("demo seed error = %v (stderr: %s)", err, stderr.String())
	}

	if strings.Contains(stdout.String(), supplied) {
		t.Errorf("the supplied password was echoed back:\n%s", stdout.String())
	}

	h.withStore(t, func(db *view) {
		if err := auth.VerifyPassword(db.account("demo-gm").PasswordHash, supplied); err != nil {
			t.Errorf("the account does not verify against the supplied password: %v", err)
		}
	})
}

// TestThePasswordNeverReachesTheLog is S-12.3 applied to the demo credential.
//
// **Over the rendered output of a capturing handler**, because that is where a credential
// actually goes: a log aggregator or a terminal scrollback, neither of which the operator can
// revoke it from. `Secret`'s formatting methods are what make this structural rather than a
// discipline, and this is the test that would notice somebody reaching for `Reveal` at the wrong
// call site.
//
// The mutation: logging the password — `slog.Any("password", secret.Reveal())` — turns this red.
func TestThePasswordNeverReachesTheLog(t *testing.T) {
	h := newHarness(t)

	h.knownSecret = false

	captured := &recordingHandler{}

	previous := slog.Default()
	slog.SetDefault(slog.New(captured))

	t.Cleanup(func() { slog.SetDefault(previous) })

	stdout, _, err := h.run(t, "seed")
	if err != nil {
		t.Fatalf("demo seed error = %v", err)
	}

	password := printedPassword(t, stdout)

	if strings.Contains(captured.String(), password) {
		t.Errorf("the generated password reached a log line:\n%s", captured.String())
	}
}

// TestTheSecretRendersRedactedUnderEveryFormattingVerb is the type's own guarantee.
//
// **`fmt` does not consult a `Stringer` for `%#v` or `%d`**, so `Secret` implements
// `fmt.Formatter` rather than only `Stringer` — and this test is what proves the implementation
// covers the verbs a `Stringer` would have leaked through. A version of this type with only a
// `String` method fails here, which is exactly why the assertion enumerates the verbs instead of
// trusting `fmt`'s documented behaviour.
func TestTheSecretRendersRedactedUnderEveryFormattingVerb(t *testing.T) {
	t.Parallel()

	const value = "hunter2-the-demo-password"

	secret := demo.NewSecret(value)

	if secret.Reveal() != value {
		t.Errorf("Reveal() = %q, want %q", secret.Reveal(), value)
	}

	// `%#v` renders a Go-syntax representation, `%d` a decimal number, and neither reaches a
	// `Stringer`; both leaked the value before `Secret` implemented `fmt.Formatter`.
	for _, verb := range []string{
		"%v", "%s", "%q", "%x", "%X", "%+v", "%#v", "%d", "%8s", "%-12q", "%[1]v",
	} {
		rendered := fmt.Sprintf(verb, secret)
		if strings.Contains(rendered, value) {
			t.Errorf("%s of a Secret rendered the value: %s", verb, rendered)
		}
	}

	// The same through a logger, which is where a credential would actually end up.
	captured := &recordingHandler{}
	slog.New(captured).
		Info("password", slog.Any("password", secret), slog.String("plain", secret.String()))

	if strings.Contains(captured.String(), value) {
		t.Errorf("a Secret reached a log line through slog.Any:\n%s", captured.String())
	}

	if !demo.NewSecret("").Empty() {
		t.Error("an empty Secret does not report itself empty")
	}
}

// TestTheGeneratedPasswordDrawsFromTheWholeAlphabet exercises the rejection loop.
//
// One draw cannot observe rejection sampling: a plain modulo would still produce a
// 24-character password most of the time, and only a distribution test would show the bias. So
// the reader is a `bytes.Reader` over every byte value — including the 32 that `256 % 57` makes
// over-represented — and the assertions are the length, membership of the alphabet, and enough
// distinct characters that a generator reading only the first 24 bytes would fail.
//
// **What this does not claim**: that every character is exactly equiprobable. That needs a
// distribution test, and a flaky one is worse than none, so the guard's presence is asserted
// structurally here and the uniformness is the comment's claim rather than a measurement.
func TestTheGeneratedPasswordDrawsFromTheWholeAlphabet(t *testing.T) {
	t.Parallel()

	var all bytes.Buffer
	for value := range 256 {
		all.WriteByte(byte(value))
	}

	secret, err := demo.GeneratePassword(bytes.NewReader(all.Bytes()))
	if err != nil {
		t.Fatalf("GeneratePassword error = %v, want nil", err)
	}

	drawn := secret.Reveal()

	if len(drawn) != 24 {
		t.Errorf("the generated password is %d characters, want 24: %q", len(drawn), drawn)
	}

	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	seen := make(map[rune]bool, len(drawn))

	for _, char := range drawn {
		if !strings.ContainsRune(alphabet, char) {
			t.Errorf("the generated password contains %q, which is not in the alphabet", char)
		}

		seen[char] = true
	}

	if len(seen) < 12 {
		t.Errorf("24 characters yielded only %d distinct ones, which suggests the draw is not "+
			"spanning its input", len(seen))
	}
}

// TestTheSeedIsRefusedASecondTimeNamingBothVersions is the DoD's second half.
//
// **Run through the command, twice**, because the refusal has to be one an operator actually
// receives — not a check on an internal function. The message is asserted to name both versions
// *and* what already exists, because "already seeded" without the slugs sends them looking for a
// command that lists them.
//
// The mutation: letting a second seed through turns this red on the error assertion, and so does
// a message that drops either version.
func TestTheSeedIsRefusedASecondTimeNamingBothVersions(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	_, _, err := h.run(t, "seed")
	if err == nil {
		t.Fatal("the second seed was accepted; want a refusal naming both versions")
	}

	if !errors.Is(err, demo.ErrAlreadySeeded) {
		t.Fatalf("the second seed error = %v, want errors.Is(err, demo.ErrAlreadySeeded)", err)
	}

	for _, want := range []string{h.artefactVersion, h.version, "greyhaven", "demo-gm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the second-seed refusal does not name %q: %s", want, err.Error())
		}
	}

	// And nothing was duplicated, which is the harm the refusal exists to prevent.
	h.withStore(t, func(db *view) {
		if got := len(db.campaigns()); got != 3 {
			t.Errorf("the instance holds %d campaigns after two seeds, want 3", got)
		}

		if got := db.countRows("users"); got != 2 {
			t.Errorf("the instance holds %d user rows after two seeds, want 2", got)
		}

		// Two in greyhaven (the GM and the player), one in each boundary campaign: four in
		// all, and a second seed that duplicated any of them would show here.
		if got := db.countRows("campaign_members"); got != 4 {
			t.Errorf("the instance holds %d memberships after two seeds, want 4", got)
		}
	})
}

// TestResetRemovesTheRowsAndTheStateAndTheVaultSurvives is the load-bearing word.
//
// **The vault is compared by content hash and by mode, before and after**, over a walk of the
// whole tree. Not "the files are still there" — a reset that rewrote a page, or loosened a
// directory's mode, would satisfy that. The snapshot is a sorted list of `path|mode|sha256`, so a
// difference anywhere is a difference in the comparison.
//
// The mutation: making `Reset` remove the campaign's content root turns this red, and so does a
// `Reset` that rewrites a page's bytes.
func TestResetRemovesTheRowsAndTheStateAndTheVaultSurvives(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		if got := db.countRows("campaign_state"); got != 1 {
			t.Fatalf("the seeded instance holds %d campaign_state rows, want 1", got)
		}
	})

	before := snapshotTree(t, h.root)

	h.runOrFail(t, "reset")

	h.withStore(t, func(db *view) {
		campaigns := db.campaigns()
		if len(campaigns) != 0 {
			t.Errorf("the instance holds %d campaigns after a reset, want 0: %v",
				len(campaigns), campaigns)
		}

		// The state rows, which have no foreign key and so need the explicit delete.
		if got := db.countRows("campaign_state"); got != 0 {
			t.Errorf("campaign_state holds %d rows after a reset, want 0", got)
		}

		// The memberships cascade from the campaign, and the accounts have to go with them, or
		// a re-seed refuses on a username collision.
		if got := db.countRows("campaign_members"); got != 0 {
			t.Errorf("the instance holds %d memberships after a reset, want 0", got)
		}

		for _, username := range []string{"demo-gm", "demo-player"} {
			if _, err := db.UserByUsername(
				t.Context(),
				username,
			); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Errorf("the account %q still exists after a reset (err = %v)", username, err)
			}
		}
	})

	after := snapshotTree(t, h.root)

	if before != after {
		t.Errorf("the vault changed across a reset\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestResetRefusesToDeleteAnInstanceAdministrator is the one account reset will not remove.
//
// The demo never creates one, so an admin named in the manifest is an account this instance had
// before the demo was seeded — most likely the operator's own — and deleting it because a
// downloaded artefact listed its username would be the worst thing the command could do. The
// refusal happens in the preflight, so **nothing** is removed: the assertion is that all three
// campaigns survive.
//
// The mutation: dropping the `IsAdmin` guard turns the first assertion red, and moving the guard
// after the deletions turns the second red.
func TestResetRefusesToDeleteAnInstanceAdministrator(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) { db.promoteToAdmin("demo-gm") })

	_, _, err := h.run(t, "reset")
	if err == nil {
		t.Fatal("reset removed an instance administrator; want a refusal")
	}

	if !strings.Contains(err.Error(), "demo-gm") {
		t.Errorf("the refusal does not name the account: %s", err.Error())
	}

	h.withStore(t, func(db *view) {
		if got := len(db.campaigns()); got != 3 {
			t.Errorf("a refused reset removed campaigns: %d remain, want 3", got)
		}
	})
}

// TestResetRefusesToDeleteAnAccountInACampaignItDidNotCreate is the second guard, and it is a
// guard rather than a detail: the account could belong to somebody entirely.
//
// A membership in a campaign the artefact does not name means this instance had the account
// before the demo, and `store.DeleteUser` would refuse it with `ErrForeignKey` anyway — checking
// here means the refusal arrives before the first deletion rather than after.
func TestResetRefusesToDeleteAnAccountInACampaignItDidNotCreate(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(
		t,
		func(db *view) { db.joinCampaignOutsideTheDemo("an-actual-campaign", "demo-player") },
	)

	_, _, err := h.run(t, "reset")
	if err == nil {
		t.Fatal("reset deleted an account that belongs to another campaign; want a refusal")
	}

	if !strings.Contains(err.Error(), "demo-player") {
		t.Errorf("the refusal does not name the account: %s", err.Error())
	}

	h.withStore(t, func(db *view) {
		if got := len(db.campaigns()); got != 4 {
			t.Errorf("a refused reset removed campaigns: %d remain, want 4", got)
		}
	})
}

// TestResetProceedsThroughAReleaseSkewAndIsReplayable is the recovery path.
//
// The asymmetry is the design: a version mismatch is one of the reasons an operator reaches for
// `demo reset`, so a reset that refused on the condition it exists to recover from would have no
// recovery path at all. Asserted as behaviour rather than as a comment: the reset succeeds, says
// what it proceeded through, and a seed afterwards succeeds — which is what "replayable" means.
func TestResetProceedsThroughAReleaseSkewAndIsReplayable(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.version = harnessOtherVersion

	stdout, stderr := h.runOrFail(t, "reset")

	if !strings.Contains(stderr, harnessOtherVersion) ||
		!strings.Contains(stderr, h.artefactVersion) {
		t.Errorf("reset did not report the skew it proceeded through: %s", stderr)
	}

	if stdout == "" {
		t.Error("reset said nothing about what it removed")
	}

	h.withStore(t, func(db *view) {
		if got := len(db.campaigns()); got != 0 {
			t.Errorf("the skewed reset removed nothing: %d campaigns remain", got)
		}
	})

	// And the tutorial replays, with the versions agreeing again.
	h.version = h.artefactVersion
	h.seed(t)
}

// TestTheSeededTableTopLoadsAndIsTheSceneTheManifestDeclared decodes the seeded state
// through the reader the product uses.
//
// **Decoded through `realtime.DecodeDocument`, not through a struct this file declares**,
// because the state column is written in one place and read in another and a test that agreed
// with the writer about the shape would pass over a document the server refuses.
// `DecodeDocument` is the reader the product uses, so its refusals are the ones that matter: a
// placement at version 0, one above the revision, or a duplicated id.
func TestTheSeededTableTopLoadsAndIsTheSceneTheManifestDeclared(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		blob, version := db.readState("greyhaven")

		document, err := realtime.DecodeDocument(blob)
		if err != nil {
			t.Fatalf("DecodeDocument error = %v, want nil", err)
		}

		if uint64(version) != document.Revision {
			t.Errorf("campaign_state.version = %d and the document's revision = %d; the load "+
				"refuses a row whose two disagree", version, document.Revision)
		}

		if len(document.Placements) != 3 {
			t.Fatalf("the seeded table has %d placements, want 3: %+v",
				len(document.Placements), document.Placements)
		}

		// Sorted by id, which is what makes the persisted bytes reproducible.
		ids := make([]string, 0, len(document.Placements))
		for _, placement := range document.Placements {
			ids = append(ids, string(placement.ID))
		}

		if !slicesSorted(ids) {
			t.Errorf("the seeded placements are not sorted by id: %v", ids)
		}

		hidden := 0

		for _, placement := range document.Placements {
			if placement.Version == 0 {
				t.Errorf("placement %q is at version 0, which DecodeDocument refuses", placement.ID)
			}

			if placement.Version > document.Revision {
				t.Errorf("placement %q is at version %d above the revision %d",
					placement.ID, placement.Version, document.Revision)
			}

			if !slicesSorted(placement.Conditions) {
				t.Errorf(
					"placement %q has unsorted conditions %v",
					placement.ID,
					placement.Conditions,
				)
			}

			if !placement.Visible {
				hidden++
			}
		}

		if hidden != 1 {
			t.Errorf("%d placements are hidden, want the one the manifest declared "+
				"visible: false", hidden)
		}
	})
}

// TestTheSeededTableTopResumesThroughTheProductsOwnRegistry is D11's actual claim.
//
// Everything above asserts the seeded `campaign_state` blob *decodes*; this asserts the
// realtime plane **resumes** it. Those are different claims: `realtime.Registry.Open` loads
// the row through `Registry.load`, which requires the document's revision and the
// `version` column to agree, every placement to be stamped, and — the part only this path
// checks — the blob to carry the document magic. A seed that wrote a well-formed but
// unwritable state would pass every other assertion here and leave `/play` refusing.
//
// **A real registry over the real store**, with the two closures `cmd/server/realtime.go`
// writes by hand for the same reason: `store.Write` takes an unexported parameter type, so
// no interface can express it and a closure is the only way across.
func TestTheSeededTableTopResumesThroughTheProductsOwnRegistry(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		registry := realtime.NewRegistry(t.Context(), realtime.Config{
			Write: func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
				if err := db.Write(ctx, fn); err != nil {
					return fmt.Errorf("store write: %w", err)
				}

				return nil
			},
			Read: func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
				var persisted realtime.Persisted

				row := db.DB().QueryRowContext(ctx,
					"SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = ?",
					campaignID)

				var updatedAt int64
				if err := row.Scan(&persisted.Blob, &persisted.Version, &updatedAt); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return realtime.Persisted{}, fmt.Errorf("no row: %w", realtime.ErrNoState)
					}

					return realtime.Persisted{}, fmt.Errorf("read: %w", err)
				}

				persisted.UpdatedAt = time.Unix(updatedAt, 0).UTC()

				return persisted, nil
			},
		})

		defer func() {
			if err := registry.Close(t.Context()); err != nil {
				t.Errorf("close the registry: %v", err)
			}
		}()

		state, err := registry.Open(t.Context(), db.campaign("greyhaven").ID)
		if err != nil {
			t.Fatalf("realtime.Registry.Open error = %v, want nil — the seeded state is not "+
				"resumable, which is exactly the failure D11 names", err)
		}

		// `Resume(incarnation, since)` is the reconnecting client's question. A zero
		// incarnation is one this state never issued, which is the "is this client
		// talking to the same loading of the state" answer the protocol asks for — and
		// either answer is the whole state, which is what makes this the right way to
		// read the seeded placements back.
		resume, err := state.Resume(realtime.Incarnation{}, 0)
		if err != nil {
			t.Fatalf("CampaignState.Resume error = %v, want nil", err)
		}

		if len(resume.Placements) != 3 {
			t.Errorf("the resumed tabletop holds %d placements, want 3: %+v",
				len(resume.Placements), resume.Placements)
		}
	})
}

// TestACampaignWithNoDeclaredTabletopHasNoStateRow is the other half of the `state:` key.
//
// `public-post` declares none, so the seed writes nothing: a campaign nobody has joined has no
// `campaign_state` row, and `realtime.Registry.Open` writes the empty state when someone does.
func TestACampaignWithNoDeclaredTabletopHasNoStateRow(t *testing.T) {
	h := newHarness(t)

	h.seed(t)

	h.withStore(t, func(db *view) {
		var count int

		row := db.DB().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM campaign_state WHERE campaign_id = ?", db.campaign("public-post").ID)
		if err := row.Scan(&count); err != nil {
			t.Fatalf("count public-post's state rows: %v", err)
		}

		if count != 0 {
			t.Errorf("a campaign with no declared tabletop holds %d state rows, want 0", count)
		}
	})
}

// TestTheSeedIsRefusedWhenTheVaultIsNotThere is the silently-empty-demo gate.
//
// The registrar *creates* the directory it is given, so a manifest naming a vault the artefact
// does not contain would otherwise be seeded into a brand-new empty directory: every page answers
// 404, the wiki renders, the campaign is in the list, and the demo shows nothing. That is the
// "silently absent behaviour" this repository keeps building defences against, one layer up from a
// 404 asset.
//
// The mutation: dropping `checkVault` turns this red, because the seed would then succeed.
func TestTheSeedIsRefusedWhenTheVaultIsNotThere(t *testing.T) {
	h := newHarness(t)

	if err := os.RemoveAll(filepath.Join(h.root, "public-post")); err != nil {
		t.Fatalf("remove a vault directory: %v", err)
	}

	_, err := h.seedExpectingFailure(t)
	if !errors.Is(err, demo.ErrIncompleteManifest) {
		t.Fatalf("the seed error = %v, want errors.Is(err, demo.ErrIncompleteManifest)", err)
	}

	if !strings.Contains(err.Error(), "public-post") {
		t.Errorf("the refusal does not name the missing campaign: %s", err.Error())
	}

	// Nothing was written, because the preflight runs before the first row.
	h.withStore(t, func(db *view) {
		if got := db.countRows("campaigns"); got != 0 {
			t.Errorf("a refused seed wrote %d campaign rows, want 0", got)
		}
	})
}

// TestTheSeedIsRefusedWhenTheVaultIsAFile is the other half of the same check: a path that is a
// file cannot be a campaign's content root, and `os.OpenRoot` would fail later with a message
// about a directory rather than about the artefact.
func TestTheSeedIsRefusedWhenTheVaultIsAFile(t *testing.T) {
	h := newHarness(t)

	if err := os.RemoveAll(filepath.Join(h.root, "forgotten-realm")); err != nil {
		t.Fatalf("remove the vault directory: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(h.root, "forgotten-realm"),
		[]byte("not a vault"),
		0o600,
	); err != nil {
		t.Fatalf("write a file where the vault should be: %v", err)
	}

	_, err := h.seedExpectingFailure(t)
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("the seed error = %v, want it to say the vault is a file", err)
	}
}

// TestTheManifestRefusesAVaultDirectoryThatIsNotNamedForItsSlug is the registrar's constraint,
// stated.
//
// `campaigns.Registrar` — the one door into tenancy, reused here for the reason
// `admin campaign add` reuses it — derives each content root as `<demo root>/<slug>` and offers no
// way to say otherwise. A manifest claiming anything else is a claim the seed cannot honour, so
// it is refused with the sentence that explains the constraint rather than silently ignored.
func TestTheManifestRefusesAVaultDirectoryThatIsNotNamedForItsSlug(t *testing.T) {
	t.Parallel()

	source := strings.Replace(shippedManifestSource(harnessArtefactVersion),
		"vault: greyhaven", "vault: grey-haven", 1)

	_, err := demo.Parse([]byte(source))
	if !errors.Is(err, demo.ErrIncompleteManifest) {
		t.Fatalf("Parse error = %v, want errors.Is(err, demo.ErrIncompleteManifest)", err)
	}

	if !strings.Contains(err.Error(), "grey-haven") {
		t.Errorf("the refusal does not name the offending value: %s", err.Error())
	}
}

// TestTheManifestRefusesACampaignWithoutExactlyOneGM holds the registrar's other constraint.
//
// `Register` takes one owner and creates exactly one GM membership, so a manifest naming two would
// have the registrar pick one and the other silently never happen, and one naming none would
// produce a campaign nobody can edit or run. Both are refused.
func TestTheManifestRefusesACampaignWithoutExactlyOneGM(t *testing.T) {
	t.Parallel()

	twoGMs := tinyManifest() + "      - username: second-gm\n        role: gm\n"

	if _, err := demo.Parse([]byte(twoGMs)); !errors.Is(err, demo.ErrIncompleteManifest) {
		t.Errorf("two GMs: Parse error = %v, want a refusal", err)
	}

	none := strings.Replace(
		tinyManifest(),
		"        role: gm\n",
		"        role: player\n",
		1,
	)

	if _, err := demo.Parse([]byte(none)); !errors.Is(err, demo.ErrIncompleteManifest) {
		t.Errorf("no GM: Parse error = %v, want a refusal", err)
	}
}

// TestBuildFingerprintsRefusesMoreThanOneRegisteredSystem is the guard against writing the wrong
// fingerprint.
//
// `BuildFingerprints` is handed **one** fingerprint, because the composition root holds one engine.
// A build that registered a second gameplay system would need a second, and answering both with the
// first would put 5e's resolution semantics into a campaign that plays under something else —
// which the gate would either refuse or, worse, not. So a second system turns the seed red with a
// message that says exactly this.
//
// The mutation: dropping the `len(entries) != 1` guard turns this red.
func TestBuildFingerprintsRefusesMoreThanOneRegisteredSystem(t *testing.T) {
	t.Parallel()

	registry := plugin.New()

	for _, system := range []rules.System{harnessEngine(t), notfiveSystem()} {
		if err := registry.Register(
			plugin.Entry{System: system, Codec: plugin.PlacementCodec{}},
		); err != nil {
			t.Fatalf("Register error = %v, want nil", err)
		}
	}

	_, err := demo.BuildFingerprints(registry, "sp1:whatever")
	if err == nil {
		t.Fatal("BuildFingerprints accepted a registry with two systems and one fingerprint; it " +
			"would write the wrong resolution semantics into a campaign's row")
	}

	// One system is the ordinary case and must work.
	single := plugin.New()
	if registerErr := single.Register(plugin.Entry{
		System: harnessEngine(t),
		Codec:  plugin.PlacementCodec{},
	}); registerErr != nil {
		t.Fatalf("Register error = %v, want nil", registerErr)
	}

	fingerprints, err := demo.BuildFingerprints(single, "sp1:single")
	if err != nil {
		t.Fatalf("BuildFingerprints error = %v, want nil", err)
	}

	resolved, known := fingerprints.Resolved("dnd5e")
	if !known || resolved != "sp1:single" {
		t.Errorf("Resolved(dnd5e) = %q, %t; want %q, true", resolved, known, "sp1:single")
	}

	if _, known := fingerprints.Resolved("pathfinder-2e"); known {
		t.Error("a system this build does not register resolved; forgotten-realm would get a " +
			"fingerprint naming a system that does not exist")
	}
}

// TestBuildFingerprintsOnABuildWithNoGameplaySystemIsEmptyRatherThanARefusal is S-10.6's
// survivable case: every campaign's wiki still serves and no campaign resolves a game, which is a
// working build rather than a broken one.
func TestBuildFingerprintsOnABuildWithNoGameplaySystemIsEmpty(t *testing.T) {
	t.Parallel()

	fingerprints, err := demo.BuildFingerprints(plugin.New(), "")
	if err != nil {
		t.Fatalf("BuildFingerprints error = %v, want nil", err)
	}

	if len(fingerprints) != 0 {
		t.Errorf("an empty registry produced %d fingerprints, want none", len(fingerprints))
	}
}

// TestTheSeedRefusesOptionsThatCannotProduceAWorkingInstance is the wiring guard.
//
// Each case is a composition root that forgot something. The no-registry case is the interesting
// one: a nil house-rule registry answers every module set with an empty `Effective` and no error,
// which is exactly the "the module was never checked" state ADR 0048 exists to make unreachable,
// and this is the one place the demo could reintroduce it.
func TestTheSeedRefusesOptionsThatCannotProduceAWorkingInstance(t *testing.T) {
	h := newHarness(t)

	manifest, err := demo.Load(h.root)
	if err != nil {
		t.Fatalf("Load the artefact: %v", err)
	}

	base, err := h.options(t)
	if err != nil {
		t.Fatalf("BuildFingerprints error = %v, want nil", err)
	}

	cases := map[string]func(demo.Options) demo.Options{
		"no house-rule registry": func(options demo.Options) demo.Options {
			options.HouseRules = nil

			return options
		},
		"a relative demo root": func(options demo.Options) demo.Options {
			options.Root = "relative/path"

			return options
		},
		"no store": func(options demo.Options) demo.Options {
			options.Store = nil

			return options
		},
		"no password to create accounts with": func(options demo.Options) demo.Options {
			options.Password = demo.NewSecret("")

			return options
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := mutate(base)
			options.Store = nil

			var seedErr error

			h.withStore(t, func(db *view) {
				if options.Store == nil && name != "no store" {
					options.Store = db.Store
				}

				_, seedErr = demo.Seed(t.Context(), manifest, options)
			})

			if seedErr == nil {
				t.Fatalf("the seed ran with %s; it must refuse", name)
			}

			if name == "no house-rule registry" &&
				!errors.Is(seedErr, houserules.ErrIncompleteDefinition) {
				t.Errorf(
					"the seed error = %v, want errors.Is(err, houserules.ErrIncompleteDefinition)",
					seedErr,
				)
			}

			if name == "a relative demo root" &&
				!strings.Contains(seedErr.Error(), "not absolute") {
				t.Errorf("the seed accepted a relative root: %v", seedErr)
			}
		})
	}
}

// TestTheCampaignsContentRootsAreAbsoluteAndConfined asserts the tenancy invariant the seed
// inherits and the one thing it adds.
//
// The absolute path is the store's own rule (migration 0005) and the seed cannot bypass it. The
// mode is the seed's: the registrar creates a content root 0700, but the demo's directories were
// made by `tar`, so nothing had set their mode — and a content root readable by every account on
// the host is a campaign readable by every account on the host, which bypasses §S-8 entirely.
//
// The mutation: dropping the `os.Chmod` turns the mode assertion red.
func TestTheCampaignsContentRootsAreAbsoluteAndConfined(t *testing.T) {
	h := newHarness(t)

	// The fixture already extracts the vaults 0755; stated again so the assertion below is not
	// resting on a detail three files away.
	loose := filepath.Join(h.root, "greyhaven")
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatalf("loosen the vault mode: %v", err)
	}

	h.seed(t)

	h.withStore(t, func(db *view) {
		campaign := db.campaign("greyhaven")

		if !filepath.IsAbs(campaign.ContentRoot) {
			t.Errorf("content_root = %q, want an absolute path", campaign.ContentRoot)
		}

		want := filepath.Join(h.root, "greyhaven")
		if campaign.ContentRoot != want {
			t.Errorf("content_root = %q, want %q", campaign.ContentRoot, want)
		}

		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("stat the content root: %v", err)
		}

		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("the content root's mode is %o, want 700", mode)
		}
	})
}

// TestResetOnAnInstanceThatWasNeverSeededSaysSo is the question `demo reset` exists to answer:
// "is it clean yet?" A refusal would leave no way to ask.
func TestResetOnAnInstanceThatWasNeverSeededSaysSo(t *testing.T) {
	h := newHarness(t)

	stdout, _ := h.runOrFail(t, "reset")

	if !strings.Contains(stdout, "Nothing to remove") {
		t.Errorf("reset on a fresh instance did not say so:\n%s", stdout)
	}
}

// TestTheCommandRefusesABadCommandLine covers the grammar, including the one that matters: an
// **unknown** flag is a refusal rather than a shrug, because a silently ignored `--pasword` on a
// seed is an instance whose accounts all have a password the operator did not choose and does not
// know.
//
// Each refusal is also asserted to be a **usage** error, because `cmd/server/main.go` chooses the
// exit code and the presentation from that class: an operator's typo reported as `fatal` through
// `slog` and exiting 1 is the wrong shape and puts typos in a log aggregator. `IsUsage` exists
// because the type it inspects is unexported and lives in a package the composition root cannot
// import.
func TestTheCommandRefusesABadCommandLine(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		nil,
		{"nonsense"},
		{"seed"},
		{"seed", "--root"},
		{"seed", "--root="},
		{"seed", "--pasword", "x", "--root", "/tmp"},
		{"seed", "--root", "relative"},
		{"seed", "positional"},
		{"reset", "--root", "/tmp", "--password", "x"},
	}

	for _, args := range cases {
		t.Run(fmt.Sprintf("%v", args), func(t *testing.T) {
			t.Parallel()

			var stdout, stderr strings.Builder

			err := democmd.Run(t.Context(), args, democmd.Dependencies{},
				democmd.Env{Stdout: &stdout, Stderr: &stderr},
			)
			if err == nil {
				t.Fatalf("Run(%v) error = nil, want a refusal", args)
			}

			if !democmd.IsUsage(err) {
				t.Errorf("Run(%v) error = %v, which is not a usage error; main would report an "+
					"operator's typo as a fatal fault", args, err)
			}
		})
	}
}

// TestAFaultIsNotAUsageError is the other direction, and it is the one that would be wrong
// quietly: a refused version skew is a fault an operator needs in a log, not a typo, and
// classifying it as a usage error would print it plainly and exit 2.
func TestAFaultIsNotAUsageError(t *testing.T) {
	h := newHarness(t)

	h.version = harnessOtherVersion

	_, _, err := h.run(t, "seed")
	if err == nil {
		t.Fatal("a seed from another release was accepted; want a refusal")
	}

	if democmd.IsUsage(err) {
		t.Errorf("a version-skew refusal is classified as a usage error: %v", err)
	}

	// And it is still a fault the store's own sentinels reach through the wrap the command
	// adds, which is the other thing the composition root needs from this layer.
	if !errors.Is(err, demo.ErrVersionSkew) {
		t.Errorf("the wrapped refusal lost its sentinel: %v", err)
	}
}

// TestHelpNamesEverySubcommand is the failure `cmd/server/main.go` records for the moment `admin`
// was added: a subcommand that exists and is not in the usage text is a subcommand an operator is
// told does not exist.
func TestHelpNamesEverySubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	if err := democmd.Run(t.Context(), []string{"help"}, democmd.Dependencies{},
		democmd.Env{Stdout: &stdout, Stderr: &stderr},
	); err != nil {
		t.Fatalf("Run(help) error = %v, want nil", err)
	}

	for _, subcommand := range []string{"demo seed", "demo reset"} {
		if !strings.Contains(stdout.String(), subcommand) {
			t.Errorf("the usage text does not name %q:\n%s", subcommand, stdout.String())
		}
	}

	if !strings.Contains(stdout.String(), "--root") {
		t.Errorf("the usage text does not document --root:\n%s", stdout.String())
	}
}
