package publicapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Linux installer is piped to bash, which rejects CRLF line endings
// ("invalid option name pipefail"). Whatever line endings the embedded
// template was checked out with, the served script must be LF only.
func TestInstallScriptServedWithLFOnly(t *testing.T) {
	saved := installScriptTmpl
	defer func() { installScriptTmpl = saved }()
	installScriptTmpl = strings.ReplaceAll(strings.ReplaceAll(saved, "\r\n", "\n"), "\n", "\r\n")

	h := &Handler{cloudBaseURL: "https://cloud.example.test"}
	rec := httptest.NewRecorder()
	h.ServeInstallScript(rec, httptest.NewRequest("GET", "/public/collectors/install.sh", nil))
	body := rec.Body.String()
	if strings.Contains(body, "\r") {
		t.Fatal("served install.sh contains carriage returns")
	}
	if !strings.Contains(body, "set -euo pipefail\n") || !strings.Contains(body, "https://cloud.example.test") {
		t.Fatal("served install.sh is missing expected content")
	}
}

// With no configured CLOUD_BASE_URL the script's cloud URL comes from the
// request's forwarding headers. The script is piped to bash and publicly
// cached, so anything that isn't a plain scheme://host[:port] must be
// refused rather than pasted into it.
func TestInstallScriptRefusesUnsafeForwardedOrigin(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name, proto, host string
	}{
		{"command substitution in host", "", "evil$(id).example.test"},
		{"quote in host", "", `cloud.example.test"; curl x|sh; "`},
		{"path in host", "", "cloud.example.test/x"},
		{"odd scheme", "javascript", "cloud.example.test"},
	}
	for _, c := range cases {
		for _, serve := range []func(http.ResponseWriter, *http.Request){h.ServeInstallScript, h.ServeInstallScriptPS} {
			req := httptest.NewRequest("GET", "/public/collectors/install.sh", nil)
			if c.proto != "" {
				req.Header.Set("X-Forwarded-Proto", c.proto)
			}
			req.Header.Set("X-Forwarded-Host", c.host)
			rec := httptest.NewRecorder()
			serve(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", c.name, rec.Code)
			}
			if strings.Contains(rec.Body.String(), c.host) {
				t.Errorf("%s: response contains the forwarded host", c.name)
			}
		}
	}

	// A well-formed forwarded origin is still used (dev / no CLOUD_BASE_URL).
	req := httptest.NewRequest("GET", "/public/collectors/install.sh", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "api.dev.example.test:8443")
	rec := httptest.NewRecorder()
	h.ServeInstallScript(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "https://api.dev.example.test:8443") {
		t.Fatalf("valid forwarded origin: %d", rec.Code)
	}
}
