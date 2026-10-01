package content_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// The fixtures are real directories in t.TempDir() and real symlinks, never
// mocks. A confinement test over a fake filesystem tests the fake: the claim
// under test is a claim about what the kernel does with `..`, a relative
// symlink, and a rename, and none of those are things a mock has.

// fixture is one campaign's content tree on disk, plus a directory outside it.
type fixture struct {
	// base is the content root's directory on the host.
	base string
	// outside holds a file no confined read may reach. It exists so the escape
	// assertions are assertions about a real refusal and not about a name that
	// merely looks wrong — a TempDir with nothing in it is refused for the
	// wrong reason.
	outside string
	// escape is a root-relative reference that leaves the root, built from the
	// real name of `outside`.
	escape string
}

// newFixture builds a content tree with pages nested a few levels deep, a
// relative symlink out of the root, and a relative symlink that stays inside.
func newFixture(t *testing.T) fixture {
	t.Helper()

	base := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}

	// Depth five is deliberate. Confinement has to hold at every component,
	// and a check that only examined the first would pass at depth two.
	tree(t, base, map[string]string{
		"index.md":               "the campaign index",
		"notes/child.md":         "a child page",
		"notes/deep/leaf.md":     "a leaf page",
		"notes/deep/deeper/x.md": "the deepest page",
	})

	// Relative, not absolute. `os.Root` refuses an absolute symlink outright,
	// so an absolute fixture would exercise the stdlib's easy case and never
	// reach this package's own check.
	if err := os.Symlink(
		filepath.Join("..", filepath.Base(outside), "secret.md"),
		filepath.Join(base, "escape.md"),
	); err != nil {
		t.Fatalf("symlink out: %v", err)
	}

	// A link that stays inside, which is the case the policy is about.
	if err := os.Symlink("notes/child.md", filepath.Join(base, "alias.md")); err != nil {
		t.Fatalf("symlink in: %v", err)
	}

	return fixture{
		base:    base,
		outside: outside,
		escape:  filepath.ToSlash(filepath.Join("..", filepath.Base(outside), "secret.md")),
	}
}

// newRoot opens a root over a fresh fixture and returns it, closing it when the
// test ends.
func newRoot(t *testing.T, policy content.SymlinkPolicy) (*content.Root, fixture) {
	t.Helper()

	fx := newFixture(t)

	root, err := content.NewRoot("test-campaign", fx.base, policy)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}

	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})

	return root, fx
}

// tree writes files into dir, keyed by slash-separated root-relative paths.
// Intermediate directories are created, because a campaign's pages are nested
// and a fixture that is not would leave half the path handling untested.
func tree(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}

		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// TestResolveRefusesEscape is S-3.5's central assertion: a `..` that leaves the
// root is refused, for reads and for writes, and the file it aimed at is
// untouched afterwards.
func TestResolveRefusesEscape(t *testing.T) {
	t.Parallel()

	root, fx := newRoot(t, content.RefuseSymlinks)

	_, err := root.Resolve(fx.escape, content.RootDir())
	if !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("resolve escape: got %v, want ErrOutsideRoot", err)
	}

	// The write path too. Confinement that covers reads only is a read-only
	// sandbox, not a boundary.
	_, err = root.At(fx.escape)
	if !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("at escape: got %v, want ErrOutsideRoot", err)
	}

	// A relative escape from a nested directory, which is the shape a
	// front-matter path takes and the reason Resolve takes a base at all.
	base, err := content.DirOf("notes/deep/page.md")
	if err != nil {
		t.Fatalf("dir of: %v", err)
	}

	_, err = root.Resolve("../../../index.md", base)
	if !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("resolve nested escape: got %v, want ErrOutsideRoot", err)
	}

	// "Refused" as opposed to "refused with a side effect".
	body, err := os.ReadFile(filepath.Join(fx.outside, "secret.md"))
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}

	if string(body) != "SECRET" {
		t.Fatalf("outside file was modified: %q", body)
	}

	assertNames(t, names(fx.outside), []string{"secret.md"})
}

