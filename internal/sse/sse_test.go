package sse

import (
	"strings"
	"testing"
)

func TestPublishAssignsMonotonicIDs(t *testing.T) {
	h := NewHub()
	a := h.Publish(Event{Type: "x", Data: "{}"})
	b := h.Publish(Event{Type: "x", Data: "{}"})
	if b.ID != a.ID+1 {
		t.Fatalf("ids = %d, %d; want monotonic", a.ID, b.ID)
	}
}

func TestReplayAfterLastID(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: "a", Data: "1"})
	second := h.Publish(Event{Type: "b", Data: "2"})
	h.Publish(Event{Type: "c", Data: "3"})
	got := h.Replay(second.ID)
	if len(got) != 1 || got[0].Type != "c" {
		t.Fatalf("replay = %+v, want [c]", got)
	}
	if len(h.Replay(999)) != 0 {
		t.Fatal("replay past the tip must be empty")
	}
}

func TestSubscribeReceivesBacklogThenLive(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: "old", Data: "0"})
	ch, unsub := h.Subscribe(0)
	defer unsub()
	h.Publish(Event{Type: "new", Data: "1"})
	first := <-ch
	second := <-ch
	if first.Type != "old" || second.Type != "new" {
		t.Fatalf("got %q then %q, want old then new", first.Type, second.Type)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe(0)
	unsub()
	h.Publish(Event{Type: "x", Data: "1"})
	select {
	case e := <-ch:
		t.Fatalf("unsubscribed channel received %+v", e)
	default:
	}
}

func TestFormatFrame(t *testing.T) {
	frame := Format(Event{ID: 7, Type: "block_revealed", Data: "{\"a\":1}\n{\"b\":2}"})
	if !strings.HasPrefix(frame, "id: 7\nevent: block_revealed\n") {
		t.Fatalf("frame header wrong:\n%s", frame)
	}
	if !strings.Contains(frame, "data: {\"a\":1}\ndata: {\"b\":2}\n\n") {
		t.Fatalf("multi-line data not split:\n%s", frame)
	}
}
