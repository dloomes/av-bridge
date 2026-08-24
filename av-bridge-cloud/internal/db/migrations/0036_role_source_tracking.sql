-- 0036_role_source_tracking.sql — origin-tracking for role grants (M3.1).
--
-- Enables strict-sync of Entra group→role mappings on every sign-in
-- without wiping manual admin promotions. Two columns:
--
--   * user_roles.granted_by — 'entra' | 'manual'. On every customer Entra
--     sign-in, all rows with granted_by='entra' for that user are wiped
--     and re-derived from current group memberships. Rows with
--     granted_by='manual' (whatever the /users edit UI wrote) survive.
--
--   * users.role_source — 'entra' | 'manual'. Vendor sign-in uses the
--     legacy users.role text (vendors have no per-tenant roles catalogue).
--     Strict-sync overwrites the row's role from mappings only when
--     role_source='entra'; a manually-promoted vendor helpdesk user
--     (role_source='manual', flipped by the coming vendor /users edit
--     path) keeps their role even if their group membership changes.
--
-- Both columns default to 'manual' — safest backfill: every existing row
-- is treated as manually-managed, so strict-sync landing later doesn't
-- wipe grants that were made before this migration. New entra-derived
-- grants explicitly set the source.

ALTER TABLE user_roles
    ADD COLUMN granted_by text NOT NULL DEFAULT 'manual'
        CHECK (granted_by IN ('entra', 'manual'));

CREATE INDEX user_roles_entra_idx ON user_roles (user_id) WHERE granted_by = 'entra';

ALTER TABLE users
    ADD COLUMN role_source text NOT NULL DEFAULT 'manual'
        CHECK (role_source IN ('entra', 'manual'));
