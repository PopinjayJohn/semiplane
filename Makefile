SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BINARY   := bin/server
PKGS     := ./...

# The container does not source Go by default, so a fresh shell needs it. A
# GitHub runner has no /etc/profile.d/go.sh at all -- actions/setup-go puts go
# on PATH itself -- so the source step has to be conditional. An unconditional
# `. /etc/profile.d/go.sh &&` makes every target below fail on a runner with a
# bare "No such file or directory", and the failure reads like a toolchain
# problem rather than a Makefile problem.
GOENV := $(if $(wildcard /etc/profile.d/go.sh),. /etc/profile.d/go.sh &&)
GO     := $(GOENV) go

# Tool versions live here and nowhere else. CI reads them from this file rather
# than repeating the pins, so the local gate and the CI gate cannot drift apart.
GOLANGCI_VERSION := v2.14.0
ACTIONLINT_VERSION := v1.7.7
YQ_VERSION        := v4.47.2
TEMPL_VERSION     := v0.3.1020
# The frontend tools are installed to a repository-local GOBIN rather than
# $HOME/go/bin: ci.yml already does this for the Go tools, and a developer's
# personal GOBIN is not something a build should depend on.
TOOLBIN      := $(CURDIR)/.toolbin
# Tailwind ships a standalone binary and a sha256sums.txt covering every
# platform. Pinning the *manifest's* digest is one committed hash instead of
# four, and it is the stronger pin: the per-platform digests are then verified
# against an upstream artifact whose own hash is committed here. See
# docs/content/en/decisions/0019-tailwind-standalone-pinned.md.
TAILWIND_VERSION         := v4.3.3
TAILWIND_MANIFEST_SHA256 := 527b4fcd96950f9ae8f83bbbff27c61e4ff3596cb0b2eb760f9b3516de5d3c56
# CGO_ENABLED=0 selects Hugo's standard edition. With cgo enabled, go install
# builds the extended edition, which needs a C compiler for Sass the site never
# uses -- and makes the built artifact depend on the build machine.
HUGO_VERSION      := v0.167.0

SITE_DIR     := docs
SITE_PUBLISH := $(SITE_DIR)/public
SITE_CACHE   := $(SITE_DIR)/resources
# The path the site is served under, on its own. Kept separate from BASEURL
# because the link checker needs the path and BASEURL carries a scheme and a
# host, and deriving one from the other is string surgery that breaks silently.
BASE_PATH    ?= /semiplane
BASEURL      ?= https://popinjayjohn.github.io$(BASE_PATH)/
# Pinned so a local build and a CI build of the same commit produce the same
# binary. Set to nothing to resolve whatever the toolchain offers.
LABEL_REPO ?= PopinjayJohn/semiplane

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: gopls golangci-lint actionlint yq hugo templ-bin tailwind ## Install every developer tool (see AGENTS.md)

.PHONY: tools-ci
tools-ci: golangci-lint templ-bin tailwind ## Install only what the CI gate needs

.PHONY: templ-bin
templ-bin: ## Install the templ compiler at $(TEMPL_VERSION) into .toolbin
	@# `export`, not an assignment prefix. Every other tool target sets GOBIN
	@# inline and the prefix works, because the prefix applies to a single
	@# command. This one is `. /etc/profile.d/go.sh && go install ...` -- the
	@# conditional GOENV -- so a prefix would attach to the `.` builtin and
	@# never reach go, silently installing to $HOME/go/bin instead.
	export GOBIN="$(TOOLBIN)"; $(GOENV) go install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)

.PHONY: tailwind
tailwind: ## Verify and install the pinned Tailwind standalone binary into .toolbin
	TAILWIND_VERSION=$(TAILWIND_VERSION) \
	TAILWIND_MANIFEST_SHA256=$(TAILWIND_MANIFEST_SHA256) \
	TOOLBIN="$(TOOLBIN)" ./tools/install-tailwind.sh

.PHONY: gopls
gopls: ## Install gopls (LSP; not needed in CI)
	$(GO) install golang.org/x/tools/gopls@latest

.PHONY: golangci-lint
golangci-lint: ## Install golangci-lint at $(GOLANGCI_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

