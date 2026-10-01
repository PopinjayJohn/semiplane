---
title: "0035 — No `Vary: Cookie`, and exactly one blocking inline script"
description: "The document never varies by the theme cookie, so no `Vary` header is emitted, and the only JavaScript on the critical path is one inline resolver in `<head>` under a byte budget."
lede: "Two rules with the same cause. Because the theme and the layout mode are resolved before the first paint rather than during the response, the bytes a reader receives are identical whoever they are — and the strongest statement the server can make about that is to vary on nothing at all."
weight: 225
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

`sp_ui` carries two fields, `theme` and `ui`
([§6.6]({{ "design/ui-ux/" | relURL }})). A reader's theme choice and their layout-mode choice both
live in it, and both change what the interface looks like. The obvious reading is that the document
varies by the cookie, and that the correct HTTP response therefore says so.

That reading is wrong, and being wrong about it is expensive in a specific way: it turns every themed
response into a per-cookie cache entry, which is a shared-cache fragmentation tax across *all* readers
in exchange for no correctness gain. S-13.5 states the conclusion — *"The document never varies by the
theme cookie. **No `Vary: Cookie` is emitted.**"* — but not the mechanism, and the mechanism is what
makes it hold.

There is a second rule in the same place. §3.7 requires both root attributes to be resolved *before the
first paint*, which means JavaScript on the critical path. A shell that loads its layout from the server
cannot do that, and a shell that loads it from a script *after* first paint flashes the wrong theme — a
dark-theme reader gets a white flash on every navigation, which is the most visible possible way to ship
a feature that works.

## Decision

**The document does not vary by `sp_ui`, no `Vary` header is emitted, and exactly one blocking inline
script in `<head>` resolves both root attributes before the first paint.**

**No `Vary` header naming `sp_ui` or `Cookie`, on any route** — with one exception, stated below and
corrected after it was found to be false.

The distinction that matters for caches and intermediaries: `Vary: Accept-Encoding` on a response whose
encoding was already negotiated is a separate claim with a separate cost, and a shell response has no
reason to make any claim. The absence is asserted as an absence — through `Header.Values`, because
`Header.Get` cannot tell "unset" from "set to the empty string", and an empty `Vary` is still a header a
cache reads.

### The exception: the wiki route does vary by reader

**Correction.** This record originally claimed "no `Vary` header at all, on any route", on the reasoning
that the document does not vary by `sp_ui` and therefore needs no cache key for it. That reasoning is
right about `sp_ui` and wrong in general: **the document does vary by access tier**, because the shell
carries the reader's name and a sign-out form.

Measured on a running server, on a **public** campaign so both readers get 200 and neither body is an
error page:

```
GM      4460 bytes
anonymous 4227 bytes
```

The difference is `data-testid="header-account"` with the reader's name, and the sign-out form. A shared
cache keyed only on the URL would hand one reader another's name.

So `Vary: Cookie` is **required on the wiki route** and is what phase 3 already shipped, with the
correct rationale recorded then. This record's original claim would have deleted a correct header
because a test could not see the variation it was describing.

What survives is the load-bearing part: **no response varies by `sp_ui`**, which is the claim that
matters, because `sp_ui` is the cookie a reader's own preferences arrive in and fragmenting every
shared cache on it would buy nothing. The five-cookie byte-identity test proves that one.

**The bytes are identical for every reader of a given route and campaign.** Not "equivalent" and not
"equivalent modulo a cache key": identical, which is the property that makes the absence of `Vary`
safe rather than merely convenient. The substantive test is therefore not that no `Vary` is present —
that is a symptom — but that five cookie values, including a malformed one and no cookie at all, produce
byte-identical bodies. The second catches what the first cannot: a *later* phase adding a cookie
dependency, with no header change to notice.

**One blocking inline script, in `<head>`, before the stylesheet link, under a byte budget.** It reads
four sources in a fixed order — `?ui=`, the cookie, a narrow user-agent hint allowlist used only before
a cookie exists, then media queries — and writes `data-theme` and `data-ui`. It is the only JavaScript
on the critical path. The second script element it renders is the sheet re-parenting below 640px, which
must wait for `document.body` to exist and therefore cannot share the first; that is a second *element*,
not a second concern.

**A CSP nonce hook is plumbed but no `Content-Security-Policy` header is set.** The resolver honours
`templ.WithNonce`. Adding the header without a nonce generator is what would break the shell, because
the one script the design depends on would be the one script a policy forbids.

## Consequences

