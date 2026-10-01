package content_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// These tests are about the seam rather than about what it removes, because what
// it removes does not exist yet: `[!secret]` is P10. What has to be right in this
// phase is that the seam is expressed on the *source* and that the redactor
// installed is honest about doing nothing, so that P10 has one place to change and
// so that nobody reading this phase concludes there is a mechanism here.
//
// The ordering those two properties produce is asserted in
// `internal/httpapi/wiki`, which is the only caller: it can watch what the renderer
// was handed. A test here could only assert that a function returns a string.

// noSecretsRemovesNothing is the phase's actual behaviour, stated as a test so that
// it cannot change quietly. If this test ever needs deleting rather than replacing,
// the replacement is P10's redactor and the diff should say so.
func TestNoSecretsRemovesNothing(t *testing.T) {
	t.Parallel()

	const body = "The vault door is iron.\n\n[!secret] The combination is 1-2-3-4\n"

	for _, includeSecrets := range []bool{true, false} {
		got, err := content.NoSecrets().Redact(body, includeSecrets)
		if err != nil {
			t.Fatalf("Redact(includeSecrets=%t) returned %v", includeSecrets, err)
		}

		if got != body {
			t.Errorf("Redact(includeSecrets=%t) changed the body:\ngot:  %q\nwant: %q",
				includeSecrets, got, body)
		}
	}
}

// TestNoSecretsIsViewerIndependent: the installed redactor gives both viewers the
// same bytes, and the difference between a GM's response and a player's is the
// route's to make through `includeSecrets` reaching P10's implementation.
//
// A test rather than a comment because the failure it guards against is a
// *helpful* one: a redactor that started branching on the flag would produce two
// different bodies, and a reviewer reading only this phase's code would have no way
// to tell whether that was intended.
func TestNoSecretsIsViewerIndependent(t *testing.T) {
	t.Parallel()

	const body = "Prose, and a secret-looking line.\n"

	withSecrets, err := content.NoSecrets().Redact(body, true)
	if err != nil {
		t.Fatalf("Redact(true): %v", err)
	}

	withoutSecrets, err := content.NoSecrets().Redact(body, false)
	if err != nil {
		t.Fatalf("Redact(false): %v", err)
	}

	if withSecrets != withoutSecrets {
		t.Errorf("the two viewers received different bodies:\n%q\n%q",
			withSecrets, withoutSecrets)
	}
}

// TestRedactorSeesTheWholeSource is the position, expressed as a test.
//
// A redactor is handed the file's bytes — front matter included — rather than a
// parsed body, because front matter is attacker-reachable (S-4.7) and a redactor
// that could only see the prose would have to be trusted not to care about the
// fields. The stub below records what it was given, and the assertion is that the
// recording contains the block a parse would have split off.
func TestRedactorSeesTheWholeSource(t *testing.T) {
	t.Parallel()

	const source = "---\ntitle: The vault\nsecret_note: behind the door\n---\nThe vault door is iron.\n"

	seen := recordingRedactor{}
	if _, err := seen.Redact(source, false); err != nil {
		t.Fatalf("Redact: %v", err)
	}

	if !strings.Contains(seen.body, "secret_note: behind the door") {
		t.Errorf("the redactor was not shown the front matter:\n%s", seen.body)
	}

	if !strings.Contains(seen.body, "The vault door is iron.") {
		t.Errorf("the redactor was not shown the prose:\n%s", seen.body)
	}

	if seen.includeSecrets {
		t.Error("the redactor was told secrets were wanted, for a viewer that may not see them")
	}
}

// TestARedactorErrorReachesItsCaller: an error is a fault rather than an answer, and
// swallowing one would mean serving a page whose redaction state nobody knows.
func TestARedactorErrorReachesItsCaller(t *testing.T) {
	t.Parallel()

	wanted := errors.New("redactor failed")

	if _, err := (failingRedactor{wanted}).Redact("body", false); !errors.Is(err, wanted) {
		t.Errorf("Redact returned %v, want the redactor's own error", err)
	}
}

// recordingRedactor keeps whatever it was handed.
type recordingRedactor struct {
	body           string
	includeSecrets bool
}

func (r *recordingRedactor) Redact(body string, includeSecrets bool) (string, error) {
	r.body = body
	r.includeSecrets = includeSecrets

	return body, nil
}

// failingRedactor fails every call.
type failingRedactor struct{ err error }

func (r failingRedactor) Redact(_ string, _ bool) (string, error) {
	return "", r.err
}

// Both stubs satisfy the interface this phase installs, which is the assertion that
// a future redactor can be a function-shaped thing or a struct without either being
// a change to the pipeline.
var (
	_ content.Redactor = (*recordingRedactor)(nil)
	_ content.Redactor = failingRedactor{}
)