.PHONY: actionlint
actionlint: ## Install actionlint at $(ACTIONLINT_VERSION)
	$(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: yq
yq: ## Install yq at $(YQ_VERSION); reads the label manifest
	$(GO) install github.com/mikefarah/yq/v4@$(YQ_VERSION)

.PHONY: hugo
hugo: ## Install Hugo at $(HUGO_VERSION); builds the docs site
	$(GOENV) CGO_ENABLED=0 go install github.com/gohugoio/hugo@$(HUGO_VERSION)

.PHONY: fmt
fmt: ## Apply formatters (gofumpt + gci + golines)
	$(GOENV) golangci-lint fmt

WEB_CSS_SRC := $(CURDIR)/internal/web/static/css/app.css
WEB_CSS_OUT := $(CURDIR)/internal/web/static/dist/app.css
WEB_DIST    := $(CURDIR)/internal/web/static/dist
WEB_JS_SRC  := $(CURDIR)/internal/web/static/js
WEB_VENDOR_SRC := $(CURDIR)/internal/web/static/vendor

# --- The vendor pin (ADR 0052) ------------------------------------------------
# Three targets, three different claims, and the division is the whole point:
#
#   vendor-check  re-hashes the bytes that are **committed**. No network. This is
#                 the one in `check`, because it is the one that catches a
#                 swapped blob in CI rather than trusting it.
#   vendor-dry-run re-fetches and verifies, writes nothing. Network. This is the
#                 check a manifest edit needs *before* it is committed, and the
#                 reason it exists is that `vendor-check` cannot answer it: a
#                 manifest edit changes the digests, so the offline check passes
#                 the moment the wrong digests are written down.
#   vendor        re-fetches, verifies, and writes. Network. Backs a `source`
#                 whose bytes are not on disk yet.
#
# `vendor-check` and `vendor-dry-run` are deliberately two targets rather than
# one with a flag: the first is gate-blocking and must reach no network — CI has
# no npm account and no credentials, and a gate that fetched there would make
# `make check` depend on a third party being up. The second is a developer's
# command, run deliberately, with a network.
#
# **`vendor-check` is a `go test -run`, and that is a silent-pass hazard**, so it
# carries the guard `make a11y` carries: `go test -run` exits **0** when the
# pattern matches nothing, and a pattern matching nothing is a green gate wired
# to no assertion at all. This repository has been bitten by that twice — the
# wiki route held 23 tests and contributed none of them, and the assets route 49
# — so a name here is a *claim* that the audit exists, and the claim is enforced
# by counting what the pattern selects. See AGENTS.md.
#
# The check itself is `TestTheVendoredBytesMatchThePin` and it is **not**
# reimplemented here. It reads the whole manifest rather than one package, so
# adding a package to `packages` is covered without touching this file, and a
# second digest check would be a second answer to the same question. It also
# asserts the claim this target cannot: that **every file in the served vendor
# tree is declared**, because a hand-copied script nobody hashed is a module the
# browser loads with no digest covering it.
VENDOR_PKG   := ./internal/web/static/js/map
VENDOR_TESTS := Vendored

.PHONY: vendor-check
vendor-check: ## Re-hash every committed vendor byte against tools/vendor.json (no network)
	@# The guard, in the same shape as the one under `a11y`: a listed pattern
	@# that selects nothing is a gate nobody ran, and the exit status says 0.
	@#
	@# `|| true` on both substitutions, and that is not cosmetic. `.SHELLFLAGS`
	@# carries `-e`, so a `go test -list` that exits non-zero — a package that
	@# does not build, which is what a missing `*_templ.go` looks like from here
	@# — kills the shell inside the assignment and the branch below never runs.
	@# The guard then fails the target with no explanation, which is the one
	@# outcome worse than the one it exists to prevent: a reader looking for a
	@# missing audit rather than for a build that did not finish.
	@listing="$$($(GO) test -list '$(VENDOR_TESTS)' $(VENDOR_PKG) 2>&1 >/dev/null || true)"; \
		if [ -n "$$listing" ]; then \
			echo "vendor-check: $(VENDOR_PKG) does not build, so the check cannot run:"; \
			echo "$$listing" | sed 's/^/vendor-check:   /'; \
			echo "vendor-check: report this as the build failure it is. Claiming it"; \
			echo "vendor-check: 'contributes no test' sends a reader looking for a"; \
			echo "vendor-check: missing audit instead of a missing generated file."; \
			exit 1; \
		fi
	@ran="$$($(GO) test -list '$(VENDOR_TESTS)' $(VENDOR_PKG) 2>/dev/null | grep -c '^Test' || true)"; \
		if [ "$$ran" -eq 0 ]; then \
			echo "vendor-check: $(VENDOR_PKG) contributes no test matching VENDOR_TESTS."; \
			echo "vendor-check: naming a package here is a claim that the vendor pin is"; \
			echo "vendor-check: checked at all. Restore the test, or drop the package"; \
			echo "vendor-check: from VENDOR_PKG — and then nothing checks the pin."; \
			exit 1; \
		fi
	$(GO) test -count=1 -run '$(VENDOR_TESTS)' $(VENDOR_PKG)

.PHONY: vendor-dry-run
vendor-dry-run: ## Re-fetch every npm package and verify it, writing nothing (needs the network)
	$(GO) run ./tools/vendor -dry-run

.PHONY: vendor
vendor: ## Re-fetch every npm package and verify it into internal/web/static/vendor/
	$(GO) run ./tools/vendor

# --- Staging the served browser assets ---------------------------------------
# `internal/web` embeds `all:static/dist` and `/assets/` serves exactly that
# tree, so **anything the browser fetches has to be in `static/dist/` or it 404s**
# — and a 404 asset is a *silently* absent behaviour: the page renders, the
# tabletop loads, and the map is simply a blank box. `make css` was building the
# stylesheet into that tree and nothing was staging `static/js/` or
# `static/vendor/` into it, which is how two work items arrived reporting the
# same missing Makefile target rather than taking it.
#
# The relative layout is preserved because it is load-bearing twice over:
# `scene.js` imports `../../vendor/pixi.min.mjs`, and `head.js` is the one
# blocking script, so a flattened copy is a module graph that cannot resolve.
#
# `*.js` only, and that is not an optimisation. These trees also hold Go source:
# the map's and the token list's audits are `*_test.go` files sitting beside the
# modules they read, and a copy that took everything would embed a Go test file
# into the binary and serve it at `/assets/js/…`. That is a source disclosure
# through a route that has no gate on it.
#
# `tar -T -` rather than `cp --parents`: `cp --parents` is GNU, and a target
# that only runs on the container's Linux is a target a contributor on macOS
# cannot build the product with.
.PHONY: stage-assets
stage-assets: ## Stage the browser modules and vendored assets into internal/web/static/dist/
	@rm -rf $(WEB_DIST)/js $(WEB_DIST)/vendor
	@mkdir -p $(WEB_DIST)/js $(WEB_DIST)/vendor
	@cd $(WEB_JS_SRC) && find . -type f -name '*.js' -print | tar -cf - -T - \
		| tar -xf - -C $(WEB_DIST)/js
	@cd $(WEB_VENDOR_SRC) && find . -type f -print | tar -cf - -T - \
		| tar -xf - -C $(WEB_DIST)/vendor
	@echo "staged $$(find $(WEB_DIST)/js $(WEB_DIST)/vendor -type f | wc -l) browser assets"

.PHONY: css
css: tailwind stage-assets ## Build the stylesheet and stage the assets into internal/web/static/dist/
	@mkdir -p $(dir $(WEB_CSS_OUT))
	$(TOOLBIN)/tailwindcss --input $(WEB_CSS_SRC) --output $(WEB_CSS_OUT) --minify

.PHONY: templ
templ: templ-bin ## Generate Go from every .templ file
	$(TOOLBIN)/templ generate

.PHONY: build
build: ## Compile all packages
	@# internal/web embeds the generated stylesheet, so a build without `make
	@# css` first fails on a missing embed pattern. Saying so beats the
	@# alternative, which is an error about a directory that legitimately is
	@# not in version control.
	@test -f $(WEB_CSS_OUT) || { \
		echo "==> $(WEB_CSS_OUT) is missing."; \
		echo "==> internal/web embeds it, so run: make css"; \
		exit 1; }
	$(GO) build $(PKGS)

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKGS)

.PHONY: lint
lint: ## Run golangci-lint
	$(GOENV) golangci-lint run $(PKGS)

.PHONY: lint-fix
lint-fix: ## Auto-fix fixable lint and format issues
	$(GOENV) golangci-lint run --fix $(PKGS)
	$(MAKE) fmt

.PHONY: lint-verify
lint-verify: ## Validate .golangci.yml against the v2 schema
	$(GOENV) golangci-lint config verify

.PHONY: test
test: ## Run tests
	$(GO) test -race -count=1 $(PKGS)

# UI §10.1, §10.2 and §10.6 are gate-blocking, so they get their own target —
# `make a11y` — and it is in `check` rather than only in `ci`, for the reason
# `css` and `templ` are: these tests read a *built* artefact
# (internal/web/static/dist/app.css) and the shell's rendered documents, and a
# target a developer can forget is a gate nobody runs.
#
# Split from `test` so a failure names the accessibility contract rather than
# "a test failed", and so `go test ./...` stays fast enough to run in a loop while
# the a11y subset stays explicit about what it covers.
#
# Deliberately NOT wired to any Playwright step. UI §10.3-§10.7 are the
# agent-assisted sweeps, and the plan is explicit that the MCP must not become a
# CI dependency: §10.9 requires every *finding* from those sweeps to become a
# committed Go test instead, and every one of them below is. Adding a browser here
# would make CI depend on a Node toolchain this repository deliberately does not
# have.
# The packages carrying a §10.2 audit. One per route package, because a route
# that is not named here does not run its audits at all — and "the gate is green"
# then means the gate looked somewhere else.
#
# `./internal/httpapi/wiki` and `./internal/httpapi/search` and
# `./internal/httpapi/assets` are here because each renders a distinct document
# under the same shell, and §10.2 says *every* route.
#
# **Each entry is a name AND a `$(wildcard)`, and adding a route is two steps.**
# The wildcard gives two things, and only one of them is the one it looks like:
#
#   - a route package that does not exist yet cannot fail the target, so this line
#     can be extended before the package lands, which is the mistake a literal list
#     makes the moment one lands before another;
#   - **it expands nothing that is not already named.** `$(wildcard a b c)` is
#     `a b c` with the missing ones dropped. It is not a directory scan.
#
# So a new route is invisible to this gate until somebody adds its name here, and
# `internal/httpapi/plugins` was: the directory landed with #64's six audits and the
# gate did not look at them, because nobody edited this line. Nothing said so —
# `go test -run` exits 0 on a pattern that matches nothing, which is the same silent
# pass the guard below exists for. The wildcard is here for the first bullet only,
# and the claim a name makes is enforced by the guard, not by the wildcard.
A11Y_ROUTE_PKGS := $(wildcard ./internal/httpapi/wiki ./internal/httpapi/search \
	./internal/httpapi/assets ./internal/httpapi/edit ./internal/httpapi/plugins \
	./internal/httpapi/events ./internal/httpapi/theme ./internal/httpapi/play)

# Component packages, listed separately rather than folded into A11Y_PKGS
# silently, because for **two whole phases** they were absent. `./internal/web`
# and `./internal/web/components` were in the gate and the packages below were
# not, so `components/play`'s token-list audits and `components/chat`'s
# live-region audit were written and never run by `make a11y`. Nothing said so.
#
# `go test -run` exits **0** on a pattern matching nothing, which is the silent
# pass the ROUTE_PKGS guard exists for -- and that guard only walked
# A11Y_ROUTE_PKGS, so a component package named without also being guarded had no
# guard at all. The loop below therefore walks both lists.
#
# `components/play` earns its place on the same claim as any route package: the
# token list is UI §7.6's **accessibility source of truth** for the tabletop, and
# the document it renders into is a route document by any reading.
A11Y_COMPONENT_PKGS := $(wildcard ./internal/web/components/play \
	./internal/web/components/chat ./internal/web/components/live \
	./internal/web/components/secret)

