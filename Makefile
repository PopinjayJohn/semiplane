# Semiplane Makefile — the contract. CI is a thin wrapper over these targets.
# See docs/ci.md for the tier mapping. Run `make help` for the list.

GO ?= go
BINARY := bin/semiplane
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)

.PHONY: help lint test golden fuzz integration e2e build web embed cross contract doctor clean

help: ## list targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk -F':.*?## ' '{printf "  %-12s %s\n", $$1, $$2}'

lint: ## gofmt + vet (+ golangci-lint if present); web typecheck
	@test -z "$$(gofmt -l cmd internal pkg tools 2>/dev/null)" || (gofmt -l cmd internal pkg tools 2>/dev/null; echo "gofmt: files above need formatting"; exit 1)
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "lint: golangci-lint not installed, skipping (vet passed)"; fi
	@if [ -d web/node_modules ]; then (cd web && pnpm lint); else echo "lint: web/node_modules missing, skipping svelte-check (run: corepack enable && cd web && pnpm install)"; fi

test: ## go tests (+ web vitest); -race when a C toolchain exists (always in CI)
	@if command -v cc >/dev/null || command -v gcc >/dev/null; then \
	  CGO_ENABLED=1 $(GO) test -race ./...; \
	else \
	  echo "test: no C compiler — running WITHOUT -race (CI always races)"; \
	  $(GO) test ./...; \
	fi
	@if [ -d web/node_modules ]; then (cd web && pnpm test); else echo "test: web/node_modules missing, skipping vitest"; fi

golden: ## markdown→HTML golden files (thin slice: none yet, gate stays wired)
	@if [ -d testdata/golden ]; then $(GO) test -run TestGolden ./...; else echo "golden: no testdata/golden yet — pass"; fi

fuzz: ## short fuzz runs for dice grammar + expr hook compile (30s smoke; release runs -fuzztime=10m)
	$(GO) test -run=NONE -fuzztime=15s -fuzz=FuzzParse ./internal/dice/
	$(GO) test -run=NONE -fuzztime=15s -fuzz=FuzzCompileHook ./internal/rules/

integration: ## httptest suite against testdata/vaults
	$(GO) test -count=1 ./internal/...

e2e: ## Playwright suite: make e2e [project=smoke] (default: smoke)
	$(MAKE) build
	@if [ ! -d e2e/node_modules ]; then (cd e2e && pnpm install); fi
	e2e/scripts/run.sh "$(or $(project),smoke)"

build: web embed ## full chain: web → embed → single binary
	CGO_ENABLED=0 $(GO) build -o $(BINARY) ./cmd/semiplane
	@echo "built $(BINARY)"

web: ## build the SPA into web/dist
	@if [ ! -d web/node_modules ]; then (cd web && pnpm install); fi
	@(cd web && pnpm build)
	@test -f web/dist/index.html || (echo "web: web/dist/index.html missing after build"; exit 1)

embed: ## copy web/dist into internal/webui/dist (fails loudly without a build)
	@test -f web/dist/index.html || (echo "embed: run 'make web' first (web/dist/index.html missing)"; exit 1)
	@cp -r web/dist/. internal/webui/dist/
	@echo "embedded $$(ls internal/webui/dist | wc -l) entries"

cross: ## cross-compile matrix (linux/darwin/windows × amd64/arm64, CGO=0, ±plugin tag)
	@mkdir -p dist
	@for os in linux darwin windows; do \
	  for arch in amd64 arm64; do \
	    for tags in "" "plugin_dnd5e"; do \
	      out="dist/semiplane-$$os-$$arch$$( [ -n "$$tags" ] && echo '-plugins' )$$( [ "$$os" = windows ] && echo '.exe' )"; \
	      echo "building $$out (tags='$$tags')"; \
	      CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -tags "$$tags" -o "$$out" ./cmd/semiplane || exit 1; \
	    done; \
	  done; \
	done

contract: ## validate live routes against openapi.yaml (thin slice: path presence)
	@$(MAKE) -s build >/dev/null
	@./scripts/contract.sh

doctor: ## smoke: doctor bundle against a scratch vault
	@$(MAKE) -s build >/dev/null
	@./$(BINARY) doctor

clean: ## remove build outputs (never the vault)
	rm -rf bin dist web/dist internal/webui/dist/*.html internal/webui/dist/assets
	@echo "clean (kept internal/webui/dist/placeholder.html)"
