// Chat: the in-memory message log, and the explicit action that moves it into
// the vault.
//
// Architecture §7.4 in two halves, and the halves are not halves of one design —
// they are two designs that happen to be adjacent, and the reason they differ is
// the only interesting thing in this file.
//
// # Half one: a bounded, in-memory, lossy ring
//
// "No session entity, so chat is an in-memory ring buffer per campaign, lost on
// restart." Every property in that sentence is load-bearing and each one is
// argued below: bounded, per campaign, in memory, and **lost**.
//
// # Why losing this on restart is acceptable and losing game state is not
//
// The contrast is the justification, so it is stated first and in full.
//
// `Placement.Version` is the **ordering authority** (S-7.2). A client reconnects
// and reconciles against it, and a version means nothing without exactly one
// holder of the thing it versions. So if a process dies between a mutation and
// its debounced flush, the game does not merely lose two seconds of typing: every
// client connected to one instance keeps a coherent tabletop, every client
// connected to another instance keeps a *different* coherent tabletop, and both
// sets are internally consistent. That is ADR 0004's failure — not a crash, a
// divergence — and it is why `campaign_state` is persisted at all, why the write
// is debounced rather than absent, and why a second `Registry.Open` is refused
// instead of replacing the first.
//
// Chat has no such claim on anybody's attention. No version is stamped by it, no
// intent is resolved from it, no placement moves because of it, and nothing the
// project persists is derived from it. Losing it loses *wording* — text that was
// never authoritative and that no client was ever obliged to reconcile against.
// The failure mode is a hole in a record, not two authorities. A table that
// crashes mid-session and comes back with the last two seconds of tokens missing
// is in the same position as one that crashes mid-session and comes back with the
// last two seconds of chatter missing: both are recoverable by asking somebody
// what happened, and the second costs nothing to recover from because the first
// one is a known, bounded, two-second window that the architecture record states.
//
// So the asymmetry is not "chat is cheap, state is precious". It is **chat has no
// reader who is entitled to an answer, and state has many.** The consequence for
// this file is that chat may be lost, and the consequence for `state.go` is that
// it may not. A design that persisted chat and lost state would be worse than
// either.
//
// # Why the loss is visible rather than silent
//
// A counter that resets on restart is the classic way a client is told "you have
// seen everything" when it has not. So chat is versioned the way `state.go`
// versions itself: `Chat.Incarnation` identifies *this loading*, and
// `Chat.Since` refuses a cursor from another one rather than answering a delta
// against a counter that no longer means what the client's cursor says it means.
// A client that reconnects across a restart is answered with the whole retained
// log and an honest gap, not with an empty list.
//
// # Why the buffer is bounded, and why the message length is bounded too
//
// An unbounded append-a-slice is not a ring buffer; it is the shape that turns a
// chatty table into an OOM. A campaign's chat grows for as long as the campaign
// is open, with nothing to stop it, and the process holding it is the same
// process holding the state every client is reconciling against — so the OOM
// kills the tabletop too. A fixed-capacity ring bounds the memory by construction
// rather than by policy.
//
// The bound is only meaningful if each element is also bounded, so a message body
// has `MaxChatBodyBytes` over it and a longer one is refused. 500 messages of
// 4 KiB is about 2 MiB per live campaign; 500 messages of unbounded length is
// whatever a client chose to POST.
//
// # Why eviction is deterministic, and why there is no map in this file
//
// `Chat` holds a slice, a start index, a count and two counters. There is no map,
// and the eviction rule is one index subtraction — when the ring is full, writing
// over `start` advances `start` by one and drops exactly the oldest message. That
// is the whole rule, and it is a function of the append order alone.
//
// A map would make eviction order a function of Go's randomised map iteration, so
// the contents of a full buffer would differ between two runs that appended the
// same messages in the same order. AGENTS.md forbids map iteration in rule code
// for exactly this reason, and `state.go`'s `placementsLocked` sorts for the same
// reason. Here there is nothing to sort, because there is nothing to iterate: the
// order *is* the storage order. `TestEvictionIsDeterministic` runs the same fill
// sixty-four times and compares the output byte for byte, because "it is
// deterministic" is a claim about every future edit too and not only about this
// one.
//
// # `seq`, and what it is for
//
// `Message.Seq` is a monotonic per-campaign counter, and it answers one question:
// "is there anything I have not seen, or is there nothing new?" Those are different
// states and a client cannot tell them apart without one. It is the same
// `seq`/`version` distinction §7.1 draws for the wire, with `Message.Seq` on the
// server's side and `Chat.Since` as the only way to ask.
//
// It is **not** the campaign's `Revision`. Chat does not touch `campaign_state`,
// a chat line stamps no placement version, and a table that types a sentence
// should not move a token's version. Two counters that are not one counter, for
// the same reason `state.go` keeps them apart.
//
// It never resets and never wraps. A reset is the restart problem above, and a
// wrap would make two different messages share a number, so `ErrSeqExhausted`
// exists even though reaching it would take a table sending 1.8×10^19 messages.
//
// # Half two: the export, and why it is a write
//
// `Exporter.Export` writes a `kind: journal` markdown page into the campaign's
// own vault, through the same `content.Root` confinement and the same
// `Target.WriteFile` atomic path as an editor save. That is the whole reason chat
// is in memory and the export is not: the exported page joins the one state the
// vault can rebuild, it gets a `page_revisions` row, it is editable in Obsidian,
// and it survives a restart. The buffer is a convenience for the two hours a
// session lasts; the vault is the record that outlives it.
//
// Four properties are load-bearing and each is argued at its own definition:
// the path is confined by `os.Root` (`ExportRequest.Folder` is the only
// caller-supplied string that reaches the filesystem and `JournalFileName` is
// derived from a timestamp), secret messages are **excluded by default** and by
// omission rather than by a marker, the revision row is appended **before** the
// file is moved, and an export never overwrites an earlier one.
//
// # No event is emitted from this file
//
// S-12.3 and `observability.AllEventNames()`'s own count test: nothing here
// carries a message body, a page body or a path into a log, and there is no new
// event name to add — the write that fails is returned to the caller, which is
// the route, and the route already has a logger and an event vocabulary this
// package may not extend. A `slog.Any("detail", messages)` here would compile,
// ship, and put a `[!secret]` line in an aggregator, which is the one thing
// S-12.3 forbids.

package realtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/semiplane/semiplane/internal/content"
)

// DefaultChatCapacity is the buffer's bound: 500 messages.
//
// The reasoning is about how long a reader's scrollback has to reach to be useful
// rather than about how much memory is comfortable. A D&D table produces perhaps
// forty lines an hour, so 500 is most of a long evening — and a table that wants
// more than that wants the export, which is the operation that is supposed to be
// the answer to "we have run out of room".
//
// It is a per-`Chat` value rather than a package constant used everywhere: a
// campaign with a hundred players on one table and a campaign testing a ruleset
// alone have no reason to share a number, and `NewChat` defaults a caller that
// does not care to one.
const DefaultChatCapacity = 500

// MaxChatBodyBytes is the largest message body this component accepts, at 4 KiB.
//
// A cap and not a validation, and it is what makes `DefaultChatCapacity` mean
// something: a ring of 500 *bounded* messages is a bound, and a ring of 500
// messages whose length is whatever a browser POSTs is not. 4 KiB is far past any
// line a person types — the longest chat message anybody has ever needed is a
// paragraph — and far below `content.MaxDocumentBytes`, so an oversized message is
// a client bug rather than a message.
//
// The refusal is `ErrMessageTooLong` and it happens **before** the append, so a
// body that was too long is not half in the buffer and not a sequence number
// burned.
const MaxChatBodyBytes = 4 << 10

