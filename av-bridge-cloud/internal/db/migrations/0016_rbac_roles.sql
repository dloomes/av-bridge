-- 0016_rbac_roles.sql — per-tenant role catalogue + multi-role users +
-- physical scope.
--
-- Replaces the 3-fixed-role model (users.role text CHECK 'admin|operator|
-- viewer') with a per-customer role catalogue, a many-to-many user_roles
-- join, and per-user physical scope (limit a user to a subset of the
-- tenant's buildings).
--
-- Effective permissions of a user = UNION of permissions across every role
-- the user holds. Physical scope is orthogonal to role and lives on the
-- user row so promoting/demoting doesn't require touching scope.
--
-- Three system-default roles ("admin", "operator", "viewer") are seeded
-- into every customer with the permission bundles the old hardcoded model
-- enforced. is_system_default rows are locked against editing/deletion at
-- the API layer — the schema doesn't enforce that so a migration can still
-- reshape them if needed.
--
-- users.role text is kept for now — the resolver still reads it as a
-- fallback until the permission engine ships in slice 2. A later migration
-- drops it once no code reads it.

-- Roles table — per-customer. name is unique inside the tenant.
CREATE TABLE roles (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id       uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name              text NOT NULL,
    description       text,
    is_system_default boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, name)
);
CREATE INDEX roles_customer_idx ON roles (customer_id);

-- Permissions catalogue lives in code (see portalauth/permissions.go).
-- Storing role → permission as text keeps the schema stable when new
-- permissions are added — no DDL required for a new capability.
CREATE TABLE role_permissions (
    role_id    uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission text NOT NULL,
    PRIMARY KEY (role_id, permission)
);
CREATE INDEX role_permissions_role_idx ON role_permissions (role_id);

-- Multi-role: a user may hold multiple roles; effective permissions are
-- the UNION. FK on role_id CASCADES: deleting a role drops all its
-- assignments (the API-layer guard prevents deleting a role that still
-- has assigned users unless the caller forces it — DB just cleans up).
CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX user_roles_role_idx ON user_roles (role_id);

-- Physical scope: NULL / empty array ⇒ full tenant; non-empty ⇒ limited
-- to those building_ids. Sits on users because a single user's scope is
-- orthogonal to whichever roles they hold — a London Viewer + London
-- Operator is one user with two roles and one scope, not two rows in a
-- user_role_scopes table.
ALTER TABLE users ADD COLUMN building_scope_ids uuid[];

-- Seed the three system-default roles into every existing customer.
-- Fresh customers created after this migration get seeded by
-- Store.CreateCustomer (see db/customers.go update in slice 1 backfill).
INSERT INTO roles (customer_id, name, description, is_system_default)
SELECT c.id, r.name, r.description, true
  FROM customers c
 CROSS JOIN (VALUES
    ('admin',    'Full tenant management: device + hierarchy + user + notification + firmware + role CRUD, plus all reads and controls.'),
    ('operator', 'Send commands, run bulk fan-out, acknowledge and resolve alerts, test notification channels.  All reads. No user/role management.'),
    ('viewer',   'Read-only monitoring. No control actions.')
 ) AS r(name, description);

-- Seed the permission bundles matching the old hardcoded RequireRole
-- gates. Permission keys are drawn from the catalogue in
-- portalauth/permissions.go — keep those two in sync when adding new
-- permissions.
--
-- Admin: everything.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
  FROM roles r
 CROSS JOIN (VALUES
    ('view.dashboard'),       ('view.audit'),          ('view.reports'),
    ('view.firmware'),        ('view.notifications'),  ('view.users'),
    ('command.device'),       ('command.bulk'),        ('reconnect.device'),
    ('device.crud'),
    ('alert.acknowledge'),    ('alert.resolve'),
    ('hierarchy.crud'),
    ('notification.crud'),    ('notification.test'),
    ('firmware_target.crud'),
    ('user.create'),          ('user.update'),
    ('user.reset_password'),  ('user.delete'),
    ('role.crud')
 ) AS p(permission)
 WHERE r.name = 'admin' AND r.is_system_default;

-- Operator: reads + control + alert lifecycle + notification test.
-- No user/role/notification/hierarchy management.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
  FROM roles r
 CROSS JOIN (VALUES
    ('view.dashboard'),       ('view.audit'),          ('view.reports'),
    ('view.firmware'),        ('view.notifications'),  ('view.users'),
    ('command.device'),       ('command.bulk'),        ('reconnect.device'),
    ('alert.acknowledge'),    ('alert.resolve'),
    ('notification.test')
 ) AS p(permission)
 WHERE r.name = 'operator' AND r.is_system_default;

-- Viewer: reads only.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
  FROM roles r
 CROSS JOIN (VALUES
    ('view.dashboard'),       ('view.audit'),          ('view.reports'),
    ('view.firmware'),        ('view.notifications'),  ('view.users')
 ) AS p(permission)
 WHERE r.name = 'viewer' AND r.is_system_default;

-- Backfill user_roles from existing users.role values. Every user that
-- currently has role='admin' gets assigned the admin system role in
-- their tenant, etc. Users in vendor_tenants (users.vendor_tenant_id
-- IS NOT NULL) are skipped — vendor users don't participate in the
-- customer role catalogue.
INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
  FROM users u
  JOIN roles r
    ON r.customer_id = u.customer_id
   AND r.name = u.role
   AND r.is_system_default
 WHERE u.customer_id IS NOT NULL;

-- Grants — same pattern as the other RBAC tables (users, vendor_tenants,
-- role_mappings): all reads live on the admin pool because the middleware
-- looks them up before tenant scope is known; writes go through
-- WithTenant so they respect RLS on the roles table (added below).
ALTER TABLE roles             ENABLE ROW LEVEL SECURITY;
ALTER TABLE roles             FORCE ROW LEVEL SECURITY;
ALTER TABLE role_permissions  ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permissions  FORCE ROW LEVEL SECURITY;
-- user_roles + users.building_scope_ids stay off RLS: user rows are
-- cross-tenant on the admin pool (login lookup), and user_roles inherits
-- scope from the users row it joins.

CREATE POLICY tenant_isolation_roles ON roles
    USING (customer_id::text = current_setting('app.current_customer', true));

CREATE POLICY tenant_isolation_role_permissions ON role_permissions
    USING (EXISTS (
        SELECT 1 FROM roles r
         WHERE r.id = role_permissions.role_id
           AND r.customer_id::text = current_setting('app.current_customer', true)
    ));

GRANT SELECT, INSERT, UPDATE, DELETE ON roles            TO app_tenant;
GRANT SELECT, INSERT,         DELETE ON role_permissions TO app_tenant;

-- Admin pool needs SELECT on everything for the permission-resolver: it
-- runs before the tenant pool is scoped, so it reads user_roles + roles +
-- role_permissions without setting app.current_customer.
GRANT SELECT ON roles, role_permissions, user_roles TO app_admin;
-- User-role assignment writes go through the admin pool because the
-- assignee's role_id lookup is customer-scoped but happens before the
-- caller has picked up a tenant transaction — same pattern as user CRUD.
GRANT INSERT, DELETE ON user_roles TO app_admin;
