package content_test

// Path traversal, and specifically the difference between a **lexical** check and
// a **confinement** one.
//
// `root_test.go` holds the lexical half in good shape: `../`, an absolute path, a
// NUL, a path that escapes only after normalising, and a symlink at the leaf of a
// path. Every one of those is refused by `path.Clean` plus a prefix comparison, which
// is why `cleanRef` exists and why it is documented as "a fast path and not the
// confinement".
//
// So the cases here are the ones a lexical check **cannot** catch, because the
// dangerous component of the path contains no `..` at all and looks, to any
// string-level analysis, entirely inside the root:
//
//   - **A symlink at a directory component.** `link/secret.md`, where `link` is a
//     relative symlink to a directory outside the tree. `path.Clean` returns it
//     unchanged, a prefix check passes, and the read succeeds. It is the single most
//     ordinary traversal payload there is — it is what a shared vault containing one
//     planted directory looks like — and it is a total bypass of the lexical half.
//   - **A component replaced between two operations.** The same path read
//     successfully a moment ago, then the directory is replaced by a symlink, then
//     the same `Target` is read again. A check that ran once — at construction, or
//     on the path string — is stale by definition; `os.Root` re-resolves every
//     component on every operation, which is the whole difference between a sandbox
//     and a boundary.
//   - **A pattern** (`Glob`) that lexically stays inside and traverses the link,
//     where `fs.Glob` answers an escaping pattern with an empty list rather than an
//     error — so a caller who asked for everything outside the root would be told the
//     root is empty.
//
// Every fixture here is a real directory with real symlinks. A confinement claim
// tested against a fake filesystem is a claim about the fake: what is under test is
// what the kernel does with `..`, a relative symlink and a rename, and no mock has
// those.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// traversalOutside is a real directory outside every content root, holding a real
// file.
//
// It exists so the refusals are refusals of a path that *would* have worked: a
// temporary directory with nothing in it is refused for the wrong reason, and a test
// that proved only that would prove nothing.
const traversalSecret = "SECRET-OUTSIDE-THE-ROOT"

// traversalFixture is one campaign's tree, plus the directory outside it and a
// relative symlink pointing there.
type traversalFixture struct {
	// base is the content root's directory on the host.
	base string
	// outside holds the file no confined read may reach.
	outside string
	// outsideName is `outside`'s own directory name, which is what a *relative*
	// symlink from inside `base` has to name — `os.Root` refuses an absolute
	// symlink outright, so an absolute fixture would exercise the standard
	// library's easy case and never reach this package's own check.
	outsideName string
}

// newTraversalRoot builds a tree and opens a root over it, for one policy.
//
// A function rather than a fixture method taking `t` at every call, because the
// policy is the only thing that varies between the cases below and a fixture method
// with one parameter and three fields is a constructor.
func newTraversalRoot(
	t *testing.T,
	policy content.SymlinkPolicy,
) (*content.Root, traversalFixture) {
	t.Helper()

	base := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret.md"),
		[]byte(traversalSecret), 0o600); err != nil {
		t.Fatalf("write the outside fixture: %v", err)
	}

	// One directory nested deep, so a link planted at a *component* can be tested
	// from a base the product actually uses (`DirOf` on a nested page).
	tree(t, base, map[string]string{
		"index.md":           "the campaign index",
		"notes/child.md":     "a child page",
		"notes/deep/leaf.md": "a leaf page",
	})

	fixture := traversalFixture{base: base, outside: outside, outsideName: filepath.Base(outside)}

	// A leaf symlink out of the root, which `root_test.go`'s fixture also builds. It
	// is here for the `Glob` case: a *matching* symlink is what `Glob` promises to
	// refuse, as opposed to a directory component it never matches.
	if err := os.Symlink(filepath.Join("..", fixture.outsideName, "secret.md"),
		filepath.Join(base, "escape.md")); err != nil {
		t.Fatalf("symlink a leaf out: %v", err)
	}

	// A **directory** component that is a relative symlink out of the root. The
	// payload: `doorway/secret.md` contains no `..`, and a lexical check admits it.
	if err := os.Symlink(filepath.Join("..", fixture.outsideName),
		filepath.Join(base, "doorway")); err != nil {
		t.Fatalf("symlink the directory out: %v", err)
	}

	// The same, one level down, which is the shape a reference in a nested page
	// takes: from `notes/deep/`, `../../doorway/secret.md`.
	if err := os.Symlink(filepath.Join("..", "..", fixture.outsideName),
		filepath.Join(base, "notes", "backdoor")); err != nil {
		t.Fatalf("symlink the nested directory out: %v", err)
	}

	root, err := content.NewRoot("test-campaign", base, policy)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}

	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})

	return root, fixture
}

