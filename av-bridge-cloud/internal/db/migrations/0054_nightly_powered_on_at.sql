-- When a nightly run's morning power-on began.
--
-- A run's duration was measured from started_at, the evening power-off, so
-- every overnight run showed 12-odd hours. Measured from power-on it says
-- how long the room took to be ready (or to fail) in the morning. Runs
-- without a power cycle (Run now) keep measuring from started_at.

ALTER TABLE nightly_run ADD COLUMN IF NOT EXISTS powered_on_at timestamptz;

-- The scheduler starts a room's run only if it has none in progress; a
-- small partial index keeps that check cheap however many nights pile up.
CREATE INDEX IF NOT EXISTS nightly_run_in_progress_room_idx
    ON nightly_run (room_id)
    WHERE status = 'in_progress';
