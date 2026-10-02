// Ruleset-version gating: the fingerprint, the refusal, and the escape (D17, D18).
//
// # What the fingerprint is, and the one thing it is not
//
// ADR 0018 is the record this file implements and it settles the question the
// architecture overview's §10.5 chain leaves open. Read literally, §10.5 resolves
// a campaign's ruleset as "system ID → base pack → overlay → enabled house-rule
// modules", which would put the enabled house-rule set *inside* the version that
// gates resuming a game. That reading is wrong, and it is wrong in a way that
// makes a routine action look like a bug: a GM enabling "critical hits" at the
// table would be refused on their own campaign the next time it resumed.
//
// So `Fingerprint` has four fields and `Descriptor` has five, and the fifth —
// `HouseRules` — is **read by nothing**. That is the whole exclusion, and it is
// only as good as the test that holds it:
// `TestTheFingerprintExcludesHouseRules` toggles a house rule and requires the
// encoded bytes to be identical, because an exclusion asserted only in a comment
// is an exclusion that a later edit to "also include the house rules, it is more
// informative" removes silently.
//
// The exclusion is safe rather than convenient because house rules are
// **data-level** (ADR 0018, ADR 0012): a module may toggle a flag, change a DC
// constant, disable a condition, and may not reorder resolution or introduce
// nondeterminism. So a house rule changes outcomes, not the meaning of a stored
// mutation, and a campaign that toggled one mid-game has nothing to misresolve.
// A pack revision is the opposite case and **does** gate resume, which is why
// `TestTheFingerprintChangesWithAPackVersion` exists: without it, the exclusion
// test would be satisfied by a fingerprint that never changes at all.
//
// # Why the fingerprint is an encoded string and not a column per input
//
// `campaigns.ruleset_version` is a single TEXT column (migration 0005) and it is
// stated there as "the empty string is a meaningful value — state written under no
// particular ruleset". So the structured value is *encoded* into that column rather
// than spread across four, and the encoding is parsed back on every resume. Two
// reasons the encoding is worth the round trip:
//
//   - It is **deterministic**. A fixed component order and a fixed separator mean
//     the same inputs always produce the same bytes, so "did the fingerprint
//     change" is a string comparison rather than a question about map order. The
//     same argument `Document`'s sorted placement list makes in `state.go`.
//   - It is **readable in the place it has to be read**. ADR 0018 requires
//     `/c/{slug}/status` to *name* the persisted version and the expected one, and
//     a status page reading "system=5e;ruleset=5e-2024;base=core-1.0.0;overlay="
//     is a page a GM can act on. An opaque hash is not.
//
// A component carrying a separator is **refused** rather than escaped. Escaping
// would be correct and would also mean a parser with three edge cases, and a
// plugin whose id contains a semicolon is a plugin whose id should be renamed — so
// the error names the field and the value, which is the actionable half.
//
// # Refusal names three things, or it is not a refusal
//
// §10.8's row is "refuse to resume rather than silently misresolve", and a refusal
// that says only "incompatible" has moved the diagnosis onto the reader.
// `DriftError.Error` names the **persisted** version, the **expected** version, the
// **input that differs**, and **what happens now**, in one sentence a GM can read at
// the table. A test asserts all four are present, because the version of this
// message that omits the expected version still reads like a finished message.
// `DriftError.Class` exists so the log line and the page say the same word, through
// `observability.Classed` rather than a chain somebody has to edit.
//
// # The escape is a separate act, and it is audited
//
// Recovery is `Prepare` then `Discard`, and the split is the confirmation gate
// (S-7.8). `Prepare` reads and mints a `DiscardConfirmation` bound to *the exact
// revision it read*; `Discard` re-reads the row and refuses unless it is still at
// that revision. So the gate has three teeth, each testable on its own:
//
//  1. There is no path from a read to a delete. `Prepare` writes nothing, and every
//     field of a `DiscardConfirmation` is unexported, so no caller can fabricate
//     one — the only constructor in the package is the one that read the row.
//  2. A confirmation is spent by its own staleness. A GM who read the versions,
//     closed the page, and came back an hour later to a game that has since
//     advanced is refused (`ErrDiscardStale`), because what they confirmed was a
//     game that no longer exists.
//  3. It is GM-only, at the **operation** and not only at the route. ADR 0024
//     mounts authorisation as a gate and forbids a handler checking for itself, and
//     this is not a violation of that: `Discard` has no overload without a
//     `domain.Tier`, so the route's mounted gate is the only way to reach it, and
//     the operation refuses a tier that somehow is not `TierGM` rather than
//     trusting that it could not happen.
//
// The deletion and the `audit_log` row are **one transaction**. That pairing is the
// reason `audit_log` exists at all (migration 0009): "the state is gone and the
// record of why is missing" is the failure, and a best-effort log line cannot
// prevent it while a transaction can. A test drops the audit table and requires the
// state to survive.
//
// # What the audit row is allowed to say
//
// S-12.3 forbids an event carrying secret content, and a state document is a bag of
// everything on the tabletop, so the row carries the **shape** of what was lost —
// the two versions, the revision, the placement count — and none of it. Those
// values are plugin identifiers, version strings, and integers this package minted;
// a placement's fields cannot reach the `detail` string because the only call site
// formats counts and versions.
//
// # No new event name
//
// A drift is reported to the log through `plugin.version_mismatch` (§13.2, "Unknown
// `system_id` or `ruleset_version`"), which is the signal that already means "this
// campaign cannot start its game and its wiki must still serve". A new name would
// move the count `observability.AllEventNames` asserts at 24, and would split one
// operator's alert into two queries that each answer "did this one thing happen".

package realtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/semiplane/semiplane/internal/domain"
)

// fingerprintFormat versions the encoding, and is the prefix every fingerprint
// begins with.
//
// Present because the column will outlive this build: a future release that adds an
// input must be able to refuse a row written by an older one rather than parse it
// as a shorter fingerprint and report the campaign as drifted for a reason that is
// an artefact of the upgrade. The same reasoning `state.go`'s `documentMagic` gives,
// on the adjacent column.
const fingerprintFormat = "sp1"

// fingerprintSeparator divides the components, and fingerprintAssign separates a
// component's name from its value.
//
// Both are refused inside a value, so the encoding is unambiguous in one direction
// without an escaping table — see the header.
const (
	fingerprintSeparator = ";"
	fingerprintAssign    = "="
)

