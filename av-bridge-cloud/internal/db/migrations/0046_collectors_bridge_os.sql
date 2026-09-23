-- 0046_collectors_bridge_os.sql — record the OS/arch the bridge runs on.
--
-- Populated by the bridge on every /ingest and /bridge/config request,
-- same pattern as bridge_version / bridge_build_time. Nullable so
-- older bridges that don't report it don't wipe existing values.
--
-- Format examples:
--   "linux/amd64"                     — kernel-only fallback
--   "Ubuntu 22.04.4 LTS (linux/amd64)" — when /etc/os-release is available
--   "windows/amd64"                    — Windows collectors
--   "darwin/arm64"                     — dev laptops

ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS bridge_os text;

ALTER TABLE collectors
    ADD CONSTRAINT collectors_bridge_os_length
        CHECK (bridge_os IS NULL OR length(bridge_os) <= 128);
