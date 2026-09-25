-- 0047_hierarchy_scope.sql — physical scope at every level of the hierarchy.
--
-- Before this migration a user could be restricted to buildings (0019) and
-- business units (0043), and the two ANDed together. This migration:
--
--   1. Adds region, location and room scope alongside the existing
--      building and business-unit scope columns on users.
--   2. Changes the semantics to UNION: a user sees everything under ANY
--      ticked node. Ticking "North region" + one room in a southern
--      building gives all of North plus that room. New rooms/buildings
--      added under a ticked node are covered automatically.
--   3. Scopes the hierarchy itself: a restricted user sees only the nodes
--      in their scope plus the ancestors needed to reach them (the
--      "path"), e.g. Region › Location › Building above a single ticked
--      room. Previously the whole tree was visible to everyone.
--
-- Session variables (set per transaction by Store.WithTenantScope):
--   app.business_unit_scope, app.region_scope, app.location_scope,
--   app.building_scope, app.room_scope  — the user's ticked node ids
--   app.scope_path                      — ancestor ids of ticked nodes,
--                                         resolved in Go before the scope
--                                         variables are set
-- Each is a comma-joined uuid list; empty or unset = nothing ticked at
-- that level. A caller with nothing ticked anywhere is unscoped.
--
-- Why policies only ever look UP the tree: a policy on buildings that
-- looked down at rooms ("show buildings that contain an in-scope room")
-- would recurse with the rooms policy looking up at buildings, which
-- Postgres rejects. Ancestor visibility therefore comes from
-- app.scope_path instead of from a downward join. Data tables (devices,
-- telemetry, …) are visible when their room is visible, so every scope
-- level applies to them through the rooms policy.
--
-- Rolling-deploy note: an old task still running during the rollout sets
-- only app.building_scope and app.business_unit_scope, and no path.
-- Those names are kept, so its restricted users stay restricted — in fact
-- over-restricted: without the path their building's parents are hidden,
-- the rooms policy can't join through them, and they see nothing until the
-- old task drains (a minute or two). This fails closed; full-tenant users
-- are unaffected. Covered by TestPhysicalScopeRLS.

-- ── New scope columns ────────────────────────────────────────────────────

ALTER TABLE users ADD COLUMN region_scope_ids   uuid[];
ALTER TABLE users ADD COLUMN location_scope_ids uuid[];
ALTER TABLE users ADD COLUMN room_scope_ids     uuid[];

-- ── Helpers ──────────────────────────────────────────────────────────────
--
-- Pure functions over session variables — no table access, so they can be
-- called from any policy without recursion concerns.

CREATE OR REPLACE FUNCTION app_scope_ids(var text) RETURNS text[]
    LANGUAGE sql STABLE AS $$
    SELECT CASE
             WHEN coalesce(current_setting(var, true), '') = '' THEN '{}'::text[]
             ELSE string_to_array(current_setting(var, true), ',')
           END
$$;

CREATE OR REPLACE FUNCTION app_scoped() RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT cardinality(app_scope_ids('app.business_unit_scope'))
         + cardinality(app_scope_ids('app.region_scope'))
         + cardinality(app_scope_ids('app.location_scope'))
         + cardinality(app_scope_ids('app.building_scope'))
         + cardinality(app_scope_ids('app.room_scope')) > 0
$$;

GRANT EXECUTE ON FUNCTION app_scope_ids(text) TO app_tenant, app_admin;
GRANT EXECUTE ON FUNCTION app_scoped()        TO app_tenant, app_admin;

-- ── Drop the old per-level policies ──────────────────────────────────────

DROP POLICY IF EXISTS business_unit_scope_regions   ON regions;
DROP POLICY IF EXISTS business_unit_scope_locations ON locations;
DROP POLICY IF EXISTS business_unit_scope_buildings ON buildings;
DROP POLICY IF EXISTS business_unit_scope_rooms     ON rooms;

DROP POLICY IF EXISTS building_scope_devices             ON devices;
DROP POLICY IF EXISTS building_scope_telemetry           ON telemetry;
DROP POLICY IF EXISTS building_scope_events              ON events;
DROP POLICY IF EXISTS building_scope_alerts              ON alerts;
DROP POLICY IF EXISTS building_scope_commands            ON commands;
DROP POLICY IF EXISTS building_scope_assets              ON assets;
DROP POLICY IF EXISTS building_scope_room_nightly_config ON room_nightly_config;
DROP POLICY IF EXISTS building_scope_nightly_run         ON nightly_run;

