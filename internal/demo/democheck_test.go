package demo_test

// The gate's own tests.
//
// # The discipline, and why it is in this shape
//
// Every rule is proven red by a **mutation**, not by an assertion that it is
// enabled. The mutation is always the same shape: copy the committed fixture into a
// temporary directory, break exactly one thing, and require the named rule to fire.
// A rule with no mutation is a rule that may pass vacuously, and this gate exists
// because a gate that passes vacuously is worse than no gate — the demo vault is two
// waves away, so "there is nothing to fail on" is the state this work item is most
// able to ship.
//
// Two mutations are worth naming because they are the ones that would otherwise
// survive:
//
//   - `TestDemoTheBudgetIsDerivedAndNotAConstant` runs the same gate over vaults with
//     **0**, **3** and **5** deliberate breakages and requires all three to pass. A
//     gate that hardcoded the fixture's own count fails two of the three, so the
//     mutation is not "change the number" — it is "prove the number is not there".
//   - `TestDemoTheAuditFailsOnAnEmptyVault` and `TestDemoTheShippedVaultIsAuditedWhenItExists`
//     together are the empty-vault crux: one proves the auditor goes red on nothing,
//     and the other proves that the absence of the real vault is *exercised as a red
//     path* rather than skipped. A test that skipped would be the silent pass with a
//     green tick on it.

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// demoFixtureRoot is the committed vault the tests audit and mutate.
//
// A committed fixture rather than a tree a test builds inline, because a fixture
// built by test code is a fixture whose every property the test author chose, and the
// claims this gate makes are about what a *vault* looks like. The committed tree is
// the vault; the mutations are the experiments.
const demoFixtureRoot = "testdata/vault"

// demoShippedVault is the real vault, relative to this package.
//
// **Not a requirement, and its absence is not a skip.** It does not exist yet — the
// vault is wave B of phase 11, authored against this gate. `TestDemoTheShippedVaultIsAuditedWhenItExists`
// audits it when it is there and, when it is not, requires that auditing the *absent*
// path is red rather than green, so the state "there is no vault to check" is proven
// to be a finding and not a pass.
const demoShippedVault = "../../demo-vault"

// demoVaultDirEnv overrides which vault the shipped-vault test audits, and it is the
// same variable `scripts/check-demo.sh` reads.
//
// **Not a way to make the gate pass.** An override pointing at an empty directory is
// red for the same reason the default is red when it is absent, and pointing it at a
// broken vault turns the target red — which is how that was verified, after an earlier
// version of this test hardcoded `../../demo-vault` and left the override doing nothing
// except changing the script's banner. A documented override that silently changes no
// answer is worse than no override.
const demoVaultDirEnv = "DEMO_VAULT_DIR"

// demoVaultUnderTest returns the vault the shipped-vault test audits: the override when
// it is set, and the committed path otherwise.
//
// The override is read **as the script reads it** — a path relative to the repository
// root, made absolute from this package — because a reader who sets
// `DEMO_VAULT_DIR=demo-vault` from the root and gets a test that looks in
// `internal/demo/demo-vault` has been told the wrong thing about where the gate looks.
func demoVaultUnderTest() string {
	override := os.Getenv(demoVaultDirEnv)
	if override == "" {
		return demoShippedVault
	}

	if filepath.IsAbs(override) {
		return override
	}

	return filepath.Join("..", "..", override)
}

// demoAudit runs every rule over a vault root and fails the test if the root could
// not be read at all. It returns the findings, empty or not.
func demoAudit(t *testing.T, root string) []demoFinding {
	t.Helper()

	return auditDemoVault(t, root)
}

// demoRequireClean fails unless the audit found nothing, printing every finding.
//
// The findings are printed **whole**, because the gate's whole value is that a
// failure says what to do next — and the printed text is what the vault author reads.
func demoRequireClean(t *testing.T, findings []demoFinding) {
	t.Helper()

	if len(findings) == 0 {
		return
	}

	var out strings.Builder

	out.WriteString("demo vault findings:\n")

	for _, finding := range findings {
		out.WriteString("  " + finding.String() + "\n")
	}

	t.Fatal(out.String())
}

// demoRules returns the distinct rules the findings name, sorted.
func demoRules(findings []demoFinding) []string {
	seen := make(map[string]struct{}, len(findings))
	named := make([]string, 0, len(findings))

	for _, finding := range findings {
		if _, present := seen[finding.rule]; present {
			continue
		}

		seen[finding.rule] = struct{}{}
		named = append(named, finding.rule)
	}

	slices.Sort(named)

	return named
}

// demoRequireRule fails unless some finding carries this rule.
func demoRequireRule(t *testing.T, findings []demoFinding, rule string) {
	t.Helper()

	for _, finding := range findings {
		if finding.rule == rule {
			return
		}
	}

	t.Fatalf("the audit reported no %q finding, and reported %v instead:\n%s",
		rule, demoRules(findings), demoReport(findings))
}