// TestASymlinkedDirectoryComponentIsNotADoor is the case a lexical check cannot
// catch, and it is the reason `os.Root` is the confinement rather than a prefix
// comparison.
//
// `doorway/secret.md` has no `..` in it. `path.Clean` returns it unchanged and a
// `strings.HasPrefix(cleaned, base)` check passes, so an implementation built that
// way reads the file — and the file is the one outside the root. Every assertion
// below is therefore a claim about an implementation this repository must not have.
//
// Both policies are exercised, and **the refusal happens at different layers**,
// which is the point:
//
//   - `RefuseSymlinks` refuses it in `At`, as `ErrSymlink`: a link in a synced
//     vault is a configuration to look at rather than an attack, and the remedy is
//     different.
//   - `AllowSymlinks` skips that check — it follows links that stay inside — so the
//     refusal has to come from `os.Root` at the operation. The permissive policy is
//     therefore a policy choice and not a hole, and that is only true if something
//     *other* than the policy is holding the boundary.
//
// So the assertion is on the read never succeeding, and on whichever layer refused
// answering with the error that layer owns. A test that asserted only `At` would pass
// against an implementation with no `os.Root` at all, provided the policy check
// caught the paths it happened to be given.
//
// And the write half, because confinement that covers reads only is a read-only
// sandbox rather than a boundary: a save whose destination is reached through the
// link must be refused, and the outside directory must afterwards hold exactly the
// one file it started with.
func TestASymlinkedDirectoryComponentIsNotADoor(t *testing.T) {
	cases := []struct {
		name   string
		policy content.SymlinkPolicy
		// atErr is what `At` itself answers. Nil for the permissive policy, whose
		// `At` deliberately does not look.
		atErr error
	}{
		{
			name:   "the refusing policy",
			policy: content.RefuseSymlinks,
			atErr:  content.ErrSymlink,
		},
		{
			name:   "the permissive policy",
			policy: content.AllowSymlinks,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root, fx := newTraversalRoot(t, testCase.policy)

			// The read. `doorway/secret.md` — no `..`, lexically inside, and a real
			// file one level out.
			target, err := root.At("doorway/secret.md")

			switch {
			case testCase.atErr != nil && !errors.Is(err, testCase.atErr):
				t.Fatalf("At(\"doorway/secret.md\") error = %v, want %v.\n"+
					"The path contains no `..`, so a `path.Clean` plus prefix check "+
					"admits it and the read reaches a file outside the root",
					err, testCase.atErr)
			case testCase.atErr == nil && err != nil:
				t.Fatalf("At(\"doorway/secret.md\") error = %v; the permissive policy is "+
					"meant to defer to os.Root rather than to refuse here", err)
			}

			if target != nil {
				body, readErr := target.ReadFile()

				if readErr == nil {
					t.Fatalf("read %q through a symlinked directory and got %q; the "+
						"confinement is a prefix check rather than os.Root",
						target.Path(), body)
				}

				// The operation's own answer, which is a refusal and never a 404: a
				// 404 says "there is nothing at this path", and a reader who can tell
				// that from "there is nothing at this path *and* it is outside" has
				// learned something about a filesystem they were never granted.
				if !errors.Is(readErr, content.ErrOutsideRoot) {
					t.Errorf("ReadFile() through the link error = %v, want ErrOutsideRoot",
						readErr)
				}
			}

			// The same from a nested base, which is the shape a reference inside a
			// page takes and the reason `Resolve` takes a `Dir` at all.
			base, dirErr := content.DirOf("notes/deep/leaf.md")
			if dirErr != nil {
				t.Fatalf("DirOf() error = %v, want nil", dirErr)
			}

			if _, resolveErr := root.Resolve("../../backdoor/secret.md", base); resolveErr == nil {
				t.Error("Resolve() through a symlinked directory from a nested base " +
					"succeeded; a reference in a page could reach a file outside the root")
			}

			// The write, and it is the half that turns a read-only bypass into a
			// write one.
			planted, err := root.At("doorway/planted.md")
			if err == nil {
				if writeErr := planted.WriteFile(
					t.Context(),
					[]byte("planted"),
					0o600,
				); writeErr == nil {
					t.Errorf("WriteFile() through a symlinked directory reported success; " +
						"a save can write outside the root through a link component")
				}
			}

			// And the outside directory is untouched, which is what distinguishes
			// "refused" from "refused with a side effect".
			entries, err := os.ReadDir(fx.outside)
			if err != nil {
				t.Fatalf("read the outside directory: %v", err)
			}

			if len(entries) != 1 || entries[0].Name() != "secret.md" {
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}

				t.Errorf("the outside directory now holds %v, want [secret.md]", names)
			}
		})
	}
}

