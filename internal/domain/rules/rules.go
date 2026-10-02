// The gameplay contract: the vocabulary a gameplay system implements and
// semiplane routes intents through.
//
// # What this package is for
//
// S-10.1 makes a plugin a compiled-in Go module registered explicitly at the
// composition root, and §10.2 states the difference between the tiers in one
// sentence: a gameplay plugin defines the operation vocabulary and resolves it
// into mutations, and a UI plugin may only emit operations some gameplay system
// already resolves. This package is the half of that sentence semiplane can
// enforce, and it is deliberately small — nine methods, no registry, no
// registration, no loading, no storage and no state. Everything in it is a pure
// type or a pure function over one.
//
// # Three rules, each of them in the type rather than in a document
//
//  1. **A system describes what changed; the hub applies it.** `Apply` returns
//     `[]Mutation` and takes the state by value (S-10.2). There is no
//     `campaign_state` pointer anywhere reachable from a `System`, so the
//     sentence "a panic inside `Apply` leaves `campaign_state` byte-identical"
//     is a consequence of the signature rather than a review promise — and
//     `State` keeps its objects unexported so the one slice a system could have
//     scribbled on is handed out as copies.
//
//  2. **Nothing ambient is reachable from a resolution.** `Context` carries a
//     campaign, an actor, a role and a `Seed`, and the only randomness it offers
//     is `Rand(label)`, a fresh deterministic source derived from that seed
//     (S-10.4). There is no clock field, no handle and no ambient source — so a
//     resolution has no clock to *read*, and the only way one could come to depend
//     on elapsed time or on the process-global random source is for a plugin to
//     reach for one itself. That call is still available to it; what refuses it is
//     P1d's lint rule and P1c's suite. Subtraction here is what makes S-14.6
//     reachable rather than aspirational: identical `(state, intent, seed)` yields
//     identical mutations, because the three inputs are the whole of the input.
//
//  3. **A plugin owns only what it needs.** `Kind` names the five kinds semiplane
//     owns (§10.2.1, S-3.4) and `Validate` refuses a `System` that declares one.
//     `token` and `scene` being semiplane's is what lets the PixiJS map layer
//     render placements, fog and initiative without knowing any rules, and so a
//     system sharing nothing with 5e needs no client work at all.
//
// # How this relates to `internal/realtime`
//
// Dependencies point inward and `domain` imports nothing from the project, so
// this package cannot import `internal/realtime` and cannot be imported by it
// without a cycle. The two vocabularies are therefore *related by naming*, and an
// adapter in the composition root is what translates:
//
//   - `realtime.Intent` (campaign, actor, role, validated frame) becomes a
//     `rules.Intent` plus a `rules.Context`.
//   - `[]rules.Mutation` becomes `realtime.Resolution`, whose `Broadcast` is
//     `[]realtime.Change` — the target placement, the op, the args, and the
//     **version the hub stamped**, never a version the system named.
//
// Three pairs name the same value in the two directions, and each is a distinct
// type on purpose so a conversion is a line somebody wrote: `rules.Op` ↔
// `realtime.Op`, `rules.ObjectID` ↔ `realtime.PlacementID`, and `Context`'s
// campaign/actor/role ↔ the three fields `realtime.Intent` carries from the
// connection. The last is the security-relevant one: a client frame has no field
// in which to name a campaign (`hub.go` says so), so an actor identity can only
// ever come from the peer the access gate admitted, and a system is handed what
// the connection proved rather than what the frame claimed.
//
// `rules.Mutation` is **not** `realtime.Mutation`. The hub's is a receipt —
// placement, version, revision, no op, no args — because the caller that requested
// the change already knows what it asked for. This package's is the statement:
// what changed, in this system's own words, with no version and no revision,
// because S-7.2 makes the version the only ordering authority and `state.go`
// stamps it after the caller's function returns. A system that could name a
// version could forge currency.
//
// # What is deliberately absent
//
// A registry (P1b), the conformance suite every system must pass (P1c), the
// determinism audit (P1d), the 5e engine and packs (P2), house rules (P2d) and
// any UI-plugin contract are all other work items and none of them belongs here.
// What belongs here is only the shape they are all typed against, which is why the
// whole package's import list is: `cmp`, `context`, `crypto/sha256`,
// `encoding/binary`, `encoding/hex`, `errors`, `fmt`, `math/rand/v2`, `regexp`,
// `slices`, `strings`, and `internal/domain` for the role vocabulary. **No `time`,
// no `os`, no I/O of any kind** — and one test holds that, by parsing this
// package's own imports rather than by trusting this paragraph.
//
// The deviations from §10.3's sketch — `Apply` taking a `context.Context`,
// `state *State` becoming a value, `Derive` returning `Payload` rather than `any`
// — are recorded in ADR 0041 rather than left as a silent divergence from a
// dated design record.