// JournalFileMode is the mode an exported journal page is written with.
//
// 0o600, and the same value `internal/httpapi/edit` writes a saved page with, for
// its reason: a campaign's content root is created 0o700, so a world-readable file
// inside one would be consistent with that root only by accident. The mode is
// stated rather than inherited from whatever created the file first, because a
// folder that arrived from a sync client may hold 0o644 files and a write would
// then preserve that.
const JournalFileMode = 0o600

// JournalFolder is where an exported transcript lands when the caller names no
// folder.
//
// A single top-level folder, and the export does **not** create it — see
// `Export` for why that is `content`'s business and not this file's. A caller that
// wants the transcript at the root leaves `ExportRequest.Folder` empty.
//
// `page_revisions.path` spells paths root-relative and slash-separated, so this is
// one segment and not `"Journal/"`: `Journal/chat-….md` is built by joining, and
// a trailing slash in the constant would produce a doubled separator in a path
// that becomes a database column.
const JournalFolder = "Journal"

// JournalKind is the `kind` an exported transcript declares.
//
// Declared rather than resolved: phase 8 owns the kind registry, so a journal
// page parses as `domain.KindProse` with `DeclaredKind` naming `journal`. That is
// the correct degradation and S-3.3's: an unregistered kind renders as prose and
// is reportable, rather than costing the page its title. The round trip in
// `TestTheExportRoundTripsThroughTheRealFrontMatterParser` asserts the declared
// value, so a rename here fails rather than producing a page nothing recognises.
const JournalKind = "journal"

// journalExtension is the `.md` every exported page ends with.
//
// The same value the wiki and edit routes use, and for the same reason: `pages`
// has one extension for a page and a file that is not one is a file the indexer
// must learn to skip. A transcript without it would be a file semiplane wrote and
// semiplane could not read.
const journalExtension = ".md"

// journalAttempts is how many names one export will try before giving up on a
// collision.
//
// A second granularity, then a third, up to this bound, because the timestamp in
// `JournalFileName` is only precise to the second and two exports in the same
// second are a thing a GM pressing a button twice does. Refusing is the last
// answer rather than the first: an export that fails because two exports happened
// in one second is a bug report, and an export that silently overwrites the
// previous transcript destroys a record and leaves a `page_revisions` row
// describing content that is no longer on disk.
const journalAttempts = 100

// The answers a caller branches on. Each is `errors.Is`-testable, and none of them
// carries a message body, an author or a page path: an error's text that varies
// with an attacker's input is a channel, which is `content.Root`'s own rule and the
// reason its refusals name no path either.
var (
	// ErrMessageTooLong means a message body was over `MaxChatBodyBytes`. It is
	// refused before the append, so the buffer's bound holds whatever the client
	// sends.
	ErrMessageTooLong = errors.New("realtime: chat message is too long")

	// ErrEmptyMessage means a message body was empty. A blank chat line is not a
	// message, and admitting one would burn a sequence number and put an empty
	// `<li>` in a `role="log"` that a screen reader announces.
	ErrEmptyMessage = errors.New("realtime: chat message is empty")

	// ErrNoAuthor means a message carried no author name. The name is what a
	// reader of the log uses to know who spoke, so a message without one is a
	// message the log cannot render.
	ErrNoAuthor = errors.New("realtime: chat message has no author")

	// ErrSeqExhausted means the campaign's message counter reached its maximum.
	// Unreachable in any lifetime of any process — 1.8×10^19 messages — and
	// present precisely because "unreachable" is the reasoning that produces a
	// silent wrap on the day a counter is loaded from somewhere this component
	// did not write. Two messages sharing a sequence number would be worse than a
	// chat that stops working.
	ErrSeqExhausted = errors.New("realtime: chat sequence exhausted")

	// ErrFutureSeq means a client presented a cursor above this loading's highest
	// sequence number. Within one loading the counter only grows, so a cursor past
	// it is a client talking to the wrong process (ADR 0004) or a corrupt cursor —
	// and answering it with an empty feed would tell a client that it has seen
	// messages it has not.
	ErrFutureSeq = errors.New("realtime: chat cursor is ahead of this loading")

	// ErrNoExport means there is nothing to export: the buffer is empty, or every
	// message in it is a secret one and secrets are excluded. A page saying "no
	// messages" is a page the GM has to delete, and it is the one export that
	// teaches nothing.
	ErrNoExport = errors.New("realtime: there is nothing to export")

	// ErrJournalNameTaken means every name this export would have used is already
	// taken on disk. The refusal is after `journalAttempts` collisions in one
	// second, which is not a thing a person does and is reported rather than
	// papered over.
	ErrJournalNameTaken = errors.New("realtime: no free journal name for this second")

	// ErrJournalMalformed means a transcript could not be read back. Strict by
	// design: a bullet line that does not parse is an error rather than a skipped
	// line, because a format that skips what it cannot read is a format that
	// silently loses messages.
	ErrJournalMalformed = errors.New("realtime: journal transcript is malformed")

	// ErrExportUnconfigured means the exporter has no root lookup or no revision
	// log. Checked at the export rather than in the constructor for the reason
	// `NewRegistry` checks its writer at `Open`: a misconfigured instance should
	// fail the action loudly at the point that needed it, naming the seam, rather
	// than nil-panic or — worse — quietly write a page with no history row.
	ErrExportUnconfigured = errors.New("realtime: chat export is not configured")
)

// Message is one line of a campaign's chat.
//
// A value with no pointers into anything mutable, so a caller holding one is
// holding a copy and cannot reach back into the buffer to change it. The buffer's
// `[]Message` is allocated once and written in place, which is what makes appends
// allocation-free — so a `*Message` handed to a caller would be invalidated by the
// next append from another connection. That is the reason it is a value.
type Message struct {
	// Seq is this message's position in the campaign's chat, from 1, monotonic,
	// never reset and never reused. See the file comment.
	Seq uint64
	// Author is the display name of whoever sent it. Bounded by the route rather
	// than here, because the account name is the route's fact.
	Author string
	// Body is the text. Bounded by `MaxChatBodyBytes` and empty never.
	Body string
	// At is when it was sent, in UTC. Carried rather than re-derived at render
	// time so that a transcript's timestamps cannot shift with a timezone change on
	// the reader's machine.
	At time.Time
	// Secret reports that this message is secret content: it is withheld from a
	// reader who may not see secrets, and excluded from an export by default.
	//
	// A flag on the message rather than a marker inside the body, because S-5.6's
	// position is that secret content is *removed* — not hidden with CSS, not
	// commented out, not wrapped in a class. A marker in the body would have to be
	// written by the sender and could be omitted by anything that writes a body
	// directly, and the failure of an omitted marker is a secret on a page every
	// reader of the campaign can fetch.
	Secret bool
}

// Feed is the answer to "what is new since what I already have".
//
// The three fields are three different answers and a client needs all three, which
// is the entire reason this is a struct and not a `[]Message`:
//
//   - `Messages` empty with `Gap` false means **there is nothing new**.
//   - `Messages` non-empty with `Gap` false means **here is the delta** and it is
//     complete.
//   - `Gap` true means **you missed messages that no longer exist** — either
//     because the ring evicted them or because you are talking to a different
//     loading of the buffer. The messages present are still returned, because
//     dropping them would make a client that could have shown most of the table
//     show nothing, but the client must not present them as a complete delta.
//
// A ring with no gap signal has this failure: it returns what it has, the client
// splices it after what it already shows, and the missing line in the middle of a
// conversation is invisible to both of them.
type Feed struct {
	// Messages are the new messages, oldest first.
	Messages []Message
	// Incarnation is the loading these messages came from, so a client knows
	// whether its cursor can be used against the next answer.
	Incarnation Incarnation
	// Highest is the campaign's newest sequence number, whether or not it is in
	// `Messages`. A client stores this as its cursor for the next request.
	Highest uint64
	// Gap reports that messages between the caller's cursor and the oldest retained
	// message are gone. See the type comment.
	Gap bool
}

