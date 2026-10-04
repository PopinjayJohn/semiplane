package main

// `semiplane demo` on the router — sorry, on the command line the binary serves.
//
// # Why this file exists
//
// V3 wrote `cmd/server/demo` and its own tests, and every one of them calls
// `demo.Run` directly. That is the right way to test a command and it is **not** the
// same claim: a subcommand that is built and never dispatched is a command that does
// not exist, and the failure is an operator typing `semiplane demo seed` and being
// told "unknown command".
//
// So this is the wiring assertion, and it is the same shape as
// `secrets_wiring_test.go`: **the name is dispatched, and `demo help` prints the
// subcommands.** An unreachable subcommand and an undocumented one are the same
// defect from the operator's side.
//
// # Why the mutation that matters is the registration
//
// `runDemo` calls `registerPlugins` before anything else, and that is not boilerplate.
// The seed resolves each campaign's `ruleset_version` through the real resolver, so a
// wiring that skipped registration would seed every campaign with a fingerprint from
// nothing — which `realtime.Gate` then reports as an unresumable campaign. A demo that
// seeds successfully and then cannot start is worse than a demo that refuses to seed,
// and the assertion below is that the registry is present when the command runs.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	democmd "github.com/semiplane/semiplane/cmd/server/demo"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
)

// TestTheDemoSubcommandIsDispatched is the smallest form: the name reaches a handler
// rather than the "unknown command" arm.
//
// **On the returned error, not on stderr.** `run` returns and `main` prints, so a test
// that watches stderr for a dispatch that did happen sees nothing — which is the shape
// of a test that passes for the wrong reason, and my first version of it did exactly
// that. It asserted "not unknown command" on a channel that is empty either way.
func TestTheDemoSubcommandIsDispatched(t *testing.T) {
	// No `t.Parallel()`: ADR 0004, a `*store.Store` is a process-wide slot, and
	// `runDemo` reaches the store.
	err := run([]string{subcommandDemo})
	if err == nil {
		t.Fatal("run(demo) error = nil, want the demo command's own usage error. " +
			"`demo` with no subcommands is an operator mistake and must say so")
	}

	message := err.Error()

	if strings.Contains(message, "unknown command") {
		t.Fatalf("run(demo) error = %q. The subcommand is not dispatched, so an "+
			"operator seeding the demo vault is told it does not exist", message)
	}

	if !strings.Contains(message, "demo has no subcommands") {
		t.Errorf("run(demo) error = %q, want the demo command's own usage error. The "+
			"subcommand dispatched something other than `demo.Run`", message)
	}

	// And it is a **usage** error, so `main` exits 2 rather than logging a fatal. The
	// demo command exports `IsUsage` for exactly this, because the type it inspects
	// lives in a package `main` cannot import.
	if !democmd.IsUsage(err) {
		t.Errorf("run(demo) error = %v is not a usage error, so `main` would log it "+
			"as a fault and exit 1. An operator's missing subcommand is their typo",
			err)
	}
}

// TestTheDemoSubcommandIsInTheUsageText: `semiplane help` is how an operator finds
// the command, and a command missing from it is a command nobody runs.
func TestTheDemoSubcommandIsInTheUsageText(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"semiplane demo seed",
		"semiplane demo reset",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usageText does not mention %q. `semiplane help` is how an "+
				"operator discovers the demo vault exists", want)
		}
	}
}

// TestDemoHelpListsItsSubcommands is the second half, and it is a separate assertion
// because the top-level usage and the subcommand's own help are two different
// documents: a command listed in one and absent from the other is discovered and then
// told it has no subcommands.
func TestDemoHelpListsItsSubcommands(t *testing.T) {
	t.Parallel()

	// `run` dispatches `help` to stdout, so capture stdout for this one.
	restore := os.Stdout

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}

	os.Stdout = writer

	done := make(chan string, 1)

	go func() {
		var buf bytes.Buffer

		_, _ = buf.ReadFrom(reader)

		done <- buf.String()
	}()

	runErr := run([]string{subcommandDemo, "help"})

	_ = writer.Close()

	os.Stdout = restore

	got := <-done

	if runErr != nil {
		t.Fatalf("run(demo help) error = %v, want nil", runErr)
	}

	for _, want := range []string{"seed", "reset"} {
		if !strings.Contains(got, want) {
			t.Errorf("`semiplane demo help` does not list %q:\n%s", want, got)
		}
	}
}

// TestDemoDependenciesCarriesTheRealFingerprint is the wiring claim, on the value the
// command actually hands over.
//
// The first version of this test called `registered.fingerprint()` itself, so putting
// `""` in `runDemo`'s literal — the exact mutation that produces a vault whose every
// campaign carries a `ruleset_version` naming nothing this build resolves — left the
// suite green. Both the test and the wiring were reading the registry instead of the
// dependency.
//
// So this asserts on `demoDependencies`' return value, which is what `runDemo` passes.
func TestDemoDependenciesCarriesTheRealFingerprint(t *testing.T) {
	t.Parallel()

	registered, err := registerPlugins(discardLogger())
	if err != nil {
		t.Fatalf("registerPlugins() error = %v, want nil", err)
	}

	dependencies, err := demoDependencies(registered)
	if err != nil {
		t.Fatalf("demoDependencies() error = %v, want nil", err)
	}

	if dependencies.Systems == nil {
		t.Fatal("Systems is nil. The seed resolves every campaign's system through it, " +
			"and a nil registry is a build that resolves nothing")
	}

	if _, ok := dependencies.Systems.Lookup(dnd5e.SystemID); !ok {
		t.Errorf("the systems handed to the seed do not include %q, so a demo "+
			"campaign naming it cannot be seeded", dnd5e.SystemID)
	}

	// **The load-bearing assertion.** Empty is meaningful for a build with no
	// gameplay system, and this build has one — so an empty fingerprint here is a
	// campaign seeded with a value no gate can match.
	if dependencies.Fingerprint == "" {
		t.Error("the fingerprint handed to the seed is empty. Every seeded campaign " +
			"would carry an empty ruleset_version, which realtime.Gate reports as " +
			"unresumable: a demo that seeds and then will not start")
	}

	if dependencies.Fingerprint != registered.fingerprint().String() {
		t.Errorf("the fingerprint is %q, want %q — `ruleset_version` stores the encoded "+
			"four-component form and nothing else recomputes it",
			dependencies.Fingerprint, registered.fingerprint().String())
	}

	// And the house-rule layer is handed over **on the dependency**, because ADR 0018
	// keeps modules out of the fingerprint deliberately: toggling a house rule must
	// not strand a campaign, so the seed resolves the two separately.
	//
	// The assertion is on `dependencies.HouseRules`, and that is the second time in
	// this file the distinction mattered: my first version checked
	// `registered.houseRules`, so passing `nil` in `demoDependencies` — the exact
	// mutation — left the suite green. Both halves of the claim were reading the
	// registry the product builds rather than the value the seed receives.
	if dependencies.HouseRules == nil {
		t.Error("the house-rule registry handed to the seed is nil. ADR 0018 " +
			"resolves it separately from the fingerprint, so the seed needs both, " +
			"and a manifest naming rule modules would seed rows no gate can account for")
	}

	if dependencies.HouseRules != registered.houseRules {
		t.Error("the house-rule registry handed over is not the one this build " +
			"registered. A second registry would answer for modules this binary " +
			"does not carry")
	}
}
