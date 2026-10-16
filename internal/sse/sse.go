// Package sse is the server→client broadcast hub behind
// GET /api/v1/events (tech-stack §3.5, §7).
//
// Low-frequency broadcasts only (block revealed, dice rolled, file
// changed): native EventSource reconnect with Last-Event-ID replay covers
// phones that slept through a roll. The hub keeps a bounded ring of recent
// events so reconnects replay what they missed; the 60fps future VTT gets
// its own reserved /ws/vtt endpoint and never touches this design.
package sse

import (
	"fmt"
	"strings"
	"sync"
)

// Event is one broadcast frame. ID is the server-assigned monotonically
// increasing sequence rendered as the SSE `id:` field; clients send it back
// as Last-Event-ID and the hub replays everything after it.
type Event struct {
	// ID is assigned by Publish; senders leave it zero.
	ID uint64
	// Type is the SSE event name (block_revealed, dice_rolled, ...).
	Type string
	// Data is the event payload, already JSON-encoded by the publisher.
	Data string
}

// Hub is a concurrency-safe broadcast hub with replay. The zero value is
// not usable; construct with NewHub.
type Hub struct {
	mu     sync.Mutex
	next   uint64
	kept   []Event
	subs   map[uint64]chan Event
	subSeq uint64
}

// Keep is how many recent events replay for Last-Event-ID reconnects.
const Keep = 256

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[uint64]chan Event)}
}

// Publish appends e to the replay ring and fans out to live subscribers.
// It assigns e.ID. Publish never blocks: slow subscribers drop the event
// (buffered channel, non-blocking send) and catch up via replay.
func (h *Hub) Publish(e Event) Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	e.ID = h.next
	h.kept = append(h.kept, e)
	if len(h.kept) > Keep {
		h.kept = append([]Event(nil), h.kept[len(h.kept)-Keep:]...)
	}
	for _, ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
	return e
}

// Replay returns every kept event with ID > lastID, in order.
func (h *Hub) Replay(lastID uint64) []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Event
	for _, e := range h.kept {
		if e.ID > lastID {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe registers a live channel plus the replay backlog after lastID.
// The caller must invoke the returned unsubscribe func to remove the
// subscription, or the subscriber leaks.
func (h *Hub) Subscribe(lastID uint64) (<-chan Event, func()) {
	h.mu.Lock()
	var backlog []Event
	for _, e := range h.kept {
		if e.ID > lastID {
			backlog = append(backlog, e)
		}
	}
	ch := make(chan Event, 32)
	for _, e := range backlog {
		select {
		case ch <- e:
		default:
		}
	}
	h.subSeq++
	id := h.subSeq
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, id)
	}
}

// Format renders e as one SSE frame (id + event + data lines).
func Format(e Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "id: %d\nevent: %s\n", e.ID, e.Type)
	for _, line := range strings.Split(e.Data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}
