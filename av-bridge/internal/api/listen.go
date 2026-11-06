package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// safeListenAddr returns the address the local API should actually bind to.
//
// The local API can read devices and send commands, so it must never be open
// to the network without authentication. If addr is not a loopback address
// (127.0.0.0/8, ::1 or "localhost") and auth is disabled or has no usable
// keys, the API is forced onto 127.0.0.1 with the same port and forced is
// true. An empty host (":8080") means all interfaces.
func safeListenAddr(addr string, auth AuthConfig) (effective string, forced bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Unparseable (e.g. no port): ListenAndServe rejects it too, so
		// nothing is opened. Leave it for that error to report.
		return addr, false
	}
	if isLoopbackHost(host) || authUsable(auth) {
		return addr, false
	}
	return net.JoinHostPort("127.0.0.1", port), true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authUsable reports whether auth is enabled with at least one non-blank key.
// A placeholder like ${AV_BRIDGE_API_KEY} expands to "" when the variable is
// unset, which must not count as a key.
func authUsable(auth AuthConfig) bool {
	if !auth.Enabled {
		return false
	}
	for _, k := range auth.APIKeys {
		if strings.TrimSpace(k) != "" {
			return true
		}
	}
	return false
}

// checkSameOrigin is the WebSocket CheckOrigin for /ws/events. Non-browser
// clients send no Origin header and are allowed; browsers are allowed only
// when the Origin's host:port equals the request Host, so a page on another
// site cannot open the event stream through a user's browser.
func checkSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
