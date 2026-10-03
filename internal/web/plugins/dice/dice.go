// Package dice is §10.6's graphical dice roller: a page type, a form, and the wire
// frame a send produces.
//
// # It does not roll, and the reason is structural rather than a promise
//
// §10.6's table says the roller "must **not** roll client-side; a client-side roll is
// unverifiable and would break the audit trail". The reason that is worth a package
// comment rather than a `// TODO` is *what* makes a number auditable: the answer
// arrives in the delta the hub broadcasts, **stamped with the version the hub
// assigned** (S-7.2's only ordering authority, ADR 0009 rule 2), and a number this
// package invented has no such stamp. There is nowhere in this file to put one, and
// there is nowhere to put one *because* of the two structural facts below rather than
// because of discipline:
//
//   - **There is no source of randomness in this package's import graph.**
//     `TestTheRollerHasNoSourceOfRandomness` parses the imports and fails on
//     `math/rand`, `math/rand/v2` and `crypto/rand`. A roll needs a number from one
//     of those, so the audit is not a convention — adding the import fails the build.
//
//   - **There is no field a result could arrive in.** `Request.Frame` marshals a
//     `realtime.ClientIntent`, whose `args` is a flat *typed* union with no opaque
//     field, and `realtime.Decode` refuses an unknown key rather than discarding it.
//     `TestTheFrameCarriesNoFieldAResultCouldArriveIn` pins the four keys the frame
//     may contain, so a future field called `total` fails rather than shipping.
//
// The roller's output is therefore `Answer`, and `Answer` is read out of the
// `realtime.Resolution` the hub returned. Not the dice. Not a total. The frame.
//
// # What it renders, and what it deliberately does not
//
// `Answer` carries the placement, the **version**, the operation and the actor. The
// *numbers* are not in it, and that is not an omission in this package — it is
// `rules.Mutation.Args` arriving whole. The roll's total lives inside the system's
// own mutation payload, and S-12.3 plus `rules.Intent.Args`'s own comment say
// semiplane transports an opaque payload and never interprets it: a JSON-shaped guess
// at 5e's private creature encoding would be a second answer that holds until 5e's
// second pack revision. §10.6.1 says the same thing from the other side — "the rule
// data itself comes from a gameplay module, never from the UI".
//
// So a reader of this page learns the roll was recorded, on which token, at which
// version, and by whom — and reads the total off the token, which every client at the
// Table already holds. What the roller adds is the *verifiable* half: a number on a
// page with no stamp beside it is a claim, and this is not one.
//
// # Where the notation comes from
//
// Nothing here knows what a d6 is. `WidgetView.Grammar` is the campaign's own
// `rules.Grammar`, so the summary shown beside the expression and the worked example
// on the quick-roll button are the *system's* copy (§10.4: the table is data). A
// build whose plugin was removed still renders the page, with an empty grammar, and
// says so — which is §14's "renders for an empty `Derive` output" applied to a page
// type rather than to a view.
//
// # What it needs from the outside
//
// One thing the UI tier cannot supply, and it is stated here rather than discovered
// at the table: `webplugins.Emitter.Emit` returns a bare `error`, so the `Resolution`
// ADR 0042 named as the missing seam never reaches a plugin through the emitter that
// record introduced. This package therefore declares what it emits
// (`Declaration.Emits`) and hands the *frame* to whoever mounts it; the route in
// `internal/httpapi/plugins` decodes that frame with `realtime.Decode` and calls
// `realtime.Hub.Dispatch`, which is the same path a human player's keystroke takes.
// The registry still enforces §10.6's "it cannot register a new op": `Emits` is
// checked against the registered gameplay systems before the plugin is admitted.
package dice

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
)

