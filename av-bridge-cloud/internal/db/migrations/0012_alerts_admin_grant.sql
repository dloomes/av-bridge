-- 0012_alerts_admin_grant.sql — fix missing grant on alerts.
--
-- 0009_alerts granted SELECT/INSERT/UPDATE only to app_tenant, but the
-- helpdesk overview reads alerts cross-tenant via the admin pool. app_admin
-- needs SELECT too. Mirrors the grant pattern other admin-readable tables
-- (devices, collectors, etc) get from the initial schema.

GRANT SELECT ON alerts TO app_admin;
