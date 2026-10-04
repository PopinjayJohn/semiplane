package observability_test

// The payload contract: what a log line may carry, and what happens when the
// content it must not carry tries anyway.
//
// # Why this file exists next to the others
//
// S-12.3 — no event carries secret content, file contents, or dice results — is
// enforced by **the absence of a place to put them**, and the tests that assert
// the absence of a place are in `observability_test.go`. The three files that
// already cover the *values* (`watch_test.go`, `index_test.go`,
// `class_test.go`) all build their marker by hand: a constant (`contentMarker`,
// `secretMarker`) pasted into a string that looks like a page.
//
// # Why a hand-written marker is the wrong instrument, and what it cost two work
// items in this phase
//
// A hand-written marker is only as good as the assertion that the string really
// is a secret, and every marker in this package's existing tests is a bare
// literal. That leaves two ways for the assertion to be unfalsifiable, and
// **both happened to sibling work items in this phase**:
//
//   - **A false positive**: the word also occurs somewhere unrelated — the
//     shell's own inline script, another fixture, a document — so a test
//     asserting its *absence* can never go red, and one asserting its presence
//     goes red for a reason that has nothing to do with redaction.
//   - **A pass for the wrong reason**: the word was never secret at all, so its
//     absence from a log line proves nothing about the pipeline.
//
// The fixture below fixes both by **deriving the needle instead of declaring
// it**. The page is a real Obsidian `[!secret]-` callout; the needle is the
// longest word of that callout's body that occurs exactly once in the whole
// page; and `content.ScanSecrets` plus `content.OmitSecrets` — the production
// scanner and the production redactor — establish that it is a secret, rather
// than this file's opinion that it is one. Each of those steps is an assertion
// in `TestTheNeedleIsASecretAndNothingElse`, so a fixture that stops being
// distinctive fails the test instead of silently weakening it.
//
// # What is left to this file, then
//
// Four claims the existing tests cannot see:
//
//  1. **The door itself.** `Event`'s own signature takes an `EventAttributes` and
//     nothing else. The structural claim holds only for as long as the function
//     that consumes the struct cannot be handed something else, and a
//     `...any` or a `...slog.Attr` on it would restore everything the struct
//     excludes.
//  2. **The shape of a decoded value, not only its text.** `assertNoMarker` in
//     `watch_test.go` asserts on attributes whose decoded value is a `string` and
//     skips everything else — which is exactly what `slog.Any("detail",
//     someStruct)` produces, because the JSON handler renders it as a nested
//     object rather than a string. A page body in a struct is invisible to a
//     string-only assertion, so the walk here descends into maps and slices and
//     the key set is checked to be closed.
//  3. **The fallback classification.** `ErrorClass`'s last resort is `%T`, and
//     AGENTS.md records the complaint about it: a wrapped error with no `Class()`
//     is classified `*fmt.wrapError`, which is the one string an alert cannot
//     match. That is still true and is **reported, not fixed and not frozen** —
//     see `TestErrorClassFallsBackToATypeNameAndNeverToTheErrorsText`.
//  4. **The other two typed surfaces.** `watch_test.go` holds `Watch`'s method
//     set to a shape; `Index` and `Writes` have no equivalent, and two of the
//     four emitters in this package were audited by nobody.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/observability"
)

// secretFixture is the page the payload tests are measured against.
//
// **A real callout in the real grammar**, which is the point: `> [!secret]-` and
// a two-line quoted body, so `content.ScanSecrets` finds it and
// `content.OmitSecrets` cuts it. The prose around it exists so the page has
// content on both sides of the callout — a fixture whose only content is the
// secret cannot tell "redaction removed the callout" from "the page was empty".
//
// The invented name in the body is the needle, and it is asserted unique in the
// repository by `TestTheNeedleIsASecretAndNothingElse` rather than by this
// comment being true: a word that also occurs in another file is a word an
// absence assertion cannot falsify.
const secretFixture = `---
title: The Duke's Ledger
---

The eastern signal fire was replaced on the eve of the feast, and the order
authorising it was signed in a hand nobody could later identify.

> [!secret]- The clerk heard exactly one name spoken aloud and wrote it down.
> Nobody was told. The name was Voranthril, and it is written nowhere else.

The rest of the page carries on, and names nobody at all.
`

// needleScanRoots are the trees a second occurrence of the needle would hide in.
//
// `internal` holds the shipped browser assets, the rendered markup and every
// other test fixture in the repository; `demo-vault` is a set of documents, and a
// document is exactly what the trap this file guards against put a stray word
// into; `docs/content` is the last tree holding prose rather than build output.
var needleScanRoots = []string{"../../internal", "../../demo-vault", "../../docs/content"}

// needleScanSkipDirs are the directory names never descended into, each with the
// reason, because an unexplained skip inside an absence assertion is a hole.
var needleScanSkipDirs = map[string]string{
	"vendor": "committed third-party bytes; nobody chose a word in them",
	"dist":   "build output, and absent on a fresh checkout",
}

// fixtureFile is this file, the one place the needle is *declared*: every other
// occurrence of it would be a collision, so the scan skips exactly this path and
// nothing else.
var fixtureFile = filepath.ToSlash(filepath.Join("internal", "observability", "payload_test.go"))

