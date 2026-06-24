-- 0009_alerts.sql — persisted alerts with open/ack/resolved lifecycle.
--
-- Alerts are derived from "alert:*" events fired by the bridge. The events
-- table still stores the raw stream (immutable, used for the activity feed);
-- this table tracks alert state (acknowledged / resolved) so the portal can
-- show an actionable open-alert list.
--
-- Auto-resolve: when a `device_recovered` event arrives for a device, any
-- still-open `device_offline` or `device_degraded` alert for that device
-- gets resolved automatically. Acknowledge is a separate, manual step.

CREATE TABLE alerts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    device_id       uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    alert_key       text NOT NULL,
    severity        text NOT NULL CHECK (severity IN ('info','warning','critical')),
    message         text NOT NULL DEFAULT '',
    payload         jsonb,
    status          text NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open','acknowledged','resolved')),
    opened_at       timestamptz NOT NULL DEFAULT now(),
    acknowledged_at timestamptz,
    acknowledged_by text,
    resolved_at     timestamptz,
    resolved_by     text
);

CREATE INDEX alerts_customer_status_opened_idx
    ON alerts (customer_id, status, opened_at DESC);
CREATE INDEX alerts_device_idx
    ON alerts (device_id);

-- Unique partial index: at most one open alert per (device, alert_key). New
-- alerts of the same kind on the same device update the existing row instead
-- of creating duplicates, keeping the open list tractable.
CREATE UNIQUE INDEX alerts_open_unique
    ON alerts (device_id, alert_key)
    WHERE status = 'open';

ALTER TABLE alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE alerts FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_alerts ON alerts
    USING (customer_id::text = current_setting('app.current_customer', true));

GRANT SELECT, INSERT, UPDATE ON alerts TO app_tenant;
