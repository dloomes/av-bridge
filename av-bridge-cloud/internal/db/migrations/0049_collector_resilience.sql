-- Collector resilience (phase 1 + 2).
--
-- 1. Config change signalling. config_version is bumped by a trigger
--    whenever a device's adapter-relevant config, collector, or deleted
--    state changes. config_version_pulled records the version the bridge
--    last fetched via /bridge/config. /bridge/poll answers resync=true
--    while they differ, so the bridge re-pulls within seconds instead of
--    waiting for its 5-minute config tick. Counters, not timestamps: the
--    puller reads the version BEFORE reading devices, so a change that
--    commits mid-pull can only cause one extra pull, never a missed one.
--
-- 2. Device move tombstones. When a device moves between collectors, the
--    old collector may keep reporting it for a while (until its next
--    config pull, or when it replays spooled telemetry after an outage).
--    Without a guard, ingest's upsert would recreate the device on the
--    old collector as a duplicate. A tombstone row tells ingest not to
--    auto-create (collector_id, reported_id).

ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS config_version        bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS config_version_pulled bigint NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION devices_bump_collector_config() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE collectors SET config_version = config_version + 1 WHERE id = OLD.collector_id;
        PERFORM pg_notify('cmd_pending', OLD.collector_id::text);
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND NEW.collector_id IS DISTINCT FROM OLD.collector_id) THEN
        UPDATE collectors SET config_version = config_version + 1 WHERE id = NEW.collector_id;
        PERFORM pg_notify('cmd_pending', NEW.collector_id::text);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER devices_config_insert
    AFTER INSERT ON devices
    FOR EACH ROW EXECUTE FUNCTION devices_bump_collector_config();

CREATE TRIGGER devices_config_delete
    AFTER DELETE ON devices
    FOR EACH ROW EXECUTE FUNCTION devices_bump_collector_config();

-- Only the columns the bridge's adapter cares about (see hub's
-- adapterRelevantChange) plus collector_id / deleted_at. Ingest rewrites
-- name/type/tags and the observation columns on every push, so those must
-- not fire this.
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
       OR OLD.subscriptions     IS DISTINCT FROM NEW.subscriptions)
    EXECUTE FUNCTION devices_bump_collector_config();

CREATE TABLE IF NOT EXISTS collector_device_tombstones (
    customer_id  uuid NOT NULL REFERENCES customers(id)  ON DELETE CASCADE,
    collector_id uuid NOT NULL REFERENCES collectors(id) ON DELETE CASCADE,
    reported_id  text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collector_id, reported_id)
);

ALTER TABLE collector_device_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_device_tombstones FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON collector_device_tombstones TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON collector_device_tombstones
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
