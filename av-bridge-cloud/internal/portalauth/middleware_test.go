package portalauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	devToken   = "dev-portal-token"
	devCust    = "11111111-1111-1111-1111-111111111111"
	devRole    = "viewer"
	otherToken = "some-other-token"
)

func newMiddleware() http.Handler {
	res := NewStaticResolver(devToken, devCust, devRole)
	return Middleware(res, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := From(r.Context())
		if !ok {
			http.Error(w, "no principal", http.StatusInternalServerError)
			return
		}
		// Echo back the resolved customer so tests can assert on it.
		w.Header().Set("X-Principal-Customer", p.CustomerID)
		w.Header().Set("X-Principal-Role", p.Role)
		w.WriteHeader(http.StatusOK)
	}))
}

func TestMiddleware_ValidTokenHeader(t *testing.T) {
	h := newMiddleware()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+devToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if got := w.Header().Get("X-Principal-Customer"); got != devCust {
		t.Errorf("customer: got %q want %q", got, devCust)
	}
	if got := w.Header().Get("X-Principal-Role"); got != devRole {
		t.Errorf("role: got %q want %q", got, devRole)
	}
}

func TestMiddleware_ValidTokenQueryParam(t *testing.T) {
	// Query-param path exists so the future WebSocket route works.
	h := newMiddleware()
	r := httptest.NewRequest("GET", "/x?token="+devToken, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
}

func TestMiddleware_Rejects(t *testing.T) {
	h := newMiddleware()

	cases := []struct {
		name    string
		header  string
		query   string
		wantCode int
	}{
		{"no header, no query", "", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic abc", "", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", "", http.StatusUnauthorized},
		{"wrong token via header", "Bearer " + otherToken, "", http.StatusUnauthorized},
		{"wrong token via query", "", otherToken, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := "/x"
			if c.query != "" {
				url += "?token=" + c.query
			}
			r := httptest.NewRequest("GET", url, nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.wantCode {
				t.Fatalf("status: got %d want %d", w.Code, c.wantCode)
			}
		})
	}
}

func TestRequirePermission(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// The static resolver grants every known permission, so a call gated
	// on any real permission key passes. Pick device.crud arbitrarily.
	needsDeviceCRUD := RequirePermission(PermDeviceCRUD)
	res := NewStaticResolver(devToken, devCust, "admin")
	auth := Middleware(res, needsDeviceCRUD(inner))

	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+devToken)
	w := httptest.NewRecorder()
	auth.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("static admin should pass: code=%d reached=%v", w.Code, reached)
	}

	// A principal with an empty permission map is 403 for the same call.
	// Manual construction — the static resolver always grants everything.
	reached = false
	limited := &staticPermResolver{Principal{CustomerID: devCust, Role: "viewer", Permissions: map[string]struct{}{PermViewDashboard: {}}}}
	authL := Middleware(limited, needsDeviceCRUD(inner))
	rv := httptest.NewRequest("POST", "/x", nil)
	rv.Header.Set("Authorization", "Bearer whatever")
	wv := httptest.NewRecorder()
	authL.ServeHTTP(wv, rv)
	if wv.Code != http.StatusForbidden {
		t.Fatalf("caller without device.crud should be forbidden, got %d", wv.Code)
	}
	if reached {
		t.Fatal("inner handler must not run when permission check fails")
	}

	// Vendor principal bypasses the permission check regardless of the
	// Permissions map contents — HasPermission short-circuits on IsVendor.
	reached = false
	vendor := &staticPermResolver{Principal{IsVendor: true, Role: "admin"}}
	authV := Middleware(vendor, needsDeviceCRUD(inner))
	rv2 := httptest.NewRequest("POST", "/x", nil)
	rv2.Header.Set("Authorization", "Bearer whatever")
	wv2 := httptest.NewRecorder()
	authV.ServeHTTP(wv2, rv2)
	if wv2.Code != http.StatusOK || !reached {
		t.Fatalf("vendor should bypass permission check: code=%d reached=%v", wv2.Code, reached)
	}
}

// staticPermResolver is a test-only resolver that always returns a fixed
// Principal — lets the test dial in a specific permission set without
// standing up the full LocalResolver stack.
type staticPermResolver struct{ p Principal }

func (s *staticPermResolver) Resolve(_ string) (Principal, bool) { return s.p, true }

func TestStaticResolver_RejectsEmptyConfiguredToken(t *testing.T) {
	res := NewStaticResolver("", devCust, devRole)
	if _, ok := res.Resolve("anything"); ok {
		t.Fatal("resolver with empty configured token should reject all input")
	}
}