// demoRequireNoRule fails if any finding carries this rule.
func demoRequireNoRule(t *testing.T, findings []demoFinding, rule string) {
	t.Helper()

	for _, finding := range findings {
		if finding.rule == rule {
			t.Fatalf("the audit reported an unexpected %q finding: %s\nall findings:\n%s",
				rule, finding, demoReport(findings))
		}
	}
}

// demoReport renders findings for a failure message.
func demoReport(findings []demoFinding) string {
	if len(findings) == 0 {
		return "  (none)\n"
	}

	var out strings.Builder

	for _, finding := range findings {
		out.WriteString("  " + finding.String() + "\n")
	}

	return out.String()
}

// demoCopyFixture copies the committed fixture into a temporary directory and
// returns its path.
//
// `os.CopyFS` rather than a hand-written walk, so the copy has no rule of its own
// that could differ from the filesystem's: a test that mutates a tree it rebuilt by
// its own rules is a test whose fixture is not the fixture.
func demoCopyFixture(t *testing.T) string {
	t.Helper()

	dest := t.TempDir()
	vault := filepath.Join(dest, "vault")

	if err := os.CopyFS(vault, os.DirFS(demoFixtureRoot)); err != nil {
		t.Fatalf("copy the demo fixture: %v", err)
	}

	return vault
}

// demoPage reads a page out of a copied fixture, failing the test if it is absent.
func demoRead(t *testing.T, vault, rel string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(vault, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}

	return string(raw)
}

// demoWrite writes a page into a copied fixture.
func demoWrite(t *testing.T, vault, rel, body string) {
	t.Helper()

	abs := filepath.Join(vault, rel)

	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(abs), err)
	}

	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// demoReplaceOnce replaces the first occurrence of old in a page and writes it back,
// failing the test if the text is absent.
//
// **Failing on an absent needle rather than writing the file unchanged**, because a
// mutation that does not apply is a green test that proved nothing — which is the
// exact hazard this whole package is about, reproduced in its own tests.
func demoReplaceOnce(t *testing.T, vault, rel, old, updated string) {
	t.Helper()

	body := demoRead(t, vault, rel)

	if !strings.Contains(body, old) {
		t.Fatalf("the mutation does not apply: %s does not contain %q", rel, old)
	}

	if strings.Count(body, old) != 1 {
		t.Fatalf(
			"the mutation is ambiguous: %s contains %q %d times, so replacing the "+
				"first would mutate something this test does not describe",
			rel, old, strings.Count(body, old),
		)
	}

	demoWrite(t, vault, rel, strings.Replace(body, old, updated, 1))
}

// demoReplaceEvery replaces every occurrence of old in a page and writes it back,
// failing the test if the text is absent.
//
// **One guard, and it covers "already applied" as well.** This helper used to carry a
// second check — "does the page already contain the replacement?" — on the reasoning
// that a re-run mutation should be caught. It is redundant: a page somebody else already
// mutated no longer contains `old`, so the first check fires. And it was *wrong*, not
// merely redundant: the mutation that strips the brackets off `[[The Sentinel]]` on the
// page whose front matter declares `- The Sentinel` is a legitimate one, and the
// replacement text occurs outside the construct quite correctly. A guard that rejects
// valid work is a guard that gets deleted rather than fixed, so it is not here.
//
// `demoReplaceOnce` does carry the stronger check — exactly one occurrence — because
// there "more than one" means the test is mutating something it does not describe.
func demoReplaceEvery(t *testing.T, vault, rel, old, updated string) {
	t.Helper()

	body := demoRead(t, vault, rel)

	if !strings.Contains(body, old) {
		t.Fatalf(
			"the mutation does not apply: %s contains no %q, so either the fixture "+
				"changed or this mutation has already been made",
			rel, old,
		)
	}

	demoWrite(t, vault, rel, strings.ReplaceAll(body, old, updated))
}

// demoLoaded loads a vault and closes its roots when the test ends.
//
// `auditLoadedVault` registers the same cleanup, so a test that loads and *audits*
// does not need this; a test that loads and then pokes at the tree does, because an
// `os.Root` is a live descriptor and a suite that leaks them fails on a machine with a
// low `RLIMIT_NOFILE` for reasons that have nothing to do with this package.
func demoLoaded(t *testing.T, vault string) (*demoVault, []demoVaultResolution) {
	t.Helper()

	loaded, findings := loadDemoVault(t, vault, demoKindsForBuild(t))
	if len(findings) > 0 {
		t.Fatalf("loading the vault reported findings, so a mutation may already be applied:\n%s",
			demoReport(findings))
	}

	t.Cleanup(func() {
		for at := range loaded.campaigns {
			if loaded.campaigns[at].root != nil {
				_ = loaded.campaigns[at].root.Close()
			}
		}
	})

	return loaded, demoResolveVault(loaded, demoVisibleCampaigns(loaded))
}

