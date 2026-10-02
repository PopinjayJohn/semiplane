package plugin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/realtime"
)

// RejectionError is a gameplay system's refusal, naming one of the wire's closed
// reasons.
//
// It exists because `rules.System.Apply` returns a bare `error` and
// `realtime.RejectionError` is a *hub*-side type this package must not construct
// on a system's behalf — a plugin's error travels through this package on its way
// to a browser, and the bridge has to be somewhere that can be tested. Without it
// every refusal would arrive as `server_error`, which is safe and useless: a GM
// whose `set_hp` was refused as `not_your_turn` and one whose refused `set_hp` hit
// a bug would be told the same word, and the second is worth a bug report.
//
// **Two fields, never one.** `Reason` goes on the wire; `Err` is the system's own
// refusal, reachable with `errors.Is` and `errors.As` and **printed by nothing
// here**. The split is `realtime.RejectionError`'s, and it is why a system's error
// text can quote the payload it choked on — which on this project is a dice result
// or a `[!secret]` callout body — without that payload reaching a browser or a log
// line. S-12.3 again, and the reason `Reason` is not derived from `Err`.
//
// A caller that wants the detail in the log logs it deliberately, from the field.
// That arrangement is the point: the default is to say nothing, and a plugin whose
// text belongs in a log has to be the one that asks for it.
//
// ## The closed set is not checked here
//
// `internal/realtime` does not export its membership test, and **this package
// deliberately does not keep a second copy of the eight words**: a second list is
// a second answer to "what may a client be told", and the one place that owns it
// already verifies what it is given — `reasonFor` reduces anything outside the set
// to `RejectServerError`. So a system returning `Rejection{Reason: "whatever"}`
// produces a refusal the client reads as `server_error`, which is the safe
// direction, and a system returning a reason inside the set gets exactly it.
//
// Construct one with `Reject`, which exists so the two fields cannot be confused
// at a call site: the common mistake is a formatted error where a reason belongs,
// and a formatted error reaches a browser.
type RejectionError struct {
	// Reason is what the actor is told, from `protocol.go`'s closed set.
	Reason realtime.RejectReason

	// Err is the system's own refusal. Reachable through `errors.Is` and
	// `errors.As`; printed by nothing in this package. May be nil.
	Err error
}

// Reject returns a refusal naming reason, wrapping the system's own error.
//
// The constructor rather than a struct literal because the two fields have
// opposite rules and a literal invites putting the detail in the wrong one.
func Reject(reason realtime.RejectReason, err error) error {
	return &RejectionError{Reason: reason, Err: err}
}

// Error returns the reason and nothing else.
//
// Content-free by construction, for `realtime.RejectionError`'s reason: this text
// reaches a log line, and a system's error quotes the thing it choked on.
func (r *RejectionError) Error() string {
	return "plugin: system refused the intent: " + string(r.Reason)
}

// Unwrap returns the system's own error, so `errors.Is` reaches it.
func (r *RejectionError) Unwrap() error { return r.Err }

// Class returns the content-free class a log line should carry.
//
// The reason itself is the class: it is one of eight words chosen by the file that
// owns the wire vocabulary, so it is safe to log verbatim, and an alert can match
// it. `observability.errorClass` would otherwise fall back to `%T` and report
// `*plugin.Rejection`, which is an identifier no alert can match — the same defect
// `rules.OwnershipError.Class` exists to fix.
func (r *RejectionError) Class() string { return "plugin.rejected:" + string(r.Reason) }

