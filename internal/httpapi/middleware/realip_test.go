package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// clientIPHandler records the address the middleware resolved.
func clientIPHandler(observed *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*observed = middleware.MustClientIP(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

// resolveIP runs a request through RealIP and returns the resolved address.
func resolveIP(
	t *testing.T,
	remoteAddr string,
	headers map[string][]string,
	trusted ...string,
) string {
	t.Helper()

	var observed string

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = remoteAddr

	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	middleware.RealIP(trusted)(clientIPHandler(&observed)).ServeHTTP(httptest.NewRecorder(), req)

	return observed
}

// TestRealIPBelievesNothingByDefault is the case that makes a self-hosted
// server safe to expose directly. With no configured proxy, a forwarding header
// is caller-controlled, and honouring it would let any client put its own
// address into the access log — which is what a future rate limit or ban reads.
func TestRealIPBelievesNothingByDefault(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "10.0.0.9:5000", map[string][]string{
		"X-Forwarded-For": {"203.0.113.7"},
	})

	if got != "10.0.0.9" {
		t.Errorf("client ip = %q, want the transport address 10.0.0.9", got)
	}
}

// TestRealIPWalksTheChainRightToLeft is the ordering that matters. Each hop
// appends the address it saw, so the list is nearest-first and its rightmost
// entry is closest to this server. A left-to-right walk would return the
// leftmost value, which is the one a client chooses.
func TestRealIPWalksTheChainRightToLeft(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		forwarded  []string
		trusted    []string
		want       string
	}{
		{
			// The peer is a trusted proxy, so the rightmost hop it appended is
			// taken: that is the address the proxy actually saw. The
			// leftmost entry is whatever an earlier hop believed, and this
			// server has no way to check it.
			name:       "rightmost untrusted hop wins",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{"203.0.113.7, 198.51.100.4"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "198.51.100.4",
		},
		{
			// A trusted hop nearest the server is skipped, and the address
			// *it* saw becomes the answer.
			name:       "skips a trusted hop at the near end",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{"203.0.113.7, 198.51.100.4, 10.0.0.2"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "198.51.100.4",
		},
		{
			// A client that prepends a forged entry does not get to be believed
			// when the chain to its right is entirely trusted proxies: the
			// nearest untrusted hop is the real one.
			name:       "a prepended forgery loses to a nearer hop",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{"192.0.2.123, 203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "every hop trusted falls back to the transport address",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{"10.0.0.2"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "10.0.0.1",
		},
		{
			name:       "empty entries are ignored",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{" , 203.0.113.7, "},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "IPv4-mapped IPv6 is unmapped",
			remoteAddr: "10.0.0.1:5000",
			forwarded:  []string{"::ffff:203.0.113.7"},
			trusted:    []string{"10.0.0.0/8"},
			want:       "203.0.113.7",
		},
		{
			name:       "an IPv6 client is reported in its own form",
			remoteAddr: "[2001:db8::1]:5000",
			forwarded:  []string{"2001:db8::2"},
			trusted:    []string{"fd00::/8"},
			want:       "2001:db8::2",
		},
		{
			name:       "a bare address in the config is a host prefix",
			remoteAddr: "192.168.1.50:5000",
			forwarded:  []string{"203.0.113.7"},
			trusted:    []string{"192.168.1.50"},
			want:       "203.0.113.7",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := resolveIP(t, testCase.remoteAddr,
				map[string][]string{"X-Forwarded-For": testCase.forwarded},
				testCase.trusted...,
			)

			if got != testCase.want {
				t.Errorf("client ip = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRealIPStopsAtAMalformedHop covers the laundering case. A bad hop means
// the chain cannot be trusted past that point, and falling through to a hop
// further left would adopt exactly the client-chosen value the walk exists to
// avoid.
func TestRealIPStopsAtAMalformedHop(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "10.0.0.1:5000", map[string][]string{
		"X-Forwarded-For": {"192.0.2.123, not-an-address"},
	}, "10.0.0.0/8")

	if got != "10.0.0.1" {
		t.Errorf("client ip = %q, want the transport address; a malformed hop must not launder "+
			"a further-left client-chosen value", got)
	}
}

// TestRealIPHandlesMultipleHeaders covers a proxy that sends the chain as
// several header lines rather than one comma-separated value.
func TestRealIPHandlesMultipleHeaders(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "10.0.0.1:5000", map[string][]string{
		"X-Forwarded-For": {"192.0.2.123", "203.0.113.7"},
	}, "10.0.0.0/8")

	// The last header is the one nearest the server, so its rightmost untrusted
	// hop is the answer.
	if got != "203.0.113.7" {
		t.Errorf("client ip = %q, want 203.0.113.7", got)
	}
}

// TestRealIPHonoursRealIPOnlyFromATrustedPeer covers the older single-valued
// header, which a client behind a proxy may send and an untrusted peer may not.
func TestRealIPHonoursRealIPOnlyFromATrustedPeer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		trusted    []string
		want       string
	}{
		{"trusted peer", "10.0.0.1:5000", []string{"10.0.0.0/8"}, "203.0.113.7"},
		{"untrusted peer", "198.51.100.4:5000", nil, "198.51.100.4"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := resolveIP(t, testCase.remoteAddr,
				map[string][]string{"X-Real-IP": {"203.0.113.7"}},
				testCase.trusted...,
			)

			if got != testCase.want {
				t.Errorf("client ip = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRealIPHandlesNoRemoteAddr covers httptest and any in-process caller that
// has no transport peer. An empty client ip in a log line is worse than one
// that says it is unknown.
func TestRealIPHandlesNoRemoteAddr(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "", nil)

	if got != "unknown" {
		t.Errorf("client ip = %q, want %q", got, "unknown")
	}
}

// TestRealIPStripsThePortFromTheTransportAddress: the port is never part of a
// client identity, and keeping it makes every comparison against an allowlist
// fail.
func TestRealIPStripsThePortFromTheTransportAddress(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "203.0.113.7:54321", nil)

	if got != "203.0.113.7" {
		t.Errorf("client ip = %q, want the address without the port", got)
	}
}

// TestRealIPToleratesMalformedTrustedEntries covers an operator typo. The list
// is configuration, and one bad entry must not stop the server booting; failing
// toward the transport address is the safe direction.
func TestRealIPToleratesMalformedTrustedEntries(t *testing.T) {
	t.Parallel()

	got := resolveIP(t, "203.0.113.7:5000", map[string][]string{
		"X-Forwarded-For": {"198.51.100.4"},
	}, "not-a-cidr", "", "  ")

	if got != "203.0.113.7" {
		t.Errorf("client ip = %q, want the transport address when nothing is trusted", got)
	}
}
