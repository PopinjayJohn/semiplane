// Package realtime is the in-memory authority for a live tabletop, and the
// cadence its persistence runs on.
//
// # One state per campaign, and why that is not tidiness
//
// `version` is the **only** ordering authority (S-7.2), and a version means
// nothing without exactly one holder of the thing it versions. Two
// `CampaignState` values for one campaign would each hand out versions from
// their own counter, both would be internally consistent, and every client
// connected to one of them would see a coherent and *wrong* game — the exact
// failure shape ADR 0004 names, arrived at from the other direction. So a
// second `Registry.Open` for a live campaign is refused with `ErrStateOpen`
// rather than replacing the first, and the accessor for "somebody already has
// it" is `Registry.Get`.
//
// Replacing was the other available answer and it is worse in a way that is not
// obvious: the first holder is the hub, which is holding `*CampaignState`
// pointers, so a replacement does not remove the first authority — it orphans
// it. A reference the hub still holds is a second order of authority with
// nobody able to say which one is current.
//
// # Per-placement versions, and the campaign counter that is not one
//
// §7.2's "Per-placement version, not global" is load-bearing and the reason is
// contention, not tidiness: a single global counter makes every placement's
// write serialise on one value, so a table dragging one token across a map
// contends with a table rolling initiative. So `Placement.Version` is per
// placement, stamped from a per-id map, and mutating `p1` does not move `p2`'s
// number.
//
// `CampaignState.Revision` is a **different thing** and is deliberately not an
// ordering authority. It counts every mutation the campaign has ever applied,
// it is what `campaign_state.version` holds, and it answers "how far is the
// persisted row behind memory" and "may this client's `since` be believed". A
// client reconciles a placement against that placement's `version`; nothing
// reconciles against `Revision`.
//
// # `seq` is the client's, and this file is why it cannot forge anything
//
// `seq` is the client's optimistic-reconcile counter (S-7.1) and it is spoken by
// the protocol codec, not here. **There is no `seq` parameter anywhere in this
// file**, and that absence is the guarantee: a client cannot reach the version
// authority through one, because the only claim this type accepts is `seen`, a
// version it must have been *given*. A caller that invents a high number is
// refused by `ErrVersionMismatch`, and a caller that claims to be ahead of the
// campaign's mutation counter is refused by `ErrFutureVersion` — which is also
// the detector for a client that reached the wrong process.
//
// # The cadence, and the reason it is two seconds
//
// `campaign_state` is written on a **trailing** debounce of about two seconds
// after the last mutation, and on shutdown (S-7.5, architecture §7.3). A table
// produces a handful of writes per minute instead of hundreds per second, and
// **that is the main reason SQLite's single-writer limit is adequate here**: the
// limit would bind long before the disk did, and the whole point of the debounce
// is that it never gets near.
//
// # Durability is the debounce's problem, not an fsync per mutation's
//
// There is no `fsync` on the mutation path, and adding one would be wrong rather
// than cautious. `store/pragmas.go` already fixes `synchronous=NORMAL` and says
// why in the place that decides it: FULL "fsyncs the WAL on every commit", and
// the cost here is "a tabletop where a token's hit points cost a disk round
// trip". `campaign_state` is a debounced cache of the last state (ADR 0004), the
// filesystem holds the authoritative content, and a `page_revisions` row lost to
// power loss is a lost history entry rather than a lost page. The durability
// window this component accepts is therefore **bounded by the debounce, and
// nothing narrower** — which is the same trade the pragma already made, reached
// from the other end.
//
// # The crash floor
//
// The crash floor is **the last debounced state**: roughly two seconds of
// gameplay. A process killed between a mutation and its window loses the
// mutations since the last write, and that is the accepted cost, not an oversight
// (ADR 0004). Two consequences are load-bearing and both are asserted by tests:
//
//   - **Cancelling the lifetime context is a crash, not a shutdown.** The
//     scheduler returns on `ctx.Done()` and writes nothing, so the table is the
//     only record of what the last debounce captured. `Close` is the shutdown and
//     it flushes. A composition root that cancels without calling
//     `Registry.Close` has simulated a power cut, and the code cannot tell it
//     from the real thing.
//   - **A failed write is not a write.** A flush that errors leaves the state
//     dirty and re-arms the window, so the next attempt carries it. Leaving the
//     state clean after a failed write would make the crash floor quietly wrong,
//     which is the failure `store/writer.go` refuses to accept for the same
//     reason it leaves its queue unbounded: dropping a `campaign_state` flush
//     silently is worse than an OOM kill, which at least leaves the vault — the
//     source of truth — untouched.
//
// # Zero is not a fixed point
//
// ADR 0037 is about a `stat` that cannot tell an empty file from an unwritten
// one. The same reasoning has three faces here, and all three are closed by
// construction rather than by convention:
//
//  1. **"Nothing changed" is a flag, never a byte comparison.** A campaign that
//     creates a placement and immediately removes it serialises to the same
//     placements as a campaign that never had one, so comparing the encoded form
//     against the last-written form would skip the write and lose the fact that
//     the game advanced. `dirty` is set by the mutation path and cleared by the
//     flush, and nothing derives it from bytes.
//  2. **A zero-length or unrecognised blob is refused, loudly.** A loaded state
//     and a never-written one must not look alike, so the encoding carries a
//     magic prefix and `Decode` refuses a blob that does not begin with it. A
//     resume on a truncated or foreign `state` column is an error, never an
//     empty tabletop.
//  3. **A version of zero is not a legal version.** Every placement this process
//     creates is stamped before it is observable, so 0 is unreachable by
//     construction and its appearance in a decoded document means the bytes are
//     not what was written. A loaded placement at version 0 would also be a hole
//     in the authority: a client resuming at 0 would be told its copy is current.
//
// # The clock is injected, and why
//
// The cadence is the one thing in this file that cannot be deterministic — a
// debounce that does not consult a clock is a `sleep`. It is therefore an
// injectable `Cadence`, a struct with documented zero-value defaults for the same
// reason `content.SettleTimings` is one: the timings are one decision, and a
// caller handed three constructor arguments has to get them in the right order
// to get a working filter. The zero value is the production default rather than
// "no waiting", for the reason `content.SettleTimings.withDefaults` gives: a
// debouncer built without stated timings is the safe one.
//
// Injecting it is not only about test speed. "A mutation at T−1ms does not
// produce a write at T" is a claim about the *absence* of an event, and asserting
// absence in real time means sleeping past the window — which makes the test slow
// and the assertion weaker at the same time. With an injected clock the window is
// exact and the assertion is exact.
//
// # Why the statement lives here and not in `internal/store`
//
// Two reasons, and the first is path ownership: `internal/store/*.go` belongs to
// another work item. The second is that phase 1 asserted, on purpose, that
// nothing in `store` writes `campaign_state` yet, so that adding a speculative
// accessor would be the second answer to the same question. The write therefore
// goes through a `Writer` function type, which is a closure over
// `store.Store.Write` in the composition root — the identical shape, and for the
// identical mechanical reason, as `edit.Writer`: `Store.Write` takes an
// unexported `writeFunc`, so no interface outside `store` can name it.
//
// Reads go through a `Reader` function type instead of a `*sql.DB`, because
// `Store.DB()` is documented for reads and a resume should not need a live
// `*store.Store` to be testable against a table.
//
// # No new event name
//
// §13.2's `state.write_ms` is already in `observability.AllEventNames()`, so the
// outcome of each write is reported through the `WriteRecorder` seam rather than
// through a new name — the histogram itself is a later work item's, and adding a
// name here would move a count that another file's test owns for no gain. The
// read that this file does *not* take is the `*slog.Logger`: S-12.3 is enforced
// by `observability.EventAttributes` having no field a state document could be
// passed through, and routing around it with a `slog.Any` at this call site is
// the exact hole the type exists to prevent.

package realtime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultDebounce is the persistence timing, and it is the number in architecture
// §7.3.
//
// Two seconds is stated in the record and repeated in S-7.5, and it is chosen to
// sit above the gap between two human actions (a GM dragging a token, a player
// typing a chat message) and below the window in which a crash is noticeable.
// The number is not load-bearing for correctness — any value produces a working
// filter, and the cadence is configurable per registry — so the reason it is a
// constant rather than a required argument is that a caller who has to think
// about it will think about it wrongly.
const DefaultDebounce = 2 * time.Second

// documentMagic is the prefix every persisted state document begins with.
//
// Exists to make "a state that was never written" and "a state that was written
// and is empty" different observations. Without it a zero-length `state` column,
// a truncated one, and a JSON document from a future version all decode to an
// empty tabletop, and a resume would silently replace a real game with a blank
// one — the "a watcher that indexes the third of ten events is a wrong answer
// that looks right" shape ADR 0037 is about. See the file comment.
const documentMagic = "spstate1:"