// Chat is one campaign's in-memory message log: a bounded ring and a sequence
// counter.
//
// One exists per campaign with a live tabletop, exactly like `CampaignState`, and
// for the same reason — see the file comment. It is safe for concurrent use, and
// a campaign's chat and its state are separate objects on purpose: a chat line is
// not a mutation, and `CampaignState`'s revision counter must not move because
// somebody typed a sentence.
type Chat struct {
	// mu guards everything below it. One lock rather than one per campaign, and
	// the ring is small enough that a lock is cheaper than the alternative of
	// reasoning about who else is reading it.
	mu sync.Mutex

	// ring is the storage, allocated once at the capacity and never reallocated.
	//
	// **Never grown, and never replaced.** A buffer whose backing array is
	// reallocated as it fills is a buffer that copies everything it holds at the
	// moment it is most contended, and `TestTheBufferDoesNotGrowWithTheNumberOfMessages`
	// holds the property by comparing the first element's address across a thousand
	// appends — an append that grew the ring would move it.
	ring []Message
	// start is the index of the oldest retained message.
	start int
	// count is how many slots are occupied, always at most `len(ring)`.
	count int
	// oldest is the sequence number of the oldest retained message, and zero while
	// the buffer is empty.
	//
	// **Maintained rather than derived**, which is the whole of this field's
	// existence: computing it is `highest - count + 1`, and that expression is a
	// `uint64` whose operands are an `int` and a counter, so every derivation is a
	// conversion `gosec` is right to object to and a subtraction that is only safe
	// because of an invariant three lines away. The invariant is now the arithmetic.
	oldest uint64
	// highest is the sequence number of the newest message, and the next append's
	// number minus one. Zero while the buffer is empty, and never equal to a
	// retained message's `Seq` when the buffer has been emptied by eviction.
	highest uint64
	// dropped is how many messages eviction has discarded since this loading
	// began. Reported so an operator can see a table rolling over, and it is what
	// `Since` compares a cursor against rather than guessing.
	dropped uint64
	// incarnation identifies this loading. See the file comment for why a counter
	// that resets on restart needs one.
	incarnation Incarnation
}

// NewChat returns a chat holding at most capacity messages.
//
// A capacity of zero or less is the documented default rather than an error, and
// for the reason `content.SettleTimings.withDefaults` gives: a buffer built
// without stated timings is the safe one, and a zero-capacity ring is a chat that
// accepts messages and keeps none, which is a worse failure than a chat that keeps
// five hundred.
//
// entropy draws the incarnation and may be nil, in which case
// `crypto/rand.Reader` is used. It is a value read rather than a call, so this is
// not the direct `crypto/rand` AGENTS.md forbids in rule code, and nothing about a
// chat depends on it except the ability to tell one loading from another.
func NewChat(capacity int, entropy io.Reader) *Chat {
	if capacity <= 0 {
		capacity = DefaultChatCapacity
	}

	if entropy == nil {
		entropy = rand.Reader
	}

	// A failure to draw is not fatal and not ignored, for `newCampaignState`'s
	// reason: the zero incarnation is legal and produces the conservative answer
	// for every client — the whole retained log rather than a delta.
	incarnation, err := newIncarnation(entropy)
	if err != nil {
		incarnation = Incarnation{}
	}

	return &Chat{
		ring:        make([]Message, capacity),
		incarnation: incarnation,
	}
}

// Incarnation returns the identity of this loading of the chat log.
//
// Not a campaign's identity: a campaign outlives its process, and this answers
// "are you talking to the buffer whose sequence numbers you have been reading".
// `CampaignState.Incarnation` exists for the same question about the same reason.
func (c *Chat) Incarnation() Incarnation { return c.incarnation }

// Capacity returns how many messages this chat retains before it evicts.
//
// Read without the lock, and it cannot change: the ring is allocated once at the
// capacity and nothing ever resizes it. That is what makes it safe to read, and it
// is a fact about the type rather than an accident.
func (c *Chat) Capacity() int { return len(c.ring) }

// Len returns how many messages are retained right now.
func (c *Chat) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.count
}

// Highest returns the newest sequence number, or 0 when nothing has been said.
//
// A cursor a client stores, and the value `Since` refuses a caller for exceeding.
// It does not fall when messages are evicted: "the newest message is #900" and
// "the newest message we still have is #412" are different facts and only the
// first one is worth a client's cursor.
func (c *Chat) Highest() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.highest
}

// Dropped returns how many messages eviction has discarded since this loading
// began.
//
// The number a reader of `Since`'s gap signal can turn into an explanation: a gap
// of one on a table that has dropped nine hundred messages is normal, and a gap of
// one on a table that has dropped none is a cursor from another process.
func (c *Chat) Dropped() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.dropped
}

// Visible returns the retained messages a reader entitled to `includeSecrets` may
// see, oldest first.
//
// **Exclusion, never hiding.** A secret message is not in the returned slice; it
// is not marked, classed or commented. S-5.6's position is that redaction is
// omission — `display: none` still ships the text to the browser, an HTML comment
// still ships it to the response body, and a class still ships it to anything that
// reads the attribute. The only omission that leaves nothing to leak is not
// building the string.
//
// The name says what it does and not what it prevents. A caller that wants "the
// log with the secrets in it" calls this with true, and the one place in this
// package that does is `Exporter.exportable`, where the answer is still no.
func (c *Chat) Visible(includeSecrets bool) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	visible := make([]Message, 0, c.count)

	for offset := range c.count {
		message := c.ring[(c.start+offset)%len(c.ring)]

		// `continue` rather than an empty `if`: the line that must not exist in
		// the output is the line that appends it, and this is that line.
		if message.Secret && !includeSecrets {
			continue
		}

		visible = append(visible, message)
	}

	return visible
}