A11Y_PKGS := ./internal/web ./internal/web/components ./internal/httpapi \
	$(A11Y_COMPONENT_PKGS) $(A11Y_ROUTE_PKGS)

# The pattern is a list of substrings of the gate's test names, and it is
# deliberately *readable* rather than exhaustive-looking: a new gate test is added
# here by naming it, and `make a11y` is the review point where somebody notices
# it was not. An over-broad pattern would be worse -- it would run half the suite
# and look thorough.
#
# Each alternative names one test or one family:
#   Contrast*                 UI §10.1, tokens_contrast_test.go (S1)
#   Structural|Vocabulary     UI §10.2, the per-route DOM audit and its self-tests
#   ...RepresentsTheRoutes    the audit's self-tests over the §10.2 rules
#   Target|TvMode|Preferences
#     AreNever|FocusIndicator UI §10.6 and §3.3, the stylesheet gate in a11y_test.go
#   BuiltStylesheet           that no layer is missing from the build, and that the
#     |ScanCoverage           plugin sources are among the scan roots (the scan-coverage
#                             half of the built-artefact gate, internal/web)
#   ProductName|ViewModelLayers|LoadFailureIs|CampaignFallsBack|SignOutTarget
#     |EntitledToAssert|DegradedWarning
#                             the composition's own invariants
#   SheetIsInsideTheBuild | the per-sheet "is this sheet in the built artefact"
#     |SheetDeclaresNo     tests, and the "does every class this sheet styles
#     |RuleInTheSheet      exist in the document" tests that go with them. Added
#     |SelectorInThisSheet in phase 9, and the reason is in the history: three work
#     |SheetIsResponsibleFor items independently shipped
#                         `TestTheSheetIsInsideTheBuild` and the pattern below
#                         matched **none of them**, so every sheet's
#                         built-artefact assertion was written, tested by
#                         `make check`, and invisible to `make a11y` -- which is
#                         the target whose whole job is the claims that read
#                         `static/dist/app.css`. Deleting a `@import` left
#                         `make a11y` green on all three.
#
#                         It is a name in a list of what the gate claims to run,
#                         not a rename to quiet the guard. The guard is about a
#                         package *contributing*; this makes the contribution
#                         actually execute, which is the other half of the same
#                         problem and the half `AGENTS.md` records twice: the
#                         wiki route held 23 tests and the search route 33, and
#                         neither matched a single alternative.
A11Y_TESTS := Contrast|Structural|Vocabulary|RepresentsTheRoutes|Target|TvMode|TvRail|PreferencesAreNever|FocusIndicator|BuiltStylesheet|ScanCoverage|InheritedTypeSize|HoverRule|ProductName|ViewModelLayers|LoadFailureIs|CampaignFallsBack|SignOutTarget|EntitledToAssert|DegradedWarning|DegradedNotice|HeadingLevels|EveryRoute|Identifier|IsTheStatePackageType|ExactlyOneH1|SkipLink|SheetIsInsideTheBuild|SheetDeclaresNo|RuleInTheSheet|SelectorInThisSheet|SheetIsResponsibleFor

.PHONY: a11y
# `css` and `templ` are prerequisites, and they are here because **this target
# reads a built artefact and `go test -run` on a missing one reports the wrong
# failure.**
#
# Two of §10.1's and §10.6's claims are about `static/dist/app.css`, and the
# per-route audits need the served documents, which need `*_templ.go`. Run
# standalone on a fresh checkout -- which AGENTS.md documents as a supported
# invocation, "just the §10.1/§10.2/§10.6 gate" -- this target used to report:
#
#     FAIL github.com/semiplane/semiplane/internal/web [setup failed]
#     FAIL github.com/semiplane/semiplane/internal/httpapi/theme [setup failed]
#
# which reads as *the audits are broken* and sends a reader looking for a
# missing audit. The truth is that nothing was built. That is the same failure
# `AGENTS.md` records for the `A11Y_ROUTE_PKGS` wildcard and for the `a11y`
# guard's own `-e` bug, in a third place: **a gate whose failure names something
# other than what is wrong is worse than no gate**, because it costs a reader
# the one thing they cannot afford, which is time spent on the wrong thing.
#
# `check` already ran both before reaching here, so this costs nothing in the
# normal path and makes the standalone invocation correct.
a11y: css templ ## Run the UI §10.1/§10.2/§10.6 accessibility gate
	@echo "==> a11y"
	@# The guard below exists because `go test -run` reports success when the
	@# pattern matches nothing. A route package can be listed in A11Y_PKGS, hold
	@# forty-nine tests, and contribute **zero** to this gate — which is what the
	@# assets route did: no target-size audit, no vocabulary audit, no landmark
	@# audit, and `make a11y` green because the package answered "[no tests to
	@# run]" and exited 0.
	@#
	@# "The gate is green" must mean the gate looked. So a listed package that runs
	@# nothing fails the gate, with the package named.
	@# `|| true` on the substitution, and it is load-bearing: `.SHELLFLAGS`
	@# carries `-e`, so a package that does not build exits the assignment before
	@# the branch beneath it can print anything. The guard would then fail the
	@# target silently, which is worse than the silent pass it was written to
	@# stop — a reader would go looking for a missing audit rather than for a
	@# build that did not finish. Found by writing `vendor-check`'s identical
	@# guard and running it against a package that does not exist.
	@for pkg in $(A11Y_ROUTE_PKGS) $(A11Y_COMPONENT_PKGS); do \
		listing=$$($(GO) test -list '$(A11Y_TESTS)' $$pkg 2>&1 >/dev/null || true); \
		if [ -n "$$listing" ]; then \
			echo "a11y: $$pkg does not build, so the gate cannot look at it:"; \
			echo "$$listing" | sed 's/^/a11y:   /'; \
			echo "a11y: report this as the build failure it is. Claiming it"; \
			echo "a11y: 'contributes no test' sends a reader looking for missing"; \
			echo "a11y: audits instead of a missing generated file."; \
			exit 1; \
		fi; \
		ran=$$($(GO) test -list '$(A11Y_TESTS)' $$pkg 2>/dev/null | grep -c '^Test' || true); \
		if [ "$$ran" -eq 0 ]; then \
			echo "a11y: $$pkg contributes no test matching A11Y_TESTS."; \
			echo "a11y: naming a package here is a claim that it has §10.2 audits."; \
			echo "a11y: add them, or drop the package from A11Y_ROUTE_PKGS or"; \
			echo "a11y: A11Y_COMPONENT_PKGS."; \
			exit 1; \
		fi; \
	done
	$(GO) test -race -count=1 -run '$(A11Y_TESTS)' $(A11Y_PKGS)

.PHONY: cover
cover: ## Run tests and report coverage
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-integration
test-integration: ## Run integration-tagged tests
	$(GO) test -race -count=1 -tags=integration $(PKGS)

