-- Tier 1 collector health fields.
--
-- version and build_time are what the bridge reports on every /ingest, so
-- when a customer reports a bug we can answer "what code are they on?"
-- without SSH. last_config_pull_at is the freshness signal for the config
-- reconciliation loop — if a bridge hasn't pulled config in a while its
-- device list may be running stale.
--
-- All three nullable — pre-Tier-1 bridges won't populate them and that's
-- fine. The portal renders "—" until values arrive.

ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS bridge_version         text,
    ADD COLUMN IF NOT EXISTS bridge_build_time      timestamptz,
    ADD COLUMN IF NOT EXISTS last_config_pull_at    timestamptz;
