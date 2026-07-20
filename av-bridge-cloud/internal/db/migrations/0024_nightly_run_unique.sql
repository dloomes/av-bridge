-- 0024_nightly_run_unique.sql — dedup nightly_run rows per (room, cycle).
--
-- Slice 3 support. The scheduler computes today's scheduled_at for each
-- room deterministically from the (customer_default, room_override) pair,
-- so the same (room, scheduled_at) shouldn't appear twice. A unique index
-- lets the INSERT use ON CONFLICT DO NOTHING and skip re-creation without
-- any read-modify-write race, even if two scheduler instances end up
-- running (we don't have leader election yet).

CREATE UNIQUE INDEX nightly_run_room_cycle_uniq
    ON nightly_run (room_id, scheduled_at);