// ErrRulesetDrift means a campaign's persisted state was written under a different
// ruleset fingerprint from the one this process has registered, and so is refused
// rather than re-resolved (S-7.8, architecture §10.8).
//
// A `*DriftError` wraps it, so a caller asks `errors.Is(err, ErrRulesetDrift)` and a
// handler that wants the detail reaches for the concrete type — the shape
// `protocol.go` uses for a frame refusal.
var ErrRulesetDrift = errors.New("realtime: persisted state was written under a different ruleset")

// ErrRulesetUnreadable means a campaign's persisted `ruleset_version` could not be
// compared by this build.
//
// A distinct error from `ErrRulesetDrift` on purpose: a fingerprint this build
// cannot read is not drift, it is a row written by something else, and telling a GM
// to discard their game over an unparseable column would be the software inflicting
// the loss ADR 0018 exists to keep it from inflicting. It is still **refused** —
// guessing at a version is the "silently misresolve" outcome — but the recovery is
// a migration, not a discard.
var ErrRulesetUnreadable = errors.New("realtime: persisted ruleset_version is not comparable")

// ErrDiscardForbidden is the umbrella for every refusal of the discard, so a route
// can apply S-8's "no access is 404, never 403" rule with one `errors.Is` and the
// sentinels below name which precondition failed.
var ErrDiscardForbidden = errors.New("realtime: discarding persisted state is not permitted")

var (
	// ErrDiscardNotGM is a requestor whose tier is below `domain.TierGM`.
	ErrDiscardNotGM = fmt.Errorf(
		"%w: the requestor is not this campaign's GM", ErrDiscardForbidden,
	)

	// ErrDiscardNoActor is a discard with no authenticated account behind it, and so
	// no `audit_log.actor_id` to write. A row that cannot be attributed is not an
	// audit record, and migration 0009 makes `actor_id` NOT NULL for that reason
	// rather than allowing an anonymous discard to be recorded as nobody's.
	ErrDiscardNoActor = fmt.Errorf(
		"%w: the discard has no authenticated account behind it to record in audit_log",
		ErrDiscardForbidden,
	)
)

// ErrDiscardUnconfirmed is a `Discard` with no `DiscardConfirmation`, and so with no
// evidence that a GM read what they are about to lose.
//
// It is the refusal that makes "reaching the discard requires the explicit act" a
// property of the API rather than a property of a page.
var ErrDiscardUnconfirmed = errors.New(
	"realtime: discarding persisted state requires a prepared confirmation",
)

// ErrDiscardStale is a confirmation for state that has moved on since it was
// prepared: the GM confirmed a game that no longer exists.
var ErrDiscardStale = errors.New(
	"realtime: the persisted state changed since the discard was prepared",
)

// ErrDiscardForeign is a confirmation prepared for a different campaign than the one
// being discarded.
var ErrDiscardForeign = errors.New(
	"realtime: the discard confirmation was prepared for another campaign",
)

// ErrNoDiscardableState means there is no `campaign_state` row to discard.
//
// A real state rather than a drift: a campaign nobody has opened has no game, and
// "there is nothing to lose" is not the same finding as "your game is
// incompatible". It is still refused rather than treated as success, because a
// caller that believes it discarded something and did not is a caller whose
// recovery path has silently stopped working.
var ErrNoDiscardableState = errors.New("realtime: the campaign has no persisted state to discard")

// Component names one input of a fingerprint.
//
// A type rather than a string so the closed set is declared once, and so
// `exhaustive` holds a future edit to the set: adding an input to `Fingerprint`
// without adding its `Component` is a compile error rather than a status page that
// says "the ruleset differs" and does not say which.
type Component string

const (
	// ComponentNone is the answer for a fingerprint that matches.
	ComponentNone Component = ""

	// ComponentSystem is the system id.
	ComponentSystem Component = "system"

	// ComponentRuleset is the system's own `RulesetVersion()`.
	ComponentRuleset Component = "ruleset"

	// ComponentBasePack is the base data pack's version.
	ComponentBasePack Component = "base pack"

	// ComponentOverlayPack is the overlay data pack's version, empty for a system
	// that ships one standalone pack (architecture §10.4: overlay is a strategy
	// among several, not a requirement).
	ComponentOverlayPack Component = "overlay pack"

	// ComponentSeveral is the answer when more than one input differs, which is the
	// common case after an upgrade: a system and its base pack move together.
	// Naming only the first would be a lie of omission, and the GM would fix one
	// input and find the other waiting.
	ComponentSeveral Component = "several inputs"
)

// componentOrder is the order components are compared in and encoded in.
//
// Fixed rather than derived, because the encoded form is persisted and a
// fingerprint whose spelling depended on Go's map iteration would make every resume
// a coin toss. This order is the tie-break for `Diff` too, so the first difference
// is always the same input.
var componentOrder = []Component{
	ComponentSystem,
	ComponentRuleset,
	ComponentBasePack,
	ComponentOverlayPack,
}

// componentKeys is the persisted name of each component, positionally paired with
// `componentOrder`.
//
// Separate from the `Component` values, and the separation is load-bearing: a
// `Component` is a **label for a person** ("base pack", with a space, because a GM
// reads it on a status page) and a key is a **name in a stored column**. Coupling
// them would mean rewording a label silently invalidating every fingerprint ever
// written, so a copy edit in the documentation would cost every campaign a resume.
// The two lists are positionally paired and `componentKey`/`componentForKey` are the
// only places that convert, so reordering one without the other is a panic at the
// conversion rather than a silent remapping.
var componentKeys = []string{"system", "ruleset", "base", "overlay"}

// componentKey returns the persisted name of a component.
func componentKey(component Component) string {
	return componentKeys[slices.Index(componentOrder, component)]
}

// componentForKey returns the component a persisted name denotes.
func componentForKey(key string) (Component, bool) {
	at := slices.Index(componentKeys, key)
	if at < 0 {
		return ComponentNone, false
	}

	return componentOrder[at], true
}

// HouseRule is one house-rule module, as a campaign has it configured.
//
// It exists in this file for one reason: to be the thing `FingerprintOf` **does not
// read**. A type that is declared and deliberately unused is the only way to make
// an exclusion executable — a test toggles `Enabled` and requires the fingerprint to
// be byte-identical, which is a claim the compiler and that test between them make
// impossible to lose by accident.
//
// `ModuleID` and `Position` are part of the same claim. ADR 0018 requires conflicts
// to resolve first-match-wins by `position` with both module ids logged, so a
// fingerprint that read the *ordering* would strand a campaign over a re-order that
// changes nothing except which module answers a conflict that is not being hit.
type HouseRule struct {
	// ModuleID is the plugin module's stable id.
	ModuleID string

	// Position is the declared order, and first-match-wins is resolved on it.
	Position int

	// Enabled is whether the campaign has the module switched on.
	//
	// The field the exclusion is about. Toggling it must not move the fingerprint,
	// and `TestTheFingerprintExcludesHouseRules` says so by comparing the encoded
	// bytes rather than by asserting a comment.
	Enabled bool
}

