package e2e_test

// The fixture for the demo-vault suite: the committed artefact, the binary that
// installs it, and the running instance they are driven through.
//
// # What this is, and why it runs the real binary
//
// `play_test.go` sets the pattern this file extends — no Node and no browser in
// `make check`, deliberately, per the toolchain note in `AGENTS.md`. This is
// still an ordinary HTTP client against a real server; what changed is *whose*
// server, and the answer is the shipped one.
//
// **The alternative was rejected, and the rejection is why this file exists in
// this shape.** Everything the claims below are about is decided by `runServer` —
// which gameplay system this build resolves, which page kinds a renderer honours,
// which roll notation a campaign's roller offers, which campaigns a boot pass
// reports as missing a system — and every one of those decisions is made by an
// **unexported** helper in `cmd/server`, which is `package main`.
// `newWikiRoute`, `pageLister`, `newRealtimePlane`, `newPlayRoute`, `instanceView`
// and `editorRenderers` cannot be reached from here at all. Building a router in
// this package would mean writing a couple of hundred lines that re-implement the
// composition root: a second door into tenancy, a second derivation of the
// page-kind registry, and a second answer to "what does this binary resolve
// under". `AGENTS.md`'s own rule is that a second copy is the defect, and §10.5's
// chain is documented precisely so that *one* file holds every registration.
//
// So this file compiles `./cmd/server` and runs it, and the composition root is
// then literally the composition root.
//
// # What that buys, concretely, and it is not only convenience
//
// Three of the four claims could not be stated at all otherwise:
//
//   - **`plugin.missing` on the boot log.** `reportMissingSystems` is unexported
//     and `package main`, called once from `runServer`. It is the only thing that
//     puts "campaign X names system Y, which this build does not resolve" in a
//     log an operator reads, and it runs **before the listener opens**, so the
//     answer is on stdout before a first request is possible. A test in this
//     package cannot call it; a test in `package main` cannot load the demo vault
//     without re-implementing `runServer`. A subprocess reads it the way an
//     operator does.
//   - **The registry.** §10.5's chain decides which kinds exist, and the demo gate
//     already works around not being able to reach it — see
//     `demoKindsForBuild`, which derives the union over `overlays.IDs()` because
//     "`cmd/server` is `package main`, so no copy of it is even possible". Here
//     the product's own registration is the thing under test, and this suite's own
//     registry is only the *expectation*.
//   - **Wiring no component test can see.** The instance view, the play route's
//     `Systems` and `Snapshot`, the roller's `Systems` — each is a field the
//     composition root either sets or does not, and the served document is the
//     only place the difference is visible. This suite found three of them unset
//     on its first run; the report names them.
//
// # How the artefact is obtained, and why it is copied
//
// **`demo-vault/` is copied into a temporary directory and the copy is what is
// seeded and served.** Not optional, and not tidiness:
//
//   - `demo.Seed`'s `checkVault` `chmod 0700`s every campaign directory. That is
//     the right thing for a released artefact to arrive as, and it is a write to
//     the committed tree.
//   - `secretReconciler` is the content pipeline's last sink and rewrites a
//     `[!secret]` marker byte on disk when the ledger and the file disagree. A
//     suite serving the committed tree could change it.
//   - a suite that mutated the vault for its own fixtures would leave the tree
//     mutated for the next `go test` and for the next reader of the repository.
//
// # The database is not the vault
//
// `Options.Root` and `SEMIPLANE_DATABASE_URL` are independent, and the distinction
// is worth repeating here because getting it wrong is quiet rather than loud. A
// `SEMIPLANE_DATABASE_URL` left at its default of `file:semiplane.db` finds an
// instance an earlier run seeded, `demo seed` refuses with `ErrAlreadySeeded`, and
// every "a fresh instance" assertion below quietly becomes a second-seed
// assertion. Each instance therefore gets a database file in its own temporary
// directory, and `seedDemo` checks the seed's own report for every slug and
// account the artefact declares — so "fresh" is a claim and not an assumption.
//
// # The instance is a singleton, and the boot is not in TestMain
//
// One instance serves every shared claim, because booting it costs a build, a
// seed, a migration run and three watcher subsystems — and because the claims must
// be about **one** instance: "the boot log names forgotten-realm" and "the wiki
// answers 200" are only the same observation if they are the same process.
//
// `TestMain` does not build it; a `sync.OnceValues` does. A `go test -run` on a
// pattern that needs no instance therefore starts no server, which matters because
// `make a11y` and `make vendor-check` both drive `go test -run` with patterns.
// `TestMain` exists only to remove the two directories that outlive any one test:
// the compiled binary and the singleton's vault copy.
//
// # Everything here is a real request, and everything is read from a parsed tree
//
// No handler is called directly and no component is composed by hand. A claim about
// "the page the server serves" that skipped the socket would be a claim about
// `httptest.NewRecorder` and a middleware chain, which is what phase 9's other layers
// already test. And no claim reads the response as text: `demo_document_test.go`
// holds the fetched page and the walks over it, and this file stops at the process.
//
// # The four claims, and where each one lives
//
//   - `demo_kinds_test.go` -- a rendered page per registered kind;
//   - `demo_secrets_test.go` -- both `[!secret]` states, as the Game Master and as a
//     player, and the refusal to a player as omission rather than hiding;
//   - `demo_visibility_test.go` -- an anonymous read of the public campaign and a 404
//     for a private one; and
//   - `demo_degraded_test.go` -- the campaign whose gameplay system this build does
//     not register.
//
// Three of those files also carry a test that records a **defect this suite found**,
// written so it cannot rot away and cannot be mistaken for intent. They are named in
// their own headers and none of them is a claim the product makes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The demo accounts, as the artefact's manifest declares them. They are read from
// nothing: this suite seeds the committed manifest unmodified, so these are the
// two names `demo.manifest.yml` lists and there is no third place for them to
// drift in.
//
// **Neither is an instance administrator** — D14, and the seed writes
// `IsAdmin: false` with no key that could change it — which is why the GM reads
// all three campaigns and the player reads exactly one.
const (
	demoGM     = "demo-gm"
	demoPlayer = "demo-player"
)

