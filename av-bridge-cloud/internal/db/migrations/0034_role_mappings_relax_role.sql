-- 0034_role_mappings_relax_role.sql — allow custom role names in mappings.
--
-- 0010 introduced role_mappings.role with a CHECK to the legacy three-role
-- set ('admin','operator','viewer'). 0016 replaced that model with a
-- per-customer roles catalogue; a customer can now define custom roles
-- like "nightshift-operator" or "readonly-audit". Group→role mappings
-- must be able to target those, not just the three legacy names.
--
-- Semantics after this migration:
--   * Vendor-scoped rows (vendor_tenant_id NOT NULL) continue to store the
--     legacy string ('admin' | 'operator' | 'viewer') in `role`. Vendors
--     don't have a per-tenant roles catalogue.
--   * Customer-scoped rows (customer_id NOT NULL) store the NAME of a row
--     from the customer's own `roles` table. Name is looked up
--     case-insensitively at JIT time; if it doesn't resolve, the JIT falls
--     back to 'viewer' via the legacy users.role text.
--
-- Uniqueness (a group only maps to one role within a given tenant) is
-- unchanged; only the value-space of `role` is broadened.

ALTER TABLE role_mappings DROP CONSTRAINT IF EXISTS role_mappings_role_check;

-- No new positive CHECK — the role name is validated at the API layer
-- (customer path: must exist in roles table; vendor path: must be one of
-- the legacy three). Enforcing the vendor rule in the DB would need
-- either a partial CHECK or a trigger, both worse than an application-
-- side guard here.

-- Seed the new 'role_mapping.manage' permission onto every existing admin
-- system-default role. New customers created after this migration get it
-- via seedSystemRoles() in db/customers.go — keep both in sync.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'role_mapping.manage'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'role_mapping.manage'
   );