// Descriptor is what a campaign resolved to: the gameplay system, the data packs,
// and the enabled house-rule modules.
//
// The struct a composition root or a plugin registry fills in, and the only input
// `FingerprintOf` takes. `HouseRules` is part of it because excluding it *at the
// source* is stronger than excluding it downstream: there is no second path by
// which a house rule could reach the version, which is the failure mode of
// computing a fingerprint from fields the caller chose to omit.
type Descriptor struct {
	// System is the gameplay plugin's stable id, stored on the campaign and never
	// renamed (architecture §10.3).
	System string

	// Ruleset is that system's `RulesetVersion()`.
	Ruleset string

	// BasePack is the base data pack's version.
	BasePack string

	// OverlayPack is the overlay data pack's version, or empty for a system that
	// ships one standalone pack.
	OverlayPack string

	// HouseRules is the campaign's house-rule modules in declared order.
	//
	// **Not an input to the fingerprint.** Read the header before changing anything
	// in this struct; ADR 0018 is the record, and the reason is the one sentence
	// that matters: toggling a house rule must not strand a campaign.
	HouseRules []HouseRule
}

// Fingerprint is the identity of a campaign's **resolution semantics**: the inputs
// that decide what a persisted mutation means when it is replayed (S-7.7).
//
// Four fields, and the absence of a fifth is the decision. Everything the
// fingerprint names is a thing whose *meaning* can change without anyone editing
// semiplane: a plugin release, a data pack revision. Everything it omits is a thing
// that changes *outcomes* and is applied afresh at resolution time — the house
// rules.
type Fingerprint struct {
	// System is the gameplay plugin's id.
	System string

	// Ruleset is the system's `RulesetVersion()`.
	Ruleset string

	// BasePack is the base data pack's version.
	BasePack string

	// OverlayPack is the overlay data pack's version, empty for a standalone pack.
	OverlayPack string
}

// Empty reports whether the fingerprint names no ruleset at all.
//
// The zero value's question, and it is the question `persistedText` asks of a
// campaign whose column could not be parsed. Not a "is this valid" predicate: a
// fingerprint with only some components set is invalid and `validate` says so, and
// mixing the two would let an invalid fingerprint read as merely absent.
func (f Fingerprint) Empty() bool { return f == Fingerprint{} }

// FingerprintOf returns the fingerprint of a resolved descriptor.
//
// The one line that implements ADR 0018's exclusion, and it is worth reading the
// body before changing it: `d.HouseRules` appears nowhere in it, and that is the
// reason. Adding it "for completeness" would make enabling a house rule a reason a
// GM is refused on their own campaign — a bug that reads as a feature, because the
// error would be perfectly reasonable on its face.
//
// The error is for a descriptor that cannot be encoded, not for a campaign that
// cannot resume. A refused *value* is a wiring fault and fails at registration,
// where the plugin author sees it; a mismatched *value* is a GM's decision and fails
// at resume, where the GM sees it. Conflating the two would make a plugin with a
// semicolon in its id look like a campaign with incompatible state.
func FingerprintOf(descriptor Descriptor) (Fingerprint, error) {
	fingerprint := Fingerprint{
		System:      descriptor.System,
		Ruleset:     descriptor.Ruleset,
		BasePack:    descriptor.BasePack,
		OverlayPack: descriptor.OverlayPack,
	}

	if err := fingerprint.validate(); err != nil {
		return Fingerprint{}, err
	}

	return fingerprint, nil
}

// checkComponent refuses one component's value, and says which one it was.
func checkComponent(component Component, value string) error {
	if value == "" && component != ComponentOverlayPack {
		return fmt.Errorf(
			"realtime: fingerprint has no %s; a fingerprint of nothing cannot be told apart from a "+
				"campaign written under no particular ruleset, which is a different thing",
			component,
		)
	}

	if strings.ContainsAny(value, fingerprintSeparator+fingerprintAssign) {
		return fmt.Errorf(
			"realtime: fingerprint %s is %q, which contains a reserved separator; the encoding does "+
				"not escape, and a plugin identifier carrying one should be renamed",
			component,
			value,
		)
	}

	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf(
				"realtime: fingerprint %s contains a control character, which could not be parsed back",
				component,
			)
		}
	}

	return nil
}

// String returns the encoded fingerprint, which is what `campaigns.ruleset_version`
// holds and what ADR 0018 requires `/c/{slug}/status` to name.
//
// One line, fixed order, no escaping — the format makes a statement about values
// that are identifiers and version strings, and a format that could not be read back
// is not one worth persisting.
func (f Fingerprint) String() string {
	parts := make([]string, 0, len(componentOrder))

	for _, component := range componentOrder {
		parts = append(parts, componentKey(component)+fingerprintAssign+f.value(component))
	}

	return fingerprintFormat + ":" + strings.Join(parts, fingerprintSeparator)
}

// Equal reports whether two fingerprints name the same resolution semantics.
func (f Fingerprint) Equal(other Fingerprint) bool {
	return f == other
}

// Diff reports which input differs between two fingerprints, `ComponentNone` when
// they are equal, and `ComponentSeveral` when more than one does.
//
// Exists so the refusal and the status page can say *which* input moved. "The
// ruleset differs" leaves a GM guessing between three things they could each fix
// differently, and a GM who guesses wrong has a second refusal to read.
func (f Fingerprint) Diff(other Fingerprint) Component {
	differing := 0
	first := ComponentNone

	for _, component := range componentOrder {
		if f.value(component) == other.value(component) {
			continue
		}

		differing++

		if first == ComponentNone {
			first = component
		}
	}

	switch differing {
	case 0:
		return ComponentNone
	case 1:
		return first
	default:
		return ComponentSeveral
	}
}

