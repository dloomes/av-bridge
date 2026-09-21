-- 0044_devices_power.sql — nameplate power rating on devices, for kWh reports.
--
-- Two nullable fields on devices:
--   power_watts_on       nameplate consumption while the device reports online
--   power_watts_standby  consumption while offline / soft-off (typically 0.3–8W)
--
-- Both nullable because populating the fleet takes time — the report skips
-- unrated devices rather than assuming zero. Standby defaults to NULL rather
-- than 0 so we can distinguish "operator said zero" from "not set".
--
-- Rating on the device row (not device_type / model catalogue) is the
-- pragmatic v1 shape:
--   * CSV import already exists for devices → bulk-populate is a spreadsheet.
--   * A per-model default catalogue is a straightforward v2 add on top; the
--     device-level value stays as an override.

ALTER TABLE devices
    ADD COLUMN power_watts_on      numeric(7,2),
    ADD COLUMN power_watts_standby numeric(7,2);

-- Sanity constraints: negative watts is meaningless; standby > on is likely
-- a data-entry error (should never exceed on-state consumption).
ALTER TABLE devices
    ADD CONSTRAINT devices_power_watts_on_nonneg
        CHECK (power_watts_on IS NULL OR power_watts_on >= 0),
    ADD CONSTRAINT devices_power_watts_standby_nonneg
        CHECK (power_watts_standby IS NULL OR power_watts_standby >= 0),
    ADD CONSTRAINT devices_power_standby_le_on
        CHECK (
            power_watts_standby IS NULL
            OR power_watts_on IS NULL
            OR power_watts_standby <= power_watts_on
        );
