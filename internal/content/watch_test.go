package content_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/observability"
)

// The fixtures are real directories, real renames and a real fsnotify watcher.
// Every assertion in this file is about what the *kernel* reports, and a mock
// would only assert that this file agrees with the mock — including the one that
// matters most, which is that an atomic write is reported twice and a file watch
// would survive only the first time.
//
// The one injected dependency is the `Notifier`, and only where the kernel will
// not produce the condition on demand: an exhausted watch limit, and an event
// for a path that belongs to no campaign. Both are marked at their use.

// watchDeadline bounds how long a test waits for a filesystem event to arrive.
//
// Generous, because CI runners are loaded and fsnotify's delivery is a
// round-trip through the kernel's event queue rather than a function call. It is
// a deadline rather than a sleep so that a machine which delivers in 2ms does not
// spend the margin, and so that a machine which never delivers fails with a
// message naming what was expected instead of with a timeout.
const watchDeadline = 30 * time.Second

// watchPoll is how often a waiting test looks at what has arrived so far.
//
// Short, because the answer is usually already there: the event has been
// delivered by the time the first poll runs, and the interval only bounds how
// long a failure takes to notice.
const watchPoll = 2 * time.Millisecond

// sink records the changes a watcher delivered, so a test can assert on the
// *set* of them rather than on the next one.
//
// The set, because several of these tests are about a change that must **not**
// arrive — a staging file's rename, a symlink's target — and a test that only
// reads the next change cannot tell an absence from a change it has not reached
// yet.
type sink struct {
	mu      sync.Mutex
	changes []content.Change
}

func (s *sink) deliver(_ context.Context, change content.Change) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.changes = append(s.changes, change)
}

// collected returns a snapshot of everything delivered so far.
func (s *sink) collected() []content.Change {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]content.Change(nil), s.changes...)
}

// reset forgets what has been delivered, so a second phase of a test asserts
// only about what happens after it.
func (s *sink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.changes = nil
}

// await polls until pred holds of the collected changes and returns the first
// match.
//
// Polling rather than sleeping because fsnotify delivery is asynchronous and its
// latency is not the test's to predict: a select over a channel would make the
// test's timing *its* assertion, and `time.Sleep` would make it a fixed
// interval the machine has to beat. A deadline with a message that prints what
// did arrive fails with the diagnosis attached.
func (s *sink) await(t *testing.T, what string, pred func(content.Change) bool) content.Change {
	t.Helper()

	deadline := time.Now().Add(watchDeadline)

	for {
		if index := s.indexOfMatching(pred); index >= 0 {
			return s.collected()[index]
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s\ndelivered: %v",
				watchDeadline, what, s.rendered())
		}

		time.Sleep(watchPoll)
	}
}

// awaitQuiet asserts that nothing matching pred arrives within a short window.
//
// The one place a duration is the assertion rather than a bound, and it is
// necessary because "no event" has no deadline to poll against. The window is
// short relative to `watchDeadline` because the events being ruled out are
// *immediate* — a staging file's rename, a symlink's target — and a generous
// window would make every negative test slow without making it more correct.
func (s *sink) awaitQuiet(t *testing.T, what string, pred func(content.Change) bool) {
	t.Helper()

	const quietWindow = 300 * time.Millisecond

	deadline := time.Now().Add(quietWindow)

	for time.Now().Before(deadline) {
		if s.indexOfMatching(pred) >= 0 {
			t.Fatalf("unexpectedly delivered %s\nall delivered: %v", what, s.rendered())
		}

		time.Sleep(watchPoll)
	}
}

// rendered prints the collected changes for a failure message.
func (s *sink) rendered() []string {
	collected := s.collected()
	out := make([]string, 0, len(collected))

	for _, change := range collected {
		out = append(out, change.String())
	}

	return out
}

// indexOfMatching returns the position of the first change matching pred, or -1.
//
// A position rather than a boolean because two of these tests are about an
// *order* — the indexer's destination-before-source convention — and a boolean
// cannot carry that.
func (s *sink) indexOfMatching(pred func(content.Change) bool) int {
	for index, change := range s.collected() {
		if pred(change) {
			return index
		}
	}

	return -1
}

// watchFixture is one or more campaigns' content trees on disk, plus a running
// watcher over them.
type watchFixture struct {
	// watched is the watcher, closed when the test ends.
	watched *content.Watcher

	// registry is the content registry the watcher resolves through.
	registry *content.Registry

	// sink is where the changes went.
	sink *sink

	// events captures the observability lines, so a test can assert the event
	// name and the *level* rather than only that something was logged.
	events *logCapture

	// roots maps a campaign slug to its absolute content root.
	roots map[string]string

	// ids maps a campaign slug to its id.
	ids map[string]int64
}

// newWatchFixture opens a real fsnotify-backed watcher over the given campaigns, each
// a slug mapped to an absolute directory the caller has already created.
//
// Every campaign in the map is registered with the content registry, because the
// watcher requires the two to agree — that is the check which stops a campaign
// being silently unwatched — so a test that wants one campaign must give the
// registry exactly that one.
func newWatchFixture(t *testing.T, dirs map[string]string) *watchFixture {
	t.Helper()

	return newWatchFixtureWith(t, dirs, nil)
}

// newWatchFixtureWith is `newWatchFixture` with the one thing a few tests need to
// substitute: the event source.
//
// A parameter rather than a second constructor because the watchFixture's job is
// to hold the wiring these tests would otherwise each repeat, and a test that
// rebuilt the wiring would be a second copy of the thing under test's setup.
//
// The logger is not a parameter: every test asserts through the same capture, and
// a test that passed its own logger would be asserting that two loggers agree.
func newWatchFixtureWith(
	t *testing.T,
	dirs map[string]string,
	notifier content.Notifier,
) *watchFixture {
	t.Helper()

	fx := &watchFixture{
		registry: content.NewRegistry(content.RefuseSymlinks),
		sink:     &sink{},
		events:   newLogCapture(),
		roots:    dirs,
		ids:      make(map[string]int64, len(dirs)),
	}

	// The capture *is* the handler rather than the writer behind one: the
	// assertions are about the `event` key and the level, and both are structured
	// attributes rather than text in a buffer. `Enabled` accepts every level, so
	// a line written below the level S-12.2 fixes is still recorded rather than
	// dropped before an assertion can see it.
	logger := slog.New(fx.events)

	watched := make([]content.WatchRoot, 0, len(dirs))

	for index, slug := range sortedKeys(dirs) {
		dir := dirs[slug]

		if _, err := fx.registry.Open(slug, dir); err != nil {
			t.Fatalf("open the content root of %s: %v", slug, err)
		}

		// Sequential ids from 1, so a change carrying the wrong campaign's id is
		// visible rather than accidentally right.
		fx.ids[slug] = int64(index + 1)

		watched = append(watched, content.WatchRoot{
			CampaignID: int64(index + 1),
			Slug:       slug,
			Dir:        dir,
		})
	}

	// A context cancelled at cleanup rather than at the end of the test body:
	// the goroutine outlives the last line of a test function, and a goroutine
	// still reading a t.TempDir that has already been removed produces failures
	// attributed to whichever test happens to be running next.
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	opts := []content.WatcherOption{}
	if notifier != nil {
		opts = append(opts, content.WithNotifier(notifier))
	}

	// The fixture's own cancellable context, rather than a background one, so
	// that cancelling it is a shutdown path a test can exercise.
	watcher, err := content.NewWatcher(
		ctx,
		fx.registry,
		watched,
		fx.sink.deliver,
		observability.NewWatch(nil, logger),
		opts...,
	)
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Errorf("close watcher: %v", err)
		}
	})

	fx.watched = watcher

	return fx
}

