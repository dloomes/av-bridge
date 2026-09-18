-- 0043_business_units.sql — optional Business Unit tier above Region.
--
-- Adds a business_units table one level above regions, wired in with a
-- nullable FK on regions so the addition is fully backward compatible:
-- customers who never create a BU see nothing change.
--
-- Gated by a per-customer boolean (customers.business_units_enabled). The
-- flag controls whether the portal SURFACES the BU concept — schema is
-- always present. Only vendor-side admin flips the flag today; if we
-- later want tenants to self-serve, we add a portal endpoint that gates
-- on a new permission. That decision is orthogonal to the schema.
--
-- User physical scope extends with business_unit_scope_ids alongside the
-- existing building_scope_ids. Both are orthogonal — a user restricted
-- to Business Unit X AND buildings [A, B] sees only rooms in {A, B} that
-- also sit under BU X. Empty on both = full-tenant.
--
-- New permission `business_unit.crud` — seeded on admin only (BUs are
-- top-of-hierarchy structural changes, not a routine operator action).

-- ── Feature flag ──────────────────────────────────────────────────────────

ALTER TABLE customers
    ADD COLUMN business_units_enabled boolean NOT NULL DEFAULT false;

-- ── Business units table ──────────────────────────────────────────────────

CREATE TABLE business_units (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, name)
);
CREATE INDEX business_units_customer_idx ON business_units (customer_id);

-- Auto-touch updated_at on any change.
CREATE OR REPLACE FUNCTION business_units_touch() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER business_units_updated_at
    BEFORE UPDATE ON business_units
    FOR EACH ROW EXECUTE FUNCTION business_units_touch();

-- ── Regions.business_unit_id ──────────────────────────────────────────────

-- Nullable so existing regions remain valid without any assignment. When a
-- BU is deleted, contained regions revert to unassigned rather than being
-- destroyed — regions are the real structural anchor for buildings/rooms
-- and losing them would cascade to devices.
ALTER TABLE regions
    ADD COLUMN business_unit_id uuid REFERENCES business_units(id) ON DELETE SET NULL;

CREATE INDEX regions_business_unit_idx ON regions (business_unit_id)
    WHERE business_unit_id IS NOT NULL;

-- ── Users.business_unit_scope_ids ─────────────────────────────────────────

-- Orthogonal to building_scope_ids. Empty / NULL = unscoped at BU level
-- (identical to today's building-scope semantics). Non-empty restricts
-- the caller to those BUs. Both scopes AND together in RLS.
ALTER TABLE users
    ADD COLUMN business_unit_scope_ids uuid[];

-- ── RLS on business_units ─────────────────────────────────────────────────

ALTER TABLE business_units ENABLE ROW LEVEL SECURITY;
ALTER TABLE business_units FORCE  ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON business_units TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON business_units
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- ── BU-scope RESTRICTIVE policies on the hierarchy ────────────────────────
--
-- Semantics: when app.business_unit_scope is empty, no filter. When
-- non-empty, a region is visible only if its BU is in the scope; unassigned
-- regions are hidden. Downstream tables filter transitively through the
-- EXISTS chain — locations via their region, buildings via their location,
-- rooms via their building. Because RLS applies to sub-queries inside
-- EXISTS, the existing building_scope policies on devices / telemetry /
-- events / alerts / commands / assets naturally reach through a BU-filtered
-- rooms view without needing per-table BU policies of their own.

CREATE POLICY business_unit_scope_regions ON regions
    AS RESTRICTIVE
    USING (
        current_setting('app.business_unit_scope', true) = ''
        OR (
            business_unit_id IS NOT NULL
            AND business_unit_id::text = ANY(
                string_to_array(current_setting('app.business_unit_scope', true), ',')
            )
        )
    );

CREATE POLICY business_unit_scope_locations ON locations
    AS RESTRICTIVE
    USING (
        current_setting('app.business_unit_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM regions r
             WHERE r.id = locations.region_id
               AND r.business_unit_id IS NOT NULL
               AND r.business_unit_id::text = ANY(
                   string_to_array(current_setting('app.business_unit_scope', true), ',')
               )
        )
    );

CREATE POLICY business_unit_scope_buildings ON buildings
    AS RESTRICTIVE
    USING (
        current_setting('app.business_unit_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM locations loc
              JOIN regions r ON r.id = loc.region_id
             WHERE loc.id = buildings.location_id
               AND r.business_unit_id IS NOT NULL
               AND r.business_unit_id::text = ANY(
                   string_to_array(current_setting('app.business_unit_scope', true), ',')
               )
        )
    );

CREATE POLICY business_unit_scope_rooms ON rooms
    AS RESTRICTIVE
    USING (
        current_setting('app.business_unit_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM buildings b
              JOIN locations loc ON loc.id = b.location_id
              JOIN regions r     ON r.id = loc.region_id
             WHERE b.id = rooms.building_id
               AND r.business_unit_id IS NOT NULL
               AND r.business_unit_id::text = ANY(
                   string_to_array(current_setting('app.business_unit_scope', true), ',')
               )
        )
    );

-- ── Permission catalogue ──────────────────────────────────────────────────

-- Seed `business_unit.crud` on the system-default admin role for every
-- existing customer. New customers get it via customers.go/seedSystemRoles.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'business_unit.crud'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'business_unit.crud'
   );