// theNeedle is the fixture's callout together with the word derived from its
// body, computed on first use.
//
// A `sync.OnceValue` rather than an `init()` because AGENTS.md forbids `init()`
// outright: it hides ordering and leaks state between tests. A plain package-level
// `var` holding the answer would be the same smell wearing a different hat, so
// this is a function value with the derivation beside it and nothing runs until a
// test asks.
var theNeedle = sync.OnceValue(deriveTheNeedle)

// derivedNeedle is what `deriveTheNeedle` returns: the word, and the callout it
// came from.
//
// Both halves travel together because the two assertions that use them are about
// the same relationship — the word must occur exactly once **inside that
// callout's body span** — and passing them separately is how a test ends up
// checking one against the other's fixture.
type derivedNeedle struct {
	// needle is the word, or empty when the fixture holds no distinctive one.
	needle string

	// secret is the callout the word was taken from.
	secret content.Secret
}

// deriveTheNeedle derives the needle from the fixture's callout.
//
// **The longest word of the callout body that occurs exactly once in the page.**
// Uniqueness is what makes an absence assertion falsifiable, and length is what
// makes the derived word a *name* rather than a function word — determinism comes
// from `FieldsFunc` preserving order and from ties resolving to the first.
//
// The needle is empty when the fixture holds no distinctive word, and the tests
// that use it assert on that rather than ignoring it: a fixture with no unique
// word is a fixture whose assertions cannot fail.
func deriveTheNeedle() derivedNeedle {
	found := content.ScanSecrets(secretFixture)
	if len(found) != 1 {
		return derivedNeedle{}
	}

	secret := found[0]

	var longest string

	// Splitting on the non-word characters rather than on whitespace is what
	// makes the needle a word and not a word plus its comma: a needle of
	// `Voranthril,` would not be found in a line carrying `Voranthril`, so the
	// assertion would pass on a leak.
	for _, word := range strings.FieldsFunc(secret.Body, isNotWordCharacter) {
		if strings.Count(secretFixture, word) != 1 {
			continue
		}

		if len(word) > len(longest) {
			longest = word
		}
	}

	return derivedNeedle{needle: longest, secret: secret}
}

// isNotWordCharacter is the separator predicate `strings.FieldsFunc` wants: a
// letter or a digit is part of a word, and everything else — including the space
// and the comma a callout body ends its sentences with — is not.
func isNotWordCharacter(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// TestTheNeedleIsASecretAndNothingElse proves the fixture's word is a secret, so
// the tests that assert its absence from a log line are asserting something.
//
// # The four properties, and what each one is for
//
//  1. **It occurs exactly once in the page, and inside the callout's body span.**
//     A word occurring twice would let the redactor leave one occurrence and the
//     assertion still pass; a word occurring outside the body would mean the
//     redaction is not what removes it.
//  2. **The production redactor removes it.** `content.OmitSecrets` cutting the
//     needle out of the page is what makes it a secret rather than a word in a
//     fixture. A hand-written marker has no such witness, which is the whole
//     reason this file exists.
//  3. **The production redactor keeps everything else.** If redaction had cut the
//     whole page, property 2 would hold for the wrong reason.
//  4. **Nothing else in the repository contains it.** This is the false-positive
//     guard, and the one two sibling work items in this phase needed and did not
//     have.
func TestTheNeedleIsASecretAndNothingElse(t *testing.T) {
	t.Parallel()

	details := theNeedle()
	needle, secret := details.needle, details.secret

	if needle == "" {
		t.Fatalf("the fixture's callout body holds no word unique to the page:\n%s",
			secretFixture)
	}

	if secret.State.IsRevealed() {
		t.Fatalf("the fixture's callout is revealed (%q); a revealed callout is "+
			"public, and a word in it is not a secret", secret.State)
	}

	if got := strings.Count(secretFixture, needle); got != 1 {
		t.Fatalf("the needle %q occurs %d times in the fixture, want exactly 1: an "+
			"occurrence outside the callout would let the redactor leave it and the "+
			"assertion still pass", needle, got)
	}

	at := strings.Index(secretFixture, needle)
	if at < secret.BodyStart || at >= secret.BodyEnd {
		t.Fatalf("the needle %q is at byte %d, outside the callout body's [%d,%d); "+
			"it must come from the secret and nowhere else", needle, at,
			secret.BodyStart, secret.BodyEnd)
	}

	redacted := redactedFor(t, false)

	if strings.Contains(redacted, needle) {
		t.Fatalf("the needle %q survives redaction: the word this file measures is "+
			"not one the redactor removes, so its absence from a log line proves "+
			"nothing", needle)
	}

	// The control for the assertion above, and why it is not enough alone: a
	// redactor that dropped the whole page would satisfy "the needle is absent"
	// too.
	const outsideTheCallout = "The rest of the page carries on"
	if !strings.Contains(redacted, outsideTheCallout) {
		t.Fatalf("redaction removed the prose around the callout as well:\n%s", redacted)
	}

	if shown := redactedFor(t, true); !strings.Contains(shown, needle) {
		t.Fatalf("the GM's copy of the page does not contain the needle %q, so the "+
			"redacted copy's missing it proves nothing about redaction", needle)
	}

	assertTheNeedleIsUniqueInTheRepository(t, needle)
}

// redactedFor runs the production redactor over the fixture and fails the test if
// it errors: a redactor that returned an error would mean the precondition above
// was never established at all.
func redactedFor(t *testing.T, includeSecrets bool) string {
	t.Helper()

	redacted, err := content.OmitSecrets().Redact(secretFixture, includeSecrets)
	if err != nil {
		t.Fatalf("OmitSecrets().Redact(includeSecrets=%t) errored: %v", includeSecrets, err)
	}

	return redacted
}

// assertTheNeedleIsUniqueInTheRepository is the false-positive guard, and the
// reason it walks files rather than trusting a comment.
//
// Two work items in this phase shipped a needle that also occurred somewhere
// unrelated, which is the shape of an absence assertion that cannot fail. The
// walk is a few hundred files and one string search each; the only assertion in
// this file that notices the *fixture* drifting rather than the code.
func assertTheNeedleIsUniqueInTheRepository(t *testing.T, needle string) {
	t.Helper()

	for _, root := range needleScanRoots {
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if entry.IsDir() {
				if reason, skipped := needleScanSkipDirs[entry.Name()]; skipped {
					t.Logf("not descending into %s: %s", path, reason)

					return filepath.SkipDir
				}

				return nil
			}

			// This file is where the needle is written down, so its one occurrence
			// is the fixture and not a collision.
			if strings.HasSuffix(filepath.ToSlash(path), fixtureFile) {
				return nil
			}

			bytes, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("reading %s: %w", path, readErr)
			}

			if strings.Contains(string(bytes), needle) {
				t.Errorf("%s also contains the needle %q; a word that occurs in two "+
					"places makes \"no log line carries it\" a coincidence rather than "+
					"a property of the code", path, needle)
			}

			return nil
		})
		if walkErr != nil {
			t.Fatalf("walking %s: %v", root, walkErr)
		}
	}
}