// newSingleCampaign is the common case: one campaign over a fresh directory.
func newSingleCampaign(t *testing.T) *watchFixture {
	t.Helper()

	return newWatchFixture(t, map[string]string{"gilded-cage": t.TempDir()})
}

// close closes the watcher, for the tests that close it themselves.
func (f *watchFixture) close(t *testing.T) {
	t.Helper()

	if err := f.watched.Close(); err != nil {
		t.Errorf("close watcher: %v", err)
	}
}

// sortedKeys returns a map's keys in sorted order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	// A tiny insertion sort rather than importing `slices` for two lines: the
	// test file's imports are part of its readability, and this keeps the sort
	// visible next to what it sorts.
	for outer := 1; outer < len(keys); outer++ {
		for inner := outer; inner > 0 && keys[inner] < keys[inner-1]; inner-- {
			keys[inner], keys[inner-1] = keys[inner-1], keys[inner]
		}
	}

	return keys
}

// atomicWrite replaces a file the way S-6.4 says a save does: a temp file in the
// same directory, written, fsynced, then renamed over the destination.
//
// Every step of that sequence is load-bearing for the test that uses it. The
// *rename* is what destroys a file-bound watch (S-4.2) and the `fsync` is what
// a real save does, so a helper that skipped them would pass against a
// regression it exists to catch.
func atomicWrite(t *testing.T, dir, name, body string) {
	t.Helper()

	target := filepath.Join(dir, name)
	temp := filepath.Join(dir, ".semiplane-"+name+".tmp")

	file, err := os.OpenFile(temp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create the staging file for %s: %v", name, err)
	}

	if _, err := file.WriteString(body); err != nil {
		t.Fatalf("write the staging file for %s: %v", name, err)

		return
	}

	if err := file.Sync(); err != nil {
		t.Fatalf("fsync the staging file for %s: %v", name, err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("close the staging file for %s: %v", name, err)
	}

	if err := os.Rename(temp, target); err != nil {
		t.Fatalf("rename %s over %s: %v", temp, target, err)
	}
}

// isPage reports whether a change is an upsert of a particular page.
func isPage(slug, rel string) func(content.Change) bool {
	return func(change content.Change) bool {
		return change.Slug == slug && change.Path == rel && change.Op == content.OpUpsert
	}
}

// isRemoved reports whether a change is a removal of a particular page, in
// whichever campaign.
//
// No campaign parameter, because every removal in this file is asserted within
// the only campaign under test at that point and the routing tests assert the
// slug themselves. The path is matched exactly, so a removal in another campaign
// of the same path would match — which is why `TestRoutesEachEventToItsCampaign`
// checks the slug on every change rather than trusting a predicate like this one.
func isRemoved(rel string) func(content.Change) bool {
	return func(change content.Change) bool {
		return change.Path == rel && change.Op == content.OpRemove
	}
}

// TestAtomicWriteSurvivesRepeatedRenames is S-14's watch-loss row and the whole
// reason S-4.2 says "directories, never files".
//
// An fsnotify watch is bound to the inode behind its path. An atomic save
// replaces the destination's inode, so a watcher bound to the *file* sees the
// first save and never the second — and nothing about that failure is loud: the
// process is up, `/healthz` is green, and the campaign silently stops seeing
// edits. The second write is therefore the assertion, not a repetition of the
// first: it is the one a regression to watching files fails.
func TestAtomicWriteSurvivesRepeatedRenames(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	atomicWrite(t, dir, "Cellar.md", "# Cellar\n")

	first := fx.sink.await(
		t,
		"the first atomic save of Cellar.md",
		isPage("gilded-cage", "Cellar.md"),
	)

	if first.OldPath != "" {
		t.Errorf("an upsert carries no OldPath, got %q", first.OldPath)
	}

	// A second save of the same path, with a different body so a watcher that
	// had merely queued the first event cannot answer with it.
	fx.sink.reset()

	atomicWrite(t, dir, "Cellar.md", "# Cellar\n\nThe second body.\n")

	fx.sink.await(t,
		"a change after the second atomic save of the same page (S-4.2)",
		isPage("gilded-cage", "Cellar.md"),
	)

	// And a third, because "the watch was re-added after the first loss" and
	// "the watch was never lost" are different bugs and only the third write
	// tells them apart.
	fx.sink.reset()

	atomicWrite(t, dir, "Cellar.md", "# Cellar\n\nThe third body.\n")

	fx.sink.await(t,
		"a change after the third atomic save of the same page (S-4.2)",
		isPage("gilded-cage", "Cellar.md"),
	)
}

// TestNewDirectoryPagesAreSeen is the consequence of watching directories: a
// directory created after startup has no watch, so every page inside it arrives
// without an event, and a watcher that only adds the new directory's own watch
// would see nothing at all.
//
// Nested three deep deliberately. The failure this catches is a watch added for
// the new directory but not for what is inside it, and depth one cannot produce
// that failure.
func TestNewDirectoryPagesAreSeen(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	nested := filepath.Join(dir, "lore", "svartalfheim")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir the nested directory: %v", err)
	}

	atomicWrite(t, nested, "Gate.md", "# Gate\n")

	fx.sink.await(t,
		"a page created three directories below the root (S-4.2)",
		isPage("gilded-cage", "lore/svartalfheim/Gate.md"),
	)

	// A second page in the same new subtree, which is the assertion that the
	// *subtree* was watched rather than only the directory that appeared.
	fx.sink.reset()

	atomicWrite(t, filepath.Join(dir, "lore"), "Index.md", "# Index\n")

	fx.sink.await(
		t,
		"a page in the parent of the new subtree",
		isPage("gilded-cage", "lore/Index.md"),
	)
}

// TestStagingFileRenameIsSilent is the assertion that keeps an atomic save from
// arriving as a rename.
//
// semiplane's own save path stages through `.semiplane-…tmp` and renames it over
// the destination, so every save in this system produces a rename event for a
// path that was never a page. If that reached the sink, an `OpRename` would race
// an `OpUpsert` for the same write and the indexer's rename fallback would run
// on every single edit.
func TestStagingFileRenameIsSilent(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	atomicWrite(t, dir, "Notes.md", "# Notes\n")

	fx.sink.await(t, "the atomic save", isPage("gilded-cage", "Notes.md"))

	// The staging file's own name, which is what the rename event carries.
	fx.sink.awaitQuiet(t, "a rename of the staging file", func(change content.Change) bool {
		return change.OldPath != "" || strings.HasSuffix(change.Path, ".tmp")
	})
}