// TestResolveRefusesAbsolutePath covers the other shape a request can carry. An
// absolute path is not a slow escape, it is not a path at all in a
// root-relative world, and it is answered as malformed so a caller can tell a
// 400 from a rejection.
func TestResolveRefusesAbsolutePath(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	absolute, err := filepath.Abs(filepath.Join(t.TempDir(), "elsewhere.md"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	slash := filepath.ToSlash(absolute)

	if _, err := root.Resolve(slash, content.RootDir()); !errors.Is(err, content.ErrInvalidRef) {
		t.Fatalf("resolve absolute: got %v, want ErrInvalidRef", err)
	}

	if _, err := root.At(slash); !errors.Is(err, content.ErrInvalidRef) {
		t.Fatalf("at absolute: got %v, want ErrInvalidRef", err)
	}
}

// TestCleanRefRefusesUnusablePaths is the lexical half of confinement, stated
// over the inputs that cannot occur in a request as well as the ones that can.
// A normaliser with an untested corner is where a bypass goes to live.
func TestCleanRefRefusesUnusablePaths(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	tests := []struct {
		name    string
		rel     string
		want    string
		wantErr error
	}{
		{name: "plain", rel: "index.md", want: "index.md"},
		{name: "nested", rel: "notes/deep/leaf.md", want: "notes/deep/leaf.md"},
		{name: "dot is normalised", rel: "./index.md", want: "index.md"},
		{name: "double slash is normalised", rel: "notes//child.md", want: "notes/child.md"},
		{name: "trailing slash is trimmed", rel: "notes/", want: "notes"},
		{name: "parent that stays inside", rel: "notes/deep/../child.md", want: "notes/child.md"},

		{name: "escape", rel: "../elsewhere.md", wantErr: content.ErrOutsideRoot},
		{
			name:    "escape after normalising",
			rel:     "notes/../../elsewhere.md",
			wantErr: content.ErrOutsideRoot,
		},
		{name: "bare parent", rel: "..", wantErr: content.ErrOutsideRoot},
		{name: "bare dot", rel: ".", wantErr: content.ErrInvalidRef},
		{name: "dot reached by round trip", rel: "notes/..", wantErr: content.ErrInvalidRef},
		{name: "absolute", rel: "/etc/passwd", wantErr: content.ErrInvalidRef},
		{name: "empty", rel: "", wantErr: content.ErrInvalidRef},
		{name: "nul", rel: "index\x00.md", wantErr: content.ErrInvalidRef},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target, err := root.At(tc.rel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("at %q: got %v, want %v", tc.rel, err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("at %q: %v", tc.rel, err)
			}

			if got := target.Path(); got != tc.want {
				t.Fatalf("at %q: path got %q, want %q", tc.rel, got, tc.want)
			}
		})
	}
}

// TestSymlinkPolicy covers S-4.4. The default refuses a link, including one
// that stays inside the root, and the permissive policy follows an inside link
// while a link that leaves is still refused by `os.Root` itself — the property
// that makes AllowSymlinks a policy choice rather than a hole.
func TestSymlinkPolicy(t *testing.T) {
	t.Parallel()

	t.Run("default refuses a link that stays inside", func(t *testing.T) {
		t.Parallel()

		root, _ := newRoot(t, content.RefuseSymlinks)

		_, err := root.Resolve("alias.md", content.RootDir())
		if !errors.Is(err, content.ErrSymlink) {
			t.Fatalf("resolve inside link: got %v, want ErrSymlink", err)
		}

		// ErrSymlink and not ErrOutsideRoot: the two are separate answers
		// because the remedy is. A vault with a link in it is a thing to look
		// at; an escape is an attack.
		if errors.Is(err, content.ErrOutsideRoot) {
			t.Fatalf("inside link reported as an escape: %v", err)
		}
	})

	t.Run("default refuses a link that leaves", func(t *testing.T) {
		t.Parallel()

		root, _ := newRoot(t, content.RefuseSymlinks)

		_, err := root.Resolve("escape.md", content.RootDir())
		if !errors.Is(err, content.ErrSymlink) {
			t.Fatalf("resolve escaping link: got %v, want ErrSymlink", err)
		}
	})

	t.Run("default refuses a link in the middle of a path", func(t *testing.T) {
		t.Parallel()

		// A single Lstat of the whole path cannot catch this: Lstat does not
		// follow the last component, so a symlinked directory above the target
		// is invisible to it. This is the case that makes the prefix walk
		// necessary rather than tidier.
		root, fx := newRoot(t, content.RefuseSymlinks)

		if err := os.Symlink("notes", filepath.Join(fx.base, "aliased-dir")); err != nil {
			t.Fatalf("symlink dir: %v", err)
		}

		_, err := root.Resolve("aliased-dir/child.md", content.RootDir())
		if !errors.Is(err, content.ErrSymlink) {
			t.Fatalf("resolve through aliased dir: got %v, want ErrSymlink", err)
		}
	})

	t.Run("permissive follows a link that stays inside", func(t *testing.T) {
		t.Parallel()

		root, _ := newRoot(t, content.AllowSymlinks)

		target, err := root.Resolve("alias.md", content.RootDir())
		if err != nil {
			t.Fatalf("resolve inside link: %v", err)
		}

		body, err := target.ReadFile()
		if err != nil {
			t.Fatalf("read through link: %v", err)
		}

		if string(body) != "a child page" {
			t.Fatalf("read through link: got %q", body)
		}
	})

	t.Run("permissive still refuses a link that leaves", func(t *testing.T) {
		t.Parallel()

		root, _ := newRoot(t, content.AllowSymlinks)

		_, err := root.Resolve("escape.md", content.RootDir())
		if !errors.Is(err, content.ErrOutsideRoot) {
			t.Fatalf("resolve escaping link: got %v, want ErrOutsideRoot", err)
		}
	})
}

// TestResolveNestedPath is the other half of the escape tests: confinement that
// refuses everything also passes its own test suite, and a real campaign is
// nested.
func TestResolveNestedPath(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	const want = "the deepest page"

	target, err := root.Resolve("notes/deep/deeper/x.md", content.RootDir())
	if err != nil {
		t.Fatalf("resolve nested: %v", err)
	}

	if got := target.Path(); got != "notes/deep/deeper/x.md" {
		t.Fatalf("path: got %q", got)
	}

	// Root-relative, which is what makes it safe to put in a cache key, a log
	// line or a link href. An absolute one would carry the host's directory
	// layout into all three.
	if filepath.IsAbs(target.Path()) {
		t.Fatalf("path is absolute: %q", target.Path())
	}

	body, err := target.ReadFile()
	if err != nil {
		t.Fatalf("read nested: %v", err)
	}

	if string(body) != want {
		t.Fatalf("read nested: got %q, want %q", body, want)
	}
}

// TestResolveRelativeToBase is the rule a relative link in markdown means:
// relative to the referring page's directory, not to the campaign root. The
// `..`-that-stays-inside case is the one that exercises the join against a
// non-empty base; a sibling alone would pass with a base that was ignored.
func TestResolveRelativeToBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  string
		base string
		want string
	}{
		{
			name: "sibling in the same directory",
			ref:  "child.md",
			base: "notes/page.md",
			want: "notes/child.md",
		},
		{
			name: "parent that stays inside the root",
			ref:  "../../index.md",
			base: "notes/deep/page.md",
			want: "index.md",
		},
		{
			name: "up then down again",
			ref:  "../child.md",
			base: "notes/deep/page.md",
			want: "notes/child.md",
		},
		{
			name: "explicit dot is normalised away",
			ref:  "./child.md",
			base: "notes/page.md",
			want: "notes/child.md",
		},
		{
			name: "several levels down from the root",
			ref:  "notes/child.md",
			base: "index.md",
			want: "notes/child.md",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root, _ := newRoot(t, content.RefuseSymlinks)

			base, err := content.DirOf(tc.base)
			if err != nil {
				t.Fatalf("dir of %s: %v", tc.base, err)
			}

			target, err := root.Resolve(tc.ref, base)
			if err != nil {
				t.Fatalf("resolve %q from %s: %v", tc.ref, base, err)
			}

			if got := target.Path(); got != tc.want {
				t.Fatalf("path: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDirOf covers the helper reference resolution depends on, including the
// root itself and page paths that escape.
func TestDirOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		page    string
		want    string
		wantErr error
	}{
		{name: "top level page", page: "index.md", want: "."},
		{name: "nested page", page: "notes/deep/leaf.md", want: "notes/deep"},
		{name: "escaping page", page: "../outside.md", wantErr: content.ErrOutsideRoot},
		{name: "absolute page", page: "/etc/passwd", wantErr: content.ErrInvalidRef},
		{name: "empty page", page: "", wantErr: content.ErrInvalidRef},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir, err := content.DirOf(tc.page)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("dir of %q: got %v, want %v", tc.page, err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("dir of %q: %v", tc.page, err)
			}

			if got := dir.String(); got != tc.want {
				t.Fatalf("dir of %q: got %q, want %q", tc.page, got, tc.want)
			}
		})
	}
}

