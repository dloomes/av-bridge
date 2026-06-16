-- Slice 3.1: stale-claim recovery. The cloud's command sweeper requeues rows
-- that have been in_progress past the stale-after threshold and fails them
-- after claim_count >= max_claims attempts. ClaimPending bumps this column on
-- every claim so a flapping bridge can't trap a command in an infinite loop.

ALTER TABLE commands
    ADD COLUMN IF NOT EXISTS claim_count int NOT NULL DEFAULT 0;