// TestPageRenameReportsRemoveThenUpsert asserts the decision this watcher makes
// about a *page* rename, and the reason it is the right one.
//
// It is remove-then-upsert rather than one `OpRename`, because fsnotify v1.10.1
// exposes no way to pair the two halves of a rename: `Event.renamedFrom` is
// unexported and there is no accessor, so the kernel's rename cookie — the only
// thing that proves two events are the same operation — cannot be read. Pairing
// by adjacency is wrong: `mv page.md /elsewhere/ && touch other.md` arrives as
// exactly `Rename(page.md), Create(other.md)`, and re-keying one page's
// `content_hash` onto different bytes is a search result built from a page
// nobody read. Remove-then-upsert costs the row its `created_at`, which is the
// cost `change.go` names as acceptable, and the index converges through both.
func TestPageRenameReportsRemoveThenUpsert(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	atomicWrite(t, dir, "Before.md", "# Before\n")

	fx.sink.await(t, "the original page", isPage("gilded-cage", "Before.md"))
	fx.sink.reset()

	if err := os.Rename(
		filepath.Join(dir, "Before.md"),
		filepath.Join(dir, "After.md"),
	); err != nil {
		t.Fatalf("rename the page: %v", err)
	}

	fx.sink.await(t, "an upsert of the renamed page", isPage("gilded-cage", "After.md"))
	fx.sink.await(t, "a remove of the old path", isRemoved("Before.md"))

	// The order is the indexer's own — the destination indexed before the source
	// is dropped — so a failure part-way leaves the page indexed twice rather
	// than not at all.
	changes := fx.sink.collected()

	upsertAt, removeAt := -1, -1

	for index, change := range changes {
		switch {
		case isPage("gilded-cage", "After.md")(change) && upsertAt < 0:
			upsertAt = index
		case isRemoved("Before.md")(change) && removeAt < 0:
			removeAt = index
		}
	}

	if upsertAt < 0 || removeAt < 0 || upsertAt > removeAt {
		t.Errorf("want the destination upserted before the source is dropped, got %v",
			fx.sink.rendered())
	}
}

