-- Poly Lens tagging moves to the poller: Lens rejects a freshly created tag for
-- several seconds, so a new link starts 'pending' and the poller retries until
-- the tag is accepted or the give-up window passes.
ALTER TABLE integration_devices DROP CONSTRAINT IF EXISTS integration_devices_tag_state_check;
ALTER TABLE integration_devices ADD CONSTRAINT integration_devices_tag_state_check
    CHECK (tag_state IN ('none', 'pending', 'tagged', 'failed', 'left_behind'));
ALTER TABLE integration_devices ADD COLUMN IF NOT EXISTS tag_requested_at timestamptz;
ALTER TABLE integration_devices ADD COLUMN IF NOT EXISTS tag_attempted_at timestamptz;
-- The id of the per-device Lens tag, stored when it is created so it is created once.
ALTER TABLE integration_devices ADD COLUMN IF NOT EXISTS lens_tag_id text;
