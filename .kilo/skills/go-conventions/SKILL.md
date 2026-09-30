---
name: go-conventions
description: Reference for writing idiomatic Go in semiplane - package layout, error handling, context and cancellation, HTTP handler patterns, database access, and test structure. Use when adding or refactoring Go code, or when deciding where a new file belongs.
---

# Go Conventions for semiplane

Semiplane is a self-hosted TTRPG wiki and VTT. Go 1.27, `net/http` with the
standard library router, and `log/slog` for structured logging.

## Layout

```
cmd/server/main.go        # thin: parse config, build server, handle signals
internal/config/          # environment parsing, no dependencies
internal/domain/          # pure business types and rules, no I/O
internal/store/           # persistence, owns SQL
internal/httpapi/         # handlers, routing, middleware
internal/web/             # templates and static assets
```

Dependencies point inward: `httpapi` may import `domain` and `store`;
`domain` imports nothing from the project. A package that needs a database
does not belong in `domain`.

## Errors

- Wrap with `%w` and add context: `fmt.Errorf("load campaign %s: %w", id, err)`.
- Sentinel errors are `Err`-prefixed: `var ErrNotFound = errors.New("not found")`.
- Handle at the boundary, log once. Do not log and re-return the same error.
- `errors.Is` for sentinels, `errors.As` for typed errors.
- Config parsing returns errors; `main` is the only place that exits non-zero.

## Context and cancellation

- `ctx context.Context` is the first parameter of anything that does I/O.
- Never store a context in a struct (`containedctx` enforces this).
- Never call `context.Background()` inside a call chain — derive with
  `context.WithoutCancel(ctx)` when you intentionally drop cancellation
  (this is what graceful shutdown does).
- Every `exec.Command` becomes `exec.CommandContext`.

## HTTP

- Use the Go 1.22+ method-and-pattern mux: `mux.HandleFunc("GET /campaigns/{id}", ...)`.
- Path values via `r.PathValue("id")` — do not re-parse `r.URL`.
- Handlers parse, delegate to `domain`/`store`, then write. No business rules
  in handlers.
- Always set a status before writing a body. Encode errors as JSON with a
  stable shape so the frontend and Playwright tests can assert on them.
- Outbound HTTP calls need `http.NewRequestWithContext` (`noctx`).
- Middleware is `func(http.Handler) http.Handler` and composes by wrapping.

## Database

- One `*sql.DB` per process, created once, never copied. Set
  `db.SetMaxOpenConns`, `db.SetMaxIdleConns`, `db.SetConnMaxLifetime`.
- Always `defer rows.Close()` and check `rows.Err()` after the loop
  (`rowserrcheck`).
- Use `db.BeginTx(ctx, nil)`, and `defer tx.Rollback()` — a rollback after a
  successful commit is a harmless no-op.
- Migrations are ordered, forward-only, and live under `internal/store/migrations/`.
  Never edit a migration that has shipped; add a new one.
- Parameterize every query. `gosec` G201/G202 flag string-concatenated SQL.

## Testing

- Standard library `testing`. Table-driven with `t.Run` subtests.
- `t.Context()` for cancellation, `t.TempDir()` for filesystem, `t.Setenv` for env.
- Package-internal tests use `<pkg>_test`; black-box tests use `<pkg>_test` package.
- Prefer `httptest.NewServer` over mocks when testing handlers end to end.
- `testifylint` is enabled, so if testify is used, its assertions must be idiomatic.

## Comments

- Comment the *why*, not the *what*. Godoc comments on every exported
  identifier, starting with the identifier name and ending in a period (`godot`).
- `//nolint:` needs a reason: `//nolint:gosec // constant-time compare, see RFC 6962`.
