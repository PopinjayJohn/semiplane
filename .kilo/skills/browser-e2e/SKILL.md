---
name: browser-e2e
description: Use when verifying that a semiplane web change actually works in a browser, or when debugging a UI/HTTP behaviour. Covers driving the Playwright MCP against a locally running server and reading its output correctly.
---

# Browser Verification with Playwright MCP

The `playwright` MCP server is configured in `kilo.json` and pinned to
`@playwright/mcp` 0.0.83 with a pre-installed Chromium. It exposes 25
`browser_*` tools. Use it to confirm a change end to end instead of assuming
the handler works.

## Before driving the browser

1. Start the server. `make run` serves on `:8080`.
2. Confirm it answers before opening a page: `curl -fsS localhost:8080/healthz`.
3. The server is launched with `--isolated`, so browser state is per-session and
   nothing leaks between test runs. Logins must be re-done each time.

## Core loop

1. `browser_navigate` to `http://localhost:8080/<path>`.
2. `browser_snapshot` for an accessibility-tree view of the page. Prefer this
   over a screenshot to find elements and to assert on text.
3. Act with `browser_click`, `browser_type`, `browser_fill_form`,
   `browser_select_option`, `browser_press_key`.
4. Assert with `browser_find` (text must be present) or `browser_evaluate`
   (arbitrary JS — requires approval).
5. Capture with `browser_take_screenshot` when a visual matters, or
   `browser_console_messages` and `browser_network_requests` when something
   broke. Console errors and 4xx/5xx are the fastest failure signal.
6. `browser_close` when finished to free the process.

## Identifying elements

- Selectors default to the `data-testid` attribute.
- Add `data-testid` to elements under test in templates. Do not assert on CSS
  classes or DOM structure — they are implementation detail and will churn.
- Good ids name intent: `data-testid="campaign-create-submit"`, not
  `data-testid="btn1"`.

## Transport detail that bites

Playwright MCP 0.0.83 speaks **newline-delimited JSON** over stdio, *not* the
older `Content-Length`-framed MCP stdio encoding. If you script the server
directly, write one JSON object per line. Handshake:

```
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"v","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
```

## Prerequisites the container must satisfy

Chromium will not launch as root without `--no-sandbox`. The error is
`Running as root without --no-sandbox is not supported`, and it surfaces through
the MCP as the unhelpful `Target page, context or browser has been closed`.

That is why `kilo.json` passes `--no-sandbox`. It is a real security tradeoff:
it is correct for a disposable root-owned dev container, and it should be
**removed** for a rootless deployment or any host where browser isolation
matters. System libraries are already present; `ldd` on the Chromium binary
reports nothing missing.

Verify the browser works before debugging application code:

```bash
curl -fsS localhost:8080/healthz   # server up
# then: browser_navigate -> browser_snapshot should show the response body
```

## Scope and safety

- `browser_run_code_unsafe` executes arbitrary Playwright code and
  `browser_evaluate` executes arbitrary page JS. Both are set to `ask`. Do not
  reach for them when a normal tool answers the question.
- `browser_file_upload` is `ask`; it can read files from disk.
- The server is `--isolated` and file access is confined to the workspace root.
  Do not pass `--allow-unrestricted-file-access`.

## What belongs in the repo

Interactive MCP use is for *diagnosis*. Anything that must keep running in CI
belongs in a committed test: Playwright's own test runner, or Go tests driving
`httptest`. Do not make CI depend on an agent driving a browser.
