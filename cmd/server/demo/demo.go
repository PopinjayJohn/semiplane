// Package demo is the `semiplane demo` command: `seed`, `reset`, and the usage text
// that names them.
//
// # Why the command is here and the logic is in `internal/demo`
//
// `internal/demo` owns the manifest schema, the seed and the reset — everything that
// is a function of the manifest, the database and the registries. This package owns
// the three things that cannot be:
//
//   - **The plugin registrations.** `cmd/server`'s `registerPlugins` builds the
//     gameplay registry and the house-rule registry, and `plugins.fingerprint()` is
//     the only code in the tree that can turn an engine's `Versions()` into a
//     `ruleset_version`. `package main` cannot be imported, so the composition root
//     calls in here and hands those three values over as `Dependencies`.
//   - **The configuration and the store handle.** `config.Load` and `store.Open` are
//     the two steps every subcommand performs, and a subcommand holds the process's
//     single-instance slot for its whole run — which is why this is a subcommand and
//     not a flag on the server: the two never run at the same time, so the migration
//     runner is never applying migrations underneath a live process.
//   - **The operator's terminal.** The generated password is drawn, printed once and
//     handed to the seed, and it reaches nothing else.
//
// # The one duplicated thing, stated rather than hidden
//
// `cmd/server/admin.go` has a `parseFlags` and a `withStore`, and both are
// unexported members of `package main`, so nothing under `cmd/server/demo/` can
// reach them. Rather than duplicate the general parser wholesale, this package parses
// **the three flags this command has** — `--root`, `--password` and the help forms —
// with a loop short enough to read in one piece, and `withStore` is not duplicated at
// all: it is inlined into `Run`'s caller because it is eight lines and one of them is
// the `defer` the process needs.
//
// Extracting a shared flag grammar would mean editing `cmd/server/admin.go`, which
// this work item does not own. When somebody does own both files, the parser here is
// the thing to delete rather than the one to keep.
package demo

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/demo"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/store"
)

// Subcommand names, as constants, for the reason `cmd/server/main.go` gives about
// its own: a subcommand name repeated across a dispatch and a usage string is a name
// that eventually gets a typo in one of them, and a typo in a subcommand name is a
// command that silently does not exist.
const (
	subcommandSeed  = "seed"
	subcommandReset = "reset"
)

// UsageText is written on `demo help` and on a usage error.
//
// Every subcommand is named here or nowhere, so an operator who runs `semiplane demo`
// is never told it does not exist — which is the failure `cmd/server/main.go`'s own
// `usageText` header records for the moment `admin` was added.
const usageText = `Usage:
  semiplane demo seed  --root DIR [--password SECRET]
  semiplane demo reset --root DIR
  semiplane demo help

seed
  --root      the extracted demo artefact: the directory holding
              demo.manifest.yml and one directory per campaign. Required, and
              must be absolute.
  --password  the demo accounts' password. Drawn at random and printed once when
              omitted, which is what a scripted or CI run does not want.

reset
  --root      the same directory, read for the manifest only. Reset deletes
              database rows and never touches a file in the vault.

The demo account is a Game Master of the seeded campaigns and nothing else. It is
never an instance administrator, so it cannot register campaigns or manage users on
an instance that already has any.
`

// Dependencies is what the composition root owns and this package cannot reach.
//
// Three values, all of them the registrations `cmd/server/systems.go` performs.
// Passing them rather than importing them is the point: `package main` is not
// importable, so a `cmd/server/demo` that reached for `registerPlugins` itself would be
// a second copy of the §10.5 chain — and a copy that could register a different
// edition than the server does, which is a demo that plays by rules the server does
// not resolve.
type Dependencies struct {
	// Systems is this build's gameplay registry, consulted by
	// `demo.BuildFingerprints` to learn which system ids this build resolves.
	Systems *plugin.Registry

	// Fingerprint is what this build resolves under — `plugins.fingerprint()`, the
	// encoded four-component form. **Empty is meaningful**: a build with no gameplay
	// system resolves nothing, and every campaign is then seeded with an empty
	// `ruleset_version`, which migration 0005 defines as a real value.
	Fingerprint string

	// HouseRules is this build's compiled-in house-rule registry, and the resolver the
	// seed runs each campaign's module set through.
	HouseRules *houserules.Registry
}

