-- The power schedule and one-off dates by room tag, as 0071 did for
-- readiness checks.
--
-- Power: a tag can carry its own power-off and power-on times and days.
-- Each field resolves on its own, room first:
--
--     room_nightly_config  →  the room's tag  →  nightly_schedule
--
-- A room takes its tag settings from its highest-priority tag that has
-- any (room_tags.priority, shared with readiness checks); that tag's empty
-- fields fall through to the estate default, never to a lower tag.
--
-- Dates: a one-off date can apply to every room with a tag. On a day, a
-- room's own date wins, then its highest-priority tag with a date that day,
-- then an estate-wide date.

CREATE TABLE IF NOT EXISTS tag_power_config (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    tag_id         uuid NOT NULL UNIQUE REFERENCES room_tags(id) ON DELETE CASCADE,
    power_off_time time,
    power_on_time  time,
    days_of_week   int[],
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tag_power_config_customer_idx ON tag_power_config (customer_id);

DROP TRIGGER IF EXISTS tag_power_config_updated_at ON tag_power_config;
CREATE TRIGGER tag_power_config_updated_at
    BEFORE UPDATE ON tag_power_config
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();

ALTER TABLE tag_power_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag_power_config FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON tag_power_config TO app_tenant, app_admin;
DROP POLICY IF EXISTS tenant_isolation ON tag_power_config;
CREATE POLICY tenant_isolation ON tag_power_config
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- room_tag_power: each room's winning tag and its power settings. As
-- room_tag_readiness, read with the caller's own row security.
CREATE OR REPLACE VIEW room_tag_power WITH (security_invoker = true) AS
SELECT DISTINCT ON (a.room_id)
       a.room_id, t.id AS tag_id, t.name AS tag_name,
       c.power_off_time, c.power_on_time, c.days_of_week
  FROM room_tag_assignments a
  JOIN room_tags t        ON t.id = a.tag_id
  JOIN tag_power_config c ON c.tag_id = t.id
 ORDER BY a.room_id, t.priority NULLS LAST, lower(t.name), t.id;

GRANT SELECT ON room_tag_power TO app_tenant, app_admin;

-- ── Dates by tag ──────────────────────────────────────────────────────────
ALTER TABLE schedule_date ADD COLUMN IF NOT EXISTS tag_id uuid REFERENCES room_tags(id) ON DELETE CASCADE;

ALTER TABLE schedule_date DROP CONSTRAINT IF EXISTS schedule_date_room_or_tag;
ALTER TABLE schedule_date ADD CONSTRAINT schedule_date_room_or_tag CHECK (room_id IS NULL OR tag_id IS NULL);

-- Estate-wide now means no room and no tag; one entry per tag per date.
DROP INDEX IF EXISTS schedule_date_estate_unique;
CREATE UNIQUE INDEX IF NOT EXISTS schedule_date_estate_unique
    ON schedule_date (customer_id, on_date) WHERE room_id IS NULL AND tag_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS schedule_date_tag_unique
    ON schedule_date (tag_id, on_date) WHERE tag_id IS NOT NULL;
