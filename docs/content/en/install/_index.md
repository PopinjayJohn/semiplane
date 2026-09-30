---
title: "Install"
description: "Run semiplane from source, from a release binary, or behind a reverse proxy."
lede: "One binary, one SQLite file, and a directory your campaigns can point at. There is no external service to sign up for."
weight: 10
---

Everything below runs on a machine with a Go toolchain. semiplane is a single
statically linked binary with no runtime dependencies beyond a writable
directory and a C library from your distribution.

## From source

```bash
git clone https://github.com/PopinjayJohn/semiplane.git
cd semiplane
make run
```

That serves on `http://localhost:8080`. There is nothing to configure first: the
server starts and answers `/healthz` before any database or campaign exists, and
that is the intended first milestone — a running, empty instance you can log
into and then fill.

## Accounts

There is no interface to create the first user yet, so there is nothing to do at
this step. When it lands it will be a CLI subcommand rather than an environment
variable, so that no password ever sits where `ps` and shell history can read
it:

```bash
# planned — not implemented yet
semiplane admin create --username you
```

## Configuration

Read from the environment, with defaults chosen so that an empty environment
works:

| Variable | Default | Meaning |
|---|---|---|
| `SEMIPLANE_ADDR` | `:8080` | Listen address. Keep it on a private interface behind a proxy. |
| `SEMIPLANE_DATABASE_URL` | `file:semiplane.db` | SQLite DSN. `sqlite://path` is accepted and translated. A DSN that sets a pragma is **rejected**: `journal_mode`, `busy_timeout` and `foreign_keys` are what the server depends on, and a configured one would silently turn them off. |
| `SEMIPLANE_CONTENT_ROOT_BASE` | `/var/lib/semiplane/vaults` | Directory campaign vaults live beneath. **Must be absolute** — a campaign's content root is stored absolute, so a relative base would resolve differently after a restart from a different working directory. |
| `SEMIPLANE_READ_TIMEOUT` | `10s` | Per-request read timeout. |
| `SEMIPLANE_WRITE_TIMEOUT` | `30s` | Per-request response-write timeout, measured on the socket. |
| `SEMIPLANE_HANDLER_TIMEOUT` | `25s` | Per-handler budget. A handler that exceeds it gets a `504`. Separate from the write timeout because that one bounds writing to the socket and this one bounds a handler that has been given the connection and has not returned — a handler blocked on a database read is the case that matters. |
| `SEMIPLANE_SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests on `SIGINT`/`SIGTERM`. |
| `SEMIPLANE_TRUSTED_PROXIES` | *(empty)* | Comma-separated CIDRs or addresses whose forwarding headers are believed. Empty means believe none, which is correct when the server is reachable directly: an empty list is the only default under which a caller cannot forge its own address in the access log. |
| `SEMIPLANE_ENV` | `development` | `production` changes what is logged and served. |

## Behind a reverse proxy

Put TLS and a hostname in front of it. Two things matter specifically:

**Use HTTP/2.** Browsers allow roughly six concurrent connections per origin over
HTTP/1.1, and every long-lived stream holds one permanently. HTTP/2 removes that
ceiling entirely. Without it, a tabletop with a live WebSocket and a couple of
event streams is a handful of connections from the ceiling — which is why
semiplane confines server-sent events to exactly two places and never uses them
on the game route at all.

**Do not buffer.** The game route upgrades to a WebSocket and the event routes
stream; a proxy that buffers responses will hold both open until they time out.

The trust boundary and what a reverse proxy may and may not see are covered under
[security]({{ "operating/security/" | relURL }}).

## Verify the install

```bash
curl -fsS http://localhost:8080/healthz   # {"status":"ok"}
curl -fsS http://localhost:8080/readyz    # {"status":"ready"}
```

`/readyz` is the one that reflects whether storage is actually usable; `/healthz`
only says the process is alive. A difference between them is a campaign whose
content root has gone missing, which is reported in the interface as a persistent
banner rather than a transient toast — an ongoing condition, not an event.

## What is not here yet

- Releases are not published, so there is no prebuilt binary and no container
  image. Building from source is the supported path.
- `admin create`, campaign registration, and the content root do not exist yet.
  The server currently serves `/healthz`, `/readyz`, and a 404 for everything
  else.
- Tag-triggered release workflows are deliberately out of scope until there is a
  version to release.

The [roadmap]({{ "roadmap/" | relURL }}) says which phase each of these lands in.
