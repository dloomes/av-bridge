-- Readiness checks by room tag. A tag can carry its own check (routine,
-- time, days); rooms with the tag use it unless the room has its own.
-- Each field resolves on its own, room first:
--
--     room_readiness_config  →  the room's tag  →  readiness_schedule
--
-- A room with several tags takes its settings from the highest-priority
-- one that has any: room_tags.priority, set by dragging the tags into
-- order (lowest number first; tags never ordered come last, by name).
-- That tag's empty fields fall through to the estate default; a lower tag
-- never fills them.

ALTER TABLE room_tags ADD COLUMN IF NOT EXISTS priority int;

CREATE TABLE IF NOT EXISTS tag_readiness_config (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    tag_id          uuid NOT NULL UNIQUE REFERENCES room_tags(id) ON DELETE CASCADE,
    test_routine_id uuid REFERENCES nightly_test_routine(id) ON DELETE SET NULL,
    run_time        time,
    days_of_week    int[],
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tag_readiness_config_customer_idx ON tag_readiness_config (customer_id);

DROP TRIGGER IF EXISTS tag_readiness_config_updated_at ON tag_readiness_config;
CREATE TRIGGER tag_readiness_config_updated_at
    BEFORE UPDATE ON tag_readiness_config
    FOR EACH ROW EXECUTE FUNCTION nightly_touch_updated_at();

ALTER TABLE tag_readiness_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag_readiness_config FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON tag_readiness_config TO app_tenant, app_admin;
DROP POLICY IF EXISTS tenant_isolation ON tag_readiness_config;
CREATE POLICY tenant_isolation ON tag_readiness_config
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- room_tag_readiness: each room's winning tag and its check. Read with the
-- caller's own row security (security_invoker), so a tenant sees only its
-- own; the scheduler reads it as app_admin across all customers.
CREATE OR REPLACE VIEW room_tag_readiness WITH (security_invoker = true) AS
SELECT DISTINCT ON (a.room_id)
       a.room_id, t.id AS tag_id, t.name AS tag_name,
       c.test_routine_id, c.run_time, c.days_of_week
  FROM room_tag_assignments a
  JOIN room_tags t            ON t.id = a.tag_id
  JOIN tag_readiness_config c ON c.tag_id = t.id
 ORDER BY a.room_id, t.priority NULLS LAST, lower(t.name), t.id;

GRANT SELECT ON room_tag_readiness TO app_tenant, app_admin;
