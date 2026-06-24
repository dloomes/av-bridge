-- 0010_entra_rbac.sql — Entra ID auth + RBAC scaffolding.
--
-- Each customer can be tied to its own Entra (Azure AD) tenant — that's the
-- multi-tenant requirement: a customer's users sign in via their own Entra,
-- not a shared one. Group object IDs in the token map to roles via
-- role_mappings, scoped to either a customer OR a vendor tenant.
--
-- Vendor tenants are a separate concept: rows in vendor_tenants represent
-- support orgs (Involve / helpdesks) whose users can act across any customer
-- by passing X-Customer-Scope on each request. The current Principal
-- (extended in this slice) carries IsVendor so handlers can gate vendor-only
-- endpoints (e.g. the helpdesk customer list).
--
-- The scaffolding here is wire-compatible with real Entra: the mock JWT
-- resolver and a future real-OIDC resolver both populate the same Principal
-- shape and consult the same mappings.

ALTER TABLE customers ADD COLUMN IF NOT EXISTS entra_tenant_id text;
CREATE UNIQUE INDEX IF NOT EXISTS customers_entra_tenant_idx
    ON customers (entra_tenant_id) WHERE entra_tenant_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS vendor_tenants (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL,
    entra_tenant_id text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- role_mappings is the (group → role) lookup. Exactly one of customer_id or
-- vendor_tenant_id is set: the CHECK enforces that. The role is one of the
-- three values the app uses today; new roles need a CHECK update.
CREATE TABLE IF NOT EXISTS role_mappings (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id      uuid REFERENCES customers(id) ON DELETE CASCADE,
    vendor_tenant_id uuid REFERENCES vendor_tenants(id) ON DELETE CASCADE,
    group_id         text NOT NULL,
    role             text NOT NULL CHECK (role IN ('admin','operator','viewer')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (customer_id IS NOT NULL AND vendor_tenant_id IS NULL)
        OR
        (customer_id IS NULL AND vendor_tenant_id IS NOT NULL)
    )
);

-- Unique-per-scope: a single group only ever maps to one role within a given
-- customer/vendor tenant. Two partial unique indexes because the conditional
-- (customer vs vendor) can't fit into one constraint cleanly.
CREATE UNIQUE INDEX IF NOT EXISTS role_mappings_customer_group_idx
    ON role_mappings (customer_id, group_id) WHERE customer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS role_mappings_vendor_group_idx
    ON role_mappings (vendor_tenant_id, group_id) WHERE vendor_tenant_id IS NOT NULL;

-- Both lookup tables are read by the cloud's admin role (cross-tenant: a
-- request comes in carrying a tid, we have to look up which customer/vendor
-- it belongs to before we can scope anything). They're not RLS-enforced —
-- this is intentional, the data isn't per-tenant in the same sense as
-- devices/audit/etc.
GRANT SELECT ON vendor_tenants, role_mappings TO app_admin;
GRANT SELECT ON vendor_tenants, role_mappings TO app_tenant;