// demoPassword is the credential `demo seed --password` is given.
//
// A literal rather than a drawn one because a test that cannot sign in cannot make
// any of its claims, and a drawn password would have to reach the test through a
// channel that exists only to carry it. It is a credential for a throwaway
// instance on a loopback port with a database in a temporary directory, and it
// reaches no log line: `demo seed` prints a password only when it drew one itself.
const demoPassword = "e2e-fixture-password"

// The three slugs and the visibility each one carries in
// `demo-vault/demo.manifest.yml`.
//
// **The visibilities are load-bearing and are named beside the slugs for that
// reason.** `public-post` is `public`, which is what makes an anonymous read
// possible; `greyhaven` and `forgotten-realm` are `private`, which is what makes
// their 404s an access decision rather than a missing route. A suite holding a
// slug without its visibility would not notice a manifest that changed one, and
// the third claim is about exactly that difference.
const (
	// publicSlug is the campaign whose wiki a reader with no account may open.
	publicSlug = "public-post"

	// showcaseSlug is the showcase: private, every kind demonstrated, and the one
	// carrying the seeded tabletop.
	showcaseSlug = "greyhaven"

	// degradedSlug names a gameplay system this build does not register. Its pages
	// serve and its game refuses.
	degradedSlug = "forgotten-realm"

	// degradedSystemID is the id `forgotten-realm` declares. It is why the manifest
	// says `make demo-check` must turn red if a Pathfinder plugin is ever
	// registered, and `demo_degraded_test.go` depends on this build resolving
	// nothing under it.
	degradedSystemID = "pathfinder-2e"
)

// The budgets, all named so a failure says which one it hit.
//
// `demoBuildBudget` is the only one ever close: a cold `go build` of the binary
// compiles every dependency in the module. On a warm cache — which is what
// `make check` hands it, because `check` runs `build` before `test` — it is a
// link and takes about a second. The figure is for the cold case, so a fresh
// `go test ./internal/web/e2e` is not killed by it.
const (
	demoBuildBudget    = 5 * time.Minute
	demoStartupBudget  = 90 * time.Second
	demoShutdownBudget = 30 * time.Second
	demoProbeInterval  = 20 * time.Millisecond
)

// demoBoot is one instance of the shipped server over a copy of the committed
// demo vault: the base URL, three readers, the vault copy, and the boot log.
type demoBoot struct {
	// base is the root every request is made against.
	base string

	// gm, player and anon are three independent readers, each with its own jar.
	//
	// **Three clients rather than one client with a header a test sets**, because
	// "the same URL answered 200 to the GM and 404 to a stranger" is a claim about
	// two *independent* requests. Each jar holds at most one session and there is
	// no way to reach another's.
	gm     *http.Client
	player *http.Client
	anon   *http.Client

	// vault is the copy of `demo-vault` this instance serves, so a test can read a
	// page's own source — which is where a secret's text and a page's declared kind
	// come from. Never the committed tree: see the header.
	vault demoVaultDir

	// logPath is the child's **stdout**, which is where `runServer` writes its JSON
	// lines. A file and not a pipe: the log grows with every request, a pipe nobody
	// drains fills at 64KiB and stops the server, and a file can be read at
	// whichever moment a claim needs it.
	logPath string

	// stop ends the server. Idempotent, because a per-test fixture's `t.Cleanup` and
	// `TestMain` both reach it.
	stop func()
}

