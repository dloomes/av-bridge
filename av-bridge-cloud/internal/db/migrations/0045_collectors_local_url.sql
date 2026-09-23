-- 0045_collectors_local_url.sql — LAN-reachable URL for each collector.
--
-- Populated by the operator via the portal (Collectors → edit). Used by
-- the touch-panel button so a browser on the same LAN as the bridge can
-- open the panel through the bridge's on-prem proxy instead of trying
-- to hit the panel directly (which fails when browser and panel are on
-- different subnets that the bridge routes between).
--
-- NULL means "no local URL configured" — portal falls back to the
-- direct-to-panel link. Format is a full URL including scheme + port,
-- e.g. `http://10.0.5.10:8080`.
--
-- Not validated at the DB layer beyond length — the portal enforces
-- URL shape on save and displays a helpful message when it's unset.

ALTER TABLE collectors
    ADD COLUMN local_url text;

ALTER TABLE collectors
    ADD CONSTRAINT collectors_local_url_length
        CHECK (local_url IS NULL OR length(local_url) <= 512);
