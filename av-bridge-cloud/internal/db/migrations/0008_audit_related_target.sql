-- Slice 5.1: cross-reference audit entries to a secondary subject.
--
-- Some portal actions affect more than one entity. The primary case is
-- command.submit — its target is the new command queue row, but operators
-- looking at "what happened to this device" want to see those commands too.
--
-- target_kind / target_id stay the primary (what was created/modified).
-- related_target_kind / related_target_id are the secondary entity. The
-- read endpoint OR-matches on both when filtering, so a device activity
-- feed picks up entries where the device is either target or related.
--
-- Kept generic (not "device_id") so future actions can cross-reference
-- collectors, rooms, etc. without another migration.

ALTER TABLE audit_log
    ADD COLUMN IF NOT EXISTS related_target_kind text,
    ADD COLUMN IF NOT EXISTS related_target_id   text;

-- Partial index — most rows don't have a related target, no point indexing them.
CREATE INDEX IF NOT EXISTS audit_log_related_target_idx
    ON audit_log (customer_id, related_target_kind, related_target_id, ts DESC)
    WHERE related_target_id IS NOT NULL;
