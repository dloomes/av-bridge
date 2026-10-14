-- Room Readiness splits in two:
--
--   Power schedule    nightly_schedule + room_nightly_config (unchanged):
--                     switch rooms off at night and on in the morning.
--   Readiness checks  readiness_schedule + room_readiness_config (new):
--                     run a test routine at its own time, whether or not
--                     the room is power-cycled.
--
-- A room can have either, both or neither. Shared between them: the
-- customer's timezone, helpdesk email, digest and retention (still on
-- nightly_schedule), and a room's exclusion (room_nightly_config).
--
-- nightly_run.kind says which feature a run belongs to. Power runs stop
-- after the warm-up; readiness runs go straight to the routine.
--
-- The old routine columns (nightly_schedule.test_routine_id,
-- room_nightly_config.test_routine_id) are copied across and then left
-- unused rather than dropped, so an older cloud image can still start.

-- ── Readiness schedule: the customer default ──────────────────────────────
CREATE TABLE readiness_schedule (
    customer_id      uuid PRIMARY KEY REFERENCES customers(id) ON DELETE CASCADE,
    enabled          bool NOT NULL DEFAULT false,
    test_routine_id  uuid REFERENCES nightly_test_routine(id) ON DELETE SET NULL,
    run_time         time NOT NULL DEFAULT '07:45',
    days_of_week     int[] NOT NULL DEFAULT ARRAY[1,2,3,4,5]::int[],
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- ── Per-room readiness overrides (NULL = inherit the default) ─────────────
CREATE TABLE room_readiness_config (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id      uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id          uuid NOT NULL UNIQUE REFERENCES rooms(id) ON DELETE CASCADE,
    test_routine_id  uuid REFERENCES nightly_test_routine(id) ON DELETE SET NULL,
    run_time         time,
    days_of_week     int[],
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX room_readiness_config_customer_idx ON room_readiness_config (customer_id);

CREATE TRIGGER readiness_schedule_updated_at
    BEFORE UPDATE ON readiness_schedule
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();
CREATE TRIGGER room_readiness_config_updated_at
    BEFORE UPDATE ON room_readiness_config
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();

-- Same row security as their power-schedule counterparts: tenant
-- isolation, plus physical scope on the per-room rows.
ALTER TABLE readiness_schedule    ENABLE ROW LEVEL SECURITY;
ALTER TABLE readiness_schedule    FORCE  ROW LEVEL SECURITY;
ALTER TABLE room_readiness_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_readiness_config FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON readiness_schedule    TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON room_readiness_config TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON readiness_schedule
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
CREATE POLICY tenant_isolation ON room_readiness_config
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
CREATE POLICY physical_scope ON room_readiness_config
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM rooms r WHERE r.id = room_readiness_config.room_id)
    );

-- ── Runs: which feature ───────────────────────────────────────────────────
ALTER TABLE nightly_run
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'power'
        CHECK (kind IN ('power', 'readiness'));

-- One run per room per kind per scheduled time (was per room per time).
DROP INDEX IF EXISTS nightly_run_room_cycle_uniq;
CREATE UNIQUE INDEX nightly_run_room_cycle_uniq
    ON nightly_run (room_id, kind, scheduled_at);

-- Runs started with Run now are readiness runs: a routine, no power cycle.
-- Run now stores scheduled_at and started_at from the same now(); a
-- scheduled power run never has them equal.
UPDATE nightly_run SET kind = 'readiness' WHERE scheduled_at = started_at;

-- ── Copy today's routine assignments across ───────────────────────────────
-- A customer with a routine gets a readiness check with it, at power-on
-- plus 5 minutes on the same days, switched on if Room Readiness was on.
INSERT INTO readiness_schedule (customer_id, enabled, test_routine_id, run_time, days_of_week)
SELECT customer_id,
       enabled AND test_routine_id IS NOT NULL,
       test_routine_id,
       (power_on_time + interval '5 minutes')::time,
       days_of_week
  FROM nightly_schedule
ON CONFLICT (customer_id) DO NOTHING;

-- Rooms with their own routine, or their own power-on time (so their check
-- follows their own power-on rather than the default's), get their own
-- readiness settings to match.
INSERT INTO room_readiness_config (customer_id, room_id, test_routine_id, run_time, days_of_week)
SELECT customer_id,
       room_id,
       test_routine_id,
       CASE WHEN power_on_time IS NOT NULL THEN (power_on_time + interval '5 minutes')::time END,
       days_of_week
  FROM room_nightly_config
 WHERE test_routine_id IS NOT NULL OR power_on_time IS NOT NULL
ON CONFLICT (room_id) DO NOTHING;