// TestRootDirIsTheTopOfTheCampaign pins the zero value's meaning, because it is
// the base every front-matter path in a top-level page resolves against and
// nothing else states it.
func TestRootDirIsTheTopOfTheCampaign(t *testing.T) {
	t.Parallel()

	if got := content.RootDir().String(); got != "." {
		t.Fatalf("root dir: got %q, want %q", got, ".")
	}

	root, _ := newRoot(t, content.RefuseSymlinks)

	target, err := root.Resolve("index.md", content.RootDir())
	if err != nil {
		t.Fatalf("resolve from root dir: %v", err)
	}

	if got := target.Path(); got != "index.md" {
		t.Fatalf("path: got %q", got)
	}
}

// TestMissingIsDistinguishableFromRefused is the assertion that keeps an
// existence oracle out of the error surface. A missing page is a 404 and an
// escape is a rejection, and a caller that cannot tell them apart either 404s
// an attack — losing the signal — or rejects a missing page, which turns a
// broken link into an incident.
func TestMissingIsDistinguishableFromRefused(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	_, missingErr := root.Resolve("notes/absent.md", content.RootDir())
	if !errors.Is(missingErr, content.ErrNotExist) {
		t.Fatalf("resolve missing: got %v, want ErrNotExist", missingErr)
	}

	if errors.Is(missingErr, content.ErrOutsideRoot) {
		t.Fatalf("missing reported as a refusal: %v", missingErr)
	}

	// The missing message names the path, because a broken-link report is
	// built from it.
	if got := missingErr.Error(); !strings.Contains(got, "notes/absent.md") {
		t.Fatalf("missing error does not name the path: %q", got)
	}

	_, refusedErr := root.Resolve("../elsewhere.md", content.RootDir())
	if !errors.Is(refusedErr, content.ErrOutsideRoot) {
		t.Fatalf("resolve refused: got %v, want ErrOutsideRoot", refusedErr)
	}

	if errors.Is(refusedErr, content.ErrNotExist) {
		t.Fatalf("refusal reported as missing: %v", refusedErr)
	}

	// No path in the refusal, so its text is a function of the policy rather
	// than of the attacker's input — an error message that varies with the
	// request is a channel.
	if got := refusedErr.Error(); got != content.ErrOutsideRoot.Error() {
		t.Fatalf("refusal carries input: %q", got)
	}
}