-- ── Hierarchy ────────────────────────────────────────────────────────────
--
-- A node is visible when the caller is unscoped, when it (or an ancestor)
-- is ticked, or when it is on the path to a ticked node.

CREATE POLICY physical_scope ON business_units
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR id::text = ANY(app_scope_ids('app.business_unit_scope'))
        OR id::text = ANY(app_scope_ids('app.scope_path'))
    );

CREATE POLICY physical_scope ON regions
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR id::text = ANY(app_scope_ids('app.region_scope'))
        OR id::text = ANY(app_scope_ids('app.scope_path'))
        OR (business_unit_id IS NOT NULL
            AND business_unit_id::text = ANY(app_scope_ids('app.business_unit_scope')))
    );

CREATE POLICY physical_scope ON locations
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR id::text = ANY(app_scope_ids('app.location_scope'))
        OR id::text = ANY(app_scope_ids('app.scope_path'))
        OR EXISTS (
            SELECT 1 FROM regions g
             WHERE g.id = locations.region_id
               AND (g.id::text = ANY(app_scope_ids('app.region_scope'))
                    OR (g.business_unit_id IS NOT NULL
                        AND g.business_unit_id::text = ANY(app_scope_ids('app.business_unit_scope'))))
        )
    );

CREATE POLICY physical_scope ON buildings
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR id::text = ANY(app_scope_ids('app.building_scope'))
        OR id::text = ANY(app_scope_ids('app.scope_path'))
        OR EXISTS (
            SELECT 1 FROM locations l
              JOIN regions g ON g.id = l.region_id
             WHERE l.id = buildings.location_id
               AND (l.id::text = ANY(app_scope_ids('app.location_scope'))
                    OR g.id::text = ANY(app_scope_ids('app.region_scope'))
                    OR (g.business_unit_id IS NOT NULL
                        AND g.business_unit_id::text = ANY(app_scope_ids('app.business_unit_scope'))))
        )
    );

-- Rooms are leaves: never on a path, visible only when in scope.
CREATE POLICY physical_scope ON rooms
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR id::text = ANY(app_scope_ids('app.room_scope'))
        OR EXISTS (
            SELECT 1 FROM buildings b
              JOIN locations l ON l.id = b.location_id
              JOIN regions g   ON g.id = l.region_id
             WHERE b.id = rooms.building_id
               AND (b.id::text = ANY(app_scope_ids('app.building_scope'))
                    OR l.id::text = ANY(app_scope_ids('app.location_scope'))
                    OR g.id::text = ANY(app_scope_ids('app.region_scope'))
                    OR (g.business_unit_id IS NOT NULL
                        AND g.business_unit_id::text = ANY(app_scope_ids('app.business_unit_scope'))))
        )
    );

-- ── Room-anchored data ───────────────────────────────────────────────────
--
-- Visible when the room is visible. The EXISTS sub-query on rooms is
-- itself filtered by the rooms policy above, so every scope level applies.
-- Unplaced rows (room_id IS NULL) stay hidden from restricted users, as
-- before — showing them would leak fleet size across the tenant.

CREATE POLICY physical_scope ON devices
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR (room_id IS NOT NULL AND EXISTS (SELECT 1 FROM rooms r WHERE r.id = devices.room_id))
    );

CREATE POLICY physical_scope ON assets
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR (room_id IS NOT NULL AND EXISTS (SELECT 1 FROM rooms r WHERE r.id = assets.room_id))
    );

CREATE POLICY physical_scope ON room_nightly_config
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM rooms r WHERE r.id = room_nightly_config.room_id)
    );

CREATE POLICY physical_scope ON nightly_run
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM rooms r WHERE r.id = nightly_run.room_id)
    );

-- ── Device-anchored data ─────────────────────────────────────────────────
--
-- Visible when the device is visible (devices policy above → rooms).

CREATE POLICY physical_scope ON telemetry
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = telemetry.device_id)
    );

CREATE POLICY physical_scope ON events
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = events.device_id)
    );

CREATE POLICY physical_scope ON alerts
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = alerts.device_id)
    );

CREATE POLICY physical_scope ON commands
    AS RESTRICTIVE
    USING (
        NOT app_scoped()
        OR EXISTS (SELECT 1 FROM devices d WHERE d.id = commands.device_id)
    );
