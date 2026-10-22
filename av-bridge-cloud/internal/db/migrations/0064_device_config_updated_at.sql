-- When a device's settings were last edited in the portal. Telemetry
-- carries the tags the device's Collector is running with, and the ingest
-- upsert copies them onto the device, so a reading taken just before an
-- edit (sent up to a push interval later, or replayed after an outage)
-- would put the old tags back. Ingest keeps the portal's tags for
-- telemetry from before the edit and for a short while after it, until
-- the Collector has restarted the device with its new settings.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS config_updated_at timestamptz;
