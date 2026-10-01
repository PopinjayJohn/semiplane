---
title: "0025 — The first account is a command, not an environment variable"
description: "`semiplane admin create` makes the first account, and no configuration value can bootstrap one; a password in the environment is a password in /proc."
lede: "A bootstrap environment variable puts the instance's first credential somewhere it cannot be withdrawn from — the process environment, the container config, every crash dump. A command an operator runs deliberately, once, inside the container, is withdrawable and auditable."
weight: 210
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane is self-hosted and starts with no accounts. Something has to create the first one,
and every self-hosted product solves this the same way: a configuration value that, when
present at startup, creates an administrator and is then ignored.

The value is the problem. A password in the environment is readable from
`/proc/<pid>/environ` by anything running as the same user, it is in the container's
configuration wherever that configuration is written, it is in every crash report and core
dump that captures the environment, and it is in the shell history or the CI log of whoever
set it. Worse, it is withdrawable only by removing it — and removing it does not remove the
copies. A self-hoster who reads their own deployment configuration and finds an admin password
there has learned something true and unwelcome about their setup, and no amount of
documentation saying otherwise changes what they read.

There is a second, quieter problem. A bootstrap that runs during startup has to be
distinguishable from every subsequent startup, which means either a sentinel the operator sets
again by hand or a row the code has to reason about. And it has to work against a database
that has just been migrated, so it is running writes at a point where a failure is
indistinguishable from a migration failure.

## Decision

The first account is created by **`semiplane admin create --username NAME`**, run deliberately
against a stopped server. There is no bootstrap environment variable, and there will not be
one.

Two properties follow from the command being a command:

- **It holds the process's single-instance slot.** `store.Open` refuses a second handle per
  process, so a subcommand cannot be issued against a running server. That is the mechanism
  rather than a check: there is no flag to get wrong, and the migration runner is never
  applying migrations underneath a live process.
- **It is retryable and not silently idempotent.** A username already taken answers
  `ErrConflict` and says so. An operator who interrupts the command re-runs it and learns
  whether the first attempt committed, rather than discovering a duplicate account later.

The password is read from the terminal when `--password` is omitted, so the secret does not
reach the shell history, the process list, or a `docker exec` invocation preserved in a
terminal scrollback. That read is line-based and echoes, which is a real cost: a proper no-echo
read needs either `golang.org/x/term` — a new dependency for one prompt — or a termios
syscall that is not portable. The trade is deliberate and the cost is paid by an operator
typing into a terminal they control, on a machine they administer, for an account they are
creating. The operator is told which invocation to use.

When stdin is not a terminal, the command refuses and says to pass `--password`, rather than
reading an empty password and creating an account nobody can sign in to.

`--admin` is opt-in. The default account can play, and becomes a campaign GM when a campaign
names it — which is the whole of what a single-operator instance needs.

### The CLI reuses the HTTP registration path

`admin campaign add` calls `campaigns.NewRegistrar(...).Register`, the same function the
campaign-registration route calls. Not a second implementation for the CLI: an absolute content
root, a 0700 directory, an opened `os.Root`, a validated slug and a seeded owner are the
tenancy rules, and a CLI path that reimplemented them would be a second copy that drifts in
exactly the properties that are expensive to get wrong. The route arrives in P3; the shared
function arrives here so it has somewhere to be called from.

## Consequences

- First-run setup is two commands rather than one environment variable, and an operator has
  to reach a shell in the container. For the self-hosted audience that is the audience, and
  `make run` plus a documented command is the local path.
- The installation guide has to say this, and the README's quickstart has to carry it. A
  reader who cannot find the first-account step cannot start, so this is documentation that
  P11 must get right rather than a detail that can be deferred.
- `admin create` is a usage error, not a crash, for every argument mistake. Exit code 2 with
  the usage text, so a script can tell a typo from a fault.
- The password hash is computed before the store is opened, so a misconfigured database does
  not cost half a second of PBKDF2 for nothing.
- The subcommand sets no instance-wide state, so removing the account later is a `DeleteUser`
  and nothing else — which the `RESTRICT` on `campaign_members` will refuse if the account owns
  a campaign. That refusal is correct and is a separate decision from this one.

## Alternatives considered

**A bootstrap password in the environment, consumed once.** Rejected for the reasons in
Context: the secret is copied somewhere it cannot be recalled from, and "consumed once" is a
state the configuration has to carry rather than something the schema can enforce.

**A random password printed on first startup.** Rejected. It lands in the log, which is exactly
where a credential should not be, and the operator still has to get it out of the log. It also
makes an unattended first start produce a credential nobody chose, which is a surprising way to
become an administrator of something.

**A one-time setup URL, like a password-reset link.** Rejected as premature. It needs a signed
token, an expiry and a page that does not exist yet, and it is the right answer for an instance
reachable from the internet — which is a deployment this project does not assume. If that
assumption changes, this is the decision to revisit, and the `admin` subcommand is the thing it
replaces rather than coexists with.

**Let the first request to any route claim the admin account.** Rejected outright: it is a race
between whoever connects first, on a self-hosted instance that is briefly reachable before its
operator has finished configuring it.