// The plugin's identity, as §10.6's three registrations state it.
//
// `PagePath` is the route pattern the page type is mounted at, and `Kind` is the game
// object kind a page of this kind claims. Neither is a semiplane kind, which is what
// `webplugins.PageType.check` refuses otherwise — a UI plugin offering to render the
// Table's own game objects would be offering to render `token`.
const (
	// PluginName identifies this plugin within the build.
	PluginName = "dice-roller"

	// PluginTitle is what a person is shown.
	PluginTitle = "Graphical dice roller"

	// Kind is the page kind this plugin declares.
	Kind rules.Kind = "dice-roller"

	// PagePath is where the page type is mounted.
	//
	// The **pattern**, not a path: `net/http`'s mux needs `{slug}` here because the
	// campaign access gate reads that wildcard from the pattern that matched. It is
	// declared once, on the page type, and `internal/httpapi/plugins`'s `Mount` derives
	// the mount from the registry rather than restating the string — a second copy of a
	// route is two answers for one route, which is the stylesheet-import failure this
	// repository keeps paying for.
	PagePath = "/c/{slug}/plugins/dice-roller"

	// OpRoll is the operation this plugin emits.
	//
	// **Written as a literal, not imported from a gameplay system.** A UI plugin that
	// imports `dnd5e` to learn its vocabulary has made itself a 5e plugin, and
	// `webplugins.Registry.checkEmits` then has nothing to check: the literal is what
	// gets compared against `plugin.Registry.Resolves`, so a build with no 5e refuses
	// this plugin at registration — S-10.6's first row, reached from the UI side.
	OpRoll rules.Op = "roll"

	// ViewName is the view the roller's page renders.
	//
	// A `rules.Payload` records the view it answers, and `Payload.View` is what picks
	// the renderer. A page type's payload is not one of the gameplay system's declared
	// views — it is the plugin's own view model — so the name is the plugin's, and the
	// payload carries a `WidgetView`, which is §10.6.1's "a plugin's own templ
	// component takes its own Go type" rather than a map traversal.
	ViewName = "dice-roller"
)

// The refusals this package produces. Every one is a *class* — none carries the frame
// that broke it, the expression it choked on, or anything else a reader chose.
//
// S-12.3 is the reason the separation is this firm, and the dice case is the sharp
// end of it: an expression is a reader's input and a resolved total is not, but an
// error that interpolated the former would be one edit away from interpolating the
// latter through a wrapper that quotes what it received.
var (
	// ErrFrame is a roll request that could not be marshalled as a frame. Unreachable
	// for the value this package builds — every field is a string or an integer — and
	// still handled, because a plugin's wire format is not a place to assume.
	ErrFrame = fmt.Errorf("%w: the roll request could not be marshalled", errRoll)

	// ErrNoAnswer is a resolution that applied nothing and so carries no `applied`
	// frame. `plugin.answer`'s own comment says a resolution with no changes has no
	// version to report, and a zero version is not a version.
	ErrNoAnswer = fmt.Errorf("%w: the resolution carries no answer", errRoll)

	// ErrNotApplied is an answer that is not the `applied` frame. It is the shape a
	// mistake in the adapter would produce, and it is distinct from `ErrNoAnswer` for
	// the reason the wire vocabulary is a closed set: "there was nothing" and "there
	// was something else" have different fixes.
	ErrNotApplied = fmt.Errorf("%w: the answer is not an applied frame", errRoll)

	// ErrNotRefused is a refusal that carried no `rejected` frame. A refusal with no
	// `seq` to echo cannot be reported to a reader as a refusal of *their* roll.
	ErrNotRefused = fmt.Errorf("%w: the refusal carries no rejected frame", errRoll)
)

// errRoll is the umbrella sentinel, so a caller can ask "was this the roller's?"
// without naming four.
var errRoll = errors.New("dice: the graphical dice roller")

