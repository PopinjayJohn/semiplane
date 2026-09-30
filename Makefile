SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BINARY   := bin/server
PKGS     := ./...
GO       := . /etc/profile.d/go.sh && go

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install the Go toolchain targets (gopls, golangci-lint)
	$(GO) install golang.org/x/tools/gopls@latest
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: fmt
fmt: ## Apply formatters (gofumpt + gci + golines)
	. /etc/profile.d/go.sh && golangci-lint fmt

.PHONY: build
build: ## Compile all packages
	$(GO) build $(PKGS)

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKGS)

.PHONY: lint
lint: ## Run golangci-lint
	. /etc/profile.d/go.sh && golangci-lint run $(PKGS)

.PHONY: lint-fix
lint-fix: ## Auto-fix fixable lint and format issues
	. /etc/profile.d/go.sh && golangci-lint run --fix $(PKGS)
	$(MAKE) fmt

.PHONY: lint-verify
lint-verify: ## Validate .golangci.yml against the v2 schema
	. /etc/profile.d/go.sh && golangci-lint config verify

.PHONY: test
test: ## Run tests
	$(GO) test -race -count=1 $(PKGS)

.PHONY: cover
cover: ## Run tests and report coverage
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-integration
test-integration: ## Run integration-tagged tests
	$(GO) test -race -count=1 -tags=integration $(PKGS)

.PHONY: check
check: ## Mandatory gate: fmt-check, build, vet, lint, test
	@echo "==> format check"
	@diffs="$$(. /etc/profile.d/go.sh && golangci-lint fmt --diff 2>/dev/null)"; \
		if [ -n "$$diffs" ]; then echo "unformatted files:"; echo "$$diffs"; \
		echo "run: make fmt"; exit 1; fi
	@echo "==> build";  $(MAKE) --no-print-directory build
	@echo "==> vet";    $(MAKE) --no-print-directory vet
	@echo "==> lint";   $(MAKE) --no-print-directory lint
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
	. /etc/profile.d/go.sh && go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: clean
clean: ## Remove build and test artifacts
	rm -rf bin coverage.out .playwright-mcp test-results
	$(GO) clean -testcache

.PHONY: ci
ci: lint-verify check ## What CI runs