// The sentinels a caller branches on. Every one is `errors.Is`-testable, and none
// of them carries anything a log line would repeat.
var (
	// ErrStateOpen means a live state already exists for the campaign.
	//
	// Its own name rather than a reuse of something more general, because the
	// only correct response to it is `Registry.Get`: a caller that wanted to
	// create a second authority and was refused must go and find the first.
	ErrStateOpen = errors.New("realtime: a live state is already open for this campaign")

	// ErrNoPlacement means no placement of that id exists.
	ErrNoPlacement = errors.New("realtime: no such placement")

	// ErrVersionMismatch means the caller's claim about a placement is not the
	// current one.
	//
	// Three situations reach it and they are deliberately not distinguished: the
	// caller is behind, the caller is ahead, and the caller says it has never
	// seen a placement that exists. All three are the same fact — this copy is not
	// what the server has — and all three are answered by resynchronising, so
	// telling the caller *which* it was would be a hint about state a stale client
	// has no business holding.
	ErrVersionMismatch = errors.New("realtime: placement version does not match")

	// ErrFutureVersion means a client is ahead of this campaign's mutation
	// counter, holding the same incarnation.
	//
	// Under the same incarnation the counter only ever grows, so a client beyond
	// it is talking to something other than the process that last served it. Two
	// instances behind a load balancer (ADR 0004) produce exactly this, and the
	// refusal is the whole point: the alternative is a snapshot, which papers over
	// a divergence instead of naming it.
	ErrFutureVersion = errors.New("realtime: client is ahead of the campaign's revision")

	// ErrStateUnreadable means a persisted document could not be decoded into a
	// state, or decoded into one that is not self-consistent.
	//
	// Never returned as an *empty* state. A document this component cannot read is
	// a campaign whose game is not recoverable, and answering with a blank
	// tabletop would be the same wrong answer wearing a successful result.
	ErrStateUnreadable = errors.New("realtime: persisted state is not readable")

	// ErrClosed means the state has been shut down and no longer accepts
	// mutations.
	ErrClosed = errors.New("realtime: campaign state is closed")
)

// PlacementID names a placement within one campaign.
//
// Runtime only, and it is **not** a page path: §4.2's rule is that placements
// are never written to files, so this string never becomes a filename and never
// reaches `os.Root`. A placement is an instance of a game object, and the game
// object is a page.
// valid reports whether id is usable as a placement id.
//
// The rules are few, and the one that is not obvious is the control character.
// A placement id is a protocol-visible identifier: it is the `placement` field of
// every `delta` and `applied` message, and it is a value a log line will quote.
// An id carrying a newline produces a line that lies about where it breaks, and
// one carrying a control character cannot be read back out of a terminal at all.
func (p PlacementID) valid() bool {
	if p == "" {
		return false
	}

	if len(p) > maxPlacementIDLen {
		return false
	}

	for _, char := range string(p) {
		if unicode.IsControl(char) {
			return false
		}
	}

	return true
}

// maxPlacementIDLen bounds a placement id at 128 bytes.
//
// A bound rather than a rule because the ids come from a gameplay system, not
// from a person, and the failure this prevents is a pathological one: an id large
// enough to be a document rather than an identifier would be serialised into
// every delta for every client. 128 bytes is three orders of magnitude above any
// id a system will mint and one below the point where the bound is the interesting
// part of a diff.
const maxPlacementIDLen = 128

// Placement is one game object instance on the live tabletop: a position, the
// hit points it has *now*, the conditions on it, and the version the server last
// stamped on it.
//
// Every field is a runtime fact. §4.2 puts the durable half — max HP, AC,
// portrait, size — in page front matter, written by a GM, and forbids the other
// half from ever reaching a file: current HP in front matter would make every
// damage event rewrite the page and collide head-on with Obsidian sync.
//
// The JSON tags are the persisted form, and they are the *only* encoding of a
// placement in this project. The protocol's `snapshot` embeds this type rather
// than restating it, because a second struct over the same data is a second
// answer to "what is a placement" and the two would drift the first time a field
// was added.
type Placement struct {
	// ID names the placement within its campaign, and is written once at
	// creation. A mutation cannot change it: `Mutate` stamps it back after the
	// caller's function returns.
	ID PlacementID `json:"id"`
	// X and Y are the map position, in the canvas's own units. Integers rather
	// than floats because the client renders from them and a float in a persisted
	// document is a rounding question nobody in this project has an answer for.
	X int `json:"x"`
	Y int `json:"y"`
	// HP is the current hit points, which is the field that moves most often and
	// the reason a per-mutation `fsync` would be felt.
	HP int `json:"hp"`
	// MaxHP is copied from the game object's front matter at placement time and is
	// not authoritative afterwards: the GM changes it on the page, and a placement
	// that disagreed with its own definition would render a bar that is wrong.
	MaxHP int `json:"maxHp"`
	// Conditions is the set of status conditions, held **sorted and free of
	// duplicates** so the in-memory value is already in its serialised order.
	//
	// Sorted in memory rather than at encode time on purpose: an encoder that
	// sorts is an encoder whose output depends on a sort, and a document that
	// differs between two runs of the same state is a document a test cannot
	// compare. The set is a handful of entries, so insertion cost is not a
	// concern and determinism is.
	Conditions []string `json:"conditions"`
	// Visible is whether the placement is shown to players. Cosmetic on the wire
	// and authoritative in state: §7.2 is explicit that client-side hiding is
	// cosmetic, so the server has to be the one that knows.
	Visible bool `json:"visible"`
	// Version is the server's version for this placement and the **only** ordering
	// authority for it (S-7.2). Stamped by the mutation path, overwriting
	// whatever the caller's function left, so authority cannot be delegated to a
	// gameplay system.
	Version uint64 `json:"version"`
}

// AddCondition puts name into p's condition set, keeping it sorted and free of
// duplicates.
//
// Exported rather than left to a caller appending to the slice, because the
// ordering is the invariant: a set built by appending is in whatever order the
// intents arrived in, and the encoder writes that order straight out. A second
// condition added between two calls to `AddCondition` would produce a document
// whose bytes depend on the order two clients happened to act in, which is the
// determinism `Document` promises and cannot deliver.
//
// The empty name is ignored rather than refused: it is not a condition, and a
// gameplay system resolving an intent that named one has nothing to apply.
func (p *Placement) AddCondition(name string) {
	if name == "" {
		return
	}

	at, found := slices.BinarySearch(p.Conditions, name)
	if found {
		return
	}

	p.Conditions = slices.Insert(p.Conditions, at, name)
}

// RemoveCondition drops name from p's condition set, if it is there.
//
// The absent case is not an error: a system resolving "the prone condition ends"
// against a token that is not prone has reached the answer it wanted, and a
// refusal would make every caller handle a condition that has already been
// removed.
func (p *Placement) RemoveCondition(name string) {
	at, found := slices.BinarySearch(p.Conditions, name)
	if !found {
		return
	}

	p.Conditions = slices.Delete(p.Conditions, at, at+1)
}

// clone returns a copy sharing no mutable state with p.
//
// The only mutable field is `Conditions`, and the reason this exists is that a
// `Placement` handed to a caller is a **value**, and a value copy of a slice shares
// its backing array. Without the clone, a hub that sorted a returned placement's
// conditions in place would be writing to the authority from outside its lock, and
// the race detector would not see it because nothing else is touching that array at
// the time.
//
// A pointer receiver like its two siblings, because a type with both kinds of receiver
// is the mistake `recvcheck` exists to name, and because the map holds pointers: the
// clone source is always reachable as a `*Placement`, so there is never a reason to
// need a value receiver.
func (p *Placement) clone() Placement {
	copied := *p
	copied.Conditions = slices.Clone(p.Conditions)

	return copied
}

// Mutation is the outcome of one applied mutation: enough for a caller to
// broadcast it and for a client to reconcile its optimistic copy.
//
// It carries no operation name and no arguments, and that is a boundary rather
// than an omission. The caller that requested a mutation knows what it asked for
// — it called `SetHP`, or it dispatched `set_hp` and a system resolved it — so an
// operation field here would be a second record of the same fact, free to drift
// from the code that performed the change. The protocol vocabulary is R2's
// (§7.1) and it is assembled where the broadcast is assembled.
type Mutation struct {
	// Placement is the placement the mutation applied to, and is meaningful for a
	// removal too: the id is what a client needs in order to drop its copy, and
	// it is no longer in the state.
	Placement PlacementID
	// Version is the version stamped by this mutation. For a removal it is the
	// version the placement *would* have carried, which is why the version map
	// outlives the placement: see `CampaignState.versions`.
	Version uint64
	// Revision is the campaign's mutation counter after this change. Not an
	// ordering authority; see the file comment.
	Revision uint64
}

// Resume is the answer to a reconnecting client's `hello`.
//
// The protocol's own shape is R2's (§7.1: "Reconnect sends `since` for a delta
// instead of a full snapshot"). What belongs here is the *decision*, because it
// is a property of the state and not of the transport: whether the client's claim
// about how far it has got is possible at all, and whether a delta could be built
// from what this type holds.
type Resume struct {
	// Incarnation is the identity of the state that answered, which the client
	// echoes on its next resume.
	Incarnation Incarnation
	// Revision is the campaign's mutation counter now.
	Revision uint64
	// Placements is the whole live state, sorted by id. Empty when `Changed` is
	// false.
	Placements []Placement
	// Changed is whether the client has anything to apply.
	//
	// This component keeps no change log, so it cannot build the delta a protocol
	// that understood `since` would send, and it says so rather than pretending:
	// a resume against the same incarnation has exactly two honest answers, "nothing
	// happened" and "here is everything". Constructing the delta — a bounded
	// per-campaign ring of recent changes, which is where `state.write_ms`'s
	// siblings would be spent — belongs with the hub that broadcasts, and until it
	// exists an incremental answer would be a fabricated one.
	Changed bool
}