// TestWriteFileIsAtomic covers S-6.4's write mechanics from the outside, which
// is the only angle available: the new content is in place, the directory holds
// nothing but the page, and the mode is the one asked for.
func TestWriteFileIsAtomic(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	target, err := root.At("notes/deep/leaf.md")
	if err != nil {
		t.Fatalf("at: %v", err)
	}

	if writeErr := target.WriteFile(t.Context(), []byte("rewritten"), 0o600); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}

	body, err := target.ReadFile()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if string(body) != "rewritten" {
		t.Fatalf("read back: got %q", body)
	}

	// The whole directory, asserted rather than the file alone. "The file I
	// wrote has the right content" passes even when the write left a staged
	// file behind, and a leftover is a second copy of a page in the tree.
	assertEntries(t, root, "notes/deep", "deeper", "leaf.md")

	info, err := target.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode: got %o, want %o", perm, 0o600)
	}
}

// TestWriteFileCreatesNewPage covers a save to a path that does not exist yet,
// and asserts the same clean directory: the staged file has to be gone for a
// create and an overwrite alike.
func TestWriteFileCreatesNewPage(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	target, err := root.At("notes/brand-new.md")
	if err != nil {
		t.Fatalf("at: %v", err)
	}

	if writeErr := target.WriteFile(t.Context(), []byte("new page"), 0o600); writeErr != nil {
		t.Fatalf("write new: %v", writeErr)
	}

	assertEntries(t, root, "notes", "brand-new.md", "child.md", "deep")

	// And it is readable afterwards through the ordinary read path, which is
	// the assertion that a write is not visible only to the writer.
	resolved, err := root.Resolve("notes/brand-new.md", content.RootDir())
	if err != nil {
		t.Fatalf("resolve new page: %v", err)
	}

	if got := resolved.Path(); got != "notes/brand-new.md" {
		t.Fatalf("path: got %q", got)
	}
}

