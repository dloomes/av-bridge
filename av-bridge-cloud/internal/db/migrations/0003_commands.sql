-- Slice 3: command queue. Portal POSTs a command → row sits as 'pending'; the
-- target collector's bridge pulls it on its next poll, executes via the local
-- adapter, and posts back the result. The portal sees a synchronous-looking
-- API: cloud waits up to a short timeout for the row to reach a terminal state,
-- then either returns the result inline or returns 202 + command_id for later
-- polling.
--
-- Reapply note: docker-entrypoint-initdb.d only runs on a fresh volume. To pick
-- up this migration on an existing dev env: `docker compose down -v` then up.

CREATE TABLE commands (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  uuid NOT NULL REFERENCES customers(id)  ON DELETE CASCADE,
    collector_id uuid NOT NULL REFERENCES collectors(id) ON DELETE CASCADE,
    device_id    uuid NOT NULL REFERENCES devices(id)    ON DELETE CASCADE,
    name         text NOT NULL,
    args         jsonb,
    status       text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','in_progress','succeeded','failed','cancelled')),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    claimed_at   timestamptz,
    completed_at timestamptz,
    result       jsonb,
    error        text,
    submitted_by text -- role today; user_id once auth lands
);

-- Tight partial index for the hot-path claim query.
CREATE INDEX commands_collector_pending_idx
    ON commands (collector_id, submitted_at)
    WHERE status = 'pending';

-- For the portal's poll-by-id wait loop.
CREATE INDEX commands_id_status_idx ON commands (id, status);

ALTER TABLE commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE commands FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON commands TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON commands
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
