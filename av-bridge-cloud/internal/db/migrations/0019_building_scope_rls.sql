-- 0019_building_scope_rls.sql — physical scope enforcement via RLS.
--
-- Makes users.building_scope_ids (populated by Slice 6 UI) load-bearing.
-- WithTenantScoped sets a comma-joined `app.building_scope` session
-- variable alongside `app.current_customer`; empty string = full-tenant
-- access, non-empty = restricted to those building_ids.
--
-- Existing tenant_isolation policies are PERMISSIVE — they OR together
-- to determine which rows a query sees. Adding a RESTRICTIVE policy
-- alongside gives us AND semantics: a row must satisfy tenant isolation
-- AND (be in-scope OR the caller is unscoped). That's exactly the
-- filter we want.
--
-- Tables covered here: devices, telemetry, events, alerts, commands.
-- Every row on these tables reaches a building via one of two chains
-- (devices.room_id → rooms.building_id, or via a device_id FK). The
-- audit_log, notification_channels and firmware_targets tables are NOT
-- scoped — they're per-tenant conceptually (audit is a paper trail,
-- notifications and firmware targets are tenant-wide policy). The
-- hierarchy tables (regions/locations/buildings/rooms) also stay full-
-- tenant so the sidebar's location tree renders sensibly even for a
-- narrowly-scoped user.
--
-- Perf note: each RESTRICTIVE policy adds an EXISTS join to every query
-- on the affected table. Under a WHERE device_id = X filter this is
-- constant-time per result row. For unfiltered scans (bulk exports)
-- it'll cost a nested loop join through devices+rooms. Acceptable for
-- MVP; a denormalised device.building_id column would speed things up
-- if needed later.

-- Devices — unplaced devices (room_id IS NULL) are visible only to
-- unscoped callers. A scoped user should only see devices in their
-- assigned buildings; letting them see "orphan" devices from unrelated
-- rooms would leak fleet size.
CREATE POLICY building_scope_devices ON devices
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR (
            room_id IS NOT NULL
            AND EXISTS (
                SELECT 1 FROM rooms r
                WHERE r.id = devices.room_id
                  AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
            )
        )
    );

-- Telemetry / events / alerts / commands filter through devices. If
-- the device isn't visible under the current scope, its rows aren't
-- either — even without a device row itself being FILTERED, the join
-- through rooms would just miss.
CREATE POLICY building_scope_telemetry ON telemetry
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM devices d
              JOIN rooms r ON r.id = d.room_id
             WHERE d.id = telemetry.device_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );

CREATE POLICY building_scope_events ON events
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM devices d
              JOIN rooms r ON r.id = d.room_id
             WHERE d.id = events.device_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );

CREATE POLICY building_scope_alerts ON alerts
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM devices d
              JOIN rooms r ON r.id = d.room_id
             WHERE d.id = alerts.device_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );

CREATE POLICY building_scope_commands ON commands
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR EXISTS (
            SELECT 1 FROM devices d
              JOIN rooms r ON r.id = d.room_id
             WHERE d.id = commands.device_id
               AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
        )
    );
