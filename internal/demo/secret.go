package demo

import (
	"crypto/rand"
	"fmt"
	"io"
)

// Secret is a value that must reach exactly one place — the operator's terminal,
// once — and nowhere else.
//
// **The redaction is on the type, which is what makes the rule structural rather than a
// promise.** `Secret` implements both `fmt.Stringer` and `fmt.Formatter`, so a
// `slog.Any("password", secret)`, a `%v` inside an error message, a `%+v` in a debug
// dump, a `%#v` Go-syntax dump and a `fmt.Sprintf("%d", secret)` all render
// `[redacted]`. `Reveal` is the only accessor, so "print it once" becomes a decision
// somebody has to make at one call site rather than a default every call site inherits.
//
// The alternative — a plain `string` field plus a discipline — is the failure this
// repository keeps meeting: a password that reached a log aggregator is a credential
// in a place the operator cannot revoke it from, and no linter in `.golangci.yml`
// can see it happening.
type Secret struct {
	value string
}

// NewSecret wraps a value. Named rather than left to a field literal so the zero
// value's meaning is unambiguous: a `Secret{}` is the empty string and renders
// redacted like any other, which is the safe direction.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the value, and **every** caller of it is a decision to disclose.
func (s Secret) Reveal() string { return s.value }

// String renders the redaction.
//
// A `Stringer`, which `fmt` consults for `%v`, `%s`, `%q`, `%x` and `%X` — **but not for
// `%#v` or `%d`**, and both of those render an unexported field's value. That is why
// `Format` exists beside this: a version of this type with only the method below leaked the
// password through `fmt.Sprintf("%#v", secret)`, and
// `TestTheSecretRendersRedactedUnderEveryFormattingVerb` is what found it.
func (s Secret) String() string { return redacted }

// Format writes the redaction for **every** verb, including the ones `fmt` does not route
// through a `Stringer`.
//
// The recursion guard is `redacted`, a package-level constant, rather than `s`: printing `s`
// from inside `Format` would call `Format` again, and a `Stringer`'s output going through
// `Fprintf` on the same state is the one way to write this that does not terminate.
//
// `%q` gets a quoted redaction so the shape of the verb is preserved — a caller formatting a
// secret into a JSON string gets `"[redacted]"`, which is honest about what happened.
func (s Secret) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(state, "%q", redacted)

		return
	}

	fmt.Fprint(state, redacted)
}

// redacted is what every rendering of a `Secret` produces.
//
// A constant rather than `Secret{}.String()` for the reason `Format` gives: `String` is a method
// on the type and `Format` is called for it, so routing through either would recurse.
const redacted = "[redacted]"

// Empty reports whether there is no value, which is what tells the command to draw
// one rather than to echo the operator's.
func (s Secret) Empty() bool { return s.value == "" }

// passwordBytes is how much entropy a generated demo password carries.
//
// Twenty-four characters drawn from a 57-character alphabet — past any
// human-typable length and short enough to retype from a terminal, which are the two
// things a demo credential has to balance.
//
// **Drawn one character at a time with rejection sampling**, not as bytes mapped with a
// plain modulo. 256 is not a multiple of 57, so a modulo makes the first 13 characters
// of the alphabet measurably more likely than the rest: a real and entirely unnecessary
// weakness in the one value this command mints. Values at or above `256 - (256 % 57)`
// are discarded and redrawn, which costs nothing and makes every character exactly
// equiprobable.
const passwordBytes = 24

// passwordAlphabet is what a generated password is drawn from.
//
// **57 characters, and every one of them unambiguous**: no `0`/`O` pair and no
// `1`/`l`/`I` triple. A demo credential is read off a terminal by a person comparing
// it against a browser field, and one ambiguous character becomes a failed sign-in that
// reads as a bug in the demo rather than as a typo.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ErrNoEntropy is a draw that produced nothing, which is reported rather than turned
// into an empty password — an account with an empty password is an account anyone can
// claim, and `auth.HashPassword` would refuse it anyway, further down and with a
// message about the password rather than about the entropy source.
var ErrNoEntropy = fmt.Errorf("%w: could not read entropy for the demo password", errDemo)

// GeneratePassword draws one demo password from reader.
//
// **`crypto/rand.Reader` in production, and that is the deliberate exception to this
// repository's usual injected reader.** AGENTS.md's ban is on *ambient* randomness in
// rule code, where a resolution must be a function of `(state, intent, seed)` — see ADR
// 0044. A password's entire property is that nothing predicts it, so a seeded or ambient
// source would make it worthless, and the ban does not reach here because
// `internal/demo` is not rule code and `forbidigo`'s patterns are scoped to
// `internal/domain/rules` and `internal/domain/systems` alone.
//
// A parameter rather than a call buried in the body, so a test can assert the alphabet,
// the length and the uniformity property without drawing from the system pool, and
// because there is then no way to reach a third source — the shape `realtime`'s
// `Entropy` field and `auth`'s `hashPassword` parameter both exist to guarantee.
//
// `io.ReadFull` rather than a single `Read`, because a short read is legal for any
// reader and a password truncated to whatever one call returned is a password with less
// entropy than the operator was told about.
func GeneratePassword(reader io.Reader) (Secret, error) {
	if reader == nil {
		reader = rand.Reader
	}

	// 256 mod 57 is 13, so 224 is the first value that must be rejected.
	ceiling := byte(256 - (256 % len(passwordAlphabet)))

	drawn := make([]byte, 0, passwordBytes)

	for len(drawn) < passwordBytes {
		raw := make([]byte, passwordBytes)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return Secret{}, fmt.Errorf("%w: %w", ErrNoEntropy, err)
		}

		for _, value := range raw {
			if value >= ceiling {
				continue
			}

			drawn = append(drawn, passwordAlphabet[int(value)%len(passwordAlphabet)])

			if len(drawn) == passwordBytes {
				break
			}
		}
	}

	return NewSecret(string(drawn)), nil
}
