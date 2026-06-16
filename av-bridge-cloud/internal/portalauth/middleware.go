// Package portalauth turns a bearer token into a (customer_id, role) Principal
// and attaches it to the request context. Every downstream handler must scope
// its queries to that customer (typically via Store.WithTenant).
//
// The Resolver interface is the seam where the auth source changes per slice:
//
//   - Slice 2:  StaticResolver (one env-held token -> the PoC customer).
//   - Later:    DBResolver (portal_tokens table, hashed tokens, per-user/role).
//   - Final:    JWTResolver (Microsoft Entra ID + NHS/GOV.UK SSO; same Principal shape).
//
// Replacing the Resolver does not require touching handlers — they always read
// Principal from context and call WithTenant.
package portalauth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type Principal struct {
	CustomerID string
	Role       string
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// From retrieves the Principal placed by Middleware. Returns ok=false if no
// Principal is set — handlers downstream of Middleware should never see this.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Resolver maps a bearer token to a Principal.
type Resolver interface {
	Resolve(token string) (Principal, bool)
}

// StaticResolver maps a single env-held token to a fixed (customer_id, role).
// Slice 2 only — replaced by a DB-backed resolver / JWT validator as the
// portal/auth slices land.
type StaticResolver struct {
	token      string
	customerID string
	role       string
}

func NewStaticResolver(token, customerID, role string) *StaticResolver {
	return &StaticResolver{token: token, customerID: customerID, role: role}
}

func (s *StaticResolver) Resolve(token string) (Principal, bool) {
	if s.token == "" || token == "" {
		return Principal{}, false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return Principal{}, false
	}
	return Principal{CustomerID: s.customerID, Role: s.role}, true
}

// Middleware enforces a valid bearer token and attaches the Principal to ctx.
// 401 otherwise. Header `Authorization: Bearer <token>` is canonical; a
// `?token=` query param is also accepted so the future WebSocket route can use
// it (browsers can't set custom headers on the WS handshake).
func Middleware(r Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		token := extractBearer(req)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		p, ok := r.Resolve(token)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, req.WithContext(withPrincipal(req.Context(), p)))
	})
}

// RequireRole returns a middleware that rejects requests whose Principal's
// Role isn't in the allowed set. Use to gate write endpoints behind
// "admin"/"operator"/etc. Must be composed inside Middleware.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, "no principal in context")
				return
			}
			if _, ok := set[p.Role]; !ok {
				writeErr(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearer(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, prefix) {
		return strings.TrimPrefix(auth, prefix)
	}
	return r.URL.Query().Get("token")
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
