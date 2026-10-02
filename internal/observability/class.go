// The error-class contract: how an error becomes the short word in `Detail`.
//
// # Why this is a file of its own
//
// It used to live at the bottom of `watch.go`, which was true until the protocol
// codec arrived and made the thing it does obviously wrong. `protocol.FrameError`
// carries a `Class()` method for a stated reason: `errorClass` reduced an error to
// its *dynamic type*, so every refusal the codec can produce — "a client sent
// nonsense", "a client sent a field outside the grammar", "a client sent 40MB" —
// arrived as the single useless class `*realtime.FrameError`. An alert cannot
// match that, and the three cases call for three different operator responses.
//
// The fix is not to teach `ErrorClass` about `realtime`. It is the reverse: an error
// that knows its own class says so, and this file asks. That is what `Classed` is,
// and the difference matters more than the feature.
//
// # The three ways to be wrong here, and what each costs
//
//   - **String matching** the error text is the obvious move and it is forbidden
//     twice over. It is brittle against rewording, which every prose message in a
//     Go program eventually gets; and it is a **content leak**: the text is exactly
//     what S-12.3 forbids, because a Markdown or YAML parser quotes the line it
//     choked on and on a wiki page that line is routinely a `[!secret]` callout
//     body. A matcher would have to be written knowing the messages.
//   - **A switch over concrete types** in this package is the second obvious move and
//     it is the same failure as a chain: every package that adds an error has to
//     come here and be added, and the one that does not is the one whose errors all
//     read as `*pkg.MyError`. It also inverts the dependency — `observability` would
//     import every subsystem it reports on, and a package that cannot import its
//     reporters cannot report on them.
//   - **A registry** — `observability.RegisterClass(func(error) string)` — fixes the
//     chain's edit requirement and introduces a worse one: registration order
//     becomes behaviour, `init()` leaks state between tests, and a subsystem nobody
//     remembered to wire reports nothing. It is also the shape AGENTS.md forbids
//     ("Registration happens in the composition root, never in `init()`" — and a
//     package-level registry *is* that, with the ordering hidden).
//
// `Classed` is the remaining answer and it is worth being precise about why it
// cannot rot: the interface is satisfied by the error's own type, so a new error
// type in any package is classified the moment somebody adds the method, with no
// edit here, no registration, and no import. The failure mode is a developer
// forgetting to add the method, and that is the *visible* failure — a dynamic-type
// class, exactly what this function already does for errors with no class.
//
// # Order, and why the class wins
//
// `Classed` is consulted **first**, before the `fs` sentinels. A type that declares
// its own class is the most specific statement available, and the sentinel table is
// a fallback for errors that have no opinion. Nothing in the tree that matches an
// `fs` sentinel also implements `Classed`, so the order is not currently load
// bearing — which is exactly why it is written down rather than left to whichever
// case arrives first.
//
// # What a class is and is not
//
// It is a **stable identifier** chosen by the type that implements the interface: an
// alert matches it, so changing one is a silent break for every dashboard built on
// it, and a class that is a sentence is not an identifier. It is *not* the error's
// text and never a prefix of it. `realtime.FrameError.Class` already documents that
// its values are content-free identifiers chosen for exactly this purpose, and the
// contract is that an implementer owes the same.

package observability

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"syscall"
)

// Classed is an error that knows the short, content-free name it should be logged
// under.
//
// Implementing it is how a package teaches this package about its errors: the type
// carries the answer, so `ErrorClass` needs no knowledge of the type and no import
// of the package that declared it. A type may implement it through an `Unwrap`
// chain — an error that wraps a classified one is classified by it — which is what
// makes a class survive the wrapping every call site adds with `%w`.
//
// # The contract on an implementation
//
//   - The returned string is an **identifier**, not a sentence, and it is **stable**:
//     an alert may match it, so a rename is a break in the same way an event name's
//     is. `observability.EventName` is the same discipline applied to a name.
//   - It must be **content-free**: no error text, no path, no page body, no
//     attacker-chosen value. `ErrNotExist` is a class; "open /vault/x.md: no such
//     file" is not, and on a wiki page the difference is a secret.
//
// The embedded `error` is required because `errors.AsType` constrains its type
// parameter to `error`, and because an interface satisfied only by accident is not
// a contract.
type Classed interface {
	error

	// Class returns the short identifier this error is logged under.
	Class() string
}

// ErrorClass reduces an error to the short class name that belongs in `Detail`.
//
// It never returns `err.Error()`. S-12.3 forbids an event carrying file contents,
// and the error text is how they arrive: a Markdown parser quotes the line it failed
// on, a YAML parser quotes the line it could not parse, and on a wiki page either of
// those lines is routinely the body of a `[!secret]` callout. A path is dropped for
// the same reason — an `*fs.PathError` repeats it, and `Path` already carries it —
// so what is left is the part an operator can act on.
//
// Exported because the classification is a contract other packages document, and
// because a caller that wants to log an error *correctly* from outside this package
// needs it. The alternative is for the next package to reach for `err.Error()`.
//
// The order is: a declared class, then the `fs` and context sentinels, then the
// errnos, then the dynamic type. Each step is less specific than the one before it.
// The final fallback is a type name, which is an identifier and so cannot carry
// content — an error this build has never heard of is still classifiable, and the
// only thing it loses is the distinction a `Classed` implementer would have given.
func ErrorClass(err error) string {
	if err == nil {
		// No error, no detail. A fabricated class here would read as a fact about a
		// failure that did not happen, which is the one thing `Detail` must never
		// be.
		return ""
	}

	// First, and deliberately: a type that states its own class is the most
	// specific answer available, and `errors.AsType` walks the chain, so a wrapper
	// keeps the class of the error it wraps.
	if classed, ok := errors.AsType[Classed](err); ok {
		if class := classed.Class(); class != "" {
			return class
		}
	}

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	case errors.Is(err, fs.ErrExist):
		return "EEXIST"
	case errors.Is(err, fs.ErrInvalid):
		return "EINVAL"
	case errors.Is(err, fs.ErrClosed):
		return "EBADF"
	case errors.Is(err, errors.ErrUnsupported):
		return "ENOTSUP"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}

	// No `fs` sentinel covers ENOSPC or the descriptor errnos, and those are the
	// ones the watcher fails with. This is the branch that turns "too many open
	// files" into something an alert can match.
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return errnoClass(errno)
	}

	return fmt.Sprintf("%T", err)
}

// errnoClass names the errnos this project's named failure modes arrive as, and
// reports every other errno as its number.
//
// The two named ones are S-4.5's exhausted watch limit and S-4.4's refused
// symlink, because those are the two an operator reads the specification for.
// Everything else is a number rather than a sentence because `Detail` is a
// discriminator and the kernel's prose for an errno ("input/output error", "invalid
// cross-device link") is a sentence.
func errnoClass(errno syscall.Errno) string {
	switch errno {
	case syscall.EMFILE, syscall.ENFILE:
		// Per-process and system-wide descriptor exhaustion, collapsed to one
		// class: the operator's next action — raise the limit, or watch fewer
		// trees — is the same either way, and splitting them buys a distinction
		// nobody has ever fixed a problem with.
		return "watch_limit"
	case syscall.ELOOP:
		// S-4.4's rejection arriving through the kernel rather than through the
		// root's own walk.
		return "ELOOP"
	default:
		return "errno_" + strconv.Itoa(int(errno))
	}
}
