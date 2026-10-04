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
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

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

// newRealtimePlane builds the plane over one store handle and the registered plugins.
//
// `ctx` is the **process** lifetime context (the signal context in `runServer`),
// not a request's: `NewRegistry` and `NewHub` each keep it and derive a cancelable
// child for their goroutines, so a context that died with the request that created
// them would stop persistence within seconds of the first join. Both headers state
// this and both are true.
//
// `registered` is the `systems.go` value rather than the individual registries, for
// the reason that file gives: the resolver needs the gameplay registry, the gate needs
// this build's fingerprint, and the plugin route needs the system lookup — and handing
// over three values separately is three chances to pair a registry with another
// registry's fingerprint, which is a wiring that compiles and resolves nothing.
func newRealtimePlane(
	ctx context.Context,
	backing *store.Store,
	registry *observability.Registry,
	registered plugins,
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

	// 2. The gate, over the fingerprint this build resolves under and a reader
	// for the column a campaign was last written under.
	//
	// `registered.fingerprint()` and not a constant: the four components come from the
	// engine that was actually compiled, so a base-pack or overlay revision moves the
	// gate's expectation **without an edit to this file**. That is ADR 0018's whole
	// claim — the version names the resolution semantics, not this build's intentions
	// — and a hand-written fingerprint would be the second encoding `ruleset.go` warns
	// against, one that could not notice a pack had moved.
	gate := realtime.NewGate(registered.fingerprint(), rulesetVersionReader(backing))

	// 3. The resolver, over the gameplay registry and **this very state registry**.
	//
	// The ordering is mechanical, not stylistic: `plugin.ResolverConfig.States` is a
	// concrete `*realtime.Registry` rather than an interface — S-10.2's promise is about
	// `campaign_state` and the only convincing proof is a test against the real one — so
	// a resolver built over a second registry would apply mutations to a document
	// nothing persists and nothing broadcasts.
	//
	// `NewResolver` can fail on three wiring faults, none of which anything an operator
	// did. **Panic rather than substitute a nil resolver**, for the reason ADR 0039
	// rejected as an alternative: a hub with no resolver answers every intent
	// `server_error`, which is safe and says nothing, so the failure is a boot that
	// succeeds and refuses every roll. `plugin.New()` returns a registry whether or not
	// anything was registered, so the only way to reach this panic is a `systems.go` bug.
	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems:  registered.gameplay,
		States:   states,
		SystemOf: registered.systemOf(backing),
	})
	if err != nil {
		panic(fmt.Sprintf("semiplane: the gameplay resolver is not wired: %v", err))
	}

	// 4. The hub, over the registry and the resolver.
	//
	// One per process, which is also what the gate's single expected fingerprint
	// assumes: two hubs over one state registry is a divergence rather than a
	// scale-out, and ADR 0004 is about exactly that.
	hub := realtime.NewHub(ctx, realtime.HubConfig{
		States:  states,
		Resolve: resolver,
	})

	return &realtimePlane{registry: states, gate: gate, hub: hub}
}