// demoVaultDir is a copy of the committed artefact.
type demoVaultDir struct {
	root string
}

// page reads one page of the copy, by its vault-relative path.
func (v demoVaultDir) page(t *testing.T, rel string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read the copied vault's page %s: %v", rel, err)
	}

	return string(raw)
}

// write writes one page into the copy, creating its directory.
//
// For the fixtures that plant content before the seed runs. The mode matches what
// `campaignroots` expects, and the seed tightens the directory itself.
func (v demoVaultDir) write(t *testing.T, rel, body string) {
	t.Helper()

	target := filepath.Join(v.root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("create the directory of %s: %v", rel, err)
	}

	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s into the vault copy: %v", rel, err)
	}
}

// demoLogLine is one line of the child's stdout, decoded.
//
// **Decoded rather than substring-matched**, for the reason every other audit in
// this repository parses: `plugin.missing` is a *value* in a JSON object, and a
// line carrying the campaign's name inside a `detail` sentence is not a boot line
// naming it. A walk that searched the text would match `"campaign":
// "forgotten-realm"` inside a line whose `msg` said something else.
type demoLogLine struct {
	Msg      string `json:"msg"`
	Level    string `json:"level"`
	Campaign string `json:"campaign"`
	System   string `json:"system"`
	Kinds    int    `json:"kinds"`
	Detail   string `json:"detail"`
}

// bootLog reads every line the server has written so far, in order.
func (b *demoBoot) bootLog(t *testing.T) []demoLogLine {
	t.Helper()

	raw, err := os.ReadFile(b.logPath)
	if err != nil {
		t.Fatalf("read the server's log at %s: %v", b.logPath, err)
	}

	var lines []demoLogLine

	for text := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(text) == "" {
			continue
		}

		var line demoLogLine

		if err := json.Unmarshal([]byte(text), &line); err != nil {
			// An unparseable line is a finding and not a skip. `runServer` writes a
			// JSON handler and nothing else, so an unparseable line means something
			// wrote to the child's stdout this suite does not understand, and a suite
			// that skipped it would report green about a log it could not read.
			t.Fatalf("the server wrote a line to stdout that is not JSON: %q: %v", text, err)
		}

		lines = append(lines, line)
	}

	return lines
}

// logCarrying returns the lines whose `msg` is this one, in order.
func (b *demoBoot) logCarrying(t *testing.T, msg string) []demoLogLine {
	t.Helper()

	var found []demoLogLine

	for _, line := range b.bootLog(t) {
		if line.Msg == msg {
			found = append(found, line)
		}
	}

	return found
}

// rawLog reads the child's output verbatim, for a failure message.
func (b *demoBoot) rawLog() string {
	raw, err := os.ReadFile(b.logPath)
	if err != nil {
		return "(the log file could not be read: " + err.Error() + ")"
	}

	return string(raw)
}

// The shared instance, and the binary it runs.
var (
	demoBinaryOnce = sync.OnceValues(buildDemoBinary)
	demoBootOnce   = sync.OnceValues(startSharedDemo)

	// demoSharedWork is the singleton's temporary directory. It has to outlive
	// whichever test first asked for the instance, so it is not `t.TempDir()`:
	// that is removed when that test ends and every later test would find a vault
	// deleted underneath a running server.
	demoSharedWork string

	// demoSharedBoot is the singleton itself, once one has been started.
	//
	// **Held so `TestMain` can stop it.** `os.Exit` does not run deferred work and does
	// not reach a child process, so a `TestMain` that only removed the directories
	// would leave a `semiplane serve` running for the life of the machine -- one per
	// `go test` invocation, each holding a database file and a watcher.
	demoSharedBoot *demoBoot
)

