// Package portalauth — local username+password resolver.
//
// LocalResolver validates opaque `av_<hex>` session tokens against the
// user_sessions table. Sessions are minted by portalapi.Login (bcrypt-verify
// the email+password, insert a row, return the token). This resolver is the
// non-Entra path — customers who don't want federated sign-in log in with
// email+password stored in the users table.
//
// The stored hash is SHA-256(token). Tokens are 32 bytes of crypto/rand hex,
// so a plain SHA-256 has ample pre-image resistance for the DB-at-rest
// threat model; the DB never sees the raw token after INSERT.
//
// Sessions have an absolute TTL (24h by default). Rolling refresh is a
// deliberate no — a user logging in once a day is not a UX problem, and a
// bounded lifetime caps blast radius if a token leaks.
package portalauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LocalTokenPrefix distinguishes local session tokens from mock JWTs so
// ChainResolver doesn't waste a DB round-trip on the wrong tokens.
const LocalTokenPrefix = "av_"

// LocalResolver looks up session tokens against user_sessions + users. It
// owns its own admin-pool connection because sessions have no tenant scope
// yet (Principal.CustomerID is what the tenant middleware later uses).
type LocalResolver struct {
	pool *pgxpool.Pool
}

func NewLocalResolver(pool *pgxpool.Pool) *LocalResolver {
	return &LocalResolver{pool: pool}
}

// HashToken returns the hex SHA-256 of a session token. Used by both the
// resolver (lookup) and the login handler (insert) so the two agree on the
// hashing scheme.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (l *LocalResolver) Resolve(token string) (Principal, bool) {
	if !strings.HasPrefix(token, LocalTokenPrefix) {
		return Principal{}, false
	}
	// 1s deadline — same shape as MockJWTResolver so a slow DB can't hang
	// the auth middleware indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var (
		userID       string
		email        string
		fullName     *string
		role         string
		customerID   *string
		vendorTenant *string
		disabledAt   *time.Time
	)
	err := l.pool.QueryRow(ctx, `
		SELECT u.id::text, u.email, u.full_name, u.role,
		       u.customer_id::text, u.vendor_tenant_id::text, u.disabled_at
		  FROM user_sessions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1
		   AND s.revoked_at IS NULL
		   AND s.expires_at > now()`,
		HashToken(token)).
		Scan(&userID, &email, &fullName, &role, &customerID, &vendorTenant, &disabledAt)
	if err != nil {
		return Principal{}, false
	}
	if disabledAt != nil {
		return Principal{}, false
	}

	p := Principal{
		UserID: userID,
		Email:  email,
		Role:   role,
	}
	if fullName != nil {
		p.Name = *fullName
	}
	if customerID != nil {
		p.CustomerID = *customerID
	}
	if vendorTenant != nil {
		p.IsVendor = true
		// CustomerID stays empty — vendor callers set it per-request via
		// X-Customer-Scope (handled by Middleware).
	}
	return p, true
}