// Document is the state in the form that is persisted and sent: placements
// flattened into a sorted list, with the campaign's counters beside them.
//
// One type for both, deliberately. A separate wire struct would be a second
// answer to "what is a placement", and the two would drift the first time a field
// was added — which is the failure `edit.Revisions` exists to avoid for a
// different reason and the same codebase argues against everywhere.
//
// The list is a slice and not a map so that `encoding/json` emits a deterministic
// order. Map keys are iterated in sorted order by `encoding/json` today, and a
// persisted blob whose bytes depend on that is a blob whose bytes are a property
// of the standard library rather than of this project.
type Document struct {
	// Revision is the campaign's mutation counter, and is the same number the
	// `campaign_state.version` column holds. A load refuses a document whose
	// revision disagrees with that column, because two counters answering one
	// question is a state whose authority is a coin toss.
	Revision uint64 `json:"revision"`
	// Paused is the tabletop's paused flag. Campaign-level, so it carries no
	// placement version — §7.2's ordering authority is per placement, and a pause
	// changes none of them.
	Paused bool `json:"paused"`
	// Placements is every live placement, sorted by id, with `Placement.ID` set to
	// the same value as its position in the list so a reader of the bytes need not
	// infer it.
	Placements []Placement `json:"placements"`
}

// EncodeDocument renders a document as the bytes stored in `campaign_state.state`.
//
// The magic prefix is the first thing written and `Decode` refuses anything
// without it, for the reason in the file comment: a zero-length blob, a truncated
// one and a document from a future version must not all decode to an empty
// tabletop.
//
// A sorted `Document` is required, not produced. `CampaignState.Snapshot` is the
// only thing that builds one and it sorts; re-sorting here would be a second place
// where an order could be wrong, and this function's job is bytes.
func EncodeDocument(document Document) ([]byte, error) {
	body, err := json.Marshal(document)
	if err != nil {
		// Unreachable for this type — every field is an integer, a bool, a string
		// or a slice of those — and the error is still handled rather than dropped,
		// because a persister that discarded it would report a successful write of
		// an empty state.
		return nil, fmt.Errorf("realtime: encode campaign state: %w", err)
	}

	return append([]byte(documentMagic), body...), nil
}

// DecodeDocument reads bytes written by EncodeDocument into a document.
//
// Every failure is `ErrStateUnreadable` and every one of them is loud. There is
// no path through this function that returns a zero `Document` and a nil error,
// because a document that decoded to nothing and a document that was never
// written are the same observation, and answering the second with the first
// replaces a real game with a blank one (ADR 0037's fixed point, in the place
// where it would cost a table its combat).
//
// The self-consistency checks are the rest of ADR 0037. A placement at version 0
// is refused because every placement this process creates is stamped before it is
// observable, so 0 is unreachable by construction. A placement whose version
// exceeds the document's revision is refused because each version bump is also a
// revision bump, so a version above the revision means the two counters came from
// different states. And a duplicate id is refused because a document listing one
// placement twice has two answers to "where is p1", which is the ambiguity
// per-placement versioning exists to remove.
func DecodeDocument(blob []byte) (Document, error) {
	body, prefixed := strings.CutPrefix(string(blob), documentMagic)
	if !prefixed {
		return Document{}, fmt.Errorf(
			"%w: %d bytes, want a document beginning %q; a truncated or empty state column "+
				"is not an empty tabletop",
			ErrStateUnreadable, len(blob), documentMagic,
		)
	}

	// An empty body is refused separately from an unprefixed one, because the two
	// arrive differently and mean different things: `spstate1:` with nothing after
	// it is a truncated write, and a document that decodes to nothing is a state
	// this component could not have produced. Both are `ErrStateUnreadable` and
	// both are loud, and saying which is here rather than in a message a caller has
	// to match on.
	if strings.TrimSpace(body) == "" {
		return Document{}, fmt.Errorf(
			"%w: the document carries the marker and no body, which is a truncated write",
			ErrStateUnreadable,
		)
	}

	var document Document

	if err := json.Unmarshal([]byte(body), &document); err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrStateUnreadable, err)
	}

	if err := document.check(); err != nil {
		return Document{}, err
	}

	return document, nil
}

// check refuses a document that could not have been written by this component.
func (d Document) check() error {
	seen := make(map[PlacementID]struct{}, len(d.Placements))

	for _, placement := range d.Placements {
		switch {
		case !placement.ID.valid():
			return fmt.Errorf(
				"%w: placement %q is not a usable id",
				ErrStateUnreadable,
				placement.ID,
			)
		case placement.Version == 0:
			return fmt.Errorf(
				"%w: placement %q is at version 0; every placement is stamped at creation, "+
					"so 0 is unreachable and its presence means the bytes are not what was written",
				ErrStateUnreadable, placement.ID,
			)
		case placement.Version > d.Revision:
			return fmt.Errorf(
				"%w: placement %q is at version %d, above the campaign revision %d; each "+
					"version bump is also a revision bump, so these came from different states",
				ErrStateUnreadable, placement.ID, placement.Version, d.Revision,
			)
		}

		if _, repeated := seen[placement.ID]; repeated {
			return fmt.Errorf("%w: placement %q is listed twice", ErrStateUnreadable, placement.ID)
		}

		seen[placement.ID] = struct{}{}
	}

	return nil
}

// IncarnationLen is the byte length of an Incarnation, exported so a test can size
// a hex string without restating the number. Sixteen bytes.
const IncarnationLen = incarnationLen

// Incarnation is the identity of one *loading* of a campaign's state, as opposed
// to its identity as a campaign.
//
// It exists because the crash floor puts a restart in the middle of a client's
// resume, and the two cases are otherwise indistinguishable. A client that had
// seen revision 12 reconnects after a process that debounced at 10: its claim is
// impossible against a state that never stopped, and completely ordinary against
// one that was reloaded. Without an incarnation both are the same refusal, and the
// honest answer — "here is the whole state" — is turned into an error that a GM
// sees as a broken game.
//
// Sixteen random bytes rather than a process-local counter, because a counter
// resets across the restart this type exists to detect, and a collision would
// turn the crash case back into the impossible one. This is not a rule and not
// part of a deterministic result: nothing about a campaign's state depends on it,
// and it is read only to decide whether a delta could be believed. The reader is
// injectable anyway, which is what makes the format's round trip testable
// without randomness.
type Incarnation [incarnationLen]byte

// incarnationLen is the byte length of an incarnation: 128 bits, which is the
// same width as an MD5 collision bound and is chosen because it is a number
// people already recognise rather than because the arithmetic matters.
const incarnationLen = 16

// newIncarnation draws a fresh incarnation from entropy.
//
// The parameter is not called `rand` because `crypto/rand` is imported into this
// file and a shadowed package name is a name a later reader resolves to the wrong
// thing.
func newIncarnation(entropy io.Reader) (Incarnation, error) {
	var incarnation Incarnation

	if _, err := io.ReadFull(entropy, incarnation[:]); err != nil {
		return Incarnation{}, fmt.Errorf("realtime: draw an incarnation: %w", err)
	}

	return incarnation, nil
}

// String renders the incarnation as lower-case hex, which is what travels on the
// wire and what appears in a log line.
func (i Incarnation) String() string {
	return hex.EncodeToString(i[:])
}

// IsZero reports whether the incarnation is the zero value, which is what a
// client that has never connected sends.
func (i Incarnation) IsZero() bool {
	return i == Incarnation{}
}

// ParseIncarnation reads the hex form written by `String`.
//
// It exists because the client sends the incarnation as a string and there has to
// be exactly one function that turns it back, or a client and the server disagree
// about the spelling of the same 16 bytes. An unparseable value is a zero
// incarnation rather than an error, because the zero incarnation is the
// "I have never connected" claim and it produces the right answer — a full
// snapshot — for any string the function does not recognise.
func ParseIncarnation(encoded string) (Incarnation, error) {
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return Incarnation{}, fmt.Errorf("realtime: parse an incarnation: %w", err)
	}

	if len(raw) != incarnationLen {
		return Incarnation{}, fmt.Errorf(
			"realtime: parse an incarnation: %d bytes, want %d", len(raw), incarnationLen,
		)
	}

	return Incarnation(raw), nil
}

