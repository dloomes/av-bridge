-- Telemetry rollups and retention (internal/telemetry).
--
-- Raw telemetry is a reading per device per poll: at 50,000 devices polled
-- every minute, ~72 million rows a day. Reports read a daily summary per
-- device instead, and raw readings are kept only for TELEMETRY_RETENTION_DAYS
-- (default 90, as the Service Description commits), long enough for device
-- history and readiness checks.
--
-- telemetry_daily holds, per device and UTC day, the sample counts behind
-- uptime and power, and 24-bit masks of the UTC hours with a call, with
-- people in the room, and with a display on (online readings only), so
-- Room usage can still count hours per room. Summaries are kept 400 days,
-- past the longest report window (365).

-- When each raw reading arrived, so the rollup job picks up readings that
-- arrive late (a Collector replaying its buffer after an outage). Existing
-- rows stay NULL; the job backfills them by reading time.
ALTER TABLE telemetry ADD COLUMN IF NOT EXISTS inserted_at timestamptz;
ALTER TABLE telemetry ALTER COLUMN inserted_at SET DEFAULT now();

-- Append-only, so block-range indexes stay tiny and still find ranges fast.
CREATE INDEX IF NOT EXISTS telemetry_inserted_at_brin ON telemetry USING brin (inserted_at);
CREATE INDEX IF NOT EXISTS telemetry_ts_brin ON telemetry USING brin (ts);

CREATE TABLE telemetry_daily (
    customer_id       uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    device_id         uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    day               date NOT NULL,              -- UTC
    samples           int  NOT NULL DEFAULT 0,    -- readings
    online_samples    int  NOT NULL DEFAULT 0,    -- readings with status online
    call_hours        int  NOT NULL DEFAULT 0,    -- bit h: a call during UTC hour h
    occupied_hours    int  NOT NULL DEFAULT 0,    -- bit h: people in the room
    display_on_hours  int  NOT NULL DEFAULT 0,    -- bit h: power_status on / active
    usage_signal      boolean NOT NULL DEFAULT false, -- reported active_calls or people_count
    peak_people       int,
    PRIMARY KEY (device_id, day)
);

CREATE INDEX telemetry_daily_customer_day_idx ON telemetry_daily (customer_id, day);

ALTER TABLE telemetry_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry_daily FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON telemetry_daily TO app_tenant, app_admin;
CREATE POLICY tenant_isolation ON telemetry_daily
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
-- Visible when the device is visible, as for telemetry (migration 0047).
CREATE POLICY physical_scope ON telemetry_daily
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = telemetry_daily.device_id)
    );

-- Where the rollup job has got to: rolled_to for readings by arrival time,
-- backfill_to for readings from before this migration (inserted_at NULL),
-- by reading time; backfill_done once it has caught up.
CREATE TABLE telemetry_rollup_state (
    id             int PRIMARY KEY CHECK (id = 1),
    rolled_to      timestamptz NOT NULL DEFAULT 'epoch',
    backfill_to    timestamptz,
    backfill_done  boolean NOT NULL DEFAULT false
);
INSERT INTO telemetry_rollup_state (id) VALUES (1) ON CONFLICT DO NOTHING;
GRANT SELECT, UPDATE ON telemetry_rollup_state TO app_admin;
