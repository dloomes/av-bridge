-- 0048_servicenow_channel.sql — ServiceNow as a notification channel type.
--
-- A servicenow channel opens an incident when an alert first opens and
-- resolves it when the alert resolves. The channel row holds the instance
-- URL (target) and, in config, the integration user, the encrypted password
-- (password_enc, AES-GCM via the app cipher — never returned by the API) and
-- the incident field defaults.
--
-- alert_tickets links each alert to the incident it raised, one row per
-- (alert, channel). The dispatcher inserts it on open; the ticket-sync loop
-- resolves incidents whose alert has since resolved, and retries opens that
-- failed (state 'pending') while the alert is still open.

ALTER TABLE notification_channels DROP CONSTRAINT IF EXISTS notification_channels_type_check;
ALTER TABLE notification_channels
    ADD CONSTRAINT notification_channels_type_check
    CHECK (type IN ('email','teams','webhook','servicenow'));

CREATE TABLE alert_tickets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    alert_id     uuid NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    channel_id   uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    external_id  text,            -- ServiceNow sys_id
    number       text,            -- e.g. INC0010023
    state        text NOT NULL DEFAULT 'pending'
                 CHECK (state IN ('pending','open','resolved','skipped')),
    attempts     int  NOT NULL DEFAULT 0,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz,
    UNIQUE (alert_id, channel_id)
);

-- The sync loop only ever looks at unfinished tickets.
CREATE INDEX alert_tickets_active_idx
    ON alert_tickets (state) WHERE state IN ('pending','open');

ALTER TABLE alert_tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_tickets FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_alert_tickets ON alert_tickets
    USING (customer_id::text = current_setting('app.current_customer', true));

-- Tenants may read which incident an alert raised; only the dispatcher
-- (app_admin, cross-tenant) writes.
GRANT SELECT ON alert_tickets TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON alert_tickets TO app_admin;