# --- The demo vault gate -------------------------------------------------------
#
# Phase 11's completeness gate (plan D9): every page reachable from its campaign index,
# every registered page kind demonstrated, every installed render extension exercised,
# both `[!secret]` states present, the unresolved-link count equal to exactly the
# breakage the vault declares, and every referenced asset present.
#
# **Every one of those requirements is derived, not enumerated.** The kind list is
# `plugin.Registry.Kinds()` over every edition the build ships, so adding a kind to a
# plugin turns this red until the vault demonstrates it; the extension list is
# `ext.Builtins()`; the secret states are the two `content.SecretState` values; and the
# broken-link budget is the length of the vault's own `demo.broken` declarations rather
# than a number written here. A gate listing page names would be a checklist describing
# pages that no longer exist, and deleting a page from the list and deleting the page from
# the vault are the same event.
#
# `DEMO_TESTS` is a list of substrings of the gate's test names, in the same shape as
# `A11Y_TESTS` and for the same reason: a name here is a claim that the audit is run by
# *this* target, and the claim is enforced. Two guards hold it and neither is enough
# alone:
#
#   - `scripts/check-demo.sh` checks that the pattern selects at least one test. It
#     cannot see a test that exists and is not in the pattern, because `go test -run`
#     exits 0 on a pattern matching nothing — the silent pass AGENTS.md records three
#     times over.
#   - `TestDemoEveryGateTestIsSelectedByTheMakefileGate` reads this package's own AST and
#     requires **every** `TestDemo*` function to appear below. That is the half the
#     shell guard cannot see, and it is the half that bit three separate work items in
#     phase 9.
#
# The list is deliberately *readable* rather than exhaustive-looking: a new gate test is
# added here by naming it, and `make demo-check` is the review point where somebody
# notices it was not — except that here the omission fails the build rather than waiting
# to be noticed.
#
# **`demo-check` is part of `check` as of the commit that landed the first campaign
# directory**, which is what this comment used to ask the integrator for. It was
# deliberately withheld until then because a green `make demo-check` over no vault
# verifies *the gate* and not a vault, and the exit status alone could not tell those
# apart — the banner said so on stderr, and
# `TestDemoTheAuditFailsOnAnEmptyVault` proved the auditor is red on nothing.
#
# **All three campaigns are present now, so that caveat is discharged** and the gate is
# a claim about the shipped vault. The two properties that made it safe to admit are
# still what hold it: the rules are **derived from `plugin.Registry` and
# `ext.Builtins()`** rather than written down, so a kind added in a later phase turns
# this red until the vault demonstrates it; and the unresolved-link **budget is the
# length of the vault's own `demo.broken`**, so it cannot drift from the breakage the
# vault demonstrates.
#
# It sits after `a11y` and before `test` because it reads `static/dist/app.css` (the
# built stylesheet) for the callout's contrast, which is why `css` has to have run.
DEMO_PKG   := ./internal/demo
DEMO_TESTS := FixtureVault|UndeclaredBrokenLink|RepairedDeclaration|BudgetIsDerived|RemovingAKind|RegistryTurnsTheGate|RemovingAnExtension|RemovingTheRevealedSecret|RemovingTheCollapsedSecret|UnreachablePage|MissingAsset|AssetInACampaign|NoIndexPage|TwoIndexPages|NoFindingCarriesSecretText|SkipsThePagesContent|AuditIsDeterministic|AuditFailsOnAnEmptyVault|AuditFailsOnAVaultRoot|ShippedVaultIsAudited|ExistsAndIsEmpty|BudgetIsResolvedForTheDemoReader|KindsTheGateDemands|EverySecretStateTheScanner|RedactionMarkerIsTheProductsOwn|GateDerivesItsRequirements|GateTestIsSelectedByTheMakefileGate|GateTargetRunsThisPackagesTests|FixtureCarriesBothSecretStates

.PHONY: demo-check
demo-check: ## Assert the demo vault is complete (registry-derived; no hand-written list)
	@# `$(GOENV)` rather than `$(GO)`: the script needs `go` on PATH, and `$(GO)`
	@# expands to `. /etc/profile.d/go.sh && go`, whose `&&` does not survive being
	@# handed to a script as an environment variable. See the script's header.
	@$(GOENV) DEMO_PKG='$(DEMO_PKG)' DEMO_TESTS='$(DEMO_TESTS)' GO_TEST='go test' \
		./scripts/check-demo.sh

# --- The demo artefact (ADR 0060) ------------------------------------------------
# `make demo-artifact` builds `$(DEMO_DIST)/semiplane-demo-v<version>.tar.gz` out of the
# committed `demo-vault/`, and the release publishes it beside the binary. The vault is
# not embedded: D7's argument is that a map image is tens of megabytes and a compiled-in
# copy makes the vault ship on the Go build's schedule instead of its own. The cost is the
# version skew ADR 0060 records, which `semiplane demo seed` refuses.
#
# **It is not in `check`, and that is the plan's rule rather than an omission**: `check`
# touches neither the artefact nor the network. The target's own last assertion enforces
# the first half — it reads `make -n check` and fails if this target is reachable from
# there — because a claim nobody checks is a claim nobody has to keep.
#
# ## What "reproducible" means here, precisely
#
# The delivery plan's words are "same input, byte-identical output". A tarball does not
# give you that for free: mtimes, uid/gid, uname/gname, permission bits and **entry order**
# all vary between two machines and between two checkouts, and gzip adds a timestamp and
# the original filename to its own header. So every one of those is normalised and then
# *checked*, because a normalisation nothing verifies is a comment:
#
#   --format=gnu        not pax. A pax header records atime and ctime, and reading a file
#                       updates atime, so a pax archive of an unchanged tree differs from
#                       itself. Measured, not hypothesised: two pax builds 200ms apart over
#                       one tree produce two digests.
#   --mtime=@0          every entry, files and directories. A raw epoch, so the stored
#                       value cannot depend on the builder's timezone.
#   --owner=0 --group=0 --numeric-owner
#                       the numeric ids *and* the uname/gname strings, which tar looks up
#                       unless --numeric-owner is given. Dropping these changes the digest
#                       even when every file is already root-owned, because "root" is a
#                       string in the header.
#   --mode='a=rwX,go-w'  the permission bits, because git records only the executable bit
#                       and the checkout's the umask's. Measured across files at 600,
#                       640, 644, 664, 666 and directories at 700, 750, 755, 777: one
#                       digest for all nine. It cannot remove an executable bit from a
#                       *file*, and does not need to: git cannot produce one from a
#                       non-executable blob.
#   --no-recursion      **the one the plan's own recipe is missing.** See below.
#   gzip -9n             the header's timestamp and filename. `-9n` is load-bearing on a
#                       *named* input file: gzip stamps the tar's own mtime and records
#                       "archive.tar" unless told not to. It also means the name of the
#                       intermediate file cannot reach the shipped bytes at all.
#
# ## `--no-recursion`, and why `find … | sort | tar -T -` does not sort anything
#
# The recipe this target was asked to follow is `find … | sort | tar -T -`. Run as
# written, **the archive is not in the sorted order**, and it holds three times as many
# entries as the list has names:
#
#   - `tar` given a **directory** recurses into it, emitting its children in readdir
#     order — which is filesystem hash order, and is precisely what differs between an
#     ext4 box, a zfs box and a tmpfs. `find`'s order is that same readdir order, so
#     sorting `find`'s output and then handing it to a recursing `tar` sorts a list the
#     archive then ignores.
#   - every child is therefore archived twice: once by the recursion and once by its own
#     line in the list. The second copy is emitted as a **hard link**, so the archive
#     grows `hrw-r--r … link to …` entries pointing at themselves.
#
# Measured here on GNU tar 1.35: a 52-name list over `demo-vault/` produced a 177-entry
# archive, in readdir order, with 106 self-referential hard links. `LC_ALL=C sort` on the
# input changed nothing about the recorded order. `--no-recursion` fixes both, and the
# assertion below (`the recorded order == the sorted list, entry for entry`) is what keeps
# it fixed.
#
# `--sort=name` is deliberately **not** used. It would produce a correct archive over a
# *recursing* tar, and it would make this target's own order probe vacuous, because the
# flag would sort the walk rather than the list this target hands over — so the reversed-
# list control below would stop distinguishing *our* sort from tar's. The sort this target
# relies on is the one it does, in a pipeline, where a reader can see it.
#
# **Measured, and the flag is weaker than the note first claimed.** GNU tar's `--sort` orders
# what tar *walks*, and it does not reorder a `-T` file list: with `--no-recursion` and a
# reversed list, `--sort=name` left the archive in reversed order and produced a different
# digest from the sorted build. So the control below does **not** fire when `--sort=name` is
# added — an earlier version of this comment said it would, and it does not. `--sort=name`
# would mask a missing `--no-recursion` (it sorts the recursion, and the entry-count
# assertion then has to catch it instead), which is why `--no-recursion` is asserted
# directly and carries its own count check.
#
# ## What is proved, and what would go red if it stopped being true
#
# Four builds, not one, because "produces a tarball" and "produces a *reproducible*
# tarball" are different claims and the second one is the deliverable:
#
#   1. the staged tree as committed, from the sorted list   -> sha A
#   2. after **perturbing every mtime and every mode**, from the sorted list  -> sha B
#   3. from the **reversed** list                            -> sha C
#
# and then three assertions: B == A, C == A, and the recorded entry order == the sorted
# list. Each perturbation exists because the naive version of the check passes without the
# thing it is checking: two runs over an unchanged tree produce the same archive even with
# `--mtime`, `--mode` and the sort all deleted, because nothing about the tree changed. So
# build 2 is over a deliberately mis-stamped, mis-moded tree, and build 3 is over a
# deliberately reversed one. Removing any of the five flags turns one of them red; the
# mutation table is in the work item's report.
#
# `sleep 1` between the builds is load-bearing too, for one second of it: gzip's header
# timestamp has one-second granularity, so without a gap a build that dropped `gzip -n`
# would stamp both builds with the same second and pass.