// TestConfinementIsReEstablishedAtEveryOperation is the TOCTOU claim, and it is the
// one that separates a boundary from a check.
//
// The sequence is: read a nested page successfully, replace its directory with a
// symlink pointing outside, then read the *same path* again through a *fresh*
// `Target`. A confinement that was decided when the path was resolved is stale by
// then; `os.Root` re-resolves every component on every operation through
// `openat2`-style beneath-resolution, so the second read is refused even though the
// first succeeded and the path never changed.
//
// This is not a contrived race. A content root is a sync target, and a sync client
// that replaces a directory with a link — or a plugin that does — does it between two
// requests rather than during one.
func TestConfinementIsReestablishedAtEveryOperation(t *testing.T) {
	t.Parallel()

	root, fx := newTraversalRoot(t, content.RefuseSymlinks)

	const rel = "notes/deep/leaf.md"

	// Before: the read works, so every refusal below is a change in the tree rather
	// than a path that never resolved.
	target, err := root.At(rel)
	if err != nil {
		t.Fatalf("At(%q) error = %v, want nil", rel, err)
	}

	body, err := target.ReadFile()
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", rel, err)
	}

	if string(body) != "a leaf page" {
		t.Fatalf("ReadFile(%q) = %q, want the page's own bytes", rel, body)
	}

	// The swap: a real directory becomes a real symlink to the outside, which is
	// what a sync client or a plugin would leave behind.
	if swapErr := os.RemoveAll(filepath.Join(fx.base, "notes", "deep")); swapErr != nil {
		t.Fatalf("remove the directory: %v", swapErr)
	}

	if linkErr := os.Symlink(filepath.Join("..", "..", fx.outsideName),
		filepath.Join(fx.base, "notes", "deep")); linkErr != nil {
		t.Fatalf("symlink the directory out: %v", linkErr)
	}

	// After: a fresh handle on the same path, and a refusal.
	fresh, err := root.At(rel)
	if err == nil {
		if _, readErr := fresh.ReadFile(); readErr == nil {
			t.Errorf("read %q again after its directory became a symlink out of the "+
				"root, and it succeeded; the confinement was decided once rather than "+
				"re-established per operation", rel)
		} else if !errors.Is(readErr, content.ErrOutsideRoot) {
			t.Errorf("ReadFile(%q) after the swap error = %v, want ErrOutsideRoot",
				rel, readErr)
		}
	}

	// And the file the symlink names was not read by either handle.
	entries, err := os.ReadDir(fx.outside)
	if err != nil {
		t.Fatalf("read the outside directory: %v", err)
	}

	if len(entries) != 1 || entries[0].Name() != "secret.md" {
		t.Errorf("the outside directory now holds %d entr(ies); a read reached it",
			len(entries))
	}
}