// TestMain removes the two directories that outlive any single test.
//
// Registered as cleanup rather than left to `t.TempDir` because both are created
// by `sync.OnceValues` and no one test's lifetime contains them. Everything else
// this package creates lives in a `t.TempDir` and is removed by the testing
// package.
func TestMain(m *testing.M) {
	code := m.Run()

	// **Stop the child before removing its directory.** `os.Exit` runs neither a
	// `defer` nor anything a test registered, so this is the only place the shared
	// server is ended; and removing the directory out from under a running
	// `semiplane serve` would leave it holding descriptors to paths that no longer
	// exist. `stop` is idempotent, so a fixture that already stopped it costs
	// nothing.
	if demoSharedBoot != nil {
		demoSharedBoot.stop()
	}

	os.RemoveAll(demoSharedWork)
	os.RemoveAll(demoBinaryDir)

	os.Exit(code)
}

// demoBinaryDir is where the compiled binary lives.
var demoBinaryDir string

// sharedDemo returns the package's one seeded instance, starting it on first use.
func sharedDemo(t *testing.T) *demoBoot {
	t.Helper()

	boot, err := demoBootOnce()
	if err != nil {
		t.Fatalf("start the seeded demo instance: %v", err)
	}

	return boot
}

// startSharedDemo is the singleton's boot.
//
// `plant` is nil: the shared instance serves the committed artefact as committed,
// because every claim it carries is a claim about what ships. The nested-callout
// fixture needs a mutated copy and gets its own instance.
func startSharedDemo() (*demoBoot, error) {
	work, err := os.MkdirTemp("", "semiplane-e2e-instance-")
	if err != nil {
		return nil, fmt.Errorf("create a directory for the seeded instance: %w", err)
	}

	demoSharedWork = work

	boot, err := startDemoInstance(work, nil)
	if err != nil {
		// Only on the failure path: on success `TestMain` removes it, and removing it
		// here would delete a vault a running server is serving.
		os.RemoveAll(work)
		demoSharedWork = ""

		return nil, err
	}

	demoSharedBoot = boot

	return boot, nil
}

// fixtureDemo starts a **second** instance over its own copy of the vault, which
// `plant` may modify before the seed runs.
//
// Its own instance and not a mutation of the shared one for two reasons: the
// watcher would have to notice the change and reindex it, which is a race this
// suite has no reason to enter; and a fixture that changed the shared vault would
// change the answer every other claim reads.
func fixtureDemo(t *testing.T, plant func(vault demoVaultDir) error) *demoBoot {
	t.Helper()

	work := t.TempDir()

	boot, err := startDemoInstance(work, plant)
	if err != nil {
		t.Fatalf("start a second seeded demo instance in %s: %v", work, err)
	}

	t.Cleanup(boot.stop)

	return boot
}

// startDemoInstance copies the artefact into work, seeds an instance from it,
// starts the server and waits until it answers.
//
// `plant` runs **after** the copy and **before** the seed, so a fixture plants
// content into the artefact the seed then installs rather than writing into a
// running vault and racing the watcher.
func startDemoInstance(work string, plant func(demoVaultDir) error) (*demoBoot, error) {
	binary, err := demoBinaryOnce()
	if err != nil {
		return nil, err
	}

	vault := demoVaultDir{root: filepath.Join(work, "vault")}

	// `os.CopyFS` rather than a hand-written walk, for `internal/demo`'s reason: the
	// copy must have no rule of its own that could differ from the filesystem's,
	// because a fixture that rebuilt the tree in its own image would be seeding
	// something other than the artefact.
	if err := os.CopyFS(vault.root, os.DirFS(demoVaultRoot())); err != nil {
		return nil, fmt.Errorf("copy the committed demo vault: %w", err)
	}

	if plant != nil {
		if err := plant(vault); err != nil {
			return nil, fmt.Errorf("plant a fixture into the vault copy: %w", err)
		}
	}

	database := filepath.Join(work, "instance.db")

	if err := seedDemo(binary, vault.root, database); err != nil {
		return nil, err
	}

	return serveDemo(work, binary, database, vault)
}

