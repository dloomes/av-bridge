-- Slice 5: audit log. Every portal-driven write appends a row here so there's
-- a tenant-scoped paper trail of who changed what and when. Read by Customer
-- Admins (and eventually Helpdesk for cross-customer ops) via /api/v1/audit.
--
-- actor: today this is the Principal's Role string ("admin" / "operator");
--        when real user auth lands the same column will hold user id/email.
-- action: dotted lowercase namespace, e.g. "device.create", "command.submit".
-- target_kind / target_id: what was acted on. target_id is TEXT (not UUID)
--                          because some actions act on collections or use a
--                          non-UUID identifier.
-- before / after: JSON snapshots of the row, with credential columns excluded
--                 by the application layer — audit must not leak even
--                 ciphertext secrets.
-- metadata: open-ended bag for future enrichments (request id, IP, etc.).
--
-- Retention is an ops policy, not enforced in schema. Strategies (partition,
-- vacuum, archive to cold storage) can layer on later.

CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    actor       text NOT NULL,
    action      text NOT NULL,
    target_kind text NOT NULL,
    target_id   text,
    before      jsonb,
    "after"     jsonb,
    metadata    jsonb,
    ts          timestamptz NOT NULL DEFAULT now()
);

-- Customer feed (the common read pattern: most recent N for this tenant).
CREATE INDEX audit_log_customer_ts_idx
    ON audit_log (customer_id, ts DESC);

-- Per-target history ("show me everything that happened to this device").
CREATE INDEX audit_log_target_idx
    ON audit_log (customer_id, target_kind, target_id, ts DESC)
    WHERE target_id IS NOT NULL;

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT ON audit_log TO app_tenant, app_admin;
GRANT USAGE, SELECT ON audit_log_id_seq TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON audit_log
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
