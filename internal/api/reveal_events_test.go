package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRevealBroadcastsBlockRevealed(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	seed := postJSON(t, srv.URL+"/api/v1/notes/campaign/secret.md",
		map[string]string{"content": "# Secret\n\n:::gm\nDragon here.\n:::\n", "hash": ""})
	_ = seed.Body.Close()
	if seed.StatusCode != http.StatusCreated {
		t.Fatalf("seed = %d, want 201", seed.StatusCode)
	}

	// Open the SSE stream first (headers flush immediately), then reveal
	// from a goroutine while the main test blocks reading one frame. The
	// request context carries a timeout and is cancelled at test end so
	// srv.Close() never blocks on the open stream.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	revealed := make(chan uint64, 1)
	go func() {
		r := postJSON(t, srv.URL+"/api/v1/reveal",
			map[string]string{"page": "campaign/secret.md", "blockId": "b1"})
		var out struct {
			OK      bool   `json:"ok"`
			EventID uint64 `json:"eventId"`
		}
		decodeBody(t, r, &out)
		if r.StatusCode == http.StatusOK && out.OK {
			revealed <- out.EventID
		}
	}()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data += strings.TrimPrefix(line, "data: ")
		case line == "" && event != "":
			if event != "block_revealed" {
				t.Fatalf("event = %q, want block_revealed", event)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["page"] != "campaign/secret.md" || payload["blockId"] != "b1" {
				t.Fatalf("reveal payload = %v", payload)
			}
			html, _ := payload["html"].(string)
			if !strings.Contains(html, "Dragon") {
				t.Fatalf("pre-rendered GM html missing secret: %q", html)
			}
			select {
			case evtID := <-revealed:
				if evtID == 0 {
					t.Fatal("reveal must return the published event id")
				}
			case <-ctx.Done():
				t.Fatal("reveal POST did not complete")
			}
			return
		}
	}
	t.Fatal("SSE stream ended before block_revealed arrived")
}

func TestRevealMissingPage404(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/reveal",
		map[string]string{"page": "nope.md", "blockId": "b1"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestEventsReplayWithLastEventID(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	seed := postJSON(t, srv.URL+"/api/v1/notes/a.md",
		map[string]string{"content": "x", "hash": ""})
	_ = seed.Body.Close()
	r1 := postJSON(t, srv.URL+"/api/v1/reveal", map[string]string{"page": "a.md", "blockId": "b1"})
	var first struct {
		EventID uint64 `json:"eventId"`
	}
	decodeBody(t, r1, &first)
	r2 := postJSON(t, srv.URL+"/api/v1/reveal", map[string]string{"page": "a.md", "blockId": "b2"})
	var second struct {
		EventID uint64 `json:"eventId"`
	}
	decodeBody(t, r2, &second)

	// Reconnect with Last-Event-ID = first: only the second event replays,
	// then the live stream continues (we close after the first frame).
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/events", nil)
	req.Header.Set("Last-Event-ID", strings.TrimSpace(uintToString(first.EventID)))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	var ids []string
	var dataLines int
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "id: ") {
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		} else if strings.HasPrefix(line, "data: ") {
			dataLines++
		}
		if len(ids) >= 1 && dataLines >= 1 {
			break
		}
	}
	if len(ids) == 0 {
		t.Fatal("no replayed event received")
	}
	if ids[0] != uintToString(second.EventID) {
		t.Fatalf("replayed id = %s, want %d (only events after Last-Event-ID)", ids[0], second.EventID)
	}
}

func uintToString(v uint64) string {
	return strconv.FormatUint(v, 10)
}