package rules

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// ErrMalformedSystem is the umbrella every refusal in this package satisfies, so a
// registry can ask one question — "is this plugin one I may register?" — with
// `errors.Is` and never name a sentinel. A refusal that does not satisfy it is a
// refusal a caller has to match on text, and text matching is how a log line ends up
// reporting our bug as theirs.
var ErrMalformedSystem = errors.New("rules: a system this contract cannot accept")

// The refusals that are about a **system as a whole** or about a registration in
// general. The rest sit beside the type whose operation returns them, which is the
// convention `internal/domain` already uses for `ErrInvalidRole`,
// `ErrInvalidVisibility` and `ErrInvalidSlug`, and which keeps a reader looking at
// `ParseID` from having to know which file `ErrInvalidID` is in.
//
// Each is `errors.Is`-testable, each carries a message naming the input that broke
// it, and none of them carries anything from a vault: an id, a kind name, a
// pattern. A plugin's identifiers are compiled in, so they are operator input
// rather than attacker input, and they are bounded — but a refusal message is still
// a log line and is written as if it were.
var (
	// ErrNoSystem is a nil System. It is distinct from a system that fails a check
	// because it is a wiring fault in the composition root rather than a fault in
	// a plugin, and the two have different owners.
	ErrNoSystem = fmt.Errorf("%w: there is none", ErrMalformedSystem)

	// ErrNoTitle is a system with no display name. An empty title renders an empty
	// heading in the one place a campaign's system is named, and "the system" is
	// not a name a GM can act on.
	ErrNoTitle = fmt.Errorf("%w: the system has no title", ErrMalformedSystem)

	// ErrNoRulesetVersion is a system whose `RulesetVersion` is empty. That half
	// of the fingerprint is what makes §10.8's "refuse to resume rather than
	// silently misresolve" possible at all: a campaign with an empty expected
	// ruleset has nothing to compare its persisted one against, so an upgrade that
	// changed resolution semantics would resolve the past quietly.
	ErrNoRulesetVersion = fmt.Errorf(
		"%w: the system reports no ruleset version, so a resume could not detect drift",
		ErrMalformedSystem,
	)

	// ErrInvalidView is a `View` outside the rules `View.Check` enforces: no name,
	// a name that is not a usable token, no title, a renderer that is neither of
	// the two, or a shape that disagrees with the renderer.
	ErrInvalidView = fmt.Errorf("%w: a declared view cannot be rendered", ErrMalformedSystem)

	// ErrNotAGrammar is a grammar with no notation name or no terms. A client
	// cannot preview a roll against an empty grammar, and §10.3's reason for
	// `Grammar` existing is that preview.
	ErrNotAGrammar = fmt.Errorf(
		"%w: the system declares no expression notation for a client to validate against",
		ErrMalformedSystem,
	)

	// ErrInvalidPattern is a grammar term whose pattern is not a valid RE2
	// expression. Refused at registration because a pattern that does not compile
	// is a preview that silently accepts everything.
	ErrInvalidPattern = fmt.Errorf(
		"%w: a grammar term's pattern is not a valid expression",
		ErrMalformedSystem,
	)

	// ErrKindOwned is the refusal of a `System` that claims a kind semiplane owns.
	// A `*OwnershipError` wraps it, naming the kind and the claimant, because
	// "a kind is owned" is not something a plugin author can act on and "system
	// `5e-2024` declares kind `token`" is.
	ErrKindOwned = fmt.Errorf(
		"%w: a system may only declare rules-content kinds; the game-object kinds are semiplane's",
		ErrMalformedSystem,
	)
)

// OwnershipError names the kind a system tried to claim and the system that tried
// to claim it.
//
// A typed error rather than only a formatted sentinel, for the reason the
// fingerprint's `DriftError` gives: the refusal has to be actionable, and an
// operator holding a refusal that says only "kind owned" has to go and read the
// plugin to find out which kind. The text is two plugin identifiers and nothing
// else, so it is safe in a log line; `Class` exists so the log line and the
// startup message say the same word through `observability.Classed`.
type OwnershipError struct {
	// Kind is the kind the system claimed.
	Kind Kind
	// System is the id of the system that claimed it.
	System ID
}

// Error returns the claim and the claimant, and nothing that came from a vault.
func (e *OwnershipError) Error() string {
	return "rules: system " + string(e.System) + " declares kind " + string(e.Kind) +
		", which semiplane owns"
}

// Unwrap returns ErrKindOwned, so `errors.Is` answers the one question most
// callers have and `errors.As` reaches the detail.
func (e *OwnershipError) Unwrap() error { return ErrKindOwned }

