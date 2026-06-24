// Package portalauth — mock JWT resolver.
//
// MockJWTResolver accepts tokens of the form `mock.<base64-json>` where the
// JSON body matches a subset of an Entra ID id-token's claim shape. It does
// the same DB-driven tenant→customer/vendor lookup and group→role mapping
// that a real-Entra resolver will eventually do, so swapping in real-OIDC
// is just a different parser feeding the same lookups.
//
// Mock tokens are intentionally trivial to construct so dev/test can simulate
// multi-user, multi-customer scenarios without standing up Entra. They are
// not cryptographically secure — this resolver is gated to non-production
// deployments by the operator (enable via env, exactly as static token).
package portalauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const mockPrefix = "mock."

// MockClaims is the subset of an Entra ID id-token's claims this resolver
// understands. Field names match the standard claim names so a future real-
// JWT validator can decode into the same struct.
type MockClaims struct {
	Sub    string   `json:"sub"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Tid    string   `json:"tid"`
	Groups []string `json:"groups"`
}

// TenantLookup is the read-side interface the resolver depends on. Letting
// the resolver own its own queries keeps it self-contained and easy to mock
// in tests. Real implementation lives in db.Store.
type TenantLookup interface {
	// LookupCustomerByEntraTenant returns the customer id for an Entra tenant
	// id, or ("", pgx.ErrNoRows) if none matches.
	LookupCustomerByEntraTenant(ctx context.Context, tid string) (string, error)
	// LookupVendorByEntraTenant returns the vendor tenant id, or ("",
	// pgx.ErrNoRows).
	LookupVendorByEntraTenant(ctx context.Context, tid string) (string, error)
	// ResolveRole picks the strongest role across the supplied groups for the
	// given scope (one of customer_id / vendor_tenant_id is set, the other
	// blank). Returns "" if no mapping matched.
	ResolveRole(ctx context.Context, customerID, vendorTenantID string, groups []string) (string, error)
}

// MockJWTResolver decodes a mock token and resolves it against the DB.
// Embeds a tiny TTL cache so repeated requests from the same token don't
// hammer the DB.
type MockJWTResolver struct {
	lookup    TenantLookup
	cacheTTL  time.Duration
	mu        sync.Mutex
	cache     map[string]cachedPrincipal
	allowMock bool
}

type cachedPrincipal struct {
	p       Principal
	expires time.Time
}

// NewMockJWTResolver. allowMock=false makes Resolve always return ("", false)
// — useful for production deployments that want the type wired but the
// feature off.
func NewMockJWTResolver(lookup TenantLookup, allowMock bool) *MockJWTResolver {
	return &MockJWTResolver{
		lookup:    lookup,
		cacheTTL:  60 * time.Second,
		cache:     make(map[string]cachedPrincipal),
		allowMock: allowMock,
	}
}

// Resolve. Returns (zero, false) if the token isn't a mock token, claims
// don't validate, or no tenant/role can be resolved. The cloud's
// ChainResolver decides what to do next on a miss (try the next resolver).
func (m *MockJWTResolver) Resolve(token string) (Principal, bool) {
	if !m.allowMock || !strings.HasPrefix(token, mockPrefix) {
		return Principal{}, false
	}

	m.mu.Lock()
	if hit, ok := m.cache[token]; ok && time.Now().Before(hit.expires) {
		p := hit.p
		m.mu.Unlock()
		return p, true
	}
	m.mu.Unlock()

	claims, err := decodeMockClaims(token)
	if err != nil {
		return Principal{}, false
	}
	if claims.Tid == "" || claims.Sub == "" {
		return Principal{}, false
	}

	// 1s deadline keeps a slow DB from holding the request indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if customerID, err := m.lookup.LookupCustomerByEntraTenant(ctx, claims.Tid); err == nil {
		role, err := m.lookup.ResolveRole(ctx, customerID, "", claims.Groups)
		if err != nil || role == "" {
			return Principal{}, false
		}
		p := Principal{
			UserID:     claims.Sub,
			Email:      claims.Email,
			Name:       claims.Name,
			CustomerID: customerID,
			Role:       role,
		}
		m.remember(token, p)
		return p, true
	}

	if vendorID, err := m.lookup.LookupVendorByEntraTenant(ctx, claims.Tid); err == nil {
		role, err := m.lookup.ResolveRole(ctx, "", vendorID, claims.Groups)
		if err != nil || role == "" {
			return Principal{}, false
		}
		p := Principal{
			UserID:   claims.Sub,
			Email:    claims.Email,
			Name:     claims.Name,
			Role:     role,
			IsVendor: true,
			// CustomerID intentionally empty — vendor sets it per-request via
			// X-Customer-Scope, handled by Middleware.
		}
		m.remember(token, p)
		return p, true
	}

	return Principal{}, false
}

func (m *MockJWTResolver) remember(token string, p Principal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[token] = cachedPrincipal{p: p, expires: time.Now().Add(m.cacheTTL)}
}

func decodeMockClaims(token string) (MockClaims, error) {
	body := strings.TrimPrefix(token, mockPrefix)
	if body == "" {
		return MockClaims{}, errors.New("empty mock token body")
	}
	// Accept both standard and URL-safe base64, with or without padding.
	var (
		raw []byte
		err error
	)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		raw, err = enc.DecodeString(body)
		if err == nil {
			break
		}
	}
	if err != nil {
		return MockClaims{}, err
	}
	var c MockClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return MockClaims{}, err
	}
	return c, nil
}

// ChainResolver tries its resolvers in order, returning the first successful
// match. Lets us run mock + static side-by-side: mock for everyday dev with
// realistic identity, static as a back-door for ops/smoke tests.
type ChainResolver struct {
	resolvers []Resolver
}

func NewChainResolver(rs ...Resolver) *ChainResolver {
	return &ChainResolver{resolvers: rs}
}

func (c *ChainResolver) Resolve(token string) (Principal, bool) {
	for _, r := range c.resolvers {
		if r == nil {
			continue
		}
		if p, ok := r.Resolve(token); ok {
			return p, true
		}
	}
	return Principal{}, false
}

// DBTenantLookup is the production TenantLookup backed by the admin pool.
// It does cross-tenant lookups (a request hasn't been scoped yet at this
// point) so the admin pool is the right pgxpool to read from.
type DBTenantLookup struct {
	pool *pgxpool.Pool
}

func NewDBTenantLookup(pool *pgxpool.Pool) *DBTenantLookup {
	return &DBTenantLookup{pool: pool}
}

func (d *DBTenantLookup) LookupCustomerByEntraTenant(ctx context.Context, tid string) (string, error) {
	var id string
	err := d.pool.QueryRow(ctx,
		`SELECT id::text FROM customers WHERE entra_tenant_id = $1`, tid).Scan(&id)
	return id, err
}

func (d *DBTenantLookup) LookupVendorByEntraTenant(ctx context.Context, tid string) (string, error) {
	var id string
	err := d.pool.QueryRow(ctx,
		`SELECT id::text FROM vendor_tenants WHERE entra_tenant_id = $1`, tid).Scan(&id)
	return id, err
}

// ResolveRole picks the strongest role across the caller's groups within the
// given scope. Ordering: admin > operator > viewer. A group not present in
// role_mappings contributes nothing.
func (d *DBTenantLookup) ResolveRole(ctx context.Context, customerID, vendorTenantID string, groups []string) (string, error) {
	if len(groups) == 0 {
		return "", nil
	}
	var role string
	var err error
	if customerID != "" {
		err = d.pool.QueryRow(ctx, `
			SELECT role FROM role_mappings
			 WHERE customer_id = $1 AND group_id = ANY($2)
			 ORDER BY CASE role WHEN 'admin' THEN 1 WHEN 'operator' THEN 2 ELSE 3 END
			 LIMIT 1`,
			customerID, groups).Scan(&role)
	} else {
		err = d.pool.QueryRow(ctx, `
			SELECT role FROM role_mappings
			 WHERE vendor_tenant_id = $1 AND group_id = ANY($2)
			 ORDER BY CASE role WHEN 'admin' THEN 1 WHEN 'operator' THEN 2 ELSE 3 END
			 LIMIT 1`,
			vendorTenantID, groups).Scan(&role)
	}
	if err != nil {
		// No rows is a soft miss (user has no mapped groups) — distinct from
		// a DB error. Both bubble up; the caller treats both as "no role".
		return "", err
	}
	return role, nil
}