No response carries a `Vary` header naming `sp_ui`, so no shared cache fragments on the reader's own
theme and mode preferences. The wiki route carries `Vary: Cookie` for the reason above: its document
genuinely differs by reader, and a cache that ignored that would serve one reader another's name. More importantly, the *cost* of that decision is paid
in the place where it belongs: the document varies by access tier, by campaign, and — critically — by
`include_secrets`, which is a genuine variation and is not a cache decision at all. The
`include_secrets` variants are the reason a "the document never varies" claim would be false if stated
without qualification, and they are settled by [0016]({{ "decisions/0016-salted-etag/" | relURL }})
and [0028]({{ "decisions/0028-render-output-is-permission-neutral/" | relURL }}) rather than by
anything here. §3.7's table is the authority: what the document varies by is access tier,
`include_secrets`, and campaign — and never `sp_ui`.

The budget is enforced rather than trusted. `TestTheResolverFitsItsBudget` fails the build if the
resolver grows past its budget, at 1008 bytes against 1024. A budget that is only a comment stops being
respected the first time a feature needs two bytes.

The invariants are asserted where they can be falsified. On the real router's served bytes:

- neither `data-theme` nor `data-ui` appears in the served document, checked **on the parsed tree** —
  including comments and text nodes — because the resolver's own source names both attributes, so a
  substring test either fails on a correct document or has to carve out the script, and a carve-out is
  exactly where a `data-ui` in an HTML comment hides. `TestTheAuditFindsAResolvedAttributeWhereverItIs`
  feeds that audit six ways of smuggling one in and requires it to object.
- the document does not vary by `sp_ui`, byte for byte, across five cookie values — on the routes where
  that is true, which is the account surfaces; the wiki route's variation is by access tier and is what
  `Vary: Cookie` exists for;
- no `Vary` header names `sp_ui`;
- the resolver is in `<head>`, precedes the stylesheet link, and fits its budget;
- the editable file and the served constant are byte-identical, so they cannot drift;
- the script cannot end its own `<script>` element — the body is written unescaped, which is required
  because `&lt;` is not an operator, and therefore guarded.

Two things are honestly **not** asserted. Nothing in this repository executes JavaScript, so the order
of the four sources rests on reading `head.js` and on a Go transcription sitting beside it; a test that
grepped for the order of two substrings would be asserting a formatting detail and calling it
behavioural. The structural comparison of the transcription is what makes it trustworthy — the data is
what drifts between two copies of an algorithm, not the order — but the order itself is verified by
hand. The second gap is the agent-assisted half: that the browser actually applies `data-ui` before the
first paint is a browser pass, and §10.9 requires every *finding* from it to become a committed test
rather than a CI step that needs a browser.

## Alternatives considered

**Keep the original "no `Vary` at all" claim and delete the wiki route's header.** Rejected, and this is
the interesting rejection of this record. The claim was derived from "the document does not vary by
`sp_ui`", which is true, and generalised to "the document does not vary", which is false. A test asserting
the absence of a header cannot see the variation the header was protecting — it only sees that the
header is gone. The measurement in Context is what distinguishes them, and it is why the byte-identity
test is the substantive one and the header-absence test is a symptom.

**`Vary: Cookie` on responses whose theme is reader-specific.** Rejected: the response is not
theme-specific. Sending it would be a false promise — `Vary` asserts the representation depends on the
cookie, which is the opposite of the design — and it would fragment every shared cache in front of the
instance for a variation that does not exist in the bytes.

**Resolving the theme server-side, per cookie.** Rejected because it makes the document vary, which is
the thing being avoided, and because it costs a cache entry per reader to deliver a value the reader's
own operating system already has. `prefers-color-scheme` is the right default and it is free; the cookie
is an *override* of that default, and an override applied before first paint costs nothing.

**Applying the theme after load, from a deferred script or a class on `<html>` written by the client.**
Rejected: it flashes. A reader who chose the dark theme sees a white page on every navigation until the
script runs, which is worse than not offering the preference at all — and it converts a working feature
into the most-reported kind of visual bug. §3.7's ordering of the resolver before the stylesheet link
exists to make the flash impossible rather than unlikely.

**Loading the resolver as an external, cacheable, nonce-free script file.** Tempting: cacheable across
readers, so the bytes are paid once. Rejected because an external script is not render-blocking by
default, and forcing it to block with `async=false` in `<head>` reintroduces a network round trip on the
critical path — so the document's *first paint* waits on a fetch that could have been a 1KB inline
string already in the response. The budget exists to keep the inline cost bounded; an external file
trades a bounded inline cost for an unbounded latency cost.

**Adding `Content-Security-Policy` with `'unsafe-inline'`, or with no nonce.** Rejected. The first
forfeits the protection on every script for the sake of one. The second is worse: a policy without a
nonce forbids the one script the design depends on, so the shell silently stops resolving its root
attributes and every tier is wrong with no error anywhere. The nonce hook is plumbed so the header can
arrive with a generator; the header without a generator is the failure mode, and it is why the header is
not set.