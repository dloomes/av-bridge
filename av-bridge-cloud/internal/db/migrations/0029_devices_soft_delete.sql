-- Soft-delete for devices.
--
-- Reason: the bridge polls its device list from /bridge/config on a
-- ~5-minute cycle, but pushes telemetry every 30s. When an operator
-- deletes a device in the portal, the bridge keeps polling it (and
-- pushing telemetry) until the next config-pull. The ingest handler
-- was a blind UPSERT — it saw "unknown device", INSERTed a fresh row
-- with a new UUID, and the deleted device reappeared in the UI within
-- seconds. Every subsequent telemetry push then updated the resurrected
-- row until the bridge finally caught up.
--
-- Fix: soft-delete. DELETE marks deleted_at = now() rather than removing
-- the row. Ingest's ON CONFLICT DO UPDATE gets a WHERE deleted_at IS NULL
-- guard so a resurrected device stays deleted. Bridge stops polling on
-- next config-pull as before. Portal reads filter deleted rows so users
-- see the delete take effect immediately.
--
-- Migration is additive + safe:
--   * deleted_at added, nullable, defaults NULL — existing rows are all
--     live and remain visible.
--   * Partial index on non-deleted rows keeps the hot path fast; the
--     existing UNIQUE (collector_id, reported_id) index is kept as-is
--     so ON CONFLICT continues to match against soft-deleted rows
--     (that's what stops the resurrection).
--
-- Physical cleanup is left to a future maintenance job — once the row
-- has been deleted for N days and no telemetry has arrived, it can be
-- hard-deleted. Not urgent; storage is cheap.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

-- Partial index over live rows only — every portal SELECT will filter
-- WHERE deleted_at IS NULL, and the planner uses this to skip scanning
-- soft-deleted rows. Also sorts by name for the common list order.
CREATE INDEX IF NOT EXISTS idx_devices_live
    ON devices (customer_id, name)
    WHERE deleted_at IS NULL;
