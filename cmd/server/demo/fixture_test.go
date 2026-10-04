package demo_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/demo"
)

// The fixture manifest is the **shipped** one, read from `demo-vault/` and given the
// version the test asks for.
//
// A test manifest written separately would be a schema's own idea of itself: it would
// keep passing after the shipped artefact changed shape, and the first person to
// discover the difference would be downloading the release. So the two are the same
// bytes, and the only substitution is the one value a test legitimately varies.
func shippedManifestSource(product string) string {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "demo-vault", demo.ManifestFileName))
	if err != nil {
		// Not `t.Fatal`: this is called from helpers used by parallel subtests and from
		// one non-test helper. Panicking is right for a missing committed file, which is
		// a broken checkout rather than a test condition.
		panic("read the shipped demo manifest: " + err.Error())
	}

	// The one substitution, and it is **line-oriented**: the first top-level line
	// beginning `product:` is replaced whole. A substring substitution of `\nproduct: `
	// into a document that already spells its value as `product: "0.1.0"` produces
	// `product: "0.1.0""0.1.0"` — a YAML parse error, which reads as a defect in the
	// schema rather than as a defect in this fixture. Replacing the line means a manifest
	// that dropped the key fails here loudly instead of yielding a fixture with no
	// version in it.
	lines := strings.Split(string(source), "\n")

	replaced := false

	for index, line := range lines {
		if !strings.HasPrefix(line, "product:") {
			continue
		}

		if replaced {
			panic("the shipped demo manifest declares `product:` twice; a fixture cannot " +
				"say which one is the artefact's")
		}

		lines[index] = `product: "` + product + `"`
		replaced = true
	}

	if !replaced {
		panic("the shipped demo manifest has no top-level `product:` line to substitute")
	}

	return strings.Join(lines, "\n")
}

// parse reads a manifest, failing the test if it does not parse.
func parse(t *testing.T, source string) demo.Manifest {
	t.Helper()

	manifest, err := demo.Parse([]byte(source))
	if err != nil {
		t.Fatalf("Parse error = %v, want nil\n%s", err, source)
	}

	return manifest
}

// tinyManifest is the smallest manifest this schema accepts: one account, one campaign,
// one GM, no state and no modules. Every negative case in the suite starts from it, so a
// case is only ever *one* thing wrong.
//
// The fields are spelled in the order `Manifest` declares them, which is also the order
// the reader is meant to fill them in — and the campaign's `vault` equals its `slug`
// because the registrar derives the content root that way and offers no other option.
func tinyManifest() string {
	return "schema: 1\n" +
		"product: \"" + fixtureProduct + "\"\n" +
		"accounts:\n" +
		"  - username: gm\n" +
		"campaigns:\n" +
		"  - slug: greyhaven\n" +
		"    name: Greyhaven\n" +
		"    vault: greyhaven\n" +
		"    system: dnd5e\n" +
		"    visibility: private\n" +
		"    members:\n" +
		"      - username: gm\n" +
		"        role: gm\n"
}

// fixtureProduct is the version every negative fixture declares, so the refusals below
// are about the one field under test rather than about the version.
const fixtureProduct = "1"

// manifestWith is `tinyManifest` with extra lines appended to the campaign block.
//
// `where` is either `campaign` (after the `members` block) or `state` (a `state:` block
// appended to the campaign), and the two cases exist because a YAML indentation mistake
// in a test fixture is a fixture bug that reads as a schema bug.
func manifestWith(where, extra string) string {
	base := tinyManifest()

	switch where {
	case "campaign":
		return base + extra
	case "state":
		return base + "    state:\n" + extra
	default:
		panic("manifestWith: unknown insertion point " + where)
	}
}

// placement is one placement entry, indented for a `state:` block.
func placement(id string, hp, maxHP int) string {
	return fmt.Sprintf(
		"      paused: false\n      placements:\n        - id: %q\n          x: 1\n          y: 2\n"+
			"          hp: %d\n          max_hp: %d\n",
		id,
		hp,
		maxHP,
	)
}