// Cadence is the persistence timing, and the clock it runs on.
//
// A struct rather than constructor arguments because the timings and the clock
// are one decision, and a caller handed three durations in the right order gets a
// working filter and a wrong one the other way round. The zero value is the
// production default rather than "no waiting", for the reason
// `content.SettleTimings.withDefaults` gives: a debouncer built without stated
// timings is the safe one.
type Cadence struct {
	// Debounce is how long the state must go without a mutation before it is
	// written. Zero or negative means `DefaultDebounce`.
	Debounce time.Duration

	// Now reads the clock. Nil means `time.Now`.
	//
	// Injected so the window is exact in tests: "a mutation at T−1ms does not
	// produce a write at T" is a claim about the absence of an event, and asserting
	// an absence in real time means sleeping past the window, which makes the test
	// slow and the assertion weaker at the same time.
	Now func() time.Time

	// After returns a channel that receives once `d` has elapsed on the same
	// clock. Nil means `time.After`.
	//
	// A function rather than a `*time.Timer` for the same reason: a fake timer
	// cannot be driven by a fake clock without reimplementing the runtime's timer
	// wheel, and reimplementing the wheel is how a test ends up agreeing with a
	// defect in the reimplementation. The abandoned timers this leaves in
	// production are bounded by the debounce window and cost one runtime timer
	// each, which is the price for a cadence that is exactly assertable.
	After func(d time.Duration) <-chan time.Time
}

// withDefaults fills the zero values, and is the only place the defaults above
// are read.
//
// Non-positive rather than only zero, so that a computed negative duration — one
// configuration value minus another — produces a working filter rather than a
// timer that fires in the past forever.
func (c Cadence) withDefaults() Cadence {
	if c.Debounce <= 0 {
		c.Debounce = DefaultDebounce
	}

	if c.Now == nil {
		c.Now = time.Now
	}

	if c.After == nil {
		c.After = time.After
	}

	return c
}

// Writer runs fn inside a transaction and returns after it commits.
//
// A function type rather than an interface method, and the reason is mechanical:
// `store.Store.Write` takes an unexported `writeFunc`, and Go requires an
// interface method's parameters to be *identical* rather than merely assignable.
// There is therefore no interface `*store.Store` can satisfy that expresses this,
// and a closure in the composition root converts one to the other without naming
// either. The same shape as `edit.Writer`, for the same reason, and the same one
// line to write.
type Writer func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error

// Reader returns the persisted row for a campaign, or `ErrNoState` if it has
// none.
//
// A function type for the same mechanical reason as `Writer`, and because a
// resume should be testable against a table without a live `*store.Store`. Reads
// are safe to issue concurrently and belong on `Store.DB()` rather than the
// writer queue — a read that queued behind a `campaign_state` flush would make
// every reconnect wait for a write it did not ask for.
type Reader func(ctx context.Context, campaignID int64) (Persisted, error)

// ErrNoState means a campaign has no `campaign_state` row yet, which is a real
// state rather than an error: an empty tabletop nobody has joined.
var ErrNoState = errors.New("realtime: campaign has no persisted state")

// Persisted is one `campaign_state` row as read.
//
// The version column is carried beside the blob so the load can require the two
// to agree. They are one number written twice, and a load that trusted the blob
// alone would let a row whose column says 10 and whose document says 3 pick one
// arbitrarily — a state whose authority is a coin toss.
type Persisted struct {
	// Blob is the `state` column's bytes, exactly as stored.
	Blob []byte
	// Version is the `version` column, which is the campaign's mutation counter.
	Version int64
	// UpdatedAt is the `updated_at` column, which is an operator's only clue about
	// how far behind a row is. Nothing in this component branches on it.
	UpdatedAt time.Time
}

// WriteRecorder receives the outcome of one persisted write.
//
// The seam onto §13.2's `state.write_ms`. A function type rather than an
// interface, and nil-tolerant, in the spirit of `observability.NewWatch`: the
// write must happen whether or not anyone is measuring it.
//
// Note that `elapsed` is measured on the *injected* clock. With the production
// clock that is a real duration; under a test clock it is zero by construction,
// which is honest — the value is a measurement and there was nothing to measure.
type WriteRecorder func(ctx context.Context, campaignID int64, elapsed time.Duration, err error)

// upsertState is the one statement that writes `campaign_state`.
//
// `ON CONFLICT DO UPDATE` rather than a delete-then-insert, because the primary
// key is the whole of S-7.5's "exactly one row per campaign" (migration 0002, and
// the phase-1 test that asserts it) and a delete-then-insert would hold the write
// lock across two statements and leave a window in which the row does not exist
// for any reason at all. An upsert is one statement, so the row is never absent
// once it is present — which is what lets `Open` write an initial row and know
// that every later write is an update.
//
// Named rather than inlined at the call site for the reason `store/pages.go` names
// its statements: a column list and the arguments filling it have to agree, and
// one name is what makes a mismatch impossible to introduce rather than unlikely.
const upsertState = `INSERT INTO campaign_state (campaign_id, state, version, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(campaign_id) DO UPDATE SET
		state = excluded.state,
		version = excluded.version,
		updated_at = excluded.updated_at`

// Registry holds the process's live campaign states: exactly one per campaign.
//
// A registry rather than a package-level map because the whole of ADR 0004 is
// that state ownership is the constraint, and a map in a package variable cannot
// be closed, cannot be tested in parallel, and is a second Store in disguise
// (`store.Open` refuses a second handle for exactly this reason).
type Registry struct {
	write   Writer
	read    Reader
	cadence Cadence
	record  WriteRecorder
	// entropy draws each state's incarnation. Overridable so the format's round
	// trip is testable without randomness, and defaulted to `crypto/rand.Reader` —
	// a value read rather than a call, so this is not the "direct `crypto/rand`"
	// AGENTS.md forbids in *rule* code. Nothing about a campaign's state depends
	// on it; it only answers "is this client talking to the same loading".
	entropy io.Reader

	// ctx is the registry's lifetime, and every state scheduler runs on it.
	//
	//nolint:containedctx // The schedulers outlive the call that started them, so
	// the context that stops them has to outlive it too. Held as a cancelable
	// child rather than through a cancel func because each state derives its own
	// cancelable child from this one, and a per-state cancel func alone cannot
	// create that child without the parent.
	ctx context.Context

	// cancel stops every state scheduler, whether or not Close is called. A
	// context-cancelled registry is a crash, and the two are deliberately the same
	// observation — see the file comment.
	cancel context.CancelFunc

	// mu guards live. One lock rather than one per campaign, because the map is
	// small: a live state exists per campaign with a live tabletop, not per
	// registered campaign.
	mu sync.Mutex
	// live holds the reservation for every campaign whose state is open *or being
	// opened*. The second state of the map value is what makes a second `Open`
	// refuse rather than race: see `Open`.
	live map[int64]*CampaignState

	// closed is set by Close, after which Open refuses. A registry that accepted a
	// new state during shutdown would hand out an authority nobody will ever flush.
	closed bool
}

// NewRegistry returns a registry that persists through write and reads through
// read.
//
// ctx is the registry's lifetime: cancelling it stops every state scheduler
// **without flushing**, which is the crash case, and `Close` is the shutdown. A
// composition root that wants a graceful shutdown calls `Close` with a
// `context.WithoutCancel` context *before* cancelling.
//
// A nil `Write` or `Read` is refused by `Open`, not by this constructor, so the
// failure names the call that needed it. A state that cannot be persisted would
// run a game whose only copy is in memory, and the failure would be a crash hours
// later rather than an error here — the same reason `NewDebouncer` treats a nil
// `Watch` as fatal rather than as a signal to skip.
func NewRegistry(ctx context.Context, cfg Config) *Registry {
	registryCtx, cancel := context.WithCancel(ctx)

	return &Registry{
		write:   cfg.Write,
		read:    cfg.Read,
		cadence: cfg.Cadence.withDefaults(),
		record:  cfg.Record,
		entropy: cfg.Entropy,
		ctx:     registryCtx,
		cancel:  cancel,
		live:    make(map[int64]*CampaignState),
	}
}

// Config is the set of seams a `Registry` is built over.
//
// A struct rather than five constructor arguments because they are one decision
// — how this process reaches the database, measures itself, and says what time it
// is — and a caller handed five in the right order gets a working registry and a
// caller handed them in the wrong order gets a state that silently never persists.
type Config struct {
	// Write persists one `campaign_state` row. Required; see `Writer`.
	Write Writer

	// Read returns one `campaign_state` row. Required; see `Reader`.
	Read Reader

	// Record receives the outcome of every write, which is the seam onto §13.2's
	// `state.write_ms`. Optional.
	Record WriteRecorder

	// Cadence is the persistence timing and clock. The zero value is the production
	// default, so an unwired field is a working filter rather than no filter.
	Cadence Cadence

	// Entropy draws each state's `Incarnation`. Optional, and defaulted to
	// `crypto/rand.Reader` — a value read rather than a call, so this is not the
	// "direct `crypto/rand`" AGENTS.md forbids in *rule* code. Nothing about a
	// campaign's state depends on it; it only answers "is this client talking to
	// the same loading of the state", and the injectable reader is what makes the
	// format's round trip testable without randomness.
	Entropy io.Reader
}