// value returns one component's value, and is the switch `String`, `Diff` and
// `ParseFingerprint` share so the three cannot disagree about which field is which.
//
// An exhaustive switch with no default, because `exhaustive` holds this file to
// `componentOrder`: a new `Component` is a compile error here rather than an empty
// string in a persisted fingerprint.
func (f Fingerprint) value(component Component) string {
	switch component {
	case ComponentSystem:
		return f.System
	case ComponentRuleset:
		return f.Ruleset
	case ComponentBasePack:
		return f.BasePack
	case ComponentOverlayPack:
		return f.OverlayPack
	default:
		// Unreachable through `String` or `Diff`, which both iterate componentOrder.
		// Present because a function with an implicit zero return is a function whose
		// failure is invisible.
		return ""
	}
}

// validate refuses a fingerprint that could not be encoded unambiguously.
//
// A component may not be empty where emptiness is not a legal value, and no
// component may contain a separator, an assignment, or a control character. The
// first rule is what stops a fingerprint that is the empty string: an "everything
// empty" fingerprint is indistinguishable from a campaign written under no
// particular ruleset, which migration 0005 says is a *meaningful* value, and the
// two would be one value meaning two things.
func (f Fingerprint) validate() error {
	fields := []struct {
		component Component
		value     string
	}{
		{ComponentSystem, f.System},
		{ComponentRuleset, f.Ruleset},
		{ComponentBasePack, f.BasePack},
		// Overlay is exempt: architecture §10.4 makes a standalone pack a
		// first-class shape, so "no overlay" is a fact about the system rather than
		// a missing input.
		{ComponentOverlayPack, f.OverlayPack},
	}

	for _, field := range fields {
		if err := checkComponent(field.component, field.value); err != nil {
			return err
		}
	}

	return nil
}

// ParseFingerprint reads the encoded form back into a fingerprint.
//
// Needed on every resume, because the column is one TEXT value and the comparison
// has to be per-input for `Diff` to name anything. It is a parser and not a
// `strings.Contains` because a permissive parse is how a truncated or foreign row
// becomes a plausible-looking fingerprint — the exact failure `state.go`'s
// `documentMagic` refuses, on the adjacent column.
//
// The order is checked, not sorted: the encoding writes components in
// `componentOrder`, and a row in any other order is a row this build did not write,
// which is `ErrRulesetUnreadable` rather than a fingerprint.
func ParseFingerprint(encoded string) (Fingerprint, error) {
	prefix := fingerprintFormat + ":"

	body, prefixed := strings.CutPrefix(encoded, prefix)
	if !prefixed {
		return Fingerprint{}, fmt.Errorf(
			"%w: %q does not begin with %q; a value written by another build cannot be compared",
			ErrRulesetUnreadable, encoded, prefix,
		)
	}

	parts := strings.Split(body, fingerprintSeparator)
	if len(parts) != len(componentOrder) {
		return Fingerprint{}, fmt.Errorf(
			"%w: %q has %d components, want %d",
			ErrRulesetUnreadable, encoded, len(parts), len(componentOrder),
		)
	}

	parsed := make(map[Component]string, len(parts))

	for index, part := range parts {
		name, value, assigned := strings.Cut(part, fingerprintAssign)
		if !assigned {
			return Fingerprint{}, fmt.Errorf(
				"%w: %q has a component with no value", ErrRulesetUnreadable, encoded,
			)
		}

		want := componentOrder[index]

		got, known := componentForKey(name)
		if !known || got != want {
			return Fingerprint{}, fmt.Errorf(
				"%w: %q has %s where %s was expected; components are written in a fixed order",
				ErrRulesetUnreadable, encoded, name, componentKey(want),
			)
		}

		parsed[got] = value
	}

	fingerprint := Fingerprint{
		System:      parsed[ComponentSystem],
		Ruleset:     parsed[ComponentRuleset],
		BasePack:    parsed[ComponentBasePack],
		OverlayPack: parsed[ComponentOverlayPack],
	}

	// Re-validated, because the bytes came from a column and a column can hold
	// anything. A parse that produced an empty system id would otherwise be
	// compared against a real one and reported as drift, sending a GM to discard a
	// game over a row that was never valid.
	if err := fingerprint.validate(); err != nil {
		return Fingerprint{}, fmt.Errorf("%w: %w", ErrRulesetUnreadable, err)
	}

	return fingerprint, nil
}

// DriftError is the refusal: a campaign's state was written under one fingerprint and
// this process has registered another (S-7.8).
//
// A concrete error rather than a formatted string, so a handler can read the
// versions as fields — which is what `/c/{slug}/status` renders — while `Error`
// carries the sentence for a log and a plain-text alert. `Class` makes
// `observability.ErrorClass` report `ruleset_drift` rather than this type, which is
// the same mechanism `protocol.FrameError` uses and the reason it is a convention
// rather than a chain somebody has to edit.
type DriftError struct {
	// CampaignID is the campaign that cannot resume.
	CampaignID int64

	// Persisted is what the state was written under, parsed.
	Persisted Fingerprint

	// Expected is what this process has registered.
	Expected Fingerprint

	// Component is which input differs, and what the status page points at.
	Component Component
}

// Error names the persisted version, the expected version, and the consequence.
//
// Not "incompatible". §10.8's requirement is a refusal a GM can act on, and the
// three facts an action needs are which version the game is sitting under, which one
// the server would resolve it under, and what will not happen. A test asserts all
// three appear, because the version of this sentence that drops the expected version
// still reads like a finished message.
func (d *DriftError) Error() string {
	return fmt.Sprintf(
		"realtime: campaign %d cannot resume: the persisted state was written under %q, the "+
			"registered ruleset is %q, and the %s differs; the game will not resume until the GM "+
			"discards the persisted state, because mutations resolved under the older ruleset may "+
			"not replay under the newer one",
		d.CampaignID, d.Persisted, d.Expected, d.Component,
	)
}

// Is reports target as `ErrRulesetDrift`, so a caller branches on the umbrella.
func (d *DriftError) Is(target error) bool { return target == ErrRulesetDrift }

// Class names this refusal for a log line's `detail`, through `observability.Classed`.
func (d *DriftError) Class() string { return "ruleset_drift" }

// VersionReader reads the `ruleset_version` a campaign's state was last written
// under, exactly as the column holds it.
//
// A function type for the mechanical reason `state.Reader` gives — `store.Store`
// takes an unexported closure type, so no interface outside `store` can name the
// query — and because a gate should be assertable against a table without a live
// `*store.Store`. An empty string is a real answer: migration 0005 defines it as
// "state written under no particular ruleset".
type VersionReader func(ctx context.Context, campaignID int64) (string, error)

