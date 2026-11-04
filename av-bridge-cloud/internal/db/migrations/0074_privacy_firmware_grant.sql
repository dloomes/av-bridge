-- GDPR data requests run as app_admin (BYPASSRLS) across one organisation.
-- firmware_targets (0014) was granted to app_tenant only, so the access
-- export could not read firmware_targets.updated_by and erasure could not
-- replace it. Every other table in the personal data register already has
-- these grants; privacy.TestAdminCanReadRegister keeps it that way.
GRANT SELECT, UPDATE ON firmware_targets TO app_admin;
