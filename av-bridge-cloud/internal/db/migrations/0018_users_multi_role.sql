-- 0018_users_multi_role.sql — relax the legacy users.role column so a
-- user can hold roles that aren't one of the three system defaults.
--
-- Before this migration users.role was NOT NULL with
-- CHECK (role IN ('admin','operator','viewer')). Slice 2's permission
-- engine reads capabilities from user_roles + role_permissions, so the
-- text column is now advisory only — kept for compatibility with the old
-- login response and for helpdesk filtering by "primary role", but no
-- longer authoritative.
--
-- Making it nullable + dropping the CHECK lets a user hold only custom
-- roles (empty text field) or a mix without the constraint firing. The
-- CRUD handlers still set the column to the first assigned role's name
-- when that role is a system default, so existing consumers of
-- users.role keep working with typical assignments.

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ALTER COLUMN role DROP NOT NULL;