// Class returns the content-free class a log line should carry. `errorClass`
// falls back to `%T`, which for this type would be `*rules.OwnershipError` — an
// identifier no alert can match.
func (e *OwnershipError) Class() string { return "kind_owned" }

// System is a gameplay plugin: the whole of what a rules package implements.
//
// Nine methods, and each one's shape is load-bearing rather than convenient:
//
//   - `ID` is permanent. It is stored in `campaigns.system_id`, it is one
//     component of the ruleset fingerprint, and a rename strands every campaign
//     that named it — which is why §10.8 refuses an unknown id rather than
//     guessing at a replacement.
//   - `Grammar` and `Parse` are how a client previews and validates an expression
//     *before* sending it, and they exist because **the protocol never assumes
//     d20**. A d20 system and a 2d6-pool system describe different grammars, so a
//     grammar is data this system declares and `Expr` is a tree only this system
//     reads.
//   - `Apply` returns its changes and takes the state by value (S-10.2).
//   - `Derive` answers a render query with opaque data, and `Views` says how that
//     data becomes HTML — server-side, by templ, or by one of semiplane's built-in
//     renderers (S-10.7, §10.6).
//   - `ContentKinds` declares only rules-content kinds.
//
// A `System` is safe for concurrent use: the hub may resolve two intents for one
// campaign at once (S-7.2's per-placement versioning is what makes that
// possible), and the types handed in here are values with no interior state that
// outlives a call.
//
// A system that ships a broken data pack validates the pack in its own
// constructor. This interface has no `Check() error` and that is deliberate: a
// tenth method would make every future method a breaking change for every
// plugin, and the thing this package can check — the contract above — is
// exactly the thing a method on the interface cannot.
type System interface {
	// ID is the stable identifier for this system, e.g. `5e-2024`.
	//
	// Stored in `campaigns.system_id` and never renamed. A campaign whose id
	// resolves to nothing still serves its wiki and refuses only its game
	// (S-10.6), so a rename is a migration for every campaign that named it, not
	// a rename.
	ID() ID

	// Title is the name shown where a campaign's system is named to a person: the
	// system's own display name, not its id.
	Title() string

	// RulesetVersion is this system's half of the ruleset fingerprint: the version
	// of its resolution *semantics*, which is what changes when a pack revision
	// changes what an operation means.
	//
	// It is not the house-rule configuration, and ADR 0018 is the record for why
	// that distinction is load-bearing rather than pedantic.
	RulesetVersion() string

	// Grammar declares this system's expression notation, as data a client can
	// validate against before it sends anything.
	Grammar() Grammar

	// Parse turns notation text into an `Expr`, or refuses it.
	//
	// Server-side authority, called with the text the client sent and never with
	// text the client parsed. A client may *ask* for a roll; it never says what the
	// roll was (S-7.3), and this method is the boundary where a hostile string
	// becomes something the resolver will act on.
	Parse(expr string) (Expr, error)

	// Apply resolves one already-validated intent against one read-only state, and
	// returns the changes it wants made.
	//
	// **It returns them; it does not make them.** That is S-10.2, and it is the
	// whole reason a compiled-in plugin — which has no sandbox and runs in this
	// process with its full authority — cannot write `campaign_state` or forge a
	// broadcast. The hub applies each returned mutation under its own lock, stamps
	// the version, and assembles the frames.
	//
	// `ctx` is Go's cancellation context and `call` is this package's rule context.
	// They are different types with opposite rules and they are named differently
	// so neither can be passed where the other belongs. The `ctx` may **end** the
	// call: a system that observes cancellation returns `(nil, ctx.Err())` and never
	// a partial mutation list, because a list half-computed on the way to being
	// discarded is a list whose contents depend on *when* the hub cancelled. What
	// decides the result may only be `state`, `in` and `call`.
	//
	// Returning no mutations with no error is a legitimate answer: an intent the
	// system resolved to nothing changed. Returning mutations *and* an error is
	// not, and a hub that sees it should apply nothing.
	Apply(ctx context.Context, call Context, state State, in Intent) ([]Mutation, error)

	// Derive answers one render query with opaque data.
	//
	// Opaque is the contract: semiplane does not interpret the payload, does not
	// validate its fields, and does not know what a "hit bonus" is (§10.6.1). What
	// it does with it is hand it to whichever renderer `Views` named, which is why
	// `Payload` records which view it answers rather than being an `any` the caller
	// has to remember to match up.
	Derive(state State, query Query) (Payload, error)

	// Views declares how this system wants derived data rendered, and it is the
	// complete list: a system that ships no views is a system whose data is only
	// ever read by its own `Derive` callers.
	//
	// Each view is either backed by a templ component the plugin ships or served by
	// one of semiplane's built-in renderers (S-10.7).
	Views() []View

	// ContentKinds declares the rules-content kinds this system defines — `spell`,
	// `class`, `feat`, `creature`, `ancestry` and whatever else it needs.
	//
	// It may **not** declare `token`, `scene`, `journal`, `handout` or `index`:
	// those are semiplane's (§10.2.1, S-3.4) and `Validate` refuses a system that
	// does. The refusal is not pedantry — `token` and `scene` being semiplane's is
	// exactly what lets the client render a table without knowing the rules, so a
	// system that could redefine them would put client work back on the list for
	// every new system.
	ContentKinds() []Kind
}