// wireReason reduces an error from a resolution to the reason the actor is told.
//
// The table, in the order it is asked:
//
//   - a `*Rejection` is the system's own answer and is passed through;
//   - `rules`' own malformed-intent refusals are `invalid_args`, because they are
//     about the arguments the actor sent and the codec forwarded;
//   - `realtime.ErrVersionMismatch` is `stale_version` and `realtime.ErrNoPlacement`
//     is `no_such_placement`, because both are questions about state the client
//     resolves by resynchronising;
//   - **everything else is `server_error`**, which is the safe default in a way the
//     others are not: it says nothing about what failed. A refusal this table does
//     not recognise is a failure nobody here classified, and inventing a specific
//     reason would assert a fact nobody established — `not_your_turn` in particular
//     is a hint about the game's rules that a stranger must not be given.
//
// `realtime.reasonFor` reduces the answer a second time, against the closed set,
// so a reason outside it degrades to `server_error` there as well.
func wireReason(err error) realtime.RejectReason {
	if rejection, ok := errors.AsType[*RejectionError](err); ok {
		return rejection.Reason
	}

	if errors.Is(err, rules.ErrInvalidOp) ||
		errors.Is(err, rules.ErrInvalidObject) ||
		errors.Is(err, rules.ErrOpArgsTooLarge) {
		return realtime.RejectInvalidArgs
	}

	if errors.Is(err, realtime.ErrVersionMismatch) {
		return realtime.RejectStaleVersion
	}

	if errors.Is(err, realtime.ErrNoPlacement) {
		return realtime.RejectNoSuchPlacement
	}

	return realtime.RejectServerError
}

// refusal builds the error a refused intent returns: a `*Rejection` carrying the
// reason, holding the cause.
//
// **Not wrapped in anything.** The cause is already reachable — `Rejection.Unwrap`
// returns it, so `errors.Is(err, ErrNoPlacement)` and `errors.As` for a
// `*PanicError` both work — and wrapping it again would put a system's own text into
// the outer error's `Error()`, which is exactly what S-12.3 forbids a log line to
// carry. The outer message is therefore always the reason and nothing else, and a
// caller that wants the detail reads it off the field.
//
// `Hub.Apply` reads only the reason and reports a refusal as a *successful* answer
// to the client, which is why this function's value must never also be the thing
// that fails a request.
func refusal(reason realtime.RejectReason, err error) error {
	return Reject(reason, err)
}

// SystemOf reports which gameplay system a campaign plays under.
//
// A seam rather than a query because the answer lives in `campaigns.system_id` and
// `internal/store` is not this package's to read. It is also the half of S-10.6
// that needs a registry: a campaign naming an id nothing registered is refused
// **with the id in the message**, and the two functions compose to say it.
type SystemOf func(ctx context.Context, campaignID int64) (rules.ID, error)

// SeedFunc mints the entropy one resolution draws from.
//
// Per **resolution**, not per campaign and not per campaign-loading, and the reason
// is the one `rules.Context.Rand`'s own contract makes unavoidable: a source
// derived from `(seed, label)` yields the same numbers for the same label. With one
// seed per campaign, every `roll` in that campaign would draw the same value, so
// the per-resolution seed is what makes a roll log a log. §16.3 wants the seed
// recorded so an audit can re-derive a roll; that recording is the *system's* — it
// puts the seed in its mutation payload — and this function's only job is to supply
// one that is not ambient.
type SeedFunc func(ctx context.Context, campaignID int64) (rules.Seed, error)

// CryptoSeeds returns a `SeedFunc` drawing from `crypto/rand.Reader`.
//
// **Not the rule code's forbidden source.** S-10.4 forbids a *resolver* calling
// `crypto/rand` directly, because a resolver that drew ambient randomness would
// resolve to a different answer on a replay. This is the other side of the same
// line: the hub is where entropy enters a process, `rules.Seed` is where it becomes
// a recorded value, and a system that wants a reproducible roll re-derives it from
// that value rather than from the reader. `realtime.newCampaignState` draws its
// incarnation from the same reader for the same reason.
//
// One seed per call, 32 bytes, and a reader that fails produces an error rather
// than a partial seed — a zero `rules.Seed` is a *legal* seed (ADR 0041), so a
// failed draw must not silently resolve a campaign's rolls to the same numbers
// forever.
func CryptoSeeds() SeedFunc {
	return cryptoSeeds(rand.Reader)
}

// cryptoSeeds is `CryptoSeeds` over a named reader, and it exists so the draw is
// testable without randomness — the same arrangement `realtime.Config.Entropy`
// makes, and for the same reason: the bytes are not what this function is for, the
// handling of a reader that fails is.
func cryptoSeeds(reader io.Reader) SeedFunc {
	return func(_ context.Context, _ int64) (rules.Seed, error) {
		entropy := make([]byte, rules.SeedLen)
		if _, err := io.ReadFull(reader, entropy); err != nil {
			return rules.Seed{}, fmt.Errorf("plugin: draw a resolution seed: %w", err)
		}

		return rules.NewSeed(entropy)
	}
}

