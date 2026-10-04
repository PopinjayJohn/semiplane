package main

// `semiplane demo seed` and `semiplane demo reset`.
//
// # Why this is a function and not a case in `run`
//
// `run` dispatches three subcommands inline because two of them are `serve` and
// `admin`, both of which are about *this process becoming something*. The demo
// subcommand is the first one that is not: it opens the database, writes rows, prints a
// password once and exits, without a listener. Its wiring is therefore a named function
// rather than three more lines in `run`, and the reason it lives in its own file is that
// `runDemo` needs the plugin registry — which `registerPlugins` builds and which
// nothing else in `main.go` touches.
//
// # The password goes to stdout exactly once
//
// D14. `demo.Run` prints it; this function's only job is to hand it a real stdout
// rather than the process's, so that the command is testable and the print happens in
// one place. Nothing here logs it, and the wiring passes `os.Stdout` rather than the
// logger because a credential in a log aggregator cannot be withdrawn from.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	democmd "github.com/semiplane/semiplane/cmd/server/demo"
)

// runDemo dispatches `semiplane demo`.
func runDemo(args []string) error {
	ctx := context.Background()

	// `slog.Default()`, and the separation from stdout is the point rather than an
	// accident. The seed prints a password to stdout once; every other line it
	// produces is a log, and a log belongs on stderr so that
	// `semiplane demo seed --root x > seed.txt` captures the credential and nothing
	// else. `runAdmin` needs no logger because it registers no plugins; this is the
	// first subcommand that does.
	registered, err := registerPlugins(slog.Default())
	if err != nil {
		return fmt.Errorf("register the plugins the demo vault needs: %w", err)
	}

	dependencies, err := demoDependencies(registered)
	if err != nil {
		return err
	}

	if err := democmd.Run(ctx, args, dependencies, democmd.Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: productVersion,
	}); err != nil {
		// `IsUsage` first, so an operator's typo still exits 2 with the usage text
		// rather than becoming a logged fault. The check has to happen here rather
		// than in `main`, because `main` cannot see the demo command's unexported
		// error type — which is what `IsUsage` exists to solve.
		if democmd.IsUsage(err) {
			return fmt.Errorf("semiplane demo: %w", err)
		}

		return fmt.Errorf("semiplane demo: %w", err)
	}

	return nil
}

// demoDependencies builds what the seed is handed, from what this build registered.
//
// # A named function, because the fingerprint is the claim and it was untestable
//
// The first version inlined this into `runDemo`, and the wiring test then asserted the
// fingerprint by calling `registered.fingerprint()` **itself** — so replacing the
// argument with `""` inside `runDemo` left the suite green. The test and the wiring
// were both reading the registry rather than the value handed over, which is the shape
// of a test that passes while the thing it names is broken.
//
// Naming it makes the claim testable without changing behaviour: a test reads the
// `Dependencies` the command will actually pass, and a `""` there is a `""` the seed
// receives.
//
// # The fingerprint is a string because `ruleset_version` is
//
// `plugins.fingerprint()` is the encoded four-component form and the column stores
// exactly that. A build with no gameplay system yields `""`, which migration 0005
// defines as a real value and which is what a campaign naming no system should carry —
// so empty is **meaningful** here and must never be defaulted into something plausible.
func demoDependencies(registered plugins) (democmd.Dependencies, error) {
	systems := registered.gameplay
	if systems == nil {
		// Refused rather than defaulted. A nil registry is a build that resolves
		// nothing, and handing the seed one produces campaigns whose `ruleset_version`
		// names nothing this build can answer for — a demo that seeds successfully and
		// then cannot start. `plugin.New` returns an empty registry rather than nil, so
		// this is unreachable by construction and is here to make that structural.
		return democmd.Dependencies{}, errors.New(
			"semiplane demo: this build registered no gameplay system, so the demo " +
				"vault's campaigns could not be given a ruleset_version")
	}

	return democmd.Dependencies{
		Systems:     systems,
		Fingerprint: registered.fingerprint().String(),
		HouseRules:  registered.houseRules,
	}, nil
}
