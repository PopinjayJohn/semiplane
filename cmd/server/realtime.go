// The realtime plane's composition-root wiring: the state registry, the ruleset
// gate, the hub, and the three closures that reach the database.
//
// # The order, and why each step is where it is
//
// The plane is four values with a strict dependency flow, and every step below is
// forced by the one before it:
//
//	content roots and store  →  state registry  →  ruleset gate  →  hub  →  route
//
// 1. **The registry** (`realtime.NewRegistry`) is first because it is the only
//    holder of live campaign state, and both later steps need it. It is built over
//    two closures into `*store.Store` and nothing else — see the closures below.
//
// 2. **The gate** (`realtime.NewGate`) comes next and is *not* reachable from the
//    hub at all: `HubConfig` has no gate field and `Registry.Open` cannot consult
//    one, because the fingerprint is a column on `campaigns` and `state.go` does
//    not read that table. The gate is therefore a value the composition root holds
//    and is the only thing that can order a resume correctly.
//
// 3. **The hub** (`realtime.NewHub`) takes the registry and the resolver. The
//    resolver is `realtime.Core`, constructed here and passed in — ADR 0011: no
//    `init()`, no package-level registration, one explicit statement of what this
//    process resolves intents with.
//
// 4. **The route** takes the hub and a logger, and nothing else.
//
// # The ordering hazard, and why this file cannot express it wrongly
//
// `ruleset.go`'s own header states the hazard better than this one could: `Registry.Open`
// writes a fresh `campaign_state` row under the *new* fingerprint, so a composition
// root that opens first and checks afterwards has already destroyed the evidence
// that there had ever been a difference to report — and the refusal that follows
// names a game that is no longer on disk. So:
//
//   - `resumeCampaignStates` below calls `gate.Resume`, never `registry.Open`.
//   - `gate.Resume` is the only call in this package that opens a state.
//
// One call cannot be ordered wrongly, which is the entire reason it exists.
// `resumeCampaignStates` is the **only** place in the binary that resumes a
// campaign's state, and `resumeStatesOnlyThroughTheGate` is the test that holds
// that claim: it greps this package's own source, so a later `registry.Open`
// added beside the gate is a failing test rather than a silent one.
//
// # What is deliberately NOT done here
//
// A campaign's state is **not** opened speculatively for campaigns nobody has
// opened a table for, and `Hub.Join` is the other half of that decision: it opens
// on demand. That is the right shape — a registry with a state per campaign on the
// instance is a table's memory held for every campaign nobody is playing, and ADR
// 0004 makes in-memory state ownership the whole constraint. What the boot pass
// *does* do is check every campaign's fingerprint **without opening anything**, so
// drift is an operator's log line at startup rather than a GM's failed join
// halfway through a session. `gate.Inspect` is the read-only half for exactly that
// reason (`ruleset.go` separates it from `Check` so two pages can render the same
// facts).
//
// That leaves one residual path, reported rather than papered over: a campaign
// whose state was **not** live at boot and which drifts afterwards is opened by
// `Hub.openAuthority` → `Registry.Open`, which does not consult the gate. Closing
// it needs a `Gate` field on `HubConfig`, which is a change to
// `internal/realtime` and therefore not this work item's to make. It cannot lose
// data — the gate refuses, the join is refused, and the row keeps the fingerprint
// it had — but the refusal arrives from `Hub.Apply`'s caller rather than from the
// gate, so it is a 500 rather than a drift message. `TestTheGateRefusesACampaign
// WhosePersistedVersionDrifted` is the behaviour, and it is tested at the gate.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// coreSystemID is the system id the inert `realtime.Core` resolver reports.
//
// A constant rather than configuration because there is no configuration for it
// yet, and because the inert core is what phase 7 ships: naming it "core" makes
// a log line and a refusal read as what they are ("this build resolves nothing
// under the core system") rather than as an unknown system. Phase 8 replaces the
// resolver and this value with it.
const coreSystemID = "core"

