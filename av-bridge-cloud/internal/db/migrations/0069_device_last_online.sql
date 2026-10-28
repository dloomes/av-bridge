-- devices.last_online_at: when the device last answered its Collector,
-- shown on the device page as "Last contacted". last_seen_at can't say
-- this: a failed poll still sends telemetry (status offline), so it moves
-- on every poll whether the device answered or not. Ingest sets
-- last_online_at only from online telemetry.

ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_online_at timestamptz;

-- Backfill. devices and telemetry have FORCE ROW LEVEL SECURITY, and the
-- migration role (the RDS master) doesn't bypass it, so lift FORCE inside
-- this transaction (see 0068) and put it back before commit.
ALTER TABLE devices NO FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry NO FORCE ROW LEVEL SECURITY;

-- Online now: its last report is its last contact.
UPDATE devices SET last_online_at = last_seen_at
 WHERE last_online_at IS NULL AND latest_status = 'online' AND last_seen_at IS NOT NULL;

-- Otherwise its last online reading in the past week, if any (one index
-- range per device, telemetry_device_ts_idx). Older than that shows as
-- unknown until the device next answers.
UPDATE devices d
   SET last_online_at = (
        SELECT ts FROM telemetry
         WHERE device_id = d.id AND status = 'online' AND ts > now() - interval '7 days'
         ORDER BY ts DESC
         LIMIT 1)
 WHERE d.last_online_at IS NULL AND d.deleted_at IS NULL;

ALTER TABLE telemetry FORCE ROW LEVEL SECURITY;
ALTER TABLE devices FORCE ROW LEVEL SECURITY;
