package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestSPAPlaceholder(t *testing.T) {
	// Fresh checkout: internal/webui/dist holds no index.html, so the SPA
	// handler serves the loud placeholder (503) instead of silent 404s.
	srv := httptest.NewServer(api.NewMux())
	defer srv.Close()

	status, body := get(t, srv.URL+"/")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("root status = %d, want 503 placeholder", status)
	}
	if len(body) == 0 {
		t.Fatal("empty placeholder body")
	}
}