// coreRulesetVersion is the version string the inert `realtime.Core` reports and
// the one half of the fingerprint that is about the resolver.
//
// It is **not** the campaign's `ruleset_version` column value. That is the encoded
// `sp1:` string the *gate* compares, produced by `FingerprintOf` from a registered
// descriptor, and the two being different values is the point: a campaign written
// under one ruleset and this process's resolver are two separate questions, and
// collapsing them into one constant would make the gate compare a campaign against
// a value that describes the server rather than the game.
//
// `p7` for "phase 7". It changes when the resolver's semantics do, and it does not
// change when the inert core gains an operation — because S-7.7's fingerprint is
// about the **meaning** of a persisted mutation, and an operation the resolver
// still refuses is not a change in meaning. Phase 8 replaces the constant with the
// gameplay system's own `RulesetVersion`.
const coreRulesetVersion = "p7"

// coreBasePackVersion is the base data pack the inert core resolves against.
//
// Present because `Fingerprint.validate` refuses an empty base pack, and for a
// good reason stated in `ruleset.go`: a fingerprint naming no ruleset is
// indistinguishable from "written under no particular ruleset", which is a
// *different* answer with different consequences — one is a campaign this build
// knows, the other is a campaign with nothing to compare against.
//
// Also `p7`, and for the same reason. The inert core ships no packs, and the
// honest way to say "no packs yet" in a schema where the component is mandatory is
// a placeholder that changes with the resolver rather than an empty string, which
// the validator reads as an absent fingerprint.
//
// The overlay is **empty** and that is a real value, not a gap: `validate` exempts
// it because architecture §10.4 makes a standalone pack a first-class shape. An
// empty overlay is how this build says "this system ships one pack", and a phase 8
// build that ships two names both.
const coreBasePackVersion = "p7"

// realtimePlane is the assembled plane, held as one value so the composition root
// and the shutdown path both name one thing rather than four.
//
// `registry` and `hub` are exported fields because they are used outside this
// file: `hub` by the route and by the shutdown step, `registry` by the resume
// pass. `gate` is unexported because nothing outside this file may open a state
// with it, and that restriction is the whole point of it — the hazard above is
// prevented by there being exactly one caller.
type realtimePlane struct {
	registry *realtime.Registry
	gate     *realtime.Gate
	hub      *realtime.Hub
}

// newRealtimePlane builds the plane over one store handle.
//
// `ctx` is the **process** lifetime context (the signal context in `runServer`),
// not a request's: `NewRegistry` and `NewHub` each keep it and derive a cancelable
// child for their goroutines, so a context that died with the request that created
// them would stop persistence within seconds of the first join. Both headers state
// this and both are true.
func newRealtimePlane(
	ctx context.Context,
	backing *store.Store,
	registry *observability.Registry,
	logger *slog.Logger,
) *realtimePlane {
	// §13.2's `state.write_ms`, before the registry it records into.
	//
	// Constructed first of the four because `NewWrites` *registers its counter into
	// the observability registry*, and the wiring assignment below hands it to the
	// realtime registry as its `WriteRecorder`. The signatures are identical by
	// design (`writes.go` says so), so this is an assignment and `observability`
	// never imports `realtime` — a dependency that ran the wrong way for no
	// benefit.
	writes := observability.NewWrites(registry, logger)

	// 1. The state registry. Over the two closures below, and nothing else.
	states := realtime.NewRegistry(ctx, realtime.Config{
		Write:  realtimeWriter(backing),
		Read:   realtimeReader(backing),
		Record: writes.Record,
	})

	// 2. The gate, over the fingerprint this process resolves under and a reader
	// for the column a campaign was last written under.
	gate := realtime.NewGate(expectedFingerprint(), rulesetVersionReader(backing))

	// 3. The hub, over the registry and the resolver.
	//
	// `Core` is a value, not a pointer, and it is held by value here rather than
	// looked up: ADR 0011 forbids `init()` registration and this is the explicit
	// statement that replaces it. One per process, which is also what the
	// fingerprint below assumes.
	hub := realtime.NewHub(ctx, realtime.HubConfig{
		States:  states,
		Resolve: realtime.Core{SystemID: coreSystemID, RulesetVersion: coreRulesetVersion},
	})

	return &realtimePlane{registry: states, gate: gate, hub: hub}
}

