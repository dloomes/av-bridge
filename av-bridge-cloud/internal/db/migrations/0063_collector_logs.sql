-- Collector logs: each Collector sends its own log (Info and above) with
-- every push (bridge internal/logship). Kept 30 days from arrival by the
-- retention job (internal/retention); viewed on the Collectors page with
-- the view.collector_logs permission.
--
-- ts is the Collector's clock; inserted_at is ours, and is what retention
-- goes by, so a Collector with a wrong clock can't keep lines forever.
-- Like collectors themselves, logs are per organisation, not per site.

CREATE TABLE collector_logs (
    id           bigserial PRIMARY KEY,
    customer_id  uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    collector_id uuid NOT NULL REFERENCES collectors(id) ON DELETE CASCADE,
    ts           timestamptz NOT NULL,
    level        text NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARN', 'ERROR')),
    msg          text NOT NULL,
    attrs        jsonb,
    inserted_at  timestamptz NOT NULL DEFAULT now()
);

-- The read path: one Collector's lines, newest first.
CREATE INDEX collector_logs_collector_idx ON collector_logs (collector_id, id DESC);
CREATE INDEX collector_logs_inserted_at_brin ON collector_logs USING brin (inserted_at);

ALTER TABLE collector_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON collector_logs
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

GRANT SELECT, INSERT ON collector_logs TO app_tenant;
GRANT SELECT, INSERT, DELETE ON collector_logs TO app_admin;
GRANT USAGE, SELECT ON collector_logs_id_seq TO app_tenant, app_admin;

-- Administrators can read them; any custom role can be given it.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'view.collector_logs'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'view.collector_logs'
   );
