-- Where each check step's readings came from: per device judged, a fresh
-- on-demand poll or the device's last report, when that reading was taken
-- and how old it was when the step ran. Shown on the run page and in its
-- PDF, so a pass on an old reading is visible.
ALTER TABLE nightly_step_result ADD COLUMN IF NOT EXISTS readings jsonb;
