package pubapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Principal is the identity established by the public-API middleware.
// Distinct from portalauth.Principal so the two auth surfaces can evolve
// separately — a portal Principal carries user identity + role
// permissions; a pub-API Principal carries a token identity + explicit
// scopes.
//
// CustomerID is the RLS scope every downstream query runs under (same
// role and mechanism as the portal — store.WithTenant sets the
// app.current_customer GUC, RLS does the rest).
//
// Scopes is the closed set of permission keys the token holder is
// entitled to. Handlers gate on this via RequireScope. v1 restricts
// valid scope strings to view.* keys at issuance time so an integrator
// can never end up with, e.g., command.device on a public-API token.
//
// TokenID is stamped onto audit rows so a stolen key can be traced to
// its revoke row after the fact.
type Principal struct {
	CustomerID string
	TokenID    string
	TokenName  string
	Scopes     map[string]struct{}
}

// HasScope reports whether the token grants the given capability.
func (p Principal) HasScope(scope string) bool {
	if p.Scopes == nil {
		return false
	}
	_, ok := p.Scopes[scope]
	return ok
}

type principalKey struct{}

// From retrieves the Principal placed by Middleware. Returns ok=false
// if no Principal is set — a handler seeing this is a wiring bug.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Resolver looks up a raw token against api_tokens and returns the
// Principal it confers. Runs under the admin pool because RLS on
// api_tokens keys on app.current_customer, which we don't know yet —
// the whole point of the lookup is to discover the customer.
type Resolver struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewResolver(pool *pgxpool.Pool, log *slog.Logger) *Resolver {
	return &Resolver{pool: pool, log: log}
}

// resolve returns the Principal or (zero, false). It never distinguishes
// "token not present" from "token revoked" — the middleware collapses
// both into a 401 with the same message. That's deliberate: a public
// API leaks less by refusing to describe why.
func (r *Resolver) resolve(ctx context.Context, raw string) (Principal, bool) {
	if _, err := ParseToken(raw); err != nil {
		return Principal{}, false
	}
	hash := HashToken(raw)

	// 1s deadline mirrors the portal LocalResolver — a slow DB must
	// not hang the auth path indefinitely; a 401 with retry is better
	// than a stalled request.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	var (
		id         string
		customerID string
		name       string
		scopes     []string
		expiresAt  *time.Time
		revokedAt  *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, customer_id::text, name,
		       COALESCE(scopes, '{}'::text[]),
		       expires_at, revoked_at
		  FROM api_tokens
		 WHERE token_hash = $1`, hash,
	).Scan(&id, &customerID, &name, &scopes, &expiresAt, &revokedAt)
	if err != nil {
		return Principal{}, false
	}
	if revokedAt != nil {
		return Principal{}, false
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return Principal{}, false
	}
	set := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		set[s] = struct{}{}
	}
	return Principal{
		CustomerID: customerID,
		TokenID:    id,
		TokenName:  name,
		Scopes:     set,
	}, true
}

// TouchLastUsed records the caller's IP + now() against the token, best-
// effort. Failure is logged but doesn't fail the request — an audit
// gap on a rare DB blip beats a spurious 500 on a valid read.
func (r *Resolver) TouchLastUsed(ctx context.Context, tokenID, ip string) {
	if tokenID == "" {
		return
	}
	// Own short deadline so a slow write doesn't tail-latency the response.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := r.pool.Exec(ctx, `
		UPDATE api_tokens
		   SET last_used_at = now(), last_used_ip = $2
		 WHERE id = $1`, tokenID, ip,
	); err != nil {
		r.log.Warn("pubapi: touch last_used failed", "token_id", tokenID, "error", err)
	}
}

// Middleware enforces a valid public-API bearer token and attaches the
// Principal to ctx. Every /pub/v1 route composes inside this.
//
// Errors:
//   - Missing Authorization header  → 401 "missing bearer token"
//   - Invalid token / revoked / expired → 401 "invalid token"
//
// The last_used_at touch runs in a goroutine so the update never
// blocks the response.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw := extractBearer(req)
		if raw == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		p, ok := r.resolve(req.Context(), raw)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		go r.TouchLastUsed(context.Background(), p.TokenID, clientIP(req))
		next.ServeHTTP(w, req.WithContext(withPrincipal(req.Context(), p)))
	})
}

// RequireScope gates the handler on the named scope key. Composes INSIDE
// Middleware — reads Principal from context. A caller missing the
// scope gets a 403 with a clear message so an integrator sees exactly
// which permission they need on the token.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p, ok := From(req.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, "no principal in context")
				return
			}
			if !p.HasScope(scope) {
				writeErr(w, http.StatusForbidden, "token missing scope: "+scope)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// extractBearer pulls the token out of `Authorization: Bearer <token>`.
// Query-string tokens are intentionally NOT accepted — the public API
// is a machine-to-machine surface, and URL-borne credentials end up in
// access logs and CDNs.
func extractBearer(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	}
	return ""
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}

// writeErr writes the canonical public-API error envelope. Every
// non-2xx response uses this so client SDKs can parse errors uniformly.
func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": msg,
			"code":    codeForStatus(status),
		},
	})
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusInternalServerError:
		return "internal_error"
	}
	return "error"
}

