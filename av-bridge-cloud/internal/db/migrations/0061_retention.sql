-- Retention (internal/retention): the periods the Service Description
-- commits to, enforced by an hourly clean-up. These indexes let it find
-- old rows without scanning; the tables are append-mostly, so block-range
-- indexes stay tiny.
--
--   audit_log        24 months
--   alerts           12 months after they're resolved (open ones are kept)
--   events           90 days
--   commands         90 days after they finish
--   nightly_run      each customer's retention_days (Room Readiness settings)
--   remote_session   90 days after they end (their audit entries stay)

CREATE INDEX IF NOT EXISTS audit_log_ts_brin ON audit_log USING brin (ts);
CREATE INDEX IF NOT EXISTS events_ts_brin ON events USING brin (ts);
CREATE INDEX IF NOT EXISTS commands_submitted_at_brin ON commands USING brin (submitted_at);
CREATE INDEX IF NOT EXISTS nightly_run_scheduled_at_brin ON nightly_run USING brin (scheduled_at);
CREATE INDEX IF NOT EXISTS alerts_resolved_at_idx ON alerts (resolved_at) WHERE status = 'resolved';

-- Room Readiness retention is offered as 30–365 days.
ALTER TABLE nightly_schedule DROP CONSTRAINT IF EXISTS nightly_schedule_retention_days_max;
ALTER TABLE nightly_schedule ADD CONSTRAINT nightly_schedule_retention_days_max CHECK (retention_days <= 365) NOT VALID;
