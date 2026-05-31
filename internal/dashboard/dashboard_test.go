package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackHost(t *testing.T) {
	good := []string{"127.0.0.1", "127.0.0.1:9722", "localhost", "localhost:9722", "[::1]:80", "::1"}
	for _, h := range good {
		if !loopbackHost(h) {
			t.Errorf("loopbackHost(%q) = false, want true", h)
		}
	}
	bad := []string{"", "evil.com", "evil.com:9722", "192.168.1.5:9722", "10.0.0.1", "outpost.example.com:9722"}
	for _, h := range bad {
		if loopbackHost(h) {
			t.Errorf("loopbackHost(%q) = true, want false", h)
		}
	}
}

// localOnly must reject both non-loopback peers AND a loopback peer carrying a
// non-loopback Host header (the DNS-rebinding attack), while allowing genuine
// loopback requests.
func TestLocalOnlyGate(t *testing.T) {
	h := localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	run := func(remote, host string) int {
		r := httptest.NewRequest("GET", "/api/code", nil)
		r.RemoteAddr = remote
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if code := run("127.0.0.1:5555", "127.0.0.1:9722"); code != http.StatusOK {
		t.Fatalf("genuine loopback request: got %d, want 200", code)
	}
	if code := run("127.0.0.1:5555", "evil.com"); code != http.StatusForbidden {
		t.Fatalf("DNS-rebinding (loopback peer, attacker Host): got %d, want 403", code)
	}
	if code := run("192.168.1.9:5555", "127.0.0.1:9722"); code != http.StatusForbidden {
		t.Fatalf("non-loopback peer: got %d, want 403", code)
	}
}
