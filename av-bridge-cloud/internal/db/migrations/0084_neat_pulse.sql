-- Neat Pulse: a second vendor-cloud integration (read-only). The Pulse
-- organisation id is stored in integrations.account_id and the API key in
-- integrations.secret_enc (encrypted, write-only), so no new columns.
--
-- Device types are not constrained in the database (the API's allowedTypes
-- is the only list), so the new zoom_room type needs nothing here.

ALTER TABLE integrations DROP CONSTRAINT IF EXISTS integrations_provider_check;
ALTER TABLE integrations ADD CONSTRAINT integrations_provider_check
    CHECK (provider IN ('poly_lens', 'neat_pulse'));

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_source_check;
ALTER TABLE devices ADD CONSTRAINT devices_source_check
    CHECK (source IN ('collector', 'poly_lens', 'neat_pulse'));