// TestWriteFileAtTheTopLevel exercises the atomic write where the target's
// directory *is* the content root, so the staged file is created beside the
// root's own entries and the directory fsync is on the root.
//
// A bare tree, because the shared fixture carries symlinks and List refuses
// those: the assertion here is that nothing extra was left in the directory,
// which needs a directory whose whole contents are known.
func TestWriteFileAtTheTopLevel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tree(t, dir, map[string]string{
		"index.md":       "the index",
		"notes/child.md": "a child",
	})

	root, err := content.NewRoot("top-level", dir, content.RefuseSymlinks)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})

	target, err := root.At("index.md")
	if err != nil {
		t.Fatalf("at: %v", err)
	}

	if writeErr := target.WriteFile(t.Context(), []byte("replaced index"), 0o640); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}

	assertEntries(t, root, ".", "index.md", "notes")

	body, readErr := target.ReadFile()
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}

	if string(body) != "replaced index" {
		t.Fatalf("read back: got %q", body)
	}

	info, err := target.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("mode: got %o, want %o", perm, 0o640)
	}
}

// TestWriteFileRefusesEscape is the write half of the confinement, stated on
// its own because a write that is not confined does not need a read to be
// found. The staged file is created in the *target's* directory, so a target
// outside the root would stage outside the root too.
func TestWriteFileRefusesEscape(t *testing.T) {
	t.Parallel()

	root, fx := newRoot(t, content.RefuseSymlinks)

	planted := filepath.ToSlash(filepath.Join("..", filepath.Base(fx.outside), "planted.md"))

	if _, err := root.At(planted); !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("at escape: got %v, want ErrOutsideRoot", err)
	}

	// Nothing outside, and nothing inside either: the refusal happens before
	// the handle exists, so there is no write that can fail.
	assertNames(t, names(fx.outside), []string{"secret.md"})
}

// TestWriteFileReplacesSymlinkNotTarget asserts the rename's behaviour on a
// planted link. A save must not be redirectable outside the root by putting a
// symlink at the destination, and `os.Root`'s rename replaces the path rather
// than following it.
//
// Asserted because the write path depends on that behaviour and does not itself
// provide it, and because the alternative — a rename that followed the link —
// would be a write an attacker redirected with a filesystem object.
func TestWriteFileReplacesSymlinkNotTarget(t *testing.T) {
	t.Parallel()

	// Under the refusing policy At will not hand out a handle for a symlinked
	// path, so this runs under AllowSymlinks, where the handle exists and the
	// question of what the rename does to the link is live.
	root, fx := newRoot(t, content.AllowSymlinks)

	target, err := root.At("alias.md")
	if err != nil {
		t.Fatalf("at link: %v", err)
	}

	overwritten := []byte("written over the link")
	if writeErr := target.WriteFile(t.Context(), overwritten, 0o600); writeErr != nil {
		t.Fatalf("write over link: %v", writeErr)
	}

	// The link is gone, replaced by a regular file holding the new content...
	body, err := target.ReadFile()
	if err != nil {
		t.Fatalf("read rewritten: %v", err)
	}

	if string(body) != "written over the link" {
		t.Fatalf("rewritten content: got %q", body)
	}

	// ...and the page the link pointed at is unchanged, which is the assertion
	// that matters: a write followed the link.
	original, err := root.At("notes/child.md")
	if err != nil {
		t.Fatalf("at original: %v", err)
	}

	untouched, err := original.ReadFile()
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	if string(untouched) != "a child page" {
		t.Fatalf("link target was written through: %q", untouched)
	}

	// And no staged file was left where the link was.
	info, err := os.Lstat(filepath.Join(fx.base, "alias.md"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("alias.md is still a symlink")
	}
}

// TestOpenIsConfinedAndSeekable covers the handle P6 serves ranged asset reads
// from: usable — a real stat and a real ranged read — and naming a
// root-relative path, so nothing downstream has an absolute path to escape
// with.
func TestOpenIsConfinedAndSeekable(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	target, err := root.Resolve("index.md", content.RootDir())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	file, err := target.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("file stat: %v", err)
	}

	if info.Size() != int64(len("the campaign index")) {
		t.Fatalf("size: got %d", info.Size())
	}

	// The ranged read itself, which is the whole of what http.ServeContent
	// needs from this handle.
	if _, err := file.Seek(4, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}

	tail := make([]byte, 8)
	if _, err := file.Read(tail); err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if string(tail) != "campaign" {
		t.Fatalf("tail: got %q", tail)
	}
}

// TestReadFileOnADirectoryDistinguishesErrors pins the classification of a path
// that exists, is inside the root, and is not readable as a file. It is neither
// "missing" nor "outside", and answering either would send a caller looking in
// the wrong place.
func TestReadFileOnADirectoryDistinguishesErrors(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	target, err := root.At("notes")
	if err != nil {
		t.Fatalf("at dir: %v", err)
	}

	_, err = target.ReadFile()
	if errors.Is(err, content.ErrNotExist) {
		t.Fatalf("a directory reported as missing: %v", err)
	}

	if errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("a directory reported as an escape: %v", err)
	}
}