// Append adds one message and returns it as it was stored.
//
// The `Seq` on the returned value is the one that was assigned; a `Seq` on the
// argument is overwritten, because the sequence is this type's to hand out and a
// caller's claim about it would be a claim about a counter it cannot see.
//
// Every refusal happens before the ring is touched, so a rejected message does not
// burn a sequence number and does not evict the oldest retained one. That is worth
// stating because the alternative is easy to write by accident: assigning `Seq`
// first and validating afterwards means a client that posts a megabyte of text
// evicts somebody else's message.
//
// Safe for concurrent use, and the returned `Message` is a copy: the ring's backing
// array is reused across appends, so a pointer into it would be a pointer the
// next message from another connection overwrites.
func (c *Chat) Append(author, body string, moment time.Time, secret bool) (Message, error) {
	switch {
	case strings.TrimSpace(author) == "":
		return Message{}, fmt.Errorf("%w: nothing to attribute it to", ErrNoAuthor)
	case body == "":
		return Message{}, fmt.Errorf("%w: a blank line is not a message", ErrEmptyMessage)
	case len(body) > MaxChatBodyBytes:
		// The byte length and not the rune count, because `MaxChatBodyBytes` is a
		// memory bound and memory is bytes. A message of four thousand
		// astral-plane characters is under the limit in every sense that matters
		// and over it in the only one the bound is about.
		return Message{}, fmt.Errorf(
			"%w: %d bytes, maximum is %d", ErrMessageTooLong, len(body), MaxChatBodyBytes,
		)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Checked before the arithmetic rather than after, so the increment that would
	// wrap is never performed. `state.go` guards its revision for the same reason
	// and in the same words.
	if c.highest == math.MaxUint64 {
		return Message{}, fmt.Errorf(
			"%w: campaign chat reached %d messages", ErrSeqExhausted, uint64(math.MaxUint64),
		)
	}

	message := Message{
		Seq:    c.highest + 1,
		Author: author,
		Body:   body,
		At:     moment.UTC(),
		Secret: secret,
	}

	// One branch for both directions of a full ring, because they are one rule: a
	// full ring writes over its oldest message and drops it.
	//
	// `(start + count) % len` is the oldest slot when the ring is full, so writing
	// there is the eviction. When it is not full the same expression is the next
	// free slot. No `if count == capacity` to keep in step with the increment
	// below, and no second arithmetic expression, which is where an off-by-one
	// would live.
	c.ring[(c.start+c.count)%len(c.ring)] = message

	if c.count == len(c.ring) {
		// Full: the write above landed on `start`, so the message there is gone and
		// every remaining window moved forward by one. `count` deliberately does
		// not change — the ring is exactly as full as it was, one message newer.
		c.start = (c.start + 1) % len(c.ring)
		c.oldest++
		c.dropped++
	} else {
		if c.count == 0 {
			// The buffer was empty, so this message is also the oldest retained one.
			// Set here rather than derived from `highest` because there is nothing to
			// subtract from on the first append.
			c.oldest = message.Seq
		}

		c.count++
	}

	c.highest = message.Seq

	return message, nil
}

// Since answers a client holding a cursor from a particular loading.
//
// The four cases, in the order they matter, and the same four `CampaignState.Resume`
// has for the same reasons:
//
//   - **A different incarnation** — the whole retained log, and `Gap` true. The
//     counter the client's cursor came from described a buffer that no longer
//     exists, so the cursor is not consulted at all: comparing it against this
//     loading's counter would produce a delta computed against a number that means
//     something else. This is the crash floor seen from the chat side, and it is
//     why `Chat` has an incarnation at all.
//   - **Same incarnation, `since` above `Highest`** — `ErrFutureSeq`. Within one
//     loading the counter only grows, so this is a client on the wrong process
//     (ADR 0004) or a corrupt cursor, and answering it with an empty feed would
//     tell a client it has seen messages it has not.
//   - **Same incarnation, `since` equal to `Highest`** — nothing happened, and an
//     empty `Messages` with `Gap` false says so. This is the state a client must
//     be able to distinguish from a gap, and it is why `Feed` is not a slice.
//   - **Same incarnation, `since` below `Highest`** — the messages above the
//     cursor, plus `Gap` true when the cursor is older than anything still
//     retained.
func (c *Chat) Since(incarnation Incarnation, since uint64) (Feed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if incarnation != c.incarnation {
		return Feed{
			Messages:    c.messagesLocked(),
			Incarnation: c.incarnation,
			Highest:     c.highest,
			Gap:         true,
		}, nil
	}

	if since > c.highest {
		return Feed{}, fmt.Errorf(
			"%w: the client holds %d and this loading is at %d", ErrFutureSeq, since, c.highest,
		)
	}

	feed := Feed{Incarnation: c.incarnation, Highest: c.highest}

	if since == c.highest {
		return feed, nil
	}

	// A gap is a cursor older than the oldest retained message. Compared with the
	// counter rather than derived from it, because `since+1` is the first message
	// the caller has *not* seen and `oldest` is the first message this loading still
	// has: if the first is lower than the second, the messages between them are
	// gone.
	//
	// An empty buffer has `oldest` zero and every `since` above `highest` was
	// already refused, so this is only reached with `oldest` at least one.
	feed.Gap = since+1 < c.oldest
	feed.Messages = c.messagesFromLocked(since + 1)

	return feed, nil
}

// messagesLocked copies every retained message, oldest first.
//
// The storage order *is* the sequence order, which is why there is no sort here
// and no reason for one to be needed: walking `start` forward by one, wrapping at
// the end of the ring, visits the messages in the order they were appended. A map
// anywhere in this path would make the result depend on Go's randomised iteration
// and this is the place that would do it.
func (c *Chat) messagesLocked() []Message {
	messages := make([]Message, 0, c.count)

	for offset := range c.count {
		messages = append(messages, c.ring[(c.start+offset)%len(c.ring)])
	}

	return messages
}

// messagesFromLocked copies the retained messages at or above `from`.
//
// **By comparing each message's own `Seq`, not by computing how many to skip.** The
// second is `from - oldest` turned into an index, which is a `uint64` becoming an
// `int` — safe only because the ring's capacity is an `int`, and a rule that is
// safe only because of an invariant in another field is a rule waiting for that
// invariant to change. This version walks the ring once and asks each slot whether
// it is wanted, so there is no arithmetic to be wrong about and no conversion to be
// audited.
//
// The walk is over `count` slots rather than until `from` is passed, because the
// ring holds a contiguous run of sequence numbers and stopping early would be a
// second thing to get right for no benefit at this size.
func (c *Chat) messagesFromLocked(from uint64) []Message {
	messages := make([]Message, 0, c.count)

	for offset := range c.count {
		message := c.ring[(c.start+offset)%len(c.ring)]

		if message.Seq < from {
			continue
		}

		messages = append(messages, message)
	}

	return messages
}

// Revisions is the append-only record of what this process published, as the
// export needs it.
//
// A separate type from `edit.Revisions` and not an import of it, and the reason is
// direction rather than duplication: `internal/realtime` is the in-memory
// authority and `internal/httpapi/edit` is a route, and a domain-adjacent package
// importing a sibling route's type is a dependency that survives the route's
// deletion and its author's judgement about it. The shape is identical — one
// `Append`, no read, because nothing reads history in this phase — so the
// composition root wires both to the same table with a one-line closure.
//
// An interface rather than a concrete log for the reason `edit.Revisions` is one:
// the write-fails branch is otherwise unreachable from a test, and an unreachable
// branch is an unverified one.
type Revisions interface {
	// Append records one published revision. It returns an error rather than
	// swallowing one because the caller has already decided to write, so a
	// swallowed error here is a page on disk that no history explains.
	Append(ctx context.Context, revision Revision) error
}

// Revision is one row of `page_revisions`: what a page was published as.
//
// `Source` is deliberately absent, and the reason is that this package writes
// exactly one kind of publication: a transcript semiplane exported on a GM's
// behalf, which `edit.SourceSystem` already names and which migration 0008's CHECK
// already accepts. A field a caller could set would be a second spelling of the
// same three values, and the adapter in the composition root is where the constant
// belongs.
type Revision struct {
	// CampaignID is the campaign whose root the path is relative to, and the
	// column the foreign key enforces.
	CampaignID int64
	// Path is the page's root-relative path, spelled as `pages.path` spells it —
	// which is the *cleaned* path from `Target.Path()` and not the string the
	// caller asked for, so a path with a redundant `./` in it produces one spelling
	// in the table and one on disk.
	Path string
	// Content is the whole source as it was written.
	//
	// Unredacted, and the reason is `edit.Revision`'s: the row is readable only
	// through the GM-only route, and a history with the secrets edited out cannot
	// explain the file it is a history of. The load-bearing decision is upstream of
	// here — what reaches `Content` — and it is that an export excludes secrets by
	// default, so the common case puts nothing secret in the column at all.
	Content string
	// AuthorID is the semiplane account that asked for the export, or 0 when
	// there was none. `page_revisions.author_id` is nullable for a write with no
	// account behind it.
	AuthorID int64
}

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it as it stands, and the narrowness is
// `wiki.RootLookup`'s: a type that can enumerate every campaign's root is a type
// that can be handed a slug it was never authorised for, and the only slug it is
// ever given here is the one the export's caller resolved through the access gate.
//
// It is declared here rather than imported from `wiki` for the same reason
// `Revision` is: the direction. And it is a distinct declaration rather than an
// alias of that one, because Go's `iface` linter holds two structurally identical
// interfaces in one package to be a duplication — and, more to the point, two
// declarations are two contracts, and a route that satisfies one has not thereby
// promised the other.
type RootLookup interface {
	// Get returns the retained root for a slug, or `content.ErrNoRoot`.
	Get(slug string) (*content.Root, error)
}

// ExportRequest is one "export to journal" action.
//
// Everything in it is the caller's decision and nothing here is derived from the
// request in a way the caller could not make for itself: there is no `include_secrets`
// query parameter, because S-5.7 makes that value a fact about the *viewer* and
// never an input from the request, and there is no `title`, because the title is
// derived from the timestamp — which also removes the one field ADR 0036 says a
// caller must be careful with, since `pages.title` is served in the search index,
// the nav tree and the `<h1>` and is **not** redacted.
type ExportRequest struct {
	// CampaignID is whose chat is being exported, and the tenancy key the revision
	// row carries.
	CampaignID int64
	// AuthorID is the account that asked for the export. Written into the revision
	// row and into nothing else.
	AuthorID int64
	// Slug is the campaign's slug, resolved through `RootLookup`. A slug and not a
	// campaign id because the root registry is keyed by slug — `content.Registry`
	// is the thing being asked, and asking it by a key it does not have would be
	// the caller's bug answered as a missing campaign.
	Slug string
	// Folder is the root-relative directory the page lands in, and **the only
	// caller-supplied string in this type that reaches the filesystem.** Empty is
	// the root itself. See `Export` for what is refused and by whom.
	Folder string
	// At is when the export happened, in the caller's timezone or not — it is
	// converted to UTC here. A parameter rather than a clock this package reads,
	// for `Cadence`'s reason: this component has no time source of its own and
	// inventing one would make the derived filename untestable.
	At time.Time
	// IncludeSecrets asks for the secret messages to be exported too.
	//
	// **Nothing satisfies it today, and the exclusion is unconditional.** See
	// `Export`: the field is the seam the opt-in will be built behind, and a field
	// that does nothing cannot be flipped by a route that wires a query parameter
	// to it by accident. Turning it into a leak requires an edit to *this* file,
	// and whoever makes it reads the three conditions the opt-in would have to
	// satisfy.
	IncludeSecrets bool
}

// Exported is what one export produced.
//
// `Included` and `Excluded` are reported rather than counted silently, because a
// GM pressing "export to journal" on a table where somebody whispered a secret
// needs to be told that a line is missing from the page rather than discovering it
// weeks later from the transcript. Neither number is secret: it says how many, not
// what.
type Exported struct {
	// Path is the cleaned root-relative path the page was written to, which is the
	// same string in the revision row.
	Path string
	// Revision is the row that was appended, including its `Content`. Held so a
	// caller can report what was published without re-reading it.
	Revision Revision
	// Included is how many messages are in the page.
	Included int
	// Excluded is how many secret messages were withheld. Never zero on a successful
	// export that had secret messages to withhold, because the exclusion is
	// unconditional — see `ExportRequest.IncludeSecrets`.
	Excluded int
}

// Exporter writes one campaign's chat into its own vault as a `kind: journal`
// page.
//
// Built per campaign, holding the `*Chat` it exports, because "export this
// campaign's chat" is the only question it can be asked and a design that took the
// messages as an argument would let a caller pair one campaign's messages with
// another campaign's root. That pairing is a tenancy bug, and it is exactly the
// kind this project's rule code refuses to make expressible.
type Exporter struct {
	chat      *Chat
	roots     RootLookup
	revisions Revisions
}

// NewExporter returns an exporter that publishes chat's messages into the vault
// roots resolves, recording each publication as a page revision.
//
// A nil `roots` or `revisions` is tolerated and refused at the first export, for
// `ErrExportUnconfigured`'s reason: an instance wired without a revision log should
// fail the action loudly, naming the seam, rather than write a page whose history
// nobody can explain.
func NewExporter(chat *Chat, roots RootLookup, revisions Revisions) *Exporter {
	return &Exporter{chat: chat, roots: roots, revisions: revisions}
}

// Export writes a `kind: journal` page holding this campaign's chat and returns
// what it published.
//
// # The four decisions, in the order the code makes them
//
//  1. **Confinement, before anything else is written.** `ExportRequest.Folder` is
//     the only caller-supplied string that reaches the filesystem, and it is
//     resolved by `content.Root.At`, which is an `os.Root` — never
//     `filepath.Clean` plus a prefix check (S-3.5). The file name beside it is
//     derived from `ExportRequest.At` through a fixed layout, so it can contain no
//     separator and no traversal however the clock is set; see `JournalFileName`
//     for the property and its test.
//
//  2. **Secret exclusion, unconditional.** `ExportRequest.IncludeSecrets` is the
//     seam the opt-in will be built behind and **nothing satisfies it today** — the
//     exclusion is a property of `Chat.exportable` taking no parameter at all, so a
//     route cannot reach it.
//
//     Why default-on would be wrong even though a GM might want it: the exported
//     page is a **wiki page in the vault**, and everything that makes a vault page
//     safe stops at the point the file exists. It is indexed (its `body_plain`
//     excludes `[!secret]` *content*, ADR 0031, but that is a property of callout
//     syntax and this is a bare line in a bullet list, not a callout). It is
//     readable by any account on the host with filesystem access to the root. And
//     it is replicated — Obsidian Sync will send it to whatever devices the GM has
//     connected, which is the whole point of the vault and is entirely outside
//     semiplane's authority. S-5.6's `[!secret]` boundary is a *content pipeline*
//     property; an export that wrote a secret into prose would be the one page in
//     the vault with no boundary on it at all.
//
//     What the opt-in would have to be — all three, and **none of them a branch**:
//     a GM-only action carrying an explicit field (never a query parameter,
//     S-5.7); secret messages written **inside `[!secret]` callouts** so the
//     boundary travels with the text, which means the export has to produce a page
//     that goes through `content.Redact` rather than bytes that bypass it; and the
//     resulting page behind the campaign's read gate, which it is.
//     `Chat.exportable` says the same three at the place the change would be made.
//
//  3. **The revision row, then the file.** S-6.4's order, and the argument is
//     which of the two failures is recoverable: a revision row for content that did
//     not land is noise a later export duplicates, while a file that landed with
//     no row is a hole in the one part of this project's state the vault cannot
//     rebuild (ADR 0008). `TestTheRevisionRowIsWrittenBeforeTheFile` asserts the
//     order from both sides — a failing writer leaves the row, and a working one
//     sees it already there.
//
//  4. **A name that is free.** The timestamp is precise to the second, so two
//     exports in one second would collide, and the collision is handled by asking
//     the filesystem rather than by assuming: the first name that does not exist
//     is used, up to `journalAttempts`. `TestTwoExportsInOneSecondDoNotOverwriteEachOther`
//     asserts two files and two rows, because silently overwriting the previous
//     transcript would leave its revision row describing content that is no longer
//     on disk.
//
// # No event is emitted
//
// S-12.3. The errors below carry a campaign slug, a folder the caller supplied and
// an error string, and none of them carries a message body, an author or the page
// content. The failure is returned to the caller — the route — which owns the
// logging and the event vocabulary; `observability.AllEventNames()` has a test
// asserting its own count, so a new name is not this file's to add.
func (e *Exporter) Export(ctx context.Context, request ExportRequest) (Exported, error) {
	if err := request.validate(); err != nil {
		return Exported{}, err
	}

	if e == nil || e.chat == nil || e.roots == nil || e.revisions == nil {
		return Exported{}, fmt.Errorf(
			"%w: a chat, a root lookup and a revision log",
			ErrExportUnconfigured,
		)
	}

	messages, excluded := e.chat.exportable()
	if len(messages) == 0 {
		return Exported{}, fmt.Errorf("%w: nothing to publish for this campaign", ErrNoExport)
	}

	body := RenderJournal(Journal{
		Title:    JournalTitle(request.At),
		At:       request.At,
		Messages: messages,
	})

	root, err := e.roots.Get(request.Slug)
	if err != nil {
		return Exported{}, fmt.Errorf("realtime: chat export: the content root: %w", err)
	}

	// Confinement first, and before the revision row: a path that leaves the root
	// must produce no bytes *and* no row, because a row for a page that was never
	// written is exactly the noise the ordering argument is about.
	target, err := e.target(root, request.Folder, request.At)
	if err != nil {
		return Exported{}, fmt.Errorf("realtime: chat export: the page path: %w", err)
	}

	revision := Revision{
		CampaignID: request.CampaignID,
		Path:       target.Path(),
		Content:    string(body),
		AuthorID:   request.AuthorID,
	}

	if err := e.revisions.Append(ctx, revision); err != nil {
		// Before the file, so nothing on disk has changed. Returning the error
		// rather than logging it is what keeps a message body out of the log: the
		// caller logs the error, and the error does not contain the page.
		return Exported{}, fmt.Errorf("realtime: chat export: record the revision: %w", err)
	}

	if err := target.WriteFile(ctx, body, JournalFileMode); err != nil {
		// After the rename may already have happened, and `Target.WriteFile`'s own
		// documentation says what that means: *durability unknown*, not *not
		// written*. The row says the page was published and the log says the move
		// failed, which is the recoverable pair rather than the other one.
		return Exported{}, fmt.Errorf("realtime: chat export: write the page: %w", err)
	}

	return Exported{
		Path:     target.Path(),
		Revision: revision,
		Included: len(messages),
		Excluded: excluded,
	}, nil
}

// target resolves the first free page path for this export and returns a handle
// to it.
//
// A method rather than a function so the attempts counter and the error are one
// place, and so the "ask the filesystem" step is not something a later edit
// replaces with an assumption about the clock.
func (e *Exporter) target(
	root *content.Root,
	folder string,
	moment time.Time,
) (*content.Target, error) {
	for attempt := 1; attempt <= journalAttempts; attempt++ {
		target, err := root.At(journalPath(folder, moment, attempt))
		if err != nil {
			return nil, fmt.Errorf("confine the journal page path: %w", err)
		}

		_, statErr := target.Stat()
		if statErr == nil {
			continue
		}

		// `ErrNotExist` is the only one of `content`'s answers that means "this
		// name is free", and it is the only one that is checked by value rather
		// than by shape. Anything else — a refusal, a permission error — is
		// returned rather than retried, because retrying a refusal fifty more
		// times produces the same refusal and a slower answer.
		if errors.Is(statErr, content.ErrNotExist) {
			return target, nil
		}

		return nil, fmt.Errorf("stat the journal page path: %w", statErr)
	}

	return nil, fmt.Errorf(
		"%w: %d names in one second were all taken", ErrJournalNameTaken, journalAttempts,
	)
}

// exportable returns the messages an export would publish, and how many it is
// withholding.
//
// **The load-bearing line in this file is the `if`, and the load-bearing property
// is that it takes no parameter.** A secret message is not marked, not replaced by a
// placeholder and not moved to the end of the list: it is never copied into the
// slice that becomes the page's bytes. A placeholder would satisfy "the export does
// not show the secret" while shipping the secret's text into the file, the index,
// the sync client and every browser that ever fetches the page — which is the
// failure ADR 0029 exists to prevent, arrived at from the export side rather than
// the render side.
//
// The absence of a parameter is the security property rather than an oversight.
// The moment this function takes an `includeSecrets bool`, a route can pass it a
// query parameter and S-5.7's "the viewer's entitlement is never an input from the
// request" becomes a convention rather than a type. So the opt-in is not a branch
// here; it is an edit to this file, made by somebody who reads what the opt-in
// would have to be — three conditions, all of which are beyond what a ring buffer
// can supply:
//
//  1. A GM-only action carrying an explicit field. Not a query parameter, and not
//     an inference from the caller already being a GM: a GM pressing "export" and a
//     GM pressing "export with the whispers" are two actions, because one of them
//     writes plaintext into a synced file.
//  2. The secret messages written **inside `[!secret]` callouts**, so the boundary
//     travels with the text. `content.Redact` is the thing that finds those, and it
//     runs on the source before the render — which means the export has to produce
//     a *page* that goes through the pipeline rather than bytes that bypass it.
//     That is a bigger change than a branch, and it is why this is not one.
//  3. The resulting page behind the campaign's read gate, which it is, and which is
//     the only reason the other two are worth building.
//
// `excluded` is counted rather than inferred from the lengths, because "how many
// lines are missing from this page" is something a GM is entitled to be told — and
// a count is not a disclosure.
func (c *Chat) exportable() (messages []Message, excluded int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	messages = make([]Message, 0, c.count)

	for offset := range c.count {
		message := c.ring[(c.start+offset)%len(c.ring)]

		if message.Secret {
			excluded++

			continue
		}

		messages = append(messages, message)
	}

	return messages, excluded
}

// validate refuses a request that names no campaign or no campaign to export for.
//
// Checked here and not by the caller because both of these are fields whose zero
// value is a *plausible* value rather than an obviously absent one: `CampaignID`
// zero would write a revision row whose foreign key fails, and `Slug` empty would
// ask the root registry for a campaign named "" — which is a real lookup that
// returns `ErrNoRoot`, and an error naming the seam would send a reader looking at
// the registry instead of at their request.
func (r ExportRequest) validate() error {
	if r.CampaignID <= 0 {
		return fmt.Errorf("%w: campaign id %d is not a campaign", ErrNoExport, r.CampaignID)
	}

	if strings.TrimSpace(r.Slug) == "" {
		return fmt.Errorf("%w: no campaign slug to export into", ErrNoExport)
	}

	return nil
}

// journalPath builds the root-relative path of the attempt'th name for this second.
//
// `attempt` 1 is the plain derived name and attempt 2 is the same name with a
// `-2` before the extension, so the sequence is a suffix on the *stem* rather
// than on the extension: `chat-20261002-140505-2.md` is a page and
// `chat-20261002-140505.md.2` is not one.
//
// `folder` is joined rather than concatenated so that an empty folder produces a
// bare file name with no leading separator — and so that `path.Join` cleans the
// result, which is a convenience and **not** the confinement: a `path.Join` that
// produces `../escape` is still refused, by `os.Root`, one call later.
func journalPath(folder string, at time.Time, attempt int) string {
	name := JournalFileName(at)

	if attempt > 1 {
		name = stemOf(name) + "-" + strconv.Itoa(attempt) + journalExtension
	}

	if strings.TrimSpace(folder) == "" {
		return name
	}

	return path.Join(folder, name)
}

// stemOf is a file name without its extension.
func stemOf(name string) string {
	return strings.TrimSuffix(name, journalExtension)
}

// Journal is one exported transcript: the title it declares, when it was
// exported, and the messages it holds.
//
// A value rather than the arguments of a function so that rendering and parsing
// have one vocabulary between them: a transcript's fields are a set, and a
// renderer taking three positional arguments and a parser returning four values
// is a shape where one of them gains a field and the other does not.
type Journal struct {
	// Title is the front-matter `title:`. Derived by `JournalTitle` rather than
	// supplied, because ADR 0036 says a page title is served in three places and
	// is **not** redacted — so a caller-supplied title is a way to put a secret in
	// the search index, the nav tree and the `<h1>` at once.
	Title string
	// At is when the export happened. Rendered in the front matter as `exported:`
	// and used for the prose sentence above the transcript, so a GM reading the
	// page knows when it was taken.
	At time.Time
	// Messages are the transcript, oldest first. Never contains a secret message,
	// because the only caller that builds a `Journal` is `Export`, which excludes
	// them before this point.
	Messages []Message
}

// journalFieldSeparator divides the fields of one transcript line.
//
// ASCII hyphen and space, and **that is load-bearing rather than cosmetic.** The
// escaping below writes a backslash before every ASCII punctuation byte in a
// message, so a body can never contain a raw `-`, and a line's fields can
// therefore be found by splitting on this and nothing else can be confused for a
// separator. An em dash or a middle dot would have looked better and would have
// been a delimiter a message body could forge, because neither is ASCII and so
// neither would be escaped.
//
// The body is the fourth field of `SplitN`, so a separator *inside* it is not a
// problem anyway — but the first three are parsed, and a body forging a separator
// in an author name would make the author's name carry the rest of the line.
const journalFieldSeparator = " - "

// journalBullet opens one transcript line.
//
// A bullet, so a GM opening the page in Obsidian sees a list. It is also the parse
// rule: a line beginning with this is a message and **must** parse, and any other
// line is prose and is skipped. See `ParseJournal`.
const journalBullet = "- "

// journalAuthorOpener and journalAuthorCloser wrap the author so it is rendered
// bold.
//
// Also two bytes the escaping guarantees a body cannot contain adjacently: an
// escaped `*` is `\*`, so `\*\*` is never two adjacent asterisks and the closing
// delimiter cannot be forged from inside the author or the body.
const (
	journalAuthorOpener = "**"
	journalAuthorCloser = "**"
)

// RenderJournal renders one transcript as the markdown page semiplane writes.
//
// The output is a page, not a data file: it has front matter, it reads as prose,
// and a GM can edit it in Obsidian. Two properties are therefore in tension —
// **it must be readable by a person** and **it must be read back by this
// package** — and the escaping below is where they meet.
//
// # Why the escaping is total rather than minimal
//
// A minimal escape (backslash before `*`, `_`, “ ` “ and a leading `#`) renders
// beautifully and is not invertible: there is no way to tell a body that contained
// `\*` from one that contained `*`, so the round trip would lose information and a
// transcript would be a lossy copy of a buffer that is about to be overwritten.
//
// So every ASCII punctuation byte in a message is escaped, which is exactly the
// set CommonMark defines as escapable and therefore renders as itself. The file
// reads with a few extra backslashes in it and renders identically, and the
// round trip is total over arbitrary input — `TestTheExportRoundTripsThroughTheRealFrontMatterParser`
// carries the punctuation, the backslashes, the newlines, the quotes and the
// non-ASCII text that a minimal escape would have lost.
//
// # Why a newline becomes `\n`
//
// The format is line-oriented — one message per line — and a raw newline inside a
// body would be indistinguishable from the start of the next message. So a line
// break is written as the two characters `\` and `n` and read back as one.
//
// The rendering cost is real: markdown does not treat `\n` as a break, so a
// multi-line message appears in Obsidian with an `n` where the line break was. That
// is the trade, and the direction is chosen deliberately: the transcript's job is to
// be the durable copy, and a durable copy that silently loses a message's structure
// is worse than one that shows an `n`. The live log is where a line break is
// rendered, and the transcript says so in the sentence above the list.
//
// # Why the front matter is single-quoted YAML
//
// `yaml.Marshal` would emit a whole document with its own conventions and its own
// ordering, and the block's bytes are then semiplane's to predict if a round trip
// is to mean anything. Single quotes are the one YAML form with exactly one escape
// (`”`), which makes quoting total with no library and no guessing about which
// characters need it.
func RenderJournal(journal Journal) []byte {
	var out strings.Builder

	out.WriteString(frontMatterDelimiter + "\n")
	out.WriteString("kind: " + JournalKind + "\n")
	out.WriteString("title: " + yamlSingleQuoted(journal.Title) + "\n")
	out.WriteString("exported: " + yamlSingleQuoted(journal.At.UTC().Format(time.RFC3339)) + "\n")
	out.WriteString("messages: " + strconv.Itoa(len(journal.Messages)) + "\n")
	out.WriteString(frontMatterDelimiter + "\n\n")

	out.WriteString(journalIntro(journal) + "\n\n")

	for _, message := range journal.Messages {
		out.WriteString(journalBullet)
		out.WriteString(strconv.FormatUint(message.Seq, 10))
		out.WriteString(journalFieldSeparator)
		out.WriteString(message.At.UTC().Format(time.RFC3339))
		out.WriteString(journalFieldSeparator)
		out.WriteString(journalAuthorOpener)
		out.WriteString(escapeJournalText(message.Author))
		out.WriteString(journalAuthorCloser)
		out.WriteString(journalFieldSeparator)
		out.WriteString(escapeJournalText(message.Body))

		out.WriteByte('\n')
	}

	return []byte(out.String())
}

// ParseJournal reads a transcript back out of a page's body.
//
// The body and not the file, because splitting the front matter off is
// `content.Parse`'s job and doing it twice would be two answers to "where does the
// prose begin". The round-trip test therefore goes through the real parser and
// hands this function what it produced, rather than asserting against a fixture
// that happens to look like the renderer's output.
//
// # Why a bullet line that does not parse is an error
//
// Two rules, and the strict one is the important one:
//
//   - A line beginning with `journalBullet` is a message and **must** parse as one.
//     A GM adding their own bullet to the transcript gets `ErrJournalMalformed`
//     rather than a silently shorter message list.
//   - Any other line is prose and is skipped, which is what lets the explanatory
//     sentence above the list survive a re-read.
//
// A parser that skipped what it could not read would be the "a watcher that indexed
// three of ten events is a wrong answer that looks right" shape ADR 0037 is about:
// a transcript missing a message and a transcript that never had one are the same
// document. The refusal is visible; the skip would not be.
//
// `Secret` is always false in what comes back. That is not an oversight and not a
// hole: a transcript written by this package contains no secret message, because
// `Export` excludes them, and a secret message reaching a transcript is the failure
// `Export`'s doc comment is about rather than a thing to be recovered from one.
func ParseJournal(body string) ([]Message, error) {
	var messages []Message

	for line := range strings.Lines(body) {
		// One trim, not two: `strings.Lines` keeps the newline it found, and a CRLF
		// transcript is a transcript a GM edited on Windows, so both carriage return
		// and newline come off. Nothing is lost by removing every trailing one — a
		// real trailing `\r` in a message body is escaped by `escapeJournalText` and
		// arrives here as the two characters `\` and `r`.
		line = strings.TrimRight(line, "\r\n")

		if !strings.HasPrefix(line, journalBullet) {
			continue
		}

		message, err := parseJournalLine(strings.TrimPrefix(line, journalBullet))
		if err != nil {
			return nil, err
		}

		messages = append(messages, message)
	}

	return messages, nil
}

// parseJournalLine reads one `- seq - when - **author** - body` line.
func parseJournalLine(rest string) (Message, error) {
	fields := strings.SplitN(rest, journalFieldSeparator, 4)
	if len(fields) != 4 {
		return Message{}, fmt.Errorf(
			"%w: a transcript line has %d fields, not 4", ErrJournalMalformed, len(fields),
		)
	}

	seq, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 64)
	if err != nil {
		return Message{}, fmt.Errorf("%w: the sequence number: %w", ErrJournalMalformed, err)
	}

	sent, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[1]))
	if err != nil {
		return Message{}, fmt.Errorf("%w: the timestamp: %w", ErrJournalMalformed, err)
	}

	author, err := parseJournalAuthor(fields[2])
	if err != nil {
		return Message{}, err
	}

	body, err := unescapeJournalText(fields[3])
	if err != nil {
		return Message{}, err
	}

	return Message{Seq: seq, Author: author, Body: body, At: sent.UTC()}, nil
}