// TestEventAttributesAndItsDoorHaveNoFieldAPageBodyCouldReach is S-12.3's
// structural claim, asserted on the type and on the function that consumes it.
//
// The claim is that the enforcement is *structural*: `EventAttributes` has no
// field a page body could be passed through, so a leak is not something a
// reviewer has to notice. That is a claim about a **type**, and it holds only
// while two things do:
//
//   - Every field is a scalar. `any` accepts anything; `[]byte` is a page;
//     `map[string]any` is the free-form context bag the field set was written to
//     exclude; `error` carries the text a parser quoted off the page. A *named*
//     type over a scalar is fine — that is a discriminator with a vocabulary, not
//     a bag — which is why the check is on the underlying kind and not on the
//     spelling.
//   - **The door takes the struct.** A `...slog.Attr` or an `any` parameter on
//     `observability.Event` restores every one of those exclusions while leaving
//     the struct untouched. `TestWatchMethodsCannotAcceptContent` in
//     `watch_test.go` holds the same line for the typed emitters; `Event` is
//     exported and reachable from every package in the tree, so nothing holds it
//     but this.
func TestEventAttributesAndItsDoorHaveNoFieldAPageBodyCouldReach(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[observability.EventAttributes]()

	if typ.Kind() != reflect.Struct {
		t.Fatalf("EventAttributes is a %s, want a struct: the claim under test is "+
			"about its fields", typ.Kind())
	}

	for field := range typ.Fields() {
		if !field.IsExported() {
			t.Errorf("EventAttributes.%s is unexported; a private field is still a "+
				"field a page body could be passed through, and a private bag is the "+
				"one nobody reviews", field.Name)
		}

		if isScalarKind(field.Type.Kind()) {
			continue
		}

		t.Errorf("EventAttributes.%s is %s: %s. S-12.3 is enforced by this type "+
			"having no field to pass a body through, so adding one is what breaks it",
			field.Name, field.Type, whyAContentCarrier(field.Type))
	}

	assertEventTakesOnlyAnEventAttributes(t)
}

// isScalarKind reports whether a kind is one a log attribute may carry.
//
// Deliberately a short list rather than "anything that is not a container". A
// float or a complex number cannot carry a page body, but nothing in this package
// needs one, and rejecting it makes the allowlist a decision rather than an
// accident — so a future field that wants one has to argue for it against a test
// that says no, rather than inheriting a permissive rule.
func isScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

// whyAContentCarrier names, for a rejected field type, the specific way it would
// become a leak. A generic "not a scalar" is a message a reader skims past; the
// point of naming the shape is that the reader recognises the thing they were
// about to add.
func whyAContentCarrier(typ reflect.Type) string {
	switch {
	case typ == reflect.TypeFor[any]():
		return "`any` accepts a page body, a dice result, or a rendered fragment"
	case typ == reflect.TypeFor[error]():
		return "an error carries its text, and a parser's text is the source line it " +
			"choked on"
	case typ == reflect.TypeFor[slog.Attr](), typ == reflect.TypeFor[slog.Value]():
		return "a slog value is a free-form bag by construction"
	case typ.Kind() == reflect.Interface:
		return "an interface is a bag with no shape"
	case typ.Kind() == reflect.Slice, typ.Kind() == reflect.Array:
		return "a slice or an array is unbounded in practice, and []byte is a page"
	case typ.Kind() == reflect.Map:
		return "a map is the free-form context bag this field set exists to exclude"
	case typ.Kind() == reflect.Pointer:
		return "a pointer is a way to grow this field later without changing it"
	case typ.Kind() == reflect.Struct:
		return "a struct hides a field of any shape one level down"
	case typ.Kind() == reflect.Func, typ.Kind() == reflect.Chan:
		return "it is not a value a log line can carry at all"
	default:
		return "it is not a scalar, and the invariant is carried by the type's shape"
	}
}