// newPlayRoute builds the tabletop socket handler.
//
// Two fields and **no `Instance` view**, and the absence is the point rather than
// an omission. The other five routes render a document through the shell, so they
// carry the instance's identity for the header and the rail. `/play` renders no
// document: it answers either `101 Switching Protocols` with a socket on it, or a
// sentence of plain text from `play.refuse`. A field nothing reads is the
// stylesheet-import failure in field form — a value in the product that looks like
// it is wired and is read by nobody — so the handler holds the hub and the logger
// and nothing else, which is exactly what `play.Handler` declares.
//
// The bounds (`ReadTimeout`, `ReadLimit`) are left zero on purpose. `play`'s
// header says zero means "the default", and the defaults are
// `realtime.MaxTransportReadBytes` and the route's own 90 seconds. Stating them
// here would be a second place to change them, and a copy of
// `MaxTransportReadBytes` could be lowered below the codec's own bound without
// anything noticing — the same silent redefinition of the protocol the constant's
// own comment is about.
func newPlayRoute(hub *realtime.Hub, logger *slog.Logger) *play.Handler {
	return &play.Handler{Hub: hub, Logger: logger}
}

// expectedFingerprint is the resolution semantics **this process** offers.
//
// Built by `FingerprintOf` from the inert core's descriptor rather than written as
// a literal, for one reason: an encoding written by hand and an encoding produced
// by the package that defines it drift apart the first time the format changes,
// and the drift is silent. `FingerprintOf` is also where a component carrying a
// reserved separator is refused, which is the failure a hand-written string would
// defer until a campaign tried to resume.
//
// The pack versions are named even though the inert core ships none, because
// `Fingerprint.validate` requires a base pack and accepts an empty **overlay**
// only — so this is the one descriptor shape the package accepts for a standalone
// system. A phase 8 build names its own, and the boot pass in
// `resumeCampaignStates` is what reports the campaigns the new fingerprint
// strands.
//
// An impossible error, therefore. `FingerprintOf` is called with constants this
// file owns and whose validity is asserted by the round-trip test below, so there
// is nothing to return. It is handled rather than ignored because an ignored error
// here would be a `Fingerprint{}` — an *empty* fingerprint, which is a different
// thing from an absent one and compares as drift against every campaign.
func expectedFingerprint() realtime.Fingerprint {
	fingerprint, err := realtime.FingerprintOf(realtime.Descriptor{
		System:      coreSystemID,
		Ruleset:     coreRulesetVersion,
		BasePack:    coreBasePackVersion,
		OverlayPack: coreRulesetVersion,
	})
	if err != nil {
		// Unreachable by construction, and a panic rather than a substituted zero
		// value: this runs once at boot over compile-time constants, so a failure
		// is a defect in this file rather than anything an operator did, and the
		// honest response to one is to say so loudly at startup rather than to
		// hand the gate a fingerprint that compares as drift against every
		// campaign on the instance.
		panic(fmt.Sprintf("semiplane: the core system's fingerprint does not encode: %v", err))
	}

	return fingerprint
}

// realtimeWriter is the registry's `Write` seam: one `campaign_state` row, in a
// transaction, on the store's **writer queue**.
//
// The closure exists because of a Go language rule rather than a design
// preference, and the same rule `edit.NewRevisionLog`'s caller states in full:
// `store.Store.Write` takes an **unexported** parameter type, `store.writeFunc`,
//
//	func (s *Store) Write(ctx context.Context, fn writeFunc) error
//
// and an interface method's parameter types must be *identical*, not merely
// assignable — so no interface `*store.Store` can satisfy expresses "run this in a
// transaction", and no package outside `store` can even name the type. A closure
// converts one function type to the other implicitly without either being named at
// the call site. This is that closure, written once and shared by the editor's and
// the registry's, rather than a second copy of it inline.
//
// It is not a convenience, and it is not one of several ways to reach a
// transaction: it is the only way to reach the **writer queue**. `Store.DB()` is
// documented for reads, and a state flush issued on it would bypass the queue whose
// entire job is to keep exactly one statement in flight so SQLite's
// single-writer limit is never contended. Two writers is a `SQLITE_BUSY`, and the
// second one to arrive loses a GM's game.
func realtimeWriter(backing *store.Store) realtime.Writer {
	return func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
		return backing.Write(ctx, fn)
	}
}