DEMO_VAULT_DIR := demo-vault
DEMO_DIST      ?= dist
# The one directory inside the tarball. Fixed rather than versioned, so
# `--root ./semiplane-demo` is the same path for every release and the install
# guide can quote it without a placeholder.
DEMO_ROOT_NAME := semiplane-demo
DEMO_MANIFEST  := $(DEMO_VAULT_DIR)/demo.manifest.yml
# The release the artefact declares, and where the default comes from.
#
# **The committed manifest is the default**, because that file is already the answer to
# "which semiplane release was this built against" — `demo.Check` compares its `product`
# against the binary's own version, and D7/D11's whole skew argument is that there is
# nowhere else either number appears. A second copy of the version in the Makefile would
# be a second answer to the same question, and the release workflow's override would be a
# third. A release therefore passes DEMO_VERSION= from the tag and the manifest inside the
# tarball is stamped to it; a local build ships the committed manifest verbatim.
#
# `?=` rather than `=`, so an environment variable wins without the `$(shell …)` being
# evaluated at all. Recursive expansion on purpose: the same reason, one level down.
DEMO_VERSION  ?= $(shell sed -n 's/^product: *"\([^"]*\)".*$$/\1/p' $(DEMO_MANIFEST))
DEMO_ARCHIVE  := $(DEMO_DIST)/semiplane-demo-v$(DEMO_VERSION).tar.gz
DEMO_TARFLAGS := --no-recursion --format=gnu --mtime=@0 --owner=0 --group=0 \
                 --numeric-owner --mode='a=rwX,go-w'

