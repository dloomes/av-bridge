-- 0017_rbac_admin_grants.sql — grant write access on the RBAC tables to
-- the app_admin role.
--
-- 0016 granted only SELECT on roles + role_permissions to app_admin,
-- expecting writes to flow through the app_tenant pool via WithTenant.
-- In practice the roles CRUD handlers (portalapi/roles.go) mirror how
-- /users CRUD works: the admin pool does both the lookup and the write,
-- explicitly filtering by customer_id in every WHERE clause. Consistent
-- with users + user_sessions; keeps the handlers simple and single-pool.
--
-- app_admin holds BYPASSRLS so the FORCE ROW LEVEL SECURITY on both
-- tables does not restrict it — the customer_id filter in the query is
-- the sole tenant-isolation guard. Reviewer note: any handler using
-- admin pool for a write to these tables MUST include the customer_id
-- filter, or a bug lets one tenant's admin mutate another tenant's
-- roles. Same risk profile as users / user_sessions.

GRANT INSERT, UPDATE, DELETE ON roles            TO app_admin;
GRANT INSERT,         DELETE ON role_permissions TO app_admin;
