-- Which collectors have a remote-access tunnel open. Tunnels end on the
-- worker copy, but the portal API runs on web copies, so presence lives in
-- the database where every copy can read it. The worker clears the table at
-- startup (its tunnels are gone) and removes a row when its tunnel closes.
CREATE TABLE remote_tunnel (
    collector_id uuid PRIMARY KEY REFERENCES collectors(id) ON DELETE CASCADE,
    connected_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON remote_tunnel TO app_admin;