// parseJournalAuthor unwraps the bold author field.
func parseJournalAuthor(field string) (string, error) {
	if !strings.HasPrefix(field, journalAuthorOpener) ||
		!strings.HasSuffix(field, journalAuthorCloser) {
		return "", fmt.Errorf("%w: the author is not delimited", ErrJournalMalformed)
	}

	// The delimiters are two bytes each and an escaped body cannot produce two
	// adjacent asterisks, so trimming one from each end is exact rather than a
	// search.
	inner := strings.TrimSuffix(strings.TrimPrefix(field, journalAuthorOpener), journalAuthorCloser)

	author, err := unescapeJournalText(inner)
	if err != nil {
		return "", err
	}

	return author, nil
}

// escapeJournalText backslash-escapes one message body or author name.
//
// Escapes every ASCII byte that is not a letter, a digit or a space, and writes a
// newline as `\n`. Two consequences, both load-bearing:
//
//   - **A body can never contain a raw `-`, `*`, space-hyphen-space or newline**,
//     which is what makes the line format parseable at all.
//   - **It is invertible**, because the escape is a function of the byte rather
//     than of its position in a line.
//
// Bytes at or above 0x80 pass through untouched, because ASCII punctuation is the
// whole of what CommonMark escapes and mangling every accented character into
// `\x` sequences would make the file unreadable to the GM it is for.
func escapeJournalText(text string) string {
	var out strings.Builder

	out.Grow(len(text) + 16)

	for index := range len(text) {
		char := text[index]

		switch {
		case char == '\n':
			out.WriteString(`\n`)
		case char == ' ' || isJournalAlphanumeric(char):
			out.WriteByte(char)
		case char < 0x80:
			out.WriteByte('\\')
			out.WriteByte(char)
		default:
			out.WriteByte(char)
		}
	}

	return out.String()
}