// assertEventTakesOnlyAnEventAttributes holds `observability.Event`'s signature
// to the struct, because it is exported and therefore reachable from anywhere —
// including from the call-site `slog.Any("detail", …)` that ADR 0032 names as the
// thing this package was written to stop.
func assertEventTakesOnlyAnEventAttributes(t *testing.T) {
	t.Helper()

	// `TypeOf` and not `TypeFor`: `Event` is a function value, and
	// `TypeFor` takes a type. A func type is what comes back either way,
	// which is the point — the signature is the claim.
	fn := reflect.TypeOf(observability.Event)

	if fn.IsVariadic() {
		t.Errorf("observability.Event is variadic (%v): a variadic tail is a free-form "+
			"bag, which is the one shape EventAttributes exists to exclude", fn)
	}

	want := []reflect.Type{
		reflect.TypeFor[context.Context](),
		reflect.TypeFor[*slog.Logger](),
		reflect.TypeFor[observability.EventName](),
		reflect.TypeFor[slog.Level](),
		reflect.TypeFor[observability.EventAttributes](),
	}

	if fn.NumIn() != len(want) {
		t.Fatalf("observability.Event takes %d parameters, want %d (%v): a parameter "+
			"added to this function is a way to carry content past the struct",
			fn.NumIn(), len(want), fn)
	}

	for index, wantType := range want {
		if got := fn.In(index); got != wantType {
			t.Errorf("observability.Event parameter %d is %s, want %s", index, got, wantType)
		}
	}

	if got := fn.NumOut(); got != 0 {
		t.Errorf("observability.Event returns %d values, want 0", got)
	}
}