// Env is the process-level inputs a run needs, so a test can drive the command without
// a terminal or a signal handler.
//
// A struct rather than three arguments because they are one decision — where this
// command reads and writes — and because `os.Stdout` appearing in a signature is how a
// test ends up asserting against the real terminal.
type Env struct {
	// Stdout carries the result the operator asked for: the seeded slugs, the
	// accounts, and the generated password. Stderr carries warnings and refusals.
	Stdout io.Writer
	Stderr io.Writer

	// Version is this binary's release, compared against the manifest's `product`.
	// Empty means the build stamps no version, and `demo.Check` says so out loud
	// rather than pretending to have compared anything.
	Version string
}

// Run dispatches `semiplane demo …`.
//
// **The manifest is read before the store is opened**, so a `--root` naming nothing is
// a refusal that never touches the database and never runs the migrations underneath
// a live process. `ctx` is the command's own context — `context.Background()`, not the
// server's signal context, for the reason `cmd/server/admin.go` gives: a subcommand is
// short and does not need a graceful drain, and inheriting a cancelled context would
// make a Ctrl-C partway through a write leave the outcome ambiguous.
func Run(ctx context.Context, args []string, deps Dependencies, env Env) error {
	if env.Stdout == nil || env.Stderr == nil {
		return errors.New("semiplane demo: no streams were given, so nothing could be reported")
	}

	if len(args) == 0 {
		return usagef("demo has no subcommands")
	}

	switch args[0] {
	case subcommandSeed:
		return runSeed(ctx, args[1:], deps, env)
	case subcommandReset:
		return runReset(ctx, args[1:], deps, env)
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usageText)

		return nil
	default:
		return usagef("unknown demo subcommand %q", args[0])
	}
}

// usageError marks a mistake in the command line rather than a fault in the system.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// IsUsage reports whether err is a mistake in the command line rather than a fault in the
// system.
//
// **Exported, and it exists because the type cannot be.** `cmd/server/main.go` decides the
// exit code and the presentation with `errors.AsType[*usageError]` against the `usageError`
// **in `package main`** — a different type from this one, so a caller cannot write the
// comparison itself. This predicate is the whole of the seam: the composition root's dispatch
// is
//
//	case subcommandDemo:
//		if err := demodemo.Run(ctx, rest, deps, env); err != nil {
//			if demodemo.IsUsage(err) {
//				return err // main prints it and exits 2
//			}
//
//			return err
//		}
//
// without it, every typo an operator makes is reported as `fatal` through `slog` and exits 1 —
// which is the wrong shape for a mistyped flag and puts operator typos in a log aggregator.
func IsUsage(err error) bool {
	var usage *usageError

	return errors.As(err, &usage)
}

// runSeed reads the manifest and seeds the instance.
func runSeed(ctx context.Context, args []string, deps Dependencies, env Env) error {
	parsed, err := parseFlags(subcommandSeed, args)
	if err != nil {
		return err
	}

	root, err := requireRoot(parsed)
	if err != nil {
		return err
	}

	manifest, err := demo.Load(root)
	if err != nil {
		return fmt.Errorf("read %s: %w", demo.ManifestFileName, err)
	}

	// Drawn here rather than inside the seed, and that is the whole reason the
	// generated password can be reported on a seed that failed half way: this package
	// holds it from before the first row is written, so no error path can lose it. The
	// seed receives a credential it is guaranteed to have, and a `Seed` that refused an
	// empty one could not lose it either.
	password, generated, err := demoPassword(parsed.password)
	if err != nil {
		return err
	}

	options, err := optionsFor(root, deps, env, password)
	if err != nil {
		return err
	}

	var result demo.Result

	seedErr := withStore(ctx, env, func(ctx context.Context, db *store.Store) error {
		options.Store = db

		seeded, seedFailed := demo.Seed(ctx, manifest, options)
		result = seeded

		if seedFailed != nil {
			return fmt.Errorf("seed the demo: %w", seedFailed)
		}

		return nil
	})

	// **Printed before the error is returned, and printed whether or not the seed
	// succeeded.** A generated password shown only on success is a password an operator
	// whose seed half-failed never learns, and the half-seeded instance they are left
	// with holds an account they cannot sign in to. This is the only place the value is
	// revealed, and it is revealed once.
	if generated {
		printGeneratedPassword(result.Password, env)
	}

	if seedErr != nil {
		return seedErr
	}

	printSeed(result, env)

	return nil
}

