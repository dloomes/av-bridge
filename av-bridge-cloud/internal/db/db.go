package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds two connection pools with different DB roles:
//
//	admin  — app_admin (BYPASSRLS): collector auth lookup, registration, bootstrap.
//	tenant — app_tenant (RLS-enforced): all per-tenant data operations, only ever
//	         used inside WithTenant so the RLS session variable is set.
type Store struct {
	admin  *pgxpool.Pool
	tenant *pgxpool.Pool
}

func New(ctx context.Context, adminDSN, tenantDSN string) (*Store, error) {
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("admin pool: %w", err)
	}
	tenant, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		admin.Close()
		return nil, fmt.Errorf("tenant pool: %w", err)
	}
	return &Store{admin: admin, tenant: tenant}, nil
}

func (s *Store) Close() {
	s.admin.Close()
	s.tenant.Close()
}

// AdminPool exposes the BYPASSRLS pool for cross-tenant work (registration,
// helpdesk). Use sparingly and never for routine per-tenant data.
func (s *Store) AdminPool() *pgxpool.Pool { return s.admin }

// WaitReady pings the DB until it answers or the deadline passes. Compose may
// start this service before Postgres is accepting connections.
func (s *Store) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := s.admin.Ping(ctx); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("database not ready within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// WithTenant runs fn in a transaction scoped to customerID via the RLS
// session variable. Building scope defaults to empty (full-tenant access) —
// bridge/ingest callers have no user Principal and need to write across
// every device.  Portal handlers should use WithTenantScoped so a user's
// building_scope_ids restricts what their queries can see.
func (s *Store) WithTenant(ctx context.Context, customerID string, fn func(pgx.Tx) error) error {
	return s.WithTenantScoped(ctx, customerID, nil, fn)
}

// WithTenantScoped is WithTenant + a physical-scope restriction. When
// buildingScope is empty/nil the caller sees every row in their tenant
// (identical to WithTenant). When non-empty, the RESTRICTIVE policies
// added in migration 0019 filter devices + telemetry + events + alerts +
// commands to rows that hang off one of those buildings.
//
// Session vars are set with is_local=true so they revert at COMMIT /
// ROLLBACK. A subsequent WithTenant call on the same pooled connection
// starts fresh (empty scope). Reviewer note: never pass user-controlled
// building_scope_ids without validating them against the caller's tenant
// — an escape by planting foreign UUIDs would defeat the whole point.
func (s *Store) WithTenantScoped(ctx context.Context, customerID string, buildingScope []string, fn func(pgx.Tx) error) error {
	tx, err := s.tenant.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_customer', $1, true)", customerID); err != nil {
		return fmt.Errorf("set tenant scope: %w", err)
	}
	// Comma-joined into a single string because Postgres GUCs are scalar.
	// Empty string in the RLS policy short-circuits to "unscoped".
	if _, err := tx.Exec(ctx, "SELECT set_config('app.building_scope', $1, true)", strings.Join(buildingScope, ",")); err != nil {
		return fmt.Errorf("set building scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Collector is the result of an auth lookup.
type Collector struct {
	ID         string
	CustomerID string
	SecretEnc  []byte
}

// TouchCollector stamps last_seen_at = now() for the given collector. Called
// after every successful bridge auth so portal-side health checks can render
// real online/offline state instead of "unknown". Uses the admin pool because
// we want this write regardless of which (or whether any) tenant variable is
// set in the current connection.
func (s *Store) TouchCollector(ctx context.Context, id string) error {
	_, err := s.admin.Exec(ctx, `UPDATE collectors SET last_seen_at = now() WHERE id = $1`, id)
	return err
}

// MarkCollectorSeen records a fresh /ingest — same last_seen_at bump as
// TouchCollector, plus the bridge's self-reported version and build time
// when supplied. Empty inputs are preserved via COALESCE(NULLIF(...), …)
// so a pre-Tier-1 bridge that doesn't report these fields doesn't wipe
// values that a later build set.
func (s *Store) MarkCollectorSeen(ctx context.Context, id, version, buildTime string) error {
	_, err := s.admin.Exec(ctx, `
		UPDATE collectors
		   SET last_seen_at       = now(),
		       bridge_version     = COALESCE(NULLIF($2, ''), bridge_version),
		       bridge_build_time  = COALESCE(NULLIF($3, '')::timestamptz, bridge_build_time)
		 WHERE id = $1`, id, version, buildTime)
	return err
}

// TouchCollectorConfigPull stamps last_config_pull_at = now(). Called from
// the /bridge/config GET handler so the /collectors page can distinguish
// "bridge is pushing telemetry but hasn't refreshed its config in a while"
// from "bridge is fully healthy".
func (s *Store) TouchCollectorConfigPull(ctx context.Context, id string) error {
	_, err := s.admin.Exec(ctx, `UPDATE collectors SET last_config_pull_at = now() WHERE id = $1`, id)
	return err
}

// LookupCollectorByBridgeID resolves the id the bridge reports to a collector
// row. Runs as app_admin (cross-tenant) — we don't know the customer yet.
func (s *Store) LookupCollectorByBridgeID(ctx context.Context, bridgeID string) (Collector, error) {
	var c Collector
	err := s.admin.QueryRow(ctx,
		`SELECT id::text, customer_id::text, hmac_secret_enc
		   FROM collectors WHERE bridge_collector_id = $1`,
		bridgeID).Scan(&c.ID, &c.CustomerID, &c.SecretEnc)
	return c, err
}

// PoC fixed UUIDs so the seed is idempotent and referenceable.
const (
	pocCustomerID = "11111111-1111-1111-1111-111111111111"
	pocRegionID   = "22222222-2222-2222-2222-222222222222"
	pocLocationID = "33333333-3333-3333-3333-333333333333"
	pocBuildingID = "44444444-4444-4444-4444-444444444444"
	pocRoomID     = "55555555-5555-5555-5555-555555555555"
	pocCollectorID = "66666666-6666-6666-6666-666666666666"

	// PocEntraTenantID is the Entra tenant the POC customer's users live in
	// for the mock-JWT dev flow. Real deployments set this per-customer via
	// admin tooling; for POC we hardcode + seed.
	PocEntraTenantID    = "tenant-acme-poc"
	PocVendorTenantID   = "tenant-involve-vendor"
	pocVendorTenantUUID = "77777777-7777-7777-7777-777777777777"
)

// PocVendorTenantUUID exposes the fixed vendor tenant UUID so callers outside
// the db package (e.g. main.go's vendor-admin seed) can reference the same
// row BootstrapPoC creates.
func PocVendorTenantUUID() string { return pocVendorTenantUUID }

// BootstrapPoC idempotently seeds one tenant's full hierarchy plus a collector
// whose HMAC secret matches the on-prem bridge. Runs as app_admin (BYPASSRLS).
// Re-running updates the collector secret (rotation) but leaves the rest.
func (s *Store) BootstrapPoC(ctx context.Context, cipher secrets.Cipher, bridgeID, customerName, hmacSecret string) error {
	enc, err := cipher.Encrypt([]byte(hmacSecret))
	if err != nil {
		return fmt.Errorf("encrypt poc secret: %w", err)
	}

	tx, err := s.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO customers (id, name, entra_tenant_id) VALUES ($1, $2, $3)
		  ON CONFLICT (id) DO UPDATE SET entra_tenant_id = EXCLUDED.entra_tenant_id`,
			[]any{pocCustomerID, customerName, PocEntraTenantID}},
		{`INSERT INTO regions (id, customer_id, name) VALUES ($1, $2, 'Default Region') ON CONFLICT (id) DO NOTHING`,
			[]any{pocRegionID, pocCustomerID}},
		{`INSERT INTO locations (id, customer_id, region_id, name) VALUES ($1, $2, $3, 'Default Location') ON CONFLICT (id) DO NOTHING`,
			[]any{pocLocationID, pocCustomerID, pocRegionID}},
		{`INSERT INTO buildings (id, customer_id, location_id, name) VALUES ($1, $2, $3, 'Default Building') ON CONFLICT (id) DO NOTHING`,
			[]any{pocBuildingID, pocCustomerID, pocLocationID}},
		{`INSERT INTO rooms (id, customer_id, building_id, name) VALUES ($1, $2, $3, 'Default Room') ON CONFLICT (id) DO NOTHING`,
			[]any{pocRoomID, pocCustomerID, pocBuildingID}},
		{`INSERT INTO collectors (id, customer_id, building_id, bridge_collector_id, name, hmac_secret_enc)
		    VALUES ($1, $2, $3, $4, 'PoC Collector', $5)
		  ON CONFLICT (id) DO UPDATE SET bridge_collector_id = EXCLUDED.bridge_collector_id,
		                                 hmac_secret_enc = EXCLUDED.hmac_secret_enc`,
			[]any{pocCollectorID, pocCustomerID, pocBuildingID, bridgeID, enc}},

		// Customer-side role mappings — group names match the mock-JWT
		// presets used by the dev portal sign-in screen.
		{`INSERT INTO role_mappings (customer_id, group_id, role) VALUES ($1, 'poc-admins', 'admin')
		  ON CONFLICT (customer_id, group_id) WHERE customer_id IS NOT NULL DO NOTHING`,
			[]any{pocCustomerID}},
		{`INSERT INTO role_mappings (customer_id, group_id, role) VALUES ($1, 'poc-operators', 'operator')
		  ON CONFLICT (customer_id, group_id) WHERE customer_id IS NOT NULL DO NOTHING`,
			[]any{pocCustomerID}},
		{`INSERT INTO role_mappings (customer_id, group_id, role) VALUES ($1, 'poc-viewers', 'viewer')
		  ON CONFLICT (customer_id, group_id) WHERE customer_id IS NOT NULL DO NOTHING`,
			[]any{pocCustomerID}},

		// Vendor (Involve / helpdesk) tenant + admin group mapping.
		{`INSERT INTO vendor_tenants (id, name, entra_tenant_id)
		  VALUES ($1, 'Involve (Vendor)', $2) ON CONFLICT (entra_tenant_id) DO NOTHING`,
			[]any{pocVendorTenantUUID, PocVendorTenantID}},
		{`INSERT INTO role_mappings (vendor_tenant_id, group_id, role) VALUES ($1, 'helpdesk-admins', 'admin')
		  ON CONFLICT (vendor_tenant_id, group_id) WHERE vendor_tenant_id IS NOT NULL DO NOTHING`,
			[]any{pocVendorTenantUUID}},
	}
	for _, st := range stmts {
		if _, err := tx.Exec(ctx, st.sql, st.args...); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
	}
	return tx.Commit(ctx)
}