// TestNoEmittedLineCarriesASecretCalloutsText is S-12.3 at the only point where
// a leak actually happens: an error handed to an emitter.
//
// # What is fed in, and why these shapes
//
// The error channel is the vector, and the shapes are the ones a caller really
// produces: a Markdown parser's error quoting the source line it choked on, a
// document error quoting the whole page, a `%w` wrap of either, an `*fs.PathError`
// whose `Path` **is** the callout body, an `*os.LinkError` repeating it twice, an
// `errors.Join` burying it beside a real sentinel, and a `Classed` error whose own
// class is innocent and whose cause is not. Every one of them puts the secret in
// the position most likely to be passed through, and redaction cannot help from
// there: the bytes have already been read, and the log line is a copy made after
// that.
//
// # What is asserted, and the two ways it could pass for the wrong reason
//
// A line is captured through a real `slog` JSON handler over a buffer, so what is
// searched is the bytes an aggregator would receive, and the decoded record is
// walked too — including nested objects, which a string-only assertion skips
// entirely.
//
// Two guards keep it from passing vacuously. Every error's **own text, and every
// error in its unwrap chain**, is asserted to contain the needle, so the fixture
// cannot be one where the secret never reached the call site; and the number of
// emitted records is asserted against the number of emitters called, so a signal
// that stopped logging is a failure rather than an absence.
func TestNoEmittedLineCarriesASecretCalloutsText(t *testing.T) {
	t.Parallel()

	details := theNeedle()
	needle, secret := details.needle, details.secret

	// A Markdown parser quoting the callout body is the leak AGENTS.md names:
	// the line it choked on is, on a wiki page, routinely a secret.
	quotedBody := fmt.Errorf("render %s: %q: unexpected callout marker",
		"towns/duke.md", secret.Body)

	testCases := map[string]error{
		"a parser quoting the callout body": quotedBody,
		"a parser quoting the whole page": fmt.Errorf("render %s: %q: bad document",
			"towns/duke.md", secretFixture),
		"the caller's %w wrap": fmt.Errorf("index page: %w", quotedBody),
		"a path error whose path is the body": &fs.PathError{
			Op:   "open",
			Path: secret.Body,
			Err:  fs.ErrNotExist,
		},
		"a link error repeating the body twice": &os.LinkError{
			Op:  "rename",
			Old: secret.Body,
			New: secret.Body,
			// **`Err` is set, and that is load bearing rather than tidiness.**
			// `os.LinkError.Error` dereferences it unconditionally, so a LinkError
			// with no cause panics the moment anything prints it — including the
			// precondition below. Found here, by the panic, on the first run.
			Err: fs.ErrNotExist,
		},
		"a join burying it beside a sentinel": errors.Join(quotedBody, fs.ErrNotExist),
		"a classified error whose cause is the body": &classifiedError{
			class: "front_matter_unreadable",
			cause: quotedBody,
		},
	}

	for name, err := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The precondition that makes the assertion below falsifiable: the
			// secret really was inside the error, so its absence from the line is
			// the pipeline's doing and not the fixture's.
			//
			// **The whole chain, not `err.Error()`.** A `%w` wrapper renders its
			// cause, but nothing obliges a type to: `classifiedError` in
			// `class_test.go` prints its own class and omits the cause entirely,
			// which is the correct discipline for a content-free type and still
			// leaves a body one level down. Searching only the top-level text
			// would have declared that case incapable of leaking, on the evidence
			// of a rendering choice rather than of the pipeline.
			chain := errorChainText(err)
			if !strings.Contains(chain, needle) {
				t.Fatalf("the fixture error's text does not contain the needle %q, "+
					"so this case cannot demonstrate a leak:\n%s", needle, chain)
			}

			logger, buf := capture()

			watch := observability.NewWatch(observability.NewRegistry(), logger)
			index := observability.NewIndex(observability.NewRegistry(), logger)
			writes := observability.NewWrites(observability.NewRegistry(), logger)

			ctx := t.Context()

			// Every typed emitter that takes an error, because a claim about "the
			// emitters" that covers only the ones the author remembered is a claim
			// about the ones the author remembered.
			emitters := []struct {
				event observability.EventName
				emit  func()
			}{
				{
					event: observability.EventWatchAddFailed,
					emit:  func() { watch.AddFailed(ctx, "greyhaven", "towns/duke.md", err) },
				},
				{
					event: observability.EventContentSettleFailed,
					emit:  func() { watch.SettleFailed(ctx, "greyhaven", "towns/duke.md", err) },
				},
				{
					event: observability.EventContentRenderError,
					emit:  func() { watch.RenderError(ctx, "greyhaven", "towns/duke.md", err) },
				},
				{
					event: observability.EventIndexChangeFailed,
					emit: func() {
						index.ChangeFailed(ctx, "greyhaven", "upsert", "towns/duke.md", err)
					},
				},
				{
					event: observability.EventIndexPageSkipped,
					emit:  func() { index.PageSkipped(ctx, "greyhaven", "towns/duke.md", err) },
				},
				{
					event: observability.EventIndexPageDegraded,
					emit:  func() { index.PageDegraded(ctx, "greyhaven", "towns/duke.md", err) },
				},
				{
					event: observability.EventIndexRenameSourceLeft,
					emit: func() {
						index.RenameSourceLeft(ctx, "greyhaven", "towns/duke.md", 2, err)
					},
				},
				{
					event: observability.EventStateWriteMs,
					emit:  func() { writes.Record(ctx, 7, time.Millisecond, err) },
				},
			}

			for _, emitter := range emitters {
				emitter.emit()
			}

			parsed := records(t, buf)

			// **`Errorf`, not `Fatalf`, and the reason is a mutation.** This count
			// exists to catch a signal that stopped logging, and a fatal here would
			// mask the leak assertion for the one mistake it most needs to catch: a
			// raw `slog.Any` at a call site emits an *extra* line beside the typed
			// one, so the count is wrong **and** the secret is on the buffer — and
			// stopping at the count reports only the half that is not the claim.
			// Reporting both is what makes this test's failure say what is wrong.
			if len(parsed) != len(emitters) {
				t.Errorf("emitted %d records for %d emitters; a signal that stopped "+
					"logging, or a call site logging beside the typed emitter, is "+
					"visible here and nowhere else", len(parsed), len(emitters))
			}

			if strings.Contains(buf.String(), needle) {
				t.Errorf("an emitted line carries the secret %q:\n%s", needle, buf.String())
			}

			for at, parsedRecord := range parsed {
				where := "record[" + strconv.Itoa(at) + "]"

				leaves := assertNoNeedle(t, needle, where, map[string]any(parsedRecord))

				// The walk reported how much it looked at, and the caller checks it.
				// A type switch matches the **exact** dynamic type, so passing the
				// named `record` this package parses into — rather than
				// `map[string]any` — walks nothing and reports success; every
				// assertion in this file would then be about an empty value. Two
				// leaves is the floor: `event` and `campaign_id`.
				if leaves < 2 {
					t.Errorf("%s: the walk inspected %d values, want at least 2; a "+
						"record nobody read is a record that cannot leak and cannot "+
						"prove anything either", where, leaves)
				}

				if event, ok := parsedRecord["event"].(string); !ok || event == "" {
					t.Errorf("%s carries no `event` key, so the line cannot be queried "+
						"and the walk above is reading an unattributable record", where)
				}
			}
		})
	}
}

// assertNoNeedle walks a decoded JSON value and fails on the needle anywhere in
// it, **including inside a nested object or array**. It returns the number of
// scalar leaves it inspected.
//
// The descent is the part that matters. `slog.Any("detail", attrs.Detail)` where
// the value is a struct reaches a JSON handler as an object, and an assertion that
// type-asserts each value to `string` skips it silently — the one shape a page
// body takes when a caller wraps it in a type of their own.
//
// The count is what makes the walk itself falsifiable. A type switch matches the
// **exact** dynamic type, so handing it the named `record` type this package
// parses into — rather than a `map[string]any` — matches neither case and walks
// nothing while reporting success. The caller below converts before calling for
// that reason, and checks the count so the mistake cannot come back.
func assertNoNeedle(t *testing.T, needle, where string, value any) int {
	t.Helper()

	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, needle) {
			t.Errorf("%s carries the secret %q: %q", where, needle, typed)
		}

		return 1
	case map[string]any:
		seen := 0

		for key, nested := range typed {
			seen += assertNoNeedle(t, needle, where+"."+key, nested)
		}

		return seen
	case []any:
		seen := 0

		for at, nested := range typed {
			seen += assertNoNeedle(t, needle, where+"["+strconv.Itoa(at)+"]", nested)
		}

		return seen
	default:
		return 0
	}
}