// ResolverConfig is the set of seams a `Resolver` is built over.
//
// A struct rather than four constructor arguments because they are one decision —
// which systems this process resolves under, and where its state and its entropy
// come from — and a caller handed four in the wrong order gets a resolver that
// resolves nothing and says nothing about why.
type ResolverConfig struct {
	// Systems is the gameplay registry. Required.
	Systems *Registry

	// States is the hub's live state registry, and the only authority this
	// package writes through.
	//
	// **A concrete type, not an interface.** Two reasons, and the second is the
	// important one. Mechanically, `internal/realtime` could not satisfy an
	// interface that narrowed its own `Get` without a wrapper, and a wrapper would
	// be a seam that exists to be a seam. Substantively: S-10.2's promise is about
	// `campaign_state`, and the only convincing proof of it is a test against the
	// real one — `TestAPanicInsideApplyLeavesTheHubStateByteIdentical` encodes the
	// document before and after a panicking system and compares the bytes.
	States *realtime.Registry

	// SystemOf reports a campaign's system. Required.
	SystemOf SystemOf

	// Seeds mints per-resolution entropy. Optional; `CryptoSeeds` when empty.
	Seeds SeedFunc
}

// Resolver is the `realtime.Resolver` a build registers, and the whole of what
// phase 8 does to the hub.
//
// One value, one process, and it satisfies the interface by construction:
//
//	var _ realtime.Resolver = (*Resolver)(nil)
//
// It is what replaces `realtime.Core` (ADR 0039), which resolved nothing so that
// phase 7 could be runnable on its own. The composition root hands it to
// `realtime.NewHub` in `Core`'s place and changes nothing else — which is the
// measure of how narrow this tier is meant to be.
type Resolver struct {
	systems  *Registry
	states   *realtime.Registry
	systemOf SystemOf
	seeds    SeedFunc
}

// Resolver is a `realtime.Resolver` by interface satisfaction, checked at compile
// time so a signature change in `internal/realtime` fails the build here rather
// than at the composition root's wiring.
var _ realtime.Resolver = (*Resolver)(nil)

// NewResolver returns the hub's resolver over the given seams.
//
// The refusals are the wiring faults: a missing registry, a missing state
// registry, a missing `SystemOf`. All three are mistakes in the composition root,
// all three are found here rather than on the first intent, and none of them is
// something a caller could sensibly retry.
func NewResolver(cfg ResolverConfig) (*Resolver, error) {
	if cfg.Systems == nil {
		return nil, fmt.Errorf("%w: no gameplay registry to resolve under", ErrMalformedPlugin)
	}

	if cfg.States == nil {
		return nil, fmt.Errorf(
			"%w: no campaign state registry to write through",
			ErrMalformedPlugin,
		)
	}

	if cfg.SystemOf == nil {
		return nil, fmt.Errorf(
			"%w: nothing can say which system a campaign plays under",
			ErrMalformedPlugin,
		)
	}

	seeds := cfg.Seeds
	if seeds == nil {
		seeds = CryptoSeeds()
	}

	return &Resolver{
		systems:  cfg.Systems,
		states:   cfg.States,
		systemOf: cfg.SystemOf,
		seeds:    seeds,
	}, nil
}

