-- 0023_nightly_lifecycle.sql — Room Readiness (nightly lifecycle) foundation.
--
-- Phase A slice 1: schema + permissions only. Behavioural pieces (scheduler
-- goroutine, lifecycle runner, digest emailer, retention cleaner) land in
-- subsequent slices.
--
-- Design reference: docs/nightly-lifecycle-spec.md
--
-- Five new tables:
--   nightly_schedule       one row per customer, the tenant default
--   room_nightly_config    per-room override (nullable fields inherit)
--   nightly_test_recipe    reusable functional-test definition
--   nightly_run            one row per scheduled execution
--   nightly_step_result    per-step outcome inside a run
--
-- Plus a `capabilities jsonb` column on devices so adapters can declare
-- which commands / metrics / power controls they support. The runner reads
-- this to decide whether a device can be power-cycled and which commands
-- are valid in a recipe.

-- ── Customer default schedule ──────────────────────────────────────────────

CREATE TABLE nightly_schedule (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id       uuid NOT NULL UNIQUE REFERENCES customers(id) ON DELETE CASCADE,
    power_off_time    time NOT NULL DEFAULT '19:00',
    power_on_time     time NOT NULL DEFAULT '07:30',
    -- ISO weekday numbers (1 = Mon … 7 = Sun). Default is Mon–Fri only.
    days_of_week      int[] NOT NULL DEFAULT ARRAY[1,2,3,4,5]::int[],
    timezone          text NOT NULL DEFAULT 'Europe/London',
    -- Nullable so a customer can save schedule without a recipe yet. The
    -- runner treats missing recipe as "power cycle only, no test".
    test_recipe_id    uuid,   -- FK added below (forward ref)
    -- Where lifecycle failures cc'd for helpdesk pickup. Text (not FK) so
    -- an ops address that isn't a portal user still works.
    helpdesk_email    text,
    -- 90-day default with a floor of 30 (below which run debugging becomes
    -- impossible). Cleaner (later slice) deletes step_result rows past this.
    retention_days    int NOT NULL DEFAULT 90 CHECK (retention_days >= 30),
    -- Start disabled — the customer must explicitly opt into nightly
    -- lifecycle. Prevents surprise power cycles on any existing tenant.
    enabled           bool NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- Basic sanity: off < on within same 24h window (v1 constraint;
    -- overnight ranges will need lifting once we support 24h-open rooms).
    CHECK (power_off_time <> power_on_time)
);

-- ── Reusable test recipes ──────────────────────────────────────────────────

CREATE TABLE nightly_test_recipe (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name          text NOT NULL,
    description   text,
    steps         jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX nightly_test_recipe_customer_idx ON nightly_test_recipe (customer_id);

-- Now that both tables exist, wire the FK. ON DELETE SET NULL so deleting a
-- recipe doesn't cascade into the schedule row (customer just loses their
-- test until they pick another; scheduling still works).
ALTER TABLE nightly_schedule
    ADD CONSTRAINT nightly_schedule_recipe_fk
    FOREIGN KEY (test_recipe_id)
    REFERENCES nightly_test_recipe(id)
    ON DELETE SET NULL;

-- ── Per-room override ──────────────────────────────────────────────────────

CREATE TABLE room_nightly_config (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Denormalised customer_id so RLS is a plain WHERE, not a join through
    -- rooms. Matches the pattern in devices / assets.
    customer_id              uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id                  uuid NOT NULL UNIQUE REFERENCES rooms(id) ON DELETE CASCADE,
    -- Nullable overrides — NULL means inherit the customer default. Every
    -- read path applies COALESCE(room.field, customer.field).
    power_off_time           time,
    power_on_time            time,
    days_of_week             int[],
    test_recipe_id           uuid REFERENCES nightly_test_recipe(id) ON DELETE SET NULL,
    -- Manual exclusion — room is skipped by the scheduler until this date
    -- passes, at which point the value auto-loses its effect (no cron
    -- needed; the read query checks excluded_until > CURRENT_DATE).
    excluded_until           date,
    -- Optional recipient override so a specific room's failures can route
    -- to a different team. Shape: [{"channel":"email","target":"..."}, …].
    notification_recipients  jsonb,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CHECK (
        power_off_time IS NULL
        OR power_on_time IS NULL
        OR power_off_time <> power_on_time
    )
);

CREATE INDEX room_nightly_config_customer_idx ON room_nightly_config (customer_id);

-- ── Per-execution run row ──────────────────────────────────────────────────

CREATE TABLE nightly_run (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id      uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id          uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    -- Snapshot recipe pointer. If a recipe is deleted mid-lifetime the run
    -- history stays intact; the recipe_id just goes NULL and the portal
    -- shows "(recipe deleted)".
    recipe_id        uuid REFERENCES nightly_test_recipe(id) ON DELETE SET NULL,
    phase            text NOT NULL DEFAULT 'pending'
        CHECK (phase IN (
            'pending', 'scheduled_off', 'off',
            'scheduled_on', 'waking', 'warming',
            'testing', 'ready', 'failed'
        )),
    status           text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'succeeded', 'failed', 'skipped')),
    scheduled_at     timestamptz NOT NULL,
    started_at       timestamptz,
    completed_at     timestamptz,
    failure_reason   text,
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- Portal drills into "recent runs for this room" (detail view) and "recent
-- runs across the estate" (heatmap + digest). Both are covered by this
-- combined index. Query planner picks the room_id lookup when room is
-- filtered, otherwise scans by scheduled_at.
CREATE INDEX nightly_run_customer_scheduled_idx
    ON nightly_run (customer_id, scheduled_at DESC);