// TestListRefusesSymlinks covers the listing path, because a listing that
// quietly omits a symlink is indistinguishable from a directory that does not
// contain one — and the index built from it would be wrong with nothing to
// notice.
func TestListRefusesSymlinks(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	// A clean directory lists.
	assertEntries(t, root, "notes/deep", "deeper", "leaf.md")

	// A directory holding a symlink refuses rather than returning a short list.
	if _, err := root.List("."); !errors.Is(err, content.ErrSymlink) {
		t.Fatalf("list dir with link: got %v, want ErrSymlink", err)
	}

	// A path that exists and is not a directory is its own answer.
	_, err := root.List("index.md")
	if !errors.Is(err, content.ErrNotDir) {
		t.Fatalf("list a file: got %v, want ErrNotDir", err)
	}

	// A missing directory is missing, not refused.
	if _, err := root.List("notes/absent"); !errors.Is(err, content.ErrNotExist) {
		t.Fatalf("list missing dir: got %v, want ErrNotExist", err)
	}
}

// TestWalkReportsRefusedSymlinks covers the tree scan, whose contract differs
// from every other operation: a refused symlink is passed to the callback and
// the walk continues, because a vault containing one link still has pages that
// need indexing.
func TestWalkReportsRefusedSymlinks(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	var pages, refused []string

	err := root.Walk(func(rel string, entry fs.DirEntry, err error) error {
		if errors.Is(err, content.ErrSymlink) {
			refused = append(refused, rel)

			return nil
		}

		if err != nil {
			return err
		}

		if !entry.IsDir() {
			pages = append(pages, rel)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Both links refused, including the one that stays inside the root, because
	// S-4.4 rejects symlinks rather than only the dangerous ones.
	if !slices.Equal(refused, []string{"alias.md", "escape.md"}) {
		t.Fatalf("refused: got %v", refused)
	}

	// And the walk reached everything else, which is the property that makes
	// reporting a refusal rather than aborting worth anything.
	for _, want := range []string{
		"index.md",
		"notes/child.md",
		"notes/deep/leaf.md",
		"notes/deep/deeper/x.md",
	} {
		if !slices.Contains(pages, want) {
			t.Fatalf("walk did not reach %s: %v", want, pages)
		}
	}
}

// TestWalkPropagatesCallbackError asserts the callback's own refusal stops the
// walk, which is how a caller caps the work it will do.
func TestWalkPropagatesCallbackError(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	sentinel := errors.New("enough")

	var visited int

	err := root.Walk(func(string, fs.DirEntry, error) error {
		visited++

		if visited == 3 {
			return sentinel
		}

		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("walk: got %v, want the callback's error", err)
	}

	if visited != 3 {
		t.Fatalf("walk visited %d entries after the callback refused", visited)
	}
}

// TestGlobRefusesEscapingPattern covers a glob asked for outside the root.
// `fs.Glob` answers an escaping pattern with an empty list rather than an
// error, so a caller who did not check would be told the campaign is empty — a
// lie about the campaign's own content, and one an indexer cannot detect.
func TestGlobRefusesEscapingPattern(t *testing.T) {
	t.Parallel()

	root, _ := newRoot(t, content.RefuseSymlinks)

	if _, err := root.Glob("../*.md"); !errors.Is(err, content.ErrOutsideRoot) {
		t.Fatalf("glob escape: got %v, want ErrOutsideRoot", err)
	}

	matches, err := root.Glob("notes/*.md")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	if !slices.Equal(matches, []string{"notes/child.md"}) {
		t.Fatalf("glob: got %v", matches)
	}
}

// TestRegistryResolvesBySlug covers the mapping the HTTP layer needs, from a
// `{slug}` in a path to a confined root. A campaign with no retained root is the
// S-4.5 case and has to be distinguishable from any other failure.
func TestRegistryResolvesBySlug(t *testing.T) {
	t.Parallel()

	reg := content.NewRegistry(content.RefuseSymlinks)

	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("close registry: %v", err)
		}
	})

	dir := t.TempDir()
	tree(t, dir, map[string]string{"index.md": "content"})

	if _, err := reg.Get("nobody"); !errors.Is(err, content.ErrNoRoot) {
		t.Fatalf("get before open: got %v, want ErrNoRoot", err)
	}

	opened, err := reg.Open("the-campaign", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.Slug() != "the-campaign" {
		t.Fatalf("slug: got %q", opened.Slug())
	}

	got, err := reg.Get("the-campaign")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got != opened {
		t.Fatal("get returned a different root than Open did")
	}

	// A second adoption of the same slug is refused rather than replacing the
	// handle, which would leak the old one and swap a confinement boundary.
	if _, err := reg.Open("the-campaign", dir); !errors.Is(err, content.ErrDuplicateRoot) {
		t.Fatalf("duplicate open: got %v, want ErrDuplicateRoot", err)
	}

	if slugs := reg.Slugs(); !slices.Equal(slugs, []string{"the-campaign"}) {
		t.Fatalf("slugs: got %v", slugs)
	}

	// Get does not close, and a Root shared by every request for the campaign
	// must not be closable by one of them.
	if _, err := got.At("index.md"); err != nil {
		t.Fatalf("at after get: %v", err)
	}
}

// TestRegistryAdoptMatchesTheRetainCallback pins the signature the registrar's
// retain callback has, so `campaigns.NewRegistrar(store, base, reg.Adopt)`
// compiles without a closure.
func TestRegistryAdoptMatchesTheRetainCallback(t *testing.T) {
	t.Parallel()

	reg := content.NewRegistry(content.RefuseSymlinks)

	dir := t.TempDir()
	tree(t, dir, map[string]string{"index.md": "adopted"})

	// Opened the way the registrar opens it, and handed over the same way.
	raw, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open raw root: %v", err)
	}

	if adoptErr := reg.Adopt("adopted-campaign", raw); adoptErr != nil {
		t.Fatalf("adopt: %v", adoptErr)
	}

	// A duplicate adoption must not close the handle it was given. The
	// registrar's contract is that it closes a handle it failed to hand over,
	// so closing here as well would double-close it.
	dupErr := reg.Adopt("adopted-campaign", raw)
	if !errors.Is(dupErr, content.ErrDuplicateRoot) {
		t.Fatalf("duplicate adopt: got %v, want ErrDuplicateRoot", dupErr)
	}

	root, err := reg.Get("adopted-campaign")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Still usable, which is the assertion that the rejected adoption did not
	// close the handle out from under the registry.
	target, err := root.Resolve("index.md", content.RootDir())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	body, err := target.ReadFile()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(body) != "adopted" {
		t.Fatalf("body: got %q", body)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	// Close empties the registry, so a second close has nothing left to leak
	// and a post-shutdown Get reports no root rather than a closed one.
	if _, err := reg.Get("adopted-campaign"); !errors.Is(err, content.ErrNoRoot) {
		t.Fatalf("get after close: got %v, want ErrNoRoot", err)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestRegistryOpenFailsWithoutLeaking asserts a failed Open leaves nothing
// retained, so a campaign whose directory is missing does not occupy the slug
// and block the retry that would fix it.
func TestRegistryOpenFailsWithoutLeaking(t *testing.T) {
	t.Parallel()

	reg := content.NewRegistry(content.RefuseSymlinks)

	missing := filepath.Join(t.TempDir(), "never-created")

	if _, err := reg.Open("ghost", missing); err == nil {
		t.Fatal("open of a missing directory succeeded")
	}

	if slugs := reg.Slugs(); len(slugs) != 0 {
		t.Fatalf("slugs after a failed open: %v", slugs)
	}
}

// TestRegistrySlugsAreSorted asserts the startup and shutdown order is stable,
// because a map's iteration order differs between runs and two runs of the same
// server over the same data should produce the same log.
func TestRegistrySlugsAreSorted(t *testing.T) {
	t.Parallel()

	reg := content.NewRegistry(content.RefuseSymlinks)

	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("close registry: %v", err)
		}
	})

	dir := t.TempDir()
	tree(t, dir, map[string]string{"index.md": "content"})

	for _, slug := range []string{"zulu", "alpha", "mike", "bravo"} {
		if _, err := reg.Open(slug, dir); err != nil {
			t.Fatalf("open %s: %v", slug, err)
		}
	}

	want := []string{"alpha", "bravo", "mike", "zulu"}
	if got := reg.Slugs(); !slices.Equal(got, want) {
		t.Fatalf("slugs: got %v, want %v", got, want)
	}
}

// TestRegistryIsSafeForConcurrentUse is the `-race` assertion for the registry,
// exercised through the shape the HTTP layer produces: many goroutines
// registering and resolving at once while others read pages through the roots
// they get back.
//
// All campaigns share one directory on purpose. A root is confined to a path,
// not to the contents of a path, so two campaigns reading the same fixture is
// what makes the concurrent access here real rather than incidental.
func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const (
		campaignCount = 8
		readerCount   = 16
		perReader     = 20
	)

	reg := content.NewRegistry(content.RefuseSymlinks)

	dir := t.TempDir()
	tree(t, dir, map[string]string{
		"index.md":       "shared index",
		"notes/child.md": "shared child",
	})

	var wg sync.WaitGroup

	// Registrars. Started first, but not awaited: the readers below begin
	// immediately, so Get races the Open rather than following it. A campaign
	// not registered yet is an ordinary outcome and not a failure.
	for idx := range campaignCount {
		wg.Go(func() {
			slug := campaignSlug(idx)
			if _, err := reg.Open(slug, dir); err != nil {
				t.Errorf("open %s: %v", slug, err)

				return
			}

			if slugs := reg.Slugs(); len(slugs) == 0 {
				t.Errorf("slugs empty after opening %s", slug)
			}
		})
	}

	for range readerCount {
		wg.Go(func() {
			for step := range perReader {
				// Every reader visits every campaign, so the access pattern
				// does not depend on a random draw and a failure reproduces.
				slug := campaignSlug((step + readerCount) % campaignCount)

				root, err := reg.Get(slug)
				if errors.Is(err, content.ErrNoRoot) {
					continue
				}

				if err != nil {
					t.Errorf("get %s: %v", slug, err)

					return
				}

				target, err := root.Resolve("notes/child.md", content.RootDir())
				if err != nil {
					t.Errorf("resolve in %s: %v", slug, err)

					return
				}

				if _, err := target.ReadFile(); err != nil {
					t.Errorf("read in %s: %v", slug, err)

					return
				}
			}
		})
	}

	wg.Wait()

	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	if slugs := reg.Slugs(); len(slugs) != 0 {
		t.Fatalf("slugs after close: %v", slugs)
	}
}

