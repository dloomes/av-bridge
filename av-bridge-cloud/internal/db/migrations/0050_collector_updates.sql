-- Collector self-update.
--
-- The cloud image bundles a signed collector release (/downloads +
-- manifest.json). A collector is offered it on /bridge/poll when an admin
-- presses Update, or inside its maintenance window when it's behind.
--
-- Reported by the bridge on every poll:
--   bridge_platform      "linux/amd64", "windows/amd64", ...
--   update_capable       can this install update itself?
--   update_blocker       why not, in words an operator can act on
--
-- Set by operators:
--   update_window_start  minutes after local midnight the 2-hour automatic
--                        update window opens; NULL = manual updates only.
--                        Local time is the collector's building time zone
--                        (Europe/London when it has none).
--
-- Update progress (one attempt at a time):
--   update_state         idle → requested → in_progress → succeeded
--                                                       → failed / rolled_back
--   update_target_version, update_message, update_state_at
ALTER TABLE collectors
    ADD COLUMN IF NOT EXISTS bridge_platform       text,
    ADD COLUMN IF NOT EXISTS update_capable        boolean,
    ADD COLUMN IF NOT EXISTS update_blocker        text,
    ADD COLUMN IF NOT EXISTS update_window_start   smallint
        CHECK (update_window_start BETWEEN 0 AND 1439),
    ADD COLUMN IF NOT EXISTS update_state          text NOT NULL DEFAULT 'idle'
        CHECK (update_state IN ('idle','requested','in_progress','succeeded','failed','rolled_back')),
    ADD COLUMN IF NOT EXISTS update_target_version text,
    ADD COLUMN IF NOT EXISTS update_message        text,
    ADD COLUMN IF NOT EXISTS update_state_at       timestamptz;

-- The timeout sweep only looks at attempts in flight.
CREATE INDEX IF NOT EXISTS collectors_update_inflight_idx
    ON collectors (update_state_at)
    WHERE update_state IN ('requested', 'in_progress');
