-- Integrations: devices managed through a vendor's cloud (Poly Lens) rather
-- than a Collector, plus the per-customer integration, its linked devices
-- and the alert state for those devices.

-- 1. Devices without a Collector. source says who reports the device; a
--    Collector device must have a collector and a Lens device must not.
ALTER TABLE devices ALTER COLUMN collector_id DROP NOT NULL;
ALTER TABLE devices
    ADD COLUMN source   text NOT NULL DEFAULT 'collector'
        CONSTRAINT devices_source_check CHECK (source IN ('collector', 'poly_lens')),
    ADD COLUMN platform text
        CONSTRAINT devices_platform_check CHECK (platform IN ('windows', 'android'));
ALTER TABLE devices
    ADD CONSTRAINT devices_source_collector_check
        CHECK ((source = 'collector') = (collector_id IS NOT NULL));

-- 2. The config-version trigger (0049, fired by 0065) assumed a collector.
--    A device with no collector has nothing to bump or notify.
CREATE OR REPLACE FUNCTION devices_bump_collector_config() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.collector_id IS NOT NULL THEN
        UPDATE collectors SET config_version = config_version + 1 WHERE id = OLD.collector_id;
        PERFORM pg_notify('cmd_pending', OLD.collector_id::text);
    END IF;
    IF (TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND NEW.collector_id IS DISTINCT FROM OLD.collector_id))
       AND NEW.collector_id IS NOT NULL THEN
        UPDATE collectors SET config_version = config_version + 1 WHERE id = NEW.collector_id;
        PERFORM pg_notify('cmd_pending', NEW.collector_id::text);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- 3. The integration itself: one per customer and provider. The client
--    secret is encrypted (secrets.Cipher) and never returned by any API.
CREATE TABLE integrations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    provider        text NOT NULL CHECK (provider IN ('poly_lens')),
    status          text NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'disconnected', 'error')),
    client_id       text,
    secret_enc      bytea,
    account_id      text,
    account_name    text,
    allow_tagging   boolean NOT NULL DEFAULT false,
    last_success_at timestamptz,
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    created_by      text,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      text,
    UNIQUE (customer_id, provider)
);

-- 4. Which external device each linked device is.
CREATE TABLE integration_devices (
    device_id      uuid PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    external_id    text NOT NULL,
    external_name  text,
    serial_number  text,
    mac_address    text,
    model          text,
    tag_state      text NOT NULL DEFAULT 'none' CHECK (tag_state IN ('none', 'tagged', 'failed', 'left_behind')),
    tag_error      text,
    UNIQUE (integration_id, external_id)
);
CREATE INDEX integration_devices_customer_idx ON integration_devices (customer_id);

-- 5. How long each alert condition has held for a device, and the alert it opened.
CREATE TABLE integration_alert_state (
    device_id   uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    condition   text NOT NULL,
    since       timestamptz NOT NULL,
    alert_id    uuid REFERENCES alerts(id) ON DELETE SET NULL,
    PRIMARY KEY (device_id, condition)
);
CREATE INDEX integration_alert_state_customer_idx ON integration_alert_state (customer_id);

-- 6. Row-level security, as 0056. The tenant API writes integration rows
--    (as 0013's channels); alert state is written by the background job.
ALTER TABLE integrations            ENABLE ROW LEVEL SECURITY;
ALTER TABLE integrations            FORCE  ROW LEVEL SECURITY;
ALTER TABLE integration_devices     ENABLE ROW LEVEL SECURITY;
ALTER TABLE integration_devices     FORCE  ROW LEVEL SECURITY;
ALTER TABLE integration_alert_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE integration_alert_state FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON integrations
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
CREATE POLICY tenant_isolation ON integration_devices
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
CREATE POLICY tenant_isolation ON integration_alert_state
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON integrations, integration_devices TO app_tenant;
GRANT SELECT ON integration_alert_state TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON integrations, integration_devices, integration_alert_state TO app_admin;

-- 7. Administrators get the new permission; any custom role can be given it.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'integration.manage'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'integration.manage'
   );