// unescapeJournalText is `escapeJournalText`'s inverse.
//
// Byte-oriented, so a backslash before a multi-byte rune is handled without
// decoding: the byte after the backslash is written as itself and the remaining
// bytes of that rune are written as themselves, which reproduces the original
// bytes exactly. A trailing backslash is `ErrJournalMalformed` rather than a
// silently dropped character — a transcript with a truncated escape is a
// transcript whose last message is not what it says.
func unescapeJournalText(text string) (string, error) {
	var out strings.Builder

	out.Grow(len(text))

	escaped := false

	for index := range len(text) {
		char := text[index]

		switch {
		case !escaped && char == '\\':
			escaped = true
		case escaped && char == 'n':
			out.WriteByte('\n')

			escaped = false
		case escaped:
			out.WriteByte(char)

			escaped = false
		default:
			out.WriteByte(char)
		}
	}

	if escaped {
		return "", fmt.Errorf("%w: a line ends with a backslash", ErrJournalMalformed)
	}

	return out.String(), nil
}

// isJournalAlphanumeric reports whether an ASCII byte is a letter or a digit.
//
// The three bytes that are *not* escaped: letters, digits and the space. A space
// stays raw because escaping it would turn a sentence into one word, and it is
// safe unescaped because every field separator in the line format contains a
// hyphen or an asterisk and those are escaped.
func isJournalAlphanumeric(char byte) bool {
	switch {
	case char >= 'a' && char <= 'z':
		return true
	case char >= 'A' && char <= 'Z':
		return true
	case char >= '0' && char <= '9':
		return true
	default:
		return false
	}
}