// buildDemoBinary compiles `./cmd/server` once for the package.
//
// `sync.OnceValues` and not a `TestMain` build so a `go test -run` pattern needing
// no server does not pay for one. `go build` rather than `go run`: the child has
// to outlive the build and have its stdout captured for its whole life, and a
// `go run` wrapper would put a second process between this suite and the boot line
// one of the claims is about.
//
// **Not `-race`.** The child is the product's own binary, built the way a release
// builds it, and the claims here are about bytes on a socket. A race build would
// multiply the build time and assert nothing this suite is for; the race gate is
// `make check`'s `go test -race ./...`, which covers this package's own code.
func buildDemoBinary() (string, error) {
	dir, err := os.MkdirTemp("", "semiplane-e2e-binary-")
	if err != nil {
		return "", fmt.Errorf("create a directory for the compiled binary: %w", err)
	}

	demoBinaryDir = dir

	binary := filepath.Join(dir, "semiplane")

	ctx, cancel := context.WithTimeout(context.Background(), demoBuildBudget)
	defer cancel()

	// `./cmd/server` from the module root, because this test runs in
	// `internal/web/e2e` and a relative package path would name nothing. Combined
	// output so a build failure is a build failure with its reason.
	build := exec.CommandContext(ctx, goTool(), "build", "-o", binary, "./cmd/server")
	build.Dir = moduleRoot()
	build.Env = os.Environ()

	out, err := build.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"build ./cmd/server: %w\n%s\n\n"+
				"this suite drives the demo vault through the shipped binary, so this is "+
				"`make build` failing. Note that `internal/web` embeds `static/dist`, so "+
				"`make css` and `make templ` must have run first; they are prerequisites "+
				"of `make check`",
			err, out,
		)
	}

	return binary, nil
}

// seedDemo runs `semiplane demo seed --root <copy> --password <password>` and
// checks it seeded what the artefact declares.
//
// **The database is a file inside the instance's own directory**, never
// `SEMIPLANE_DATABASE_URL`'s default: see the header on why that mistake is quiet.
func seedDemo(binary, vault, database string) error {
	ctx, cancel := context.WithTimeout(context.Background(), demoStartupBudget)
	defer cancel()

	seed := exec.CommandContext(ctx, binary, "demo", "seed",
		"--root", vault, "--password", demoPassword)
	seed.Env = demoEnv(database)

	out, err := seed.CombinedOutput()
	if err != nil {
		return fmt.Errorf("seed the demo instance from %s: %w\n%s", vault, err, out)
	}

	// The seed's report is the operator's, and checking it is what makes "a fresh
	// instance" a claim rather than an assumption. Every slug and account the
	// artefact declares has to appear.
	report := string(out)

	for _, want := range []string{
		`Registered campaign "` + showcaseSlug + `"`,
		`Registered campaign "` + publicSlug + `"`,
		`Registered campaign "` + degradedSlug + `"`,
		`Created account "` + demoGM + `"`,
		`Created account "` + demoPlayer + `"`,
	} {
		if !strings.Contains(report, want) {
			return fmt.Errorf(
				"the seed did not report %q, so this instance is not the artefact's:\n%s",
				want, report,
			)
		}
	}

	return nil
}

// demoEnv is the environment the child runs under.
//
// **Every `SEMIPLANE_*` variable that could change an answer is set explicitly**
// rather than inherited, because the claims are about a known configuration and an
// operator's shell in CI is not part of it. `SEMIPLANE_ENV` is `development` and
// not `production` because `accounts.Router.Secure` follows it and a `Secure`
// session cookie over plain HTTP is a cookie `net/http` will not send back — which
// would make every signed-in assertion fail for a reason that has nothing to do
// with the demo.
//
// `SEMIPLANE_INSTANCE_NAME` is set from the environment because the artefact's
// manifest declares an `instance:` key that **this build reads nowhere**; see the
// report. Setting it here keeps the served documents coherent without asserting a
// claim about a key that does nothing.
func demoEnv(database string) []string {
	return append(
		os.Environ(),
		"SEMIPLANE_DATABASE_URL=file:"+database,
		// Never the default `/var/lib/semiplane/vaults`: registration derives a
		// campaign's `content_root` from it, and pointing it inside the instance's own
		// directory keeps a stray registration inside the temporary tree.
		"SEMIPLANE_CONTENT_ROOT_BASE="+filepath.Dir(database),
		"SEMIPLANE_ENV=development",
		"SEMIPLANE_INSTANCE_NAME=Semiplane demo",
	)
}

