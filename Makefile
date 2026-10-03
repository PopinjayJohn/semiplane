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
	./internal/httpapi/events ./internal/httpapi/theme)

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
	./internal/web/components/chat ./internal/web/components/live)

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
A11Y_TESTS := Contrast|Structural|Vocabulary|RepresentsTheRoutes|Target|TvMode|TvRail|PreferencesAreNever|FocusIndicator|BuiltStylesheet|ScanCoverage|InheritedTypeSize|HoverRule|ProductName|ViewModelLayers|LoadFailureIs|CampaignFallsBack|SignOutTarget|EntitledToAssert|DegradedWarning|DegradedNotice|HeadingLevels|EveryRoute|Identifier|IsTheStatePackageType|ExactlyOneH1|SkipLink

.PHONY: a11y
a11y: ## Run the UI §10.1/§10.2/§10.6 accessibility gate
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

.PHONY: check
check: ## Mandatory gate: fmt-check, vendor-check, css, templ, build, vet, lint, a11y, test
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
	rm -rf $(SITE_PUBLISH) $(SITE_CACHE) $(SITE_DIR)/.site-check $(SITE_PLAN)S
	rm -f $(SITE_DIR)/.hugo_build.lock
	rm -rf $(TOOLBIN)
	rm -rf internal/web/static/dist
	rm -f $(shell find internal -name '*_templ.go' 2>/dev/null)
	$(GO) clean -testcache