// Open returns the one live state for a campaign, loading whatever was persisted.
//
// A campaign with no row is not an error: it opens as an empty tabletop, and
// **writes that state immediately**. A row that exists before the first intent is
// a real state — a game that has been joined and left is not the same thing as a
// game that has not been joined — and the distinction is the same fixed point the
// file comment is about, in the place where it would otherwise be invisible.
//
// A second `Open` for a live campaign returns `ErrStateOpen`. See the file
// comment for why refusing beats replacing.
func (r *Registry) Open(ctx context.Context, campaignID int64) (*CampaignState, error) {
	if campaignID <= 0 {
		return nil, fmt.Errorf("%w: campaign id %d is not a campaign", ErrNoState, campaignID)
	}

	// Checked before the reservation so a misconfigured registry fails on the
	// first call rather than after a database round trip, and so the refusal names
	// the seam rather than a symptom of it.
	if r.write == nil {
		return nil, fmt.Errorf(
			"%w: no writer is configured, so no state could be persisted",
			ErrClosed,
		)
	}

	if r.read == nil {
		return nil, fmt.Errorf(
			"%w: no reader is configured, so no state could be resumed",
			ErrClosed,
		)
	}

	state, err := r.reserve(campaignID)
	if err != nil {
		return nil, err
	}

	document, err := r.load(ctx, campaignID)
	if err != nil {
		r.discard(campaignID, state)

		return nil, err
	}

	state.seed(document)

	// The initial write. Not a debounced one and not marked dirty: it establishes
	// the row, so a reader that finds a `campaign_state` row knows the campaign has
	// been opened even if nothing has happened on it yet.
	if err := state.persist(ctx, document); err != nil {
		r.discard(campaignID, state)

		return nil, err
	}

	// The scheduler runs on the **registry's** lifetime context, not on this
	// call's. `ctx` here is the load's context and is done the moment this
	// function returns, so a state started on it would stop persisting before its
	// first window elapsed — the state would accept a game's whole session and lose
	// all of it at the end of the request that opened it.
	//
	//nolint:contextcheck // The lifetime context is the registry's, established in
	// NewRegistry, and no parameter of Open is the context the scheduler should run
	// on. Deriving it from this call's context is the failure this line avoids.
	state.start(r.ctx)

	return state, nil
}

// Get returns the live state for a campaign, if one is open.
//
// The accessor for the caller that wanted to create a state and was refused, and
// the one a hub uses on every join. It does not create anything: a caller that
// would rather open a game than be told one is not open wants `Open`.
func (r *Registry) Get(campaignID int64) (*CampaignState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, held := r.live[campaignID]

	// A reservation whose load has not finished is not yet a state. Returning it
	// would hand out a value with no placements in it and a revision of zero,
	// which is exactly the "loaded and never written" ambiguity this file refuses
	// everywhere else.
	if !held || !state.seeded() {
		return nil, false
	}

	return state, true
}

// Live returns how many campaign states are open. For `/readyz` and for a test
// asserting that a shutdown really shut everything down.
func (r *Registry) Live() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.live)
}

// Close flushes every live state and stops every scheduler.
//
// Errors are joined rather than returned one at a time, because a shutdown that
// stops at the first failure leaves the remaining campaigns unflushed and reports
// as a clean shutdown having lost two of three games.
//
// The flush happens with the caller's context, not the registry's, so a shutdown
// that has already cancelled gets the chance to persist. That is the whole reason
// `Close` takes a context and the reason the composition root passes
// `context.WithoutCancel`.
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()

	states := make([]*CampaignState, 0, len(r.live))
	for _, state := range r.live {
		states = append(states, state)
	}

	r.closed = true
	r.mu.Unlock()

	// Cancel first so no scheduler can flush a second time, and let each `Close`
	// wait for its own goroutine to be gone. The scheduler exits on this signal
	// without writing; the flush below is the only one that runs.
	r.cancel()

	var errs []error

	for _, state := range states {
		if err := state.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	r.mu.Lock()
	clear(r.live)
	r.mu.Unlock()

	return errors.Join(errs...)
}

// reserve takes the single slot for a campaign, before anything is read.
//
// The reservation is taken *before* the load for the reason S-4.2 gives about
// watching a directory before reading it: the reverse order leaves a window in
// which two callers both find nothing and both proceed. Here the window is a
// database round trip rather than thirty microseconds, which makes it wider, not
// narrower.
//
// A reservation that is not yet seeded is still a reservation, so a second `Open`
// refuses rather than waiting — and refusing is the answer, because the second
// caller has no reason to believe it should own this campaign.
func (r *Registry) reserve(campaignID int64) (*CampaignState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, fmt.Errorf("%w: the registry is shut down", ErrClosed)
	}

	if _, held := r.live[campaignID]; held {
		return nil, fmt.Errorf("%w: campaign %d", ErrStateOpen, campaignID)
	}

	state := newCampaignState(campaignID, r.write, r.record, r.cadence.withDefaults(), r.entropy)

	r.live[campaignID] = state

	return state, nil
}

// discard releases a reservation whose state never became usable.
func (r *Registry) discard(campaignID int64, state *CampaignState) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Only ever removes its own reservation. A concurrent `Open` cannot have
	// replaced it — `reserve` refuses while the entry exists — so the check is
	// belt-and-braces against a future change, and it is cheap.
	if held, ok := r.live[campaignID]; ok && held == state {
		delete(r.live, campaignID)
	}
}

// load reads a campaign's persisted document, or the empty one if it has none.
func (r *Registry) load(ctx context.Context, campaignID int64) (Document, error) {
	if r.read == nil {
		return Document{}, fmt.Errorf("%w: no reader is configured", ErrClosed)
	}

	row, err := r.read(ctx, campaignID)
	if errors.Is(err, ErrNoState) {
		return Document{}, nil
	}

	if err != nil {
		return Document{}, fmt.Errorf("realtime: read campaign %d state: %w", campaignID, err)
	}

	document, err := DecodeDocument(row.Blob)
	if err != nil {
		return Document{}, fmt.Errorf("realtime: campaign %d: %w", campaignID, err)
	}

	// The two counters must agree, and the check is here rather than in `Decode`
	// because the column is not part of the document: it is the same number
	// written twice, and a load that picked one would be picking arbitrarily.
	//
	// The upper bound is checked before the conversion so a document whose
	// revision exceeds int64 is refused rather than wrapped into a negative
	// number that happens to equal the column.
	if document.Revision > math.MaxInt64 {
		return Document{}, fmt.Errorf(
			"%w: campaign %d: revision %d does not fit the version column",
			ErrStateUnreadable, campaignID, document.Revision,
		)
	}

	if int64(document.Revision) != row.Version {
		return Document{}, fmt.Errorf(
			"%w: campaign %d: the version column says %d and the document says %d",
			ErrStateUnreadable, campaignID, row.Version, document.Revision,
		)
	}

	return document, nil
}

// CampaignState is the in-memory authority for one campaign's live tabletop.
//
// Exactly one exists per campaign and it is created by `Registry.Open`. It holds:
//
//   - `placements`, the live game objects, keyed by id. Mutated only under `mu`.
//   - `versions`, the version stamped for every id this campaign has ever used,
//     **including ids whose placement has been removed**. A re-created placement
//     is therefore stamped above where the removed one was rather than restarting
//     at 1 — a client that cached version 3 of a removed `p1` and was handed a new
//     `p1` at version 1 would otherwise conclude it was already current, and would
//     apply a stale delta to a token that did not exist when it sent it.
//   - `revision`, the campaign's mutation counter, which is the `version` column
//     and is **not** an ordering authority.
//
// The placement's own `Version` is the ordering authority (S-7.2) and is stamped
// by this type, over anything the caller's mutation function wrote. Authority is
// not delegable to a gameplay system: a system resolves an intent into a change
// to a placement's values, and it does not get to say what version that change is.
type CampaignState struct {
	campaignID int64
	write      Writer
	record     WriteRecorder
	cadence    Cadence

	// incarnation identifies this *loading* of the state. See its own doc comment
	// for why it is not a campaign's identity.
	incarnation Incarnation

	// mu guards everything below it.
	//
	// One lock rather than one per placement, and the reason is contention: §7.2's
	// per-placement versioning exists so two tables on two placements do not
	// serialise on each other, and a per-placement lock over a single map would
	// reintroduce exactly that through the map's own lock. Held for a handful of
	// instructions and never across an encode or a write.
	mu sync.Mutex
	// placements is the live game objects. Values are pointers so that a mutation
	// is a write to the existing value rather than a map assignment, and so that a
	// caller reading under the lock and a caller mutating under the lock cannot be
	// looking at two different addresses for the same placement.
	placements map[PlacementID]*Placement
	// versions is the per-placement version counter, retained after a removal.
	versions map[PlacementID]uint64
	// revision is the campaign's mutation counter. Increments on every applied
	// mutation, including the campaign-level ones that stamp no placement version.
	revision uint64
	// paused is the tabletop's paused flag.
	paused bool
	// dirty is whether a mutation has happened since the last successful write.
	//
	// A flag and never a comparison of encoded bytes, and that is ADR 0037's fixed
	// point in the place it would bite: a campaign that creates a placement and
	// removes it encodes to the same placement list as a campaign that never had
	// one, so "did the bytes change" would answer no for a game that had advanced.
	dirty bool
	// deadline is when the debounced write is due. Zero when nothing is dirty.
	deadline time.Time
	// lastWriteErr is the most recent write failure, reported by the next `Flush`
	// or `Close` so a failure inside a debounce window — which has no caller to
	// return to — still reaches a human.
	lastWriteErr error
	// ready is set once the state has been seeded from a document, and closed
	// after. `Registry.Get` waits on nothing and reads it under the registry lock.
	ready bool
	// stopped is set when the scheduler has exited, whether by `Close` or by a
	// cancelled context. It makes every mutation refuse, so a state whose
	// scheduler is gone cannot accumulate work nobody will ever write.
	stopped bool
	// closed is set by `Close` specifically, and is what a caller reads to tell a
	// deliberate shutdown from a crash.
	closed bool

	// flushMu serialises flushes. Held across the write, deliberately.
	//
	// Two concurrent flushes could take their snapshots in one order and commit in
	// the other, leaving the row holding the *older* state — a row that does not
	// come back on its own, because the next flush writes from memory and memory
	// is already past it. The single scheduler goroutine makes this impossible on
	// its own; `flushMu` keeps it impossible once `Close` can race a scheduler that
	// is on its way out.
	flushMu sync.Mutex

	// wake carries one bit, never more. The scheduler needs to be told "the
	// deadline moved" and nothing else; the deadline itself is read under the lock,
	// because a value sent over a channel could be stale by the time it is read.
	wake chan struct{}

	// stop asks the scheduler to return, and done closes when it has. Separate
	// because `Close` has to be able to wait for the goroutine to be gone rather
	// than for its own signal to be delivered.
	stop chan struct{}
	// done closes when the scheduler goroutine exits, and is guarded by `started`
	// rather than being pre-closed. `Close` waits on it only when a scheduler was
	// launched, because a state reserved by `Registry.Open` and abandoned before
	// `start` would otherwise wait on a channel nothing will ever close — a shutdown
	// that hangs forever, discovered by whichever future edit adds an early return
	// between the reservation and the goroutine.
	done chan struct{}
	// started records whether the scheduler goroutine was launched. Under `mu`.
	started bool
	// closeOnce guards Close's signalling. Shutdown racing a test's cleanup is the
	// normal case, not the exceptional one.
	closeOnce sync.Once
}

