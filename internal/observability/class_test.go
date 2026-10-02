// The tests for the error-class contract.
//
// # What is being claimed
//
// That a *distinct* error yields a *distinct, meaningful* class. Three words, each
// load bearing:
//
//   - **Distinct**, because the failure this fixes is every protocol refusal
//     collapsing to `*realtime.FrameError`.
//   - **Non-empty**, because a class of `""` reads as "there was no error" on a line
//     that has one, which is worse than a useless class.
//   - **Meaningful**, because the alternative to a meaningful class is a package name,
//     and an alert cannot match `*errors.errorString` any more than it can match
//     `*realtime.FrameError`.
//
// # Why the cases are reached through a method rather than by calling `ErrorClass`
//
// `ErrorClass` is exported, so calling it directly is possible, and every assertion
// here goes through `Watch.RenderError` instead. The reason is that the claim is
// about **the log line**: a class that `ErrorClass` computes correctly and that
// nothing puts on the line is not a classification any operator benefits from.
// Asserting the attribute is what makes the claim about the signal, and it is also
// the only way an `slog` key that got renamed or dropped would be caught.

package observability_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/semiplane/semiplane/internal/observability"
)

// classifiedError is an error that knows its own class, which is the whole
// mechanism: a type asserting it, and no registration, no import, and no edit to
// `observability` when it is added somewhere else entirely.
type classifiedError struct {
	class string
	cause error
}

func (e *classifiedError) Error() string { return "something went wrong: " + e.class }
func (e *classifiedError) Unwrap() error { return e.cause }
func (e *classifiedError) Class() string { return e.class }

// sentinelClassError is a classified error that is ALSO an `fs` sentinel, which is
// the only shape that observes the *order* the two are consulted in.
//
// Real, and the reason the order is documented rather than left to whichever case
// arrives first: a package's own "no such page" error almost always wraps
// `fs.ErrNotExist` so that its own callers can use `errors.Is` on the sentinel. With
// the sentinels consulted first, that package's carefully chosen class never reaches
// a log line and every one of its errors reads as the errno it wraps.
type sentinelClassError struct {
	class string
}

func (e *sentinelClassError) Error() string { return "no such page" }
func (*sentinelClassError) Unwrap() error   { return fs.ErrNotExist }
func (e *sentinelClassError) Class() string { return e.class }

// silentClassError declares the interface and returns nothing, which is the case
// `ErrorClass` has to fall through rather than log an empty class for.
type silentClassError struct{}

func (*silentClassError) Error() string { return "no class to give" }
func (*silentClassError) Class() string { return "" }

