-- Two Room Readiness additions.
--
-- 1. Room alerts. A scheduled health check (readiness run) or power cycle
--    that fails opens an alert on the room, naming what failed; the next
--    one that passes resolves it (nightly.syncRunAlerts). Alerts so far
--    were about one device or one Collector; a room is a third subject.
--    Notifications, ServiceNow incidents and the alert lists follow the
--    usual alert lifecycle.
--
-- 2. One-off schedule dates. A date can close a room, or the whole estate,
--    for the day (a bank holiday, a non-sitting day: no power-on, no
--    readiness check), or open it with its own times (a weekend sitting,
--    extended hours), whatever the weekly schedule says. A room's own date
--    wins over an estate-wide one for the same day.

-- ── Room alerts ────────────────────────────────────────────────────────────

ALTER TABLE alerts
    ADD COLUMN IF NOT EXISTS room_id uuid REFERENCES rooms(id) ON DELETE CASCADE;

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_subject_one_of;
ALTER TABLE alerts
    ADD CONSTRAINT alerts_subject_one_of
    CHECK (num_nonnulls(device_id, collector_id, room_id) = 1);

CREATE INDEX IF NOT EXISTS alerts_room_idx ON alerts (room_id) WHERE room_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS alerts_open_room_unique
    ON alerts (room_id, alert_key)
    WHERE status = 'open' AND room_id IS NOT NULL;

-- Site-limited users see a room's alerts when they can see the room, as
-- they see a device's alerts when they can see the device.
DROP POLICY IF EXISTS physical_scope ON alerts;
CREATE POLICY physical_scope ON alerts
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = alerts.device_id)
        OR EXISTS (SELECT 1 FROM rooms r WHERE r.id = alerts.room_id)
    );

-- Which finished runs have been reflected in room alerts. Runs already
-- finished are marked done, so history doesn't raise a burst of alerts.
ALTER TABLE nightly_run ADD COLUMN IF NOT EXISTS alert_checked_at timestamptz;
UPDATE nightly_run SET alert_checked_at = now()
 WHERE completed_at IS NOT NULL AND alert_checked_at IS NULL;
CREATE INDEX IF NOT EXISTS nightly_run_alert_pending_idx
    ON nightly_run (completed_at)
    WHERE completed_at IS NOT NULL AND alert_checked_at IS NULL;

-- ── One-off schedule dates ───────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS schedule_date (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id        uuid REFERENCES rooms(id) ON DELETE CASCADE, -- NULL: every room
    on_date        date NOT NULL,                               -- in the organisation's timezone
    closed         boolean NOT NULL,
    -- For an open date: its times, or NULL for the usual ones.
    power_on_time  time,
    power_off_time time,
    note           text NOT NULL DEFAULT '',
    created_by     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT closed OR (power_on_time IS NULL AND power_off_time IS NULL)),
    CHECK (power_on_time IS NULL OR power_off_time IS NULL OR power_on_time < power_off_time)
);

-- One entry per room per date, and one estate-wide entry per date.
CREATE UNIQUE INDEX IF NOT EXISTS schedule_date_room_unique
    ON schedule_date (room_id, on_date) WHERE room_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS schedule_date_estate_unique
    ON schedule_date (customer_id, on_date) WHERE room_id IS NULL;
CREATE INDEX IF NOT EXISTS schedule_date_customer_date_idx ON schedule_date (customer_id, on_date);

ALTER TABLE schedule_date ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_date FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON schedule_date
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- Site-limited users see their rooms' dates and the estate-wide ones.
CREATE POLICY physical_scope ON schedule_date
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR room_id IS NULL
        OR EXISTS (SELECT 1 FROM rooms r WHERE r.id = schedule_date.room_id)
    );

GRANT SELECT, INSERT, UPDATE, DELETE ON schedule_date TO app_tenant, app_admin;
