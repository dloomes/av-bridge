-- A portal tag edit stamps devices.config_updated_at (0064). Two follow-ups:
--
--  1. The stamp now bumps the Collector's config_version, so its long-poll
--     answers resync and it re-pulls within seconds, instead of on its
--     5-minute device sync. Ingest never writes config_updated_at, so this
--     can't cause resync storms the way tags (rewritten on every push)
--     would.
--  2. The config the Collector pulls carries the stamp, and its telemetry
--     sends it back; ingest takes tags only from telemetry made with the
--     current settings (see the ingest upsert). That replaces 0064's
--     3-minute window, which mixed the Collector's clock with ours.

DROP TRIGGER IF EXISTS devices_config_update ON devices;
CREATE TRIGGER devices_config_update
    AFTER UPDATE ON devices
    FOR EACH ROW
    WHEN (OLD.collector_id      IS DISTINCT FROM NEW.collector_id
       OR OLD.deleted_at        IS DISTINCT FROM NEW.deleted_at
       OR OLD.protocol          IS DISTINCT FROM NEW.protocol
       OR OLD.address           IS DISTINCT FROM NEW.address
       OR OLD.baud_rate         IS DISTINCT FROM NEW.baud_rate
       OR OLD.username_enc      IS DISTINCT FROM NEW.username_enc
       OR OLD.password_enc      IS DISTINCT FROM NEW.password_enc
       OR OLD.poll_rate_seconds IS DISTINCT FROM NEW.poll_rate_seconds
       OR OLD.commands          IS DISTINCT FROM NEW.commands
       OR OLD.subscriptions     IS DISTINCT FROM NEW.subscriptions
       OR OLD.config_updated_at IS DISTINCT FROM NEW.config_updated_at)
    EXECUTE FUNCTION devices_bump_collector_config();