// serveDemo starts `semiplane serve` on a loopback port and waits for it.
//
// **The port is chosen by binding and releasing one**, which has a race window and
// is the only way: `runServer` logs the address it was *configured* with, so a
// `127.0.0.1:0` request would be reported as `127.0.0.1:0` and the test would have
// no way to learn the port the kernel chose. The window is closed by retrying —
// and only a port lost between the release and the child's bind is retried, so a
// genuinely broken checkout still fails once and says why.
func serveDemo(work, binary, database string, vault demoVaultDir) (*demoBoot, error) {
	const attempts = 3

	var lastErr error

	for range attempts {
		port, err := freeLoopbackPort()
		if err != nil {
			return nil, err
		}

		boot, err := runServerOnce(work, binary, database, vault, port)
		if err == nil {
			return boot, nil
		}

		lastErr = err

		if !errors.Is(err, errPortTaken) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("the server could not bind a loopback port in %d attempts: %w",
		attempts, lastErr)
}

// errPortTaken is the one boot failure worth retrying.
//
// Matched on the runtime's own error text, which is the only thing available: the
// child exits with a non-zero status and a line on stderr, and a substring test
// over a *runtime* message is a different kind of compromise from one over the
// product's prose. Everything else a boot can fail at is a fact about this
// checkout and is reported with the child's whole output.
var errPortTaken = errors.New(
	"demo: the reserved loopback port was taken before the server bound it",
)

// runServerOnce is one attempt at the whole boot.
func runServerOnce(
	work, binary, database string,
	vault demoVaultDir,
	port int,
) (*demoBoot, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	logPath := filepath.Join(work, "server-"+strconv.Itoa(port)+".log")

	// **Left open and handed to the child as its own descriptor.** `exec.Cmd` passes
	// an `*os.File` through as fd 1 and fd 2 without a copying goroutine, so the
	// only requirement is that the handle be valid at `Start` — and closing it here
	// would leave a server whose every log line went to a closed descriptor. It is
	// closed in `stop`, after the child has gone.
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create the server's log at %s: %w", logPath, err)
	}

	// **Not `exec.CommandContext`.** Cancelling a context sends `SIGKILL`, and this
	// server has a deliberate graceful shutdown: event streams end, then the
	// realtime hub flushes, then the HTTP drain. A `SIGKILL` skips all three, and the
	// suite would be asserting against a binary it had killed mid-write. `SIGTERM`
	// below, with `SIGKILL` only behind a budget.
	//nolint:noctx // G204 and noctx: the ctx form sends SIGKILL on cancel, and this
	// server has a deliberate graceful shutdown. `stop` signals SIGTERM and keeps a
	// SIGKILL behind a budget. The binary is this checkout's own ./cmd/server.
	command := exec.Command(binary)
	command.Dir = work
	command.Env = append(demoEnv(database), "SEMIPLANE_ADDR="+addr)
	command.Stdout = logFile
	command.Stderr = logFile

	if err := command.Start(); err != nil {
		if closeErr := logFile.Close(); closeErr != nil {
			return nil, fmt.Errorf("close %s: %w", logPath, closeErr)
		}

		return nil, fmt.Errorf("start %s: %w", binary, err)
	}

	server := &demoProcess{command: command, log: logFile, exited: make(chan struct{})}

	go func() {
		server.failure = command.Wait()
		close(server.exited)
	}()

	boot := &demoBoot{
		base:    "http://" + addr,
		gm:      newReader(),
		player:  newReader(),
		anon:    newReader(),
		vault:   vault,
		logPath: logPath,
		stop:    server.stop,
	}

	if err := boot.awaitHealthy(server); err != nil {
		server.stop()

		if strings.Contains(boot.rawLog(), "address already in use") {
			return nil, fmt.Errorf("%w: %w", errPortTaken, err)
		}

		return nil, err
	}

	if err := boot.signIn(demoGM); err != nil {
		server.stop()

		return nil, fmt.Errorf("sign %s in: %w", demoGM, err)
	}

	if err := boot.signIn(demoPlayer); err != nil {
		server.stop()

		return nil, fmt.Errorf("sign %s in: %w", demoPlayer, err)
	}

	return boot, nil
}

// demoProcess is the running child and the two facts about it this suite reads:
// whether it is still running, and how it ended.
type demoProcess struct {
	command *exec.Cmd

	// exited is closed once `Wait` has returned, so `<-p.exited` is the test for
	// "the child is gone" and can be read any number of times. A buffered channel
	// carrying the error would have to be read exactly once, and this suite reads
	// the exit state from a probe loop and from a failure message.
	exited chan struct{}

	// failure is the child's exit status, written before `exited` is closed and
	// therefore safe to read after it. A field rather than a captured variable
	// because a variable written only from a goroutine is not a *use* to the
	// compiler, and a suite that has to defeat the compiler to keep a diagnostic
	// is a suite with a defect in its diagnostics.
	failure error

	// log is the child's stdout handle, kept open for the child's whole life and
	// closed here once it has gone.
	log *os.File

	// once makes `stop` idempotent, which it has to be: a per-test cleanup and
	// `TestMain` both reach it and a second `Wait` is a panic.
	once sync.Once
}