// Resolve is `realtime.Resolver`'s one method: one validated client intent in,
// the mutations the campaign's system made out of it out.
//
// ## The order, and why each step is where it is
//
//  1. **Cancellation first.** A cancelled request resolves nothing, and the hub
//     reports `server_error` for the refusal it cannot avoid.
//  2. **The frame's shape.** No frame, an op outside the wire's token shape, a
//     target that is not an object id, arguments over the bound — all
//     `invalid_args`. This is the *envelope*: the codec has already refused
//     unknown fields and every other grammar violation, and what is left is what
//     this adapter is responsible for translating.
//  3. **Which system.** `campaigns.system_id` through the registry. A missing one
//     is `server_error` naming the id (§10.8), because it is an operator's removal
//     rather than a client's mistake.
//  4. **The campaign's state.** Absent means the game has not been opened, which
//     §10.8's first row also calls an operator's condition: the wiki still serves.
//  5. **The rule context.** Campaign, actor and role from the **connection**, never
//     from the frame — a client frame has no field in which to name either, so a
//     system cannot be told which player is rolling by anything that player sent.
//     The seed is fresh for this resolution.
//  6. **`Apply`, inside the recovery boundary.** The mutations come back; the
//     adapter applies them itself, under the hub's lock, one at a time.
//  7. **The answer.** An `applied` for the actor's `seq` and a broadcast of every
//     change, or a refusal with one of the eight reasons and **nothing applied**.
//
// ## What is deliberately not here
//
// **No authorisation check and no operation table.** §7.2's GM-only rule lives in
// the system, which branches on `call.Role` and returns `RejectNotPermitted` — a
// list of GM-only ops here would be a second answer to a question `realtime.Core`
// already had to fake (`isGMOnlyShape` is a shape test, not an authority, and its
// comment says so). And membership of the operation vocabulary is the system's
// answer too: an op this adapter does not recognise is one the system refuses, and
// the refusal reaches the wire as whatever reason the system named.
//
// Ordering matters here, as it does in `Core`: the role check is the *system's* and
// happens inside `Apply`, after the adapter's envelope checks, so a player gets
// `invalid_args` for a malformed frame and `not_permitted` for a GM-only op. Neither
// word tells a non-member whether a campaign has that op at all.
func (r *Resolver) Resolve(
	ctx context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	if err := ctx.Err(); err != nil {
		return realtime.Resolution{}, fmt.Errorf("plugin: resolving an intent: %w", err)
	}

	if intent.Frame == nil {
		return realtime.Resolution{}, refusal(realtime.RejectInvalidArgs, ErrNoFrame)
	}

	requested, err := translate(intent)
	if err != nil {
		return realtime.Resolution{}, refusal(realtime.RejectInvalidArgs, err)
	}

	entry, call, err := r.rule(ctx, intent)
	if err != nil {
		return realtime.Resolution{}, err
	}

	state, live := r.states.Get(intent.Campaign)
	if !live {
		return realtime.Resolution{}, refusal(realtime.RejectServerError,
			fmt.Errorf("plugin: campaign %d has no live state", intent.Campaign))
	}

	// Everything from here to the end of `settle` is inside the recovery boundary,
	// and `settle` returns either changes or an error — never both.
	changes, err := settle(
		ctx, entry, state, call, requested, intent.Actor,
	)
	if err != nil {
		return realtime.Resolution{}, refusal(wireReason(err), err)
	}

	return realtime.Resolution{
		Answer:    answer(intent, changes, intent.Actor),
		Broadcast: changes,
	}, nil
}

// ErrNoFrame is a `realtime.Intent` with no frame.
//
// Its own sentinel because `realtime.Core` refuses this one without a wire reason
// and so does the adapter, and a caller asking "was this a frame problem?" should
// not have to match on the word "frame" inside a formatted sentence.
var ErrNoFrame = fmt.Errorf("%w: the intent carries no frame", ErrMalformedPlugin)

// rule resolves the two halves of everything a system is given: which system it
// is, and the context it is called with.
//
// Separate from `Resolve` because the two failures are different kinds. A missing
// system is §10.8's operator-visible condition and is refused **naming the id**. A
// refused context is a role this build has no name for — `rules.NewContext` checks
// it — which is a data fault in the row the access gate read.
func (r *Resolver) rule(
	ctx context.Context,
	intent realtime.Intent,
) (Entry, rules.Context, error) {
	id, err := r.systemOf(ctx, intent.Campaign)
	if err != nil {
		return Entry{}, rules.Context{}, refusal(realtime.RejectServerError,
			fmt.Errorf("plugin: the system for campaign %d: %w", intent.Campaign, err))
	}

	entry, err := r.systems.Resolve(id)
	if err != nil {
		return Entry{}, rules.Context{}, refusal(realtime.RejectServerError,
			fmt.Errorf("plugin: campaign %d: %w", intent.Campaign, err))
	}

	seed, err := r.seeds(ctx, intent.Campaign)
	if err != nil {
		return Entry{}, rules.Context{}, refusal(realtime.RejectServerError,
			fmt.Errorf("plugin: campaign %d: %w", intent.Campaign, err))
	}

	call, err := rules.NewContext(intent.Campaign, int64(intent.Actor), intent.Role, seed)
	if err != nil {
		return Entry{}, rules.Context{}, refusal(realtime.RejectServerError,
			fmt.Errorf("plugin: campaign %d, actor %d: %w", intent.Campaign, intent.Actor, err))
	}

	return entry, call, nil
}