// Declaration is what this plugin adds to the interface.
//
// Two registrations and no observer. The **page type** is the widget and is the one
// thing here that is reachable: §10.6's "a new kind plus its route handler and templ
// component", declared in `Declaration` and mounted by `internal/httpapi/plugins`,
// which derives the mount from `webplugins.Registry` rather than from a literal.
//
// **No observer, deliberately.** An `Observer` renders a fragment the live chrome
// patches in, and the live chrome is phase 9's client. Registering one here would add
// a declaration whose output nothing can reach, and ADR 0042's reason for calling a
// page type unreachable until somebody mounts it applies just as much to an observer
// with no client to patch it.
//
// `Emits` is §10.6's "on send, emits `{"op":"roll"}`" in the only form the registry
// checks, and the check is not a convention: `webplugins.Registry.checkEmits` refuses
// the registration outright when no registered system resolves it.
func Declaration() webplugins.Declaration {
	return webplugins.Declaration{
		Name:  PluginName,
		Title: PluginTitle,
		PageTypes: []webplugins.PageType{{
			Kind:   Kind,
			Title:  PluginTitle,
			Path:   PagePath,
			Render: Widget,
		}},
		Emits: []rules.Op{OpRoll},
	}
}

// Request is one roll the widget asks for.
//
// Every field is a field the wire's `IntentArgs` already has, and that is not a
// coincidence to be tidy about: `realtime.Protocol`'s rule 2 is that every inbound
// payload is a **typed** struct rather than an opaque one, precisely so no field can
// carry a resolved outcome. A request whose fields are not the wire's fields would
// have to be encoded by this package, and an encoder is an interpreter.
type Request struct {
	// Seq is the client's sequence number, echoed in the answer.
	//
	// A server-rendered form is not a socket client and has no counter of its own, so
	// the value is whatever the form carried — one per page load. The discipline that
	// matters is not "increases" but "comes back": `realtime.ClientSeq` exists so a
	// client can match an answer to the intent that asked for it, and the only thing a
	// form can get wrong is answering the wrong roll.
	Seq realtime.ClientSeq

	// Placement is the token on the Table the roll is recorded against.
	//
	// `dnd5e`'s `resolveRoll` needs one: `rules.NewMutation` requires a target and a
	// roll names no object of its own, so the record travels on the creature whose
	// token every client already holds. Naming it is the reader's choice — it is the
	// same field a `move_token` intent carries — and the system's own role check is
	// what decides whether this reader may roll *that* creature.
	Placement realtime.PlacementID

	// Expression is the notation the reader asked for, e.g. `1d20+5`.
	Expression string

	// Reason is what the reader called the roll, shown in the recap.
	Reason string
}

// Frame returns the wire bytes a browser would put on the socket to ask for this
// roll.
//
// **Marshalled, then decoded.** The plugin emits bytes and the route parses them with
// `realtime.Decode`, so the request a plugin makes crosses the same codec, the same
// grammar allowlist and the same bounds a human player's keystroke crosses. Building
// the frame and handing the route a `*realtime.ClientIntent` directly would be fewer
// bytes and one boundary fewer, and it would mean the plugin's own idea of the wire
// was never checked by the wire's own codec — a plugin could emit an op the grammar
// rejects and find out at the first send.
func (request Request) Frame() ([]byte, error) {
	frame, err := json.Marshal(realtime.ClientIntent{
		Type: realtime.TypeIntent,
		Seq:  request.Seq,
		Op:   realtime.Op(OpRoll),
		Args: realtime.IntentArgs{
			Placement: request.Placement,
			Expr:      request.Expression,
			Reason:    request.Reason,
		},
	})
	if err != nil {
		// Content-free, and the wrap names the operation rather than the request: the
		// expression is a reader's input and this is one edit away from a total.
		return nil, fmt.Errorf("%w: marshalling a %s frame", ErrFrame, OpRoll)
	}

	return frame, nil
}

// Answer is what the server said about one roll, reduced to what a person may be told.
//
// Every field is a field of `realtime.ServerApplied`. **`Args` is deliberately
// absent**, and its absence is the package's argument: the resolved total is inside
// the system's opaque mutation payload, and a UI plugin that decoded it would be
// interpreting a gameplay system's private encoding (S-12.3, §10.6.1). What survives
// is what semiplane stamped itself.
type Answer struct {
	// Seq echoes the request's sequence number, and the assertion that it does is the
	// `seq` discipline reduced to something a page can be checked for.
	Seq realtime.ClientSeq

	// Placement is the token the roll was recorded against.
	Placement realtime.PlacementID

	// Version is **the version the hub assigned**. This is the field the whole package
	// exists to show: it is S-7.2's ordering authority, it is what makes the roll
	// auditable, and it is the thing a number invented by this process would not have.
	Version realtime.Version

	// Op is the operation that was applied.
	Op realtime.Op

	// By is the account the resolution was attributed to, which for a plugin's dispatch
	// is the account that loaded the page and nothing else.
	By realtime.UserID
}

