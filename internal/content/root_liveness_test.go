// Tests for two defects found while reviewing the phase-4 supervisor.
//
// Both were reported by the work item that built the supervisor, and both are
// confirmed here rather than taken on trust: each is a case where the system
// reports success while doing the wrong thing, which is the shape that survives
// review and fails in production.

package content_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// TestAClosedRootDoesNotWalkAsAnEmptyTree is the assertion behind the liveness
// check `Root.Walk` now makes.
//
// The behaviour it guards against is not hypothetical and not a matter of
// interpretation: on a closed `os.Root`, `root.FS()` still returns a live `fs.FS`,
// `fs.WalkDir` reports **no error**, and reports **no entries**. A walk of a dead
// descriptor is byte-for-byte a walk of an empty vault.
//
// That matters because of what consumes a walk. `ReindexCampaign` walks, indexes
// what it found, then prunes what it did not — so a closed root would delete every
// row for its campaign and report a clean success at every step. The failure is
// not "the index is empty"; it is "the index was emptied by a walk that claimed to
// have found nothing".
func TestAClosedRootDoesNotWalkAsAnEmptyTree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "Goblin.md"),
		[]byte("# Goblin\n"),
		0o600,
	); err != nil {
		t.Fatalf("write the page: %v", err)
	}

	root, err := content.NewRoot("greyhaven", dir, content.RefuseSymlinks)
	if err != nil {
		t.Fatalf("open the root: %v", err)
	}

	if closeErr := root.Close(); closeErr != nil {
		t.Fatalf("close the root: %v", closeErr)
	}

	// The walk must fail, and it must fail with something a caller can recognise
	// as "this root is unusable" rather than as "the tree is empty".
	walked := 0

	err = root.Walk(func(string, fs.DirEntry, error) error {
		walked++

		return nil
	})
	if err == nil {
		t.Fatal("Walk on a closed root returned nil: a dead descriptor is " +
			"indistinguishable from an empty vault, and a caller that prunes " +
			"on the difference would delete every row it holds")
	}

	if walked != 0 {
		t.Errorf("Walk visited %d entries after Close, want 0", walked)
	}

	if !errors.Is(err, fs.ErrClosed) && !errors.Is(err, os.ErrClosed) {
		t.Errorf("Walk error is %v; want one wrapping fs.ErrClosed so a caller can "+
			"tell a dead root from a missing one", err)
	}
}

// TestAClosedRootRefusesEveryOperationThatDoesIO is the broader claim: the
// liveness check belongs where the filesystem is touched, so this asserts the
// other I/O entry points refuse a dead root too.
//
// `At` is deliberately **not** in this test, and the reason is the contract rather
// than an omission. `At` confines a path and returns a handle; it makes no syscall
// and is documented as accepting a path that does not exist yet, because a save
// addresses a file that is about to be created. Asserting it fails on a closed
// root would mean asserting that a pure function consults a liveness flag, which
// is a different design and a worse one.
func TestAClosedRootRefusesEveryOperationThatDoesIO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(dir, "Goblin.md"),
		[]byte("# Goblin\n"),
		0o600,
	); err != nil {
		t.Fatalf("write the page: %v", err)
	}

	root, err := content.NewRoot("greyhaven", dir, content.RefuseSymlinks)
	if err != nil {
		t.Fatalf("open the root: %v", err)
	}

	if closeErr := root.Close(); closeErr != nil {
		t.Fatalf("close the root: %v", closeErr)
	}

	// `At` still succeeds: it confines a path, and a save names a file that is
	// about to exist. Asserted so that a future change making `At` liveness-aware
	// is a deliberate decision rather than a side effect.
	if _, atErr := root.At("Goblin.md"); atErr != nil {
		t.Errorf("At on a closed root: %v; At confines a path and makes no syscall, "+
			"so it should not depend on the handle being live", err)
	}

	// The operations that do touch the filesystem must refuse, because each of
	// them can otherwise report a plausible answer for a campaign it can no
	// longer read.
	target, atErr := root.At("Goblin.md")
	if atErr != nil {
		t.Fatalf("At: %v", atErr)
	}

	if _, err := target.ReadFile(); err == nil {
		t.Error("ReadFile on a closed root returned the contents")
	}

	if _, err := target.Stat(); err == nil {
		t.Error("Stat on a closed root reported a file")
	}
}

// TestAnOpenRootStillWalks guards the liveness check against being the thing that
// breaks: an over-eager `Walk` that refuses everything would pass the two tests
// above and break the product.
func TestAnOpenRootStillWalks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	for _, rel := range []string{"Goblin.md", "lore/Hobgoblin.md"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))

		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}

		if err := os.WriteFile(full, []byte("# x\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	root, err := content.NewRoot("greyhaven", dir, content.RefuseSymlinks)
	if err != nil {
		t.Fatalf("open the root: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close the root: %v", closeErr)
		}
	})

	seen := make(map[string]struct{})

	if err := root.Walk(func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			seen[rel] = struct{}{}
		}

		return nil
	}); err != nil {
		t.Fatalf("Walk on an open root: %v", err)
	}

	for _, want := range []string{"Goblin.md", "lore/Hobgoblin.md"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("Walk did not visit %s; visited %v", want, seen)
		}
	}
}