// Status is the answer to "may this campaign resume, and if not, why", which is
// what `/c/{slug}/status` renders and what the `/play` alert is built from.
//
// A value rather than a pair of methods because the two facts are read together, and
// a caller that asked twice could be handed two campaigns' answers — a hazard that
// is invisible until a page shows a mismatch nobody has.
type Status struct {
	// Persisted is the `ruleset_version` column's value, verbatim, and empty when
	// the campaign has never been fingerprinted.
	Persisted string

	// Expected is the fingerprint this process has registered.
	Expected Fingerprint

	// Unfingerprinted is whether the persisted value is the empty string, which
	// migration 0005 defines as "state written under no particular ruleset".
	//
	// It is reported and not refused, and the reason is the same one that makes the
	// house-rule exclusion safe: there is nothing to compare, so there is no claim
	// that the semantics differ, and refusing would strand every campaign created
	// before this column was populated — a refusal with no recovery, which is the
	// outcome ADR 0018 exists to prevent.
	Unfingerprinted bool

	// Resumable is whether the live state may be opened.
	Resumable bool

	// Drift is the refusal, or nil. Non-nil exactly when `!Resumable` and
	// `Unreadable` is nil, so a caller that renders the alert from this field and
	// the resume decision from that one cannot disagree.
	Drift *DriftError

	// Unreadable is the comparison error, or nil. Set when the column holds
	// something this build cannot compare, and mutually exclusive with `Drift`: a
	// row this build cannot read is not a campaign whose game is incompatible, and
	// telling a GM to discard for one would be the software inflicting the loss.
	Unreadable error
}

// Gate decides whether a campaign's persisted state may be resumed.
//
// One per process rather than one per campaign, because the fingerprint it holds is
// the process's *registered* ruleset and a campaign supplies only the version it was
// written under. The alternative — a gate per campaign — would mean each campaign
// carries a copy of the same answer, and the copies are exactly what drifts.
type Gate struct {
	expected Fingerprint
	read     VersionReader
}

// NewGate returns a gate over the registered fingerprint and a column reader.
//
// A nil reader is tolerated at construction and **refused at use**, failing closed.
// The alternative — treating an absent reader as "no drift" — is a gate wired to
// nothing that reports success, and a gate that cannot fail is worse than no gate
// because it is trusted. The failure names the seam, the same way `Registry.Open`
// refuses a nil writer rather than running a state that could never be persisted.
func NewGate(expected Fingerprint, read VersionReader) *Gate {
	return &Gate{expected: expected, read: read}
}

// Expected returns the fingerprint the gate compares against.
func (g *Gate) Expected() Fingerprint { return g.expected }

// Inspect resolves a campaign's resume status without deciding anything.
//
// Read-only by construction, and separate from `Check` because ADR 0018 requires the
// same facts on two pages — a GM alert on `/play` and `/c/{slug}/status` — and two
// functions that each read the column could disagree. Every refusal happens here,
// and `Check` is this with a decision on top.
func (g *Gate) Inspect(ctx context.Context, campaignID int64) (Status, error) {
	status := Status{Expected: g.expected}

	if campaignID <= 0 {
		return status, fmt.Errorf("%w: campaign id %d is not a campaign", ErrNoState, campaignID)
	}

	if g.read == nil {
		return status, fmt.Errorf(
			"%w: no ruleset_version reader is configured, so no comparison could be made",
			ErrRulesetUnreadable,
		)
	}

	persisted, err := g.read(ctx, campaignID)
	if err != nil {
		// Wrapped and not swallowed: an unreadable column is not an absent
		// fingerprint, and answering "resumable" for a database that is not
		// answering is the fail-open this file exists to avoid.
		return status, fmt.Errorf("realtime: read campaign %d ruleset_version: %w", campaignID, err)
	}

	status.Persisted = persisted
	status.Resumable = true

	// The empty column, handled before any parse: `ParseFingerprint("")` is an error,
	// and "no particular ruleset" is not an unreadable version.
	if persisted == "" {
		status.Unfingerprinted = true

		return status, nil
	}

	parsed, err := ParseFingerprint(persisted)
	if err != nil {
		status.Resumable = false
		status.Unreadable = err

		//nolint:nilerr // The refusal is the *answer*, not a failure to produce one:
		// `Inspect` is what a status page renders, and a page that answered with an
		// error instead of a status would be a page that cannot say "this campaign
		// cannot resume and here is why". `Check` is the function that turns this
		// into a returned error, and it is one line below.
		return status, nil
	}

	if parsed.Equal(g.expected) {
		return status, nil
	}

	status.Resumable = false
	status.Drift = &DriftError{
		CampaignID: campaignID,
		Persisted:  parsed,
		Expected:   g.expected,
		Component:  parsed.Diff(g.expected),
	}

	return status, nil
}

// Check reports whether a campaign's persisted state may be resumed, returning the
// `*DriftError` when it may not.
//
// The call a composition root makes **before** the live state is opened, and the
// ordering is the only one that means anything: opening first would write a fresh
// row under the new fingerprint and destroy the evidence that there had ever been a
// difference to report.
func (g *Gate) Check(ctx context.Context, campaignID int64) error {
	status, err := g.Inspect(ctx, campaignID)
	if err != nil {
		return err
	}

	if status.Drift != nil {
		return status.Drift
	}

	return status.Unreadable
}

// Resume opens a campaign's live state, refusing on drift first.
//
// It exists because the *order* is the whole of the gate and an order is easy to get
// wrong in a composition root. `Registry.Open` does not consult a gate, and it cannot:
// the fingerprint is a column on `campaigns` and `state.go` does not read that table.
// A root that opens first and checks afterwards has, by the time it checks, written a
// fresh `campaign_state` row under the new fingerprint — the evidence that there had
// ever been a difference is gone, and the refusal that follows names a game that is no
// longer on disk.
//
// One call cannot be ordered wrongly, so this is the one a composition root should
// use. A root that needs the two steps apart can still call `Check` and `Open`
// itself, and the documentation on `Check` says so in the place it is easy to miss.
func (g *Gate) Resume(
	ctx context.Context,
	registry *Registry,
	campaignID int64,
) (*CampaignState, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: no registry to open a state in", ErrClosed)
	}

	if err := g.Check(ctx, campaignID); err != nil {
		return nil, err
	}

	return registry.Open(ctx, campaignID)
}

