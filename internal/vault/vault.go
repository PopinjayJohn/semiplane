package vault

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// Vault implements pluginapi.NoteRepository over a directory of Markdown files.
type Vault struct {
	Root string
}

// New returns a Vault rooted at root.
func New(root string) *Vault {
	return &Vault{Root: root}
}

// Compile-time interface check.
var _ pluginapi.NoteRepository = (*Vault)(nil)

// HashContent returns the hex sha256 of b.
func HashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile returns the hex sha256 of the note at vault-relative path.
func (v *Vault) HashFile(path string) (string, error) {
	rel, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(v.Root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("vault get %q: %w", rel, pluginapi.ErrNotFound)
		}
		return "", err
	}
	return HashContent(b), nil
}

// Get reads a note.
func (v *Vault) Get(_ context.Context, path string) (pluginapi.Note, error) {
	rel, err := canonicalPath(path)
	if err != nil {
		return pluginapi.Note{}, err
	}
	b, err := os.ReadFile(filepath.Join(v.Root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return pluginapi.Note{}, fmt.Errorf("vault get %q: %w", rel, pluginapi.ErrNotFound)
		}
		return pluginapi.Note{}, err
	}
	return pluginapi.Note{Path: rel, Content: string(b), Hash: HashContent(b)}, nil
}

// Put creates or replaces a note with optional hash precondition.
func (v *Vault) Put(_ context.Context, note pluginapi.Note) (pluginapi.Note, error) {
	rel, err := canonicalPath(note.Path)
	if err != nil {
		return pluginapi.Note{}, err
	}
	target := filepath.Join(v.Root, filepath.FromSlash(rel))

	if note.Hash != "" {
		cur, err := os.ReadFile(target)
		if err != nil {
			if !os.IsNotExist(err) {
				return pluginapi.Note{}, err
			}
			// New file but caller supplied a precondition hash: any
			// supplied hash for a nonexistent file is stale.
			return pluginapi.Note{}, fmt.Errorf("vault put %q: %w", rel, pluginapi.ErrConflict)
		} else if HashContent(cur) != note.Hash {
			return pluginapi.Note{}, fmt.Errorf("vault put %q: %w", rel, pluginapi.ErrConflict)
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return pluginapi.Note{}, err
	}

	var randBytes [8]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		return pluginapi.Note{}, err
	}
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".%s.tmp-%x", filepath.Base(target), randBytes))

	content := []byte(note.Content)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return pluginapi.Note{}, err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := fsyncDir(filepath.Dir(target)); err != nil {
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return pluginapi.Note{}, err
	}
	if err := fsyncDir(filepath.Dir(target)); err != nil {
		return pluginapi.Note{}, err
	}

	return pluginapi.Note{Path: rel, Content: note.Content, Hash: HashContent(content)}, nil
}

// Delete removes a note.
func (v *Vault) Delete(_ context.Context, path string) error {
	rel, err := canonicalPath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(v.Root, filepath.FromSlash(rel))); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("vault delete %q: %w", rel, pluginapi.ErrNotFound)
		}
		return err
	}
	return nil
}

// List returns lexical sorted vault-relative slash paths of *.md under prefix.
func (v *Vault) List(_ context.Context, prefix string) ([]string, error) {
	clean := ""
	if prefix != "" {
		clean = filepath.ToSlash(filepath.Clean(prefix))
		if filepath.IsAbs(prefix) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
			return nil, fmt.Errorf("vault list prefix %q: invalid path", prefix)
		}
		if clean == "." {
			clean = ""
		}
	}

	var out []string
	err := filepath.WalkDir(v.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(v.Root, p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if matchPrefix(slash, clean) {
			out = append(out, slash)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// matchPrefix reports whether slash path is under prefix, where prefix may be
// "" (all), a directory, or a partial file-name prefix.
func matchPrefix(slash, prefix string) bool {
	if prefix == "" {
		return true
	}
	if slash == prefix {
		return true
	}
	if strings.HasPrefix(slash, prefix+"/") {
		return true
	}
	// File-prefix match: "notes/ab" matches "notes/abc.md".
	if strings.HasPrefix(slash, prefix) {
		rest := slash[len(prefix):]
		// Only treat as file-prefix match when the boundary is inside the
		// final path element (prefix must not end mid-separator).
		if !strings.Contains(rest, "/") && !strings.HasSuffix(prefix, "/") {
			return true
		}
	}
	return false
}

// canonicalPath validates and canonicalizes a vault-relative slash path.
func canonicalPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("vault path %q: empty path", p)
	}
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("vault path %q: invalid path", p)
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("vault path %q: invalid path", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("vault path %q: invalid path", p)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("vault path %q: invalid path", p)
		}
	}
	if !strings.HasSuffix(clean, ".md") {
		return "", fmt.Errorf("vault path %q: must end in .md", p)
	}
	return clean, nil
}

func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
