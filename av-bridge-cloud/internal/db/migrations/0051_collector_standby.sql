-- Warm-standby collectors (HA phase 3).
--
-- A standby is an ordinary collector row (own identity, secret, version,
-- self-update, health) with standby_for pointing at the collector whose
-- devices it can take over. The devices stay on the primary row; a
-- primary and its standbys form a group.
--
-- The cloud grants a renewable lease on the primary row to exactly one
-- machine of the group. Only the lease holder gets the device list and
-- commands. A holder that stops renewing loses the lease when it expires
-- and the next machine to poll takes over (failover). The bridge stops
-- polling devices well before its lease could expire (fencing), so two
-- machines never poll the same devices.
--
--   lease_holder       machine (collectors.id) holding the group lease
--   lease_expires_at   when it lapses unless renewed
--   lease_not_before   a handed-over lease starts no earlier than this,
--                      giving the previous holder time to stop polling
--   lease_handover_to  operator asked for this machine to become active
--   serving_seen_at    last poll by a standby while it held the lease —
--                      device freshness uses the later of this and the
--                      primary's own last_seen_at
ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS standby_for       uuid REFERENCES collectors(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS lease_holder      uuid REFERENCES collectors(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS lease_expires_at  timestamptz,
    ADD COLUMN IF NOT EXISTS lease_not_before  timestamptz,
    ADD COLUMN IF NOT EXISTS lease_handover_to uuid REFERENCES collectors(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS serving_seen_at   timestamptz;

ALTER TABLE collectors
    ADD CONSTRAINT collectors_standby_not_self CHECK (standby_for IS NULL OR standby_for <> id);

CREATE INDEX IF NOT EXISTS collectors_standby_for_idx
    ON collectors (standby_for) WHERE standby_for IS NOT NULL;