// Validate reports whether a system is one this contract accepts, and refuses the
// first thing wrong with it.
//
// It exists because the alternative is a registry that trusts every registration,
// and a plugin is compiled in: a mistake in one is a mistake in the product, found
// by a GM at the table rather than by a developer at a laptop. Every refusal is
// `errors.Is(err, ErrMalformedSystem)`, and the order is the order a plugin author
// should be told about — identity, then the two strings that describe it, then
// what it claims to own, then what it wants rendered, then the notation a client
// would validate against.
//
// A nil `System` is refused. A **typed** nil is not: `Validate` calls methods, and
// a plugin registered as a nil pointer panics on the first of them, which is a
// loud failure at the composition root — the place that owns the wiring — rather
// than a reflection check in a package about game rules.
//
// This is a contract check and not a data check. It cannot see whether a pack
// parses; that is the system's own constructor's job, and a system whose packs
// are broken should never reach a registry.
func Validate(system System) error {
	if system == nil {
		return ErrNoSystem
	}

	if !system.ID().Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidID, system.ID())
	}

	if system.Title() == "" {
		return fmt.Errorf("%w: system %q has no title", ErrNoTitle, system.ID())
	}

	if system.RulesetVersion() == "" {
		return fmt.Errorf(
			"%w: system %q reports no ruleset version",
			ErrNoRulesetVersion,
			system.ID(),
		)
	}

	if err := checkKinds(system.ID(), system.ContentKinds()); err != nil {
		return err
	}

	if err := checkViews(system.ID(), system.Views()); err != nil {
		return err
	}

	return checkGrammar(system.ID(), system.Grammar())
}

// checkKinds applies the ownership table of §10.2.1 and S-3.4 to one system's
// declaration.
func checkKinds(owner ID, kinds []Kind) error {
	seen := make(map[Kind]struct{}, len(kinds))

	for _, kind := range kinds {
		if !kind.Valid() {
			return fmt.Errorf("%w: system %q declared %q", ErrInvalidKind, owner, kind)
		}

		if IsSemiplaneKind(kind) {
			// The typed error rather than a formatted sentinel: which kind is the
			// actionable half, and a refusal that does not name it sends the author
			// to the ownership table to find out which of their own five kinds it
			// was.
			return &OwnershipError{Kind: kind, System: owner}
		}

		if _, duplicate := seen[kind]; duplicate {
			return fmt.Errorf("%w: system %q declared %q twice", ErrDuplicateKind, owner, kind)
		}

		seen[kind] = struct{}{}
	}

	return nil
}

// checkViews reports the first declared view that could not be rendered.
func checkViews(owner ID, views []View) error {
	for _, view := range views {
		if err := view.Check(); err != nil {
			return fmt.Errorf(
				"%w: system %q declared view %q: %w",
				ErrInvalidView,
				owner,
				view.Name,
				err,
			)
		}
	}

	return nil
}

// checkGrammar reports the first thing wrong with a declared notation, including
// a pattern that does not compile.
//
// The compile is the part worth having. A grammar's whole job is to let a client
// refuse an expression before sending it (§10.3), and a pattern that does not
// compile is a preview that accepts everything and says nothing — a client
// validating a roll against it would learn that its input was fine.
func checkGrammar(owner ID, grammar Grammar) error {
	if grammar.Notation == "" || len(grammar.Terms) == 0 {
		return fmt.Errorf("%w: system %q declared %q", ErrNotAGrammar, owner, grammar.Notation)
	}

	for _, term := range grammar.Terms {
		if _, err := regexp.Compile(term.Pattern); err != nil {
			// The underlying error's text names the character RE2 choked on, and
			// that character came from a compiled-in plugin rather than from a
			// vault — so this is safe to quote, and it is the actionable half.
			return fmt.Errorf(
				"%w: system %q, term %q: %w", ErrInvalidPattern, owner, term.Name, err,
			)
		}
	}

	return nil
}
