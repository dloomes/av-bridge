-- Devices Room Readiness has switched off for the night.
--
-- Set by the nightly scheduler on each device that accepted the scheduled
-- power-off, and cleared when the run finishes its morning warm-up (or
-- ends any other way). While set, the device's effective status reads
-- 'switched_off' instead of offline, and offline alerts are held back.
-- ON DELETE SET NULL: removing old runs can never leave a device asleep.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS switched_off_by_run uuid REFERENCES nightly_run(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS devices_switched_off_by_run_idx
    ON devices (switched_off_by_run)
    WHERE switched_off_by_run IS NOT NULL;
