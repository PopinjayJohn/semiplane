// Package watch provides a debounced recursive file watcher over a vault
// directory, emitting events for Markdown (*.md) files only.
//
// Hash-suppressing echo loops (ignoring events caused by the process's own
// writes) is the CALLER's job: compare event paths against content hashes or
// mtimes you track yourself. This package only coalesces bursts.
package watch

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	errNotDir      = errors.New("not a directory")
	errOutsideRoot = errors.New("path outside root")
)

// DefaultDebounce is used by New when debounce <= 0.
const DefaultDebounce = 100 * time.Millisecond

// OutBuffer is the capacity of the events channel. Sends are non-blocking;
// events are dropped with a warning when the consumer falls behind.
const OutBuffer = 128

// Op is the kind of filesystem change observed for a path.
type Op string

const (
	OpCreate Op = "create"
	OpWrite  Op = "write"
	OpRemove Op = "remove"
	OpRename Op = "rename"
)

// Event describes a change to a single vault-relative slash path.
type Event struct {
	Path string // vault-relative slash path, e.g. "notes/todo.md"
	Op   Op
}

// Watcher recursively watches root for *.md changes, debouncing bursts per
// path. Create it with New, consume Events, and Close when done.
type Watcher struct {
	root     string
	debounce time.Duration

	fs      *fsnotify.Watcher
	out     chan Event
	done    chan struct{}
	stopped chan struct{}

	closeOnce sync.Once
	closeErr  error

	watched map[string]struct{}
}

// New creates a Watcher over root, adding watches for all existing
// directories (skipping .semiplane and .git at any depth) and starting the
// event loop. A debounce <= 0 selects DefaultDebounce.
func New(root string, debounce time.Duration) (*Watcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &os.PathError{Op: "watch", Path: root, Err: errNotDir}
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:     abs,
		debounce: debounce,
		fs:       fs,
		out:      make(chan Event, OutBuffer),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		watched:  make(map[string]struct{}),
	}
	if err := w.addRecursive(abs); err != nil {
		_ = fs.Close()
		return nil, err
	}
	go w.loop()
	return w, nil
}

// Events returns the channel delivering debounced Events.
func (w *Watcher) Events() <-chan Event { return w.out }

// Close stops the watcher, flushing nothing, and closes the Events channel.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		w.closeErr = w.fs.Close()
		<-w.stopped
		close(w.out)
	})
	return w.closeErr
}

// RelativePath converts an absolute path under root to a vault-relative
// slash path. It errors for paths outside root.
func RelativePath(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &os.PathError{Op: "relative", Path: abs, Err: errOutsideRoot}
	}
	return filepath.ToSlash(rel), nil
}

func (w *Watcher) loop() {
	defer close(w.stopped)

	pending := make(map[string]Op)
	var timer *time.Timer
	var timerCh <-chan time.Time

	schedule := func() {
		if timer == nil {
			timer = time.NewTimer(w.debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.debounce)
		}
		timerCh = timer.C
	}

	flush := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timerCh = nil
		}
		for abs, op := range pending {
			delete(pending, abs)
			rel, err := RelativePath(w.root, abs)
			if err != nil {
				slog.Debug("watch: skipping event outside root", "path", abs)
				continue
			}
			ev := Event{Path: rel, Op: op}
			select {
			case w.out <- ev:
			default:
				slog.Warn("watch: dropping event, consumer slow", "path", rel, "op", string(op))
			}
		}
	}

	for {
		select {
		case <-w.done:
			flush()
			return
		case raw, ok := <-w.fs.Events:
			if !ok {
				flush()
				return
			}
			w.handleRaw(raw, pending, schedule)
		case err, ok := <-w.fs.Errors:
			if !ok {
				continue
			}
			// Overflow means events were lost; re-establish watches so no
			// new directory goes unwatched. Nothing spurious is emitted.
			slog.Warn("watch: watcher error, resyncing", "err", err)
			w.resync()
		case <-timerCh:
			flush()
		}
	}
}

// handleRaw translates one fsnotify event into pending state. Only *.md
// files feed the debounce map; directory creates/removes trigger watch
// maintenance instead.
func (w *Watcher) handleRaw(raw fsnotify.Event, pending map[string]Op, schedule func()) {
	name := raw.Name

	if raw.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if _, ok := w.watched[name]; ok {
			// A watched directory went away or moved: re-walk and fix up.
			w.resync()
		} else if isMarkdown(name) {
			pending[name] = mapOp(raw.Op)
			schedule()
		} else {
			// A non-markdown path was removed/renamed; it may still have
			// been a directory we never watched (race) — resync is cheap.
			if st, err := os.Stat(name); err == nil && st.IsDir() {
				w.resync()
			}
		}
		return
	}

	if raw.Op&fsnotify.Create != 0 {
		if st, err := os.Stat(name); err == nil && st.IsDir() {
			// New directory (or moved-in tree): watch it and below.
			if err := w.addRecursive(name); err != nil {
				slog.Warn("watch: add recursive failed", "path", name, "err", err)
			}
			return
		}
		if isMarkdown(name) {
			pending[name] = OpCreate
			schedule()
		}
		return
	}

	if raw.Op&fsnotify.Write != 0 {
		if isMarkdown(name) {
			pending[name] = OpWrite
			schedule()
		}
		return
	}
	// Chmod and anything else: ignored.
}

func mapOp(op fsnotify.Op) Op {
	switch {
	case op&fsnotify.Rename != 0:
		return OpRename
	case op&fsnotify.Remove != 0:
		return OpRemove
	case op&fsnotify.Write != 0:
		return OpWrite
	default:
		return OpCreate
	}
}

// resync re-walks root, adding watches for any directory lacking one and
// pruning entries for directories that no longer exist. Called on
// directory remove/rename and on watcher overflow.
func (w *Watcher) resync() {
	_ = filepath.WalkDir(w.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != w.root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if _, ok := w.watched[p]; ok {
			return nil
		}
		if err := w.fs.Add(p); err != nil {
			slog.Warn("watch: resync add failed", "path", p, "err", err)
			return nil
		}
		w.watched[p] = struct{}{}
		return nil
	})
	for p := range w.watched {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			w.fs.Remove(p) //nolint:errcheck // best effort prune
			delete(w.watched, p)
		}
	}
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != w.root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if _, ok := w.watched[p]; ok {
			return nil
		}
		if err := w.fs.Add(p); err != nil {
			slog.Warn("watch: add failed", "path", p, "err", err)
			return nil
		}
		w.watched[p] = struct{}{}
		return nil
	})
}

func skipDir(name string) bool {
	return name == ".semiplane" || name == ".git"
}

func isMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}
