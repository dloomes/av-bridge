-- Roles + row-level security.
--
-- Two login roles:
--   app_tenant : RLS-enforced. ALL per-tenant data operations run as this role.
--   app_admin  : BYPASSRLS. Only for the collector-auth lookup, registration,
--                migrations-adjacent bootstrap, and helpdesk/cross-tenant work.
--
-- Tables are owned by the bootstrap superuser; the app never connects as the
-- owner. FORCE ROW LEVEL SECURITY is set anyway so policies apply even to the
-- owner — defence in depth.
--
-- Dev passwords are set here for Docker Compose. In production these roles'
-- credentials come from the deployment's secret store, not from this file.

DO $$
BEGIN
   IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_tenant') THEN
      CREATE ROLE app_tenant LOGIN PASSWORD 'app_tenant_dev';
   END IF;
   IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_admin') THEN
      CREATE ROLE app_admin LOGIN PASSWORD 'app_admin_dev' BYPASSRLS;
   END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_tenant, app_admin;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant, app_admin;

-- Enable + FORCE RLS on every tenant table.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['customers','regions','locations','buildings','rooms','collectors','devices','telemetry','events']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  END LOOP;
END
$$;

-- Tenant isolation policy. current_setting(..., true) returns NULL when the
-- session variable is unset → comparison is NULL → no rows match (fail-closed).
-- app_admin has BYPASSRLS, so these policies never restrict it.

-- customers is keyed on its own id; every other table on customer_id.
CREATE POLICY tenant_isolation ON customers
  USING       (id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON regions
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON locations
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON buildings
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON rooms
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON collectors
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON devices
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON telemetry
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON events
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