// translate turns one client frame into the intent a system resolves.
//
// Three of the four fields are conversions, and the fourth is the one with a
// decision in it.
//
//   - `Op`: `realtime.Op` → `rules.Op`. The same characters, and the same rule —
//     `rules.Op.Valid` mirrors `validOpToken` to the letter, so an op this
//     accepts is an op the codec accepted and one the codec refused is refused
//     here first with a message that names the operation.
//
//   - `Target`: `realtime.PlacementID` → `rules.ObjectID`. **Empty is legal** —
//     `pause` and a campaign-wide roll name nothing on the table, and
//     `rules.Intent.Target`'s own comment says so. A non-empty one that is not a
//     usable object id is `invalid_args`, because a placement id that cannot
//     appear on a `delta` cannot be acted on.
//
//   - `Args`: the frame's typed `IntentArgs`, **encoded as JSON**. This is the one
//     worth arguing. `rules.Intent.Args` is `[]byte` and `rules.Mutation.Args` is
//     `[]byte`, and semiplane's job is to transport them without interpreting them
//     (S-12.3) — so the encoding must carry the frame's arguments and nothing
//     else, which is what `json.Marshal` of that one field is. `omitempty` on every
//     field means an argument the frame did not set is absent from the bytes, so a
//     system decoding them sees the frame's own optionality rather than a struct of
//     zeroes.
//
//     `rules.MaxOpArgsLen` is checked on the way **in**, and it is this package's
//     reminder to itself that the two bounds are one: `protocol.go`'s `maxArgsLen`
//     is the same 8 KiB on the outbound side, and `Mutation.Args` is checked against
//     the same constant before it reaches a broadcast. A limit enforced in one of
//     two places is a limit one caller routes around.
//
// `rules.NewIntent` builds the value rather than a struct literal, so the argument
// bytes are **copied** out of the frame's buffer: the buffer is the transport's, and
// a resolver that scribbled on it would corrupt a frame the codec still holds.
func translate(intent realtime.Intent) (rules.Intent, error) {
	operation := rules.Op(intent.Frame.Op)
	if !operation.Valid() {
		return rules.Intent{}, fmt.Errorf("%w: %q", rules.ErrInvalidOp, operation)
	}

	args, err := json.Marshal(intent.Frame.Args)
	if err != nil {
		return rules.Intent{}, fmt.Errorf("plugin: encode the arguments of %q: %w", operation, err)
	}

	target := rules.ObjectID(intent.Frame.Args.Placement)

	// Wrapped, and only to add the operation: `rules.NewIntent`'s refusals name the
	// op and the target already, and a caller reading this refusal wants to know
	// which intent it was about.
	translated, err := rules.NewIntent(operation, target, args)
	if err != nil {
		return rules.Intent{}, fmt.Errorf("plugin: translate %q: %w", operation, err)
	}

	return translated, nil
}

// answer builds the frame that resolves the actor's `seq`, or nil.
//
// **Nil for no changes, and that is the honest answer.** `Resolution.Answer` is
// optional by design and a resolution that applied nothing — a campaign-wide pause,
// a roll whose result the table already has — has no version to report, and an
// `applied` carrying a version of nothing would claim currency nobody established.
// `ServerApplied` documents the same: a version is per placement, so there is no
// meaningful zero.
//
// **The first change for several.** `ServerApplied` names one placement and one
// version, so a resolution that changed three things can only answer about one of
// them. The first is the answer for the overwhelmingly common case of a
// single-mutation resolution, and for the rest the `delta` carries every placement
// and version anyway — the actor's own optimistic copy reconciles against the
// delta, and this frame says "your intent was applied".
func answer(
	intent realtime.Intent,
	changes []realtime.Change,
	actor realtime.UserID,
) realtime.ServerFrame {
	if len(changes) == 0 {
		return nil
	}

	return &realtime.ServerApplied{
		Type:      realtime.TypeApplied,
		Seq:       intent.Frame.Seq,
		Version:   changes[0].Version,
		Placement: changes[0].Placement,
		Op:        changes[0].Op,
		Args:      changes[0].Args,
		By:        actor,
	}
}
