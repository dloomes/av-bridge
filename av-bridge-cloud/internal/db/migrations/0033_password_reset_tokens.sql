-- 0033_password_reset_tokens.sql — self-serve password reset flow.
--
-- Users hit /forgot-password on the portal → server mints a random token,
-- stores its SHA-256 hash here, and emails the raw token as a link. Clicking
-- the link takes them to /reset-password?token=... where they set a new
-- password. Existing sessions are revoked on completion so a leaked session
-- can't outlive the rotation.
--
-- Tokens are single-use (used_at IS NULL gates the redeem path), short-lived
-- (1 hour by default, enforced by expires_at), and identified only by their
-- SHA-256 hash — a DB dump alone can't be redeemed. Same rest-at-hash
-- pattern as user_sessions.token_hash.
--
-- No RLS: password reset must work BEFORE the user has a session (no tenant
-- context available). Isolation is enforced by:
--   1. The redeem path scoping the UPDATE by both id + used_at IS NULL.
--   2. Token lookup being by SHA-256 hash — an attacker can't enumerate.
-- Same rationale as users / user_sessions (see 0015).

CREATE TABLE password_reset_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     text NOT NULL UNIQUE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz,
    requester_ip   text,
    user_agent     text
);

-- Fast lookup for the "any recent unused token?" cooldown check on the
-- request path. Partial index keeps it small — used tokens are archival.
CREATE INDEX password_reset_tokens_user_active_idx
    ON password_reset_tokens (user_id, created_at DESC)
 WHERE used_at IS NULL;

-- Expiry sweep helper — a housekeeping job (or a future cleaner) can prune
-- long-expired rows without full-table scans.
CREATE INDEX password_reset_tokens_expires_idx
    ON password_reset_tokens (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON password_reset_tokens TO app_admin;