// newCampaignState constructs a state with no document and no placements.
//
// Split from `seed` so that a state exists before anything is read, which is what
// makes `Registry.reserve` a reservation rather than a read.
func newCampaignState(
	campaignID int64,
	write Writer,
	record WriteRecorder,
	cadence Cadence,
	entropy io.Reader,
) *CampaignState {
	if entropy == nil {
		entropy = rand.Reader
	}

	// A failure to draw an incarnation is not fatal and not ignored: the zero
	// incarnation is a legal value, and it produces the conservative answer for
	// every client — a full snapshot rather than a delta, which is the right
	// direction for the one question this value answers. `crypto/rand.Reader` does
	// not fail, and a test's reader can be made to, so the branch is reachable and
	// is answered with the value that cannot be wrong.
	incarnation, err := newIncarnation(entropy)
	if err != nil {
		incarnation = Incarnation{}
	}

	return &CampaignState{
		campaignID:  campaignID,
		write:       write,
		record:      record,
		cadence:     cadence,
		incarnation: incarnation,
		placements:  make(map[PlacementID]*Placement),
		versions:    make(map[PlacementID]uint64),
		wake:        make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// CampaignID returns the campaign this state is authoritative for.
func (s *CampaignState) CampaignID() int64 { return s.campaignID }

// Incarnation returns the identity of this loading of the state.
func (s *CampaignState) Incarnation() Incarnation { return s.incarnation }

// Revision returns the campaign's mutation counter.
//
// Not an ordering authority. A client reconciles a placement against that
// placement's `Version`; this number answers "how far is the persisted row behind
// memory" and "is this client's `since` possible".
func (s *CampaignState) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.revision
}

// Paused reports whether the tabletop is paused.
func (s *CampaignState) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.paused
}

// Dirty reports whether a mutation has happened since the last successful write.
//
// The crash floor, stated as a question a caller can ask: a `true` here means the
// persisted row does not describe this state.
func (s *CampaignState) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dirty
}

// Version returns the current version of a placement, which is the ordering
// authority for it (S-7.2), and whether the placement is live.
//
// A removed placement keeps its version, so this answers "how far ahead of you is
// the server" for a client whose placement has been taken away as well as for one
// whose placement has moved.
func (s *CampaignState) Version(id PlacementID) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, known := s.versions[id]

	return version, known
}

// VersionOf returns the current version of a placement, or 0 for one this campaign
// has never used.
//
// A convenience over `Version` for the two places a caller has no use for the
// boolean: a test asserting a placement's sequence, and a client building its next
// intent from a copy it already holds. Where the boolean matters — deciding
// whether a placement exists — `Version` is the method, because treating "absent"
// and "at version 0" as one value is the confusion this file refuses everywhere.
func (s *CampaignState) VersionOf(id PlacementID) uint64 {
	version, _ := s.Version(id)

	return version
}

// Placement returns a copy of one placement, and whether it is live.
//
// A copy that shares nothing mutable with the state, which is the whole reason
// `Placement.clone` exists: a hub that held the live pointer could write to the
// authority without the lock, and a hub that held a shallow copy could write to a
// shared `Conditions` array without the lock.
func (s *CampaignState) Placement(id PlacementID) (Placement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	placement, live := s.placements[id]
	if !live {
		return Placement{}, false
	}

	return placement.clone(), true
}

// Snapshot returns the whole live state, in the form that is persisted and sent.
//
// The document is sorted and self-consistent by construction, so it is exactly
// what `Encode` needs and exactly what the protocol's `snapshot` embeds.
func (s *CampaignState) Snapshot() Document {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.documentLocked()
}

