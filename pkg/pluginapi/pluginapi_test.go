package pluginapi_test

import (
	"errors"
	"testing"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

func TestNegotiateIntersectsPreservingWantOrder(t *testing.T) {
	offered := []pluginapi.Capability{"b", "a", "unknown-host-only"}
	wanted := []pluginapi.Capability{"a", "b", "missing"}
	got := pluginapi.Negotiate(offered, wanted)
	if !got.Has("a") || !got.Has("b") {
		t.Fatalf("negotiated = %v, want a and b present", got)
	}
	if got.Has("unknown-host-only") || got.Has("missing") {
		t.Fatalf("negotiated = %v, want only the intersection", got)
	}
	if len(got) != 2 {
		t.Fatalf("len(negotiated) = %d, want 2", len(got))
	}
}

func TestNegotiateEmpty(t *testing.T) {
	if got := pluginapi.Negotiate(nil, nil); len(got) != 0 {
		t.Fatalf("negotiated = %v, want empty", got)
	}
}

func TestValidSlotClosedVocabulary(t *testing.T) {
	for _, s := range pluginapi.KnownSlots {
		if !pluginapi.ValidSlot(s) {
			t.Fatalf("ValidSlot(%q) = false, want true", s)
		}
	}
	if pluginapi.ValidSlot("under-the-map") {
		t.Fatal("ValidSlot(under-the-map) = true, want false (slots, never positions)")
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	errs := []error{
		pluginapi.ErrNotFound,
		pluginapi.ErrConflict,
		pluginapi.ErrUnauthorized,
		pluginapi.ErrForbidden,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel %v matches %v, want distinct", a, b)
			}
		}
	}
}