// demoFindPageByKind returns the first page whose **declared** kind the predicate
// accepts, as `(vault-relative path, kind)`.
//
// In the loader's order — campaign then path, both sorted — so the choice is the same
// on every machine and the test never depends on which page happened to be read first.
// The path is **vault-relative**, like every other path a mutation helper takes, because
// a mutation that edits `testdata/vault/<slug>/…` by a campaign-relative path edits
// nothing and reports a missing file — which is how three of these mutations failed
// before the helpers were fixed.
func demoFindPageByKind(
	t *testing.T,
	vault string,
	want func(kind string) bool,
) (rel, kind string) {
	t.Helper()

	loaded, _ := demoLoaded(t, vault)

	for at := range loaded.campaigns {
		for index := range loaded.campaigns[at].pages {
			page := &loaded.campaigns[at].pages[index]

			if page.declared != "" && want(page.declared) {
				return demoVaultPath(loaded.campaigns[at].slug, page.rel), page.declared
			}
		}
	}

	t.Fatal("no page in the vault declares a kind the predicate accepts")

	return "", ""
}

// demoVaultPath joins a campaign and a page path into a vault-relative one.
func demoVaultPath(slug, rel string) string { return filepath.Join(slug, filepath.FromSlash(rel)) }

// demoUnlinkFromIndex removes the campaign index's link to a page, and fails unless
// there was a link to remove.
//
// **Why the choice of page matters.** A page three other pages link to cannot be made
// unreachable by removing one link, so the mutation would not fire and the test would
// be green for nothing. `demoFindSoleInboundPage` therefore chooses a page the index is
// the *only* way to, which is what makes "remove the index's link" a complete mutation.
func demoUnlinkFromIndex(t *testing.T, vault, slug, rel string) {
	t.Helper()

	indexRel := demoIndexPageOf(t, vault, slug)
	body := demoRead(t, vault, indexRel)

	target := strings.TrimSuffix(rel, ".md")
	base := path.Base(target)

	for _, candidate := range []string{target, base} {
		pattern := regexp.MustCompile(
			`\[\[` + regexp.QuoteMeta(candidate) + `(?:\|[^\]]*)?\]\]`,
		)

		if !pattern.MatchString(body) {
			continue
		}

		// The page's own name as plain text, so the sentence still reads and the only
		// thing that changed is that it no longer links.
		demoWrite(t, vault, indexRel, pattern.ReplaceAllString(body, base))

		return
	}

	t.Fatalf("the mutation does not apply: %s does not link to %s", indexRel, rel)
}

// demoIndexPageOf returns a campaign's index page, as a vault-relative path.
func demoIndexPageOf(t *testing.T, vault, slug string) string {
	t.Helper()

	loaded, _ := demoLoaded(t, vault)

	for at := range loaded.campaigns {
		campaign := &loaded.campaigns[at]

		if campaign.slug != slug {
			continue
		}

		if campaign.indexPath == "" {
			t.Fatalf("campaign %s has no index page, so this mutation cannot apply", slug)
		}

		return demoVaultPath(campaign.slug, campaign.indexPath)
	}

	t.Fatalf("campaign %s is not in the vault", slug)

	return ""
}

// demoFindSoleInboundPage returns the `(campaign, campaign-relative path)` of a page its
// campaign index is the only way to reach.
func demoFindSoleInboundPage(t *testing.T, vault string) (slug, rel string) {
	t.Helper()

	loaded, resolution := demoLoaded(t, vault)

	for campaignAt := range loaded.campaigns {
		campaign := &loaded.campaigns[campaignAt]

		inbound := make(map[string][]string, len(campaign.pages))

		for pageAt := range campaign.pages {
			for oneAt := range resolution[campaignAt][pageAt] {
				target, ok := demoSameCampaignTarget(
					campaign,
					resolution[campaignAt][pageAt][oneAt],
				)
				if !ok {
					continue
				}

				inbound[target] = append(inbound[target], campaign.pages[pageAt].rel)
			}
		}

		for index := range campaign.pages {
			page := &campaign.pages[index]

			if page.rel == campaign.indexPath {
				continue
			}

			from := inbound[page.rel]
			if len(from) != 1 || from[0] != campaign.indexPath {
				continue
			}

			return campaign.slug, page.rel
		}
	}

	t.Fatal("no page in the fixture is reachable only through its campaign index, so the " +
		"unlink mutation could not be made a complete one")

	return "", ""
}

// TestDemoTheFixtureVaultSatisfiesEveryRule is the gate itself, run over the committed
// fixture.
//
// Every other test in this package is a mutation of it, and this one is the baseline
// they mutate away from: a mutation that does not make *this* red first has nothing to
// prove.
func TestDemoTheFixtureVaultSatisfiesEveryRule(t *testing.T) {
	t.Parallel()

	demoRequireClean(t, demoAudit(t, demoFixtureRoot))
}