// yamlSingleQuoted wraps a value in YAML's single-quoted form.
//
// The one form with exactly one escape, which is why it is the one used: doubling
// a single quote is the whole of the rule and needs no library to know it. Double
// quotes would need Go's escape set and YAML's to agree, and they do not have to.
func yamlSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// journalIntro is the sentence above the transcript.
//
// It says the three things a GM reading the page months later needs: who exported
// it, when, and that the order is oldest-first. The count is here as prose rather
// than as a heading because the transcript is a section of a journal page and the
// page's own title carries the date.
func journalIntro(journal Journal) string {
	exported := journal.At.UTC().Format("2006-01-02 15:04 UTC")

	plural := "messages"
	if len(journal.Messages) == 1 {
		plural = "message"
	}

	return "Exported by semiplane at " + exported + ". " +
		strconv.Itoa(len(journal.Messages)) + " " + plural + ", oldest first. " +
		"A message that contained a line break is written here on one line, with \\n where the break was."
}

// frontMatterDelimiter opens and closes the front matter block.
//
// The literal `---` on its own line, because that is the one form Obsidian,
// Jekyll and Hugo all agree on; `content.frontMatterDelimiter` says the same and
// the two must not drift, which is why this is named rather than written inline.
const frontMatterDelimiter = "---"

