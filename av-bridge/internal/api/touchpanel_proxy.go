package api

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/gorilla/mux"
)

// touchPanelProxy reverse-proxies HTTP(S) traffic from the portal to a device's
// embedded web UI (e.g. Aurora RXT touch panels) when the panel sits on a LAN
// the user's browser can't reach but the bridge can.
//
// Mounted at: /api/v1/devices/{id}/touch-panel/{rest:.*}
//
// Known caveats:
//   - The panel's UI must tolerate being served from a sub-path. Absolute asset
//     URLs ("/css/app.css") will 404. Verify per model.
//   - Self-signed TLS is expected on local devices; verification is disabled.
//   - WebSocket upgrades work via httputil.ReverseProxy as long as the
//     responseWriter wrapper in middleware.go keeps its Hijack passthrough.
func (s *Server) touchPanelProxy(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	prefix := fmt.Sprintf("/api/v1/devices/%s/touch-panel", id)
	rest := strings.TrimPrefix(r.URL.Path, prefix)

	dev := s.hub.GetDevice(id)
	if dev == nil {
		writeJSONError(w, http.StatusNotFound, "device not found")
		return
	}

	target, err := resolveTouchPanelURL(dev.Info())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Redirect bare /touch-panel to /touch-panel/ so the panel's relative
	// asset URLs resolve against the proxy path, not its parent.
	if rest == "" && !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = "/" + strings.TrimPrefix(rest, "/")
			// Don't leak the bridge's bearer token to the device.
			req.Header.Del("Authorization")
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		ModifyResponse: func(resp *http.Response) error {
			// Rewrite absolute-path redirects so the browser stays inside the
			// proxy mount instead of resolving against the portal's origin.
			// e.g. panel returns "Location: /user/" → we send "Location:
			// /api/v1/devices/{id}/touch-panel/user/".
			if loc := resp.Header.Get("Location"); loc != "" {
				if u, err := url.Parse(loc); err == nil && u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") {
					u.Path = prefix + u.Path
					resp.Header.Set("Location", u.String())
				}
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("touch-panel proxy error", "device", id, "target", target.String(), "error", err)
			writeJSONError(rw, http.StatusBadGateway, "touch panel unreachable: "+err.Error())
		},
	}

	proxy.ServeHTTP(w, r)
}

// resolveTouchPanelURL returns the upstream URL for a device's embedded web UI,
// or an error if the protocol doesn't expose one.
func resolveTouchPanelURL(cfg config.DeviceConfig) (*url.URL, error) {
	switch cfg.Protocol {
	case "aurora_rxt":
		host := cfg.Tags["ip_address"]
		if host == "" {
			host = cfg.Address
			if i := strings.Index(host, ":"); i >= 0 {
				host = host[:i]
			}
		}
		if host == "" {
			return nil, fmt.Errorf("device has no ip_address tag or address")
		}
		return &url.URL{Scheme: "https", Host: host}, nil
	default:
		return nil, fmt.Errorf("protocol %q does not expose a touch panel", cfg.Protocol)
	}
}