// TestAPatternReachesNothingOutsideTheRoot is the listing shape, and it needs its
// own test because of a behaviour of the standard library: **`fs.Glob` answers an
// escaping pattern with an empty list rather than an error.** A caller who asked for
// everything outside the root would be told the root is empty — a lie about the
// campaign's own content, and the kind an indexer cannot detect, because it
// converges to a campaign with no pages and reports success at every step.
//
// What `Glob` promises is narrower and this test states it in two parts:
//
//   - A pattern that **matches** a symlink the policy refuses is an error, not a
//     match. `*.md` matches `escape.md`, which is a link out of the root, and a
//     short list there would be indistinguishable from a directory that does not
//     contain one.
//   - A pattern through a symlinked **directory** returns nothing under either
//     policy, because `fs.Glob` does not descend through the link at all. Nothing
//     outside the root is named either way, which is the security property; the
//     *quiet short set* is the convergence observation, and `Root.List` explicitly
//     refuses to produce one where a symlink is an entry. The indexer walks rather
//     than globs (`BuildPageIndex` uses `Walk`, which reports a refused link to its
//     callback and continues), so the omission does not reach `pages` — but a future
//     caller that globbed would silently index fewer pages than the tree holds.
//
// Every match is then opened through `At` and read, so the assertion is not "the
// function refused" but "nothing that came back names a file outside the root".
func TestAPatternReachesNothingOutsideTheRoot(t *testing.T) {
	t.Parallel()

	for _, policy := range []content.SymlinkPolicy{content.RefuseSymlinks, content.AllowSymlinks} {
		t.Run(policyName(policy), func(t *testing.T) {
			t.Parallel()

			root, fx := newTraversalRoot(t, policy)

			// The matching-symlink case. `escape.md` is a leaf link out of the root,
			// so `*.md` matches it.
			matches, err := root.Glob("*.md")

			switch policy {
			case content.RefuseSymlinks:
				if !errors.Is(err, content.ErrSymlink) {
					t.Errorf("Glob(\"*.md\") error = %v over %v, want ErrSymlink: a match "+
						"that is a refused link must be an error rather than a short list, "+
						"which is what an indexer cannot tell from an empty directory",
						err, matches)
				}
			case content.AllowSymlinks:
				// The permissive policy may report the link; what it may not do is let
				// the match be read.
				for _, match := range matches {
					assertConfinedRead(t, root, fx, match)
				}
			}

			// The directory-component case, which is a quietly short set rather than
			// an error under either policy.
			through, err := root.Glob("doorway/*.md")
			if err != nil {
				t.Errorf("Glob(\"doorway/*.md\") error = %v; the pattern contains no `..` "+
					"and an error here would say the policy is refusing a link it never "+
					"matched", err)
			}

			for _, match := range through {
				assertConfinedRead(t, root, fx, match)
			}

			// The positive, because a `Glob` that answered nothing to everything would
			// pass every assertion above.
			inside, err := root.Glob("notes/*.md")
			if err != nil {
				t.Fatalf("Glob(\"notes/*.md\") error = %v, want nil", err)
			}

			if len(inside) != 1 || inside[0] != "notes/child.md" {
				t.Errorf("Glob(\"notes/*.md\") = %v, want [notes/child.md]", inside)
			}
		})
	}
}

