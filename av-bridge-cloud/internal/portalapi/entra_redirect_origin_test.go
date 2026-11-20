package portalapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// With no ENTRA_PORTAL_BASE_URL (local dev) the sign-in redirects fall
// back to the request's own Host. A client-supplied X-Forwarded-Host must
// never choose where the browser (and, after sign-in, the session token)
// goes.
func TestEntraRedirectFallbackIgnoresForwardedHost(t *testing.T) {
	hostile := func() *http.Request {
		r := httptest.NewRequest("GET", "http://portal.dev.test/api/v1/auth/entra/callback", nil)
		r.Header.Set("X-Forwarded-Host", "evil.example")
		return r
	}
	check := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil || rec.Code != http.StatusFound {
			t.Fatalf("%s: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
		if loc.Host != "portal.dev.test" {
			t.Errorf("%s: redirected to %s, want the request's own host portal.dev.test", name, loc.Host)
		}
	}

	vendor := &EntraVendorHandler{}
	rec := httptest.NewRecorder()
	vendor.redirectSignInError(rec, hostile(), "state_mismatch")
	check("vendor error", rec)
	if o := vendor.portalOrigin(hostile()); o != "http://portal.dev.test" {
		t.Errorf("vendor portalOrigin = %q, want http://portal.dev.test", o)
	}

	customer := &EntraCustomerHandler{}
	rec = httptest.NewRecorder()
	customer.redirectSignInError(rec, hostile(), "", "state_mismatch")
	check("customer error", rec)

	// A configured base URL still wins (UAT / production).
	vendor = &EntraVendorHandler{portalBaseURL: "https://app.example.test/"}
	if o := vendor.portalOrigin(hostile()); o != "https://app.example.test" {
		t.Errorf("configured portalOrigin = %q", o)
	}
}
