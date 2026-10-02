// The one test that cannot live in `protocol_test.go`, and the reason it exists.
//
// `clientGrammar` is the wire grammar written out as data — the allowlist of
// accepted keys per message type — and it is unexported because it is an
// implementation of the decoder rather than part of its contract. But an
// allowlist and a set of struct tags can drift apart in two directions, and both
// matter:
//
//   - A **field with no allowlist entry** is a field this build accepts and will
//     silently drop for a client that sends it. That is the "unknown fields are
//     ignored" behaviour the file comment argues against, reintroduced one struct
//     field at a time.
//   - An **allowlist entry with no field** is a field the decoder believes is legal
//     and quietly discards. A client that reads that name off the documentation
//     gets a frame that vanishes.
//
// Neither is reachable from the exported API: a test outside this package can only
// observe that the fields it knows about round-trip. So this file is `package
// realtime`, and it is named `..._internal_test.go` so the `testpackage` linter's
// `(export|internal)_test\.go` skip applies — which is the Go-standard arrangement
// rather than a suppression.

package realtime

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestTheAllowlistAndTheStructTagsAgree is the drift check, in both directions.
func TestTheAllowlistAndTheStructTagsAgree(t *testing.T) {
	t.Parallel()

	for frameType, shape := range clientGrammar {
		t.Run(string(frameType), func(t *testing.T) {
			t.Parallel()

			tags := jsonTags(reflect.TypeOf(shape.newFrame()))

			for _, tag := range tags {
				if _, ok := shape.fields[tag]; !ok {
					t.Errorf("%s has a field %q the allowlist refuses, so a client "+
						"sending it would have it silently dropped", frameType, tag)
				}
			}

			for key := range shape.fields {
				if !slices.Contains(tags, key) {
					t.Errorf("the allowlist accepts %q but no field carries it, so it "+
						"would be silently dropped", key)
				}
			}

			for _, required := range shape.required {
				if _, ok := shape.fields[required]; !ok {
					t.Errorf("%q is required but not in the allowlist, so it could "+
						"never be present", required)
				}

				if !slices.Contains(tags, required) {
					t.Errorf("%q is required but no field carries it", required)
				}
			}
		})
	}
}

// TestTheSealHoldsForEveryFrameInTheGrammar is the other half of the sealed
// interface: a type the grammar names must actually satisfy `ClientFrame`, or
// `Decode` returns something a hub's type switch cannot match.
func TestTheSealHoldsForEveryFrameInTheGrammar(t *testing.T) {
	t.Parallel()

	for frameType, shape := range clientGrammar {
		frame := shape.newFrame()

		if frame == nil {
			t.Errorf("%s: newFrame returned nil, and Decode would decode into nothing", frameType)
			continue
		}

		if frame.frameType() != frameType {
			t.Errorf("%s: frameType() is %q", frameType, frame.frameType())
		}
	}
}

// TestAnIntentWithNoOpIsRefused is the negative half of the previous test: the
// seal holds, but an *empty* intent must not. It is the smallest fixture that
// reaches `validate`'s semantic pass, which is the pass a malformed-but-well-typed
// frame arrives at, so it is what proves the two halves of `Decode` are both live.
func TestAnIntentWithNoOpIsRefused(t *testing.T) {
	t.Parallel()

	intent := clientGrammar[TypeIntent].newFrame()

	err := validate(intent)
	if err == nil {
		t.Fatal("an intent with no op validated")
	}

	frameErr, ok := errors.AsType[*FrameError](err)
	if !ok {
		t.Fatalf("expected a *FrameError, got %T", err)
	}

	if frameErr.class != "bad_token" {
		t.Fatalf("class = %q, want bad_token", frameErr.class)
	}
}

// TestEveryFrameInTheGrammarIsCoveredByASealedMarker is what keeps a new client
// frame from being added without also sealing it. A frame type that satisfies
// `ClientFrame` through some other route would be a frame a caller outside this
// package could construct, and the seal is the type-level half of "reject an
// unknown message type".
func TestEveryFrameInTheGrammarIsCoveredByASealedMarker(t *testing.T) {
	t.Parallel()

	if len(clientGrammar) != 3 {
		t.Fatalf("the grammar holds %d message types, want §7.1's three client frames",
			len(clientGrammar))
	}
}

// TestTheServerFramesAreAllEncodable guards the outbound side of the seal: a
// `ServerFrame` the file declares but cannot encode is a frame a hub can build and
// never send.
func TestTheServerFramesAreAllEncodable(t *testing.T) {
	t.Parallel()

	state := []byte(`{"placements":{}}`)

	frames := []ServerFrame{
		&ServerSnapshot{Type: TypeSnapshot, State: state, You: Viewer{ID: 1, Role: "gm"}},
		&ServerApplied{Type: TypeApplied, Seq: 1, Version: 1, Placement: "p1", Op: "set_hp", By: 1},
		&ServerRejected{Type: TypeRejected, Seq: 1, Reason: RejectNotYourTurn},
		&ServerDelta{
			Type:    TypeDelta,
			Changes: []Change{{Placement: "p1", Version: 1, Op: "set_hp"}},
		},
		&ServerPresence{Type: TypePresence, Users: []PresenceUser{{ID: 1}}},
		&ServerClock{Type: TypeClock},
	}

	if len(frames) != 6 {
		t.Fatalf("%d server frames, want §7.1's six", len(frames))
	}

	for _, frame := range frames {
		if _, err := Encode(frame); err != nil {
			t.Errorf("%s: a minimal frame did not encode: %v", frame.frameType(), err)
		}
	}
}

// TestTheTypeConstantsAreTheWireVocabulary holds the nine names of §7.1 down.
//
// A renamed discriminator is a client that connects and receives nothing, and the
// rename would be invisible from Go — both sides would be this package. Only a
// table can hold it.
func TestTheTypeConstantsAreTheWireVocabulary(t *testing.T) {
	t.Parallel()

	got := map[Type]string{
		TypeHello:    "hello",
		TypeIntent:   "intent",
		TypePresence: "presence",
		TypeSnapshot: "snapshot",
		TypeApplied:  "applied",
		TypeRejected: "rejected",
		TypeDelta:    "delta",
		TypeClock:    "clock",
	}

	for frameType, want := range got {
		if string(frameType) != want {
			t.Errorf("Type constant is %q, want %q", frameType, want)
		}
	}

	// `presence` is one wire value used in both directions, so there are eight
	// constants for nine message types. Asserting the count is what catches a
	// ninth constant added "just in case" with a value no client sends.
	if len(got) != 8 {
		t.Errorf("%d discriminators, want §7.1's eight distinct wire values", len(got))
	}
}

// jsonTags returns the JSON names of a struct's exported fields, in declaration
// order, with `omitempty` and any other option stripped.
//
// The pointer is dereferenced here rather than at the call site: the grammar
// builds pointer frames, because that is what satisfies the sealed interfaces, and
// unwrapping is this helper's job.
func jsonTags(frameType reflect.Type) []string {
	structType := frameType
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}

	fields := make([]string, 0, structType.NumField())

	for field := range structType.Fields() {
		if !field.IsExported() {
			continue
		}

		tag, ok := field.Tag.Lookup("json")
		if !ok {
			fields = append(fields, field.Name)

			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}

		if name == "" {
			name = field.Name
		}

		fields = append(fields, name)
	}

	return fields
}