.PHONY: demo-artifact
demo-artifact: ## Build $(DEMO_ARCHIVE) and prove it byte-identical across three builds
	@echo "==> demo-artifact"
	@# GNU tar, stated rather than discovered. The flags above are GNU tar's spellings
	@# and a failure to find one is a wall of "unrecognized option" rather than a
	@# sentence about the artefact. This target is release-time and is deliberately not
	@# in `check`, unlike `stage-assets`, whose GNU-freeness is why a macOS contributor
	@# can still build the product.
	@tar --version 2>/dev/null | head -1 | grep -q 'GNU tar' || { \
		echo "demo-artifact: this needs GNU tar. Found:"; \
		tar --version 2>&1 | head -1 | sed 's/^/demo-artifact:   /'; \
		exit 1; }
	@test -d $(DEMO_VAULT_DIR) || { \
		echo "demo-artifact: $(DEMO_VAULT_DIR) is not a directory."; exit 1; }
	@# The version, and the shape of the one line it is read from.
	@#
	@# The sed is a **shape** check as much as a reader: it demands the manifest's
	@# `product: "…"` on a line of its own. A build that reflowed the manifest, quoted
	@# the value differently or moved the key reads an empty string here and stops,
	@# rather than shipping `semiplane-demo-v.tar.gz` — which is a filename, a
	@# download URL and a version-skew check that all agree with each other and are all
	@# wrong. `grep -c` is the second half: two `product:` lines is a manifest with two
	@# answers, and picking the first is picking one.
	@committed="$$(sed -n 's/^product: *"\([^"]*\)".*$$/\1/p' $(DEMO_MANIFEST))"; \
		productlines="$$(grep -c '^product:' $(DEMO_MANIFEST) || true)"; \
		if [ "$$productlines" -ne 1 ] || [ -z "$$committed" ]; then \
			echo "demo-artifact: cannot read one product: version from $(DEMO_MANIFEST)."; \
			echo "demo-artifact: the sed above needs exactly one line of the form"; \
			echo "demo-artifact:   product: \"0.1.0\""; \
			echo "demo-artifact: found $$productlines such line(s), reading '$$committed'."; \
			echo "demo-artifact: set DEMO_VERSION= to build anyway, and fix the manifest."; \
			exit 1; \
		fi; \
		version="$(DEMO_VERSION)"; \
		printf '%s' "$$version" | grep -qE '^[0-9A-Za-z][0-9A-Za-z.+-]*$$' || { \
			echo "demo-artifact: '$$version' is not a version this artefact may be named."; \
			echo "demo-artifact: it becomes a filename, an asset name and the"; \
			echo "demo-artifact: manifest's product, and seed refuses a skew it cannot"; \
			echo "demo-artifact: read. Letters, digits, dot, plus and dash, starting with"; \
			echo "demo-artifact: a letter or a digit."; exit 1; }
	@# Stage a copy rather than archiving the working tree. Two reasons, and the second
	@# is the one that would bite: the stamp below rewrites one line of the manifest,
	@# and **staging is what keeps that rewrite out of the repository**. `demo-vault/` is
	@# the source and stays byte-identical to what is committed.
	@rm -rf $(DEMO_DIST)/.stage
	@mkdir -p $(DEMO_DIST)/.stage/$(DEMO_ROOT_NAME)
	@tar -cf - -C $(DEMO_VAULT_DIR) . | tar -xpf - -C $(DEMO_DIST)/.stage/$(DEMO_ROOT_NAME)
	@# Re-read the version rather than relying on the block above: **every recipe line is
	@# its own shell**, so nothing a previous line set is in scope here, and `.SHELLFLAGS`
	@# carries `-u` so a forgotten variable is an error rather than an empty string.
	@#
	@# The `diff` below counts **both** sides of a change, so an untouched copy is 0 and a
	@# one-line rewrite is 2. A rewriting tool — a `yq -i`, a YAML round-trip — would
	@# reformat the manifest and land here as a larger number, which is the whole reason
	@# the stamp above is a `sed` over one line and not a YAML round-trip.
	@#
	@# These notes are outside the block on purpose. A `#` line inside a backslash-
	@# continued recipe **ends the logical line as far as make is concerned**, so a
	@# comment written there is not a comment: it truncates the recipe, and the shell
	@# then runs the rest of the block as a fresh command with nothing defined in it.
	@committed="$$(sed -n 's/^product: *"\([^"]*\)".*$$/\1/p' $(DEMO_MANIFEST))"; \
		version="$(DEMO_VERSION)"; \
		staged="$(DEMO_DIST)/.stage/$(DEMO_ROOT_NAME)/demo.manifest.yml"; \
		test -f "$$staged" || { \
			echo "demo-artifact: $$staged is missing, so $(DEMO_VAULT_DIR) holds no manifest."; \
			exit 1; }; \
		if [ "$$version" != "$$committed" ]; then \
			sed 's|^product: "[^"]*"$$|product: "'"$$version"'"|' $(DEMO_MANIFEST) > "$$staged"; \
		fi; \
		changed="$$(diff $(DEMO_MANIFEST) "$$staged" 2>/dev/null | grep -c '^[<>]' || true)"; \
		if [ "$$version" = "$$committed" ] && [ "$$changed" -ne 0 ]; then \
			echo "demo-artifact: staging rewrote the manifest although nothing was"; \
			echo "demo-artifact: stamped. diff found $$changed differing line(s)."; exit 1; \
		fi; \
		if [ "$$version" != "$$committed" ] && [ "$$changed" -ne 2 ]; then \
			echo "demo-artifact: stamping product \"$$committed\" -> \"$$version\" changed"; \
			echo "demo-artifact: $$changed line(s); expected exactly 2 (one before, one after)."; \
			echo "demo-artifact: the staged manifest is:"; sed 's/^/demo-artifact:   /' "$$staged"; \
			exit 1; \
		fi; \
		grep -qx 'product: "'"$$version"'"' "$$staged" || { \
			echo "demo-artifact: the staged manifest does not declare product \"$$version\"."; \
			exit 1; }; \
		echo "demo-artifact: manifest declares product \"$$version\"" \
			"(committed \"$$committed\", $$changed line(s) changed)"
	@# The two lists. `LC_ALL=C` is not decoration: without it the collation is the
	@# builder's locale, so two machines with different locales produce two different
	@# orders and the archive stops being a function of the input.
	@cd $(DEMO_DIST)/.stage && \
		find $(DEMO_ROOT_NAME) > list.raw && \
		LC_ALL=C sort list.raw > list.sorted && \
		LC_ALL=C sort -r list.raw > list.reversed && \
		test -s list.sorted || { echo "demo-artifact: $(DEMO_DIST)/.stage is empty."; exit 1; }
	@echo "demo-artifact: $$(wc -l < $(DEMO_DIST)/.stage/list.sorted | tr -d ' ') entries, staged from $(DEMO_VAULT_DIR)"
	@# Build 1: the staged tree as committed, from the sorted list.
	@cd $(DEMO_DIST)/.stage && \
		tar $(DEMO_TARFLAGS) -cf build.tar -T list.sorted && gzip -9nf build.tar && \
		sha256sum build.tar.gz | cut -d' ' -f1 > sha.first
	@# Perturb every field tar reads that is not content, then wait out gzip's one-second
	@# header granularity. `touch -t` rather than `touch -d`, which BSD touch has not got.
	@#
	@# **Ownership is perturbed too, and it has to be.** `--owner=0 --group=0
	@# --numeric-owner` was originally untested here: an earlier version of this step
	@# touched mtimes and modes only, so deleting those three flags left the digest
	@# unchanged and the mutation read as caught when nothing was checking. The reason is
	@# that a checkout is usually already owned by whoever is building it, so the flag
	@# and its absence agree until something *changes* the ownership. `chown` to a
	@# non-zero uid/gid is what makes the absence visible.
	@#
	@# It needs privilege, so it is **attempted and reported rather than assumed**: on a
	@# tree this target cannot chown, the three flags are unexercised and the output says
	@# so, because a gate that silently skips the check it exists to perform is the
	@# failure this repository has records of. The worktree and a release runner are
	@# both root; a contributor's laptop is not, and there the honest answer is "this did
	@# not run", not a green that means nothing.
	@cd $(DEMO_DIST)/.stage && \
		find $(DEMO_ROOT_NAME) -exec touch -t 203801190314.07 {} + && \
		find $(DEMO_ROOT_NAME) -type f -exec chmod 600 {} + && \
		find $(DEMO_ROOT_NAME) -type d -exec chmod 700 {} + && \
		if chown -R 12345:12345 $(DEMO_ROOT_NAME) 2>/dev/null; then \
			echo "demo-artifact: ownership perturbed to 12345:12345 for build 2"; \
		else \
			echo "demo-artifact: WARNING: cannot chown this tree, so --owner/--group/"; \
			echo "demo-artifact: --numeric-owner are UNEXERCISED by this run. They are"; \
			echo "demo-artifact: still correct — but nothing here proved it, and a green"; \
			echo "demo-artifact: reproducibility result without this line covers only mtime,"; \
			echo "demo-artifact: mode and order."; \
		fi && \
		sleep 1
	@# Build 2: the same list over the perturbed tree.
	@cd $(DEMO_DIST)/.stage && \
		tar $(DEMO_TARFLAGS) -cf build.tar -T list.sorted && gzip -9nf build.tar && \
		sha256sum build.tar.gz | cut -d' ' -f1 > sha.second
	@cd $(DEMO_DIST)/.stage && \
		if ! cmp -s sha.first sha.second; then \
			echo "demo-artifact: NOT REPRODUCIBLE. The same list over the same content gave"; \
			echo "demo-artifact: two digests once the tree's mtimes and modes changed:"; \
			echo "demo-artifact:   as committed : $$(cat sha.first)"; \
			echo "demo-artifact:   perturbed    : $$(cat sha.second)"; \
			echo "demo-artifact: so the archive carries something from the machine. Look for"; \
			echo "demo-artifact: --mtime, --mode, --owner, --group, --numeric-owner, and"; \
			echo "demo-artifact: for a gzip without -n, which stamps its own header."; \
			exit 1; \
		fi
	@# The recorded order is the sorted list, entry for entry. The digests above say the
	@# two builds agreed; this says **what** they agreed on, so a target that stopped
	@# sorting could not pass by agreeing with itself. The `awk` strips the trailing
	@# slash tar appends to a directory name and `find` does not — and it is `awk` rather
	@# than a `sed 's,/$,/,'` because a lone `$` in a Makefile recipe is make's escape
	@# character, and getting its quoting wrong truncates the script to `s,/` and leaves a
	@# half-written file for the `diff` below to compare against. That failure was measured.
	@#
	@# **This is the assertion a filesystem cannot make vacuous.** Whether the archive
	@# happens to come out sorted depends on the machine's readdir order, which is why
	@# comparing two builds of the same tree is not enough on its own: delete the sort
	@# and both builds still agree with each other, on the wrong order. Comparing the
	@# recorded order against the sorted *list* is not comparing two builds at all.
	@cd $(DEMO_DIST)/.stage && \
		tar -tf build.tar.gz | awk '{sub(/\/$$/,""); print}' > recorded.list && \
		LC_ALL=C sort recorded.list > recorded.sorted && \
		LC_ALL=C sort -u recorded.list > recorded.unique && \
		names="$$(wc -l < list.raw)"; \
		recorded="$$(wc -l < recorded.list)"; \
		distinct="$$(wc -l < recorded.unique)"; \
		if [ "$$recorded" -ne "$$names" ] || [ "$$distinct" -ne "$$names" ]; then \
			echo "demo-artifact: $$names names, $$recorded recorded entries, $$distinct of those"; \
			echo "demo-artifact: distinct. A tar given a directory recurses into it, writes"; \
			echo "demo-artifact: each child itself in readdir order, and then archives every one"; \
			echo "demo-artifact: of them a second time as a hard link to itself. --no-recursion"; \
			echo "demo-artifact: is what stops it, and this is the assertion that says so."; \
			exit 1; \
		fi; \
		if ! cmp -s recorded.list recorded.sorted; then \
			echo "demo-artifact: the recorded order is not sorted. What the archive holds,"; \
			echo "demo-artifact: against LC_ALL=C sort of exactly those names:"; \
			diff -u recorded.sorted recorded.list | sed 's/^/demo-artifact:   /'; \
			echo "demo-artifact: the order above is whatever handed tar its list, so this is"; \
			echo "demo-artifact: where the sort is missing."; \
			exit 1; \
		fi; \
		echo "demo-artifact: recorded order is LC_ALL=C sorted, $$names entries, $$names distinct"
	@# Build 3: the list **reversed**, and the positive control for the assertion above.
	@#
	@# A reversed list must produce a *different* archive, and it must be in the reversed
	@# order. That sounds like the opposite of a reproducibility check and it is the
	@# opposite of one: it proves the archive's order really is the order of the list this
	@# target handed over, which is what makes "recorded order == the sorted list" evidence
	@# about **our** sort rather than about tar's.
	@#
	@# **What this control does and does not catch**, because an earlier version of this
	@# comment claimed it caught something it does not. It catches a tar that reorders the
	@# list it was handed. It does **not** catch `--sort=name`: GNU tar's `--sort` orders
	@# what it walks, and it does not reorder a `-T` list, so adding the flag leaves the
	@# reversed archive reversed and this comparison still passes. That was measured rather
	@# than assumed, and the note at the top now says so. What `--sort=name` *would* mask
	@# is a missing `--no-recursion` — it sorts the recursion instead — which is why
	@# `--no-recursion` has its own entry-count assertion and is not left to this one.
	@# A different output name, and that is load-bearing rather than tidiness: this is
	@# the **control**, and `build.tar.gz` is the artefact. Building the control over the
	@# same path and shipping whatever is there would ship the reversed archive — a
	@# valid archive, in the wrong order, which passes every assertion above it. Only the
	@# sorted build is ever moved into place.
	@cd $(DEMO_DIST)/.stage && \
		tar $(DEMO_TARFLAGS) -cf reversed.tar -T list.reversed && gzip -9nf reversed.tar && \
		sha256sum reversed.tar.gz | cut -d' ' -f1 > sha.third && \
		tar -tf reversed.tar.gz | awk '{sub(/\/$$/,""); print}' > recorded.reversed && \
		diff -q list.reversed recorded.reversed >/dev/null || { \
			echo "demo-artifact: the reversed list did not produce a reversed archive,"; \
			echo "demo-artifact: so the recorded order is not the order of the list handed"; \
			echo "demo-artifact: over. Nothing above this assertion can be evidence about"; \
			echo "demo-artifact: the sort, because the sort is what would have reordered."; \
			exit 1; \
		}
	@cd $(DEMO_DIST)/.stage && \
		if cmp -s sha.first sha.third; then \
			echo "demo-artifact: a reversed list produced the SAME archive. That is not"; \
			echo "demo-artifact: reproducible output, it is output somebody else sorted:"; \
			echo "demo-artifact: --sort=name would do it, and the note in this Makefile says"; \
			echo "demo-artifact: the sort is this pipeline's, in the open, where a reader"; \
			echo "demo-artifact: can see it. Remove --sort=name, or move this control above"; \
			echo "demo-artifact: the assertion it disarms and say so in the note."; \
			exit 1; \
		fi; \
		echo "demo-artifact: control: a reversed list reverses the archive, so the order above is ours"
	@# The gzip header, read as bytes rather than inferred from a digest: magic, no
	@# filename, a zero timestamp. This is the claim `gzip -n` makes, asserted directly,
	@# so a future gzip that stamps stdin differently is caught here rather than by a
	@# contributor noticing two digests a year apart.
	@# `od -tx1`, not `-tu1`: stripping the separators off a decimal listing turns the
	@# bytes 0, 10, 0 into "010", which is the same string as the bytes 0, 0, 0 — and a
	@# header check that cannot tell two headers apart is not a check. Hex has no such
	@# ambiguity, and the header's own magic is already two bytes in.
	@flg="$$(od -An -tx1 -N1 -j3 $(DEMO_DIST)/.stage/build.tar.gz | tr -d ' \n')"; \
		mtim="$$(od -An -tx1 -N4 -j4 $(DEMO_DIST)/.stage/build.tar.gz | tr -d ' \n')"; \
		if [ "$$(( 0x$$flg / 8 % 2 ))" -ne 0 ] || [ "$$mtim" != "00000000" ]; then \
			echo "demo-artifact: the gzip header carries data that is not the vault's."; \
			echo "demo-artifact:   flags byte 0x$$flg, bit 3 set means a stored filename"; \
			echo "demo-artifact:   mtime bytes 0x$$mtim, expected 0x00000000"; \
			echo "demo-artifact: gzip without -n writes both. The two builds above can"; \
			echo "demo-artifact: agree for a second and still differ the next run."; \
			exit 1; \
		fi; \
		echo "demo-artifact: gzip header: no filename, no timestamp"
	@# `--numeric-owner` is the one flag an **agreement** assertion cannot reach, and
	@# saying so is more useful than pretending otherwise.
	@#
	@# Every other normalisation is caught by comparing build 1 with build 2: remove
	@# `--mtime`, `--mode`, `--owner` or `--group` and the perturbed tree produces a
	@# different digest, because build 1 saw the committed tree and build 2 did not.
	@# Removing `--numeric-owner` changes **both** builds identically — they still agree
	@# with each other — so no amount of comparing them notices. Measured: removing it
	@# leaves the target green.
	@#
	@# So it is asserted from the bytes instead, the way the gzip header above is. In a
	@# gnu header `uname` is 32 bytes at offset 265 and `gname` 32 at 297; without
	@# `--numeric-owner` tar writes the **name** it looked up, and here that is `root`,
	@# which differs between two machines that disagree about `/etc/passwd` and matches
	@# nothing about the vault. `tr -d '\0'` and a non-empty result is the failure.
	@#
	@# The perturbation's `chown` above is what makes this meaningful rather than
	@# incidental: a tree owned by root already yields `root`, so without a chown the
	@# check would be asserting about a tree nobody changed.
	@# Read from `build.tar.gz` rather than `build.tar`, and that is deliberate on two
	@# counts. `gzip -f` **deletes its input**, so the tar is gone by this point — an
	@# earlier version of this check read `build.tar` and failed with `No such file or
	@# directory` on every run. And `gzip -dc` reads the bytes that actually ship, so
	@# this asserts about the artefact rather than about an intermediate that the
	@# compression might have altered.
	@#
	@# **Decompressed to a file first, because `od -N32` closes the pipe.** `od` stops
	@# reading after 32 bytes and exits, `gzip` dies of SIGPIPE on the write it had
	@# already started, and `.SHELLFLAGS` carries `-o pipefail` — so the recipe failed
	@# with 141 on every run. That is the same defect the `make -n check` guard below
	@# documents for `grep -q`, reached a second way: **a short-reading consumer in a
	@# pipeline under `pipefail` reports the killed status, not the answer.** Two ways
	@# to read a few bytes out of a compressed file, and both pipelines are wrong; the
	@# file is the only form that is not.
	@gzip -dc $(DEMO_DIST)/.stage/build.tar.gz > $(DEMO_DIST)/.stage/probe.tar
	@# **Hex, not `od -c`, because the obvious spelling of this check does not work.** The
	@# first version read the field with `od -An -c` and stripped NULs with
	@# `tr -d ' \0'`, and it reported owner names on a build that *had* `--numeric-owner`.
	@# `tr` operates on characters and a shell argument cannot carry a NUL, so `\0` there
	@# means backslash and `0`; every NUL survived, `od -c` had already rendered them as
	@# the four characters `\0`, and the result was non-empty no matter what was in the
	@# field. The check passed for its own output. A vendor's name is `root` — the field
	@# is 32 bytes and a short name is followed by NUL padding — so "the field is not
	@# empty" and "the field is all NUL" are the same question asked two ways, and only
	@# the second has an answer.
	@#
	@# Hex sidesteps it: 32 bytes is exactly 64 hex characters, all of them `0`, and
	@# stripping the digit `0` leaves nothing. `-tx1` rather than a decimal listing for
	@# the reason the gzip check above gives — stripping separators off a decimal listing
	@# turns the bytes 0, 10, 0 into `010`, which is the same string as 0, 0, 0.
	@#
	@# **`-v`, and without it this check reports a violation on a correct archive.** `od`
	@# collapses a run of identical bytes to one line plus a `*`, so 32 zero bytes print as
	@# sixteen `0`s and a `*`. The `*` is not whitespace, so it survived the separator
	@# strip, `tr -d 0` left it, and "the field is not all zero" was reported against a
	@# build that did carry `--numeric-owner`. A repeat marker is the opposite of what a
	@# byte-level assertion wants, and it is silent: the output looks like data.
	@uname="$$(od -An -v -tx1 -j265 -N32 $(DEMO_DIST)/.stage/probe.tar | tr -d ' \n')"; \
		gname="$$(od -An -v -tx1 -j297 -N32 $(DEMO_DIST)/.stage/probe.tar | tr -d ' \n')"; \
		if [ -n "$$(printf '%s' "$$uname" | tr -d 0)" ] || \
		   [ -n "$$(printf '%s' "$$gname" | tr -d 0)" ]; then \
			echo "demo-artifact: the tar header carries owner NAMES, not numbers."; \
			echo "demo-artifact:   uname=\"$$uname\" gname=\"$$gname\""; \
			echo "demo-artifact: tar wrote the name it resolved uid 0 to. That name is read"; \
			echo "demo-artifact: from the BUILDING machine's passwd database, so two machines"; \
			echo "demo-artifact: disagreeing about root produce two different artefacts from"; \
			echo "demo-artifact: one vault. --numeric-owner is what clears those fields, and"; \
			echo "demo-artifact: comparing two builds cannot detect its absence because both"; \
			echo "demo-artifact: builds change together — this reads the bytes instead."; \
			exit 1; \
		fi; \
		echo "demo-artifact: tar header: owner names cleared (--numeric-owner exercised)"
	@# `make check` must not reach this target. Read out of make's own dry run rather than
	@# asserted in a comment, because the failure this prevents — a release target wired
	@# into the gate — is the kind that is one convenience edit away and has nothing to do
	@# with the artefact at all. `check` runs before this target exists in any sequence
	@# anyone has to wait for, so this reads nothing off the disk but the Makefile.
	@# Captured into a variable and matched with `case`, **not** piped into `grep -q`. The
	@# pipeline version was written first and silently did nothing: `grep -q` exits the
	@# instant it matches, which closes the pipe, which kills `make -n` with SIGPIPE, and
	@# `.SHELLFLAGS` carries `-o pipefail`, so the pipeline reported the *killed* status and
	@# the guard never fired. Wiring this target into `check` and seeing it stay green is
	@# how that was found. A gate that reports green because its own pipeline broke is
	@# worse than no gate, and this repository has three records of that.
	@gate="$$($(MAKE) --no-print-directory -n check 2>/dev/null || true)"; \
		case "$$gate" in \
		*demo-artifact*) \
			echo "demo-artifact: \`make check\` now reaches this target."; \
			echo "demo-artifact: the delivery plan keeps the artefact and the network out of"; \
			echo "demo-artifact: the gate, and \`check\` is what CI runs on every push. Take the"; \
			echo "demo-artifact: dependency back out rather than working around this check."; \
			exit 1 ;; \
		esac
	@mkdir -p $(DEMO_DIST)
	@mv $(DEMO_DIST)/.stage/build.tar.gz $(DEMO_ARCHIVE)
	@rm -rf $(DEMO_DIST)/.stage
	@echo "demo-artifact: $(DEMO_ARCHIVE)"
	@echo "demo-artifact:   $$(wc -c < $(DEMO_ARCHIVE) | tr -d ' ') bytes, sha256 $$(sha256sum $(DEMO_ARCHIVE) | cut -d' ' -f1)"
	@echo "demo-artifact: reproducible: three builds over one perturbed tree and one reversed list, one digest"

.PHONY: check
check: ## Mandatory gate: fmt-check, vendor-check, css, templ, build, vet, lint, a11y, demo-check, test
	@echo "==> format check"
	@diffs="$$($(GOENV) golangci-lint fmt --diff 2>/dev/null)"; \
		if [ -n "$$diffs" ]; then echo "unformatted files:"; echo "$$diffs"; \
		echo "run: make fmt"; exit 1; fi
	@# `vendor-check` sits here, immediately after the format check and before
	@# `css`, for two reasons and the first is the one that decides it.
	@#
	@# **It reads a committed file, not a build output**, exactly as the format
	@# check above does. Everything from `css` down is about to *produce* the
	@# tree the binary embeds; `vendor-check` is about whether the bytes going
	@# into it are the bytes the repository claims to ship. A property of the
	@# tree is checked before anything is built from the tree, because that is
	@# when finding it is cheap.
	@#
	@# And it is the same argument that puts `css` and `templ` before `build`:
	@# `stage-assets` copies `static/vendor/` into `static/dist/`, `internal/web`
	@# embeds `all:static/dist`, and `build` therefore embeds **whatever vendored
	@# bytes are on disk** into the shipped binary. A `build` that embedded a
	@# swapped blob would produce a working binary serving somebody else's
	@# JavaScript, and every gate below this line would report green about it.
	@echo "==> vendor-check"; $(MAKE) --no-print-directory vendor-check
	@echo "==> css";    $(MAKE) --no-print-directory css
	@echo "==> templ";  $(MAKE) --no-print-directory templ
	@echo "==> build";  $(MAKE) --no-print-directory build
	@echo "==> vet";    $(MAKE) --no-print-directory vet
	@echo "==> lint";   $(MAKE) --no-print-directory lint
	@echo "==> a11y";   $(MAKE) --no-print-directory a11y
	@# `demo-check` reads the **built** stylesheet for the callout's contrast, which is
	@# why it sits below `css` and `templ` and above nothing that would rebuild them.
	@# It is a Go test run through the shell guard below, and the guard is what makes
	@# `go test -run` matching nothing an exit status of 0 rather than a pass.
	@echo "==> demo-check"; $(MAKE) --no-print-directory demo-check
	@echo "==> test";   $(MAKE) --no-print-directory test
	@echo "==> all checks passed"

.PHONY: run
run: ## Run the dev server on :8080
	$(GO) run ./cmd/server

.PHONY: tidy
tidy: ## Tidy and verify modules
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: deps
deps: ## List direct and outdated dependencies
	$(GO) list -m -f '{{if not .Indirect}}{{.Path}} {{.Version}}{{end}}' all
	$(GO) list -u -m all 2>/dev/null | grep -v '^$' || true

.PHONY: vuln
vuln: ## Scan for known vulnerabilities
	$(GOENV) go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: ci
ci: lint-verify check ## What the Go gate in CI runs

# --- GitHub plumbing ---------------------------------------------------------
# Needs the network and a token, so none of it is wired into `check`.

.PHONY: labels
labels: ## Create or update every label in .github/labels.yml
	SEMIPLANE_REPO=$(LABEL_REPO) ./scripts/sync-labels.sh apply

.PHONY: labels-prune
labels-prune: ## Delete repo labels that are NOT in .github/labels.yml
	SEMIPLANE_REPO=$(LABEL_REPO) ./scripts/sync-labels.sh prune

.PHONY: labels-check
labels-check: ## Fail if the repo's labels and .github/labels.yml disagree
	SEMIPLANE_REPO=$(LABEL_REPO) ./scripts/sync-labels.sh check

.PHONY: lint-workflows
lint-workflows: ## Lint the GitHub Actions workflows
	actionlint

# --- Docs site ---------------------------------------------------------------
# Deliberately NOT part of `check`. The Go gate must not go red because a docs
# toolchain is missing; the `docs` job in CI is what gates the site. Contrast
# `css` and `templ` in the UI plan, which `check` does depend on -- those produce
# runtime assets the binary serves, unlike Hugo.

SITE_PLANS := $(SITE_DIR)/assets/plans
PLAN_SRC   := .opencode/plans

.PHONY: site-plans
site-plans: ## Stage the committed design records where Hugo can read them
	@mkdir -p $(SITE_PLANS)
	@cp $(PLAN_SRC)/*.md $(SITE_PLANS)/
	@echo "staged $$(ls -1 $(SITE_PLANS)/*.md | wc -l) design records"

.PHONY: site
site: site-plans ## Build the docs site into $(SITE_PUBLISH)
	cd $(SITE_DIR) && hugo --minify --gc --baseURL "$(BASEURL)"

.PHONY: site-check
site-check: site-plans ## Build the docs site, failing on any Hugo warning or broken link
	cd $(SITE_DIR) && hugo --minify --gc --panicOnWarning \
		--destination "$(CURDIR)/$(SITE_DIR)/.site-check" --baseURL "$(BASEURL)"
	./scripts/check-site-links.sh $(SITE_DIR)/.site-check "$(BASE_PATH)"
	./scripts/check-site-structure.sh $(SITE_DIR)/.site-check
	rm -rf $(SITE_DIR)/.site-check

.PHONY: site-serve
site-serve: site-plans ## Serve the docs site locally with live reload
	cd $(SITE_DIR) && hugo server --buildDrafts

.PHONY: clean
clean: ## Remove build and test artifacts
	rm -rf bin coverage.out .playwright-mcp test-results
	rm -rf $(DEMO_DIST)
	rm -rf $(SITE_PUBLISH) $(SITE_CACHE) $(SITE_DIR)/.site-check $(SITE_PLAN)S
	rm -f $(SITE_DIR)/.hugo_build.lock
	rm -rf $(TOOLBIN)
	rm -rf internal/web/static/dist
	rm -f $(shell find internal -name '*_templ.go' 2>/dev/null)
	$(GO) clean -testcache