// JournalTitle is the `title:` an export declares for the moment it happened.
//
// Derived and never supplied, for ADR 0036's reason: `pages.title` is served in
// the search index, the nav tree and the `<h1>`, and it is **not** redacted, so a
// caller-supplied title is three ways to put a secret into the product. A title
// derived from a timestamp cannot hold one.
func JournalTitle(at time.Time) string {
	return "Chat " + at.UTC().Format("2006-01-02 15:04") + " UTC"
}

// JournalFileName returns the file name an export at that moment would use.
//
// **It is derived from a timestamp through a fixed layout, and that is the whole
// of its safety.** The layout's fields are year, month, day, hour, minute and
// second, all of which render as digits, plus the two hyphens and the `Z`. So the
// name can contain no path separator, no `..`, no leading dot, no NUL and no
// space — whatever the clock is set to, and there is no clock this function reads
// for itself.
//
// `TestTheDerivedNameCannotCarryASeparatorOrATraversal` holds that over the whole
// representable range of `time.Time` that a layout can format, and the claim it
// protects is S-3.5's: the *one* string semiplane derives from untrusted-adjacent
// input cannot be the thing that escapes the root. The confinement itself is
// `os.Root`'s, one call away in `Exporter.target`; this is the property that makes
// the caller-supplied `ExportRequest.Folder` the only thing that needs it.
//
// Seconds and not nanoseconds, deliberately: the suffix counter in `journalPath`
// already handles a collision, and a name with nanoseconds in it is a name no
// human recognises in a file listing.
func JournalFileName(at time.Time) string {
	return "chat-" + at.UTC().Format("20060102-150405") + journalExtension
}