// TestRootIsSafeForConcurrentReads is the same assertion one level down, on the
// root itself. `os.Root` is documented safe for concurrent use and the render
// cache in P3 will read through a shared root from many request goroutines, so
// the property is asserted here rather than taken on faith.
func TestRootIsSafeForConcurrentReads(t *testing.T) {
	t.Parallel()

	const (
		readers   = 24
		perReader = 25
	)

	root, _ := newRoot(t, content.RefuseSymlinks)

	var wg sync.WaitGroup

	for range readers {
		wg.Go(func() {
			for range perReader {
				target, err := root.Resolve("notes/deep/deeper/x.md", content.RootDir())
				if err != nil {
					t.Errorf("resolve: %v", err)

					return
				}

				body, err := target.ReadFile()
				if err != nil {
					t.Errorf("read: %v", err)

					return
				}

				if string(body) != "the deepest page" {
					t.Errorf("body: got %q", body)

					return
				}
			}
		})
	}

	wg.Wait()
}

// campaignSlug returns the slug of campaign idx.
func campaignSlug(idx int) string {
	return "campaign-" + string(rune('a'+idx))
}

// assertEntries asserts a confined directory holds exactly the named entries,
// sorted before comparison.
//
// The whole-directory assertion is the point. "The file I wrote has the right
// content" passes even when the write left a staged file behind, and a leftover
// staged file is a second copy of a page that the watcher will pick up.
func assertEntries(t *testing.T, root *content.Root, rel string, want ...string) {
	t.Helper()

	entries, err := root.List(rel)
	if err != nil {
		t.Fatalf("list %s: %v", rel, err)
	}

	got := entryNames(entries)
	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Fatalf("entries of %s: got %v, want %v", rel, got, want)
	}
}

// assertNames asserts a plain directory on the host holds exactly these names.
func assertNames(t *testing.T, got, want []string) {
	t.Helper()

	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Fatalf("directory entries: got %v, want %v", got, want)
	}
}

// names returns the names of a directory's entries on the host.
func names(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// The callers have already proved the directory exists; a failure here
		// is a broken test rather than a broken product.
		panic("reading fixture directory " + dir + ": " + err.Error())
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	return got
}

// entryNames maps directory entries to their names.
//
// Not sorted: `fs.ReadDir` documents itself as sorted, and a helper that
// re-sorted would hide a caller that stopped relying on that.
func entryNames(entries []fs.DirEntry) []string {
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	return got
}
