package publicapi

import (
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