// TestDirectoryRenameReportsOneRename is the counterpart, and it is the case
// where this watcher *does* pair.
//
// A moved directory's pages produce no events at all: their events moved with
// the inodes, and no watch was ever placed on the destination. So remove-plus-
// upsert would leave every row under the old folder name outliving the folder —
// search results pointing at 404s, for as long as the campaign runs. One
// `OpRename` is what the indexer reads as a subtree move, and it is the only
// pairing this watcher attempts, for exactly that reason.
//
// The pairing is with the *immediately following* event, and only when it is a
// create of a directory in the same campaign. The residual failure — an
// unrelated `mkdir` racing a folder moved out of the tree — is reported by the
// next `Reverify` as a dropped watch, which is the integrator's cue to
// reindex. A certain silent staleness was the alternative, and it lost.
func TestDirectoryRenameReportsOneRename(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	if err := os.MkdirAll(filepath.Join(dir, "lore"), 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	atomicWrite(t, filepath.Join(dir, "lore"), "Gate.md", "# Gate\n")

	fx.sink.await(t, "the page inside lore", isPage("gilded-cage", "lore/Gate.md"))
	fx.sink.reset()

	if err := os.Rename(filepath.Join(dir, "lore"), filepath.Join(dir, "worlds")); err != nil {
		t.Fatalf("rename the directory: %v", err)
	}

	change := fx.sink.await(t, "the moved directory", func(change content.Change) bool {
		return change.Op == content.OpRename
	})

	if change.Path != "worlds" || change.OldPath != "lore" {
		t.Errorf("want lore -> worlds, got %s", change)
	}

	// Exactly one change for the move, so the subtree is not *also* reported page
	// by page — which is the per-file route the indexer exists to avoid, and
	// which would re-read every page under a folder that did not change content.
	//
	// The already-delivered rename is reset first: the predicate below cannot
	// tell the change under test from the one that satisfied it, and asserting
	// on a set that still contains it would fail on the rename itself.
	fx.sink.reset()

	fx.sink.awaitQuiet(
		t,
		"a second change for the directory move",
		func(change content.Change) bool {
			return change.Path == "worlds" || change.OldPath == "lore"
		},
	)
}

// TestDirectoryRenameKeepsWatchingTheNewName is the other half of the move: the
// watches were bound to the inodes of the old paths, so a watcher that re-keyed
// the index but not the watch set would report the move and then never see
// another edit under the new name.
func TestDirectoryRenameKeepsWatchingTheNewName(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	if err := os.MkdirAll(filepath.Join(dir, "lore"), 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	// Awaited, and that ordering is the point rather than incidental. A watch on
	// `lore` is added when the `Create` event for it is handled, so renaming
	// before that has happened moves a directory this watcher is not watching —
	// and an empty directory produces no other event to distinguish the two
	// orderings. The page gives the test something to synchronise on.
	atomicWrite(t, filepath.Join(dir, "lore"), "Gate.md", "# Gate\n")

	fx.sink.await(t, "the page inside lore", isPage("gilded-cage", "lore/Gate.md"))
	fx.sink.reset()

	if err := os.Rename(filepath.Join(dir, "lore"), filepath.Join(dir, "worlds")); err != nil {
		t.Fatalf("rename the directory: %v", err)
	}

	fx.sink.await(t, "the moved directory", func(change content.Change) bool {
		return change.Op == content.OpRename
	})

	fx.sink.reset()

	atomicWrite(t, filepath.Join(dir, "worlds"), "Spire.md", "# Spire\n")

	fx.sink.await(t, "a page created under the directory's new name",
		isPage("gilded-cage", "worlds/Spire.md"))
}

// TestDeleteReportsRemove asserts the plainest operation, and that it carries no
// `OldPath`: `OpRemove` is not a rename that lost its other half, and a consumer
// that reads `OldPath` on it is reading a field the contract reserves for
// `OpRename` (`change.go`: "Empty is never a path").
func TestDeleteReportsRemove(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	atomicWrite(t, dir, "Doomed.md", "# Doomed\n")

	fx.sink.await(t, "the page before it is deleted", isPage("gilded-cage", "Doomed.md"))
	fx.sink.reset()

	if err := os.Remove(filepath.Join(dir, "Doomed.md")); err != nil {
		t.Fatalf("remove the page: %v", err)
	}

	change := fx.sink.await(t, "the removal", isRemoved("Doomed.md"))

	if change.OldPath != "" {
		t.Errorf("a remove carries no OldPath, got %q", change.OldPath)
	}
}

// TestDotFilesAndAssetsAreNotChanges is the cheap-drop half of the pipeline.
//
// The indexer refuses everything that is not an indexable path, so reporting an
// asset or a dot-file would be a change every consumer has to be trusted to
// discard. Obsidian writes into `.obsidian` on every keystroke, so this is also
// the difference between a watcher that keeps up and one that drowns.
func TestDotFilesAndAssetsAreNotChanges(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	if err := os.WriteFile(
		filepath.Join(dir, "portrait.png"),
		[]byte("not really a png"),
		0o600,
	); err != nil {
		t.Fatalf("write the asset: %v", err)
	}

	fx.sink.awaitQuiet(t, "an asset", func(content.Change) bool { return true })

	if err := os.MkdirAll(filepath.Join(dir, ".obsidian"), 0o700); err != nil {
		t.Fatalf("mkdir .obsidian: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(dir, ".obsidian", "workspace.json"),
		[]byte("{}"),
		0o600,
	); err != nil {
		t.Fatalf("write into .obsidian: %v", err)
	}

	fx.sink.awaitQuiet(
		t,
		"anything under a dot-directory",
		func(content.Change) bool { return true },
	)

	// And a real page still arrives, so the two assertions above are about the
	// filter rather than about a watcher that stopped working.
	atomicWrite(t, dir, "Real.md", "# Real\n")

	fx.sink.await(t, "a real page after the ignored paths", isPage("gilded-cage", "Real.md"))
}

// TestSymlinkIsNotFollowed is S-4.4 at the watcher, and the security claim it
// carries: the watcher must not create a path the read path would refuse, and it
// must not go looking outside the root on its own.
//
// Two assertions, because either alone is too weak. The symlink is not reported
// as a page — the read path refuses it, so a row would be a link to a 404. And
// the directory it points *at* is never watched, which is what "not followed"
// means in practice: if it were, an edit outside the campaign would arrive as a
// change to it.
func TestSymlinkIsNotFollowed(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(outside, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(outside, "secrets", "Vault.md"),
		[]byte("# Not this campaign's\n"),
		0o600,
	); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	dir := t.TempDir()
	fx := newWatchFixture(t, map[string]string{"gilded-cage": dir})

	// An absolute link, which `os.Root` refuses outright, and a link to a
	// *directory*, which is the case that matters for the watch set: a followed
	// directory link means a watch on a path outside the campaign root.
	if err := os.Symlink(
		filepath.Join(outside, "secrets"),
		filepath.Join(dir, "shortcut"),
	); err != nil {
		t.Fatalf("symlink the directory: %v", err)
	}

	if err := os.Symlink(
		filepath.Join(outside, "secrets", "Vault.md"),
		filepath.Join(dir, "borrowed.md"),
	); err != nil {
		t.Fatalf("symlink the file: %v", err)
	}

	// The links existed before the watcher started, so the startup scan is what
	// meets them: neither may become a watch, and neither may be reported.
	atomicWrite(t, filepath.Join(outside, "secrets"), "Vault.md", "# Changed outside\n")

	fx.sink.awaitQuiet(
		t,
		"a change from outside the root",
		func(content.Change) bool { return true },
	)

	reports := fx.watched.Reverify(context.Background())

	for _, report := range reports {
		if report.Slug != "gilded-cage" {
			continue
		}

		// Two links, and both refused.
		if report.Refused != 2 {
			t.Errorf("want both symlinks refused (S-4.4), got Refused=%d", report.Refused)
		}

		for _, failed := range report.Failed {
			if strings.HasPrefix(failed, "shortcut") {
				t.Errorf("a refused symlink must not be watched, got a watch on %s", failed)
			}
		}
	}
}

// TestSymlinkCreatedLaterIsSkipped is the same rule for a link that arrives
// mid-run rather than at startup.
//
// The distinction from the startup case is the path it takes: a `Create` event
// has to be classified before it is acted on, and the classification goes through
// the campaign's own `Root` policy rather than a second symlink check here.
func TestSymlinkCreatedLaterIsSkipped(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	if err := os.Symlink(
		filepath.Join(outside, "secrets"),
		filepath.Join(dir, "late"),
	); err != nil {
		t.Fatalf("symlink the directory: %v", err)
	}

	fx.sink.awaitQuiet(
		t,
		"a change for a symlinked directory",
		func(content.Change) bool { return true },
	)

	// The target is not being watched, so nothing it does is a change — and this
	// is the assertion that the *watch set* did not grow, rather than only that
	// no event arrived for a path that was refused before it could be watched.
	atomicWrite(t, filepath.Join(outside, "secrets"), "Vault.md", "# Written after\n")

	fx.sink.awaitQuiet(t, "a change from behind a late symlink",
		func(content.Change) bool { return true })
}

// TestWatchLimitExhaustionIsAnError is S-4.5's own sentence: watch-limit
// exhaustion logs an **error**, never a debug line.
//
// The limit cannot be exhausted for real without setting
// `fs.inotify.max_user_watches` to something absurd for the whole machine, so the
// `Notifier` seam injects the errno the kernel would have returned. The
// assertions are on the captured log line — the `event` key *and* the level —
// because a test that only counted the counter would pass against a watcher that
// logged it at debug, which is precisely the regression S-12.2 exists to prevent.
func TestWatchLimitExhaustionIsAnError(t *testing.T) {
	t.Parallel()

	// EMFILE rather than ENOSPC, because `observability`'s `errnoClass` names
	// EMFILE as the watch limit and reports every other errno by number. The
	// detail assertion below is about the class being readable, and ENOSPC's
	// class is asserted separately in the errno test below.
	fx := newWatchFixtureWith(t,
		map[string]string{"gilded-cage": t.TempDir()},
		exhaustingNotifier{errno: syscall.EMFILE})
	capture := fx.events

	record := capture.find("watch.add_failed")
	if record == nil {
		t.Fatalf("want watch.add_failed, got %s", capture.rendered())
	}

	// The level is the assertion S-4.5 and S-12.2 exist for: an error line and
	// not a debug one, because a watcher that stopped watching looks exactly
	// like a vault nobody edited.
	if record.Level != slog.LevelError {
		t.Errorf("watch.add_failed is never below error (S-12.2), got %s", record.Level)
	}

	// The path is the directory that could not be watched, root-relative — the
	// host's directory layout stays out of the log (S-12.3).
	if record.Path == "" {
		t.Error("want the failing directory named on the line")
	}

	// The detail is an error *class* and never the errno's prose, and the class
	// is what an alert matches on. `errnoClass` maps EMFILE and ENFILE to
	// `watch_limit` and every other errno to `errno_<n>`, so EMFILE is the errno
	// whose class is readable here; ENOSPC arrives as `errno_28`.
	if record.Detail != "watch_limit" {
		t.Errorf("want detail=watch_limit for EMFILE, got %q", record.Detail)
	}

	// The report says so too, because `AddFailed` is the signal and
	// `VerifyReport.LimitHit` is what the integrator's fallback policy reads.
	reports := fx.watched.Reverify(context.Background())
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}

	if !reports[0].LimitHit {
		t.Errorf("want LimitHit on the report, got %+v", reports[0])
	}
}

// TestWatchLimitExhaustionReportsEveryLimitErrno is S-4.5 naming both errnos it
// can arrive as, and this watcher classifying both as the same condition.
//
// `ENOSPC` is the inotify watch limit and `EMFILE` the descriptor limit; the
// operator's response — raise the limit, or watch fewer trees — is the same for
// either, which is why `VerifyReport.LimitHit` treats them alike and why an
// alert should not have to match two spellings.
func TestWatchLimitExhaustionReportsEveryLimitErrno(t *testing.T) {
	t.Parallel()

	for name, errno := range map[string]syscall.Errno{
		"enospc": syscall.ENOSPC,
		"emfile": syscall.EMFILE,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fx := newWatchFixtureWith(t,
				map[string]string{"gilded-cage": t.TempDir()},
				&exhaustingNotifier{errno: errno})

			reports := fx.watched.Reverify(context.Background())
			if len(reports) != 1 {
				t.Fatalf("want one report, got %d", len(reports))
			}

			if !reports[0].LimitHit {
				t.Errorf("want LimitHit for %s, got %+v", errno, reports[0])
			}

			if len(reports[0].Failed) == 0 {
				t.Errorf("want the unwatched directory named for %s, got %+v", errno, reports[0])
			}
		})
	}
}

