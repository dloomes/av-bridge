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

// Principal carries the identity established by a Resolver. CustomerID is the
// scope every tenant-bound query runs under; for vendor users it may be set
// by the X-Customer-Scope header on each request (the resolver hands back a
// vendor-flagged Principal with an empty CustomerID and the middleware fills
// it in if a valid scope header is present).
//
// Audit consumers should prefer Email (real identity) over Role (just the
// authorisation level) when stamping actor — Role is a fallback for the
// legacy StaticResolver path where no user identity exists.
//
// Permissions is the union of every capability granted to the caller via
// their role assignments — the resolver computes it at token-resolution time
// so route middleware doesn't need a DB round-trip per request. Vendor users
// have an empty Permissions map because RequirePermission short-circuits on
// IsVendor (they bypass tenant RBAC entirely).
type Principal struct {
	UserID      string
	Email       string
	Name        string
	CustomerID  string
	Role        string
	IsVendor    bool
	Permissions map[string]struct{}
	// BuildingScopeIDs restricts the caller to a subset of their tenant's
	// buildings. Empty = full-tenant access. Enforced by the RLS policies
	// added in migration 0019 whenever a handler runs its query under
	// Store.WithTenantScoped. Vendor principals ignore this — they act as
	// unscoped admins inside whichever customer they scope to.
	BuildingScopeIDs []string
}

// HasPermission returns true if the principal holds the given capability.
// Vendor principals return true for every key — vendor callers bypass the
// customer-tenant RBAC when acting on that customer's data. Handlers should
// use this over direct map access so the vendor-bypass rule stays in one
// place.
func (p Principal) HasPermission(perm string) bool {
	if p.IsVendor {
		return true
	}
	if p.Permissions == nil {
		return false
	}
	_, ok := p.Permissions[perm]
	return ok
}

// ActorLabel returns the best available identifier for audit/log purposes.
// Real users surface their email; the legacy static-token path falls back to
// the role string so existing audit rows remain interpretable.
func (p Principal) ActorLabel() string {
	if p.Email != "" {
		return p.Email
	}
	return p.Role
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
	// Static token is an ops/smoke fallback — grant every known permission
	// so scripts using this token behave as an admin under the RequirePermission
	// engine. If a deployment wants to gate the static token more tightly,
	// they should use the local-auth path (users + user_roles) instead.
	perms := make(map[string]struct{}, len(KnownPermissions))
	for k := range KnownPermissions {
		perms[k] = struct{}{}
	}
	return Principal{CustomerID: s.customerID, Role: s.role, Permissions: perms}, true
}

// Middleware enforces a valid bearer token and attaches the Principal to ctx.
// 401 otherwise. Header `Authorization: Bearer <token>` is canonical; a
// `?token=` query param is also accepted so the future WebSocket route can use
// it (browsers can't set custom headers on the WS handshake).
//
// Vendor users (IsVendor=true) may pass `X-Customer-Scope: <customer-id>` to
// act as that customer for tenant-scoped endpoints. Non-vendor users supplying
// the header get a 403 — only vendor support staff can cross-tenant. A vendor
// with no scope header keeps an empty CustomerID; handlers that require a
// tenant scope must reject that explicitly.
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
		if scope := req.Header.Get("X-Customer-Scope"); scope != "" {
			if !p.IsVendor {
				writeErr(w, http.StatusForbidden, "X-Customer-Scope is vendor-only")
				return
			}
			p.CustomerID = scope
		}
		next.ServeHTTP(w, req.WithContext(withPrincipal(req.Context(), p)))
	})
}

// RequirePermission returns a middleware that rejects requests whose
// Principal doesn't hold the named capability. This is the primary
// authorization gate for /api/v1 routes — every write endpoint (and every
// read that needs a specific view.* permission) mounts inside a
// RequirePermission wrapper, so a caller with a role missing the key gets
// a 403 before the handler runs.
//
// Vendor principals bypass this check entirely (HasPermission returns true
// for every key) — they're treated as admin-equivalent inside whichever
// customer they're currently scoped to via X-Customer-Scope. See the
// Principal type doc for why.
//
// Must be composed inside Middleware — reads Principal from context.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, "no principal in context")
				return
			}
			if !p.HasPermission(perm) {
				writeErr(w, http.StatusForbidden, "missing permission: "+perm)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireVendor rejects non-vendor callers. For helpdesk-only endpoints
// (e.g. cross-customer list views). Compose inside Middleware.
func RequireVendor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := From(r.Context())
		if !ok {
			writeErr(w, http.StatusUnauthorized, "no principal in context")
			return
		}
		if !p.IsVendor {
			writeErr(w, http.StatusForbidden, "vendor access required")
			return
		}
		next.ServeHTTP(w, r)
	})
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