// stop ends the child the way the product's own shutdown expects, and then insists.
//
// **`SIGTERM` first and `SIGKILL` only behind a budget.** The server's shutdown
// order is load-bearing — event streams end, then the realtime hub flushes, then
// the HTTP drain — and `SIGKILL` skips all of it, leaving a WAL to recover and a
// state row unwritten. A child that ignores `SIGTERM` for longer than the budget
// has hung, and hanging is what the kill is for.
func (p *demoProcess) stop() {
	p.once.Do(func() {
		if signalErr := p.command.Process.Signal(syscall.SIGTERM); signalErr != nil {
			// The process has already gone, which is the state this guards against, so
			// there is nothing to do about the error.
			_ = signalErr
		}

		select {
		case <-p.exited:
		case <-time.After(demoShutdownBudget):
			if killErr := p.command.Process.Kill(); killErr != nil {
				_ = killErr
			}

			<-p.exited
		}

		if closeErr := p.log.Close(); closeErr != nil {
			// The child is gone and the log has been read; a close failure on a
			// descriptor with nothing left to write to is not a fact about the
			// product, and there is nowhere to return it to.
			_ = closeErr
		}
	})
}

// awaitHealthy waits for `/healthz`, which `runServer` can only answer after the
// startup index has run.
//
// **That ordering is why this is the readiness signal and not the log line "http
// server listening".** The listener is opened *after* `pipeline.buildIndex`, so a
// refused connection means the server has not started and a 200 means the `pages`
// table is populated — which is the boot step every `[[wikilink]]` in the vault
// depends on, and whose absence makes every reference render `data-broken`.
func (b *demoBoot) awaitHealthy(server *demoProcess) error {
	deadline := time.Now().Add(demoStartupBudget)

	var lastErr error

	for time.Now().Before(deadline) {
		select {
		case <-server.exited:
			return fmt.Errorf("the server exited before answering /healthz: %w\n%s",
				server.failure, b.rawLog())
		default:
		}

		request, err := http.NewRequestWithContext(
			context.Background(), http.MethodGet, b.base+"/healthz", http.NoBody,
		)
		if err != nil {
			return fmt.Errorf("build the /healthz request: %w", err)
		}

		response, err := b.anon.Do(request)
		if err != nil {
			lastErr = err

			time.Sleep(demoProbeInterval)

			continue
		}

		_, copyErr := io.Copy(io.Discard, response.Body)

		if closeErr := response.Body.Close(); closeErr != nil {
			return fmt.Errorf("close the /healthz response: %w", closeErr)
		}

		if copyErr == nil && response.StatusCode == http.StatusOK {
			return nil
		}

		lastErr = fmt.Errorf("GET /healthz = %d", response.StatusCode)

		time.Sleep(demoProbeInterval)
	}

	return fmt.Errorf("the server did not answer /healthz within %s: %w\n%s",
		demoStartupBudget, lastErr, b.rawLog())
}

// newReader is one reader: an `http.Client` with its own cookie jar.
//
// The jar is what makes the three readers independent, and it is the **only**
// identity mechanism in play: `identity.Authenticate` resolves a request from
// `sp_session` and from nothing else, so a suite that wanted to be a GM without
// signing in would have to forge a session, which is a different test.
func newReader() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		// `cookiejar.New` with nil options cannot fail: the standard library
		// documents the only error as a `PublicSuffixList` one and nil is not one.
		panic("cookiejar.New(nil): " + err.Error())
	}

	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

