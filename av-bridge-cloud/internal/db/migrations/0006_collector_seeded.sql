-- Slice 4 hotfix: track when the cloud has been seeded for a collector so a
-- bridge restart can't re-seed YAML on top of portal-side deletes.
--
-- Without this, PUT /bridge/config gates only on "collector currently has
-- devices > 0". If an operator deletes every device via the portal and the
-- bridge then restarts (clearing its in-memory `seeded` flag), the bridge's
-- next config-pull tick sees an empty cloud, treats it as a fresh deployment,
-- and re-seeds its local YAML — undoing every delete.
--
-- first_seeded_at is set the first time PUT /bridge/config succeeds for that
-- collector. Subsequent PUTs are rejected with 409 regardless of how many
-- devices the collector currently has. Re-seeding requires an operator to
-- clear this column explicitly (a deliberate, audit-loggable action).

ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS first_seeded_at timestamptz;