// assertConfinedRead opens one match through `At`, reads it, and requires that
// either the read failed or the bytes are the page's own.
//
// The two acceptable answers and no others: a match naming a file outside the root
// either refuses (which is the boundary) or is never returned (which is `fs.Glob`'s
// choice not to descend). What is not acceptable is the file's contents coming back.
func assertConfinedRead(
	t *testing.T,
	root *content.Root,
	fx traversalFixture,
	match string,
) {
	t.Helper()

	target, err := root.At(match)
	if err != nil {
		return
	}

	body, err := target.ReadFile()
	if err != nil {
		return
	}

	if strings.Contains(string(body), traversalSecret) {
		t.Errorf("Glob returned %q and reading it yielded the outside file's contents; "+
			"the fixture for it is %s", match, filepath.Join(fx.outside, "secret.md"))
	}
}

// policyName labels the two policies in a subtest name.
//
// A function because a subtest name of "0" or "1" is a name nobody can read in a
// failure message, and the *distinction* between the two is the whole point of the
// case.
func policyName(policy content.SymlinkPolicy) string {
	if policy == content.AllowSymlinks {
		return "the permissive policy"
	}

	return "the refusing policy"
}

// TestARefusedTraversalDoesNotConsultTheFilesystem is the small property that makes
// a refusal a refusal rather than a probe, and it is asserted over every shape.
//
// `Resolve` asks about existence only *after* confinement is settled. The reverse
// order would let the shape of a path change whether the filesystem is consulted at
// all — which is the difference between a 404 and a probe, because
// `ErrNotExist` names the path it could not find and `ErrOutsideRoot` does not.
//
// So: an escaping path and a missing path are different errors, and the escaping one
// says nothing about whether anything is there.
func TestARefusedTraversalDoesNotConsultTheFilesystem(t *testing.T) {
	t.Parallel()

	root, fx := newTraversalRoot(t, content.RefuseSymlinks)

	// A path that leaves the root and names a file that is certainly there.
	outside := filepath.ToSlash(filepath.Join("..", fx.outsideName, "secret.md"))

	_, err := root.Resolve(outside, content.RootDir())
	if !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("Resolve(%q) error = %v, want ErrOutsideRoot", outside, err)
	}

	if errors.Is(err, content.ErrNotExist) {
		t.Error("an escape was reported as a missing path: the two are different " +
			"answers and conflating them turns a refusal into a 404 that confirms nothing")
	}

	// A path that stays inside and is not there: the other answer, and it does name
	// the path, because a missing page inside a campaign the caller was already
	// authorised for is what a broken-link report is built from.
	missing := "notes/never-written.md"

	_, err = root.Resolve(missing, content.RootDir())
	if !errors.Is(err, content.ErrNotExist) {
		t.Fatalf("Resolve(%q) error = %v, want ErrNotExist", missing, err)
	}

	// The two refusals say different things about the filesystem, and the difference
	// is the whole reason they are two errors: only `ErrNotExist` names a path,
	// because a missing page inside a campaign the caller was already authorised for
	// is what a broken-link report is built from — while the refusals carry no path
	// at all, so their text is a function of the policy rather than of the
	// attacker's input (S-12.3).
	//
	// Asserted rather than described, because the two are one line apart in
	// `classify` and a swap is invisible in review.
	if strings.Contains(content.ErrOutsideRoot.Error(), fx.outsideName) {
		t.Errorf("ErrOutsideRoot carries the offending path (%q); a refusal's text must "+
			"be a function of the policy and not of the input",
			content.ErrOutsideRoot.Error())
	}

	// `At` itself does not consult the filesystem, because a save addresses a file
	// that is about to be created — which is what makes "existence is asked about
	// only after confinement is settled" a statement about the order of two
	// operations rather than a comment about one of them.
	pending, err := root.At("notes/never-written.md")
	if err != nil {
		t.Fatalf("At() on a page that does not exist error = %v, want nil: a save "+
			"addresses a file that is about to be created", err)
	}

	if pending.Path() != "notes/never-written.md" {
		t.Errorf("Path() = %q, want the page's own path", pending.Path())
	}
}