// signIn posts the credential the seed was given and requires the session cookie.
//
// **The status is checked as 200, after the redirect**, because the client follows
// it: a sign-in returning 200 without a session cookie would satisfy a status
// check and fail every later claim for no visible reason. The cookie is then
// asserted explicitly, which is the fact the later claims rest on.
func (b *demoBoot) signIn(username string) error {
	// **A throwaway client**, not `b.anon`. The response's `Set-Cookie` is installed
	// on the client that made the request, so signing in through the anonymous
	// reader would leave that jar holding the *last* account signed in — and every
	// "as a reader with no account" assertion would quietly be an assertion about
	// the player.
	signIn := newReader()

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, b.base+"/login",
		strings.NewReader(formOf(map[string]string{
			"username": username,
			"password": demoPassword,
		})),
	)
	if err != nil {
		return fmt.Errorf("build the sign-in request for %s: %w", username, err)
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := signIn.Do(request)
	if err != nil {
		return fmt.Errorf("POST /login as %s: %w", username, err)
	}
	defer response.Body.Close()

	if _, drainErr := io.Copy(io.Discard, response.Body); drainErr != nil {
		return fmt.Errorf("read the sign-in response for %s: %w", username, drainErr)
	}

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /login as %s = %d, want 200 after the redirect",
			username, response.StatusCode)
	}

	base, err := neturl.Parse(b.base + "/")
	if err != nil {
		return fmt.Errorf("parse the instance base URL %q: %w", b.base, err)
	}

	// **From the jar and not from the final response's headers.** The session cookie
	// is set on the 303 that `POST /login` answers with, and `http.Client` follows
	// that redirect, so `response` is the document after it and carries no
	// `Set-Cookie` of its own. Reading the jar is also the more faithful thing to
	// assert: it is where a browser would hold the session, and it is what the
	// reader below is actually going to send.
	for _, cookie := range signIn.Jar.Cookies(base) {
		if cookie.Name != "sp_session" || cookie.Value == "" {
			continue
		}

		b.clientFor(username).Jar.SetCookies(base, []*http.Cookie{cookie})

		return nil
	}

	return fmt.Errorf("signing %s in left no sp_session cookie in the jar", username)
}

// clientFor names the reader an account signs in as.
func (b *demoBoot) clientFor(username string) *http.Client {
	if username == demoPlayer {
		return b.player
	}

	return b.gm
}

// formOf encodes form values the way `net/http` would.
//
// `url.Values.Encode` rather than a hand-built body: the field names are a wire
// between this suite and `accounts.Router`'s `PostFormValue` calls, and a
// hand-built body would be a second spelling of it.
func formOf(fields map[string]string) string {
	values := make(neturl.Values, len(fields))

	for name, value := range fields {
		values.Set(name, value)
	}

	return values.Encode()
}

// moduleRoot is the directory holding `go.mod`, found by walking up from the
// package directory rather than by counting `..` levels.
//
// The count would break the moment this package moved, and it would break by
// pointing the build at a directory that happens to contain a `go.mod` or by
// failing for no visible reason. The walk is the same answer every time.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic("read the working directory: " + err.Error())
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			panic("no go.mod above the working directory; the suite cannot find the module")
		}

		dir = parent
	}
}

// demoVaultRoot is the committed artefact, as an absolute path.
//
// Through `moduleRoot` rather than a relative `../../../demo-vault`, so a suite
// that cannot find the artefact says so by name instead of by a `ReadFile` error
// naming a path with three `..` in it.
func demoVaultRoot() string {
	return filepath.Join(moduleRoot(), "demo-vault")
}

// goTool is the `go` command, found on `PATH` or beside the toolchain that built
// this test binary.
//
// **The two fallbacks are the load-bearing half.** `make check` sources
// `/etc/profile.d/go.sh`, so `PATH` carries `go` -- but a test binary is a
// subprocess of the test runner, and the gate's environment is not a promise about
// every environment.
//
//   - `GOROOT` from the environment is tried first, because it is a deliberate
//     answer rather than a guess; and
//   - `runtime.GOROOT` names the toolchain this test was compiled by, and its
//     `bin/go` exists whenever the compiler did.
//
// `PATH` is preferred over both so the build lands on the same toolchain the gate
// installed rather than on one a stale variable points at.
func goTool() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}

	fallbacks := []string{os.Getenv("GOROOT")}

	if compiled := runtime.GOROOT(); compiled != "" { //nolint:staticcheck // SA1019
		fallbacks = append(fallbacks, compiled)
	}

	for _, root := range fallbacks {
		if root == "" {
			continue
		}

		fallback := filepath.Join(root, "bin", "go")

		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
	}

	panic("no go command on PATH, none at $GOROOT/bin/go and none beside the " +
		"toolchain that built this test binary")
}

// freeLoopbackPort binds and releases a port on the loopback interface.
//
// `127.0.0.1` rather than `:0` on every interface: the suite has no business
// listening on a routable address, and a port another host could reach is a port
// another host could connect to.
func freeLoopbackPort() (int, error) {
	var listen net.ListenConfig

	listener, err := listen.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a loopback port: %w", err)
	}

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		if closeErr := listener.Close(); closeErr != nil {
			return 0, fmt.Errorf("close the reserved listener: %w", closeErr)
		}

		return 0, errors.New("the reserved listener is not a TCP address")
	}

	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release the reserved loopback port: %w", err)
	}

	return addr.Port, nil
}