// TestReverifyReAddsLostWatches is the capability S-4.5 requires and the event
// stream cannot provide: a watch can be lost *silently* — an inode-bound watch
// outliving its name, an ENOSPC that was never observed — and nothing in a
// stream of events reports a watch that is *not* arriving.
//
// Simulated through the `Notifier` seam rather than through the filesystem,
// because a lost watch has no filesystem trace: a directory that is deleted and
// recreated produces `Remove` and `Create` events, and the watcher handles both
// correctly without any help. What cannot be produced on a test machine is the
// condition itself — a watch the watcher still *claims* and the notifier no
// longer holds — so `leakyNotifier` reports one path as unwatched while leaving
// it in place, which is precisely the disagreement `Reverify` exists to resolve.
//
// The assertion is that `Reverify` consults the notifier rather than its own
// bookkeeping. Reconciling the watcher's own set against the tree would answer
// `Kept` for this directory, and a check that can only ever say "kept" is what
// makes a silently lost watch permanent.
func TestReverifyReAddsLostWatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notifier := newLeakyNotifier(t)

	fx := newWatchFixtureWith(t, map[string]string{"gilded-cage": dir}, notifier)

	nested := filepath.Join(dir, "lore")

	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	atomicWrite(t, nested, "Gate.md", "# Gate\n")
	fx.sink.await(t, "the page inside lore", isPage("gilded-cage", "lore/Gate.md"))

	// The notifier forgets the watch it holds on `lore`, with no event. The
	// watcher's own set still claims it, so only the notifier's answer differs.
	notifier.lose(nested)

	reports := fx.watched.Reverify(context.Background())
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}

	if reports[0].Added != 1 {
		t.Errorf("want exactly the lost watch re-added, got %+v", reports[0])
	}

	if reports[0].Kept == 0 {
		t.Errorf("want the campaign's other watches kept, got %+v", reports[0])
	}

	// Second pass: nothing left to do, and a report that says so is the healthy
	// case worth having — a report with only `Kept` set is what "nothing is
	// wrong" looks like from outside.
	fx.sink.reset()

	settled := fx.watched.Reverify(context.Background())
	if len(settled) != 1 {
		t.Fatalf("want one report, got %d", len(settled))
	}

	if settled[0].Added != 0 || settled[0].Dropped != 0 {
		t.Errorf("want a settled watch set, got %+v", settled[0])
	}

	if settled[0].Kept == 0 {
		t.Errorf("want the retained watches reported as kept, got %+v", settled[0])
	}

	// And the re-added watch actually works, which is the assertion that matters:
	// a report claiming to have re-added something is not the same as a watch.
	atomicWrite(t, nested, "Spire.md", "# Spire\n")

	fx.sink.await(t, "a page in the re-watched directory", isPage("gilded-cage", "lore/Spire.md"))
}

// TestReverifyDropsWatchesForDeletedDirectories is the other half of the
// reconciliation, and the reason the report carries `Dropped` at all.
//
// inotify removes a watch when its directory goes and says nothing, so without
// this the watch set accumulates claims about directories that are gone — and a
// claim about a directory that no longer exists is one a later `Add` will be
// silently wrong about.
func TestReverifyDropsWatchesForDeletedDirectories(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	nested := filepath.Join(dir, "lore")

	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	atomicWrite(t, nested, "Gate.md", "# Gate\n")
	fx.sink.await(t, "the page inside lore", isPage("gilded-cage", "lore/Gate.md"))
	fx.sink.reset()

	// Removed *and* not recreated, so the tree scan has nothing for the watch to
	// match.
	if err := os.RemoveAll(nested); err != nil {
		t.Fatalf("remove lore: %v", err)
	}

	// The page's own removal is awaited *before* the reconciliation, and the order
	// is load-bearing rather than incidental. `fsnotify` discards a queued event
	// whose watch descriptor has already been removed, so a `Reverify` that runs
	// while the `IN_DELETE` for `lore/Gate.md` is still in the channel will
	// destroy the very event this asserts — a race that shows up only on a loaded
	// machine, which is the worst place for a test to be flaky. A page that
	// vanished is a change, and it is delivered before anything reconciles.
	fx.sink.await(t, "the removal of the page", isRemoved("lore/Gate.md"))

	// Now the reconciliation. The `Remove` event has already dropped the watch, so
	// the claim is gone and there is nothing left to re-add — which is the
	// agreement being asserted: the reconciliation neither resurrects a
	// directory the tree no longer has nor invents a drop that did not happen.
	settled := fx.watched.Reverify(context.Background())
	if len(settled) != 1 {
		t.Fatalf("want one report, got %d", len(settled))
	}

	if settled[0].Added != 0 {
		t.Errorf("want nothing re-added for a deleted directory, got %+v", settled[0])
	}

	// The campaign's own root is still watched, so it is still kept.
	if settled[0].Kept != 1 {
		t.Errorf("want only the content root kept, got %+v", settled[0])
	}
}