// ReadAnswer reduces a resolution to the frame the hub answered a roll with.
//
// Refuses when there is nothing to read: a resolution with no changes answers with
// **no** frame at all (`plugin.answer` says an `applied` carrying a version of nothing
// would claim currency nobody established), and a refusal arrives as an error rather
// than as a resolution. A caller that wants the refusal asks `ReadRefusal`.
func ReadAnswer(resolution realtime.Resolution) (Answer, error) {
	if resolution.Answer == nil {
		return Answer{}, ErrNoAnswer
	}

	// The typed-nil case is checked rather than assumed: `ServerFrame` is an interface,
	// a nil `*ServerApplied` satisfies it, and reading a field off it would panic
	// inside somebody's HTTP handler.
	applied, isApplied := resolution.Answer.(*realtime.ServerApplied)
	if applied == nil || !isApplied {
		return Answer{}, ErrNotApplied
	}

	// Both halves of a frame's identity agreeing, for `protocol.go`'s reason: a `Type`
	// is a field, and a field can be forgotten.
	if applied.Type != realtime.TypeApplied {
		return Answer{}, ErrNotApplied
	}

	return Answer{
		Seq:       applied.Seq,
		Placement: applied.Placement,
		Version:   applied.Version,
		Op:        applied.Op,
		By:        applied.By,
	}, nil
}

// Refusal is what the server said about a roll it did not apply.
//
// Two fields and no detail, which is `plugin.RejectionError`'s split arriving here by
// a different route: `Reason` is one of the codec's eight closed words and goes on the
// page; the system's own explanation quotes what it choked on, which on this project
// is a dice expression or a `[!secret]` callout body, and is printed by nothing.
type Refusal struct {
	// Seq echoes the request's sequence number.
	Seq realtime.ClientSeq

	// Reason is one of `protocol.go`'s eight words.
	Reason realtime.RejectReason
}

// ReadRefusal recovers the reason a dispatch was refused for, and whether it was a
// refusal at all.
//
// **Two sources, and the second is the one that matters in production.** `Dispatch`
// surfaces a refusal through the resolver, and there are two shapes it can take:
//
//   - **A frame.** `realtime.Core` builds one, and `realtime.ResolutionFor` — the only
//     supported way to read it — recovers it. The refusal names the `seq` as well as the
//     reason, so the `seq` comes from the frame.
//   - **A `plugin.RejectionError`.** This is the shape ADR 0042 makes the production one,
//     because `plugin.Resolver` is the hub's resolver in a build with gameplay systems,
//     and its doc comment says a refusal is reported as "a `*Rejection` carrying the reason,
//     holding the cause" with **no frame** — a plugin's dispatch has no client frame
//     behind it, which is exactly the case `Dispatch` exists for.
//
// Reading only the first would make this return "no refusal" for every refusal a real
// build produces. `seq` is therefore a parameter: the frame carries it when there is one,
// and the caller's own value is the honest fallback when the reason came without a frame.
//
// **A refusal and a fault are different.** A wiring fault reaches here as an error with no
// reason and no frame, and the answer is `(Refusal{}, false)` — so a page that rendered
// "not your turn" for a resolver that crashed would be a lie about the campaign's rules,
// and one that disclosed the fault instead would be S-12.3's exact complaint in a browser.
func ReadRefusal(err error, seq realtime.ClientSeq) (Refusal, bool) {
	if err == nil {
		return Refusal{}, false
	}

	if resolution := realtime.ResolutionFor(err); resolution.Answer != nil {
		if rejected, isRejected := resolution.Answer.(*realtime.ServerRejected); rejected != nil &&
			isRejected {
			return Refusal{Seq: rejected.Seq, Reason: rejected.Reason}, true
		}
	}

	if rejection, isRejection := errors.AsType[*plugin.RejectionError](err); isRejection {
		return Refusal{Seq: seq, Reason: rejection.Reason}, true
	}

	return Refusal{}, false
}