// errorChainText renders an error and everything it unwraps to, one per line.
//
// Both unwrap shapes are followed, and the multi-error one is followed to **all**
// of its elements: `errors.Join`'s `Unwrap` returns a slice, so a traversal that
// only knew the singular form would stop at the first element — which is exactly
// the shape that hides the second half of a body.
//
// Rendering the whole chain is what makes it a usable *precondition*. A type is
// under no obligation to print its cause: `classifiedError` in `class_test.go`
// prints only its own class, which is the right discipline and leaves a page body
// one level down. An assertion that read only the top-level text would call that
// case incapable of leaking, on the evidence of a formatting choice rather than
// of anything the pipeline does.
func errorChainText(err error) string {
	var out strings.Builder

	writeErrorChain(&out, err)

	return out.String()
}

// writeErrorChain appends one error and its causes, recursing through either
// unwrap shape.
func writeErrorChain(out *strings.Builder, err error) {
	if err == nil {
		return
	}

	if out.Len() > 0 {
		out.WriteByte('\n')
	}

	out.WriteString(err.Error())

	// `errors.As` rather than a type switch, for two reasons. The linter is right
	// that a type switch on an error misses a wrapped one; and the interface
	// targets are how the two shapes are told apart without naming a single
	// concrete type — `errors.Join` implements only `Unwrap() []error` and a
	// `*fs.PathError` only `Unwrap() error`, so exactly one of the two matches.
	var (
		joined interface{ Unwrap() []error }
		single interface{ Unwrap() error }
	)

	switch {
	case errors.As(err, &joined):
		for _, inner := range joined.Unwrap() {
			writeErrorChain(out, inner)
		}
	case errors.As(err, &single):
		writeErrorChain(out, single.Unwrap())
	}
}

// TestEveryEmittedAttributeIsAScalarUnderAClosedSetOfKeys is the other half of
// "no line carries a body", and it is a claim about the **shape** of a decoded
// value rather than about its text.
//
// Two holes close here, and both are silent:
//
//   - **A non-scalar value.** `assertNoMarker` in `watch_test.go` skips any
//     attribute whose decoded value is not a `string`, and the JSON handler
//     renders `slog.Any("detail", someStruct)` as an object. A body wrapped in a
//     struct is therefore invisible to a text assertion, which is why
//     `assertNoNeedle` descends and why this test rejects the shape outright.
//   - **An unexpected key.** Nothing checks that the key set is closed, so
//     `slog.Any("body", raw)` at a call site adds a key nobody is watching and
//     every spelling test still passes — `TestEventOmitsEmptyAttributes` checks
//     three keys, and the spelling tests check the values of keys they name.
//
// The `fieldKeys` table is the mechanism rather than a convenience: `Event` builds
// its record by hand, so the mapping from a field to a key is a convention, and a
// convention with no assertion is how a ninth key appears unnoticed. Its length is
// asserted against the struct's field count, so a field cannot be added without
// deciding the key it is emitted under, and every declared key is asserted to
// appear on a real line, so a key cannot be declared and never emitted.
func TestEveryEmittedAttributeIsAScalarUnderAClosedSetOfKeys(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[observability.EventAttributes]()

	if got, want := len(fieldKeys), typ.NumField(); got != want {
		t.Fatalf("fieldKeys declares %d keys for %d EventAttributes fields; add the "+
			"missing one, because the key set is closed and an undeclared key is an "+
			"attribute no dashboard is watching", got, want)
	}

	for name := range fieldKeys {
		if _, present := typ.FieldByName(name); !present {
			t.Errorf("fieldKeys declares %q, which is not an EventAttributes field", name)
		}
	}

	logger, buf := capture()

	watch := observability.NewWatch(observability.NewRegistry(), logger)
	index := observability.NewIndex(observability.NewRegistry(), logger)

	ctx := t.Context()

	// Three calls chosen to fill every declared field between them: `Renamed` is
	// the only emitter carrying `from`, `to` and `count`, `ChangeFailed` the one
	// carrying `op`, and both carry `path`, `detail` and `campaign_id`.
	index.Renamed(ctx, "3", "notes/old", "notes/new", 7)
	index.ChangeFailed(ctx, "3", "upsert", "notes/page.md",
		&classifiedError{class: "row_write_failed"})
	watch.RenderError(ctx, "3", "notes/page.md", fs.ErrNotExist)

	parsed := records(t, buf)
	if len(parsed) != 3 {
		t.Fatalf("emitted %d records, want 3", len(parsed))
	}

	// slog's own keys are the handler's, not this package's; every other key has
	// to be one the struct declares.
	handlerKeys := map[string]struct{}{"time": {}, "level": {}, "msg": {}}

	declaredKeys := map[string]struct{}{"event": {}}
	for _, key := range fieldKeys {
		declaredKeys[key] = struct{}{}
	}

	observed := map[string]struct{}{}

	for at, parsedRecord := range parsed {
		for key, value := range parsedRecord {
			if _, ours := handlerKeys[key]; !ours {
				if _, declared := declaredKeys[key]; !declared {
					t.Errorf("record %d carries %q, which no EventAttributes field "+
						"declares; S-12.3's type has no room for it", at, key)
				}
			}

			observed[key] = struct{}{}

			// A scalar, and nothing else. `float64` is what the JSON decoder makes
			// of `slog.Int`; `bool` is here so that adding one is a deliberate choice
			// rather than something the shape check happens to permit.
			switch value.(type) {
			case string, float64, bool:
			default:
				t.Errorf("record %d attribute %q is a %T, want a scalar: a nested value "+
					"is a way to carry a page body that a text assertion cannot see",
					at, key, value)
			}
		}
	}

	for key := range declaredKeys {
		if _, present := observed[key]; !present {
			t.Errorf("no emitted line carried %q; a declared key that is never emitted "+
				"is a field that exists for nothing", key)
		}
	}
}