// TestErrorClassAsksTheErrorItself is the mechanism, and it is what makes the fix
// not a chain.
func TestErrorClassAsksTheErrorItself(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a class declared on the error",
			err:  &classifiedError{class: "too_large"},
			want: "too_large",
		},
		{
			name: "a different class, so the two are distinguishable",
			err:  &classifiedError{class: "bad_token"},
			want: "bad_token",
		},
		{
			name: "a classified error wrapped by a caller with %w",
			err:  fmt.Errorf("resolve the intent: %w", &classifiedError{class: "unknown_field"}),
			want: "unknown_field",
		},
		{
			name: "a classified error wrapped twice, which is what every call site adds",
			err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w",
				&classifiedError{class: "counter_range"})),
			want: "counter_range",
		},
		{
			name: "a classified error inside a filesystem error's own wrapper",
			err: &fs.PathError{
				Op:   "open",
				Path: "/vault/towns/duke.md",
				Err:  &classifiedError{class: "ENOENT"},
			},
			want: "ENOENT",
		},
		{
			name: "a classified error that is also an fs sentinel, so the order is observable",
			err:  &sentinelClassError{class: "no_such_page"},
			want: "no_such_page",
		},
		{
			name: "an empty class falls through rather than logging nothing",
			err:  &silentClassError{},
			want: "*observability_test.silentClassError",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch, rec := newWatch(t)
			watch.RenderError(t.Context(), "greyhaven", "towns/duke.md", tc.err)

			record := only(t, rec)

			if got := record["detail"]; got != tc.want {
				t.Errorf("detail = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestErrorClassDistinguishesTwoErrorsOfTheSameShape is the brief's claim, stated
// directly: two different concrete errors, two different non-empty meaningful
// classes.
//
// Without this, a `Classed` implementation that returned a constant would satisfy
// every other test in this file.
func TestErrorClassDistinguishesTwoErrorsOfTheSameShape(t *testing.T) {
	t.Parallel()

	first := &classifiedError{class: "too_large"}
	second := &classifiedError{class: "malformed"}

	firstClass := observability.ErrorClass(first)
	secondClass := observability.ErrorClass(second)

	if firstClass == secondClass {
		t.Errorf("both errors classified as %q, want two distinct classes", firstClass)
	}

	for name, class := range map[string]string{"first": firstClass, "second": secondClass} {
		switch {
		case class == "":
			t.Errorf("%s error classified as %q, want a class: an empty detail on a line "+
				"that has an error reads as a line that does not", name, class)
		case class == fmt.Sprintf("%T", first):
			t.Errorf("%s error classified as %q, want the error's own class rather than its "+
				"dynamic type; a package name is not something an alert can match", name, class)
		}
	}
}

// TestErrorClassIsStableAcrossCalls is the property the first test above could not
// see, and it is what makes the class an identifier rather than a coincidence.
//
// A classifier that derived a class from something volatile — a map iteration, a
// counter — would satisfy every other test in this file and then produce a different
// string for the same error on the next process, which is a log query that stops
// matching without anybody changing anything.
func TestErrorClassIsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	err := &classifiedError{class: "unknown_type", cause: errors.New("inner")}

	first := observability.ErrorClass(err)

	for range 5 {
		if got := observability.ErrorClass(err); got != first {
			t.Fatalf("ErrorClass() = %q then %q, want it stable: a class is an identifier and "+
				"an alert matches it", first, got)
		}
	}
}

// TestErrorClassStillReducesTheSentinels is the regression guard on the ordering
// change.
//
// `Classed` is consulted first, so this asserts the table underneath it is intact:
// removing the sentinel branch would make a missing file read as a dynamic type, and
// a wrapped `syscall` read as `*fs.PathError` — both content-free and both useless.
func TestErrorClassStillReducesTheSentinels(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{"missing", fs.ErrNotExist, "ENOENT"},
		{"permission", fs.ErrPermission, "EACCES"},
		{"unsupported", errors.ErrUnsupported, "ENOTSUP"},
		{"deadline", context.DeadlineExceeded, "deadline_exceeded"},
		{"canceled", context.Canceled, "canceled"},
		{"watch limit", syscall.EMFILE, "watch_limit"},
		{"symlink loop", syscall.ELOOP, "ELOOP"},
		{
			"an errno behind a path error",
			&fs.PathError{Op: "open", Path: "/x", Err: syscall.EMFILE},
			"watch_limit",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := observability.ErrorClass(tc.err); got != tc.want {
				t.Errorf("ErrorClass() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestErrorClassNeverReturnsTheErrorText is S-12.3 at the point it is made, for a
// `Classed` error as much as for a parser's.
//
// A `Classed` implementer controls the string, so this is the assertion that the
// interface has not become a hole: a class that quoted the error's text would be a
// content leak with a type assertion in front of it.
func TestErrorClassNeverReturnsTheErrorText(t *testing.T) {
	t.Parallel()

	// An error whose *text* is a secret-shaped string and whose class is not.
	secret := "[!secret] The passphrase is hunter2"
	leaky := &classifiedError{class: "parse_failed"}
	leaky.cause = errors.New(secret)

	got := observability.ErrorClass(leaky)

	if got != "parse_failed" {
		t.Errorf("ErrorClass() = %q, want %q", got, "parse_failed")
	}

	for _, forbidden := range []string{secret, "hunter2", "passphrase"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("ErrorClass() = %q, want it to carry no part of the error's text (S-12.3): "+
				"it contains %q", got, forbidden)
		}
	}
}

// TestErrorClassOfANilErrorIsEmpty keeps the one case that is not about an error.
//
// A fabricated class on a nil error would read as a fact about a failure that did not
// happen, and `Event` omits an empty `Detail` entirely, so this is also the assertion
// that no `detail` key appears at all.
func TestErrorClassOfANilErrorIsEmpty(t *testing.T) {
	t.Parallel()

	if got := observability.ErrorClass(nil); got != "" {
		t.Errorf("ErrorClass(nil) = %q, want %q", got, "")
	}

	watch, rec := newWatch(t)
	watch.RenderError(t.Context(), "greyhaven", "towns/duke.md", nil)

	if detail, present := only(t, rec)["detail"]; present {
		t.Errorf("detail = %v, want the attribute absent for a nil error", detail)
	}
}