// realtimeReader is the registry's `Read` seam: one `campaign_state` row.
//
// On `Store.DB()` rather than the writer queue, and the reason is stated in
// `state.Reader`'s own header: a read that queued behind a `campaign_state` flush
// would make every reconnect wait for a write it did not ask for. Reads are safe to
// issue concurrently, and the pool exists for them.
//
// Two details that are not optional:
//
//   - `sql.ErrNoRows` becomes `realtime.ErrNoState`, because "this campaign has no
//     row yet" is a **real state** — an empty tabletop nobody has joined — and not
//     a failure. `Registry.load` distinguishes them, and an untranslated
//     `ErrNoRows` would be a campaign that can never open.
//   - The row is read through a `sql.Row`, and `State` is a `BLOB` holding bytes
//     that were written as bytes, so the scan is into `[]byte`. The three columns
//     are the row's whole content and `updated_at` is stored in the same unix
//     seconds as every other timestamp in the schema.
//
// There is no `store` method for this read, and that is why the statement is named
// here: a column list and the arguments filling it have to agree, and one name is
// what makes a mismatch impossible to introduce rather than unlikely. Migration
// 0002 is the schema's authority.
func realtimeReader(backing *store.Store) realtime.Reader {
	const selectState = `SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = ?`

	return func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
		var (
			persisted realtime.Persisted
			updatedAt int64
		)

		what := fmt.Sprintf("read the persisted state of campaign %d", campaignID)

		err := backing.DB().QueryRowContext(ctx, selectState, campaignID).Scan(
			&persisted.Blob, &persisted.Version, &updatedAt,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// A real answer rather than a failure. See the doc comment.
				return realtime.Persisted{}, realtime.ErrNoState
			}

			return realtime.Persisted{}, fmt.Errorf("%s: %w", what, err)
		}

		persisted.UpdatedAt = time.Unix(updatedAt, 0).UTC()

		return persisted, nil
	}
}

// rulesetVersionReader is the gate's `VersionReader`: `campaigns.ruleset_version`,
// verbatim, as the column holds it.
//
// "Exactly as the column holds it" is the contract and the reason for a dedicated
// statement rather than a `CampaignBySlug`. `gate.Inspect` parses the string and
// treats the **empty string** as a meaningful value — migration 0005 defines it as
// "state written under no particular ruleset", and `Inspect` reports it as
// unfingerprinted rather than as unreadable. A read that defaulted an empty value
// to something else, or that failed on one, would strand every campaign created
// before the column was populated, which is the outcome ADR 0018 exists to
// prevent.
//
// `store` has no method for this column alone — `CampaignByID` would return eight
// columns of which four are irrelevant to a comparison — so the statement is named
// here for the same reason `selectState` is. The read is on `DB()`, for the same
// reason every other read here is.
func rulesetVersionReader(backing *store.Store) realtime.VersionReader {
	const selectRulesetVersion = "SELECT ruleset_version FROM campaigns WHERE id = ?"

	return func(ctx context.Context, campaignID int64) (string, error) {
		var version string

		what := fmt.Sprintf("read the ruleset_version of campaign %d", campaignID)

		err := backing.DB().QueryRowContext(ctx, selectRulesetVersion, campaignID).Scan(&version)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Not `realtime.ErrNoState`. That sentinel means "no persisted
				// state", and the gate wraps this error rather than comparing
				// against it — what it must not do is report "no particular
				// ruleset", which is what an empty string would become if this
				// returned the zero value. A campaign id with no row is a caller
				// error, and the gate's `Inspect` already refuses a non-positive
				// one.
				return "", fmt.Errorf("%s: %w", what, store.ErrNotFound)
			}

			return "", fmt.Errorf("%s: %w", what, err)
		}

		return version, nil
	}
}

