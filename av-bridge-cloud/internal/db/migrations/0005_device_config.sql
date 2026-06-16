-- Slice 4: config-pull. The cloud becomes the source of truth for per-device
-- protocol configuration; the bridge fetches its device set from /bridge/config
-- on a tick instead of reading local YAML.
--
-- Columns added are all nullable so existing inventory rows (created by the
-- ingest upsert) remain valid. Credentials are stored encrypted with the same
-- AES-GCM cipher used for collector HMAC secrets — the cloud never holds
-- plaintext device creds at rest.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS address           text,
    ADD COLUMN IF NOT EXISTS baud_rate         int,
    ADD COLUMN IF NOT EXISTS username_enc      bytea,
    ADD COLUMN IF NOT EXISTS password_enc      bytea,
    ADD COLUMN IF NOT EXISTS poll_rate_seconds int,
    ADD COLUMN IF NOT EXISTS commands          jsonb,
    ADD COLUMN IF NOT EXISTS subscriptions     jsonb;
