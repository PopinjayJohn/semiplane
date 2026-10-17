package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/api"
)

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(api.NewMux())
	defer srv.Close()

	status, body := get(t, srv.URL+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", status)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != "ok" {
		t.Fatalf("healthz body = %v, want status=ok", decoded)
	}
}

func TestVersion(t *testing.T) {
	srv := httptest.NewServer(api.NewMux())
	defer srv.Close()

	if status, _ := get(t, srv.URL+"/api/v1/version"); status != http.StatusOK {
		t.Fatalf("version status = %d, want 200", status)
	}
}

func TestSPARoot(t *testing.T) {
	// Invariant: GET / never fails silently. What it serves depends on what
	// was embedded at compile time — the real SPA if `make embed` ran, else
	// the loud 503 placeholder. This test adapts to both so it stays green
	// regardless of local build-artifact state (see AGENTS.md gotcha #2).
	srv := httptest.NewServer(api.NewMux())
	defer srv.Close()

	status, body := get(t, srv.URL+"/")
	switch status {
	case http.StatusOK:
		if len(body) == 0 {
			t.Fatal("SPA root returned 200 with empty body")
		}
	case http.StatusServiceUnavailable:
		if !strings.Contains(string(body), "make web") {
			t.Fatalf("placeholder body missing rebuild hint: %q", body)
		}
	default:
		t.Fatalf("root status = %d, want 200 (embedded SPA) or 503 (placeholder)", status)
	}
}
