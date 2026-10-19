package api

import (
	"bufio"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRevealBroadcastsBlockRevealed(t *testing.T) {
	t.Skip("requires full auth middleware setup for SSE identity; tested via integration")
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

// TestStripGMBlocksFromHTML tests the HTML stripping function
func TestStripGMBlocksFromHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no gm blocks",
			input:    "<h1>Title</h1><p>Content</p>",
			expected: "<h1>Title</h1><p>Content</p>",
		},
		{
			name:     "single gm block",
			input:    "<h1>Title</h1><section class=\"gm-block\"><p>Secret</p></section><p>Public</p>",
			expected: "<h1>Title</h1><p>Public</p>",
		},
		{
			name:     "gm block with title",
			input:    "<section class=\"gm-block\" data-testid=\"gm-block\"><p class=\"gm-title\">Title</p><p>Secret</p></section>",
			expected: "",
		},
		{
			name:     "multiple gm blocks",
			input:    "<section class=\"gm-block\"><p>Secret1</p></section><p>Public</p><section class=\"gm-block\"><p>Secret2</p></section>",
			expected: "<p>Public</p>",
		},
		{
			name:     "nested sections in gm block",
			input:    "<section class=\"gm-block\"><section><p>Secret</p></section></section>",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := stripGMBlocksFromHTML(tc.input)
			if result != tc.expected {
				t.Errorf("stripGMBlocksFromHTML(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func uintToString(v uint64) string {
	return strconv.FormatUint(v, 10)
}
