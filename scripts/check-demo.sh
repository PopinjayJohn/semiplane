#!/bin/sh
# `make demo-check` — the demo vault's completeness gate.
#
# # What this runs, and why it is a script and not a Go program
#
# `go test -run "$DEMO_TESTS" $DEMO_PKG`. The rules live in that package's test files
# because `internal/demo`'s non-test files belong to another work item (the seed and the
# manifest reader), and the `_test` suffix on the package keeps the two apart entirely —
# see `internal/demo/demovault_test.go`.
#
# `DEMO_PKG`, `DEMO_TESTS` and `GO_TEST` come from the environment, which the Makefile
# sets. Not duplicated here on purpose: the pattern is a list of test names, and a second
# copy of that list in a shell script is a second thing to keep in step — which is the
# failure `AGENTS.md` records for `A11Y_TESTS` and `VENDOR_TESTS` themselves.
#
# `GO_TEST` is `go test` and **not** the Makefile's `$(GO)`. `$(GO)` expands to
# `. /etc/profile.d/go.sh && go`, and passing that whole string through an environment
# variable does not survive the re-parse: `dash` turns the expansion into
# `(. /etc/profile.d/go.sh) && (go test …)` and then word-splits `go`'s arguments into
# the sourced file, so the script silently sees no output and its guard reports a package
# that "contributes no test" — a green gate reading red for a reason that has nothing to
# do with the gate. The Makefile therefore runs this script under `$(GOENV)`, so `go` is
# on `PATH` for the recipe and `GO_TEST` needs no shell syntax of its own.
#
# # The guard, and why it is here at all
#
# **`go test -run` exits 0 on a pattern matching nothing**, and that has bitten this
# repository three times in a row: the wiki route held 23 tests and contributed none of
# them to `make a11y`, the assets route held 49, and three work items independently
# shipped `TestTheSheetIsInsideTheBuild` while `A11Y_TESTS` matched none of them. A gate
# is therefore a *claim* that an audit exists, and the claim is enforced by counting what
# the pattern selects. This target carries the same guard as `a11y` and `vendor-check`.
#
# There is a second guard inside the package — `TestDemoEveryGateTestIsSelectedByTheMakefileGate`
# — and it is the half this script cannot see. The guard below checks that the pattern
# selects *something*; that one checks that the something is *every* gate test, by reading
# the package's own AST. A test that exists and is not in `DEMO_TESTS` runs under
# `make check` and not here, which is exactly the failure AGENTS.md records three times.
#
# `|| true` on both substitutions is load-bearing and is the same reasoning `a11y`
# documents: `.SHELLFLAGS` carries `-e`, so a `go test -list` that exits non-zero — a
# package that does not build, which is what a missing generated file looks like from
# here — kills the shell inside the assignment and the branch beneath it never runs. The
# guard would then fail with no explanation, which is worse than the silent pass it
# exists to prevent: a reader would go looking for a missing audit rather than for a build
# that did not finish.
#
# # The vault does not exist yet, and what this prints about that
#
# Phase 11's vault (`demo-vault/greyhaven/**`) is wave B and will be authored *against*
# this gate, so when this script lands there is nothing at `demo-vault/` to check. The
# gate is proved against its own committed fixture, `internal/demo/testdata/vault`, which
# is a small complete deliberately-broken vault exercising every rule.
#
# **The banner below is not decoration.** A green `make demo-check` today means "the gate
# works", not "the demo vault is verified", and the two are indistinguishable from the exit
# status alone — which is why the state is stated on stderr with `NOT` in it, and why two
# tests hold the honesty of that sentence: `TestDemoTheAuditFailsOnAnEmptyVault` proves
# the auditor is red on nothing, and `TestDemoTheShippedVaultIsAuditedWhenItExists`
# audits the real vault the moment it appears and fails if it is present but empty.
#
# `DEMO_VAULT_DIR` overrides the path, which is how a reviewer can point the gate at a
# vault under construction before it is committed. The empty case is **not** a way to skip
# the vault: an absent or campaignless root audits red, which is what the test above
# asserts about the auditor itself.
set -eu

: "${DEMO_PKG:?DEMO_PKG must be set by the Makefile}"
: "${DEMO_TESTS:?DEMO_TESTS must be set by the Makefile}"
: "${GO_TEST:=go test}"
: "${DEMO_VAULT_DIR:=demo-vault}"

listing="$($GO_TEST -list "$DEMO_TESTS" "$DEMO_PKG" 2>&1 >/dev/null || true)"
if [ -n "$listing" ]; then
	echo "demo-check: $DEMO_PKG does not build, so the gate cannot run:" >&2
	echo "$listing" | sed 's/^/demo-check:   /' >&2
	echo "demo-check: report this as the build failure it is. Claiming it" >&2
	echo "demo-check: 'contributes no test' sends a reader looking for a" >&2
	echo "demo-check: missing audit instead of a missing generated file." >&2
	exit 1
fi

ran="$($GO_TEST -list "$DEMO_TESTS" "$DEMO_PKG" 2>/dev/null | grep -c '^Test' || true)"
if [ "$ran" -eq 0 ]; then
	echo "demo-check: $DEMO_PKG contributes no test matching DEMO_TESTS." >&2
	echo "demo-check: naming a package here is a claim that the demo gate is" >&2
	echo "demo-check: checked at all. Restore the tests, or drop the package" >&2
	echo "demo-check: from DEMO_PKG — and then nothing checks the demo vault." >&2
	exit 1
fi

if [ -d "$DEMO_VAULT_DIR" ] && [ -n "$(find "$DEMO_VAULT_DIR" -mindepth 2 -name '*.md' -print -quit 2>/dev/null)" ]; then
	echo "==> auditing $DEMO_VAULT_DIR"
	$GO_TEST -count=1 -run "$DEMO_TESTS" "$DEMO_PKG"
	exit 0
fi

{
	echo "demo-check: ########################################################"
	echo "demo-check:"
	echo "demo-check:   THERE IS NO DEMO VAULT TO CHECK."
	echo "demo-check:"
	echo "demo-check:   $DEMO_VAULT_DIR does not exist, or holds no campaign."
	echo "demo-check:"
	echo "demo-check:   Phase 11's vault is wave B. It will be authored against this"
	echo "demo-check:   gate, and until it lands a green run here verifies THE GATE,"
	echo "demo-check:   NOT a demo vault. The exit status alone cannot tell those"
	echo "demo-check:   apart, which is why this banner exists."
	echo "demo-check:"
	echo "demo-check:   Being checked instead: $DEMO_PKG against its own committed"
	echo "demo-check:   fixture, which is a small complete deliberately-broken vault."
	echo "demo-check:"
	echo "demo-check:   Two tests hold the honesty of that sentence:"
	echo "demo-check:     TestDemoTheAuditFailsOnAnEmptyVault"
	echo "demo-check:     TestDemoTheShippedVaultIsAuditedWhenItExists"
	echo "demo-check:"
	echo "demo-check:   Set DEMO_VAULT_DIR to audit a vault before it is committed."
	echo "demo-check: ########################################################"
} >&2

$GO_TEST -count=1 -run "$DEMO_TESTS" "$DEMO_PKG"
