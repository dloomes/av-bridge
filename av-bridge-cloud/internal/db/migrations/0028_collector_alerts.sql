-- Tier 1 collector-offline alerts.
--
-- The alerts table was device-only: device_id NOT NULL, unique index on
-- (device_id, alert_key). Collectors don't have a device row, so we
-- extend the schema to make the subject polymorphic: either device_id
-- OR collector_id is populated, never both. Everything else about alerts
-- (severity, ack/resolve lifecycle, notify dispatch) stays the same.
--
-- Migration is additive + safe:
--   * device_id relaxed to NULL-able. Existing rows all have it set — the
--     NOT NULL constraint drop doesn't affect them.
--   * collector_id added, FK to collectors, nullable, CASCADE on delete
--     so cleaning up a collector cleans up its alerts.
--   * CHECK ensures exactly one subject is set per row.
--   * New partial unique index for open collector alerts, matching the
--     existing device one so re-fires update rather than duplicate.

ALTER TABLE alerts
    ADD COLUMN IF NOT EXISTS collector_id uuid REFERENCES collectors(id) ON DELETE CASCADE;

ALTER TABLE alerts
    ALTER COLUMN device_id DROP NOT NULL;

-- One-of-two subject check. Drop-if-exists so this migration is safe to
-- re-run in dev where devs might have edited history.
ALTER TABLE alerts
    DROP CONSTRAINT IF EXISTS alerts_subject_one_of;
ALTER TABLE alerts
    ADD CONSTRAINT alerts_subject_one_of
    CHECK ((device_id IS NOT NULL) <> (collector_id IS NOT NULL));

CREATE INDEX IF NOT EXISTS alerts_collector_idx
    ON alerts (collector_id) WHERE collector_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS alerts_open_collector_unique
    ON alerts (collector_id, alert_key)
    WHERE status = 'open' AND collector_id IS NOT NULL;

-- The collector-health watcher runs as app_admin (cross-tenant) and needs to
-- insert new collector_offline alerts and auto-resolve them when the bridge
-- returns. 0012 gave app_admin SELECT only.
GRANT INSERT, UPDATE ON alerts TO app_admin;
