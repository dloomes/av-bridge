-- 0013_notification_channels.sql — per-customer outbound notification routes.
--
-- Each customer configures one or more channels (email/teams/webhook) that
-- alerts get dispatched to when an alert *first opens*. Repeat fires of an
-- already-open alert do not re-notify — that's the existing partial unique
-- index on alerts kicking in.
--
-- target carries the channel-specific address: an email address, a Teams
-- incoming-webhook URL, or a generic POST URL. config is reserved for
-- type-specific extras (auth headers, channel name overrides, etc) — empty
-- for v1 channels but future-proof.
--
-- min_severity gates delivery — a channel set to "critical" only receives
-- critical alerts; "warning" gets warning + critical; "info" gets all.
-- Sensible default is "warning" because info-level alerts (e.g. recovery)
-- generally don't need to wake anyone up.

CREATE TABLE notification_channels (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name          text NOT NULL,
    type          text NOT NULL CHECK (type IN ('email','teams','webhook')),
    target        text NOT NULL,
    config        jsonb,
    min_severity  text NOT NULL DEFAULT 'warning'
                  CHECK (min_severity IN ('info','warning','critical')),
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_sent_at  timestamptz,
    last_error    text
);

CREATE INDEX notification_channels_customer_idx
    ON notification_channels (customer_id);

ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_notification_channels ON notification_channels
    USING (customer_id::text = current_setting('app.current_customer', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON notification_channels TO app_tenant;
-- Admin reads only — dispatcher needs to read channels cross-tenant to
-- fan out alerts from the ingest path (which already runs as app_admin).
GRANT SELECT, UPDATE ON notification_channels TO app_admin;