// Outcome is the server's answer to the roll a page reports on, in whichever of the
// two shapes it arrived.
//
// Two pointers rather than a flag: "applied and refused are both absent" is the page
// before any roll, "applied and refused are both present" is not a state the hub can
// produce, and a `bool` plus two values would have to be reasoned about rather than
// checked.
type Outcome struct {
	// Applied is the `applied` frame, or nil when the server refused.
	Applied *Answer

	// Refused is the `rejected` frame, or nil when the server applied.
	Refused *Refusal
}

// WidgetView is the roller's page model.
//
// It is the plugin's own Go type carried in a `rules.Payload`, which is §10.6.1's
// mechanism for a renderer to be type-safe instead of a map traversal: the payload's
// `any` is unwrapped once, here, and the template below sees named fields.
type WidgetView struct {
	// Placement is the token the roll will be recorded against, echoed so a refused
	// send can be retried without retyping it.
	Placement string

	// Expression is the notation asked for, echoed for the same reason.
	Expression string

	// Reason is what the reader called the roll.
	Reason string

	// Seq is the sequence number the form will carry, for `Answer`'s and `Refusal`'s
	// `Seq` to echo.
	Seq uint64

	// Grammar is the campaign system's own notation, or its zero value when the
	// campaign declares no system this build can resolve (S-10.6's first row). The
	// page renders in both cases: §14 requires every declared view to render for an
	// empty output, and a campaign with no plugin is a state the product supports.
	Grammar rules.Grammar

	// Outcome is the server's answer, and the zero value is the page before any roll.
	Outcome Outcome
}

// ReadWidgetView unwraps a payload into the roller's page model, and whether it was one.
//
// The second half is what makes the "renders for an empty payload" rule testable
// rather than asserted: a test can hand `Widget` the zero payload, the wrong payload
// and no payload and require all three to produce a form.
func ReadWidgetView(payload rules.Payload) (WidgetView, bool) {
	view, isWidget := payload.Value().(WidgetView)

	return view, isWidget
}

// reasonCopy turns one of the codec's eight wire reasons into a sentence.
//
// **A mapping, not the word.** `not_your_turn` rendered as prose is a machine token on
// a page a person is reading, and §10.2 audits rendered documents — so the copy is
// written here and the word stays on `data-reason` where a test can read it. The
// default is the honest one: a reason this build does not have a sentence for is a
// reason to say nothing specific.
func reasonCopy(reason realtime.RejectReason) string {
	switch reason {
	case realtime.RejectNotYourTurn:
		return "it is not this reader's turn."
	case realtime.RejectNotPermitted:
		return "this reader may not make that roll."
	case realtime.RejectUnknownOp:
		return "this campaign's system does not resolve rolls."
	case realtime.RejectInvalidArgs:
		return "the expression was not one this campaign's system accepts."
	case realtime.RejectStaleVersion:
		return "the token moved while the roll was in flight."
	case realtime.RejectOutOfOrder:
		return "another change to that token came first."
	case realtime.RejectNoSuchPlacement:
		return "the Table has no token with that name."
	case realtime.RejectServerError:
		return "the Table could not record it."
	default:
		return "the Table did not record it, and would not say why."
	}
}

// Class returns the content-free class a log line should carry.
//
// `observability.errorClass` falls back to `%T`, which for these would be
// `*errors.errorString` and then the wrapped sentinel's own text — so the class is
// stated here rather than derived. The values are this package's words.
func Class(err error) string {
	switch {
	case errors.Is(err, ErrFrame):
		return "roll.frame_unbuildable"
	case errors.Is(err, ErrNoAnswer):
		return "roll.no_answer"
	case errors.Is(err, ErrNotApplied):
		return "roll.not_applied"
	case errors.Is(err, ErrNotRefused):
		return "roll.not_refused"
	default:
		return "roll"
	}
}