// Create places a game object on the tabletop and returns the change.
//
// The `id` is the system's, not the client's. §4.2 splits a game object — a page,
// durable, with a GM-authored definition — from a placement, and creating one is
// the gameplay system's answer to an intent it resolved. This type is where that
// answer is applied; a client cannot cause a creation by naming an id, because
// naming an id is not what reaches this function.
//
// The supplied placement's `ID` and `Version` are ignored and overwritten. An
// initial `Version` of zero is what every legal placement arrives with, and
// requiring it would be a rule about a field the caller does not own.
func (s *CampaignState) Create(id PlacementID, initial Placement) (Mutation, error) {
	if !id.valid() {
		return Mutation{}, fmt.Errorf("%w: %q is not a usable placement id", ErrNoState, id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mutableLocked(); err != nil {
		return Mutation{}, err
	}

	if _, taken := s.placements[id]; taken {
		// Not a version mismatch. The caller said "place this" about something that
		// is already on the table, and the answer to that is that the request is
		// wrong, not that it is stale.
		return Mutation{}, fmt.Errorf("%w: %q is already placed", ErrNoPlacement, id)
	}

	placement := initial
	placement.ID = id
	placement.Version = s.stampLocked(id)
	placement.Conditions = nil

	// Added rather than copied, so a caller whose slice was unsorted still produces
	// a document whose bytes do not depend on the order the conditions arrived in.
	// The copy is discarded deliberately: taking the caller's array into the
	// authority would be the sharing `clone` exists to prevent.
	for _, condition := range initial.Conditions {
		placement.AddCondition(condition)
	}

	s.placements[id] = &placement
	s.advanceLocked()

	return Mutation{Placement: id, Version: placement.Version, Revision: s.revision}, nil
}

// Mutate applies apply to the placement named by id, and returns the change.
//
// `seen` is the caller's claim: the version it was last given for this placement,
// and 0 for "I have never seen this placement". The claim is checked and applied
// **under one lock**, which is the entire reason they are one call: a `Check`
// followed by a `Mutate` has a window between them in which another client's
// intent lands, and the mutation would then be applied on top of a version the
// caller never saw. There is no `Check` method for that reason.
//
// Every situation where the claim is wrong is `ErrVersionMismatch`, including
// "never seen this" for a placement that exists. A client that is resuming has
// been given a snapshot, so it has a version for everything in it.
//
// `apply` receives the live placement under the lock and may change any of its
// values. It cannot change the placement's authority: `ID` and `Version` are
// stamped back after it returns, so a system that sets `Version` to 9999 gets
// `seen+1`. It must not retain the pointer — the lock is released when this
// function returns.
//
// A nil `apply` is a valid mutation: it stamps the version and records the change
// without touching a value, which is how a caller advances a placement's version
// for a reason this component does not model.
func (s *CampaignState) Mutate(
	id PlacementID,
	seen uint64,
	apply func(*Placement),
) (Mutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mutableLocked(); err != nil {
		return Mutation{}, err
	}

	placement, live := s.placements[id]
	if !live {
		if seen != 0 {
			// The caller describes a version of something that is not here. Its
			// copy is stale — most likely a resume against a placement that has since
			// been removed — and the answer is the same: resynchronise.
			return Mutation{}, fmt.Errorf(
				"%w: %q is not placed, and the caller holds version %d of it",
				ErrVersionMismatch, id, seen,
			)
		}

		return Mutation{}, fmt.Errorf("%w: %q", ErrNoPlacement, id)
	}

	if placement.Version != seen {
		return Mutation{}, fmt.Errorf(
			"%w: %q is at version %d, the caller holds %d",
			ErrVersionMismatch,
			id,
			placement.Version,
			seen,
		)
	}

	if apply != nil {
		apply(placement)
	}

	placement.ID = id
	placement.Version = s.stampLocked(id)
	s.advanceLocked()

	return Mutation{Placement: id, Version: placement.Version, Revision: s.revision}, nil
}

// Remove takes a placement off the tabletop and returns the change.
//
// The version it reports is the one the placement *would* have carried, and the
// version map keeps it, so a re-created placement of the same id is stamped above
// it. The returned `Change` is the only place that number exists afterwards: the
// row is gone, and a delta-only client learns of the removal from the broadcast.
func (s *CampaignState) Remove(id PlacementID, seen uint64) (Mutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mutableLocked(); err != nil {
		return Mutation{}, err
	}

	placement, live := s.placements[id]
	if !live {
		return Mutation{}, fmt.Errorf("%w: %q", ErrNoPlacement, id)
	}

	if placement.Version != seen {
		return Mutation{}, fmt.Errorf(
			"%w: %q is at version %d, the caller holds %d",
			ErrVersionMismatch,
			id,
			placement.Version,
			seen,
		)
	}

	delete(s.placements, id)

	version := s.stampLocked(id)
	s.advanceLocked()

	return Mutation{Placement: id, Version: version, Revision: s.revision}, nil
}

// SetPaused sets the tabletop's paused flag and returns the new campaign revision.
//
// Campaign-level, so it stamps no placement version: §7.2's authority is per
// placement, and a pause changes none of them. It does advance the revision,
// because the campaign has mutated and the persisted row has to show that.
func (s *CampaignState) SetPaused(paused bool) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mutableLocked(); err != nil {
		return 0, err
	}

	s.paused = paused
	s.advanceLocked()

	return s.revision, nil
}

// Resume answers a reconnecting client.
//
// The four cases, in the order they matter:
//
//   - **A different incarnation** — a full snapshot, no error. The process
//     restarted, or the state was reloaded, and the client's copy describes a
//     loading that no longer exists. This is the crash floor seen from the other
//     side, and it is the reason `Incarnation` exists: without it, a client that
//     was two seconds ahead of the last debounce would be told its claim was
//     impossible when it was ordinary.
//   - **Same incarnation, `since` above the revision** — `ErrFutureVersion`. The
//     revision only grows within a loading, so this is a client talking to the
//     wrong process (ADR 0004) or a corrupt counter, and a snapshot would hide
//     both.
//   - **Same incarnation, `since` equal to the revision** — nothing happened, and
//     an empty `Resume` says so.
//   - **Same incarnation, `since` below the revision** — a full snapshot. This
//     component holds no change log, and the delta a protocol that understood
//     `since` would send is a function of one; see `Resume.Changed`.
//
// The client's `seq` is not a parameter and is not consulted. See the file
// comment.
func (s *CampaignState) Resume(incarnation Incarnation, since uint64) (Resume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if incarnation != s.incarnation {
		return Resume{
			Incarnation: s.incarnation,
			Revision:    s.revision,
			Placements:  s.placementsLocked(),
			Changed:     true,
		}, nil
	}

	if since > s.revision {
		return Resume{}, fmt.Errorf(
			"%w: the client holds %d and this state is at %d", ErrFutureVersion, since, s.revision,
		)
	}

	if since == s.revision {
		return Resume{Incarnation: s.incarnation, Revision: s.revision}, nil
	}

	return Resume{
		Incarnation: s.incarnation,
		Revision:    s.revision,
		Placements:  s.placementsLocked(),
		Changed:     true,
	}, nil
}

// Close flushes the state and stops its scheduler. Safe to call more than once.
//
// The shutdown path, and the only one that writes on the way out. A context
// cancellation is *not* this: it stops the scheduler and writes nothing, which is
// the crash case (see the file comment). That asymmetry is why this takes a
// context — a shutdown that has already cancelled its own context still has to be
// able to persist, and the composition root passes `context.WithoutCancel`.
//
// Nothing is written when nothing is dirty. A shutdown that always wrote would be
// a shutdown that always touched the disk, on a table that was not being played.
func (s *CampaignState) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		close(s.stop)
	})

	// Wait for the goroutine, not just for the signal: a caller that returns from
	// `Close` and then tears down the store must not race a scheduler that is still
	// inside a flush.
	//
	// Only when one was launched. A state that `Registry.Open` reserved and then
	// abandoned has no scheduler, and waiting for it would hang the shutdown
	// forever. A scheduler that starts *after* this point is not missed: `Registry.Close`
	// cancels the registry's context before it walks the live set, so `run` returns on
	// its first `select`, and `Close` has already closed `stop`, which is the same
	// thing.
	if s.schedulerStarted() {
		<-s.done
	}

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	return s.Flush(ctx)
}

// Flush writes the state if it is dirty, and returns the first failure it met.
//
// Exposed for the shutdown path and for a caller that wants the crash floor
// lowered at a moment of its own choosing. It is a force: a state whose deadline
// has not arrived is written anyway, which is what makes it usable from `Close`.
//
// The last write failure is reported here even when this call succeeded, so a
// failure that happened inside a debounce window — which had no caller to return
// to — reaches a human. A *successful* write clears it, because the row now
// describes the state.
func (s *CampaignState) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	if !s.dirty {
		err := s.lastWriteErr
		s.lastWriteErr = nil

		s.mu.Unlock()

		return err
	}

	document := s.documentLocked()
	s.dirty = false
	s.deadline = time.Time{}
	s.mu.Unlock()

	writeErr := s.writeState(ctx, document)

	s.mu.Lock()
	s.lastWriteErr = writeErr

	if writeErr != nil {
		// A failed write is not a write. Leaving the state clean here would make
		// the crash floor quietly wrong, which is the failure `store/writer.go`
		// refuses to accept for the same reason it leaves its queue unbounded.
		//
		// The window is re-armed so the retry happens on the cadence rather than
		// waiting for a mutation that may never come, and an unbounded retry is
		// the deliberate choice: a broken store reports one error every two seconds
		// per campaign until it is fixed, which is loud, and giving up would be
		// quiet. See the file comment.
		s.dirty = true
		s.deadline = s.cadence.Now().Add(s.cadence.Debounce)
	}

	s.mu.Unlock()

	return writeErr
}

// LastWriteError returns the most recent write failure, and whether there is one.
//
// Separate from `Flush` so an operator-facing path can ask without causing a
// write, and so a state whose scheduler is gone can still report why.
func (s *CampaignState) LastWriteError() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastWriteErr
}

// seed installs a loaded document into the state.
//
// Separate from the constructor so that the state is reservable before its contents
// are known, and separate from `Registry.load` so that the load's failure modes stay
// in one place. It cannot fail: the document has already been through
// `Document.check`, so a placement here is one this component wrote.
//
// The version map is filled from the document's own versions rather than from a
// fresh stamp, which is what makes a resume continue the sequence rather than restart
// it: a client that left at version 3 comes back to a state whose `p1` is at 3, and
// its next intent is stamped 4.
//
// Conditions are re-sorted through `AddCondition` rather than copied, so a document
// written by a future version with an unsorted set still produces a sorted one. The
// encoder's determinism is a property of this package's output, not of its input.
func (s *CampaignState) seed(document Document) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, loaded := range document.Placements {
		placement := Placement{
			ID:      loaded.ID,
			X:       loaded.X,
			Y:       loaded.Y,
			HP:      loaded.HP,
			MaxHP:   loaded.MaxHP,
			Visible: loaded.Visible,
			Version: loaded.Version,
		}

		for _, condition := range loaded.Conditions {
			placement.AddCondition(condition)
		}

		s.placements[loaded.ID] = &placement
		s.versions[loaded.ID] = loaded.Version
	}

	s.revision = document.Revision
	s.paused = document.Paused
	s.ready = true
}

// seeded reports whether the state has a document installed.
//
// Not a field read: `Registry.Get` calls it while holding the *registry's* lock, and
// the state has its own.
func (s *CampaignState) seeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ready
}

// schedulerStarted reports whether the persistence goroutine was launched.
func (s *CampaignState) schedulerStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started
}

// start launches the persistence scheduler on the given context.
//
// One line, and it exists so that the goroutine's context is a parameter rather than
// a field read. The lifetime is the registry's, established in `NewRegistry`, and a
// goroutine started from `Registry.Open` on the *registry's* context field would be a
// context that `contextcheck` cannot see is inherited — and which a future edit would
// be one line away from deriving from the per-call context, silently stopping every
// state's persistence when the request that opened it ended.
func (s *CampaignState) start(ctx context.Context) {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()

	go s.run(ctx)
}

