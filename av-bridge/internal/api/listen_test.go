package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSafeListenAddr(t *testing.T) {
	on := AuthConfig{Enabled: true, APIKeys: []string{"k1"}}
	off := AuthConfig{}
	enabledNoKeys := AuthConfig{Enabled: true}
	blankKeys := AuthConfig{Enabled: true, APIKeys: []string{"", "  "}}
	keysNotEnabled := AuthConfig{APIKeys: []string{"k1"}}

	cases := []struct {
		name       string
		addr       string
		auth       AuthConfig
		want       string
		wantForced bool
	}{
		{"loopback v4 no auth", "127.0.0.1:8080", off, "127.0.0.1:8080", false},
		{"loopback v4 other in /8", "127.0.0.2:9000", off, "127.0.0.2:9000", false},
		{"loopback v6 no auth", "[::1]:8080", off, "[::1]:8080", false},
		{"localhost no auth", "localhost:8080", off, "localhost:8080", false},
		{"localhost uppercase", "LocalHost:8080", off, "LocalHost:8080", false},
		{"all interfaces no auth", "0.0.0.0:8080", off, "127.0.0.1:8080", true},
		{"empty host no auth", ":8080", off, "127.0.0.1:8080", true},
		{"v6 any no auth", "[::]:8081", off, "127.0.0.1:8081", true},
		{"lan ip no auth", "192.168.1.10:8080", off, "127.0.0.1:8080", true},
		{"hostname no auth", "collector.local:8080", off, "127.0.0.1:8080", true},
		{"all interfaces auth enabled no keys", "0.0.0.0:8080", enabledNoKeys, "127.0.0.1:8080", true},
		{"all interfaces blank keys", "0.0.0.0:8080", blankKeys, "127.0.0.1:8080", true},
		{"all interfaces keys but disabled", "0.0.0.0:8080", keysNotEnabled, "127.0.0.1:8080", true},
		{"all interfaces with auth", "0.0.0.0:8080", on, "0.0.0.0:8080", false},
		{"empty host with auth", ":8080", on, ":8080", false},
		{"lan ip with auth", "10.0.0.5:8443", on, "10.0.0.5:8443", false},
		{"fully qualified localhost", "localhost.:8080", off, "localhost.:8080", false},
		{"loopback v6 with zone", "[::1%lo]:8080", off, "[::1%lo]:8080", false},
		{"lookalike hostname", "127.0.0.1.nip.io:8080", off, "127.0.0.1:8080", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, forced := safeListenAddr(tc.addr, tc.auth)
			if got != tc.want || forced != tc.wantForced {
				t.Fatalf("safeListenAddr(%q) = (%q, %v), want (%q, %v)", tc.addr, got, forced, tc.want, tc.wantForced)
			}
		})
	}
}

func TestNewFailsClosedWithoutAuth(t *testing.T) {
	s := New("0.0.0.0:8080", nil, nil, AuthConfig{})
	if s.srv.Addr != "127.0.0.1:8080" {
		t.Fatalf("server addr = %q, want 127.0.0.1:8080", s.srv.Addr)
	}
	s = New("0.0.0.0:8080", nil, nil, AuthConfig{Enabled: true, APIKeys: []string{"k"}})
	if s.srv.Addr != "0.0.0.0:8080" {
		t.Fatalf("server addr = %q, want 0.0.0.0:8080", s.srv.Addr)
	}
}

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin (non-browser)", "collector:8080", "", true},
		{"same origin", "127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"same origin https", "collector.local:8080", "https://collector.local:8080", true},
		{"same origin case-insensitive", "Collector.Local:8080", "http://collector.local:8080", true},
		{"cross origin", "127.0.0.1:8080", "http://evil.example", false},
		{"cross origin same host other port", "127.0.0.1:8080", "http://127.0.0.1:3000", false},
		{"null origin", "127.0.0.1:8080", "null", false},
		{"garbage origin", "127.0.0.1:8080", "::not a url", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/ws/events", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := checkSameOrigin(r); got != tc.want {
				t.Fatalf("checkSameOrigin(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func TestWSEventsRejectsCrossOrigin(t *testing.T) {
	s := New("127.0.0.1:0", nil, nil, AuthConfig{})
	ts := httptest.NewServer(s.srv.Handler)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"

	h := http.Header{}
	h.Set("Origin", "http://evil.example")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err == nil {
		conn.Close()
		t.Fatal("cross-origin websocket upgrade succeeded, want refusal")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %v, want 403", resp)
	}

	conn, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("no-origin websocket upgrade failed: %v", err)
	}
	conn.Close()
}

// browserGuard stops pages in a browser on the Collector host from driving
// the local API: cross-site requests are refused, and without API keys only
// loopback Host headers are answered (defeats DNS rebinding).
func TestBrowserGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	noKeys := browserGuard(AuthConfig{})(ok)
	keyed := browserGuard(AuthConfig{Enabled: true, APIKeys: []string{"k"}})(ok)

	cases := []struct {
		name   string
		h      http.Handler
		path   string
		host   string
		origin string
		fetch  string
		want   int
	}{
		{"local tool, no origin", noKeys, "/api/v1/devices", "127.0.0.1:8080", "", "", http.StatusOK},
		{"localhost name", noKeys, "/api/v1/devices", "localhost:8080", "", "", http.StatusOK},
		{"rebound domain", noKeys, "/api/v1/devices", "evil.example:8080", "http://evil.example:8080", "", http.StatusForbidden},
		{"rebound domain, no origin", noKeys, "/api/v1/devices", "evil.example:8080", "", "", http.StatusForbidden},
		{"cross-site post to loopback", noKeys, "/api/v1/devices/x/command", "127.0.0.1:8080", "https://evil.example", "", http.StatusForbidden},
		{"sec-fetch cross-site", noKeys, "/api/v1/devices", "127.0.0.1:8080", "", "cross-site", http.StatusForbidden},
		{"same origin page", noKeys, "/api/v1/devices", "127.0.0.1:8080", "http://127.0.0.1:8080", "same-origin", http.StatusOK},
		{"healthz always open", noKeys, "/healthz", "evil.example:8080", "https://evil.example", "cross-site", http.StatusOK},
		{"keyed: network host allowed", keyed, "/api/v1/devices", "10.0.0.5:8080", "", "", http.StatusOK},
		{"keyed: cross-site still refused", keyed, "/api/v1/devices", "10.0.0.5:8080", "https://evil.example", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			w := httptest.NewRecorder()
			tc.h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