// fieldKeys maps each `EventAttributes` field to the `slog` key `Event` emits it
// under.
var fieldKeys = map[string]string{
	"CampaignID": "campaign_id",
	"Path":       "path",
	"Detail":     "detail",
	"Op":         "op",
	"From":       "from",
	"To":         "to",
	"Count":      "count",
}

// TestErrorClassFallsBackToATypeNameAndNeverToTheErrorsText is the last branch of
// the classifier, and the one AGENTS.md has a paragraph about.
//
// # The finding: reported, not fixed, and not frozen
//
// `ErrorClass`'s final fallback is `fmt.Sprintf("%T", err)`. An error that wraps
// another with `%w` and adds no `Class()` of its own arrives as `*fmt.wrapError`,
// which **is** the string an alert cannot match: it is not a package's name, it is
// the standard library's, and it describes the wrapping rather than the failure.
// AGENTS.md records this as measured — a boot that refused to resume reported
// `realtime.ruleset_unreadable … class:"*fmt.wrapError"`, which is the one thing
// an alert cannot match. It is still true today, and the fix belongs to whoever
// owns `class.go`.
//
// So this test deliberately does **not** assert `*fmt.wrapError`. Freezing it
// would make the eventual fix a failing test, which is how a known defect becomes
// permanent. Asserting only the good half would let a fallback that returned the
// message pass. What is asserted is the property that makes the defect merely
// useless rather than a leak: **whatever the fallback is, it is a type name and
// never the error's text.** A parser quotes the line it choked on, and on a wiki
// page that line is routinely a callout body, so this is the difference between an
// alert nobody can match and a secret in a log aggregator.
//
// The wrapped shape is included because that is the one AGENTS.md measured, and it
// is the shape a caller produces by writing the obvious
// `fmt.Errorf("read page: %w", err)`.
func TestErrorClassFallsBackToATypeNameAndNeverToTheErrorsText(t *testing.T) {
	t.Parallel()

	details := theNeedle()
	needle, secret := details.needle, details.secret

	if needle == "" {
		t.Fatalf("the fixture's callout body holds no word unique to the page:\n%s",
			secretFixture)
	}

	quotedBody := fmt.Errorf("render %s: %q: unexpected callout marker",
		"towns/duke.md", secret.Body)

	// Each has the secret in its text and has no class, no `fs` sentinel and no
	// errno behind it, so every step of `ErrorClass`'s ladder falls through to the
	// fallback. The wrapped one is the shape AGENTS.md measured.
	testCases := map[string]error{
		"a wrapped error with no class":   quotedBody,
		"an opaque error with no class":   errors.New(quotedBody.Error()),
		"an error whose text is the page": errors.New(secretFixture),
	}

	for name, err := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			class := observability.ErrorClass(err)

			if class == "" {
				t.Fatalf("ErrorClass() = %q; an empty detail on a line that has an "+
					"error reads as a line that does not", class)
			}

			for _, forbidden := range []string{
				needle,
				secret.Body,
				"towns/duke.md",
				"unexpected callout marker",
			} {
				if strings.Contains(class, forbidden) {
					t.Errorf("ErrorClass() = %q, which carries %q: the fallback must be "+
						"a type name, because the error text is how a page body arrives "+
						"(S-12.3)", class, forbidden)
				}
			}

			// What makes the fallback an identifier rather than a sentence: it is a
			// qualified type name. Whether it is the *useful* type is the finding
			// above; that it is a type at all is the safety property, and it is what
			// keeps `Detail` from becoming a content channel.
			if !strings.Contains(class, ".") {
				t.Errorf("ErrorClass() = %q, want a qualified type name: the fallback is "+
					"%%T and something has replaced it", class)
			}
		})
	}
}