CREATE INDEX nightly_run_room_scheduled_idx
    ON nightly_run (room_id, scheduled_at DESC);

-- ── Per-step result inside a run ───────────────────────────────────────────

CREATE TABLE nightly_step_result (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    run_id         uuid NOT NULL REFERENCES nightly_run(id) ON DELETE CASCADE,
    -- Some steps are room-scoped (power_on all in room) — no single device.
    device_id      uuid REFERENCES devices(id) ON DELETE SET NULL,
    step_index     int NOT NULL,
    step_name      text NOT NULL,
    step_type      text NOT NULL,
    expected       jsonb,
    actual         jsonb,
    passed         bool NOT NULL,
    error          text,
    started_at     timestamptz,
    completed_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX nightly_step_result_run_idx
    ON nightly_step_result (run_id, step_index);

-- ── updated_at triggers ────────────────────────────────────────────────────

-- Reuse a shared touch function so we don't sprinkle six near-identical
-- plpgsql blocks across the migration.
CREATE OR REPLACE FUNCTION nightly_touch_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER nightly_schedule_updated_at
    BEFORE UPDATE ON nightly_schedule
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();
CREATE TRIGGER nightly_test_recipe_updated_at
    BEFORE UPDATE ON nightly_test_recipe
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();
CREATE TRIGGER room_nightly_config_updated_at
    BEFORE UPDATE ON room_nightly_config
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();

-- ── Row Level Security ────────────────────────────────────────────────────

ALTER TABLE nightly_schedule       ENABLE ROW LEVEL SECURITY;
ALTER TABLE nightly_schedule       FORCE  ROW LEVEL SECURITY;
ALTER TABLE nightly_test_recipe    ENABLE ROW LEVEL SECURITY;
ALTER TABLE nightly_test_recipe    FORCE  ROW LEVEL SECURITY;
ALTER TABLE room_nightly_config    ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_nightly_config    FORCE  ROW LEVEL SECURITY;
ALTER TABLE nightly_run            ENABLE ROW LEVEL SECURITY;
ALTER TABLE nightly_run            FORCE  ROW LEVEL SECURITY;
ALTER TABLE nightly_step_result    ENABLE ROW LEVEL SECURITY;
ALTER TABLE nightly_step_result    FORCE  ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON nightly_schedule    TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON nightly_test_recipe TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON room_nightly_config TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON nightly_run         TO app_tenant, app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON nightly_step_result TO app_tenant, app_admin;

-- Tenant isolation on every table — the standard pattern.
CREATE POLICY tenant_isolation ON nightly_schedule
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON nightly_test_recipe
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON room_nightly_config
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON nightly_run
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

CREATE POLICY tenant_isolation ON nightly_step_result
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- Physical-scope RESTRICTIVE policies on the room-anchored tables. Same
-- shape as 0022 (assets) — scoped users see only rooms in their buildings.
--   * nightly_schedule is customer-level → no building scope needed.
--   * nightly_test_recipe is customer-level → no building scope needed.
--   * nightly_step_result is always accessed via run_id; if a user can see
--     the run, they can see its steps — building_scope on the run is enough.
CREATE POLICY building_scope_room_nightly_config ON room_nightly_config
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM rooms r
             WHERE r.id = room_nightly_config.room_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );

CREATE POLICY building_scope_nightly_run ON nightly_run
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM rooms r
             WHERE r.id = nightly_run.room_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );

-- ── Device capabilities column ─────────────────────────────────────────────

-- Adapters declare what commands / metrics / power controls each device
-- supports. Shape (all fields optional; NULL top-level = pre-declaration):
--   {
--     "power_off": {"supported": true, "method": "command"},
--     "power_on":  {"supported": true, "method": "command"},
--     "commands":  ["dial", "disconnect", "reboot"],
--     "metrics":   ["input_level_dbfs", "cpu_pct"]
--   }
-- The runner treats NULL as "cannot power-cycle; skip in lifecycle". Portal
-- recipe editor reads this to offer valid commands only.
ALTER TABLE devices
    ADD COLUMN capabilities jsonb;

-- ── Permission catalogue additions ────────────────────────────────────────

-- New permissions: nightly.view (see the pages), nightly.manage (edit
-- schedule / recipes / overrides). Seeded on system admin here + in
-- customers.go/seedSystemRoles for future customer create paths.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
  FROM roles r
 CROSS JOIN (VALUES ('nightly.view'), ('nightly.manage')) AS p(permission)
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = p.permission
   );

-- Operator + viewer get nightly.view so they can see run history without
-- editing schedules. Only admin gets nightly.manage.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'nightly.view'
  FROM roles r
 WHERE r.name IN ('operator', 'viewer') AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'nightly.view'
   );
