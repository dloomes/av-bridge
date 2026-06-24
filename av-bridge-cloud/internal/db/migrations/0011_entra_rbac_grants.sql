-- 0011_entra_rbac_grants.sql — fix grants from 0010.
--
-- 0010 only granted SELECT to both roles. POC bootstrap runs as app_admin
-- and needs to INSERT/UPDATE vendor_tenants + role_mappings to seed the dev
-- environment, so grant the missing writes here. Separate migration rather
-- than a 0010 amendment so the version log stays linear.

GRANT INSERT, UPDATE, DELETE ON vendor_tenants TO app_admin;
GRANT INSERT, UPDATE, DELETE ON role_mappings TO app_admin;
