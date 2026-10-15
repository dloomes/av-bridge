-- Remote access: people open a device's web interface from the portal,
-- relayed through the cloud and the device's collector (internal/remote).
--
-- A session is created by a portal user and opened in a browser tab on its
-- own address, <host_label>.<access domain>. The launch link carries a
-- one-time code, swapped on first use for a cookie on that address; only
-- hashes of either are stored. Sessions end after 15 minutes idle, 8 hours
-- at most, or when someone ends them.

CREATE TABLE remote_session (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id       uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    device_id         uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    user_id           uuid,
    actor             text NOT NULL,               -- who opened it, as in the audit log
    kind              text NOT NULL DEFAULT 'web' CHECK (kind IN ('web')),
    host_label        text NOT NULL UNIQUE,        -- first label of the session's address
    scheme            text NOT NULL CHECK (scheme IN ('http', 'https')),
    port              int  NOT NULL CHECK (port BETWEEN 1 AND 65535),
    launch_code_hash  bytea NOT NULL,
    cookie_hash       bytea,                       -- set when the launch link is used
    created_at        timestamptz NOT NULL DEFAULT now(),
    launched_at       timestamptz,
    last_activity_at  timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,        -- hard limit
    ended_at          timestamptz,
    end_reason        text,
    bytes_in          bigint NOT NULL DEFAULT 0,   -- device → browser
    bytes_out         bigint NOT NULL DEFAULT 0    -- browser → device
);

CREATE INDEX remote_session_customer_idx ON remote_session (customer_id, created_at DESC);
CREATE INDEX remote_session_open_idx ON remote_session (expires_at) WHERE ended_at IS NULL;

ALTER TABLE remote_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE remote_session FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON remote_session TO app_tenant, app_admin;
CREATE POLICY tenant_isolation ON remote_session
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- Administrators get the new permission; any custom role can be given it.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'device.remote_access'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'device.remote_access'
   );