// resumeCampaignStates is the boot pass over every registered campaign.
//
// # What it does and does not do
//
// It **checks** every campaign and opens **nothing**.
//
// That is the deliberate reading of the ordering hazard above. `Gate.Resume` is
// the only call in this package that opens a state, and the boot pass is not it:
// opening a state for every campaign on the instance at startup would put one live
// tabletop in memory per registered campaign, which is the cost ADR 0004 makes the
// project's central constraint and which the hub's on-demand open avoids
// entirely. A registry holding a state per campaign is also a registry whose
// `Live()` count is the campaign count, which would make `/readyz`'s number
// meaningless.
//
// What the pass buys instead is that **drift is a log line the operator reads at
// startup**, rather than a failed join in the middle of a session with a GM
// waiting. It uses `Gate.Inspect`, which is read-only by construction and is the
// same function `Check` is built on, so the two cannot disagree about a campaign's
// status (ADR 0018 requires the same facts on two pages, which is why the two
// functions are separate).
//
// # What each outcome means
//
//   - Resumable: nothing to say. No line, because a boot log that names every
//     healthy campaign is a boot log nobody reads.
//   - Unreadable: an **error**. A row this build cannot parse is a defect in what
//     was written or in this build, and an operator cannot act on it.
//   - Drift: an **error**, naming both versions and the component that differs,
//     because `DriftError.Error` was written to be the whole diagnosis in one line
//     and a log that dropped it would be the record's message unused.
//   - Unfingerprinted: nothing. The empty column is a real value with a defined
//     meaning, and refusing it would strand every pre-migration campaign.
//
// # Why a failure here does not stop the boot
//
// The same reasoning `mustListCampaigns` gives: a campaign that cannot be resumed
// is one campaign. The wiki, the search, the editor and every other campaign's
// table keep working, and an instance that refuses to start over a single
// incompatible game is worse than one that starts and says so. Failing toward
// serving is what the whole phase does — `play` answers the join with a status code
// rather than a silent empty socket.
func resumeCampaignStates(
	ctx context.Context,
	plane *realtimePlane,
	campaigns []domain.Campaign,
	logger *slog.Logger,
) {
	// Indexed: domain.Campaign is 128 bytes and only the ID is read here.
	for i := range campaigns {
		campaign := &campaigns[i]

		status, err := plane.gate.Inspect(ctx, campaign.ID)
		if err != nil {
			logger.Error("realtime.ruleset_unreadable",
				slog.String("campaign", campaign.Slug),
				slog.String("class", errorClass(err)),
			)

			continue
		}

		if status.Unreadable != nil {
			logger.Error("realtime.ruleset_unreadable",
				slog.String("campaign", campaign.Slug),
				slog.String("class", errorClass(status.Unreadable)),
			)

			continue
		}

		if status.Drift != nil {
			logger.Error("realtime.ruleset_drift",
				slog.String("campaign", campaign.Slug),
				// The message, verbatim. `DriftError.Error` names the persisted
				// version, the expected one, the input that differs and what
				// happens now, and a line that rendered it as "drift" would throw
				// away the only thing written to be read.
				slog.String("detail", status.Drift.Error()),
				slog.String("class", errorClass(status.Drift)),
			)

			continue
		}

		if status.Unfingerprinted {
			// Informational and singular, because it is a property of the
			// installation rather than of one campaign: every row written before
			// migration 0005 is empty. One line, once, and only when there is
			// something to say.
			logger.Info("realtime.ruleset_unfingerprinted",
				slog.String("campaign", campaign.Slug),
			)
		}
	}
}

// errorClass classifies an error for a log attribute.
//
// A call to `observability.ErrorClass` rather than `err.Error()`, and the rule it
// follows is the one AGENTS.md states for every log line in this project: a
// Markdown or YAML parser quotes the line it choked on, and on a wiki page that
// line is routinely a `[!secret]` callout body. An error's text is **not**
// classified, only its class, because S-12.3 forbids carrying page content into a
// log and the parser is the reason that is a live hazard rather than a theoretical
// one.
//
// The drift case above is the deliberate exception and is worth naming: a
// `DriftError`'s text is composed by this project from four fingerprint
// components, none of which is campaign content — it is written *by hand* rather
// than parsed, so it cannot quote a line it did not author. `ruleset.go` and its
// `TestADriftErrorNamesAllFour` are what make that safe to log.
func errorClass(err error) string {
	if err == nil {
		return ""
	}

	return observability.ErrorClass(err)
}