// newPlayRoute builds the tabletop document and socket handler.
//
// # The comment that used to be here was false, and it was false quietly
//
// It read: *"Two fields and no `Instance` view, and the absence is the point... `/play`
// renders no document: it answers either `101 Switching Protocols` with a socket on it,
// or a sentence of plain text from `play.refuse`... the handler holds the hub and the
// logger and nothing else, which is exactly what `play.Handler` declares."*
//
// Every clause of that was defensible when written and none of it was true by phase 9.
// `play/document.go` and `handler.serveDocument` write a complete shell document, and
// `internal/web/e2e/play_test.go` fetches one on every run. `play.Handler` had grown
// `Campaigns`, `Systems` and `Snapshot` so the document could render the campaign
// switcher, the die sheet and the token list — and this function kept its two-field
// literal.
//
// So **every campaign rendered §4.7's empty state for all three**: a roll dialog with
// "no roll notation", a token list reading "No tokens have been placed yet" — including
// `greyhaven` and its three seeded placements, one of them marked `visible: false`. UI
// §7.6 calls the token list *the accessibility source of truth for the table*, so what
// a screen reader was told about the table was that nothing was on it.
//
// # Why nothing was red
//
// Each field's own doc comment says a nil value renders the empty state, and that is
// the correct contract. It is also exactly what made the defect invisible: **an unwired
// field is indistinguishable from a campaign that has nothing on it**, so a test
// asserting "the token list is empty" passes in both worlds. This is the same failure
// the repository documents for a deleted `@import` and for an unread `route_pkgs`
// entry — correct markup, absent behaviour, nothing red — and **a comment asserting the
// absence is part of how it stayed absent**: the sentence above read as a design
// decision rather than as a stale note.
//
// Found by the phase 11 demo-vault suite, which is what a committed end-to-end suite
// is for. Every unit test saw its own layer, and this is the layer where the handler's
// fields meet the composition root.
//
// # What is still deliberately absent
//
// `Instance`, `SignOutHref` and `StatusHref` are **not `play.Handler` fields at all**,
// so there is nothing to set; the tabletop document is served through the same shell as
// every other route and carries the instance identity from there.
//
// # The bounds
//
// `ReadTimeout` and `ReadLimit` are left zero on purpose. `play`'s header says zero
// means "the default", and the defaults are `realtime.MaxTransportReadBytes` and the
// route's own 90 seconds. Stating them here would be a second place to change them, and
// a copy of `MaxTransportReadBytes` could be lowered below the codec's own bound
// without anything noticing — the same silent redefinition of the protocol the
// constant's own comment is about.
//
// **`newPlayRoute` handed the handler two fields and the document needs five.** Phase 9
// added `Campaigns`, `Systems` and `Snapshot` to `play.Handler` so the play document
// could render the campaign switcher, the die sheet and the token list, and this
// function kept its two-field literal. So **every campaign rendered §4.7's empty state
// for all three** — a die sheet with no roll notation, a token list reading "No tokens
// have been placed yet" — including `greyhaven` and its three seeded placements.
//
// Nothing was red, and that is the interesting part. Each field's own doc comment says
// a nil value renders the empty state, which is the correct contract and is exactly what
// made the defect invisible: **an unwired field is indistinguishable from a campaign
// with nothing on it.** A test asserting "the token list is empty" passes in both
// worlds. Found by the phase 11 demo-vault suite — which is what a committed
// end-to-end suite is for, since every unit test saw its own layer and this is the layer
// where the fields meet the composition root.
func newPlayRoute(
	plane *realtimePlane,
	backing *store.Store,
	registered plugins,
	logger *slog.Logger,
) *play.Handler {
	return &play.Handler{
		Hub:       plane.hub,
		Logger:    logger,
		Campaigns: backing,

		// **A closure, not a conversion.** `plugins.Systems` and `play.Systems` are two
		// named types over one signature, and Go will not convert between them
		// implicitly — the assignment is a compile error, which is the right outcome:
		// the two packages cannot drift apart in shape without this line saying so.
		//
		// The body is the plugin route's own resolver, called rather than copied.
		// `registered.systemFor(backing)` is the single place that joins
		// `campaigns.system_id` to the gameplay registry, and a second copy of its
		// body would be a second answer to the same question.
		Systems: func(ctx context.Context, campaignID int64) (rules.System, error) {
			return registered.systemFor(backing)(ctx, campaignID)
		},

		// The seam `play.SnapshotFunc` documents, closed over the **same** registry the
		// hub resolves against. A second registry would render a token list from a
		// state nothing broadcasts and nothing persists.
		//
		// `false` for "no state is live" is the designed answer, not a failure: this
		// document is the render *before* the socket delivers the snapshot, so empty is
		// the truth for exactly the moment it is called.
		Snapshot: func(_ context.Context, campaignID int64) (realtime.Document, bool) {
			state, live := plane.registry.Get(campaignID)
			if !live {
				return realtime.Document{}, false
			}

			return state.Snapshot(), true
		},
	}
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
