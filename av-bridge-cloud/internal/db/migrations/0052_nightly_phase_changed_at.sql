-- When a nightly run entered its current phase.
--
-- The warm-up after power-on was timed from started_at (the evening's
-- power-off), so by morning it had always elapsed and a room went straight
-- from power-on to ready. Timing each phase from when it began fixes that,
-- and bounds the wait for a phase's power commands to finish.
--
-- Commands submitted by the scheduler are found by submitted_by
-- ('nightly-schedule:<run id>'); index that lookup.

ALTER TABLE nightly_run ADD COLUMN IF NOT EXISTS phase_changed_at timestamptz;

CREATE INDEX IF NOT EXISTS commands_submitted_by_idx
    ON commands (submitted_by)
    WHERE submitted_by LIKE 'nightly-schedule:%';