// closeRealtimePlane stops the plane, in the order the packages require.
//
// # The order, and why each step is where it is
//
//		hub.Close  →  registry.Close  →  store.Close
//
//	 1. **The hub first.** `Hub.Close` ends every peer, stops the sweeper, and then
//	    closes the state registry itself (`hub.go`'s header states that it does).
//	    So the hub is also the *outer* half of the registry's shutdown, and calling
//	    the registry's close first would flush live states underneath peers that are
//	    still connected — a peer could still broadcast into a state that had already
//	    been flushed and closed.
//
//	 2. **The registry second, explicitly.** `Hub.Close` already closes it, and
//	    `Registry.Close` is idempotent-by-design in the sense that a second close
//	    flushes nothing and stops nothing, so this call is about the *order being
//	    stated* rather than about work being done. It is here because the ordering
//	    hazard this file's whole header is about is one a reader has to be able to
//	    check by reading one function, and "the hub closes the registry" is a fact
//	    about another package's implementation rather than a fact about this one.
//
//	 3. **The store last**, in `runServer`'s own deferred close, because every
//	    writer in the process is a queue over that handle and the flush in step 2
//	    runs on the queue. Closing the handle first would abandon an in-flight
//	    `campaign_state` write mid-statement, and SQLite would roll it back while the
//	    caller was told it succeeded.
//
// # `context.WithoutCancel`, and why it is not optional
//
// By the time this runs the process context is **already cancelled** — that is what
// started the shutdown. `Registry.Close` flushes with the *caller's* context, so
// handing it a cancelled one is a crash by construction: every state's final write
// fails with `context.Canceled`, and a game that was being played a moment ago is
// silently lost on a clean stop. `context.WithoutCancel` keeps the values and drops
// the cancellation.
//
// The context is bounded anyway, by `flushBudget` rather than by the signal: a
// flush that could hang must not hold the process open forever, and a bounded flush
// that times out leaves the state on disk as of the last successful write, which is
// strictly better than no flush at all.
//
// `flushBudget` is generous relative to what a flush costs — one small row per live
// campaign, on a queue that is otherwise idle because the HTTP server has already
// stopped accepting — and exists so that a wedged writer cannot turn a graceful
// stop into a hang. It is the same reasoning `http.Server.Shutdown`'s budget is,
// applied to the other thing that has to finish before the store closes.
func closeRealtimePlane(
	ctx context.Context,
	plane *realtimePlane,
	logger *slog.Logger,
	flushBudget time.Duration,
) {
	// `WithoutCancel` here rather than at the call site, so that the reason it is
	// needed sits on the line that needs it. `ctx` is the signal context and is
	// cancelled by definition at this point; passing it straight through would
	// make every state's final write fail with `context.Canceled`.
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushBudget)
	defer cancel()

	// Dereferenced rather than checked: this function is only ever called with the
	// plane the composition root built, and a nil check here would be an admission
	// that it might be called without one.
	if err := plane.hub.Close(flushCtx); err != nil {
		// Logged, not returned, for the reason the store's close is: the process
		// is on its way out and there is nobody left to return to. The error is
		// classified rather than printed, so a store error cannot carry a row's
		// contents into the log.
		logger.Error("close the realtime hub", slog.String("class", errorClass(err)))
	}

	stats := plane.hub.Stats()

	logger.Info("realtime hub closed",
		slog.Int("peers", stats.Peers),
		slog.Int("campaigns", stats.Campaigns),
		slog.Int64("published", stats.Published),
		slog.Int64("superseded", stats.Superseded),
		slog.Int64("answered", stats.Answered),
		slog.Int64("staled", stats.Staled),
	)

	// The explicit second close. See point 2 above: `Hub.Close` already did this,
	// and this line is here so the order is stated where it is checked.
	if err := plane.registry.Close(flushCtx); err != nil {
		logger.Error("flush the realtime state registry",
			slog.String("class", errorClass(err)),
			slog.Int("still_live", plane.registry.Live()),
		)
	}
}
