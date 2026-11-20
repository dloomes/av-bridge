package publicapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// With no portal base URL (local dev) the magic-link error redirect uses
// the request's own Host, never a client-supplied X-Forwarded-Host.
func TestMagicErrorRedirectIgnoresForwardedHost(t *testing.T) {
	r := httptest.NewRequest("GET", "http://portal.dev.test/public/auth/magic", nil)
	r.Header.Set("X-Forwarded-Host", "evil.example")
	rec := httptest.NewRecorder()
	(&Handler{}).redirectMagicError(rec, r, "invalid")
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || rec.Code != http.StatusFound {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if loc.Host != "portal.dev.test" {
		t.Errorf("redirected to %s, want the request's own host portal.dev.test", loc.Host)
	}
}