// runReset reads the manifest and removes what the seed created.
func runReset(ctx context.Context, args []string, deps Dependencies, env Env) error {
	parsed, err := parseFlags(subcommandReset, args)
	if err != nil {
		return err
	}

	// Refused rather than ignored. Silently accepting a flag that does nothing is how a
	// scripted run believes it set something it did not, and reset creates no account
	// and sets no credential — it deletes accounts, and the next seed draws a new one.
	if parsed.password != "" {
		return usagef(
			"demo reset takes no --password: it creates no account and sets no credential",
		)
	}

	root, err := requireRoot(parsed)
	if err != nil {
		return err
	}

	manifest, err := demo.Load(root)
	if err != nil {
		return fmt.Errorf("read %s: %w", demo.ManifestFileName, err)
	}

	// `demo.Secret("")` — a reset has no credential, and `Options.checkCredential`
	// is deliberately reached only from `Seed`, so an empty one here is the honest
	// value rather than a placeholder that has to be explained.
	options, err := optionsFor(root, deps, env, demo.Secret{})
	if err != nil {
		return err
	}

	return withStore(ctx, env, func(ctx context.Context, db *store.Store) error {
		options.Store = db

		result, resetErr := demo.Reset(ctx, manifest, options)
		if resetErr != nil {
			return fmt.Errorf("reset the demo: %w", resetErr)
		}

		printReset(result, env)

		return nil
	})
}

// demoPassword returns the credential and whether it was drawn rather than supplied.
func demoPassword(supplied string) (demo.Secret, bool, error) {
	if supplied != "" {
		return demo.NewSecret(supplied), false, nil
	}

	// `crypto/rand.Reader` passed explicitly rather than left to the generator's own
	// default, because this is the one line a reader should be able to see: a demo
	// credential comes from the system pool, and nothing in this repository decides that
	// by accident. See `demo.GeneratePassword` for why this is the deliberate exception
	// to the repository's usual injectable reader.
	password, err := demo.GeneratePassword(rand.Reader)
	if err != nil {
		return demo.Secret{}, false, fmt.Errorf("draw the demo password: %w", err)
	}

	return password, true, nil
}

// optionsFor builds the seed's and reset's options from the registries the composition
// root registered.
func optionsFor(
	root string,
	deps Dependencies,
	env Env,
	password demo.Secret,
) (demo.Options, error) {
	fingerprints, err := demo.BuildFingerprints(deps.Systems, deps.Fingerprint)
	if err != nil {
		return demo.Options{}, fmt.Errorf("resolve this build's ruleset fingerprints: %w", err)
	}

	return demo.Options{
		Root:         root,
		Fingerprints: fingerprints,
		HouseRules:   deps.HouseRules,
		Version:      env.Version,
		Password:     password,
	}, nil
}

// printSeed writes what the seed did.
func printSeed(result demo.Result, env Env) {
	if result.Warning != "" {
		fmt.Fprintln(env.Stderr, result.Warning)
	}

	for _, slug := range result.Campaigns {
		fmt.Fprintf(env.Stdout, "Registered campaign %q.\n", slug)
	}

	for _, username := range result.Accounts {
		fmt.Fprintf(env.Stdout, "Created account %q.\n", username)
	}

	if len(result.Unresolved) > 0 {
		// Reported, and the sentence is the one `demo.Result.Unresolved` exists for: a
		// campaign naming a system this build does not register serves its wiki and
		// refuses its game, which is §10.8's required behaviour and the reason
		// `forgotten-realm` is in the shipped demo.
		fmt.Fprintf(env.Stderr,
			"semiplane: no gameplay system is registered for %s, so those campaigns serve their "+
				"wiki and refuse to start a game. That is the demo demonstrating the degraded "+
				"path, not a fault.\n",
			strings.Join(result.Unresolved, ", "),
		)
	}

	fmt.Fprintln(env.Stdout, "\nSeeded. Start the server and sign in with a demo account.")
}

// printGeneratedPassword writes the drawn credential, once.
//
// **Only a drawn one.** A password the operator supplied with `--password` is one they
// already have, and it has just been through their shell history; echoing it back would
// put it in a terminal scrollback a second time and in any `script` capture of the
// session. A drawn one has no other copy in the world, so it is printed here and goes
// nowhere else — never logged, never put in an error, and never written to the database
// in anything but its PBKDF2 hash.
func printGeneratedPassword(password demo.Secret, env Env) {
	fmt.Fprintf(env.Stdout,
		"\nDemo account password, printed once and stored nowhere in plain text:\n  %s\n\n",
		password.Reveal(),
	)
}