// AuditActor identifies who performed an audited act.
//
// **Renamed from `Actor` in phase 8**, and the rename is the record's point rather
// than a preference: this package was using one word for two different identities.
// `Intent.Actor` is the user a resolution is performed *as* — a live identity, carrying
// a campaign and a role and never a name — and this type is who an `audit_log` row is
// attributed *to*, carrying a display name and nothing else. A dispatch had to name
// its identity, found the word taken, and the two available answers were both bad:
// // borrowing this name for the live identity would have made a `Dispatch` argument and
// an audit row's author the same type, and inventing a third word would have left
// three words for one idea and no name saying which was which.
//
// So the audit attribution took the qualifier it always wanted — it is the *audit*
// actor, it is written by `ruleset.go`, and every call site said so already — and the
// unqualified `Actor` is now the identity a resolution is performed as, which is what
// `Intent.Actor` has always meant. Two names, each saying which question it answers.
//
// Two fields rather than one because `audit_log` records an id (migration 0009) and
// a human reading the row later needs the name, and a row that can only be
// correlated against `users` is a row whose answer depends on another table having
// survived. The name is written into `detail`, not into a column of its own: a
// column per fact is a migration per fact, and the id remains the identity.
//
// A username is not a secret. It is rendered in the shell's own chrome and is not
// `body_plain`, so neither S-5.11 nor S-12.3 is engaged.
type AuditActor struct {
	// UserID is the account's id, and must be positive: it is `audit_log.actor_id`,
	// which is NOT NULL because an unattributed act is not an audit record.
	UserID int64

	// Username is the account's name at the time of the act.
	Username string
}

// DiscardConfirmation is the GM's explicit act, bound to the exact state it read.
//
// Every field is unexported, and that is the gate. A caller cannot build one, so
// `Discard` has exactly one possible source for the value it requires, and that
// source read the row. A struct with exported fields would be a struct a route
// handler could populate from a form post, which is the "a GET does it" failure the
// confirmation gate exists to prevent.
//
// The binding to `revision` is what makes it a confirmation rather than a flag: a GM
// who read the versions, walked away, and came back to a game that has since advanced
// is refused, because what they confirmed is not what would be deleted.
type DiscardConfirmation struct {
	campaignID int64
	revision   uint64
	placements int
	persisted  Fingerprint
	expected   Fingerprint
	// unreadable records that the persisted state document could not be decoded, so
	// the audit row can say the discarded state was unreadable rather than empty.
	// The discard is allowed either way: a document this build cannot read is still
	// a state to lose, and the GM is the one deciding.
	unreadable error
}

// Revision returns the campaign revision the confirmation was prepared against.
func (c DiscardConfirmation) Revision() uint64 { return c.revision }

// Placements returns how many placements were on the table when the confirmation was
// prepared, which is the number the audit row records as what was lost.
func (c DiscardConfirmation) Placements() int { return c.placements }

// Persisted returns the fingerprint the state was written under, and is the zero
// value when the column could not be parsed.
func (c DiscardConfirmation) Persisted() Fingerprint { return c.persisted }

// Expected returns the registered fingerprint the confirmation was prepared against:
// the version the GM was shown, and the version the audit row quotes.
//
// Read from the confirmation rather than re-derived, so the row and the alert cannot
// disagree. This package would otherwise have two implementations of the expected
// version, and the second one would be the one nobody tests.
func (c DiscardConfirmation) Expected() Fingerprint { return c.expected }

// zero reports whether c was never prepared, which is what `Discard` refuses.
func (c DiscardConfirmation) zero() bool { return c.campaignID <= 0 }

// auditAction is the `audit_log.action` this package writes.
//
// One value, and the vocabulary is closed by the schema (migration 0009) so adding
// one is a migration. Dot-separated subject-then-verb because the first question a
// reader asks of an act is *what was acted on*, and `state.discard` answers that
// before `discard.state` does.
const auditAction = "state.discard"

// auditTargetPrefix is prepended to the campaign id to form `audit_log.target`.
//
// The table is named as well as the row because "what was discarded" and "where it
// lived" are different questions, and a target of `7` answers only the second.
const auditTargetPrefix = "campaign_state/"

// insertAuditLog writes one audit row.
//
// Named rather than inlined for the reason `state.go` names `upsertState`: a column
// list and the arguments filling it have to agree, and one name is what makes a
// mismatch impossible to introduce rather than unlikely.
const insertAuditLog = `INSERT INTO audit_log
	(campaign_id, actor_id, action, target, detail, created_at)
	VALUES (?, ?, ?, ?, ?, ?)`

// deleteCampaignState removes the one `campaign_state` row for a campaign.
//
// The predicate is the primary key rather than a `WHERE state = <the bytes we
// read>`, and the reason is the transaction: the revision comparison immediately
// above has already established that this is the row the GM confirmed, and
// comparing blobs against a value read on a different connection is not a check.
// `campaign_state` is one row per campaign (migration 0002), so the key is the whole
// of what identifies what is lost.
const deleteCampaignState = `DELETE FROM campaign_state WHERE campaign_id = ?`

// selectCampaignState is the one statement that reads the state row for a discard.
//
// A prepared constant rather than a query built at a call site, so the column list
// and the `Scan` targets below are stated once — the same argument as
// `upsertState`, and the reason the `Rows` seam below returns values rather than
// handing back a `*sql.Row` the caller would have to shape.
const selectCampaignState = `SELECT state, version FROM campaign_state WHERE campaign_id = ?`

// Rows reads one `campaign_state` row outside a transaction, returning the `state`
// blob and the `version` column, and returns `ErrNoState` when the campaign has no
// row.
//
// A function type for the same mechanical reason as `VersionReader` — `Writer` yields
// only a transaction, and a read-only transaction on SQLite would take a shared lock
// that every debounced write contends for — and so that a discard is testable
// against a table without a live `*store.Store`.
type Rows func(ctx context.Context, campaignID int64) (blob []byte, version int64, err error)

// DiscardConfig is the set of seams a `Discarder` is built over.
//
// A struct for the reason `state.Config` gives: these are one decision — how this
// process reaches the database and says what time it is — and a caller handed
// arguments in the wrong order gets a working object and a wrong one the other way
// round.
type DiscardConfig struct {
	// Write runs the discard and its audit row in one transaction. Required, and
	// refused at use rather than here, so the failure names the call that needed it.
	Write Writer

	// Rows reads the row `Prepare` binds its confirmation to. Optional, and refused
	// at use: `Discard` re-reads inside the transaction it deletes in, so a
	// `Discarder` that will only ever be handed a confirmation another one prepared
	// does not need it.
	Rows Rows

	// Now reads the clock for the audit row's `created_at`. Nil means `time.Now`.
	//
	// Injected because the row's timestamp is part of what the tests assert, and an
	// assertion about "a moment ago" is an assertion about a tolerance. This is
	// bookkeeping and not a rule's outcome, so it is not the "no clock in rule code"
	// discipline ADR 0012 places on resolution.
	Now func() time.Time
}

