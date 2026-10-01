---
title: "0024 — Authorisation is a gate on the route, not a check inside the handler"
description: "The S-8 access matrix is enforced by one middleware that resolves a campaign's tier, and by three named gates routes mount; no handler re-derives it."
lede: "A matrix enforced at the call site is a matrix with one copy per call site, and the copy that drifts is the one nobody reviewed. Resolving the tier once, in the middleware, and mounting a named gate makes the decision a reviewable claim about a route rather than a claim a reviewer has to re-derive."
weight: 210
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

S-8 is a table: instance admin, campaign GM, campaign player, anonymous. `domain.ResolveAccess`
holds it as one pure function, which is already the right shape — it is testable, total, and
free of I/O, so the whole matrix can be asserted in a table test without a database.

The gap is between that function and a request. Something has to look the campaign up by the
slug in the path, look the requestor's membership up, call `ResolveAccess`, and then decide
what to do with the answer. That something could be a handler, and there are about thirty
routes in the phase plan.

Written per handler, it goes wrong in three specific ways, and each is hard to spot in
review:

- **A 403 on a private campaign confirms the campaign exists.** `notFoundHandler` exists to
  make an unmatched route and a hidden campaign answer identically. A handler that answers
  403 to a non-member breaks that equivalence, and the leak is one status code wide.
- **An anonymous request and a database failure must be the same answer.** If a handler
  propagates a store error as a 500 but renders a missing campaign as a 404, the difference
  is an oracle: a scanner learns which slugs exist by watching which requests produce 500s
  under load.
- **`public` grants read and nothing else, and that is easy to half-remember.** A route
  guarded by "is the campaign public or am I a member" admits a non-member's play attempt.

None of these is a mistake a careful author avoids. They are the residue of writing the same
decision thirty times.

## Decision

Authorisation is a **middleware that resolves, plus a named gate that a route mounts**.

`campaigns.Resolve(store)` reads the `{slug}` path value, loads the campaign, loads the
requestor's membership when there is one, calls `domain.ResolveAccess`, and puts an `Access`
value on the request context. `campaigns.RequireRead`, `RequirePlay` and `RequireEdit` are
the three gates, each a `Guard` bound to one of the three capability predicates
`domain.Tier` already exposes.

A route is mounted behind them:

```go
mux.Handle("/c/", campaigns.Resolve(campaignStore)(
    middleware.Chain(campaignMux, campaigns.RequireRead),
))
```

`mountCampaignRoutes` is the single list a route is added to, so adding one without a gate is
not expressible.

Three consequences are worth stating, because they are the parts a later reader is tempted
to undo:

- **No access is 404, not 403.** The body and status are identical to an unmatched route. A
  reader who cannot see a campaign learns nothing from the difference, which is the property
  the `notFoundHandler` comment claims.
- **Some access but not the required capability is 401 when anonymous, 403 otherwise.**
  S-14.4 permits either for an anonymous request. 401 is chosen because it is the answer that
  tells a reader signing in will help, and reaching a capability gate at all means the
  campaign is not itself a secret — it is public, or the reader is a member of it.
- **A membership read failure downgrades to read-only; a campaign read failure is a 500.** The
  first fails toward less access, which is free: a private campaign is a 404 either way and a
  public one stays readable. The second must not be a 404, because reporting a broken database
  as an absent campaign tells an operator their database is healthy when it is serving errors.

The gates are `var`s of function type rather than functions, so a mount list reads as a claim
about the route: `RequireEdit` says what it needs, where `Guard(domain.Tier.CanEdit)` makes
the reader look up what that means.

### Why the gates are not tier constants

`Guard` takes `...func(domain.Tier) bool` rather than a `domain.Tier` to compare against,
because the tiers are not a total order for this purpose. `CanRead`, `CanPlay` and `CanEdit`
are three capabilities with three membership tests, and each already exists in `domain` as the
single implementation of the matrix. A gate taking a tier would push toward a `t >= TierGM`
comparison, and `Tier` is an `int` — so any conversion could produce a value outside the set,
and `t >= TierGM` would make that value the most privileged one in it. Every predicate is an
explicit membership test, so a tier this build does not recognise grants nothing.

## Consequences

- Every campaign-scoped route inherits the matrix by being mounted. The failure mode of
  forgetting is a compile error, not a review comment.
- The matrix has one enforcement point, and the table test in `campaigns_test.go` covers all
  twelve cells of the S-8 table across three gates.
- `Resolve` is a read on every campaign-scoped request. It is two indexed single-row queries,
  and P4's index maintenance is what keeps the second one cheap. A cache here would be a
  second source of truth about a membership, and memberships change by an explicit GM action.
- Adding a fourth capability means adding a predicate to `domain.Tier` and a fourth named gate
  here. That is a change to the S-8 table, which is the point: it should require saying so.
- `mountCampaignRoutes` is empty in this phase. The wiki surface is P3 and the write path P6,
  and neither is mounted ahead of its gate.

## Alternatives considered

**Check `tier.CanEdit()` inside each handler.** Rejected. It is the shape that produces a
matrix with one copy per call site, and a reviewer reading `if !access.Tier.CanEdit()` has to
remember the matrix to review it. A named gate in a mount list is the same decision with the
reasoning already done.

**A single `Authorise(capability)` middleware taking the capability as an argument.** Rejected
in favour of three named gates. The middleware form pushes the choice to the call site, which
is the same call site the decision was supposed to move away from. The named gates make the
wrong thing a compile error and the right thing the shortest possible line.

**Answer 403 for an anonymous request on a capability gate.** Permitted by S-14.4, and
rejected. 403 says "you are known and you may not"; for a reader who has not signed in yet,
that is the wrong diagnosis and sends them to look for a permission they do not have. 401 with
a `Cookie` challenge is the answer that names the fix.

**Filter the campaign list by visibility rather than by membership.** Rejected, and it is what
the store's `CampaignsForUser` comment already records: a public campaign the reader is not a
member of is reachable by its slug, and listing it in their campaign list would claim they
hold a role in a campaign they have no role in. Membership is the list.