// printReset writes what the reset removed.
func printReset(result demo.ResetResult, env Env) {
	if result.Warning != "" {
		fmt.Fprintln(env.Stderr, result.Warning)
	}

	for _, slug := range result.Campaigns {
		fmt.Fprintf(env.Stdout, "Removed the campaign %q and its state.\n", slug)
	}

	for _, username := range result.Accounts {
		fmt.Fprintf(env.Stdout, "Removed the account %q.\n", username)
	}

	for _, username := range result.Absent {
		fmt.Fprintf(env.Stdout, "There was no account %q to remove.\n", username)
	}

	if len(result.Campaigns) == 0 && len(result.Accounts) == 0 {
		fmt.Fprintln(
			env.Stdout,
			"Nothing to remove: this instance was not seeded from this artefact.",
		)

		return
	}

	fmt.Fprintln(env.Stdout, "\nThe vault is untouched. Extract the artefact again to start over.")
}

// requireRoot reads `--root` and refuses anything the seed could not use.
func requireRoot(parsed parsedFlags) (string, error) {
	root := strings.TrimSpace(parsed.root)
	if root == "" {
		return "", usagef(
			"demo %s needs --root, the extracted artefact's directory",
			parsed.subcommand,
		)
	}

	if !strings.HasPrefix(root, "/") {
		// Not `filepath.IsAbs`, and the reason is worth stating: this check happens
		// before any file is read, so it must not touch the filesystem — and the only
		// filesystem-independent absolute-path test on a string is a leading separator.
		// `config.contentRootBaseFromEnv` makes the same call for the same reason, and
		// `demo.Options.check` refuses it again once the value is in hand.
		return "", usagef(
			"demo %s: --root %q is not absolute; every campaign's content root is derived from "+
				"it and stored absolute, so a relative root would name a different vault after "+
				"a restart from a different directory",
			parsed.subcommand, root,
		)
	}

	return root, nil
}

// parsedFlags is what this command's own grammar produces.
type parsedFlags struct {
	subcommand string
	root       string
	password   string
}

// parseFlags reads `--name value` pairs and `--name=value` forms.
//
// **A three-flag grammar, not a copy of `cmd/server/admin.go`'s parser.** The package
// header says why it could not be the same function; this is what that costs, and the
// cost is kept small by refusing rather than guessing. An unknown flag is an error and
// not a value, because a silently ignored `--pasword` on a seed is an instance whose
// accounts all have a password the operator did not choose and does not know.
func parseFlags(subcommand string, args []string) (parsedFlags, error) {
	parsed := parsedFlags{subcommand: subcommand}

	for index := 0; index < len(args); index++ {
		arg := args[index]

		if !strings.HasPrefix(arg, "--") {
			return parsedFlags{}, usagef("unexpected argument %q; every demo flag is --name", arg)
		}

		name, inline, hasInline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")

		var value string

		switch name {
		case "root":
			var err error

			if value, err = flagValue(name, args, &index, inline, hasInline); err != nil {
				return parsedFlags{}, err
			}

			parsed.root = value
		case "password":
			var err error

			if value, err = flagValue(name, args, &index, inline, hasInline); err != nil {
				return parsedFlags{}, err
			}

			parsed.password = value
		default:
			return parsedFlags{}, usagef("unknown flag --%s", name)
		}
	}

	return parsed, nil
}

// flagValue reads one flag's value, from an inline `=value` or the next argument.
//
// The index is advanced through a pointer so the caller's loop moves past a consumed
// value; not doing that is how `--root --password x` silently makes the root
// `--password`.
func flagValue(
	name string,
	args []string,
	index *int,
	inline string,
	hasInline bool,
) (string, error) {
	if hasInline {
		if inline == "" {
			return "", usagef("flag --%s needs a value", name)
		}

		return inline, nil
	}

	if *index+1 >= len(args) {
		return "", usagef("flag --%s needs a value", name)
	}

	*index++

	return args[*index], nil
}

// withStore opens the store for the command, runs fn, and closes it.
//
// Inlined rather than shared with `cmd/server/admin.go`'s `withStore`, which is an
// unexported member of `package main`. It holds the process's single-instance slot for
// the whole run, which is the feature: a subcommand cannot be issued against a live
// server, so the migration runner is never applying migrations underneath one.
func withStore(
	ctx context.Context,
	env Env,
	run func(context.Context, *store.Store) error,
) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	defer func() {
		// Reported rather than returned, for the reason the store's own close is: the
		// command is on its way out and there is nobody left to return to.
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(env.Stderr, "semiplane: close store: %v\n", closeErr)
		}
	}()

	return run(ctx, db)
}