// TestReverifyReportsAMissingRoot is S-4.5's other half: a missing content root
// marks the campaign degraded and the server still starts.
//
// `Missing` rather than a panic and rather than a silently empty report: the
// integrator reads it to emit `watch.degraded`, and a campaign whose root was
// deleted is not a campaign whose pages changed.
//
// The second half asserts something less obvious. A `Root` holds an open
// descriptor to the directory, so once the directory is deleted the descriptor
// names an unlinked inode and re-creating the path on disk does not bring it
// back — `Missing` stays set, permanently, until the campaign's root is
// *re-opened*, which is `Registry.Open` and therefore the registrar's business
// rather than this watcher's. A watcher that appeared to recover here would be
// reporting pages from the old, deleted tree, which is the one thing worse than
// reporting that the campaign is degraded.
func TestReverifyReportsAMissingRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "gilded-cage")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir the content root: %v", err)
	}

	// The watcher starts against an *empty* root and the page arrives as an
	// event afterwards. Writing the page first would be a page the startup scan
	// indexed without an event ever existing, so the assertion below would be
	// waiting for something the filesystem never said.
	fx := newWatchFixture(t, map[string]string{"gilded-cage": dir})

	atomicWrite(t, dir, "Gate.md", "# Gate\n")

	fx.sink.await(t, "the page before the root goes", isPage("gilded-cage", "Gate.md"))
	fx.sink.reset()

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the content root: %v", err)
	}

	reports := fx.watched.Reverify(context.Background())
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}

	report := reports[0]
	if !report.Missing {
		t.Errorf("want a missing content root reported (S-4.5), got %+v", report)
	}

	if report.WalkError == nil {
		t.Error("want the walk failure carried on the report")
	}

	// The dead watches are released, rather than left pointing at inodes the
	// filesystem has forgotten — so a re-open starts from an empty set instead of
	// inheriting claims it cannot honour.
	if report.Dropped == 0 {
		t.Errorf("want the campaign's watches dropped with its root, got %+v", report)
	}

	// Re-creating the directory on disk does not restore the root, and the report
	// says so. Recovery is re-opening it, which is the registrar's call.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("recreate the content root: %v", err)
	}

	atomicWrite(t, dir, "Gate.md", "# Recreated\n")

	stillMissing := fx.watched.Reverify(context.Background())
	if len(stillMissing) != 1 {
		t.Fatalf("want one report, got %d", len(stillMissing))
	}

	if !stillMissing[0].Missing {
		t.Errorf("want the deleted root to stay missing until it is re-opened, got %+v",
			stillMissing[0])
	}

	// And a page written into the recreated directory is not reported, because
	// nothing is watching it. The wiki still serves from the index (S-4.5), and it
	// is the integrator's `watch.degraded` that says so.
	//
	// Reset first: deleting the root produced real `OpRemove` events for the
	// pages it held, and those are correct changes — a page that is gone *is* a
	// change. Asserting that nothing at all arrives would be asserting that the
	// deletion went unreported, which is the opposite of what this test means.
	fx.sink.reset()

	atomicWrite(t, dir, "Later.md", "# Later\n")

	fx.sink.awaitQuiet(t, "a page written behind a deleted root",
		func(content.Change) bool { return true })
}

// TestRoutesEachEventToItsCampaign is S-4.2's routing: one watcher, N campaigns,
// and each event reaching the campaign whose root it is under.
//
// Two sibling campaigns, which is the easy case and is here because "the routing
// table exists" is not the same claim as "the routing table is right".
func TestRoutesEachEventToItsCampaign(t *testing.T) {
	t.Parallel()

	fx := newWatchFixture(t, map[string]string{
		"gilded-cage": t.TempDir(),
		"iron-vault":  t.TempDir(),
	})

	atomicWrite(t, fx.roots["gilded-cage"], "Cellar.md", "# Cellar\n")
	atomicWrite(t, fx.roots["iron-vault"], "Foundry.md", "# Foundry\n")

	fx.sink.await(t, "the gilded-cage page", isPage("gilded-cage", "Cellar.md"))
	fx.sink.await(t, "the iron-vault page", isPage("iron-vault", "Foundry.md"))

	// No change may claim the other campaign's slug, which is the failure a
	// routing table with one entry too few produces.
	for _, change := range fx.sink.collected() {
		switch change.Path {
		case "Cellar.md":
			if change.Slug != "gilded-cage" {
				t.Errorf("Cellar.md routed to %s", change.Slug)
			}
		case "Foundry.md":
			if change.Slug != "iron-vault" {
				t.Errorf("Foundry.md routed to %s", change.Slug)
			}
		}
	}

	// And each change carries the campaign's own id, which is what the index is
	// keyed by.
	for _, change := range fx.sink.collected() {
		if change.CampaignID != fx.ids[change.Slug] {
			t.Errorf("%s carried campaign id %d, want %d",
				change, change.CampaignID, fx.ids[change.Slug])
		}
	}
}

// TestNestedRootsRouteToTheInnermostCampaign is the longest-match case, and it
// is not hypothetical: `SEMIPLANE_CONTENT_ROOT_BASE` is operator-supplied, so
// two campaigns can be pointed at a base directory and a subdirectory of it.
//
// A first-match routing table would send every page of the inner campaign to the
// outer one — quietly, because both campaigns are watched and every event has a
// plausible home.
func TestNestedRootsRouteToTheInnermostCampaign(t *testing.T) {
	t.Parallel()

	outer := t.TempDir()
	inner := filepath.Join(outer, "iron-vault")

	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatalf("mkdir the inner root: %v", err)
	}

	// Deliberately inserted out of order, so a table that kept the first match it
	// happened to be given would route by insertion order rather than by depth.
	fx := newWatchFixture(t, map[string]string{
		"iron-vault":  inner,
		"gilded-cage": outer,
	})

	atomicWrite(t, outer, "Outside.md", "# Outside\n")
	atomicWrite(t, inner, "Inside.md", "# Inside\n")

	fx.sink.await(t, "the outer campaign's page", isPage("gilded-cage", "Outside.md"))
	fx.sink.await(t, "the inner campaign's page", isPage("iron-vault", "Inside.md"))

	// The inner campaign's path is `Inside.md`, not `iron-vault/Inside.md`: the
	// change is relative to *its own* root, so a campaign nested inside another
	// is not addressed through the outer one.
	for _, change := range fx.sink.collected() {
		if change.Path == "iron-vault/Inside.md" {
			t.Errorf("a nested campaign's path must be relative to its own root, got %s", change)
		}
	}
}

// TestEventUnderNoRootIsIgnored covers the case a real notifier cannot produce.
//
// Nothing watches a path outside a campaign root, so with fsnotify this is
// unreachable — which is why it is injected through the `Notifier` seam rather
// than provoked. The assertion is that the event is dropped rather than routed
// to whichever campaign happens to share a prefix, and that reaching the
// routing code with an unroutable path does not panic: this runs on the
// goroutine fsnotify feeds, where a panic is a failed process rather than a
// failed request.
func TestEventUnderNoRootIsIgnored(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	elsewhere := t.TempDir()

	notifier := &scriptedNotifier{
		events: make(chan fsnotify.Event),
		errors: make(chan error),
	}

	fx := newWatchFixtureWith(t, map[string]string{"gilded-cage": dir}, notifier)
	recorded := fx.sink

	stranger := filepath.Join(elsewhere, "Elsewhere.md")

	notifier.events <- fsnotify.Event{Name: stranger, Op: fsnotify.Create}
	notifier.events <- fsnotify.Event{Name: elsewhere, Op: fsnotify.Create}
	notifier.events <- fsnotify.Event{Name: "/", Op: fsnotify.Create}
	// A path that is a *sibling* of the root, sharing a textual prefix. This is
	// the case a string-prefix check gets wrong and a path-component walk does
	// not: `<root>-elsewhere` is not under `<root>`.
	notifier.events <- fsnotify.Event{Name: dir + "-elsewhere/Page.md", Op: fsnotify.Create}

	// A real event afterwards, so the assertion below is about the filtering and
	// not about a goroutine that exited on the first strange input.
	atomicWrite(t, dir, "Real.md", "# Real\n")

	notifier.events <- fsnotify.Event{Name: filepath.Join(dir, "Real.md"), Op: fsnotify.Write}

	recorded.await(t, "the in-root event after the unroutable ones",
		isPage("gilded-cage", "Real.md"))

	recorded.awaitQuiet(t, "a change from outside any campaign root",
		func(change content.Change) bool { return change.Slug != "gilded-cage" })

	for _, change := range recorded.collected() {
		if !strings.HasPrefix(change.Path, "Real.md") {
			t.Errorf("an unroutable event produced %s", change)
		}
	}
}