// withDefaults fills the zero values, and is the only place the default is read.
func (c DiscardConfig) withDefaults() DiscardConfig {
	if c.Now == nil {
		c.Now = time.Now
	}

	return c
}

// Discarder performs the GM-only, confirmation-gated recovery from drift (S-7.8).
//
// Its whole existence is the fourth property of ADR 0018's escape: without it, a
// plugin author bumping `RulesetVersion()` in a patch release strands every live
// campaign with a recovery path of "restore a backup". With it, the loss is a GM's
// decision, taken with both versions in front of them, and recorded.
type Discarder struct {
	write Writer
	rows  Rows
	now   func() time.Time
}

// NewDiscarder returns a discarder over a writer, a row reader and a clock.
func NewDiscarder(cfg DiscardConfig) *Discarder {
	defaults := cfg.withDefaults()

	return &Discarder{write: defaults.Write, rows: defaults.Rows, now: defaults.Now}
}

// Prepare reads a campaign's persisted state and mints the confirmation `Discard`
// requires.
//
// **It writes nothing.** That is the point of it being a separate call rather than a
// flag on `Discard`, and it is what makes "reaching the discard requires the explicit
// act" a property of the API: there is no call a status page or a
// `GET /c/{slug}/status` can make that moves this component closer to a delete.
//
// It also returns the `Status` the caller is rendering, so the GM confirms against
// the very versions the alert named rather than a second read that could differ. The
// two reads are separate statements and therefore *could* differ, which is why the
// revision is what `Discard` re-checks: if the row moved between them, the prepared
// confirmation is stale and refused rather than applied to a state nobody looked at.
func (d *Discarder) Prepare(
	ctx context.Context,
	gate *Gate,
	campaignID int64,
) (DiscardConfirmation, Status, error) {
	var none DiscardConfirmation

	if gate == nil {
		return none, Status{}, fmt.Errorf(
			"%w: a discard must be prepared against a gate, or the expected version is unknown",
			ErrDiscardUnconfirmed,
		)
	}

	status, err := gate.Inspect(ctx, campaignID)
	if err != nil {
		return none, status, err
	}

	confirmation, err := d.prepareRow(ctx, campaignID, status)
	if err != nil {
		return none, status, err
	}

	return confirmation, status, nil
}

// Discard deletes a campaign's persisted game state and records the deletion.
//
// The order of the refusals is load-bearing, and it is the reverse of what it looks
// like it should be:
//
//  1. The writer, then the confirmation, then the campaign the confirmation was
//     prepared for, then the tier, then the identity. The first three are cheap and
//     total, so a caller that has wired nothing is told what it has not wired. The
//     tier and the identity come **before** the row is read, so a player cannot
//     learn a campaign's revision by asking to discard and reading the error.
//  2. The row is read and its revision compared, inside the same transaction that
//     deletes it. Comparing outside would leave a window in which the game advanced
//     and the delete removed a different one.
//
// The transaction is not optional and the audit row is not best-effort: the row and
// the deletion are one commit, so a failure to write the record aborts the deletion.
// That direction is the whole argument — a GM told "the discard failed" still has
// their game, and a GM told "the discard succeeded" has a row saying what it was.
func (d *Discarder) Discard(
	ctx context.Context,
	campaignID int64,
	tier domain.Tier,
	actor AuditActor,
	confirmation DiscardConfirmation,
) error {
	if d.write == nil {
		return fmt.Errorf("%w: no writer is configured", ErrDiscardUnconfirmed)
	}

	if confirmation.zero() {
		return fmt.Errorf(
			"%w: call Prepare and pass what it returned; a discard reached by any other route is not "+
				"a discard this component will perform",
			ErrDiscardUnconfirmed,
		)
	}

	if confirmation.campaignID != campaignID {
		return fmt.Errorf(
			"%w: prepared for campaign %d, asked about %d",
			ErrDiscardForeign, confirmation.campaignID, campaignID,
		)
	}

	// The tier is the route's mounted gate (ADR 0024) made unforgeable from here, and
	// the identity is what the audit row names. Neither is optional and there is no
	// overload without them, so "a player discarded the table" has no path through
	// this function at all; the refusal below is the belt to that braces.
	if tier != domain.TierGM {
		return fmt.Errorf("%w: tier is %s", ErrDiscardNotGM, tier)
	}

	if actor.UserID <= 0 {
		return fmt.Errorf("%w: user id %d", ErrDiscardNoActor, actor.UserID)
	}

	return d.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		row, err := readDiscardRow(ctx, tx, campaignID)
		if err != nil {
			return err
		}

		if row.version != confirmation.revision {
			return fmt.Errorf(
				"%w: it was at revision %d when the discard was prepared and is at %d now",
				ErrDiscardStale, confirmation.revision, row.version,
			)
		}

		detail := discardDetail(campaignID, confirmation, row)

		if _, err := tx.ExecContext(ctx, deleteCampaignState, campaignID); err != nil {
			return fmt.Errorf("realtime: delete campaign %d state: %w", campaignID, err)
		}

		if _, err := tx.ExecContext(ctx, insertAuditLog,
			campaignID,
			actor.UserID,
			auditAction,
			auditTargetPrefix+strconv.FormatInt(campaignID, 10),
			detail,
			d.now().UTC().Unix(),
		); err != nil {
			return fmt.Errorf(
				"realtime: record the discard of campaign %d: %w "+
					"(the state was not deleted; the two are one transaction)",
				campaignID, err,
			)
		}

		return nil
	})
}

