-- Room tags: a customer's own list of labels ("Courtroom", "Pilot",
-- "Video link"), managed on the Room tags page and applied to rooms, any
-- number per room. Used to filter rooms, devices and Room Readiness, and
-- in the Public API.
--
-- Tag names are unique per customer ignoring case, so "Courtroom" and
-- "courtroom" can't both exist. Deleting a tag removes it from its rooms;
-- deleting a room removes its tags.

CREATE TABLE IF NOT EXISTS room_tags (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 60),
    -- A key from the portal's palette (room-tag-chip.tsx), checked by the API.
    colour      text NOT NULL DEFAULT 'slate',
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS room_tags_customer_name_key ON room_tags (customer_id, lower(name));

CREATE OR REPLACE FUNCTION room_tags_touch() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS room_tags_updated_at ON room_tags;
CREATE TRIGGER room_tags_updated_at
    BEFORE UPDATE ON room_tags
    FOR EACH ROW EXECUTE FUNCTION room_tags_touch();

CREATE TABLE IF NOT EXISTS room_tag_assignments (
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id     uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    tag_id      uuid NOT NULL REFERENCES room_tags(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, tag_id)
);
CREATE INDEX IF NOT EXISTS room_tag_assignments_tag_idx ON room_tag_assignments (tag_id);
CREATE INDEX IF NOT EXISTS room_tag_assignments_customer_idx ON room_tag_assignments (customer_id);

-- Tenant isolation, as every tenant table. Room lists aren't limited by a
-- user's physical scope (0019), so neither are their tags. The API only
-- assigns rooms and tags it can see, so an assignment can't point at
-- another customer's room even though the foreign keys don't check RLS.
ALTER TABLE room_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_tags FORCE  ROW LEVEL SECURITY;
ALTER TABLE room_tag_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_tag_assignments FORCE  ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON room_tags, room_tag_assignments TO app_tenant, app_admin;

DROP POLICY IF EXISTS tenant_isolation ON room_tags;
CREATE POLICY tenant_isolation ON room_tags
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

DROP POLICY IF EXISTS tenant_isolation ON room_tag_assignments;
CREATE POLICY tenant_isolation ON room_tag_assignments
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