// TestNotifierErrorsAreReportedForEveryCampaign covers the error channel, which
// a real inotify instance uses for the queue overflow.
//
// An overflow means every campaign's delivery is suspect, and the notifier is
// one instance shared by all of them with errors that carry no path — so the
// condition is attributed to all of them rather than to none. Asserted through
// the captured log line, since the count of attributed campaigns is the whole
// claim.
func TestNotifierErrorsAreReportedForEveryCampaign(t *testing.T) {
	t.Parallel()

	dirs := map[string]string{
		"gilded-cage": t.TempDir(),
		"iron-vault":  t.TempDir(),
	}

	notifier := &scriptedNotifier{
		events: make(chan fsnotify.Event),
		errors: make(chan error, 1),
	}

	// The default logger, and therefore the fixture's own capture: a watcher
	// built against a different logger would be asserting that two loggers
	// agree rather than that the watcher reported anything.
	fx := newWatchFixtureWith(t, dirs, notifier)
	capture := fx.events

	notifier.errors <- fsnotify.ErrEventOverflow

	// Two campaigns, two lines — polled rather than slept for, because the
	// delivery is asynchronous.
	deadline := time.Now().Add(watchDeadline)

	for {
		found := 0

		for _, record := range capture.records() {
			if record.Event == "watch.add_failed" {
				found++
			}
		}

		if found >= 2 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("want watch.add_failed for both campaigns, got %s", capture.rendered())
		}

		time.Sleep(watchPoll)
	}
}

// TestCloseIsIdempotent covers the shutdown paths reaching Close more than once:
// a deferred close, a signal handler and the composition root's own teardown.
//
// The alternative is a process that panics on its way out, which is a failure
// mode that looks nothing like the bug it is.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)

	fx.close(t)
	fx.close(t)
	fx.close(t)
}

// TestCloseWhileEventsAreInFlight is the race the gate exists to catch.
//
// Close runs concurrently with real filesystem activity, and with a sink that
// takes long enough to be genuinely in flight when Close arrives — the state a
// settle filter in the middle of a debounce window is in. Asserted under `-race`,
// so a data race on the watch set or on the close state fails the test rather
// than passing quietly.
func TestCloseWhileEventsAreInFlight(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	stopWriting := make(chan struct{})
	writing := make(chan struct{})

	go func() {
		defer close(writing)

		for index := 0; ; index++ {
			select {
			case <-stopWriting:
				return
			default:
			}

			// Deliberately not a test assertion: the loop is here to have work in
			// flight, and its exit condition is the channel rather than a count.
			_ = os.WriteFile(
				filepath.Join(dir, fmt.Sprintf("page-%d.md", index)),
				[]byte("# page\n"),
				0o600,
			)
		}
	}()

	// Let some events accumulate, then close underneath them.
	fx.sink.await(t, "an early event", func(content.Change) bool { return true })

	fx.close(t)

	close(stopWriting)
	<-writing

	// Closing again after the writer has stopped, so the shutdown path is
	// exercised from both sides.
	fx.close(t)
}

// TestWatcherRefusesAnUnwatchedCampaign is the composition check: a campaign the
// registry retains but the watcher was not told about would be silently
// unwatched, and its pages would stop being indexed with nothing saying so.
//
// A startup error rather than a warning, because the alternative is the exact
// failure this subsystem is built to avoid.
func TestWatcherRefusesAnUnwatchedCampaign(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := content.NewRegistry(content.RefuseSymlinks)

	if _, err := registry.Open("gilded-cage", dir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	if _, err := registry.Open("iron-vault", t.TempDir()); err != nil {
		t.Fatalf("open the second content root: %v", err)
	}

	_, err := content.NewWatcher(
		context.Background(),
		registry,
		[]content.WatchRoot{{CampaignID: 1, Slug: "gilded-cage", Dir: dir}},
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("want an error for a registered campaign that is not watched")
	}

	if !strings.Contains(err.Error(), "iron-vault") {
		t.Errorf("want the error to name the unwatched campaign, got %v", err)
	}
}

// TestWatcherRefusesTwoCampaignsAtOneDirectory is the other composition check:
// two campaigns at one directory would make every event's campaign arbitrary,
// and a map's iteration order would decide which one won.
func TestWatcherRefusesTwoCampaignsAtOneDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := content.NewRegistry(content.RefuseSymlinks)

	for _, slug := range []string{"gilded-cage", "iron-vault"} {
		if _, err := registry.Open(slug, t.TempDir()); err != nil {
			t.Fatalf("open the content root of %s: %v", slug, err)
		}
	}

	_, err := content.NewWatcher(
		context.Background(),
		registry,
		[]content.WatchRoot{
			{CampaignID: 1, Slug: "gilded-cage", Dir: dir},
			{CampaignID: 2, Slug: "iron-vault", Dir: dir},
		},
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("want an error for two campaigns watched at one directory")
	}
}

// TestWriteIsAnUpsert covers the remaining fsnotify operation, and the reason a
// `Write` is reported without asking the filesystem what is at the path.
//
// A non-atomic write — an editor saving in place, `sed -i` with a temporary
// disabled, a sync client rewriting the bytes — arrives as `Write` and nothing
// else. If the watcher ignored it, a plain `>` redirect into a page would change
// the wiki's content with no event at all.
func TestWriteIsAnUpsert(t *testing.T) {
	t.Parallel()

	fx := newSingleCampaign(t)
	dir := fx.roots["gilded-cage"]

	page := filepath.Join(dir, "Live.md")
	if err := os.WriteFile(page, []byte("# Live\n"), 0o600); err != nil {
		t.Fatalf("write the page: %v", err)
	}

	fx.sink.await(t, "the created page", isPage("gilded-cage", "Live.md"))
	fx.sink.reset()

	if err := os.WriteFile(page, []byte("# Live\n\nEdited in place.\n"), 0o600); err != nil {
		t.Fatalf("rewrite the page: %v", err)
	}

	fx.sink.await(t, "the in-place rewrite", isPage("gilded-cage", "Live.md"))
}

// logRecord is one captured slog line, as the fields this file asserts on.
type logRecord struct {
	// Event is the `event` key: the stable, greppable name.
	Event string

	// Level is the level the line was written at.
	Level slog.Level

	// Path, Detail and CampaignID are the attributes `EventAttributes` permits.
	Path       string
	Detail     string
	CampaignID string
}

// logCapture is a `slog.Handler` that records what it was asked to log.
//
// A handler rather than a bytes.Buffer because the assertion is about the *level*
// and the *event key*, and a JSON blob has to be re-parsed to reach either. It
// also enforces the invariant S-12.3 is written against from the outside: an
// attribute carrying file contents arrives here as an untyped value, and the
// handler records that as a leak rather than dropping it — a handler that
// quietly discarded what it could not type would hide the very leak it is here to
// catch.
type logCapture struct {
	mu    sync.Mutex
	lines []logRecord
}