// run is the persistence scheduler: one goroutine, one timer, one deadline.
//
// The `timer.C` case is followed by a `flushDue` that re-reads the deadline under
// the lock rather than trusting the timer. That re-read is the whole trailing-edge
// guarantee: a mutation arriving between the timer being armed and the timer firing
// has moved the deadline *later*, and a scheduler that wrote anyway would write on
// the leading edge of the previous window — which is the "3s is insufficient"
// failure S-5.2 records for the content watcher, in a place where it would be much
// harder to see.
func (s *CampaignState) run(ctx context.Context) {
	defer close(s.done)
	defer func() {
		// Set here rather than in `Close` alone, so that a cancelled context stops
		// the state exactly as a `Close` does. Without it, intents would keep
		// arriving into a state whose scheduler is gone, and the last of them would
		// be a mutation nothing will ever persist.
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
	}()

	for {
		wait, armed := s.nextWait()
		if !armed {
			// Nothing pending: sleep on the wake bit rather than on a timer, so an
			// idle campaign costs no wakeups at all.
			select {
			case <-s.wake:
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			}

			continue
		}

		select {
		case <-s.cadence.After(wait):
		case <-s.wake:
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		}

		// `flushDue` returns nothing because the scheduler has no caller to report
		// to: the failure is recorded on the state (`LastWriteError`), the window is
		// re-armed, and the loop comes round again. A returned error here would be
		// discarded, and a discarded error is how a broken store becomes silent.
		s.flushDue(ctx)
	}
}

// nextWait returns how long until the debounced write is due, and whether one is
// armed at all.
//
// The clock is read under the lock **with** the deadline, so the wait and the
// deadline it is derived from cannot straddle a mutation. A wait that is zero or
// negative is returned as-is: the channel then fires immediately, which is how a
// deadline that has already passed is written without a special case.
func (s *CampaignState) nextWait() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty || s.stopped {
		return 0, false
	}

	return s.deadline.Sub(s.cadence.Now()), true
}

// flushDue writes the state if the deadline it was armed for is still the one
// that applies.
//
// Returns nothing, and that is a decision about where a failure is reported rather
// than about whether it is. A write that fails inside a debounce window has no caller
// to return to — the intent that triggered it returned 200 thirty seconds ago — so the
// error is recorded on the state and the window is re-armed, and `LastWriteError` is
// how it reaches a human. `Flush` and `Close` return it directly for the paths that
// do have a caller.
//
// Writing nothing when the deadline moved is not a failure either: the loop comes
// straight back round, re-reads the deadline and arms again. That is the trailing
// edge, and it is why the scheduler does not need its own "was I woken early" flag —
// the question is answered by the same read that would take the snapshot.
func (s *CampaignState) flushDue(ctx context.Context) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	if !s.dirty || s.stopped {
		s.mu.Unlock()

		return
	}

	if s.cadence.Now().Before(s.deadline) {
		s.mu.Unlock()

		return
	}

	document := s.documentLocked()
	s.dirty = false
	s.deadline = time.Time{}
	s.mu.Unlock()

	writeErr := s.writeState(ctx, document)

	s.mu.Lock()
	s.lastWriteErr = writeErr

	if writeErr != nil {
		// A failed write is not a write, so the state goes back to dirty and the
		// window is re-armed for the retry. The same trade `Flush` makes, stated
		// here rather than delegated, because the two are the same rule and a reader
		// arriving at one of them from the other should find the reasoning.
		s.dirty = true
		s.deadline = s.cadence.Now().Add(s.cadence.Debounce)
	}

	s.mu.Unlock()
}

// mutableLocked refuses a mutation once the state is no longer accepting them.
func (s *CampaignState) mutableLocked() error {
	switch {
	case s.closed:
		return fmt.Errorf("%w: campaign %d was shut down", ErrClosed, s.campaignID)
	case s.stopped:
		// The lifetime context was cancelled rather than shut down, so this is a
		// crash rather than a shutdown — and both answer `ErrClosed` on purpose. A
		// caller can tell them apart with `Registry.Live`, and the difference is
		// about the row, not about the call: in both cases nothing accepted now
		// would be written, and a caller that could act on the distinction would be
		// a caller that acted on it wrongly.
		return fmt.Errorf(
			"%w: campaign %d has no scheduler; nothing accepted now would be written",
			ErrClosed, s.campaignID,
		)
	case !s.ready:
		// Unreachable through `Registry.Open`, which seeds before it publishes.
		// Stated rather than panicked because this runs on the path a hub's intent
		// handler takes, and a panic there takes the WebSocket goroutine — and with
		// it every client on that connection — down.
		return fmt.Errorf(
			"%w: campaign %d has not loaded its state",
			ErrStateUnreadable,
			s.campaignID,
		)
	}

	return nil
}

// stampLocked returns the next version for an id and records it.
//
// The bump is unconditional for an id that has no version yet, which is what makes
// a created placement's version 1 and every later one higher. The map is retained
// after a removal so a re-created id continues above where it was; see
// `CampaignState.versions`.
func (s *CampaignState) stampLocked(id PlacementID) uint64 {
	s.versions[id]++

	return s.versions[id]
}

// advanceLocked records that the campaign has mutated, and arms the debounce.
//
// One function rather than three call sites, because the two facts it sets are the
// ones the crash floor is made of: a mutation that did not arm the window would
// be a mutation that is never written, and one that did not set `dirty` would be
// invisible to `Flush`.
func (s *CampaignState) advanceLocked() {
	s.revision++
	s.dirty = true
	s.deadline = s.cadence.Now().Add(s.cadence.Debounce)
	s.signalLocked()
}

// signalLocked tells the scheduler the deadline moved.
//
// Non-blocking, and one buffered bit, because the signal carries no information —
// the deadline is read from the lock — so a bit that is already set means the
// scheduler has not yet looked at what prompted it, and one look is enough.
// Blocking here would put a client's intent goroutine behind the scheduler's,
// which is the opposite of what a wake-up is for.
func (s *CampaignState) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// documentLocked renders the current state as a document.
//
// Called under the lock, and it does no I/O: encoding happens after the lock is
// released, so a state with a thousand placements does not hold every other
// placement's writer for the duration of a `json.Marshal`.
func (s *CampaignState) documentLocked() Document {
	return Document{
		Revision:   s.revision,
		Paused:     s.paused,
		Placements: s.placementsLocked(),
	}
}

// placementsLocked returns every live placement, sorted by id.
//
// Sorted, and not "sorted by the encoder", so that the order is a property of the
// state rather than of the standard library's map handling. A persisted blob whose
// bytes depend on a library's iteration order is a blob a test cannot compare and
// a diff cannot show.
func (s *CampaignState) placementsLocked() []Placement {
	ordered := slices.Sorted(maps.Keys(s.placements))
	placements := make([]Placement, 0, len(ordered))

	for _, id := range ordered {
		placements = append(placements, s.placements[id].clone())
	}

	return placements
}

// persist writes a document with no bookkeeping, for the initial row `Open`
// creates.
//
// Not `Flush`: the initial write is not a debounced write, is not conditional on
// dirtiness, and must happen before the scheduler starts so that a failure leaves
// no half-registered state. It is reported through the same `WriteRecorder` and
// through the same `lastWriteErr`, because it is a `campaign_state` write and an
// operator cannot tell the two apart from the log.
func (s *CampaignState) persist(ctx context.Context, document Document) error {
	writeErr := s.writeState(ctx, document)

	s.mu.Lock()
	s.lastWriteErr = writeErr
	s.mu.Unlock()

	return writeErr
}

// writeState encodes and writes one document, and reports the outcome.
//
// The encode happens outside every lock, and the write is on the store's writer
// queue rather than on a connection of its own: `store.Store.Write` is the only
// sanctioned write path in the project, and reaching for `DB().ExecContext`
// bypasses the queue whose whole job is to stop two writers contending for
// SQLite's lock.
func (s *CampaignState) writeState(ctx context.Context, document Document) error {
	// The version column is a signed 64-bit integer (migration 0002) and the
	// revision is unsigned, so the conversion below is checked rather than cast.
	// A revision beyond int64 is not reachable in any lifetime of any process — the
	// mutation counter would have to advance 9.2 quintillion times — but "not
	// reachable" is exactly the reasoning that produces a silent wrap on the day a
	// document is loaded from somewhere this component did not write, which is the
	// fixed point the file comment is about.
	if document.Revision > math.MaxInt64 {
		return fmt.Errorf(
			"%w: campaign %d: revision %d does not fit the version column",
			ErrStateUnreadable, s.campaignID, document.Revision,
		)
	}

	blob, err := EncodeDocument(document)
	if err != nil {
		return err
	}

	if s.write == nil {
		return fmt.Errorf("%w: no writer is configured for campaign %d", ErrClosed, s.campaignID)
	}

	// Converted once, here, rather than inside the transaction closure. The guard
	// above is what makes the conversion safe, and a conversion written inside the
	// closure reads as an unguarded one even when it is guarded five lines above it.
	version := int64(document.Revision)

	started := s.cadence.Now()

	writeErr := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Unix seconds, in UTC, filled from Go rather than from the driver —
		// `store/migrate.go`'s reason: the timestamps in this schema are integers
		// written as integers, and a value produced by SQLite's own clock is a
		// second answer to the same question with a different type.
		if _, err := tx.ExecContext(ctx, upsertState,
			s.campaignID, blob, version, started.UTC().Unix(),
		); err != nil {
			return fmt.Errorf("write campaign_state for campaign %d: %w", s.campaignID, err)
		}

		return nil
	})

	if s.record != nil {
		s.record(ctx, s.campaignID, s.cadence.Now().Sub(started), writeErr)
	}

	if writeErr != nil {
		return fmt.Errorf("realtime: persist campaign %d state: %w", s.campaignID, writeErr)
	}

	return nil
}
