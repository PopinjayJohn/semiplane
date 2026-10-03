---
name: go-quality-gate
description: Use when writing, changing, or reviewing any Go code in semiplane. Enforces the mandatory verify loop (fmt, build, vet, lint, test) so no change lands unverified. Also use before declaring any Go task complete.
---

# Go Quality Gate

No Go change in this repo is complete until the full gate passes. Run these in
order from the repo root. The shell here does not source `go.sh` by default, so
source it first or use the Makefile targets.

## The loop

```bash
. /etc/profile.d/go.sh

gofmt -l .                 # 1. formatting: must print nothing
go build ./...             # 2. compiles
go vet ./...               # 3. standard vet
golangci-lint run ./...    # 4. full lint gate
go test ./...              # 5. tests
```

Or `make check`, which runs the same sequence and stops at the first failure.

## Rules

1. **Never skip the gate.** If a step fails, fix it before reporting done. Do
   not report success on a partially passing gate.
2. **Never silence a linter to make it pass** unless the suppression is
   genuinely correct. `//nolint:` requires an explanation and a specific
   linter (`nolintlint` enforces both). Prefer fixing the code.
3. **`golangci-lint` is the source of truth for style**, not `gofmt`. The
   config uses `gofumpt` with extra rules plus `gci` import ordering. Run
   `golangci-lint fmt` to apply formatting, not `gofmt -w`.
4. **Config is verified, not assumed.** After editing `.golangci.yml`, run
   `golangci-lint config verify` — the file uses golangci-lint **v2** schema,
   where `linters` and `formatters` are separate top-level sections.
5. **Unwrapping and error wrapping** uses `%w` and `fmt.Errorf`; errors are
   never discarded or replaced with bare strings.

## What the gate catches in this repo

| Symptom | Cause |
| --- | --- |
| `Non-inherited new context` | `context.WithoutCancel` / `context.WithTimeout` needed, not `context.Background()` deep in a call chain (`contextcheck`) |
| `exitAfterDefer` | `os.Exit` after `defer` in the same func — use the `main() { if err := run(); ... }` pattern |
| `missing whitespace above this line` | `wsl_v5` wants blank lines around blocks; run `golangci-lint fmt` |
| `func X is unused` | dead helper; delete it or call it |
| `nonamedreturns` / `varnamelen` | name the returns, keep names >= 3 chars (`r`, `w`, `err`, `ctx` are allowlisted) |

## Before touching a new dependency

- Check it is already in `go.mod` before adding a second library for the same job.
- Prefer the standard library. Reach for a dependency only with a stated reason.
- Run `go mod tidy` and confirm `go.mod`/`go.sum` are the only files changed.
- Security-sensitive additions (auth, crypto, parsing, SQL) must pass `gosec`
  without an `excludes` entry added to `.golangci.yml`.
