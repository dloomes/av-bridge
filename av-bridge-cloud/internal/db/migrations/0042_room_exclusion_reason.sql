-- 0042_room_exclusion_reason.sql — reason capture for per-room nightly exclusions.
--
-- Extends room_nightly_config.excluded_until (from 0023) with a reason and a
-- free-text note, so operators can record WHY a room is being excluded from
-- scheduled power / health-check events. Directly serves two functional
-- requirements common in regulated environments:
--
--   * Override a scheduled power-down for a room that remains in use
--     (reason = 'in_use'), typically an operator action for tonight only.
--   * Exclude a room from scheduled routines because of an active incident,
--     equipment awaiting replacement, or planned maintenance
--     (reason = 'active_incident' | 'awaiting_replacement' | 'planned_maintenance'),
--     typically an admin action lasting until the situation clears.
--
-- Also adds a new permission `nightly.defer` — narrower than `nightly.manage`
-- — so an operator can defer tonight's scheduled events without being granted
-- full schedule management. Full long-duration exclusion changes still gate
-- on `nightly.manage`.

-- ── Columns ────────────────────────────────────────────────────────────────

-- Reason enum. Kept as text + CHECK rather than a native enum type so we can
-- add or rename values without a DDL migration to the type.
ALTER TABLE room_nightly_config
    ADD COLUMN excluded_reason text,
    ADD COLUMN excluded_note   text;

ALTER TABLE room_nightly_config
    ADD CONSTRAINT room_nightly_config_excluded_reason_check
    CHECK (
        excluded_reason IS NULL
        OR excluded_reason IN (
            'in_use',
            'active_incident',
            'awaiting_replacement',
            'planned_maintenance',
            'other'
        )
    );

-- Data-quality guard: if a reason is set, the exclusion date must be set too.
-- We deliberately do NOT enforce the reverse — existing pre-0042 rows may
-- carry an excluded_until without a reason and stay valid.
ALTER TABLE room_nightly_config
    ADD CONSTRAINT room_nightly_config_excluded_reason_needs_date_check
    CHECK (excluded_reason IS NULL OR excluded_until IS NOT NULL);

-- ── Permission catalogue ──────────────────────────────────────────────────

-- New permission `nightly.defer` — grant to admin + operator system-default
-- roles. Idempotent so re-runs don't error. Full nightly.manage remains
-- admin-only.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'nightly.defer'
  FROM roles r
 WHERE r.name IN ('admin', 'operator')
   AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'nightly.defer'
   );