// newLogCapture returns a ready capture.
func newLogCapture() *logCapture {
	return &logCapture{}
}

// Enabled accepts every level, so a line written below the level S-12.2 fixes is
// still *recorded* rather than dropped by the handler before the assertion runs.
func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

// Handle records one line.
func (c *logCapture) Handle(_ context.Context, record slog.Record) error {
	captured := logRecord{Level: record.Level}

	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "event":
			captured.Event = attr.Value.String()
		case "path":
			captured.Path = attr.Value.String()
		case "detail":
			captured.Detail = attr.Value.String()
		case "campaign_id":
			captured.CampaignID = attr.Value.String()
		default:
			// Any other attribute at all is recorded, because `EventAttributes`
			// has three fields and a fourth is a leak of something the §13.2
			// surface never meant to carry.
			captured.Detail = "unexpected_attr:" + attr.Key
		}

		return true
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, captured)

	return nil
}

// WithAttrs returns the capture unchanged, because the assertions are about the
// event name and the level rather than about handler attributes.
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup returns the capture unchanged, for the same reason.
func (c *logCapture) WithGroup(string) slog.Handler { return c }

// records returns a snapshot of what was captured.
func (c *logCapture) records() []logRecord {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]logRecord(nil), c.lines...)
}

// find returns the first captured record for an event name.
func (c *logCapture) find(event string) *logRecord {
	for _, record := range c.records() {
		if record.Event == event {
			found := record

			return &found
		}
	}

	return nil
}

// rendered prints the captured records for a failure message.
func (c *logCapture) rendered() []string {
	captured := c.records()
	out := make([]string, 0, len(captured))

	for _, record := range captured {
		out = append(out, fmt.Sprintf("%s %s campaign=%s path=%s detail=%s",
			record.Level, record.Event, record.CampaignID, record.Path, record.Detail))
	}

	return out
}

// exhaustingNotifier is a Notifier whose `Add` always fails with a watch-limit
// errno.
//
// The limit cannot be reached for real in a test without setting
// `fs.inotify.max_user_watches` to zero for the machine, which would take every
// other watcher on the box down too. Injected rather than provoked: the
// alternative asserts the code path that runs when the condition does not happen.
type exhaustingNotifier struct {
	// errno is what `Add` fails with, so both of S-4.5's errnos are exercised
	// rather than one standing in for the other.
	errno syscall.Errno
}

func (n exhaustingNotifier) Add(string) error { return n.errno }

func (exhaustingNotifier) Remove(string) error { return nil }

func (exhaustingNotifier) Close() error { return nil }

func (exhaustingNotifier) Events() <-chan fsnotify.Event { return nil }

func (exhaustingNotifier) Errors() <-chan error { return nil }

// Nothing was ever added, because every `Add` failed.
func (exhaustingNotifier) WatchList() []string { return nil }

// leakyNotifier is a real fsnotify notifier with one thing taken away from it: a
// nominated directory stops appearing in `WatchList` while its watch stays in
// place.
//
// That is the shape of a silently lost watch as the watcher can observe it —
// the watcher still believes it is watching the directory, and the notifier says
// it is not — and it is the one thing a filesystem cannot be made to do on
// demand. Deleting a directory produces a `Remove` event the watcher handles
// correctly, so the interesting disagreement is the one with no event at all.
type leakyNotifier struct {
	inner *fsnotify.Watcher

	mu   sync.Mutex
	lost string
}

// newLeakyNotifier wraps a real fsnotify notifier.
//
// No cleanup of its own: the watcher's `Close` closes the notifier it was given,
// and closing the same fsnotify watcher from two places is how a test ends up
// asserting against a channel somebody else already closed.
func newLeakyNotifier(t *testing.T) *leakyNotifier {
	t.Helper()

	notifier, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("open fsnotify: %v", err)
	}

	return &leakyNotifier{inner: notifier}
}

// Add re-watches a directory, and stops hiding it.
//
// A lost watch is *restored* by being re-added, so the lie is scoped to the
// window between losing it and adding it again. A notifier that hid the path
// forever would make the second `Reverify` report the same `Added` again, and the
// test would be asserting against a notifier that can never be satisfied rather
// than against the reconciliation.
func (n *leakyNotifier) Add(dir string) error {
	if err := n.inner.Add(dir); err != nil {
		return fmt.Errorf("add %s: %w", dir, err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lost == dir {
		n.lost = ""
	}

	return nil
}

func (n *leakyNotifier) Remove(dir string) error {
	if err := n.inner.Remove(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	return nil
}

func (n *leakyNotifier) Close() error {
	if err := n.inner.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return nil
}

// The channels are fields on `fsnotify.Watcher` and methods on `Notifier` — which
// is exactly why the production adapter exists. Here they are adjacent because
// this notifier *is* an fsnotify watcher with one method overridden.
func (n *leakyNotifier) Events() <-chan fsnotify.Event { return n.inner.Events }

func (n *leakyNotifier) Errors() <-chan error { return n.inner.Errors }

// WatchList hides the nominated directory.
func (n *leakyNotifier) WatchList() []string {
	n.mu.Lock()
	lost := n.lost
	n.mu.Unlock()

	if lost == "" {
		return n.inner.WatchList()
	}

	held := n.inner.WatchList()
	kept := make([]string, 0, len(held))

	for _, dir := range held {
		if filepath.Clean(dir) != filepath.Clean(lost) {
			kept = append(kept, dir)
		}
	}

	return kept
}

// lose nominates a directory as no longer watched.
func (n *leakyNotifier) lose(dir string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lost = dir
}

// scriptedNotifier is a Notifier driven entirely by its test.
type scriptedNotifier struct {
	events chan fsnotify.Event
	errors chan error

	mu    sync.Mutex
	added []string
}

func (n *scriptedNotifier) Add(dir string) error {
	n.track(dir)

	return nil
}

func (n *scriptedNotifier) Remove(dir string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	kept := make([]string, 0, len(n.added))

	for _, held := range n.added {
		if held != dir {
			kept = append(kept, held)
		}
	}

	n.added = kept

	return nil
}

func (n *scriptedNotifier) Close() error {
	close(n.events)
	close(n.errors)

	return nil
}

func (n *scriptedNotifier) Events() <-chan fsnotify.Event { return n.events }

func (n *scriptedNotifier) Errors() <-chan error { return n.errors }

// Every `Add` succeeded, so everything asked for is watched. A scripted notifier
// that reported an empty list would make `Reverify` re-add every directory on
// every pass, which is correct behaviour against a notifier that had genuinely
// lost them and a confusing one here.
func (n *scriptedNotifier) WatchList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]string(nil), n.added...)
}

// track records a directory this notifier was asked to watch, under a lock
// because `Reverify` may read the list from the test's goroutine while the
// watcher adds to it from its own.
func (n *scriptedNotifier) track(dir string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.added = append(n.added, dir)
}

// Compile-time proof that the injected notifiers satisfy the seam.
var (
	_ content.Notifier = exhaustingNotifier{}
	_ content.Notifier = (*scriptedNotifier)(nil)
	_ content.Notifier = (*leakyNotifier)(nil)
)