// TestTheIndexAndWriteSurfacesStillRejectContentShapes holds two of this
// package's four typed surfaces to the rule `watch_test.go` applies to `Watch`:
// every parameter an emitter takes is a context, a string, an integer or an
// `error`.
//
// It is here because every assertion in this file is only as strong as the
// emitters it reaches, and `TestWatchMethodsCannotAcceptContent` says nothing
// about `Index` or `Writes`. `Writes.Record` matters for a second reason — its
// `Detail` is a latency bucket on success and an error class on failure, so it is
// the one emitter whose `detail` is not a caller's string at all.
//
// A `[]byte`, a `map[string]any` or a variadic `...slog.Attr` is a way to hand this
// package a page body, and adding one is what should break this test.
//
// # Emitters and readers are declared separately, and the union is closed
//
// The first version of this test demanded that *every* method be a signal — no
// return value, context first — and `Writes.Snapshot` failed it, correctly:
// `Snapshot` renders the histogram and the failure gauge for `/readyz` and is
// neither an emitter nor shaped like one. So each surface declares its emitters
// and its readers, and **every method must be one of the two**. That is the part
// worth having: a method added later is not silently unaudited, it is a name the
// test refuses to recognise, which is the same rule
// `TestWatchMethodsCannotAcceptContent` applies to `Watch`.
func TestTheIndexAndWriteSurfacesStillRejectContentShapes(t *testing.T) {
	t.Parallel()

	errType := reflect.TypeFor[error]()
	ctxType := reflect.TypeFor[context.Context]()

	testCases := map[string]struct {
		surface  reflect.Type
		emitters map[string]struct{}
		readers  map[string]struct{}
	}{
		"Index": {
			surface: reflect.TypeFor[*observability.Index](),
			emitters: map[string]struct{}{
				"ChangeFailed":     {},
				"PageSkipped":      {},
				"PageDegraded":     {},
				"RenameSourceLeft": {},
				"Renamed":          {},
			},
			readers: map[string]struct{}{},
		},
		"Writes": {
			surface:  reflect.TypeFor[*observability.Writes](),
			emitters: map[string]struct{}{"Record": {}},
			// `Snapshot` is what `/readyz` renders. A reader, so it may return a
			// value and takes no context; it still may not take a bag.
			readers: map[string]struct{}{"Snapshot": {}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Copied per subtest because the loop deletes from them, and a map shared
			// between two subtests would report the second as missing everything the
			// first consumed.
			emitters := copyNameSet(tc.emitters)
			readers := copyNameSet(tc.readers)

			for method := range tc.surface.Methods() {
				_, isEmitter := emitters[method.Name]
				_, isReader := readers[method.Name]

				if !isEmitter && !isReader {
					t.Errorf("%s has an unexpected method %q; add it to this test as an "+
						"emitter or a reader, because a method nobody classified is a "+
						"method nobody audited", name, method.Name)

					continue
				}

				delete(emitters, method.Name)
				delete(readers, method.Name)

				fn := method.Func.Type()

				if isReader {
					assertReaderShape(t, name, method.Name, fn, ctxType)

					continue
				}

				assertEmitterShape(t, name, method.Name, fn, ctxType, errType)
			}

			for missing := range emitters {
				t.Errorf("%s is missing the emitter %q", name, missing)
			}

			for missing := range readers {
				t.Errorf("%s is missing the reader %q", name, missing)
			}
		})
	}
}

// assertEmitterShape holds a signal to the shape S-12.1's events have: it returns
// nothing, it is scoped by a context, and everything after that is a
// discriminator.
func assertEmitterShape(
	t *testing.T,
	surface, method string,
	fn reflect.Type,
	ctxType, errType reflect.Type,
) {
	t.Helper()

	if got := fn.NumOut(); got != 0 {
		t.Errorf("%s.%s returns %d values; a signal returns nothing", surface, method, got)
	}

	if fn.NumIn() < 2 {
		t.Errorf("%s.%s takes %d parameters, want a receiver and a context",
			surface, method, fn.NumIn())

		return
	}

	if fn.In(1) != ctxType {
		t.Errorf("%s.%s parameter 1 is %s, want context.Context",
			surface, method, fn.In(1))
	}

	for arg := 2; arg < fn.NumIn(); arg++ {
		if isASignalParameter(fn.In(arg), errType) {
			continue
		}

		t.Errorf("%s.%s parameter %d is %s; a parameter that is not a context, a "+
			"string, an integer or an error is a way to carry a page body (S-12.3)",
			surface, method, arg, fn.In(arg))
	}
}

// assertReaderShape holds a reader to the same rule, minus the parts that do not
// apply: it may return a value, and it takes a context only if it has parameters
// at all.
func assertReaderShape(t *testing.T, surface, method string, fn, ctxType reflect.Type) {
	t.Helper()

	for arg := 1; arg < fn.NumIn(); arg++ {
		param := fn.In(arg)

		if param == ctxType || isScalarKind(param.Kind()) {
			continue
		}

		t.Errorf("%s.%s parameter %d is %s; a reader takes a context or a "+
			"discriminator, and a bag here would end up in the /readyz body",
			surface, method, arg, param)
	}
}

// copyNameSet returns a fresh set, so a subtest's deletions cannot reach the
// table the next subtest reads.
func copyNameSet(names map[string]struct{}) map[string]struct{} {
	copied := make(map[string]struct{}, len(names))
	for name := range names {
		copied[name] = struct{}{}
	}

	return copied
}

// isASignalParameter reports whether a signal's parameter type may carry a
// discriminator: a string, an integer, or an error. `time.Duration` is an integer
// kind, which is why `Writes.Record`'s duration passes without being named here.
func isASignalParameter(typ, errType reflect.Type) bool {
	return typ == errType || typ.Kind() == reflect.String || isScalarKind(typ.Kind())
}