// prepareRow reads the row the confirmation binds to, and takes the version from the
// status the gate already resolved.
//
// The version is not read again here, and that is the point: the column lives on
// `campaigns` and the row lives on `campaign_state`, so reading both would be two
// statements that could disagree — and a disagreement would put one version in the
// alert a GM is reading and another in the audit row recording what they deleted.
// `Status` already parsed the column once, so `DiscardConfirmation` carries that
// answer and `Discard` records it.
func (d *Discarder) prepareRow(
	ctx context.Context,
	campaignID int64,
	status Status,
) (DiscardConfirmation, error) {
	if d.rows == nil {
		return DiscardConfirmation{}, fmt.Errorf(
			"%w: no row reader is configured, so no confirmation could be prepared",
			ErrDiscardUnconfirmed,
		)
	}

	if campaignID <= 0 {
		return DiscardConfirmation{}, fmt.Errorf(
			"%w: campaign id %d is not a campaign",
			ErrNoState,
			campaignID,
		)
	}

	blob, version, err := d.rows(ctx, campaignID)
	if errors.Is(err, ErrNoState) {
		return DiscardConfirmation{}, fmt.Errorf(
			"%w: campaign %d",
			ErrNoDiscardableState,
			campaignID,
		)
	}

	if err != nil {
		return DiscardConfirmation{}, fmt.Errorf(
			"realtime: read campaign %d state: %w",
			campaignID,
			err,
		)
	}

	if version < 0 {
		return DiscardConfirmation{}, fmt.Errorf(
			"%w: campaign %d: the version column is %d, which is not a campaign that advanced",
			ErrNoDiscardableState, campaignID, version,
		)
	}

	row := rowFromBlob(uint64(version), blob)

	confirmation := DiscardConfirmation{
		campaignID: campaignID,
		revision:   row.version,
		placements: len(row.document.Placements),
		expected:   status.Expected,
		// Both kinds of unreadable, joined: the version column this build could not
		// compare, and the state document it could not decode. They are different
		// failures and the audit row should be able to distinguish them, because
		// "the version was unreadable" and "the game was unreadable" point an
		// operator at different places.
		unreadable: errors.Join(status.Unreadable, row.parseErr),
	}

	if parsed, err := ParseFingerprint(status.Persisted); err == nil {
		confirmation.persisted = parsed
	}

	return confirmation, nil
}

// discardRow is one `campaign_state` row, read for the staleness check and for the
// audit row's numbers.
type discardRow struct {
	document Document
	// version is the `version` column: the campaign's mutation counter, and what a
	// confirmation binds to.
	version uint64
	// parseErr is why the blob could not be decoded, or nil. Recorded rather than
	// returned, because an unreadable state is still a state to discard.
	parseErr error
}

// discardDetail renders what was discarded, for the audit row.
//
// Four facts, and the shape of the list is the point: the two versions, the revision,
// and the count. **None of the content.** A state document is every placement on the
// tabletop, and S-12.3's exclusion is enforced everywhere else by
// `observability.EventAttributes` having no field a document could be passed
// through; this string is the one place in the project that formats something
// *about* a state, so it takes only values this package minted — integers, and
// plugin identifiers that arrived through registration.
//
// A reader is told how much was lost and can then decide whether that was
// acceptable. They cannot reconstruct it from the row, and that is the intent: the
// audit record's job is to make the loss accountable, not to make it recoverable.
func discardDetail(campaignID int64, confirmation DiscardConfirmation, row discardRow) string {
	return fmt.Sprintf(
		"discarded the persisted game state of campaign %d: it was written under %q, the registered "+
			"ruleset is %q, and it held revision %d with %d placements; the state is gone and nothing "+
			"re-derives it",
		campaignID,
		persistedText(confirmation),
		confirmation.Expected(),
		row.version,
		len(row.document.Placements),
	)
}

// persistedText renders the version the state was written under, for the audit row.
//
// From the confirmation rather than from the row, because the fingerprint lives in
// `campaigns.ruleset_version` and the state blob does not carry it: this file would
// otherwise have a second read of a column it already has an answer for, and a
// disagreement between the two would be a row saying the game was written under a
// version the alert did not name.
func persistedText(confirmation DiscardConfirmation) string {
	if !confirmation.persisted.Empty() {
		return confirmation.persisted.String()
	}

	if confirmation.unreadable != nil {
		return "a version this build could not read (" + errorText(confirmation.unreadable) + ")"
	}

	return "no particular ruleset"
}

// errorText renders an optional error for one line of a column.
//
// The newline flattening is not cosmetic: `errors.Join` renders its members
// newline-separated, and a `detail` with an embedded newline is a row a `SELECT` in a
// terminal renders as two. The reasons joined here are two sentences that belong on
// the same line.
func errorText(err error) string {
	if err == nil {
		return "no reason recorded"
	}

	return strings.ReplaceAll(err.Error(), "\n", "; ")
}

// readDiscardRow reads and decodes the row inside the discarding transaction.
//
// The query is here and not behind the `Rows` seam because a read inside a
// transaction must be inside it: `Discard`'s whole argument is that the row it
// compared against and the row it deleted are the same row, and a comparison against
// a read taken on a different connection is not that.
func readDiscardRow(ctx context.Context, tx *sql.Tx, campaignID int64) (discardRow, error) {
	var (
		blob    []byte
		version int64
	)

	err := tx.QueryRowContext(ctx, selectCampaignState, campaignID).Scan(&blob, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return discardRow{}, fmt.Errorf("%w: campaign %d", ErrNoDiscardableState, campaignID)
	}

	if err != nil {
		return discardRow{}, fmt.Errorf("realtime: read campaign %d state: %w", campaignID, err)
	}

	// The version column is a signed 64-bit integer (migration 0002), so a negative
	// value is not a campaign that advanced. Checked before the conversion rather
	// than cast, because "not reachable" is exactly the reasoning that produces a
	// silent wrap from a value this process did not write — the fixed point
	// `state.go` refuses everywhere else.
	if version < 0 {
		return discardRow{}, fmt.Errorf(
			"%w: campaign %d: the version column is %d, which is not a campaign that advanced",
			ErrNoDiscardableState, campaignID, version,
		)
	}

	// Only the two facts the blob can answer: how far the campaign had advanced, and
	// how much was on the table. The ruleset version lives in `campaigns` and comes
	// from the prepared confirmation.
	return rowFromBlob(uint64(version), blob), nil
}

// rowFromBlob decodes a persisted row into the facts the discard needs.
//
// A decode failure is **not** fatal. A document this build cannot read is a state a
// GM may still want to discard — often that is exactly *why* it cannot be read — and
// refusing would leave the recovery path unavailable for the case it exists for. So
// the placement count falls back to zero and the decode error is carried into the
// audit row's detail, where an operator sees that the state was unreadable as well
// as discarded.
func rowFromBlob(version uint64, blob []byte) discardRow {
	row := discardRow{version: version}

	document, err := DecodeDocument(blob)
	if err != nil {
		row.parseErr = fmt.Errorf("the state document could not be decoded: %w", err)

		return row
	}

	row.document = document

	return row
}
